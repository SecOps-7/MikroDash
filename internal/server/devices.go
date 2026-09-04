package server

import (
	"encoding/json"
	"log"
	"os"
	"time"

	"mikrodash/internal/alertpool"
	"mikrodash/internal/collection"
	"mikrodash/internal/historywire"
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

// buildStatsSources gathers everything `routers.BuildStats` needs.
//
// Assembled HERE rather than in `internal/routers` so that package keeps no
// dependency on sessions, the store or the database: it is pure, and its tests
// run without any of them.
func (s *Server) buildStatsSources(sess *Session) routers.StatsSources {
	out := routers.StatsSources{
		Main:       map[string]routers.MainSession{},
		Background: map[string]routers.Summary{},
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
		out.Routers = append(out.Routers, routers.StatsRouter{
			ID: r.ID, Label: r.Label, Host: r.Host, Disabled: r.Disabled,
			SiteIDs: store.RouterSiteIDs(r),
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

	// The GLOBAL default interface, the low half of the precedence a row
	// resolves. `BuildStats` falls back again to "ether1" after it.
	//
	// ── THE KEY IS `defaultIf`, AND IT WAS `defaultInterface` HERE ─────────
	//
	// `Merge` DROPS a key that is not in the defaults table — deliberately, so a
	// retired setting left on disk cannot reappear — so this read returned
	// nothing on every install and the global setting was silently ignored. It
	// looked harmless because the table's default for `defaultIf` is "ether1"
	// and so is `DefaultIfFor`'s fallback, so the two agreed by accident
	// wherever nobody had changed the setting. An operator who DID change it was
	// overruled, everywhere, with no error. Found while wiring the same read
	// into the recorders (#126).
	out.DefaultIf = s.globalDefaultIf()

	// INTERACTIVE sessions. Presence decides which rows read a main payload;
	// `Connected()` decides what the row says, because a session exists before it
	// connects.
	if s.sessions != nil {
		for id, sn := range s.sessions.Live() {
			m := routers.MainSession{
				Connected: sn.Connected(), Known: sn.Observed(), LastError: sn.LastError(),
			}
			if c := sn.System(); c != nil {
				m.System = c.Last()
			}
			if c := sn.IfStatus(); c != nil {
				m.IfStatus = c.Last()
			}
			if c := sn.DHCPLeases(); c != nil {
				m.DHCPLeases = c.Last()
			}
			out.Main[id] = m
		}
	}

	for _, sum := range s.poolSummaries() {
		out.Background[sum.RouterID] = sum
	}

	// ── THEN THE ALERT POOL, FOR ROUTERS NEITHER OF THE ABOVE COVERS ────────
	//
	// FILL, NOT OVERWRITE. The overview pool's summary is the richer one — it
	// carries DHCP leases and this does not — so an id it already answered for
	// keeps its entry. A router with an interactive session ignores `Background`
	// entirely (`routers.BuildStats` picks one source per row, `Main` first), so
	// nothing here can mix two connections' readings into one card.
	//
	// This is what stops the page opening with a fleet of red "Offline" cards:
	// the alert pool is synced at startup and already holds a connection to
	// every enabled router, while the overview pool is synced from this page and
	// takes a few seconds to dial. See alertpool.Snapshot for the full argument
	// and for what a snapshot does NOT carry.
	if s.alertPool != nil {
		fillFromAlertPool(out.Background, s.alertPool.Snapshots())
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

	out.MaySeeWanIp = s.maySaveSettings(sess)
	out.Visible = s.visibleRouters(sess)
	return out
}

// fillFromAlertPool adds a summary for every snapshotted router the overview
// pool did not answer for, and leaves the ones it did alone.
//
// A free function over the map rather than a method, so the precedence can be
// asserted without a pool, a store or a socket — the same argument
// `internal/routers` makes for being pure. The precedence is the part worth
// testing: getting it backwards is not a crash, it is a card that quietly loses
// its Clients count.
func fillFromAlertPool(bg map[string]routers.Summary, snaps []alertpool.Snapshot) {
	for _, snap := range snaps {
		// PRESENT IS NOT THE SAME AS ANSWERED, and getting that wrong is what
		// made the first version of this fix do nothing at all. `Summaries`
		// returns an entry for every session the overview pool HOLDS, including
		// one built moments ago whose dial has not returned. So on first open of
		// the Devices page the key was ALWAYS already here, this loop always
		// skipped, and the alert pool's real answer was discarded in favour of a
		// zero value that rendered as a red "Offline" — the exact symptom the
		// merge was added to remove.
		if cur, have := bg[snap.RouterID]; have && cur.Known {
			continue
		}
		bg[snap.RouterID] = routers.Summary{
			RouterID:  snap.RouterID,
			Connected: snap.Connected,
			// A snapshot is only ever built from an observation — see
			// alertpool.Pool.Snapshots, which omits a session that has not
			// answered rather than reporting it as down.
			Known:    true,
			System:   snap.System,
			IfStatus: snap.IfStatus,
		}
	}
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

// routerListForSocket is `_routersForSocket`: filtered, and the WAN address
// removed from anyone without `system:settings`.
func (s *Server) routerListForSocket(sess *Session) []map[string]any {
	recs := s.routerRecordsFor(sess)
	if s.maySaveSettings(sess) {
		return recs
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, stripWanIP(r))
	}
	return out
}

// stripWanIP removes `geo.auto.ip` and nothing else.
//
// The live `strip`:
//
//	if (!r.geo || !r.geo.auto || r.geo.auto.ip === undefined) return r;
//	const { ip, ...auto } = r.geo.auto;
//	return { ...r, geo: { ...r.geo, auto } };
//
// A COPY at every level it rewrites, because the record came from a shared read
// and deleting in place would strip it for the next principal too — including
// one entitled to see it.
func stripWanIP(r map[string]any) map[string]any {
	geo, ok := r["geo"].(map[string]any)
	if !ok {
		return r
	}
	auto, ok := geo["auto"].(map[string]any)
	if !ok {
		return r
	}
	if _, has := auto["ip"]; !has {
		return r
	}
	newAuto := make(map[string]any, len(auto))
	for k, v := range auto {
		if k == "ip" {
			continue
		}
		newAuto[k] = v
	}
	newGeo := make(map[string]any, len(geo))
	for k, v := range geo {
		newGeo[k] = v
	}
	newGeo["auto"] = newAuto

	out := make(map[string]any, len(r))
	for k, v := range r {
		out[k] = v
	}
	out["geo"] = newGeo
	return out
}

// broadcastRouterList is `_broadcastRoutersList`: one payload PER SOCKET,
// because each is filtered for its own principal.
//
// NOT `BroadcastAll`. That sends one marshalled payload to everybody, which is
// exactly wrong here — a viewer restricted to two routers would receive the
// whole fleet's addresses because somebody else's edit triggered the send.
func (s *Server) broadcastRouterList() {
	for _, cn := range s.connections() {
		s.hub.Send(cn.c, "routers:update", s.routerListForSocket(cn.sess))
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

// poolSummaries is the background pool's cache, or nothing when no pool runs.
func (s *Server) poolSummaries() []routers.Summary {
	if s.pool == nil {
		return nil
	}
	return s.pool.Summaries()
}

// syncPool brings the background pool in line with the fleet.
//
// `excluded` is every router with an interactive session — derived here on every
// call rather than tracked, because a second record of who is watching what
// drifts from the first.
func (s *Server) syncPool() {
	if s.pool == nil || s.store == nil {
		return
	}
	all, _ := s.store.Routers()
	global := s.globalDefaultIf()
	cfgs := make([]routers.RouterConfig, 0, len(all))
	for _, r := range all {
		if r.Disabled {
			continue // a disabled router is not connected to at all
		}
		s.declareRecordedInterfaces(r.ID, routers.DefaultIfFor(r.DefaultIf, global))
		s.noteConnThreshold(r.ID, r.ConnDownThresholdSec)
		s.declareReporting(r)
		cfgs = append(cfgs, routers.RouterConfig{
			ID: r.ID, Label: r.Label, Host: r.Host, Port: r.Port,
			TLS: r.TLS, InsecureTLS: r.TLSInsecure,
			User: r.Username, Password: r.Password,
			// The record's own collection block (#105). A nil one resolves to the
			// fleet defaults, so a router that has never been configured is not a
			// special case here.
			Collection: collection.ParseRouter(r.Collection),
			// For the history pair only — the same two values Session passes to
			// NewTraffic and NewPing, so a pooled recording and a page-driven one
			// measure the same interface and target.
			// RESOLVED, not raw. A router with no default interface produced an
			// EMPTY stream here — `syncStream` opens nothing for an empty
			// interface list — so the background recorder wrote no traffic at
			// all until a browser attached and added one. That is the reported
			// "no data unless I have the Dashboard open".
			DefaultIf:  routers.DefaultIfFor(r.DefaultIf, global),
			PingTarget: r.PingTarget,
			// See the note in `syncAlertPool`: a hand-written field list, so a
			// flag left out here is invisible to the pool.
			ReportingEnabled: store.ReportingOn(r),
		})
	}

	excluded := map[string]bool{}
	if s.sessions != nil {
		for id := range s.sessions.Live() {
			excluded[id] = true
		}
	}
	s.pool.Sync(cfgs, excluded)
	// ── AND HAND IT BACK IF NOBODY IS WATCHING ────────────────────────────
	//
	// `Sync` DIALS. Most callers here are not the Devices page — a router edit,
	// a create, a delete, a site change — and each one woke the overview pool
	// against the whole fleet and left it there for the life of the process,
	// because a release is only ever scheduled when somebody stops watching a
	// page they never started watching.
	//
	// Two costs, and the second is the one that was reported: a connection to
	// every router nobody asked for, and `/healthz` reporting the active router
	// disconnected — `alertPoolExclusions` hands those routers to the overview
	// pool and the alert pool forgets their status. Measured against 0.8.18: one
	// router edit, then `ok:false` for as long as the process ran.
	//
	// `scheduleDevicesRelease` re-reads the watcher set when it fires, so this
	// is a no-op while the Devices page IS open — which is why it can live here,
	// once, rather than at each of the five callers.
	s.scheduleDevicesRelease()
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

// declareRecordedInterfaces tells the recorder which interfaces this router's
// history covers.
//
// ONE INTERFACE TODAY — the resolved default. The point of declaring it is not
// the number but the INDEPENDENCE: before this, the recorded set was whatever
// happened to be in the traffic stream, which is the default plus every
// interface a browser was watching. History therefore appeared and disappeared
// with a browser tab. See `historywire.Wire.SetRecordedInterfaces`.
//
// Widening this to several interfaces — which a multi-WAN router needs, and
// which costs no extra router channel because `/interface/monitor-traffic`
// takes a comma list — is a separate change needing somewhere for the operator
// to say which ones.
func (s *Server) declareRecordedInterfaces(routerID, defaultIf string) {
	s.historyWire.SetRecordedInterfaces(routerID, []string{defaultIf})
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

// noteConnThreshold remembers this router's outage debounce, in milliseconds.
//
// ── CACHED, BECAUSE THE READER IS A STATUS HOOK ───────────────────────────
//
// `alertPoolStatus` is handed a router id and a bool and nothing else, and
// reading the record there would mean `store.Routers()` — which decrypts every
// router's password with scrypt — on every connect and drop. The fleet syncs
// already walk every record, so the value is picked up where it is free.
func (s *Server) noteConnThreshold(routerID string, sec *int) {
	ms := historywire.ThresholdMs(0, false) // the live default when unset
	if sec != nil {
		ms = historywire.ThresholdMs(*sec, true)
	}
	s.connThreshMu.Lock()
	if s.connThresh == nil {
		s.connThresh = map[string]int64{}
	}
	s.connThresh[routerID] = ms
	s.connThreshMu.Unlock()
}

// connThresholdMs is this router's debounce, or the live default for a router
// no sync has seen yet.
func (s *Server) connThresholdMs(routerID string) int64 {
	s.connThreshMu.Lock()
	defer s.connThreshMu.Unlock()
	if ms, ok := s.connThresh[routerID]; ok {
		return ms
	}
	return historywire.ThresholdMs(0, false)
}

// devicesFocus is what a browser opening the Devices page sets in motion.
//
// The pool RESUMES on the first watcher and suspends on the last, matching
// `_routersPageSockets`: nobody looking at the page means nothing needs the
// background rows, and holding a connection to every router for a page no one
// has open is the cost this design exists to avoid.
func (cn *conn) devicesFocus() {
	cn.srv.devicesMu.Lock()
	first := len(cn.srv.devicesWatchers) == 0
	cn.srv.devicesWatchers[cn.c] = true
	cn.srv.devicesMu.Unlock()

	if first && cn.srv.pool != nil {
		cn.srv.pool.Resume()
	}
	cn.srv.syncPool()
	cn.srv.syncAlertPool()
	cn.sendRoutersStats()
	cn.startDevicesTick()
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
// because the payload is built PER PRINCIPAL — `visible` and `maySeeWanIp` are
// resolved for one viewer — so a shared timer would have to pick whose rows to
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
				// removed or re-enabled from another tab has to reach the pool,
				// and the live app rebuilds its summaries from a pool that its
				// own `syncSessions` keeps current on every routers.json write.
				cn.srv.syncPool()
				cn.srv.syncAlertPool()
				cn.sendRoutersStats()
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
func (cn *conn) devicesBlur() {
	cn.stopDevicesTick()
	cn.srv.devicesMu.Lock()
	_, had := cn.srv.devicesWatchers[cn.c]
	delete(cn.srv.devicesWatchers, cn.c)
	// `had &&` IS UNKILLABLE, and recorded rather than counted. Dropping it
	// survives the suite, and the reason is a property of the caller rather than
	// a gap in the tests: the ONLY thing that resumes the pool is `devicesFocus`,
	// and that always adds to this set. So an empty set implies the pool is
	// already suspended, and suspending it again changes nothing observable.
	//
	// It stays because that invariant belongs to a different function. If
	// anything else ever resumes the pool — a warm start, an admin action — an
	// unrelated connection's teardown would suspend it out from under a watcher,
	// and this line is what makes that impossible rather than merely unlikely.
	last := had && len(cn.srv.devicesWatchers) == 0
	cn.srv.devicesMu.Unlock()

	if last && cn.srv.pool != nil {
		// ── STOP COLLECTING NOW, LET THE SOCKETS GO AFTER A GRACE ─────────
		//
		// Suspending is the part that must be immediate: it is what stops
		// costing the routers anything the moment nobody is looking.
		//
		// RELEASING is what hands the fleet back to the alert pool, and it was
		// immediate too until this grace. That was correct and too eager. The
		// overview pool's whole reason for keeping its sockets is that returning
		// to the page should be instant, and dropping them on every blur made
		// each visit re-dial the fleet — which is exactly the several-second
		// wait where the page has no data and reports every device offline.
		//
		// So the same shape as the session's idle grace: leave and come back
		// inside the window and the sockets are still there; stay away and the
		// alert pool takes the fleet, which is the coverage half that mattered.
		// Re-checked when the timer fires, so a viewer who returned keeps them.
		cn.srv.pool.Suspend()
		cn.srv.scheduleDevicesRelease()
	}
}

// scheduleDevicesRelease hands the fleet to the alert pool once the Devices page
// has been unwatched for a whole grace period.
//
// Several timers can be in flight after repeated visits; each re-reads the
// watcher set, so all but the last find somebody watching and do nothing. That
// is the same reasoning `suspendIfNoRoomOccupied` uses, and it is why this needs
// no timer bookkeeping of its own.
func (s *Server) scheduleDevicesRelease() {
	time.AfterFunc(s.graceFor(), func() {
		s.devicesMu.Lock()
		gone := len(s.devicesWatchers) == 0
		s.devicesMu.Unlock()
		if !gone || s.pool == nil {
			return
		}
		// BOTH HALVES, still: releasing without the re-sync would leave these
		// routers covered by nothing at all, which is worse than the bug this
		// release exists to fix.
		s.pool.ReleaseAll()
		s.syncAlertPool()
	})
}

// sendRoutersStats builds and sends this viewer's rows.
//
// PER SOCKET, not broadcast: the payload carries `visible` and `maySeeWanIp`,
// both resolved for one principal. Broadcasting one viewer's rows would show
// another viewer routers they may not read.
func (cn *conn) sendRoutersStats() {
	cn.srv.hub.Send(cn.c, "routers:stats",
		routers.BuildStats(cn.srv.buildStatsSources(cn.sess)))
}
