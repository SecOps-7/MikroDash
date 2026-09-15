package collect

import (
	"mikrodash/internal/hub"
	"testing"

	"mikrodash/internal/routeros"
)

// stubARP is an ARPByIP and an ARPByMAC without a collector behind it, which is
// the point of declaring those as capabilities rather than as *ARP.
type stubARP struct {
	byIP  map[string]string
	byMAC map[string]string
}

func (s stubARP) MACForIP(ip string) (string, string) { return s.byIP[ip], "" }
func (s stubARP) IPForMAC(mac string) string          { return s.byMAC[mac] }

func TestBuildARPIndexesBothWays(t *testing.T) {
	ix := BuildARP([]routeros.Reply{
		{"address": "10.0.0.5", "mac-address": "30:9C:23:B4:12:9A", "interface": "Home"},
		{"address": "172.16.0.10", "mac-address": "94:54:C5:28:59:C7", "interface": "IoT"},
	})
	if got := ix.ByIP["10.0.0.5"].MAC; got != "30:9C:23:B4:12:9A" {
		t.Errorf("ByIP[10.0.0.5].MAC = %q", got)
	}
	if got := ix.ByIP["10.0.0.5"].Iface; got != "Home" {
		t.Errorf("the interface was dropped: %q", got)
	}
	if got := ix.ByMAC["94:54:C5:28:59:C7"].IP; got != "172.16.0.10" {
		t.Errorf("ByMAC[…].IP = %q", got)
	}
}

// TestBuildARPNeedsBothHalves.
//
// RouterOS emits a row for a resolution that FAILED — the live fleet had exactly
// one, at 172.16.0.251 with no mac-address at all. Indexing it would put an
// empty MAC in ByMAC, where it would answer for every lookup of a nameless
// device; and a row with a MAC and no address answers a lookup with "".
func TestBuildARPNeedsBothHalves(t *testing.T) {
	ix := BuildARP([]routeros.Reply{
		{"address": "172.16.0.251"},                  // failed resolution
		{"mac-address": "AA:BB:CC:DD:EE:FF"},         // no address
		{"address": "10.0.0.1", "mac-address": "  "}, // whitespace is not a MAC
		{"address": "10.0.0.2", "mac-address": "A1:B2:C3:D4:E5:F6"},
	})
	if len(ix.ByIP) != 1 || len(ix.ByMAC) != 1 {
		t.Errorf("indexed %d by IP and %d by MAC, want 1 and 1 — a half row was kept",
			len(ix.ByIP), len(ix.ByMAC))
	}
	if _, bad := ix.ByMAC[""]; bad {
		t.Error("an empty MAC is a key in ByMAC; it would answer every nameless lookup")
	}
}

// TestBuildARPFallsBackToActiveAddress — the live `_applyEntry` reads
// `address || active-address`, because a published or DHCP-sourced row can carry
// only the second.
func TestBuildARPFallsBackToActiveAddress(t *testing.T) {
	ix := BuildARP([]routeros.Reply{
		{"active-address": "10.0.0.9", "mac-address": "11:22:33:44:55:66"},
	})
	if got := ix.ByMAC["11:22:33:44:55:66"].IP; got != "10.0.0.9" {
		t.Errorf("active-address was not used: %q", got)
	}
}

// TestBuildARPNormalisesMACCase.
//
// RouterOS answers upper here, but the lease table's `active-mac-address` has
// been seen both ways — and a case-sensitive map is a join that silently finds
// nothing, which looks exactly like a device that is not on the network.
func TestBuildARPNormalisesMACCase(t *testing.T) {
	ix := BuildARP([]routeros.Reply{
		{"address": "10.0.0.5", "mac-address": "aa:bb:cc:dd:ee:ff"},
	})
	if _, ok := ix.ByMAC["AA:BB:CC:DD:EE:FF"]; !ok {
		t.Fatalf("a lower-case row is not reachable by its upper-case MAC: %v", ix.ByMAC)
	}
	a := &ARP{}
	a.last = ix
	if got := a.IPForMAC("aa:bb:cc:DD:ee:FF"); got != "10.0.0.5" {
		t.Errorf("IPForMAC is case-sensitive: %q", got)
	}
}

// TestBuildARPLastRowWins. Two addresses on one MAC is ordinary — a device that
// moved subnet — and the later row is the current one, which is what the live
// Map's assignment did.
func TestBuildARPLastRowWins(t *testing.T) {
	ix := BuildARP([]routeros.Reply{
		{"address": "10.0.0.5", "mac-address": "AA:BB:CC:DD:EE:FF"},
		{"address": "172.16.0.5", "mac-address": "AA:BB:CC:DD:EE:FF"},
	})
	if got := ix.ByMAC["AA:BB:CC:DD:EE:FF"].IP; got != "172.16.0.5" {
		t.Errorf("ByMAC kept the earlier address: %q", got)
	}
}

// TestARPLookupsAreNilSafe — before the first read there is no index, and every
// consumer holds this as an optional capability.
func TestARPLookupsAreNilSafe(t *testing.T) {
	var a *ARP = &ARP{}
	if mac, iface := a.MACForIP("10.0.0.5"); mac != "" || iface != "" {
		t.Errorf("an unread collector answered %q/%q", mac, iface)
	}
	if ip := a.IPForMAC("AA:BB:CC:DD:EE:FF"); ip != "" {
		t.Errorf("an unread collector answered %q", ip)
	}
}

// ── THE FOUR CONSUMER CHAINS ───────────────────────────────────────────────
//
// These are what the collector exists for, and each one was measurably broken
// before it: on the live fleet 26 of 26 WiFi clients had no address, and
// `topology`'s ARP fallback was dead code behind a nil check.

func leasePayload(rows ...Lease) *LeasesPayload { return &LeasesPayload{Leases: rows} }

// TestConnectionsNameOfWalksAllThreeSteps.
//
// The middle step is the one that was missing: a device using an address its
// lease is not keyed by — a static assignment, a lease that moved — falls all
// the way through to a bare IP without ARP.
func TestConnectionsNameOfWalksAllThreeSteps(t *testing.T) {
	leases := leasePayload(
		Lease{IP: "10.0.0.5", Name: "Desktop", MAC: "30:9C:23:B4:12:9A"},
		Lease{IP: "10.0.0.99", HostName: "Moved", MAC: "AA:BB:CC:DD:EE:FF"},
	)
	arp := stubARP{byIP: map[string]string{
		"10.0.0.7": "AA:BB:CC:DD:EE:FF", // lease exists, keyed by a DIFFERENT ip
		"10.0.0.8": "11:22:33:44:55:66", // in ARP, no lease at all
	}}
	c := &Connections{leases: stubLeases{leases}, arp: arp}

	for _, tc := range []struct{ why, ip, name, mac string }{
		{"step 1: the lease keyed by this address", "10.0.0.5", "Desktop", "30:9C:23:B4:12:9A"},
		{"step 2: ARP finds the MAC, the lease names it", "10.0.0.7", "Moved", "AA:BB:CC:DD:EE:FF"},
		{"step 3: no lease, but the MAC is worth having", "10.0.0.8", "", "11:22:33:44:55:66"},
		{"not in ARP either", "10.0.0.200", "", ""},
	} {
		name, mac := c.nameOf(tc.ip)
		if name != tc.name || mac != tc.mac {
			t.Errorf("%s: nameOf(%s) = %q/%q, want %q/%q",
				tc.why, tc.ip, name, mac, tc.name, tc.mac)
		}
	}
}

// TestConnectionsNameOfIsCaseInsensitiveOnTheMACJoin. RouterOS answers the ARP
// table upper and the lease table has been seen both ways; a case-sensitive
// join finds nothing and looks exactly like a device with no lease.
func TestConnectionsNameOfMatchesALowerCaseLease(t *testing.T) {
	c := &Connections{
		leases: stubLeases{leasePayload(Lease{IP: "10.0.0.99", Name: "Moved", MAC: "aa:bb:cc:dd:ee:ff"})},
		arp:    stubARP{byIP: map[string]string{"10.0.0.7": "AA:BB:CC:DD:EE:FF"}},
	}
	if name, _ := c.nameOf("10.0.0.7"); name != "Moved" {
		t.Errorf("nameOf = %q; the MAC join is case-sensitive", name)
	}
}

// TestBandwidthNameOfWalksTheSameChain, with the one difference the live pair
// has: where Connections' caller substitutes the address for an empty name,
// this page renders the row without one.
func TestBandwidthNameOfWalksTheSameChain(t *testing.T) {
	b := &Bandwidth{
		leases: stubLeases{leasePayload(Lease{IP: "10.0.0.99", Name: "Moved", MAC: "AA:BB:CC:DD:EE:FF"})},
		arp:    stubARP{byIP: map[string]string{"10.0.0.7": "AA:BB:CC:DD:EE:FF"}},
	}
	name, mac := b.nameOf("10.0.0.7")
	if name != "Moved" || mac != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("nameOf = %q/%q, want Moved/AA:BB:CC:DD:EE:FF", name, mac)
	}
}

// TestNameOfSurvivesAMissingARP — every consumer holds this as an optional
// capability, and a router with the collector disabled must lose the column
// rather than panic.
func TestNameOfSurvivesAMissingARP(t *testing.T) {
	c := &Connections{leases: stubLeases{leasePayload()}}
	if name, mac := c.nameOf("10.0.0.7"); name != "" || mac != "" {
		t.Errorf("with no ARP: %q/%q", name, mac)
	}
	b := &Bandwidth{}
	if name, mac := b.nameOf("10.0.0.7"); name != "" || mac != "" {
		t.Errorf("with no leases and no ARP: %q/%q", name, mac)
	}
}

// TestWirelessClientGetsItsAddressFromARP.
//
// ── DRIVEN THROUGH A TICK, BECAUSE THE HELPER IS NOT THE BUG ───────────────
//
// A registration row carries a MAC and never an address. `parseWirelessClient`
// was passed a literal `""` from the port until 2026-09-10, so the WiFi Clients
// page's address line — rendered only `if (c.ip)` — never drew once.
//
// A first version of this test called `w.ipOf(mac)` directly and PASSED against
// a collector with the call site reverted to `""`. That is the same defect the
// whole change is about: a lookup that works and nothing calling it. So the tick
// is driven and the assertion is on the PAYLOAD.
func TestWirelessClientGetsItsAddressFromARP(t *testing.T) {
	ros := fakeReader{rows: map[string][]routeros.Reply{
		"/interface/wifi/registration-table/print": {
			{"mac-address": "AA:BB:CC:DD:EE:FF", "signal": "-52", "interface": "wifi1", "ssid": "Home"},
		},
		"/interface/wifi/print": {
			{"name": "wifi1", "configuration.ssid": "Home", "disabled": "false", "running": "true"},
		},
	}}
	c := NewWireless(ros, hub.Relay{}, nil, 30000).
		WithARP(stubARP{byMAC: map[string]string{"AA:BB:CC:DD:EE:FF": "10.0.0.69"}})
	c.Tick()

	got := c.Last()
	if got == nil || len(got.Clients) != 1 {
		t.Fatalf("no client in the payload: %+v", got)
	}
	if got.Clients[0].IP != "10.0.0.69" {
		t.Errorf("client IP = %q, want 10.0.0.69 — the registration row has no address, "+
			"so an empty one here means the ARP join is not being called at all",
			got.Clients[0].IP)
	}
}

// TestWirelessSurvivesAMissingARP — the capability is optional, and a router
// with the collector disabled must lose the column rather than panic.
func TestWirelessSurvivesAMissingARP(t *testing.T) {
	ros := fakeReader{rows: map[string][]routeros.Reply{
		"/interface/wifi/registration-table/print": {
			{"mac-address": "AA:BB:CC:DD:EE:FF", "signal": "-52", "interface": "wifi1"},
		},
	}}
	c := NewWireless(ros, hub.Relay{}, nil, 30000)
	c.Tick()
	if got := c.Last(); got == nil || len(got.Clients) != 1 || got.Clients[0].IP != "" {
		t.Errorf("with no ARP: %+v", got)
	}
}

// arpTopoReader answers the neighbour table and nothing else, which is all this
// case needs: an MNDP row with a MAC and no address of any kind.
type arpTopoReader struct{ rows []routeros.Reply }

func (a *arpTopoReader) Connected() bool { return true }
func (a *arpTopoReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	if cmd.Path == topoNeighborCmd.Path {
		return a.rows, nil
	}
	return nil, nil
}

// TestTopologyFillsTheARPIPSeam.
//
// ── THE SEAM WAS DECLARED AND NEVER FILLED, WHICH IS THE POINT ─────────────
//
// `TopoInput.ARPIP` has existed since the port and is used at two sites, both
// behind `if in.ARPIP != nil`, and nothing ever set it — so a neighbour whose
// own row carries no address had no way to be pinged, and so no status.
//
// A first version of this test built `TopoInput` BY HAND with `ARPIP: tp.arpIP`
// and passed against a collector that never sets it. Testing the derivation
// proves the derivation; only a driven tick proves the wiring, which was the
// thing that was missing for the whole life of the port.
func TestTopologyFillsTheARPIPSeam(t *testing.T) {
	r := &arpTopoReader{rows: []routeros.Reply{
		// No `address`, no `address4` — an MNDP row from a device with no L3
		// configuration on this segment, which is the case the fallback is for.
		{"mac-address": "AA:BB:CC:DD:EE:FF", "identity": "cAP", "interface": "Home"},
	}}
	c := NewTopology(r, hub.Relay{}, nil, "r1", "lab", 30000).
		WithARP(stubARP{byMAC: map[string]string{"AA:BB:CC:DD:EE:FF": "10.0.0.4"}})
	c.Tick()

	got := c.Last()
	if got == nil {
		t.Fatal("no payload")
	}
	// `Nodes` is `[]any` because the map carries several node kinds.
	var found bool
	for _, node := range got.Nodes {
		n, ok := node.(*TopoNeighbor)
		if !ok || n.MAC != "AA:BB:CC:DD:EE:FF" {
			continue
		}
		found = true
		if n.IP != "10.0.0.4" {
			t.Errorf("an address-less neighbour got IP %q; without one there is "+
				"nothing to ping, so it has no status", n.IP)
		}
	}
	if !found {
		t.Fatalf("the neighbour is not in the payload at all (%d nodes)", len(got.Nodes))
	}
}
