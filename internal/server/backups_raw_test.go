package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mikrodash/internal/backups"
)

// The `/raw` route is the ONE backup route with no session behind it, so its
// tests are about the token being the entire gate rather than about the body.

func rawServer(t *testing.T, now func() time.Time) *Server {
	t.Helper()
	return &Server{restoreTokens: backups.NewRestoreTokens(now)}
}

func rawGet(s *Server, id, token, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/backups/"+id+"/raw?t="+token, nil)
	r.SetPathValue("id", id)
	r.RemoteAddr = remote + ":54321"
	w := httptest.NewRecorder()
	s.backupRaw(w, r)
	return w
}

// NO TOKEN IS A REFUSAL, not a miss. The distinction matters: a 404 would tell a
// prober that the id was wrong rather than that they had no capability.
func TestRawWithoutATokenIsForbidden(t *testing.T) {
	s := rawServer(t, time.Now)
	w := rawGet(s, "1", "", "10.0.0.2")
	if w.Code != 403 {
		t.Fatalf("status %d, want 403", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "forbidden" {
		t.Errorf("body %v", body)
	}
}

func TestRawWithAnUnknownTokenIsForbidden(t *testing.T) {
	s := rawServer(t, time.Now)
	if w := rawGet(s, "1", "deadbeef", "10.0.0.2"); w.Code != 403 {
		t.Fatalf("status %d, want 403", w.Code)
	}
}

// A TOKEN IS SOURCE-BOUND. One that leaks off the box cannot be redeemed from
// anywhere else, so a valid token from the wrong address is refused.
func TestRawRefusesAValidTokenFromTheWrongSource(t *testing.T) {
	s := rawServer(t, time.Now)
	tok, err := s.restoreTokens.Mint(1, "r1", "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if w := rawGet(s, "1", tok, "10.0.0.99"); w.Code != 403 {
		t.Fatalf("status %d, want 403 — the token is bound to 10.0.0.2", w.Code)
	}
}

// SINGLE USE, AND SPENT ON THE FIRST ATTEMPT WHETHER OR NOT IT SUCCEEDED.
//
// This is the delete-before-validate rule from `restoretoken.go`: a token that
// survived a rejected read would be one an attacker could keep presenting while
// varying the conditions until a combination was accepted.
func TestRawSpendsTheTokenEvenOnAFailedAttempt(t *testing.T) {
	s := rawServer(t, time.Now)
	tok, _ := s.restoreTokens.Mint(1, "r1", "10.0.0.2")

	// Wrong source: refused, and the token is gone.
	rawGet(s, "1", tok, "10.0.0.99")
	if n := s.restoreTokens.Count(); n != 0 {
		t.Fatalf("%d tokens remain after a rejected attempt; it must be spent", n)
	}
	// The right source now fails too, because there is nothing left to redeem.
	if w := rawGet(s, "1", tok, "10.0.0.2"); w.Code != 403 {
		t.Errorf("status %d; a spent token must not be redeemable", w.Code)
	}
}

// A REDEEMED TOKEN WITH NO DATABASE ANSWERS 404, not 500. The row lookup failing
// is "there is nothing to serve", and the router should treat it that way.
func TestRawWithNoDatabaseIsNotFound(t *testing.T) {
	s := rawServer(t, time.Now)
	tok, _ := s.restoreTokens.Mint(7, "r1", "10.0.0.2")
	if w := rawGet(s, "7", tok, "10.0.0.2"); w.Code != 404 {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

// A NON-NUMERIC ID IS NOT FOUND — and it still spends the token, because
// redemption happens before the id is even parsed.
func TestRawWithAJunkIDIsNotFound(t *testing.T) {
	s := rawServer(t, time.Now)
	tok, _ := s.restoreTokens.Mint(1, "r1", "10.0.0.2")
	if w := rawGet(s, "not-a-number", tok, "10.0.0.2"); w.Code != 404 {
		t.Fatalf("status %d, want 404", w.Code)
	}
	if n := s.restoreTokens.Count(); n != 0 {
		t.Error("the token survived a junk id")
	}
}

// EXPIRY IS ENFORCED. The clock is injected so this does not sleep.
func TestRawRefusesAnExpiredToken(t *testing.T) {
	base := time.Now()
	clock := base
	s := rawServer(t, func() time.Time { return clock })
	tok, _ := s.restoreTokens.Mint(1, "r1", "10.0.0.2")

	clock = base.Add(backups.RestoreTokenTTL + time.Second)
	if w := rawGet(s, "1", tok, "10.0.0.2"); w.Code != 403 {
		t.Fatalf("status %d; a token past its TTL must not redeem", w.Code)
	}
}

// remoteIP strips the port. A comparison against "10.0.0.2:54321" would never
// match a token bound to a host, and the failure would read as a token problem
// rather than a parsing one.
func TestRemoteIPStripsThePort(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range []struct{ in, want string }{
		{"10.0.0.2:54321", "10.0.0.2"},
		{"[fe80::1]:443", "fe80::1"},
		{"10.0.0.2", "10.0.0.2"}, // no port at all: used as-is
	} {
		r.RemoteAddr = c.in
		if got := remoteIP(r); got != c.want {
			t.Errorf("remoteIP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The route is registered at the REAL path, not the staging prefix — that is the
// operator's answer to the base-URL question, and it is what makes the URL handed
// to a router correct without any new configuration.
func TestRawIsRegisteredAtTheRealPath(t *testing.T) {
	if backupRawPath != "/api/backups/{id}/raw" {
		t.Errorf("backupRawPath = %q; the router is handed this path and cannot be told a "+
			"staging prefix", backupRawPath)
	}
	body, err := os.ReadFile(filepath.Join("backups_raw.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" {
		t.Fatal("empty source")
	}
}
