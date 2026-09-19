package collect

import (
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/roscache"
)

// TestASuspendedIfStatusHoldsNoRateChannel. Suspend ended the subscription and
// left ifStatus's holder on /interface/monitor-traffic in place (Stop releases
// it; Suspend did not), so a collector nobody was watching kept a router channel
// open, which is the cost this design exists to avoid (review loop).
func TestASuspendedIfStatusHoldsNoRateChannel(t *testing.T) {
	r := &wdReader{conn: true}
	s := NewIfStatus(r, hub.Relay{}, "r1", 5000)
	s.UseCache(roscache.New(r))

	s.syncRateChannel([]string{"ether1"})
	r.mu.Lock()
	opens := r.opens
	r.mu.Unlock()
	if opens != 1 {
		t.Fatalf("%d channels opened for one interface, want 1: the control", opens)
	}

	s.Suspend()
	r.mu.Lock()
	open := r.opens - r.stops
	r.mu.Unlock()
	if open != 0 {
		t.Errorf("%d rate channel(s) still open while suspended", open)
	}

	// And a resume rejoins once, on the next sync.
	s.Resume()
	s.syncRateChannel([]string{"ether1"})
	r.mu.Lock()
	opens, stops := r.opens, r.stops
	r.mu.Unlock()
	if opens != 2 || opens-stops != 1 {
		t.Errorf("after resume: %d opens, %d stops; want the channel reopened exactly once", opens, stops)
	}
	s.Stop()
}
