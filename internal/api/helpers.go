package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/bridgemusic/bridge-server/internal/config"
)

// triggerDelivery invokes the `deliver-purchase` Supabase Edge Function,
// which generates signed download URLs, HMAC-signs the payload, and POSTs
// it to this server's /api/webhook/purchase endpoint.
//
// Used by the redeliver endpoint. The primary purchase path goes through
// Stripe → stripe-webhook → deliver-purchase without touching this code.
func triggerDelivery(client *http.Client, cfg *config.Config, purchaseID string) error {
	body, _ := json.Marshal(map[string]string{"purchase_id": purchaseID})

	url := fmt.Sprintf("%s/functions/v1/deliver-purchase", cfg.SupabaseURL)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("edge function unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("edge function failed: %d %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// resolveDevUser fetches the first auth user from local Supabase to use as the
// purchase owner in dev mode (where there's no real JWT).
func resolveDevUser(client *http.Client, cfg *config.Config) (string, error) {
	url := cfg.SupabaseURL + "/auth/v1/admin/users?per_page=1"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Users []struct {
			ID string `json:"id"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Users) == 0 {
		return "", fmt.Errorf("no users found")
	}
	return result.Users[0].ID, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": message,
	})
}
