package alertwire

import "testing"

// `memStore` directly.
//
// ── WHY THESE ARE UNIT TESTS AND NOT WIRE TESTS ────────────────────────────
//
// Driving this through `Wire.Evaluate` does not reach it. Every rule guards on
// the evaluator's OWN edge state first — the CPU rule fires only when
// `prevCPUAlert` flips — so a store that answered every question wrongly still
// produced the right alerts in a wire-level test. Two mutations, `HasOpen`
// always false and `Resolve` always returning an id, both survived a suite that
// looked like it was testing exactly them.
//
// The store is the SECOND guard, behind the edge state: it is what stops a
// condition re-raising when the evaluator has been rebuilt, and what decides
// whether a recovery has anything to recover from. Those are properties of this
// type, so they are tested on this type.

func TestMemStoreRemembersAnOpenCondition(t *testing.T) {
	m := newMemStore()
	if m.HasOpen("r-1", "high_cpu", "") {
		t.Fatal("a fresh store reports something open")
	}
	m.Record("r-1", "high_cpu", "", "CPU at 99%")
	if !m.HasOpen("r-1", "high_cpu", "") {
		t.Error("the condition it just recorded is not open; a persisting " +
			"condition would re-raise on every evaluation")
	}
}

func TestMemStoreResolveClosesIt(t *testing.T) {
	m := newMemStore()
	m.Record("r-1", "high_cpu", "", "")
	ids := m.Resolve("r-1", "high_cpu", "")
	if len(ids) != 1 {
		t.Fatalf("Resolve returned %v; `emit` reads an empty slice as 'nothing "+
			"was open', so the recovery alert never fires", ids)
	}
	if m.HasOpen("r-1", "high_cpu", "") {
		t.Error("it is still open after being resolved")
	}
}

// TestMemStoreResolvesNothingThatWasNeverOpen — returning an id here would
// invent a recovery notification for an outage that never happened.
func TestMemStoreResolvesNothingThatWasNeverOpen(t *testing.T) {
	m := newMemStore()
	if ids := m.Resolve("r-1", "high_cpu", ""); len(ids) != 0 {
		t.Errorf("Resolve returned %v for a condition that was never open", ids)
	}
	// And after a full cycle it is closed again, not still resolvable.
	m.Record("r-1", "high_cpu", "", "")
	m.Resolve("r-1", "high_cpu", "")
	if ids := m.Resolve("r-1", "high_cpu", ""); len(ids) != 0 {
		t.Errorf("a second resolve returned %v", ids)
	}
}

// TestMemStoreKeysOnAllThreeFields — type and subject both matter. Interfaces
// and peers share an alert type and differ only by subject, so collapsing them
// would let one interface's outage suppress another's.
func TestMemStoreKeysOnAllThreeFields(t *testing.T) {
	m := newMemStore()
	m.Record("r-1", "iface_down", "ether1", "")

	for _, c := range []struct{ router, typ, subject, why string }{
		{"r-2", "iface_down", "ether1", "a different router"},
		{"r-1", "iface_up", "ether1", "a different alert type"},
		{"r-1", "iface_down", "ether2", "a different interface"},
	} {
		if m.HasOpen(c.router, c.typ, c.subject) {
			t.Errorf("%s reports open from another condition's record", c.why)
		}
	}
	if !m.HasOpen("r-1", "iface_down", "ether1") {
		t.Error("the recorded condition is not open")
	}
}

// TestMemStoreIdsAreNegative — a memory id must never be mistaken for a row id.
// SQLite rowids start at 1, so a negative one is unmistakable anywhere it turns
// up: a log line, a payload, a debugger.
func TestMemStoreIdsAreNegative(t *testing.T) {
	m := newMemStore()
	seen := map[int64]bool{}
	for i := 0; i < 5; i++ {
		id := m.Record("r-1", "iface_down", string(rune('a'+i)), "")
		if id >= 0 {
			t.Errorf("id %d could be mistaken for a database row id", id)
		}
		if seen[id] {
			t.Errorf("id %d was handed out twice", id)
		}
		seen[id] = true
	}
}

// TestMemStoreResolveReturnsTheRecordedId — `emit` only checks the length, but
// returning the wrong id would be a lie in a value that reaches a caller.
func TestMemStoreResolveReturnsTheRecordedId(t *testing.T) {
	m := newMemStore()
	want := m.Record("r-1", "high_cpu", "", "")
	got := m.Resolve("r-1", "high_cpu", "")
	if len(got) != 1 || got[0] != want {
		t.Errorf("Resolve returned %v, want [%d]", got, want)
	}
}
