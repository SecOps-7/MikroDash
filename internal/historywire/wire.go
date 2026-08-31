// Package historywire feeds collector samples into `internal/history`'s
// bucketer and persists the minute rows it rolls over.
//
// ── IT IS CUTOVER CODE AND IT IS OFF UNLESS SWITCHED ON ────────────────────
//
// Two processes bucketing the same samples into one SQLite file DOUBLE every
// row, and during coexistence Node is the one doing it. So `New` takes
// `enabled bool`, the server passes false, and a disabled wire records nothing —
// not even into its own in-memory buckets, so turning it on later starts from a
// clean minute rather than flushing a backlog of samples nobody wrote.
//
// `PORT-QUEUE.md`'s checklist lists this as step 0: code that must exist and be
// tested BEFORE the window rather than discovered inside it.
//
// ── BOTH SAMPLES COME FROM THE EMIT SEAM, AND THAT IS DELIBERATE ───────────
//
// The live app records ping from a hook rather than from the room-scoped emit,
// and its comment says why: `recordPing` "used to sit on the router-wide emit()
// alone, so the moment ping:update became page-scoped (issue #108) history would
// have stopped being written, silently, with the Ping report going empty and
// nothing logging an error."
//
// This port's seam is the emit CLOSURE, which every collector payload passes
// through before any room is chosen — so it sees a page-scoped event exactly as
// it sees a router-wide one. The hazard that forced the live app onto a hook
// does not exist here, and that is the reason to use the seam rather than an
// accident of it.
//
// TRAFFIC is a per-second stream: `/interface/monitor-traffic` is fixed at one
// sample per second per interface, and `history.Writer`'s megabyte accumulation
// assumes exactly that. It is not a poll interval that can be tuned.
package historywire

import (
	"log"
	"sync"

	"mikrodash/internal/collect"
	"mikrodash/internal/history"
)

// Store is the subset of `*db.DB` this package needs. An interface so the tests
// can assert what WOULD have been written, without a database.
type Store interface {
	PersistHistoryLogged(rows []history.Row) int
}

type Wire struct {
	mu      sync.Mutex
	enabled bool
	w       *history.Writer
	store   Store

	// The connectivity half — see conn.go. A SEPARATE LOCK from `mu`: a
	// connectivity event and a traffic sample have nothing to say to each other,
	// and sharing one mutex would put every router's samples behind one router's
	// debounce.
	connMu sync.Mutex
	conns  map[string]*connState
}

func New(enabled bool, store Store) *Wire {
	return &Wire{
		enabled: enabled, w: history.NewWriter(), store: store,
		conns: map[string]*connState{},
	}
}

func (w *Wire) Enabled() bool { return w != nil && w.enabled }

// Record takes one collector payload and persists whatever minute rows it
// completed.
//
// ── THE TYPE IS THE GUARD, AS IN alertwire ─────────────────────────────────
//
// Checking the event name alone would let a renamed event stop recording
// silently — which is the exact failure the live comment above describes. A
// payload of the wrong type records nothing rather than being coerced.
func (w *Wire) Record(routerID, event string, payload any) {
	if w == nil || !w.enabled || routerID == "" {
		return
	}
	var rows []history.Row

	switch p := payload.(type) {
	case *collect.TrafficSample:
		if event != "traffic:update" || p == nil {
			return
		}
		w.mu.Lock()
		rows = w.w.RecordTraffic(routerID, p.IfName, p.RxMbps, p.TxMbps, p.TS)
		w.mu.Unlock()

	case *collect.PingPayload:
		if event != "ping:update" || p == nil {
			return
		}
		// ── A PAYLOAD WITH NO LOSS READING IS NOT A ZERO-LOSS SAMPLE ──────
		//
		// The live guard is `typeof data.loss === 'number'`, and it is the whole
		// filter: a ping collector that could not run emits a payload with no
		// loss, and recording that as 0% would write a minute of perfect
		// connectivity for a router that was unreachable.
		if p.Loss == nil {
			return
		}
		// `rtt` is separately absent: a lost ping has a loss figure and no round
		// trip. `hasRTT` false is what makes the stored column NULL rather than
		// a plausible 0 ms.
		rtt, hasRTT := 0.0, false
		if p.RTT != nil {
			rtt, hasRTT = *p.RTT, true
		}
		w.mu.Lock()
		rows = w.w.RecordPing(routerID, p.Target, rtt, hasRTT, float64(*p.Loss), p.TS)
		w.mu.Unlock()

	default:
		return
	}

	w.persist(rows)
}

// Flush writes a router's open buckets.
//
// ── ON SESSION TEARDOWN, OR THE LAST MINUTE IS LOST ───────────────────────
//
// A bucket only rolls over when the NEXT minute's first sample arrives, so a
// session that ends mid-minute leaves that minute unwritten. The live app calls
// this for the same reason: "flush all open buckets — call on session teardown
// to avoid data loss".
func (w *Wire) Flush(routerID string) {
	if w == nil || !w.enabled || routerID == "" {
		return
	}
	w.mu.Lock()
	rows := w.w.Flush(routerID)
	w.mu.Unlock()
	w.persist(rows)
}

func (w *Wire) persist(rows []history.Row) {
	if len(rows) == 0 || w.store == nil {
		return
	}
	// OUTSIDE THE BUCKET LOCK. Persisting takes a database transaction, and
	// holding the bucket lock across it would stall every other router's samples
	// behind one router's write.
	if n := w.store.PersistHistoryLogged(rows); n != len(rows) {
		log.Printf("[history] persisted %d of %d rows", n, len(rows))
	}
}
