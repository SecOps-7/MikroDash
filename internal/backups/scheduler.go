package backups

// The scheduler: which routers are due, and running them one at a time.
//
// ── EVERYTHING IS INJECTED ──────────────────────────────────────────────────
//
// The whole thing can be driven in a test with no router, no database and no
// clock. That is the original's design and it is worth keeping: a scheduler that
// needs hardware to test is a scheduler nobody tests, and this one decides
// whether backups happen at all.
//
// ── ONE ROUTER AT A TIME, AND NEVER THE SAME ONE TWICE ──────────────────────
//
// Runs go through the caller's per-router write queue, and `running` guards
// against a slow run being started again by the next tick. A backup holds a
// router's flash and an API channel for as long as it takes; two at once on the
// same device is the failure the queue exists to prevent, and the tick interval
// is no guarantee the previous run has finished.
//
// ── A FAILING ROUTER DOES NOT STOP THE FLEET ────────────────────────────────
//
// Each router's error is logged and the loop continues. One unreachable device
// must not mean the other five are never backed up — the same failure shape as a
// retention sweep aborting on the first locked file.

import (
	"fmt"
	"sync"
	"time"
)

// TickInterval is how often to ask whether anything is due. Cheap: one integer
// compare per router.
const TickInterval = 5 * time.Minute

// SchedRouter is the slice of a router record the scheduler needs.
type SchedRouter struct {
	ID       string
	Label    string
	Disabled bool
	Backup   *Backup
}

// SchedDeps is everything the scheduler reaches outside itself.
type SchedDeps struct {
	Routers  func() []SchedRouter
	LastRun  func(routerID string) int64
	Timezone func() string
	// Queue serialises work per router. The caller owns it because the same
	// queue must also hold the writes a page makes — a backup and a firewall
	// edit on one router are not independent.
	Queue func(routerID string, fn func() error) error
	// RunFor takes one backup. Separate from Queue so a test can assert what was
	// ATTEMPTED without providing a router.
	RunFor func(r SchedRouter) error
	Log    func(string)
	Now    func() time.Time
}

// Scheduler owns the timer and the in-flight set.
type Scheduler struct {
	deps SchedDeps

	mu      sync.Mutex
	running map[string]bool
	stop    chan struct{}
	stopped bool
}

func NewScheduler(d SchedDeps) *Scheduler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = func(string) {}
	}
	return &Scheduler{deps: d, running: map[string]bool{}}
}

// DueRouters is every router whose backup is due right now.
//
// A DISABLED ROUTER IS SKIPPED BEFORE `IsDue` IS ASKED. It is not that its
// backup is not due — it is that the router is not in service, and asking would
// answer a question about a device nobody is polling.
func (s *Scheduler) DueRouters(now int64) []SchedRouter {
	tz := ""
	if s.deps.Timezone != nil {
		tz = s.deps.Timezone()
	}
	out := []SchedRouter{}
	for _, r := range s.deps.Routers() {
		if r.Disabled {
			continue
		}
		if IsDue(r.Backup, s.deps.LastRun(r.ID), now, tz) {
			out = append(out, r)
		}
	}
	return out
}

// Tick runs every due router, one at a time.
func (s *Scheduler) Tick() {
	now := s.deps.Now().UnixMilli()
	for _, r := range s.DueRouters(now) {
		if !s.claim(r.ID) {
			// Still running from an earlier tick. Not an error and not worth a
			// log line every five minutes — a backup legitimately outlasts the
			// interval on a large configuration.
			continue
		}
		err := s.deps.Queue(r.ID, func() error {
			defer s.release(r.ID)
			return s.deps.RunFor(r)
		})
		if err != nil {
			// The queue refused it, so the deferred release never ran.
			s.release(r.ID)
			s.deps.Log(fmt.Sprintf("[backup][%s] scheduler error: %v", r.Label, err))
		}
	}
}

// claim reports whether this router was free, and takes it if so.
func (s *Scheduler) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Scheduler) release(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

// Running reports whether a router is being backed up right now. The page shows
// it, so a viewer can see why the button is disabled.
func (s *Scheduler) Running(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}

// Start begins ticking.
//
// DELIBERATELY DOES NOT TICK IMMEDIATELY. A restart should not stampede the
// fleet before the sessions it needs have even connected — and a process that
// crash-loops would otherwise back up every router on every restart.
func (s *Scheduler) Start(every time.Duration) {
	if every <= 0 {
		every = TickInterval
	}
	s.mu.Lock()
	if s.stop != nil || s.stopped {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.stop = stop
	s.mu.Unlock()

	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.Tick()
			}
		}
	}()
}

// Stop ends the loop. Safe to call twice, and safe to call having never started.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	// The in-flight set is cleared, matching the original's `_running.clear()`.
	// A run still executing will call release on a map that no longer has it,
	// which is harmless.
	s.running = map[string]bool{}
}
