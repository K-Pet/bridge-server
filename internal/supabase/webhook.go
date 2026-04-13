package supabase

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/bridgemusic/bridge-server/internal/store"
)

// VerifyAndParse reads the request body, verifies the HMAC-SHA256 signature
// from the X-Bridge-Signature header, and returns the parsed purchase.
func VerifyAndParse(r *http.Request, secret string) (*store.Purchase, error) {
	signature := r.Header.Get("X-Bridge-Signature")
	if signature == "" {
		return nil, fmt.Errorf("missing signature header")
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return nil, fmt.Errorf("invalid signature")
	}

	var purchase store.Purchase
	if err := json.Unmarshal(body, &purchase); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}

	return &purchase, nil
}
