package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/config"
)

type purchaseRequest struct {
	AlbumID string `json:"album_id,omitempty"`
	TrackID string `json:"track_id,omitempty"`
}

type catalogAlbum struct {
	ID        string `json:"id"`
	PriceCents *int  `json:"price_cents"`
}

type catalogTrack struct {
	ID        string `json:"id"`
	PriceCents *int  `json:"price_cents"`
}

// handleMarketplacePurchase creates a purchase in Supabase and triggers the
// deliver-purchase Edge Function. This simulates the full buy flow for testing.
func handleMarketplacePurchase(cfg *config.Config) http.HandlerFunc {
	client := &http.Client{Timeout: 30 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// In dev mode the middleware may set "dev-user" which isn't a valid
		// Supabase UUID. Resolve it to the first auth user so purchases work.
		if cfg.DevMode && userID == "dev-user" {
			resolved, err := resolveDevUser(client, cfg)
			if err != nil {
				slog.Error("failed to resolve dev user", "error", err)
				http.Error(w, "no test user found — run ./supabase/seed.sh first", http.StatusInternalServerError)
				return
			}
			userID = resolved
		}

		var req purchaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if req.AlbumID == "" && req.TrackID == "" {
			http.Error(w, "album_id or track_id required", http.StatusBadRequest)
			return
		}

		// Look up the price from the catalog
		var priceCents int
		var itemType string
		var itemID string

		if req.AlbumID != "" {
			itemType = "album"
			itemID = req.AlbumID
			price, err := lookupAlbumPrice(client, cfg, req.AlbumID)
			if err != nil {
				slog.Error("failed to lookup album price", "error", err)
				http.Error(w, "album not found", http.StatusNotFound)
				return
			}
			priceCents = price
		} else {
			itemType = "track"
			itemID = req.TrackID
			price, err := lookupTrackPrice(client, cfg, req.TrackID)
			if err != nil {
				slog.Error("failed to lookup track price", "error", err)
				http.Error(w, "track not found", http.StatusNotFound)
				return
			}
			priceCents = price
		}

		// Create the purchase
		purchaseID, err := createPurchase(client, cfg, userID, priceCents)
		if err != nil {
			slog.Error("failed to create purchase", "error", err)
			http.Error(w, "failed to create purchase", http.StatusInternalServerError)
			return
		}

		// Create purchase item
		if err := createPurchaseItem(client, cfg, purchaseID, itemType, itemID, priceCents); err != nil {
			slog.Error("failed to create purchase item", "error", err)
			http.Error(w, "failed to create purchase item", http.StatusInternalServerError)
			return
		}

		slog.Info("purchase created", "purchase_id", purchaseID, "user_id", userID, "item_type", itemType, "item_id", itemID)

		// Trigger the Edge Function to deliver
		deliveryErr := triggerDelivery(client, cfg, purchaseID)
		if deliveryErr != nil {
			slog.Warn("delivery trigger failed (purchase still created)", "purchase_id", purchaseID, "error", deliveryErr)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"purchase_id":    purchaseID,
			"status":         "pending",
			"delivery_error": errString(deliveryErr),
		})
	}
}

func lookupAlbumPrice(client *http.Client, cfg *config.Config, albumID string) (int, error) {
	url := fmt.Sprintf("%s/rest/v1/albums?id=eq.%s&select=id,price_cents", cfg.SupabaseURL, albumID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var albums []catalogAlbum
	if err := json.NewDecoder(resp.Body).Decode(&albums); err != nil {
		return 0, err
	}
	if len(albums) == 0 {
		return 0, fmt.Errorf("album not found: %s", albumID)
	}
	if albums[0].PriceCents == nil {
		return 0, fmt.Errorf("album not for sale: %s", albumID)
	}
	return *albums[0].PriceCents, nil
}

func lookupTrackPrice(client *http.Client, cfg *config.Config, trackID string) (int, error) {
	url := fmt.Sprintf("%s/rest/v1/tracks?id=eq.%s&select=id,price_cents", cfg.SupabaseURL, trackID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var tracks []catalogTrack
	if err := json.NewDecoder(resp.Body).Decode(&tracks); err != nil {
		return 0, err
	}
	if len(tracks) == 0 {
		return 0, fmt.Errorf("track not found: %s", trackID)
	}
	if tracks[0].PriceCents == nil {
		return 0, fmt.Errorf("track not for sale: %s", trackID)
	}
	return *tracks[0].PriceCents, nil
}

func createPurchase(client *http.Client, cfg *config.Config, userID string, totalCents int) (string, error) {
	body, _ := json.Marshal([]map[string]any{{
		"user_id":     userID,
		"total_cents": totalCents,
		"payment_ref": "dev-test-" + time.Now().Format("20060102-150405"),
		"status":      "pending",
		"server_id":   "local-dev",
	}})

	url := cfg.SupabaseURL + "/rest/v1/purchases"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=representation")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create purchase failed: %d %s", resp.StatusCode, string(respBody))
	}

	var created []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}
	if len(created) == 0 {
		return "", fmt.Errorf("no purchase returned")
	}
	return created[0].ID, nil
}

func createPurchaseItem(client *http.Client, cfg *config.Config, purchaseID, itemType, itemID string, priceCents int) error {
	item := map[string]any{
		"purchase_id": purchaseID,
		"price_cents": priceCents,
	}
	if itemType == "album" {
		item["album_id"] = itemID
	} else {
		item["track_id"] = itemID
	}

	body, _ := json.Marshal([]map[string]any{item})
	url := cfg.SupabaseURL + "/rest/v1/purchase_items"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create item failed: %d %s", resp.StatusCode, string(respBody))
	}
	return nil
}

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
