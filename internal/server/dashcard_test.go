package server

import "testing"

// TestDashCardPageResolvesEveryRoom pins the table the RBAC gate reads.
//
// The values are checked against the LIVE registry by tools/grid-tables.js,
// which can run Node; this is the Go-side half — that every room the grid can
// ask for is present, and that an unknown key falls back to the dashboard rather
// than to the empty string, which would gate on a page nobody has.
func TestDashCardPageResolvesEveryRoom(t *testing.T) {
	// The eight rooms CARD_ROOMS can produce.
	for _, room := range []string{
		"firewall", "logs", "vpn", "diagnostics",
		"connections", "wireless", "interfaces", "dhcp",
	} {
		if got := dashCardPage(room); got == "" {
			t.Errorf("dashCardPage(%q) is empty — that gates the card on a page nobody has", room)
		}
	}
	if got := dashCardPage("somethingelse"); got != "home" {
		t.Errorf("an unknown card key resolved to %q, want home", got)
	}
	// diagnostics is neither a page nor a collector in the live registry.
	if got := dashCardPage("diagnostics"); got != "home" {
		t.Errorf("dashCardPage(diagnostics) = %q, want home", got)
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
