package alertwire

// The alert store for a router whose report data is not kept.
//
// ── THE DATABASE IS THE DE-DUPLICATION MEMORY ──────────────────────────────
//
// `alert.Evaluator.emit` consults the store on every path, and its answers are
// what decide whether an alert fires at all:
//
//	HasOpen   an alert of this type and subject already open is NOT re-raised.
//	          Without it a condition that persists rings on every poll tick.
//	Resolve   an "up" alert fires ONLY if a row was actually closed. With
//	          nothing to close, recovery notifications never fire at all.
//
// So a router whose alerts must notify but leave nothing on disk cannot simply
// have its writes suppressed: that gives repeat down-notifications for ever and
// no recovery notifications — worse than either extreme. It needs the same
// memory somewhere else, which is this.
//
// ── WHAT IS GIVEN UP, DELIBERATELY ─────────────────────────────────────────
//
// This memory dies with the process. An outage that spans a restart is
// re-notified once on the way back, because nothing remembers it was already
// open. That is the honest cost of "notify but never persist" and it is the
// reason the DB is used everywhere else — `alertwire`'s own header says the
// in-memory evaluator is dropped on a router switch, so a durable memory was
// always the point of the store.
//
// The bell, the Devices alert count and Reports → Alerts read rows. A router
// using this store has none, so all three show nothing for it. That is the
// stated intent of turning reporting off, not an oversight.

import "sync"

// memStore satisfies `alert.Store` without touching the database.
//
// One per router, built by `forRouter`, so the routerID arguments are checked
// rather than assumed — a shared instance would be a silent cross-router leak
// if that ever changed.
type memStore struct {
	mu   sync.Mutex
	open map[string]int64
	// next walks DOWNWARDS. A memory id must never be mistaken for a row id, and
	// SQLite rowids start at 1 — so a negative id is unmistakable in a log, in a
	// payload, and in a debugger.
	next int64
}

func newMemStore() *memStore {
	return &memStore{open: map[string]int64{}, next: 0}
}

func memKey(routerID, alertType, subject string) string {
	// The three fields the evaluator keys on, joined with a byte that cannot
	// appear in any of them.
	return routerID + "\x00" + alertType + "\x00" + subject
}

func (m *memStore) HasOpen(routerID, alertType, subject string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.open[memKey(routerID, alertType, subject)]
	return ok
}

// Resolve closes an open condition and returns its id, or nothing.
//
// THE EMPTY SLICE IS LOAD-BEARING: `emit` reads it as "nothing was open, so
// nothing resolved" and returns no alert. Returning a made-up id for a
// condition that was never open would invent a recovery notification for an
// outage that never happened.
func (m *memStore) Resolve(routerID, alertType, subject string) []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey(routerID, alertType, subject)
	id, ok := m.open[k]
	if !ok {
		return nil
	}
	delete(m.open, k)
	return []int64{id}
}

// Record opens a condition. `detail` is discarded — nothing reads it back,
// because nothing renders a row that was never written.
func (m *memStore) Record(routerID, alertType, subject, _ string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next--
	id := m.next
	m.open[memKey(routerID, alertType, subject)] = id
	return id
}

// setNow satisfies the wire's clock seam. This store stamps nothing, because it
// writes nothing; the method exists so both stores are interchangeable.
func (m *memStore) setNow(int64) {}
