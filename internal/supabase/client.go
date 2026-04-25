// Marketplace API client for bridge-server.
//
// As of Phase 2b, none of these calls use the project service-role key.
// Operations split into two auth modes:
//
//   server-context (no user JWT available — poller, downloader):
//     HMAC-signed via SignedRequester using the per-server
//     (server_id, webhook_secret) pair from /data/bridge/credentials.json.
//     Routes through marketplace Edge Functions:
//       - poll-pending-purchases   → FetchPendingPurchases
//       - mark-purchase-status     → MarkPurchaseStatus / MarkDelivered
//
//   user-context (caller has a user JWT — onboarding, /api/auto-pair):
//     Bearer-token forwarded to either:
//       - register-home-server EF  → AutoPair
//       - get-home-server EF       → GetPairStatus
//       - PostgREST + apikey       → GetUserProfile (RLS allows users
//         to read their own user_profiles row)
//
// Functions that previously took userID arguments now take userJWT —
// the relevant EFs derive userID from the JWT, so passing it
// explicitly was redundant *and* let bridge-server forge user_id on
// service-role calls. Removing it closes that hole.

package supabase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/store"
)

type Client struct {
	cfg        *config.Config
	httpClient *http.Client

	// signed handles HMAC-authenticated server-context calls. Nil when
	// the bridge-server isn't fully bootstrapped yet (e.g. dev mode
	// without credentials, or first-boot before mint completes).
	signed *SignedRequester
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		signed: NewSignedRequester(
			cfg.SupabaseURL,
			cfg.SupabaseAnonKey,
			cfg.ServerID,
			cfg.WebhookSecret,
		),
	}
}

// HomeServer represents the user's paired home-server row in
// user_home_servers — the public-safe projection (webhook_secret
// stripped) that get-home-server returns.
type HomeServer struct {
	ID         string  `json:"id"`
	UserID     string  `json:"user_id"`
	Label      string  `json:"label"`
	ServerID   *string `json:"server_id"`
	WebhookURL string  `json:"webhook_url"`
}

// =============================================================================
// Server-context calls (HMAC-authenticated)
// =============================================================================

// FetchPendingPurchases returns purchases the marketplace has routed
// to this server but hasn't yet delivered. Each track in the response
// already carries a freshly-signed download URL — equivalent to what
// deliver-purchase produces for webhook deliveries — so the caller's
// downloader path is shape-identical between webhook and poll modes.
//
// The serverID parameter is preserved for caller compat but unused;
// the EF reads server_id from the X-Bridge-Server-Id header set by
// SignedRequester, and only returns purchases routed to that server.
func (c *Client) FetchPendingPurchases(ctx context.Context, serverID string) ([]store.Purchase, error) {
	_ = serverID // signing identity already encodes which server we are
	if c.signed == nil {
		return nil, errors.New("bridge-server not bootstrapped (no signed requester)")
	}

	type response struct {
		Purchases []store.Purchase `json:"purchases"`
	}
	var resp response
	if err := c.signed.Invoke(ctx, "poll-pending-purchases", nil, &resp); err != nil {
		return nil, fmt.Errorf("poll-pending-purchases: %w", err)
	}
	return resp.Purchases, nil
}

// MarkDelivered updates a purchase status to "delivered" in Supabase.
func (c *Client) MarkDelivered(ctx context.Context, purchaseID string) error {
	return c.MarkPurchaseStatus(ctx, purchaseID, "delivered")
}

// MarkPurchaseStatus patches an arbitrary status (one of: pending,
// delivering, delivered, failed) on a purchase row. Used by the
// downloader to reconcile delivery state once tasks for a purchase
// reach a terminal state.
//
// No-ops when the bridge-server isn't bootstrapped — a fresh-install
// system can still process its own download queue locally; the
// status update just becomes a missed signal until pairing completes.
func (c *Client) MarkPurchaseStatus(ctx context.Context, purchaseID, status string) error {
	if c.signed == nil {
		return nil
	}
	if err := c.signed.Invoke(ctx, "mark-purchase-status",
		map[string]any{
			"purchase_id": purchaseID,
			"status":      status,
		}, nil); err != nil {
		return fmt.Errorf("mark-purchase-status: %w", err)
	}
	return nil
}

// =============================================================================
// User-context calls (user JWT forwarded)
// =============================================================================

// AutoPair registers this bridge-server as the user's paired home
// server. Calls the marketplace's register-home-server EF with the
// user's JWT for authorization and the server's per-install identity
// (server_id + webhook_secret from /data/bridge/credentials.json) +
// public-facing webhook URL.
//
// Returns the public-safe HomeServer projection (no webhook_secret).
// Caller is the bridge-server's /api/auto-pair handler, which
// extracts the user JWT from the inbound request and forwards it.
func (c *Client) AutoPair(ctx context.Context, userJWT string) (*HomeServer, error) {
	if c.cfg.SupabaseURL == "" {
		return nil, errors.New("supabase not configured")
	}
	if c.cfg.ExternalURL == "" {
		return nil, errors.New("BRIDGE_EXTERNAL_URL is required for auto-pair")
	}
	if c.cfg.ServerID == "" || c.cfg.WebhookSecret == "" {
		return nil, errors.New("server identity not minted yet (BRIDGE_SERVER_ID / WEBHOOK_SECRET)")
	}

	label := c.cfg.Label
	if label == "" {
		label = c.cfg.ServerID
	}
	webhookURL := trimRightSlash(c.cfg.ExternalURL) + "/api/webhook/purchase"

	body, err := json.Marshal(map[string]any{
		"label":          label,
		"webhook_url":    webhookURL,
		"server_id":      c.cfg.ServerID,
		"webhook_secret": c.cfg.WebhookSecret,
	})
	if err != nil {
		return nil, err
	}

	out, err := c.callUserEF(ctx, "register-home-server", userJWT, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ServerID   string `json:"server_id"`
		Label      string `json:"label"`
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("decode register-home-server response: %w", err)
	}
	sid := resp.ServerID
	return &HomeServer{
		Label:      resp.Label,
		ServerID:   &sid,
		WebhookURL: resp.WebhookURL,
	}, nil
}

// GetPairStatus returns the user's paired home-server row, or nil if
// they're unpaired. Calls the marketplace's get-home-server EF with
// the user's JWT — RLS on user_home_servers denies all client roles,
// so this is the only sanctioned read path.
func (c *Client) GetPairStatus(ctx context.Context, userJWT string) (*HomeServer, error) {
	if c.cfg.SupabaseURL == "" {
		return nil, errors.New("supabase not configured")
	}
	out, err := c.callUserEF(ctx, "get-home-server", userJWT, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Pairing *HomeServer `json:"pairing"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("decode get-home-server response: %w", err)
	}
	return resp.Pairing, nil
}

// GetUserProfile fetches the user's row from user_profiles. RLS allows
// users to read their own row, so we hit PostgREST directly with
// apikey + Bearer userJWT — no EF round-trip needed.
func (c *Client) GetUserProfile(ctx context.Context, userJWT string) (map[string]any, error) {
	if c.cfg.SupabaseURL == "" || c.cfg.SupabaseAnonKey == "" {
		return nil, errors.New("supabase not configured")
	}

	// `id=eq.auth.uid()` won't fly through PostgREST as a literal —
	// the simplest portable form is a no-filter SELECT and rely on
	// RLS to restrict to the caller's own row. user_profiles RLS
	// policy: users SELECT WHERE id = auth.uid().
	endpoint := c.cfg.SupabaseURL + "/rest/v1/user_profiles?select=id,username,full_name,avatar_url"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+userJWT)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("user_profiles fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("user_profiles status %d: %s", resp.StatusCode, string(body))
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode user_profiles: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// CanAutoPair reports whether this server has the configuration
// required for auto-pairing (external URL, server id, webhook secret).
// Both ServerID and WebhookSecret auto-mint on first boot now (see
// internal/config/credentials.go), so the only way this returns
// false in practice is a missing BRIDGE_EXTERNAL_URL.
func (c *Client) CanAutoPair() bool {
	return c.cfg.ExternalURL != "" && c.cfg.ServerID != "" && c.cfg.WebhookSecret != ""
}

// =============================================================================
// Helpers
// =============================================================================

// callUserEF POSTs to a marketplace Edge Function with the user's JWT
// for authorization and the project anon key as `apikey` so kong can
// route the request. The body is forwarded as-is; nil sends an empty
// JSON object.
func (c *Client) callUserEF(ctx context.Context, function, userJWT string, body []byte) ([]byte, error) {
	if userJWT == "" {
		return nil, errors.New("user JWT required for " + function)
	}
	endpoint := c.cfg.SupabaseURL + "/functions/v1/" + function

	var reader io.Reader
	if body != nil {
		reader = bytesReader(body)
	} else {
		reader = bytesReader([]byte(`{}`))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", c.cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+userJWT)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s call: %w", function, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned %d: %s", function, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func bytesReader(b []byte) io.Reader {
	// Local helper so we don't carry a bytes import alongside the
	// io interface — tiny readability win.
	return &bytesReaderImpl{b: b}
}

type bytesReaderImpl struct {
	b []byte
	i int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
