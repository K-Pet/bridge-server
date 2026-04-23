package api

import (
	"bytes"
	"context"
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
func triggerDelivery(ctx context.Context, client *http.Client, cfg *config.Config, purchaseID string) error {
	body, _ := json.Marshal(map[string]string{"purchase_id": purchaseID})

	reqURL := fmt.Sprintf("%s/functions/v1/deliver-purchase", cfg.SupabaseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build delivery request: %w", err)
	}
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
