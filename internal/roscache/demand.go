package roscache

// The demand set: who currently wants which menu, and how fresh.
//
// ── WHAT THIS IS FOR ────────────────────────────────────────────────────────
//
// Today a collector owns a timer and decides for itself whether to read, and
// five separate gates decide whether that decision counts. Phase 3 of
// Collectors-Rewrite.md inverts it: a query runs if and only if something
// currently wants it, and "should this run" has ONE answer instead of five.
//
// This file is the bookkeeping half. It records demand and computes what the
// active set is. It fetches nothing: the scheduler that consumes this arrives in
// 3.2, and until then the cache stays purely pull-driven through Get.
//
// ── SET A ONLY, AND THAT IS NOT AN OMISSION ─────────────────────────────────
//
// A subscription is keyed by menu and field list, which is a TABLE READ's key.
// Measurements and streams -- interface rates, pings, the log listener -- are
// keyed by their target and their moment, and a stream has no result to key at
// all. They are not modelled here and must not be forced in: collapsing both
// behind one abstraction is exactly what made phase 1's first premise wrong. See
// `internal/collect/acquisition.go`.
//
// ── THE UNION MUST SHRINK, WHICH IS NEW ─────────────────────────────────────
//
// `entry.widen` never shrinks, and that is right for what it serves: the callers
// of Get are collectors that live as long as the session, so their union only
// grows because their number only grows.
//
// SUBSCRIPTIONS DO NOT WORK THAT WAY. They come and go with pages and viewers,
// so a monotonic union would mean a page opened once and closed makes every later
// read of that menu carry its columns for ever. So this keeps each subscriber's
// fields and recomputes the union when one leaves.
//
// That is why there is a subscriber LIST here and not the bare per-menu refcount
// the plan's Go note suggests. A count cannot shrink a union. What the note is
// really protecting is the hot path, and that is untouched: `Get` never reads any
// of this, so a subscription change cannot contend with a read.

import (
	"sort"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// Demand is one menu's live demand.
type Demand struct {
	Menu string
	// Fields is the union of every subscriber's field list, sorted. NIL MEANS
	// EVERY FIELD, matching the convention Get uses: one subscriber wanting
	// everything makes the union everything, and no narrower list can shrink it
	// back while that subscriber is live.
	Fields []string
	// Cadence is the SHORTEST any subscriber asked for, for the same reason the
	// cache keeps the shortest TTL: a consumer that needs a value every second
	// must not be paced by one that would tolerate a minute.
	Cadence time.Duration
}

type subscription struct {
	fields   []string
	allField bool
	// cadence is asked every time Demand runs, so a re-tuned collector is read
	// at its new interval from the next pass. It was a number taken at
	// Subscribe, and a slider moved on a live session changed what the page
	// reported and not how often the router was read (review 2026-09-19).
	cadence func() time.Duration
	// onRows is fired by the scheduler after it refreshes this menu. Nil for a
	// subscriber that only wants to keep the menu in the active set -- declaring
	// demand and consuming a result are separate things, and a view that renders
	// from another view's output needs the first without the second.
	onRows func([]routeros.Reply, error)
}

// Subscribe registers demand for a menu and returns the release for it.
//
// The release is IDEMPOTENT. A caller that releases twice -- a page torn down on
// both a blur and a disconnect, which this app does routinely -- must not remove
// a second, still-live subscriber's demand.
//
// An empty field list means "every field", and a nil cadence (or one answering
// zero) means "no cadence of my own": it never shortens the menu's cadence, so a
// consumer that just wants whatever others keep fresh can say so. That mirrors
// the zero TTL rule at `dhcpLeases`' call site in internal/collect.
//
// THE CADENCE IS A FUNCTION, asked on every Demand, so a collector's live
// interval is what the scheduler reads. Every(d) is a fixed one.
func (c *Cache) Subscribe(menu string, fields []string, cadence func() time.Duration,
	onRows func([]routeros.Reply, error)) (release func()) {
	c.demandMu.Lock()
	defer c.demandMu.Unlock()

	if c.subs == nil {
		c.subs = map[string]map[uint64]subscription{}
	}
	if c.subs[menu] == nil {
		c.subs[menu] = map[uint64]subscription{}
	}
	c.nextSub++
	id := c.nextSub
	c.subs[menu][id] = subscription{
		fields:   append([]string(nil), fields...),
		allField: len(fields) == 0,
		cadence:  cadence,
		onRows:   onRows,
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			c.demandMu.Lock()
			defer c.demandMu.Unlock()
			delete(c.subs[menu], id)
			if len(c.subs[menu]) == 0 {
				// The menu leaves the active set entirely rather than lingering
				// with an empty map, so `Demand` needs no emptiness check and a
				// caller cannot mistake "wanted by nobody" for "wanted with no
				// fields".
				delete(c.subs, menu)
			}
		})
	}
}

// Demand returns the active set: one entry per menu that somebody currently
// wants. A menu with no subscribers is absent, which is the whole point.
//
// Sorted by menu so two calls can be diffed, and so a scheduler's log is stable.
func (c *Cache) Demand() []Demand {
	c.demandMu.Lock()
	defer c.demandMu.Unlock()

	out := make([]Demand, 0, len(c.subs))
	for menu, subs := range c.subs {
		d := Demand{Menu: menu}
		union := map[string]bool{}
		all := false
		for _, s := range subs {
			if s.allField {
				all = true
			}
			for _, f := range s.fields {
				union[f] = true
			}
			cad := time.Duration(0)
			if s.cadence != nil {
				cad = s.cadence()
			}
			if cad > 0 && (d.Cadence <= 0 || cad < d.Cadence) {
				d.Cadence = cad
			}
		}
		if !all {
			d.Fields = make([]string, 0, len(union))
			for f := range union {
				d.Fields = append(d.Fields, f)
			}
			sort.Strings(d.Fields)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Menu < out[j].Menu })
	return out
}

// Every is a fixed cadence for Subscribe.
func Every(d time.Duration) func() time.Duration { return func() time.Duration { return d } }

// deliverStreamed hands a menu's newly completed round to its subscribers,
// shaped exactly as the scheduler shapes a refresh.
//
// ── THE SAME PATH, ON THE DATA'S CLOCK RATHER THAN THE SCHEDULER'S ─────────
//
// `Get` on a stream-filled entry answers from the last complete round and sends
// the router nothing, so this costs one map read and whatever the subscriber
// does with it. A menu nobody subscribes to is not delivered: the fill may be
// held open for a row hook alone (traffic), and inventing a delivery for it
// would run a derive nobody asked for.
func (c *Cache) deliverStreamed(menu string) {
	for _, d := range c.Demand() {
		if d.Menu != menu {
			continue
		}
		rows, err := c.Get(menu, d.Fields, d.Cadence)
		c.demandMu.Lock()
		if c.roundAt == nil {
			c.roundAt = map[string]time.Time{}
		}
		c.roundAt[menu] = time.Now()
		c.demandMu.Unlock()
		c.deliver(menu, rows, err)
		return
	}
}

// deliveredRoundSince reports whether a streamed round of this menu was
// delivered after `since`.
func (c *Cache) deliveredRoundSince(menu string, since time.Time) bool {
	c.demandMu.Lock()
	defer c.demandMu.Unlock()
	at, ok := c.roundAt[menu]
	return ok && at.After(since)
}
