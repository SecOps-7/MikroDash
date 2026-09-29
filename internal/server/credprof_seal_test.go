package server

import (
	"strings"
	"testing"
)

// TestACredentialProfilePasswordSurvivesTheRoundTrip.
//
// ── THE BUG THIS EXISTS FOR, WHICH A LIVE WRITE FOUND ──────────────────────
//
// The password was first sealed with `sealChannelConfig`, which marshals its
// argument to JSON before encrypting because what it seals is a config OBJECT.
// A password is a bare string, and `json.Marshal("hunter2")` is `"hunter2"`
// WITH THE QUOTE CHARACTERS. So every provisioned account got a password two
// characters longer than the operator typed.
//
// ── AND WHY THE WHOLE SUITE WAS GREEN ABOUT IT ─────────────────────────────
//
// Every other test in this feature builds a `credprof.Spec` directly and hands
// it to the applier. Not one of them crossed the seal/open seam, so the account
// appeared on the router, in the right group, carrying the right ownership
// marker, and the link read `applied` — with the wrong password. Nothing short
// of signing in to the router as that account could tell the difference, and
// that is what found it.
//
// The mechanism generalises past this feature: a round trip through ONE
// implementation agrees with itself whatever it wrote.
func TestACredentialProfilePasswordSurvivesTheRoundTrip(t *testing.T) {
	s, _, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	// ── EVERY CHARACTER JSON WOULD TOUCH ─────────────────────────────────────
	//
	// A quote and a backslash are what the original bug added; the rest are
	// what an escaping round trip mangles rather than lengthens.
	for _, pw := range []string{
		"Liv3-T3st-Pass",          // the plain case, which is what shipped wrong
		`quote"inside`,            // JSON would escape it
		`back\slash`,              // and this
		"new\nline\ttab",          // control characters
		"unicode-ß-ñ-日本",          // multi-byte
		`{"sealed":"not really"}`, // a password shaped like the envelope itself
		strings.Repeat("x", 1024), // long
		" leading and trailing  ", // whitespace must not be trimmed
	} {
		sealed, err := s.sealCredSecret(pw)
		if err != nil {
			t.Fatalf("sealing %q: %v", pw, err)
		}
		// THE SEALED FORM IS NOT THE PLAINTEXT. Without this the test passes for
		// an implementation that stores the password as it was given.
		if strings.Contains(sealed, pw) {
			t.Errorf("the sealed form contains the password in the clear: %q", sealed)
		}
		if got := s.openCredSecret(sealed); got != pw {
			t.Errorf("a password did not survive the round trip:\n  typed %q\n  got   %q", pw, got)
		}
	}

	// ── AND THE FAILURE MODES ARE EMPTY, NEVER A GUESS ───────────────────────
	//
	// "" is what every caller treats as "refuse to apply this profile", so
	// anything unreadable has to produce exactly that rather than a partial or
	// a plausible-looking string.
	for _, bad := range []string{"", "not json", `{}`, `{"sealed":""}`, `{"sealed":"not-ciphertext"}`} {
		if got := s.openCredSecret(bad); got != "" {
			t.Errorf("unreadable stored value %q opened as %q, which would be written to a "+
				"router as a password", bad, got)
		}
	}
}

// TestTheOldSealingWouldHaveBeenCaught is the control for the test above: it
// demonstrates that `sealChannelConfig`, the helper this feature first used,
// really does change a bare string — so the assertion above is about a real
// difference rather than a distinction with no consequence.
func TestTheOldSealingWouldHaveBeenCaught(t *testing.T) {
	s, _, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	const pw = "Liv3-T3st-Pass"
	sealed, err := s.sealChannelConfig(pw)
	if err != nil {
		t.Fatal(err)
	}
	got := s.openChannelConfig(sealed)
	if got == pw {
		t.Skip("sealChannelConfig no longer changes a bare string; this control has " +
			"outlived the bug it describes and the comment above should be re-read")
	}
	if got != `"`+pw+`"` {
		t.Errorf("sealChannelConfig round-trips a bare string as %q; the bug was that it "+
			"adds JSON quoting, and if it now does something else the sibling test above "+
			"may be pinning the wrong thing", got)
	}
}
