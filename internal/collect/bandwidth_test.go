package collect

// The bandwidth differential gate.
//
// tools/bandwidth-cases.js drives the LIVE collector over the captured
// connection table twice — once as recorded, once with the byte counters
// advanced on a fixed rule — and records what it made of both. This replays the
// same two snapshots through the Go transform and requires the same answer.
//
// It is a generator rather than a fixture because this collector performs no
// router I/O at all: there is nothing to capture. See the generator's header.

import (
	"path/filepath"
	"strconv"
	"testing"

	"mikrodash/internal/routeros"
)

type bwCases struct {
	LanCidrs []string `json:"lanCidrs"`
	T0       int64    `json:"t0"`
	DT       int64    `json:"dt"`
	Rows     int      `json:"rows"`
	First    struct {
		Devices []BandwidthDevice `json:"devices"`
	} `json:"first"`
	Second struct {
		Devices []BandwidthDevice `json:"devices"`
	} `json:"second"`
}

// advance is the generator's counter rule, reproduced exactly. Both sides have
// to see byte-identical input or the gate is comparing two different questions.
func advance(row routeros.Reply, i int) routeros.Reply {
	out := routeros.Reply{}
	for k, v := range row {
		out[k] = v
	}
	orig, _ := strconv.ParseInt(row["orig-bytes"], 10, 64)
	repl, _ := strconv.ParseInt(row["repl-bytes"], 10, 64)
	out["orig-bytes"] = strconv.FormatInt(orig+int64(i%7)*1500+int64(i%3)*40000, 10)
	out["repl-bytes"] = strconv.FormatInt(repl+int64(i%5)*9000+int64(i%11)*700, 10)
	return out
}

func TestBandwidthAgainstCases(t *testing.T) {
	var cases bwCases
	readJSON(t, filepath.Join(testdata, "bandwidth-cases.json"), &cases)
	if cases.Rows == 0 {
		t.Fatal("no cases — run: node tools/bandwidth-cases.js")
	}

	var fixture struct {
		Exchanges []struct {
			Rows []routeros.Reply `json:"rows"`
		} `json:"exchanges"`
	}
	readJSON(t, filepath.Join(testdata, "fixtures", "Mikrotik identity-0cc5 AX3", "conns.json"), &fixture)
	rows := []routeros.Reply{}
	for _, e := range fixture.Exchanges {
		rows = append(rows, e.Rows...)
	}
	if len(rows) != cases.Rows {
		t.Fatalf("the fixture holds %d rows, the cases were generated from %d", len(rows), cases.Rows)
	}

	prev := map[string]bwPrev{}
	first := BuildBandwidth(prev, BandwidthInput{
		Rows: rows, Now: cases.T0, LanCidrs: cases.LanCidrs, PollMs: 3000,
	})
	if diff := diffJSON(toAny(t, first.Devices), toAny(t, cases.First.Devices), ""); diff != "" {
		t.Errorf("the FIRST tick differs from the Node collector:\n%s", diff)
	}

	moved := make([]routeros.Reply, len(rows))
	for i, r := range rows {
		moved[i] = advance(r, i)
	}
	second := BuildBandwidth(prev, BandwidthInput{
		Rows: moved, Now: cases.T0 + cases.DT, LanCidrs: cases.LanCidrs, PollMs: 3000,
	})
	if diff := diffJSON(toAny(t, second.Devices), toAny(t, cases.Second.Devices), ""); diff != "" {
		t.Errorf("the SECOND tick differs from the Node collector:\n%s", diff)
	}

	// The first tick has NO baseline, so every rate must be zero. Asserted
	// separately because it is the property a port could most easily "improve"
	// away by inventing a rate from a single reading.
	for _, d := range first.Devices {
		if d.TotalMbps != 0 {
			t.Errorf("the first tick reported a rate for %s with no previous reading", d.SrcIP)
			break
		}
	}
}

// A counter that went BACKWARDS is a reset or a reused connection id, not a
// burst of traffic. No fixture can hold this: the capture is one table, and a
// generator advancing counters never decreases them.
func TestBandwidthIgnoresBackwardsCounters(t *testing.T) {
	row := func(orig, repl string) []routeros.Reply {
		return []routeros.Reply{{
			".id": "*1", "src-address": "10.0.0.5:5000", "dst-address": "10.0.0.9:443",
			"protocol": "tcp", "orig-bytes": orig, "repl-bytes": repl,
		}}
	}
	in := BandwidthInput{LanCidrs: []string{"10.0.0.0/8"}, PollMs: 3000}
	prev := map[string]bwPrev{}

	in.Rows, in.Now = row("100000", "100000"), 1000
	BuildBandwidth(prev, in)

	// Counters go backwards: the answer is zero, not a negative rate and not a
	// vast one from the wrapped subtraction.
	in.Rows, in.Now = row("500", "400"), 6000
	back := BuildBandwidth(prev, in)
	if len(back.Devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(back.Devices))
	}
	if back.Devices[0].TotalMbps != 0 {
		t.Errorf("a backwards counter produced %v Mbps, want 0", back.Devices[0].TotalMbps)
	}

	// And the reading is still RECORDED, so the next tick measures from the new
	// baseline rather than from the pre-reset one.
	in.Rows, in.Now = row("1000500", "400"), 11000
	next := BuildBandwidth(prev, in)
	if next.Devices[0].TxMbps == 0 {
		t.Error("the tick after a reset measured nothing — the reset reading was not recorded")
	}
}

// The port strip, which decides what counts as one source. Ports are part of a
// connection, not of a device: leaving them on would draw one host as dozens.
func TestExtractAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"10.0.0.5", "10.0.0.5"},
		{"10.0.0.5:443", "10.0.0.5"},
		{"10.0.0.0/24", "10.0.0.0"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"2001:db8::1", "2001:db8::1"},
		{"not-an-address", "not-an-address"},
	}
	for _, c := range cases {
		if got := extractAddress(c.in); got != c.want {
			t.Errorf("extractAddress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The two asymmetries between the SOURCE filter and the `isLan` flag, neither
// of which this corpus can show: every captured source sits inside the one LAN
// range, so the filter never drops a row and the fallback never engages.
func TestBandwidthLanFilterAsymmetries(t *testing.T) {
	rows := []routeros.Reply{
		// An RFC-1918 source talking to an RFC-1918 destination.
		{".id": "*1", "src-address": "192.168.1.10:5000", "dst-address": "192.168.1.1:53",
			"protocol": "udp", "orig-bytes": "1000", "repl-bytes": "2000"},
	}

	// TWO TICKS, because a destination only becomes the "top" one once it has
	// carried traffic: the pick is a strict `>` against a zero seed, so on a
	// first tick every device has an EMPTY dstIp and `isLan` is false whatever
	// the ranges say. The generated cases show exactly that, and an earlier
	// version of this test asserted otherwise and failed — correctly.
	moved := []routeros.Reply{{
		".id": "*1", "src-address": "192.168.1.10:5000", "dst-address": "192.168.1.1:53",
		"protocol": "udp", "orig-bytes": "900000", "repl-bytes": "900000",
	}}

	// WITH NO CONFIGURED LAN RANGES the source filter falls back to RFC 1918, so
	// the device is shown — the page is not blank on first load. But `isLan` is
	// computed against the CONFIGURED ranges only, so it stays false: it answers
	// "is this traffic staying inside the network you configured", and guessing
	// would make an internet destination look local.
	noPrev := map[string]bwPrev{}
	BuildBandwidth(noPrev, BandwidthInput{Rows: rows, Now: 1000, LanCidrs: nil, PollMs: 3000})
	noCidrs := BuildBandwidth(noPrev,
		BandwidthInput{Rows: moved, Now: 6000, LanCidrs: nil, PollMs: 3000})
	if len(noCidrs.Devices) != 1 {
		t.Fatalf("the RFC-1918 fallback did not admit the source: %d devices", len(noCidrs.Devices))
	}
	if noCidrs.Devices[0].DstIP == "" {
		t.Fatal("no top destination on the second tick — the rates did not register")
	}
	if noCidrs.Devices[0].IsLan {
		t.Error("isLan is true with no configured LAN ranges — it must not use the fallback")
	}

	// With the range configured, the same destination IS local.
	withPrev := map[string]bwPrev{}
	cidrs := []string{"192.168.1.0/24"}
	BuildBandwidth(withPrev, BandwidthInput{Rows: rows, Now: 1000, LanCidrs: cidrs, PollMs: 3000})
	withCidrs := BuildBandwidth(withPrev,
		BandwidthInput{Rows: moved, Now: 6000, LanCidrs: cidrs, PollMs: 3000})
	if !withCidrs.Devices[0].IsLan {
		t.Error("isLan is false for a destination inside a configured LAN range")
	}
}

// A counter is recorded for EVERY connection, including ones the LAN filter
// drops — the filter decides what is SHOWN, not what is measured.
//
// It matters when the LAN ranges arrive late, which is the normal startup
// sequence: the DHCP networks are read on their own clock, so a source can be
// excluded on one tick and included on the next. Recording only what passed the
// filter leaves that source with no baseline, and its first visible tick reports
// zero traffic instead of its real rate.
func TestBandwidthRecordsCountersBeforeFiltering(t *testing.T) {
	rows := func(orig string) []routeros.Reply {
		return []routeros.Reply{{
			".id": "*1", "src-address": "172.20.0.5:5000", "dst-address": "172.20.0.9:443",
			"protocol": "tcp", "orig-bytes": orig, "repl-bytes": "0",
		}}
	}
	prev := map[string]bwPrev{}

	// Tick one: the ranges are known but do NOT include this source, so it is
	// filtered out of the payload.
	narrow := BuildBandwidth(prev, BandwidthInput{
		Rows: rows("1000000"), Now: 1000, LanCidrs: []string{"10.0.0.0/8"}, PollMs: 3000})
	if len(narrow.Devices) != 0 {
		t.Fatalf("a source outside the LAN ranges was shown: %d devices", len(narrow.Devices))
	}
	if _, ok := prev["*1"]; !ok {
		t.Fatal("the filtered row's counters were not recorded")
	}

	// Tick two: the ranges now include it. Its rate must be measured from the
	// reading taken while it was invisible.
	wide := BuildBandwidth(prev, BandwidthInput{
		Rows: rows("2000000"), Now: 6000,
		LanCidrs: []string{"10.0.0.0/8", "172.16.0.0/12"}, PollMs: 3000})
	if len(wide.Devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(wide.Devices))
	}
	if wide.Devices[0].TxMbps == 0 {
		t.Error("no rate on the first visible tick — the counters were only recorded after filtering")
	}
}

// The shared snapshot, and the guard that makes it safe.
//
// Two collectors read one table: connections deposits it, bandwidth takes it.
// The timestamp is what tells the second consumer whether it is looking at
// something new — and re-differencing one reading against itself would report a
// busy network as idle, which is a wrong answer rather than a missing one.
func TestBandwidthUsesSharedSnapshotOnce(t *testing.T) {
	table := NewConnTable()
	emitted := 0
	b := NewBandwidth(fakeReader{}, func(string, string, any) { emitted++ }, nil, nil, nil, 3000).
		WithTable(table)

	rows := func(orig string) []routeros.Reply {
		return []routeros.Reply{{
			".id": "*1", "src-address": "10.0.0.5", "dst-address": "10.0.0.9",
			"dst-port": "443", "protocol": "tcp", "orig-bytes": orig, "repl-bytes": "0",
		}}
	}

	// NOTHING DEPOSITED YET: the collector must not invent a reading.
	b.Tick()
	if b.Last() != nil {
		t.Fatal("a payload was built before the table had ever been read")
	}

	table.Set(rows("1000000"), 1000)
	b.Tick()
	first := b.Last()
	if first == nil {
		t.Fatal("no payload from the first snapshot")
	}

	// The SAME snapshot again: no new payload, because there is no new reading.
	b.Tick()
	if b.Last() != first {
		t.Error("the same snapshot was differenced twice — rates would read as zero")
	}

	// A new snapshot, five seconds later: now there is something to measure.
	table.Set(rows("2000000"), 6000)
	b.Tick()
	second := b.Last()
	if second == first {
		t.Fatal("a new snapshot produced no new payload")
	}
	if len(second.Devices) != 1 || second.Devices[0].TxMbps == 0 {
		t.Errorf("no rate from two snapshots: %+v", second.Devices)
	}
}
