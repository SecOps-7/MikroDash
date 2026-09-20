package asn

import "testing"

// THE MAP MUST NOT BE EMPTY, which is the one thing internal/verify's ledger
// cannot tell you: with no categories it finds nothing on either side and
// passes. Every badge would be grey and every Sankey node the same colour.
func TestTheCategoryMapIsNotEmpty(t *testing.T) {
	if len(categories) == 0 {
		t.Fatal("the category map is empty; every badge is grey and the ledger " +
			"comparing it against app.css has nothing to compare")
	}
}
