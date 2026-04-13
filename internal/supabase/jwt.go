package supabase

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// VerifyJWT validates a Supabase JWT and returns the user ID (sub claim).
// The jwtSecret is the Supabase project's JWT secret (HMAC-SHA256 signing key).
func VerifyJWT(tokenString string, jwtSecret string) (userID string, err error) {
	if jwtSecret == "" {
		return "", fmt.Errorf("JWT secret not configured")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil {
		return "", fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("invalid claims")
	}

	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return "", fmt.Errorf("missing sub claim")
	}

	return sub, nil
}
