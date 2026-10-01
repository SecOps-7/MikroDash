package routers

import (
	"testing"

	"mikrodash/internal/collect"
)

// NOTHING READ IS NULL, NOT ZERO, and the ports say "not read" apart from "none".
func TestALiveFrameWithNothingReadClaimsNothing(t *testing.T) {
	l := BuildLive(LiveInput{RouterID: "r1"})
	if l.CPU != nil || l.MemPct != nil || l.HddPct != nil || l.Uptime != nil || l.Leases != nil {
		t.Errorf("a frame with no readings carries numbers: %+v", l)
	}
	if l.PortsRead || l.Ports == nil || len(l.Ports) != 0 {
		t.Errorf("ports before the interface reading = %v (read %v), want [] and unread", l.Ports, l.PortsRead)
	}
	if l.Points == nil {
		t.Error("points is a null array; Go never sends one")
	}
	none := BuildLive(LiveInput{Ifaces: &collect.IfStatusPayload{Interfaces: []collect.Interface{
		{Name: "bridge", Type: "bridge"}}}})
	if !none.PortsRead || len(none.Ports) != 0 {
		t.Errorf("a router with no physical ports = %v (read %v), want [] and read", none.Ports, none.PortsRead)
	}
}

// THE FIRST FRAME CARRIES THE RING'S TAIL; LATER ONES ONLY WHAT IS NEW.
func TestLivePointsAreTheTailThenTheDelta(t *testing.T) {
	ring := make([]collect.TrafficPoint, LiveRingPoints+50)
	for i := range ring {
		ring[i].TS = int64(i + 1)
	}
	first := BuildLive(LiveInput{Wan: ring})
	if len(first.Points) != LiveRingPoints || first.Points[0].TS != 51 {
		t.Fatalf("first frame: %d points from ts %d, want %d from 51",
			len(first.Points), first.Points[0].TS, LiveRingPoints)
	}
	next := BuildLive(LiveInput{Wan: ring, SinceTS: int64(len(ring) - 2)})
	if len(next.Points) != 2 {
		t.Errorf("a later frame resent %d points, want the 2 newer than SinceTS", len(next.Points))
	}
}

// ONLY PHYSICAL PORTS, with the card's filter.
func TestLivePortsArePhysicalOnly(t *testing.T) {
	l := BuildLive(LiveInput{Ifaces: &collect.IfStatusPayload{Interfaces: []collect.Interface{
		{Name: "ether1", Type: "ether", Running: true},
		{Name: "sfp1", Type: "sfp-sfpplus"},
		{Name: "bridge", Type: "bridge", Running: true},
		{Name: "wlan1", Type: "wlan"},
	}}, Leases: &collect.LeasesPayload{Leases: make([]collect.Lease, 3)}})
	if len(l.Ports) != 2 || l.Ports[0].Name != "ether1" || !l.Ports[0].Running || l.Ports[1].Name != "sfp1" {
		t.Errorf("ports = %+v, want ether1 (up) and sfp1", l.Ports)
	}
}

// CLIENTS ARE ACTIVE LEASES ONLY, ordered by address, and only for a viewer
// who may read DHCP - and the list rides a frame only when asked to.
func TestLiveClientsAreTheBoundLeases(t *testing.T) {
	leases := &collect.LeasesPayload{Leases: []collect.Lease{
		{IP: "198.51.100.20", MAC: "02:00:00:00:00:02", HostName: "b", Status: "bound", VlanID: "10"},
		{IP: "198.51.100.3", MAC: "02:00:00:00:00:01", Name: "a-by-name", Status: "bound"},
		{IP: "198.51.100.9", Status: "waiting"},
		{IP: "198.51.100.10", Status: "bound", Disabled: true},
	}}
	l := BuildLive(LiveInput{Leases: leases, ClientsAllowed: true, SendClients: true})
	if l.Leases == nil || *l.Leases != 2 {
		t.Errorf("client count = %v, want the 2 bound, enabled leases", l.Leases)
	}
	if !l.ClientsSent || len(l.Clients) != 2 || l.Clients[0].IP != "198.51.100.3" ||
		l.Clients[0].HostName != "a-by-name" || l.Clients[1].VlanID != "10" {
		t.Errorf("clients = %+v, want .3 (named from Name) then .20 on VLAN 10, by address not text", l.Clients)
	}
	quiet := BuildLive(LiveInput{Leases: leases, ClientsAllowed: true})
	if quiet.ClientsSent || len(quiet.Clients) != 0 {
		t.Error("a frame not asked to carry the list carried it")
	}
	denied := BuildLive(LiveInput{Leases: leases, SendClients: true})
	if denied.ClientsSent || denied.Leases != nil || denied.ClientsAllowed {
		t.Errorf("a viewer without DHCP read was sent lease data: %+v", denied)
	}
}
