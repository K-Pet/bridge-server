package api

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
	"github.com/bridgemusic/bridge-server/internal/store"
)

//go:embed all:../../web/dist
var frontendFS embed.FS

func NewRouter(cfg *config.Config, nd *navidrome.Client, queue *store.Queue) http.Handler {
	mux := http.NewServeMux()

	// Health check (unauthenticated)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Webhook endpoint (authenticated via signature, not JWT)
	mux.Handle("POST /api/webhook/purchase", &webhookHandler{cfg: cfg, queue: queue})

	// Authenticated Bridge API routes
	authed := http.NewServeMux()
	authed.HandleFunc("GET /api/purchases", handlePurchases(queue))
	authed.HandleFunc("GET /api/settings", handleGetSettings(cfg))
	mux.Handle("/api/", auth.Middleware(cfg.SupabaseURL)(authed))

	// Reverse proxy to Navidrome (with credential injection)
	ndProxy := nd.ProxyHandler(cfg)
	mux.Handle("/rest/", ndProxy)
	mux.Handle("/nd/", http.StripPrefix("/nd", ndProxy))

	// Frontend SPA — serve static files, fallback to index.html for client-side routing
	frontendDist, _ := fs.Sub(frontendFS, "web/dist")
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
