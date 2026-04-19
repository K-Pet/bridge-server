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
	hub   *EventHub
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	purchase, err := supabase.VerifyAndParse(r, h.cfg.WebhookSecret)
	if err != nil {
		slog.Warn("webhook verification failed", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Webhook is the idempotent entry point for this purchase — users can
	// retry delivery from the marketplace UI and re-fire this exact payload.
	// The queue's task primary key is "<purchase_id>:<track_id>", so a
	// second delivery would hit a UNIQUE violation on every track insert
	// and surface as a 500 back to the caller.  Clear previous attempts
	// first so retries start from a clean queued state.
	if err := h.queue.DeleteTasksForPurchase(purchase.ID); err != nil {
		slog.Error("failed to reset prior tasks", "purchase", purchase.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
	if h.hub != nil {
		h.hub.Publish(Event{
			Type:     "purchase_enqueued",
			Purchase: purchase.ID,
			Data:     map[string]any{"tracks": len(purchase.Tracks)},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}
