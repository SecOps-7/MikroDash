package routers

import "testing"

// TestDefaultIfForPrecedence — the router's own choice, then the install-wide
// setting, then the fallback.
//
// It is exported for the background recorders, and that is the point: they took
// `r.DefaultIf` RAW. A router with none gave the traffic collector an empty
// interface list, `syncStream` opens nothing for an empty list, and so the
// pools — the half that runs when nobody is watching — recorded no traffic at
// all. Meanwhile the page displayed "ether1" and the interactive session
// streamed "WAN1". Three answers to one question, two of them invisible.
// Issue #126.
func TestDefaultIfForPrecedence(t *testing.T) {
	cases := []struct{ router, global, want string }{
		{"sfp1", "ether5", "sfp1"},  // the router's own choice wins
		{"", "ether5", "ether5"},    // then the install-wide setting
		{"", "", fallbackDefaultIf}, // then the fallback
		{"sfp1", "", "sfp1"},        // no setting is not a reason to ignore the router
	}
	for _, c := range cases {
		if got := DefaultIfFor(c.router, c.global); got != c.want {
			t.Errorf("DefaultIfFor(%q, %q) = %q, want %q", c.router, c.global, got, c.want)
		}
	}
}

// TestTheFallbackIsNeverEmpty is the property the recorders actually depend on:
// an empty answer means an empty interface list, which means no stream, which
// means no history and no WAN badge — silently.
func TestTheFallbackIsNeverEmpty(t *testing.T) {
	if DefaultIfFor("", "") == "" {
		t.Error("resolved to an empty interface; the traffic stream opens nothing " +
			"for an empty list and records nothing, with no error anywhere")
	}
}
