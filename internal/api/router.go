package api

import (
	"io/fs"
	"net/http"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
	"github.com/bridgemusic/bridge-server/internal/store"
	"github.com/bridgemusic/bridge-server/web"
)

func NewRouter(cfg *config.Config, nd *navidrome.Client, queue *store.Queue, hub *EventHub) http.Handler {
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

	// Authenticated Bridge API routes
	authed := http.NewServeMux()
	if hub != nil {
		authed.HandleFunc("GET /api/events", handleEvents(hub))
	}
	authed.HandleFunc("GET /api/purchases", handlePurchases(cfg, queue))
	authed.HandleFunc("POST /api/purchases/{id}/redeliver", handleRedeliver(cfg, queue))
	authed.HandleFunc("GET /api/tracks/{id}/download", handleTrackDownload(cfg))
	authed.HandleFunc("GET /api/albums/{id}/zip", handleAlbumZip(cfg))
	authed.HandleFunc("GET /api/entitlements", handleEntitlements(cfg))
	authed.HandleFunc("GET /api/settings", handleGetSettings(cfg))
	authed.HandleFunc("POST /api/pair/generate", handleGeneratePairCode())

	var authMiddleware func(http.Handler) http.Handler
	if cfg.DevMode {
		authMiddleware = auth.DevMiddleware(cfg.SupabaseJWTSecret)
	} else {
		authMiddleware = auth.Middleware(cfg.SupabaseJWTSecret)
	}
	mux.Handle("/api/", authMiddleware(authed))

	// Reverse proxy to Navidrome (with credential injection)
	if nd != nil {
		ndProxy := nd.ProxyHandler(cfg)
		mux.Handle("/rest/", ndProxy)
		mux.Handle("/nd/", http.StripPrefix("/nd", ndProxy))
	}

	// Frontend SPA — serve static files, fallback to index.html for client-side routing
	frontendDist, _ := fs.Sub(web.DistFS, "dist")
	mux.Handle("/", spaHandler(http.FileServerFS(frontendDist)))

	return mux
}

// spaHandler wraps a file server to serve index.html for any path that doesn't
// match a real file (supporting client-side routing).
func spaHandler(fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try serving the file directly; if 404, serve index.html
		fileServer.ServeHTTP(w, r)
	})
}
