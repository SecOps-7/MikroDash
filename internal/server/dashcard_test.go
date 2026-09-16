package server

import (
	"testing"

	"mikrodash/internal/dashcards"
)

// TestDashCardPageResolvesEveryRoom pins the table the RBAC gate reads.
//
// ── THE LIST WAS HARDCODED, AND ITS CROSS-CHECK DID NOT EXIST ───────────────
//
// This named its eight rooms in a literal and said the values were checked
// against the live registry by a JavaScript generator under tools/. That
// generator is not in the repository, so nothing tied this list to the cards the
// grid can actually ask for: a ninth room would have been added with this test
// still passing, and a deleted one would have left a literal describing nothing.
//
// (Its filename is deliberately not spelled as a path here. The citation check
// cannot tell a location from the name of something absent.)
//
// It reads the declaration now, so the list cannot drift from it.
func TestDashCardPageResolvesEveryRoom(t *testing.T) {
	rooms := dashcards.Rooms()
	if len(rooms) < 5 {
		t.Fatalf("only %d card rooms declared — the table is truncated", len(rooms))
	}
	for _, room := range rooms {
		if got := dashCardPage(room); got == "" {
			t.Errorf("dashCardPage(%q) is empty — that gates the card on a page nobody has", room)
		}
		// ── THE KEY SHAPE, AT BUILD TIME RATHER THAN AT RUNTIME ──────────
		//
		// `dashCardFocus` returns early on a key this regexp refuses, silently.
		// A card declared with a hyphen or an over-long room would therefore
		// never subscribe, and nothing would say so — the card would simply
		// never receive a payload.
		if !dashCardKeyRe.MatchString(room) {
			t.Errorf("card room %q does not match %s — dashCardFocus drops it in silence, "+
				"so the card would never subscribe", room, dashCardKeyRe)
		}
	}
	if got := dashCardPage("somethingelse"); got != "dashboard" {
		t.Errorf("an unknown card key resolved to %q, want dashboard", got)
	}
	// diagnostics is neither a page nor a collector in the live registry.
	if got := dashCardPage("diagnostics"); got != "dashboard" {
		t.Errorf("dashCardPage(diagnostics) = %q, want dashboard", got)
	}
}

// TestDashCardKeyIsValidatedNotEscaped — the key becomes part of a room name,
// and room names are how payloads are addressed.
func TestDashCardKeyIsValidatedNotEscaped(t *testing.T) {
	good := []string{"vpn", "firewall", "diagnostics", "logs"}
	bad := []string{
		"", "a", "A", "vpn1", "vpn-card", "vpn card", "../other", "vpn:focus",
		"averyveryverylongcardkeyname", "VPN", "vpn\n",
	}
	for _, k := range good {
		if !dashCardKeyRe.MatchString(k) {
			t.Errorf("%q was refused and should be accepted", k)
		}
	}
	for _, k := range bad {
		if dashCardKeyRe.MatchString(k) {
			t.Errorf("%q was accepted and should be refused", k)
		}
	}
}
