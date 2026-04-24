package supabase

// AuthVerifier resolves Supabase JWTs to user IDs by round-tripping
// through ${SUPABASE_URL}/auth/v1/user — no shared HMAC secret on the
// bridge-server side. Combined with a small in-process TTL cache so
// the ~50 ms round-trip doesn't hit every authenticated request.
//
// Why this over the legacy HS256 verification path:
//   - The Supabase JWT secret is god-mode (anyone holding it can mint
//     valid tokens for any user). Distributing it in container images
//     or .env files for end-user installs is a non-starter.
//   - The anon key is publishable by design, so we can bake it into
//     the image and let bridge-server forward an `apikey` header.
//   - /auth/v1/user is the canonical Supabase path for "validate this
//     bearer token + tell me who it is" and respects session revocation
//     in real time (modulo our cache TTL).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// AuthVerifier is safe for concurrent use. Construct with NewAuthVerifier
// at boot and pass into the auth middleware + SSE handler.
type AuthVerifier struct {
	supabaseURL string
	anonKey     string
	httpClient  *http.Client
	cacheTTL    time.Duration

	mu    sync.RWMutex
	cache map[string]cachedAuth
}

type cachedAuth struct {
	userID    string
	expiresAt time.Time
}

// NewAuthVerifier returns nil if either supabaseURL or anonKey is empty
// — callers MUST tolerate this and fall through unauthenticated (the
// dev-mode path). cacheTTL of 0 picks the default (60 s).
func NewAuthVerifier(supabaseURL, anonKey string, cacheTTL time.Duration) *AuthVerifier {
	if supabaseURL == "" || anonKey == "" {
		return nil
	}
	if cacheTTL <= 0 {
		// 60 s balances per-request round-trip cost against revocation
		// latency. JWT lifetimes are 1 hour by default, so 60 s of
		// cached-after-revoke is well within standard Supabase semantics.
		cacheTTL = 60 * time.Second
	}
	return &AuthVerifier{
		supabaseURL: supabaseURL,
		anonKey:     anonKey,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		cacheTTL:    cacheTTL,
		cache:       make(map[string]cachedAuth),
	}
}

// VerifyToken returns the Supabase user id (the `sub` claim) for a
// valid bearer token, or an error for any verification failure.
//
// Successful results are cached for cacheTTL. Failures are not cached
// — a transient 5xx must not lock out a real user, and a stale
// expired-token cache entry must not silently delay the user noticing
// they need to re-auth.
func (v *AuthVerifier) VerifyToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", errors.New("empty token")
	}

	if cached, ok := v.lookup(token); ok {
		return cached, nil
	}

	userID, err := v.fetchUserID(ctx, token)
	if err != nil {
		return "", err
	}

	v.store(token, userID)
	return userID, nil
}

// Invalidate evicts a token from the cache. Use it when bridge-server
// observes an event that should drop authorization (e.g. an upstream
// 401 from Supabase). Today the cache is otherwise expiry-only.
func (v *AuthVerifier) Invalidate(token string) {
	v.mu.Lock()
	delete(v.cache, token)
	v.mu.Unlock()
}

func (v *AuthVerifier) lookup(token string) (string, bool) {
	v.mu.RLock()
	c, ok := v.cache[token]
	v.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(c.expiresAt) {
		// Drop expired entries lazily. The double-check on write is
		// fine — store() unconditionally overwrites.
		v.mu.Lock()
		// Re-check under write lock to avoid racing a concurrent store().
		if cur, still := v.cache[token]; still && time.Now().After(cur.expiresAt) {
			delete(v.cache, token)
		}
		v.mu.Unlock()
		return "", false
	}
	return c.userID, true
}

func (v *AuthVerifier) store(token, userID string) {
	v.mu.Lock()
	v.cache[token] = cachedAuth{
		userID:    userID,
		expiresAt: time.Now().Add(v.cacheTTL),
	}
	v.mu.Unlock()
}

func (v *AuthVerifier) fetchUserID(ctx context.Context, token string) (string, error) {
	endpoint := v.supabaseURL + "/auth/v1/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("apikey", v.anonKey)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth round-trip: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", errors.New("invalid token")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("auth endpoint returned %d", resp.StatusCode)
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode auth response: %w", err)
	}
	if body.ID == "" {
		return "", errors.New("auth response missing id")
	}
	return body.ID, nil
}
