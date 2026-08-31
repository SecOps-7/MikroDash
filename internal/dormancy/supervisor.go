package dormancy

// The supervisor: which collectors to suspend, wake and re-probe on each tick.
//
// ── A PLAN, NOT AN ACTOR ────────────────────────────────────────────────────
//
// `Tick` performs nothing. It takes what each collector last produced and
// returns the operations the caller should carry out, in order. Same shape as
// `internal/routers/overview.go`'s `SyncPool` — "one pure SyncPool with no
// RouterOS in it" — and for the same reason: the decisions are the part worth
// pinning, and a decision that can only be observed through a live router is a
// decision nothing tests.
//
// The supervisor holds one `State` per collector across ticks, so it is stateful;
// it is `Tick` that is free of I/O.
//
// ── WHY ONE SUPERVISOR AND NOT A LOOP PER COLLECTOR ─────────────────────────
//
// The live comment: "emptiness is declared once in the registry (`emptyKey`), so
// the judgement reads `lastPayload` generically and no collector grows an
// emptiness hook."
//
// ── DORMANCY IS A VETO, AND THE CALLER MUST HONOUR IT ───────────────────────
//
// Three gates decide whether a collector runs — idle (nobody on this router),
// page rooms (nobody on its page) and dormancy — and they are LAYERED, not
// competing. The live app funnels every resume through `_resumeCollector` so a
// gate that knows nothing about dormancy cannot undo it: "_idleResume calling
// resume() directly is precisely what would wake a dormant collector on the next
// socket join." `IsDormant` is that veto, and the caller has to ask.

import (
	"sort"
	"sync"
)

// Op is one thing the caller should do to one collector.
type Op struct {
	Key string
	// Do is "suspend", "wake" or "probe".
	//
	// "probe" is deliberately not "resume": the live `_probeCollector` prefers a
	// collector's own `probe()`, which clears a capability latch that `resume()`
	// honours on purpose, and falls back to resume-plus-refreshNow so the answer
	// arrives on this tick rather than one poll interval later. Which of those a
	// given collector gets is the CALLER's knowledge, not this package's.
	Do string
}

const (
	OpSuspend = "suspend"
	OpWake    = "wake"
	OpProbe   = "probe"
)

// Collector is one collector's situation this tick.
//
// ── THE PAYLOAD IS READ BY THE CALLER, NOT HERE ─────────────────────────────
//
// The live supervisor reads `lastPayload` generically because a JavaScript
// payload is already a map. This port has two callers with two representations —
// `internal/session` holds typed structs and reads them by json tag, and the
// corpus test holds the maps the live app produced — so the READING is theirs
// and the JUDGEMENT is this package's.
//
// They do not each own the rule: `collection.PayloadEmptyBy` is the rule, and
// both supply it a lookup. `TestTheLookupAgreesWithTheMapLookup` drives one
// payload through both and fails if they ever disagree, which is what carries
// the live corpus across to the struct side.
type Collector struct {
	Key string
	// Enabled is the operator's per-router switch. A collector turned off is
	// skipped entirely — it is not asleep, it is not running.
	Enabled bool
	// Present is whether the collector has produced a payload at all. A
	// collector with no payload yet is neither judged nor probed: it has said
	// nothing, which is not the same as saying nothing is there.
	Present bool
	// TS is the payload's timestamp. A repeated one means the collector has
	// produced nothing new — see Observe.
	TS int64
	// Empty is `payloadEmpty(payload, def.emptyKey)`.
	Empty bool
	// Unsupported is `payload.available === false`: this RouterOS has no such
	// menu, which earns the MAXIMUM backoff rather than the base one.
	Unsupported bool
}

// TickInput is one supervisor tick.
type TickInput struct {
	Now int64
	// Watching is whether anybody is in this router's room.
	//
	// JUDGE ONLY WHILE SOMEBODY IS WATCHING. A suspended collector emits nothing,
	// so an idle session would read as universally empty and put the whole set to
	// sleep for a reason that has nothing to do with the router. The live guard
	// is a room-size check; this is the same question, asked by the caller.
	Watching bool
	// StartupReady is the live `entry.startupReady`: collectors are still coming
	// up and their emptiness means nothing yet.
	StartupReady bool
	Destroyed    bool
	Collectors   []Collector
}

// Plan is what the caller should do.
type Plan struct {
	Ops []Op
	// Emit is whether to send `collection:status`.
	//
	// SET BY A VERDICT, NOT BY A PROBE. A probe that finds nothing changes the
	// delay and nothing the browser can see, and re-announcing an unchanged
	// dormant set every tick would be a payload per fifteen seconds per router
	// saying what the last one said.
	Emit bool
	// Dormant is every sleeping collector, for the payload. Present whenever
	// Emit is set.
	Dormant []string
}

// Supervisor holds one State per collector.
// ── THE MAP IS GUARDED, AND THE REASON IS THIS PORT, NOT THE ORIGINAL ─────
//
// The live supervisor is reached from one event loop, so its map cannot race and
// it has no lock. Here it is reached from at least three goroutines:
//
//	the dormancy ticker    `Tick`      (session/dormancy_run.go)
//	the connect loop       `Reset`     on reconnect (session.go)
//	websocket handlers     `WakeForFocus`, `IsDormant`, `Dormant` on page focus
//
// and `state(key)` WRITES the map on first sight of a collector, so three of
// those are writers. A page focus during a tick is a concurrent map write, which
// is a RUNTIME FATAL: it takes the process, not the request.
//
// That is not hypothetical. `alert.Evaluator` had the identical shape — ported
// from JS, plain maps, no lock — and killed the server on 2026-08-29 with
// `fatal error: concurrent map writes`. This one was found by asking which OTHER
// ported objects hold unguarded state, and confirmed with the race detector.
type Supervisor struct {
	// mu guards `states` and every *State in it. Held for the whole of each
	// public method: the rules read a state, decide, then write it, so a lock
	// released between the read and the write would leave the race in place.
	mu sync.Mutex

	opt    Options
	states map[string]*State
}

func NewSupervisor(opt Options) *Supervisor {
	return &Supervisor{opt: opt, states: map[string]*State{}}
}

// state is the live `_dormancyState`: created on first use, so a collector that
// never reports never gets one.
// state returns this collector's state, creating it on first sight.
//
// CALLERS MUST HOLD `mu`. It is the only writer of the map, and every public
// method that reaches it takes the lock first.
func (s *Supervisor) state(key string) *State {
	st, ok := s.states[key]
	if !ok {
		st = New(s.opt)
		s.states[key] = st
	}
	return st
}

// IsDormant is the veto. Every resume path must consult it.
func (s *Supervisor) IsDormant(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isDormantLocked(key)
}

// isDormantLocked is IsDormant for callers already holding `mu`.
//
// ── GO MUTEXES ARE NOT REENTRANT, AND THREE METHODS CALL THIS ─────────────
//
// `Tick`, `Reset` and `WakeForFocus` all need the dormant set, and
// `WakeForFocus` needs this too. Locking in the public method and calling it
// from another locked method deadlocks the process — which is exactly what the
// first version of this change did, and the symptom was the test suite hanging
// rather than failing.
func (s *Supervisor) isDormantLocked(key string) bool {
	st, ok := s.states[key]
	return ok && st.dormant
}

// Dormant lists the sleeping collectors, for the `collection:status` payload.
//
// SORTED, where the live `_dormancyPayload` walks a Map in insertion order —
// which is the order collectors first reported, not the registry order. The
// consumer indexes `COLLECTOR_CARDS` by name and never reads the order, and a Go
// map has no insertion order to preserve; sorting is what makes two emits of the
// same set identical.
func (s *Supervisor) Dormant() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dormantLocked()
}

// dormantLocked is Dormant for callers already holding `mu` — see
// isDormantLocked for why the split exists.
func (s *Supervisor) dormantLocked() []string {
	out := []string{}
	for k, st := range s.states {
		if st.dormant {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Tick judges every eligible collector and returns what to do.
func (s *Supervisor) Tick(in TickInput) Plan {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !in.StartupReady || in.Destroyed || !in.Watching {
		return Plan{}
	}
	var plan Plan
	for _, c := range in.Collectors {
		if !c.Enabled {
			continue
		}
		st := s.state(c.Key)

		// ── AN EQUIVALENT GUARD, MEASURED ───────────────────────────────
		//
		// Replacing this with `if true` SURVIVES mutation testing, and it is
		// equivalent rather than untested: a collector with no payload reports
		// TS 0, and `Observe` returns None on its zero-TS guard
		// before touching any state. The live `if (p)` is redundant with
		// `if (!obs.ts) return null` in exactly the same way.
		//
		// Kept because it says what it means — a collector that has produced
		// nothing has said nothing — and because the reader should see the same
		// structure as the original.
		if c.Present {
			switch st.Observe(Observation{
				TS: c.TS, Empty: c.Empty, Unsupported: c.Unsupported,
			}, in.Now) {
			case Sleep:
				plan.Emit = true
				plan.Ops = append(plan.Ops, Op{Key: c.Key, Do: OpSuspend})
			case Wake:
				plan.Emit = true
				plan.Ops = append(plan.Ops, Op{Key: c.Key, Do: OpWake})
			}
		}

		// AFTER the verdict, and on every eligible collector including one with no
		// payload — matching the live loop, where `dueForProbe` sits outside the
		// `if (p)`. A collector that has never reported is never asleep, so this
		// is false for it; the placement matters only if that ever changes.
		if st.DueForProbe(in.Now) {
			st.MarkProbed(in.Now)
			plan.Ops = append(plan.Ops, Op{Key: c.Key, Do: OpProbe})
		}
	}
	if plan.Emit {
		plan.Dormant = s.dormantLocked()
	}
	return plan
}

// Reset is `_resetDormancy`: clear every verdict and wake whatever was asleep.
//
// A reconnect may follow a RouterOS upgrade or a package install, which is
// exactly the event that turns an "unknown command" into a working menu.
//
// It probes what WAS dormant and emits only if something was.
func (s *Supervisor) Reset() Plan {
	s.mu.Lock()
	defer s.mu.Unlock()

	var plan Plan
	keys := make([]string, 0, len(s.states))
	for k := range s.states {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		st := s.states[k]
		if st.dormant {
			plan.Emit = true
			plan.Ops = append(plan.Ops, Op{Key: k, Do: OpProbe})
		}
		st.Reset()
	}
	if plan.Emit {
		plan.Dormant = s.dormantLocked()
	}
	return plan
}

// WakeForFocus is `_wakeForFocus`: somebody opened the page this collector feeds.
//
// "That is the cheapest and most timely re-probe there is — a user who has just
// added a netwatch host opens the NetWatch page next — so it pre-empts the
// backoff entirely."
//
// Does NOTHING for a collector that is not dormant, which is the arm a port gets
// wrong by probing unconditionally.
func (s *Supervisor) WakeForFocus(key string) Plan {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isDormantLocked(key) {
		return Plan{}
	}
	s.states[key].Reset()
	return Plan{Ops: []Op{{Key: key, Do: OpProbe}}, Emit: true, Dormant: s.dormantLocked()}
}
