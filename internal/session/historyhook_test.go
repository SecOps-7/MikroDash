package session

import (
	"strings"
	"testing"
)

// Where the history recorder is CALLED FROM, pinned out of the source.
//
// ── WHY THE SOURCE AND NOT A BEHAVIOURAL TEST ──────────────────────────────
//
// Both properties are about POSITION, and position is exactly what a
// behavioural test through a fake would not see:
//
//   - Record must sit in the ONE emit closure, which runs before any room is
//     chosen. The live app records ping from a separate hook because its
//     equivalent seam was router-wide only, and its own comment says what that
//     cost: when `ping:update` became page-scoped, "history would have stopped
//     being written, silently".
//   - Flush must run BEFORE the collectors are stopped. Afterwards no further
//     sample can arrive to roll the open bucket over, so the last minute of
//     every session is simply lost — and a lost minute looks like a quiet
//     minute on the chart.
//
// A test that drove a fake wire would pass with Record moved into a per-room
// branch, or with Flush moved below the Stops.

func TestTheHistoryRecorderSitsOnTheOneEmitSeam(t *testing.T) {
	src := sessionSource(t)
	emit := blockBetween(t, src, "emit := func(sub, event string, payload any) {", "\n\t}")
	if !strings.Contains(emit, "m.history.Record(") {
		t.Error("the emit closure does not call m.history.Record — a page-scoped " +
			"event would then stop being recorded, silently, which is the exact " +
			"failure the live app's ping hook exists to avoid")
	}
	// BEFORE any room is chosen. The first `sub == ""` branch is where room
	// selection starts; recording after it would make the seam page-scoped.
	rec := strings.Index(emit, "m.history.Record(")
	room := strings.Index(emit, `if sub == "" {`)
	if room >= 0 && rec > room {
		t.Error("Record is called after room selection begins; it must see every " +
			"payload, not the router-wide ones")
	}
}

// RE-AIMED 2026-09-01 from `Release` to `idleOut`, which is where the release
// path's teardown moved when the idle grace was added. Release now only arms a
// timer; the ORDERING this asserts -- flush before the collectors stop, so a
// sample can still roll the open bucket over -- is a property of the teardown,
// and follows it.
func TestTheLastMinuteIsFlushedBeforeTheCollectorsStop(t *testing.T) {
	rel := blockBetween(t, sessionSource(t), "func (m *Manager) idleOut(", "\n}")
	flush := strings.Index(rel, "m.history.Flush(")
	if flush < 0 {
		t.Fatal("Release does not flush the history buckets: every session would " +
			"lose the minute it ended in")
	}
	stop := strings.Index(rel, ".Stop()")
	if stop >= 0 && flush > stop {
		t.Error("Flush runs after the collectors are stopped, so no sample can " +
			"arrive to roll the open bucket over and the last minute is lost")
	}
}
