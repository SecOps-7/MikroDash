package geo

// The lazily-built, idle-evicted city index — the lifecycle half of
// `src/cityIndex.js`. `BuildCityIndex` and `Search` are the decoding and the
// search; this is when they run and when the result is dropped.
//
// ── THREE BEHAVIOURS, EACH LOAD-BEARING ─────────────────────────────────────
//
// 1. BUILT ON FIRST USE. The live comment: "Choosing a town is a rare
//    administrative act, so most installs would otherwise carry tens of
//    megabytes forever for a list nobody opens." Building at startup would put
//    that cost on every install including those that never open the picker.
//
// 2. EVICTED AFTER TEN IDLE MINUTES, with the timer reset by every search. The
//    memory goes back when the operator stops typing.
//
// 3. A FAILURE IS RECORDED AND NEVER RETRIED — `if (_reason) return false;
//    // already failed; do not retry every keystroke`. Without it a missing data
//    file means a rebuild attempt per character typed into the picker, each
//    reading and failing on the same absent file.

import (
	"sync"
	"time"
)

// cityIdleEvict is `IDLE_EVICT_MS`.
const cityIdleEvict = 10 * time.Minute

// CityHolder owns the index's lifecycle. The zero value is not usable; call
// NewCityHolder.
type CityHolder struct {
	mu    sync.Mutex
	dir   string
	index *CityIndex
	// reason is why the build failed, and its presence is what stops a retry.
	// Empty means "not tried, or succeeded" — the two are distinguished by
	// `index`.
	reason string
	timer  *time.Timer
	// idle is the eviction interval. A FIELD so a test can shorten it and watch
	// the REAL timer, rather than asserting on a recorded deadline beside it:
	// `scheduledAt` was that shadow, and a mutation that updated it while
	// leaving the timer alone passed — the shadow moved and the behaviour did
	// not. Ten minutes unless a test says otherwise.
	idle time.Duration
	// now and build are seams for the tests: an idle eviction measured in real
	// minutes is not something a suite can wait for.
	build func(string) (*CityIndex, error)
}

// NewCityHolder returns a holder that will build from `dir` on first use.
func NewCityHolder(dir string) *CityHolder {
	return &CityHolder{dir: dir, build: BuildCityIndex, idle: cityIdleEvict}
}

// ensure is `_ensure()`: reset the eviction timer, then build if needed.
//
// THE TIMER IS RESET EVEN WHEN THE BUILD HAS ALREADY FAILED, matching the live
// order — `clearTimeout`/`setTimeout` run before the `_reason` check. It costs
// nothing and keeps the two implementations' behaviour identical for a caller
// watching when the memory is released.
func (h *CityHolder) ensure() (*CityIndex, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.timer != nil {
		h.timer.Stop()
	}
	// THE CALLBACK IS A NAMED METHOD, not a closure, so the test that drives
	// eviction drives THIS code rather than a reimplementation of it. A seam
	// that bypasses the path it stands in for tests nothing: mutating the
	// closure's body survived while `evictNow` had its own copy.
	h.timer = time.AfterFunc(h.idle, h.evict)

	if h.index != nil {
		return h.index, true
	}
	if h.reason != "" {
		// ALREADY FAILED. Not retried, or a missing data file costs a failed
		// read per keystroke.
		return nil, false
	}
	idx, err := h.build(h.dir)
	if err != nil {
		h.reason = err.Error()
		h.index = nil
		return nil, false
	}
	h.index = idx
	return idx, true
}

// Available reports whether the picker can be offered, building if necessary.
func (h *CityHolder) Available() bool {
	_, ok := h.ensure()
	return ok
}

// UnavailableReason is why the build failed, or empty.
func (h *CityHolder) UnavailableReason() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reason
}

// Search builds if needed and searches. An unavailable index answers an EMPTY
// SLICE, never nil: the payload is JSON-encoded and the client iterates it.
func (h *CityHolder) Search(q, limit string) []Place {
	idx, ok := h.ensure()
	if !ok {
		return []Place{}
	}
	return idx.Search(q, limit)
}

// ── TESTING SEAMS ───────────────────────────────────────────────────────────
//
// An idle eviction measured in minutes is not something a suite can wait for,
// and a holder whose eviction cannot be driven is a behaviour nothing checks —
// which is how this project has repeatedly found untested rules. These are
// unexported and used only by the tests in this package.

// evict drops the index. It is what the idle timer calls, and dropping an index
// is NOT a failure — `reason` is deliberately untouched, or an eviction would
// make the picker permanently unavailable rather than merely cold.
func (h *CityHolder) evict() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.index, h.timer = nil, nil
}

// held reports whether an index is currently in memory.
func (h *CityHolder) held() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.index != nil
}

// evictNow runs the eviction the timer would, without waiting for it. It calls
// the SAME method the timer calls — see the note at the `AfterFunc` above.
func (h *CityHolder) evictNow() {
	h.mu.Lock()
	if h.timer != nil {
		h.timer.Stop()
	}
	h.mu.Unlock()
	h.evict()
}

// stop releases the timer, so a test does not leave one running.
func (h *CityHolder) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}
