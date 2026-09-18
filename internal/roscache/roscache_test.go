package roscache

// The four properties Collectors-Rewrite.md step 1.2 names, plus the two traps
// that make this package dangerous if it is subtly wrong.
//
// Every test counts ROUTER READS, because that is the only number this package
// exists to move. A test that asserted on returned rows alone would pass against
// a cache that never cached anything.

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// fake counts reads and records the command it was asked for. `delay` widens the
// window in which a second caller can arrive, which is what makes the
// single-flight test test something rather than pass by luck.
type fake struct {
	mu    sync.Mutex
	calls int32
	last  routeros.Cmd
	delay time.Duration
	rows  []routeros.Reply
	err   error
}

func (f *fake) Do(c routeros.Cmd) ([]routeros.Reply, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	f.last = c
	d, rows, err := f.delay, f.rows, f.err
	f.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	return rows, err
}

func (f *fake) n() int { return int(atomic.LoadInt32(&f.calls)) }

func (f *fake) cmd() routeros.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func rows(n int) []routeros.Reply {
	out := make([]routeros.Reply, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, routeros.Reply{"name": "row"})
	}
	return out
}

// THE HEADLINE PROPERTY: concurrent callers cost one command.
//
// The 50ms delay is the point. Without it both goroutines could serialise
// naturally and the test would pass against a cache with no single-flight at
// all.
func TestConcurrentGetsCostOneRead(t *testing.T) {
	f := &fake{rows: rows(2), delay: 50 * time.Millisecond}
	c := New(f)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Get("/ip/address", []string{"address"}, time.Second); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if f.n() != 1 {
		t.Errorf("8 concurrent callers produced %d router reads, want 1", f.n())
	}
}

func TestSecondGetInsideTTLDoesNotRead(t *testing.T) {
	f := &fake{rows: rows(1)}
	c := New(f)
	for i := 0; i < 5; i++ {
		if _, err := c.Get("/ip/address", []string{"address"}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if f.n() != 1 {
		t.Errorf("5 sequential calls inside the TTL produced %d reads, want 1", f.n())
	}
}

func TestGetAfterTTLRefetches(t *testing.T) {
	f := &fake{rows: rows(1)}
	c := New(f)
	if _, err := c.Get("/ip/address", []string{"address"}, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := c.Get("/ip/address", []string{"address"}, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if f.n() != 2 {
		t.Errorf("a call after the TTL produced %d reads, want 2", f.n())
	}
}

// An error is shared like any other answer, and remembered only briefly.
//
// Both halves matter. Not caching it at all means a router refusing a menu is
// asked again on every tick, which spends exactly what this package saves.
// Caching it for a full TTL means a momentary failure looks like a broken router
// for far longer than it was.
func TestErrorIsSharedThenRetried(t *testing.T) {
	boom := errors.New("no such command")
	f := &fake{err: boom}
	c := New(f)

	for i := 0; i < 4; i++ {
		if _, err := c.Get("/nope", []string{"x"}, time.Minute); !errors.Is(err, boom) {
			t.Fatalf("call %d returned %v, want the shared error", i, err)
		}
	}
	if f.n() != 1 {
		t.Errorf("4 calls inside errTTL produced %d reads, want 1", f.n())
	}

	time.Sleep(errTTL + 50*time.Millisecond)
	if _, err := c.Get("/nope", []string{"x"}, time.Minute); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if f.n() != 2 {
		t.Errorf("after errTTL the error was not retried: %d reads, want 2", f.n())
	}
}

// TRAP ONE: a later consumer wanting a field nobody asked for must not be served
// a value fetched without it.
//
// This is the difference between a slow answer and a WRONG one, and it is the
// property most likely to be lost in a future "optimisation" that skips the
// invalidation.
func TestWideningTheProplistInvalidates(t *testing.T) {
	f := &fake{rows: rows(1)}
	c := New(f)

	if _, err := c.Get("/interface", []string{"name"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := f.cmd().Args; len(got) != 1 || got[0] != "=.proplist=name" {
		t.Fatalf("first fetch asked %v, want just name", got)
	}

	// A second consumer wants a field the first never asked for.
	if _, err := c.Get("/interface", []string{"running"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.n() != 2 {
		t.Errorf("widening the union produced %d reads, want 2 — the cached value "+
			"was fetched WITHOUT the new field and must not be reused", f.n())
	}
	if got := f.cmd().Args; len(got) != 1 || got[0] != "=.proplist=name,running" {
		t.Fatalf("second fetch asked %v, want the sorted union name,running", got)
	}

	// A third consumer wanting only fields already in the union is served from
	// cache: widening is what invalidates, not merely a differing list.
	if _, err := c.Get("/interface", []string{"name"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.n() != 2 {
		t.Errorf("a subset of the union refetched: %d reads, want 2", f.n())
	}
}

// Empty fields means "everything", and it is permanent: a later narrow caller
// must not shrink the proplist back and starve the one that wanted it all.
func TestEmptyFieldsMeansEverythingAndSticks(t *testing.T) {
	f := &fake{rows: rows(1)}
	c := New(f)

	if _, err := c.Get("/interface", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := f.cmd().Args; len(got) != 0 {
		t.Fatalf("a request for every field sent %v, want no proplist", got)
	}
	if _, err := c.Get("/interface", []string{"name"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.n() != 1 {
		t.Errorf("a narrow caller refetched after an all-fields one: %d reads, want 1", f.n())
	}
	if got := f.cmd().Args; len(got) != 0 {
		t.Fatalf("the proplist came back as %v; all-fields must stick", got)
	}
}

// TRAP TWO: rows are SHARED, not copied. Pinned so the sharing is a decision
// rather than an accident, and so anyone who changes it has to change this test
// and read why.
func TestRowsAreSharedNotCopied(t *testing.T) {
	f := &fake{rows: rows(1)}
	c := New(f)

	a, err := c.Get("/interface", []string{"name"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Get("/interface", []string{"name"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("got %d and %d rows", len(a), len(b))
	}
	a[0]["name"] = "MUTATED"
	if b[0]["name"] != "MUTATED" {
		t.Error("rows are copied per caller. That is safer but it trades the router " +
			"channels this package saves for allocations; if it is now deliberate, " +
			"update the package comment and Get's contract, not just this test")
	}
}

// The shortest tolerance wins: a one-second consumer must not be served a
// five-second-old answer because somebody else asked for five.
func TestShortestTTLWins(t *testing.T) {
	f := &fake{rows: rows(1)}
	c := New(f)

	if _, err := c.Get("/x", []string{"a"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get("/x", []string{"a"}, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if f.n() != 1 {
		t.Fatalf("setup produced %d reads, want 1", f.n())
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := c.Get("/x", []string{"a"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.n() != 2 {
		t.Errorf("the long TTL served a stale answer to the short consumer: %d reads, want 2", f.n())
	}
}

func TestInvalidateAndReset(t *testing.T) {
	f := &fake{rows: rows(1)}
	c := New(f)

	if _, err := c.Get("/x", []string{"a"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	c.Invalidate("/x")
	if _, err := c.Get("/x", []string{"a"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.n() != 2 {
		t.Errorf("Invalidate did not force a refetch: %d reads, want 2", f.n())
	}

	c.Reset()
	if _, err := c.Get("/x", []string{"a"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.n() != 3 {
		t.Errorf("Reset did not clear the entry: %d reads, want 3", f.n())
	}
	// Reset drops the union too, so the proplist is rebuilt from whoever asks
	// next rather than carrying a departed consumer's fields forever.
	if got := f.cmd().Args; len(got) != 1 || got[0] != "=.proplist=a" {
		t.Errorf("after Reset the fetch asked %v, want just a", got)
	}
}

// TestOnDeliverFiresForEveryRefresh. The dormancy supervisor rides this hook
// instead of a fifteen-second ticker, so a refresh that fires no callback is a
// judgement that never happens.
func TestOnDeliverFiresForEveryRefresh(t *testing.T) {
	f := &fake{}
	c := New(f)
	var seen []string
	var mu sync.Mutex
	c.OnDeliver(func(menu string) {
		mu.Lock()
		seen = append(seen, menu)
		mu.Unlock()
	})

	// A subscriber with NO callback of its own. The hook must still fire: a
	// collector that only declares demand still proves the router answered, and
	// dormancy judges payloads rather than the rows this delivery carried.
	rel := c.Subscribe("/ip/dns/print", nil, Every(time.Millisecond), nil)
	defer rel()

	s := NewScheduler(c, time.Millisecond)
	s.Start()
	defer s.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("OnDeliver fired %d times in two seconds; dormancy would never run", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestOnDeliverIsCalledWithoutTheDemandLock. The handler suspends and resumes
// collectors, which releases and takes subscriptions on this same cache. Holding
// the lock across it is a deadlock, and it is the kind that only shows up under
// a real router.
func TestOnDeliverIsCalledWithoutTheDemandLock(t *testing.T) {
	c := New(&fake{})
	done := make(chan struct{})
	c.OnDeliver(func(string) {
		// Would block for ever if deliver still held demandMu.
		c.Subscribe("/other/print", nil, Every(time.Second), nil)()
		close(done)
	})
	c.deliver("/ip/dns/print", nil, nil)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnDeliver ran under the demand lock")
	}
}
