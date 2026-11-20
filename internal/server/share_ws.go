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
// card focus for shareable cards, `share:slide` (an index into the server's
// own slide list) and keepalive. Every other event is ignored; no write
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
	"slices"
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
	ip     string
	inner  *conn // no session: see the file header
	cancel context.CancelFunc

	mu       sync.Mutex
	link     db.ShareLink
	slide    int // the current slide of a slideshow
	page     string
	routerID string
	rs       *session.Session
	closed   bool
	once     sync.Once

	// view is what the hub's filter reads (see accepts). It is an atomic, not
	// a field under mu, because the hub asks while holding its own lock, and
	// show holds mu while it joins rooms: one lock order each way would be a
	// deadlock.
	view atomic.Pointer[shareView]
}

// shareView is what a share's socket is showing, for the hub's filter.
type shareView struct {
	routerID   string
	page       string   // the page shown; "" for a card link
	card       bool     // a card link
	dashEvents []string // a card link: the events it takes from the Dashboard's room
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

// accepts is the hub's filter for this share (hub.NewRoomOnlyClient): of
// what is broadcast to the router-wide room, only the top-bar data and, for
// the pages that use it, the lease list; and a card link takes from the
// Dashboard's page room only its own card's events. Every other room it is
// in was joined for its subject.
func (sc *shareConn) accepts(room, event string) bool {
	v := sc.view.Load()
	if v == nil {
		return false
	}
	wide := "router-" + v.routerID
	switch room {
	case wide:
		return shareChrome[event] || (event == "leases:list" && !v.card && shareLeasesPages[v.page])
	case wide + "-page-dashboard":
		return !v.card || slices.Contains(v.dashEvents, event)
	}
	return true
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
	l, reason := s.shareFromTicket(shareTicketFrom(r), r)
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
	sc := &shareConn{srv: s, ws: ws, linkID: l.ID, ip: clientIPOf(r), cancel: cancel, link: *l, slide: -1}
	client := hub.NewRoomOnlyClient("share-"+itoa(connSeq.Add(1)), sendQueue, sc.accepts)
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
	s.hub.Join(cn.c, "router-"+routerID+"-page-"+page)
	cn.resumePage(page)
	EvRouterSwitched.Send(s.hub, cn.c, map[string]any{"activeId": routerID})
}

// showCard shows one Dashboard card and nothing else of the Dashboard: the
// card's own room, or only its own events from the Dashboard's room, and the
// top-bar data. See sharelink.CardFeed.
func (sc *shareConn) showCard(routerID, cardID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	feed, ok := sharelink.CardFeedOf(cardID)
	if sc.closed || !ok {
		return
	}
	v := &shareView{routerID: routerID, card: true, dashEvents: feed.DashEvents}
	if !sc.enterRouterLocked(routerID, v) {
		return
	}
	s := sc.srv
	cn := sc.inner
	if feed.Room != "" {
		s.hub.Join(cn.c, dashCardRoomFor(routerID, feed.Room))
	}
	if len(feed.DashEvents) > 0 {
		s.hub.Join(cn.c, "router-"+routerID+"-page-dashboard")
	}
	// Demand follows the rooms just joined. The replay is the card's own
	// payload only, never the Dashboard's whole set.
	s.applyDemand(sc.rs, routerID)
	if feed.Room != "" {
		if src := dashCardPage(feed.Room); src != "dashboard" {
			cn.resumePage(src)
		}
	}
	for _, ev := range feed.DashEvents {
		sc.replayDashEventLocked(ev)
	}
	EvRouterSwitched.Send(s.hub, cn.c, map[string]any{"activeId": routerID})
}

// replayDashEventLocked sends the last payload of one Dashboard-room event.
func (sc *shareConn) replayDashEventLocked(event string) {
	s, rs, c := sc.srv, sc.rs, sc.inner.c
	switch event {
	case "netwatch:update":
		if last := rs.Netwatch().Last(); last != nil {
			collect.EvNetwatchUpdate.Send(s.hub, c, *last)
		}
	case "talkers:update":
		if last := rs.Talkers().Last(); last != nil {
			collect.EvTalkersUpdate.Send(s.hub, c, *last)
		}
	case "ping:update":
		if p := rs.Ping(); p != nil {
			if last := p.Last(); last != nil {
				collect.EvPingUpdate.Send(s.hub, c, *last)
			}
		}
	}
	// routing:update has no cached payload to replay; it arrives on its poll.
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
	sc.rs, sc.routerID, sc.page = nil, "", ""
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
