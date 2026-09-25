package wifiscan

import (
	"fmt"
	"sync"
)

// Stream is the open RouterOS channel a scan reads from.
type Stream interface{ Stop() }

// Scan is one running frequency scan.
type Scan struct {
	ID          string
	RouterID    string
	Iface       string
	IfaceID     string
	DurationSec int
	OwnerSocket string
	StartedAt   int64
	EndsAt      int64

	Table *Table

	// settled is what makes finish() run its body exactly once. See Finish.
	settled bool
	// finishedNaturally records that the stream ended by itself.
	finishedNaturally bool
	usedDuration      bool
	retriedNoDuration bool
	stream            Stream
	dirty             bool

	// onDone belongs to the SCAN, not to the registry.
	//
	// It was a single field on Registry, which is wrong the moment two operators
	// scan two routers at once: one callback cannot deliver to two connections,
	// and whichever scan started last would silently steal the other's terminal
	// event. The registry is fleet-wide on purpose; the reporting is not.
	onDone func(Done)
}

// Done is the payload of the single terminal event.
type Done struct {
	ScanID      string
	Reason      string
	Rows        []Row
	SampleCount int
	Truncated   bool
}

// Registry holds every scan running across the fleet, and the per-socket
// cooldowns. One instance for the whole process: the fleet cap is fleet-wide.
type Registry struct {
	mu        sync.Mutex
	scans     map[string]*Scan // routerID -> scan
	cooldowns map[string]int64 // socketID -> when that socket's last scan ENDED
	now       func() int64
	seq       int
}

func NewRegistry(now func() int64) *Registry {
	return &Registry{
		scans: map[string]*Scan{}, cooldowns: map[string]int64{}, now: now,
	}
}

// Admit asks the guard, with the registry's own state.
func (g *Registry) Admit(req AdmitRequest) Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Admit(req, g.state())
}

// state snapshots what the guard needs. Caller holds the lock.
func (g *Registry) state() State {
	running := make(map[string]string, len(g.scans))
	for id, s := range g.scans {
		running[id] = s.Iface
	}
	cool := make(map[string]int64, len(g.cooldowns))
	for k, v := range g.cooldowns {
		cool[k] = v
	}
	return State{Running: running, Cooldowns: cool, Now: g.now()}
}

// Begin admits a request and, if it passes, registers the scan.
//
// ADMISSION AND REGISTRATION ARE ONE CRITICAL SECTION. Checking the fleet cap
// and then inserting under a second lock would let two operators past a cap of
// three at the same instant — the check is only worth anything if nothing can
// change between it and the insert.
func (g *Registry) Begin(req AdmitRequest, onDone func(Done)) (*Scan, Verdict) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if v := Admit(req, g.state()); !v.OK {
		return nil, v
	}
	var ifaceID string
	for _, i := range req.Interfaces {
		if i.Name == req.Iface {
			ifaceID = i.ID
			break
		}
	}
	g.seq++
	started := g.now()
	s := &Scan{
		ID:       fmt.Sprintf("scan-%d-%d", g.seq, started),
		RouterID: req.RouterID, Iface: req.Iface, IfaceID: ifaceID,
		DurationSec: req.DurationSec, OwnerSocket: req.SocketID,
		StartedAt: started, EndsAt: started + int64(req.DurationSec)*1000,
		Table: NewTable(), usedDuration: true, onDone: onDone,
	}
	g.scans[req.RouterID] = s
	return s, Verdict{OK: true}
}

// Finish is THE SINGLE EXIT. Every path arrives here — natural completion, the
// wall-clock stop, an abort, a socket disconnect, a dead connection — and the
// body runs exactly once.
//
// That guard is not defensive programming: several racing things can each
// legitimately decide the scan is over. The wall-clock timer and the stream's
// own 'done' routinely fire within milliseconds of each other on a scan that
// completed normally, and without `settled` the operator would see two terminal
// events and the registry would delete an entry twice.
//
// It returns whether it was the call that actually settled the scan, so a caller
// can tell "I ended it" from "it was already over".
func (g *Registry) Finish(s *Scan, reason string) bool {
	g.mu.Lock()
	if s.settled {
		g.mu.Unlock()
		return false
	}
	s.settled = true

	// Skip Stop() when the stream ended by itself: stopping opens a NEW channel
	// to write /cancel with a now-stale tag, which is one more write to a device
	// that has just finished scanning.
	stream := s.stream
	stopIt := !s.finishedNaturally && stream != nil

	// The cooldown starts when the scan ENDS, not when it starts — otherwise a
	// 120-second scan would leave the operator free to start another the moment
	// it finished.
	if s.OwnerSocket != "" {
		g.cooldowns[s.OwnerSocket] = g.now()
	}
	if cur, ok := g.scans[s.RouterID]; ok && cur == s {
		delete(g.scans, s.RouterID)
	}
	done := Done{
		ScanID: s.ID, Reason: reason, Rows: s.Table.Rows(),
		SampleCount: s.Table.SampleCount, Truncated: s.Table.Truncated,
	}
	onDone := s.onDone
	g.mu.Unlock()

	// Outside the lock: a listener that emitted back into the registry would
	// otherwise deadlock, and stopping a stream is I/O.
	if stopIt {
		stream.Stop()
	}
	if onDone != nil {
		onDone(done)
	}
	return true
}

// SetStream records the open channel so Finish can stop it.
func (g *Registry) SetStream(s *Scan, st Stream, withDuration bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s.stream = st
	s.usedDuration = withDuration
}

// MarkNatural records that the stream ended by itself.
func (g *Registry) MarkNatural(s *Scan) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s.finishedNaturally = true
}

// Add takes one parsed row and reports whether anything changed, so the flush
// can stay quiet when nothing has.
func (g *Registry) Add(s *Scan, r Row) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.settled {
		return
	}
	s.Table.Add(r)
	s.dirty = true
}

// TakeDirty reports whether there is anything new to send, and clears the flag.
//
// A flush that emitted regardless would send the whole table every 250 ms for
// the length of the scan whether or not the radio had reported anything —
// through a WebSocket, to every watching browser.
func (g *Registry) TakeDirty(s *Scan) ([]Row, bool, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.settled || !s.dirty {
		return nil, false, false
	}
	s.dirty = false
	return s.Table.Rows(), s.Table.Truncated, true
}

// ShouldRetryWithoutDuration decides whether a trap is worth one more attempt.
//
// Older builds may not know `=duration=`. Exactly one retry is allowed, without
// it, leaning entirely on the wall-clock stop; a second such trap is fatal. The
// three conditions are all necessary: a different trap is not a syntax problem,
// a scan that never sent `=duration=` cannot be helped by removing it, and
// retrying twice would loop against a router that always answers this way.
func ShouldRetryWithoutDuration(code string, usedDuration, alreadyRetried bool) bool {
	return code == "bad-parameter" && usedDuration && !alreadyRetried
}

// RetryWithoutDuration marks the one retry as taken and reports whether it was
// available.
func (g *Registry) RetryWithoutDuration(s *Scan, code string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.settled || !ShouldRetryWithoutDuration(code, s.usedDuration, s.retriedNoDuration) {
		return false
	}
	s.retriedNoDuration = true
	return true
}

// Running reports the scan on a router, if any.
func (g *Registry) Running(routerID string) (*Scan, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.scans[routerID]
	return s, ok
}

// Size is how many scans are running across the fleet.
func (g *Registry) Size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.scans)
}

// AbortByOwner ends every scan a socket started — what a closing browser tab
// must trigger, or a disconnected operator leaves a radio off the air until the
// wall-clock stop.
func (g *Registry) AbortByOwner(socketID string) int {
	g.mu.Lock()
	var victims []*Scan
	for _, s := range g.scans {
		if s.OwnerSocket == socketID {
			victims = append(victims, s)
		}
	}
	g.mu.Unlock()
	n := 0
	for _, s := range victims {
		if g.Finish(s, "aborted") {
			n++
		}
	}
	return n
}

// AbortAllForRouter ends whatever is running on one router.
func (g *Registry) AbortAllForRouter(routerID string) bool {
	g.mu.Lock()
	s, ok := g.scans[routerID]
	g.mu.Unlock()
	if !ok {
		return false
	}
	return g.Finish(s, "aborted")
}
