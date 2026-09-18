package server

import (
	"encoding/json"
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

// TestABandwidthTestsAuditRowHasNoPassword. The request carries the far server's
// password; the audit row is built from the same request, and must hold the
// user — the control, which shows the row is built from it at all — and not the
// password, in any field.
func TestABandwidthTestsAuditRowHasNoPassword(t *testing.T) {
	req := toolsBtestReq{Address: "198.51.100.53", User: "md-btest", Password: "hunter2-not-for-the-log",
		Protocol: "tcp", Direction: "both"}
	b, err := json.Marshal(btestAudit("r1", req, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "md-btest") {
		t.Fatalf("the audit row does not carry the user, so this check proves nothing: %s", b)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Errorf("the audit row carries the password: %s", b)
	}
}

// TestATunnelsDefaultRouteIsTheRouteItInstalls. tunnelDefault hands the route
// guard 0.0.0.0/0 exactly while the client is enabled with add-default-route on,
// in either spelling of a checkbox; off, or disabled, there is no route.
func TestATunnelsDefaultRouteIsTheRouteItInstalls(t *testing.T) {
	for _, on := range []string{"yes", "true"} {
		r := tunnelDefaultRoute(map[string]string{"name": "ovpn-out1", "addDefaultRoute": on, "disabled": "no"})
		if !r.Present || r.Dst != "0.0.0.0/0" || r.Gateway != "ovpn-out1" {
			t.Errorf("add-default-route=%s gave %+v, want 0.0.0.0/0 through ovpn-out1", on, r)
		}
	}
	for name, v := range map[string]map[string]string{
		"off":      {"name": "c", "addDefaultRoute": "no"},
		"disabled": {"name": "c", "addDefaultRoute": "yes", "disabled": "true"},
		"absent":   {"name": "c"},
	} {
		if r := tunnelDefaultRoute(v); r.Present {
			t.Errorf("%s client installs a route: %+v", name, r)
		}
	}
}
