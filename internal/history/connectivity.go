package history

// The connectivity-event state machine, ported from src/alertSessions.js.
//
// `recordConnectivity` in the live app is three lines (src/db-writer.js:134).
// Everything that matters is WHEN it is called, and that is this file. The
// rules are pinned against the live module by tools/connectivity-cases.js,
// which drives the real alertSessions.js rather than re-deriving it.
//
// ── FOUR RULES, EACH FROM A DEFECT ─────────────────────────────────────────
//
//  1. TRANSITION-ONLY on the way up (alertSessions.js:140). The live comment:
//     "unconditional writes on every reconnect inflate uptime for a flapping
//     link."
//
//  2. OBSERVED, NOT DECLARED, on the way down (alertSessions.js:~193, #99).
//     "The outage started now, not when the debounce expires. Record the
//     observed time so downtime is not under-reported by threshMs." The
//     captured moment travels through the timer; the firing moment is discarded.
//
//  3. THE COLD-START DISCONNECT SKIPS THE DEBOUNCE (alertSessions.js:168).
//     With no prior observation there is nothing to debounce against, so a
//     router that is already down when the session opens is recorded at once.
//     It is also the only down path that never sets declaredOffline.
//
//  4. A ZERO THRESHOLD IS ITS OWN BRANCH, not a debounce of length zero. It
//     records on EVERY close, because the repeat guard there is on the alert
//     and not on the row. Reproduced deliberately: the corpus was generated
//     before this comment was written and it disagreed with the assumption.
//
// NOTHING CONSTRUCTS THIS YET. Like Bucketer, it is ported ahead of the pool
// that will drive it, so the rules are pinned while the live source is in
// front of us rather than reconstructed at wiring time.

// ConnEffect is what one event produced: rows to write and router:status
// emits. Status is separate because it does NOT follow the rows — the live code
// emits status on every connect, including the reconnects that write no row.
//
// The rows are the SAME `Row` the bucketer emits, carrying Table
// "connectivity", so the writer that eventually drains both has one path
// rather than two.
type ConnEffect struct {
	Rows   []Row
	Status []bool
}

// Connectivity tracks one router. The zero value is a session that has observed
// nothing yet, which is rule 3's starting state.
type Connectivity struct {
	RouterID string

	// ThreshMs is connDownThresholdSec × 1000. The live default is 30s and is
	// applied by the caller; zero here really means zero — rule 4.
	ThreshMs int64

	prev      *bool // nil = never observed, rule 3
	timerAt   int64
	timerObs  int64
	timerLive bool

	// declaredOffline gates the recovery alert in the live app. Carried here
	// so the alert port has it, and because leaving it out would make the
	// zero-threshold branch look identical to the debounce branch when it is
	// not: the live code sets it unconditionally in the debounce and only when
	// alerts are enabled at threshold zero.
	declaredOffline bool
}

// row builds a connectivity row. `explicit` records whether the live caller
// passed a timestamp at all: `recordConnectivity(id, true)` leaves it undefined
// and db-writer.js:134 defaults with `ts || Date.now()`, so the STORED row is
// the same either way — but the two are different facts about the caller, and a
// port that always passed a time would look identical in the database while
// having quietly lost rule 2.
func (c *Connectivity) row(connected bool, ts int64, explicit bool) Row {
	return Row{
		Table: "connectivity", RouterID: c.RouterID,
		Connected: connected, TS: ts, ExplicitTS: explicit,
	}
}

func truePtr() *bool  { b := true; return &b }
func falsePtr() *bool { b := false; return &b }

// Connected handles a ROS 'connected' event.
func (c *Connectivity) Connected(now int64) ConnEffect {
	c.timerLive = false // rule: a connect cancels a pending debounce outright
	e := ConnEffect{Status: []bool{true}}
	if c.prev == nil || !*c.prev {
		// Rule 1: only a real transition writes.
		e.Rows = append(e.Rows, c.row(true, now, false))
	}
	c.declaredOffline = false
	c.prev = truePtr()
	return e
}

// Disconnected handles a ROS 'close' or 'connectionError' event. Both are the
// same handler in the live app and must stay the same here.
func (c *Connectivity) Disconnected(now int64) ConnEffect {
	if c.timerLive {
		// A second close while the debounce is pending is ignored: it must not
		// re-arm the timer, or a flapping link would postpone its own outage
		// row indefinitely.
		return ConnEffect{}
	}
	if c.prev == nil {
		// Rule 3. Note what is NOT set: declaredOffline stays false.
		c.prev = falsePtr()
		return ConnEffect{Rows: []Row{c.row(false, now, false)}, Status: []bool{false}}
	}
	if c.ThreshMs <= 0 {
		// Rule 4. The row is written every time; only the alert is guarded.
		if *c.prev {
			c.declaredOffline = true
		}
		c.prev = falsePtr()
		return ConnEffect{Rows: []Row{c.row(false, now, false)}, Status: []bool{false}}
	}
	// Rule 2: capture the observed moment HERE, not when the timer fires.
	c.timerObs = now
	c.timerAt = now + c.ThreshMs
	c.timerLive = true
	return ConnEffect{}
}

// Tick advances the debounce. The caller drives it; the state machine holds no
// timer of its own, which is what makes the whole thing testable without one.
func (c *Connectivity) Tick(now int64) ConnEffect {
	if !c.timerLive || now < c.timerAt {
		return ConnEffect{}
	}
	c.timerLive = false
	c.declaredOffline = true
	c.prev = falsePtr()
	// The row carries timerObs — the moment the link was seen to go — and the
	// timestamp is passed EXPLICITLY, which is the whole of rule 2.
	return ConnEffect{
		Rows:   []Row{c.row(false, c.timerObs, true)},
		Status: []bool{false},
	}
}

// DeclaredOffline reports whether an outage has been declared and not yet
// recovered. Nothing reads it yet; the alert port will.
func (c *Connectivity) DeclaredOffline() bool { return c.declaredOffline }
