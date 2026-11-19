package swos

import (
	"testing"
	"time"
)

// ── Proven against REAL captures (testdata/fixtures/swos) ────────────────────

func TestTheSchemeIsReadFromSysB(t *testing.T) {
	for rel, want := range map[string]Scheme{
		"lite-css610-2.21/sys.b":                           SchemeLite,
		"swos-css318-2.19/sys.b":                           SchemeSwOS,
		"pyswitchos-css610-2.21/sys_b_mock_response_1.txt": SchemeLite,
	} {
		got, ok := DetectScheme(parseFixture(t, rel))
		if !ok || got != want {
			t.Errorf("%s: %q %v, want %q", rel, got, ok, want)
		}
	}
	// Doc-only: css106 uses SwOS's mnemonics plus `sip`.
	css106, _ := Parse([]byte("{brd:'5242323630475350',ver:'322e3133',sip:0x0158a8c0}"))
	if got, ok := DetectScheme(css106); !ok || got != SchemeCSS106 {
		t.Errorf("css106: %q %v", got, ok)
	}
	// Anything else is not SwOS: an empty answer, a version without a board.
	for _, blob := range []string{"{}", "{ver:'322e3139'}", "{i06:'322e3231'}", "[]"} {
		v, _ := Parse([]byte(blob))
		if s, ok := DetectScheme(v); ok {
			t.Errorf("%s was taken for SwOS (%q)", blob, s)
		}
	}
}

func TestALiteSwitchIsBuiltFromItsCaptures(t *testing.T) {
	sw := BuildSwitch(SchemeLite,
		parseFixture(t, "lite-css610-2.21/sys.b"), parseFixture(t, "lite-css610-2.21/link.b"),
		parseFixture(t, "lite-css610-2.21/sfp.b"), parseFixture(t, "lite-css610-2.21/poe.b"))
	if sw.Model != "CSS610-8P-2S+" || sw.Version != "2.21" || sw.Identity != "core-switch" ||
		sw.Serial != "SWTEST00001" || sw.MAC != "02:00:00:00:00:1E" || sw.IP != "10.10.10.5" {
		t.Errorf("identity: %+v", sw)
	}
	if sw.UptimeSec != 0x2254 || sw.TempC == nil || *sw.TempC != 70 {
		t.Errorf("uptime %d temp %v", sw.UptimeSec, sw.TempC)
	}
	if len(sw.Ports) != 10 {
		t.Fatalf("%d ports, want 10", len(sw.Ports))
	}
	p1, p4, sfp1, sfp2 := sw.Ports[0], sw.Ports[3], sw.Ports[8], sw.Ports[9]
	if p1.Name != "p1_uplink-a" || !p1.Up || p1.Speed != "1G" || !p1.FullDuplex {
		t.Errorf("port 1: %+v", p1)
	}
	// Down, and its speed index 7 is "no value", not a speed.
	if p4.Up || p4.Speed != "" {
		t.Errorf("port 4: %+v", p4)
	}
	if sfp2.Speed != "10G" || sfp2.SFP == nil || sfp2.SFP.Part != "SFP-10G-SR" ||
		sfp2.SFP.TempC == nil || *sfp2.SFP.TempC != 43 || sfp2.SFP.TxDbm == nil || *sfp2.SFP.TxDbm != -2.56 {
		t.Errorf("SFP+2: %+v %+v", sfp2, sfp2.SFP)
	}
	// A copper DAC: -128 means no temperature sensor, and no light.
	if sfp1.SFP == nil || sfp1.SFP.TempC != nil || sfp1.SFP.TxDbm != nil {
		t.Errorf("SFP+1: %+v", sfp1.SFP)
	}
	if p := sw.Ports[2].PoE; p == nil || p.Status != "powered on" || p.CurrentMA != 301 || p.VoltageV != 27.4 || p.PowerW != 8.2 {
		t.Errorf("port 3 PoE: %+v", p)
	}
	if sw.Ports[0].PoE == nil || sw.Ports[0].PoE.Status != "disabled" || sw.Ports[8].PoE != nil {
		t.Errorf("PoE on port 1 %+v, on SFP+1 %+v", sw.Ports[0].PoE, sw.Ports[8].PoE)
	}
}

func TestAFullSwOSSwitchIsBuiltFromItsCaptures(t *testing.T) {
	sw := BuildSwitch(SchemeSwOS,
		parseFixture(t, "swos-css318-2.19/sys.b"), parseFixture(t, "swos-css318-2.19/link.b"), Value{}, Value{})
	if sw.Model != "CSS318-16G-2S+" || sw.Version != "2.19" || sw.Identity != "MikroTik" ||
		sw.Serial != "SWTEST000318" || sw.MAC != "02:00:00:00:00:31" || sw.IP != "192.168.88.1" {
		t.Errorf("identity: %+v", sw)
	}
	// SwOS counts uptime in centiseconds.
	if sw.UptimeSec != 0x00252e11/100 || sw.TempC == nil || *sw.TempC != 61 {
		t.Errorf("uptime %d temp %v", sw.UptimeSec, sw.TempC)
	}
	if len(sw.Ports) != 18 || sw.Ports[17].Name != "SFP+2" {
		t.Fatalf("ports: %d, last %q", len(sw.Ports), sw.Ports[len(sw.Ports)-1].Name)
	}
	up := 0
	for _, p := range sw.Ports {
		if p.Up {
			up++
		}
	}
	// lnk 0x0001c045: ports 1, 3, 7, 15, 16 and 17.
	if up != 6 || !sw.Ports[16].Up || sw.Ports[16].Speed != "10G" || sw.Ports[0].Speed != "1G" {
		t.Errorf("%d up; SFP+1 %+v; port 1 %+v", up, sw.Ports[16], sw.Ports[0])
	}
}

func TestLiteCountersAre64BitAndOnlyGrow(t *testing.T) {
	earlier := ReadCounters(SchemeLite, parseFixture(t, "lite-css610-2.21/!stats.b.sample2"))
	later := ReadCounters(SchemeLite, parseFixture(t, "lite-css610-2.21/!stats.b"))
	if len(later) != 10 || len(earlier) != 10 {
		t.Fatalf("%d and %d ports", len(earlier), len(later))
	}
	// Port 2's receive counter has wrapped 32 bits once: high word 1.
	if want := uint64(0x379fbb8e) + 1<<32; later[1].RxBytes != want {
		t.Errorf("port 2 rx bytes %d, want %d: the high word was dropped", later[1].RxBytes, want)
	}
	if !later[0].HasRate {
		t.Error("the switch's own rates were not read")
	}
	rates := BuildPorts(earlier, later, 30*time.Second, false)
	for i, r := range rates {
		if r.Reset {
			t.Errorf("port %d reads as reset between two captures where every counter grew", i+1)
		}
		if r.RxBps != later[i].RxBps {
			t.Errorf("port %d: the device's rate was not used", i+1)
		}
	}
}

// ── Doc-based only: python-switchos's field tables, no capture ──────────────

func TestFullSwOSCountersFollowTheDocumentedKeys(t *testing.T) {
	stats, err := Parse([]byte("{rb:[0x00000010,0xffffffff],rbh:[0x00000000,0x00000002]," +
		"tb:[0x00000020,0x00000001],tbh:[0x00000000,0x00000000],rrb:[0x00000064,0x00000000]," +
		"trb:[0x000000c8,0x00000000],rfcs:[0x00000001,0x00000000],tlc:[0x00000002,0x00000000]}"))
	if err != nil {
		t.Fatal(err)
	}
	c := ReadCounters(SchemeSwOS, stats)
	if c[1].RxBytes != 0xffffffff+2<<32 || c[0].TxBytes != 0x20 {
		t.Errorf("counter64: %+v", c)
	}
	// Scale 0.01: the wire value is a hundredth of the bits per second.
	if c[0].RxBps != 10000 || c[0].TxBps != 20000 || c[0].RxErrors != 1 || c[0].TxErrors != 2 {
		t.Errorf("rates and errors: %+v", c[0])
	}
}

func TestBuildPortsSeesAResetAndFallsBackToDeltas(t *testing.T) {
	prev := []Counters{{RxBytes: 1000, TxBytes: 1000, RxErrors: 5}, {RxBytes: 1000, TxBytes: 1000}}
	cur := []Counters{{RxBytes: 2000, TxBytes: 1500, RxErrors: 7}, {RxBytes: 10, TxBytes: 10}}
	r := BuildPorts(prev, cur, 2*time.Second, false)
	if r[0].Reset || r[0].RxBps != 4000 || r[0].TxBps != 2000 || r[0].NewRxErrors != 2 {
		t.Errorf("port 1: %+v", r[0])
	}
	// Counters that went backwards: a reset, never a huge unsigned delta.
	if !r[1].Reset || r[1].RxBps != 0 || r[1].NewRxErrors != 0 {
		t.Errorf("port 2: %+v", r[1])
	}
	// The uptime going backwards resets every port.
	for i, p := range BuildPorts(prev, cur, time.Second, Rebooted(500, 3)) {
		if !p.Reset || p.NewRxErrors != 0 {
			t.Errorf("after a reboot port %d: %+v", i+1, p)
		}
	}
	if Rebooted(3, 500) {
		t.Error("an uptime that grew was read as a reboot")
	}
}

// TestHostileIndexesDoNotPanic: an enum or offset far out of range, as a
// broken or hostile reply could send, is "no value", never a panic.
func TestHostileIndexesDoNotPanic(t *testing.T) {
	sys, _ := Parse([]byte("{i06:'322e3231',i07:'43535336313030'}"))
	link, _ := Parse([]byte("{i0a:['7031','7032'],i06:0x03,i08:[0xffffffffffffffff,0x8000000000000000]}"))
	poe, _ := Parse([]byte("{i04:[0xffffffffffffffff,0x8000000000000000]}"))
	sfp, _ := Parse([]byte("{i01:['41'],i02:['42']}"))
	full, _ := Parse([]byte("{nm:['7031'],prt:0x01,lnk:0x01,spd:[0xffffffffffffffff],sfpo:0xffffffffffffffff}"))
	sw := BuildSwitch(SchemeLite, sys, link, sfp, poe)
	if sw.Ports[0].Speed != "" || sw.Ports[0].PoE != nil || sw.Ports[1].PoE != nil {
		t.Errorf("out-of-range values were read as values: %+v", sw.Ports)
	}
	_ = BuildSwitch(SchemeSwOS, sys, full, sfp, Value{})
}
