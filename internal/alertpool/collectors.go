package alertpool

import (
	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
	"mikrodash/internal/routeros"

	"mikrodash/internal/roslimit"
)

// EventHook receives every payload a pooled session's collectors emit.
//
// The server points this at `alertwire.Evaluate`. It is the whole reason the
// collectors run: nothing renders these payloads, and no browser is listening.
// The live shim says the same thing in its own way — a `stubIo` whose `emit`
// forwards to the evaluator and discards the rest.
type EventHook func(r Router, event string, payload any)

// reader is the collectors' view of a pooled session.
//
// `Connected` gates every poll, so a collector left running across a drop reads
// nothing rather than erroring in a loop — the same arrangement
// `internal/routers` uses and for the same reason.
type reader struct{ s *poolSession }

func (r reader) Connected() bool {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.s.conn != nil && r.s.conn.Connected()
}

func (r reader) Do(c routeros.Cmd) ([]routeros.Reply, error) {
	r.s.mu.Lock()
	conn := r.s.conn
	r.s.mu.Unlock()
	if conn == nil {
		return nil, errNotConnected{}
	}
	// The third holder of this router's budget. A router that is watched AND
	// alerted would otherwise get two independent allowances.
	done := roslimit.Acquire(r.s.r.ID)
	defer done()
	return conn.Do(c)
}

func (r reader) Stream(c routeros.Cmd, onRow func(routeros.Reply)) (func(), error) {
	r.s.mu.Lock()
	conn := r.s.conn
	r.s.mu.Unlock()
	if conn == nil {
		return nil, errNotConnected{}
	}
	return conn.Stream(c, onRow)
}

type errNotConnected struct{}

func (errNotConnected) Error() string { return "alertpool: background session not connected" }

// buildCollectors gives an alerts-enabled session its six collectors.
//
// ── THE SET IS THE LIVE POOL'S, NOT THE PAGE SESSION'S ────────────────────
//
// system, ping, ifstatus, vpn, netwatch and routing — the six the alert rules
// read, and no others. A page session starts fourteen; running those here would
// put ten collectors per router on the fleet for payloads nobody renders.
//
// ROUTING IS bgpOnly. The rules read `peers` and nothing else, so the two route
// tables would be load for a payload no page renders — the live pool's own
// reasoning, and the reason `collect.Routing` gained the mode.
//
// ── THE INTERVALS COME FROM THE RESOLVED CONFIG, WHICH LIVE'S DO NOT ──────
//
// `alertSessions.js` reads the global `Settings.load()` polls. This port
// resolves per-router config (#105) here, matching what `internal/routers.Pool`
// and `session.go` already do — so a router configured to poll slowly is polled
// slowly by every part of this app rather than by two of three. Recorded as a
// deliberate difference rather than slipped in.
// emitTargets decides where one session's payloads go.
//
// ── A FUNCTION SO THAT "A HISTORY-ONLY SESSION DOES NOT ALERT" IS TESTABLE ─
//
// This was two conditions inside the emit closure, and no test could reach them:
// the closure only runs when a collector produces, and a unit test has no
// connection to produce from. A mutation dropping the `AlertsEnabled` half
// survived every assertion in this package.
//
// The property it protects: a router the operator turned alerting OFF for is
// built here anyway when it is the one being recorded, and its payloads must not
// reach the evaluator. Otherwise enabling continuous history would silently turn
// alerting back on for that router — a notification the operator switched off.
func emitTargets(alertsEnabled, hist bool) (toEvaluator, toHistory bool) {
	return alertsEnabled, hist
}

func buildCollectors(s *poolSession, eff collection.Resolved, on EventHook, rec RecordHook, hist bool) {
	// ── HISTORY IS NOT GATED ON ALERTS, AND THAT MATTERS ──────────────────
	//
	// A status-only session — `alertsEnabled` false — used to return here with no
	// collectors at all, which is right for alerting and wrong for history: the
	// ACTIVE router is whichever one the operator selected, and nothing says it
	// has alerts on. Today two of this fleet's three have alerts off; that the
	// active one has them on is luck, not design.
	if !s.r.AlertsEnabled && !hist {
		return
	}
	rd := reader{s}
	toEval, toHist := emitTargets(s.r.AlertsEnabled, hist)
	emit := func(_ string, event string, payload any) {
		if toEval && on != nil {
			on(s.r, event, payload)
		}
		if toHist && rec != nil {
			rec(s.r.ID, event, payload)
		}
	}

	if hist {
		// TRAFFIC IS THE ONE COLLECTOR THIS POOL DOES NOT ALREADY RUN, and the
		// whole added cost of continuous history: one command channel, on the
		// active router only, on a connection this pool already holds.
		s.traffic = collect.NewTraffic(rd, emit, s.r.DefaultIf, 5)
	}

	if !s.r.AlertsEnabled {
		// History-only: ping is the other half history needs, and the six alert
		// collectors are not wanted.
		s.ping = collect.NewPing(rd, emit, eff.Poll["ping"], s.r.PingTarget)
		return
	}

	s.system = collect.NewSystem(rd, emit, eff.Poll["system"])
	// The router's own ping target, falling back to the live default inside the
	// collector exactly as the page path does.
	s.ping = collect.NewPing(rd, emit, eff.Poll["ping"], s.r.PingTarget)
	s.ifStatus = collect.NewIfStatus(rd, emit, s.r.ID, eff.Poll["ifStatus"])
	s.vpn = collect.NewVPN(rd, emit, eff.Poll["vpn"])
	s.netwatch = collect.NewNetwatch(rd, emit, eff.Poll["netwatch"])
	s.routing = collect.NewRouting(rd, emit, eff.Poll["routing"]).BGPOnly()
}

// startCollectors runs on every CONNECT, including a reconnect.
//
// Gated on the resolved config like `internal/routers.Pool`'s: a collector the
// operator turned off for this router is never started, and #105 exists so that
// choice is honoured everywhere rather than on the pages only.
func (s *poolSession) startCollectors(eff collection.Resolved) {
	// THE HISTORY PAIR FIRST, and outside the status-only guard below: a
	// history-only session has no `system` and must still record.
	if s.traffic != nil && eff.Enabled["traffic"] {
		s.traffic.Start()
	}
	if s.system == nil {
		if s.ping != nil && eff.Enabled["ping"] {
			s.ping.Start() // history-only session
		}
		return // status-only session
	}
	if eff.Enabled["system"] {
		s.system.Start()
	}
	if eff.Enabled["ping"] {
		s.ping.Start()
	}
	if eff.Enabled["ifStatus"] {
		s.ifStatus.Start()
	}
	if eff.Enabled["vpn"] {
		s.vpn.Start()
	}
	if eff.Enabled["netwatch"] {
		s.netwatch.Start()
	}
	// RESUME, not Start — `collect.Routing` has no Start, only Resume, which is
	// the same asymmetry `session.go`'s reconnect list carries. Resume starts the
	// loop when the client is up, which it is at this point: this runs
	// immediately after a successful dial.
	if eff.Enabled["routing"] {
		s.routing.Resume()
	}
}

// stopCollectors stops all six UNCONDITIONALLY.
//
// Like `internal/routers.Pool`'s, and for the reason it gives: a stop on a
// stopped collector is a no-op, and tracking "running" only creates a second
// source of truth. Unconditional also means a collector that was enabled when
// it started still stops after the operator disables it.
func (s *poolSession) stopCollectors() {
	// UNCONDITIONALLY, and before the status-only guard: a history-only session
	// has no `system` and would otherwise keep its two collectors running after
	// the pool dropped it.
	if s.traffic != nil {
		s.traffic.Stop()
	}
	if s.system == nil {
		if s.ping != nil {
			s.ping.Stop()
		}
		return
	}
	s.system.Stop()
	s.ping.Stop()
	s.ifStatus.Stop()
	s.vpn.Stop()
	s.netwatch.Stop()
	s.routing.Stop()
}
