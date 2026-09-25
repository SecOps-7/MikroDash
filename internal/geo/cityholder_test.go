package geo

// The city index's LIFECYCLE. `cityindex_test.go` covers the decoding and the
// search; what is here is when the build runs and when the result is dropped —
// three behaviours the live module is explicit about and that a search test
// cannot see.

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// counted returns a holder whose build is instrumented and cannot touch disk.
func counted(t *testing.T, idx *CityIndex, err error) (*CityHolder, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	h := NewCityHolder("unused")
	h.build = func(string) (*CityIndex, error) {
		mu.Lock()
		n++
		mu.Unlock()
		return idx, err
	}
	return h, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// TestTheIndexIsBuiltOnceAndOnFirstUse.
//
// "Choosing a town is a rare administrative act, so most installs would
// otherwise carry tens of megabytes forever for a list nobody opens." Building
// at construction would put that cost on every install; rebuilding per call
// would put it on every keystroke.
func TestTheIndexIsBuiltOnceAndOnFirstUse(t *testing.T) {
	h, builds := counted(t, &CityIndex{}, nil)
	defer h.stop()

	if builds() != 0 {
		t.Errorf("the index was built at construction (%d) -- it must wait for a search",
			builds())
	}
	for i := 0; i < 5; i++ {
		if !h.Available() {
			t.Fatal("Available answered false for a build that succeeds")
		}
	}
	if builds() != 1 {
		t.Errorf("%d builds across five searches, want 1 -- the index is being rebuilt per call",
			builds())
	}
}

// TestAFailedBuildIsNotRetried.
//
// `if (_reason) return false; // already failed; do not retry every keystroke`.
// Without it, a missing data file means one failed read PER CHARACTER typed into
// the picker.
func TestAFailedBuildIsNotRetried(t *testing.T) {
	h, builds := counted(t, nil, errors.New("no such file"))
	defer h.stop()

	for i := 0; i < 10; i++ {
		if h.Available() {
			t.Fatal("Available answered true for a build that fails")
		}
	}
	if builds() != 1 {
		t.Errorf("%d builds across ten searches, want 1. A recorded failure must stop the "+
			"retry, or a missing data file costs a failed read per keystroke", builds())
	}
	if h.UnavailableReason() != "no such file" {
		t.Errorf("reason is %q, want the build error", h.UnavailableReason())
	}
	// ...and a search still answers an EMPTY SLICE rather than nil: the payload
	// is JSON-encoded and the client iterates it, where a nil marshals to null.
	if got := h.Search("lon", ""); got == nil {
		t.Error("Search returned nil for an unavailable index -- it marshals to null")
	} else if len(got) != 0 {
		t.Errorf("Search returned %d places from an unavailable index", len(got))
	}
}

// TestTheIndexIsDroppedWhenIdle, and REBUILT on the next search.
//
// Driven by shortening the interval rather than waiting ten minutes, which is
// what makes the behaviour testable at all.
func TestTheIndexIsDroppedWhenIdle(t *testing.T) {
	h, builds := counted(t, &CityIndex{}, nil)
	defer h.stop()

	h.Available()
	if builds() != 1 {
		t.Fatalf("%d builds, want 1", builds())
	}
	// Force the eviction the timer would perform, rather than sleeping for the
	// real interval.
	h.evictNow()
	if h.held() {
		t.Fatal("the index survived an eviction")
	}
	// The next search REBUILDS: eviction is not the same as failure, and a
	// holder that recorded it as one would answer unavailable for ever.
	if !h.Available() {
		t.Error("the index did not rebuild after an idle eviction")
	}
	if builds() != 2 {
		t.Errorf("%d builds, want 2 -- the eviction must not be recorded as a failure",
			builds())
	}
	if h.UnavailableReason() != "" {
		t.Errorf("an eviction set a failure reason: %q", h.UnavailableReason())
	}
}

// TestEveryUseResetsTheEvictionClock.
//
// A picker being typed into must not have its index dropped mid-session.
//
// ── IT WATCHES THE REAL TIMER, AND THE FIRST VERSION DID NOT ────────────────
//
// It asserted on a `scheduledAt` field recorded beside the timer — and a
// mutation that armed the timer only once while still updating that field
// PASSED. The deadline moved and the behaviour did not. The interval is a field
// now, so the test shortens it and watches what actually happens.
func TestEveryUseResetsTheEvictionClock(t *testing.T) {
	h, builds := counted(t, &CityIndex{}, nil)
	defer h.stop()
	h.idle = 60 * time.Millisecond

	h.Available()
	// Four searches at half the interval. A timer that is RE-ARMED each time
	// never fires; one armed only on the first call fires midway through.
	for i := 0; i < 4; i++ {
		time.Sleep(30 * time.Millisecond)
		h.Search("lon", "")
	}
	if builds() != 1 {
		t.Errorf("%d builds across 120ms of searching at half the 60ms idle interval, want 1. "+
			"The eviction timer is not being re-armed, so the index is dropped while somebody "+
			"is still typing into the picker", builds())
	}

	// ...and it DOES still evict once the searching stops, or the assertion
	// above would pass against a holder that never evicts at all.
	time.Sleep(120 * time.Millisecond)
	if h.held() {
		t.Error("the index survived well past the idle interval with no searches")
	}
}
