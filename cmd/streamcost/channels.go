package main

// B.0b of the collector rewrite: HOW MANY CONCURRENT CHANNELS DOES ONE ROUTER
// TOLERATE.
//
// ── WHY THIS IS A DIFFERENT QUESTION FROM B.0 ───────────────────────────────
//
// B.0 asked what one WIDE channel costs, and answered it: streaming thirty
// interfaces instead of two is 2.6 KB/s, one channel either way, no measurable
// CPU. The finding that dissolved that trade was "one stream is one channel
// whether its comma list holds two interfaces or thirty".
//
// Track B now proposes something B.0 never measured. The operator's rule is that
// every collector offers Stream and Poll, and twenty collectors declare a
// `streamKey`. Twenty subscribed collectors on a watched router is TWENTY
// PERMANENTLY OPEN CHANNELS -- against the bottleneck this project documents as
// concurrent channels, and which `internal/roslimit` caps at eight for COMMANDS.
//
// That cap does not protect against this and does not measure it: `reader.Do`
// takes a slot and `reader.Stream` does not. So the number is unknown, and every
// step after B.0b depends on it.
//
// ── WHAT IT MEASURES ────────────────────────────────────────────────────────
//
// Channels are opened one at a time on ONE connection, which is how the app
// works -- every collector for a router shares a session's client, and each
// stream is one tag on it. At each width:
//
//	does the next channel OPEN            a refusal is the hard ceiling
//	the router's CPU                      against a no-channel control
//	rows/s on the FIRST channel           the quiet failure: a router that
//	                                      accepts a channel and then starves the
//	                                      ones already open would look fine at
//	                                      every width and be broken at all of them
//
// The third is the one worth the tool. A ceiling announces itself; degradation
// does not, and a collector delivering half its rows renders a page that is
// merely stale rather than one that is empty.
//
// READ-ONLY. It opens monitor channels and closes them, and writes nothing.

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"mikrodash/internal/routeros"
)

// channelStep is one width: N channels open, held, and measured.
type channelStep struct {
	open     int     // channels actually open
	wanted   int     // channels attempted
	refusal  string  // why the last open failed, if it did
	cpuMean  float64 //
	firstRPS float64 // rows/s on channel 1, which has been open throughout
}

// measureChannels opens up to `max` concurrent streams, one per step, and
// reports what the router did at each width.
//
// EACH CHANNEL WATCHES ONE INTERFACE, cycling through the list. The width being
// costed is the CHANNEL COUNT, and B.0 already established that the interface
// count inside a channel is not what costs anything -- so a narrow channel is
// the honest unit here and keeps the byte volume out of the measurement.
func measureChannels(c *routeros.Client, names []string, max, hold int) []channelStep {
	var firstRows atomic.Int64
	var stops []func()
	defer func() {
		for _, s := range stops {
			s()
		}
	}()

	var out []channelStep
	for i := 0; i < max; i++ {
		iface := names[i%len(names)]
		onRow := func(routeros.Reply) {}
		if i == 0 {
			// CHANNEL ONE IS THE WITNESS. It stays open for every width, so its
			// rate is comparable across the whole run -- which is the only way
			// starvation of an existing channel is visible at all.
			onRow = func(routeros.Reply) { firstRows.Add(1) }
		}
		stop, err := c.Stream(routeros.Cmd{Path: "/interface/monitor-traffic", Args: []string{
			"=interface=" + iface,
			"=interval=1",
			"=.proplist=" + rateProps,
		}}, onRow)

		step := channelStep{open: len(stops), wanted: i + 1}
		if err != nil {
			// THE CEILING, and the run stops here rather than trying wider: past
			// a refusal every later number is about a different router state.
			step.refusal = err.Error()
			out = append(out, step)
			return out
		}
		stops = append(stops, stop)
		step.open = len(stops)

		// Settle, then measure the witness over a clean window.
		time.Sleep(500 * time.Millisecond)
		before := firstRows.Load()
		var cpu []float64
		for s := 0; s < hold; s++ {
			time.Sleep(time.Second)
			if v, ok := cpuLoad(c); ok {
				cpu = append(cpu, v)
			}
		}
		step.firstRPS = float64(firstRows.Load()-before) / float64(hold)
		if len(cpu) > 0 {
			for _, v := range cpu {
				step.cpuMean += v
			}
			step.cpuMean /= float64(len(cpu))
		}
		out = append(out, step)
	}
	return out
}

func reportChannels(steps []channelStep, control float64) {
	fmt.Printf("  %-10s %-12s %-14s %s\n", "channels", "router CPU", "ch1 rows/s", "")
	fmt.Printf("  %-10s %-12s %-14s %s\n", "--------", "----------", "----------", "")
	fmt.Printf("  %-10d %-12s %-14s %s\n", 0, pct(control), "-", "control")
	for _, s := range steps {
		if s.refusal != "" {
			fmt.Printf("  %-10d %-12s %-14s REFUSED: %s\n", s.wanted, "-", "-",
				firstLine(s.refusal))
			continue
		}
		fmt.Printf("  %-10d %-12s %-14.1f\n", s.open, pct(s.cpuMean), s.firstRPS)
	}
	fmt.Println()

	// THE TWO FINDINGS, NAMED RATHER THAN LEFT TO BE READ OUT OF THE TABLE. A
	// number nobody interprets gets quoted later as whatever the reader hoped.
	var opened int
	var refused string
	first, last := 0.0, 0.0
	for i, s := range steps {
		if s.refusal != "" {
			refused = s.refusal
			break
		}
		opened = s.open
		if i == 0 {
			first = s.firstRPS
		}
		last = s.firstRPS
	}
	if refused != "" {
		fmt.Printf("  CEILING: the router refused channel %d — %s\n", opened+1, firstLine(refused))
	} else {
		fmt.Printf("  NO CEILING FOUND: %d concurrent channels all opened.\n", opened)
	}
	if first > 0 {
		drop := (first - last) / first * 100
		switch {
		case drop >= 10:
			fmt.Printf("  STARVATION: channel 1 fell from %.1f to %.1f rows/s (%.0f%%) as the\n"+
				"  others opened. A collector on an older channel would silently go stale.\n",
				first, last, drop)
		default:
			fmt.Printf("  NO STARVATION: channel 1 held %.1f rows/s at width 1 and %.1f at width %d.\n",
				first, last, opened)
		}
	}
	fmt.Println()
}

func pct(v float64) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", v)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
