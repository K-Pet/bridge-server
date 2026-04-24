package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// credentialsFilename is the JSON document inside cfg.DataDir that stores
// auto-minted per-server values. Field names are stable on-disk format —
// don't rename without a migration.
const credentialsFilename = "credentials.json"

// persistedCredentials are values that:
//   - never need to leave the host (so they don't belong in env, image,
//     or a config server)
//   - never change after first generation (regenerating would orphan the
//     marketplace's user_home_servers row + invalidate any in-flight
//     webhook signatures)
//
// Stored at ${BRIDGE_DATA}/credentials.json with 0600 perms, written
// atomically so a crash mid-write can't corrupt the document.
type persistedCredentials struct {
	ServerID      string `json:"server_id"`
	WebhookSecret string `json:"webhook_secret"`
}

// loadOrMintCredentials populates cfg.ServerID and cfg.WebhookSecret if
// either is empty, persisting any newly-minted values back to disk.
//
// Resolution order per field (most → least specific):
//  1. value already on cfg (came from env)
//  2. value in credentials.json (minted on a previous boot)
//  3. fresh random value (minted now, written to credentials.json)
//
// Idempotent: subsequent boots load step 2 and skip minting.
func loadOrMintCredentials(cfg *Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	path := filepath.Join(cfg.DataDir, credentialsFilename)

	persisted, err := readCredentials(path)
	if err != nil {
		return err
	}

	minted := false

	if cfg.ServerID == "" {
		if persisted.ServerID == "" {
			id, err := randomHex(16) // 128 bits — plenty for uniqueness
			if err != nil {
				return fmt.Errorf("mint server id: %w", err)
			}
			persisted.ServerID = id
			minted = true
		}
		cfg.ServerID = persisted.ServerID
	}

	if cfg.WebhookSecret == "" {
		if persisted.WebhookSecret == "" {
			secret, err := randomHex(32) // 256 bits — matches openssl rand -hex 32
			if err != nil {
				return fmt.Errorf("mint webhook secret: %w", err)
			}
			persisted.WebhookSecret = secret
			minted = true
		}
		cfg.WebhookSecret = persisted.WebhookSecret
	}

	// Only rewrite the file if we minted new values OR the existing file
	// was missing fields a future bridge-server would expect — keeps the
	// boot path read-only for unchanged installs.
	if minted {
		if err := writeCredentialsAtomic(path, persisted); err != nil {
			return fmt.Errorf("persist credentials: %w", err)
		}
		slog.Info("auto-minted server credentials", "path", path)
	}

	return nil
}

func readCredentials(path string) (*persistedCredentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &persistedCredentials{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c := &persistedCredentials{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf(
			"parse %s: %w (delete the file to regenerate)",
			path, err,
		)
	}
	return c, nil
}

// writeCredentialsAtomic writes via a temp file + rename so a crash
// mid-write leaves either the old or new contents on disk — never a
// half-written file. Linux rename(2) is atomic on the same filesystem.
func writeCredentialsAtomic(path string, c *persistedCredentials) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup; the failure already returns
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func randomHex(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
