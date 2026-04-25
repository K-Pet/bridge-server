// Onboarding endpoints — streamline sign-up → profile → pair into a
// single server-side flow so a new user hitting this bridge-server for the
// first time can be fully set up in under a minute.
//
// As of Phase 2b, every Supabase write here goes through a marketplace
// Edge Function authenticated with the user's forwarded JWT. Bridge-
// server no longer holds the project's service-role key, so these
// handlers extract the inbound Bearer token and pass it to the client.

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/supabase"
)

// bearerToken returns the user JWT from the Authorization header, or
// "" if absent. The auth.Middleware already verified it before we got
// here, so we trust its presence — the only reason it would be empty
// is dev-mode pass-through, which the handlers gate on userID anyway.
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

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
		jwt := bearerToken(r)
		slog.Info("onboarding status request", "userID", userID, "hasAuth", jwt != "")
		if userID == "" {
			// No user identity (dev pass-through) — skip onboarding entirely.
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

		profile, profileErr := sc.GetUserProfile(r.Context(), jwt)
		server, serverErr := sc.GetPairStatus(r.Context(), jwt)
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
// marketplace account by calling register-home-server with the user's
// forwarded JWT.
func handleAutoPair(sc *supabase.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		jwt := bearerToken(r)
		if userID == "" || jwt == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing user identity")
			return
		}

		server, err := sc.AutoPair(r.Context(), jwt)
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
		jwt := bearerToken(r)
		if userID == "" || jwt == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing user identity")
			return
		}

		server, err := sc.GetPairStatus(r.Context(), jwt)
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
