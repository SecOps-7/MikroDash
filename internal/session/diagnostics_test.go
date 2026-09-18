package session

import (
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/hub"
	"mikrodash/internal/roscache"
	"mikrodash/internal/roslimit"
	"mikrodash/internal/routeros"
)

type diagReader struct{}

func (diagReader) Do(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }

// ── WHAT THESE TESTS ARE FOR ────────────────────────────────────────────────
//
// The API Diagnostics card is an INSTRUMENT, and the failure mode of an
// instrument is not a crash — it is a plausible wrong number. A card that says
// "4 menus" when eleven are subscribed reads exactly like a card that is right,
// and the first thing an operator does with it is size a poll interval. So each
// figure is asserted against a state built by hand.
//
// It was also, for the whole life of this port, a card that rendered NOTHING and
// looked merely quiet. `internal/verify/event_test.go` recorded that as
// deliberate. So the second job here is to make the emptiness detectable.

// TestDiagnosticsCountsTheAcquisitionLayer: menus, the streamed/polled split, and
// the pushed-first ordering of the menu list.
func TestDiagnosticsCountsTheAcquisitionLayer(t *testing.T) {
	s := NewForTest(hub.New(), "r1")
	s.roscache = roscache.New(diagReader{})

	// THREE MENUS, ONE SUBSCRIBER EACH, which is what a real router looks like:
	// measured across seven pages on the live fleet on 2026-09-10, no menu ever
	// had two. An earlier version of this test subscribed several collectors to
	// one menu to exercise a "shared reads" section, and passed — against a
	// section that could not occur in production.
	s.roscache.Subscribe("/ip/dhcp-server/lease/print", nil, nil, func([]routeros.Reply, error) {})
	s.roscache.Subscribe("/ip/arp/print", nil, nil, func([]routeros.Reply, error) {})
	s.roscache.Subscribe("/system/resource/print", nil, nil, func([]routeros.Reply, error) {})

	d := s.Diagnostics(0)
	a := d.Acquisition

	if a.Menus != 3 {
		t.Fatalf("Menus = %d, want 3 — the card would under-report what this app asks the router for", a.Menus)
	}
	// STREAMED + POLLED == MENUS is the invariant the split is only useful
	// under. Nothing is streaming here, so all three are polled.
	if a.Streamed+a.Polled != a.Menus {
		t.Errorf("Streamed(%d)+Polled(%d) != Menus(%d): a menu is in neither column and the card loses it",
			a.Streamed, a.Polled, a.Menus)
	}
	if a.Polled != 3 {
		t.Errorf("Polled = %d, want 3 (no stream is open in this test)", a.Polled)
	}

	// EVERY MENU IS NAMED. The counts say how much; this list is the only thing
	// on the card that says what, and a menu missing from it is a read an
	// operator cannot account for.
	if len(a.Reads) != 3 {
		t.Fatalf("Reads has %d entries, want 3", len(a.Reads))
	}
	if a.More != 0 {
		t.Errorf("More = %d with three menus and a cap of %d, want 0", a.More, diagMenus)
	}
	// Nothing streams here, so the tie-break is the name.
	if a.Reads[0].Menu != "/ip/arp/print" {
		t.Errorf("Reads[0] = %q, want /ip/arp/print — a list that is not ordered "+
			"shuffles every two seconds", a.Reads[0].Menu)
	}
	if a.Cap != roslimit.Cap() {
		t.Errorf("Cap = %d, want the real concurrency cap %d — the in-flight figure means "+
			"nothing without the ceiling it is measured against", a.Cap, roslimit.Cap())
	}
}

// TestDiagnosticsCapsTheMenuList, because a card is not a table. Without the cap
// a router with thirty subscribed menus produces a card that scrolls past the
// dashboard.
func TestDiagnosticsCapsTheMenuList(t *testing.T) {
	s := NewForTest(hub.New(), "r1")
	s.roscache = roscache.New(diagReader{})
	for i := 0; i < diagMenus+5; i++ {
		s.roscache.Subscribe(string(rune('a'+i))+"/print", nil, nil, func([]routeros.Reply, error) {})
	}

	d := s.Diagnostics(0)
	if got := len(d.Acquisition.Reads); got != diagMenus {
		t.Errorf("Reads has %d entries, want the cap %d", got, diagMenus)
	}
	// The CAP IS ADMITTED. A list silently shortened is an instrument reporting
	// less than it measured, which is the failure this whole card is exposed to.
	if d.Acquisition.More != 5 {
		t.Errorf("More = %d, want 5 — the card cannot say it truncated", d.Acquisition.More)
	}
	// The COUNT is not capped, only the list. Truncating both would report a
	// router doing less work than it is.
	if d.Acquisition.Menus != diagMenus+5 {
		t.Errorf("Menus = %d, want %d: the cap is on the list, not on the count",
			d.Acquisition.Menus, diagMenus+5)
	}
}

// TestDiagnosticsCountsPayloadsNotCommands. The derivation layer's figure is the
// one that separates "this app is busy" from "this router is being asked a lot",
// and the two diverge exactly when coalescing is working.
func TestDiagnosticsCountsPayloadsNotCommands(t *testing.T) {
	s := NewForTest(hub.New(), "r1")
	for i := 0; i < 7; i++ {
		s.notePayload()
	}
	if got := s.Diagnostics(0).Derivation.PayloadsPerMin; got != 7 {
		t.Errorf("PayloadsPerMin = %d, want 7", got)
	}
}

// TestDiagnosticsCountsEachRoomOnce is the assertion the room counter exists
// for. Collectors SHARE rooms — `page-dashboard` is claimed by several — and
// counting per collector reports one viewer on the Dashboard as several occupied
// rooms, which is a number that can exceed the rooms that exist.
func TestDiagnosticsCountsEachRoomOnce(t *testing.T) {
	h := hub.New()
	s := NewForTest(h, "r1")

	// Find a room more than one collector declares. If none exists the premise
	// of this test has changed and the dedupe may be dead code.
	shared, owners := "", 0
	counts := map[string]int{}
	for _, key := range targetKeys {
		seen := map[string]bool{}
		for _, r := range collect.DemandRooms(key) {
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			counts[r]++
			if counts[r] > owners {
				shared, owners = r, counts[r]
			}
		}
	}
	if owners < 2 {
		t.Skip("no room is declared by two collectors any more; the dedupe has nothing to prevent")
	}

	// `Add` FIRST: `Join` silently no-ops for a client the hub has never seen,
	// which produces a test that measures nothing and passes the day the dedupe
	// is deleted.
	c := hub.NewClient("c1", 8)
	h.Add(c)
	h.Join(c, RoomFor("r1", shared))

	if got := s.Diagnostics(0).Views.Rooms; got != 1 {
		t.Errorf("Rooms = %d with one client in %q, want 1 — %d collectors declare that room, "+
			"so counting per collector would report %d", got, shared, owners, owners)
	}
}

// TestDiagnosticsReportsHolds: the non-viewer reasons a session is alive. Without
// them an operator looking at a router nobody is watching sees collectors running
// and no explanation, which is the single most confusing thing this card can show.
func TestDiagnosticsReportsHolds(t *testing.T) {
	s := NewForTest(hub.New(), "r1")
	s.holds = map[string]bool{"alerts": true, "history": true}

	got := s.Diagnostics(0).Views.Holds
	if len(got) != 2 || got[0] != "alerts" || got[1] != "history" {
		t.Errorf("Holds = %v, want [alerts history] in that order — a shuffling list "+
			"repaints the card every two seconds", got)
	}

	// EMPTY, NEVER NIL: the renderer tests `holds.length`, and a JSON `null`
	// there is a different value from `[]` on the way through.
	s.holds = nil
	if h := s.Diagnostics(0).Views.Holds; h == nil {
		t.Error("Holds is nil with no holds; it must marshal as [] so the card can test its length")
	}
}

// TestDiagnosticsGatedCountIsEveryGatedCollector. `Running` is meaningless
// without it: "5" is a number, "5 / 22" is a reading.
func TestDiagnosticsGatedCountIsEveryGatedCollector(t *testing.T) {
	s := NewForTest(hub.New(), "r1")
	d := s.Diagnostics(0)
	if d.Views.Gated != len(targetKeys) {
		t.Errorf("Gated = %d, want %d", d.Views.Gated, len(targetKeys))
	}
	if d.Views.Running != 0 {
		t.Errorf("Running = %d with no viewer and no hold, want 0", d.Views.Running)
	}
}

// TestDiagnosticsOnANilSessionIsEmpty, not a panic. The sender looks a session up
// by the router the socket has selected, and a router that has just been removed
// answers nil.
func TestDiagnosticsOnANilSessionIsEmpty(t *testing.T) {
	var s *Session
	if d := s.Diagnostics(99); d.TS != 99 || d.RouterID != "" {
		t.Errorf("nil session gave %+v", d)
	}
}
