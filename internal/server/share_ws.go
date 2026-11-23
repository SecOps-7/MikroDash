package server

// The share link's socket (/share/ws): a read-only, fixed-subject viewer for
// somebody with no account.
//
// ── NOT A conn, AND NOT IN s.conns ──────────────────────────────────────────
//
// A shareConn is its own type. It never holds a Session, it is not in
// s.conns (code that iterates the signed-in viewers never meets it), and its
// hub client is ROOM-ONLY, so hub-wide broadcasts (router lists, settings,
// permissions, sites) never reach it. It holds an internal `conn` with NO
// session purely to reuse the room, replay and demand machinery
// (resumePage, the traffic watch, leaveRouterRooms); every permission check on
// that conn answers no, so nothing it reaches can widen.
//
// ── WHAT IT ACCEPTS ─────────────────────────────────────────────────────────
//
// Read-only by construction: the only inbound events handled are Dashboard-
// card focus for shareable cards, `share:slide` and `share:warm` (indexes
// into the server's own slide list), `secscan:get` (the last report, on the
// Security Scan page) and keepalive. Every other event is ignored; no write
// handler exists here at all.
//
// ── RE-CHECKED WHILE IT LIVES ───────────────────────────────────────────────
//
// The link is re-checked every 30 seconds (validity, the client address) and
// at its expiry, and a revocation or an edit reaches it at once through
// s.shareLive.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"mikrodash/internal/collect"
	"mikrodash/internal/dashcards"
	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/pages"
	"mikrodash/internal/routers"
	"mikrodash/internal/secscan"
	"mikrodash/internal/session"
	"mikrodash/internal/sharelink"
)

// shareSubprotocol is the WebSocket subprotocol a share view asks for; the
// ticket travels as a second offered value, so it never appears in a URL.
const shareSubprotocol = "mikrodash-share"

// shareRecheck is how often a live share re-checks its link.
var shareRecheck = 30 * time.Second

type shareConn struct {
	srv    *Server
	ws     *websocket.Conn
	linkID string
	viewer string // the ticket's viewer id: share read routes answer only an open socket's
	ip     string
	inner  *conn // no session: see the file header
	cancel context.CancelFunc

	mu        sync.Mutex
	link      db.ShareLink
	slide     int // the current slide of a slideshow
	page      string
	routerID  string
	rs        *session.Session
	warm      string        // the room joined ahead of the next slide, if any
	devStop   chan struct{} // the Devices page's stats timer, while it is shown
	agentStop chan struct{} // the Agent Overview line, while its card is shown (share_agent.go)
	closed    bool
	once      sync.Once

	// view is what the hub's filter reads (see accepts). It is an atomic, not
	// a field under mu, because the hub asks while holding its own lock, and
	// show holds mu while it joins rooms: one lock order each way would be a
	// deadlock.
	view atomic.Pointer[shareView]
}

// shareView is what a share's socket is showing, for the hub's filter.
type shareView struct {
	routerID string
	page     string             // the page shown; "" for a card link
	card     sharelink.CardData // a card link: its events and the fields of each
}

// shareChrome are the router-wide events a share receives: the top-bar data
// every page draws on. Everything else sent router-wide (alert
// acknowledgements, collection settings, a router disabled or switched) is
// for signed-in viewers only.
var shareChrome = map[string]bool{
	"system:update": true, "ifstatus:names": true, "wan:status": true,
	"router:status": true, "stream:health": true, "lan:wan": true,
}

// shareLeasesPages are the pages whose module names devices from the
// router-wide DHCP lease list; no other share receives it.
var shareLeasesPages = map[string]bool{"dashboard": true, "dhcp": true, "connections": true}

// shareControl are the events a card link receives whole: they say which
// router is shown and that the view ended or changed, and carry no data.
var shareControl = map[string]bool{
	"share:end": true, "share:reload": true, "router:active": true, "router:switched": true,
}

// filter is the hub's filter for this share (hub.NewRoomOnlyClient), and
// sees every frame bound for it.
//
// A page or slideshow view: of what is broadcast to the router-wide room,
// only the top-bar data and, for the pages that use it, the lease list.
// Every other room it is in was joined for its subject, and what is sent to
// it directly is the replay of that subject.
//
// A card link: only its card's events, each cut down to the fields its card
// reads (sharelink.CardDataOf, the operator's ruling of 2026-11-20), from
// whatever room or direct send it comes; an event the card does not read is
// not sent at all.
func (sc *shareConn) filter(room, event string, payload any) (any, bool) {
	v := sc.view.Load()
	if v == nil {
		return nil, shareControl[event]
	}
	if v.card != nil {
		if shareControl[event] {
			return nil, true
		}
		paths, ok := v.card[event]
		if !ok {
			return nil, false
		}
		out, err := sharelink.Project(paths, payload)
		if err != nil {
			log.Printf("[share] %s: cannot project %s: %v", sc.linkID, event, err)
			return nil, false
		}
		return out, true
	}
	if room == "router-"+v.routerID {
		return nil, shareChrome[event] || (event == "leases:list" && shareLeasesPages[v.page])
	}
	return nil, true
}

// shareTicketFrom reads the ticket a share view offers as a subprotocol.
func shareTicketFrom(r *http.Request) string {
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			p = strings.TrimSpace(p)
			if p != "" && p != shareSubprotocol {
				return p
			}
		}
	}
	return ""
}

func (s *Server) handleShareWS(w http.ResponseWriter, r *http.Request) {
	l, tk, reason := s.shareFromTicket(shareTicketFrom(r), r)
	if l == nil || !sharelink.Kind(l.Kind).Viewable() {
		s.shareDenied(r, "", reason)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{shareSubprotocol},
		CompressionMode: websocket.CompressionContextTakeover,
		OriginPatterns:  s.originPatterns,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 14)
	ctx, cancel := context.WithCancel(context.Background())
	sc := &shareConn{srv: s, ws: ws, linkID: l.ID, viewer: tk.Viewer, ip: clientIPOf(r), cancel: cancel, link: *l, slide: -1}
	client := hub.NewRoomOnlyClient("share-"+itoa(connSeq.Add(1)), sendQueue, sc.filter)
	sc.inner = &conn{c: client, ws: ws, srv: s, clientIP: sc.ip}
	viewers := l.ViewerCap
	if viewers <= 0 {
		viewers = sharelink.DefaultViewerCap
	}
	if !s.shareLive.add(l.ID, sc, viewers) {
		s.shareDenied(r, l.ID, "viewer-cap")
		_ = ws.Close(4003, "viewer cap")
		cancel()
		return
	}
	s.hub.Add(client)
	go sc.inner.writer(ctx)
	go sc.watch(ctx)

	switch sharelink.Kind(l.Kind) {
	case sharelink.KindPage:
		sc.show(l.RouterID, l.Page)
	case sharelink.KindCard:
		sc.showCard(l.RouterID, shareCardOf(*l))
	case sharelink.KindSlideshow:
		// The shell asks for slide 0 once it is ready.
	}
	sc.read(ctx)
	sc.close()
}

// show moves the viewer to one page on one router: the router-wide room and
// the page's room, the replay a signed-in viewer gets, and the demand that
// starts the page's collectors.
func (sc *shareConn) show(routerID, page string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.closed || !pages.IsShareable(page) {
		return
	}
	s := sc.srv
	cn := sc.inner
	if routerID != sc.routerID {
		if !sc.enterRouterLocked(routerID, &shareView{routerID: routerID, page: page}) {
			return
		}
	} else if sc.page != "" && sc.page != page {
		s.hub.Leave(cn.c, "router-"+routerID+"-page-"+sc.page)
		sc.leaveCardsLocked()
	}
	sc.view.Store(&shareView{routerID: routerID, page: page})
	sc.page = page
	sc.devicesTimerLocked(page == "devices")
	if page != "dashboard" {
		sc.agentLineLocked(false)
	}
	room := "router-" + routerID + "-page-" + page
	if sc.warm != "" && sc.warm != room {
		s.hub.Leave(cn.c, sc.warm)
	}
	sc.warm = ""
	s.hub.Join(cn.c, room)
	cn.resumePage(page)
	EvRouterSwitched.Send(s.hub, cn.c, map[string]any{"activeId": routerID})
}

// showCard shows one Dashboard card and nothing else of the Dashboard: the
// rooms its data arrives in, every frame cut down to its fields by filter.
// See sharelink.CardFeed and sharelink.CardDataOf.
func (sc *shareConn) showCard(routerID, cardID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	feed, ok := sharelink.CardFeedOf(cardID)
	data, hasData := sharelink.CardDataOf(cardID)
	if sc.closed || !ok || !hasData {
		return
	}
	if !sc.enterRouterLocked(routerID, &shareView{routerID: routerID, card: data}) {
		return
	}
	s := sc.srv
	cn := sc.inner
	for _, room := range feed.Rooms {
		s.hub.Join(cn.c, dashCardRoomFor(routerID, room))
	}
	if feed.Dash {
		s.hub.Join(cn.c, "router-"+routerID+"-page-dashboard")
	}
	if feed.AILine {
		sc.agentLineLocked(true)
	}
	s.applyDemand(sc.rs, routerID)
	// The replays a page focus gives, through the filter like everything else.
	for _, room := range feed.Rooms {
		if src := dashCardPage(room); src != "dashboard" {
			cn.resumePage(src)
		}
	}
	if feed.Dash {
		cn.resumePage("dashboard")
	}
	EvRouterSwitched.Send(s.hub, cn.c, map[string]any{"activeId": routerID})
}

// enterRouterLocked moves the viewer to a router: the router-wide room
// (filtered, see accepts) and the top-bar replay a signed-in viewer gets.
func (sc *shareConn) enterRouterLocked(routerID string, v *shareView) bool {
	s := sc.srv
	cn := sc.inner
	sc.releaseLocked()
	rs, err := s.sessions.Acquire(routerID)
	if err != nil {
		log.Printf("[share] %s: router %s: %v", sc.linkID, routerID, err)
		return false
	}
	sc.view.Store(v)
	sc.routerID, sc.rs = routerID, rs
	cn.setRouter(routerID, rs)
	s.hub.Join(cn.c, "router-"+routerID)
	EvRouterActive.Send(s.hub, cn.c, map[string]any{"activeId": routerID})
	session.EvRouterStatus.Send(s.hub, cn.c,
		session.StatusFrame(s.connTrack, routerID, rs.Connected(), rs.LastError()))
	if last := rs.IfStatus().Last(); last != nil {
		collect.EvIfstatusNames.Send(s.hub, cn.c, *collect.NamesOf(last))
	}
	if last := rs.System().Last(); last != nil {
		collect.EvSystemUpdate.Send(s.hub, cn.c, *last)
	}
	if last := rs.Traffic().LastWan(); last != nil {
		collect.EvWanStatus.Send(s.hub, cn.c, *last)
	}
	cn.trafficSelectDefault(defaultIfFor(rs))
	return true
}

// shareCardRoom reports whether a share may focus a Dashboard card's room:
// the card must exist and its page must be shareable.
func shareCardRoom(key string) bool {
	for _, c := range dashcards.All {
		if c.Room == key {
			return sharelink.CardShareable(c.ID)
		}
	}
	return false
}

func (sc *shareConn) focusCard(key string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if key == "agent" {
		// The Agent Overview has no room a share joins and borrows from a page
		// a share never replays (the AI assistant): its line is written for the
		// share (share_agent.go), and only while the Dashboard is shown.
		if !sc.closed && sc.page == "dashboard" && sc.rs != nil && sharelink.CardShareable("dc-card-agent") {
			sc.agentLineLocked(true)
		}
		return
	}
	if sc.closed || sc.page != "dashboard" || sc.rs == nil || !shareCardRoom(key) {
		return
	}
	if sharelink.Kind(sc.link.Kind) == sharelink.KindCard {
		return // a card link's one room was joined by showCard
	}
	src := dashCardPage(key)
	if !pages.IsShareable(src) {
		return
	}
	sc.srv.hub.Join(sc.inner.c, dashCardRoomFor(sc.routerID, key))
	sc.inner.resumePage(src)
}

func shareCardOf(l db.ShareLink) string {
	var o sharelink.Options
	_ = json.Unmarshal([]byte(l.Options), &o)
	return o.Card
}

func (sc *shareConn) leaveCardsLocked() {
	for _, room := range sc.inner.c.Rooms() {
		if strings.Contains(room, "-dash-card-") {
			sc.srv.hub.Leave(sc.inner.c, room)
		}
	}
}

// goSlide shows slide i of a slideshow: an index into the server's own list,
// never a page or router the client names.
func (sc *shareConn) goSlide(i int) {
	sc.mu.Lock()
	l := sc.link
	sc.mu.Unlock()
	if sharelink.Kind(l.Kind) != sharelink.KindSlideshow || i < 0 || i >= len(l.Slides) {
		return
	}
	sl := l.Slides[i]
	if !pages.IsShareable(sl.Page) || !sc.srv.shareRouterOK(sl.RouterID) {
		return
	}
	sc.mu.Lock()
	sc.slide = i
	sc.mu.Unlock()
	sc.show(sl.RouterID, sl.Page)
}

// warmSlide joins slide i's page room ahead of its turn, so its collectors
// are running when it appears: the link's warm-up option. Only when the link
// has warm-up on, only for the slide after the one on screen, only on the
// same router (a second router would need a second session held), and only
// one room ahead; the room is left when another page is shown.
func (sc *shareConn) warmSlide(i int) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	l := sc.link
	if sc.closed || sc.rs == nil || sharelink.Kind(l.Kind) != sharelink.KindSlideshow ||
		len(l.Slides) == 0 || i < 0 || i >= len(l.Slides) || i != (sc.slide+1)%len(l.Slides) {
		return
	}
	var o sharelink.Options
	_ = json.Unmarshal([]byte(l.Options), &o)
	sl := l.Slides[i]
	if o.WarmUpSecs <= 0 || sl.RouterID != sc.routerID || sl.Page == sc.page || !pages.IsShareable(sl.Page) {
		return
	}
	room := "router-" + sc.routerID + "-page-" + sl.Page
	if sc.warm != "" && sc.warm != room {
		sc.srv.hub.Leave(sc.inner.c, sc.warm)
	}
	sc.warm = room
	sc.srv.hub.Join(sc.inner.c, room)
	sc.srv.applyDemand(sc.rs, sc.routerID)
}

type shareInbound struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// read handles the few events a share accepts. Anything else is ignored.
func (sc *shareConn) read(ctx context.Context) {
	for {
		typ, b, err := sc.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var in shareInbound
		if json.Unmarshal(b, &in) != nil {
			continue
		}
		switch in.Event {
		case "dashcard:focus":
			var key string
			if json.Unmarshal(in.Data, &key) == nil {
				sc.focusCard(key)
			}
		case "share:slide":
			var i int
			if json.Unmarshal(in.Data, &i) == nil {
				sc.goSlide(i)
			}
		case "secscan:get":
			sc.secScanGet()
		case "share:warm":
			var i int
			if json.Unmarshal(in.Data, &i) == nil {
				sc.warmSlide(i)
			}
		default:
			// page:focus for the link's own page (already shown), keepalive,
			// and every other event: nothing to do. No write handler exists.
		}
	}
}

// watch re-checks the link while the viewer lives, and ends it at its expiry.
func (sc *shareConn) watch(ctx context.Context) {
	t := time.NewTicker(shareRecheck)
	defer t.Stop()
	for {
		var expiry <-chan time.Time
		sc.mu.Lock()
		if sc.link.ExpiresAt != nil {
			d := time.Until(time.UnixMilli(*sc.link.ExpiresAt))
			if d < 0 {
				d = 0
			}
			expiry = time.After(d)
		}
		sc.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-expiry:
			sc.end()
			return
		case <-t.C:
			l, _ := sc.srv.shareLinkCheck(sc.linkID, sc.ip)
			if l == nil {
				sc.end()
				return
			}
			sc.mu.Lock()
			sc.link = *l
			sc.mu.Unlock()
		}
	}
}

// end tells the screen it stopped and closes the socket. The screen shows one
// sentence whatever the reason, so a viewer learns nothing about why.
func (sc *shareConn) end() {
	EvShareEnd.Send(sc.srv.hub, sc.inner.c, map[string]any{"ended": true})
	time.AfterFunc(200*time.Millisecond, func() {
		_ = sc.ws.Close(4001, "ended")
		sc.cancel()
	})
}

// reload tells the screen to reload, and closes the socket: its link was
// edited, and the rooms this socket is in were chosen for the old link. The
// screen reconnects with a ticket and is shown the edited link from scratch;
// a client that ignores the event keeps nothing of the old subject.
func (sc *shareConn) reload() {
	EvShareReload.Send(sc.srv.hub, sc.inner.c, map[string]any{"reload": true})
	time.AfterFunc(200*time.Millisecond, func() {
		_ = sc.ws.Close(4002, "edited")
		sc.cancel()
	})
}

func (sc *shareConn) releaseLocked() {
	if sc.rs == nil {
		return
	}
	s := sc.srv
	cn := sc.inner
	cn.dropTraffic()
	cn.leaveRouterRooms()
	s.hub.Leave(cn.c, "router-"+sc.routerID)
	s.applyDemand(sc.rs, sc.routerID)
	s.sessions.Release(sc.routerID)
	cn.setRouter("", nil)
	sc.devicesTimerLocked(false)
	sc.agentLineLocked(false)
	sc.rs, sc.routerID, sc.page, sc.warm = nil, "", "", ""
}

func (sc *shareConn) close() {
	sc.once.Do(func() {
		sc.mu.Lock()
		sc.closed = true
		sc.releaseLocked()
		sc.mu.Unlock()
		sc.srv.shareLive.remove(sc.linkID, sc)
		sc.srv.hub.Remove(sc.inner.c)
		sc.cancel()
		_ = sc.ws.Close(websocket.StatusNormalClosure, "")
	})
}

// devicesTimerLocked runs the Devices page's two-second stats frame while a
// share shows that page, and stops it otherwise. A signed-in viewer gets the
// frame from its own timer (conn.sendRoutersStats); a share gets its own,
// cut down (shareStatsRows).
//
// ── EVERY DEVICE, AND KEPT CURRENT (the operator, 2026-11-23) ───────────────
//
// A shared Devices page showed only the link's router. It lists the whole fleet
// now, so while it is open the share counts as somebody watching the Devices
// page: it joins `devicesWatchers`, and `syncFleetHolds` runs every router's
// Devices collectors exactly as for a signed-in viewer. It leaves the set when
// the page goes, and the hold goes with the last watcher.
func (sc *shareConn) devicesTimerLocked(on bool) {
	s := sc.srv
	if !on {
		if sc.devStop != nil {
			close(sc.devStop)
			sc.devStop = nil
			s.devicesMu.Lock()
			_, had := s.devicesWatchers[sc.inner.c]
			delete(s.devicesWatchers, sc.inner.c)
			last := had && len(s.devicesWatchers) == 0
			s.devicesMu.Unlock()
			if last {
				go s.syncFleetHolds()
			}
		}
		return
	}
	if sc.devStop != nil {
		return
	}
	stop := make(chan struct{})
	sc.devStop = stop
	s.devicesMu.Lock()
	s.devicesWatchers[sc.inner.c] = true
	s.devicesMu.Unlock()
	geo := sc.devicesMapLocked()
	go func() {
		s.syncFleetHolds()
		if s.sessions != nil {
			s.sessions.PrimeStats()
		}
	}()
	send := func() {
		sc.mu.Lock()
		rid := sc.routerID
		sc.mu.Unlock()
		if rid == "" {
			return
		}
		EvRoutersStats.Send(sc.srv.hub, sc.inner.c, shareStatsRows(sc.srv, rid, geo))
	}
	go func() {
		send()
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				send()
			}
		}
	}()
}

// devicesMapLocked is whether this link shows the Devices page as a map, the
// one view that needs each device's location.
func (sc *shareConn) devicesMapLocked() bool {
	var o sharelink.Options
	_ = json.Unmarshal([]byte(sc.link.Options), &o)
	return sharelink.Kind(sc.link.Kind) == sharelink.KindPage && o.DevicesView == "map"
}

// shareStatsRows is the Devices page's rows for a share: every device in the
// fleet (TestASharedDevicesPageShowsEveryDevice).
//
// THE ROWS ARE CUT DOWN for a share. A signed-in viewer sees where a router is
// reached and how MikroDash signs in to it; a share link can reach anyone, so
// the management address, the public address, the serial, the last error
// (which names addresses and reasons), the login profile and the sites are
// never sent. The location is sent only to a link whose view is the map, the
// view that cannot be drawn without it; the administrator chose that view
// knowing it shows where each device is. The card keeps its name, state,
// readings, model, version and alert count.
func shareStatsRows(s *Server, routerID string, geo bool) []routers.Row {
	src := s.buildStatsSources(nil, routerID)
	src.Visible = nil
	rows := routers.BuildStats(src)
	for i := range rows {
		r := &rows[i]
		r.Host, r.PublicIP, r.Serial, r.LastError, r.LoginProfile = "", nil, nil, nil, nil
		r.SiteIDs, r.SiteNames, r.SiteID, r.SiteName = []string{}, []string{}, nil, nil
		if !geo {
			r.Geo = nil
		}
	}
	return rows
}

// secScanGet answers the Security Scan page's `secscan:get` for a share: the
// shared router's last report, and only while that page is on screen. A
// share can read a report; it can never start a scan (no `secscan:run`
// handler exists here).
func (sc *shareConn) secScanGet() {
	sc.mu.Lock()
	rid, page, live := sc.routerID, sc.page, sc.rs != nil
	sc.mu.Unlock()
	if page != "security-scan" {
		return
	}
	out := SecScanPayload{RouterID: rid, Total: len(secscan.Menus())}
	if !live {
		out.Code = "unavailable"
	} else {
		e, ok, running := sc.srv.secScans.get(rid)
		if ok {
			out.Report, out.ScannedAt = e.report, e.at.UnixMilli()
		}
		out.Running = running
	}
	EvSecScanResult.Send(sc.srv.hub, sc.inner.c, out)
}
