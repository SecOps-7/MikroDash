// Package roslimit bounds how many API commands may be in flight against ONE
// router at a time.
//
// ── THE CONSTRAINT IS THE DEVICE, NOT THIS PROCESS ──────────────────────────
//
// CLAUDE.md states the bottleneck plainly: "more efficient means fewer router
// channels, not faster payload assembly". A hAP ac2 does not care what this
// server's memory looks like; it cares how many API channels are open on it.
//
// Nothing bounded that. Every collector owns its own timer goroutine -- 29
// poll loops across the three pools -- and each one calls `reader.Do` whenever
// its interval elapses. The client is safe for concurrent use (async mode tags
// every call), so nothing errors; the commands simply all go out at once. The
// staggered startup in `startCollectors` spreads the FIRST tick by 75 ms per
// burst group and says nothing about the steady state, where independent
// intervals drift into alignment on their own.
//
// ── AND WHY THE GATE IS KEYED BY ROUTER ─────────────────────────────────────
//
// Three separate pools reach the same devices: the viewing session
// (`internal/session`), the background pool for unwatched routers
// (`internal/routers`), and the alerting pool (`internal/alertpool`). A cap
// inside any one of them is not a cap on the router, because the other two keep
// their own count -- so a router being watched AND alerted AND polled for the
// Devices page would see three independent budgets.
//
// The gate is therefore process-wide and keyed by router id, which is the only
// key that matches what is actually scarce.
//
// ── WHAT THIS IS NOT ────────────────────────────────────────────────────────
//
// It is not the single-reader refactor. That remains a costed proposal in
// docs/architecture-next.md: 40-45 files, of which ~2,250 lines are
// source-scanning tests pinned to literal text that cannot be adapted, only
// re-authored. This is a dozen lines at the one function every collector read
// already passes through, and it delivers the documented goal. Revisit the
// refactor once this shows whether contention is real.
package roslimit

import (
	"os"
	"strconv"
	"sync"
)

// DefaultMax is deliberately generous rather than tuned.
//
// The point is to put a CEILING under a number that had none -- 29 poll loops
// could previously all be inside `Do` at once -- not to throttle normal work. A
// figure low enough to be felt would change collector timing, and timing is what
// 136 gates compare. Eight is above every steady-state burst measured here and
// far below the unbounded case.
const DefaultMax = 8

var (
	mu     sync.Mutex
	gates  = map[string]chan struct{}{}
	maxOne = -1 // resolved once, on first use
)

// max reads the override once. An unparseable or non-positive value falls back
// rather than failing: this is a performance guard, and refusing to start over a
// malformed tuning knob would be a worse failure than ignoring it.
func max() int {
	if maxOne > 0 {
		return maxOne
	}
	maxOne = DefaultMax
	if v := os.Getenv("MIKRODASH_ROUTER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxOne = n
		}
	}
	return maxOne
}

// Acquire blocks until this router has a free slot and returns the release.
//
// The returned function MUST be called, and callers use `defer` at the top of
// the wrapped call so an early return or a panic cannot leak a slot -- a leaked
// slot is permanent, and enough of them deadlock every collector on that router.
//
// An empty id is NOT gated. A router with no id is a test fixture or a session
// being torn down, and blocking those on a shared bucket would couple unrelated
// work.
func Acquire(routerID string) func() {
	if routerID == "" {
		return func() {}
	}
	mu.Lock()
	g, ok := gates[routerID]
	if !ok {
		g = make(chan struct{}, max())
		gates[routerID] = g
	}
	mu.Unlock()

	g <- struct{}{}
	var once sync.Once
	return func() { once.Do(func() { <-g }) }
}

// InFlight reports how many commands hold a slot for this router. For tests and
// diagnostics only.
func InFlight(routerID string) int {
	mu.Lock()
	defer mu.Unlock()
	return len(gates[routerID])
}

// Reset drops every gate. Tests only: it exists so one test's saturation cannot
// leak into the next, and calling it while commands are in flight would let them
// release into a channel nobody is holding.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	gates = map[string]chan struct{}{}
	maxOne = -1
}
