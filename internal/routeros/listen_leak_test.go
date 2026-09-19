package routeros

import (
	"runtime"
	"testing"
	"time"
)

// TestAStreamLeavesNoGoroutineBehind. go-routeros started a goroutine per
// listener that waited on `<-ctx.Done()` to cancel the connection's reader,
// and `ListenArgs` passes context.Background(), whose Done channel is nil: the
// goroutine could never wake. A stream reopened every ten seconds parked
// about 8,600 of them per menu per day (review loop; PATCHES.md, change 3).
func TestAStreamLeavesNoGoroutineBehind(t *testing.T) {
	port := slowRouter(t, 0) // answers every command with !done at once
	cl := dialFake(t, port)

	settle := func() int {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	// One stream first, so the client's own loops are running at baseline.
	stop, err := cl.Stream(Cmd{Path: "/interface/listen"}, func(Reply) {})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	before := settle()

	const n = 100
	for i := 0; i < n; i++ {
		stop, err := cl.Stream(Cmd{Path: "/interface/listen"}, func(Reply) {})
		if err != nil {
			t.Fatal(err)
		}
		stop()
	}
	var after int
	if !waitFor(func() bool { after = settle(); return after < before+n/10 }) {
		t.Errorf("%d goroutines before %d streams and %d after: each stream left one behind",
			before, n, after)
	}
}
