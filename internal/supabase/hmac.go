package supabase

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SignedRequester signs outbound requests to marketplace Edge Functions
// that act in *server context* (no user JWT available — e.g. the poller
// or the downloader's status updates). Symmetric counterpart to the
// inbound supabase.VerifyAndParse: same HMAC-SHA256 over the JSON body,
// same hex encoding, same in-body `timestamp` for replay protection.
//
// Server identity to the marketplace is the (server_id, webhook_secret)
// pair the registration flow upserts into user_home_servers. EFs
// receiving these requests look up the row by server_id and verify the
// HMAC against its webhook_secret.
type SignedRequester struct {
	functionsBaseURL string // typically <SUPABASE_URL>/functions/v1
	anonKey          string // sent as `apikey` so kong/Supabase routes the call
	serverID         string
	webhookSecret    string
	httpClient       *http.Client

	// nowFunc lets tests inject a fixed time for the in-body timestamp.
	// Production callers leave it nil and the helper uses time.Now().
	nowFunc func() time.Time
}

// NewSignedRequester returns nil if any of the required identity inputs
// is empty. Callers that get nil must surface a useful error rather than
// silently no-op'ing — an unconfigured signer means the bridge-server
// hasn't bootstrapped yet.
func NewSignedRequester(
	supabaseURL, anonKey, serverID, webhookSecret string,
) *SignedRequester {
	if supabaseURL == "" || anonKey == "" || serverID == "" || webhookSecret == "" {
		return nil
	}
	return &SignedRequester{
		functionsBaseURL: supabaseURL + "/functions/v1",
		anonKey:          anonKey,
		serverID:         serverID,
		webhookSecret:    webhookSecret,
		httpClient:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Invoke marshals payload to JSON, signs it, and POSTs to the named
// function. The returned response body is decoded into out (which may
// be nil for fire-and-forget calls). Non-2xx responses come back as a
// formatted error including the response body for diagnosis.
//
// payload may be a struct, map, or any json.Marshal-compatible value.
// A `timestamp` field is added to top-level JSON objects automatically;
// passing a struct that already contains one will get overwritten by
// the signer (so callers can ignore it). Non-object payloads (arrays,
// scalars) are signed without a timestamp wrapper, but no current EF
// expects that shape and the signer wraps non-object payloads in
// `{"data": ..., "timestamp": ...}` to keep the contract consistent.
func (s *SignedRequester) Invoke(ctx context.Context, function string, payload, out any) error {
	body, err := s.encodeBody(payload)
	if err != nil {
		return fmt.Errorf("encode body: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(s.webhookSecret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		s.functionsBaseURL+"/"+function,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.anonKey)
	// EF authentication header — no Authorization Bearer (no user JWT).
	// The EF reads server_id, looks up the row's webhook_secret, and
	// re-computes the HMAC over the body to verify.
	req.Header.Set("X-Bridge-Server-Id", s.serverID)
	req.Header.Set("X-Bridge-Signature", signature)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("invoke %s: %w", function, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read up to 4 KB of the error body so we can include the EF's
		// JSON error in the returned error string. The EF body is the
		// best signal for distinguishing wrong-secret from
		// not-yet-paired from genuinely-broken.
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return fmt.Errorf("%s returned %d: %s", function, resp.StatusCode, string(buf[:n]))
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", function, err)
	}
	return nil
}

func (s *SignedRequester) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now().UTC()
}

// encodeBody adds a `timestamp` field to JSON objects. Non-objects get
// wrapped so EFs always see a uniform shape (object with `timestamp`).
func (s *SignedRequester) encodeBody(payload any) ([]byte, error) {
	ts := s.now().Format(time.RFC3339Nano)

	if payload == nil {
		return json.Marshal(map[string]any{"timestamp": ts})
	}

	// First marshal to bytes so we can sniff whether it serialised to
	// a JSON object. Doing it via a round-trip keeps us out of any
	// reflection edge cases for callers passing custom types.
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 && raw[0] == '{' {
		// Top-level object — splice the timestamp in.
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		m["timestamp"] = ts
		return json.Marshal(m)
	}
	// Non-object payload — wrap.
	return json.Marshal(map[string]any{"data": payload, "timestamp": ts})
}
