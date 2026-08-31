package session

import (
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
	"mikrodash/internal/store"
)

func noEmit(string, string, any) {}

// TestApplyPollRetunesReachesTheCollectors.
func TestApplyPollRetunesReachesTheCollectors(t *testing.T) {
	s := &Session{RouterID: "r1"}
	s.system = collect.NewSystem(nil, noEmit, 5000)
	s.dns = collect.NewDNS(nil, noEmit, 4000)

	applied := s.ApplyPollRetunes(
		store.Settings{"pollSystem": 9000, "pollDns": 7000},
		store.Settings{"pollSystem": 9000, "pollDns": 7000},
	)
	if len(applied) != 2 {
		t.Fatalf("applied %v, want both system and dns", applied)
	}
	if got := s.system.PollMs(); got != 9000 {
		t.Errorf("system = %d, want 9000", got)
	}
	if got := s.dns.PollMs(); got != 7000 {
		t.Errorf("dns = %d, want 7000", got)
	}
}

// TestAPinnedKeyIsNotApplied.
//
// #105: the value is still SAVED to the file, it is just not applied to THIS
// router. The overrides therefore have to come from the session — a fleet-wide
// save has one set of updates and as many override sets as there are live
// routers, and passing nil would silently un-pin every one of them.
func TestAPinnedKeyIsNotApplied(t *testing.T) {
	s := &Session{RouterID: "r1"}
	s.system = collect.NewSystem(nil, noEmit, 5000)
	s.eff = collection.Resolved{Overrides: map[string]any{"pollSystem": 3000}}

	// Believability: unpinned, it IS applied — otherwise the assertion below
	// holds for a function that applies nothing.
	free := &Session{RouterID: "r2"}
	free.system = collect.NewSystem(nil, noEmit, 5000)
	if applied := free.ApplyPollRetunes(
		store.Settings{"pollSystem": 9000}, store.Settings{"pollSystem": 9000}); len(applied) != 1 {
		t.Fatalf("an unpinned key was not applied (%v)", applied)
	}

	applied := s.ApplyPollRetunes(
		store.Settings{"pollSystem": 9000}, store.Settings{"pollSystem": 9000})
	if len(applied) != 0 {
		t.Errorf("applied %v on a router that pinned pollSystem", applied)
	}
	if got := s.system.PollMs(); got != 5000 {
		t.Errorf("system = %d; the pin was silently overwritten by a fleet-wide save", got)
	}
}

// TestACollectorThisSessionHasNotBuiltIsSkipped.
//
// Several collectors exist only after a page focus or a reconnect. A save must
// not depend on which pages happen to be open, and must not panic on the ones
// that are nil.
func TestACollectorThisSessionHasNotBuiltIsSkipped(t *testing.T) {
	s := &Session{RouterID: "r1"} // every collector nil
	applied := s.ApplyPollRetunes(
		store.Settings{"pollSystem": 9000, "pollDns": 7000},
		store.Settings{"pollSystem": 9000, "pollDns": 7000},
	)
	if len(applied) != 0 {
		t.Errorf("applied %v against a session with no collectors", applied)
	}
}

// TestKeepCurrentDoesNotSetTheFloor.
//
// A stored value that is not a finite number means "leave the collector alone".
// Passing the zero through would set it to 500 instead — the fastest interval
// allowed, on a collector nobody asked to speed up.
func TestKeepCurrentDoesNotSetTheFloor(t *testing.T) {
	s := &Session{RouterID: "r1"}
	s.system = collect.NewSystem(nil, noEmit, 5000)

	applied := s.ApplyPollRetunes(
		store.Settings{"pollSystem": 9000},
		store.Settings{"pollSystem": "not a number"},
	)
	if len(applied) != 0 {
		t.Errorf("applied %v for an unparseable stored value", applied)
	}
	if got := s.system.PollMs(); got != 5000 {
		t.Errorf("system = %d, want the untouched 5000", got)
	}
}
