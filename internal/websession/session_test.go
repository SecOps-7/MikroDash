package websession

// Judged against `testdata/websession-cases.json`, which is what the LIVE
// sessionStore.js answered — not against what this file's author expected.

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

type corpus struct {
	FixedNow int64 `json:"fixedNow"`
	Parse    []struct {
		Name              string            `json:"name"`
		Header            *string           `json:"header"`
		HeaderIsNull      bool              `json:"headerIsNull"`
		HeaderIsUndefined bool              `json:"headerIsUndefined"`
		Out               map[string]string `json:"out"`
	} `json:"parse"`
	Cookie []struct {
		Name       string `json:"name"`
		TimeoutMs  int64  `json:"timeoutMs"`
		Infinite   bool   `json:"infinite"`
		Plain      string `json:"plain"`
		ForceHTTPS string `json:"forceHttps"`
	} `json:"cookie"`
	Clear struct {
		Plain      string `json:"plain"`
		ForceHTTPS string `json:"forceHttps"`
	} `json:"clear"`
	Expiry []struct {
		Name            string `json:"name"`
		TimeoutMs       int64  `json:"timeoutMs"`
		Infinite        bool   `json:"infinite"`
		ExpiresAt       *int64 `json:"expiresAt"`
		TokenLength     int    `json:"tokenLength"`
		TokenIsHex      bool   `json:"tokenIsHex"`
		LiveImmediately bool   `json:"liveImmediately"`
	} `json:"expiry"`
}

func load(t *testing.T) corpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/websession-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Parse) == 0 || len(c.Cookie) == 0 || len(c.Expiry) == 0 {
		t.Fatal("the corpus is empty in at least one section -- this test would pass against " +
			"nothing")
	}
	return c
}

// pinned returns a store whose clock is the corpus's, so the Max-Age arithmetic
// is compared rather than approximated.
func pinned(c corpus) *Store {
	s := New()
	s.now = func() time.Time { return time.UnixMilli(c.FixedNow) }
	return s
}

func TestParseCookieHeaderMatchesLive(t *testing.T) {
	c := load(t)
	for _, tc := range c.Parse {
		t.Run(tc.Name, func(t *testing.T) {
			// A null or undefined header cannot arrive through Go's header API;
			// the live guard treats both as "no cookies", and so does the empty
			// string this passes instead.
			in := ""
			if tc.Header != nil {
				in = *tc.Header
			}
			got := ParseCookieHeader(in)
			if len(got) != len(tc.Out) {
				t.Fatalf("%d pairs, live %d\n  got  %v\n  live %v",
					len(got), len(tc.Out), got, tc.Out)
			}
			for k, want := range tc.Out {
				if got[k] != want {
					t.Errorf("%q = %q, live %q", k, got[k], want)
				}
			}
		})
	}
}

func TestBuildCookieHeaderMatchesLive(t *testing.T) {
	c := load(t)
	s := pinned(c)
	for _, tc := range c.Cookie {
		t.Run(tc.Name, func(t *testing.T) {
			exp := NeverExpires
			if !tc.Infinite {
				exp = time.UnixMilli(c.FixedNow + tc.TimeoutMs)
			}
			if got := s.BuildCookieHeader("TOKEN", exp, false); got != tc.Plain {
				t.Errorf("plain:\n  got  %q\n  live %q", got, tc.Plain)
			}
			if got := s.BuildCookieHeader("TOKEN", exp, true); got != tc.ForceHTTPS {
				t.Errorf("FORCE_HTTPS:\n  got  %q\n  live %q", got, tc.ForceHTTPS)
			}
		})
	}
	if got := ClearCookieHeader(false); got != c.Clear.Plain {
		t.Errorf("clear plain:\n  got  %q\n  live %q", got, c.Clear.Plain)
	}
	if got := ClearCookieHeader(true); got != c.Clear.ForceHTTPS {
		t.Errorf("clear FORCE_HTTPS:\n  got  %q\n  live %q", got, c.Clear.ForceHTTPS)
	}
}

func TestExpiryMatchesLive(t *testing.T) {
	c := load(t)
	for _, tc := range c.Expiry {
		t.Run(tc.Name, func(t *testing.T) {
			s := pinned(c)
			sess, err := s.Create("u1", "someone", "admin",
				time.Duration(tc.TimeoutMs)*time.Millisecond, []string{"r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := sess.ExpiresAt.Equal(NeverExpires); got != tc.Infinite {
				t.Errorf("never-expires = %v, live %v. A non-positive timeout means NEVER; a "+
					"port reading 0 as an immediate expiry signs every user out the instant "+
					"they sign in", got, tc.Infinite)
			}
			if !tc.Infinite && sess.ExpiresAt.UnixMilli() != *tc.ExpiresAt {
				t.Errorf("expiresAt %d, live %d", sess.ExpiresAt.UnixMilli(), *tc.ExpiresAt)
			}
			if len(sess.Token) != tc.TokenLength {
				t.Errorf("token is %d characters, live %d", len(sess.Token), tc.TokenLength)
			}
			if live := s.Get(sess.Token) != nil; live != tc.LiveImmediately {
				t.Errorf("live immediately = %v, live %v -- the check is STRICTLY after, so a "+
					"session expiring exactly now is still valid", live, tc.LiveImmediately)
			}
		})
	}
}

// TestTokensAreUnguessableAndUnique. Deliberately NOT in the corpus: the token
// is 32 random bytes and there is nothing to compare against the live one. The
// properties that matter are asserted directly, which is the right tool for a
// value that is different every time by design.
func TestTokensAreUnguessableAndUnique(t *testing.T) {
	s := New()
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		sess, err := s.Create("u1", "someone", "admin", 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(sess.Token) != 64 {
			t.Fatalf("token is %d characters, want 64 (32 bytes of hex)", len(sess.Token))
		}
		for _, ch := range sess.Token {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				t.Fatalf("token is not lower-case hex: %q", sess.Token)
			}
		}
		if seen[sess.Token] {
			t.Fatalf("a token repeated after %d draws -- the source is not random", i)
		}
		seen[sess.Token] = true
	}
	if s.Count() != 500 {
		t.Errorf("%d sessions stored, want 500", s.Count())
	}
}

// TestExpiredSessionsGoOnAccessAndOnPrune.
func TestExpiredSessionsGoOnAccessAndOnPrune(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	s := New()
	s.now = func() time.Time { return now }

	short, _ := s.Create("u1", "a", "admin", time.Minute, nil)
	forever, _ := s.Create("u1", "a", "admin", 0, nil)

	now = now.Add(time.Minute) // EXACTLY at the expiry: still live, `>` is strict.
	if s.Get(short.Token) == nil {
		t.Error("a session expiring exactly now was already gone -- the comparison is strict")
	}
	now = now.Add(time.Millisecond)
	if s.Get(short.Token) != nil {
		t.Error("an expired session was returned")
	}
	if s.Count() != 1 {
		t.Errorf("%d sessions after an expired one was read, want 1 -- Get removes it", s.Count())
	}
	if s.Get(forever.Token) == nil {
		t.Error("a never-expiring session expired")
	}

	// A never-expiring session must survive a prune at the end of time.
	now = time.UnixMilli(math.MaxInt64 / int64(time.Millisecond) / 2)
	if n := s.PruneExpired(); n != 0 {
		t.Errorf("prune removed %d, want 0", n)
	}
	if s.Get(forever.Token) == nil {
		t.Error("a never-expiring session was pruned")
	}
}

// TestSignOutEverywhereElseKeepsThisOne.
func TestSignOutEverywhereElseKeepsThisOne(t *testing.T) {
	s := New()
	var mine string
	for i := 0; i < 4; i++ {
		sess, _ := s.Create("u1", "a", "admin", 0, nil)
		if i == 0 {
			mine = sess.Token
		}
	}
	other, _ := s.Create("u2", "b", "viewer", 0, nil)

	removed := s.DeleteForUser("u1", mine)
	if len(removed) != 3 {
		t.Errorf("removed %d of u1's other sessions, want 3", len(removed))
	}
	if s.Get(mine) == nil {
		t.Error("the requesting session was signed out too")
	}
	if s.Get(other.Token) == nil {
		t.Error("ANOTHER USER was signed out -- DeleteForUser must key on the user id")
	}
	if got := len(s.ForUser("u1")); got != 1 {
		t.Errorf("u1 has %d sessions left, want 1", got)
	}
	// An empty user id removes nothing, rather than everything.
	if n := len(s.DeleteForUser("", "")); n != 0 {
		t.Errorf("an empty user id removed %d sessions", n)
	}
	if s.Count() != 2 {
		t.Errorf("%d sessions remain, want 2", s.Count())
	}
}
