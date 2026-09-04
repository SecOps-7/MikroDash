package server

// The WebSocket endpoint: what the browser says, and what it is allowed to hear.
//
// The event names and payloads are the Node ones verbatim — `page:focus`,
// `page:blur`, `dns:update`, `router:status` — because the wire contract is the
// part of a port that must not drift. What changed is underneath: plain
// WebSocket instead of Socket.IO, since the app used named fire-and-forget
// events, server-side rooms and reconnection, and nothing else. No
// acknowledgement callbacks, no namespaces, no binary frames.

import (
	"context"
	"encoding/json"
	"log"
	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"mikrodash/internal/alert"
	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/session"
	"mikrodash/internal/store"
)

// A frame the browser sends. `data` is decoded per event, because page:focus
// carries a bare string while res:save carries an object.
type inbound struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

const (
	// sendQueue is how many frames may be outstanding to one browser before it
	// starts losing them. Deep enough to absorb a burst of collectors emitting
	// together, shallow enough that a stalled tab cannot hold megabytes.
	sendQueue = 64
	// writeWait bounds one frame. A browser that has stopped reading must not
	// pin the writer goroutine, and through it the connection, indefinitely.
	writeWait = 10 * time.Second
	// revalidate matches the 60s session sweep in src/index.js, so a revoked
	// session dies on the Go side no later than it would on the Node side.
	revalidate = 60 * time.Second
)

var connSeq atomic.Uint64

// conn is one browser, with the state a socket carries in Node: which router it
// watches, and who is holding it.
type conn struct {
	c    *hub.Client
	ws   *websocket.Conn
	srv  *Server
	sess *Session

	routerID string
	rsession *session.Session
	// trafficIf is the interface this viewer's chart is watching, if any. Held
	// here rather than in the collector because it is a property of the VIEWER;
	// the collector keeps only the refcount per interface.
	trafficIf string
	cookie    string
	// clientIP is resolved once at the upgrade: the audit trail records who did
	// a thing and from where, and the request is the only place that is known.
	clientIP string
	// userID is the grant graph's key for this session's user, resolved once at
	// the upgrade. Empty means "not found", and every authorization question
	// then fails closed.
	userID string
	// resHist is undo/redo, per resource, living and dying with this socket.
	// See history.go for why it is neither shared nor persisted.
	resHist map[string]*histStack

	// devicesTick is this viewer's Devices-page refresh, and it is PER SOCKET
	// exactly as the live `_routersTimer` is — declared inside the connection
	// handler, cleared on blur and on disconnect. See devicesFocus.
	devicesMu   sync.Mutex
	devicesTick *time.Ticker
	devicesStop chan struct{}
	// mu guards `cards`. The grid can send dashcard:focus while another
	// goroutine is selecting a router, and the map is written by both.
	mu sync.Mutex
	// cards is the card keys this browser has subscribed to, kept independently
	// of any router: the grid subscribes before `router:select` arrives, and the
	// rooms are per router so a switch must rejoin them. See dashcard.go.
	cards map[string]bool
	// page is the page this browser has focused, kept for the SAME reason and
	// guarded by the same mutex.
	//
	// ── THE CARD PATH LEARNED THIS AND THE PAGE PATH DID NOT ────────────────
	//
	// `dashCardFocus` records its key BEFORE testing `routerID` and
	// `rejoinCards` replays it from `selectRouter`; that was added on
	// 2026-08-29 for "cards with no data". `pageFocus` kept returning silently
	// when the frame arrived first, and nothing remembered it — so the page room
	// was never joined and the page's collectors were never woken.
	//
	// The client cannot recover either: its `router:active` handler skips the
	// FIRST event on the stated grounds that "the room has already been joined
	// by the code that opened the page", which is exactly what did not happen.
	//
	// Reported twice — "sometimes when I sign in, some of the cards on the
	// dashboard dont have any data", and again on 2026-09-04 with the router
	// present, the dot green, and every card stale for two hours. The server
	// showed a healthy select, a connected session and collectors emitting; the
	// frames were going to a room this socket had never joined.
	page string
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	cookie := r.Header.Get("Cookie")
	user, err := s.auth.Validate(cookie)
	if err != nil {
		// 401 rather than an accepted socket that immediately closes: the
		// client can then send the browser to the login page without having to
		// interpret a close code.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Matching the Node server's perMessageDeflate. Without it the port
		// silently regresses bandwidth on exactly the payloads that are large.
		CompressionMode: websocket.CompressionContextTakeover,
		OriginPatterns:  s.originPatterns,
	})
	if err != nil {
		log.Printf("[ws] accept: %v", err)
		return
	}
	// 1 MB, matching maxHttpBufferSize on the Node server.
	ws.SetReadLimit(1 << 20)

	id := "ws-" + itoa(connSeq.Add(1))
	cn := &conn{
		c:        hub.NewClient(id, sendQueue),
		ws:       ws,
		srv:      s,
		sess:     user,
		cookie:   cookie,
		clientIP: clientIPOf(r),
		userID:   s.userIDFor(user.Username),
	}
	s.hub.Add(cn.c)
	log.Printf("[ws] %s connected as %s", id, user.Username)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go cn.writer(ctx)
	s.connsMu.Lock()
	s.conns[cn.c] = cn
	s.connsMu.Unlock()

	go cn.revalidator(ctx)

	cn.reader(ctx)

	s.connsMu.Lock()
	delete(s.conns, cn.c)
	s.connsMu.Unlock()

	// A CLOSED TAB SENDS NO BLUR. Without this the pool keeps a connection to
	// every router for a page nobody has open — the exact cost `devicesBlur`
	// exists to avoid, reached by the commonest way a viewer leaves.
	cn.devicesBlur()
	cn.releaseRouter()
	s.hub.Remove(cn.c)
	_ = ws.Close(websocket.StatusNormalClosure, "")
	log.Printf("[ws] %s gone (%d frames dropped)", id, cn.c.Dropped())
}

// writer is the only goroutine that touches the socket for writing, which is
// what makes the hub's fan-out safe from any number of collector goroutines.
func (cn *conn) writer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-cn.c.Send:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, writeWait)
			err := cn.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				_ = cn.ws.Close(websocket.StatusInternalError, "write failed")
				return
			}
		}
	}
}

// revalidator re-asks Node whether this session is still good, and whether the
// principal may still read the router it is watching.
//
// Node does the same thing on a 60s timer and for the same reason: a revocation
// used to take effect only on the next page load, so a socket kept streaming a
// router its owner had just lost. Leaving the rooms is what actually stops the
// data; the notice is only so the page can explain itself.
func (cn *conn) revalidator(ctx context.Context) {
	t := time.NewTicker(revalidate)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cn.srv.auth.Forget(cn.cookie)
			live, err := cn.srv.auth.Validate(cn.cookie)
			if err != nil {
				cn.srv.hub.Send(cn.c, "session:expired", map[string]any{})
				// Give the frame a moment to leave before the socket goes.
				time.Sleep(200 * time.Millisecond)
				_ = cn.ws.Close(websocket.StatusPolicyViolation, "session expired")
				return
			}
			cn.sess = live
			if cn.routerID != "" && !live.CanReadRouter(cn.routerID) {
				cn.releaseRouter()
				for _, room := range cn.c.Rooms() {
					cn.srv.hub.Leave(cn.c, room)
				}
				cn.srv.hub.Send(cn.c, "access:revoked", map[string]any{})
			}
		}
	}
}

func (cn *conn) reader(ctx context.Context) {
	for {
		typ, b, err := cn.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var in inbound
		if err := json.Unmarshal(b, &in); err != nil {
			continue // a frame we cannot parse is not a reason to hang up
		}
		cn.dispatch(in)
	}
}

func (cn *conn) dispatch(in inbound) {
	switch in.Event {
	case "router:select":
		var id string
		if json.Unmarshal(in.Data, &id) != nil {
			return
		}
		cn.selectRouter(id)
	case "page:focus":
		var page string
		if json.Unmarshal(in.Data, &page) != nil {
			return
		}
		cn.pageFocus(page)
	case "page:blur":
		var page string
		if json.Unmarshal(in.Data, &page) != nil {
			return
		}
		cn.pageBlur(page)
	// A Dashboard card's room. Relayed by the browser from the grid's own
	// `dashcard:room:focus`/`blur` events — see web/src/pages/dashboard.ts.
	case "dashcard:focus":
		var key string
		if json.Unmarshal(in.Data, &key) != nil {
			return
		}
		cn.dashCardFocus(key)
	case "dashcard:blur":
		var key string
		if json.Unmarshal(in.Data, &key) != nil {
			return
		}
		cn.dashCardBlur(key)
	case "res:save":
		cn.resSave(in.Data)
	case "res:remove":
		cn.resRemove(in.Data)
	// The chart's interface picker. The name reaches a router command, so it is
	// validated against the interfaces that exist rather than merely escaped —
	// see collect.Traffic.NormalizeIfName.
	case "traffic:select":
		var sel struct {
			IfName string `json:"ifName"`
		}
		if json.Unmarshal(in.Data, &sel) != nil {
			return
		}
		cn.trafficSelect(sel.IfName)
	// The Frequency Analyser. `interfaces` is a read — a viewer may see which
	// radios exist — while `start` and `stop` need the scan capability. The
	// handlers gate themselves; the dispatch does not, so the gate has exactly
	// one place to be wrong.
	case "wifiscan:interfaces":
		cn.wifiscanInterfaces()
	case "wifiscan:start":
		cn.wifiscanStart(in.Data)
	case "wifiscan:stop":
		cn.wifiscanStop(in.Data)

	case "backups:list":
		cn.backupsList()
	case "backups:diff":
		cn.backupsDiff(in.Data)
	case "backups:settings":
		cn.backupsSettings(in.Data)
	case "backups:delete":
		cn.backupsDelete(in.Data)
	case "backups:run":
		cn.backupsRun()
	case "backups:restore":
		cn.backupsRestore(in.Data)
	case "packages:caps":
		cn.packagesCaps()
	case "packages:schedule":
		cn.packagesSchedule(in.Data)
	case "packages:check":
		cn.packagesCheck()
	case "packages:upgrade":
		cn.packagesUpgrade(in.Data)
	case "packages:apply":
		cn.packagesApply(in.Data)
	case "packages:notes":
		cn.packagesNotes(in.Data)
	case "res:row":
		cn.resRow(in.Data)
	case "res:preview":
		cn.resPreview(in.Data)
	case "res:new":
		cn.resNew(in.Data)
	case "res:schema":
		cn.resSchema(in.Data)
	case "res:undo":
		cn.resUndo(in.Data)
	case "res:redo":
		cn.resRedo(in.Data)
	case "res:action":
		cn.resAction(in.Data)
	case "res:move":
		cn.resMove(in.Data)
	// Router Users is six handlers of its own rather than registry resources:
	// see internal/server/rosusers.go for why.
	case "rosuser:save":
		cn.ruUserSave(in.Data)
	case "rosuser:remove":
		cn.ruUserRemove(in.Data)
	case "rosgroup:save":
		cn.ruGroupSave(in.Data)
	case "rosgroup:remove":
		cn.ruGroupRemove(in.Data)
	case "rossession:remove":
		cn.ruSessionRemove(in.Data)
	// Queues is five handlers of its own, for the same reason Router Users is:
	// see internal/server/queues.go.
	case "queues:caps":
		cn.qCaps()
	case "queue:save":
		cn.qSave(in.Data)
	case "queue:remove":
		cn.qRemove(in.Data)
	case "queue:toggle":
		cn.qToggle(in.Data)
	case "queue:resetCounters":
		cn.qResetCounters(in.Data)
	case "queue:move":
		cn.qMove(in.Data)
	// WAN is two verbs over one body — see internal/server/wan.go. Registered
	// separately rather than as a loop for the same reason the original gives:
	// the next person looking for where this is handled will grep for the
	// literal event name.
	case "wan:caps":
		cn.wanCaps()
	case "wan:renew":
		cn.wanLeaseAction("renew", in.Data)
	case "wan:release":
		cn.wanLeaseAction("release", in.Data)
	case "firewall:tab":
		cn.fwTab(in.Data)
	}
}

// fwTab switches which table's counters are refreshed.
//
// THE ACTIVE TABLE IS SHARED SESSION STATE, streamed to every viewer of this
// router, so changing it is a WRITE-gated action even though it writes nothing
// to the router. Room membership says who is watching; it never said who may
// change what everyone else sees.
func (cn *conn) fwTab(raw json.RawMessage) {
	var table string
	if json.Unmarshal(raw, &table) != nil {
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		return
	}
	if !cn.canPage("firewall", "write") {
		return
	}
	cn.rsession.Firewall().SetActiveTable(table)
}

func (cn *conn) selectRouter(id string) {
	// ── EVERY REFUSAL SAYS SO, 2026-08-30 ─────────────────────────────────
	//
	// All three early returns below were SILENT server-side. That is how a
	// router selection could fail with the browser showing the router's name
	// anyway — the label is set client-side by `select()` whether or not the
	// server agrees — and leave the operator on a dashboard of stale cards with
	// nothing in the log to explain it. Reported as "sometimes when I sign in,
	// some of the cards on the dashboard dont have any data".
	//
	// A refusal that writes nothing down is indistinguishable from a request
	// that never arrived, and those two need completely different fixes. Logged
	// at the seam where the decision is made, so the next occurrence names
	// itself instead of costing another reproduction.
	if id == cn.routerID {
		return
	}
	if !cn.sess.CanReadRouter(id) {
		log.Printf("[ws] %s: router:select %s REFUSED — no router:read grant", cn.c.ID, id)
		cn.srv.hub.Send(cn.c, "access:none", map[string]any{})
		return
	}
	log.Printf("[ws] %s: router:select %s", cn.c.ID, id)
	cn.releaseRouter()
	for _, room := range cn.c.Rooms() {
		cn.srv.hub.Leave(cn.c, room)
	}

	rs, err := cn.srv.sessions.Acquire(id)
	if err != nil {
		log.Printf("[ws] %s: router:select %s FAILED to acquire: %v", cn.c.ID, id, err)
		cn.srv.hub.Send(cn.c, "router:status", map[string]any{
			"routerId": id, "connected": false, "reason": err.Error()})
		return
	}
	// ── THE ALERT POOL MUST LET GO OF A ROUTER A SESSION HAS TAKEN ────────
	//
	// `syncAlertPool` excludes every router with a live `Session`, and until
	// 2026-08-30 nothing re-ran it at the moment that set CHANGED. It was called
	// from the Devices page, the routers API and startup — never from here — so
	// selecting a router left the pool holding it as well.
	//
	// TWO system collectors then fed ONE evaluator, and they disagree about
	// `updateAvailable`: the rule fires on available-with-a-version and resolves
	// on not-available, so the two sources alternated. MEASURED: 50
	// `routeros_update` rows in 24 hours on the active router, against ZERO in
	// the live app's database over the same period, most of them already
	// resolved — a fire/resolve pair every time the two polls disagreed.
	//
	// It also means two connections and two sets of collectors on the one router
	// anybody is actually looking at.
	// ── AND THE OVERVIEW POOL MUST LET GO TOO ─────────────────────────────
	//
	// Everything below applies word for word to `internal/routers`, and it was
	// simply never added. It is worse there, because `Pool.Suspend` keeps its
	// sockets deliberately -- "Suspension is 'stop collecting', not 'drop the
	// sockets'". So opening the Devices page once and navigating away leaves a
	// connection to EVERY router, and selecting one then adds a second to it
	// that never goes away: a permanent extra `/user/active` entry on the one
	// router the operator is actually looking at.
	//
	// `Drop`, NOT `syncPool`. This was written as `syncPool()` first and that is
	// a fleet-wide dial: `SyncPool` starts every router that is neither excluded
	// nor already tracked (routers/overview.go:69-74), and on a process where
	// nobody has opened Devices `tracked` is empty -- so a socket handler would
	// have connected to every router in the fleet to fix one duplicate.
	//
	// BEFORE syncAlertPool, so the alert pool computes its exclusion set from
	// `Summaries()` after this router has left the overview pool.
	if cn.srv.pool != nil {
		cn.srv.pool.Drop(id)
	}
	cn.srv.syncAlertPool()

	// The stacks describe rows on the router being LEFT, and a `.id` from one
	// router addresses something entirely different on another.
	cn.histDropAll()
	cn.routerID = id
	cn.rsession = rs
	cn.srv.hub.Join(cn.c, "router-"+id)
	cn.srv.hub.Send(cn.c, "router:active", map[string]any{"activeId": id})
	cn.srv.hub.Send(cn.c, "router:status", map[string]any{
		"routerId": id, "connected": rs.Connected(), "reason": rs.LastError()})
	cn.sendPooledStatus()
	// The card subscriptions the grid sent before a router existed — see
	// dashcard.go:rejoinCards.
	cn.rejoinCards()
	// AND THE PAGE, for the same reason and from the same cause. Without this
	// the page room is joined only if `page:focus` happened to arrive after this
	// handler ran, which is a race the client cannot see and does not retry.
	cn.rejoinPage()
	cn.sendOpenAlerts(id)
	cn.sendPageSettings()
	// ── THE PER-ROUTER COLLECTION CONFIG ────────────────────────────────────
	//
	// `index.js:4208` sends this on the same handshake. The port resolved the
	// config from the day #105 landed and never told the browser, so a collector
	// the operator turned OFF on this router showed a stale dashboard card
	// rather than `is-collector-off` — broken rather than off. Its consumer,
	// `applyCollectionConfig` in web/src/stale.ts, was written and gated and
	// called by nothing. Found 2026-08-28 by tools/live-socket-diff.js.
	cn.srv.hub.Send(cn.c, "collection:config", collection.Payload(id, rs.Collection()))
	// AND THE DORMANT SET, which the live app sends on the same handshake
	// (`index.js:4209`, the line after its `collection:config`) and for the
	// reason its comment gives: "a card for a disabled collector must be marked
	// as such before it would otherwise start its stale countdown."
	//
	// UNCONDITIONAL, even when nothing is asleep. The port emitted only on a
	// CHANGE, so a viewer attaching after a collector went dormant never learned
	// it and that card was never dimmed. Found 2026-08-28 by
	// The live-socket-diff tool, which showed the live app sending this event
	// and the port not — on a router where nothing was dormant, so the emit was
	// the whole difference.
	cn.srv.hub.Send(cn.c, "collection:status", map[string]any{
		"routerId": id, "dormant": rs.DormantCollectors()})

	// ── THE ROUTER-WIDE CHROME, REPLAYED ────────────────────────────────────
	//
	// These four are emitted to the empty room — chrome visible on every page:
	// the top-bar gauges, the interface picker every page's traffic control
	// reads, the WAN chip and the LAN summary. Their collectors run from connect
	// rather than on page focus, so nothing here ever replayed them: a viewer
	// attaching to a session that was ALREADY UP waited for the next tick, which
	// for netwatch and talkers is up to a minute of empty chrome.
	//
	// The live app sends all four in `sendInitialState`. Found 2026-08-28 by
	// The initial-state audit, written after `collection:status` turned
	// out to have exactly this shape — correct on every change, absent on the
	// one path that matters most.
	//
	// A collector that has produced nothing yet sends nothing: `nil` here would
	// be a payload claiming the router has no interfaces.
	if last := rs.IfStatus().Last(); last != nil {
		cn.srv.hub.Send(cn.c, "ifstatus:names", collect.NamesOf(last))
	}
	if last := rs.System().Last(); last != nil {
		cn.srv.hub.Send(cn.c, "system:update", last)
	}
	if last := rs.Traffic().LastWan(); last != nil {
		cn.srv.hub.Send(cn.c, "wan:status", last)
	}
	if last := rs.DHCPNetworks().Last(); last != nil {
		cn.srv.hub.Send(cn.c, "lan:wan", map[string]any{"ts": last.TS, "wanIp": last.WanIP})
	}
	// ── SUBSCRIBE TO THE DEFAULT INTERFACE, BEFORE ANY PICKER TOUCHES IT ──
	//
	// `traffic.js:bindSocket` does this on connect: `subscriptions.set(socket.id,
	// { ifName: this.defaultIf, socket })`, with the comment "defaultIf is
	// always in the stream, so this is a no-op on first connect". Nothing here
	// did, and the consequence was invisible to every gate:
	//
	// `traffic:update` is emitted into a PER-INTERFACE room, so a viewer who has
	// joined none receives none. The live app's picker only emits
	// `traffic:select` when the chosen interface goes AWAY — on an ordinary load
	// it just sets the dropdown — so on this side nothing ever joined a room and
	// no sample ever arrived. **Measured against the real AX3 on 2026-08-27**:
	// 20 seconds on the Bandwidth page delivered wan:status x19,
	// ifstatus:names x15, system:update x9, bandwidth:update x6 — and
	// traffic:update x0, with the WAN figures showing "—" beside a live app
	// showing 185 Kbps.
	//
	// No differential gate could see it. They all supply a payload and compare
	// what is rendered; this is a payload that never arrives, which is a
	// question about SUBSCRIPTION rather than about rendering.
	cn.trafficSelectDefault(defaultIfFor(rs))
	// LAST, and named for the router we have ARRIVED at. The browser drops every
	// cached schema on this and re-asks, because `permitted` is per-router;
	// announcing it before the switch completed would answer for the router we
	// are leaving.
	cn.srv.hub.Send(cn.c, "router:switched", map[string]any{"activeId": id})
}

// sendPageSettings tells this browser which pages it may draw and which
// notification toggles are on.
//
// ── ONE CLIENT, THOUGH THE LIVE APP BROADCASTS ──────────────────────────────
//
// `src/index.js` has THREE emit sites: `io.emit` after a save and after a reset,
// and `socket.emit` on connect. This is the connect one, so it is a Send. The
// two broadcast sites belong to `POST /api/settings` and are BOTH implemented —
// `settings_write_api.go` broadcasts `settings:pages` after a save and after a
// reset, which is why `emit-audit` records the whole feature as ported.
//
// This said "which is not ported yet — recorded in `emit-audit`, not left
// silent", and both halves had expired: the route is served and the audit does
// not carry it. Corrected 2026-08-27 by re-measuring rather than reading.
//
// A failure logs and returns, like the alert feed beside it: the settings file
// being unreadable costs this browser its page visibility, and taking the router
// switch down with it would turn a degraded page into no app at all.
func (cn *conn) sendPageSettings() {
	if cn.srv.store == nil {
		return
	}
	// MERGED, not the raw file.
	//
	// `store.Settings()` returns settings.json as it is on disk, and
	// `PageSettings` copies only the keys it finds — so every key the operator
	// has never changed is simply ABSENT from the payload. The live
	// `Settings.load()` merges DEFAULTS first, so those keys are always present.
	//
	// Found by the live-socket-diff tool on 2026-08-28: six keys short on this
	// install — `pageBackups`, `pageDevices`, `pageWifi`, `notifBackupDrift`,
	// `notifBackupFail`, `notifReportFail`. The three `page*` ones are nav
	// visibility flags, so the client read `undefined` and those entries were
	// hidden. On a FRESH install, where settings.json is nearly empty, almost
	// every page flag would have been missing.
	//
	// The write path already used `mergedSettings`; this one did not, and no test
	// compared them because both agree completely on an install whose
	// settings.json happens to carry every key.
	cfg, err := cn.srv.mergedSettings()
	if err != nil {
		log.Printf("[settings] page settings: %v", err)
		return
	}
	cn.srv.hub.Send(cn.c, "settings:pages", store.PageSettings(cfg))
}

// sendOpenAlerts is the notification bell's INITIAL state.
//
// ── WHY IT IS SENT AT ALL ───────────────────────────────────────────────────
//
// Without it the bell starts empty on every load and fills only as new alerts
// happen — the "empty again after a refresh while the database holds open
// alerts" problem the live emit exists to solve. Recently-RESOLVED rows ride
// along so the panel shows what just happened as well as what is still wrong.
//
// ── TO ONE CLIENT, NOT THE ROOM ─────────────────────────────────────────────
//
// `Send`, not `Broadcast`. This is one browser's opening state; broadcasting it
// would reset the panel of everybody else already on that router, discarding any
// alert they had acknowledged locally since their own connect.
//
// ── AND A FAILURE HERE IS NOT A FAILURE TO SWITCH ROUTERS ───────────────────
//
// The live side wraps this in try/catch and warns. Same here: an unreadable
// alert table costs the bell its history, and taking the router switch down with
// it would turn a cosmetic problem into an unusable app. The caller continues to
// `router:switched` either way.
func (cn *conn) sendOpenAlerts(routerID string) {
	// REDUNDANT, and kept. Every method on `*db.DB` opens with `if d == nil ||
	// d.sql == nil`, so a nil store already answers with an error rather than a
	// panic — a mutation deleting this line survives, and is recorded as
	// equivalent rather than counted. It stays because it says at the top of the
	// function that a server without an alert store is an ordinary state, which
	// is otherwise only discoverable by reading another package.
	if cn.srv.auditDB == nil {
		return
	}
	open, err := cn.srv.auditDB.OpenAlerts(routerID, db.OpenAlertsDefaultLimit)
	if err != nil {
		log.Printf("[alerts] initial state for %s: %v", routerID, err)
		return
	}
	// TWENTY-FOUR HOURS, matching the live window. "Recent" is a display choice,
	// not a storage one — Reports still reads the whole table.
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	recent, err := cn.srv.auditDB.RecentAlerts(routerID, since, db.RecentAlertsDefaultLimit)
	if err != nil {
		log.Printf("[alerts] recent state for %s: %v", routerID, err)
		return
	}

	// ONE NAME MAP for up to 250 rows, built once. Per-row it would be 250 reads
	// of routers.json to answer a question with one answer.
	names := cn.srv.allRouterNames()
	cn.srv.hub.Send(cn.c, "alerts:open", map[string]any{
		"routerId": routerID,
		"open":     alert.MakeRows(open, names),
		"recent":   alert.MakeRows(recent, names),
	})
}

// pageFocus joins the page room and replays the last payload.
//
// Both halves are gated together on purpose. Node learned this the hard way:
// gating only the join would still hand the caller a full payload for a page
// they cannot see, so the check returns before either.
func (cn *conn) pageFocus(page string) {
	// ── RECORDED BEFORE THE GUARD, exactly as `dashCardFocus` records its key.
	//
	// `page:focus` and `router:select` are two frames from one bootstrap and
	// their order is not guaranteed. Arriving first, this used to return in
	// silence: no room, no wake, and nothing kept to replay. `selectRouter` then
	// left every room and joined only its own, so the page room stayed unjoined
	// for the life of the socket.
	cn.mu.Lock()
	cn.page = page
	cn.mu.Unlock()

	if cn.routerID == "" {
		// SAID OUT LOUD. The 2026-08-30 change made every refusal in
		// `selectRouter` name itself for this reason, and this is the sibling it
		// did not cover: the next occurrence should not cost another
		// reproduction. Not an error — `selectRouter` replays it.
		log.Printf("[ws] %s: page:focus %s deferred — no router selected yet",
			cn.c.ID, page)
		return
	}
	if !cn.canPage(page, "read") {
		return
	}
	cn.srv.hub.Join(cn.c, "router-"+cn.routerID+"-page-"+page)
	// THE DEVICES PAGE IS FLEET-WIDE, not about the router this socket has
	// selected — which is why it gets its own hook rather than a collector in
	// `resumePage`. Its rows come from the background pool plus every
	// interactive session, and the pool only runs while somebody is looking.
	if page == "devices" {
		cn.devicesFocus()
	}
	cn.resumePage(page)
}

// rejoinPage re-applies this browser's page focus to the CURRENT router.
//
// The page twin of `rejoinCards`, called from the same place and for the same
// two reasons: the client can focus a page before any router is selected, and
// the room is per router so a switch has to rejoin it against the new one.
func (cn *conn) rejoinPage() {
	cn.mu.Lock()
	page := cn.page
	cn.mu.Unlock()
	if page == "" {
		return
	}
	cn.pageFocus(page)
}

// resumePage wakes a page's collectors and replays their last payloads.
//
// Split out of pageFocus so a DASHBOARD CARD can do the same thing without
// joining the page room. A card is the only view some collectors get — the
// Firewall card on a dashboard is, for a viewer who never opens the Firewall
// page, the whole reason that collector should be running — so the wake has to
// be the same one, not an approximation of it.
func (cn *conn) resumePage(page string) {
	if cn.rsession == nil {
		return
	}
	// Opening a page is the cheapest re-probe available and by far the most
	// timely; and without the replay the page sits blank for a whole poll
	// interval on every visit. The replayed `ts` is stamped now, matching the
	// Node side, so the page's staleness overlay does not fire on a payload
	// that was collected a moment ago.
	switch page {
	// ── The Dashboard ────────────────────────────────────────────────────────
	//
	// Only the ping history, and only here. The live app sends it in the block
	// it replays on CONNECT, to every viewer regardless of the page they land
	// on; this side sends it when the Dashboard is focused, which is the only
	// place the latency block exists. Same thing seen, one fewer payload for a
	// viewer who never opens it.
	//
	// `ping:update` needs no replay: the collector emits on its own cadence and
	// the card fills within a tick. The HISTORY is different — it is the chart's
	// entire backlog, and without it the chart starts empty every visit.
	case "dashboard":
		// ── THE DASHBOARD CARDS, REPLAYED ───────────────────────────────────
		//
		// These three are emitted to `page-dashboard` by collectors that run
		// from CONNECT, not from focus — so opening the Dashboard replayed
		// nothing and the cards stayed empty until the next tick, which for
		// netwatch and talkers is up to a minute. The live app sends all three
		// in `sendInitialState`.
		//
		// Found by the initial-state audit, alongside the four
		// router-wide chrome events replayed on the handshake.
		if last := cn.rsession.Netwatch().Last(); last != nil {
			cn.srv.hub.Send(cn.c, "netwatch:update", last)
		}
		if last := cn.rsession.Talkers().Last(); last != nil {
			cn.srv.hub.Send(cn.c, "talkers:update", last)
		}
		if p := cn.rsession.Ping(); p != nil {
			if last := p.Last(); last != nil {
				cn.srv.hub.Send(cn.c, "ping:update", last)
			}
		}
		// ── ROUTES AND BGP PEERS ────────────────────────────────────────────
		//
		// Unlike the three above, `routing` does NOT run from connect: it is
		// page-gated, and its only gate was the Routing page. So the Dashboard
		// has to wake it the way a card focus wakes a card's collector -- and
		// then replay, or both cards show em dashes until the next tick, which
		// is up to 60s.
		//
		// `ResumeCollector` is the right funnel rather than touching the loop
		// directly: it honours the operator's per-collector enable switch, and
		// it latches when the router is not connected yet.
		cn.rsession.ResumeCollector("routing")
		if last := cn.rsession.Routing().Last(); last != nil {
			cn.srv.hub.Send(cn.c, "routing:update", last)
		}
		if p := cn.rsession.Ping(); p != nil {
			hist := p.History()
			// Empty is not sent, as the original does not send it: an empty
			// history would clear a chart the viewer may already be watching
			// after a page switch.
			if len(hist.History) > 0 {
				out := map[string]any{"target": hist.Target, "history": hist.History}
				// min/max ride along from the last payload, exactly as the
				// original attaches them — the history points carry rtt and
				// loss only, and the card shows the extremes beside them.
				if last := p.Last(); last != nil {
					out["minRtt"] = last.MinRTT
					out["maxRtt"] = last.MaxRTT
				}
				cn.srv.hub.Send(cn.c, "ping:history", out)
			}
		}
	case "dns":
		cn.rsession.ResumeCollector("dns")
		if last := cn.rsession.DNS().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "dns:update", replay)
		}
	case "bridges":
		cn.rsession.ResumeCollector("bridges")
		if last := cn.rsession.Bridges().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "bridges:update", replay)
		}
	case "vlans":
		cn.rsession.ResumeCollector("vlans")
		if last := cn.rsession.Vlans().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "vlans:update", replay)
		}
	case "wan":
		cn.rsession.ResumeCollector("wan")
		if last := cn.rsession.Wan().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "wan:update", replay)
		}
	case "packages":
		cn.rsession.ResumeCollector("packages")
		if last := cn.rsession.Packages().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "packages:update", replay)
		}
	case "routing":
		cn.rsession.ResumeCollector("routing")
		if last := cn.rsession.Routing().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "routing:update", replay)
		}
	case "ppp":
		cn.rsession.ResumeCollector("ppp")
		if last := cn.rsession.PPP().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "ppp:update", replay)
		}
	case "vpn":
		cn.rsession.ResumeCollector("vpn")
		if last := cn.rsession.VPN().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "vpn:update", replay)
		}
	case "users":
		cn.rsession.ResumeCollector("rosusers")
		// The caps go FIRST. The page draws its buttons from `permitted`, and a
		// payload arriving before them renders a read-only table that then has to
		// be redrawn — visible as a flicker on every visit.
		cn.srv.hub.Send(cn.c, "rosusers:caps", map[string]any{
			"permitted":  cn.canPage("users", "write"),
			"routerName": cn.rsession.Label,
		})
		if last := cn.rsession.RosUsers().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "rosusers:update", replay)
		}
	case "capsman":
		cn.rsession.ResumeCollector("capsman")
		if last := cn.rsession.Capsman().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "capsman:update", replay)
		}
	// interfaceStatus is NOT suspended on blur — three other collectors take it
	// as their rate source, and a bridges viewer who never opens Interfaces
	// would otherwise see every throughput column go blank. Opening the page
	// still replays the last payload so it is not empty for a whole poll.
	case "interfaces":
		if last := cn.rsession.IfStatus().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "ifstatus:update", replay)
		}
	// The backlog, as one frame. Not a Resume: this collector holds a push
	// channel open for the life of the connection rather than polling, so there
	// is nothing to wake — a viewer opening the page just needs what has
	// accumulated so far, and the live tail is already on its way.
	// Resumed on focus and suspended on blur, unlike ifStatus: nothing else
	// reads this collector, and it holds a ping loop as well as a poll, so a
	// viewer who is not looking at the map should not be making the router ping
	// two dozen devices.
	case "network-topology":
		cn.rsession.ResumeCollector("topology")
		if last := cn.rsession.Topology().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "topology:update", replay)
		}
	// Page-gated, and it OWNS the connection-table read that bandwidth also
	// consumes — so opening either page starts it, and it is suspended only when
	// neither is being watched.
	case "connections":
		cn.rsession.ResumeCollector("conns")
		if last := cn.rsession.Conns().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "conn:update", replay)
		}
	// Page-gated, and the gate matters more here than on most: this collector
	// reads a table that can hold thousands of rows, and nothing else needs it.
	case "bandwidth":
		// The connection table is the input to BOTH, so opening Bandwidth has to
		// start the collector that reads it.
		cn.rsession.ResumeCollector("conns")
		cn.rsession.ResumeCollector("bandwidth")
		if last := cn.rsession.Bandwidth().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "bandwidth:update", replay)
		}
	case "wifi-clients":
		cn.rsession.ResumeCollector("wireless")
		if last := cn.rsession.Wireless().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "wireless:update", replay)
		}
	case "logs":
		if last := cn.rsession.Logs().Last(); last != nil {
			cn.srv.hub.Send(cn.c, "logs:history", last)
		}
	case "wifi-networks":
		cn.rsession.ResumeCollector("wifi")
		if last := cn.rsession.Wifi().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "wifi:update", replay)
		}
	case "firewall":
		cn.rsession.ResumeCollector("firewall")
		if last := cn.rsession.Firewall().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "firewall:update", replay)
		}
	case "queues":
		cn.rsession.ResumeCollector("queues")
		// Caps first, for the reason Router Users gives: the page draws its
		// buttons from `permitted`, and a payload arriving before them renders a
		// read-only table that then has to be redrawn.
		cn.srv.hub.Send(cn.c, "queues:caps", map[string]any{
			"permitted":  cn.canPage("queues", "write"),
			"routerName": cn.rsession.Label,
		})
		if last := cn.rsession.Queues().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "queues:update", replay)
		}
	case "dhcp":
		// TWO collectors and two events, and the ORDER matters: lan:overview
		// carries the pool size the gauge divides by, and the leases handler
		// redraws the gauge as its last act. Replaying the leases first would
		// draw a gauge against a pool size of zero until the next tick.
		cn.rsession.ResumeCollector("dhcpNetworks")
		cn.rsession.ResumeCollector("dhcpLeases")
		// ── NOTHING TO REPLAY MEANS READ, NOT WAIT ────────────────────────
		//
		// `Resume` above is `poll.start()`, which waits out the REMAINDER of the
		// interval rather than firing -- deliberately, so page navigation cannot
		// generate a request per visit. Both these collectors poll every 600s,
		// so when there is no last payload to replay that gate turns into a TEN
		// MINUTE blank page: "Waiting for network data…" until the tick comes
		// round, or until a reconnect calls Tick directly. The operator saw the
		// second one -- a disconnected banner, then the subnets appearing.
		//
		// The refresh is conditional on having nothing to show, which keeps the
		// "gentle on the router" property the poll loop is protecting: a page
		// with data replays it and reads nothing.
		//
		// GATED ON CollectorEnabled, and called DIRECTLY rather than in a
		// goroutine: `TestEveryCollectorEntryPointIsGated` requires the guard to
		// sit immediately above its call, and every other entry point in this
		// file reads synchronously on this goroutine. An operator who turned a
		// collector off has not consented to a page visit turning it back on.
		if last := cn.rsession.DHCPNetworks().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "lan:overview", replay)
		} else if cn.rsession.CollectorEnabled("dhcpNetworks") {
			cn.rsession.DHCPNetworks().RefreshNow()
		}
		if last := cn.rsession.DHCPLeases().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			cn.srv.hub.Send(cn.c, "leases:list", replay)
		} else if cn.rsession.CollectorEnabled("dhcpLeases") {
			cn.rsession.DHCPLeases().RefreshNow()
		}
	}
}

func (cn *conn) pageBlur(page string) {
	// BEFORE the routerID guard, and that is not tidiness. The Devices page is
	// fleet-wide: a socket can be on it with no router selected at all, and
	// returning early would leave this connection in `devicesWatchers` forever —
	// so the pool would never see its last watcher leave and would hold a
	// connection to every router indefinitely.
	if page == "devices" {
		cn.devicesBlur()
	}
	// FORGOTTEN HERE TOO, or a later `router:select` would replay a page this
	// viewer has left and re-wake its collectors. Only when it is the page we
	// are holding: a blur for some other page says nothing about this one.
	cn.mu.Lock()
	if cn.page == page {
		cn.page = ""
	}
	cn.mu.Unlock()

	if cn.routerID == "" {
		return
	}
	room := "router-" + cn.routerID + "-page-" + page
	cn.srv.hub.Leave(cn.c, room)
	// The finer gate: stop reading for a page whose last viewer just left. The
	// idle gate in Manager.Release still handles "nobody is watching the router
	// at all"; this is for somebody who is here but looking elsewhere.
	if cn.rsession == nil || cn.srv.hub.Occupants(room) != 0 {
		return
	}
	switch page {
	case "dns":
		cn.rsession.DNS().Suspend()
	case "bridges":
		cn.rsession.Bridges().Suspend()
	case "vlans":
		cn.rsession.Vlans().Suspend()
	case "wan":
		cn.rsession.Wan().Suspend()
	case "packages":
		cn.rsession.Packages().Suspend()
	case "routing":
		// The dashboard's Routes and BGP Peers cards read this collector as of
		// 2026-08-31, so a Routing-page blur no longer means nobody is watching.
		// `blur-suspend-audit` caught this the moment the second room was added,
		// which is the third time it has caught exactly this consequence
		// (dhcpNetworks, bandwidth, vpn, firewall before it).
		cn.srv.suspendIfNoRoomOccupied(cn.rsession, cn.routerID,
			[]string{"page-dashboard"}, cn.rsession.Routing().Suspend)
	case "dhcp":
		// dhcpNetworks also feeds the dashboard's Network card AND the
		// router-wide `lan:wan` chip, which is on every page — so it is
		// suspended only when nobody is looking at either.
		//
		// THE ROUTER-WIDE ROOM IS NOT LISTED, and cannot be: it is the empty
		// room, which every viewer of this router occupies, so testing it would
		// mean never suspending at all. The dashboard card is the proxy — the
		// chip is chrome fed by the same payload, and a viewer on any page has
		// the value the handshake replayed. Recorded because it is a judgement,
		// not a mechanism.
		cn.srv.suspendIfNoRoomOccupied(cn.rsession, cn.routerID,
			[]string{"dash-card-network"}, cn.rsession.DHCPNetworks().Suspend)
		cn.rsession.DHCPLeases().Suspend()
	case "dashboard":
		// ── THE OTHER HALF OF THE ROUTING GUARD ─────────────────────────────
		//
		// pageBlur had NO dashboard case, and did not need one: every collector
		// feeding a dashboard card ran from CONNECT, so there was nothing a blur
		// could usefully stop.
		//
		// `routing` broke that assumption. It is page-gated, and the Dashboard
		// now wakes it for the Routes and BGP Peers cards -- so without this, a
		// viewer who glanced at the dashboard once left it polling the router
		// forever, on a box where concurrent channels are the documented
		// bottleneck. Suspended only when the Routing page is not also open.
		cn.srv.suspendIfNoRoomOccupied(cn.rsession, cn.routerID,
			[]string{"page-routing"}, cn.rsession.Routing().Suspend)
	case "ppp":
		cn.rsession.PPP().Suspend()
	case "vpn":
		// The dashboard's VPN card reads the same collector — the live
		// `_updatePageStream` counts occupancy across ALL of a collector's
		// stream rooms, which is why the live app never had this. Found by
		// The blur-suspend audit on its first run, after the same defect
		// was fixed by hand for dhcpNetworks and bandwidth.
		cn.srv.suspendIfNoRoomOccupied(cn.rsession, cn.routerID,
			[]string{"dash-card-vpn"}, cn.rsession.VPN().Suspend)
	case "users":
		cn.rsession.RosUsers().Suspend()
	case "queues":
		cn.rsession.Queues().Suspend()
	case "firewall":
		// The dashboard card reads the same collector. Its room was added on
		// 2026-08-29 when the emit-rooms audit found this payload reaching
		// one room where live sends it to two — and `blur-suspend-audit` caught
		// the consequence immediately: a page blur says nothing about whether the
		// CARD is still watching, so suspending here would starve it.
		cn.srv.suspendIfNoRoomOccupied(cn.rsession, cn.routerID,
			[]string{"dash-card-firewall"}, cn.rsession.Firewall().Suspend)
	case "wifi-networks":
		cn.rsession.Wifi().Suspend()
	case "capsman":
		cn.rsession.Capsman().Suspend()
	case "network-topology":
		cn.rsession.Topology().Suspend()
	case "wifi-clients":
		// The dashboard card reads the same collector. Its room was added on
		// 2026-08-29 when the emit-rooms audit found this payload reaching
		// one room where live sends it to two — and `blur-suspend-audit` caught
		// the consequence immediately: a page blur says nothing about whether the
		// CARD is still watching, so suspending here would starve it.
		cn.srv.suspendIfNoRoomOccupied(cn.rsession, cn.routerID,
			[]string{"dash-card-wireless"}, cn.rsession.Wireless().Suspend)
	case "bandwidth":
		// The dashboard's bandwidth card reads the same collector.
		cn.srv.suspendIfNoRoomOccupied(cn.rsession, cn.routerID,
			[]string{"dash-card-bandwidth"}, cn.rsession.Bandwidth().Suspend)
		cn.srv.suspendConnsIfIdle(cn.rsession, cn.routerID)
	case "connections":
		cn.srv.suspendConnsIfIdle(cn.rsession, cn.routerID)
	}
}

// trafficSelect moves this viewer's chart to another interface.
//
// ONE ROOM PER INTERFACE. The Node side keeps a per-socket subscription list;
// this hub already does rooms well, so the same thing is expressed as joining
// `router-<id>-traffic-<name>` and leaving whatever was joined before. The
// collector keeps the refcount, so the stream shrinks only when the last viewer
// of an interface goes away.
// defaultIfFor is the interface a freshly attached viewer watches.
//
// It comes from the SESSION rather than from the router record, because
// `session.defaultIfOr` has already applied index.js's fallback for a router
// that names none — reading routers.json again here would reimplement that
// fallback and the two would drift.
func defaultIfFor(rs *session.Session) string {
	if rs == nil {
		return ""
	}
	return rs.Traffic().DefaultIf()
}

// trafficSelectDefault subscribes a freshly attached viewer to the default
// interface WITHOUT normalising the name.
//
// ── WHY IT SKIPS THE VALIDATION trafficSelect DOES ──────────────────────────
//
// Two reasons, and the second is why the first version of this fix did nothing.
//
//  1. THE NAME IS NOT THE CALLER'S. `trafficSelect` normalises because the name
//     arrives in a socket payload and reaches a router command; this one comes
//     from routers.json through `session.defaultIfOr`, which is the same path
//     the collector itself uses to decide what to stream. Validating the
//     server's own configuration against the router adds no safety.
//
//  2. AT ATTACH TIME THERE IS NOTHING TO VALIDATE AGAINST. `trafficSelect` feeds
//     `SetAvailable` from `IfStatus().Last()`, and on a fresh attach the status
//     collector has not produced a reading yet — so `NormalizeIfName` refuses,
//     the function returns early, and no room is joined. That is exactly what
//     happened when this fix was first written as a call to `trafficSelect`:
//     measured against the real AX3, still traffic:update x0.
func (cn *conn) trafficSelectDefault(ifName string) {
	if ifName == "" || cn.routerID == "" || cn.rsession == nil || cn.trafficIf != "" {
		return
	}
	cn.trafficIf = ifName
	cn.srv.hub.Join(cn.c, session.RoomFor(cn.routerID, collect.TrafficSub(ifName)))
	// `Watch` also registers the interest that keeps the stream running, and
	// returns whatever history has accumulated — which for the default
	// interface is usually not empty, because it streams from the connection
	// rather than from the first viewer.
	cn.srv.hub.Send(cn.c, "traffic:history", cn.rsession.Traffic().Watch(ifName))
}

func (cn *conn) trafficSelect(name string) {
	if cn.routerID == "" || cn.rsession == nil {
		return
	}
	tr := cn.rsession.Traffic()
	// The interface list comes from the status collector rather than a second
	// read: it is already watching every interface on the router.
	if last := cn.rsession.IfStatus().Last(); last != nil {
		names := make([]string, 0, len(last.Interfaces))
		for _, i := range last.Interfaces {
			names = append(names, i.Name)
		}
		tr.SetAvailable(names)
	}
	ifName, ok := tr.NormalizeIfName(name)
	if !ok {
		return
	}
	if cn.trafficIf == ifName {
		return
	}
	if cn.trafficIf != "" {
		cn.srv.hub.Leave(cn.c, session.RoomFor(cn.routerID, collect.TrafficSub(cn.trafficIf)))
		tr.Unwatch(cn.trafficIf)
	}
	cn.trafficIf = ifName
	cn.srv.hub.Join(cn.c, session.RoomFor(cn.routerID, collect.TrafficSub(ifName)))
	// The history goes to THIS viewer only, and immediately: a chart that waited
	// for the next sample would draw a single point on a five-minute axis.
	cn.srv.hub.Send(cn.c, "traffic:history", tr.Watch(ifName))
}

// dropTraffic detaches this viewer from whatever it was watching. Called when
// the router changes and when the connection goes away, because the refcount is
// what keeps the stream honest.
func (cn *conn) dropTraffic() {
	if cn.routerID == "" || cn.trafficIf == "" || cn.rsession == nil {
		return
	}
	cn.srv.hub.Leave(cn.c, session.RoomFor(cn.routerID, collect.TrafficSub(cn.trafficIf)))
	cn.rsession.Traffic().Unwatch(cn.trafficIf)
	cn.trafficIf = ""
}

// suspendConnsIfIdle stops the connection-table read only when NEITHER page that
// depends on it has a viewer.
//
// Two consumers, one read: suspending it because one page closed would blank the
// other. The hub's room occupancy is the authority on who is still looking.
func (s *Server) suspendConnsIfIdle(rs *session.Session, routerID string) {
	s.suspendIfNoRoomOccupied(rs, routerID,
		[]string{"page-connections", "page-bandwidth"}, rs.Conns().Suspend)
}

// suspendIfNoRoomOccupied stops a collector only when EVERY room it emits to is
// empty.
//
// ── THE DEFECT THIS GENERALISES ─────────────────────────────────────────────
//
// A collector that feeds more than one room must not be suspended because ONE of
// them emptied. `conns` had this guard from the start — it feeds the Connections
// page and the dashboard's connections card — and two collectors that need it
// just as much did not:
//
//	dhcpNetworks  page-dhcp, dash-card-network, AND the router-wide room
//	              (`lan:wan`, the WAN chip on every page)
//	bandwidth     page-bandwidth, dash-card-bandwidth
//
// So leaving the DHCP page froze the dashboard's Network card and the WAN chip,
// and leaving the Bandwidth page froze the dashboard's bandwidth card, for as
// long as the session lasted. Found 2026-08-28 by comparing this port's blur
// cases against the live `_PAGE_STREAM_ROOMS`: the port suspends a strict
// SUPERSET of what the live app does — which is the right direction, fewer
// channels held, and is why the live app never had this bug.
//
// The DASHBOARD ROOMS ARE THE POINT. A page room emptying is what triggers a
// blur; a `dash-card-*` room emptying is not, so it must be TESTED rather than
// assumed. `nil` is a collector this session does not have.
func (s *Server) suspendIfNoRoomOccupied(rs *session.Session, routerID string,
	rooms []string, suspend func()) {
	if rs == nil || routerID == "" || suspend == nil {
		return
	}
	if s.roomsOccupied(routerID, rooms) {
		return
	}
	// ── THE SAME GRACE THE SESSION GETS, AND FOR THE SAME EVENT ───────────
	//
	// A page refresh empties every room this viewer was in and refills them a
	// second later. Suspending on the empty moment stops the collector's stream
	// and starts it again immediately — churn on the one resource this project
	// conserves, API channels, to save one second of polling.
	//
	// THE OCCUPANCY IS RE-READ WHEN THE TIMER FIRES, which is what makes this
	// safe to do without tracking timers: a viewer who came back is simply seen,
	// and the suspend is skipped. A viewer who did not is suspended late rather
	// than never. Several timers may be in flight after rapid page switching;
	// each re-reads, and all but the last find an occupied room and do nothing.
	time.AfterFunc(s.graceFor(), func() {
		if s.roomsOccupied(routerID, rooms) {
			return
		}
		// NO "IS THE SESSION STILL LIVE?" CHECK HERE, deliberately. One was
		// written and removed: suspending a collector on a torn-down session is
		// inert, so it guarded nothing, and it could not even shorten the
		// closure's reach -- `suspend` is a method value on the collector, so
		// the session is retained by this timer either way. What it did do was
		// dereference the Manager on a TIMER GOROUTINE, where a nil is a dead
		// process rather than a failed request. The race suite found that
		// immediately, on the server tests that build no Manager at all.
		suspend()
	})
}

// graceFor is the page-level idle window, matching the session's. Zero means
// the default, so only a test has to know the field exists.
func (s *Server) graceFor() time.Duration {
	if s.idleGrace > 0 {
		return s.idleGrace
	}
	return session.DefaultIdleGrace
}

// roomsOccupied reports whether any of a collector's rooms still has a viewer.
func (s *Server) roomsOccupied(routerID string, rooms []string) bool {
	for _, r := range rooms {
		if s.hub.Occupants("router-"+routerID+"-"+r) > 0 {
			return true
		}
	}
	return false
}

func (cn *conn) releaseRouter() {
	if cn.routerID == "" {
		return
	}
	cn.dropTraffic()
	cn.srv.sessions.Release(cn.routerID)
	cn.routerID = ""
	cn.rsession = nil
	// AND THE POOL RECLAIMS IT. The other half of the exclusion: `Acquire` makes
	// the pool let go, so `Release` has to make it pick the router back up.
	// Without this the last browser closing would leave that router covered by
	// NOTHING — no status, no alerts, no history — which is the very gap the
	// always-on pool exists to close.
	//
	// `Release` is ref-counted, so this runs when the LAST watcher goes; a second
	// browser on the same router keeps the session and the exclusion.
	cn.srv.syncAlertPool()
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// sendPooledStatus tells a browser the CURRENT state of every router the alert
// pool holds.
//
// ── A TRANSITION IS NOT A STATE, AND THIS IS THE DIFFERENCE ───────────────
//
// `alertPoolStatus` broadcasts on CHANGE, which is right: an unreachable router
// re-dials every five seconds and a frame per attempt would reach every browser.
// But a browser that connects AFTER the change never heard it. Measured on
// 2026-08-29: the pool connected both non-active routers at 10:52:49, a browser
// arrived at 10:53:40, and the Settings table showed em dashes for both — the
// pool was working and the page could not know.
//
// The live app closes the same gap in `sendInitialState`: after the selected
// router's status it walks `alertSessions.getStatusMap()` and emits one frame
// per router.
//
// ── FILTERED BY WHAT THE CALLER MAY SEE ───────────────────────────────────
//
// "Reachability of other routers is only disclosed within the caller's allowed
// set" (`index.js:4276`). Whether a router is reachable is information about the
// estate, so it follows `router:read` like the list itself. `visibleRouters`
// returns nil for an install with no RBAC, which means no filtering — the same
// convention every other caller of it follows.
func (cn *conn) sendPooledStatus() {
	if cn.srv.alertPool == nil {
		return
	}
	visible := cn.srv.visibleRouters(cn.sess)
	for id, up := range cn.srv.alertPool.Status() {
		if visible != nil && !visible[id] {
			continue
		}
		cn.srv.hub.Send(cn.c, "router:status", map[string]any{
			"routerId": id, "connected": up})
	}
}
