package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port         int
	DataDir      string
	MusicDir     string
	NavidromeURL string

	SupabaseURL     string
	SupabaseAnonKey string
	// Deprecated: bridge-server stopped using service-role in Phase 2b
	// (privileged ops moved to Supabase Edge Functions authenticated by
	// the auto-minted webhook_secret or the user's forwarded JWT).
	// Field stays for one release so out-of-tree forks have a cycle to
	// migrate; nothing in this repo reads it as of the cutover.
	SupabaseServiceKey string
	// Deprecated: replaced in Phase 2a by AuthVerifier round-tripping
	// through ${SUPABASE_URL}/auth/v1/user. Same migration window.
	SupabaseJWTSecret string

	// WebhookSecret authenticates marketplace → bridge-server webhook
	// deliveries. Auto-minted at first boot in production (persisted to
	// ${BRIDGE_DATA}/credentials.json with 0600); env-supplied values
	// still win for advanced/dev workflows.
	WebhookSecret string

	DeliveryMode string
	PollInterval time.Duration

	// ServerID identifies this home server to Supabase in poll mode — the
	// marketplace writes purchases with `server_id = <this>` and the poller
	// picks them up. In webhook mode Supabase uses this to route the HTTP
	// callback. Auto-minted at first boot in production and persisted in
	// the same credentials.json as WebhookSecret.
	ServerID string

	MasterSecret string

	// DevMode disables Supabase auth requirements for local development.
	// Set BRIDGE_DEV=true to enable.
	DevMode bool

	// MarketplaceURL is the origin where the Bridge Music Marketplace (Expo
	// web bundle) is served. The embedded SPA iframes this URL for the
	// storefront tab. In prod we mount the exported Expo web bundle at
	// /marketplace/ on this same server; in dev it typically points at
	// http://localhost:8081 (the expo metro web dev server).
	MarketplaceURL string

	// ExternalURL is the publicly-reachable URL of this bridge-server
	// instance (e.g. "https://music.example.com"). Used by the auto-pair
	// onboarding flow to construct the webhook_url written into the
	// marketplace's user_home_servers table. Not required if auto-pair
	// isn't used (manual pair codes still work without it).
	ExternalURL string

	// Label is a human-friendly name for this home server shown in the
	// marketplace UI (e.g. "Living Room Server"). Falls back to ServerID.
	Label string

	// DevEmail and DevPassword are test credentials the frontend can
	// auto-sign-in with in dev mode. Only served via /api/config when
	// DevMode is true. Defaults match the marketplace seed script.
	DevEmail    string
	DevPassword string
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:         envInt("BRIDGE_PORT", 8888),
		DataDir:      envStr("BRIDGE_DATA", "/data/bridge"),
		MusicDir:     envStr("BRIDGE_MUSIC_DIR", "/data/music"),
		NavidromeURL: envStr("BRIDGE_ND_URL", "http://127.0.0.1:4533"),
		DeliveryMode: envStr("BRIDGE_DELIVERY_MODE", "poll"),
		PollInterval: envDuration("BRIDGE_POLL_INTERVAL", 5*time.Minute),
		ServerID:     envStr("BRIDGE_SERVER_ID", ""),
		MasterSecret: envStr("BRIDGE_SECRET", ""),
		DevMode:      envStr("BRIDGE_DEV", "") == "true",

		SupabaseURL:        envStr("BRIDGE_SUPABASE_URL", ""),
		SupabaseAnonKey:    envStr("BRIDGE_SUPABASE_ANON_KEY", ""),
		SupabaseServiceKey: envStr("BRIDGE_SUPABASE_SERVICE_KEY", ""),
		SupabaseJWTSecret:  envStr("BRIDGE_SUPABASE_JWT_SECRET", ""),
		WebhookSecret:      envStr("BRIDGE_WEBHOOK_SECRET", ""),
		MarketplaceURL:     envStr("BRIDGE_MARKETPLACE_URL", "/marketplace/"),
		ExternalURL:        envStr("BRIDGE_EXTERNAL_URL", ""),
		Label:              envStr("BRIDGE_LABEL", ""),
		DevEmail:           envStr("BRIDGE_DEV_EMAIL", "test@bridge.music"),
		DevPassword:        envStr("BRIDGE_DEV_PASSWORD", "testpass123"),
	}

	// Auto-mint per-server identity if not handed in via env. Stays
	// gated on !DevMode so `tilt up` keeps the stable `local-dev`
	// ServerID and doesn't drop a credentials.json into the dev data
	// dir (where it would survive `rm -rf data/` resets and confuse
	// the marketplace's user_home_servers row by user_id).
	if !cfg.DevMode {
		if err := loadOrMintCredentials(cfg); err != nil {
			return nil, fmt.Errorf("credentials: %w", err)
		}
		if cfg.SupabaseURL == "" {
			return nil, fmt.Errorf("BRIDGE_SUPABASE_URL is required")
		}
	}
	if cfg.DevMode && cfg.ServerID == "" {
		cfg.ServerID = "local-dev"
	}

	return cfg, nil
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
