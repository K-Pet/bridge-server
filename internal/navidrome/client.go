package navidrome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
)

type Client struct {
	baseURL    string
	username   string
	password   string
	jwt        string
	httpClient *http.Client
}

func NewClient(baseURL, username, password string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
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
	c.jwt = result.Token
	return nil
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
			req.Header.Set("X-ND-Authorization", "Bearer "+c.jwt)
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
