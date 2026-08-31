package routers

// The Routers page's background pool — the port of `src/overviewSessions.js`,
// as pure decisions with no RouterOS in them.
//
// ── WHAT THIS IS FOR ────────────────────────────────────────────────────────
//
// The Routers page shows a row per router, including routers nobody has open.
// Those numbers come from somewhere: a pool holding a connection per unwatched
// router, running THREE collectors — system, interfaceStatus, dhcpLeases — and
// caching their payloads for `BuildRow`.
//
// ── IT IS NOT `Session`, AND THAT IS THE WHOLE POINT ────────────────────────
//
// `internal/session.Session` starts FOURTEEN collectors on connect (pinned by
// `TestTheBackgroundCollectorCountIsRecorded`). The live pool runs three. Using
// `Session` for routers nobody is looking at would put 4.7× the collectors on
// every router in a fleet, and the documented bottleneck is concurrent API
// channels on the MikroTik rather than CPU. So this is a different object, not
// a tuned-down one.
//
// ── WIRING IT IS AN OPERATOR DECISION; BUILDING IT IS NOT ───────────────────
//
// Whether a Go pool may run DURING COEXISTENCE is open — Node already runs one
// against the same fleet, so both would connect to every router at once. That
// decision is recorded during the port and is not made here. The code is needed
// under every option, including "wait for cutover", so it is written and pinned
// now and constructed by nobody — the same arrangement as
// `internal/history.Bucketer`.

import "sort"

// PoolAction is what a Sync concluded: which routers to start a background
// session for, and which to tear down.
//
// Both lists are SORTED. The live pool iterates a Map, so its order is insertion
// order; Go's map order is random, and a caller that connected in a different
// order every sync would be needlessly hard to reason about in a log. Nothing
// depends on the order — each start and stop is independent — so sorting is free
// and makes the decision reproducible.
type PoolAction struct {
	Start []string
	Stop  []string
}

// SyncPool decides which background sessions should exist.
//
// `all` is every router the app knows that is not disabled. `excluded` is the
// set the MAIN pool is already serving — a router with somebody looking at it
// must NOT also get a background session, or every up/down transition would be
// recorded twice and the router would carry two connections. `tracked` is what
// the pool holds today.
//
// A router that is both tracked and excluded is STOPPED, not left running: the
// exclusion means somebody opened it, and the main session took over.
func SyncPool(all []string, excluded map[string]bool, tracked map[string]bool) PoolAction {
	known := make(map[string]bool, len(all))
	for _, id := range all {
		known[id] = true
	}
	var act PoolAction
	for id := range tracked {
		// Two reasons to tear down, and the live pool checks both in one
		// condition: the router is gone, or the main pool now owns it.
		if excluded[id] || !known[id] {
			act.Stop = append(act.Stop, id)
		}
	}
	for _, id := range all {
		if excluded[id] || tracked[id] {
			continue
		}
		act.Start = append(act.Start, id)
	}
	sort.Strings(act.Start)
	sort.Strings(act.Stop)
	return act
}

// OverviewSession is one background session's lifecycle, without the connection.
//
// The zero value is a session that has been built and has not connected yet,
// which is what the live constructor produces.
type OverviewSession struct {
	Connected bool
	// LastError is shown on the Routers page so an offline card can explain
	// itself instead of sending the operator to the container logs (#92). Only a
	// CLASSIFIED reason is ever stored; anything else becomes the generic string,
	// so no raw driver text, path or address can reach the browser.
	LastError string

	destroyed bool
	suspended bool
}

// OnConnected reports whether the three collectors should be started.
//
// TWO GUARDS, AND BOTH ARE LOAD-BEARING. A session that has been torn down must
// not start collectors on an in-flight event arriving after the teardown — the
// live code carries a `destroyed` flag for exactly that race, and without it a
// removed router keeps polling for ever. And a SUSPENDED pool connects without
// starting: suspension is "stop collecting", not "disconnect", so the sockets
// stay up and Resume costs nothing.
func (s *OverviewSession) OnConnected() bool {
	if s.destroyed {
		return false
	}
	s.Connected = true
	s.LastError = ""
	return !s.suspended
}

// OnClosed marks the link down. It does NOT clear LastError: the reason a
// session failed is what the page has to show while it is down.
func (s *OverviewSession) OnClosed() { s.Connected = false }

// OnError records why, taking the classified reason or the generic fallback.
//
// Only a CLASSIFIED reason is stored; anything else becomes the generic string,
// so no raw driver text, path or address can reach the browser (#92).
func (s *OverviewSession) OnError(reason string, classified bool) {
	s.Connected = false
	if classified {
		s.LastError = reason
		return
	}
	s.LastError = "Connection failed"
}

// Suspend stops collecting.
//
// IT STOPS UNCONDITIONALLY, like the original, which calls `stop()` on all three
// collectors whether or not they were running. An earlier draft here guarded on
// a `running` flag and returned whether there had been anything to stop — an
// invention, and the corpus caught it: the live pool logs three stops even for a
// session that never connected. A stop on a stopped collector is a no-op, and
// tracking "running" only creates a second source of truth that can disagree
// with the collectors themselves.
func (s *OverviewSession) Suspend() { s.suspended = true }

// Resume reports whether the collectors should be started again.
//
// GUARDED ON `Connected` AND NOTHING ELSE, which is the live rule. Starting a
// collector against a dead connection would poll into nothing; starting one that
// is already running is what the live `resume()` does after a `suspend()` and is
// harmless, so no guard is added for it.
func (s *OverviewSession) Resume() bool {
	s.suspended = false
	return s.Connected
}

// Destroy tears the session down permanently.
func (s *OverviewSession) Destroy() { s.destroyed = true; s.Connected = false }

// Destroyed reports whether this session has been torn down. The connection loop
// checks it after a dial returns: a router removed WHILE DIALLING must not have
// its collectors started, which is the race the live `destroyed` flag guards.
func (s *OverviewSession) Destroyed() bool { return s.destroyed }

// Suspended reports whether the pool has told this session to stop collecting.
func (s *OverviewSession) Suspended() bool { return s.suspended }
