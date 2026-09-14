package session

import (
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/hub"
)

// A STATUS GOES TO THE ROUTER'S ROOM, AND FLEET-WIDE ONLY THROUGH THE HOOK.
//
// `announce` used to `BroadcastAll`, which reached every signed-in browser
// whatever its role. The hub cannot tell who a client is, so the fleet-wide half
// is now the server's filtered send; with none attached, nothing but the room
// hears the status.
func TestAnnounceSendsFleetWideOnlyThroughTheHook(t *testing.T) {
	h := hub.New()
	inRoom := hub.NewClient("in-room", 4)
	outside := hub.NewClient("outside", 4)
	h.Add(inRoom)
	h.Add(outside)
	h.Join(inRoom, "router-r1")

	var hook atomic.Pointer[func(frame map[string]any)]
	s := &Session{RouterID: "r1", h: h, fleet: &hook}

	s.announce()
	select {
	case <-inRoom.Send:
	case <-time.After(time.Second):
		t.Fatal("the router's own room received no status")
	}
	select {
	case raw := <-outside.Send:
		t.Fatalf("a client outside the room received %s with no fleet send attached: the status "+
			"still goes to everybody", raw)
	case <-time.After(100 * time.Millisecond):
	}

	var got map[string]any
	fn := func(frame map[string]any) { got = frame }
	hook.Store(&fn)
	s.announce()
	if got == nil || got["routerId"] != "r1" {
		t.Fatalf("the fleet send received %v, want the frame for r1", got)
	}
	select {
	case raw := <-outside.Send:
		t.Errorf("with a fleet send attached, the hub still delivered %s to a client outside the "+
			"room: the audience is the server's to decide", raw)
	case <-time.After(100 * time.Millisecond):
	}
}
