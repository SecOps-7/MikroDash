package server

import (
	"encoding/json"
	"strings"
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/hub"
	"mikrodash/internal/session"
)

func peekConn(sess *Session) (*conn, *hub.Hub, *hub.Client) {
	h := hub.New()
	me := hub.NewClient("me", 64)
	h.Add(me)
	return &conn{srv: &Server{hub: h}, c: me, sess: sess}, h, me
}

func peekRoom(id string) string { return session.RoomFor(id, collect.DevicePeekRoom) }

// drainLive returns the router ids of every device:live frame queued so far.
func drainLive(t *testing.T, c *hub.Client) []string {
	t.Helper()
	var ids []string
	for {
		select {
		case b := <-c.Send:
			var f struct {
				Event string `json:"event"`
				Data  struct {
					RouterID string `json:"routerId"`
				} `json:"data"`
			}
			if json.Unmarshal(b, &f) == nil && f.Event == "device:live" {
				ids = append(ids, f.Data.RouterID)
			}
		default:
			return ids
		}
	}
}

// A PEEK IS A ROOM, AND THE ROOM IS THE COUNT. Opening joins the router's
// device-peek room and sends a frame at once; a second peek MOVES it; closing
// leaves; every close is idempotent.
func TestAPeekJoinsMovesAndLeavesItsRoom(t *testing.T) {
	cn, h, me := peekConn(&Session{AuthMode: "none"})
	cn.peek("r1")
	if h.Occupants(peekRoom("r1")) != 1 {
		t.Fatal("a peek did not join the router's device-peek room")
	}
	if got := drainLive(t, me); len(got) != 1 || got[0] != "r1" {
		t.Errorf("a peek sent %v, want one frame for r1 straight away", got)
	}
	cn.peek("r2")
	if h.Occupants(peekRoom("r1")) != 0 || h.Occupants(peekRoom("r2")) != 1 {
		t.Error("a second peek did not move the room from r1 to r2")
	}
	cn.unpeek()
	cn.unpeek()
	if h.Occupants(peekRoom("r2")) != 0 || cn.peekID != "" {
		t.Error("unpeek left the room or the id behind")
	}
}

// A VIEWER WITHOUT THE DEVICES PAGE CANNOT PEEK. The modal's live data is the
// Devices page's, and the gate is the overview endpoint's.
func TestAPeekNeedsTheDevicesPage(t *testing.T) {
	cn, h, me := peekConn(&Session{AuthMode: "password", Pages: map[string]string{"dashboard": "read"}})
	cn.peek("r1")
	if h.Occupants(peekRoom("r1")) != 0 || len(drainLive(t, me)) != 0 {
		t.Error("a viewer without the Devices page was given a peek")
	}
	// THE CONTROL: the same viewer with the page gets one.
	cn.sess = &Session{AuthMode: "password", Pages: map[string]string{"devices": "read"}}
	cn.peek("r1")
	if h.Occupants(peekRoom("r1")) != 1 {
		t.Error("control: a viewer with the Devices page could not peek")
	}
}

// SWITCHING ROUTER KEEPS THE PEEK. `releaseRouter` leaves every room the
// selected router put this socket in, and the device modal's room is not one of
// them: the modal is about a router the socket has NOT selected.
func TestLeavingTheRouterKeepsThePeek(t *testing.T) {
	cn, h, _ := peekConn(&Session{AuthMode: "none"})
	page := "router-r9-page-dashboard"
	h.Join(cn.c, page)
	cn.peek("r1")
	cn.leaveRouterRooms()
	if h.Occupants(page) != 0 {
		t.Error("the selected router's page room survived leaving it")
	}
	if h.Occupants(peekRoom("r1")) != 1 {
		t.Error("leaving the selected router dropped the device modal's room")
	}
	// Teardown unpeeks before it releases, so nothing is left behind there.
	cn.unpeek()
	cn.leaveRouterRooms()
	if rooms := cn.c.Rooms(); len(rooms) != 0 {
		t.Errorf("rooms left after unpeek and release: %v", rooms)
	}
}

// THE ROOM IS DEMAND FOR THE TWO COLLECTORS A WARM SESSION DOES NOT RUN, and
// for nothing else.
func TestThePeekRoomKeepsThePortsAndLeasesRunning(t *testing.T) {
	for _, key := range []string{"ifStatus", "dhcpLeases"} {
		if !strings.Contains(strings.Join(collect.DemandRooms(key), ","), collect.DevicePeekRoom) {
			t.Errorf("%s does not stay alive for the device modal", key)
		}
	}
	if strings.Contains(strings.Join(collect.DemandRooms("firewall"), ","), collect.DevicePeekRoom) {
		t.Error("control: the device modal keeps the firewall collector running")
	}
}
