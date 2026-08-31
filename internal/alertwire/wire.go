// Package alertwire joins the three pieces that have existed separately for
// most of this port: the RULES (`internal/alert`), the HISTORY
// (`internal/db`'s alert writes) and the collector payloads that feed them.
//
// ── WHAT THIS DELIBERATELY DOES NOT DO ─────────────────────────────────────
//
// IT DOES NOT DISPATCH. No Telegram message, no email, no ntfy push. That is
// step 2 of the plan in `LOOP.md`, and the switch is the operator's, for the
// reason `PORT-QUEUE.md` blocker 5 gives — the one blocker whose reasoning did
// NOT change when the port went standalone:
//
//	Both engines evaluate the same conditions against the same physical
//	routers, and the cooldown is an in-memory map rather than a shared row, so
//	neither sees the other's sends. A duplicated Telegram message or email
//	cannot be un-received.
//
// Everything here writes to THIS install's own database and nowhere else. Two
// engines filing a row twice is a duplicate an operator can delete; two engines
// sending an alert twice is not.
//
// ── ONE EVALUATOR PER ROUTER, KEYED AND DROPPABLE ──────────────────────────
//
// The rules are edge-detecting: they fire when a value CROSSES a threshold, not
// while it sits past one, so each router needs its own memory of what it last
// saw. The live app keeps `_evaluators` as a `routerId → evaluator` map and
// drops entries on a router switch or an idle teardown — and its own comment
// records what that costs: a rebuilt evaluator has no memory of having reported
// a thing, so it reports it again.
//
// That is exactly why `HasOpenAlert` asks the DATABASE rather than the
// evaluator. The dedup survives a drop; the edge state does not, and need not.
package alertwire

import (
	"log"
	"sync"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/collect"
)

// History is the subset of `*db.DB` this package needs.
//
// AN INTERFACE, not the concrete type, and not for taste: it lets the tests
// drive the whole wire against a recording fake and assert what WOULD have been
// written, with no database and none of the timing of one.
type History interface {
	HasOpenAlert(routerID, alertType, subject string) bool
	InsertAlertEvent(routerID, alertType, subject, detail string, now int64) int64
	ResolveAlertEvent(routerID, alertType, subject string, now int64) []int64
}

// store adapts History onto `alert.Store`.
//
// ── THE TWO INTERFACES DIFFER IN ONE PLACE, AND IT MATTERS ─────────────────
//
// `alert.Store.Record` takes no timestamp; `InsertAlertEvent` does. The
// evaluator does not know what time it is and should not — its job is to decide
// WHETHER, not when. So the instant is captured once per EVENT and every row
// that payload fires carries it.
//
// The live code calls `Date.now()` per insert instead, so two alerts fired by
// one `routing:update` can straddle a millisecond and sort as though they
// happened separately. Reproducing that would mean reproducing a defect with no
// upside, and nothing renders the difference. Recorded rather than silently
// improved.
type store struct {
	h   History
	now int64
}

func (s *store) HasOpen(routerID, alertType, subject string) bool {
	return s.h.HasOpenAlert(routerID, alertType, subject)
}

func (s *store) Resolve(routerID, alertType, subject string) []int64 {
	return s.h.ResolveAlertEvent(routerID, alertType, subject, s.now)
}

func (s *store) Record(routerID, alertType, subject, detail string) int64 {
	return s.h.InsertAlertEvent(routerID, alertType, subject, detail, s.now)
}

// Wire holds one evaluator per router.
type Wire struct {
	mu     sync.Mutex
	hist   History
	set    alert.Settings
	evals  map[string]*alert.Evaluator
	stores map[string]*store
	// locks serialises evaluation PER ROUTER. See Evaluate.
	locks map[string]*sync.Mutex
	// now is the clock, injectable so a test can assert that one event stamps
	// every row it files with ONE instant.
	now func() int64
}

func New(hist History, set alert.Settings) *Wire {
	return &Wire{
		hist: hist, set: set,
		evals:  map[string]*alert.Evaluator{},
		stores: map[string]*store{},
		locks:  map[string]*sync.Mutex{},
		now:    func() int64 { return time.Now().UnixMilli() },
	}
}

// SetSettings replaces the thresholds and the per-type toggles on every live
// evaluator, WITHOUT dropping their edge state. See `alert.Evaluator.SetSettings`
// for why rebuilding would be wrong.
func (w *Wire) SetSettings(set alert.Settings) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.set = set
	for _, e := range w.evals {
		e.SetSettings(set)
	}
}

// Drop forgets a router's edge state. The live `dropEvaluator`.
func (w *Wire) Drop(routerID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.evals, routerID)
	delete(w.stores, routerID)
}

// Routers reports how many evaluators are live. For the tests and for a future
// status endpoint; nothing in the app reads it yet.
func (w *Wire) Routers() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.evals)
}

// forRouter returns this router's evaluator AND the lock that must be held
// while using it. See Evaluate for why the two travel together.
func (w *Wire) forRouter(routerID string, now int64) (*alert.Evaluator, *sync.Mutex) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.stores[routerID]
	if st == nil {
		st = &store{h: w.hist}
		w.stores[routerID] = st
		w.evals[routerID] = alert.NewEvaluator(w.set, st)
		w.locks[routerID] = &sync.Mutex{}
	}
	st.now = now
	return w.evals[routerID], w.locks[routerID]
}

// Evaluate feeds one collector payload to the rules and returns what fired.
//
// ── AN UNRECOGNISED EVENT IS THE COMMON CASE, NOT AN ERROR ─────────────────
//
// This sits in the emit path of EVERY collector — dns, bridges, packages,
// backups, wifi and a dozen more — and only six events have a rule. The live
// `evaluateForRouter` switches on the same six and ignores the rest.
//
// ── AND SO IS A PAYLOAD OF THE WRONG TYPE ──────────────────────────────────
//
// Each case asserts the collector's own struct. A payload that is not that type
// evaluates nothing rather than guessing at half-read fields — the same outcome
// as the collector not having run, which is a state every rule already handles.
func (w *Wire) Evaluate(r alert.Router, event string, payload any) []alert.Fired {
	if w == nil || r.ID == "" {
		return nil
	}

	// ── ONE ROUTER'S RULES RUN ONE AT A TIME ──────────────────────────────
	//
	// `alert.Evaluator` keeps its edge state in plain maps and has no lock,
	// because the thing it was ported from cannot race: JavaScript is
	// single-threaded, so the live evaluator is reached from one event loop.
	//
	// HERE IT IS NOT. Every collector has its own poll timer, so `system:update`
	// and `ifstatus:update` for one router arrive on different goroutines, and
	// after `internal/alertpool` landed a whole fleet's collectors do the same.
	// On 2026-08-29 the server died with
	//
	//	fatal error: concurrent map writes
	//	  alert.(*Evaluator).IfstatusUpdate  eval.go:428
	//
	// during an ordinary page sweep. Not a hypothetical: a crash takes the whole
	// process, so every router loses monitoring until something restarts it.
	//
	// PER ROUTER, not one global lock: two routers share no state and serialising
	// the fleet behind one mutex would put every router's evaluation behind the
	// slowest database call.
	//
	// HELD ACROSS THE WHOLE RULE RUN, including the `Store` calls it makes. The
	// rules read edge state, decide, then write it; a lock released between the
	// read and the write would leave exactly the race it is here to remove.
	// AND THE EVALUATOR IS NOT BUILT UNTIL A RULE MATCHES. The switch below
	// chooses `run`; only then is per-router state created. Acquiring first was
	// simpler and wrong: it built an evaluator for every event with no rule —
	// most of them — which `TestAnEventWithNoRuleTouchesNothing` exists to stop.
	var run func(*alert.Evaluator) []alert.Fired

	// THE TYPE SWITCH IS THE GUARD. Checking the event name first and the type
	// second would let a renamed event silently stop evaluating; this way a
	// mismatch of either kind produces nothing, and the name is only used to
	// pick which rule family to run.
	switch p := payload.(type) {
	case *collect.SystemPayload:
		if event != "system:update" {
			return nil
		}
		cpu := float64(p.CPULoad)
		run = func(e *alert.Evaluator) []alert.Fired {
			// ── AN UNCHECKED COLLECTOR MUST NOT RESOLVE AN OPEN ALERT ──
			//
			// A row with NO VERDICT — no version, and either no status or one
			// saying the router is still working it out — is not evidence that
			// the router is up to date. Feeding it to `updateRule` resolves
			// whatever another collector opened.
			//
			// `collect.UpdateUnknown` is the collector's own retry predicate,
			// exported so there is ONE rule. This was `latest == "" && status ==
			// ""` first, which is a strict subset: a transient status slipped
			// through and four rows appeared after that fix.
			if collect.UpdateUnknown(p.LatestVersion, p.UpdateStatus) {
				return e.SystemCPUOnly(r, &cpu)
			}
			return e.SystemUpdate(r, &cpu, p.UpdateAvailable, p.LatestVersion, p.Version)
		}
	case *collect.PingPayload:
		if event != "ping:update" {
			return nil
		}
		// LOSS IS AN INT ON THE WIRE AND A FLOAT IN THE RULE, and nil must stay
		// nil: the ping rule treats a missing reading as "no answer yet", where
		// zero would RESOLVE an outstanding loss alert that is still true.
		var loss *float64
		if p.Loss != nil {
			f := float64(*p.Loss)
			loss = &f
		}
		target := p.Target
		run = func(e *alert.Evaluator) []alert.Fired { return e.PingUpdate(r, &target, loss, p.RTT) }
	case *collect.IfStatusPayload:
		if event != "ifstatus:update" {
			return nil
		}
		run = func(e *alert.Evaluator) []alert.Fired { return e.IfstatusUpdate(r, ifaces(p.Interfaces)) }
	case *collect.VPNPayload:
		if event != "vpn:update" {
			return nil
		}
		run = func(e *alert.Evaluator) []alert.Fired { return e.VPNUpdate(r, tunnels(p.Tunnels)) }
	case *collect.NetwatchPayload:
		if event != "netwatch:update" {
			return nil
		}
		run = func(e *alert.Evaluator) []alert.Fired { return e.NetwatchUpdate(r, hosts(p.Hosts)) }
	case *collect.RoutingPayload:
		if event != "routing:update" {
			return nil
		}
		run = func(e *alert.Evaluator) []alert.Fired { return e.RoutingUpdate(r, peers(p.Peers)) }
	default:
		return nil
	}

	if run == nil {
		return nil
	}

	// ONE ROUTER'S RULES RUN ONE AT A TIME — see the note above the switch.
	ev, lock := w.forRouter(r.ID, w.now())
	lock.Lock()
	fired := run(ev)
	lock.Unlock()

	if len(fired) > 0 {
		// ONE LINE PER EVENT, not per alert, so a fleet-wide flap does not bury
		// the log. Tagged `[alert]` so it is greppable when the dispatch is
		// eventually wired and someone asks what fired before it.
		log.Printf("[alert] %s %s: %d change(s)", r.ID, event, len(fired))
	}
	return fired
}

// ── the payload adapters ────────────────────────────────────────────────────
//
// Each maps a collector's row onto the rule's. They are separate types on
// purpose: `internal/alert` is gated against the live evaluator and must not
// grow a dependency on whatever shape a collector happens to emit.

func ifaces(in []collect.Interface) []alert.Interface {
	out := make([]alert.Interface, 0, len(in))
	for _, i := range in {
		out = append(out, alert.Interface{
			Name: i.Name, Type: i.Type, Comment: i.Comment,
			Running: i.Running, Disabled: i.Disabled,
		})
	}
	return out
}

// tunnels carries State, NOT a derived boolean.
//
// The rule compares against the string "active" specifically, and the live
// comment records why that is load-bearing: the collector used to emit
// 'connected'/'idle', the consumer compared against those, and after the rename
// "wasConn and isConn were both permanently false and VPN alerts could not fire
// at all". Passing a bool computed here would put that comparison in two places
// again.
func tunnels(in []collect.Tunnel) []alert.VPNTunnel {
	out := make([]alert.VPNTunnel, 0, len(in))
	for _, t := range in {
		out = append(out, alert.VPNTunnel{Name: t.Name, State: t.State})
	}
	return out
}

func hosts(in []collect.NetwatchHost) []alert.NetwatchHost {
	out := make([]alert.NetwatchHost, 0, len(in))
	for _, h := range in {
		out = append(out, alert.NetwatchHost{
			ID: h.ID, Host: h.Host, Name: h.Name, Status: h.Status,
		})
	}
	return out
}

// peers maps the BGP rows.
//
// `Prefixes`, `HoldTime` and `Keepalive` are POINTERS on the rule's side and
// plain ints on the collector's. The rule uses nil for "the router did not
// report this", and the prefix rule's threshold is a FRACTION of the previous
// count — so a peer that stops reporting must read as unknown rather than as
// having dropped to zero, which would fire a 100% prefix-loss alert on every
// tick where the field was simply absent.
//
// The collector cannot express that today: it decodes to 0. So this maps 0 to a
// pointer-to-zero rather than to nil, which is the FAITHFUL reading of what the
// collector actually said, and the difference is recorded here rather than
// papered over. If the collector ever gains a nullable prefix count, this is
// where it connects.
func peers(in []collect.Peer) []alert.BGPPeer {
	out := make([]alert.BGPPeer, 0, len(in))
	for _, p := range in {
		pfx := float64(p.Prefixes)
		hold := float64(p.HoldTime)
		keep := float64(p.Keepalive)
		out = append(out, alert.BGPPeer{
			Key: p.Key, Name: p.Name, RemoteAddr: p.RemoteAddr,
			Description: p.Description, State: p.State,
			Prefixes: &pfx, Flapping: p.Flapping,
			HoldTime: &hold, Keepalive: &keep,
		})
	}
	return out
}
