package server

import (
	"errors"
	"testing"

	"mikrodash/internal/routeros"
)

func rows(ids ...string) []routeros.Reply {
	out := make([]routeros.Reply, len(ids))
	for i, id := range ids {
		out[i] = routeros.Reply{".id": id}
	}
	return out
}

func TestConfirmCreatedNeedsExactlyOneNewRow(t *testing.T) {
	before := map[string]bool{"*1": true, "*2": true}
	if r, ok := confirmCreated(before, rows("*1", "*2", "*3")); !ok || r[".id"] != "*3" {
		t.Errorf("one new row: got %v, %v", r, ok)
	}
	if _, ok := confirmCreated(before, rows("*1", "*2")); ok {
		t.Error("an add that left no new row was confirmed")
	}
	if _, ok := confirmCreated(before, rows("*1", "*2", "*3", "*4")); ok {
		t.Error("two new rows were confirmed as ours; which one is ours cannot be told")
	}
}

func TestConfirmRemoved(t *testing.T) {
	if !confirmRemoved(rows("*1", "*3"), "*2") {
		t.Error("a row that is gone was not confirmed removed")
	}
	if confirmRemoved(rows("*1", "*2"), "*2") {
		t.Error("a row still present was confirmed removed")
	}
}

func TestConfirmMovedChecksTheDestination(t *testing.T) {
	if at, ok := confirmMoved(rows("*1", "*3", "*2"), "*3", "*2"); !ok || at != 1 {
		t.Errorf("a row before its destination: at %d, ok %v", at, ok)
	}
	if _, ok := confirmMoved(rows("*3", "*1", "*2"), "*3", "*2"); ok {
		t.Error("a row not immediately before its destination was confirmed")
	}
	if at, ok := confirmMoved(rows("*1", "*2", "*3"), "*3", ""); !ok || at != 2 {
		t.Errorf("a row moved to the end: at %d, ok %v", at, ok)
	}
	if _, ok := confirmMoved(rows("*1", "*3", "*2"), "*3", ""); ok {
		t.Error("a row not last was confirmed as moved to the end")
	}
	if _, ok := confirmMoved(rows("*1", "*2"), "*9", ""); ok {
		t.Error("a missing row was confirmed moved")
	}
}

func TestConfirmActionChecksEnableAndDisable(t *testing.T) {
	on := []routeros.Reply{{".id": "*1", "disabled": "false"}}
	off := []routeros.Reply{{".id": "*1", "disabled": "true"}}
	if _, ok := confirmAction(on, "*1", "enable"); !ok {
		t.Error("an enabled row was not confirmed enabled")
	}
	if _, ok := confirmAction(on, "*1", "disable"); ok {
		t.Error("a row still enabled was confirmed disabled")
	}
	if _, ok := confirmAction(off, "*1", "disable"); !ok {
		t.Error("a disabled row was not confirmed disabled")
	}
	if _, ok := confirmAction(on, "*1", "make-static"); !ok {
		t.Error("another verb on a present row was not confirmed")
	}
	if _, ok := confirmAction(on, "*2", "enable"); ok {
		t.Error("an action on a missing row was confirmed")
	}
}

func TestAnUnknownOutcomeHasItsOwnCode(t *testing.T) {
	if got := writeFailCode(errOutcomeUnknown); got != "outcome-unknown" {
		t.Errorf("errOutcomeUnknown maps to %q", got)
	}
	if !errors.Is(errOutcomeUnknown, errOutcomeUnknown) {
		t.Fatal("sentinel does not match itself")
	}
}
