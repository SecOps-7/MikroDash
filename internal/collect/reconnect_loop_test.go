package collect

import (
	"testing"
	"time"

	"mikrodash/internal/roscache"
)

func loopRunning(p *pollLoop) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.stopped
}

// A RECONNECT DOES NOT START A LOOP THE SCHEDULER CANNOT STOP (review
// 2026-09-19).
//
// Firewall and Wireless are scheduled (the cache reads their menu on demand),
// and their Reconnected started the raw poll loop directly. scheduled.end
// stops that loop only when there is no cache, or the loop is residual, so
// after any reconnect Suspend, Stop and the idle-out left it polling for the
// life of the process. Wifi and VLANs re-subscribe with sched.begin; so do these.
func TestAReconnectLeavesNoOrphanLoop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() (loop *pollLoop, reconnect, suspend func())
	}{
		{"firewall", func() (*pollLoop, func(), func()) {
			f := NewFirewall(emptyReader{}, Emit{}, 5000)
			f.UseCache(roscache.New(emptyReader{}))
			return f.poll, f.Reconnected, f.Suspend
		}},
		{"wireless", func() (*pollLoop, func(), func()) {
			w := NewWireless(emptyReader{}, Emit{}, nil, 30000)
			w.UseCache(roscache.New(emptyReader{}))
			return w.loop, w.Reconnected, w.Suspend
		}},
		// VPN too (review loop): its /ppp/active subscription also went, so a
		// router reboot froze the PPP and IPsec rows until a page change.
		{"vpn", func() (*pollLoop, func(), func()) {
			v := NewVPN(emptyReader{}, Emit{}, 5000)
			v.UseCache(roscache.New(emptyReader{}))
			return v.poll, v.Reconnected, v.Suspend
		}},
	} {
		loop, reconnect, suspend := tc.build()
		reconnect()
		suspend()
		if loopRunning(loop) {
			t.Errorf("%s: after Reconnected and Suspend its poll loop still runs, so it polls "+
				"for ever regardless of demand", tc.name)
		}
		loop.stop()
	}
}

// A RE-TUNE REACHES THE SCHEDULER, NOT JUST THE PAYLOAD (review 2026-09-19).
//
// scheduled.begin evaluated the cadence once and handed Subscribe a number, so
// the demand the scheduler reads kept the interval the collector was started
// with: move a slider from 60 s to 5 s and the payload said 5 s while the
// router was read every 60. TestEveryTableCollectorsCadenceFollowsARetune read
// the collector's cadence FUNCTION, which does follow, so it passed while the
// scheduler did not. This asks the scheduler.
func TestARetuneReachesTheSchedulersDemand(t *testing.T) {
	type target struct {
		name  string
		start func(*roscache.Cache) (setPoll func(int), stop func())
		ms    int
		every time.Duration // the subscription's cadence as a multiple of the poll
	}
	for _, tc := range []target{
		{"dns (table)", func(c *roscache.Cache) (func(int), func()) {
			d := NewDNS(emptyReader{}, Emit{}, 30000)
			d.UseCache(c)
			d.Start()
			return d.SetPollMs, d.Stop
		}, 45000, 1},
		{"firewall", func(c *roscache.Cache) (func(int), func()) {
			f := NewFirewall(emptyReader{}, Emit{}, 5000)
			f.UseCache(c)
			f.Resume() // Start only reads once; the subscription is Resume's
			return f.SetPollMs, f.Stop
		}, 7000, 1},
		{"vlans", func(c *roscache.Cache) (func(int), func()) {
			v := NewVlans(emptyReader{}, Emit{}, nil, nil, 5000)
			v.UseCache(c)
			v.Start()
			return v.SetPollMs, v.Stop
		}, 9000, vlanConfigEvery}, // config menus ride a multiple of the rate poll
	} {
		c := roscache.New(emptyReader{})
		set, stop := tc.start(c)
		set(tc.ms)
		want := time.Duration(tc.ms) * time.Millisecond * tc.every
		found := false
		for _, d := range c.Demand() {
			if d.Cadence == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: after SetPollMs(%d) the scheduler's demand is %+v; no menu carries %s, "+
				"so the router is still read at the old interval", tc.name, tc.ms, c.Demand(), want)
		}
		stop()
	}
}

// TestAVPNReconnectKeepsItsSubscription. VPN's Reconnected ended its scheduled
// subscription and started the raw loop in its place, so the cache stopped
// reading /ppp/active for it (review loop).
func TestAVPNReconnectKeepsItsSubscription(t *testing.T) {
	c := roscache.New(emptyReader{})
	v := NewVPN(emptyReader{}, Emit{}, 5000)
	v.UseCache(c)
	v.Start()
	defer v.Stop()
	v.Reconnected()
	for _, d := range c.Demand() {
		if d.Menu == "/ppp/active/print" {
			return
		}
	}
	t.Errorf("after Reconnected the scheduler's demand is %+v, without /ppp/active/print", c.Demand())
}
