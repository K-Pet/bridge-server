package api

import (
	"encoding/json"
	"net/http"

	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/store"
)

func handlePurchases(queue *store.Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO: query purchase history from queue DB, filtered by authenticated user
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]any{})
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
