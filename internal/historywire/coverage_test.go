package historywire

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

type touchCall struct {
	ids []int64
	at  int64
}

type fakeCoverage struct {
	next     int64
	opened   []string
	touches  []touchCall
	failOpen map[string]bool
}

func (f *fakeCoverage) OpenMonitorRun(routerID string, at int64) (int64, error) {
	if f.failOpen[routerID] {
		return 0, errors.New("disk full")
	}
	f.next++
	f.opened = append(f.opened, routerID)
	return f.next, nil
}

func (f *fakeCoverage) TouchMonitorRuns(ids []int64, at int64) error {
	cp := append([]int64(nil), ids...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	f.touches = append(f.touches, touchCall{cp, at})
	return nil
}

func cov(ids ...string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// TestCoverageOpensOneRunPerRouterAndTouchesThemInOneCall — the heartbeat is
// ONE statement for every open run, which is what keeps its cost flat as the
// fleet grows.
func TestCoverageOpensOneRunPerRouterAndTouchesThemInOneCall(t *testing.T) {
	f := &fakeCoverage{}
	c := NewCoverage(true, f)
	c.Update(cov("a", "b", "c"), 1_000)
	if !reflect.DeepEqual(f.opened, []string{"a", "b", "c"}) {
		t.Fatalf("opened %v, want one run per observed router", f.opened)
	}
	c.Update(cov("a", "b", "c"), 30_000) // inside the minute: nothing
	if len(f.touches) != 0 {
		t.Fatalf("touched inside the minute: %+v", f.touches)
	}
	c.Update(cov("a", "b", "c"), 61_000)
	if len(f.touches) != 1 || len(f.touches[0].ids) != 3 {
		t.Errorf("the heartbeat was %+v, want ONE touch naming all three runs", f.touches)
	}
}

// TestAClosedRunIsTouchedAtTheMomentItCloses — so a run ends when observation
// did, not up to a minute earlier at its last heartbeat.
func TestAClosedRunIsTouchedAtTheMomentItCloses(t *testing.T) {
	f := &fakeCoverage{}
	c := NewCoverage(true, f)
	c.Update(cov("a", "b"), 1_000)
	c.Update(cov("a"), 5_000) // b is disabled, or its session went
	if len(f.touches) != 1 || f.touches[0].at != 5_000 || len(f.touches[0].ids) != 1 {
		t.Fatalf("closing b produced %+v, want one touch of b's run at 5000", f.touches)
	}
	// And it is forgotten: the next heartbeat names only a.
	c.Update(cov("a"), 70_000)
	if last := f.touches[len(f.touches)-1]; len(last.ids) != 1 {
		t.Errorf("the closed run was still touched by the heartbeat: %+v", last)
	}
}

// TestAFailedOpenIsRetried — a run whose open failed must not be recorded as
// open with no id, which would touch and close nothing, silently.
func TestAFailedOpenIsRetried(t *testing.T) {
	f := &fakeCoverage{failOpen: map[string]bool{"a": true}}
	c := NewCoverage(true, f)
	c.Update(cov("a"), 1_000)
	delete(f.failOpen, "a")
	c.Update(cov("a"), 2_000)
	if !reflect.DeepEqual(f.opened, []string{"a"}) {
		t.Errorf("opened %v after the failure cleared, want a's run opened on the retry", f.opened)
	}
}

// TestCloseAllEndsEveryRun — a clean shutdown records itself to the second.
func TestCloseAllEndsEveryRun(t *testing.T) {
	f := &fakeCoverage{}
	c := NewCoverage(true, f)
	c.Update(cov("a", "b"), 1_000)
	c.CloseAll(9_000)
	if len(f.touches) != 1 || f.touches[0].at != 9_000 || len(f.touches[0].ids) != 2 {
		t.Fatalf("shutdown produced %+v, want one touch of both runs at 9000", f.touches)
	}
	c.CloseAll(10_000)
	if len(f.touches) != 1 {
		t.Error("a second CloseAll touched runs that were already closed")
	}
}

// TestADisabledCoverageWritesNothing — without `-history` nothing is recorded,
// so "monitored" stays a synonym for "recorded". The control is the enabled case
// in every test above.
func TestADisabledCoverageWritesNothing(t *testing.T) {
	f := &fakeCoverage{}
	c := NewCoverage(false, f)
	c.Update(cov("a"), 1_000)
	c.Update(cov("a"), 120_000)
	c.CloseAll(130_000)
	if len(f.opened) != 0 || len(f.touches) != 0 {
		t.Errorf("a disabled writer opened %v and touched %+v", f.opened, f.touches)
	}
	var nilCov *Coverage
	nilCov.Update(cov("a"), 1) // nil is inert, not a panic
	nilCov.CloseAll(1)
}

// TestOpenRoutersIsWhatIsOpenNow — the reader extends exactly these runs to
// "now", so a closed run reported here would draw a router as watched after it
// stopped being watched.
func TestOpenRoutersIsWhatIsOpenNow(t *testing.T) {
	c := NewCoverage(true, &fakeCoverage{})
	c.Update(cov("a", "b"), 1_000)
	c.Update(cov("a"), 2_000)
	if got := c.OpenRouters(); !reflect.DeepEqual(got, cov("a")) {
		t.Errorf("OpenRouters = %v, want only a: b's run closed at 2000", got)
	}
	var nilCov *Coverage
	if got := nilCov.OpenRouters(); got == nil || len(got) != 0 {
		t.Errorf("a nil writer answered %v, want an empty set", got)
	}
}
