package pages

import "testing"

// The rename ledger has to fail in BOTH directions, which is this repository's
// standing rule for a ledger: an entry pointing nowhere is as bad as a missing
// one. Both halves below have a specific failure they prevent.
func TestRenamedPointsOnlyAtRealPages(t *testing.T) {
	for old, now := range Renamed {
		// A value that is not a current key would move a grant onto a page that
		// does not exist -- turning a stranded grant into a differently stranded
		// grant, and looking fixed while it did.
		if !Has(now) {
			t.Errorf("Renamed[%q] = %q, which is not a page key", old, now)
		}
		// A KEY that is also a current key would rename a live page's grants
		// out from under it. That is the reverse of the bug, and worse: it takes
		// away access somebody has right now.
		if Has(old) {
			t.Errorf("Renamed has key %q, but %q is a CURRENT page key", old, old)
		}
		if old == now {
			t.Errorf("Renamed[%q] maps to itself", old)
		}
	}
}

// A chain (a -> b, b -> c) would leave anyone still holding `a` one hop short,
// because RenamePageGrants makes a single pass. Collapsing the chain at the
// source is cheaper than making the pass iterative, and it keeps the table
// readable as "what this key is called now".
func TestRenamedHasNoChains(t *testing.T) {
	for old, now := range Renamed {
		if next, chained := Renamed[now]; chained {
			t.Errorf("Renamed[%q] = %q, which is itself renamed to %q — "+
				"collapse it to the final key", old, now, next)
		}
	}
}
