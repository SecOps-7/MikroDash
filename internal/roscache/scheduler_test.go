package roscache

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// TestSchedulerFetchesOnlyWhatIsWanted is the property phase 3 exists for: a menu
// with no subscriber is not read, and one with a subscriber is read at its
// cadence and no faster.
func TestSchedulerFetchesOnlyWhatIsWanted(t *testing.T) {
	f := &fake{rows: []routeros.Reply{{"name": "ether1"}}}
	c := New(f)
	s := NewScheduler(c, time.Millisecond)

	t0 := time.Now()
	s.run(t0)
	if f.n() != 0 {
		t.Fatalf("a pass with nothing subscribed fetched %d times", f.n())
	}

	release := c.Subscribe("/interface/print", []string{"name"}, Every(10*time.Second), nil)
	s.run(t0)
	if f.n() != 1 {
		t.Fatalf("a subscribed menu was fetched %d times on the first pass, want 1", f.n())
	}

	// Inside the cadence: nothing.
	s.run(t0.Add(time.Second))
	if f.n() != 1 {
		t.Errorf("fetched again %v into a 10s cadence", time.Second)
	}
	// Past it: once more.
	s.run(t0.Add(11 * time.Second))
	if f.n() != 2 {
		t.Errorf("did not fetch after the cadence elapsed: %d reads", f.n())
	}

	release()
	s.run(t0.Add(30 * time.Second))
	if f.n() != 2 {
		t.Errorf("kept fetching a menu nobody wants: %d reads", f.n())
	}
}

// TestSchedulerDeliversToSubscribers: the half that lets a collector stop owning
// a timer. The rows it fetched reach whoever asked for them.
func TestSchedulerDeliversToSubscribers(t *testing.T) {
	f := &fake{rows: []routeros.Reply{{"host": "8.8.8.8", "status": "up"}}}
	c := New(f)
	s := NewScheduler(c, time.Millisecond)

	var mu sync.Mutex
	var got [][]routeros.Reply
	defer c.Subscribe("/tool/netwatch/print", nil, Every(time.Second), func(rows []routeros.Reply, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err == nil {
			got = append(got, rows)
		}
	})()

	s.run(time.Now())
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0]["host"] != "8.8.8.8" {
		t.Errorf("subscriber received %v, want the fetched rows", got)
	}
}

// TestSchedulerSkipsZeroCadence. A zero means "serve me from whatever others keep
// fresh"; scheduling it would invent a rate nobody asked for and fetch a menu on
// behalf of a consumer that said it did not care.
func TestSchedulerSkipsZeroCadence(t *testing.T) {
	f := &fake{}
	c := New(f)
	s := NewScheduler(c, time.Millisecond)
	defer c.Subscribe("/ip/dhcp-server/lease/print", nil, nil, nil)()

	s.run(time.Now())
	if f.n() != 0 {
		t.Errorf("a zero-cadence subscription was scheduled: %d reads", f.n())
	}
}

// TestSchedulerForgetsMenusNobodyWants: on a long-running session the bookkeeping
// must not grow with every page ever opened.
func TestSchedulerForgetsMenusNobodyWants(t *testing.T) {
	c := New(&fake{})
	s := NewScheduler(c, time.Millisecond)

	release := c.Subscribe("/ip/dns/print", nil, Every(time.Second), nil)
	s.run(time.Now())
	if len(s.lastRun) != 1 {
		t.Fatalf("scheduler tracked %d menus, want 1", len(s.lastRun))
	}
	release()
	s.run(time.Now())
	if len(s.lastRun) != 0 {
		t.Errorf("scheduler still tracks %d menus after the last subscriber left", len(s.lastRun))
	}
}

// TestSchedulerIsOneGoroutinePerRouter is the count Collectors-Rewrite.md asks
// for by name: constant per router, not a multiple of the collectors.
//
// TODAY'S BASELINE, for the honesty of the comparison: 30 poll loops across 25
// collector files, so ten routers is around three hundred goroutines. This is
// ten. Nobody would notice the difference in Go, and the plan says so -- the
// count is the measurable proxy, not the reason.
func TestSchedulerIsOneGoroutinePerRouter(t *testing.T) {
	const routers = 10

	settle := func() {
		for i := 0; i < 20; i++ {
			runtime.Gosched()
			time.Sleep(time.Millisecond)
		}
	}
	settle()
	before := runtime.NumGoroutine()

	scheds := make([]*Scheduler, 0, routers)
	for i := 0; i < routers; i++ {
		c := New(&fake{})
		// Several menus each, to prove the count follows the ROUTER and not the
		// number of things it is watching.
		for _, m := range []string{"/interface/print", "/ip/address/print", "/ip/dns/print"} {
			defer c.Subscribe(m, nil, Every(time.Hour), nil)()
		}
		s := NewScheduler(c, 50*time.Millisecond)
		s.Start()
		scheds = append(scheds, s)
	}
	settle()
	grew := runtime.NumGoroutine() - before

	for _, s := range scheds {
		s.Stop()
	}
	settle()

	if grew > routers+2 {
		t.Errorf("%d routers with three menus each added %d goroutines; want about %d, so the "+
			"count is following the collectors rather than the router", routers, grew, routers)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("Stop left %d goroutines behind", after-before)
	}
}

// TestSchedulerStartIsNotASecondGoroutine. Start twice is the easiest way to
// undo the entire point of this file.
func TestSchedulerStartIsNotASecondGoroutine(t *testing.T) {
	c := New(&fake{})
	s := NewScheduler(c, 50*time.Millisecond)

	before := runtime.NumGoroutine()
	s.Start()
	s.Start()
	s.Start()
	time.Sleep(20 * time.Millisecond)
	grew := runtime.NumGoroutine() - before
	s.Stop()

	if grew > 2 {
		t.Errorf("three Starts added %d goroutines, want one", grew)
	}
}

// TestSchedulerReleaseFromInsideCallback pins why deliver copies the subscriber
// list and runs outside the lock: a collector told its menu is gone would
// reasonably release itself there, and holding the lock across that deadlocks.
func TestSchedulerReleaseFromInsideCallback(t *testing.T) {
	c := New(&fake{rows: []routeros.Reply{{"a": "b"}}})
	s := NewScheduler(c, time.Millisecond)

	var release func()
	var fired int32
	release = c.Subscribe("/ip/route/print", nil, Every(time.Second), func([]routeros.Reply, error) {
		atomic.AddInt32(&fired, 1)
		release()
	})

	done := make(chan struct{})
	go func() { s.run(time.Now()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("releasing from inside a callback deadlocked the scheduler")
	}
	if atomic.LoadInt32(&fired) != 1 {
		t.Errorf("callback fired %d times, want 1", fired)
	}
}
