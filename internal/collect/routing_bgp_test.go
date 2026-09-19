package collect

import (
	"errors"
	"sync"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// noBGPReader is a router with routes and no BGP sessions, counting every menu read.
type noBGPReader struct {
	mu     sync.Mutex
	byMenu map[string]int
}

func (r *noBGPReader) Connected() bool { return true }
func (r *noBGPReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.mu.Lock()
	r.byMenu[cmd.Path]++
	r.mu.Unlock()
	if cmd.Path == "/routing/bgp/peer/print" {
		return nil, errors.New("no such command prefix")
	}
	return nil, nil
}

// TestNoBGPSessionsIsOneCommandATick. An empty session table sent a second,
// legacy `/routing/bgp/peer/print` on every tick. RouterOS v7 has no such menu
// (7.1.1 onwards; this app is v7-only), so it was a refused command per tick
// per router, on hardware whose limit is API channels (review loop).
func TestNoBGPSessionsIsOneCommandATick(t *testing.T) {
	r := &noBGPReader{byMenu: map[string]int{}}
	c := NewRouting(r, hub.Relay{}, 10000)
	for i := 0; i < 5; i++ {
		c.Tick()
	}
	if n := r.byMenu["/routing/bgp/peer/print"]; n != 0 {
		t.Errorf("the v6 peer menu was asked %d times", n)
	}
	if n := r.byMenu["/routing/bgp/session/print"]; n != 5 {
		t.Errorf("the session menu was read %d times over 5 ticks, want 5: the control", n)
	}
}

// TestATransientBGPErrorKeepsThePeers. derive discarded its error, so a timeout
// read as "no sessions" and the Routing page's peers vanished for a tick.
func TestATransientBGPErrorKeepsThePeers(t *testing.T) {
	c := NewRouting(&noBGPReader{byMenu: map[string]int{}}, hub.Relay{}, 10000)
	c.BGPOnly()
	if p, _ := c.derive([]routeros.Reply{{"name": "peer1", "established": "true"}}, nil, false); p == nil {
		t.Fatal("a good read produced no payload: the control")
	}
	if p, _ := c.derive(nil, errors.New("routeros: i/o timeout"), false); p != nil {
		t.Errorf("a failed read produced a payload (%d peers), replacing the last good one", len(p.Peers))
	}
}
