package server

import (
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/routers"
	"mikrodash/internal/session"
)

// The device modal's live data: one router, streamed to one socket, without
// changing the router that socket has selected.
//
// ── DEMAND, NOT A NEW HOLD ─────────────────────────────────────────────────
//
// The modal joins that router's `collect.DevicePeekRoom`, and `applyDemand`
// does the rest, exactly as it does for a page: the room keeps `ifStatus` and
// `dhcpLeases` running while anybody occupies it, the hub's occupancy is the
// count across every viewer, and leaving hands both to `suspendAfterGrace`.
// `system` and the default interface's traffic stream run on every connected
// session already. So a peek adds no session, no connection and no hold - only
// a name in a room on the session every enabled router already has, which is
// why opening and closing the modal cannot write a connectivity row.
//
// ── ON THE LOOP ────────────────────────────────────────────────────────────
//
// `peekID` and `peekSince` are read and written only on the connection's loop:
// the dispatch runs there, the ticker posts there, and teardown calls `unpeek`
// after the loop has drained.

// peekRefresh is the modal's cadence: the WAN stream's own one second.
const peekRefresh = time.Second

// mayPeek is the overview endpoint's gate: the Devices page, and this router
// among the ones the viewer may see there.
func (cn *conn) mayPeek(id string) bool {
	sess := cn.sess
	if sess == nil || id == "" {
		return false
	}
	if sess.AuthMode != "none" && !sessionHasPage(sess, "devices") {
		return false
	}
	visible := cn.srv.visibleRouters(sess)
	return visible == nil || visible[id]
}

// peek starts streaming one router to this socket. A second peek moves it.
func (cn *conn) peek(id string) {
	if !cn.mayPeek(id) {
		return
	}
	if cn.peekID == id {
		return
	}
	cn.unpeek()
	cn.peekID = id
	cn.peekSince = 0
	cn.srv.hub.Join(cn.c, session.RoomFor(id, collect.DevicePeekRoom))
	if rs := cn.peekSession(); rs != nil {
		cn.srv.applyDemand(rs, id)
	}
	stop := make(chan struct{})
	cn.peekStop = stop
	cn.sendLive()
	go func() {
		t := time.NewTicker(peekRefresh)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				cn.post(cn.sendLive)
			}
		}
	}()
}

// unpeek is the mirror. Idempotent: the modal closing, the Devices page
// blurring, a new peek and teardown all call it, and a closed tab sends none of
// the browser's own.
func (cn *conn) unpeek() {
	id := cn.peekID
	if id == "" {
		return
	}
	close(cn.peekStop)
	cn.peekStop = nil
	cn.peekID = ""
	cn.srv.hub.Leave(cn.c, session.RoomFor(id, collect.DevicePeekRoom))
	// LEAVE, THEN ASK, as `releaseRouter` does: this socket is no longer its
	// own audience, so demand sees only the other viewers.
	if rs := cn.srv.liveSession(id); rs != nil {
		cn.srv.applyDemand(rs, id)
	}
}

// peekSession is the session of the router being peeked, or nil when it has
// none (disabled, or not yet dialled).
func (cn *conn) peekSession() *session.Session { return cn.srv.liveSession(cn.peekID) }

func (s *Server) liveSession(id string) *session.Session {
	if s.sessions == nil || id == "" {
		return nil
	}
	return s.sessions.Live()[id]
}

// sendLive sends one frame, and re-checks the gate first: a grant revoked while
// the modal is open stops the stream at the next tick.
func (cn *conn) sendLive() {
	id := cn.peekID
	if id == "" {
		return
	}
	if !cn.mayPeek(id) {
		cn.unpeek()
		return
	}
	in := routers.LiveInput{RouterID: id, SinceTS: cn.peekSince}
	if rs := cn.peekSession(); rs != nil {
		in.Connected = rs.Connected()
		in.System = rs.SystemOrPrimed()
		if tr := rs.Traffic(); tr != nil {
			in.WanIf = tr.DefaultIf()
			in.Wan = tr.History(in.WanIf).Points
		}
		if c := rs.IfStatus(); c != nil {
			in.Ifaces = c.Last()
		}
		if c := rs.DHCPLeases(); c != nil {
			in.Leases = c.Last()
		}
	}
	live := routers.BuildLive(in)
	if n := len(live.Points); n > 0 {
		cn.peekSince = live.Points[n-1].TS
	}
	EvDeviceLive.Send(cn.srv.hub, cn.c, live)
}
