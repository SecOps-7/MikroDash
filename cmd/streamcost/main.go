// streamcost is B.0 of the collector rewrite: measure what it would cost to
// stream EVERY interface, before designing anything on top of the idea.
//
// ── THE QUESTION ────────────────────────────────────────────────────────────
//
// `ifStatus` takes interface rates as a MEASUREMENT — /interface/monitor-traffic
// with a once argument, on every interface, every poll. It is roughly 52
// commands a minute and the largest single item left on an idle router.
//
// `traffic` already holds an open channel on the same menu, for the handful of
// interfaces someone is watching. If that channel covered EVERY interface,
// `ifStatus` could read its rates from it and stop measuring, removing the 52
// outright.
//
// That inverts `traffic`'s stated design — its header says "a router with forty
// interfaces is not forty streams" and its subscription set is refcounted so the
// stream shrinks when viewers leave. So it is a real trade, and the plan says to
// measure it rather than argue it.
//
// ── WHAT THIS MEASURES, AND WHAT IT DELIBERATELY DOES NOT ───────────────────
//
// It measures the ROUTER side: rows and bytes per second pushed down one channel
// at the full interface count, against the same channel at the count `traffic`
// uses today. Those are the two numbers the design turns on and neither can be
// got from the code.
//
// It also samples the ROUTER'S CPU while each channel is open. The plan did not
// ask for that number and it is the one that actually decides the question:
// bandwidth is not this app's bottleneck and never was -- "the evidence in #104
// points at concurrent open channels rather than data volume" -- so the only way
// streaming thirty interfaces can be expensive is if PRODUCING thirty sections a
// second costs the router something.
//
// It does NOT measure the fan-out cost in this process. That is a Go question
// with a Go answer, and it is dominated by whatever this prints.
//
// READ-ONLY. Two monitor channels, opened and closed. It writes nothing.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// rateProps is `traffic`'s proplist, not `ifStatus`'s. The stream is the thing
// being costed, so it is the stream's field list that decides the byte count.
const rateProps = "name,rx-bits-per-second,tx-bits-per-second,running,disabled"

func main() {
	var (
		data    = flag.String("data", "", "read the router's credentials from this /data directory")
		rname   = flag.String("router", "", "the router's label, or its host")
		seconds = flag.Int("seconds", 30, "how long to hold each channel open")
		few     = flag.Int("few", 2, "how many interfaces stand in for what `traffic` watches today")
		chans   = flag.Int("channels", 0, "B.0b: instead of the width measurement, open up to N CONCURRENT channels and report the router's ceiling and any starvation")
		hold    = flag.Int("hold", 4, "B.0b: seconds to hold and measure at each channel width")
		probe   = flag.String("probe", "", "B.4: comma-separated menus to test for =interval= support, one at a time")
	)
	flag.Parse()

	if *data == "" {
		fmt.Fprintln(os.Stderr, "usage: streamcost -data /data -router LABEL [-seconds N]")
		os.Exit(2)
	}
	cfg, err := fromStore(*data, *rname)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	c, err := routeros.Dial(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()

	names, err := interfaceNames(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "interfaces:", err)
		os.Exit(1)
	}
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "this router reports no interfaces")
		os.Exit(1)
	}
	fmt.Printf("\n%s — %d interfaces, %ds per channel\n\n", cfg.Host, len(names), *seconds)

	small := names
	if len(small) > *few {
		small = small[:*few]
	}

	// ── B.4: DOES THIS MENU ACCEPT `=interval=`? ────────────────────────────
	//
	// See probe.go. The documentation settles the CLI and not the binary API,
	// and assuming wrongly surfaces as a trap at the moment a router is switched
	// to Stream. Same tool because the dial and the credential handling are here.
	if *probe != "" {
		menus := strings.Split(*probe, ",")
		fmt.Printf("  B.4 probe — %d menu(s), %ds each\n\n", len(menus), *hold)
		reportProbe(probeMenus(c, menus, *hold), *hold)
		return
	}

	// ── B.0b: THE CHANNEL BUDGET, WHICH IS A DIFFERENT QUESTION ─────────────
	//
	// See channels.go. B.0 costed one WIDE channel; this costs MANY. Track B
	// depends on the answer, so it is the same tool rather than a second one:
	// the dial, the credential handling and the CPU sampling are already here.
	if *chans > 0 {
		fmt.Printf("  B.0b — up to %d concurrent channels, %ds at each width\n\n", *chans, *hold)
		control := measure(c, nil, *hold)
		mean, _ := control.cpuStats()
		reportChannels(measureChannels(c, names, *chans, *hold), mean)
		return
	}

	// THE CONTROL COMES FIRST and it is not optional. A CPU figure with nothing
	// to compare it against says nothing at all: this router is also being polled
	// by MikroDash throughout, so the only readable quantity is the DIFFERENCE.
	idle := measure(c, nil, *seconds)
	base := measure(c, small, *seconds)
	full := measure(c, names, *seconds)

	report("no channel", 0, idle, *seconds)
	report("today's shape", len(small), base, *seconds)
	report("every interface", len(names), full, *seconds)

	// THE NUMBER THE DECISION TURNS ON. `ifStatus` currently costs one command
	// per poll; the stream costs no commands at all, so the trade is commands
	// against pushed rows. Printing the ratio rather than a verdict, because the
	// threshold is the operator's and depends on the router.
	if base.rows > 0 {
		fmt.Printf("  every interface pushes %.1fx the rows and %.1fx the bytes of today's shape.\n",
			float64(full.rows)/float64(base.rows), float64(full.bytes)/float64(base.bytes))
	}
	fmt.Printf("  for comparison, ifStatus's measurement is ONE command per poll — about 52 a\n" +
		"  minute at the default cadence — and would go to zero.\n\n")
}

type sample struct {
	rows  int64
	bytes int64
	first time.Duration
	last  time.Duration
	cpu   []float64
}

// cpuStats is mean and max over the samples, or zeroes when none were taken.
func (s sample) cpuStats() (mean, max float64) {
	if len(s.cpu) == 0 {
		return 0, 0
	}
	for _, v := range s.cpu {
		mean += v
		if v > max {
			max = v
		}
	}
	return mean / float64(len(s.cpu)), max
}

// measure holds one channel open and counts what comes down it.
func measure(c *routeros.Client, names []string, seconds int) sample {
	var rows, bytes atomic.Int64
	var firstAt, lastAt atomic.Int64
	start := time.Now()

	var stop func()
	var err error
	if len(names) == 0 {
		stop = func() {}
	} else {
		stop, err = c.Stream(routeros.Cmd{Path: "/interface/monitor-traffic", Args: []string{
			"=interface=" + strings.Join(names, ","),
			"=interval=1",
			"=.proplist=" + rateProps,
		}}, func(r routeros.Reply) {
			n := 0
			for k, v := range r {
				n += len(k) + len(v) + 2 // the two bytes RouterOS spends on a word's length and its '='
			}
			rows.Add(1)
			bytes.Add(int64(n))
			since := int64(time.Since(start))
			firstAt.CompareAndSwap(0, since)
			lastAt.Store(since)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  stream of %d interfaces refused: %v\n", len(names), err)
			return sample{}
		}
	}
	// SAMPLED ON THIS CONNECTION, alongside the open channel, because that is the
	// state being costed. It is one small read a second and it is counted in
	// neither the rows nor the bytes above.
	var cpu []float64
	for i := 0; i < seconds; i++ {
		time.Sleep(time.Second)
		if v, ok := cpuLoad(c); ok {
			cpu = append(cpu, v)
		}
	}
	stop()

	return sample{
		rows: rows.Load(), bytes: bytes.Load(),
		first: time.Duration(firstAt.Load()), last: time.Duration(lastAt.Load()),
		cpu: cpu,
	}
}

func cpuLoad(c *routeros.Client) (float64, bool) {
	rows, err := c.Do(routeros.Cmd{Path: "/system/resource/print",
		Args: []string{"=.proplist=cpu-load"}})
	if err != nil || len(rows) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(rows[0]["cpu-load"]), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func report(label string, n int, s sample, seconds int) {
	per := float64(s.rows) / float64(seconds)
	mean, max := s.cpuStats()
	fmt.Printf("  %-18s %2d interfaces  %5d rows  %6.1f rows/s  %6.0f B/s  cpu %4.1f%% mean %4.1f%% max  first at %v\n",
		label, n, s.rows, per, float64(s.bytes)/float64(seconds), mean, max,
		s.first.Round(time.Millisecond))
}

// interfaceNames is the same read ifStatus performs before its measurement:
// rates can only be asked once the interface list is known.
func interfaceNames(c *routeros.Client) ([]string, error) {
	rows, err := c.Do(routeros.Cmd{Path: "/interface/print",
		Args: []string{"=.proplist=name"}})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if r["name"] != "" {
			out = append(out, r["name"])
		}
	}
	sort.Strings(out)
	return out, nil
}

// fromStore is cmd/conformance's, and deliberately a copy rather than a shared
// package: both are single-file tools that must keep working if the other is
// deleted, and the live app's own store reader is the thing under test in
// neither of them.
func fromStore(dir, name string) (routeros.Config, error) {
	st, err := store.Open(dir)
	if err != nil {
		return routeros.Config{}, fmt.Errorf("open %s: %w", dir, err)
	}
	routers, errs := st.Routers()
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
	if len(routers) == 0 {
		return routeros.Config{}, fmt.Errorf("%s holds no routers", dir)
	}
	if name == "" {
		var b strings.Builder
		b.WriteString("name a router with -router. This store holds:\n")
		for _, r := range routers {
			fmt.Fprintf(&b, "  %-28s %s@%s\n", r.Label, r.Username, r.Host)
		}
		return routeros.Config{}, errors.New(b.String())
	}
	for _, r := range routers {
		if !strings.EqualFold(r.Label, name) && r.Host != name {
			continue
		}
		pw, err := st.Decrypt(r.Encrypted)
		if err != nil {
			return routeros.Config{}, fmt.Errorf("decrypt %s: %w", r.Label, err)
		}
		return routeros.Config{
			Host: r.Host, Port: r.Port, Username: r.Username, Password: pw,
			TLS: r.TLS, InsecureTLS: r.TLSInsecure,
		}, nil
	}
	return routeros.Config{}, fmt.Errorf("no router in %s is called %q", dir, name)
}
