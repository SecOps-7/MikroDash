package routers

// `switchRouter` — changing the GLOBAL active router — as a plan rather than as
// effects.
//
// ── NOT THE PORT'S EXISTING ROUTER SWITCH, AND THE DIFFERENCE MATTERS ───────
//
// `internal/server/ws.go` already handles `router:switch`, which PINS ONE SOCKET
// to a router. This is the other thing entirely: it moves the install's default,
// writes it to settings.json, tears the old session down and relocates every
// socket that was following that default. On a fresh install there is no active
// router at all, so the first-run overlay needs THIS and not that. They are not
// substitutes and must not be merged.
//
// ── WHY IT IS A PLAN ────────────────────────────────────────────────────────
//
// The live function is twelve module-level dependencies deep — the router store,
// the settings, the socket server, the session pool, three session helpers and
// the alerter. None of that is decidable; all of it is effects. What IS decidable
// is the ORDER and the CONDITIONS, and those are where the live comments say the
// behaviour lives:
//
//   - the old active id is read BEFORE the save, or the teardown targets the
//     router just switched to;
//   - only sockets following the OUTGOING router move, because "a global emit
//     would wipe charts/logs in every other user's browser";
//   - rooms are left BY PREFIX, so `router-r1:wifi` goes with `router-r1`;
//   - the synthetic status is `true` ONLY (#118).
//
// The switchrouter corpus runs the live function with all twelve stubbed
// and records the ordered operations; this reproduces that list.
//
// ── NO CALLER YET, AND IT CANNOT HAVE ONE ───────────────────────────────────
//
// `POST /api/routers/:id/activate` writes settings.json, which Node caches and
// would silently revert — cutover blocker 3. So the route waits for
// cutover and the decision is pinned now. Same arrangement as `pool.go` here.

import "strings"

// SwitchStep is one operation, in order.
type SwitchStep struct {
	// Op is one of: broadcastRosStatus, emit, settings.save, clearTimeout,
	// teardown, dropEvaluator, leave, join, ensureRouterSession, replay.
	Op string
	// Event and To describe an emit; Room is the target of a leave or join.
	Event, To, Room string
	// Socket names which client a leave or join applies to.
	Socket string
	// RouterID is the subject of teardown, dropEvaluator and
	// ensureRouterSession, and the value of a settings.save.
	RouterID string
	// Connected is the payload of a broadcastRosStatus or a ros:status emit.
	Connected bool
	// Reason accompanies broadcastRosStatus; Label rides on router:switching.
	Reason, Label string
}

// SocketState is one connected client, as the plan needs to see it.
type SocketState struct {
	ID string
	// RouterID is which router this socket currently watches.
	RouterID string
	// Rooms is every room it has joined, in a stable order.
	Rooms []string
}

// PooledSession is an entry in the live `_routerSessions` map.
type PooledSession struct {
	// HasIdleTimer decides whether a clearTimeout precedes the teardown.
	HasIdleTimer bool
}

// NewSessionState is what `ensureRouterSession` returns for the incoming router.
//
// An INPUT even though the live code obtains it midway, because the only two
// steps that read it are the last two. Modelling it as a return value would mean
// splitting this into two functions whose sole contract was "call them in
// order", which is a worse thing to hand a caller than one list.
type NewSessionState struct {
	// Connected is `rosConnected`: the pooled session was already up, so no
	// `connected` event fires and the client would never dismiss its switching
	// overlay (#118).
	Connected bool
	// StartupReady gates the REPLAY only. Collectors mid-start replay their own
	// tail shortly, and doing it here as well costs every socket a duplicate
	// fetchInterfaces.
	StartupReady bool
	// HasSession is false when the entry exists but carries no session.
	HasSession bool
}

// SwitchInput is everything the decision reads.
type SwitchInput struct {
	NewRouterID string
	// ActiveRouterID is the CURRENT global default, empty when there is none —
	// the first-run overlay's case, and the one where most of the plan is
	// skipped.
	ActiveRouterID string
	// Labels maps a router id to its label; an absent id is an unknown router.
	Labels map[string]string
	// Pooled is the live `_routerSessions` map.
	Pooled map[string]PooledSession
	// Sockets is every connected client.
	Sockets []SocketState
	// NewEntry is what ensureRouterSession will return; nil when it returns
	// nothing.
	NewEntry *NewSessionState
	// Switching is the `_switching` flag on entry.
	Switching bool
}

// SwitchPlan is the ordered work, or a refusal.
type SwitchPlan struct {
	Steps []SwitchStep
	// Err is the live `{ok:false, error}` message, empty on success.
	Err string
	// HoldsFlag says whether this path TOOK the `_switching` flag and must
	// therefore release it.
	//
	// ── THE ALREADY-IN-PROGRESS REFUSAL MUST NOT RELEASE IT ─────────────────
	//
	// That refusal returns BEFORE the live `try`, so the flag stays set — it
	// belongs to the switch still running, and clearing it would let a second
	// start on top of the first, which is the entire point of the guard. Every
	// other path goes through the `finally` and must clear it, or one failure
	// refuses every switch afterwards and nothing about that first failure looks
	// wrong.
	//
	// I had this backwards in the corpus generator's first draft, and running the
	// live implementation is what said so.
	HoldsFlag bool
}

// PlanRouterSwitch reproduces `switchRouter`'s decisions.
func PlanRouterSwitch(in SwitchInput) SwitchPlan {
	// THE TWO REFUSALS, in the live order and both before the flag is taken.
	if in.Switching {
		return SwitchPlan{Err: "Switch already in progress", HoldsFlag: true}
	}
	label, known := in.Labels[in.NewRouterID]
	if !known {
		return SwitchPlan{Err: "Router not found"}
	}

	plan := SwitchPlan{HoldsFlag: true}
	add := func(s SwitchStep) { plan.Steps = append(plan.Steps, s) }

	// THE OLD ID IS READ HERE, before the save. Everything below that names the
	// outgoing router depends on that ordering.
	old := in.ActiveRouterID
	oldEntry, hasOld := in.Pooled[old]

	if old != "" && hasOld {
		// The outgoing router's own watchers are told it is going, and by what.
		add(SwitchStep{Op: "broadcastRosStatus", Connected: false,
			Reason: "Switching to " + label + "…"})
	}
	if old != "" {
		// ROOM-SCOPED, not global. The live comment: "Only sockets watching the
		// outgoing router should reset their UI state — a global emit would wipe
		// charts/logs in every other user's browser."
		add(SwitchStep{Op: "emit", To: "router-" + old, Event: "router:switching",
			RouterID: in.NewRouterID, Label: label})
	}

	add(SwitchStep{Op: "settings.save", RouterID: in.NewRouterID})

	if old != "" && hasOld {
		if oldEntry.HasIdleTimer {
			add(SwitchStep{Op: "clearTimeout"})
		}
		add(SwitchStep{Op: "teardown", RouterID: old})
		// The alerter's edge-detection state goes with the session. Left behind,
		// the next session for that router compares against readings from before
		// the switch and reports transitions that never happened.
		add(SwitchStep{Op: "dropEvaluator", RouterID: old})
	}

	// RELOCATE THE FOLLOWERS. The live predicate is
	// `socket.routerId === oldActiveId && socket.routerId !== newRouterId`, and
	// the comment records what the previous one got wrong: it "skipped sockets
	// with an auth session, orphaning all modern-auth clients" — which is every
	// client on a modern install.
	// WITH NO OUTGOING ROUTER, NOBODY MOVES. The live predicate compares against
	// `oldActiveId`, which is null on a fresh install, and no socket's routerId
	// equals null — not even one following nothing, because `undefined === null`
	// is false. A Go port comparing two empty strings finds them EQUAL and
	// relocates every unattached socket.
	//
	// Found by a Go test disagreeing with a first draft of this function, then
	// settled by asking the live implementation: `NO previous active router, and
	// a socket following nothing` in the corpus moves nobody.
	for _, s := range in.Sockets {
		if old == "" || s.RouterID != old || s.RouterID == in.NewRouterID {
			continue
		}
		// BY PREFIX. `router-r1:wifi` is a room this socket holds for the
		// outgoing router and must go with it; `lobby` must not.
		prefix := "router-" + s.RouterID
		for _, room := range s.Rooms {
			if strings.HasPrefix(room, prefix) {
				add(SwitchStep{Op: "leave", Socket: s.ID, Room: room})
			}
		}
		add(SwitchStep{Op: "join", Socket: s.ID, Room: "router-" + in.NewRouterID})
	}

	add(SwitchStep{Op: "ensureRouterSession", RouterID: in.NewRouterID})

	// ── #118: THE SYNTHETIC STATUS ──────────────────────────────────────────
	//
	// When the incoming session was already pooled and connected,
	// `ensureRouterSession` returns early and no `connected` event fires — and
	// that event is the only producer of `ros:status{connected:true}`. The client
	// dismisses the switching modal on it and on nothing else, so switching to a
	// HEALTHY router left the operator behind a modal until they reloaded.
	//
	// TRUE ONLY. A synthetic false would be the client's second false and would
	// dismiss the overlay while the new router is still connecting.
	if in.NewEntry != nil && in.NewEntry.Connected {
		add(SwitchStep{Op: "emit", To: "router-" + in.NewRouterID, Event: "ros:status",
			Connected: true})
		if in.NewEntry.StartupReady && in.NewEntry.HasSession {
			add(SwitchStep{Op: "replay"})
		}
	}
	return plan
}
