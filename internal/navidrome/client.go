package navidrome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	username   string
	password   string
	httpClient *http.Client

	// jwt is Navidrome's native-API session token. Navidrome expires it
	// after ~48h, so doNative refreshes it on 401. The mutex guards both
	// the field and concurrent re-auth attempts.
	jwtMu sync.RWMutex
	jwt   string

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

// ChangeAdminPassword changes the password for the admin user this
// client is authenticated as via Subsonic /rest/changePassword, then
// updates the client's in-memory password and re-authenticates so
// future native-API calls have a fresh JWT bound to the new
// password. Caller is responsible for persisting the new password
// to the credentials file (see SaveAdminCredentials) — this method
// only handles the in-memory + remote side of the rotation.
//
// On any failure (Subsonic rejects the change, re-auth fails) the
// in-memory password is left at whatever Navidrome accepted; the
// caller must reconcile if Navidrome returned ok but re-auth then
// failed (extremely rare, would indicate a Navidrome bug or race).
func (c *Client) ChangeAdminPassword(ctx context.Context, newPassword string) error {
	if newPassword == "" {
		return fmt.Errorf("new password is empty")
	}
	params := c.subsonicParams()
	params.Set("username", c.username)
	params.Set("password", newPassword)
	endpoint := fmt.Sprintf("%s/rest/changePassword?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("change password request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("change password returned %d", resp.StatusCode)
	}
	var envelope struct {
		SubsonicResponse struct {
			Status string `json:"status"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"subsonic-response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode change password: %w", err)
	}
	if envelope.SubsonicResponse.Status != "ok" {
		msg := "unknown error"
		if envelope.SubsonicResponse.Error != nil {
			msg = envelope.SubsonicResponse.Error.Message
		}
		return fmt.Errorf("navidrome rejected password change: %s", msg)
	}

	// Old JWT remains valid until Navidrome's session TTL, but new
	// Subsonic salt/token requests now need the new password. Update
	// in-memory and refresh the JWT in one step so the client is
	// fully on the new credentials before returning.
	c.password = newPassword
	if err := c.Authenticate(ctx); err != nil {
		return fmt.Errorf("re-authenticate with new password: %w", err)
	}
	return nil
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
	return c.username, c.password
}

// HostPath translates a Navidrome-relative path (e.g. "Bridge/Artist/Album/song.flac")
// to the corresponding host filesystem path (e.g. "./data/music/Bridge/Artist/Album/song.flac").
// The native API returns paths relative to the library root; this prepends the host music dir.
func (c *Client) HostPath(ndPath string) string {
	return filepath.Join(c.musicDir, ndPath)
}

func (c *Client) Authenticate(ctx context.Context) error {
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, c.username, c.password)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/auth/login", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth failed with status %d", resp.StatusCode)
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode auth response: %w", err)
	}
	c.setJWT(result.Token)
	return nil
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

// subsonicParams returns Subsonic API auth query parameters.
func (c *Client) subsonicParams() url.Values {
	salt := randomSalt(12)
	token := md5Sum(c.password + salt)
	return url.Values{
		"u": {c.username},
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
