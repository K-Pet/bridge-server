package poller

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/store"
	"github.com/bridgemusic/bridge-server/internal/supabase"
)

type Poller struct {
	cfg    *config.Config
	queue  *store.Queue
	client *supabase.Client
}

func New(cfg *config.Config, queue *store.Queue) *Poller {
	return &Poller{
		cfg:    cfg,
		queue:  queue,
		client: supabase.NewClient(cfg),
	}
}

// Run polls Supabase for pending purchases at the configured interval.
func (p *Poller) Run(ctx context.Context) {
	slog.Info("poller started", "interval", p.cfg.PollInterval)
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	// Poll immediately on start, then on each tick
	p.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	if p.cfg.ServerID == "" {
		slog.Warn("poll skipped: BRIDGE_SERVER_ID is empty")
		return
	}
	purchases, err := p.client.FetchPendingPurchases(ctx, p.cfg.ServerID)
	if err != nil {
		slog.Error("poll failed", "error", err)
		return
	}

	for _, purchase := range purchases {
		// Idempotency: if the webhook already delivered this purchase (all
		// tasks completed locally) AND every expected file is still on
		// disk, skip it — avoids overwriting a terminal "delivered"
		// status back to "delivering". If files were deleted from the
		// library, fall through and re-enqueue so the marketplace's
		// "redeliver" actually redelivers.
		summaries, err := p.queue.SummariesForPurchases([]string{purchase.ID})
		if err == nil {
			if s, ok := summaries[purchase.ID]; ok && s.Total > 0 && s.AllComplete {
				if !anyTrackFileMissing(p.cfg.MusicDir, purchase.Tracks) {
					slog.Info("poll: purchase already complete locally, skipping",
						"purchase", purchase.ID, "tracks", s.Total)
					continue
				}
				slog.Info("poll: purchase complete locally but files missing — re-enqueuing",
					"purchase", purchase.ID, "tracks", s.Total)
				if err := p.queue.DeleteTasksForPurchase(purchase.ID); err != nil {
					slog.Warn("poll: failed to clear stale tasks", "purchase", purchase.ID, "error", err)
				}
			}
		}

		for _, track := range purchase.Tracks {
			if err := p.queue.Enqueue(purchase.ID, track); err != nil {
				slog.Error("failed to enqueue from poll", "purchase", purchase.ID, "error", err)
				continue
			}
		}
		// Flip status to "delivering" so the UI reflects that work is in flight.
		// The downloader will move it to "delivered" (or "failed") once every task
		// reaches a terminal state — see Downloader.reconcilePurchase.
		if err := p.client.MarkPurchaseStatus(ctx, purchase.ID, "delivering"); err != nil {
			slog.Warn("failed to mark purchase delivering", "purchase", purchase.ID, "error", err)
		}
	}

	if len(purchases) > 0 {
		slog.Info("poll found purchases", "count", len(purchases))
	}
}

// anyTrackFileMissing mirrors the webhook idempotency guard's filesystem
// check — if any expected on-disk file is gone, the local "all complete"
// state is stale and the poller should re-enqueue.
func anyTrackFileMissing(musicDir string, tracks []store.Track) bool {
	for _, t := range tracks {
		path := store.ExpectedTrackPath(musicDir, t)
		_, err := os.Stat(path)
		if err == nil {
			continue
		}
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		// Non-existence errors we can't reason about — treat as missing
		// so we attempt re-download rather than silently stalling.
		slog.Warn("poll: stat of expected track path failed", "path", path, "error", err)
		return true
	}
	return false
}
