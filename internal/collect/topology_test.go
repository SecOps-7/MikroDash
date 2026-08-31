package collect

import "testing"

import "mikrodash/internal/routeros"

// Device classification, against the rows real hardware sent.
//
// The comments in the original name the devices these came from, and the cases
// below keep that: a Meraki switch reports plain "bridge", a MikroTik router
// reports "bridge,router", and every MNDP-discovered MikroTik reports NOTHING
// at all — which is why the board fallback is the common path and not an edge
// case.
func TestClassifyDevice(t *testing.T) {
	cases := []struct {
		name string
		row  routeros.Reply
		typ  string
		src  string
	}{
		{"a Meraki switch, bridging only",
			routeros.Reply{"system-caps-enabled": "bridge"}, "switch", "caps"},
		{"a MikroTik router over LLDP: both caps set, board breaks the tie",
			routeros.Reply{"system-caps-enabled": "bridge,router", "board": "RB5009UG+S+"}, "router", "board"},
		{"both caps set and no board to break the tie",
			routeros.Reply{"system-caps-enabled": "bridge,router"}, "router", "caps"},
		{"a CRS supports routing and enables only bridging",
			routeros.Reply{"system-caps": "bridge,router", "system-caps-enabled": "bridge",
				"board": "CRS326-24G-2S+"}, "switch", "caps"},
		{"an access point by capability",
			routeros.Reply{"system-caps-enabled": "wlan-access-point"}, "ap", "caps"},
		{"MNDP sends no caps at all — the board decides",
			routeros.Reply{"board": "cAP ax"}, "ap", "board"},
		{"hAP is a ROUTER with a radio, not an access point",
			routeros.Reply{"board": "hAP ax^3"}, "router", "board"},
		{"a CRS by board",
			routeros.Reply{"board": "CRS310-1G-5S-4S+"}, "switch", "board"},
		{"a non-MikroTik platform is something else",
			routeros.Reply{"platform": "Cisco"}, "other", "platform"},
		{"platform MikroTik with no board at all",
			routeros.Reply{"platform": "MikroTik"}, "router", "platform"},
		{"nothing to go on", routeros.Reply{}, "unknown", "unknown"},
		{"supported caps are used when nothing is enabled",
			routeros.Reply{"system-caps": "wlan"}, "ap", "caps"},
	}
	for _, c := range cases {
		got := classifyDevice(c.row)
		if got.Type != c.typ || got.Source != c.src {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, got.Type, got.Source, c.typ, c.src)
		}
	}
}

func TestParseAgeSec(t *testing.T) {
	cases := []struct {
		in   string
		want *int
	}{
		{"", nil},
		{"5", intpTopo(5)}, // the neighbour table sends bare seconds
		{"5s", intpTopo(5)},
		{"1m20s", intpTopo(80)},
		{"2h", intpTopo(7200)},
		{"1w2d3h4m5s", intpTopo(788645)},
		{"nonsense", nil},
	}
	for _, c := range cases {
		got := parseAgeSec(c.in)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("parseAgeSec(%q) = %v, want %v", c.in, deref(got), deref(c.want))
		}
	}
}

// The microsecond case is the whole reason this parser exists: treating "413us"
// as milliseconds turns a 0.4 ms LAN hop into 413 ms, which reads as a broken
// link on a page whose job is to show which links are broken.
func TestParseRttMs(t *testing.T) {
	cases := []struct {
		in   string
		want *float64
	}{
		{"", nil},
		{"413us", f64p(0.413)},
		{"1.2ms", f64p(1.2)},
		{"2s", f64p(2000)},
		{"0.9", f64p(0.9)}, // no unit: already milliseconds
	}
	for _, c := range cases {
		got := parseRttMs(c.in)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("parseRttMs(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTopoMacPrefixAndIPv4(t *testing.T) {
	if got := topoMacPrefix("02:1a:cb:73:0b:2c"); got != "02:1A:CB:73:0B" {
		t.Errorf("topoMacPrefix = %q", got)
	}
	// Short input: this one keeps what it was given, where capsman's returns "".
	if got := topoMacPrefix("AA:BB"); got != "AA:BB" {
		t.Errorf("topoMacPrefix short = %q, want AA:BB", got)
	}
	if got := macPrefix("AA:BB"); got != "" {
		t.Errorf("capsman macPrefix short = %q, want empty", got)
	}
	for _, ok := range []string{"10.0.0.1", "255.255.255.255", "0.0.0.0"} {
		if !isIPv4(ok) {
			t.Errorf("isIPv4(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "10.0.0", "256.0.0.1", "2001:db8::1", "10.0.0.1/24"} {
		if isIPv4(bad) {
			t.Errorf("isIPv4(%q) = true", bad)
		}
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func f64p(f float64) *float64 { return &f }

// The LLDP parent rule, including the two branches the captured corpus cannot
// reach.
//
// A mutation that accepted the FIRST LLDP device on a port instead of requiring
// EXACTLY ONE passed the differential gate, because no port on this fleet has
// two LLDP neighbours. That is a gap in the corpus rather than in the rule, and
// synthetic rows are the honest way to close it: the rule is what stops the map
// inventing a hierarchy when something is forwarding LLDP that should not be.
func TestResolveParentsLLDPRule(t *testing.T) {
	row := func(mac, via string) routeros.Reply {
		return routeros.Reply{"mac-address": mac, "discovered-by": via,
			"interface": "ether1", "identity": mac}
	}
	// Both devices are on one physical port, per the bridge host table.
	hosts := []hostEntry{
		{MAC: "AA:AA:AA:AA:AA:01", Port: "ether5"},
		{MAC: "AA:AA:AA:AA:AA:02", Port: "ether5"},
		{MAC: "AA:AA:AA:AA:AA:03", Port: "ether5"},
	}

	parentOf := func(rows []routeros.Reply, key string) string {
		p := BuildTopology(TopoInput{Rows: rows, Hosts: hosts, ShowClients: false})
		for _, n := range p.Nodes {
			if nb, ok := n.(*TopoNeighbor); ok && nb.Key == key {
				if nb.Parent == nil {
					return ""
				}
				return *nb.Parent
			}
		}
		t.Fatalf("no node %s", key)
		return ""
	}

	// ONE LLDP device: it is the direct neighbour, the other is behind it.
	one := []routeros.Reply{row("AA:AA:AA:AA:AA:01", "lldp"), row("AA:AA:AA:AA:AA:02", "mndp")}
	if got := parentOf(one, "AA:AA:AA:AA:AA:02"); got != "AA:AA:AA:AA:AA:01" {
		t.Errorf("one LLDP device: parent = %q, want the LLDP device", got)
	}
	if got := parentOf(one, "AA:AA:AA:AA:AA:01"); got != "" {
		t.Errorf("the LLDP device itself got a parent: %q", got)
	}

	// TWO LLDP devices on one port: something is forwarding LLDP that should
	// not be, and there is no way to tell which is attached. Both stay flat.
	two := []routeros.Reply{row("AA:AA:AA:AA:AA:01", "lldp"), row("AA:AA:AA:AA:AA:02", "lldp"),
		row("AA:AA:AA:AA:AA:03", "mndp")}
	for _, k := range []string{"AA:AA:AA:AA:AA:01", "AA:AA:AA:AA:AA:02", "AA:AA:AA:AA:AA:03"} {
		if got := parentOf(two, k); got != "" {
			t.Errorf("two LLDP devices: %s got parent %q, want none", k, got)
		}
	}

	// NO LLDP device: an unmanaged switch is invisible by definition, so nothing
	// can be attributed to it and both stay on the core.
	none := []routeros.Reply{row("AA:AA:AA:AA:AA:01", "mndp"), row("AA:AA:AA:AA:AA:02", "cdp")}
	for _, k := range []string{"AA:AA:AA:AA:AA:01", "AA:AA:AA:AA:AA:02"} {
		if got := parentOf(none, k); got != "" {
			t.Errorf("no LLDP device: %s got parent %q, want none", k, got)
		}
	}

	// And the port they share is flagged, which is what tells the page that the
	// flat layout is a fact about the network rather than a missing edge.
	p := BuildTopology(TopoInput{Rows: none, Hosts: hosts, ShowClients: false})
	for _, e := range p.Edges {
		if e.From == "core" && !e.Shared {
			t.Errorf("edge %s on a shared port is not flagged shared", e.ID)
		}
	}
}

// Retention and ping status — neither reachable from a fixture, because both
// need a SECOND build to mean anything.
//
// A one-shot replay sees every device present and none of them pinged, so the
// golden pins the shape of these fields and says nothing about the behaviour
// behind them. That behaviour is the difference between a map that shows an
// outage and one that quietly forgets the device went away.
func TestRetentionAndPingStatus(t *testing.T) {
	row := func(mac string) routeros.Reply {
		return routeros.Reply{"mac-address": mac, "identity": "sw-" + mac,
			"discovered-by": "lldp", "interface": "ether5", "age": "3"}
	}
	seen := map[string]*TopoSeen{}
	ping := map[string]*TopoPing{}
	in := TopoInput{
		Rows: []routeros.Reply{row("AA:AA:AA:AA:AA:01")},
		Seen: seen, Ping: ping, Now: 1_000_000,
	}

	find := func(p *TopologyPayload, key string) *TopoNeighbor {
		for _, n := range p.Nodes {
			if nb, ok := n.(*TopoNeighbor); ok && nb.Key == key {
				return nb
			}
		}
		return nil
	}

	// Build one: the device is here, its age says it was heard recently.
	first := BuildTopology(in)
	if n := find(first, "AA:AA:AA:AA:AA:01"); n == nil || n.Status != "up" || n.Gone {
		t.Fatalf("first build: %+v", n)
	}

	// Build two, a minute later, with the device GONE from the neighbour table.
	// It must still be on the map, marked down, drawn from its last good shape.
	in.Rows = nil
	in.Now = 1_060_000
	second := BuildTopology(in)
	ghost := find(second, "AA:AA:AA:AA:AA:01")
	if ghost == nil {
		t.Fatal("a device that vanished was dropped instead of retained")
	}
	if !ghost.Gone || ghost.Status != "down" || ghost.AgeSec != nil {
		t.Errorf("retained node is %+v, want gone/down with no age", ghost)
	}
	if ghost.Name != "sw-AA:AA:AA:AA:AA:01" {
		t.Errorf("retained node lost its identity: %q", ghost.Name)
	}

	// Build three, past the retention window: now it really is gone, and its
	// ping history goes with it rather than leaking.
	ping["AA:AA:AA:AA:AA:01"] = &TopoPing{Window: []int{1}}
	in.Now = 1_000_000 + topoRetainMs + 1_000
	third := BuildTopology(in)
	if find(third, "AA:AA:AA:AA:AA:01") != nil {
		t.Error("a device past the retention window is still on the map")
	}
	if _, ok := ping["AA:AA:AA:AA:AA:01"]; ok {
		t.Error("ping history outlived the device it belonged to")
	}
	if _, ok := seen["AA:AA:AA:AA:AA:01"]; ok {
		t.Error("seen history outlived the device it belonged to")
	}
}

// The status ladder, which a ping result outranks an age on.
func TestTopoStatusFor(t *testing.T) {
	up := &TopoNeighbor{AgeSec: intpTopo(3)}
	stale := &TopoNeighbor{AgeSec: intpTopo(120)}
	unknown := &TopoNeighbor{}
	f := func(v float64) *float64 { return &v }

	cases := []struct {
		name string
		n    *TopoNeighbor
		p    *TopoPing
		want string
	}{
		{"gone outranks everything", &TopoNeighbor{Gone: true}, &TopoPing{Window: []int{1}, Loss: f(0)}, "down"},
		{"total loss is down", up, &TopoPing{Window: []int{0}, Loss: f(100)}, "down"},
		{"partial loss warns even when the replies are fast", up,
			&TopoPing{Window: []int{1, 0}, Loss: f(50), RTT: f(1)}, "warn"},
		{"slow but lossless warns", up, &TopoPing{Window: []int{1}, Loss: f(0), RTT: f(150)}, "warn"},
		{"fast and lossless is up", up, &TopoPing{Window: []int{1}, Loss: f(0), RTT: f(2)}, "up"},
		{"an EMPTY window is not a result — fall back to the age", up, &TopoPing{}, "up"},
		{"a stale age warns", stale, nil, "warn"},
		{"no age and no ping is unknown, NOT up", unknown, nil, "unknown"},
	}
	for _, c := range cases {
		if got := topoStatusFor(c.n, c.p); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
