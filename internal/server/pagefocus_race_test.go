package server

import (
	"slices"
	"testing"

	"mikrodash/internal/hub"
)

// `page:focus` arriving before `router:select`.
//
// ── TWO FRAMES FROM ONE BOOTSTRAP, IN NO GUARANTEED ORDER ──────────────────
//
// The browser emits both while it starts up. `pageFocus` needs `routerID`, and
// used to return in SILENCE when it was not set yet: no room joined, no
// collectors woken, and nothing kept to try again with. `selectRouter` then
// leaves every room and joins only its own, so the page room stayed unjoined
// for the life of that socket — the page's payloads went to a room this browser
// was never in.
//
// The client cannot recover: its `router:active` handler skips the FIRST event
// on the stated grounds that "the room has already been joined by the code that
// opened the page", which is precisely what did not happen.
//
// Reported twice. Once as "sometimes when I sign in, some of the cards on the
// dashboard dont have any data" — which produced the 2026-08-30 change that
// made `selectRouter`'s three refusals log, and left this fourth silent return
// in the sibling handler. And again on 2026-09-04: router present, dot green,
// every card stale, for two hours. The server log showed a clean select, a
// connected session and collectors emitting the whole time.
//
// `dashCardFocus` has had the fix since 2026-08-29 — record first, replay from
// `selectRouter` — and this is that, for pages.

// focusConn is the smallest connection that can answer the question: a hub, a
// client, and a session that permits everything.
func focusConn(t *testing.T) (*conn, *hub.Client, *hub.Hub) {
	t.Helper()
	h := hub.New()
	cl := hub.NewClient("ws-test", 8)
	h.Add(cl)
	return &conn{
		c:    cl,
		srv:  &Server{hub: h},
		sess: &Session{AuthMode: "none", Username: "admin"},
	}, cl, h
}

func inRoom(cl *hub.Client, room string) bool {
	return slices.Contains(cl.Rooms(), room)
}

// TestAPageFocusBeforeAnyRouterIsReplayed is the bug.
func TestAPageFocusBeforeAnyRouterIsReplayed(t *testing.T) {
	cn, cl, _ := focusConn(t)

	// The frame that lost the race. Nothing can be joined yet — there is no
	// router to name a room after.
	cn.pageFocus("dashboard")
	if got := cl.Rooms(); len(got) != 0 {
		t.Fatalf("joined %v with no router selected", got)
	}

	// `router:select` completes and replays what was remembered.
	cn.routerID = "r1"
	cn.rejoinPage()

	if !inRoom(cl, "router-r1-page-dashboard") {
		t.Errorf("after the select the page room is still unjoined (rooms: %v).\n"+
			"Every payload for that page goes to a room this browser is not in, "+
			"and nothing retries: the cards stay empty and go stale while the "+
			"server logs a healthy session.", cl.Rooms())
	}
}

// TestTheOrdinaryOrderStillJoinsImmediately — the other direction. Most
// bootstraps win the race, and the fix must not make the common path depend on
// the replay.
func TestTheOrdinaryOrderStillJoinsImmediately(t *testing.T) {
	cn, cl, _ := focusConn(t)
	cn.routerID = "r1"

	cn.pageFocus("dashboard")
	if !inRoom(cl, "router-r1-page-dashboard") {
		t.Errorf("a focus arriving AFTER the select did not join: %v", cl.Rooms())
	}
}

// TestARouterSwitchRejoinsThePageAgainstTheNewRouter. The rooms are per router,
// so the replay is not only about the startup race — it is what carries the
// operator's current page across a switch.
func TestARouterSwitchRejoinsThePageAgainstTheNewRouter(t *testing.T) {
	cn, cl, h := focusConn(t)
	cn.routerID = "r1"
	cn.pageFocus("wireless")
	if !inRoom(cl, "router-r1-page-wireless") {
		t.Fatal("the first join did not happen")
	}

	// What `selectRouter` does on a switch: drop every room, then rejoin.
	for _, room := range cl.Rooms() {
		h.Leave(cl, room)
	}
	cn.routerID = "r2"
	cn.rejoinPage()

	if !inRoom(cl, "router-r2-page-wireless") {
		t.Errorf("the page was not rejoined against the new router: %v", cl.Rooms())
	}
	if inRoom(cl, "router-r1-page-wireless") {
		t.Errorf("still in the OLD router's page room: %v", cl.Rooms())
	}
}

// TestABlurIsNotReplayed. A viewer who left a page must not have it re-woken by
// the next select — that would resume collectors for a page nobody is on.
func TestABlurIsNotReplayed(t *testing.T) {
	cn, cl, _ := focusConn(t)
	cn.routerID = "r1"
	cn.pageFocus("wireless")
	cn.pageBlur("wireless")

	cn.routerID = "r2"
	cn.rejoinPage()
	if inRoom(cl, "router-r2-page-wireless") {
		t.Errorf("a page the viewer had left was rejoined: %v", cl.Rooms())
	}
}

// TestABlurForAnotherPageLeavesTheHeldOneAlone — the pages a browser leaves as
// it navigates must not clear the one it has arrived at, or the replay would
// depend on the order two frames happen to arrive in. Which is this bug.
func TestABlurForAnotherPageLeavesTheHeldOneAlone(t *testing.T) {
	cn, cl, h := focusConn(t)
	cn.routerID = "r1"
	cn.pageFocus("dashboard")
	cn.pageBlur("wireless") // some other page's blur, arriving late

	for _, room := range cl.Rooms() {
		h.Leave(cl, room)
	}
	cn.rejoinPage()
	if !inRoom(cl, "router-r1-page-dashboard") {
		t.Errorf("an unrelated blur cleared the held page: %v", cl.Rooms())
	}
}
