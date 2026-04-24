package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/bridgemusic/bridge-server/internal/supabase"
)

type contextKey string

const userIDKey contextKey = "user_id"

// Middleware extracts and verifies the Supabase JWT on the Authorization
// header, populating the request context with the resolved user id.
//
// Verification goes through ${SUPABASE_URL}/auth/v1/user (see
// supabase.AuthVerifier) so bridge-server doesn't need a copy of the
// project's JWT secret. Pass a nil verifier when Supabase isn't
// configured (dev mode without a Supabase stack) — requests then pass
// through unauthenticated and handlers must tolerate an empty UserID.
func Middleware(verifier *supabase.AuthVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if verifier == nil {
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(authHeader, "Bearer ")

			userID, err := verifier.VerifyToken(r.Context(), token)
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
