package resource

import "testing"

// addedSinceNode names the fields this port deliberately declares beyond the
// Node module's, per resource.
//
// ── WHY A LEDGER AND NOT A REWRITTEN RECORDING ─────────────────────────────
//
// Two frozen recordings pin this package against the app it was ported from:
// `src/routeros/resources.js` (the field declarations, read by
// `options_test.go`) and `testdata/res-preview-cases.json` (the RouterOS
// command a write renders, replayed by `preview_test.go`). NEITHER CAN BE
// REGENERATED — the generator drove the live Node module and that module is
// gone — so editing one by hand converts it from "the port matches what
// shipped" into "the port matches what somebody typed", which is the whole
// value of both files.
//
// So an addition is RECORDED here, once, and both gates read it: the field is
// excused from the count and its argument is stripped before the command is
// compared. Everything the Node app did declare must still match exactly.
// `internal/collect/fixture_test.go` carries the same idiom for the payload
// goldens, and its header explains the reasoning at length.
//
// ── AND IT FAILS IN BOTH DIRECTIONS ────────────────────────────────────────
//
// An unrecorded addition fails the gates. A recorded name that is NOT a field
// of that resource, or that the Node source DOES declare, fails below — so the
// list cannot outlive the change it describes and cannot excuse a field that
// was always there.
var addedSinceNode = map[string][]string{
	// The WireGuard peer grew its client-side half on 2026-09-20, when the peers
	// moved to their own page and a peer's configuration became something this
	// app can hand to a phone. RouterOS has stored these since 7.12 and the Node
	// app never asked for them.
	//
	//   name                  7.15+, readable by the collector long before it
	//                         was editable here
	//   privateKey            the PEER's key, which is what lets a client config
	//                         be printed. TypeSecret, so it never reaches a form
	//                         and never appears in a rendered command
	//   client*               what the router hands the client: address, DNS,
	//                         endpoint, keepalive, allowed addresses
	//   responder             7.17+ (is-responder in 7.15-7.16.2). Display only
	//                         — see the field's own note for why it is not
	//                         writable
	"wgPeer": {
		"name", "privateKey", "clientAddress", "clientDns", "clientEndpoint",
		"clientKeepalive", "clientAllowedAddress", "responder",
	},
}

// addedFor reports the recorded additions for a resource.
func addedFor(key string) []string { return addedSinceNode[key] }

// TestAddedSinceNodeNamesRealFields is the ledger's own check: every recorded
// name must be a field this port actually declares. A typo would otherwise
// silently widen the allowance the two parity gates give.
//
// The other direction — a recorded name the Node source also declares — is
// checked in options_test.go, where the Node declarations are parsed.
func TestAddedSinceNodeNamesRealFields(t *testing.T) {
	if len(addedSinceNode) == 0 {
		t.Skip("nothing recorded")
	}
	for key, names := range addedSinceNode {
		res := ByKey(key)
		if res == nil {
			t.Errorf("addedSinceNode names resource %q, which is not in the registry", key)
			continue
		}
		for _, n := range names {
			if res.FieldByName(n) == nil {
				t.Errorf("addedSinceNode records %s.%s, which is not a field of that "+
					"resource. Delete the entry rather than leaving an excuse behind.", key, n)
			}
		}
	}
}
