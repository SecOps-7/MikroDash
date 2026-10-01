package history

import "sort"

// Coverage: which routers are being OBSERVED, as opposed to up or down.
//
// The Devices page's connectivity strip has three states, and the third is the
// one `connectivity_events` cannot express: time nobody was watching. A router's
// state between two rows is inferred, and across a stretch where MikroDash was
// stopped, or the router was disabled, that inference paints a colour the app
// never observed. `monitor_runs` records when each router WAS observed; this
// decides, tick by tick, which runs open and close.
//
// PURE, like the rest of this package: sets in, operations out, no clock and no
// database. The writer in `internal/historywire` carries the operations out.

// CoverageTouchMs is how often every open run's `last_seen_at` is moved
// forward. A minute bounds what a crash can lose, and it is one UPDATE for the
// whole fleet however large it is.
const CoverageTouchMs = 60_000

// CoverageOps is what one tick decides.
type CoverageOps struct {
	// Open are routers newly observed: start a run for each.
	Open []string
	// Close are routers no longer observed: touch their run once more, at this
	// moment, and forget it - so the run ends when observation did, not up to a
	// minute earlier at the last heartbeat.
	Close []string
	// Touch is whether the heartbeat is due for the runs that stay open.
	Touch bool
}

// PlanCoverage decides one tick.
//
// `open` is every router with a run open now; `covered` every router observed
// now. Both are sets. The result is sorted so a caller's writes, and a test's
// expectations, do not depend on map order.
//
// THE HEARTBEAT IS FOR RUNS THAT STAY OPEN. A run being closed is touched by
// its close; one being opened starts with `last_seen_at = started_at`. So with
// nothing staying open there is nothing to touch, however long it has been.
func PlanCoverage(open, covered map[string]bool, now, lastTouch int64) CoverageOps {
	var ops CoverageOps
	staying := 0
	for id := range open {
		if covered[id] {
			staying++
		} else {
			ops.Close = append(ops.Close, id)
		}
	}
	for id := range covered {
		if !open[id] {
			ops.Open = append(ops.Open, id)
		}
	}
	sort.Strings(ops.Open)
	sort.Strings(ops.Close)
	ops.Touch = staying > 0 && now-lastTouch >= CoverageTouchMs
	return ops
}
