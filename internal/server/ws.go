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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"mikrodash/internal/alert"
	"mikrodash/internal/areas"
	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/session"
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
	c   *hub.Client
	ws  *websocket.Conn
	srv *Server

	// ── ONE GOROUTINE OWNS sess, routerID AND rsession ──────────────────────
	//
	// They were plain fields written by the reader goroutine (router:select),
	// the 60 s revalidator (a revoked grant) and, until 2026-09-19, an HTTP
	// handler (router activation), and read by background work the reader had
	// started. Opening a 37,000-entry address list and switching router before
	// it came back made the worker call Exec on the nil session the switch had
	// left: a panic with no recover, and the whole server gone (review
	// 2026-09-19).
	//
	// So: every frame and every change of router state runs on ONE loop
	// goroutine (see serve). Code there reads the fields directly. The loop
	// writes them under scopeMu, and any OTHER goroutine reads them through
	// scope(), a snapshot taken under that lock, captured when its work starts.
	sess     *Session
	routerID string
	rsession *session.Session
	scopeMu  sync.RWMutex
	// inbox is the loop's queue: each frame from the reader, and each change
	// posted from elsewhere (the revalidator; the assistant's writes). nil when
	// no loop runs, as in tests, and then post runs the function inline.
	inbox    chan func()
	postMu   sync.Mutex
	inboxOff bool
	loopDone chan struct{}
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

	// diagTick is this viewer's API Diagnostics refresh, and it is PER SOCKET
	// for the same reason the Devices one is: the card reports on the router
	// THIS connection has selected, and two browsers on two routers want two
	// different payloads. Started when the card is added, stopped when it is
	// removed or the socket goes.
	diagMu   sync.Mutex
	diagTick *time.Ticker
	diagStop chan struct{}
	// The Agent Overview's cadence, PER SOCKET for the same reason the
	// diagnostics ticker is: the sentence describes the router THIS connection
	// has selected, so two viewers on two routers must not share one answer.
	// A ticker that exists only while the card is on somebody's Dashboard is
	// also what makes "generates nothing while unwatched" structural rather
	// than a condition somebody has to remember to check.
	agentMu   sync.Mutex
	agentStop chan struct{}
	agentKick chan struct{}
	// agentForce makes the next overview tick skip the cached line: the
	// operator pressed refresh. Read and cleared by that tick.
	agentForce atomic.Bool
	// proposals are the assistant's unanswered write proposals, PER SOCKET and
	// deliberately so: a proposal is a question put to the person looking at
	// this page, and a token minted for them must not be answerable from
	// another browser, another tab or another session. They die with the
	// socket, which is the right lifetime for a question nobody answered.
	proposeMu sync.Mutex
	proposals map[string]*aiWriteProposal
	// toolBusy is set while this connection has a diagnostic running on the
	// router: one at a time, see internal/server/tools.go.
	toolBusy atomic.Bool
	// toolQuit closes to end the page's run in flight: see startTool and
	// stopTool. Under toolMu, because releaseRouter runs on the revalidator's
	// goroutine as well as the read loop.
	toolMu   sync.Mutex
	toolQuit chan struct{}
	// scanQuit closes to end this connection's Security Scan in flight, on a
	// router switch or a closed socket: see secscan.go.
	scanMu   sync.Mutex
	scanQuit chan struct{}
	// appsQuit closes to stop following an app install on a router switch or
	// a closed socket: see apps.go.
	appsMu   sync.Mutex
	appsQuit chan struct{}
	// groups keeps this browser's last grouped-table read, so a search filters
	// it rather than reading a large list again (area_group.go).
	groups groupMemo
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

// sameOriginHost reports whether an Origin header names this same host.
//
// The comparison `coder/websocket` itself makes before consulting the patterns,
// repeated here only to decide whether the log line is worth a hint. A malformed
// Origin counts as "not the same host", which is the direction that offers help
// rather than withholding it.
func sameOriginHost(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, host)
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
		// NAME THE FIX IN THE LOG. An origin rejection is the one handshake
		// failure an operator can act on, and the library's message states the
		// mismatch without saying what to do about it — which is how issue #128
		// arrived as a bug report rather than as a configuration question.
		//
		// Detected by comparing the headers rather than by matching the error
		// text, so a library rewording cannot silently drop the hint.
		if o := r.Header.Get("Origin"); o != "" && !sameOriginHost(o, r.Host) {
			log.Printf("[ws] accept: %v — set -origins (or MIKRODASH_ORIGINS) to "+
				"the host the browser uses if MikroDash is behind a reverse proxy", err)
			return
		}
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

	cn.serve(ctx)

	s.connsMu.Lock()
	delete(s.conns, cn.c)
	s.connsMu.Unlock()

	// A CLOSED TAB SENDS NO BLUR. Without these the pool keeps a connection to
	// every router for a page nobody has open — the exact cost `devicesBlur`
	// exists to avoid, reached by the commonest way a viewer leaves — and every
	// collector this viewer started keeps polling until the session's own idle
	// grace expires. `releaseRouter` leaves the rooms and re-asks demand, which
	// is what makes a closed tab indistinguishable from a blur.
	cn.devicesBlur()
	// The diagnostics ticker too: it is per socket, so a closing connection that
	// left it running would repaint a card nobody has, for ever.
	cn.diagBlur()
	// AND THE OVERVIEW CARD'S LOOP, which was missing here: a closed tab sends no
	// blur, so it ran on for a card nobody could see.
	cn.agentBlur()
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
			live, err := cn.srv.auth.Validate(cn.cookie)
			if err != nil {
				EvSessionExpired.Send(cn.srv.hub, cn.c, map[string]any{})
				// Give the frame a moment to leave before the socket goes.
				time.Sleep(200 * time.Millisecond)
				_ = cn.ws.Close(websocket.StatusPolicyViolation, "session expired")
				return
			}
			// ON THE LOOP, not here: the revoke releases the router, and a
			// release on this goroutine raced a router:select on the loop (two
			// Releases for one Acquire, and a torn view of cn.sess).
			cn.post(func() {
				cn.setSession(live)
				if cn.routerID != "" && !live.CanReadRouter(cn.routerID) {
					// `releaseRouter` leaves every room this connection is in.
					cn.releaseRouter()
					EvAccessRevoked.Send(cn.srv.hub, cn.c, map[string]any{})
				}
			})
		}
	}
}

// serve runs the connection: the reader hands each frame to the loop, and
// returns when the socket does, after the loop has drained. Everything that
// changes this connection's router state happens on the loop (see conn).
func (cn *conn) serve(ctx context.Context) {
	cn.inbox = make(chan func(), 64)
	cn.loopDone = make(chan struct{})
	go func() {
		defer close(cn.loopDone)
		for f := range cn.inbox {
			f()
		}
	}()
	cn.reader(ctx)
	cn.postMu.Lock()
	cn.inboxOff = true
	close(cn.inbox)
	cn.postMu.Unlock()
	<-cn.loopDone
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
		cn.post(func() { cn.dispatch(in) })
	}
}

// post queues f to run on the loop, and reports false when the loop has
// stopped. With no loop (tests), f runs at once on the caller.
func (cn *conn) post(f func()) bool {
	if cn.inbox == nil {
		f()
		return true
	}
	cn.postMu.Lock()
	defer cn.postMu.Unlock()
	if cn.inboxOff {
		return false
	}
	cn.inbox <- f
	return true
}

// onLoop runs f on the loop and waits for it. For work started elsewhere that
// must see and change this connection as the loop does: the assistant's writes.
// Never call it from the loop itself, which would wait on itself.
func (cn *conn) onLoop(f func()) bool {
	if cn.inbox == nil {
		f()
		return true
	}
	done := make(chan struct{})
	if !cn.post(func() { defer close(done); f() }) {
		return false
	}
	<-done
	return true
}

// connScope is a snapshot of the router state a piece of background work was
// started for, captured with scope() when that work starts.
type connScope struct {
	sess     *Session
	routerID string
	rs       *session.Session
}

// scope is the connection's router state, read under the lock, for a goroutine
// that is not the loop.
func (cn *conn) scope() connScope {
	cn.scopeMu.RLock()
	defer cn.scopeMu.RUnlock()
	return connScope{sess: cn.sess, routerID: cn.routerID, rs: cn.rsession}
}

// setRouter and setSession are the only writes of the router state, made on the
// loop under the lock so scope() never reads a torn value.
func (cn *conn) setRouter(id string, rs *session.Session) {
	cn.scopeMu.Lock()
	cn.routerID, cn.rsession = id, rs
	cn.scopeMu.Unlock()
}

func (cn *conn) setSession(s *Session) {
	cn.scopeMu.Lock()
	cn.sess = s
	cn.scopeMu.Unlock()
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
	// One group of a grouped generated table (Address Lists, by list), read
	// filtered on the router when the page opens it. See area_group.go.
	case "area:group":
		cn.areaGroup(in.Data)
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
	case "packages:fwupgrade":
		cn.packagesFwUpgrade(in.Data)
	case "packages:autoupgrade":
		cn.packagesAutoUpgrade(in.Data)
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
	// One question to the configured model (#98). It may call READ tools: the
	// catalogue is generated from the resource registry and every entry is a
	// list, with `resource.Action` excluded by name, so nothing it can call
	// changes a router. See internal/aitools.
	case "ai:ask":
		cn.aiAsk(in.Data)
	// The saved conversation for this person on this router, and deleting it.
	// The Agent Overview card's refresh icon: a fresh line now, skipping the cache.
	case "ai:overview:refresh":
		cn.agentRefresh(in.Data)
	case "ai:history":
		cn.aiHistoryLoad(in.Data)
	case "ai:clear":
		cn.aiClear(in.Data)
	// The operator's answer to a change the assistant proposed. The token is
	// single use and belongs to this socket; everything else about the write is
	// re-derived server-side, so neither frame carries values to be trusted.
	case "ai:write:approve":
		cn.aiWriteApprove(in.Data)
	case "ai:write:reject":
		cn.aiWriteReject(in.Data)
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
	case "rossession:remove":
		cn.ruSessionRemove(in.Data)
	// Queues is five handlers of its own, for the same reason Router Users is:
	// see the Queues page's handlers.
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
	// The Tools page: see internal/server/tools.go.
	case "tools:ping":
		cn.toolsPing(in.Data)
	case "tools:traceroute":
		cn.toolsTraceroute(in.Data)
	case "tools:torch":
		cn.toolsTorch(in.Data)
	case "tools:btest":
		cn.toolsBtest(in.Data)
	case "tools:caps":
		cn.toolsCaps()
	// Stop: the run's own quit, exactly as a router switch ends it. Its last
	// frame, with code "stopped", carries the run so far.
	case "tools:stop":
		cn.stopTool()
	// The Security Scan page: see internal/server/secscan.go.
	case "secscan:get":
		cn.secScanGet()
	case "secscan:run":
		cn.secScanRun()
	// The Dashboard's Security Score card: Rescan. See secscan.go.
	case "secscore:scan":
		cn.secScoreScan()
	// The Containers page's Apps tab: see internal/server/apps.go.
	case "apps:list":
		cn.appsList()
	case "apps:do":
		cn.appsDo(in.Data)
	case "apps:setup":
		cn.appsSetup(in.Data)
	// Registered as its own literal beside firewall:tab rather than folded into
	// it, for the reason wan:renew and wan:release are separate: the next person
	// greps for the event name, and these two carry DIFFERENT PERMISSION GATES
	// that a shared handler would invite someone to collapse.
	case "firewall:v6":
		cn.fwWantV6(in.Data)
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

// fwWantV6 turns the four IPv6 tables on or off for this router's session.
//
// ── READ-GATED, WHERE fwTab IS WRITE-GATED, AND THAT IS DELIBERATE ──────────
//
// The two look alike and are not. Changing the ACTIVE TABLE changes what every
// other viewer of this router sees, so it is a write even though it writes
// nothing to the router. Asking for IPv6 only ADDS four arrays to the payload:
// nobody's view changes, nothing is removed, and every existing table keeps
// arriving exactly as before.
//
// Gating it on write would mean a read-only principal could open the IPv6 tab
// and be shown a permanently empty table with no error and no explanation —
// worse than useless, and indistinguishable from a router with no rules.
//
// It does cost router channels, which is a read-side cost, paid only while
// somebody is looking. The collector releases the want in Suspend().
func (cn *conn) fwWantV6(raw json.RawMessage) {
	var on bool
	if json.Unmarshal(raw, &on) != nil {
		return
	}
	if cn.routerID == "" || cn.rsession == nil {
		return
	}
	if !cn.canPage("firewall", "read") {
		return
	}
	cn.rsession.Firewall().SetWantV6(on)
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
		EvAccessNone.Send(cn.srv.hub, cn.c, map[string]any{})
		return
	}
	log.Printf("[ws] %s: router:select %s", cn.c.ID, id)
	// Leaves every room this connection is in, and re-asks demand for the router
	// it is leaving before it lets go of the session.
	cn.releaseRouter()

	rs, err := cn.srv.sessions.Acquire(id)
	if err != nil {
		log.Printf("[ws] %s: router:select %s FAILED to acquire: %v", cn.c.ID, id, err)
		session.EvRouterStatus.Send(cn.srv.hub, cn.c, map[string]any{
			"routerId": id, "connected": false, "reason": err.Error()})
		return
	}
	// ── THE ALERT POOL MUST LET GO OF A ROUTER A SESSION HAS TAKEN ────────
	//
	// `syncFleetHolds` excludes every router with a live `Session`, and until
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
	// BEFORE syncFleetHolds, so the alert pool computes its exclusion set from
	// `Summaries()` after this router has left the overview pool.
	if cn.srv.pool != nil {
		cn.srv.pool.Drop(id)
	}
	// ── PHASE 5.1: TAKE THE GRAPH HISTORY BEFORE THE POOL LETS GO ─────────
	//
	// ── THE GRAPH SEED IS STRUCTURAL NOW, AND THAT IS WHY THERE IS NO CALL ──
	//
	// This used to hand the retiring alert pool's per-second `traffic` and `ping`
	// rings to the Session that displaced it. The two GRAPHS are the one thing
	// `Last()` replay cannot serve: every other card is a reading and replays
	// whole, but a chart is a WINDOW, and a window that starts empty grows from a
	// single point over a minute. The operator's requirement is that no page
	// waits, so the window had to be handed over rather than re-earned.
	//
	// Phase 4.3 removed the hand-over by removing the second session. A router
	// with reporting on is HELD by `session.Manager` for the history reason, so
	// `traffic` and `ping` have been running on THIS session all along; acquiring
	// it as a viewer adds a reference and changes nothing about the rings. There
	// is no other object holding the samples, so there is nothing to copy.
	//
	// A router with reporting OFF has no rings, and had none in the pool either
	// -- `buildCollectors` returned before making any. The behaviour is
	// unchanged; only the mechanism is gone.
	cn.srv.syncFleetHolds()

	// The stacks describe rows on the router being LEFT, and a `.id` from one
	// router addresses something entirely different on another.
	cn.histDropAll()
	cn.setRouter(id, rs)
	cn.srv.hub.Join(cn.c, "router-"+id)
	EvRouterActive.Send(cn.srv.hub, cn.c, map[string]any{"activeId": id})
	session.EvRouterStatus.Send(cn.srv.hub, cn.c, map[string]any{
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
	// called by nothing. Found 2026-08-28 by a Node-era socket diff, since deleted.
	EvCollectionConfig.Send(cn.srv.hub, cn.c, collection.Payload(id, rs.Collection()))
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
	session.EvCollectionStatus.Send(cn.srv.hub, cn.c, map[string]any{
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
		collect.EvIfstatusNames.Send(cn.srv.hub, cn.c, *collect.NamesOf(last))
	}
	if last := rs.System().Last(); last != nil {
		collect.EvSystemUpdate.Send(cn.srv.hub, cn.c, *last)
	}
	if last := rs.Traffic().LastWan(); last != nil {
		collect.EvWanStatus.Send(cn.srv.hub, cn.c, *last)
	}
	if last := rs.DHCPNetworks().Last(); last != nil {
		collect.EvLanWan.Send(cn.srv.hub, cn.c, map[string]any{"ts": last.TS, "wanIp": last.WanIP})
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
	EvRouterSwitched.Send(cn.srv.hub, cn.c, map[string]any{"activeId": id})
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
	EvSettingsPages.Send(cn.srv.hub, cn.c, pageSettingsFor(cfg))
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
	EvAlertsOpen.Send(cn.srv.hub, cn.c, map[string]any{
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

// resumePage re-applies demand and replays this page's last payloads.
//
// Split out of pageFocus so a DASHBOARD CARD can do the same thing without
// joining the page room. A card is the only view some collectors get — the
// Firewall card on a dashboard is, for a viewer who never opens the Firewall
// page, the whole reason that collector should be running — so the wake has to
// be the same one, not an approximation of it.
//
// ── PHASE 4.2b: IT NO LONGER KNOWS WHICH COLLECTORS THE PAGE HAS ───────────
//
// This held 21 `ResumeCollector` calls in a `switch page`, and `pageBlur` held
// 19 matching suspends. That switchboard stated a fact `internal/collect/rooms.go`
// already declares — which collector feeds which room — and stating it twice went
// wrong five times, each one a dashboard card that silently stopped updating.
//
// The caller has already joined the room by the time this runs, in BOTH paths:
// `pageFocus` joins `page-<key>` and `dashCardFocus` joins `dash-card-<key>`. So
// `applyDemand` sees the new occupant and starts whatever it implies, without
// this function knowing anything about collectors.
//
// WHAT IS LEFT HERE IS THE REPLAY, and it is genuinely page-shaped: which
// payloads this page needs to render, in which order, with which caps frame in
// front of them. That cannot come from a room declaration, and it is why this
// switch still exists.
func (cn *conn) resumePage(page string) {
	if cn.rsession == nil {
		return
	}
	cn.srv.applyDemand(cn.rsession, cn.routerID)
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
		// Opening the Dashboard replayed nothing and the cards stayed empty
		// until the next tick, which for netwatch and talkers is up to a minute.
		// The live app sends all three in `sendInitialState`.
		//
		// Found by the initial-state audit, alongside the four
		// router-wide chrome events replayed on the handshake.
		//
		// PHASE 4.2b MADE THE REPLAY MATTER MORE, not less. This paragraph used
		// to say these three "run from CONNECT, not from focus", which was the
		// reason there was nothing to wake and only something to replay. It is
		// no longer true: `netwatch` and `talkers` declare `page-dashboard` and
		// nothing else, so under demand they are suspended until somebody opens
		// this page — and the payload replayed here is the last one from before
		// that suspension.
		if last := cn.rsession.Netwatch().Last(); last != nil {
			collect.EvNetwatchUpdate.Send(cn.srv.hub, cn.c, *last)
		}
		if last := cn.rsession.Talkers().Last(); last != nil {
			collect.EvTalkersUpdate.Send(cn.srv.hub, cn.c, *last)
		}
		if p := cn.rsession.Ping(); p != nil {
			if last := p.Last(); last != nil {
				collect.EvPingUpdate.Send(cn.srv.hub, cn.c, *last)
			}
		}
		// ── ROUTES AND BGP PEERS ────────────────────────────────────────────
		//
		// `routing` is fed to two rooms — the Routing page and this one — and
		// declaring the second is what starts it for a Dashboard viewer. It used
		// to take an explicit `ResumeCollector("routing")` here, added when the
		// cards were built and matched by a guarded suspend in `pageBlur`; both
		// are gone, and the room declaration does the whole job.
		//
		// The replay is still needed, and for the same reason: without it both
		// cards show em dashes until the next tick, which is up to 60s.
		if last := cn.rsession.Routing().Last(); last != nil {
			collect.EvRoutingUpdate.Send(cn.srv.hub, cn.c, *last)
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
				EvPingHistory.Send(cn.srv.hub, cn.c, out)
			}
		}
	case "dns":
		if last := cn.rsession.DNS().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvDnsUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "bridges":
		if last := cn.rsession.Bridges().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvBridgesUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "vlans":
		if last := cn.rsession.Vlans().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvVlansUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "wan":
		if last := cn.rsession.Wan().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvWanUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "packages":
		if last := cn.rsession.Packages().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvPackagesUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "routing":
		if last := cn.rsession.Routing().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvRoutingUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "ppp":
		if last := cn.rsession.PPP().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvPppUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "vpn":
		if last := cn.rsession.VPN().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvVpnUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "users":
		if last := cn.rsession.RosUsers().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvRosusersUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "capsman":
		if last := cn.rsession.Capsman().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvCapsmanUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	// The NetWatch page (#97). The same collector feeds the Dashboard card, so it
	// is often running already; the replay saves an empty table for a poll
	// interval that can be a minute long.
	case "netwatch":
		if last := cn.rsession.Netwatch().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvNetwatchUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	// interfaceStatus is the RATE SOURCE for five other collectors, so a bridges
	// viewer who never opens this page would otherwise see every throughput
	// column go blank. That used to be handled by never gating it at all; it is
	// `keepAliveFor["ifStatus"]` in internal/collect/rooms.go now, which gates it
	// on the four borrowing pages as well as its own. Opening the page still
	// replays the last payload so it is not empty for a whole poll.
	case "interfaces":
		if last := cn.rsession.IfStatus().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvIfstatusUpdate.Send(cn.srv.hub, cn.c, replay)
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
		if last := cn.rsession.Topology().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvTopologyUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	// Page-gated. It USED to own the connection-table read that bandwidth also
	// consumed, which is why opening either page started it; both subscribe to
	// `/ip/firewall/connection/print` separately now and share one channel at the
	// cache, so each holds its own demand and this page starts only this one.
	case "connections":
		if last := cn.rsession.Conns().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			// THE LIVE WIRE FORM, not the whole struct. This replay used to send
			// the full ConnsPayload under conn:update while the collector sent
			// the light one, so the event had two shapes and no single type could
			// describe it. The heavy indexes go in their own two events, exactly
			// as the collector sends them.
			collect.EvConnUpdate.Send(cn.srv.hub, cn.c, collect.LightOf(replay))
			if replay.CountryDests != nil || replay.SourceDests != nil {
				collect.EvConnCountryData.Send(cn.srv.hub, cn.c, collect.CountryData(&replay))
				collect.EvConnSourceData.Send(cn.srv.hub, cn.c, collect.SourceData(&replay))
			}
		}
	// Page-gated, and the gate matters more here than on most: this collector
	// reads a table that can hold thousands of rows, and nothing else needs it.
	case "bandwidth":
		if last := cn.rsession.Bandwidth().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvBandwidthUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "wifi-clients":
		if last := cn.rsession.Wireless().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvWirelessUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "logs":
		if last := cn.rsession.Logs().Last(); last != nil {
			collect.EvLogsHistory.Send(cn.srv.hub, cn.c, last)
		}
	case "wifi-networks":
		if last := cn.rsession.Wifi().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvWifiUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "firewall":
		// BEFORE the replay, so the first payload the page receives already
		// says whether this router does IPv6 and the tab does not flicker in.
		// One read of /ipv6/settings, cached for the connection; ProbeV6 is a
		// no-op on every focus after the first. NOT called from the collector's
		// Start(), which runs for every router at session connect including the
		// many nobody opens this page on.
		cn.rsession.Firewall().ProbeV6()
		if last := cn.rsession.Firewall().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvFirewallUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "queues":
		if last := cn.rsession.Queues().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvQueuesUpdate.Send(cn.srv.hub, cn.c, replay)
		}
	case "dhcp":
		// TWO collectors and two events, and the ORDER matters: lan:overview
		// carries the pool size the gauge divides by, and the leases handler
		// redraws the gauge as its last act. Replaying the leases first would
		// draw a gauge against a pool size of zero until the next tick.
		//
		// `dhcpLeases` emits ROUTER-WIDE and so declares no audience of its own;
		// it is `keepAliveFor["dhcpLeases"]` in internal/collect/rooms.go that
		// makes a viewer on this page demand it.
		// ── NOTHING TO REPLAY MEANS READ, NOT WAIT ────────────────────────
		//
		// The resume `applyDemand` performs is `poll.start()`, which waits out the REMAINDER of the
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
			collect.EvLanOverview.Send(cn.srv.hub, cn.c, replay)
		} else if cn.rsession.CollectorEnabled("dhcpNetworks") {
			cn.rsession.DHCPNetworks().RefreshNow()
		}
		if last := cn.rsession.DHCPLeases().Last(); last != nil {
			replay := *last
			replay.TS = time.Now().UnixMilli()
			collect.EvLeasesList.Send(cn.srv.hub, cn.c, replay)
		} else if cn.rsession.CollectorEnabled("dhcpLeases") {
			cn.rsession.DHCPLeases().RefreshNow()
		}
	// ── Every generated page ─────────────────────────────────────────────────
	//
	// One case for every area, because the collector is one. Without it a
	// viewer who opens an area another viewer is already reading gets NOTHING
	// until a row changes: the collector sends only a payload that differs, and
	// this viewer has never had the one it holds. Found migrating IP Addresses,
	// whose own collector had this replay.
	default:
		if _, ok := areas.ByKey(page); ok {
			if last := cn.rsession.Areas().Last(page); last != nil {
				replay := *last
				replay.TS = time.Now().UnixMilli()
				collect.EvAreaUpdate.Send(cn.srv.hub, cn.c, replay)
			}
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
	cn.srv.hub.Leave(cn.c, "router-"+cn.routerID+"-page-"+page)
	// ── PHASE 4.2b: THE ROOM IS LEFT, AND THAT IS THE WHOLE EVENT ──────────
	//
	// This held a 19-case `switch page` of suspends, seven of them wrapped in
	// `suspendIfNoRoomOccupied` because the collector also fed a dashboard card.
	// Getting that wrapper wrong — or forgetting it when a collector gained a
	// second room — is the defect that bit dhcpNetworks, bandwidth, vpn, firewall
	// and routing, five times in three weeks, each time as a card that silently
	// stopped updating.
	//
	// Leaving the room IS the state change. `applyDemand` then re-asks the only
	// question that ever mattered — is anybody in any room this collector feeds —
	// for every collector at once, so a page can no longer suspend one it does
	// not own, and cannot fail to suspend one it does.
	//
	// THE OCCUPANCY TEST IS GONE FROM HERE TOO. `if hub.Occupants(room) != 0`
	// guarded this switch because a second viewer on the same page must not lose
	// their collectors; `wantsCollector` reads that same occupancy per collector,
	// so the guard is now inside the rule rather than in front of it. Keeping it
	// here would ALSO skip the demand pass for every other collector, which is
	// what makes a blur the right moment to re-ask about all of them.
	cn.srv.applyDemand(cn.rsession, cn.routerID)
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
	collect.EvTrafficHistory.Send(cn.srv.hub, cn.c, cn.rsession.Traffic().Watch(ifName))
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
	collect.EvTrafficHistory.Send(cn.srv.hub, cn.c, tr.Watch(ifName))
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

// `suspendConnsIfIdle` and `suspendIfNoRoomOccupied` used to live here, and both
// were deleted with the switchboard in phase 4.2b.
//
// ── WHAT THEY WERE, AND WHY NOTHING REPLACED THEM ONE FOR ONE ──────────────
//
// `suspendIfNoRoomOccupied(rs, routerID, key, rooms, suspend)` stopped a
// collector only when EVERY room it emits to was empty, after a grace period,
// re-reading the occupancy when the timer fired. It existed because a page blur
// says nothing about whether a DASHBOARD CARD fed by the same collector is still
// being watched — a distinction that cost four separate fixes (dhcpNetworks,
// bandwidth, vpn, firewall) and a fifth for routing.
//
// `suspendConnsIfIdle` was its one specialisation: two pages and a card share the
// connection-table read, so neither page's blur could suspend it alone. That one
// carried the sharpest scar in the file — it listed the two page rooms and NOT
// `dash-card-connections`, so returning to the dashboard from either page
// suspended the collector with a viewer still watching the card, and the audit
// written for exactly this defect class could not see it because it only
// inspected DIRECT suspends.
//
// Every one of those bugs is the same shape: a hand-written answer to "who else
// is watching this collector". `applyDemand` asks `collect.DemandRooms` instead,
// which is derived from the declarations, and asks it about every collector
// rather than the one a page happened to name. The grace period survives as
// `suspendAfterGrace` in demand.go, with the reasoning moved there intact.

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
	// A Tools run in flight is about the router being left, and the page drops
	// its result anyway: stopped here, it stops holding a channel on that router
	// instead of running on to its timeout for nobody.
	cn.stopTool()
	cn.stopSecScan()
	cn.stopApps()
	// ── THE ROOMS GO FIRST, AND THE ORDER IS THE WHOLE POINT ──────────────
	//
	// Both switch call sites already left every room immediately after calling
	// this, so moving the loop in changes nothing for them. What it adds is the
	// path that had no blur at all: A CLOSED TAB SENDS NO page:blur, and until
	// phase 4.2b nothing was listening for one anyway — the switchboard only ran
	// from a frame the browser chose to send.
	//
	// So a viewer who closed their laptop left every collector they had started
	// running until the session's own idle grace expired, up to two minutes
	// later, and that grace exists to keep the CONNECTION warm rather than to
	// gate collectors.
	//
	// Leaving the rooms and THEN asking makes the answer right: this connection
	// is no longer its own audience, so `applyDemand` sees what a second viewer
	// on the same router still occupies, and nothing more. A lone viewer leaving
	// hands every collector to `suspendAfterGrace`; a second viewer still on the
	// Firewall page keeps `firewall` and loses the rest.
	//
	// `hub.Remove` in the disconnect path drops the same rooms a moment later
	// and is then a no-op for them.
	for _, room := range cn.c.Rooms() {
		cn.srv.hub.Leave(cn.c, room)
	}
	cn.srv.applyDemand(cn.rsession, cn.routerID)
	cn.srv.sessions.Release(cn.routerID)
	cn.setRouter("", nil)
	// The assistant's open proposals were raised on the router being left, and
	// must not be approved on the next one (takeAIProposal refuses them too).
	cn.proposeMu.Lock()
	cn.proposals = nil
	cn.proposeMu.Unlock()
	// AND THE POOL RECLAIMS IT. The other half of the exclusion: `Acquire` makes
	// the pool let go, so `Release` has to make it pick the router back up.
	// Without this the last browser closing would leave that router covered by
	// NOTHING — no status, no alerts, no history — which is the very gap the
	// always-on pool exists to close.
	//
	// `Release` is ref-counted, so this runs when the LAST watcher goes; a second
	// browser on the same router keeps the session and the exclusion.
	cn.srv.syncFleetHolds()
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

// sendPooledStatus tells a browser the CURRENT state of every router the
// session manager holds.
//
// ── A TRANSITION IS NOT A STATE, AND THIS IS THE DIFFERENCE ───────────────
//
// `Session.announce` broadcasts on CHANGE, which is right: an unreachable router
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
	if cn.srv.sessions == nil {
		return
	}
	visible := cn.srv.visibleRouters(cn.sess)
	for id, up := range cn.srv.sessions.Status() {
		if visible != nil && !visible[id] {
			continue
		}
		session.EvRouterStatus.Send(cn.srv.hub, cn.c, map[string]any{
			"routerId": id, "connected": up})
	}
}

// sendFleetStatus is the fleet-wide half of `router:status`: one frame per
// socket whose principal may read the router, as broadcastRouterList does for
// the router list and sendPooledStatus for the statuses sent on a select.
//
// NOT `BroadcastAll`, which sent every router's state and last error to every
// signed-in browser, including one whose role grants none of those routers. A
// socket's grants are read here, at send time, so a permission change reaches it
// on its next revalidation.
func (s *Server) sendFleetStatus(frame map[string]any) {
	id, _ := frame["routerId"].(string)
	for _, cn := range s.connections() {
		if visible := s.visibleRouters(cn.scope().sess); visible != nil && !visible[id] {
			continue
		}
		session.EvRouterStatus.Send(s.hub, cn.c, frame)
	}
}
