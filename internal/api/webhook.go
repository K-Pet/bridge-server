package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/store"
	"github.com/bridgemusic/bridge-server/internal/supabase"
)

type webhookHandler struct {
	cfg   *config.Config
	queue *store.Queue
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	purchase, err := supabase.VerifyAndParse(r, h.cfg.WebhookSecret)
	if err != nil {
		slog.Warn("webhook verification failed", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	for _, track := range purchase.Tracks {
		if err := h.queue.Enqueue(purchase.ID, track); err != nil {
			slog.Error("failed to enqueue track", "purchase", purchase.ID, "track", track.TrackID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	slog.Info("purchase enqueued", "purchase_id", purchase.ID, "tracks", len(purchase.Tracks))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}
