package collect

// What the fixture cannot say.
//
// `TestGoldenPayloads` replays 64 real rows and pins the whole payload against
// what the Node collector made of them — which is the strongest check available
// and still blind to anything that router happened not to have. Deliberately
// breaking the "drop a row with no MAC" rule left the golden green, because all
// 64 rows had one.
//
// So these are the shapes a capture cannot be relied on to contain: a row
// missing its key, the same key twice, an empty name, a rate that is not a
// number, and more devices than the cut. The fixture proves the port agrees with
// the original on real data; this proves it agrees on the data the original
// guards against.

import (
	"testing"

	"mikrodash/internal/routeros"
)

func talkersFor(t *testing.T, rows []routeros.Reply, topN int) *TalkersPayload {
	t.Helper()
	var got *TalkersPayload
	c := NewTalkers(nil, func(room, event string, payload any) {
		if p, ok := payload.(*TalkersPayload); ok {
			got = p
		}
	}, 30000, topN)
	c.commit(rows)
	if got == nil {
		t.Fatal("nothing was emitted")
	}
	return got
}

func row(name, mac, up, down string) routeros.Reply {
	return routeros.Reply{"name": name, "mac-address": mac, "rate-up": up, "rate-down": down}
}

// TestARowWithNoMacIsDropped — the Map is keyed by MAC, so a row without one has
// nothing to key it by. Keeping it would show a device that cannot be identified.
func TestARowWithNoMacIsDropped(t *testing.T) {
	p := talkersFor(t, []routeros.Reply{
		row("has one", "02:00:00:00:00:01", "1000000", "0"),
		row("has none", "", "9000000", "9000000"),
		{"name": "no key at all", "rate-up": "8000000"},
	}, 5)
	if len(p.Devices) != 1 {
		t.Fatalf("got %d devices, want 1: %+v", len(p.Devices), p.Devices)
	}
	if p.Devices[0].MAC != "02:00:00:00:00:01" {
		t.Errorf("kept the wrong row: %+v", p.Devices[0])
	}
}

// TestADuplicateMacKeepsTheLastRowInTheFirstPosition. A JS Map overwrites in
// place, so the VALUES come from the later row and the ORDER from the earlier —
// which only shows up when a tie makes insertion order decide.
func TestADuplicateMacKeepsTheLastValueAndTheFirstPosition(t *testing.T) {
	p := talkersFor(t, []routeros.Reply{
		row("first", "02:00:00:00:00:01", "1000000", "0"),
		row("other", "02:00:00:00:00:02", "1000000", "0"),
		row("second", "02:00:00:00:00:01", "1000000", "0"),
	}, 5)
	if len(p.Devices) != 2 {
		t.Fatalf("got %d devices, want 2 — the duplicate must collapse", len(p.Devices))
	}
	if p.Devices[0].MAC != "02:00:00:00:00:01" {
		t.Errorf("the deduplicated row lost its position: %+v", p.Devices)
	}
	if p.Devices[0].Name != "second" {
		t.Errorf("name is %q, want the LAST row's value", p.Devices[0].Name)
	}
}

// TestAnEmptyNameIsKept — the fixture has two of these and they are real: a
// kid-control entry with no name. `r.name || ”` keeps the device.
func TestAnEmptyNameIsKept(t *testing.T) {
	p := talkersFor(t, []routeros.Reply{
		{"mac-address": "02:00:00:00:00:01", "rate-up": "500000", "rate-down": "0"},
	}, 5)
	if len(p.Devices) != 1 || p.Devices[0].Name != "" {
		t.Fatalf("want one device with an empty name, got %+v", p.Devices)
	}
}

// TestRatesParseLikeParseInt.
func TestRatesParseLikeParseInt(t *testing.T) {
	// EVERY EXPECTATION BELOW CAME FROM RUNNING `+(parseInt(v||'0',10)/1e6).toFixed(3)`,
	// not from reasoning about it. The first version of this table was written by
	// reasoning and got "12abc" wrong — parseInt takes the leading 12, and 12 bits
	// per second rounds to 0.000 at three decimals, so the answer is 0 rather than
	// the 0.000012 I expected. The port was right and the test was wrong.
	cases := []struct {
		in   string
		want float64
	}{
		{"1000000", 1},
		{"", 0},
		{"0", 0},
		{"7248568", 7.249}, // rounds UP at the third decimal
		{"1500", 0.002},    // 0.0015 rounds away from zero
		{"500", 0.001},     // 0.0005 likewise
		{"499", 0},
		{"12abc", 0},      // parseInt takes 12; 12 bits/s is 0.000 Mbps
		{" 42 ", 0},       // parseInt skips leading whitespace
		{"0x10", 0},       // with radix 10 this is 0, not 16
		{"-500000", -0.5}, // nonsense from a router, carried rather than clamped
		// "abc" is NaN in the original, which JSON-encodes as null. This answers
		// 0. The only divergence in the table, and unreachable: RouterOS answers
		// these two keys with a decimal integer or omits them.
		{"abc", 0},
	}
	for _, c := range cases {
		p := talkersFor(t, []routeros.Reply{
			row("x", "02:00:00:00:00:01", c.in, "0"),
		}, 5)
		if got := p.Devices[0].TxMbps; got != c.want {
			t.Errorf("rate-up %q -> %v, want %v", c.in, got, c.want)
		}
	}
}

// TestTheCutKeepsTheBusiest, and keeps exactly topN.
func TestTheCutKeepsTheBusiest(t *testing.T) {
	rows := []routeros.Reply{
		row("quiet", "02:00:00:00:00:01", "1000000", "0"),
		row("loud", "02:00:00:00:00:02", "9000000", "0"),
		row("middling", "02:00:00:00:00:03", "5000000", "0"),
		row("silent", "02:00:00:00:00:04", "0", "0"),
	}
	p := talkersFor(t, rows, 2)
	if len(p.Devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(p.Devices))
	}
	if p.Devices[0].Name != "loud" || p.Devices[1].Name != "middling" {
		t.Errorf("wrong two kept: %+v", p.Devices)
	}
}

// TestTheSortUsesBOTHDIRECTIONS — a device that only downloads must outrank one
// that only uploads half as much, which a tx-only or rx-only sort gets wrong.
func TestTheSortUsesBothDirections(t *testing.T) {
	p := talkersFor(t, []routeros.Reply{
		row("uploader", "02:00:00:00:00:01", "4000000", "0"),
		row("downloader", "02:00:00:00:00:02", "0", "9000000"),
	}, 5)
	if p.Devices[0].Name != "downloader" {
		t.Errorf("order is %+v; the sort must add both directions", p.Devices)
	}
}

// TestATieKeepsInsertionOrder — the sort is stable in both languages, so equal
// totals stay in the order the rows arrived.
func TestATieKeepsInsertionOrder(t *testing.T) {
	p := talkersFor(t, []routeros.Reply{
		row("first", "02:00:00:00:00:01", "1000000", "0"),
		row("second", "02:00:00:00:00:02", "0", "1000000"),
		row("third", "02:00:00:00:00:03", "500000", "500000"),
	}, 5)
	for i, want := range []string{"first", "second", "third"} {
		if p.Devices[i].Name != want {
			t.Fatalf("position %d is %q, want %q — equal totals must keep insertion order: %+v",
				i, p.Devices[i].Name, want, p.Devices)
		}
	}
}

// TestNoDevicesIsStillAvailable. An empty list means nobody is using bandwidth;
// `available: false` is reserved for a router with no kid-control menu at all,
// and the card says something different for each.
func TestNoDevicesIsStillAvailable(t *testing.T) {
	p := talkersFor(t, []routeros.Reply{}, 5)
	if !p.Available {
		t.Error("an empty reading reported available:false, which means 'no such menu'")
	}
	if len(p.Devices) != 0 {
		t.Errorf("got %+v", p.Devices)
	}
}

// TestTheFingerprintIgnoresTheName — a rename alone does not repaint the card.
func TestTheFingerprintIgnoresTheName(t *testing.T) {
	emits := 0
	c := NewTalkers(nil, func(room, event string, payload any) { emits++ }, 30000, 5)
	c.commit([]routeros.Reply{row("before", "02:00:00:00:00:01", "1000000", "0")})
	c.commit([]routeros.Reply{row("after", "02:00:00:00:00:01", "1000000", "0")})
	if emits != 1 {
		t.Errorf("emitted %d times; a rename with unchanged rates must not repaint", emits)
	}
	c.commit([]routeros.Reply{row("after", "02:00:00:00:00:01", "2000000", "0")})
	if emits != 2 {
		t.Errorf("emitted %d times; a rate change must repaint", emits)
	}
}
