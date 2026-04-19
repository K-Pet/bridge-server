package supabase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/store"
)

type Client struct {
	cfg        *config.Config
	httpClient *http.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// purchaseTrackRow mirrors the shape of the `purchase_tracks` view.  The
// webhook path's `store.Purchase` uses a wire shape matching what the
// marketplace's `deliver-purchase` Edge Function POSTs (camelCase track_id
// etc.); in poll mode we have to synthesise the same thing from rows of
// the view.
type purchaseTrackRow struct {
	PurchaseID     string  `json:"purchase_id"`
	UserID         string  `json:"user_id"`
	PurchaseStatus string  `json:"purchase_status"`
	ServerID       *string `json:"server_id"`
	TrackID        string  `json:"track_id"`
	Artist         string  `json:"artist"`
	Album          string  `json:"album"`
	Title          string  `json:"title"`
	Format         string  `json:"format"`
	StoragePath    string  `json:"storage_path"`
	SizeBytes      int64   `json:"size_bytes"`
	SHA256         string  `json:"sha256"`
}

// FetchPendingPurchases returns purchases targeted at this server that the
// cloud expects us to deliver.  Two states qualify:
//
//   - `awaiting_action`: manual delivery mode, user has cleared payment
//     and the marketplace is waiting for us to opt in.
//   - `delivering`: auto mode where the webhook already fired but we
//     never ACK'd (missed delivery, server was offline, etc).  Picking
//     these up on poll is the recovery path.
//
// `pending` purchases are pre-payment — Stripe hasn't cleared yet, so
// nothing to do.  The previous implementation polled on `pending` and
// silently did nothing for working deployments.
//
// We query the `purchase_tracks` view (service-role bypasses RLS) and
// fold rows by purchase_id, signing a fresh storage URL per track.
func (c *Client) FetchPendingPurchases(ctx context.Context, serverID string) ([]store.Purchase, error) {
	if serverID == "" {
		return nil, fmt.Errorf("serverID is empty")
	}
	if c.cfg.SupabaseURL == "" || c.cfg.SupabaseServiceKey == "" {
		return nil, fmt.Errorf("supabase credentials missing")
	}

	endpoint := fmt.Sprintf(
		"%s/rest/v1/purchase_tracks?server_id=eq.%s&purchase_status=in.(awaiting_action,delivering)&order=created_at",
		c.cfg.SupabaseURL, url.QueryEscape(serverID),
	)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+c.cfg.SupabaseServiceKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("supabase returned status %d: %s", resp.StatusCode, string(body))
	}

	var rows []purchaseTrackRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode purchase_tracks: %w", err)
	}

	byPurchase := map[string]*store.Purchase{}
	order := []string{}
	for _, r := range rows {
		if r.StoragePath == "" {
			continue
		}
		signed, err := c.signStorageURL(ctx, r.StoragePath)
		if err != nil {
			return nil, fmt.Errorf("sign %s: %w", r.StoragePath, err)
		}
		track := store.Track{
			TrackID:     r.TrackID,
			Artist:      r.Artist,
			Album:       r.Album,
			Title:       r.Title,
			Format:      r.Format,
			DownloadURL: signed,
			SizeBytes:   r.SizeBytes,
			SHA256:      r.SHA256,
		}
		if p, ok := byPurchase[r.PurchaseID]; ok {
			p.Tracks = append(p.Tracks, track)
			continue
		}
		byPurchase[r.PurchaseID] = &store.Purchase{
			ID:     r.PurchaseID,
			UserID: r.UserID,
			Tracks: []store.Track{track},
		}
		order = append(order, r.PurchaseID)
	}

	out := make([]store.Purchase, 0, len(order))
	for _, id := range order {
		out = append(out, *byPurchase[id])
	}
	return out, nil
}

// signStorageURL mints a 1h signed URL for a `tracks` bucket object,
// matching what the marketplace's `deliver-purchase` Edge Function would
// have produced for webhook-mode delivery.
func (c *Client) signStorageURL(ctx context.Context, storagePath string) (string, error) {
	body := []byte(`{"expiresIn":3600}`)
	endpoint := fmt.Sprintf(
		"%s/storage/v1/object/sign/tracks/%s",
		c.cfg.SupabaseURL, escapePathSegments(storagePath),
	)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("apikey", c.cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+c.cfg.SupabaseServiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("sign status %d: %s", resp.StatusCode, string(b))
	}

	var signed struct {
		SignedURL string `json:"signedURL"`
		URL       string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&signed); err != nil {
		return "", err
	}
	path := signed.SignedURL
	if path == "" {
		path = signed.URL
	}
	if path == "" {
		return "", fmt.Errorf("empty signed URL")
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.cfg.SupabaseURL + "/storage/v1" + path, nil
}

// escapePathSegments URL-encodes each "/"-separated segment of a storage
// path while preserving the slashes themselves. Supabase Storage signs the
// literal path it receives; escaping the full path would turn "/" into
// "%2F" and cause InvalidSignature on fetch.
func escapePathSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

// MarkDelivered updates a purchase status to "delivered" in Supabase.
func (c *Client) MarkDelivered(ctx context.Context, purchaseID string) error {
	return c.MarkPurchaseStatus(ctx, purchaseID, "delivered")
}

// MarkPurchaseStatus patches an arbitrary status (one of: pending, delivering,
// delivered, failed) on a purchase row. Used by the downloader to reconcile
// delivery state once all tasks for a purchase reach a terminal state.
func (c *Client) MarkPurchaseStatus(ctx context.Context, purchaseID, status string) error {
	if c.cfg.SupabaseURL == "" || c.cfg.SupabaseServiceKey == "" {
		return nil
	}
	endpoint := fmt.Sprintf("%s/rest/v1/purchases?id=eq.%s", c.cfg.SupabaseURL, purchaseID)

	body := strings.NewReader(fmt.Sprintf(`{"status":%q}`, status))
	req, err := http.NewRequestWithContext(ctx, "PATCH", endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+c.cfg.SupabaseServiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("supabase PATCH failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("supabase returned status %d", resp.StatusCode)
	}
	return nil
}
