package wifiscan

import (
	"sync"
	"sync/atomic"
	"testing"
)

type fakeStream struct{ stops int32 }

func (f *fakeStream) Stop() { atomic.AddInt32(&f.stops, 1) }

func newTestRegistry(now *int64) *Registry {
	return NewRegistry(func() int64 { return atomic.LoadInt64(now) })
}

func okReq() AdmitRequest {
	return AdmitRequest{
		RouterID: "r1", HasROS: true, Connected: true, Iface: "wifi1",
		DurationSec: 30, SocketID: "s1", InterfacesKnown: true,
		Interfaces: []Interface{{Name: "wifi1", ID: "*1", Master: true}},
	}
}

// TestFinishRunsExactlyOnceUnderRacing is the property the whole lifecycle
// rests on.
//
// Several racing things can each legitimately decide a scan is over: the
// wall-clock stop and the stream's own 'done' routinely fire within milliseconds
// of each other on a scan that completed normally. Without the settled guard the
// operator sees two terminal events and the registry deletes an entry twice.
func TestFinishRunsExactlyOnceUnderRacing(t *testing.T) {
	now := int64(1000)
	g := newTestRegistry(&now)

	var dones int32
	s, v := g.Begin(okReq(), func(Done) { atomic.AddInt32(&dones, 1) })
	if !v.OK {
		t.Fatalf("Begin refused: %+v", v)
	}
	st := &fakeStream{}
	g.SetStream(s, st, true)

	var wg sync.WaitGroup
	var settled int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reason := "complete"
			if i%2 == 0 {
				reason = "aborted"
			}
			if g.Finish(s, reason) {
				atomic.AddInt32(&settled, 1)
			}
		}(i)
	}
	wg.Wait()

	if settled != 1 {
		t.Errorf("%d callers each believed they ended the scan", settled)
	}
	if dones != 1 {
		t.Errorf("%d terminal events were emitted", dones)
	}
	if got := atomic.LoadInt32(&st.stops); got != 1 {
		t.Errorf("the stream was stopped %d times", got)
	}
	if g.Size() != 0 {
		t.Errorf("the registry still holds %d scans", g.Size())
	}
}

// TestANaturalEndDoesNotStopTheStream: stopping opens a NEW channel to write
// /cancel with a now-stale tag, which is one more write to a device that has
// just finished scanning.
func TestANaturalEndDoesNotStopTheStream(t *testing.T) {
	now := int64(1000)
	g := newTestRegistry(&now)
	s, _ := g.Begin(okReq(), nil)
	st := &fakeStream{}
	g.SetStream(s, st, true)

	g.MarkNatural(s)
	g.Finish(s, "complete")

	if st.stops != 0 {
		t.Errorf("a stream that ended by itself was stopped %d times", st.stops)
	}
}

// TestTheCooldownStartsWhenTheScanENDS. Starting it at the beginning would let
// an operator relaunch the instant a 120-second scan finished.
func TestTheCooldownStartsWhenTheScanEnds(t *testing.T) {
	now := int64(1000)
	g := newTestRegistry(&now)
	s, _ := g.Begin(okReq(), nil)

	// Nothing recorded yet: a scan in progress is held off by `busy`, not by a
	// cooldown.
	if v := g.Admit(okReq()); v.Code != "busy" {
		t.Fatalf("a second scan on the same router answered %q", v.Code)
	}

	atomic.StoreInt64(&now, 1000+120_000)
	g.Finish(s, "complete")

	// Now the cooldown applies, measured from the END.
	v := g.Admit(okReq())
	if v.Code != "cooldown" {
		t.Fatalf("no cooldown after a scan finished: %+v", v)
	}
	if v.RetryAt != 1000+120_000+CooldownMs {
		t.Errorf("retryAt %d, want %d -- the cooldown is being measured from the START",
			v.RetryAt, 1000+120_000+CooldownMs)
	}
}

// TestBeginIsAtomicAgainstTheFleetCap.
//
// Admission and registration have to be ONE critical section: checking the cap
// and then inserting under a second lock lets several operators past a cap of
// three at the same instant, which is exactly the situation the cap exists for.
// A STARTING GATE, and repeated. Goroutines spawned in a loop reach Begin at
// measurably different times, and the window between a separate check and a
// separate insert is a few instructions wide — a mutation splitting the two
// SURVIVED forty unsynchronised callers. Releasing them all at once and running
// the whole scenario repeatedly is what makes the window reachable.
func TestBeginIsAtomicAgainstTheFleetCap(t *testing.T) {
	for round := 0; round < 200; round++ {
		now := int64(1000)
		g := newTestRegistry(&now)

		var admitted int32
		var wg sync.WaitGroup
		gate := make(chan struct{})
		const callers = 64
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				req := okReq()
				req.RouterID = "r" + itoa2(i)
				req.SocketID = "s" + itoa2(i)
				<-gate
				if _, v := g.Begin(req, nil); v.OK {
					atomic.AddInt32(&admitted, 1)
				}
			}(i)
		}
		close(gate)
		wg.Wait()

		if int(admitted) != FleetCap {
			t.Fatalf("round %d: %d scans admitted against a fleet cap of %d",
				round, admitted, FleetCap)
		}
		if g.Size() != FleetCap {
			t.Fatalf("round %d: the registry holds %d scans", round, g.Size())
		}
	}
}

// TestOneScanPerRouterUnderRacing: forty callers, one router.
func TestOneScanPerRouterUnderRacing(t *testing.T) {
	for round := 0; round < 200; round++ {
		now := int64(1000)
		g := newTestRegistry(&now)

		var admitted int32
		var wg sync.WaitGroup
		gate := make(chan struct{})
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				req := okReq()
				req.SocketID = "s" + itoa2(i)
				<-gate
				if _, v := g.Begin(req, nil); v.OK {
					atomic.AddInt32(&admitted, 1)
				}
			}(i)
		}
		close(gate)
		wg.Wait()

		if admitted != 1 {
			t.Fatalf("round %d: %d scans were started on one router", round, admitted)
		}
	}
}

func TestTakeDirtyStaysQuietWhenNothingChanged(t *testing.T) {
	now := int64(1000)
	g := newTestRegistry(&now)
	s, _ := g.Begin(okReq(), nil)

	if _, _, ok := g.TakeDirty(s); ok {
		t.Error("a scan with no rows had something to flush")
	}
	g.Add(s, Row{Ch: 1})
	if _, _, ok := g.TakeDirty(s); !ok {
		t.Error("a scan with a new row had nothing to flush")
	}
	if _, _, ok := g.TakeDirty(s); ok {
		t.Error("the same rows were flushed twice -- every watching browser would " +
			"receive the whole table every 250ms for the length of the scan")
	}
	g.Add(s, Row{Ch: 2})
	if _, _, ok := g.TakeDirty(s); !ok {
		t.Error("a second row did not mark the table dirty")
	}

	// A settled scan flushes nothing, whatever its flag says.
	g.Add(s, Row{Ch: 3})
	g.Finish(s, "complete")
	if _, _, ok := g.TakeDirty(s); ok {
		t.Error("a finished scan still flushed")
	}
}

func TestTheRetryWithoutDurationIsAllowedExactlyOnce(t *testing.T) {
	// The pure rule first, since all three conditions are necessary.
	for _, tc := range []struct {
		code                string
		used, already, want bool
	}{
		{"bad-parameter", true, false, true},
		{"bad-parameter", true, true, false},   // already retried
		{"bad-parameter", false, false, false}, // never sent =duration=
		{"busy", true, false, false},           // a different trap
		{"", true, false, false},
	} {
		if got := ShouldRetryWithoutDuration(tc.code, tc.used, tc.already); got != tc.want {
			t.Errorf("ShouldRetryWithoutDuration(%q, %v, %v) = %v, want %v",
				tc.code, tc.used, tc.already, got, tc.want)
		}
	}

	now := int64(1000)
	g := newTestRegistry(&now)
	s, _ := g.Begin(okReq(), nil)
	g.SetStream(s, &fakeStream{}, true)

	if !g.RetryWithoutDuration(s, "bad-parameter") {
		t.Fatal("the first bad-parameter trap did not earn a retry")
	}
	if g.RetryWithoutDuration(s, "bad-parameter") {
		t.Error("a second bad-parameter trap retried again -- this loops against a " +
			"router that always answers that way")
	}
}

func TestAbortByOwnerEndsOnlyThatSocketsScans(t *testing.T) {
	now := int64(1000)
	g := newTestRegistry(&now)

	mk := func(router, socket string) *Scan {
		req := okReq()
		req.RouterID, req.SocketID = router, socket
		s, v := g.Begin(req, nil)
		if !v.OK {
			t.Fatalf("Begin(%s,%s) refused: %+v", router, socket, v)
		}
		return s
	}
	mk("r1", "sA")
	mk("r2", "sA")
	mk("r3", "sB")

	if n := g.AbortByOwner("sA"); n != 2 {
		t.Errorf("aborted %d scans for sA, want 2", n)
	}
	if g.Size() != 1 {
		t.Errorf("%d scans remain, want 1", g.Size())
	}
	if _, ok := g.Running("r3"); !ok {
		t.Error("sB's scan was aborted along with sA's")
	}
}

// TestEachScanReportsToItsOwnCaller.
//
// The registry is fleet-wide and the reporting is not. This was a single
// `OnDone` field on Registry, which is wrong the moment two operators scan two
// routers at once: whichever scan started last would take the other's terminal
// event, and the first operator's dialog would sit at "scanning" forever while
// the second saw someone else's results appear in theirs.
func TestEachScanReportsToItsOwnCaller(t *testing.T) {
	now := int64(1000)
	g := newTestRegistry(&now)

	got := map[string]string{} // scanID -> which caller heard about it
	var mu sync.Mutex

	mk := func(router, socket, caller string) *Scan {
		req := okReq()
		req.RouterID, req.SocketID = router, socket
		s, v := g.Begin(req, func(d Done) {
			mu.Lock()
			got[d.ScanID] = caller
			mu.Unlock()
		})
		if !v.OK {
			t.Fatalf("Begin(%s) refused: %+v", router, v)
		}
		return s
	}

	a := mk("r1", "sA", "alice")
	b := mk("r2", "sB", "bob")

	g.Finish(b, "complete")
	g.Finish(a, "aborted")

	mu.Lock()
	defer mu.Unlock()
	if got[a.ID] != "alice" {
		t.Errorf("alice's scan reported to %q", got[a.ID])
	}
	if got[b.ID] != "bob" {
		t.Errorf("bob's scan reported to %q", got[b.ID])
	}
	if len(got) != 2 {
		t.Errorf("%d terminal events for two scans", len(got))
	}
}

// TestAScanWithNoListenerStillFinishes: nil is a legitimate callback, and the
// registry must not depend on someone being there to hear.
func TestAScanWithNoListenerStillFinishes(t *testing.T) {
	now := int64(1000)
	g := newTestRegistry(&now)
	s, _ := g.Begin(okReq(), nil)
	if !g.Finish(s, "complete") {
		t.Error("a scan with no listener did not settle")
	}
	if g.Size() != 0 {
		t.Error("a scan with no listener was left in the registry")
	}
}
