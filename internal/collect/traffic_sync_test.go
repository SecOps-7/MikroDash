package collect

import (
	"strings"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// cmdReader records every command a channel is opened with, and opens slowly.
type cmdReader struct {
	mu   sync.Mutex
	cmds []routeros.Cmd
}

func (r *cmdReader) Connected() bool                           { return true }
func (r *cmdReader) Do(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }
func (r *cmdReader) Stream(cmd routeros.Cmd, _ func(routeros.Reply)) (func(), error) {
	time.Sleep(time.Millisecond)
	r.mu.Lock()
	r.cmds = append(r.cmds, cmd)
	r.mu.Unlock()
	return func() {}, nil
}

// TestConcurrentWatchersLeaveNoInterfaceBehind. syncStream read the interface
// set, released the lock, and joined; two watchers arriving together each
// joined, the second overwrote the first's release, and the first holder was
// never released: its interfaces stayed in the merged channel for the life of
// the session (review loop).
func TestConcurrentWatchersLeaveNoInterfaceBehind(t *testing.T) {
	for round := 0; round < 20; round++ {
		r := &cmdReader{}
		tr := NewTraffic(r, hub.Relay{}, "ether1", 1)
		c := roscache.New(r)
		tr.UseCache(c)
		tr.Start()

		var wg sync.WaitGroup
		for _, name := range []string{"wlan-a", "wlan-b", "wlan-a", "wlan-b"} {
			wg.Add(1)
			go func(n string) { defer wg.Done(); tr.Watch(n) }(name)
		}
		wg.Wait()
		for _, name := range []string{"wlan-a", "wlan-b", "wlan-a", "wlan-b"} {
			wg.Add(1)
			go func(n string) { defer wg.Done(); tr.Unwatch(n) }(name)
		}
		wg.Wait()

		r.mu.Lock()
		last := strings.Join(r.cmds[len(r.cmds)-1].Args, " ")
		r.mu.Unlock()
		if strings.Contains(last, "wlan-") {
			t.Fatalf("round %d: nobody watches wlan-a or wlan-b, and the channel is open for %q", round, last)
		}
		tr.Stop()
	}
}
