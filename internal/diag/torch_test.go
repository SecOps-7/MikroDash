package diag

import (
	"strconv"
	"strings"
	"testing"
)

func TestTorchSendsWhatTheCaptureWasTakenWith(t *testing.T) {
	c := readCapture(t, "toolTorch.json", 1)
	ex := c.Exchanges[0]
	iface := strings.TrimPrefix(ex.Params[0], "=interface=")
	secs, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(ex.Params[1], "=duration="), "s"))
	cmd, err := TorchCommand(iface, secs)
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
		cmd, err := TorchCommand("ether1", tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := cmd.Args[1]; got != "=duration="+strconv.Itoa(tc.want)+"s" {
			t.Errorf("duration %d sent %s, want %ds", tc.in, got, tc.want)
		}
	}
	for _, ok := range []string{"ether1", "2.4GHz WiFi", "bridge-lan", "pppoe-out1"} {
		if _, err := TorchCommand(ok, 1); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "=x", "a\nb", strings.Repeat("a", 65)} {
		if _, err := TorchCommand(bad, 1); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
