package historywire

import (
	"log"
	"sort"
	"sync"

	"mikrodash/internal/history"
)

// The monitoring-coverage writer: `history.PlanCoverage`'s decisions, carried
// out against `monitor_runs`.
//
// A SEPARATE TYPE FROM `Wire`, with its own two-method store, rather than two
// more methods on `Store`. The history store is implemented by every fake in
// this package's tests and by the database; coverage is one small, separate job
// with its own lifecycle, and widening the shared interface for it would make
// every fake carry methods it never exercises.

// CoverageStore is the database side of coverage.
type CoverageStore interface {
	OpenMonitorRun(routerID string, at int64) (int64, error)
	TouchMonitorRuns(ids []int64, at int64) error
}

// Coverage holds the run currently open for each observed router.
type Coverage struct {
	mu        sync.Mutex
	enabled   bool
	store     CoverageStore
	open      map[string]int64 // router -> run id
	lastTouch int64
}

// NewCoverage builds the writer. `enabled` is `-history`: an install that
// records nothing records no coverage either, so "monitored" means "recorded".
func NewCoverage(enabled bool, store CoverageStore) *Coverage {
	return &Coverage{enabled: enabled, store: store, open: map[string]int64{}}
}

// Update takes the set of routers observed NOW and opens, closes and touches
// runs to match. Called once a second from the connectivity ticker; it writes
// only when a run opens or closes, plus one heartbeat a minute.
//
// A FAILED OPEN IS LEFT UNOPENED, not recorded as open: the router is still
// covered and still not open next second, so the plan asks again. Recording it
// as open with no id would touch nothing and close nothing, silently.
func (c *Coverage) Update(covered map[string]bool, now int64) {
	if c == nil || !c.enabled || c.store == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	openSet := make(map[string]bool, len(c.open))
	for id := range c.open {
		openSet[id] = true
	}
	ops := history.PlanCoverage(openSet, covered, now, c.lastTouch)

	// ONE TOUCH carries both the closes (their final moment) and, when it is
	// due, the heartbeat of every run that stays open.
	var touch []int64
	for _, id := range ops.Close {
		touch = append(touch, c.open[id])
		delete(c.open, id)
	}
	if ops.Touch {
		for _, run := range c.open {
			touch = append(touch, run)
		}
		c.lastTouch = now
	}
	if len(touch) > 0 {
		if err := c.store.TouchMonitorRuns(touch, now); err != nil {
			log.Printf("[coverage] touch %d run(s): %v", len(touch), err)
		}
	}
	for _, id := range ops.Open {
		run, err := c.store.OpenMonitorRun(id, now)
		if err != nil {
			log.Printf("[coverage] open a run for %s: %v", id, err)
			continue
		}
		c.open[id] = run
	}
}

// CloseAll ends every open run at `now`. Called on shutdown, after the ticker
// has stopped and before the database closes, so a clean stop is recorded to
// the second rather than up to a minute early.
func (c *Coverage) CloseAll(now int64) {
	if c == nil || !c.enabled || c.store == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.open) == 0 {
		return
	}
	ids := make([]int64, 0, len(c.open))
	for _, run := range c.open {
		ids = append(ids, run)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if err := c.store.TouchMonitorRuns(ids, now); err != nil {
		log.Printf("[coverage] close %d run(s) on shutdown: %v", len(ids), err)
	}
	c.open = map[string]int64{}
}
