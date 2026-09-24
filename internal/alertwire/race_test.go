package alertwire

import (
	"sync"
	"testing"
	"time"
)

// TestEvaluationRacesNeitherTheClockNorTheSettings. Two races, found by review:
// forRouter set the store's clock under the wire's lock while a rule run read it
// under the router's, and SetSettings replaced an evaluator's settings under the
// wire's lock while a rule run read them under the router's. Both writes now
// happen under the router's lock. Meaningful under -race.
//
// The in-memory store, because the persisting one's fake database keeps maps of
// its own, which would be the test's race rather than the code's.
func TestEvaluationRacesNeitherTheClockNorTheSettings(t *testing.T) {
	w, _ := wireOn(t)
	w.now = func() int64 { return time.Now().UnixMilli() } // wireOn's fake clock is not goroutine-safe
	w.SetPersisting("r-1", false)

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if (i+g)%2 == 0 {
					w.Evaluate(router, "system:update", cpuHigh())
				} else {
					w.Evaluate(router, "system:update", cpuNormal())
				}
			}
		}(g)
	}
	// ── THE SETTINGS RACER IS GONE, WITH THE SETTINGS ────────────────────
	//
	// A third goroutine hammered `SetSettings` here, because it replaced an
	// evaluator's settings under the wire's lock while a rule read them under
	// the router's. There is nothing to replace now: the thresholds are a
	// property of each notification channel, read at delivery, and no evaluator
	// holds anything that changes at runtime.
	//
	// The CLOCK half above is untouched and is the half that still races.
	wg.Wait()
}
