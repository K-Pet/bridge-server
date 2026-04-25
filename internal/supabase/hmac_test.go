package supabase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	testSupabase = "" // populated per-test from the fake server URL
	testAnon     = "anon-key"
	testServerID = "srv-abc123"
	testSecret   = "shhhh"
)

func TestSignedRequester_NilWhenUnconfigured(t *testing.T) {
	for _, c := range []struct {
		name                       string
		url, anon, server, secret  string
	}{
		{"empty url", "", testAnon, testServerID, testSecret},
		{"empty anon", "https://x", "", testServerID, testSecret},
		{"empty server", "https://x", testAnon, "", testSecret},
		{"empty secret", "https://x", testAnon, testServerID, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := NewSignedRequester(c.url, c.anon, c.server, c.secret); got != nil {
				t.Errorf("expected nil, got non-nil")
			}
		})
	}
}

// Sends body, signature, and headers exactly as documented. Verifies
// the signature is reproducible from the body + secret (so the EF on
// the receive side can check it the same way).
func TestSignedRequester_SignsBodyAndSetsHeaders(t *testing.T) {
	type capture struct {
		Body      []byte
		Headers   http.Header
		Path      string
	}
	var got capture

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path = r.URL.Path
		got.Headers = r.Header.Clone()
		got.Body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rq := NewSignedRequester(srv.URL, testAnon, testServerID, testSecret)
	if rq == nil {
		t.Fatal("requester nil")
	}

	type out struct {
		Ok bool `json:"ok"`
	}
	var o out
	if err := rq.Invoke(context.Background(), "mark-purchase-status",
		map[string]any{"purchase_id": "p1", "status": "delivered"},
		&o); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !o.Ok {
		t.Error("response decode failed")
	}

	if got.Path != "/functions/v1/mark-purchase-status" {
		t.Errorf("path = %q, want /functions/v1/mark-purchase-status", got.Path)
	}
	if got.Headers.Get("apikey") != testAnon {
		t.Errorf("apikey header missing/wrong: %q", got.Headers.Get("apikey"))
	}
	if got.Headers.Get("X-Bridge-Server-Id") != testServerID {
		t.Errorf("server-id header missing: %q", got.Headers.Get("X-Bridge-Server-Id"))
	}

	// Reproduce the signature from the body to confirm the EF can do
	// the same.
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(got.Body)
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if got.Headers.Get("X-Bridge-Signature") != expectedSig {
		t.Errorf("signature mismatch: header=%q expected=%q",
			got.Headers.Get("X-Bridge-Signature"), expectedSig)
	}

	// Body must include a timestamp for replay protection.
	var parsed map[string]any
	if err := json.Unmarshal(got.Body, &parsed); err != nil {
		t.Fatalf("body parse: %v", err)
	}
	if _, ok := parsed["timestamp"]; !ok {
		t.Error("body missing timestamp")
	}
	if parsed["purchase_id"] != "p1" || parsed["status"] != "delivered" {
		t.Errorf("body lost caller fields: %v", parsed)
	}
}

// EF returns 4xx — error must include status + body for diagnosis.
// (No matter how clever the caller, "invalid signature" beats "500".)
func TestSignedRequester_PropagatesEFErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid signature"}`))
	}))
	defer srv.Close()

	rq := NewSignedRequester(srv.URL, testAnon, testServerID, testSecret)
	err := rq.Invoke(context.Background(), "mark-purchase-status", map[string]any{"x": 1}, nil)
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !contains(err.Error(), "401") || !contains(err.Error(), "invalid signature") {
		t.Errorf("error string missing key info: %q", err.Error())
	}
}

// Timestamp is current and parseable — pinned to a fake clock so we
// don't have a fuzz factor.
func TestSignedRequester_TimestampIsCurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pinned := time.Date(2026, 4, 24, 19, 50, 0, 0, time.UTC)

	var captured []byte
	captureSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer captureSrv.Close()

	rq := NewSignedRequester(captureSrv.URL, testAnon, testServerID, testSecret)
	rq.nowFunc = func() time.Time { return pinned }

	if err := rq.Invoke(context.Background(), "any", map[string]any{"a": 1}, nil); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	_ = json.Unmarshal(captured, &parsed)
	if parsed["timestamp"] != "2026-04-24T19:50:00Z" {
		t.Errorf("timestamp = %v, want pinned UTC", parsed["timestamp"])
	}
}

// Non-object payload (e.g. nil) gets wrapped — EFs always see an
// object with a timestamp field.
func TestSignedRequester_WrapsNonObjectPayloads(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rq := NewSignedRequester(srv.URL, testAnon, testServerID, testSecret)
	if err := rq.Invoke(context.Background(), "any", nil, nil); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(captured, &parsed); err != nil {
		t.Fatalf("body not an object: %v", err)
	}
	if _, ok := parsed["timestamp"]; !ok {
		t.Error("nil payload still needs a timestamp")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
