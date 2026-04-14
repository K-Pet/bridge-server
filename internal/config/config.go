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

	SupabaseURL        string
	SupabaseAnonKey    string
	SupabaseServiceKey string
	SupabaseJWTSecret  string
	WebhookSecret      string

	DeliveryMode string
	PollInterval time.Duration

	// ServerID identifies this home server to Supabase in poll mode — the
	// marketplace writes purchases with `server_id = <this>` and the poller
	// picks them up. In webhook mode Supabase uses this to route the HTTP
	// callback. Must be unique per user-installed home server.
	ServerID string

	MasterSecret string

	// DevMode disables Supabase auth requirements for local development.
	// Set BRIDGE_DEV=true to enable.
	DevMode bool
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:         envInt("BRIDGE_PORT", 8080),
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
	}

	if !cfg.DevMode {
		if cfg.SupabaseURL == "" {
			return nil, fmt.Errorf("BRIDGE_SUPABASE_URL is required")
		}
		if cfg.WebhookSecret == "" && cfg.DeliveryMode == "webhook" {
			return nil, fmt.Errorf("BRIDGE_WEBHOOK_SECRET is required in webhook mode")
		}
		if cfg.ServerID == "" && cfg.DeliveryMode == "poll" {
			return nil, fmt.Errorf("BRIDGE_SERVER_ID is required in poll mode")
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
