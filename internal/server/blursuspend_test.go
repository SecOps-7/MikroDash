package server

// suspendIfNoRoomOccupied — the guard the source audit cannot check.
//
// The blur-suspend audit verifies that every multi-room collector is
// suspended THROUGH this helper. It cannot verify that the helper works: a
// version that stopped consulting the hub would pass the audit and freeze every
// dashboard card the moment its page was left. That mutation survived, which is
// why this exists.
//
// ── RE-AIMED 2026-09-01, DELIBERATELY ──────────────────────────────────────
//
// The suspend is now DEFERRED by an idle grace instead of running inline, so a
// page refresh does not stop and restart a collector's stream in the same
// second. The question the table asks is unchanged -- does an occupied room
// prevent the suspend? -- but "was it called" became "was it called by the time
// the grace had passed", and the not-called cases now have to WAIT to be sure
// rather than reading a bool that had no chance to change.
//
// The grace is injected rather than waited out: `Server.idleGrace` exists for
// this, because the shipped value is two minutes.

import (
	"testing"
	"time"

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
			s := &Server{hub: h, idleGrace: 10 * time.Millisecond}
			if c.occupied != "" {
				cl := hub.NewClient("viewer", 4)
				h.Add(cl)
				h.Join(cl, c.occupied)
			}
			// A channel, not a bool: the suspend now runs on a timer goroutine,
			// and a plain variable read from the test goroutine is a data race
			// whichever way the assertion comes out.
			called := make(chan struct{}, 1)
			// A non-nil session is required; the helper only uses it for the nil
			// check, so the zero value is enough to reach the room logic.
			s.suspendIfNoRoomOccupied(&session.Session{}, "r1",
				[]string{"page-vpn", "dash-card-vpn"}, func() { called <- struct{}{} })

			var got bool
			select {
			case <-called:
				got = true
			// Generously past the 10ms grace, so a `false` here means the
			// suspend was PREVENTED rather than merely slow.
			case <-time.After(2 * time.Second):
			}
			if got != c.want {
				t.Errorf("suspend called = %v, want %v", got, c.want)
			}
		})
	}
}

// A nil collector or an empty router id must not panic, and must not suspend.
func TestSuspendIfNoRoomOccupiedRefusesIncompleteInput(t *testing.T) {
	h := hub.New()
	s := &Server{hub: h, idleGrace: 10 * time.Millisecond}
	called := make(chan struct{}, 3)
	fire := func() { called <- struct{}{} }
	s.suspendIfNoRoomOccupied(nil, "r1", []string{"page-vpn"}, fire)
	s.suspendIfNoRoomOccupied(&session.Session{}, "", []string{"page-vpn"}, fire)
	s.suspendIfNoRoomOccupied(&session.Session{}, "r1", []string{"page-vpn"}, nil)
	// Incomplete input must be refused OUTRIGHT, not merely deferred — so this
	// waits out the grace before believing it.
	select {
	case <-called:
		t.Error("a suspend ran on incomplete input")
	case <-time.After(300 * time.Millisecond):
	}
}
