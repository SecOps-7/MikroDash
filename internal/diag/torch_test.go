package diag

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestTorchSendsWhatTheCaptureWasTakenWith(t *testing.T) {
	c := readCapture(t, "toolTorch.json", 1)
	ex := c.Exchanges[0]
	iface := strings.TrimPrefix(ex.Params[0], "=interface=")
	secs, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(ex.Params[1], "=duration="), "s"))
	cmd, err := TorchCommand(iface, secs, false)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != ex.Cmd || strings.Join(cmd.Args, " ") != strings.Join(ex.Params, " ") {
		t.Errorf("built %s %v, the capture ran %s %v", cmd.Path, cmd.Args, ex.Cmd, ex.Params)
	}
	if cmd.Timeout <= 0 {
		t.Error("no timeout")
	}
}

// A FLOW'S RATE IS ITS AVERAGE OVER THE SECONDS THAT REPORTED, not its last
// second and not a sum. The capture's api-ssl flow reads 3200 and 6368 bit/s of
// tx in its two seconds; a sum would report 9568 bit/s for a flow that never
// ran that fast, and the last second alone would report 6368.
func TestTorchAveragesEachFlowOverTheRun(t *testing.T) {
	c := readCapture(t, "toolTorch.json", 1)
	r := FoldTorch("ether1", c.Exchanges[0].Rows)
	if r.Reports != 2 {
		t.Fatalf("reports = %d, want the capture's 2 sections", r.Reports)
	}
	if len(r.Flows) != 2 {
		t.Fatalf("got %d flows, want 2 (one per connection, not one per row)", len(r.Flows))
	}
	var api *Flow
	for i := range r.Flows {
		if r.Flows[i].DstPort == "8729" {
			api = &r.Flows[i]
		}
	}
	if api == nil {
		t.Fatal("the api-ssl flow is missing")
	}
	if api.TxBps != (3200+6368)/2 || api.RxBps != (960+1440)/2 || api.Protocol != "tcp" {
		t.Errorf("api flow = %+v, want tx %d rx %d tcp", *api, (3200+6368)/2, (960+1440)/2)
	}
	// BUSIEST FIRST.
	if r.Flows[0].TxBps+r.Flows[0].RxBps < r.Flows[1].TxBps+r.Flows[1].RxBps {
		t.Errorf("flows are not busiest first: %+v", r.Flows)
	}
	var tx int64
	for _, f := range r.Flows {
		tx += f.TxBps
	}
	if r.TotalTxBps != tx {
		t.Errorf("total tx %d, the flows add up to %d", r.TotalTxBps, tx)
	}
}

func TestTorchKeepsOnlyTheBusiestFlows(t *testing.T) {
	var rows []map[string]string
	for i := 0; i < TorchMaxFlows+5; i++ {
		rows = append(rows, map[string]string{".section": "1", "src-address": "198.51.100.1",
			"src-port": strconv.Itoa(1000 + i), "dst-address": "198.51.100.2", "dst-port": "80",
			"ip-protocol": "tcp", "rx": strconv.Itoa(i), "tx": "0"})
	}
	r := FoldTorch("ether1", toReplies(rows))
	if len(r.Flows) != TorchMaxFlows || r.Omitted != 5 {
		t.Fatalf("kept %d, omitted %d; want %d and 5", len(r.Flows), r.Omitted, TorchMaxFlows)
	}
	if r.Flows[len(r.Flows)-1].RxBps != 5 {
		t.Errorf("the quietest kept flow reads %d bit/s, want 5: the quietest were not the ones dropped",
			r.Flows[len(r.Flows)-1].RxBps)
	}
}

// ── THE CARDS DESCRIBE THE INTERFACE, NOT THE TABLE ─────────────────────────
//
// TotalRxBps and TotalTxBps already cover every flow, including those dropped
// past TorchMaxFlows. If the top protocol and top talker were taken from the 25
// that survived, they would disagree with those totals on any busy interface -
// "udp, 90% of rate" would mean 90% of the part that happens to be on screen.
//
// So this builds a run whose BUSIEST flows are all one protocol and one pair of
// addresses, and whose CUT flows are a different protocol carrying more in
// total. Reading only the survivors gets a different answer to reading them all,
// which is what makes this test able to fail.
func TestTorchTopsCountTheFlowsThatWereCut(t *testing.T) {
	var rows []map[string]string
	// 25 kept flows: tcp, 100 bit/s each = 2500 between 10.0.0.1 and 10.0.0.2.
	for i := 0; i < TorchMaxFlows; i++ {
		rows = append(rows, map[string]string{".section": "1", "ip-protocol": "tcp",
			"src-address": "10.0.0.1", "src-port": strconv.Itoa(1000 + i),
			"dst-address": "10.0.0.2", "dst-port": "443", "rx": "100", "tx": "0"})
	}
	// 60 cut flows: udp, 60 bit/s each = 3600 between 10.0.0.3 and 10.0.0.4.
	// Each is quieter than any tcp flow, so every one of them is dropped.
	for i := 0; i < 60; i++ {
		rows = append(rows, map[string]string{".section": "1", "ip-protocol": "udp",
			"src-address": "10.0.0.3", "src-port": strconv.Itoa(2000 + i),
			"dst-address": "10.0.0.4", "dst-port": "53", "rx": "60", "tx": "0"})
	}
	r := FoldTorch("ether1", toReplies(rows))
	if len(r.Flows) != TorchMaxFlows || r.Omitted != 60 {
		t.Fatalf("kept %d omitted %d, want %d and 60 - the fixture no longer cuts anything",
			len(r.Flows), r.Omitted, TorchMaxFlows)
	}
	// THE CONTROL: every flow still on screen is tcp, so a top read from the
	// table would say tcp. The right answer is udp.
	for _, f := range r.Flows {
		if f.Protocol != "udp" {
			continue
		}
		t.Fatal("a udp flow survived the cut; this test can no longer tell the two readings apart")
	}
	if r.TopProtocol != "udp" {
		t.Errorf("top protocol = %q, want udp: 3600 bit/s of it was cut from the table and 2500 of tcp was not",
			r.TopProtocol)
	}
	if want := math.Round(3600.0/6100.0*1000) / 10; r.TopProtocolShare != want {
		t.Errorf("top protocol share = %v, want %v of the interface's 6100 bit/s", r.TopProtocolShare, want)
	}
	if r.TopTalker != "10.0.0.3" && r.TopTalker != "10.0.0.4" {
		t.Errorf("top talker = %q, want one of the cut flows' addresses (3600 bit/s each)", r.TopTalker)
	}
	if r.TopTalkerBps != 3600 {
		t.Errorf("top talker rate = %d, want 3600", r.TopTalkerBps)
	}
}

// An address at BOTH ends of a flow is counted once. Loopback traffic would
// otherwise read as twice the rate it is.
func TestTorchDoesNotCountAnAddressTwiceInOneFlow(t *testing.T) {
	r := FoldTorch("lo", toReplies([]map[string]string{{".section": "1", "ip-protocol": "tcp",
		"src-address": "127.0.0.1", "src-port": "5000", "dst-address": "127.0.0.1", "dst-port": "80",
		"rx": "400", "tx": "600"}}))
	if r.TopTalker != "127.0.0.1" || r.TopTalkerBps != 1000 {
		t.Errorf("top talker = %q at %d bit/s, want 127.0.0.1 at 1000 (not 2000)", r.TopTalker, r.TopTalkerBps)
	}
}

func TestAnEmptyTorchHasAnEmptyList(t *testing.T) {
	if r := FoldTorch("x", nil); r.Flows == nil || r.Reports != 0 {
		t.Errorf("empty run = %+v", r)
	}
}

func TestTorchBounds(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, TorchDefaultSeconds}, {-1, TorchDefaultSeconds}, {1, 1},
		{TorchMaxSeconds, TorchMaxSeconds}, {TorchMaxSeconds + 1, TorchMaxSeconds}, {3600, TorchMaxSeconds},
	} {
		cmd, err := TorchCommand("ether1", tc.in, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := cmd.Args[1]; got != "=duration="+strconv.Itoa(tc.want)+"s" {
			t.Errorf("duration %d sent %s, want %ds", tc.in, got, tc.want)
		}
	}
	for _, ok := range []string{"ether1", "2.4GHz WiFi", "bridge-lan", "pppoe-out1"} {
		if _, err := TorchCommand(ok, 1, false); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "=x", "a\nb", strings.Repeat("a", 65)} {
		if _, err := TorchCommand(bad, 1, false); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
