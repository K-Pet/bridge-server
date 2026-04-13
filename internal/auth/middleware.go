package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/bridgemusic/bridge-server/internal/supabase"
)

type contextKey string

const userIDKey contextKey = "user_id"

// Middleware extracts and verifies the Supabase JWT from the Authorization header.
func Middleware(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(auth, "Bearer ")

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

// DevMiddleware tries real JWT auth if a Bearer token is provided, otherwise
// falls back to a fixed "dev-user" identity for unauthenticated requests.
func DevMiddleware(jwtSecret ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := "dev-user"

			// Try to extract real user ID from JWT if present
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") && len(jwtSecret) > 0 && jwtSecret[0] != "" {
				token := strings.TrimPrefix(auth, "Bearer ")
				if uid, err := supabase.VerifyJWT(token, jwtSecret[0]); err == nil {
					userID = uid
				}
			}

			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func UserID(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey).(string); ok {
		return v
	}
	return ""
}
