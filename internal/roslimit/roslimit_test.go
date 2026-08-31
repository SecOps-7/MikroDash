package roslimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── A BROKEN GATE MUST FAIL, NOT HANG ───────────────────────────────────────
//
// Every failure mode here is "a slot never comes back": a release on the wrong
// path, a cap of one, a double release that is then re-taken. All of them block
// forever, so a test that simply waits turns a detection into a ten-minute
// timeout and a panic dump. Two mutations proved that the hard way while this
// package was being written.
//
// waitFor bounds the wait so the same mutations produce a one-line failure that
// names what did not happen.
func waitFor(t *testing.T, what string, d time.Duration, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timed out after %s waiting for %s — a slot was taken and never released", d, what)
	}
}

// TestNeverExceedsTheCap is the whole point of the package: however many
// goroutines pile in, the number INSIDE the guarded section at any instant must
// never exceed the cap.
//
// It measures the observed peak rather than trusting the channel, because a
// semaphore that is acquired but released on the wrong path still passes a
// "does it block" test and fails this one.
func TestNeverExceedsTheCap(t *testing.T) {
	Reset()
	defer Reset()

	const goroutines = 200
	var (
		cur, peak atomic.Int64
		wg        sync.WaitGroup
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done := Acquire("r1")
			defer done()
			n := cur.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			cur.Add(-1)
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	waitFor(t, "all 200 callers to finish", 30*time.Second, finished)

	if got := peak.Load(); got > DefaultMax {
		t.Fatalf("peak concurrency %d exceeded the cap of %d", got, DefaultMax)
	}
	// AND IT MUST ACTUALLY BE CONCURRENT. A gate that serialised everything
	// would pass the check above and be a performance disaster, so the floor
	// matters as much as the ceiling.
	if got := peak.Load(); got < 2 {
		t.Fatalf("peak concurrency %d — the gate serialised the work instead of bounding it", got)
	}
	if n := InFlight("r1"); n != 0 {
		t.Fatalf("%d slot(s) still held after every caller released", n)
	}
}

// TestBudgetsArePerRouter pins the reason the gate is keyed by id at all: one
// saturated router must not stall a different one.
func TestBudgetsArePerRouter(t *testing.T) {
	Reset()
	defer Reset()

	var hold []func()
	for i := 0; i < DefaultMax; i++ {
		hold = append(hold, Acquire("busy"))
	}
	if n := InFlight("busy"); n != DefaultMax {
		t.Fatalf("expected the busy router saturated at %d, got %d", DefaultMax, n)
	}

	got := make(chan struct{})
	go func() {
		done := Acquire("idle")
		defer done()
		close(got)
	}()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("a saturated router blocked an unrelated one — the gate is not per router")
	}
	for _, h := range hold {
		h()
	}
}

// TestReleaseIsIdempotent: a double release would hand out a slot that was never
// taken, quietly raising the ceiling for everyone on that router.
func TestReleaseIsIdempotent(t *testing.T) {
	Reset()
	defer Reset()

	done := Acquire("r1")
	done()
	done()
	if n := InFlight("r1"); n != 0 {
		t.Fatalf("in flight %d after a double release; want 0", n)
	}
	// The capacity must be intact, not one larger.
	var held []func()
	for i := 0; i < DefaultMax; i++ {
		held = append(held, Acquire("r1"))
	}
	blocked := make(chan struct{})
	go func() {
		d := Acquire("r1")
		d()
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("a slot beyond the cap was available — the double release widened the gate")
	case <-time.After(200 * time.Millisecond):
	}
	for _, h := range held {
		h()
	}
	waitFor(t, "the queued caller to get a slot once the others released", 10*time.Second, blocked)
}

// TestUnidentifiedRouterIsNotGated: an empty id must not put unrelated callers
// into one shared bucket.
func TestUnidentifiedRouterIsNotGated(t *testing.T) {
	Reset()
	defer Reset()

	for i := 0; i < DefaultMax*3; i++ {
		done := Acquire("")
		defer done()
	}
	if n := InFlight(""); n != 0 {
		t.Fatalf("an empty router id took %d slot(s); it should take none", n)
	}
}
