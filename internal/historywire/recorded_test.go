package historywire

import (
	"testing"

	"mikrodash/internal/collect"
)

// Which interfaces reach history.
//
// ── THE BROWSER WAS DECIDING WHAT GOT WRITTEN ──────────────────────────────
//
// `Record` sits on the traffic collector's emit seam, which sees every
// interface in the `monitor-traffic` stream — and that stream is the router's
// default interface PLUS whatever interfaces browsers are currently watching.
// So opening the Dashboard and selecting ether3 started writing ether3 into
// history, and closing the tab stopped it.
//
// The Bandwidth report then summed an interface whose series had holes wherever
// nobody happened to be looking, and presented the total as authoritative.
// Reported on issue #126: "If I do not open the Dashboard, the report often
// shows little or no data ... the totals do not match the actual traffic."

// samples pushes two minutes of one interface, which is what it takes to roll a
// minute over and produce rows.
func samples(w *Wire, routerID, ifName string) {
	w.Record(routerID, "traffic:update", &collect.TrafficSample{
		IfName: ifName, RxMbps: 8, TxMbps: 4, TS: min1})
	w.Record(routerID, "traffic:update", &collect.TrafficSample{
		IfName: ifName, RxMbps: 8, TxMbps: 4, TS: min2})
}

func TestOnlyDeclaredInterfacesAreRecorded(t *testing.T) {
	w, s := on(t)
	w.SetRecordedInterfaces("r-1", []string{"ether1"})

	samples(w, "r-1", "ether1")
	if len(s.rows) == 0 {
		t.Fatal("the declared interface wrote nothing")
	}
	before := len(s.rows)

	// ether3 is what a viewer picked in the Dashboard. It is in the stream, so
	// it reaches this seam — and it must not reach the database.
	samples(w, "r-1", "ether3")
	if len(s.rows) != before {
		t.Errorf("an interface nobody declared wrote %d rows; its series would "+
			"exist only while a browser was open", len(s.rows)-before)
	}
}

// TestAnUndeclaredRouterRecordsEverything is the safe default, and it is not a
// detail: this is set from the fleet syncs, so a router seen before the first
// sync — or a deployment that never calls it — must keep its history rather
// than lose it to a default nobody chose.
func TestAnUndeclaredRouterRecordsEverything(t *testing.T) {
	w, s := on(t)
	samples(w, "r-unknown", "ether7")
	if len(s.rows) == 0 {
		t.Error("a router with no declaration recorded nothing at all")
	}
}

// TestAnEmptyDeclarationRecordsEverything — same reasoning one step further.
// An empty list is "I have nothing to say", not "record nothing": a router
// whose default interface could not be resolved must not silently go dark.
func TestAnEmptyDeclarationRecordsEverything(t *testing.T) {
	w, s := on(t)
	w.SetRecordedInterfaces("r-1", nil)
	samples(w, "r-1", "ether9")
	if len(s.rows) == 0 {
		t.Error("an empty declaration stopped all recording")
	}
}

// TestTheDeclarationIsPerRouter. One router's list must not silence another's,
// which is the shape of bug that hides for a long time on a single-router
// install.
func TestTheDeclarationIsPerRouter(t *testing.T) {
	w, s := on(t)
	w.SetRecordedInterfaces("r-1", []string{"ether1"})
	w.SetRecordedInterfaces("r-2", []string{"sfp1"})

	samples(w, "r-2", "sfp1")
	if len(s.rows) == 0 {
		t.Fatal("r-2's own declared interface wrote nothing")
	}
	before := len(s.rows)
	samples(w, "r-2", "ether1") // r-1's interface, not r-2's
	if len(s.rows) != before {
		t.Error("a router recorded an interface declared for a different router")
	}
}

// TestADeclarationCanBeChanged — it is re-set on every fleet sync, so changing
// a router's default interface has to take effect rather than being pinned to
// whatever was first declared.
func TestADeclarationCanBeChanged(t *testing.T) {
	w, s := on(t)
	w.SetRecordedInterfaces("r-1", []string{"ether1"})
	w.SetRecordedInterfaces("r-1", []string{"sfp1"})

	samples(w, "r-1", "sfp1")
	if len(s.rows) == 0 {
		t.Error("the new declaration did not take effect")
	}
	before := len(s.rows)
	samples(w, "r-1", "ether1")
	if len(s.rows) != before {
		t.Error("the old declaration is still recording")
	}
}
