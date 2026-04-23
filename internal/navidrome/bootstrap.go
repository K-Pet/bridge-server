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
	adminUsername   = "bridge-admin"
)

// Bootstrap ensures we have working Navidrome admin credentials.
// On first run, it creates the admin user. On subsequent runs, it loads stored credentials.
func Bootstrap(ctx context.Context, cfg *config.Config) (*Client, error) {
	credPath := filepath.Join(cfg.DataDir, credentialsFile)

	creds, err := loadCredentials(credPath)
	if err != nil {
		slog.Info("no existing credentials, creating admin user")
		creds, err = createAdmin(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create admin: %w", err)
		}
		if err := saveCredentials(credPath, creds); err != nil {
			return nil, fmt.Errorf("failed to save credentials: %w", err)
		}
	}

	client := NewClient(cfg.NavidromeURL, creds.Username, creds.Password)
	client.musicDir = cfg.MusicDir
	if err := client.Authenticate(ctx); err != nil {
		return nil, fmt.Errorf("failed to authenticate with stored credentials: %w", err)
	}

	slog.Info("authenticated with navidrome", "user", creds.Username)
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
