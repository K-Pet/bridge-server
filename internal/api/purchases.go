package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/config"
)

type purchaseResponse struct {
	ID         string    `json:"id"`
	TotalCents int       `json:"total_cents"`
	Status     string    `json:"status"`
	PaymentRef string    `json:"payment_ref,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Items      []purchaseItemResponse `json:"items"`
}

type purchaseItemResponse struct {
	ID        string `json:"id"`
	TrackID   string `json:"track_id,omitempty"`
	AlbumID   string `json:"album_id,omitempty"`
	PriceCents int   `json:"price_cents"`
}

func handlePurchases(cfg *config.Config) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if cfg.SupabaseURL == "" || cfg.SupabaseServiceKey == "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]any{})
			return
		}

		// Query purchases with their items from Supabase
		url := fmt.Sprintf(
			"%s/rest/v1/purchases?user_id=eq.%s&select=id,total_cents,status,payment_ref,created_at,purchase_items(id,track_id,album_id,price_cents)&order=created_at.desc",
			cfg.SupabaseURL, userID,
		)

		req, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
		if err != nil {
			slog.Error("failed to build purchase query", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		req.Header.Set("apikey", cfg.SupabaseServiceKey)
		req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

		resp, err := client.Do(req)
		if err != nil {
			slog.Error("failed to query purchases", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			slog.Error("supabase purchase query failed", "status", resp.StatusCode, "body", string(body))
			json.NewEncoder(w).Encode([]any{})
			return
		}

		// Forward the Supabase response directly — it already has the shape we want
		io.Copy(w, resp.Body)
	}
}

func handleConfig(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"supabase_url":      cfg.SupabaseURL,
			"supabase_anon_key": cfg.SupabaseAnonKey,
			"dev_mode":          cfg.DevMode,
		})
	}
}

func handleGetSettings(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"delivery_mode": cfg.DeliveryMode,
			"poll_interval": cfg.PollInterval.String(),
		})
	}
}
