package wifiscan

import (
	"encoding/json"
	"os"
	"testing"
)

type admitCorpus struct {
	FleetCap    int   `json:"fleetCap"`
	Durations   []int `json:"durations"`
	MaxChannels int   `json:"maxChannels"`
	Cases       []struct {
		Name string `json:"name"`
		Ctx  struct {
			Iface       any      `json:"iface"`
			DurationSec any      `json:"durationSec"`
			SocketID    string   `json:"socketId"`
			HasROS      bool     `json:"hasRos"`
			Connected   bool     `json:"connected"`
			RouterID    string   `json:"routerId"`
			Interfaces  []string `json:"interfaces"`
		} `json:"ctx"`
		Out struct {
			OK         bool   `json:"ok"`
			Code       string `json:"code"`
			Message    string `json:"message"`
			Iface      string `json:"iface"`
			HasRetryAt bool   `json:"hasRetryAt"`
		} `json:"out"`
	} `json:"cases"`
}

// The catalogue the generator used, restated here because the corpus records
// only the NAMES — the flags are what the guard reads and they belong with the
// test that reads them.
var testIfaces = []Interface{
	{Name: "wifi1", ID: "*1", Master: true},
	{Name: "wifi2-5GHz", ID: "*2", Master: true},
	{Name: "capsman-ap", ID: "*3", Master: true, CapsmanManaged: true},
	{Name: "wifi1-guest", ID: "*4", Master: false},
	{Name: "no-id", ID: "", Master: true},
}

func loadAdmitCorpus(t *testing.T) admitCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/wifiscan-admit-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c admitCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	if c.FleetCap != FleetCap || c.MaxChannels != MaxChannels {
		t.Fatalf("live limits are cap %d / channels %d, this port has %d / %d",
			c.FleetCap, c.MaxChannels, FleetCap, MaxChannels)
	}
	if len(c.Durations) != len(Durations) {
		t.Fatalf("live offers %v, this port offers %v", c.Durations, Durations)
	}
	for i := range c.Durations {
		if c.Durations[i] != Durations[i] {
			t.Fatalf("live offers %v, this port offers %v", c.Durations, Durations)
		}
	}
	return c
}

func TestAdmitMatchesTheLiveGuard(t *testing.T) {
	c := loadAdmitCorpus(t)
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			req := AdmitRequest{
				RouterID: tc.Ctx.RouterID, HasROS: tc.Ctx.HasROS, Connected: tc.Ctx.Connected,
				SocketID: tc.Ctx.SocketID,
			}
			// A non-string interface reaches the live guard's typeof test. Go has
			// no such value, so it is modelled as the empty string, which fails the
			// same check for the same reason and gives the same code.
			if s, ok := tc.Ctx.Iface.(string); ok {
				req.Iface = s
			}
			if d, ok := tc.Ctx.DurationSec.(float64); ok {
				req.DurationSec = int(d)
			}
			if tc.Ctx.Interfaces != nil {
				req.InterfacesKnown = true
				req.Interfaces = testIfaces
			}

			st := State{Running: map[string]string{}, Cooldowns: map[string]int64{}, Now: 1_000_000}
			// The stateful cases, rebuilt from their names rather than replayed:
			// what matters is the STATE the guard sees.
			switch tc.Name {
			case "a scan is already running on this router":
				st.Running["r1"] = "wifi2-5GHz"
			case "the fleet cap is reached":
				st.Running["r1"], st.Running["r2"], st.Running["r3"] = "wifi1", "wifi1", "wifi1"
			case "the fleet is one short of the cap":
				st.Running["r1"], st.Running["r2"] = "wifi1", "wifi1"
			}

			got := Admit(req, st)
			if got.OK != tc.Out.OK {
				t.Errorf("ok=%v, live=%v (code %q vs %q)", got.OK, tc.Out.OK, got.Code, tc.Out.Code)
			}
			if got.Code != tc.Out.Code {
				t.Errorf("code %q, live %q", got.Code, tc.Out.Code)
			}
			if got.Message != tc.Out.Message {
				t.Errorf("message %q, live %q", got.Message, tc.Out.Message)
			}
			if got.Iface != tc.Out.Iface {
				t.Errorf("iface %q, live %q", got.Iface, tc.Out.Iface)
			}
		})
	}
}

// TestTheGuardRefusesAndPermits, stated independently of the corpus: a guard
// that always said yes, or always no, would match a corpus of only one kind.
func TestTheGuardRefusesAndPermits(t *testing.T) {
	c := loadAdmitCorpus(t)
	yes, no := 0, 0
	for _, tc := range c.Cases {
		if tc.Out.OK {
			yes++
		} else {
			no++
		}
	}
	if yes == 0 {
		t.Error("no corpus case is admitted -- nothing proves the guard can say yes")
	}
	if no == 0 {
		t.Error("no corpus case is refused -- nothing proves the guard can say no")
	}
}

// TestTheCooldownIsNotCorpusPinned, and says so.
//
// `COOLDOWN_MS` is not exported by the live module, so the generator cannot lift
// it the way it lifts FLEET_CAP, DURATIONS and MAX_CHANNELS. This tests the
// BEHAVIOUR against the port's own constant and records the gap: if the live
// value changes, nothing here will notice.
func TestTheCooldownIsNotCorpusPinned(t *testing.T) {
	c := loadAdmitCorpus(t)
	raw, err := os.ReadFile("../../testdata/wifiscan-admit-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if _, present := probe["cooldownMs"]; present {
		t.Error("the corpus now carries cooldownMs -- pin it here and delete this test's premise")
	}
	if flagged, _ := probe["cooldownMsIsNotExported"].(bool); !flagged {
		t.Error("the corpus no longer records that the cooldown is unpinnable")
	}
	_ = c

	ok := AdmitRequest{RouterID: "r1", HasROS: true, Connected: true, Iface: "wifi1",
		DurationSec: 30, SocketID: "s1", Interfaces: testIfaces, InterfacesKnown: true}

	// A ZERO timestamp is a real value, and `if (last)` would skip it. The live
	// comment says so explicitly, so it is tested explicitly.
	st := State{Running: map[string]string{}, Cooldowns: map[string]int64{"s1": 0}, Now: 5_000}
	if v := Admit(ok, st); v.Code != "cooldown" {
		t.Errorf("a cooldown recorded at timestamp 0 was ignored: %+v", v)
	}
	if v := Admit(ok, st); v.RetryAt != CooldownMs {
		t.Errorf("retryAt %d, want %d", v.RetryAt, CooldownMs)
	}

	// Exactly at the boundary the cooldown is OVER: the live test is `<`.
	st.Now = CooldownMs
	if v := Admit(ok, st); !v.OK {
		t.Errorf("a scan exactly CooldownMs later was refused: %+v", v)
	}
	// One millisecond short, it is not.
	st.Now = CooldownMs - 1
	if v := Admit(ok, st); v.Code != "cooldown" {
		t.Errorf("a scan one ms early was allowed: %+v", v)
	}
	// A DIFFERENT socket is unaffected -- the cooldown is per operator, not per
	// router, so one busy operator cannot lock the fleet.
	other := ok
	other.SocketID = "s2"
	if v := Admit(other, st); !v.OK {
		t.Errorf("another socket was caught by s1's cooldown: %+v", v)
	}
}

func TestHMSMatchesRouterOS(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{30, "00:00:30"}, {60, "00:01:00"}, {120, "00:02:00"},
		{0, "00:00:00"}, {59, "00:00:59"}, {61, "00:01:01"},
		{600, "00:10:00"}, {-5, "00:00:00"},
	} {
		if got := HMS(tc.in); got != tc.want {
			t.Errorf("HMS(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
