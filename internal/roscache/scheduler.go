package roscache

// One goroutine per router, servicing the demand set.
//
// ── WHAT THIS REPLACES ──────────────────────────────────────────────────────
//
// Today every collector owns a timer: 30 poll loops across 25 files, each
// deciding for itself when to read, and five separate gates deciding whether
// that decision counts. Phase 3 of the collector rewrite inverts it. The demand
// set in demand.go says WHAT is wanted and how fresh; this says WHEN, once, for
// the whole router.
//
// ── THE GOROUTINE COUNT IS THE STATED PAYOFF, AND IT IS THE SMALLER HALF ────
//
// The plan asks for a count that is constant per router rather than a multiple,
// and `TestSchedulerIsOneGoroutinePerRouter` asserts it. Be honest about what
// that is worth on its own: Go does not care about thirty goroutines, and a
// fleet of ten routers goes from about three hundred to ten. Nobody would notice.
//
// THE REAL PAYOFF IS THAT "SHOULD THIS RUN" GETS ONE ANSWER. A menu is fetched
// when something is subscribed to it and not otherwise, which is the same
// question the idle gate, the page-room gate and dormancy each answer separately
// today, in three places that have already disagreed once this month.
//
// ── SET A ONLY ──────────────────────────────────────────────────────────────
//
// This schedules table reads. Measurements and streams keep their own timing and
// their own channels: a stream has no cadence to schedule, and a measurement's
// moment belongs to whoever asked for it. See internal/collect/acquisition.go.

import (
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// Scheduler services one cache's demand set on one goroutine.
//
// NOT SAFE TO COPY, and Start is not idempotent: a second Start would be a
// second goroutine, which is the exact thing this exists to prevent.
type Scheduler struct {
	c *Cache
	// tick is how often the demand set is re-read, NOT how often a menu is
	// fetched — each menu is fetched at its own cadence. It bounds how late a
	// newly subscribed menu can be picked up, so it wants to be shorter than the
	// shortest cadence anybody asks for, and no shorter than that.
	tick time.Duration

	mu      sync.Mutex
	stop    chan struct{}
	done    chan struct{}
	lastRun map[string]time.Time
}

// NewScheduler builds one. A zero tick takes the default.
func NewScheduler(c *Cache, tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = 250 * time.Millisecond
	}
	return &Scheduler{c: c, tick: tick, lastRun: map[string]time.Time{}}
}

// Start runs the loop. Calling it twice is a no-op rather than a second
// goroutine.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	s.stop, s.done = stop, done
	s.mu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(s.tick)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.run(time.Now())
			}
		}
	}()
}

// Stop ends the loop and waits for it, so a caller tearing a session down knows
// no fetch is still in flight against a connection it is about to close.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// run services one pass. Exported behaviour is tested through this rather than
// through the ticker, so a test never has to sleep for a cadence to elapse.
func (s *Scheduler) run(now time.Time) {
	for _, d := range s.c.Demand() {
		if d.Cadence <= 0 {
			// No cadence of its own: this subscriber takes whatever another one
			// keeps fresh, and scheduling it here would invent a rate nobody
			// asked for. See the zero-cadence rule in demand.go.
			continue
		}
		if last, ok := s.lastRun[d.Menu]; ok && now.Sub(last) < d.Cadence {
			continue
		}
		s.lastRun[d.Menu] = now

		// ── A ROUND THE STREAM HAS JUST DELIVERED IS NOT DELIVERED AGAIN ────
		//
		// Since rounds are delivered as they complete (deliverStreamed), this
		// tick answering a streamed menu from its snapshot handed every
		// subscriber the SAME round a second time. For a consumer that
		// differences readings that is not harmless repetition: the second
		// reading minus the first is zero. MEASURED on the hAP AX3,
		// 2026-09-21: the Bandwidth page's rates alternated between the real
		// figures and zero on every cell, and so did the Connections List.
		//
		// So a menu whose stream delivered a round within the last cadence
		// and a half is skipped: its subscribers already have it. The half is
		// slack for a round that closes a little after the tick. A QUIET stream,
		// an emptied table that publishes no rounds, still gets this tick every
		// cadence, which is the heartbeat dormancy and the collectors' re-emit
		// rules ride (TestAStreamedMenuStillFiresOnDeliver): the tick is
		// withheld only when a delivery just happened anyway.
		if s.c.deliveredRoundSince(d.Menu, now.Add(-d.Cadence-d.Cadence/2)) {
			continue
		}

		// ── INVALIDATE FIRST, AND THAT IS NOT BELT AND BRACES ───────────────
		//
		// TWO CLOCKS MUST NOT DECIDE THE SAME THING. This loop has just decided
		// the menu is due; Get would then apply the entry's own TTL and, since
		// that TTL is the shortest cadence anybody asked for, find the value
		// fresh by a hair and decline. The menu would refresh every SECOND
		// cadence, or on a slow tick not at all — and the reads would still look
		// perfectly ordinary in a log.
		//
		// So the scheduler is authoritative for a menu it schedules. The TTL
		// keeps its old job, which is a different one: letting two PULL callers
		// share a read they both happened to want at once.
		//
		// Through Get rather than around it, because Get holds the single-flight
		// and the union field list; fetching directly would open a second read of
		// a menu a collector was already waiting on.
		s.c.Invalidate(d.Menu)
		rows, err := s.c.Get(d.Menu, d.Fields, d.Cadence)
		s.c.deliver(d.Menu, rows, err)
	}

	// A menu nobody wants any more must not keep a slot: on a long-running
	// session the map would otherwise grow with every page ever opened.
	live := map[string]bool{}
	for _, d := range s.c.Demand() {
		live[d.Menu] = true
	}
	for menu := range s.lastRun {
		if !live[menu] {
			delete(s.lastRun, menu)
		}
	}
}

// deliver hands a refreshed menu to its subscribers.
//
// CALLBACKS RUN OUTSIDE THE LOCK, and the list is copied first. A subscriber is
// free to release itself from inside its own callback -- a collector that has
// just been told the menu is gone would reasonably do that -- and holding the
// lock across it would deadlock on the release.
func (c *Cache) deliver(menu string, rows []routeros.Reply, err error) {
	c.demandMu.Lock()
	fns := make([]func([]routeros.Reply, error), 0, len(c.subs[menu]))
	for _, s := range c.subs[menu] {
		if s.onRows != nil {
			fns = append(fns, s.onRows)
		}
	}
	c.demandMu.Unlock()

	for _, fn := range fns {
		fn(rows, err)
	}

	c.demandMu.Lock()
	after := c.onDeliver
	c.demandMu.Unlock()
	if after != nil {
		after(menu)
	}
}

// OnDeliver registers one callback fired after every scheduled refresh, whatever
// menu it was and whoever consumed it.
//
// ── WHY THIS EXISTS: DORMANCY NEEDED A HEARTBEAT, NOT A CLOCK ───────────────
//
// The dormancy supervisor ran on a 15-second ticker per session. It judges
// PAYLOADS, so it has to run after collectors have produced -- but a collector
// only EMITS when its fingerprint changes, and a collector reporting nothing
// over and over emits once and then goes quiet. So `emit` is the wrong signal
// and a clock was the fallback.
//
// A DELIVERY IS THE RIGHT SIGNAL. It happens whether or not the answer changed,
// it happens at a rate the collectors themselves declared, and it stops when
// nothing is subscribed -- which is exactly when there is nothing to judge. One
// hook here replaces a goroutine per session and needs nothing from the
// nineteen collectors.
//
// Called with the cache's lock RELEASED, like the callbacks above and for the
// same reason: the handler resumes and suspends collectors, which reaches back
// into this cache.
func (c *Cache) OnDeliver(fn func(menu string)) {
	c.demandMu.Lock()
	c.onDeliver = fn
	c.demandMu.Unlock()
}
