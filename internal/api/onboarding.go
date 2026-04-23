// Onboarding endpoints — streamline sign-up → profile → pair into a
// single server-side flow so a new user hitting this bridge-server for the
// first time can be fully set up in under a minute.

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/supabase"
)

// handleOnboardingStatus returns the user's current onboarding state so
// the frontend can decide which step to show (or skip straight to the
// main app). Response shape:
//
//	{
//	  "profile_complete": true/false,
//	  "server_paired":    true/false,
//	  "auto_pair_available": true/false,
//	  "profile": { "username": "...", ... } | null,
//	  "server":  { "label": "...", ... } | null
//	}
func handleOnboardingStatus(sc *supabase.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		slog.Info("onboarding status request", "userID", userID, "hasAuth", r.Header.Get("Authorization") != "")
		if userID == "" {
			// No user identity — skip onboarding entirely.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"profile_complete":   true,
				"server_paired":     true,
				"auto_pair_available": false,
				"profile":           nil,
				"server":            nil,
			})
			return
		}

		profile, profileErr := sc.GetUserProfile(r.Context(), userID)
		server, serverErr := sc.GetPairStatus(r.Context(), userID)
		slog.Info("onboarding lookup results", "userID", userID, "profile", profile, "profileErr", profileErr, "server", server, "serverErr", serverErr)

		profileComplete := false
		if profileErr == nil && profile != nil {
			if username, ok := profile["username"].(string); ok && username != "" {
				profileComplete = true
			}
		}

		serverPaired := serverErr == nil && server != nil

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"profile_complete":   profileComplete,
			"server_paired":     serverPaired,
			"auto_pair_available": sc.CanAutoPair(),
			"profile":           profile,
			"server":            server,
		})
	}
}

// handleAutoPair pairs this bridge-server to the authenticated user's
// marketplace account by upserting into user_home_servers directly.
func handleAutoPair(sc *supabase.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing user identity")
			return
		}

		server, err := sc.AutoPair(r.Context(), userID)
		if err != nil {
			slog.Error("auto-pair failed", "user", userID, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "auto_pair_failed", "failed to pair server")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"paired":  true,
			"server":  server,
		})
	}
}

// handlePairStatus returns whether the user has a home server paired.
func handlePairStatus(sc *supabase.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing user identity")
			return
		}

		server, err := sc.GetPairStatus(r.Context(), userID)
		if err != nil {
			slog.Error("pair status check failed", "user", userID, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "pair_status_failed", "failed to check pair status")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"paired": server != nil,
			"server": server,
		})
	}
}
