package session

// Running the dormancy supervisor.
//
// The decisions are in `internal/dormancy`, which performs nothing. This is the
// half that performs: it gathers what each eligible collector last produced,
// asks for a plan, and carries it out.
//
// ── THE PROBE HAS TWO SHAPES AND THIS SIDE PICKS ────────────────────────────
//
// `_probeCollector` prefers a collector's own `probe()`, which clears a
// capability latch that `resume()` deliberately honours, and otherwise resumes
// AND refreshes so the answer arrives on this tick rather than one poll interval
// later. Which of those a collector gets is knowledge about collectors, so it
// lives here and not in the decision package.
//
// This port's collectors have no `probe()`, so every probe takes the second
// path. Recorded rather than assumed: if one ever grows a probe, `prober` is
// where it is noticed.

import (
	"log"
	"mikrodash/internal/hub"
	"time"

	"mikrodash/internal/collection"
	"mikrodash/internal/dormancy"
)

// dormancyTick is the live `_DORMANCY_TICK_MS`.
const dormancyTick = 15 * time.Second

// prober is a collector that can clear its own capability latch.
//
// NOTHING IN `internal/collect` IMPLEMENTS THIS TODAY — measured, not assumed.
// The interface exists so `_probeCollector`'s preference is expressed rather
// than silently collapsed into the fallback, and so the day a collector grows a
// probe() it is used without anybody remembering this file.
type prober interface{ Probe() }

// judgeOnDelivery wires the supervisor to the scheduler's heartbeat.
//
// ── WHAT REPLACED THE 15-SECOND GOROUTINE, AND WHY ──────────────────────────
//
// This was a ticker per session: wake every fifteen seconds, walk nineteen
// payloads, judge, sleep. Phase 3.3 asked for it to go, and it could not until
// every dormancy-eligible collector was on the scheduler -- which happened on
// 2026-09-08. Now a DELIVERY is available as the tick, and it is a better one:
//
//   - it happens whether or not the payload changed, unlike `emit`, which a
//     collector reporting nothing over and over calls exactly once. That is the
//     case dormancy exists for, so `emit` could never have been the signal.
//   - it arrives at a rate the collectors themselves declared.
//   - it STOPS when nothing is subscribed. A session with no subscriptions has
//     nothing running, so it has nothing to put to sleep, and a page focus wakes
//     a sleeping collector through `ResumeCollector` without the supervisor's
//     help. So the quiet case needs no clock at all.
//
// ── THE STEP 3.3 DESCRIBED, AND THE PART OF IT THAT WAS DROPPED ─────────────
//
// 3.3 was written as "back the QUERY off": run `internal/dormancy` per query
// rather than per collector. That half was NOT done, and the reason is this
// document's own: dormancy judges the PAYLOAD. `firewall`'s emptiness is eight
// table keys at once and `queues` is simple and tree together, so backing off
// individual menus asks a different question and would change behaviour the
// dormancy corpora pin.
//
// So the state machine, its inputs, its verdicts and its corpus are untouched.
// What moved is what drives the caller.
//
// ── THE DEBOUNCE IS THE OLD INTERVAL, ON PURPOSE ────────────────────────────
//
// Deliveries arrive several times a second on a busy router, and judging that
// often would be waste. `dormancyTick` is kept as the FLOOR between judgements
// so the rate is what it always was -- which also keeps the backoff timings in
// `internal/dormancy` meaning what they meant.
func (s *Session) judgeOnDelivery() {
	if s.roscache == nil {
		return
	}
	s.roscache.OnDeliver(func(string) { s.noteDelivery(time.Now().UnixMilli()) })
}

// noteDelivery runs a judgement if one is due. Exported to the package for its
// test; `now` is a parameter for the same reason it is one in `dormancy`.
//
// NOT ON THE CALLER'S GOROUTINE. A delivery is running inside the scheduler, and
// a judgement suspends and resumes collectors -- which releases and takes
// subscriptions on the very cache that is mid-delivery.
func (s *Session) noteDelivery(now int64) {
	last := s.dormancyAt.Load()
	if last != 0 && now-last < dormancyTick.Milliseconds() {
		return
	}
	if !s.dormancyAt.CompareAndSwap(last, now) {
		return // another delivery won the slot; one judgement is enough
	}
	go func() {
		s.mu.Lock()
		done := s.closed
		s.mu.Unlock()
		if done {
			return
		}
		s.dormancyOnce(now)
	}()
}

// dormancyOnce is one tick, split out so a test can drive it without a clock.
func (s *Session) dormancyOnce(now int64) {
	if s.dormancy == nil {
		return
	}
	targets := s.targets()

	// JUDGE ONLY WHILE SOMEBODY IS WATCHING THIS ROUTER.
	//
	// The live reason: a suspended collector emits nothing, so an idle session
	// would read as universally empty and put the whole set to sleep for a
	// reason that has nothing to do with the router. Its guard is a room-size
	// check, because an `entry` outlives its viewers — the Node pool holds a
	// router nobody is looking at.
	//
	// A Session here does NOT outlive its viewers. It is reference counted and
	// `Release` tears it down when the last one lets go, so "somebody is
	// watching" is structurally true for as long as this loop can run. Passed as
	// `true` rather than deleted from the input, because the DECISION belongs to
	// the supervisor and is pinned there against the live behaviour — and
	// because `internal/routers.Pool`, which does hold unwatched routers, is a
	// different object that may one day want the same supervisor with the answer
	// false.
	watching := true

	var cs []dormancy.Collector
	for _, c := range collection.DormancyEligible() {
		t, ok := targets[c.Key]
		if !ok {
			// Pinned by TestTheTableCoversEveryEligibleCollector, so this is a
			// belt-and-braces skip rather than a real path.
			continue
		}
		// THE PAYLOAD IS READ HERE, by json tag — see dormancy_payload.go for
		// why reflection rather than eighteen closures, and why a nil slice is
		// not an empty list.
		p := t.last()
		cs = append(cs, dormancy.Collector{
			Key:         c.Key,
			Enabled:     s.CollectorEnabled(c.Key),
			Present:     p != nil,
			TS:          payloadTS(p),
			Empty:       collection.PayloadEmptyBy(payloadLookup(p), c.EmptyKey),
			Unsupported: payloadUnsupported(p),
		})
	}

	plan := s.dormancy.Tick(dormancy.TickInput{
		// StartupReady is the live `entry.startupReady`: collectors are still
		// coming up and their emptiness means nothing yet. The port's nearest
		// truth is the connection — nothing polls before it is up.
		Now: now, Watching: watching, StartupReady: s.Connected(), Collectors: cs,
	})
	s.applyDormancy(plan, targets)
}

// applyDormancy carries out a plan and emits when it says to.
func (s *Session) applyDormancy(plan dormancy.Plan, targets map[string]collectorTarget) {
	for _, op := range plan.Ops {
		t, ok := targets[op.Key]
		if !ok {
			continue
		}
		switch op.Do {
		case dormancy.OpSuspend:
			t.suspend()
			log.Printf("[%s][dormancy] %s asleep", s.Label, op.Key)
		case dormancy.OpWake:
			log.Printf("[%s][dormancy] %s awake", s.Label, op.Key)
			// THROUGH THE FUNNEL, not t.resume(): waking is still a resume and
			// still has to pass the enabled check. By this point the supervisor
			// has already cleared the dormant flag, so the veto lets it through.
			s.ResumeCollector(op.Key)
		case dormancy.OpProbe:
			s.probe(op.Key, t)
		}
	}
	if plan.Emit {
		EvCollectionStatus.Broadcast(s.h, "router-"+s.RouterID, map[string]any{
			"routerId": s.RouterID,
			// NEVER NIL: the live payload is always an array, and `dormant: null`
			// would make `Array.isArray(st.dormant)` false in
			// `applyCollectionStatus`, which returns without clearing the marks
			// left by the previous emit.
			"dormant": nonNil(plan.Dormant),
		})
	}
}

// probe is `_probeCollector`.
func (s *Session) probe(key string, t collectorTarget) {
	if p, ok := any(t).(prober); ok {
		p.Probe()
		return
	}
	// ── A PROBE IS ONE READING. IT DOES NOT RESUME, AND IT DOES NOT WAKE ─────
	//
	// It used to resume THROUGH THE FUNNEL and then ask for a reading. On a router
	// somebody was viewing, the funnel handed the sleeping collector to
	// `WakeForFocus`, which reset its dormancy state — so every probe put the
	// backoff back to 60s and the collector re-slept ~108s later, for ever. And a
	// probe that resumed would, on an empty result, leave the collector polling
	// while the supervisor believed it asleep.
	//
	// A reading is what a probe is for. Empty: the collector stays suspended and
	// the supervisor backs off, 60s doubling to 600s. Data: the supervisor wakes
	// it through its own wake path, which resumes it. `mayRun` keeps the funnel's
	// refusal — a probe must not read what this session has no reason to run.
	//
	// THE SECOND HALF USED TO BE A TYPE ASSERTION THAT COULD NEVER PASS. It
	// asked whether the TARGET STRUCT implemented a refresher interface --
	// `collectorTarget` has no methods, so the answer was always no, and no
	// probe has ever refreshed anything. The collector was resumed and then
	// waited a full cadence for its answer. See collectorTarget.refresh.
	//
	// The old expression is deliberately NOT written out here. A gate in
	// prime_test.go scans this file for it, and quoting it made that gate fail
	// against its own explanation -- the same trap CLAUDE.md records for the
	// credential scanner reading a comment about proplists.
	if !s.mayRun(key) {
		return
	}
	if t.refresh != nil {
		t.refresh()
	}
}

// primeSpacing is the gap between one-shot reads in a priming pass.
//
// The live app staggered its startup in 75 ms burst groups, and the reason
// carries over: a weak board should not meet twenty-odd commands at once, and
// the pass must not crowd out the first REAL reads a viewer is waiting on.
// `roslimit` caps concurrency at eight regardless, so this is about smoothing
// rather than about safety.
const primeSpacing = 75 * time.Millisecond

// primeAll asks every collector that has never produced for one reading.
//
// ── PHASE 5.2: WHY THIS EXISTS ──────────────────────────────────────────────
//
// The operator's requirement is that landing on a page never waits for data.
// `Last()` replay on focus already serves any collector that has produced, so
// the hang is specifically the FIRST landing, before a collector's own cadence
// has come round -- and some of those cadences are minutes.
//
// ── ONLY WHAT HAS NOTHING, AND THAT IS THE WHOLE GATE ───────────────────────
//
// A collector with a payload is skipped. So this costs one pass at session
// start and nothing afterwards: a second call is free, a reconnect re-primes
// only what was lost, and it can be called from anywhere without a caller
// having to reason about whether it is due.
//
// ── IT DELIBERATELY READS WHAT NOBODY IS WATCHING YET ───────────────────────
//
// That is the opposite of the idle and page-room gates, ON PURPOSE, and it is
// bounded: one reading per collector, once, on a router a viewer has just asked
// for. Anyone reading this as a gate violation should read this paragraph
// instead of removing it.
//
// It also HELPS dormancy rather than fighting it: a collector that has produced
// can be judged, so a router with no queues gets that collector slept sooner.
func (s *Session) primeAll() {
	for _, key := range targetKeys {
		s.mu.Lock()
		done := s.closed
		s.mu.Unlock()
		if done || !s.Connected() {
			return
		}
		t, ok := s.targets()[key]
		if !ok || t.refresh == nil {
			continue
		}
		if !s.CollectorEnabled(key) {
			continue
		}
		if t.last() != nil {
			continue // already has something to replay
		}
		t.refresh()
		time.Sleep(primeSpacing)
	}
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// EvCollectionStatus reports which collectors are dormant. Sent here when
// dormancy changes and by internal/server when a page opens; declared once.
var EvCollectionStatus = hub.Declare[map[string]any]("collection:status")
