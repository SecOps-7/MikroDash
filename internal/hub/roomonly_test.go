package hub

import "testing"

// TestARoomOnlyClientNeverHearsABroadcast: a share link's viewer receives what
// its rooms carry and nothing sent to the whole hub.
func TestARoomOnlyClientNeverHearsABroadcast(t *testing.T) {
	h := New()
	viewer := NewRoomOnlyClient("share-1", 8)
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
