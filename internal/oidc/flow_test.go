package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── safeNext, AGAINST THE CORPUS THE BROWSER'S TWIN ALSO READS ─────────────
//
// The rule is implemented twice, in two languages, and the only thing keeping
// them in step is that both are driven from one file. A Go-only table would
// prove this half correct and say nothing about whether the page agrees.

type nextCase struct {
	In   string `json:"in"`
	Want string `json:"want"`
	Why  string `json:"why"`
}

func loadNextCases(t *testing.T) []nextCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "safenext-cases.json"))
	if err != nil {
		t.Fatalf("reading the corpus: %v", err)
	}
	var cases []nextCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	// A FLOOR, so an emptied or renamed corpus fails rather than passing
	// vacuously. It is the shape of check this repo uses wherever a scan could
	// find nothing and call that success.
	if len(cases) < 10 {
		t.Fatalf("the corpus has %d cases; it is supposed to be the specification", len(cases))
	}
	return cases
}

func TestSafeNextMatchesTheCorpus(t *testing.T) {
	for _, c := range loadNextCases(t) {
		if got := safeNext(c.In); got != c.Want {
			t.Errorf("safeNext(%q) = %q, want %q — %s", c.In, got, c.Want, c.Why)
		}
	}
}

// THE CORPUS MUST CONTAIN THE CASES THAT MATTER, not just any ten.
//
// A corpus is a specification only while it still covers the attacks; one that
// drifted into ten happy paths would pass every run and prove nothing. So the
// three shapes that would actually hurt are named here, and a corpus that loses
// one fails.
func TestTheSafeNextCorpusCoversTheAttacks(t *testing.T) {
	cases := loadNextCases(t)
	need := map[string]bool{
		"protocol-relative": false,
		"backslash":         false,
		"control character": false,
	}
	for _, c := range cases {
		switch {
		case c.In == "//evil.example/x":
			need["protocol-relative"] = true
		case len(c.In) > 1 && c.In[0] == '/' && c.In[1] == '\\':
			need["backslash"] = true
		case containsControl(c.In):
			need["control character"] = true
		}
	}
	for what, covered := range need {
		if !covered {
			t.Errorf("the corpus no longer covers the %s case", what)
		}
	}
}

func containsControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			return true
		}
	}
	return false
}

// ── THE PENDING LOGINS ──────────────────────────────────────────────────────

func TestAPendingLoginIsConsumedExactlyOnce(t *testing.T) {
	p := NewPending(func() time.Time { return testNow })
	id, rec, err := p.Create("prov-1", "/logs")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Consume(id, rec.State); !ok {
		t.Fatal("a fresh pending login could not be consumed")
	}
	// A REPLAY OF A CALLBACK THAT WORKED must be indistinguishable from one
	// that never did.
	if _, ok := p.Consume(id, rec.State); ok {
		t.Error("the same pending login was consumed twice; a captured callback URL would replay")
	}
}

// ── THE WRONG STATE BURNS THE RECORD ───────────────────────────────────────
//
// The record is deleted before the state is compared. Deleting only on success
// would leave it alive for somebody holding a valid id to keep guessing against.
func TestAWrongStateBurnsThePendingLogin(t *testing.T) {
	p := NewPending(func() time.Time { return testNow })
	id, rec, err := p.Create("prov-1", "/")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Consume(id, "not-the-state"); ok {
		t.Fatal("a wrong state was accepted")
	}
	if _, ok := p.Consume(id, rec.State); ok {
		t.Error("the right state still worked after a wrong one; an attacker holding the id " +
			"could keep guessing until they hit it")
	}
}

func TestAPendingLoginExpires(t *testing.T) {
	now := testNow
	p := NewPending(func() time.Time { return now })
	id, rec, err := p.Create("prov-1", "/")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(pendingTTL + time.Second)
	if _, ok := p.Consume(id, rec.State); ok {
		t.Error("a login older than the TTL was still accepted")
	}
	// THE CONTROL: inside the TTL it works, so the check above is about the
	// clock and not about Consume being broken.
	now = testNow
	id2, rec2, _ := p.Create("prov-1", "/")
	now = now.Add(pendingTTL - time.Second)
	if _, ok := p.Consume(id2, rec2.State); !ok {
		t.Error("a login inside the TTL was refused")
	}
}

// The start endpoint is unauthenticated, so the map must not grow without limit.
func TestThePendingMapIsCapped(t *testing.T) {
	now := testNow
	p := NewPending(func() time.Time { return now })
	for i := 0; i < maxPending*3; i++ {
		now = now.Add(time.Millisecond)
		if _, _, err := p.Create("prov-1", "/"); err != nil {
			t.Fatal(err)
		}
	}
	if n := p.Len(); n > maxPending {
		t.Errorf("the pending map holds %d, over the %d cap; anyone who can reach the login "+
			"page could grow it without limit", n, maxPending)
	}
}

func TestExpiredLoginsArePruned(t *testing.T) {
	now := testNow
	p := NewPending(func() time.Time { return now })
	for i := 0; i < 5; i++ {
		if _, _, err := p.Create("prov-1", "/"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(pendingTTL + time.Second)
	p.PruneExpired()
	if n := p.Len(); n != 0 {
		t.Errorf("%d expired logins survived a prune", n)
	}
}

// ── PKCE ────────────────────────────────────────────────────────────────────

// S256, never plain. `plain` makes the challenge equal the verifier, so anyone
// who saw the authorization request can complete the exchange.
func TestTheChallengeIsTheSHA256OfTheVerifier(t *testing.T) {
	p := NewPending(func() time.Time { return testNow })
	_, rec, err := p.Create("prov-1", "/")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Challenge() == rec.Verifier {
		t.Fatal("the challenge equals the verifier; that is plain PKCE, which is no PKCE at all")
	}
	sum := sha256.Sum256([]byte(rec.Verifier))
	if wantCh := base64.RawURLEncoding.EncodeToString(sum[:]); rec.Challenge() != wantCh {
		t.Errorf("the challenge is %q, want the base64url SHA-256 of the verifier", rec.Challenge())
	}
}

// Every value guarding a login is independent and unguessable. A generator that
// returned the same string twice, or reused one value for two purposes, would
// defeat the binding without changing any other test.
func TestEveryLoginSecretIsDistinct(t *testing.T) {
	p := NewPending(func() time.Time { return testNow })
	seen := map[string]string{}
	for i := 0; i < 20; i++ {
		id, rec, err := p.Create("prov-1", "/")
		if err != nil {
			t.Fatal(err)
		}
		for what, v := range map[string]string{
			"id": id, "state": rec.State, "nonce": rec.Nonce, "verifier": rec.Verifier,
		} {
			if v == "" {
				t.Fatalf("the %s was empty", what)
			}
			if prev, dup := seen[v]; dup {
				t.Fatalf("the %s repeated a value already used as the %s", what, prev)
			}
			seen[v] = what
		}
	}
}
