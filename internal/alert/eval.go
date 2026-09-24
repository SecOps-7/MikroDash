package alert

// The alert evaluator's rules — the port of `createEvaluator`.
//
// ── EDGE DETECTION, NOT LEVEL DETECTION ─────────────────────────────────────
//
// Every rule compares a reading against a threshold AND against its own previous
// verdict, firing only on the TRANSITION. `system:update` arrives about every
// two seconds, so a rule that fired on the LEVEL would page the operator
// continuously while a CPU stayed busy. The alert-eval corpus pins it:
// `cpuGoesHighAndStaysHigh` fires once across four readings, and a level-testing
// port passes every other CPU case.
//
// ── WHAT IS HERE, AND WHAT IS DELIBERATELY NOT ──────────────────────────────
//
// Everything except the BGP family: the CPU rule, the RouterOS update check,
// ping loss, host reachability, interface up/down and VPN peers. BGP is not
// ported; the corpus records them as uncovered BY NAME, and its
// generator fails if a family appears in the live evaluator that is in neither
// list. A green suite here does not mean the evaluator is ported.
//
// ── AND NOTHING CALLS THIS YET, ON PURPOSE ──────────────────────────────────
//
// Wiring an evaluator to the notification transports is a CUTOVER step: both
// apps would evaluate the same conditions against the same routers, and the
// cooldown is an in-memory map rather than a shared row, so every alert would be
// sent twice. `internal/notify/send.go` carries the reasoning; the port record
// Part 25 records it.

import "strconv"

// Settings is the subset of the install settings the covered rules read.
// Settings is what the rules need from the install's configuration.
//
// ── THE notif* GATES ARE GONE ─────────────────────────────────────────────
//
// It carried NotifCPU, NotifPing, NotifIfaceUpDown, IfaceTypeFilters and the
// rest: one boolean per alert type deciding whether that type was raised at
// all. Notification channels replaced them. Every alert is recorded now, and a
// channel's Events tab decides what is DELIVERED — see `emit`.
//
// What is left is the two numbers that define when an event EXISTS, which is a
// different question from who hears about it and is the reason they stayed
// install-wide: a threshold per channel would leave `alert_events` with no
// single truth about whether an alert fired, and acknowledging one would mean
// nothing.
type Settings struct {
	CPUThreshold float64
	PingLoss     float64
}

// Router is what the evaluator needs to know about the device.
//
// `ID` IS LOAD-BEARING: the live `fire` guards its whole record-and-emit block
// on `router && router.id`, so a router without one evaluates every rule and
// emits nothing. Not a hypothetical — the corpus generator hit exactly that, and
// every case read as "no alert fired" until the fixture gained an id.
type Router struct {
	ID            string
	AlertsEnabled bool
}

// Fired is one alert the rules decided on. It carries the DECISION, not the
// delivery: what type, on what subject, and whether it opens or resolves.
type Fired struct {
	// Up is a resolution rather than a new alert.
	Up bool
	// AlertType is the DISPLAY form; `storedType` derives the stored one.
	// `ResolveType` overrides it on the resolving side, because the live comment
	// warns that anything else "silently fails to match the row".
	AlertType   string
	ResolveType string
	Subject     string
	Detail      string
	// Supersede resolves an alert of the SAME type and subject before filing
	// this one, instead of the dedup guard returning early because it is
	// already open. Only the RouterOS-update rule sets it — see below.
	Supersede bool
	// Silent means: show it, record it, do NOT notify.
	//
	// ── ONE PRODUCER, AND LIVE'S STRUCTURE IS THE REASON ──────────────────
	//
	// The supersede branch below emits a synthetic RESOLUTION for the alert being
	// replaced. In live that resolution goes out through `_emit` — the browser
	// and the Reports tab see it — and never reaches the delivery loop, because
	// `fire` runs the loop once for the alert it was called with and the
	// supersede resolution is not one of those calls.
	//
	// MEASURED by the alert-eval corpus once it began recording cooldown
	// keys: the update-supersede case emits THREE alerts and delivers TWO. A port
	// that notified on everything `Evaluate` returned would send an extra "up"
	// message every time a router's available version changed.
	Silent bool
}

// Store is the alert history the rules consult. `fire` returns early when an
// alert of the same type and subject is already open, which is the
// DEDUPLICATION half of the rules rather than a storage concern — see
// `ToDo.md` §7 for where that interacts surprisingly with the update check.
type Store interface {
	HasOpen(routerID, alertType, subject string) bool
	Resolve(routerID, alertType, subject string) []int64
	// Record inserts the open row, and `emit` calls it BEFORE returning rather
	// than leaving it to the caller.
	//
	// ── WHY THE EVALUATOR OWNS THE WRITE ────────────────────────────────
	//
	// The live `fire` inserts inline, so the SECOND alert in one event already
	// sees the first. Leaving it to the caller looks equivalent and is not:
	// `routing:update` carries every peer in one event, and two peers sharing a
	// name share an alert subject -- with the write deferred, both pass the
	// dedup guard and the operator gets two rows where the live app files one.
	// Found by `bgpTwoPeersOneName`, which is the only case in the corpus where
	// one event fires the same subject twice.
	Record(routerID, alertType, subject, detail string) int64
}

// Evaluator holds the per-router edge state. One per router, as the live
// `createEvaluator` is.
type Evaluator struct {
	settings Settings
	store    Store

	// prevCPUAlert is THREE-STATE: unknown, was alerting, was normal. The live
	// value starts `null`, and that matters — a first reading above the
	// threshold must alert. A port initialising it to `false` agrees by
	// accident; one initialising it to `true` goes silent on exactly that case,
	// which `firstReadingIsAlreadyHigh` pins.
	prevCPUAlert *bool

	// prevPingAlert is PER TARGET. A port holding one flag alerts on the first
	// lossy target and then goes quiet for the whole fleet, which
	// `twoTargetsAreIndependent` pins.
	prevPingAlert map[string]bool

	// prevNetwatchState is the STATUS STRING per host id, not a boolean — the
	// live map stores `host.status`, and `unknown` has to be distinguishable
	// from both. Absent means never seen, which is its own answer: see
	// NetwatchUpdate.
	prevNetwatchState map[string]string
	// prevNetwatchSince is the router's own "last changed at" per host id, which
	// is how an outage SHORTER THAN ONE READING is caught: see NetwatchUpdate.
	prevNetwatchSince map[string]string

	// prevIfState is the running AND disabled flags per interface name. Both are
	// needed: the disabled one is what tells an admin action from a real fault,
	// and it has to be remembered from the PREVIOUS reading as well as read from
	// this one -- see IfstatusUpdate.
	prevIfState map[string]ifState

	// prevVPNState is the STATE STRING per tunnel name. `peerState` emits
	// 'active' | 'stale' | 'never', and only 'active' is connected -- so the
	// string is kept rather than a boolean, because a change BETWEEN the two
	// disconnected values must not read as a transition.
	prevVPNState map[string]string

	// prevUpdateVersion is the version last alerted on, NOT a boolean. The live
	// comment: `system:update` fires every poll, so a boolean would re-fire as
	// soon as the cooldown lapsed. Empty means none announced.
	prevUpdateVersion string

	// FOUR SEPARATE BGP MAPS, all keyed on the peer's `key`. Three of them
	// remember whether an ALERT IS OPEN rather than what the reading was, which
	// is why they are bool and why the flap and hold rules need `seen` as well
	// as the value.
	prevBGPState    map[string]bool
	prevBGPPfx      map[string]float64
	prevBGPPfxAlert map[string]bool
	prevBGPFlap     map[string]bool
	prevBGPHold     map[string]bool
}

// stateMax is the live `STATE_MAX`, and capMap is `_capMap`: past the bound it
// PRUNES THE KEYS ABSENT FROM THE CURRENT PAYLOAD. Called once per family, after
// its loop, with the live key set collected as the loop runs.
//
// ── IT USED TO CLEAR, AND THAT WAS A DEFECT THIS PORT REPORTED ─────────────
//
// `if (m.size > STATE_MAX) m.clear()`, inside the per-item loop before every
// write. Crossing 500 discarded the previous state of the whole fleet
// mid-iteration, so everything after the clear read as never-seen — and an
// unknown previous state is not a transition, so nothing fired. Measured: 501
// interfaces going down produced ONE alert, and the map refilled from the clear
// point so the split moved rather than healed. Found here while porting the BGP
// family, fixed upstream the same day (`07da9a9`).
//
// ── AND THE OBVIOUS FIX IS BACKWARDS ───────────────────────────────────────
//
// `Map.set` on an EXISTING key does not move it, so insertion order is not
// recency. An interface present since startup and re-set on every pass holds
// position 0 for ever, while the churning pppoe peers that caused the growth sit
// at the end — an oldest-first trim evicts exactly what must be kept. Absent-key
// pruning is the only one of the three rules that can never forget something
// still live, and it needs no notion of recency at all.
//
// ── STILL GATED ON THE BOUND, AND THAT IS NOT AN OPTIMISATION ──────────────
//
// Under stateMax this touches nothing, because A PAYLOAD IS NOT ALWAYS THE WHOLE
// FLEET: upstream, an `ifstatus:update` can carry a provisional snapshot
// mid-cycle, and pruning against a partial list would drop live state and
// recreate the very bug being fixed. Above the bound the risk is worth taking,
// because the alternative there is discarding all of it.
//
// If more than stateMax entries are genuinely live the map simply stays that
// size. That is correct and looks like a leak: the bound exists to stop CHURN
// accumulating, and a real fleet is not churn.
const stateMax = 500

func capMap[V any](m map[string]V, live map[string]bool) {
	if len(m) <= stateMax || live == nil {
		return
	}
	for k := range m {
		if !live[k] {
			delete(m, k)
		}
	}
}

// NewEvaluator returns an evaluator with no prior verdicts.
func NewEvaluator(s Settings, store Store) *Evaluator {
	return &Evaluator{settings: s, store: store,
		prevPingAlert:     map[string]bool{},
		prevNetwatchState: map[string]string{},
		prevNetwatchSince: map[string]string{},
		prevIfState:       map[string]ifState{},
		prevVPNState:      map[string]string{},
		prevBGPState:      map[string]bool{},
		prevBGPPfx:        map[string]float64{},
		prevBGPPfxAlert:   map[string]bool{},
		prevBGPFlap:       map[string]bool{},
		prevBGPHold:       map[string]bool{},
	}
}

// SetSettings replaces the thresholds and toggles WITHOUT touching the edge
// state.
//
// ── THE EVALUATOR IS NOT REBUILT ON A SETTINGS SAVE, AND THAT IS THE POINT ─
//
// Every `prev*` map above is edge-detection memory: the rules fire when a value
// CROSSES a line, not while it sits past one. Rebuilding the evaluator to pick
// up new settings would clear that memory, so every currently-true condition
// would read as a fresh crossing — ticking one checkbox on the Settings page
// would produce a burst of alerts for things that had been true and quiet for
// hours.
//
// The live `updateSettings` replaces the settings object in place for the same
// reason. This exists so the port can do it too rather than dropping and
// recreating, which is the obvious-looking alternative.
func (e *Evaluator) SetSettings(s Settings) { e.settings = s }

// PingUpdate evaluates one `ping:update` event.
//
// ── THREE FALLBACKS FROM ONE READING, AND THEY DISAGREE ─────────────────────
//
// With no target at all the live rule produces three different answers:
//
//	the STATE KEY   `data.target || 'host'`   -> "host"
//	the SUBJECT     `data.target || ''`       -> "" (stored as null)
//	the DETAIL      `data.target` raw         -> the text "undefined"
//
// Reproduced rather than tidied — the detail is what an operator reads in an
// email, and a port that said "host" there would not match. `pingWithNoTarget`
// records all three.
//
// `target` and `loss` are POINTERS for the same reason `cpuLoad` is: absent is
// not the empty string, and a non-numeric loss is skipped WITHOUT disturbing the
// previous verdict.
func (e *Evaluator) PingUpdate(r Router, target *string, loss, rtt *float64) []Fired {
	if r.ID == "" || !r.AlertsEnabled || loss == nil {
		return nil
	}
	key := "host"
	subject := ""
	if target != nil && *target != "" {
		key, subject = *target, *target
	}
	// The `*target != ""` half is NOT observable, and that is recorded rather
	// than trimmed. Mutating it away survives every case: an empty target and an
	// absent one both give a subject of "", and `emit` deduplicates on the
	// SUBJECT — so a key of "" versus "host" can never produce a different
	// answer. It matches the live `data.target || 'host'` and stays for that
	// reason, not because a test defends it.

	isLoss := *loss >= e.settings.PingLoss
	prev, seen := e.prevPingAlert[key]

	var out []Fired
	switch {
	case isLoss && !(seen && prev):
		out = e.emit(r, Fired{
			AlertType: "Ping Loss",
			Subject:   subject,
			Detail:    "Ping loss to " + rawTarget(target) + " is " + trimNum(*loss) + "%",
		})
	case !isLoss && seen && prev:
		out = e.emit(r, Fired{
			Up:          true,
			AlertType:   "Ping Restored",
			ResolveType: "ping_loss",
			Subject:     subject,
			Detail:      "Ping to " + rawTarget(target) + " restored",
		})
	}
	e.prevPingAlert[key] = isLoss
	return out
}

// VPNTunnel is one row of a vpn:update.
type VPNTunnel struct {
	Name  string
	State string // "active" | "stale" | "never"
}

// VPNUpdate evaluates one vpn:update event.
//
// -- ONLY "active" IS CONNECTED, AND THE STRING MATTERS ---------------------
//
// `VpnCollector.peerState` emits 'active' | 'stale' | 'never'. The live comment
// records what happened when this consumer fell out of step with it: the
// collector used to emit 'connected'/'idle', this compared against those, and
// after the rename "wasConn and isConn were both permanently false and VPN
// alerts could not fire at all". A whole alert family silently stopped working
// and nothing failed.
//
// So the comparison is against "active" specifically, and BOTH other values mean
// disconnected -- a port checking `!= "stale"` misses a peer going to "never",
// and one comparing `prev != state` fires on stale->never, which is a change of
// state that is not a change of connectedness.
//
// The FIRST sighting never fires, as with netwatch and interfaces.
func (e *Evaluator) VPNUpdate(r Router, tunnels []VPNTunnel) []Fired {
	if r.ID == "" || !r.AlertsEnabled {
		return nil
	}
	out := []Fired{}
	live := map[string]bool{}
	for _, t := range tunnels {
		prev, seen := e.prevVPNState[t.Name]
		wasConn := prev == "active"
		isConn := t.State == "active"

		if seen && wasConn != isConn {
			if !isConn {
				out = append(out, e.emit(r, Fired{
					AlertType: "VPN Disconnected",
					Subject:   t.Name,
					Detail:    "VPN peer " + t.Name + " disconnected",
				})...)
			} else {
				out = append(out, e.emit(r, Fired{
					Up:          true,
					AlertType:   "VPN Connected",
					ResolveType: "vpn_disconnected",
					Subject:     t.Name,
					Detail:      "VPN peer " + t.Name + " connected",
				})...)
			}
		}
		live[t.Name] = true
		e.prevVPNState[t.Name] = t.State
	}
	capMap(e.prevVPNState, live)
	return out
}

// ifState is the pair the interface rule remembers per interface.
//
// BOTH FLAGS, not just `running`. The disabled one is what tells an admin action
// from a real fault, and it has to be remembered from the PREVIOUS reading as
// well as read from this one -- see IfstatusUpdate.
type ifState struct {
	Running  bool
	Disabled bool
}

// Interface is one row of an ifstatus:update.
type Interface struct {
	Name     string
	Type     string
	Comment  string
	Running  bool
	Disabled bool
}

// IfstatusUpdate evaluates one ifstatus:update event.
//
// -- AN ADMIN DISABLING AN INTERFACE ALSO STOPS IT RUNNING ------------------
//
// That used to fire "Interface Down". The live comment is blunt about it: the
// disabled flag "was already being captured for exactly this purpose and then
// never read". The suppression covers BOTH directions -- an admin re-enabling
// the interface must not produce an unpaired "Interface Up" either, which is why
// the PREVIOUS reading's disabled flag counts as well as this one's.
//
// -- AND TWO TOGGLES GATE EACH ALERT ----------------------------------------
//
// The feature itself (`notifIfaceUpDown`) and the filter for this interface's
// TYPE. Both are install-wide first: the live comment says the Interface Alert
// Filter "is expected to filter the bell, not merely the push". The type key is
// computed by `IfaceTypeKey(IfaceType(name, type))`, both already ported and
// pinned by the alerter corpus -- so an explicit type wins over the name,
// and a name ending `.10` is a vlan.
//
// The FIRST sighting never fires, as with netwatch: the guard is `prev !==
// undefined`.
func (e *Evaluator) IfstatusUpdate(r Router, ifaces []Interface) []Fired {
	if r.ID == "" || !r.AlertsEnabled {
		return nil
	}
	out := []Fired{}
	live := map[string]bool{}
	for _, i := range ifaces {
		prev, seen := e.prevIfState[i.Name]
		// The transition is ADMINISTRATIVE when either reading is disabled.
		adminToggled := i.Disabled || (seen && prev.Disabled)

		// The `seen` half is NOT observable, and that is recorded rather than
		// trimmed. Dropping it survives every case, for two reasons stacked: a
		// zero `ifState` has Running=false, so a first sighting of a DOWN
		// interface shows no transition at all; and a first sighting of an UP one
		// produces an "Interface Up" whose resolve finds nothing open, which
		// `emit` drops. The guard stays because it matches the live
		// `prev !== undefined` and because both of those props could move --
		// change the zero value or the resolve behaviour and it starts mattering.
		if seen && prev.Running != i.Running && !adminToggled {
			if !i.Running {
				out = append(out, e.emit(r, Fired{
					AlertType: "Interface Down",
					Subject:   i.Name,
					Detail:    i.Name + " went down",
				})...)
			} else {
				out = append(out, e.emit(r, Fired{
					Up:          true,
					AlertType:   "Interface Up",
					ResolveType: "interface_down",
					Subject:     i.Name,
					Detail:      i.Name + " came up",
				})...)
			}
		}
		// RECORDED EVEN WHEN THE TRANSITION WAS ADMINISTRATIVE, so the next real
		// change is measured from what the interface actually is now.
		live[i.Name] = true
		e.prevIfState[i.Name] = ifState{Running: i.Running, Disabled: i.Disabled}
	}
	capMap(e.prevIfState, live)
	return out
}

// NetwatchHost is one row of a netwatch:update.
type NetwatchHost struct {
	ID     string
	Host   string
	Name   string
	Status string // "up" | "down" | "unknown"
	// Since is when the router says this host last changed state. See
	// collect.NetwatchHost.Since, and the outage rule below.
	Since string
}

// NetwatchUpdate evaluates one netwatch:update event.
//
// -- TWO RULES THAT DIFFER FROM THE CPU AND PING FAMILIES --------------------
//
//  1. THE FIRST SIGHTING NEVER FIRES. The guard is `prev !== undefined`, so a
//     host that is already down when the page loads is recorded silently. CPU
//     and ping both alert on a first reading; a port reusing their shape would
//     page on every reconnect, for every host that happens to be down.
//
//  2. An `unknown` status is SKIPPED AND DOES NOT UPDATE THE STATE. The live
//     `continue` jumps the `set` at the bottom of the loop as well, so a
//     transient re-probe leaves the previous status intact -- it does not become
//     the new baseline, and it does not make the next real reading look like a
//     transition. An `unknown` FIRST sighting therefore leaves the host unseen,
//     and the reading after it is still a first sighting.
//
// The state holds the STATUS STRING rather than a boolean, because "never seen",
// "seen as unknown" and "seen as up" are three different answers.
func (e *Evaluator) NetwatchUpdate(r Router, hosts []NetwatchHost) []Fired {
	if r.ID == "" || !r.AlertsEnabled {
		return nil
	}
	out := []Fired{}
	live := map[string]bool{}
	for _, h := range hosts {
		// RECORDED BEFORE THE `unknown` SKIP. A host being re-probed is still
		// live, and pruning it would throw away the state its next reading is
		// compared against -- the same class of bug the prune exists to fix, in
		// miniature.
		live[h.ID] = true
		if h.Status == "unknown" {
			continue
		}
		prev, seen := e.prevNetwatchState[h.ID]
		// `== "down"` and `!= "up"` are INTERCHANGEABLE here, and only because of
		// the skip above: `unknown` never reaches the state, so a seen host is
		// either "up" or "down" and nothing else. Mutating one into the other
		// survives every case — recorded rather than counted, and worth knowing
		// because the equivalence is not a property of THIS line. Let `unknown`
		// be stored and the two forms disagree immediately.
		wasDown := prev == "down"
		isDown := h.Status == "down"

		// ── AN OUTAGE SHORTER THAN ONE READING ─────────────────────────────
		//
		// This table is read once a minute, and the operator's hosts went down
		// and back up in thirty seconds (2026-09-20): both readings said "up",
		// `wasDown != isDown` was false, and nothing fired while the router's own
		// script had already told Home Assistant twice.
		//
		// `since` is the router's own "last changed at". If it MOVED while the
		// status looks unchanged, the host changed state at least twice in
		// between, and the one that matters is an up host that was down: it is
		// reported as the pair it really was, a down and then its recovery, in
		// that order so the recovery has a row to resolve.
		//
		// ONLY FOR A HOST THAT IS UP AND WAS UP. A host that is still down has an
		// alert open already, and re-raising it on every flap would be noise.
		// A ROUTER REBOOT re-probes every host and restamps `since`, so a reboot
		// reports one pair per host: the hosts were unreachable from the router,
		// and the alternative is trusting a timestamp across a restart.
		prevSince, sinceSeen := e.prevNetwatchSince[h.ID]
		missedOutage := seen && sinceSeen && !isDown && !wasDown &&
			h.Since != "" && prevSince != "" && h.Since != prevSince
		e.prevNetwatchSince[h.ID] = h.Since

		if seen && wasDown != isDown || missedOutage {
			name := h.Name
			if name == "" {
				name = h.Host
			}
			// `name (host)` only when they differ -- repeating the address would
			// read as "10.0.0.1 (10.0.0.1)".
			desc := h.Host
			if name != h.Host {
				desc = name + " (" + h.Host + ")"
			}
			if missedOutage {
				// THE DOWN FIRST, so the recovery below has a row to resolve:
				// `emit` only reports an "up" when something was actually
				// closed. The detail says when the router saw it come back,
				// because "is unreachable" for a host that is reachable now
				// would be a lie by the time it is read.
				out = append(out, e.emit(r, Fired{
					AlertType: "Host Down",
					Subject:   name,
					Detail:    "NetWatch host " + desc + " was unreachable",
				})...)
				out = append(out, e.emit(r, Fired{
					Up:          true,
					AlertType:   "Host Up",
					ResolveType: "host_down",
					Subject:     name,
					Detail:      "NetWatch host " + desc + " is reachable again (the router saw it return at " + h.Since + ")",
				})...)
			} else if isDown {
				out = append(out, e.emit(r, Fired{
					AlertType: "Host Down",
					Subject:   name,
					Detail:    "NetWatch host " + desc + " is unreachable",
				})...)
			} else {
				out = append(out, e.emit(r, Fired{
					Up:          true,
					AlertType:   "Host Up",
					ResolveType: "host_down",
					Subject:     name,
					Detail:      "NetWatch host " + desc + " is reachable",
				})...)
			}
		}
		e.prevNetwatchState[h.ID] = h.Status
	}
	capMap(e.prevNetwatchState, live)
	capMap(e.prevNetwatchSince, live)
	return out
}

// rawTarget is `” + data.target` — JavaScript's interpolation of an ABSENT
// value, which is the four-character string "undefined" and not an empty one.
// See the note on PingUpdate.
func rawTarget(t *string) string {
	if t == nil {
		return "undefined"
	}
	return *t
}

// SystemUpdate evaluates one `system:update` event and returns what fires, in
// order.
//
// `cpuLoad` is a POINTER because absent is not zero: the live rule is guarded on
// `typeof data.cpuLoad === 'number'`, and a reading that is not a number is
// skipped WITHOUT disturbing the previous verdict. A port taking a plain float64
// would read a missing value as 0, decide the CPU had recovered, and fire a
// spurious resolution.
func (e *Evaluator) SystemUpdate(r Router, cpuLoad *float64,
	updateAvailable bool, latestVersion, runningVersion string) []Fired {

	// Re-read on EVERY event rather than captured at construction, matching the
	// live comment: "in case it was toggled after session creation".
	if r.ID == "" || !r.AlertsEnabled {
		return nil
	}

	out := []Fired{}
	out = append(out, e.cpuRule(r, cpuLoad)...)
	out = append(out, e.updateRule(r, updateAvailable, latestVersion, runningVersion)...)
	return out
}

// SystemCPUOnly evaluates the CPU half of a `system:update` event and leaves the
// update verdict alone.
//
// ── FOR A PAYLOAD THAT CARRIES NO UPDATE INFORMATION AT ALL ───────────────
//
// `UpdateVerdict` conflates "not checked yet" with "up to date": with no
// `latest-version` and no `status` it returns false, and `updateRule` reads
// false as "the router reached the version" and RESOLVES an open alert.
//
// That is harmless in the live app, which shares one update result across every
// SystemCollector for a router. This port did not — `collect/system.go` says so
// outright: "This port builds ONE session per router, so the schedule lives on
// the collector. A second session type would need the shared map back."
// `internal/alertpool` WAS that second session type, and its collector had
// usually never run the check, so its payloads said "no update" and closed the
// alert the session's collector had just opened. MEASURED: 50 fire/resolve pairs
// in 24 hours against zero in the live app.
//
// ── THE SECOND SESSION TYPE IS GONE, AND THIS SPLIT STAYS ─────────────────
//
// The alert pool was deleted on 2026-09-09: every router nobody is watching is
// held by `session.Manager` now, so the premise above — two collectors for one
// router, disagreeing about the update check — cannot arise from that source.
//
// THAT IS NOT A REASON TO FOLD THIS BACK IN. The defect is in `UpdateVerdict`
// conflating "not checked" with "up to date", and it fires for ANY payload that
// carries no update information: a primed reading, a first tick before the
// check has run, a collector whose check failed. Removing the split would make
// the app correct only for as long as exactly one collector per router ever
// produces a `system:update` payload, and nothing enforces that. Kept, with the
// premise updated rather than the guard removed.
//
// ── WHY A SEPARATE METHOD RATHER THAN A FLAG ──────────────────────────────
//
// `SystemUpdate` is gated against the live evaluator by
// The alert-eval corpus. Adding an argument live does not have would make
// every case in that corpus a port-specific shape. This is a port-specific
// SPLIT, so the port-specific decision lives in `internal/alertwire`, which
// chooses between the two — and the rule this file gates stays exactly live's.
//
// It mirrors `cpuRule`'s own precedent for the same class: "a reading that is
// not a number is skipped WITHOUT disturbing the previous verdict".
func (e *Evaluator) SystemCPUOnly(r Router, cpuLoad *float64) []Fired {
	if r.ID == "" || !r.AlertsEnabled {
		return nil
	}
	return e.cpuRule(r, cpuLoad)
}

func (e *Evaluator) cpuRule(r Router, cpuLoad *float64) []Fired {
	if cpuLoad == nil {
		return nil
	}
	// `>=`, so exactly the threshold alerts.
	isHigh := *cpuLoad >= e.settings.CPUThreshold

	var out []Fired
	switch {
	case isHigh && (e.prevCPUAlert == nil || !*e.prevCPUAlert):
		out = e.emit(r, Fired{
			AlertType: "High CPU",
			Detail: "CPU at " + trimNum(*cpuLoad) + "% (threshold: " +
				trimNum(e.settings.CPUThreshold) + "%)",
		})
	case !isHigh && e.prevCPUAlert != nil && *e.prevCPUAlert:
		out = e.emit(r, Fired{
			Up:          true,
			AlertType:   "CPU Normal",
			ResolveType: "high_cpu",
			Detail:      "CPU back to " + trimNum(*cpuLoad) + "% (below threshold)",
		})
	}
	// RECORDED WHETHER OR NOT ANYTHING FIRED, and after the decision. A toggle
	// that suppressed the alert must still advance the edge state, or switching
	// the type back on would fire about a transition that happened while it was
	// off.
	v := isHigh
	e.prevCPUAlert = &v
	return out
}

func (e *Evaluator) updateRule(r Router, available bool, latest, running string) []Fired {
	switch {
	case available && latest != "":
		// Only on a version not yet announced. Without this the alert would
		// repeat on every poll once the cooldown expired.
		if e.prevUpdateVersion == latest {
			return nil
		}
		// SUPERSEDE ONLY WHEN AN EARLIER VERSION WAS ACTUALLY OBSERVED.
		//
		// This is in-memory, so a REBUILT EVALUATOR STARTS EMPTY and every open
		// alert would look like a new release — which is exactly the "rings the
		// bell again on every rebuild" failure the dedup guard exists to
		// prevent. Empty means "nothing announced yet", and the guard stands.
		supersede := e.prevUpdateVersion != ""
		e.prevUpdateVersion = latest
		return e.emit(r, Fired{
			AlertType: "RouterOS Update",
			Supersede: supersede,
			Detail: "RouterOS " + latest + " is available (running " +
				cleanVersion(running) + ")",
		})

	case !available && e.prevUpdateVersion != "":
		// The router reached the version, or the channel changed. Clear the open
		// alert so the Reports tab does not show it pending for ever.
		e.prevUpdateVersion = ""
		return e.emit(r, Fired{
			Up:        true,
			AlertType: "RouterOS Updated",
			// THE STORED FORM, not the display one. The live comment: `fire`
			// lowercases and underscores `vars.alertType` before inserting but
			// passes `resolveType` through untouched, so anything else here
			// silently fails to match the row.
			ResolveType: "routeros_update",
			Detail:      "RouterOS is up to date (" + cleanVersion(running) + ")",
		})
	}
	return nil
}

// emit is the port of `fire`'s gates: the install-wide type toggle, then the
// store.
//
// THE TOGGLE IS CHECKED HERE, before anything is recorded or sent. #109 moved it
// down into per-recipient delivery so a user could opt IN to a type the install
// had switched off, and the live comment records what that cost: "the Interface
// Alert Filter stopped filtering the notification bell, so switching Wireless
// off silenced the push and still rang the bell on every wlan flap. A filter
// that does not filter what you are looking at is not a filter."
// emit records one alert and returns it.
//
// ── IT NO LONGER GATES ON AN ALERT TYPE ───────────────────────────────────
//
// It used to take `typeEnabled` and return nil for a type switched off
// install-wide, so a disabled type was never recorded OR sent. Issue #109 moved
// that check down into delivery and had to be reverted, because the
// notification bell then stopped honouring the Interface Alert Filter.
//
// The gate is gone entirely now, which dissolves that tension rather than
// choosing a side: MikroDash RECORDS every alert, so the bell and the Alerts
// page are a complete log, and each notification channel chooses which of them
// it delivers. There is one list of event types in the app and it lives on the
// channel.
func (e *Evaluator) emit(r Router, f Fired) []Fired {
	stored := storedType(f.AlertType)

	if f.Up {
		resolve := f.ResolveType
		if resolve == "" {
			resolve = stored
		}
		if ids := e.store.Resolve(r.ID, resolve, f.Subject); len(ids) == 0 {
			// Nothing was open, so nothing resolved. The live code emits only
			// when `ids && ids.length`.
			return nil
		}
		return []Fired{f}
	}

	// DEDUPLICATION. An alert of this type and subject already open is not
	// re-raised — an alert for a condition that PERSISTS would otherwise ring
	// the bell again every time the evaluator is rebuilt.
	//
	// ...UNLESS the rule SUPERSEDES, in which case the open row is resolved
	// first and the new one filed. That was ToDo.md §7: the update rule keys on
	// the version so a later release still notifies, and the keying alone did
	// nothing, because both alerts carry type `routeros_update` and an empty
	// subject and the second was swallowed here. Fixed upstream on 2026-08-27.
	//
	// NOT fixed by putting the version in the SUBJECT. The recovery resolves on
	// (routeros_update, ""), so a versioned subject would match nothing and
	// every update alert would stay open for ever — the alert key and the
	// resolution key are the same key.
	//
	// A supersede that resolves NOTHING still fires: the resolved event is
	// conditional on rows having gone, the fire is not.
	if e.store.HasOpen(r.ID, stored, f.Subject) {
		if !f.Supersede {
			return nil
		}
		if stale := e.store.Resolve(r.ID, stored, f.Subject); len(stale) > 0 {
			e.store.Record(r.ID, stored, f.Subject, f.Detail)
			return []Fired{
				// SILENT: emitted and recorded, never notified. See Fired.Silent.
				{Up: true, AlertType: f.AlertType, ResolveType: stored,
					Subject: f.Subject, Detail: f.Detail, Silent: true},
				f,
			}
		}
	}
	e.store.Record(r.ID, stored, f.Subject, f.Detail)
	return []Fired{f}
}

// StoredType is `(vars.alertType || key).toLowerCase().replace(/\s+/g, '_')`,
// exported so a caller recording a row derives it the same way.
func StoredType(display string) string { return storedType(display) }

func storedType(display string) string {
	out := make([]rune, 0, len(display))
	space := false
	for _, ch := range display {
		if ch == ' ' || ch == '\t' || ch == '\n' {
			space = true
			continue
		}
		if space && len(out) > 0 {
			out = append(out, '_')
		}
		space = false
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		out = append(out, ch)
	}
	return string(out)
}

// cleanVersion is `(data.version || ”).replace(/\s*\(.*\)/, ”).trim() || 'unknown'`.
//
// A RouterOS version string carries its channel in parentheses — "7.14.3
// (stable)" — and the alert text wants the number alone. The regex is GREEDY and
// unanchored: it removes from the FIRST opening bracket to the LAST closing one,
// so a string with two bracketed parts loses everything between them. Reproduced
// rather than tidied, because the detail text is what an operator reads in an
// email and a port that kept more would not match.
func cleanVersion(v string) string {
	openAt := -1
	closeAt := -1
	for i, ch := range v {
		if ch == '(' && openAt < 0 {
			openAt = i
		}
		if ch == ')' {
			closeAt = i
		}
	}
	if openAt >= 0 && closeAt > openAt {
		// `\s*` before the bracket goes too.
		start := openAt
		for start > 0 && (v[start-1] == ' ' || v[start-1] == '\t' || v[start-1] == '\n') {
			start--
		}
		v = v[:start] + v[closeAt+1:]
	}
	v = trimSpace(v)
	if v == "" {
		return "unknown"
	}
	return v
}

// trimSpace is JavaScript's `String.prototype.trim` for the whitespace a version
// string realistically carries.
func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

// trimNum renders a number the way JavaScript's string concatenation does: `90`
// and not `90.0`, `90.5` and not `90.50`.
func trimNum(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ── BGP: FOUR RULES OVER ONE `routing:update`, each with its own memory ─────
//
// state (established or not), prefix swing, flapping, and a misconfigured hold
// timer. They share the loop and nothing else -- four separate maps, four
// separate alert types, and they can all fire for one peer in one reading.
//
// ── THE STATE MAPS KEY ON `key`; THE ALERT SUBJECT IS THE PEER NAME ─────────
//
// `subject` in the live `fire` is `vars.bgpPeer`, which is `name ||
// remoteAddr || key`. So two peers with the same name share one open alert row
// while keeping separate state here. Reproduced, because the deduplication that
// falls out of it is live behaviour; recorded because it is the same shape as
// the interface rule's and is worth a look upstream rather than a silent fix.

// BGPPeer is one row of a routing:update.
//
// Prefixes, HoldTime and Keepalive are POINTERS because the rules guard on
// `typeof p.prefixes === 'number'` and on comparisons that a JS undefined loses.
// Against the real collector they are never absent -- `safeInt` in
// `collectors/routing.js` answers 0 rather than undefined for everything -- so
// the nil cases below exercise a guard the live app's own collector cannot
// reach. Kept anyway: the rules are what is being ported, and `evaluate` is
// reachable from anything that emits the event.
type BGPPeer struct {
	Key         string
	Name        string
	RemoteAddr  string
	Description string
	State       string
	Prefixes    *float64
	Flapping    bool
	HoldTime    *float64
	Keepalive   *float64
}

// bgpPfxThresh is the fraction the advertised prefix count must move to be worth
// an alert. `BGP_PFX_THRESH` in the original.
const bgpPfxThresh = 0.2

// RoutingUpdate is the BGP half of the evaluator.
func (e *Evaluator) RoutingUpdate(r Router, peers []BGPPeer) []Fired {
	if r.ID == "" || !r.AlertsEnabled {
		return nil
	}
	out := []Fired{}
	live := map[string]bool{}
	for _, p := range peers {
		// A PEER WITH NO KEY IS SKIPPED ENTIRELY, before any rule runs. Every
		// map here is keyed on it, so a blank key would merge unrelated peers
		// into one state slot.
		if p.Key == "" {
			continue
		}
		live[p.Key] = true
		peer := p.Name
		if peer == "" {
			peer = p.RemoteAddr
		}
		if peer == "" {
			peer = p.Key
		}
		where := peer
		if p.RemoteAddr != "" {
			where = peer + " (" + p.RemoteAddr + ")"
		}
		isEst := p.State == "established"

		// ── 1. STATE ───────────────────────────────────────────────────────
		//
		// Boolean, not the state string: `idle` -> `connect` is not a
		// transition anybody wants paging them, only established -> not.
		if prev, seen := e.prevBGPState[p.Key]; seen && prev != isEst {
			if !isEst {
				state := p.State
				if state == "" {
					state = "unknown"
				}
				out = append(out, e.emit(r, Fired{
					AlertType: "BGP Peer Down",
					Subject:   peer,
					Detail:    "BGP peer " + where + " left established (" + state + ")",
				})...)
			} else {
				out = append(out, e.emit(r, Fired{
					Up:          true,
					AlertType:   "BGP Peer Up",
					ResolveType: "bgp_peer_down",
					Subject:     peer,
					Detail:      "BGP peer " + where + " is established",
				})...)
			}
		}
		e.prevBGPState[p.Key] = isEst

		// ── 2. PREFIX SWING ────────────────────────────────────────────────
		//
		// Against the previous ESTABLISHED reading, so a session bounce does
		// not read as a 100% drop -- the peer-down alert already covers that,
		// and counting it twice is noise. `oldPfx > 0` is load-bearing too: a
		// peer that advertised nothing would divide by zero, and every first
		// prefix from it would read as an infinite swing.
		if isEst && p.Prefixes != nil {
			now := *p.Prefixes
			if oldPfx, seen := e.prevBGPPfx[p.Key]; seen && oldPfx > 0 {
				delta := now - oldPfx
				if delta < 0 {
					delta = -delta
				}
				swung := delta/oldPfx >= bgpPfxThresh
				if swung && !e.prevBGPPfxAlert[p.Key] {
					dir := "-"
					if now > oldPfx {
						dir = "+"
					}
					out = append(out, e.emit(r, Fired{
						AlertType: "BGP Prefix Change",
						Subject:   peer,
						Detail: peer + ": " + dir + trimNum(delta) + " prefixes (" +
							trimNum(oldPfx) + " → " + trimNum(now) + ")",
					})...)
					e.prevBGPPfxAlert[p.Key] = true
				} else if !swung && e.prevBGPPfxAlert[p.Key] {
					// The count held steady for a reading, so the table has
					// settled.
					out = append(out, e.emit(r, Fired{
						Up:          true,
						AlertType:   "BGP Prefixes Settled",
						ResolveType: "bgp_prefix_change",
						Subject:     peer,
						Detail:      peer + ": prefix count steady at " + trimNum(now),
					})...)
					e.prevBGPPfxAlert[p.Key] = false
				}
			}
			e.prevBGPPfx[p.Key] = now
		}

		// ── 3. FLAPPING ────────────────────────────────────────────────────
		if p.Flapping != e.prevBGPFlap[p.Key] {
			if p.Flapping {
				out = append(out, e.emit(r, Fired{
					AlertType: "BGP Session Flapping",
					Subject:   peer,
					Detail:    "BGP session " + where + " is flapping",
				})...)
			} else if _, seen := e.prevBGPFlap[p.Key]; seen {
				// The live `else if (prevBgpFlap.has(key))` guard, and it is an
				// EQUIVALENT check rather than a reachable one: getting here
				// needs `flapping === false` and the remembered value truthy,
				// and only a `set` makes it truthy. Reproduced because the
				// original has it, not because a case can reach the false arm.
				out = append(out, e.emit(r, Fired{
					Up:          true,
					AlertType:   "BGP Session Stable",
					ResolveType: "bgp_session_flapping",
					Subject:     peer,
					Detail:      "BGP session " + where + " has stopped flapping",
				})...)
			}
			e.prevBGPFlap[p.Key] = p.Flapping
		}

		// ── 4. HOLD TIMER / KEEPALIVE MISCONFIGURATION ─────────────────────
		//
		// `isEst && holdTime > 0 && holdTime < 9 && keepalive === 0`. A nil
		// holdTime fails `> 0` the way a JS undefined does; a nil keepalive
		// fails `=== 0`, which is NOT the same as failing `== 0` -- the live
		// check is strict, so an absent keepalive is not a misconfiguration.
		badHold := isEst && p.HoldTime != nil && *p.HoldTime > 0 && *p.HoldTime < 9 &&
			p.Keepalive != nil && *p.Keepalive == 0
		if badHold != e.prevBGPHold[p.Key] {
			if badHold {
				out = append(out, e.emit(r, Fired{
					AlertType: "BGP Hold Timer Warning",
					Subject:   peer,
					Detail: peer + ": hold-time=" + trimNum(*p.HoldTime) +
						"s, keepalive=0",
				})...)
			} else if _, seen := e.prevBGPHold[p.Key]; seen {
				out = append(out, e.emit(r, Fired{
					Up:          true,
					AlertType:   "BGP Hold Timer OK",
					ResolveType: "bgp_hold_timer_warning",
					Subject:     peer,
					Detail:      peer + ": hold timer no longer misconfigured",
				})...)
			}
			e.prevBGPHold[p.Key] = badHold
		}
	}
	// ALL FIVE SHARE ONE KEY SET. `prevBGPPfxAlert` was never bounded at all --
	// written on the same churning key and simply missed, upstream and in the
	// first version of this port, which mirrored the four it saw capped.
	capMap(e.prevBGPState, live)
	capMap(e.prevBGPPfx, live)
	capMap(e.prevBGPFlap, live)
	capMap(e.prevBGPHold, live)
	capMap(e.prevBGPPfxAlert, live)
	return out
}
