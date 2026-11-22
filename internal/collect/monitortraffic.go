package collect

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// The one menu two collectors read, and the rule that lets them share it.
//
// ── WHY THIS FILE EXISTS RATHER THAN A METHOD ON EITHER COLLECTOR ──────────
//
// `/interface/monitor-traffic` is wanted by `ifStatus`, which reads a SNAPSHOT
// of every enabled interface's rates on its own cadence, and by `traffic`, which
// wants EVERY ROW as it arrives for the interfaces somebody is watching — which
// may include a disabled one, because a viewer can select it.
//
// Neither set contains the other, so this is a merge and not a subscription to
// somebody else's channel. Putting the rule on either collector would make that
// collector the authority on the other's needs, which is the coupling the two
// separate channels were avoiding in the first place.
//
// `roscache` deliberately does not know it: `=interface=` being a comma list and
// `=interval=` being a minimum are facts about THIS menu, and a cache that
// learned them would learn the next menu's rule as a second special case.
const monitorTrafficMenu = "/interface/monitor-traffic"

// monitorTrafficProps is the union of what the two holders read.
//
// `running` and `disabled` are `traffic`'s: its samples carry link state so the
// chart can grey a downed interface. `ifStatus` ignores them, and carrying two
// fields it does not read is free — one channel with a wider proplist is
// strictly cheaper than two channels.
// monitorTrafficMaxSec is the longest `=interval=` RouterOS accepts here.
const monitorTrafficMaxSec = 5

const monitorTrafficProps = "=.proplist=name,rx-bits-per-second,tx-bits-per-second,running,disabled"

// monitorTrafficCmd builds one holder's claim.
//
// The interface list is SORTED, so a given set produces a stable command and the
// merge restarts the channel on a real change rather than on map iteration
// order. Both callers sort their own list too; doing it here as well is what
// makes that a property of the command rather than of each caller remembering.
func monitorTrafficCmd(names []string, sec int) routeros.Cmd {
	if sec < 1 {
		sec = 1
	}
	// FIVE SECONDS IS THE MOST THIS MENU TAKES. Measured on CHR Test (7.24.5):
	// `=interval=6` is refused, "value of interval is out of range
	// (00:00:00.020 .. 00:00:05)". `ifStatus` polls up to a minute, so above five
	// its join failed and it fell back to `=once=` reads. A channel at five is
	// fresher than its cadence, which costs it nothing.
	if sec > monitorTrafficMaxSec {
		sec = monitorTrafficMaxSec
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return routeros.Cmd{Path: monitorTrafficMenu, Args: []string{
		"=interface=" + strings.Join(sorted, ","),
		"=interval=" + strconv.Itoa(sec),
		monitorTrafficProps,
	}}
}

// mergeMonitorTraffic is the rule `roscache` applies on every join and release.
//
// ── THE UNION, AND THE FINEST INTERVAL ─────────────────────────────────────
//
// The union because a holder must never lose an interface it asked for: dropping
// one leaves a chart empty or a throughput column blank, silently, and nothing
// errors because the channel is perfectly healthy for everybody else.
//
// The finest interval because a merged channel delivers at one rate and the
// faster holder is the one with something to lose. `traffic` draws a point per
// second; `ifStatus` reads a snapshot whenever it ticks, so a channel finer than
// its own cadence costs it nothing and gains it freshness. B.0 measured every
// interface at one second as 2.6 KB/s with no measurable CPU, which is what
// makes "take the finest" affordable rather than a compromise.
func mergeMonitorTraffic(cmds []routeros.Cmd) routeros.Cmd {
	seen := map[string]bool{}
	var names []string
	best := 0
	for _, c := range cmds {
		for _, a := range c.Args {
			switch {
			case strings.HasPrefix(a, "=interface="):
				for _, n := range strings.Split(strings.TrimPrefix(a, "=interface="), ",") {
					if n != "" && !seen[n] {
						seen[n] = true
						names = append(names, n)
					}
				}
			case strings.HasPrefix(a, "=interval="):
				if n, err := strconv.Atoi(strings.TrimPrefix(a, "=interval=")); err == nil &&
					n > 0 && (best == 0 || n < best) {
					best = n
				}
			}
		}
	}
	return monitorTrafficCmd(names, best)
}

// trafficChannel is the Dashboard chart's own channel on this menu.
//
// ── WHY THE CHART NO LONGER SHARES (the operator, 2026-11-22) ───────────────
//
// One merged channel carried every interface `ifStatus` watches and the ones
// the chart shows. RouterOS ENDS a monitor-traffic stream when an interface it
// names is removed (`!trap`, then `!done`; measured on CHR Test, 7.24.1), and a
// CAPsMAN cAP's interfaces are removed every time it rejoins: on the operator's
// hAP ax^3 that was every two to five minutes, and the chart lost 13-16 s each
// time for an interface it was not even showing. On its own channel the chart
// stream names only the chart's interfaces, so only one of THEM vanishing can
// end it. The price is one more held channel per router while the chart is
// watched or recorded; the operator chose it.
const trafficChannel = "traffic"

// joinMonitorTraffic is the one call both collectors make.
//
// `onRow` is nil for a holder that reads the snapshot and non-nil for one that
// needs every packet. `channel` is "" for the menu's shared channel (ifStatus)
// and trafficChannel for the chart's own. Everything else is identical, which
// is the point: two holders differing only in what they asked to carry cannot
// disagree about how a channel is opened.
func joinMonitorTraffic(c *roscache.Cache, channel string, names []string, sec int,
	onRow func(routeros.Reply)) (func(), error) {
	if sec < 1 {
		sec = 1
	}
	if sec > monitorTrafficMaxSec {
		sec = monitorTrafficMaxSec // the boundary is the channel's interval, see monitorTrafficCmd
	}
	return c.JoinStream(roscache.Join{
		Menu:     monitorTrafficMenu,
		Channel:  channel,
		Cmd:      monitorTrafficCmd(names, sec),
		KeyOf:    keyByIfaceName,
		Boundary: time.Duration(sec) * time.Second,
		Merge:    mergeMonitorTraffic,
		OnRow:    onRow,
	})
}
