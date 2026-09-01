package server

// suspendIfNoRoomOccupied — the guard the source audit cannot check.
//
// The blur-suspend audit verifies that every multi-room collector is
// suspended THROUGH this helper. It cannot verify that the helper works: a
// version that stopped consulting the hub would pass the audit and freeze every
// dashboard card the moment its page was left. That mutation survived, which is
// why this exists.

import (
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/session"
)

func TestSuspendIfNoRoomOccupied(t *testing.T) {
	for _, c := range []struct {
		why      string
		occupied string // a room to put a client in, "" for none
		want     bool   // was suspend called?
	}{
		{"no room is occupied", "", true},
		{"the page room still has a viewer", "router-r1-page-vpn", false},
		{"the DASHBOARD CARD room has a viewer", "router-r1-dash-card-vpn", false},
		{"an unrelated room has a viewer", "router-r1-page-dns", true},
	} {
		c := c
		t.Run(c.why, func(t *testing.T) {
			h := hub.New()
			s := &Server{hub: h}
			if c.occupied != "" {
				cl := hub.NewClient("viewer", 4)
				h.Add(cl)
				h.Join(cl, c.occupied)
			}
			called := false
			// A non-nil session is required; the helper only uses it for the nil
			// check, so the zero value is enough to reach the room logic.
			s.suspendIfNoRoomOccupied(&session.Session{}, "r1",
				[]string{"page-vpn", "dash-card-vpn"}, func() { called = true })
			if called != c.want {
				t.Errorf("suspend called = %v, want %v", called, c.want)
			}
		})
	}
}

// A nil collector or an empty router id must not panic, and must not suspend.
func TestSuspendIfNoRoomOccupiedRefusesIncompleteInput(t *testing.T) {
	h := hub.New()
	s := &Server{hub: h}
	called := false
	s.suspendIfNoRoomOccupied(nil, "r1", []string{"page-vpn"}, func() { called = true })
	s.suspendIfNoRoomOccupied(&session.Session{}, "", []string{"page-vpn"}, func() { called = true })
	s.suspendIfNoRoomOccupied(&session.Session{}, "r1", []string{"page-vpn"}, nil)
	if called {
		t.Error("a suspend ran on incomplete input")
	}
}
