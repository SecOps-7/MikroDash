package collect

import (
	"os"
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// mustReadSource reads a file in this package. The one source-level assertion
// below needs it; every other test here drives the functions directly.
func mustReadSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// The merge rule two collectors depend on, and neither owns.
//
// ── WHY IT IS TESTED HERE AND NOT THROUGH EITHER COLLECTOR ─────────────────
//
// A holder losing an interface it asked for is silent: the channel is perfectly
// healthy for everybody else, nothing errors, and the symptom is one blank
// throughput column or one empty chart. A holder losing its interval is quieter
// still — the chart simply draws fewer points and looks like a slow network.
//
// Both are properties of this function, so they are asserted on it directly
// rather than inferred from a collector that happens to call it.

func argOf(cmd routeros.Cmd, prefix string) string {
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return ""
}

func TestMergeMonitorTrafficTakesTheUnionAndTheFinestInterval(t *testing.T) {
	rates := monitorTrafficCmd([]string{"ether2", "ether1"}, 5) // ifStatus
	chart := monitorTrafficCmd([]string{"wlan1", "ether2"}, 1)  // traffic

	got := mergeMonitorTraffic([]routeros.Cmd{rates, chart})

	// THE UNION. Neither holder's set contains the other's — `ifStatus` skips
	// disabled interfaces and a viewer can select one — so dropping either
	// side's names is a blank column or an empty chart with nothing to see.
	if names := argOf(got, "=interface="); names != "ether1,ether2,wlan1" {
		t.Errorf("merged interfaces = %q, want the union ether1,ether2,wlan1", names)
	}
	// THE FINEST. The chart draws a point a second and is the holder with
	// resolution to lose; a merged channel at five seconds would look like a
	// quiet network rather than like a bug.
	if iv := argOf(got, "=interval="); iv != "1" {
		t.Errorf("merged interval = %q, want 1", iv)
	}
	// AND THE UNION OF THE FIELDS, so `traffic`'s link state survives a merge
	// with a holder that does not read it.
	props := argOf(got, "=.proplist=")
	for _, f := range []string{"name", "rx-bits-per-second", "tx-bits-per-second",
		"running", "disabled"} {
		if !strings.Contains(props, f) {
			t.Errorf("merged proplist %q is missing %q", props, f)
		}
	}
}

// TestMergeMonitorTrafficIsOrderIndependent. The command is compared against the
// open one to decide whether to restart the channel, so a merge that depended on
// holder order would restart it on every join — a gap in every holder's data for
// nothing.
func TestMergeMonitorTrafficIsOrderIndependent(t *testing.T) {
	a := monitorTrafficCmd([]string{"ether1"}, 5)
	b := monitorTrafficCmd([]string{"wlan1"}, 1)
	one := mergeMonitorTraffic([]routeros.Cmd{a, b})
	two := mergeMonitorTraffic([]routeros.Cmd{b, a})
	if strings.Join(one.Args, "|") != strings.Join(two.Args, "|") {
		t.Errorf("the merge depends on holder order:\n  %v\n  %v", one.Args, two.Args)
	}
}

// TestTheChartAsksForOneSecond pins the claim `syncStream` makes.
//
// ── THE MUTATION THAT EXPOSED THIS GAP ─────────────────────────────────────
//
// Changing the chart's interval from 1 to 5 was caught only incidentally — the
// staleness window is two intervals, so the watchdog test stopped seeing a
// restart in time and failed for a reason that had nothing to do with
// resolution. Nothing asserted the number itself, so a merge that quietly ran at
// five seconds would have drawn a fifth of the points with every test green.
func TestTheChartAsksForOneSecond(t *testing.T) {
	src := mustReadSource(t, "traffic.go")
	// ON ITS OWN CHANNEL since 2026-11-22 (trafficChannel): an interface
	// ifStatus watches vanishing must not end the chart's stream.
	if !strings.Contains(src, "joinMonitorTraffic(t.cache, trafficChannel, names, 1, t.onPacket)") {
		t.Error("traffic no longer joins its own channel at one second. The chart " +
			"draws a point per second, so a coarser interval looks like a quiet " +
			"network, not like a bug; and on the shared channel an interface it does " +
			"not show vanishing ends its stream.")
	}
}

// TestMonitorTrafficNeverAsksForMoreThanFiveSeconds. Measured on CHR Test
// (7.24.5): `=interval=6` is refused, "value of interval is out of range
// (00:00:00.020 .. 00:00:05)". `ifStatus` polls up to a minute, and at any
// cadence above five its join was refused and it fell back to `=once=` reads.
func TestMonitorTrafficNeverAsksForMoreThanFiveSeconds(t *testing.T) {
	for _, sec := range []int{6, 30, 60} {
		cmd := monitorTrafficCmd([]string{"ether1"}, sec)
		if got := cmd.Args[1]; got != "=interval=5" {
			t.Errorf("asked at %ds, the channel opens with %q; RouterOS refuses above 5", sec, got)
		}
	}
	// CONTROL: an interval it accepts is passed through.
	if got := monitorTrafficCmd([]string{"ether1"}, 3).Args[1]; got != "=interval=3" {
		t.Errorf("asked at 3s, the channel opens with %q", got)
	}
}
