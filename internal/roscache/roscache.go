// Package roscache coalesces RouterOS reads, so two collectors asking the same
// menu in the same moment cost ONE command against the router.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// Measured across `internal/collect`: 72 distinct RouterOS commands, 98 issued
// per full sweep, so 26 are redundant. Eighteen menus have more than one
// consumer — `/interface/wifi/registration-table` has four, `/ip/address` has
// three, `/ip/firewall/connection` has two that ask for the IDENTICAL fields.
//
// CLAUDE.md states the constraint this is written against: "more efficient means
// fewer router channels, not faster payload assembly". A hAP ac2 does not care
// about this process's memory; it cares how many API channels are open on it.
// So when a wider proplist on one round trip competes with two narrow ones, the
// single round trip wins every time, and this package is built on that trade.
//
// ── THE UNION PROPLIST, AND THE TRAP IN IT ──────────────────────────────────
//
// Consumers of one menu rarely want identical fields; usually one asks for a
// subset of another. The cache fetches the UNION and hands everyone the same
// rows, which is why one read can serve all of them.
//
// The trap: a consumer arriving later with a field nobody asked for widens the
// union, and the value already cached was fetched WITHOUT that field. Returning
// it would hand the newcomer rows silently missing the column it asked for —
// a wrong answer, not a slow one. Widening therefore INVALIDATES, and the next
// Get refetches. This costs one extra read on the first sweep after a new
// consumer appears and nothing thereafter.
//
// ── ROWS ARE SHARED AND MUST NOT BE MUTATED ─────────────────────────────────
//
// `routeros.Reply` is a `map[string]string`. Copying every row for every
// consumer would trade the router channels we just saved for allocations and
// garbage, which is the wrong direction on a box already doing this work for a
// fleet. So the same slice, and the same maps inside it, are handed to every
// caller.
//
// That is a real hazard and it is guarded rather than hoped for: one consumer
// writing to a row it was handed would corrupt every other consumer of that
// menu, and it would surface as an unrelated page showing wrong data. See
// TestRowsAreSharedNotCopied, which pins the sharing, and the note on Get.
package roscache

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mikrodash/internal/routeros"
)

// Reader is the half of internal/collect.Reader this needs. Declared here so the
// cache does not import the collectors it sits underneath.
type Reader interface {
	Do(routeros.Cmd) ([]routeros.Reply, error)
}

// errTTL bounds how long a failure is remembered.
//
// SHORTER THAN A SUCCESS, deliberately. A menu that traps — the usual cause is a
// RouterOS build without that feature — should not be re-asked every time a
// collector ticks, or the saving this package exists for is spent on a question
// with a known answer. But it must not be remembered for a whole poll interval
// either: a router that was briefly unreachable would then look broken for far
// longer than it was.
const errTTL = 2 * time.Second

type result struct {
	rows []routeros.Reply // SHARED. Never mutated after it is stored.
	at   time.Time
	err  error
}

type entry struct {
	fields   map[string]bool // the union, as a set; nil means "every field"
	proplist string          // the union rendered once, not per fetch
	ttl      time.Duration   // the shortest cadence any consumer asked for
	val      atomic.Pointer[result]
	inflight chan struct{} // non-nil while a fetch is running; closed when done
}

// Cache is one router's coalescing reader. Not safe to copy.
type Cache struct {
	ros Reader

	mu      sync.Mutex
	entries map[string]*entry
	// fills are the menus kept current by an open channel rather than by a read.
	// Guarded by `mu`, like `entries`, because `Get` consults both. See stream.go.
	fills map[string]*streamFill
	// streamWhen answers "may this menu be streamed". Set once per session by
	// the caller, which is the only thing that knows which collector owns a menu
	// and what the router's config says about it. See StreamWhen.
	streamWhen func(menu string) bool
	// checkOver and staleOver override the stream watchdog's timings. Zero in
	// production; see StreamTimings.
	checkOver, staleOver time.Duration

	// The demand set, on its own lock. SEPARATE FROM `mu` deliberately: `Get`
	// holds `mu` and must never wait on a page opening or closing, and a
	// subscription change must never wait on a router read. See demand.go.
	demandMu sync.Mutex
	subs     map[string]map[uint64]subscription
	nextSub  uint64
	// onDeliver is the after-refresh heartbeat. See OnDeliver in scheduler.go.
	onDeliver func(menu string)
	// roundAt is when each streamed menu last delivered a round of its own
	// (deliverStreamed), so the scheduler does not deliver that round again.
	// Under demandMu. See Scheduler.run.
	roundAt map[string]time.Time
}

func New(ros Reader) *Cache {
	return &Cache{ros: ros, entries: map[string]*entry{}}
}

// Get returns the rows for a menu, fetching only if nothing fresh is held.
//
// `fields` is this caller's proplist; the cache asks the router for the union of
// every caller's fields. `ttl` is how stale this caller will tolerate; the entry
// keeps the SHORTEST any caller asked for, because a consumer polling at one
// second must not be served a five-second-old answer.
//
// THE RETURNED ROWS ARE SHARED WITH EVERY OTHER CALLER. Treat them as read-only.
// Mutating one corrupts the others, and the symptom appears on a page that never
// touched the data.
//
// An empty `fields` means "every field", which suppresses the proplist entirely
// and widens the entry permanently. That is correct but expensive, so callers
// should name their fields.
func (c *Cache) Get(menu string, fields []string, ttl time.Duration) ([]routeros.Reply, error) {
	// ── A STREAM-FILLED MENU IS ALREADY CURRENT ─────────────────────────────
	//
	// No read, no TTL, no single-flight: the rows were pushed and the entry is
	// as fresh as the last thing the router sent. This one branch is what lets
	// the scheduler stay UNCHANGED -- it still calls Invalidate then Get then
	// deliver, and for a streamed menu the first is inert and the second
	// answers from the rolling map. Phase B is additive for that reason.
	// AND ONLY WHEN IT HAS WARMED UP. An open channel that has not yet delivered
	// its first row must not answer "no rows": see hasRows.
	if f := c.fillFor(menu); f != nil && f.authoritative() {
		return f.snapshot(), nil
	}
	for {
		e, wait, fresh := c.claim(menu, fields, ttl)

		if fresh != nil {
			return fresh.rows, fresh.err
		}
		if wait != nil {
			// Somebody else is already asking the router. Wait for their answer
			// rather than opening a second channel for the same question, then
			// loop: their result may have been fetched with a narrower proplist
			// than we need, in which case `claim` invalidates and we fetch.
			<-wait
			continue
		}

		// We hold the fetch. NOT under the lock: a read takes as long as the
		// router takes, and holding the mutex across it would serialise every
		// consumer of every menu behind the slowest one.
		cmd := routeros.Cmd{Path: menu}
		c.mu.Lock()
		if e.proplist != "" {
			cmd.Args = []string{"=.proplist=" + e.proplist}
		}
		c.mu.Unlock()

		rows, err := c.ros.Do(cmd)
		r := &result{rows: rows, at: time.Now(), err: err}

		c.mu.Lock()
		e.val.Store(r)
		close(e.inflight)
		e.inflight = nil
		c.mu.Unlock()

		return r.rows, r.err
	}
}

// claim decides, under one lock, what the caller should do next: use a fresh
// value, wait for somebody else's fetch, or perform the fetch itself.
//
// Split out so the locking is readable in one place rather than spread through
// Get's control flow, and so the "widening invalidates" rule has one home.
func (c *Cache) claim(menu string, fields []string, ttl time.Duration) (e *entry, wait chan struct{}, fresh *result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[menu]
	if !ok {
		e = &entry{fields: map[string]bool{}, ttl: ttl}
		c.entries[menu] = e
	}

	if ttl > 0 && (e.ttl <= 0 || ttl < e.ttl) {
		e.ttl = ttl // the shortest tolerance wins
	}

	if e.widen(fields) {
		// The held value was fetched without a field somebody now needs. Drop it
		// rather than answer with a missing column.
		e.val.Store(nil)
	}

	if v := e.val.Load(); v != nil {
		age, life := time.Since(v.at), e.ttl
		if v.err != nil && life > errTTL {
			life = errTTL
		}
		if age < life {
			return e, nil, v
		}
	}

	if e.inflight != nil {
		return e, e.inflight, nil
	}
	e.inflight = make(chan struct{})
	return e, nil, nil
}

// widen adds fields to the union and reports whether it grew. Caller holds c.mu.
//
// An empty `fields` means "all fields": the proplist is dropped and can never
// come back, because a narrower one would stop serving the caller that wanted
// everything.
func (e *entry) widen(fields []string) bool {
	if e.fields == nil {
		return false // already "everything"
	}
	if len(fields) == 0 {
		e.fields, e.proplist = nil, ""
		return true
	}
	grew := false
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" && !e.fields[f] {
			e.fields[f] = true
			grew = true
		}
	}
	if grew {
		// Rendered once here, not on every fetch. Sorted so the command is
		// stable, which makes a captured fixture reproducible.
		out := make([]string, 0, len(e.fields))
		for f := range e.fields {
			out = append(out, f)
		}
		sort.Strings(out)
		e.proplist = strings.Join(out, ",")
	}
	return grew
}

// Invalidate drops a menu's cached value, so the next Get refetches.
//
// For a write path: having just changed a firewall rule, the rows this cache
// holds are known to be stale, and waiting out the TTL would show the operator
// their own edit failing to appear.
func (c *Cache) Invalidate(menu string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[menu]; e != nil {
		e.val.Store(nil)
	}
}

// Reset drops everything. For a reconnect: the connection these rows came from
// is gone, and their age says nothing about the new one.
func (c *Cache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*entry{}
}
