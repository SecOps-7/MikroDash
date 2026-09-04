package hub

import (
	"sync"
	"testing"
)

// Delivering to a client that has just been removed.
//
// ── THIS PANICKED, AND A PANIC HERE IS THE WHOLE PROCESS ───────────────────
//
// `Remove` closed `Send`; `deliver` sent on it; nothing held a lock across the
// two. A send on a closed channel panics rather than erroring, and nothing
// recovers it — so one browser leaving the Devices page could take down every
// session, the history recorder and the alert dispatcher with it.
//
// Observed on the operator's install on 2026-09-04, through the Devices page's
// two-second refresh, which fires from its own goroutine and is only SIGNALLED
// to stop rather than waited for.

// TestDeliverAfterRemoveDoesNotPanic is the bug, in its simplest form.
func TestDeliverAfterRemoveDoesNotPanic(t *testing.T) {
	h := New()
	c := NewClient("ws-1", 4)
	h.Add(c)
	h.Join(c, "room")
	h.Remove(c)

	// Every fan-out path, because they all reach `deliver` with a `*Client` a
	// caller was already holding.
	h.Send(c, "system:update", map[string]any{"cpu": 1})
	h.Broadcast("room", "system:update", map[string]any{"cpu": 1})
	h.BroadcastAll("system:update", map[string]any{"cpu": 1})
}

// TestRemoveRacingDeliverDoesNotPanic is the shape that actually happened: a
// goroutine mid-delivery while teardown runs. Run with -race.
func TestRemoveRacingDeliverDoesNotPanic(t *testing.T) {
	for i := 0; i < 200; i++ {
		h := New()
		c := NewClient("ws-1", 1) // a queue of ONE, so the send path and the
		h.Add(c)                  // drop path are both exercised
		h.Join(c, "room")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				h.Broadcast("room", "tick", j)
			}
		}()
		go func() {
			defer wg.Done()
			h.Remove(c)
		}()
		wg.Wait()
	}
}

// TestAFrameAfterRemoveIsNotCountedAsDropped — a removed client has nobody to
// deliver to, and counting it would report a browser that closed cleanly as one
// that was too slow, which is a different problem with a different fix.
func TestAFrameAfterRemoveIsNotCountedAsDropped(t *testing.T) {
	h := New()
	c := NewClient("ws-1", 1)
	h.Add(c)
	h.Remove(c)

	h.Send(c, "one", 1)
	h.Send(c, "two", 2)
	if got := c.Dropped(); got != 0 {
		t.Errorf("Dropped = %d after removal; a disconnected browser is not a slow one", got)
	}
}

// TestASlowClientStillCountsDrops — the other direction, or the guard above
// could be swallowing the real drop accounting.
func TestASlowClientStillCountsDrops(t *testing.T) {
	h := New()
	c := NewClient("ws-1", 1)
	h.Add(c)
	h.Join(c, "room")

	// One fills the buffer, the rest have nowhere to go. Nothing drains it.
	for i := 0; i < 5; i++ {
		h.Broadcast("room", "tick", i)
	}
	if got := c.Dropped(); got == 0 {
		t.Error("a client that never drains reported no dropped frames")
	}
}
