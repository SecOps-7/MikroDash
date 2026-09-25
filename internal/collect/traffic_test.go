package collect

import (
	"encoding/json"
	"mikrodash/internal/hub"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/routeros"

	"mikrodash/internal/roscache"
)

// Every packet the captured stream delivered, parsed.
//
// There is no golden for this collector: it emits per-viewer, so the Node replay
// harness — which records only room emits — has nothing to compare against. The
// fixture's thirteen readings are still the real thing a real router sent, so
// they are replayed here against the parse rather than left unused.
func TestParseTrafficSampleOverFixture(t *testing.T) {
	var fixture struct {
		Streams []struct {
			Cmd  string           `json:"cmd"`
			Rows []routeros.Reply `json:"rows"`
		} `json:"streams"`
	}
	readJSON(t, filepath.Join(testdata, "fixtures", "Mikrotik identity-0cc5 AX3", "traffic.json"), &fixture)
	if len(fixture.Streams) == 0 || len(fixture.Streams[0].Rows) == 0 {
		t.Fatal("the traffic fixture holds no stream rows")
	}

	for i, row := range fixture.Streams[0].Rows {
		s := parseTrafficSample(row, 1000)
		if s.IfName == "" {
			t.Fatalf("row %d parsed with no interface name", i)
		}
		if !s.Running || s.Disabled {
			t.Errorf("row %d: running=%v disabled=%v, want a live link", i, s.Running, s.Disabled)
		}
		// Every captured reading is a plain bits-per-second integer, so the Mbps
		// value must be the integer over a million at three decimals.
		want := trafficMbps(parseBps(row["rx-bits-per-second"]))
		if s.RxMbps != want {
			t.Errorf("row %d: rx %v, want %v", i, s.RxMbps, want)
		}
	}

	// The first row, in full, so the three-decimal rounding is pinned by a
	// literal rather than by the same expression that produced it.
	first := parseTrafficSample(fixture.Streams[0].Rows[0], 1000)
	if first.RxMbps != 0.592 || first.TxMbps != 0.611 {
		t.Errorf("first sample = %v / %v Mbps, want 0.592 / 0.611", first.RxMbps, first.TxMbps)
	}
}

// The unit suffixes, which the captured stream never uses but the helper
// accepts because the original does.
func TestParseBps(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"0", 0},
		{"591720", 591720},
		{"1500bps", 1500},
		{"1.5kbps", 1500},
		{"1.5Mbps", 1_500_000},
		{"2Gbps", 2e9},
		{"nonsense", 0},
	}
	for _, c := range cases {
		if got := parseBps(c.in); got != c.want {
			t.Errorf("parseBps(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A missing flag is not a dead link. `running` defaults to TRUE and `disabled`
// to false, because the row is a measurement and the router simply did not
// mention them — getting this backwards blanks the WAN badge.
func TestTrafficFlagDefaults(t *testing.T) {
	s := parseTrafficSample(routeros.Reply{"name": "ether1"}, 1)
	if !s.Running || s.Disabled {
		t.Errorf("a row with no flags parsed as running=%v disabled=%v", s.Running, s.Disabled)
	}
	down := parseTrafficSample(routeros.Reply{"name": "ether1", "running": "false"}, 1)
	if down.Running {
		t.Error("running=false parsed as up")
	}
}

// The subscription set is REFCOUNTED: two viewers on one interface must not
// have the first to leave stop the stream for the second.
func TestTrafficWatchRefcount(t *testing.T) {
	tr := NewTraffic(fakeReader{}, hub.Relay{}, "WAN1", 5)
	tr.SetAvailable([]string{"WAN1", "ether2"})

	tr.Watch("ether2")
	tr.Watch("ether2")
	tr.mu.Lock()
	list := tr.ifaceListLocked()
	tr.mu.Unlock()
	if len(list) != 2 {
		t.Fatalf("watching = %v, want the default and ether2", list)
	}

	tr.Unwatch("ether2")
	tr.mu.Lock()
	list = tr.ifaceListLocked()
	tr.mu.Unlock()
	if len(list) != 2 {
		t.Errorf("one viewer left and the interface was dropped for the other: %v", list)
	}

	tr.Unwatch("ether2")
	tr.mu.Lock()
	list = tr.ifaceListLocked()
	tr.mu.Unlock()
	// The DEFAULT interface always remains: the WAN badge reads it on every page.
	if len(list) != 1 || list[0] != "WAN1" {
		t.Errorf("after the last viewer left, watching = %v, want just the default", list)
	}
}

// A name a browser sends reaches a router command, so it is checked against the
// interfaces that actually exist rather than merely escaped.
func TestTrafficNormalizeIfName(t *testing.T) {
	tr := NewTraffic(fakeReader{}, hub.Relay{}, "WAN1", 5)

	// BEFORE the interface list arrives nothing is accepted. The alternative is
	// trusting the browser for one poll interval.
	if _, ok := tr.NormalizeIfName("WAN1"); ok {
		t.Error("a name was accepted before the interface list was known")
	}

	tr.SetAvailable([]string{"WAN1", "ether2"})
	if n, ok := tr.NormalizeIfName("  ether2 "); !ok || n != "ether2" {
		t.Errorf("normalize(' ether2 ') = %q,%v", n, ok)
	}
	for _, bad := range []string{"", "   ", "nope", "ether2\nwith-newline", "ether2\x00"} {
		if _, ok := tr.NormalizeIfName(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

// History is kept whether anyone is watching or not, and bounded.
func TestTrafficHistoryRing(t *testing.T) {
	tr := NewTraffic(fakeReader{}, hub.Relay{}, "WAN1", 1) // 60 points
	for i := 0; i < 70; i++ {
		tr.onPacket(routeros.Reply{"name": "WAN1", "rx-bits-per-second": "1000000",
			"tx-bits-per-second": "2000000"})
	}
	h := tr.History("WAN1")
	if len(h.Points) != 60 {
		t.Errorf("history holds %d points, want the 60-point floor", len(h.Points))
	}
	if h.Points[0].RxMbps != 1 || h.Points[0].TxMbps != 2 {
		t.Errorf("history point = %+v, want 1/2 Mbps", h.Points[0])
	}
	// And the WAN status followed, because this IS the default interface.
	if w := tr.LastWan(); w == nil || w.IfName != "WAN1" || !w.Running {
		t.Errorf("wan status = %+v", w)
	}
}

// A RECONNECT KEEPS THE CHART, and this is the regression it pins.
//
// `Reconnected` used to empty the ring, so every router reconnect -- routine,
// and retried every 5s by connectLoop -- restarted the traffic graph from
// nothing. The reason given was that the router might be a different one, which
// a Session makes impossible: it is built per router ID, so a different router
// is a different Session and a different collector.
func TestReconnectKeepsTheTrafficHistory(t *testing.T) {
	tr := NewTraffic(fakeReader{}, hub.Relay{}, "WAN1", 1)
	for i := 0; i < 10; i++ {
		tr.onPacket(routeros.Reply{"name": "WAN1", "rx-bits-per-second": "1000000",
			"tx-bits-per-second": "2000000"})
	}
	before := len(tr.History("WAN1").Points)
	if before != 10 {
		t.Fatalf("history holds %d points before the reconnect, want 10", before)
	}

	tr.Reconnected()

	if got := len(tr.History("WAN1").Points); got != before {
		t.Errorf("history holds %d points after a reconnect, want the same %d", got, before)
	}
	// The samples must still be the samples, not zeroed placeholders of the
	// right length.
	if p := tr.History("WAN1").Points[0]; p.RxMbps != 1 || p.TxMbps != 2 {
		t.Errorf("first surviving point = %+v, want 1/2 Mbps", p)
	}
	// AND THE OTHER HALF, which is not symmetric: lastWan is a status, not a
	// series. Keeping "up" across a reconnect would assert something about right
	// now on the strength of a reading from before the drop.
	if w := tr.LastWan(); w != nil {
		t.Errorf("wan status survived the reconnect: %+v", w)
	}
}

// TestTheHistoryPayloadCarriesItsWindow.
//
// The live emit is `{ifName, windowMinutes, points}` and this port sent
// `{ifName, points}` — found by the live-socket-diff tool, which compares the
// shapes both servers actually emit. No static audit could have: the field was
// absent from a struct, not from any list something checks.
//
// Nothing reads it today. It is carried because the payload contract is the line
// a port may not move, and a field the live app sends is part of that whether or
// not this month's client destructures it.
func TestTheHistoryPayloadCarriesItsWindow(t *testing.T) {
	for _, minutes := range []int{5, 30, 120} {
		tr := NewTraffic(fakeReader{}, hub.Relay{}, "ether1", minutes)
		got := tr.Watch("ether1")
		if got.WindowMinutes != minutes {
			t.Errorf("a %d-minute buffer reported windowMinutes=%d", minutes, got.WindowMinutes)
		}
		if got.IfName != "ether1" {
			t.Errorf("ifName = %q", got.IfName)
		}
	}
	// AND IT IS IN THE JSON, under the live spelling. A Go field with no tag, or
	// the wrong one, passes every check above and still reaches the browser as
	// `WindowMinutes` — which is exactly how `/api/roles` shipped `Page`/`Access`.
	tr := NewTraffic(fakeReader{}, hub.Relay{}, "ether1", 30)
	b, err := json.Marshal(tr.Watch("ether1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"windowMinutes":30`) {
		t.Errorf("the marshalled payload is %s", b)
	}
}

// ── PHASE 4.1: THE THIRD SEQUENCE DERIVATION, AND THE STATE HAS A KEY ──────
//
// `ping` and `logs` fold into one ring; this folds into one ring PER INTERFACE.
// The signature is the same, because the fold only ever touches the ring the
// packet belongs to — handing it the whole map would make the derivation
// responsible for storage it does not use.

func TestFoldTrafficDropsARowWithNoName(t *testing.T) {
	prior := []TrafficPoint{{TS: 1}}
	_, next, ok := FoldTraffic(prior, 10, routeros.Reply{"rx-bits-per-second": "100"}, 5)
	if ok {
		t.Error("a nameless row was folded in; RouterOS sends one for an empty " +
			"interface list, and it would file a point against an interface called \"\"")
	}
	if len(next) != 1 {
		t.Errorf("the ring changed to %d entries on a dropped row", len(next))
	}
}

// TestFoldTrafficKeepsTheMostRecentPoints. A chart that trimmed from the back
// would freeze on its oldest window and never move — the same rule the log ring
// follows, and the same failure.
func TestFoldTrafficKeepsTheMostRecentPoints(t *testing.T) {
	var ring []TrafficPoint
	for i := 0; i < 10; i++ {
		_, ring, _ = FoldTraffic(ring, 3, routeros.Reply{
			"name": "ether1", "rx-bits-per-second": "1000000",
		}, int64(i))
	}
	if len(ring) != 3 {
		t.Fatalf("ring holds %d, want 3", len(ring))
	}
	if ring[0].TS != 7 || ring[2].TS != 9 {
		t.Errorf("ring spans ts %d..%d, want 7..9 — the chart is trimming from the "+
			"wrong end and would freeze on its oldest window", ring[0].TS, ring[2].TS)
	}
}

// TestFoldTrafficDoesNotWriteThroughToThePrior. Built with spare capacity on
// purpose: `FoldPing`'s equivalent passed a mutation for a whole round because
// slice literals have capacity equal to their length, so append reallocates by
// accident and the caller's array survives for the wrong reason.
func TestFoldTrafficDoesNotWriteThroughToThePrior(t *testing.T) {
	prior := append(make([]TrafficPoint, 0, 8), TrafficPoint{TS: 1}, TrafficPoint{TS: 2})
	_, _, ok := FoldTraffic(prior, 10, routeros.Reply{"name": "ether1"}, 3)
	if !ok {
		t.Fatal("a named row was dropped")
	}
	if len(prior) != 2 {
		t.Fatalf("the prior grew to %d", len(prior))
	}
	// The element past the prior's length is where an in-place append lands.
	if full := prior[:cap(prior)]; full[2].TS != 0 {
		t.Errorf("the fold appended into the prior's spare capacity (ts %d); the "+
			"caller's ring was written through", full[2].TS)
	}
}

// TestFoldTrafficTouchesOnlyItsOwnRing — the state is keyed, and a packet for
// one interface must not disturb another's history. That is the whole reason the
// fold takes ONE ring rather than the map.
func TestFoldTrafficTouchesOnlyItsOwnRing(t *testing.T) {
	rings := map[string][]TrafficPoint{
		"ether1": {{TS: 1}},
		"ether2": {{TS: 1}, {TS: 2}},
	}
	sample, next, ok := FoldTraffic(rings["ether1"], 10,
		routeros.Reply{"name": "ether1", "rx-bits-per-second": "8000000"}, 9)
	if !ok || sample.IfName != "ether1" {
		t.Fatalf("sample = %+v, ok=%v", sample, ok)
	}
	rings["ether1"] = next

	if len(rings["ether1"]) != 2 {
		t.Errorf("ether1 holds %d points, want 2", len(rings["ether1"]))
	}
	if len(rings["ether2"]) != 2 || rings["ether2"][1].TS != 2 {
		t.Errorf("ether2's ring changed to %v; a packet for one interface disturbed "+
			"another's history", rings["ether2"])
	}
	if sample.RxMbps != 8 {
		t.Errorf("rxMbps = %v, want 8 — the sample's own conversion changed", sample.RxMbps)
	}
}

// A RECORDED interface is in the stream with nobody watching (#59): the
// recorder writes from what the stream carries, so an interface the stream
// leaves out records nothing while the operator believes it is recording.
func TestRecordedInterfacesStayInTheStream(t *testing.T) {
	tr := NewTraffic(fakeReader{}, hub.Relay{}, "WAN1", 5)
	tr.SetAvailable([]string{"WAN1", "ether2", "ether3"})
	list := func() []string {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return tr.ifaceListLocked()
	}

	tr.SetRecorded([]string{"WAN1", "ether3"})
	if got := list(); len(got) != 2 || got[0] != "WAN1" || got[1] != "ether3" {
		t.Fatalf("recorded set = %v, want the default and ether3", got)
	}

	// A WATCHER ON TOP does not double it, and leaving does not take the
	// recorded one away with it.
	tr.Watch("ether3")
	tr.Watch("ether2")
	if got := list(); len(got) != 3 {
		t.Errorf("with a watcher on a recorded interface, %v, want three names", got)
	}
	tr.Unwatch("ether3")
	tr.Unwatch("ether2")
	if got := list(); len(got) != 2 || got[1] != "ether3" {
		t.Errorf("after the viewers left, %v, want the default and the recorded ether3", got)
	}

	// AND THE DEFAULT SURVIVES A LIST THAT OMITS IT. The badge reads it on
	// every page; `store.RecordedIfacesFor` always includes it, and this is the
	// collector's half of that.
	tr.SetRecorded([]string{"ether2"})
	if got := list(); len(got) != 2 || got[0] != "WAN1" || got[1] != "ether2" {
		t.Errorf("recorded without the default = %v, want it kept", got)
	}

	tr.SetRecorded(nil)
	if got := list(); len(got) != 1 || got[0] != "WAN1" {
		t.Errorf("recorded nothing = %v, want just the default", got)
	}
}

// SetRecorded RESTARTS THE STREAM, so declaring the same names again must be a
// no-op: the fleet syncs call it every few seconds, and a restart per sync
// would drop the measurement each time.
func TestSetRecordedIsQuietWhenNothingChanged(t *testing.T) {
	r := &cmdReader{}
	tr := NewTraffic(r, hub.Relay{}, "ether1", 1)
	c := roscache.New(r)
	tr.UseCache(c)
	tr.SetAvailable([]string{"ether1", "ether2"})

	tr.SetRecorded([]string{"ether1", "ether2"})
	r.mu.Lock()
	first := len(r.cmds)
	r.mu.Unlock()
	if first == 0 {
		t.Fatal("declaring a new recorded set opened no stream")
	}

	for i := 0; i < 5; i++ {
		tr.SetRecorded([]string{"ether2", "ether1"}) // same set, other order
	}
	r.mu.Lock()
	again := len(r.cmds)
	r.mu.Unlock()
	if again != first {
		t.Errorf("the same set reopened the stream %d time(s)", again-first)
	}
}
