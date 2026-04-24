package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// First boot: mints both fields, persists to credentials.json with 0600.
func TestLoadOrMintCredentials_FirstBoot(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{DataDir: dir}

	if err := loadOrMintCredentials(cfg); err != nil {
		t.Fatalf("first mint: %v", err)
	}

	if cfg.ServerID == "" {
		t.Fatal("ServerID empty after mint")
	}
	if cfg.WebhookSecret == "" {
		t.Fatal("WebhookSecret empty after mint")
	}
	if len(cfg.WebhookSecret) != 64 { // 32 random bytes hex-encoded
		t.Fatalf("WebhookSecret has unexpected length %d", len(cfg.WebhookSecret))
	}

	path := filepath.Join(dir, credentialsFilename)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("credentials.json not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("credentials.json perms = %o, want 0600", perm)
	}

	// File contents round-trip cleanly.
	raw, _ := os.ReadFile(path)
	var parsed persistedCredentials
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("credentials.json malformed: %v", err)
	}
	if parsed.ServerID != cfg.ServerID || parsed.WebhookSecret != cfg.WebhookSecret {
		t.Fatal("on-disk values diverged from cfg")
	}
}

// Second boot: rehydrates from the persisted file unchanged.
func TestLoadOrMintCredentials_StableAcrossBoots(t *testing.T) {
	dir := t.TempDir()

	first := &Config{DataDir: dir}
	if err := loadOrMintCredentials(first); err != nil {
		t.Fatalf("boot 1: %v", err)
	}

	second := &Config{DataDir: dir}
	if err := loadOrMintCredentials(second); err != nil {
		t.Fatalf("boot 2: %v", err)
	}

	if first.ServerID != second.ServerID {
		t.Errorf("ServerID drifted across boots: %q -> %q", first.ServerID, second.ServerID)
	}
	if first.WebhookSecret != second.WebhookSecret {
		t.Errorf("WebhookSecret drifted across boots: %q -> %q", first.WebhookSecret, second.WebhookSecret)
	}
}

// Env-supplied values override persistence even when the file is present.
func TestLoadOrMintCredentials_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()

	// Pre-populate the file so the loader has something to ignore.
	first := &Config{DataDir: dir}
	if err := loadOrMintCredentials(first); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pinnedID := "operator-pinned-id"
	pinnedSecret := "operator-pinned-secret"
	override := &Config{
		DataDir:       dir,
		ServerID:      pinnedID,
		WebhookSecret: pinnedSecret,
	}
	if err := loadOrMintCredentials(override); err != nil {
		t.Fatalf("override: %v", err)
	}

	if override.ServerID != pinnedID {
		t.Errorf("ServerID = %q, want pinned %q", override.ServerID, pinnedID)
	}
	if override.WebhookSecret != pinnedSecret {
		t.Errorf("WebhookSecret = %q, want pinned %q", override.WebhookSecret, pinnedSecret)
	}

	// Persisted file is untouched — overriding via env shouldn't write
	// the env value into credentials.json (that would burn-in a value
	// the operator might rotate via env later).
	raw, _ := os.ReadFile(filepath.Join(dir, credentialsFilename))
	var parsed persistedCredentials
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if parsed.ServerID == pinnedID {
		t.Error("env override leaked into credentials.json")
	}
}

// One-half-only: env supplies WebhookSecret, ServerID auto-mints. The
// minted ServerID still persists; the env-supplied secret does not.
func TestLoadOrMintCredentials_PartialEnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		DataDir:       dir,
		WebhookSecret: "from-env",
	}
	if err := loadOrMintCredentials(cfg); err != nil {
		t.Fatalf("mint: %v", err)
	}
	if cfg.ServerID == "" {
		t.Fatal("ServerID should mint when not env-supplied")
	}
	if cfg.WebhookSecret != "from-env" {
		t.Fatal("env WebhookSecret should be preserved")
	}

	raw, _ := os.ReadFile(filepath.Join(dir, credentialsFilename))
	var parsed persistedCredentials
	_ = json.Unmarshal(raw, &parsed)
	if parsed.ServerID != cfg.ServerID {
		t.Error("minted ServerID should persist")
	}
	if parsed.WebhookSecret == "from-env" {
		t.Error("env WebhookSecret should NOT have been persisted")
	}
}

// Corrupt credentials.json fails loudly rather than silently re-minting
// (which would orphan the marketplace's user_home_servers row).
func TestLoadOrMintCredentials_CorruptFileFailsLoud(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, credentialsFilename),
		[]byte("not json"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{DataDir: dir}
	err := loadOrMintCredentials(cfg)
	if err == nil {
		t.Fatal("expected error on corrupt file, got nil")
	}
}
