package collect

import (
	"testing"

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
