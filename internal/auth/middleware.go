package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/bridgemusic/bridge-server/internal/supabase"
)

type contextKey string

const userIDKey contextKey = "user_id"

// Middleware extracts and verifies the Supabase JWT from the Authorization
// header. Used in both dev and prod — the frontend always authenticates via
// Supabase (dev auto-signs-in with seeded credentials).
//
// If jwtSecret is empty (Supabase not configured), all requests pass through
// without a user ID — handlers must tolerate an empty UserID in that case.
func Middleware(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// No JWT secret → Supabase not configured, pass through.
			if jwtSecret == "" {
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(authHeader, "Bearer ")

			userID, err := supabase.VerifyJWT(token, jwtSecret)
			if err != nil {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserID returns the authenticated user's Supabase ID from the request
// context, or "" if no user is authenticated.
func UserID(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey).(string); ok {
		return v
	}
	return ""
}
