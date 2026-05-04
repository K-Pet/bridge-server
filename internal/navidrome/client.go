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
