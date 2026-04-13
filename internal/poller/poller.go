package poller

import (
	"context"
	"log/slog"
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
	// TODO: derive serverID from config or stored registration
	serverID := "TODO"
	purchases, err := p.client.FetchPendingPurchases(ctx, serverID)
	if err != nil {
		slog.Error("poll failed", "error", err)
		return
	}

	for _, purchase := range purchases {
		for _, track := range purchase.Tracks {
			if err := p.queue.Enqueue(purchase.ID, track); err != nil {
				slog.Error("failed to enqueue from poll", "purchase", purchase.ID, "error", err)
				continue
			}
		}
		if err := p.client.MarkDelivered(ctx, purchase.ID); err != nil {
			slog.Warn("failed to mark purchase delivered", "purchase", purchase.ID, "error", err)
		}
	}

	if len(purchases) > 0 {
		slog.Info("poll found purchases", "count", len(purchases))
	}
}
