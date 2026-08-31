package backups

import (
	"encoding/hex"
	"testing"
	"time"
)

// Each constraint on a restore token gets its own test, because each is
// separately load-bearing: this token is the ENTIRE gate on the one backup route
// with no session behind it.

func tokensAt(start time.Time) (*RestoreTokens, *time.Time) {
	now := start
	t := NewRestoreTokens(func() time.Time { return now })
	return t, &now
}

const host = "10.0.0.2"

func mint(t *testing.T, rt *RestoreTokens) string {
	t.Helper()
	tok, err := rt.Mint(7, "router-a", host)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestTokenIs32RandomBytes(t *testing.T) {
	rt, _ := tokensAt(time.Unix(1787000000, 0))
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok := mint(t, rt)
		if seen[tok] {
			t.Fatal("Mint repeated a token")
		}
		seen[tok] = true
		b, err := hex.DecodeString(tok)
		if err != nil || len(b) != 32 {
			t.Fatalf("token %q is not 32 hex-encoded bytes: %v", tok, err)
		}
	}
}

func TestAGoodTokenRedeemsOnce(t *testing.T) {
	rt, _ := tokensAt(time.Unix(1787000000, 0))
	tok := mint(t, rt)

	v := rt.Redeem(tok, host)
	if !v.OK {
		t.Fatalf("a fresh token from the right host was refused: %s", v.Reason)
	}
	if v.Entry.BackupID != 7 || v.Entry.RouterID != "router-a" {
		t.Errorf("entry = %+v", v.Entry)
	}

	// SINGLE USE.
	if again := rt.Redeem(tok, host); again.OK {
		t.Fatal("a token was redeemed twice")
	}
}

// TestARejectedTokenIsStillConsumed is the delete-before-validate rule, and the
// reason it is worth a test of its own: a token that survives a rejected read
// can be presented again under different conditions — a different source, a
// later moment — until one combination is accepted.
func TestARejectedTokenIsStillConsumed(t *testing.T) {
	for _, tc := range []struct {
		name, badIP string
		advance     time.Duration
	}{
		{"wrong source", "198.51.100.9", 0},
		{"expired", host, RestoreTokenTTL + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, now := tokensAt(time.Unix(1787000000, 0))
			tok := mint(t, rt)
			*now = now.Add(tc.advance)

			if v := rt.Redeem(tok, tc.badIP); v.OK {
				t.Fatal("the bad attempt was accepted")
			}
			// Now retry under GOOD conditions. It must still fail.
			*now = time.Unix(1787000000, 0)
			if v := rt.Redeem(tok, host); v.OK {
				t.Fatal("a token rejected once was accepted on a second attempt " +
					"under better conditions — an attacker can vary the conditions")
			}
		})
	}
}

func TestExpiryIsEnforced(t *testing.T) {
	rt, now := tokensAt(time.Unix(1787000000, 0))

	tok := mint(t, rt)
	*now = now.Add(RestoreTokenTTL - time.Millisecond)
	if v := rt.Redeem(tok, host); !v.OK {
		t.Errorf("refused just inside the window: %s", v.Reason)
	}

	tok = mint(t, rt)
	*now = now.Add(RestoreTokenTTL + time.Millisecond)
	if v := rt.Redeem(tok, host); v.OK || v.Reason != "expired" {
		t.Errorf("accepted past the window: %+v", v)
	}
}

func TestSourceIsCheckedAndIPv4MappedFormIsAccepted(t *testing.T) {
	rt, _ := tokensAt(time.Unix(1787000000, 0))

	if v := rt.Redeem(mint(t, rt), "198.51.100.9"); v.OK || v.Reason != "wrong-source" {
		t.Errorf("a token from the wrong address was accepted: %+v", v)
	}
	// A dual-stack listener reports an IPv4 peer as ::ffff:10.0.0.2. Without the
	// strip, EVERY restore from an IPv4 router would be refused.
	if v := rt.Redeem(mint(t, rt), "::ffff:"+host); !v.OK {
		t.Errorf("the IPv4-mapped form of the right host was refused: %s", v.Reason)
	}
}

func TestUnknownTokenIsRefused(t *testing.T) {
	rt, _ := tokensAt(time.Unix(1787000000, 0))
	for _, bad := range []string{"", "not-a-token", "00", hex.EncodeToString(make([]byte, 32))} {
		if v := rt.Redeem(bad, host); v.OK || v.Reason != "unknown-token" {
			t.Errorf("Redeem(%q) = %+v", bad, v)
		}
	}
}

// TestExpiredTokensDoNotAccumulate — the original arms a timer per token so a
// failed restore never leaves a live one behind. This sweeps on mint instead,
// which reaches the same state without a goroutine per token.
func TestExpiredTokensDoNotAccumulate(t *testing.T) {
	rt, now := tokensAt(time.Unix(1787000000, 0))
	for i := 0; i < 50; i++ {
		mint(t, rt)
		*now = now.Add(10 * time.Second)
	}
	if rt.Count() > 13 {
		t.Errorf("%d tokens live; expired ones are accumulating", rt.Count())
	}
	*now = now.Add(RestoreTokenTTL * 2)
	mint(t, rt)
	if rt.Count() != 1 {
		t.Errorf("%d tokens live after a long gap, want 1", rt.Count())
	}
}

// ── The row is the authority ────────────────────────────────────────────────

func TestServableChecksTheROWNotTheRequest(t *testing.T) {
	rt, _ := tokensAt(time.Unix(1787000000, 0))
	v := rt.Redeem(mint(t, rt), host) // bound to backup 7 on router-a
	if !v.OK {
		t.Fatal(v.Reason)
	}
	pruned := int64(1787000000000)

	for _, tc := range []struct {
		name     string
		id       int64
		routerID string
		stem     string
		prunedAt *int64
		want     bool
	}{
		{"the row it was minted for", 7, "router-a", "2026-01-01T000000", nil, true},
		{"a different backup id", 8, "router-a", "2026-01-01T000000", nil, false},
		{"THE SAME ID ON ANOTHER ROUTER", 7, "router-b", "2026-01-01T000000", nil, false},
		{"a row that stored nothing", 7, "router-a", "", nil, false},
		{"a pruned row", 7, "router-a", "2026-01-01T000000", &pruned, false},
	} {
		if got := BackupServable(v, tc.id, tc.routerID, tc.stem, tc.prunedAt); got != tc.want {
			t.Errorf("%s: servable = %v, want %v", tc.name, got, tc.want)
		}
	}

	// And a refused verdict serves nothing, whatever the row says.
	if BackupServable(RestoreVerdict{Reason: "expired"}, 7, "router-a", "s", nil) {
		t.Error("a refused verdict served a row")
	}
}
