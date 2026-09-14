package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// EVERY ROUTER AT ONCE, AND THE ANSWERS STILL IN ORDER.
//
// Serially a fleet request costs the SUM of its routers, so two on the far end
// of a tunnel hold an HTTP request for as long as they like and sixteen is a
// page that never loads. The fix is only a fix if it is measured: eight targets
// that each take 120ms must finish in about 120ms, not about a second.
//
// ORDER IS THE OTHER HALF. The caller pairs each answer with the router it asked
// about by POSITION, so a result slice that came back in completion order would
// label every row with somebody else's router.

func TestFleetEachAsksEveryRouterAtOnce(t *testing.T) {
	targets := make([]fleetTarget, 8)
	for i := range targets {
		targets[i] = fleetTarget{ID: string(rune('a' + i)), Label: "r"}
	}
	var running, peak int64

	start := time.Now()
	got := fleetEach(context.Background(), targets, func(_ context.Context, tg fleetTarget) string {
		n := atomic.AddInt64(&running, 1)
		for {
			p := atomic.LoadInt64(&peak)
			if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
				break
			}
		}
		time.Sleep(120 * time.Millisecond)
		atomic.AddInt64(&running, -1)
		return tg.ID
	})
	elapsed := time.Since(start)

	if int64(len(targets)) != atomic.LoadInt64(&peak) {
		t.Errorf("%d of %d routers were being asked at the busiest moment",
			peak, len(targets))
	}
	if elapsed > 700*time.Millisecond {
		t.Errorf("eight 120ms reads took %v — they are running one after another", elapsed)
	}
	for i, tg := range targets {
		if got[i] != tg.ID {
			t.Fatalf("answer %d is %q, want %q — the results are in completion order",
				i, got[i], tg.ID)
		}
	}
}

// AND THE DEADLINE IS THE REQUEST'S. A viewer who navigates away must not leave
// sixteen dials running.
func TestFleetEachPassesTheCallersCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := fleetEach(ctx, []fleetTarget{{ID: "a"}}, func(c context.Context, _ fleetTarget) bool {
		return c.Err() != nil
	})
	if len(got) != 1 || !got[0] {
		t.Error("a cancelled request reached the read as a live context")
	}
}
