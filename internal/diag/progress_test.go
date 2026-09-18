package diag

import (
	"testing"

	"mikrodash/internal/routeros"
)

// A RUN IN FLIGHT IS FOLDED ONLY UP TO THE SECTION STILL ARRIVING. The
// traceroute capture's second run holds sections 0, 1, 1, 2, 2, 2, 3, 3, 3: cut
// after the second row of section 2, the complete part is 0, 1, 1 — a two-hop
// table, not a three-hop one with its third hop missing.
func TestARunInFlightIsFoldedUpToTheSectionStillArriving(t *testing.T) {
	c := readCapture(t, "toolTraceroute.json", 2)
	rows := c.Exchanges[1].Rows
	if got := CompleteSections(rows[:5]); len(got) != 3 {
		t.Fatalf("complete part of 5 rows is %d rows, want 3", len(got))
	}
	if r := FoldTraceroute("198.51.100.1", CompleteSections(rows[:5])); len(r.Hops) != 2 {
		t.Errorf("the table in flight has %d hops, want 2", len(r.Hops))
	}
	// The control: the whole run is folded as it always was.
	if got := CompleteSections(rows); len(got) != 6 {
		t.Errorf("complete part of the finished run is %d rows, want the 6 before its last section", len(got))
	}
	// Ping's rows carry no section, and each is complete.
	p := readCapture(t, "toolPing.json", 2).Exchanges[0].Rows
	if got := CompleteSections(p); len(got) != len(p) {
		t.Errorf("ping: %d of %d rows kept", len(got), len(p))
	}
	if got := CompleteSections(nil); len(got) != 0 {
		t.Errorf("nothing yet: %v", got)
	}
	if got := CompleteSections([]routeros.Reply{{".section": "0"}}); len(got) != 0 {
		t.Errorf("one section still arriving is not complete: %v", got)
	}
}
