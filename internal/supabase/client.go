package supabase

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// FetchPendingPurchases retrieves undelivered purchases for this server from Supabase.
// Used by the poll-mode fallback for servers behind NAT.
func (c *Client) FetchPendingPurchases(ctx context.Context, serverID string) ([]store.Purchase, error) {
	url := fmt.Sprintf("%s/rest/v1/purchases?server_id=eq.%s&status=eq.pending&order=created_at",
		c.cfg.SupabaseURL, serverID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
		return nil, fmt.Errorf("supabase returned status %d", resp.StatusCode)
	}

	var purchases []store.Purchase
	if err := json.NewDecoder(resp.Body).Decode(&purchases); err != nil {
		return nil, err
	}
	return purchases, nil
}

// MarkDelivered updates a purchase status to "delivered" in Supabase.
func (c *Client) MarkDelivered(ctx context.Context, purchaseID string) error {
	url := fmt.Sprintf("%s/rest/v1/purchases?id=eq.%s", c.cfg.SupabaseURL, purchaseID)

	body := strings.NewReader(`{"status":"delivered"}`)
	req, err := http.NewRequestWithContext(ctx, "PATCH", url, body)
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
