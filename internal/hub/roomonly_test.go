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

// TestARoomOnlyClientsFilterSeesEveryRoomBroadcast: the filter decides each of
// the three room broadcasts, and never a direct send.
func TestARoomOnlyClientsFilterSeesEveryRoomBroadcast(t *testing.T) {
	h := New()
	viewer := NewRoomOnlyClient("share-1", 16, func(room, event string) bool {
		return room != "router-r1" || event == "system:update"
	})
	user := NewClient("ws-1", 16)
	for _, c := range []*Client{viewer, user} {
		h.Add(c)
		h.Join(c, "router-r1")
	}
	h.broadcast("router-r1", "alerts:acked", 1)
	h.broadcastExcept("router-r1", nil, "collection:config", 1)
	h.broadcastRooms([]string{"router-r1"}, "router:disabled", 1)
	if len(viewer.Send) != 0 {
		t.Fatalf("the filter let %d refused frames through", len(viewer.Send))
	}
	if len(user.Send) != 3 {
		t.Fatalf("control: the ordinary client received %d of 3", len(user.Send))
	}
	h.broadcast("router-r1", "system:update", 1)
	h.send(viewer, "router:status", 1)
	if len(viewer.Send) != 2 {
		t.Errorf("an accepted broadcast and a direct send: %d of 2 arrived", len(viewer.Send))
	}
}
