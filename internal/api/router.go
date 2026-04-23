package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
	"github.com/bridgemusic/bridge-server/internal/store"
	"github.com/bridgemusic/bridge-server/internal/supabase"
	"github.com/bridgemusic/bridge-server/web"
)

func NewRouter(cfg *config.Config, nd *navidrome.Client, queue *store.Queue, hub *EventHub, sc *supabase.Client) http.Handler {
	mux := http.NewServeMux()

	// Health check (unauthenticated)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Client config — serves Supabase URL + anon key so the frontend can
	// initialise auth at runtime without baking credentials into the JS build.
	mux.HandleFunc("GET /api/config", handleConfig(cfg))

	// Webhook endpoint (authenticated via signature, not JWT)
	mux.Handle("POST /api/webhook/purchase", &webhookHandler{cfg: cfg, queue: queue, hub: hub})

	// Pair code redemption — unauthenticated, the marketplace Edge
	// Function doesn't have a JWT for this home server yet.  Protected
	// by the one-shot random code generated on the authed side.
	mux.HandleFunc("GET /api/pair", handlePairExchange(cfg))

	// SSE endpoint — EventSource can't set custom headers, so this
	// endpoint accepts the JWT via ?token= query parameter as well as
	// the standard Authorization header. Registered outside the auth
	// middleware mux so it can handle both paths itself.
	if hub != nil {
		mux.HandleFunc("GET /api/events", handleEvents(hub, cfg))
	}

	// Authenticated Bridge API routes
	authed := http.NewServeMux()
	authed.HandleFunc("GET /api/purchases", handlePurchases(cfg, queue))
	authed.HandleFunc("POST /api/purchases/{id}/redeliver", handleRedeliver(cfg, queue))
	authed.HandleFunc("GET /api/tracks/{id}/download", handleTrackDownload(cfg))
	authed.HandleFunc("GET /api/albums/{id}/zip", handleAlbumZip(cfg))
	authed.HandleFunc("GET /api/entitlements", handleEntitlements(cfg))
	authed.HandleFunc("GET /api/settings", handleGetSettings(cfg))
	authed.HandleFunc("POST /api/pair/generate", handleGeneratePairCode())

	// Library management
	if nd != nil {
		authed.HandleFunc("DELETE /api/library/songs/{id}", handleDeleteSong(cfg, nd, queue, hub))
		authed.HandleFunc("DELETE /api/library/albums/{id}", handleDeleteAlbum(cfg, nd, queue, hub))
	}

	// Onboarding endpoints
	authed.HandleFunc("GET /api/onboarding/status", handleOnboardingStatus(sc))
	authed.HandleFunc("POST /api/auto-pair", handleAutoPair(sc))
	authed.HandleFunc("GET /api/pair/status", handlePairStatus(sc))

	mux.Handle("/api/", auth.Middleware(cfg.SupabaseJWTSecret)(authed))

	// Reverse proxy to Navidrome (with credential injection)
	if nd != nil {
		ndProxy := nd.ProxyHandler(cfg)
		mux.Handle("/rest/", ndProxy)
		mux.Handle("/nd/", http.StripPrefix("/nd", ndProxy))
	}

	// Frontend SPA — serve static files, fallback to index.html for client-side routing
	frontendDist, _ := fs.Sub(web.DistFS, "dist")
	mux.Handle("/", spaHandler(frontendDist))

	return mux
}

// spaHandler wraps a file server to serve index.html for any path that doesn't
// match a real file (supporting client-side routing).
func spaHandler(root fs.FS) http.Handler {
	fileServer := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if the requested file exists in the embedded FS.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(root, path); err != nil {
			// File doesn't exist — serve index.html for client-side routing.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
