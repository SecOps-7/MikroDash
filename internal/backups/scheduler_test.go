package backups

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func at(s string) *string { return &s }

// harness builds a scheduler over fakes, and records what was attempted.
type harness struct {
	mu       sync.Mutex
	routers  []SchedRouter
	lastRun  map[string]int64
	attempts []string
	queueErr map[string]error
	runErr   map[string]error
	logs     []string
	// hold blocks RunFor for a router until released, so an overlapping tick can
	// be tested without depending on timing.
	hold chan struct{}
}

func (h *harness) scheduler(now time.Time) *Scheduler {
	return NewScheduler(SchedDeps{
		Routers:  func() []SchedRouter { return h.routers },
		LastRun:  func(id string) int64 { return h.lastRun[id] },
		Timezone: func() string { return "UTC" },
		Queue: func(id string, fn func() error) error {
			if err := h.queueErr[id]; err != nil {
				return err
			}
			return fn()
		},
		RunFor: func(r SchedRouter) error {
			h.mu.Lock()
			h.attempts = append(h.attempts, r.ID)
			h.mu.Unlock()
			if h.hold != nil {
				<-h.hold
			}
			return h.runErr[r.ID]
		},
		Log: func(s string) { h.mu.Lock(); h.logs = append(h.logs, s); h.mu.Unlock() },
		Now: func() time.Time { return now },
	})
}

// 2026-03-15 09:30 UTC, with a daily 08:00 schedule and a last run late on the
// PREVIOUS day — the shape due.go's corpus was built around.
var schedNow = time.UnixMilli(1773567000000)

func dueRouter(id string) SchedRouter {
	return SchedRouter{ID: id, Label: "R-" + id,
		Backup: &Backup{Enabled: true, Schedule: "daily", Time: at("08:00")}}
}

func lateYesterday() int64 { return schedNow.UnixMilli() - 22*3600*1000 }

func TestDueRoutersSkipsDisabledBeforeAsking(t *testing.T) {
	h := &harness{
		routers: []SchedRouter{dueRouter("a"), dueRouter("b"), dueRouter("c")},
		lastRun: map[string]int64{"a": lateYesterday(), "b": lateYesterday(), "c": lateYesterday()},
	}
	h.routers[1].Disabled = true

	due := h.scheduler(schedNow).DueRouters(schedNow.UnixMilli())
	if len(due) != 2 || due[0].ID != "a" || due[1].ID != "c" {
		t.Fatalf("due = %v, want a and c — a disabled router is not in service", ids(due))
	}
}

func TestDueRoutersHonoursIsDue(t *testing.T) {
	h := &harness{
		routers: []SchedRouter{dueRouter("a"), dueRouter("b")},
		lastRun: map[string]int64{
			"a": lateYesterday(),
			// Already ran after 08:00 today.
			"b": schedNow.UnixMilli() - 60*1000,
		},
	}
	due := h.scheduler(schedNow).DueRouters(schedNow.UnixMilli())
	if len(due) != 1 || due[0].ID != "a" {
		t.Fatalf("due = %v, want only a", ids(due))
	}
}

func TestTickRunsEveryDueRouter(t *testing.T) {
	h := &harness{
		routers: []SchedRouter{dueRouter("a"), dueRouter("b")},
		lastRun: map[string]int64{"a": lateYesterday(), "b": lateYesterday()},
	}
	h.scheduler(schedNow).Tick()
	if len(h.attempts) != 2 {
		t.Fatalf("attempted %v, want both", h.attempts)
	}
}

// TestOneFailingRouterDoesNotStopTheFleet — an unreachable device must not mean
// the other five are never backed up.
func TestOneFailingRouterDoesNotStopTheFleet(t *testing.T) {
	h := &harness{
		routers:  []SchedRouter{dueRouter("a"), dueRouter("b"), dueRouter("c")},
		lastRun:  map[string]int64{"a": lateYesterday(), "b": lateYesterday(), "c": lateYesterday()},
		queueErr: map[string]error{"b": errors.New("router busy")},
	}
	h.scheduler(schedNow).Tick()
	if len(h.attempts) != 2 || h.attempts[0] != "a" || h.attempts[1] != "c" {
		t.Fatalf("attempted %v, want a and c — b was refused by the queue", h.attempts)
	}
	if len(h.logs) != 1 {
		t.Errorf("logged %v, want one scheduler error", h.logs)
	}
}

// TestASlowRunIsNotStartedTwice is what `running` exists for. A backup holds the
// router's flash and an API channel for as long as it takes, and the tick
// interval is no guarantee the previous one has finished.
func TestASlowRunIsNotStartedTwice(t *testing.T) {
	h := &harness{
		routers: []SchedRouter{dueRouter("a")},
		lastRun: map[string]int64{"a": lateYesterday()},
		hold:    make(chan struct{}),
	}
	s := h.scheduler(schedNow)

	done := make(chan struct{})
	go func() { s.Tick(); close(done) }()

	// Wait for the first run to be in flight.
	deadline := time.After(2 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.attempts)
		h.mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the first run never started")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !s.Running("a") {
		t.Error("Running says no while a run is in flight")
	}

	// A second tick while the first is held must attempt nothing.
	//
	// IN A GOROUTINE WITH A DEADLINE, because the interesting failure BLOCKS
	// rather than returning a wrong answer: without the guard this Tick calls
	// RunFor, which waits on the same hold, and the test hangs instead of
	// failing. Verified by mutation — the first run of that mutation timed out
	// at six minutes and reported nothing useful.
	second := make(chan struct{})
	go func() { s.Tick(); close(second) }()
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		close(h.hold) // let the held run finish so the test can exit
		t.Fatal("a second tick BLOCKED while the first run was in flight — the " +
			"in-flight guard is missing, so it called RunFor on a router that " +
			"was already being backed up")
	}
	h.mu.Lock()
	n := len(h.attempts)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("attempted %d runs, want 1 — a slow run was started twice", n)
	}

	close(h.hold)
	<-done
	if s.Running("a") {
		t.Error("Running still says yes after the run finished")
	}
}

// TestAQueueRefusalReleasesTheRouter — the deferred release never runs when the
// queue refuses, so the router would otherwise stay claimed for ever and never
// be backed up again.
func TestAQueueRefusalReleasesTheRouter(t *testing.T) {
	h := &harness{
		routers:  []SchedRouter{dueRouter("a")},
		lastRun:  map[string]int64{"a": lateYesterday()},
		queueErr: map[string]error{"a": errors.New("nope")},
	}
	s := h.scheduler(schedNow)
	s.Tick()
	if s.Running("a") {
		t.Fatal("a router refused by the queue stayed claimed — it would never " +
			"be backed up again")
	}
	// And the next tick tries again.
	delete(h.queueErr, "a")
	s.Tick()
	if len(h.attempts) != 1 {
		t.Errorf("attempted %v, want one after the queue recovered", h.attempts)
	}
}

// TestStartDoesNotTickImmediately. A restart must not stampede the fleet before
// the sessions it needs have connected — and a crash-looping process would
// otherwise back up every router on every restart.
func TestStartDoesNotTickImmediately(t *testing.T) {
	h := &harness{
		routers: []SchedRouter{dueRouter("a")},
		lastRun: map[string]int64{"a": lateYesterday()},
	}
	s := h.scheduler(schedNow)
	s.Start(time.Hour)
	defer s.Stop()

	time.Sleep(20 * time.Millisecond)
	h.mu.Lock()
	n := len(h.attempts)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("Start ran %d backup(s) immediately", n)
	}
}

func TestStopIsSafeTwiceAndWithoutStart(t *testing.T) {
	h := &harness{}
	s := h.scheduler(schedNow)
	s.Stop() // never started
	s.Stop() // twice
	s.Start(time.Hour)
	s.Stop()
	s.Stop()
}

func ids(rs []SchedRouter) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}
