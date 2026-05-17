package navidrome

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

const (
	credentialsFile = "nd-credentials"
	// previousCredentialsSuffix is the on-disk shadow file of the
	// last-known-good credentials. Written by atomic saves before a
	// new rotation lands, and used by Bootstrap as a recovery
	// fallback when the primary file fails to authenticate (typical
	// cause: a rotation that succeeded in Navidrome but whose disk
	// persistence finished partially, e.g. host crash mid-write).
	previousCredentialsSuffix = ".prev"
	adminUsername             = "bridge-admin"
)

// Bootstrap ensures we have working Navidrome admin credentials.
// On first run, it creates the admin user. On subsequent runs, it loads
// stored credentials.
//
// Self-healing: if stored credentials fail to authenticate (most often
// because Navidrome's data volume was wiped on stack recreation while
// /data/bridge persisted — common with relative bind mounts under
// Portainer), we attempt to re-create the admin user. If Navidrome has
// no users, createAdmin succeeds and we overwrite the stored creds; if
// it returns 403, real users exist with a different password and we
// surface a clear recovery error. Without this, an operator would have
// to manually delete /data/bridge/nd-credentials after every reset.
func Bootstrap(ctx context.Context, cfg *config.Config) (*Client, error) {
	credPath := filepath.Join(cfg.DataDir, credentialsFile)

	creds, loadErr := loadCredentials(credPath)
	freshlyMinted := false
	if loadErr != nil {
		slog.Info("no existing credentials, creating admin user")
		var err error
		creds, err = createAdmin(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create admin: %w", err)
		}
		if err := atomicSaveCredentials(credPath, creds); err != nil {
			return nil, fmt.Errorf("failed to save credentials: %w", err)
		}
		freshlyMinted = true
	}

	client, authErr := authenticatedClient(ctx, cfg, creds)
	if authErr == nil {
		slog.Info("authenticated with navidrome", "user", creds.Username)
		return client, nil
	}

	if freshlyMinted {
		// We just created the admin — if auth still fails it's not a
		// reset/mismatch case, it's a real problem. Don't retry.
		return nil, fmt.Errorf("failed to authenticate with freshly-created admin: %w", authErr)
	}

	slog.Warn("stored navidrome credentials failed to authenticate, trying .prev fallback",
		"error", authErr, "user", creds.Username)

	// Recovery step 1: try the previous-credentials shadow file. This
	// covers the case where a rotation persisted to disk but the host
	// crashed before the new password actually landed in Navidrome, or
	// where the primary file got truncated by a partial write that
	// pre-dated atomicSaveCredentials.
	if prevCreds, prevLoadErr := loadCredentials(credPath + previousCredentialsSuffix); prevLoadErr == nil {
		if prevClient, prevAuthErr := authenticatedClient(ctx, cfg, prevCreds); prevAuthErr == nil {
			slog.Warn("recovered using nd-credentials.prev — promoting back to primary",
				"user", prevCreds.Username)
			if err := atomicSaveCredentials(credPath, prevCreds); err != nil {
				// Recovered in memory; couldn't promote .prev back to
				// primary. Log and continue — the running process is
				// fine, but the next restart will hit the same fallback.
				slog.Error("failed to promote .prev credentials to primary", "error", err)
			}
			return prevClient, nil
		} else {
			slog.Warn(".prev credentials also failed to authenticate", "error", prevAuthErr)
		}
	}

	slog.Warn("attempting createAdmin recovery (only works if navidrome has no users)")
	newCreds, recoverErr := createAdmin(ctx, cfg)
	if recoverErr != nil {
		return nil, fmt.Errorf(
			"navidrome auth failed and admin re-creation failed (auth_err=%v): %w",
			authErr, recoverErr,
		)
	}
	if err := atomicSaveCredentials(credPath, newCreds); err != nil {
		return nil, fmt.Errorf("failed to save recovered credentials: %w", err)
	}

	client, authErr = authenticatedClient(ctx, cfg, newCreds)
	if authErr != nil {
		return nil, fmt.Errorf("failed to authenticate after recovery: %w", authErr)
	}
	slog.Info("recovered navidrome admin credentials after data reset", "user", newCreds.Username)
	return client, nil
}

func authenticatedClient(ctx context.Context, cfg *config.Config, creds *credentials) (*Client, error) {
	client := NewClient(cfg.NavidromeURL, creds.Username, creds.Password)
	client.musicDir = cfg.MusicDir
	if err := client.Authenticate(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

func createAdmin(ctx context.Context, cfg *config.Config) (*credentials, error) {
	password, err := generatePassword(cfg.MasterSecret)
	if err != nil {
		return nil, err
	}

	body := fmt.Sprintf(`{"username":%q,"password":%q}`, adminUsername, password)
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.NavidromeURL+"/auth/createAdmin", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("createAdmin request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("navidrome already has users — cannot auto-create admin. Remove /data/navidrome to reset, or restore /data/bridge/nd-credentials")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("createAdmin returned status %d", resp.StatusCode)
	}

	return &credentials{Username: adminUsername, Password: password}, nil
}

// generatePassword creates a password. If a master secret is provided, the password
// is deterministic (SHA-256 derived) so it can be recovered. Otherwise, random.
func generatePassword(masterSecret string) (string, error) {
	if masterSecret != "" {
		h := sha256.Sum256([]byte(masterSecret + ":navidrome-admin"))
		return hex.EncodeToString(h[:]), nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func loadCredentials(path string) (*credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

func saveCredentials(path string, creds *credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// atomicSaveCredentials writes new credentials, keeping the previous
// file as ${path}.prev for recovery, with an atomic rename for the
// primary so partial writes can't leave the primary truncated:
//
//  1. Write the new content to ${path}.new with fsync.
//  2. Snapshot the existing primary to ${path}.prev (best-effort —
//     a missing or unreadable primary is not fatal; the .new is the
//     authoritative source of truth from this point on).
//  3. Atomically rename ${path}.new → ${path}.
//
// POSIX rename is atomic within a filesystem, so step 3 guarantees
// the primary file is either the old contents or the new contents,
// never a partial write. The .prev shadow lets Bootstrap recover from
// the narrow window where step 1 succeeded but a host crash or disk
// fault interrupted step 3.
func atomicSaveCredentials(path string, creds *credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	// Best-effort: snapshot the existing primary to .prev so we have
	// a known-good fallback if a subsequent rotation goes wrong. Don't
	// fail the save if the snapshot can't be taken — the primary swap
	// below is still atomic and still safer than the old in-place
	// write.
	if existing, readErr := os.ReadFile(path); readErr == nil {
		_ = os.WriteFile(path+previousCredentialsSuffix, existing, 0600)
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// SaveAdminCredentials writes username/password to the on-disk
// credentials file (${dataDir}/nd-credentials, 0600) atomically, with
// the previous file preserved as ${dataDir}/nd-credentials.prev for
// recovery. Used by the password-rotation flow to persist a new
// admin password.
//
// Ordering note for the rotation flow: this is invoked *after*
// Navidrome has accepted the new password and *before* the in-memory
// client commits to the new password. A failure here triggers a
// Navidrome rollback in ChangeAdminPassword, so a partial state is
// not externally observable.
func SaveAdminCredentials(dataDir, username, password string) error {
	return atomicSaveCredentials(filepath.Join(dataDir, credentialsFile), &credentials{
		Username: username,
		Password: password,
	})
}

// WaitReady polls Navidrome's /ping endpoint until it responds 200 or the timeout expires.
func WaitReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+"/ping", nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("navidrome not ready after %s", timeout)
}
