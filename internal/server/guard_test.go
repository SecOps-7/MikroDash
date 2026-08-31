package server

import (
	"testing"

	"mikrodash/internal/resource"
)

// Guards are ported just-in-time with the page that needs them, so "declared
// but not ported" is a state this server is in routinely. What must never
// happen is a write proceeding as though the resource declared no guard at all.
//
// This asserts the CURRENT set, so porting a guard fails this test and forces
// the entry to be added deliberately rather than discovered later.
func TestPortedGuardsAreDeclaredExplicitly(t *testing.T) {
	want := map[string]bool{"selfPath": true, "fwGuard": true, "wifiInherit": true,
		"capsmanPush": true}
	if len(portedGuards) != len(want) {
		t.Errorf("portedGuards = %v; update this test when a guard is ported", portedGuards)
	}
	for k := range want {
		if !portedGuards[k] {
			t.Errorf("%s is expected to be ported but is not listed", k)
		}
	}
}

// Every guard any registered resource declares must either be ported, or be
// known to refuse. A resource whose guard is neither would write unguarded.
func TestEveryDeclaredGuardIsPortedOrRefuses(t *testing.T) {
	// ── THE REGISTRY, NOT A TYPED LIST ────────────────────────────────────
	//
	// This enumerated SIXTEEN resources by name against a registry of TWENTY.
	// The four it never looked at — DHCPLease, Route, Route6, WgPeer — were
	// unchecked for as long as the list had been typed out, and were harmless
	// only by coincidence: those four declare no guard today. Give one an
	// unported guard and its writes are refused at runtime, correctly, with no
	// test saying why.
	//
	// `resource.All()` is the registry itself, so a resource added tomorrow is
	// checked tomorrow. Same lesson as the `endpoint-audit` incident CLAUDE.md
	// records: a checker driven by a hand-typed list drifts from what it checks.
	all := resource.All()
	if len(all) == 0 {
		t.Fatal("resource.All() is empty — this test is measuring nothing")
	}
	for _, res := range all {
		for _, kind := range res.Guard {
			if !portedGuards[kind] {
				// Not a failure — this is the expected state for a resource
				// whose page is ported ahead of its guard. It is logged so the
				// list is visible in the test output rather than inferred.
				t.Logf("%s declares %q, which is not ported: its writes are REFUSED", res.Key, kind)
			}
		}
	}
	// The ones that should work right now.
	for _, res := range []*resource.Resource{resource.Bridge, resource.BridgePort, resource.Vlan} {
		for _, kind := range res.Guard {
			if !portedGuards[kind] {
				t.Errorf("%s declares %q — this page's write path is live and needs it", res.Key, kind)
			}
		}
	}
}
