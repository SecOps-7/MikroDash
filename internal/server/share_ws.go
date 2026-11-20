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
	"strings"
	"sync"
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
	client := hub.NewRoomOnlyClient("share-"+itoa(connSeq.Add(1)), sendQueue)
	sc := &shareConn{srv: s, ws: ws, linkID: l.ID, ip: clientIPOf(r), cancel: cancel, link: *l, slide: -1}
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
		sc.show(l.RouterID, "dashboard")
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
		sc.releaseLocked()
		rs, err := s.sessions.Acquire(routerID)
		if err != nil {
			log.Printf("[share] %s: router %s: %v", sc.linkID, routerID, err)
			return
		}
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
	} else if sc.page != "" && sc.page != page {
		s.hub.Leave(cn.c, "router-"+routerID+"-page-"+sc.page)
		sc.leaveCardsLocked()
	}
	sc.page = page
	s.hub.Join(cn.c, "router-"+routerID+"-page-"+page)
	cn.resumePage(page)
	EvRouterSwitched.Send(s.hub, cn.c, map[string]any{"activeId": routerID})
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
		// A card link focuses its own card only.
		own := ""
		for _, c := range dashcards.All {
			if c.ID == shareCardOf(sc.link) {
				own = c.Room
			}
		}
		if key != own {
			return
		}
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

// reload tells the screen to reload: its link was edited.
func (sc *shareConn) reload() {
	EvShareReload.Send(sc.srv.hub, sc.inner.c, map[string]any{"reload": true})
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
