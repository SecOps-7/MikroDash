package audit

// Refused attempts are recorded, but a client cannot flood the trail with them
// (the operator's ruling of 2026-11-20, docs/DECISIONS.md).
//
// ── ONE LIMITER FOR EVERY DENIED ROW ────────────────────────────────────────
//
// The HTTP middleware that records refused writes, the socket handlers that
// record their own refusals and the share links' `share.denied` rows all go
// through the same DeniedLimiter, attached to the recorder, so one address has
// one budget however it reaches the server.
//
// Per client address, in a fixed one-minute window:
//
//	at most 1 row per action (the route, or the event's Action)
//	at most 30 rows overall
//
// A row that would exceed either is not written but COUNTED, and the next row
// written for that address carries "and N more refused" in its note, so a flood
// is visible as a number without filling the table.
//
// Memory is bounded: at most maxDeniedAddrs addresses are tracked; when full,
// expired windows go first and then the oldest.

import (
	"strconv"
	"sync"
	"time"
)

const (
	deniedWindow    = time.Minute
	deniedPerAction = 1
	deniedPerAddr   = 30
	maxDeniedAddrs  = 4096
)

// DeniedLimiter decides whether one more denied row may be written for an
// address. The zero value is not usable; use NewDeniedLimiter.
type DeniedLimiter struct {
	mu    sync.Mutex
	now   func() time.Time
	addrs map[string]*deniedWindowState
}

type deniedWindowState struct {
	start   time.Time
	total   int
	actions map[string]int
	// pending is how many rows were suppressed and not yet reported.
	pending int
}

// NewDeniedLimiter builds a limiter on the given clock (time.Now in production).
func NewDeniedLimiter(now func() time.Time) *DeniedLimiter {
	return &DeniedLimiter{now: now, addrs: map[string]*deniedWindowState{}}
}

// Allow reports whether a denied row for (addr, action) may be written now and,
// when it may, how many earlier rows for this address were suppressed and are
// reported with it. A refused call counts toward the next report.
func (l *DeniedLimiter) Allow(addr, action string) (ok bool, suppressed int) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w := l.addrs[addr]
	if w == nil || now.Sub(w.start) >= deniedWindow {
		pending := 0
		if w != nil {
			pending = w.pending
		}
		l.evictLocked(now)
		w = &deniedWindowState{start: now, actions: map[string]int{}, pending: pending}
		l.addrs[addr] = w
	}
	if w.actions[action] >= deniedPerAction || w.total >= deniedPerAddr {
		w.pending++
		return false, 0
	}
	w.actions[action]++
	w.total++
	n := w.pending
	w.pending = 0
	return true, n
}

// evictLocked keeps the map within maxDeniedAddrs: expired windows with
// nothing pending first, then the oldest windows.
func (l *DeniedLimiter) evictLocked(now time.Time) {
	if len(l.addrs) < maxDeniedAddrs {
		return
	}
	for a, w := range l.addrs {
		if now.Sub(w.start) >= deniedWindow && w.pending == 0 {
			delete(l.addrs, a)
		}
	}
	for len(l.addrs) >= maxDeniedAddrs {
		var oldest string
		var at time.Time
		for a, w := range l.addrs {
			if oldest == "" || w.start.Before(at) {
				oldest, at = a, w.start
			}
		}
		delete(l.addrs, oldest)
	}
}

// tracked is the number of addresses held, for tests.
func (l *DeniedLimiter) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.addrs)
}

// suppressedNote is the text appended to a row that reports suppressed ones.
func suppressedNote(n int) string {
	if n == 1 {
		return "and 1 more refused in the last minute"
	}
	return "and " + strconv.Itoa(n) + " more refused in the last minute"
}
