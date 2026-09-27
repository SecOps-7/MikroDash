package diag

import (
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// snifferCapture replays the fixture: the state row, the packet table, the
// protocol table and the host table, in the order the page reads them.
func snifferCapture(t *testing.T) (state, packets, protocols, hosts []routeros.Reply) {
	t.Helper()
	c := readCapture(t, "toolSniffer.json", 4)
	by := map[string][]routeros.Reply{}
	for _, ex := range c.Exchanges {
		by[ex.Cmd] = ex.Rows
	}
	for _, want := range []string{"/tool/sniffer/print", "/tool/sniffer/packet/print",
		"/tool/sniffer/protocol/print", "/tool/sniffer/host/print"} {
		if len(by[want]) == 0 {
			t.Fatalf("the capture holds no rows for %s", want)
		}
	}
	return by["/tool/sniffer/print"], by["/tool/sniffer/packet/print"],
		by["/tool/sniffer/protocol/print"], by["/tool/sniffer/host/print"]
}

// THE PACKET TABLE IS READ WITH THE PROPLIST THE CAPTURE WAS TAKEN WITH, and
// `data` is not in it. A proplist that drifted from the capture would be
// replaying rows the router was never asked for, and adding `data` back would
// pull a hex dump of every packet across the wire for a column nothing draws.
func TestSnifferReadsThePacketTableWithoutItsHexDump(t *testing.T) {
	c := readCapture(t, "toolSniffer.json", 4)
	var params []string
	for _, ex := range c.Exchanges {
		if ex.Cmd == "/tool/sniffer/packet/print" {
			params = ex.Params
		}
	}
	want := "=.proplist=" + SnifferPacketProps
	if len(params) != 1 || params[0] != want {
		t.Errorf("the capture read the packet table with %v; this app sends %q", params, want)
	}
	if strings.Contains(SnifferPacketProps, "data") {
		t.Error("SnifferPacketProps asks for `data`, the per-packet hex dump")
	}
}

// ── THE TOTALS ARE THE CAPTURE'S, NOT THE ROWS' ─────────────────────────────
//
// The protocol table nests three levels deep, so summing all of it triple-counts
// every IP packet: 332 top-level against 1288 for the whole list. And the packet
// table holds only what the router's memory buffer still has - 120 rows for a
// capture of 332 packets - so counting the rows understates it. Both wrong
// answers look entirely plausible on a card.
func TestSnifferTotalsComeFromTheTopLevelProtocolRows(t *testing.T) {
	state, packets, protocols, hosts := snifferCapture(t)
	r := FoldSniffer(state[0]["running"] == "true", packets, protocols, hosts)

	if r.TotalPackets != 332 || r.TotalBytes != 101208 {
		t.Errorf("totals = %d packets / %d bytes, want the capture's 332 / 101208",
			r.TotalPackets, r.TotalBytes)
	}
	if r.TotalPackets == len(packets) {
		t.Error("the totals were counted from the packet rows, which the memory buffer has cut")
	}
	var everyRow int
	for _, p := range protocols {
		everyRow += atoi(p["packets"])
	}
	if r.TotalPackets == everyRow {
		t.Errorf("the totals summed the whole nested table (%d), counting each IP packet three times",
			everyRow)
	}
	if !r.Running {
		t.Error("the capture was running and the frame says it was not")
	}
}

// The busiest IP protocol, not the MAC protocol above it: "ip, 98% of bytes"
// is true of almost every capture and tells nobody anything.
func TestSnifferTopProtocolIsTheBusiestIPProtocol(t *testing.T) {
	state, packets, protocols, hosts := snifferCapture(t)
	r := FoldSniffer(state[0]["running"] == "true", packets, protocols, hosts)
	if r.TopProtocol != "tcp" || r.TopProtocolShare != 96.76 {
		t.Errorf("top protocol = %q at %v%%, want tcp at 96.76", r.TopProtocol, r.TopProtocolShare)
	}
	// AND A CAPTURE WITH NO IP IN IT falls back to the MAC protocol, which is
	// then the most specific answer there is.
	only := []routeros.Reply{
		{"protocol": "arp", "packets": "9", "bytes": "540", "share": "76.05"},
		{"protocol": "lldp", "packets": "1", "bytes": "170", "share": "23.94"},
	}
	if name, share := topProtocol(only); name != "arp" || share != 76.05 {
		t.Errorf("an ARP-only capture reads %q at %v%%, want arp at 76.05", name, share)
	}
	// A PORT ROW IS NEVER THE ANSWER, and the reason is a property of the table
	// rather than a check in the code - see topProtocol's note on the mutation
	// that survived. A row naming a port answers with its PROTOCOL, so a port
	// number can never reach the card whichever row wins.
	if name, share := topProtocol([]routeros.Reply{
		{"protocol": "ip", "ip-protocol": "tcp", "bytes": "100", "share": "50"},
		{"protocol": "ip", "ip-protocol": "tcp", "port": "8291 (winbox)", "bytes": "100", "share": "50"},
	}); name != "tcp" || share != 50 {
		t.Errorf("top protocol = %q at %v%%, want tcp at 50 - a port reached the card", name, share)
	}
}

// `total` is `rx/tx`, and the top talker is the two halves together.
func TestSnifferTopTalkerAddsBothDirections(t *testing.T) {
	state, packets, protocols, hosts := snifferCapture(t)
	r := FoldSniffer(state[0]["running"] == "true", packets, protocols, hosts)
	if r.TopTalker != "198.51.100.10" || r.TopTalkerBytes != 10692+90204 {
		t.Errorf("top talker = %s at %d bytes, want 198.51.100.10 at %d",
			r.TopTalker, r.TopTalkerBytes, 10692+90204)
	}
	// A TIE IS BROKEN BY ADDRESS, not by the order the router listed them: the
	// two ends of one conversation carry the same byte count, and a card that
	// alternated between them every poll would read as broken.
	tie := []routeros.Reply{
		{"address": "198.51.100.20", "total": "10/10"},
		{"address": "198.51.100.19", "total": "10/10"},
	}
	if name, _ := topTalker(tie); name != "198.51.100.19" {
		t.Errorf("a tie chose %s, want the lower address", name)
	}
	// NOTHING SEEN IS NOTHING SHOWN, rather than the first address at zero.
	if name, n := topTalker([]routeros.Reply{{"address": "198.51.100.20", "total": "0/0"}}); name != "" || n != 0 {
		t.Errorf("an idle capture named %q at %d, want no talker", name, n)
	}
}

// NEWEST FIRST, and cut from that end. The router hands the buffer back oldest
// first, so a cut taken from the front would carry the oldest packets of a
// capture the operator is watching happen.
func TestSnifferCarriesTheNewestPacketsFirst(t *testing.T) {
	state, packets, protocols, hosts := snifferCapture(t)
	r := FoldSniffer(state[0]["running"] == "true", packets, protocols, hosts)
	if len(r.Packets) != len(packets) {
		t.Fatalf("carried %d of %d rows - the fixture is under the cap and all of it should be here",
			len(r.Packets), len(packets))
	}
	last := packets[len(packets)-1]
	if r.Packets[0].Num != atoi(last["num"]) {
		t.Errorf("the first row carried is packet %d; the newest the router held is %s",
			r.Packets[0].Num, last["num"])
	}
	for i := 1; i < len(r.Packets); i++ {
		if r.Packets[i].Num > r.Packets[i-1].Num {
			t.Fatalf("row %d (packet %d) is newer than the one before it (%d)",
				i, r.Packets[i].Num, r.Packets[i-1].Num)
		}
	}

	// PAST THE CAP, the NEWEST SnifferMaxPackets are kept. Cutting from the
	// other end is the plausible mistake and it produces a table of the oldest
	// packets of a capture somebody is watching happen, which looks like a table
	// that has simply stopped updating.
	var many []routeros.Reply
	for i := 1; i <= SnifferMaxPackets+7; i++ {
		many = append(many, routeros.Reply{"num": itoa(i)})
	}
	cut := FoldSniffer(true, many, nil, nil)
	if len(cut.Packets) != SnifferMaxPackets {
		t.Errorf("cut to %d rows, want %d", len(cut.Packets), SnifferMaxPackets)
	}
	if cut.Packets[0].Num != SnifferMaxPackets+7 {
		t.Errorf("the cut kept packet %d first, want the newest (%d)", cut.Packets[0].Num, SnifferMaxPackets+7)
	}
	if cut.Packets[len(cut.Packets)-1].Num != 8 {
		t.Errorf("the cut kept packet %d last, want %d - it cut the wrong end",
			cut.Packets[len(cut.Packets)-1].Num, 8)
	}
}

// A packet with no IP end - ARP, 802.2, LLDP - is shown by its MACs and its MAC
// protocol, because the alternative is three empty columns on the rows that are
// usually the interesting ones.
func TestSnifferShowsAFrameWithNoIPEndByItsMACs(t *testing.T) {
	r := FoldSniffer(true, []routeros.Reply{{
		"num": "1", "interface": "ether1", "direction": "rx", "protocol": "arp",
		"src-mac": "02:00:00:00:00:01", "dst-mac": "02:00:00:00:00:02", "size": "60",
	}}, nil, nil)
	p := r.Packets[0]
	if p.Source != "02:00:00:00:00:01" || p.Dest != "02:00:00:00:00:02" || p.Protocol != "arp" {
		t.Errorf("an ARP frame folded to %+v", p)
	}
	// And an IP packet keeps its addresses and its IP protocol, not the `ip`
	// above it.
	ip := FoldSniffer(true, []routeros.Reply{{
		"num": "2", "src-address": "198.51.100.10:8728", "dst-address": "198.51.100.11:44132",
		"protocol": "ip", "ip-protocol": "tcp", "size": "61",
	}}, nil, nil).Packets[0]
	if ip.Source != "198.51.100.10:8728" || ip.Protocol != "tcp" {
		t.Errorf("an IP packet folded to %+v", ip)
	}
}

// ── EVERY FILTER IS WRITTEN, INCLUDING THE ONES THE PAGE DOES NOT OFFER ─────
//
// A capture that sent only the filled-in boxes would inherit whatever was last
// configured on the router, and an inherited `filter-src-port` narrows the
// capture with nothing on the page to say so.
func TestSnifferSetClearsEveryFilterItDoesNotUse(t *testing.T) {
	args, err := SnifferSetArgs(SnifferFilters{Interface: "ether1", IPProtocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range args {
		k, v, ok := strings.Cut(strings.TrimPrefix(a, "="), "=")
		if !ok {
			t.Fatalf("%q is not an =key=value word", a)
		}
		got[k] = v
	}
	// The fixture's own state row lists every property the sniffer has, so the
	// list of filters to clear is read from the ROUTER rather than typed twice.
	state, _, _, _ := snifferCapture(t)
	set := map[string]bool{"filter-interface": true, "filter-direction": true,
		"filter-ip-protocol": true, "filter-port": true}
	for k := range state[0] {
		if !strings.HasPrefix(k, "filter-") {
			continue
		}
		// The streaming filter and the entry operator are not about WHAT is
		// captured: one ignores packets bound for a TZSP receiver, the other is
		// left at the router's default deliberately (see the package header).
		if k == "filter-stream" || k == "filter-operator-between-entries" {
			continue
		}
		v, sent := got[k]
		if !sent {
			t.Errorf("%s is a sniffer filter and the set sentence does not write it, so a value "+
				"left on the router narrows the capture silently", k)
			continue
		}
		if !set[k] && v != "" {
			t.Errorf("%s is not a filter this page offers and is set to %q, not cleared", k, v)
		}
	}
	for _, want := range []struct{ k, v string }{
		{"filter-interface", "ether1"}, {"filter-ip-protocol", "tcp"},
		{"filter-direction", "any"}, {"only-headers", "no"}, {"file-name", ""},
		{"memory-limit", "1000"}, {"memory-scroll", "yes"},
	} {
		if got[want.k] != want.v {
			t.Errorf("%s = %q, want %q", want.k, got[want.k], want.v)
		}
	}
	// AND NOT `filter-operator-between-entries`: see the package header. Two
	// measured runs could not tell `and` from `or`, so this app does not write
	// a value whose effect it could not observe.
	if _, ok := got["filter-operator-between-entries"]; ok {
		t.Error("the set sentence writes filter-operator-between-entries")
	}
}

// One address box, two RouterOS properties: v6 in the v4 property is refused by
// the router, and an operator typing an address does not know which is which.
func TestSnifferSplitsAddressesByFamily(t *testing.T) {
	args, err := SnifferSetArgs(SnifferFilters{Address: "198.51.100.0/24, 2001:db8::1 ,198.51.100.7"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "=filter-ip-address=198.51.100.0/24,198.51.100.7 ") {
		t.Errorf("the v4 half is wrong: %s", joined)
	}
	if !strings.Contains(joined, "=filter-ipv6-address=2001:db8::1 ") {
		t.Errorf("the v6 half is wrong: %s", joined)
	}
}

// The refusals say what was wrong. Each case is one the form can produce.
func TestSnifferRefusesWhatTheRouterWould(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    SnifferFilters
		want error
	}{
		{"an interface name with a control character", SnifferFilters{Interface: "ether\x011"}, ErrSnifferInterface},
		{"a leading = in the interface", SnifferFilters{Interface: "=x"}, ErrSnifferInterface},
		{"a direction that is not one", SnifferFilters{Direction: "both"}, ErrSnifferDirection},
		{"a protocol RouterOS has no name for", SnifferFilters{IPProtocol: "quic"}, ErrSnifferProtocol},
		{"a protocol number past 255", SnifferFilters{IPProtocol: "256"}, ErrSnifferProtocol},
		{"a port that is a word", SnifferFilters{Port: "ssh"}, ErrSnifferPort},
		{"port 0", SnifferFilters{Port: "0"}, ErrSnifferPort},
		{"port 65536", SnifferFilters{Port: "65536"}, ErrSnifferPort},
		{"seventeen ports", SnifferFilters{Port: strings.Repeat("80,", SnifferMaxEntries) + "80"}, ErrSnifferPort},
		{"an address that is a host name", SnifferFilters{Address: "example.com"}, ErrSnifferAddress},
		{"a prefix that is not one", SnifferFilters{Address: "198.51.100.0/33"}, ErrSnifferAddress},
		{"seventeen addresses of one family",
			SnifferFilters{Address: strings.Repeat("198.51.100.1,", SnifferMaxEntries) + "198.51.100.1"},
			ErrSnifferAddress},
	} {
		if _, err := SnifferSetArgs(tc.f); err != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	// AND WHAT IT ACCEPTS. Every documented protocol name, a protocol number,
	// an interface with a space in it, and every direction.
	for _, p := range SnifferIPProtocols {
		if _, err := SnifferSetArgs(SnifferFilters{IPProtocol: p}); err != nil {
			t.Errorf("the documented protocol %q was refused: %v", p, err)
		}
	}
	for _, d := range SnifferDirections {
		if _, err := SnifferSetArgs(SnifferFilters{Direction: d}); err != nil {
			t.Errorf("direction %q was refused: %v", d, err)
		}
	}
	for _, ok := range []SnifferFilters{
		{}, {Interface: "2.4GHz WiFi"}, {IPProtocol: "47"}, {Port: "53,443"},
		{Address: "2001:db8::/32"},
	} {
		if _, err := SnifferSetArgs(ok); err != nil {
			t.Errorf("%+v was refused: %v", ok, err)
		}
	}
}

// itoa keeps the test free of strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
