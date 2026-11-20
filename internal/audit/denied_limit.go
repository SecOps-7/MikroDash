package audit

// Refused attempts are recorded, but a client cannot flood the trail with them
// (the operator's ruling of 2026-11-20, docs/DECISIONS.md).
//
// ── ONE LIMITER, KEYED BY WHO AND WHERE ─────────────────────────────────────
//
// The HTTP middleware that records refused writes, the socket handlers that
// record their own refusals and the share links' `share.denied` rows all go
// through the same DeniedLimiter. Its budget is per ACTOR AT AN ADDRESS (the
// user's id, or their name when there is no id, plus the client address), not
// per address alone: behind a proxy MikroDash does not trust, every visitor
// shares one address, and an anonymous flood must not use up a signed-in
// user's budget and hide their refusals.
//
// Per actor and address, in a fixed one-minute window:
//
//	at most 1 row per action and target (a refused delete of router A and one
//	of router B are two rows)
//	at most 30 rows overall
//
// ── NOTHING SUPPRESSED IS LOST ──────────────────────────────────────────────
//
// A refusal over either limit is not written individually but COUNTED, per
// action, under its actor. When the window closes (the actor's next refusal,
// the server's minute flush, or eviction under memory pressure) a SUMMARY is
// handed to the onSummary callback, which writes one row for that actor naming
// the actions and how many of each were not written. A window with counts
// still pending is never dropped without its summary.
//
// Memory is bounded: at most maxDeniedKeys actor-address pairs are tracked;
// when full, expired windows go first (their summaries emitted), then the
// oldest (summary emitted first).

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	deniedWindow    = time.Minute
	deniedPerTarget = 1
	deniedPerKey    = 30
	maxDeniedKeys   = 4096
)

// DeniedSummary reports the refusals one actor had counted and not written in
// one window.
type DeniedSummary struct {
	Actor        Actor
	Counts       map[string]int // action -> refusals not written
	Since, Until time.Time
}

// Total is the number of refusals the summary stands for.
func (d DeniedSummary) Total() int {
	n := 0
	for _, c := range d.Counts {
		n += c
	}
	return n
}

// Note is the summary's text: "12 refused attempts not recorded one by one:
// DELETE /api/routers/{id} x10, POST /api/users x2".
func (d DeniedSummary) Note() string {
	actions := make([]string, 0, len(d.Counts))
	for a := range d.Counts {
		actions = append(actions, a)
	}
	sort.Strings(actions)
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		parts = append(parts, a+" x"+strconv.Itoa(d.Counts[a]))
	}
	return strconv.Itoa(d.Total()) + " refused attempts not recorded one by one: " +
		strings.Join(parts, ", ")
}

// DeniedLimiter decides whether one more denied row may be written. The zero
// value is not usable; use NewDeniedLimiter. A nil *DeniedLimiter allows
// everything.
type DeniedLimiter struct {
	mu        sync.Mutex
	now       func() time.Time
	onSummary func(DeniedSummary)
	windows   map[string]*deniedWindowState
}

type deniedWindowState struct {
	actor   Actor
	start   time.Time
	total   int
	targets map[string]int // action + "\x00" + target -> rows written
	pending map[string]int // action -> refusals counted, not written
}

// NewDeniedLimiter builds a limiter on the given clock (time.Now in
// production). onSummary receives the summary of every window that closes
// with refusals counted; it is called outside the limiter's lock.
func NewDeniedLimiter(now func() time.Time, onSummary func(DeniedSummary)) *DeniedLimiter {
	return &DeniedLimiter{now: now, onSummary: onSummary, windows: map[string]*deniedWindowState{}}
}

func deniedKey(a Actor) string {
	who := a.ID
	if who == "" {
		who = "name:" + a.Name
	}
	return who + "\x00" + a.IP
}

// Allow reports whether a denied row for this actor, action and target may be
// written now. A refused call is counted toward the actor's summary.
func (l *DeniedLimiter) Allow(a Actor, action, target string) bool {
	if l == nil {
		return true
	}
	var out []DeniedSummary
	defer func() { l.emit(out) }()

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	k := deniedKey(a)
	w := l.windows[k]
	if w != nil && now.Sub(w.start) >= deniedWindow {
		if s, ok := w.summary(now); ok {
			out = append(out, s)
		}
		delete(l.windows, k)
		w = nil
	}
	if w == nil {
		out = append(out, l.evictLocked(now)...)
		w = &deniedWindowState{actor: a, start: now, targets: map[string]int{}, pending: map[string]int{}}
		l.windows[k] = w
	}
	t := action + "\x00" + target
	if w.targets[t] >= deniedPerTarget || w.total >= deniedPerKey {
		w.pending[action]++
		return false
	}
	w.targets[t]++
	w.total++
	return true
}

// Flush closes every window that has run its minute, emitting the summary of
// each that counted refusals. The server calls it once a minute and at
// shutdown (with force, so a clean stop reports everything pending).
func (l *DeniedLimiter) Flush(force bool) {
	if l == nil {
		return
	}
	var out []DeniedSummary
	l.mu.Lock()
	now := l.now()
	for k, w := range l.windows {
		if force || now.Sub(w.start) >= deniedWindow {
			if s, ok := w.summary(now); ok {
				out = append(out, s)
			}
			delete(l.windows, k)
		}
	}
	l.mu.Unlock()
	l.emit(out)
}

func (w *deniedWindowState) summary(now time.Time) (DeniedSummary, bool) {
	if len(w.pending) == 0 {
		return DeniedSummary{}, false
	}
	counts := make(map[string]int, len(w.pending))
	for a, n := range w.pending {
		counts[a] = n
	}
	return DeniedSummary{Actor: w.actor, Counts: counts, Since: w.start, Until: now}, true
}

// evictLocked keeps the map below maxDeniedKeys, returning the summaries of
// what it removes so none is lost.
func (l *DeniedLimiter) evictLocked(now time.Time) []DeniedSummary {
	if len(l.windows) < maxDeniedKeys {
		return nil
	}
	var out []DeniedSummary
	for k, w := range l.windows {
		if now.Sub(w.start) >= deniedWindow {
			if s, ok := w.summary(now); ok {
				out = append(out, s)
			}
			delete(l.windows, k)
		}
	}
	for len(l.windows) >= maxDeniedKeys {
		var oldest string
		var at time.Time
		for k, w := range l.windows {
			if oldest == "" || w.start.Before(at) {
				oldest, at = k, w.start
			}
		}
		if s, ok := l.windows[oldest].summary(now); ok {
			out = append(out, s)
		}
		delete(l.windows, oldest)
	}
	return out
}

func (l *DeniedLimiter) emit(out []DeniedSummary) {
	if l.onSummary == nil {
		return
	}
	for _, s := range out {
		l.onSummary(s)
	}
}

// tracked is the number of actor-address pairs held, for tests.
func (l *DeniedLimiter) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.windows)
}
