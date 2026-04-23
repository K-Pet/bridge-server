package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

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

	// Replay protection: reject payloads with a timestamp older than
	// MaxWebhookAge.  The timestamp is inside the HMAC-signed body so it
	// can't be tampered with.  Missing timestamp is tolerated during the
	// rollout window (old deliver-purchase versions don't emit it).
	if purchase.Timestamp != "" {
		ts, err := time.Parse(time.RFC3339Nano, purchase.Timestamp)
		if err != nil {
			slog.Warn("webhook has unparseable timestamp", "raw", purchase.Timestamp)
			http.Error(w, "invalid timestamp", http.StatusBadRequest)
			return
		}
		if time.Since(ts) > store.MaxWebhookAge {
			slog.Warn("webhook replay rejected", "purchase", purchase.ID, "age", time.Since(ts))
			http.Error(w, "webhook too old", http.StatusUnauthorized)
			return
		}
	}

	// Idempotency guard.  The webhook is the marketplace's push entry
	// point but the marketplace also lets users retry delivery, and
	// Stripe's at-least-once retry semantics mean we may see the same
	// purchase twice on a network blip.  If every task for this purchase
	// already finished successfully AND every expected file is still on
	// disk, swallow the re-delivery — otherwise we'd re-download files
	// we already placed on disk and re-run the Navidrome scan for
	// nothing.  Partial-failure purchases still get reset so the artist
	// can actually use the retry button.
	//
	// The "files still on disk" check is what lets a redeliver actually
	// redeliver after a manual delete: the task rows may still be
	// `complete` (especially if the delete handler failed to purge them),
	// but the payload should re-enqueue anything whose file is gone.
	summaries, err := h.queue.SummariesForPurchases([]string{purchase.ID})
	if err != nil {
		slog.Warn("idempotency check failed, proceeding", "purchase", purchase.ID, "error", err)
	} else if s, ok := summaries[purchase.ID]; ok && s.Total > 0 && s.AllComplete {
		missing := missingTrackFiles(h.cfg.MusicDir, purchase.Tracks)
		if len(missing) == 0 {
			slog.Info("webhook replay for completed purchase — no-op",
				"purchase", purchase.ID, "tracks", s.Total)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "already_delivered"})
			return
		}
		slog.Info("webhook redelivery: tasks complete locally but files missing — re-enqueuing",
			"purchase", purchase.ID, "missing", len(missing), "total", len(purchase.Tracks))
	}

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

// missingTrackFiles returns the track ids whose expected output file is not
// present on disk. Used to decide whether an "already delivered" webhook is
// actually safe to short-circuit — if the artist/user deleted the file from
// the library, the task row is still `complete` but the file is gone and
// the webhook should be treated as a fresh delivery.
func missingTrackFiles(musicDir string, tracks []store.Track) []string {
	var missing []string
	for _, t := range tracks {
		path := store.ExpectedTrackPath(musicDir, t)
		_, err := os.Stat(path)
		if err == nil {
			continue
		}
		if errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, t.TrackID)
			continue
		}
		// Anything else (permission error, I/O error) — treat as missing
		// so we re-enqueue and surface a real failure during download
		// instead of silently skipping.
		slog.Warn("stat of expected track path failed", "path", path, "error", err)
		missing = append(missing, t.TrackID)
	}
	return missing
}
