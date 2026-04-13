package supabase

import (
	"fmt"
)

// VerifyJWT validates a Supabase JWT and returns the user ID.
// TODO: implement with your Supabase JWT secret or JWKS endpoint.
func VerifyJWT(tokenString string, supabaseURL string) (userID string, err error) {
	// Implementation will use a JWT library (e.g., golang-jwt/jwt/v5)
	// to verify the token against your Supabase project's JWT secret.
	//
	// Steps:
	// 1. Parse and validate the JWT signature
	// 2. Check expiration (exp claim)
	// 3. Verify issuer matches supabaseURL + "/auth/v1"
	// 4. Extract and return the "sub" claim as userID
	return "", fmt.Errorf("not implemented")
}
