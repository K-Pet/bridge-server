// Device pairing for the Bridge marketplace app.
//
// The marketplace only knows how to reach this server by URL; it has no
// way to know the server's ID or webhook secret.  This file implements a
// short-lived one-shot pairing code:
//
//   1. The owner — signed into the bridge-server web UI — calls
//      POST /api/pair/generate, which mints a random 6-digit code and
//      caches it in memory with a ~5 min TTL.  The UI displays the code.
//
//   2. The owner types the code into the marketplace "Pair home server"
//      form alongside this server's public URL.
//
//   3. The marketplace Edge Function (`pair-home-server`) does a server-
//      to-server GET /api/pair?code=NNNNNN against the URL the user
//      typed.  On a hit we return the config the marketplace needs to
//      sign + target webhooks — {server_id, webhook_secret, label} —
//      and immediately invalidate the code.
//
// The webhook_secret is the same BRIDGE_WEBHOOK_SECRET env this server
// already uses to HMAC-verify incoming `/api/webhook/purchase` bodies,
// so the marketplace and home server stay in sync without a second
// rotation mechanism in v1.

package api

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
)

const (
	pairCodeTTL     = 5 * time.Minute
	pairCodeLen     = 6
	pairMaxAttempts = 5
	pairFailDelay   = 500 * time.Millisecond
)

type pairCode struct {
	code      string
	expiresAt time.Time
	attempts  int
}

type pairStore struct {
	mu   sync.Mutex
	code *pairCode
}

var pair = &pairStore{}

func (s *pairStore) mint() (string, time.Time, error) {
	code, err := randomDigits(pairCodeLen)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(pairCodeTTL)

	s.mu.Lock()
	s.code = &pairCode{code: code, expiresAt: expires}
	s.mu.Unlock()

	return code, expires, nil
}

// consume returns true iff `code` matches the current pending code AND
// hasn't expired.  On match the code is cleared so a second redemption
// fails — one-shot by construction.
//
// Brute-force protection: after pairMaxAttempts wrong guesses the code is
// burned and the owner must generate a new one.
func (s *pairStore) consume(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.code == nil {
		return false
	}
	if time.Now().After(s.code.expiresAt) {
		s.code = nil
		return false
	}
	if s.code.code != code {
		s.code.attempts++
		if s.code.attempts >= pairMaxAttempts {
			s.code = nil // burned — too many wrong guesses
		}
		return false
	}
	s.code = nil
	return true
}

func randomDigits(n int) (string, error) {
	const digits = "0123456789"
	buf := make([]byte, n)
	max := big.NewInt(int64(len(digits)))
	for i := range buf {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		buf[i] = digits[k.Int64()]
	}
	return string(buf), nil
}

// handleGeneratePairCode — authed (sits behind /api/* auth middleware).
// Returns a short code the UI can display for pairing.
func handleGeneratePairCode() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, expires, err := pair.mint()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "mint_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"code":       code,
			"expires_at": expires.UTC().Format(time.RFC3339),
			"ttl_sec":    int(pairCodeTTL.Seconds()),
		})
	}
}

// handlePairExchange — unauthenticated (the marketplace doesn't have a
// JWT for this home server yet).  Security comes from the one-shot
// random code + attempt limiting: without knowing the code you can't
// redeem, and after 5 wrong guesses the code is burned.
func handlePairExchange(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_code", "code query param is required")
			return
		}
		if !pair.consume(code) {
			// Throttle brute-force attempts — caps throughput at ~2 req/s
			// even under parallel connections.
			time.Sleep(pairFailDelay)
			writeJSONError(w, http.StatusUnauthorized, "invalid_code", "pair code is missing, wrong, or expired")
			return
		}

		if cfg.ServerID == "" || cfg.WebhookSecret == "" {
			writeJSONError(
				w,
				http.StatusServiceUnavailable,
				"not_configured",
				"server is missing BRIDGE_SERVER_ID or BRIDGE_WEBHOOK_SECRET",
			)
			return
		}

		label := os.Getenv("BRIDGE_LABEL")
		if label == "" {
			label = cfg.ServerID
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"server_id":      cfg.ServerID,
			"label":          label,
			"webhook_secret": cfg.WebhookSecret,
		})
	}
}
