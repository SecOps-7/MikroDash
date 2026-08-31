package collect

import (
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// These cover what the corpus cannot. The differential gate proves the ROUTE
// half against the AX3 golden; the AX3 runs no BGP, so every peer transform
// below has no fixture behind it and is tested directly instead. The same goes
// for the flag spellings — the captured router reports only one of them.

// ── flags ────────────────────────────────────────────────────────────────────

// TestFlagCaseIsAccepted is the case the MikroTik manual forced. RouterOS
// documents `C - connect, S - static` on one page and `c - connect, s - static`
// on another, so both spellings are real, and a parser that picked one would
// misread every route on some routers.
func TestFlagCaseIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags string
		want  RouteFlags
	}{
		{"upper", "ADC", RouteFlags{Active: true, Dynamic: true, Connect: true}},
		{"lower connect", "Ac", RouteFlags{Active: true, Connect: true}},
		{"lower static", "As", RouteFlags{Active: true, Static: true}},
		{"bgp lower", "Ab", RouteFlags{Active: true, BGP: true}},
		{"bgp upper", "AB", RouteFlags{Active: true, BGP: true}},
		{"ospf", "Ao", RouteFlags{Active: true, OSPF: true}},
		{"disabled", "X", RouteFlags{Disabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRouteFlags(routeros.Reply{".flags": tc.flags})
			if got != tc.want {
				t.Errorf("flags %q = %+v, want %+v", tc.flags, got, tc.want)
			}
		})
	}
}

// TestFlagsFallBackToProperties: a proplist read returns `static=true` rather
// than a flag string, and both must produce the same answer.
func TestFlagsFallBackToProperties(t *testing.T) {
	got := parseRouteFlags(routeros.Reply{"active": "true", "static": "true", "disabled": "true"})
	want := RouteFlags{Active: true, Static: true, Disabled: true}
	if got != want {
		t.Errorf("= %+v, want %+v", got, want)
	}
	// "false" is not "true": only the exact string counts, matching _bool.
	if parseRouteFlags(routeros.Reply{"active": "false"}).Active {
		t.Error(`active="false" read as true`)
	}
}

// TestUntypedRouteWithNexthopIsStatic pins the inference. RouterOS does not
// always flag a manually added route; without this it falls through to
// "connect", the one type the page treats as not editable.
func TestUntypedRouteWithNexthopIsStatic(t *testing.T) {
	for _, tc := range []struct{ gw, want string }{
		{"198.51.100.1", "static"},
		{"2001:db8::1", "static"},
		{"", "connect"},
		{"0.0.0.0", "connect"},
		{"::", "connect"},
		{"not-an-address", "connect"},
	} {
		got := mapRoute(routeros.Reply{"gateway": tc.gw, "dst-address": "0.0.0.0/0"}, "ipv4")
		if got.Type != tc.want {
			t.Errorf("gateway %q => type %q, want %q", tc.gw, got.Type, tc.want)
		}
	}
}

// TestExplicitTypeBeatsTheInference: a flagged connected route keeps its type
// even though it has a real next hop.
func TestExplicitTypeBeatsTheInference(t *testing.T) {
	r := mapRoute(routeros.Reply{".flags": "AC", "gateway": "198.51.100.1"}, "ipv4")
	if r.Type != "connect" {
		t.Errorf("type = %q, want connect", r.Type)
	}
}

// TestProtocolOverridesType: bgp and ospf name the protocol, but the row keeps
// its dynamic/static type — the page shows both columns.
func TestProtocolOverridesType(t *testing.T) {
	r := mapRoute(routeros.Reply{".flags": "ADb", "gateway": "198.51.100.1"}, "ipv4")
	if r.Type != "dynamic" || r.Protocol != "bgp" {
		t.Errorf("type=%q protocol=%q, want dynamic/bgp", r.Type, r.Protocol)
	}
}

// ── safeInt ──────────────────────────────────────────────────────────────────

func TestSafeInt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0}, {"0", 0}, {"42", 42}, {"-7", -7},
		// parseInt semantics: a leading number wins, trailing junk is ignored.
		{"64512abc", 64512},
		{"abc", 0},
		{" 12 ", 12},
	} {
		if got := safeInt(tc.in); got != tc.want {
			t.Errorf("safeInt(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ── uptime ───────────────────────────────────────────────────────────────────

func TestParseUptime(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"00:00:30", 30},
		{"01:02:03", 3723},
		{"1d", 86400},
		{"1d2h3m4s", 93784},
		{"45s", 45},
		{"2h30m", 9000},
		{"garbage", 0},
	} {
		if got := parseUptime(tc.in); got != tc.want {
			t.Errorf("parseUptime(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ── peer classification ──────────────────────────────────────────────────────

func TestClassifyPeer(t *testing.T) {
	for _, tc := range []struct {
		name             string
		as               int64
		desc, peer, want string
	}{
		{"private 16-bit low", 64512, "", "", "private"},
		{"private 16-bit high", 65534, "", "", "private"},
		{"private 32-bit low", 4200000000, "", "", "private"},
		{"just below private", 64511, "", "", "upstream"},
		{"just above private", 65535, "", "", "upstream"},
		{"ix by description", 3333, "AMS-IX peering", "", "ix"},
		{"ix by name", 3333, "", "rs1", "ix"},
		{"route server", 3333, "route-server", "", "ix"},
		{"plain transit", 3333, "Some Transit Co", "upstream-1", "upstream"},
		// A private ASN wins over IX-looking text: the range is a fact, the text
		// is a guess.
		{"private beats ix text", 65001, "peering", "", "private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPeer(tc.as, tc.desc, tc.peer); got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

// ── peer state ───────────────────────────────────────────────────────────────

func TestNormalisePeerState(t *testing.T) {
	for _, tc := range []struct {
		row  routeros.Reply
		want string
	}{
		{routeros.Reply{"state": "established"}, "established"},
		{routeros.Reply{"state": "Established"}, "established"},
		{routeros.Reply{"state": "idle"}, "idle"},
		{routeros.Reply{"state": "opensent"}, "opensent"},
		// v7 reports a boolean instead of a state word.
		{routeros.Reply{"established": "true"}, "established"},
		{routeros.Reply{"established": "false"}, "idle"},
		{routeros.Reply{}, "idle"},
		// Anything unrecognised passes through lowercased rather than being
		// forced into a bucket it does not belong in.
		{routeros.Reply{"state": "SOMETHING-NEW"}, "something-new"},
	} {
		if got := normalisePeerState(tc.row); got != tc.want {
			t.Errorf("%v => %q, want %q", tc.row, got, tc.want)
		}
	}
}

// ── buildPeers ───────────────────────────────────────────────────────────────

func routingWithSessions(sessions []routeros.Reply, cfg map[string]routeros.Reply) *Routing {
	r := NewRouting(nil, func(string, string, any) {}, 10000)
	for _, s := range sessions {
		k := peerKey(s)
		r.sessions[k] = s
		r.sessionOrder = append(r.sessionOrder, k)
	}
	if cfg != nil {
		r.peerCfg = cfg
	}
	return r
}

// TestGhostSessionsAreDropped: a router mid-teardown reports rows with neither
// an address nor a name, and they would render as a peer nobody configured.
func TestGhostSessionsAreDropped(t *testing.T) {
	r := routingWithSessions([]routeros.Reply{
		{"remote.address": "198.51.100.1", "state": "established"},
		{"name": "?"},
	}, nil)
	if got := r.buildPeers(); len(got) != 1 {
		t.Errorf("built %d peers, want 1: %+v", len(got), got)
	}
}

// TestPeerConfigSuppliesDescription: the session row has no comment, the
// connection row does, and the two are joined on the remote address.
func TestPeerConfigSuppliesDescription(t *testing.T) {
	r := routingWithSessions(
		[]routeros.Reply{{"remote.address": "198.51.100.1", "state": "established"}},
		map[string]routeros.Reply{"198.51.100.1": {"comment": "Transit A", "remote.as": "3333"}},
	)
	p := r.buildPeers()[0]
	if p.Description != "Transit A" {
		t.Errorf("description = %q", p.Description)
	}
	// The AS falls back to the configured one when the session omits it.
	if p.RemoteAs != 3333 {
		t.Errorf("remoteAs = %d, want 3333", p.RemoteAs)
	}
}

// TestPrefixHistoryAccumulatesAndCaps: the history is the one thing a single
// read cannot hold, so it is worth pinning that it grows and then stops.
func TestPrefixHistoryAccumulatesAndCaps(t *testing.T) {
	r := routingWithSessions(
		[]routeros.Reply{{"remote.address": "198.51.100.1", "state": "established", "prefix-count": "10"}}, nil)
	for i := 0; i < routeHistoryLen+5; i++ {
		r.buildPeers()
	}
	p := r.buildPeers()[0]
	if len(p.PrefixHistory) != routeHistoryLen {
		t.Errorf("history len = %d, want %d", len(p.PrefixHistory), routeHistoryLen)
	}
}

// TestFlappingNeedsThreeChanges: a peer that is merely down must not accumulate
// flaps by sitting still — only a CHANGE counts.
func TestFlappingNeedsThreeChanges(t *testing.T) {
	r := routingWithSessions(
		[]routeros.Reply{{"remote.address": "198.51.100.1", "state": "established"}}, nil)
	now := int64(1_700_000_000_000)
	r.now = func() int64 { return now }

	for i := 0; i < 10; i++ {
		if r.buildPeers()[0].Flapping {
			t.Fatal("a stable peer was reported as flapping")
		}
	}

	flip := func(state string) bool {
		r.sessions["198.51.100.1"]["state"] = state
		now += 1000
		return r.buildPeers()[0].Flapping
	}
	if flip("idle") {
		t.Error("flapping after one change")
	}
	if flip("established") {
		t.Error("flapping after two changes")
	}
	if !flip("idle") {
		t.Error("not flapping after three changes inside the window")
	}
}

// TestFlappingWindowExpires: three changes spread beyond five minutes are not a
// flap, which is the whole point of the window.
func TestFlappingWindowExpires(t *testing.T) {
	r := routingWithSessions(
		[]routeros.Reply{{"remote.address": "198.51.100.1", "state": "established"}}, nil)
	now := int64(1_700_000_000_000)
	r.now = func() int64 { return now }
	r.buildPeers()

	for _, s := range []string{"idle", "established", "idle"} {
		r.sessions["198.51.100.1"]["state"] = s
		now += 6 * 60 * 1000 // each change six minutes after the last
		if r.buildPeers()[0].Flapping {
			t.Fatal("changes six minutes apart reported as flapping")
		}
	}
}

// TestStateIsPrunedForVanishedPeers: a reconfigured router must not accumulate
// history for sessions it no longer has.
func TestStateIsPrunedForVanishedPeers(t *testing.T) {
	r := routingWithSessions([]routeros.Reply{
		{"remote.address": "198.51.100.1", "state": "established"},
		{"remote.address": "198.51.100.2", "state": "established"},
	}, nil)
	r.buildPeers()
	if len(r.prefixHistory) != 2 {
		t.Fatalf("history for %d peers, want 2", len(r.prefixHistory))
	}

	delete(r.sessions, "198.51.100.2")
	r.sessionOrder = []string{"198.51.100.1"}
	r.buildPeers()
	if len(r.prefixHistory) != 1 || len(r.peerState) != 1 {
		t.Errorf("after a peer vanished: history=%d state=%d, want 1/1",
			len(r.prefixHistory), len(r.peerState))
	}
}

// ── payload shaping ──────────────────────────────────────────────────────────

// TestOnlyStaticAndDynamicRoutesReachThePage: a connected route is a property of
// an interface and belongs on the Interfaces page. The COUNTS still see it.
func TestOnlyStaticAndDynamicRoutesReachThePage(t *testing.T) {
	r := NewRouting(nil, func(string, string, any) {}, 10000)
	r.routes = map[string]Route{
		"a": mapRoute(routeros.Reply{".id": "*1", ".flags": "AC"}, "ipv4"),
		"b": mapRoute(routeros.Reply{".id": "*2", ".flags": "AS", "gateway": "198.51.100.1"}, "ipv4"),
		"c": mapRoute(routeros.Reply{".id": "*3", ".flags": "AD"}, "ipv4"),
	}
	r.order = []string{"a", "b", "c"}
	r.emitPayload([]Peer{})

	p := r.Last()
	if len(p.Routes) != 2 {
		t.Errorf("page got %d routes, want 2 (the connected one is excluded)", len(p.Routes))
	}
	if p.RouteCounts.Total != 3 || p.RouteCounts.Connect != 1 {
		t.Errorf("counts = %+v, want total 3 with 1 connect", p.RouteCounts)
	}
}

func TestSummaryCountsPeerStates(t *testing.T) {
	r := NewRouting(nil, func(string, string, any) {}, 10000)
	r.emitPayload([]Peer{
		{Key: "a", State: "established"},
		{Key: "b", State: "idle"},
		{Key: "c", State: "active"},
	})
	if got := r.Last().Summary; got != (PeerSummary{Total: 3, Established: 1, Down: 2}) {
		t.Errorf("summary = %+v", got)
	}
}

// TestEmptyPayloadMarshalsAsArrays: peers and routes must be [] and never null,
// because the page iterates them.
func TestEmptyPayloadMarshalsAsArrays(t *testing.T) {
	r := NewRouting(nil, func(string, string, any) {}, 10000)
	r.emitPayload([]Peer{})
	p := r.Last()
	if p.Peers == nil || p.Routes == nil {
		t.Errorf("peers=%v routes=%v — both must be empty slices, not nil", p.Peers, p.Routes)
	}
}

// bgpOnly skips the route tables and nothing else.
//
// The alert pool runs this for routers nobody is watching, where the rules read
// `peers` alone — so the two route reads are load for a payload no page renders,
// per alert-enabled router, on hardware whose limit is concurrent API channels.
// The live pool passes `bgpOnly: true` for the same reason.
func TestBGPOnlySkipsTheRouteTablesAndKeepsThePeers(t *testing.T) {
	for _, tc := range []struct {
		why      string
		bgpOnly  bool
		wantSeen bool
	}{
		{"the page path reads the route tables", false, true},
		{"the alert pool does not", true, false},
	} {
		t.Run(tc.why, func(t *testing.T) {
			var paths []string
			ros := &recordingReader{onDo: func(c routeros.Cmd) { paths = append(paths, c.Path) }}
			r := NewRouting(ros, func(string, string, any) {}, 1000)
			if tc.bgpOnly {
				r.BGPOnly()
			}
			r.Tick()

			sawRoutes, sawBGP := false, false
			for _, p := range paths {
				if p == "/ip/route/print" || p == "/ipv6/route/print" {
					sawRoutes = true
				}
				if strings.HasPrefix(p, "/routing/bgp/") {
					sawBGP = true
				}
			}
			if sawRoutes != tc.wantSeen {
				t.Errorf("route tables read = %v, want %v (paths: %v)", sawRoutes, tc.wantSeen, paths)
			}
			// THE PEERS ARE READ EITHER WAY. A bgpOnly that also skipped BGP
			// would make every alert-enabled router silent for BGP alerts, which
			// is the defect this mode exists to avoid, inverted.
			if !sawBGP {
				t.Errorf("the BGP menus were not read at all (paths: %v)", paths)
			}
		})
	}
}

// A reader that records what was asked for and answers nothing.
type recordingReader struct{ onDo func(routeros.Cmd) }

func (r *recordingReader) Connected() bool { return true }
func (r *recordingReader) Do(c routeros.Cmd) ([]routeros.Reply, error) {
	if r.onDo != nil {
		r.onDo(c)
	}
	return nil, nil
}
