package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withEnv resets a curated set of BRIDGE_* env vars for the duration of
// a test so a leaky CI shell or a parent test can't bleed in. Use it at
// the top of any test that calls Load().
func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	keys := []string{
		"BRIDGE_PORT", "BRIDGE_DATA", "BRIDGE_MUSIC_DIR", "BRIDGE_ND_URL",
		"BRIDGE_SUPABASE_URL", "BRIDGE_SUPABASE_ANON_KEY",
		"BRIDGE_SUPABASE_SERVICE_KEY", "BRIDGE_SUPABASE_JWT_SECRET",
		"BRIDGE_WEBHOOK_SECRET", "BRIDGE_DELIVERY_MODE", "BRIDGE_POLL_INTERVAL",
		"BRIDGE_SERVER_ID", "BRIDGE_SECRET", "BRIDGE_DEV",
		"BRIDGE_MARKETPLACE_URL", "BRIDGE_EXTERNAL_URL", "BRIDGE_LABEL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// Tilt path: BRIDGE_DEV=true with no BRIDGE_SERVER_ID. We must NOT
// auto-mint (would scatter credentials.json into the dev data dir,
// which would survive `rm -rf data/` partial resets and bind a stale
// id to the marketplace's user_home_servers row); instead the loader
// falls back to the stable "local-dev" sentinel.
func TestLoad_DevModeKeepsLocalDev(t *testing.T) {
	dir := t.TempDir()
	withEnv(t, map[string]string{
		"BRIDGE_DEV":  "true",
		"BRIDGE_DATA": dir,
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServerID != "local-dev" {
		t.Errorf("ServerID = %q, want local-dev", cfg.ServerID)
	}
	if _, err := os.Stat(filepath.Join(dir, credentialsFilename)); !os.IsNotExist(err) {
		t.Errorf("credentials.json should NOT exist in DevMode, got err=%v", err)
	}
}

// Tilt path with env-supplied values from .env.local (typical operator
// workflow). Env wins, no file is written.
func TestLoad_DevModeRespectsEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	withEnv(t, map[string]string{
		"BRIDGE_DEV":            "true",
		"BRIDGE_DATA":           dir,
		"BRIDGE_SERVER_ID":      "tilt-from-env-local",
		"BRIDGE_WEBHOOK_SECRET": "tilt-secret",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServerID != "tilt-from-env-local" {
		t.Errorf("ServerID = %q, want tilt-from-env-local", cfg.ServerID)
	}
	if cfg.WebhookSecret != "tilt-secret" {
		t.Errorf("WebhookSecret = %q, want tilt-secret", cfg.WebhookSecret)
	}
}

// Production path: no env-supplied values. The loader auto-mints both
// fields and writes credentials.json with 0600 perms. This test
// asserts the visible side effect of Load(), not just the helper.
func TestLoad_ProdAutoMintsAndPersists(t *testing.T) {
	dir := t.TempDir()
	withEnv(t, map[string]string{
		"BRIDGE_DATA":         dir,
		"BRIDGE_SUPABASE_URL": "https://example.supabase.co",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServerID == "" || cfg.WebhookSecret == "" {
		t.Fatalf("auto-mint failed: id=%q secret=%q", cfg.ServerID, cfg.WebhookSecret)
	}
	if cfg.ServerID == "local-dev" {
		t.Errorf("ServerID should not be local-dev outside DevMode")
	}

	info, err := os.Stat(filepath.Join(dir, credentialsFilename))
	if err != nil {
		t.Fatalf("credentials.json missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("credentials.json perms = %o, want 0600", perm)
	}
}

// Production sanity: SUPABASE_URL is still required when not in dev,
// and the validation fires AFTER auto-mint (so we can't silently
// succeed on a misconfigured prod install).
func TestLoad_ProdRequiresSupabaseURL(t *testing.T) {
	dir := t.TempDir()
	withEnv(t, map[string]string{
		"BRIDGE_DATA": dir,
	})

	if _, err := Load(); err == nil {
		t.Fatal("expected error for missing BRIDGE_SUPABASE_URL")
	}
}
