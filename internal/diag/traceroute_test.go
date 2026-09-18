package diag

import (
	"strconv"
	"strings"
	"testing"
)

func TestTracerouteSendsWhatTheCaptureWasTakenWith(t *testing.T) {
	c := readCapture(t, "toolTraceroute.json", 2)
	for _, ex := range c.Exchanges {
		addr := strings.TrimPrefix(ex.Params[0], "=address=")
		hops, _ := strconv.Atoi(strings.TrimPrefix(ex.Params[2], "=max-hops="))
		cmd, err := TracerouteCommand(addr, hops)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if cmd.Path != ex.Cmd || strings.Join(cmd.Args, " ") != strings.Join(ex.Params, " ") {
			t.Errorf("built %s %v, the capture ran %s %v", cmd.Path, cmd.Args, ex.Cmd, ex.Params)
		}
		if cmd.Timeout <= 0 {
			t.Errorf("%s: no timeout", addr)
		}
	}
}

func TestATracerouteThatArrivesIsOneHop(t *testing.T) {
	c := readCapture(t, "toolTraceroute.json", 2)
	r := FoldTraceroute("1.1.1.1", c.Exchanges[0].Rows)
	if len(r.Hops) != 1 || r.Error != "" {
		t.Fatalf("got %d hops and error %q, want 1 and none", len(r.Hops), r.Error)
	}
	h := r.Hops[0]
	if h.Hop != 1 || h.Address != "1.1.1.1" || h.TimedOut || h.LossPct != 0 || h.LastMs == nil || *h.LastMs != 15.4 {
		t.Errorf("hop = %+v, want hop 1, 1.1.1.1, 15.4 ms, no loss", h)
	}
}

// THE LAST SECTION IS THE TABLE. Every earlier section is a partial snapshot
// holding the hop still being probed as a row with no address and a time of 0;
// folding all of them would report nine hops, some answering in 0 ms.
func TestOnlyTheLastSectionIsTheRoute(t *testing.T) {
	c := readCapture(t, "toolTraceroute.json", 2)
	rows := c.Exchanges[1].Rows
	if len(rows) <= 3 {
		t.Fatalf("the capture has %d rows; the control needs the earlier sections too", len(rows))
	}
	r := FoldTraceroute("198.51.100.1", rows)
	if len(r.Hops) != 3 {
		t.Fatalf("got %d hops, want the last section's 3", len(r.Hops))
	}
	for i, h := range r.Hops {
		if h.Hop != i+1 || !h.TimedOut || h.LastMs != nil || h.LossPct != 100 {
			t.Errorf("hop %d = %+v, want a timeout with 100%% loss and no time", i+1, h)
		}
	}
	if r.Error != "Too many hops" {
		t.Errorf("error = %q, want the router's 'Too many hops'", r.Error)
	}
}

func TestAnEmptyTracerouteHasAnEmptyList(t *testing.T) {
	if r := FoldTraceroute("x", nil); r.Hops == nil {
		t.Error("Hops is nil")
	}
}

func TestTracerouteBounds(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, TracerouteDefaultHops}, {-1, TracerouteDefaultHops}, {1, 1},
		{TracerouteMaxHops, TracerouteMaxHops}, {TracerouteMaxHops + 1, TracerouteMaxHops}, {255, TracerouteMaxHops},
	} {
		cmd, err := TracerouteCommand("198.51.100.1", tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := cmd.Args[2]; got != "=max-hops="+strconv.Itoa(tc.want) {
			t.Errorf("max-hops %d sent %s, want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "a b", "-x", "a=b"} {
		if _, err := TracerouteCommand(bad, 1); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
