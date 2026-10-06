package server

import (
	"encoding/json"
	"log"
	"os"
	"time"

	"mikrodash/internal/connstate"
	"mikrodash/internal/routers"
	"mikrodash/internal/store"
)

// The Devices page's `routers:stats` payload, and the background pool that fills
// the rows for routers nobody is looking at.
//
// ── THIS IS THE CALLER `pool.go` WAS WRITTEN WITHOUT ────────────────────────
//
// `internal/routers` has held both halves for some time — `SyncPool` and the
// per-session lifecycle as pure state in `overview.go`, the sockets and the
// three collectors in `pool.go` — and was constructed by nobody, because whether
// a Go pool may run DURING COEXISTENCE was an operator decision: Node runs the
// same pool against the same fleet, so both holding a connection to every router
// at once is a real cost. The strangler rule was lifted; this is the caller.
//
// ── THE EXCLUSION IS THE WHOLE CORRECTNESS ARGUMENT ─────────────────────────
//
// A router somebody has OPEN must not also get a background session. Two
// connections to one router is the visible cost; recording every up/down
// transition twice is the one that corrupts history. So `excluded` is derived
// from the session manager on every sync rather than tracked separately — a
// second source of truth about who is watching what is exactly the thing that
// drifts.

// decodeGeo turns the record's raw `geo` block into the map ResolveLocation
// validates, and answers nil for anything it cannot read.
//
// LENIENT ON PURPOSE, matching why the field is stored raw: the block is
// operator-editable, and `geoplace.ResolveLocation` already checks every value
// it uses. A router with a malformed `geo` loses its pin; it must not take the
// rest of the fleet's rows down with it, which a hard failure here would do.
func decodeGeo(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// buildStatsSources gathers everything `routers.BuildStats` needs. `activeID`
// is the router this viewer has selected, the one row the page marks active.
//
// Assembled HERE rather than in `internal/routers` so that package keeps no
// dependency on sessions, the store or the database: it is pure, and its tests
// run without any of them.
func (s *Server) buildStatsSources(sess *Session, activeID string) routers.StatsSources {
	out := routers.StatsSources{
		ActiveID:   activeID,
		Main:       map[string]routers.MainSession{},
		Online:     map[string]bool{},
		OpenAlerts: map[string]int{},
		Sites:      map[string]routers.Site{},
	}
	if s.store == nil {
		return out
	}

	all, problems := s.store.Routers()
	for _, p := range problems {
		// A router whose password will not decrypt still BELONGS ON THE PAGE —
		// with its row, its name and its site. Dropping it would make a
		// misconfigured credential look like a deleted device.
		log.Printf("[devices] %v", p)
	}
	for _, r := range all {
		// THE DEBOUNCED VERDICT, asked per router rather than taken off the
		// session's socket state. An absent entry is left absent: `BuildStats` falls
		// back to the live socket for a router nothing has judged yet, and
		// writing `false` here would be the "every card is red on first open"
		// defect again, in a new place.
		if up, known := s.connTrack.Online(r.ID); known {
			out.Online[r.ID] = up
		}
		out.Routers = append(out.Routers, routers.StatsRouter{
			ID: r.ID, Label: r.Label, Host: r.Host, Disabled: r.Disabled,
			SiteIDs: store.RouterSiteIDs(r),
			// The profile's name, resolved by `Routers()`; "" for an own login.
			LoginProfile: r.LoginProfileName,
			// ── WITHOUT THIS THE MAP PLOTS NOTHING ────────────────────────
			//
			// `BuildStats` copies this into the row's `Geo`, and
			// `geoplace.ResolveLocation` reads it for both the manual place and
			// the automatic fix. It was never set, so every device arrived with a
			// nil location and the map dropped ALL of them into the "No location"
			// tray — including ones whose town somebody had picked by hand.
			//
			// Only a site location survived, because that tier resolves from a
			// different source. The router LIST payload was fine throughout: it
			// is built from the raw record map, which kept `geo` all along, so
			// the data was on disk and reaching the browser on one path and not
			// the other.
			Geo: decodeGeo(r.Geo),
		})
	}

	// EVERY SESSION, which since 2026-10-01 means every enabled router: each is
	// held WARM from startup (fleet_holds.go). Presence decides which rows read a
	// payload; `Connected()` decides what the row says, because a session exists
	// before it connects.
	if s.sessions != nil {
		for id, sn := range s.sessions.Live() {
			m := routers.MainSession{
				Connected: sn.Connected(), Known: sn.Observed(), LastError: sn.LastError(),
				// THE PRIMED READING WHEN THE COLLECTOR HAS NONE YET. A warm
				// session runs no system collector; the `devices` hold starts one
				// on focus and its first tick is a couple of seconds away, and
				// the one-shot prime is what stops the card drawing a green
				// badge over blank gauges in between.
				System: sn.SystemOrPrimed(),
			}
			if c := sn.DHCPLeases(); c != nil {
				m.DHCPLeases = c.Last()
			}
			out.Main[id] = m
		}
	}

	if s.auditDB != nil {
		if counts, err := s.auditDB.CountOpenAlertsByRouter(); err == nil {
			out.OpenAlerts = counts
		} else {
			log.Printf("[devices] open alert counts: %v", err)
		}
		// UNFILTERED, deliberately, matching `db.listSites()`. An unresolvable id
		// therefore means the site was DELETED, not "hidden from this viewer" —
		// a permission-filtered source would make the site dropdown differ per
		// viewer, and nothing user-visible may change.
		if sites, err := s.auditDB.ListSites(); err == nil {
			for _, st := range sites {
				out.Sites[st.ID] = routers.Site{Name: st.Name}
			}
		} else {
			log.Printf("[devices] sites: %v", err)
		}
	}

	out.Visible = s.visibleRouters(sess)
	return out
}

// The router list ONE principal may see, in TWO shapes — because the live app
// has two and they differ.
//
// ── WHAT CHANGED ON 2026-08-28 ──────────────────────────────────────────────
//
// This was a single function returning a typed `publicRouter` of ELEVEN fields.
// The live payload carries twenty-three. Live verification — the Go server and
// Node asked for `/api/routers` with the same cookie against the same /data —
// showed twelve keys missing:
//
//	addedAt  alertsEnabled  backup  connDownThresholdSec  geo  model
//	osVersion  password  pingTarget  serial  siteId  tlsInsecure
//
// The Routers page shows `model` and `osVersion`; the Add/Edit modal reads
// `pingTarget`, `tlsInsecure`, `backup` and `geo`, so it seeded defaults and a
// save would have written them over the operator's values.
//
// The old header argued the absence was STRONGER than the live masking — "a mask
// that is forgotten leaks, an absent field cannot". True of the password and
// false of the other eleven, which are not secrets and are not optional.
// `store.PublicRouters` now does what `getPublic()` does: keep everything, mask
// the password, fold `backup.password` into `hasPassword`.
//
// ── AND RULE 3 IS NO LONGER VACUOUS ─────────────────────────────────────────
//
// It used to read: "THE WAN ADDRESS IS STRIPPED from `geo.auto.ip` for anyone
// without `system:settings`… RULE 3 IS VACUOUS IN THIS PORT TODAY", because
// `store.Router` had no `Geo` field and no WAN address reached the payload.
// It does now. The note predicted its own expiry — "the day `Geo` is added to
// that struct is the day the disclosure reopens" — and this is that day.
//
// ── ONE SHAPE, SINCE 2026-08-29 ─────────────────────────────────────────────
//
//	routerListForSocket  filtered + STRIPPED   `routers:update` AND `GET /api/routers`
//
// There used to be a second, unstripped shape here for the HTTP route, because
// the live HTTP route did not strip while `/api/localcc`, the socket payload and
// the stats payload all did. That divergence was REPRODUCED rather than quietly
// fixed — a port that withholds a field the live app sends is a user-visible
// change — and filed in `../MikroDash/ToDo.md` on 2026-08-28. This note said
// "when upstream fixes it, delete `routerListFor` and let both callers use the
// socket shape", and upstream fixed it in `a4ac96e` on 2026-08-29, with
// `_stripWanIp` lifted to module scope and called from both paths. So that is
// what this now is.
//
// The upstream commit message is worth keeping, because it is this port's own
// finding coming back: "Found by the Go/TypeScript port's endpoint-by-endpoint
// payload diff — not by any test, because a round trip through one
// implementation agrees with itself whatever it disclosed."
//
// ONE FUNCTION, NOT TWO THAT AGREE. Upstream's own account of the bug is that
// the rule had three copies and the fourth site was the one nobody wrote. A
// second projection here is the same hazard in Go.
func (s *Server) routerRecordsFor(sess *Session) []map[string]any {
	out := []map[string]any{}
	if s.store == nil {
		return out
	}
	all, err := s.store.PublicRouters()
	if err != nil {
		// NOT an empty fleet. A damaged routers.json reads to the page as "add
		// your first router", which is the wrong thing to tell somebody who has
		// three.
		log.Printf("[devices] routers: %v", err)
		return out
	}
	visible := s.visibleRouters(sess)
	for _, r := range all {
		id, _ := r["id"].(string)
		// A NIL visible set means unrestricted; an empty one means this principal
		// may read nothing. Both are reachable and they are opposite answers.
		if visible != nil && !visible[id] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// routerListForSocket is `_routersForSocket`: the routers this principal may
// read.
//
// THE WAN ADDRESS IS NO LONGER STRIPPED. `geo.auto.ip` was removed for anyone
// without `system:settings` until 2026-10-06, when the operator decided that
// anyone who can see a device may see its addresses; the Devices modal shows
// its public IP outright.
func (s *Server) routerListForSocket(sess *Session) []map[string]any {
	return s.routerRecordsFor(sess)
}

// broadcastRouterList is `_broadcastRoutersList`: one payload PER SOCKET,
// because each is filtered for its own principal.
//
// NOT `BroadcastAll`. That sends one marshalled payload to everybody, which is
// exactly wrong here — a viewer restricted to two routers would receive the
// whole fleet's addresses because somebody else's edit triggered the send.
func (s *Server) broadcastRouterList() {
	for _, cn := range s.connections() {
		EvRoutersUpdate.Send(s.hub, cn.c, s.routerListForSocket(cn.scope().sess))
	}
}

// connections is a snapshot of the live sockets.
func (s *Server) connections() []*conn {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	out := make([]*conn, 0, len(s.conns))
	for _, cn := range s.conns {
		out = append(out, cn)
	}
	return out
}

// visibleRouters is the RBAC-readable set, or NIL for no restriction.
//
// NIL AND EMPTY ARE OPPOSITE ANSWERS. Nil means unrestricted; empty means this
// principal may read nothing. Returning the wrong one shows a locked-down user
// the whole fleet, or shows an unrestricted one none of it.
func (s *Server) visibleRouters(sess *Session) map[string]bool {
	if sess == nil {
		return map[string]bool{} // no session, no routers
	}
	if sess.AuthMode == "none" {
		return nil
	}
	if s.rbac == nil || !s.rbac.Available() {
		return nil // the documented install-wide gap, reported at startup
	}
	ids, err := s.rbac.EffectiveRouterIDs(s.userIDFor(sess.Username), "router:read")
	if err != nil {
		log.Printf("[devices] visible routers: %v", err)
		return map[string]bool{} // an error is not a permission
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// globalDefaultIf is the install-wide default interface, the low half of the
// precedence `routers.DefaultIfFor` resolves. Empty when unset or unreadable,
// which lets the fallback take over rather than making settings a hard
// dependency of recording.
func (s *Server) globalDefaultIf() string {
	if s.store == nil {
		return ""
	}
	cfg, err := s.store.Settings()
	if err != nil {
		return ""
	}
	merged, _ := store.Merge(cfg, os.LookupEnv, s.store)
	// `defaultIf`, the key the settings table actually declares. See the note in
	// `devicesSource`: `defaultInterface` is not in the defaults table, so Merge
	// dropped it and this returned "" for every install.
	v, _ := merged["defaultIf"].(string)
	return v
}

// declareRecordedInterfaces tells BOTH sides which interfaces this router's
// history covers: the recorder, which decides what to write, and the live
// session's traffic collector, which decides what the stream carries.
//
// ── THE TWO HALVES MUST AGREE OR HISTORY HAS HOLES ─────────────────────────
//
// The recorded set is INDEPENDENT of who is looking. Before it, the set was
// whatever happened to be in the traffic stream — the default plus every
// interface a browser was watching — so history appeared and disappeared with a
// tab (`historywire.Wire.SetRecordedInterfaces`). Declaring it to the recorder
// alone fixes what is WRITTEN and not what is MEASURED: an interface the stream
// does not carry produces no samples to write, so it would record nothing while
// the operator believes it is recording. Both, together, from one list.
//
// The list is `store.RecordedIfacesFor`, so the default interface is always in
// it and it is never empty. See that helper: an empty list means "record
// everything" to the recorder.
//
// The pool's sessions get the same list through `RouterConfig`, applied on
// every sync (`routers.Pool.applyReporting`).
func (s *Server) declareRecordedInterfaces(routerID string, recorded []string) {
	s.historyWire.SetRecordedInterfaces(routerID, recorded)
	if s.sessions != nil {
		s.sessions.ApplyRecordedIfaces(routerID, recorded)
	}
}

// declareReporting tells the two recorders what this router's reporting setting
// means for them.
//
// TWO CONSUMERS, ONE SETTING. The history wire stops writing traffic, ping and
// connectivity rows; the alert wire stops writing alert rows and keeps its
// de-duplication in memory instead, so alerts still notify. Declared together
// here so the pair cannot drift — a router recording no history but still
// filing alert rows would be a half-applied setting nobody asked for.
//
// Called from BOTH fleet syncs, because either pool may hold a given router.
func (s *Server) declareReporting(r store.Router) {
	on := store.ReportingOn(r)
	s.historyWire.SetReporting(r.ID, on)
	if s.alerts != nil {
		s.alerts.SetPersisting(r.ID, on)
	}
}

// declareConnThreshold tells the debounce how long this router must be
// unreachable before it counts as offline — the device dialog's "Offline
// threshold", `connDownThresholdSec`.
//
// ── DECLARED, NOT CAPTURED, AND THAT IS THE FIX ────────────────────────────
//
// The session used to resolve this when it was BUILT and hold the answer for
// the router's whole life. A router held for alerting or recording is never
// rebuilt, so an operator who changed the threshold saw the field save and the
// record update while the running debounce went on using the old value until
// the process restarted. Reported 2026-09-20 in the same breath as the alert
// switch, which had the identical shape.
//
// Declared beside `declareReporting` because they are the same kind of fact and
// the same sync already carries it: `syncFleetHolds` runs on every router save,
// so there is no second place to remember.
func (s *Server) declareConnThreshold(r store.Router) {
	s.connTrack.SetThreshold(r.ID, connstate.ThresholdMs(connDownSecOf(r)))
}

// connDownSecOf keeps "unset" distinct from a deliberate zero — `ThresholdMs`
// gives the live 30s default for the first and declares an outage at once for
// the second.
func connDownSecOf(r store.Router) (int, bool) {
	if r.ConnDownThresholdSec == nil {
		return 0, false
	}
	return *r.ConnDownThresholdSec, true
}

// ── THE OUTAGE DEBOUNCE IS NO LONGER CACHED HERE ──────────────────────────
//
// `noteConnThreshold` and `connThresholdMs` were a per-router cache of the
// debounce, populated by both fleet syncs. Their only reader was
// `alertPoolStatus`, the hook `internal/alertpool` called on a connect or a
// drop: that hook was handed a router id and a bool and nothing else, and
// reading the record there would have meant `store.Routers()` — which decrypts
// every router's password with scrypt — on every connect and drop.
//
// The pool is gone and every router nobody is watching is held as a SESSION, so
// the session writes `connectivity_events` and carries the threshold off its own
// record: `connThreshMs`, built once in `Acquire`. There is no hook left with
// nothing but an id, so there is nothing left to cache for. Removed rather than
// left populated and unread — see `internal/server/connthresh_test.go`, which
// is where the property this protected is asserted now.

// devicesFocus is what a browser opening the Devices page sets in motion.
//
// Joining the watcher set is what makes `syncFleetHolds` take the `devices`
// hold on every router, which runs the collectors the cards read. There is no
// pool to resume any more: every enabled router is already held WARM, so the
// connection the page reads is open before the page is.
func (cn *conn) devicesFocus() {
	cn.srv.devicesMu.Lock()
	cn.srv.devicesWatchers[cn.c] = true
	cn.srv.devicesMu.Unlock()

	// ── BEFORE THE FIRST PAYLOAD, AFTER THE SYNC ───────────────────────────
	//
	// A warm session runs no collectors, so before the `devices` hold's system
	// collector has ticked, a router can say it is UP and nothing more: the card
	// drew a green badge over blank gauges. `PrimeStats` reads the gauges once on
	// the socket that is already open, which is why this is here and not in the
	// two-second tick.
	//
	// AFTER THE SYNC, and the order is load-bearing: `syncFleetHolds` decides
	// the session set and can rebuild a session whose flags changed, and a
	// rebuilt session is a new socket with no reading on it. Priming first would
	// spend a command on a session that is then discarded.
	cn.srv.syncFleetHolds()
	if cn.srv.sessions != nil {
		cn.srv.sessions.PrimeStats()
	}
	cn.logEmptyFleet()
	cn.sendRoutersStats()
	cn.startDevicesTick()
}

// logEmptyFleet says WHY the Devices page is about to be blank.
//
// ── TWO CAUSES, ONE APPEARANCE ─────────────────────────────────────────────
//
// `BuildStats` drops every row when the RBAC set is empty-but-not-nil
// (internal/routers/assemble.go: a NIL `Visible` means unrestricted, an EMPTY
// one means this principal may read nothing). So a viewer holding a page grant
// and no router-scoped grant sees precisely what somebody with no routers
// configured sees — "No routers configured." — and nothing anywhere says which
// of the two it is.
//
// That is how issue #129 arrived: a blank Devices page, no logs, and no way to
// tell an authorization result from an empty fleet without reading the source.
// The fix for the bug is whatever the grants turn out to be; the fix for the
// DIAGNOSIS is this line.
//
// ── ONCE PER OPEN, NOT PER TICK ────────────────────────────────────────────
//
// `sendRoutersStats` runs every two seconds for as long as the page is open, so
// logging there would bury the log rather than serve it. This sits beside
// `PrimeStats`, for the reason that function's own comment gives: one line per
// cold open, which is when somebody is actually asking why the page is blank.
// scopeHidesWholeFleet reports whether the Devices page will be blank BECAUSE OF
// ACCESS SCOPE rather than because no routers are configured.
//
// NIL IS THE UNRESTRICTED ANSWER, and getting that backwards would log on every
// open of a healthy install — which is why this is a separate function with a
// table test rather than a condition inline in a log call.
func scopeHidesWholeFleet(fleet int, visible map[string]bool) bool {
	return fleet > 0 && visible != nil && len(visible) == 0
}

func (cn *conn) logEmptyFleet() {
	if cn.srv.store == nil || cn.sess == nil {
		return
	}
	all, _ := cn.srv.store.Routers()
	if len(all) == 0 {
		return // genuinely no routers; the page's own message is already true
	}
	if !scopeHidesWholeFleet(len(all), cn.srv.visibleRouters(cn.sess)) {
		return
	}
	log.Printf("[devices] %q may read none of the %d configured routers, so the "+
		"page will show nothing — this is an access-scope result, not an empty "+
		"fleet. Check the grants for this principal in Settings → Authentication",
		cn.sess.Username, len(all))
}

// devicesRefresh is the live `setInterval(_emitRouters, 2000)`.
const devicesRefresh = 2 * time.Second

// startDevicesTick keeps this viewer's rows moving while the page is open.
//
// ── THE PORT SENT THE PAYLOAD ONCE AND NEVER AGAIN ─────────────────────────
//
// `devicesFocus` called `sendRoutersStats` and stopped. Everything the page
// shows is live — CPU, memory, uptime, client counts, whether a router is up —
// so the table froze at whatever the fleet looked like in the instant the page
// opened. Worse, it froze at the WORST possible instant: the background pool has
// only just been told to sync, so every router nobody was watching still read
// OFFLINE, and it stayed that way until the page was reopened. That is exactly
// what the operator would have seen, and it looks like a broken pool rather than
// a missing timer.
//
// ── PER SOCKET, LIKE THE ORIGINAL ──────────────────────────────────────────
//
// `let _routersTimer = null` is declared INSIDE the live connection handler, so
// each viewer has their own and clearing one cannot silence another. It matters
// because the payload is built PER PRINCIPAL — `visible` is resolved for one
// viewer — so a shared timer would have to pick whose rows to
// send.
//
// A TICKER PLUS A STOP CHANNEL rather than time.AfterFunc: the goroutine has to
// be stoppable from `devicesBlur` AND from teardown, and a fired-and-rescheduled
// timer has a window where neither has a handle on it.
func (cn *conn) startDevicesTick() {
	cn.devicesMu.Lock()
	defer cn.devicesMu.Unlock()
	if cn.devicesTick != nil {
		// Already ticking. The live code calls `clearInterval` before setting a
		// new one, which for a page that is already focused is a no-op with
		// extra steps; a browser can send `page:focus` twice for the same page.
		return
	}
	t := time.NewTicker(devicesRefresh)
	stop := make(chan struct{})
	cn.devicesTick = t
	cn.devicesStop = stop
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				// RE-SYNCED every tick, not just on focus. A router added,
				// removed or re-enabled from another tab has to be held, and
				// the rows are read from the held sessions.
				cn.srv.syncFleetHolds()
				// ── AND ANY SESSION THAT WENT COLLECTOR-LESS SINCE ────
				//
				// `PrimeStats` on focus covers what existed then. An
				// interactive session idling out drops to its warm hold
				// WHILE the page is open, and a warm session runs no
				// collectors at all -- so its reading goes stale and the
				// card is the green badge over blank gauges all over
				// again, until the `devices` hold's collector ticks.
				//
				// UNREAD ONLY: this is a timer, and re-reading a session
				// that already answered is the poll the toggle exists to
				// avoid.
				if cn.srv.sessions != nil {
					cn.srv.sessions.PrimeUnread()
				}
				// On the connection's loop, which owns its session.
				cn.post(cn.sendRoutersStats)
			}
		}
	}()
}

// stopDevicesTick is the mirror. Idempotent, because it is called from BOTH
// `page:blur` and teardown and a browser that closes its tab never sends a blur.
func (cn *conn) stopDevicesTick() {
	cn.devicesMu.Lock()
	defer cn.devicesMu.Unlock()
	if cn.devicesTick == nil {
		return
	}
	cn.devicesTick.Stop()
	close(cn.devicesStop)
	cn.devicesTick = nil
	cn.devicesStop = nil
}

// devicesBlur is the mirror. Called from page:blur AND from teardown, because a
// browser that closes its tab never sends a blur.
// devicesWatched reports whether anybody has the Devices page open.
//
// The page reads a payload per router — see `session.devicesFeeds` — so while it
// is open every router has a reason to run those collectors, and while it is
// closed none of them do.
func (s *Server) devicesWatched() bool {
	s.devicesMu.Lock()
	defer s.devicesMu.Unlock()
	return len(s.devicesWatchers) > 0
}

func (cn *conn) devicesBlur() {
	cn.stopDevicesTick()
	cn.srv.devicesMu.Lock()
	_, had := cn.srv.devicesWatchers[cn.c]
	delete(cn.srv.devicesWatchers, cn.c)
	last := had && len(cn.srv.devicesWatchers) == 0
	cn.srv.devicesMu.Unlock()

	// THE HOLD GOES WITH THE LAST WATCHER. `holdOne` reads `devicesWatched`, so
	// re-running the sync is what drops it, and the session falls back to its
	// WARM hold: still connected, still observed, running no collectors. There
	// is no pool to suspend or release any more - the connection was never the
	// page's to give back.
	if last {
		cn.srv.syncFleetHolds()
	}
}

// sendRoutersStats builds and sends this viewer's rows.
//
// PER SOCKET, not broadcast: the payload carries `visible`,
// resolved for one principal. Broadcasting one viewer's rows would show
// another viewer routers they may not read.
func (cn *conn) sendRoutersStats() {
	EvRoutersStats.Send(cn.srv.hub, cn.c,
		routers.BuildStats(cn.srv.buildStatsSources(cn.sess, cn.routerID)))
}
