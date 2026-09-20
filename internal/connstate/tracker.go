// Package connstate decides whether a router is OFFLINE, as opposed to whether
// its API socket happens to be open this instant.
//
// ── WHY IT IS NOT IN internal/historywire, WHERE IT USED TO LIVE ───────────
//
// The debounce — `connDownThresholdSec`, "Offline threshold" in the device
// dialog — was the connectivity half of the history wire, because recording an
// outage was the only thing that consumed it. Two things consume it now: the
// fleet's Online/Offline badge and the Router Offline / Online alert. Neither
// has anything to do with recording.
//
// That mattered immediately rather than eventually. `-history` DEFAULTS TO
// FALSE (`cmd/mikrodash/main.go`), and a disabled wire returned before touching
// the state machine — so on a default install the debounce never ran at all. A
// badge and an alert hung off that would have been silently dead on every
// install that had not opted into recording, which is the worst shape a
// dependency can have: invisible, and correct on the developer's machine.
//
// So the machine runs here, always, for every router. RECORDING IS GATED AT THE
// SINK instead: the rows this produces are offered to a callback, and the
// server drops them when the wire is off or the router's reporting is. That is
// also where the reporting check belongs — reporting decides what is WRITTEN,
// never what is true.
//
// ── THE RULES ARE STILL IN internal/history ────────────────────────────────
//
// `history.Connectivity` holds the four rules and their corpus, untouched by
// this move. This package owns only the per-router map, each router's
// threshold, and the fan-out.
package connstate

import (
	"sync"

	"mikrodash/internal/history"
)

// DefaultSec is the live default when a record carries no threshold.
const DefaultSec = 30

// MaxSec is the live upper clamp.
const MaxSec = 300

// ThresholdMs turns a record's `connDownThresholdSec` into milliseconds.
//
// `sec` is the value as read, and `ok` reports whether the record carried one at
// all — the two are different questions, and conflating them is how a deliberate
// zero becomes a thirty-second debounce.
func ThresholdMs(sec int, ok bool) int64 {
	if !ok || sec < 0 || sec > MaxSec {
		// Out of range is the DEFAULT, not a clamp to the bound: the live
		// expression is `(n >= 0 && n <= 300) ? n : 30`, so 500 becomes 30
		// rather than 300.
		return DefaultSec * 1000
	}
	return int64(sec) * 1000
}

type entry struct {
	mu sync.Mutex
	c  *history.Connectivity
}

// Tracker holds one state machine per router.
//
// `rows` receives what the machine decided to record; `verdict` receives every
// debounced Online/Offline transition. Either may be nil, which is what a test
// that only cares about the other passes.
type Tracker struct {
	mu sync.Mutex
	st map[string]*entry

	rows    func([]history.Row)
	verdict func(routerID string, up bool)
}

func New(rows func([]history.Row), verdict func(routerID string, up bool)) *Tracker {
	return &Tracker{st: map[string]*entry{}, rows: rows, verdict: verdict}
}

// SetThreshold declares one router's debounce, in milliseconds.
//
// ── THIS IS WHY THE THRESHOLD IS NOT AN ARGUMENT ANY MORE ──────────────────
//
// Every entry point used to take `threshMs`, and the one caller that mattered
// — the session — captured it when the session was BUILT and passed the same
// frozen copy for the router's whole life. A router held for alerting or
// recording is never rebuilt, so an operator who changed the Offline threshold
// saw the field save, the record update, and the running debounce go on using
// the old value until the process restarted.
//
// The state machine was blameless: it re-read the argument on every event, and
// `TestTheThresholdIsRereadOnEveryEvent` said so. The staleness was one level
// up, where nothing was looking. Holding the value HERE removes the argument
// that could go stale, and leaves exactly one writer: `declareConnThreshold`,
// called from the fleet sync that already runs on every router save.
func (t *Tracker) SetThreshold(routerID string, ms int64) {
	if t == nil || routerID == "" {
		return
	}
	e := t.entryFor(routerID)
	e.mu.Lock()
	e.c.ThreshMs = ms
	e.mu.Unlock()
}

// Connected records a router's API connection coming up.
func (t *Tracker) Connected(routerID string, now int64) {
	t.apply(routerID, func(c *history.Connectivity) history.ConnEffect {
		return c.Connected(now)
	})
}

// Disconnected records a close or a connection error. ONE method for both,
// because the live app has one handler for both.
func (t *Tracker) Disconnected(routerID string, now int64) {
	t.apply(routerID, func(c *history.Connectivity) history.ConnEffect {
		return c.Disconnected(now)
	})
}

// Online reports the debounced verdict, and whether this router has one yet.
//
// `known` is false until the machine has observed something, which is not the
// same as "down" — a router the app has never reached must not be painted
// Offline, and `routers.Summary.Known` carries that distinction to the page.
func (t *Tracker) Online(routerID string) (up, known bool) {
	if t == nil {
		return false, false
	}
	t.mu.Lock()
	e := t.st[routerID]
	t.mu.Unlock()
	if e == nil {
		return false, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.c.Online()
}

// Forget drops a router's state.
//
// ── ONLY WHEN THE ROUTER IS GONE, NEVER ON A DISCONNECT ────────────────────
//
// The state is what distinguishes "never observed" from "was up, now down", and
// rule 1 writes only on a transition. Dropping it when a session ends would make
// the next connect look like a first sighting and write a spurious "up" row for
// a router that had never been recorded down.
func (t *Tracker) Forget(routerID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.st, routerID)
	t.mu.Unlock()
}

// TickAll advances every tracked router's debounce.
//
// `history.Connectivity` holds no timer of its own — deliberately, so its rules
// are testable without one — and `Tick` is how the caller supplies the passage
// of time. Nothing called it for most of this port's life, so a non-zero
// threshold could never fire and the only workable setting was zero: record
// every close, immediately. That is what made a routine six-second reconnect
// appear in the Reports page as an outage.
//
// Ticking EVERY router rather than the ones with a pending timer: the state
// machine returns nothing for a router with no timer running, the map is one
// entry per router, and a filter would be a second place to decide what is
// pending.
func (t *Tracker) TickAll(now int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	ids := make([]string, 0, len(t.st))
	states := make([]*entry, 0, len(t.st))
	for id, e := range t.st {
		ids = append(ids, id)
		states = append(states, e)
	}
	t.mu.Unlock()

	var rows []history.Row
	for i, e := range states {
		e.mu.Lock()
		eff := e.c.Tick(now)
		e.mu.Unlock()
		rows = append(rows, eff.Rows...)
		t.announce(ids[i], eff.Status)
	}
	// ONE call for the whole sweep. Each row is its own INSERT inside the
	// store's transaction, and a fleet-wide tick that opened one transaction per
	// router would be the same work in more of them.
	t.emit(rows)
}

func (t *Tracker) entryFor(routerID string) *entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.st[routerID]
	if e == nil {
		e = &entry{c: &history.Connectivity{
			RouterID: routerID, ThreshMs: DefaultSec * 1000,
		}}
		t.st[routerID] = e
	}
	return e
}

// apply runs one event, then fans out. THE CALLBACKS RUN UNLOCKED: `verdict`
// reaches the alert evaluator and the hub, and holding a router's state machine
// across either would put this package's lock underneath both.
func (t *Tracker) apply(routerID string, fn func(*history.Connectivity) history.ConnEffect) {
	if t == nil || routerID == "" {
		return
	}
	e := t.entryFor(routerID)
	e.mu.Lock()
	eff := fn(e.c)
	e.mu.Unlock()

	t.emit(eff.Rows)
	t.announce(routerID, eff.Status)
}

func (t *Tracker) emit(rows []history.Row) {
	if len(rows) > 0 && t.rows != nil {
		t.rows(rows)
	}
}

func (t *Tracker) announce(routerID string, status []bool) {
	if t.verdict == nil {
		return
	}
	for _, up := range status {
		t.verdict(routerID, up)
	}
}
