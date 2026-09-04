package historywire

import (
	"sync"

	"mikrodash/internal/history"
)

// The connectivity half of the history wire.
//
// ── IT IS NOT DRIVEN BY THE EMIT SEAM, UNLIKE TRAFFIC AND PING ─────────────
//
// A connectivity row records a SESSION LIFECYCLE event — the router's API
// connection opening or closing — and there is no collector payload for that.
// So this half has three explicit entry points instead, matching the live
// handlers: `Connected`, `Disconnected` (which is one handler for both `close`
// and `connectionError` there and must stay one here) and `Tick`.
//
// ── THE DEBOUNCE IS THE WHOLE POINT, AND IT LIVES IN internal/history ──────
//
// `history.Connectivity` holds the four rules — a transition writes, the
// observed moment is captured when the link drops rather than when the timer
// fires, a first sighting writes immediately, and a threshold of ZERO really
// means zero. It is pinned there. This file owns only the per-router map, the
// threshold, and the persistence.
//
// THE THRESHOLD DEFAULTS TO 30 SECONDS and is clamped to 0-300, matching
// `src/routers.js:571` and `index.js:647`. Zero is a legitimate setting meaning
// "declare it down immediately", NOT an unset value to be replaced by the
// default — a port that treated it as unset would silently debounce a router
// whose operator asked for no debounce at all.

// ConnDefaultSec is the live default when a record carries no threshold.
const ConnDefaultSec = 30

// ConnMaxSec is the live upper clamp.
const ConnMaxSec = 300

// ThresholdMs turns a record's `connDownThresholdSec` into milliseconds.
//
// `sec` is the value as read, and `ok` reports whether the record carried one at
// all — the two are different questions, and conflating them is how a deliberate
// zero becomes a thirty-second debounce.
func ThresholdMs(sec int, ok bool) int64 {
	if !ok || sec < 0 || sec > ConnMaxSec {
		// Out of range is the DEFAULT, not a clamp to the bound: the live
		// expression is `(n >= 0 && n <= 300) ? n : 30`, so 500 becomes 30
		// rather than 300.
		return ConnDefaultSec * 1000
	}
	return int64(sec) * 1000
}

type connState struct {
	mu sync.Mutex
	c  *history.Connectivity
}

// Connected records a router's API connection coming up.
//
// Returns the statuses to broadcast, so the caller can emit `router:status`
// without this package importing the hub. Empty when nothing changed — the live
// rule is that only a real TRANSITION writes, so a reconnect that was never seen
// to drop produces no row and no status.
func (w *Wire) Connected(routerID string, threshMs, now int64) []bool {
	return w.apply(routerID, threshMs, func(c *history.Connectivity) history.ConnEffect {
		return c.Connected(now)
	})
}

// Disconnected records a close or a connection error. ONE method for both,
// because the live app has one handler for both.
func (w *Wire) Disconnected(routerID string, threshMs, now int64) []bool {
	return w.apply(routerID, threshMs, func(c *history.Connectivity) history.ConnEffect {
		return c.Disconnected(now)
	})
}

// Tick advances a router's debounce. The caller drives it; the state machine
// holds no timer of its own, which is what makes it testable without one.
func (w *Wire) Tick(routerID string, threshMs, now int64) []bool {
	return w.apply(routerID, threshMs, func(c *history.Connectivity) history.ConnEffect {
		return c.Tick(now)
	})
}

// Forget drops a router's connectivity state.
//
// ── ONLY WHEN THE ROUTER IS GONE, NEVER ON A DISCONNECT ────────────────────
//
// The state is what distinguishes "never observed" from "was up, now down", and
// rule 1 writes only on a transition. Dropping it when a session ends would make
// the next connect look like a first sighting and write a spurious "up" row for
// a router that had never been recorded down.
func (w *Wire) Forget(routerID string) {
	if w == nil {
		return
	}
	w.connMu.Lock()
	delete(w.conns, routerID)
	w.connMu.Unlock()
}

func (w *Wire) apply(routerID string, threshMs int64,
	fn func(*history.Connectivity) history.ConnEffect) []bool {

	if w == nil || !w.enabled || routerID == "" {
		return nil
	}
	w.connMu.Lock()
	st := w.conns[routerID]
	if st == nil {
		st = &connState{c: &history.Connectivity{RouterID: routerID, ThreshMs: threshMs}}
		w.conns[routerID] = st
	}
	// THE THRESHOLD IS REFRESHED ON EVERY CALL, not captured at first sight: an
	// operator can change it while a session is live, and the live code reads it
	// per event for the same reason.
	st.c.ThreshMs = threshMs
	w.connMu.Unlock()

	st.mu.Lock()
	e := fn(st.c)
	st.mu.Unlock()

	w.persist(e.Rows)
	return e.Status
}

// TickAll advances every tracked router's debounce.
//
// ── THE DEBOUNCE NEEDS A CLOCK, AND HAD NONE ───────────────────────────────
//
// `history.Connectivity` holds no timer of its own — deliberately, so its rules
// are testable without one — and `Tick` is how the caller supplies the passage
// of time. Nothing called it, so a non-zero threshold could never fire and the
// only workable setting was zero: record every close, immediately.
//
// That is what made a routine six-second reconnect appear in the Reports page
// as an outage. The live app debounces with `connDownThresholdSec`, default 30
// seconds, and a blip shorter than that never reaches the database at all.
//
// Ticking EVERY router rather than the ones with a pending timer: the state
// machine returns nothing for a router with no timer running, the map is one
// entry per router, and a filter would be a second place to decide what is
// pending.
func (w *Wire) TickAll(now int64) {
	if w == nil || !w.enabled {
		return
	}
	w.connMu.Lock()
	states := make([]*connState, 0, len(w.conns))
	for _, st := range w.conns {
		states = append(states, st)
	}
	w.connMu.Unlock()

	var rows []history.Row
	for _, st := range states {
		st.mu.Lock()
		e := st.c.Tick(now)
		st.mu.Unlock()
		rows = append(rows, e.Rows...)
	}
	// ONE persist for the whole sweep. Each row is its own INSERT inside the
	// store's transaction, and a fleet-wide tick that opened one transaction per
	// router would be the same work in more of them.
	w.persist(rows)
}
