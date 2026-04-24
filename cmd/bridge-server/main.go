package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bridgemusic/bridge-server/internal/api"
	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
	"github.com/bridgemusic/bridge-server/internal/poller"
	"github.com/bridgemusic/bridge-server/internal/store"
	"github.com/bridgemusic/bridge-server/internal/supabase"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if cfg.DevMode {
		slog.Info("running in dev mode — auth and Supabase requirements disabled")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var ndClient *navidrome.Client
	var queue *store.Queue

	hub := api.NewEventHub()
	publisher := eventPublisher{hub: hub}

	// Wait for Navidrome to be ready
	slog.Info("waiting for navidrome", "url", cfg.NavidromeURL)
	if err := navidrome.WaitReady(ctx, cfg.NavidromeURL, 60*time.Second); err != nil {
		if cfg.DevMode {
			slog.Warn("navidrome not available — running without it (dev mode)", "error", err)
		} else {
			slog.Error("navidrome did not become ready", "error", err)
			os.Exit(1)
		}
	} else {
		slog.Info("navidrome is ready")

		// Bootstrap: ensure we have admin credentials for Navidrome
		ndClient, err = navidrome.Bootstrap(ctx, cfg)
		if err != nil {
			slog.Error("failed to bootstrap navidrome credentials", "error", err)
			os.Exit(1)
		}

		// Start download queue
		queue, err = store.NewQueue(cfg.DataDir)
		if err != nil {
			slog.Error("failed to initialize download queue", "error", err)
			os.Exit(1)
		}
		defer queue.Close()

		dlClient := supabase.NewClient(cfg)
		downloader := store.NewDownloader(cfg, queue, ndClient, dlClient, publisher)
		go downloader.Run(ctx)

		// Start poller if in poll mode
		if cfg.DeliveryMode == "poll" && !cfg.DevMode {
			p := poller.New(cfg, queue)
			go p.Run(ctx)
		}
	}

	// Supabase client for onboarding + pair status endpoints.
	// The downloader creates its own inside the Navidrome branch above;
	// this one is always available so the onboarding UI works even when
	// Navidrome isn't ready yet.
	sbClient := supabase.NewClient(cfg)

	// Auth verifier: round-trips JWTs through ${SUPABASE_URL}/auth/v1/user
	// with the publishable anon key, so bridge-server doesn't carry the
	// project's JWT signing secret. Returns nil when Supabase isn't
	// configured (dev-without-stack); auth.Middleware passes through in
	// that case.
	verifier := supabase.NewAuthVerifier(cfg.SupabaseURL, cfg.SupabaseAnonKey, 0)

	// Build HTTP server
	router := api.NewRouter(cfg, ndClient, queue, hub, sbClient, verifier)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
	}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("bridge server starting", "port", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

// eventPublisher adapts api.EventHub to store.EventPublisher so the downloader
// can emit live progress events without importing internal/api.
type eventPublisher struct {
	hub *api.EventHub
}

func (p eventPublisher) PublishTaskEvent(eventType, purchaseID, taskID, status string, data map[string]any) {
	p.hub.Publish(api.Event{
		Type:     eventType,
		Purchase: purchaseID,
		Task:     taskID,
		Status:   status,
		Data:     data,
	})
}
