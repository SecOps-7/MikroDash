package collect

import (
	"sync"
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// topoRouter answers the two configuration menus Topology reads beside the
// neighbour table, lets them be changed between reads the way Winbox changes
// them, and counts the reads.
type topoRouter struct {
	mu        sync.Mutex
	discovery routeros.Reply
	vlans     []routeros.Reply
	reads     map[string]int
}

func (r *topoRouter) Connected() bool { return true }
func (r *topoRouter) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reads == nil {
		r.reads = map[string]int{}
	}
	r.reads[cmd.Path]++
	switch cmd.Path {
	case topoSettingsCmd.Path:
		return []routeros.Reply{r.discovery}, nil
	case topoVlanCmd.Path:
		return append([]routeros.Reply{}, r.vlans...), nil
	}
	return nil, nil
}

func (r *topoRouter) set(mode string, vlanName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discovery = routeros.Reply{"protocol": "cdp,lldp,mndp", "mode": mode,
		"discover-interface-list": "LAN", "discover-interval": "30s"}
	r.vlans = []routeros.Reply{{"vlan-id": "20", "name": vlanName}}
}

// TestTopologyRereadsItsSettingsOnASlowLane. Discovery settings and VLAN names
// were read once per connection, so a change made on the router never reached
// the map until it reconnected (survey, 2026-10-03). They are configuration, so
// they are re-read on a slow lane, topoConfigEvery, and NOT on every poll: the
// neighbour table is what moves.
func TestTopologyRereadsItsSettingsOnASlowLane(t *testing.T) {
	r := &topoRouter{}
	r.set("tx-and-rx", "guests")
	topo := NewTopology(r, hub.Relay{}, nil, "r1", "Office", 30000)
	clock := time.Unix(1_800_000_000, 0)
	topo.now = func() time.Time { return clock }

	topo.apply(nil, nil)
	if d := topo.Last().Discovery; d == nil || d.Mode != "tx-and-rx" {
		t.Fatalf("the control: the first reading has the settings: %+v", d)
	}

	r.set("rx-only", "visitors")

	// Inside the slow lane: not re-read, so the change is not seen yet.
	clock = clock.Add(30 * time.Second)
	topo.apply(nil, nil)
	if n := r.reads[topoSettingsCmd.Path]; n != 1 {
		t.Errorf("the settings were read %d times inside the slow lane, want 1", n)
	}

	// Past it: the change arrives.
	clock = clock.Add(topoConfigEvery)
	topo.apply(nil, nil)
	if d := topo.Last().Discovery; d == nil || d.Mode != "rx-only" {
		t.Errorf("a discovery change on the router never reached the map: %+v", d)
	}
	topo.mu.Lock()
	name := topo.vlanNames[20]
	topo.mu.Unlock()
	if name != "visitors" {
		t.Errorf("a renamed VLAN is still %q", name)
	}

	if topoConfigEvery < 30*time.Second {
		t.Errorf("topoConfigEvery is %s: configuration belongs on a lane of 30s or longer", topoConfigEvery)
	}
}
