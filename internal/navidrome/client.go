package navidrome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
)

type Client struct {
	baseURL    string
	httpClient *http.Client

	// credsMu guards the mutable credentials triple
	// (username, password, userID). Reads happen on every Subsonic
	// proxy call (subsonicParams) and every Authenticate; writes
	// happen during ChangeAdminPassword's final commit. Treat the
	// three fields as a unit — they describe one identity.
	credsMu  sync.RWMutex
	username string
	password string
	userID   string // Navidrome internal id, captured during /auth/login

	// jwt is Navidrome's native-API session token. Navidrome expires it
	// after ~48h, so doNative refreshes it on 401. The mutex guards both
	// the field and concurrent re-auth attempts.
	jwtMu sync.RWMutex
	jwt   string

	// rotateMu serializes password-rotation requests so two concurrent
	// rotates can't race each other into a torn state. Held for the
	// entire ChangeAdminPassword call.
	rotateMu sync.Mutex

	// musicDir is the host-side music directory (e.g. "./data/music").
	// Used to translate Navidrome-relative paths to host filesystem paths.
	musicDir string
}

func NewClient(baseURL, username, password string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// snapshotCreds returns the current credentials triple atomically.
// All read paths (subsonicParams, Authenticate, Credentials) MUST go
// through this — direct field reads are racy because ChangeAdminPassword
// mutates them at the end of a successful rotation.
func (c *Client) snapshotCreds() (username, password, userID string) {
	c.credsMu.RLock()
	defer c.credsMu.RUnlock()
	return c.username, c.password, c.userID
}

// commitCreds atomically updates the credentials triple. Used by
// Authenticate (to record the userID it just learned) and by
// ChangeAdminPassword's final commit step.
func (c *Client) commitCreds(username, password, userID string) {
	c.credsMu.Lock()
	defer c.credsMu.Unlock()
	c.username = username
	c.password = password
	c.userID = userID
}

// ChangeAdminPassword rotates the admin password atomically across
// Navidrome and the caller-provided persistence sink. The flow is
// strictly transactional:
//
//  1. Snapshot the current (old) credentials.
//  2. GET the user record via Navidrome's native API. Required because
//     PUT /api/user/{id} is a full-record overwrite (deluan/rest
//     framework) — partial bodies wipe userName/isAdmin/etc.
//  3. PUT the record back with the new password merged in.
//  4. Verify by authenticating with the new password. This proves
//     Navidrome accepted the change; if it didn't (PUT returned 200
//     but password unchanged), we roll back before mutating anything.
//  5. Run the persist callback (caller writes to disk). Disk-after-
//     Navidrome-confirmed-and-verified means a disk failure leaves a
//     known recoverable state.
//  6. If persist succeeds, commit the new credentials to memory and
//     install the freshly-minted JWT. Only here does in-memory state
//     diverge from the disk and Navidrome state.
//
// On any failure, the function attempts to roll Navidrome back to the
// old password — using the appropriate JWT for the moment. If rollback
// also fails, the error string makes that explicit so an operator
// knows manual intervention is needed.
//
// Navidrome explicitly does NOT implement the Subsonic
// /rest/changePassword endpoint (it returns 501), which is why all of
// this goes through the native API.
func (c *Client) ChangeAdminPassword(ctx context.Context, newPassword string, persist func(string) error) error {
	if newPassword == "" {
		return fmt.Errorf("new password is empty")
	}

	c.rotateMu.Lock()
	defer c.rotateMu.Unlock()

	username, oldPassword, userID := c.snapshotCreds()
	if userID == "" {
		// Force an Authenticate to populate userID. Without this the
		// rotation would 404 against /api/user/.
		if err := c.Authenticate(ctx); err != nil {
			return fmt.Errorf("authenticate before password change: %w", err)
		}
		_, _, userID = c.snapshotCreds()
		if userID == "" {
			return fmt.Errorf("authenticate did not return user id")
		}
	}

	oldJWT := c.currentJWT()

	// 1. Fetch the user record so we can PUT it back intact.
	record, err := c.fetchUserRecord(ctx, oldJWT, userID)
	if err != nil {
		return fmt.Errorf("get user for password change: %w", err)
	}

	// 2. Build the new-password body and PUT.
	if err := c.putUserRecord(ctx, oldJWT, userID, mergePassword(record, newPassword, oldPassword)); err != nil {
		return fmt.Errorf("apply new password to navidrome: %w", err)
	}

	// 3. Verify Navidrome actually accepted the change by authenticating
	//    with the new password. This is the canary — if it fails, the
	//    PUT may have been a silent no-op or the user record might be
	//    in a weird state. Roll back before mutating in-memory state.
	newJWT, newUserID, err := c.authenticateRaw(ctx, username, newPassword)
	if err != nil {
		// Rollback uses oldJWT — Navidrome doesn't invalidate sessions
		// on password change, so the old token is still valid even
		// though the credential it was minted from is no longer the
		// current one in the DB. If oldJWT has expired between then
		// and now, try a fresh login with the old password.
		rbErr := c.putUserRecord(ctx, oldJWT, userID, mergePassword(record, oldPassword, newPassword))
		if rbErr != nil {
			if freshJWT, _, freshErr := c.authenticateRaw(ctx, username, oldPassword); freshErr == nil {
				rbErr = c.putUserRecord(ctx, freshJWT, userID, mergePassword(record, oldPassword, newPassword))
			}
		}
		if rbErr != nil {
			return fmt.Errorf("verify failed (%v); rollback also failed (%v) — admin user may be in inconsistent state, manual recovery required", err, rbErr)
		}
		return fmt.Errorf("verify new password: %w", err)
	}

	// 4. Persist to disk. If this fails, roll back Navidrome to old —
	//    we use newJWT because Navidrome currently expects new
	//    credentials.
	if err := persist(newPassword); err != nil {
		rbErr := c.putUserRecord(ctx, newJWT, newUserID, mergePassword(record, oldPassword, newPassword))
		if rbErr != nil {
			return fmt.Errorf("persist to disk failed (%v); navidrome rollback also failed (%v) — disk has old password, navidrome has new password, server will not start after restart, manual recovery required", err, rbErr)
		}
		return fmt.Errorf("persist to disk: %w", err)
	}

	// 5. Commit: in-memory now matches Navidrome and disk.
	c.commitCreds(username, newPassword, newUserID)
	c.setJWT(newJWT)
	return nil
}

// fetchUserRecord GETs the Navidrome user record at /api/user/{id}
// using the given JWT explicitly (rather than the client's current
// JWT) so the rotation flow can be precise about which session it's
// operating under.
func (c *Client) fetchUserRecord(ctx context.Context, jwt, userID string) (map[string]any, error) {
	endpoint := fmt.Sprintf("%s/api/user/%s", c.baseURL, userID)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ND-Authorization", "Bearer "+jwt)
	req.Header.Set("X-ND-Client-Unique-Id", "bridge-server")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("get user returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var record map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, fmt.Errorf("decode user record: %w", err)
	}
	return record, nil
}

// putUserRecord PUTs the given record back to /api/user/{id} using
// the given JWT. The record must be a complete user object (we GET
// before PUT to satisfy this).
func (c *Client) putUserRecord(ctx context.Context, jwt, userID string, record map[string]any) error {
	endpoint := fmt.Sprintf("%s/api/user/%s", c.baseURL, userID)
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal user record: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ND-Authorization", "Bearer "+jwt)
	req.Header.Set("X-ND-Client-Unique-Id", "bridge-server")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("put user returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// mergePassword returns a shallow copy of the user record with the
// password fields set. We don't mutate the input because the rollback
// path reuses the same `record` snapshot with the old password — so
// the record acts as the immutable "shape" of the user, and only the
// password fields vary between forward and rollback writes.
func mergePassword(record map[string]any, newPassword, currentPassword string) map[string]any {
	out := make(map[string]any, len(record)+3)
	for k, v := range record {
		out[k] = v
	}
	out["password"] = newPassword
	out["currentPassword"] = currentPassword
	// Some Navidrome versions gate the password-change branch on this
	// boolean. Harmless when unused; required when used.
	out["changePassword"] = true
	return out
}

// Credentials returns the admin username/password pair this client
// is authenticated with. Intended for the self-host admin UI's
// "reveal Navidrome credentials" surface, where the owner needs to
// log into Navidrome directly to perform operations the bridge UI
// doesn't expose (full library scans, missing-files cleanup, etc.).
//
// Callers MUST gate access on a fresh re-auth check — Navidrome's
// admin user can mutate the entire library, so handing the password
// back over an already-authenticated session would let a stale
// cookie suffice for the most dangerous capability on the server.
func (c *Client) Credentials() (username, password string) {
	u, p, _ := c.snapshotCreds()
	return u, p
}

// HostPath translates a Navidrome-relative path (e.g. "Bridge/Artist/Album/song.flac")
// to the corresponding host filesystem path (e.g. "./data/music/Bridge/Artist/Album/song.flac").
// The native API returns paths relative to the library root; this prepends the host music dir.
func (c *Client) HostPath(ndPath string) string {
	return filepath.Join(c.musicDir, ndPath)
}

// Authenticate refreshes the client's JWT using the currently-stored
// credentials, updating jwt and userID in place. Concurrent callers
// are serialized by jwtMu's write lock for the setJWT call; the
// underlying HTTP request itself can interleave (idempotent) and the
// last writer wins — which is fine because all concurrent refreshes
// are using the same credentials.
func (c *Client) Authenticate(ctx context.Context) error {
	username, password, _ := c.snapshotCreds()
	jwt, userID, err := c.authenticateRaw(ctx, username, password)
	if err != nil {
		return err
	}
	c.setJWT(jwt)
	c.commitCreds(username, password, userID)
	return nil
}

// authenticateRaw is the credential-explicit form of Authenticate. It
// hits /auth/login with the given creds and returns (jwt, userID)
// without touching client state. Used by ChangeAdminPassword to verify
// a new password before committing.
func (c *Client) authenticateRaw(ctx context.Context, username, password string) (jwt, userID string, err error) {
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/auth/login", strings.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("auth failed with status %d", resp.StatusCode)
	}

	var result struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("failed to decode auth response: %w", err)
	}
	return result.Token, result.ID, nil
}

func (c *Client) currentJWT() string {
	c.jwtMu.RLock()
	defer c.jwtMu.RUnlock()
	return c.jwt
}

func (c *Client) setJWT(token string) {
	c.jwtMu.Lock()
	defer c.jwtMu.Unlock()
	c.jwt = token
}

// doNative executes a Navidrome native-API request, automatically
// re-authenticating and retrying once on 401. The build func is invoked
// twice on retry, so it must not consume a non-replayable body.
func (c *Client) doNative(ctx context.Context, build func(context.Context) (*http.Request, error)) (*http.Response, error) {
	resp, err := c.sendNative(ctx, build)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	// JWT expired (Navidrome's session timeout, ~48h by default).
	// Drop the response, re-auth, and try once more.
	resp.Body.Close()
	if err := c.Authenticate(ctx); err != nil {
		return nil, fmt.Errorf("re-authenticate after 401: %w", err)
	}
	slog.Info("navidrome jwt refreshed after 401")
	return c.sendNative(ctx, build)
}

func (c *Client) sendNative(ctx context.Context, build func(context.Context) (*http.Request, error)) (*http.Response, error) {
	req, err := build(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ND-Authorization", "Bearer "+c.currentJWT())
	req.Header.Set("X-ND-Client-Unique-Id", "bridge-server")
	return c.httpClient.Do(req)
}

// subsonicParams returns Subsonic API auth query parameters. Reads
// credentials atomically under credsMu so a concurrent rotation
// commit can't tear the username/password pair.
func (c *Client) subsonicParams() url.Values {
	username, password, _ := c.snapshotCreds()
	salt := randomSalt(12)
	token := md5Sum(password + salt)
	return url.Values{
		"u": {username},
		"t": {token},
		"s": {salt},
		"v": {"1.16.1"},
		"c": {"bridge-server"},
		"f": {"json"},
	}
}

// ProxyHandler returns an http.Handler that reverse-proxies to Navidrome,
// injecting authentication credentials into every request.
func (c *Client) ProxyHandler(cfg *config.Config) http.Handler {
	target, _ := url.Parse(cfg.NavidromeURL)
	proxy := httputil.NewSingleHostReverseProxy(target)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		if strings.HasPrefix(req.URL.Path, "/rest/") {
			// Subsonic API: inject token auth params
			q := req.URL.Query()
			for k, v := range c.subsonicParams() {
				q[k] = v
			}
			req.URL.RawQuery = q.Encode()
		} else {
			// Native API: inject JWT header
			req.Header.Set("X-ND-Authorization", "Bearer "+c.currentJWT())
		}
	}

	return proxy
}

func randomSalt(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

func md5Sum(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}
