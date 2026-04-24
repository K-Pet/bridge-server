package supabase

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAuth stands in for ${SUPABASE_URL}/auth/v1/user. Returns the user
// id encoded in the bearer token (so tests can assert which token hit
// the wire), counts hits, and lets the test stage 401s.
type fakeAuth struct {
	hits    atomic.Int32
	respond func(token string) (status int, userID string)
}

func newFakeAuth() *fakeAuth {
	f := &fakeAuth{}
	// Default behaviour: any non-empty token resolves to id="user-<token>".
	f.respond = func(token string) (int, string) {
		if token == "" || token == "bad" {
			return http.StatusUnauthorized, ""
		}
		return http.StatusOK, "user-" + token
	}
	return f
}

func (f *fakeAuth) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if r.URL.Path != "/auth/v1/user" {
			http.NotFound(w, r)
			return
		}
		token := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(token) <= len(prefix) {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		token = token[len(prefix):]

		// Apikey header is required — bridge-server bakes the anon key.
		// Reject if absent so tests notice if we forget to send it.
		if r.Header.Get("apikey") == "" {
			http.Error(w, "missing apikey", http.StatusBadRequest)
			return
		}

		status, userID := f.respond(token)
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + userID + `","email":"x@y.z"}`))
		}
	}))
}

func TestAuthVerifier_NilWhenUnconfigured(t *testing.T) {
	if v := NewAuthVerifier("", "anon", 0); v != nil {
		t.Error("expected nil when URL empty")
	}
	if v := NewAuthVerifier("https://example", "", 0); v != nil {
		t.Error("expected nil when anon key empty")
	}
}

func TestAuthVerifier_HappyPath(t *testing.T) {
	fa := newFakeAuth()
	srv := fa.server(t)
	defer srv.Close()

	v := NewAuthVerifier(srv.URL, "anon-key", 0)
	if v == nil {
		t.Fatal("expected non-nil verifier")
	}

	id, err := v.VerifyToken(context.Background(), "tok-1")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id != "user-tok-1" {
		t.Errorf("user id = %q, want user-tok-1", id)
	}
}

// Successful results cache; the second call must NOT round-trip.
func TestAuthVerifier_CachesSuccess(t *testing.T) {
	fa := newFakeAuth()
	srv := fa.server(t)
	defer srv.Close()

	v := NewAuthVerifier(srv.URL, "anon-key", 0)
	for i := 0; i < 5; i++ {
		if _, err := v.VerifyToken(context.Background(), "tok-1"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := fa.hits.Load(); got != 1 {
		t.Errorf("expected 1 round-trip across 5 calls (cache hit), got %d", got)
	}
}

// Different tokens cache independently — no cross-user pollution.
func TestAuthVerifier_DifferentTokensRoundTripIndependently(t *testing.T) {
	fa := newFakeAuth()
	srv := fa.server(t)
	defer srv.Close()

	v := NewAuthVerifier(srv.URL, "anon-key", 0)
	a, _ := v.VerifyToken(context.Background(), "alice")
	b, _ := v.VerifyToken(context.Background(), "bob")
	if a == b {
		t.Errorf("alice and bob resolved to same id: %q", a)
	}
	if got := fa.hits.Load(); got != 2 {
		t.Errorf("expected 2 round-trips for 2 distinct tokens, got %d", got)
	}
}

// Failures are NOT cached — a transient upstream 5xx must not lock
// out a real user, and an explicit 401 must surface the next time
// (so a re-issued token works without waiting on TTL).
func TestAuthVerifier_DoesNotCacheFailures(t *testing.T) {
	fa := newFakeAuth()
	srv := fa.server(t)
	defer srv.Close()

	v := NewAuthVerifier(srv.URL, "anon-key", 0)
	if _, err := v.VerifyToken(context.Background(), "bad"); err == nil {
		t.Fatal("expected error for token=bad")
	}
	if _, err := v.VerifyToken(context.Background(), "bad"); err == nil {
		t.Fatal("expected error on retry too")
	}
	if got := fa.hits.Load(); got != 2 {
		t.Errorf("failures should round-trip every time, got %d hits", got)
	}
}

// Cache entries expire after TTL.
func TestAuthVerifier_TTLExpires(t *testing.T) {
	fa := newFakeAuth()
	srv := fa.server(t)
	defer srv.Close()

	v := NewAuthVerifier(srv.URL, "anon-key", 50*time.Millisecond)
	if _, err := v.VerifyToken(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(75 * time.Millisecond)
	if _, err := v.VerifyToken(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	if got := fa.hits.Load(); got != 2 {
		t.Errorf("expected re-fetch after TTL, got %d hits", got)
	}
}

// Invalidate forces a re-fetch on the next call.
func TestAuthVerifier_Invalidate(t *testing.T) {
	fa := newFakeAuth()
	srv := fa.server(t)
	defer srv.Close()

	v := NewAuthVerifier(srv.URL, "anon-key", 0)
	if _, err := v.VerifyToken(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	v.Invalidate("tok")
	if _, err := v.VerifyToken(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	if got := fa.hits.Load(); got != 2 {
		t.Errorf("expected re-fetch after invalidate, got %d hits", got)
	}
}

// Empty bearer is rejected synchronously without a round-trip.
func TestAuthVerifier_EmptyTokenShortCircuits(t *testing.T) {
	fa := newFakeAuth()
	srv := fa.server(t)
	defer srv.Close()

	v := NewAuthVerifier(srv.URL, "anon-key", 0)
	if _, err := v.VerifyToken(context.Background(), ""); err == nil {
		t.Fatal("expected empty-token error")
	}
	if got := fa.hits.Load(); got != 0 {
		t.Errorf("empty token should not round-trip, got %d hits", got)
	}
}
