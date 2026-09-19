package session

import (
	"fmt"
	"log"
	"mikrodash/internal/hub"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/routeros"
)

// The one-shot system reading that keeps a Devices card from being blank.
//
// ── LIFTED FROM THE DELETED ALERT POOL'S OWN PRIME, WHICH THIS REPLACES ────
//
// The reasoning is that file's and it survives the move intact, so it is kept
// here rather than summarised: a session held for a non-viewer reason runs only
// what that reason needs (see needs.go), and a WARM session runs nothing at all.
// `Snapshots` can then answer only `Connected`, and the Devices page draws a
// card that knows the router is up and nothing else -- no CPU, no memory, no
// uptime, no model -- for the seconds the overview pool takes to dial its own
// connection.
//
// This closes that the cheapest way there is: ONE read, on a socket that is
// already open, at the moment somebody actually looks at the page. It costs
// nothing while nobody is looking, which is the property the reporting toggle
// exists to protect.
//
// ── THE FILTER MOVED, AND IT IS WIDER THAN THE POOL'S ──────────────────────
//
// The pool filtered on `system == nil`: a pooled session either built the
// collector or did not. A session ALWAYS builds all of them and then suspends
// what it does not need, so the equivalent question is not "is there a
// collector" but "does it hold a reading". That covers one case the pool's
// filter could not express: the connect burst starts every collector before
// `applyReasons` suspends them, so a warm session may well have taken a reading
// on its own -- and when it has, this correctly does nothing.

// primeDeadline bounds how long PrimeStats will hold its caller up.
//
// A read on an open socket comes back in tens of milliseconds; this is the
// allowance for a router that has stopped answering without its connection
// having dropped yet.
//
// ── IT NO LONGER BOUNDS THE READ ITSELF, AND THAT IS DELIBERATE ─────────────
//
// It used to: the deadline cancelled the read, which freed the router's
// `roslimit` slot. But go-routeros cancels a command by cancelling the reader
// the whole connection shares, so a prime read slower than this ended the
// connection, and every collector's command with it. A timed-out read now keeps
// its slot until Client.Do's `/cancel` ends it (reader.Do), and `primeStats`
// skips a session whose earlier prime read is still out — so a slow router
// collects one outstanding prime read, not one every two seconds.
const primeDeadline = 1500 * time.Millisecond

// primeReader is the session's `reader` with a deadline stamped on every
// command.
//
// A wrapper rather than a field on `reader`, because the collectors' own reads
// must stay unbounded: they are polls with their own cadence, and a slow one
// blocks nothing but its own loop.
type primeReader struct {
	reader
	within time.Duration
}

func (r primeReader) Do(c routeros.Cmd) ([]routeros.Reply, error) {
	c.Timeout = r.within
	s := r.reader.s
	s.primeInflight.Add(1)
	return r.reader.Do(c.OnFinished(func() { s.primeInflight.Add(-1) }))
}

// PrimeStats fills in a one-shot system reading for every live session that
// holds none. Called on a Devices page focus.
func (m *Manager) PrimeStats() { m.primeStats(primeDeadline, false) }

// PrimeUnread is PrimeStats for sessions that have never been primed, and it is
// what the two-second tick calls.
//
// ONLY THE UNREAD ONES, which is what makes this safe on a timer. Re-reading
// every reading-less session every two seconds is a command channel spent on a
// question already answered -- the cost the reporting toggle exists to avoid,
// and the reason the prime is not a poll.
func (m *Manager) PrimeUnread() { m.primeStats(primeDeadline, true) }

// primeStats is PrimeStats with the deadline injected, so a test need not wait
// out a real one. `unreadOnly` skips a session that already holds a reading.
func (m *Manager) primeStats(within time.Duration, unreadOnly bool) {
	// CANDIDATES CHOSEN OUTSIDE the manager lock: `Live` copies the map, and
	// every question below takes a SESSION lock. Nesting the two would be the
	// kind of thing that only shows up under load.
	todo := make([]*Session, 0)
	for _, s := range m.Live() {
		if s.systemReading() != nil {
			continue
		}
		if unreadOnly && s.primedSystem() != nil {
			continue
		}
		// A PRIME READ FROM AN EARLIER CALL IS STILL WITH THE ROUTER. It holds a
		// slot until its reply comes, so starting another would stack reads on
		// exactly the router too slow to answer the last one.
		if s.primeInflight.Load() > 0 {
			continue
		}
		// The claim is what stops a focus from stacking reads on a router that
		// is not answering. `devicesFocus` is reached from `pageFocus` AND from
		// `selectRouter` via `rejoinPage`, so switching router is three calls.
		if s.startPriming() {
			todo = append(todo, s)
		}
	}
	if len(todo) == 0 {
		return
	}

	// CONCURRENTLY, because these are separate routers and the wait is entirely
	// network. `reader.Do` takes the per-router budget, so this cannot open more
	// channels on one router than anything else here would.
	//
	// ONE BUFFERED CHANNEL, counted as answers arrive, rather than a WaitGroup
	// and a second pass over `primedSystem()`. The second pass counted the
	// FIELD, and the field is never cleared -- so a value left by an earlier
	// focus counted as this call's success and a router that had stopped
	// answering still logged "primed 3/3". Buffered to the full width so a
	// goroutine that finishes after the deadline can still send and exit rather
	// than blocking for ever.
	start := time.Now()
	results := make(chan bool, len(todo))
	for _, s := range todo {
		go func(s *Session) {
			defer s.donePriming()
			results <- s.primeSystem(within)
		}(s)
	}

	deadline := time.NewTimer(within)
	defer deadline.Stop()
	ok, answered := 0, 0
wait:
	for answered < len(todo) {
		select {
		case good := <-results:
			answered++
			if good {
				ok++
			}
		case <-deadline.C:
			break wait
		}
	}

	// LOGGED, because this is the only place the cost is visible. It runs on a
	// page focus and not on the tick, so it is one line per cold open of the
	// Devices page -- and when the first paint is blank again, the answer to
	// "did anything prime, and how long did it take" is the first thing needed.
	msg := ""
	if late := len(todo) - answered; late > 0 {
		msg = fmt.Sprintf(", %d did not answer in time", late)
	}
	log.Printf("[session] primed %d/%d reading-less session(s) in %s%s",
		ok, len(todo), time.Since(start).Round(time.Millisecond), msg)
}

// systemReading is what the session's OWN system collector holds, if anything.
func (s *Session) systemReading() *collect.SystemPayload {
	if sys := s.System(); sys != nil {
		return sys.Last()
	}
	return nil
}

func (s *Session) primedSystem() *collect.SystemPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.primedSys
}

// startPriming claims this session for one in-flight prime, and reports whether
// the claim was taken. `donePriming` releases it.
func (s *Session) startPriming() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.priming {
		return false
	}
	s.priming = true
	return true
}

func (s *Session) donePriming() {
	s.mu.Lock()
	s.priming = false
	s.mu.Unlock()
}

// primeSystem takes the reading, on the connection the session already holds.
//
// The collector is a THROWAWAY: it is never started, never emits -- nothing
// consumes a warm session's payloads, and handing this one to the evaluator
// would turn alerting back on for a router the operator switched it off for --
// and it is dropped as soon as its payload has been kept.
//
// A session with no connection needs no guard here: `reader` reports it as
// disconnected and `Tick` reads nothing, so the prime produces nothing rather
// than a zeroed reading. Reaching for `s.client` directly instead is what would
// need one, and is the mistake this note exists to prevent.
func (s *Session) primeSystem(within time.Duration) bool {
	// Through newSystem, so the one reading a warm session takes also reports
	// the router's identity: see Manager.SetOnIdentity.
	c := s.newSystem(primeReader{reader{s: s}, within}, hub.Relay{})
	// ONE COMMAND, which is the whole claim this makes. A fresh collector has a
	// zero `healthAt`, so its first Tick would ask `/system/health/print` before
	// the gauges -- a second roslimit-gated command per router for `TempC`,
	// which nothing outside `internal/collect` reads.
	//
	// ONE TICK, NOT TWO: `System.Tick` does its static read from the SECOND tick
	// on, so SERIAL and LICENCE LEVEL stay nil. Fetching them would cost another
	// channel per router for two pills the overview pool fills in a couple of
	// seconds anyway. ARCH is not one of them though it reads like one --
	// `architecture-name` comes back on the resource row itself.
	c.DeferHealth()
	c.Tick()
	p := c.Last()
	if p == nil {
		return false
	}
	s.mu.Lock()
	s.primedSys = p
	s.mu.Unlock()
	return true
}
