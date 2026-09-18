package session

import (
	"testing"

	"mikrodash/internal/areas"
	"mikrodash/internal/collect"
	"mikrodash/internal/routeros"
)

// answerAll is a router that answers every menu with no rows.
type answerAll struct{ asked int }

func (a *answerAll) Connected() bool { return true }
func (a *answerAll) Do(routeros.Cmd) ([]routeros.Reply, error) {
	a.asked++
	return nil, nil
}

// TestTheAreasTargetPrimesEveryArea is `primeAll`'s view of the areas collector,
// which is what seeds every generated page at connect.
//
// ── THE TWO DEFECTS IT PINS ─────────────────────────────────────────────────
//
// The target's refresh was `Tick`, which reads only areas whose room is
// occupied — and at connect no room is, so the prime read nothing. Its `last`
// answered non-nil once ANY area held a payload, so after one page had been
// read the prime skipped every other area for the life of the session.
//
// Both directions: `last` must be nil while an area is unread and non-nil once
// all are, and `refresh` must seed every area with no room occupied.
func TestTheAreasTargetPrimesEveryArea(t *testing.T) {
	if len(areas.All()) < 2 {
		t.Skip("needs two areas to tell one from all")
	}
	first := areas.All()[0]
	r := &answerAll{}
	s := &Session{areas: collect.NewAreas(r, collect.Emit{}).WithOccupancy(func(room string) bool {
		return room == collect.AreaRoomFor(first.Key)
	})}
	tgt, ok := s.targets()["areas"]
	if !ok {
		t.Fatal("no areas target")
	}
	if tgt.last() != nil {
		t.Fatal("a collector that has read nothing reports a payload")
	}

	// One page read, the way a viewer's poll reads it.
	s.areas.Tick()
	if s.areas.Last(first.Key) == nil {
		t.Fatal("the occupied area was not read; this test is measuring nothing")
	}
	if tgt.last() != nil {
		t.Error("one area read makes the target report a payload, so primeAll skips every " +
			"other generated page and it opens empty")
	}

	// The prime, with the one occupied room irrelevant to it.
	tgt.refresh()
	for _, area := range areas.All() {
		if s.areas.Last(area.Key) == nil {
			t.Errorf("area %q holds no payload after the target's refresh", area.Key)
		}
	}
	if tgt.last() == nil {
		t.Error("every area read and the target still reports nothing, so every " +
			"connect re-reads them all")
	}
}
