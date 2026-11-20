package hub

import "testing"

// TestARoomOnlyClientNeverHearsABroadcast: a share link's viewer receives what
// its rooms carry and nothing sent to the whole hub.
func TestARoomOnlyClientNeverHearsABroadcast(t *testing.T) {
	h := New()
	viewer := NewRoomOnlyClient("share-1", 8, nil)
	user := NewClient("ws-1", 8)
	h.Add(viewer)
	h.Add(user)
	h.Join(viewer, "router-r1-page-dns")
	h.broadcastAll("routers:update", []string{"r1", "r2"})
	if len(viewer.Send) != 0 {
		t.Fatal("a room-only client received a hub-wide broadcast")
	}
	if len(user.Send) != 1 {
		t.Fatal("control: an ordinary client did not receive the broadcast")
	}
	h.broadcast("router-r1-page-dns", "dns:update", map[string]int{"n": 1})
	if len(viewer.Send) != 1 {
		t.Error("a room-only client did not receive its room's event")
	}
}

// TestARoomOnlyClientsFilterSeesEveryFrame: the filter decides each of the
// three room broadcasts and a direct send, and what it returns is what is
// sent; an ordinary client in the same room gets the payload untouched.
func TestARoomOnlyClientsFilterSeesEveryFrame(t *testing.T) {
	h := New()
	var seen []string
	viewer := NewRoomOnlyClient("share-1", 16, func(room, event string, payload any) (any, bool) {
		seen = append(seen, room+" "+event)
		if event == "system:update" {
			return map[string]int{"cut": 1}, true
		}
		return nil, room == ""
	})
	user := NewClient("ws-1", 16)
	for _, c := range []*Client{viewer, user} {
		h.Add(c)
		h.Join(c, "router-r1")
	}
	h.broadcast("router-r1", "alerts:acked", map[string]int{"secret": 1})
	h.broadcastExcept("router-r1", nil, "collection:config", 1)
	h.broadcastRooms([]string{"router-r1"}, "router:disabled", 1)
	if len(viewer.Send) != 0 {
		t.Fatalf("the filter let %d refused frames through", len(viewer.Send))
	}
	if len(user.Send) != 3 {
		t.Fatalf("control: the ordinary client received %d of 3", len(user.Send))
	}
	h.broadcast("router-r1", "system:update", map[string]int{"cpu": 5, "secret": 9})
	h.send(viewer, "router:status", 1)
	if len(viewer.Send) != 2 {
		t.Fatalf("an accepted broadcast and a direct send: %d of 2 arrived", len(viewer.Send))
	}
	if got := string(<-viewer.Send); got != `{"event":"system:update","data":{"cut":1}}` {
		t.Errorf("the filter's replacement was not what was sent: %s", got)
	}
	<-user.Send
	<-user.Send
	<-user.Send
	if got := string(<-user.Send); got != `{"event":"system:update","data":{"cpu":5,"secret":9}}` {
		t.Errorf("an ordinary client's payload was changed: %s", got)
	}
	if len(seen) != 5 || seen[4] != " router:status" {
		t.Errorf("the filter did not see every frame, with its room: %v", seen)
	}
}
