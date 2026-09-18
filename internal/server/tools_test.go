package server

import (
	"strconv"
	"strings"
	"testing"

	"mikrodash/internal/diag"
	"mikrodash/internal/session"
)

// TestTorchsSummaryListsTheBusiestTen. The approved `torch` action's outcome is
// drawn in the operator's chat: the totals, then at most ten flows, one to a
// line, busiest first as the result already orders them. A port-less flow
// (icmp) has no dangling colon.
func TestTorchsSummaryListsTheBusiestTen(t *testing.T) {
	r := &diag.TorchResult{Interface: "ether1", Seconds: 5, TotalRxBps: 1200, TotalTxBps: 34}
	r.Flows = append(r.Flows, diag.Flow{Protocol: "icmp", SrcAddr: "198.51.100.9", DstAddr: "198.51.100.2", RxBps: 900})
	for i := 0; i < 12; i++ {
		r.Flows = append(r.Flows, diag.Flow{Protocol: "tcp", SrcAddr: "198.51.100.1",
			SrcPort: strconv.Itoa(1000 + i), DstAddr: "198.51.100.2", DstPort: "443", RxBps: 100})
	}
	got := torchSummary(r)
	if !strings.Contains(got, "Average rx 1200 bit/s, tx 34 bit/s") {
		t.Errorf("the totals are missing:\n%s", got)
	}
	if n := strings.Count(got, "\n- "); n != 10 {
		t.Errorf("%d flow lines, want 10:\n%s", n, got)
	}
	if !strings.Contains(got, "icmp 198.51.100.9 -> 198.51.100.2:") {
		t.Errorf("a port-less flow is not written as bare addresses:\n%s", got)
	}
	if strings.Contains(got, "ROUTER-DATA") {
		t.Errorf("the operator's text carries the model's untrusted-block markers:\n%s", got)
	}
	// The control: a run that saw nothing says so.
	if got := torchSummary(&diag.TorchResult{Interface: "ether1", Seconds: 5}); !strings.Contains(got, "No traffic") {
		t.Errorf("an empty run reads %q", got)
	}
}

// TestADiagnosticNeedsARouterAndThePagesPermission: against a bare connection,
// every page handler refuses before anything is sent — unavailable first, since
// there is no router to be denied on.
func TestADiagnosticNeedsARouterAndThePagesPermission(t *testing.T) {
	cn := &conn{}
	var got []string
	cn.startTool("write", func(code string) { got = append(got, code) }, func(*session.Session) {
		t.Error("a run started on a connection with no router")
	})
	if len(got) != 1 || got[0] != "unavailable" {
		t.Errorf("refusals = %v, want [unavailable]", got)
	}
	if cn.toolBusy.Load() {
		t.Error("a refused run took the connection's run slot")
	}
}
