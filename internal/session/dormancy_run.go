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

// refresher asks for a reading now rather than at the next interval.
type refresher interface{ RefreshNow() }

// runDormancy is the 15s loop. It stops when the session is released.
//
// There is no context to cancel — a Session is reference counted and `Release`
// sets `closed` when the last viewer lets go — so the loop checks that flag,
// which is the same condition one tick later at worst.
func (s *Session) runDormancy() {
	t := time.NewTicker(dormancyTick)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		done := s.closed
		s.mu.Unlock()
		if done {
			return
		}
		s.dormancyOnce(time.Now().UnixMilli())
	}
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
		s.h.Broadcast("router-"+s.RouterID, "collection:status", map[string]any{
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
	// The fallback: resume THROUGH THE FUNNEL, then ask for a reading now.
	s.ResumeCollector(key)
	if r, ok := any(t).(refresher); ok {
		r.RefreshNow()
	}
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
