package collect

// Shared collector machinery — the Go half of src/collectors/util.js.
//
// The value coercions below look trivial and are not. RouterOS answers in
// strings, and the Node collectors run them through JavaScript's implicit
// conversions; a Go port that reaches for strconv directly gets a payload that
// is right nearly everywhere and wrong at the edges — and the edges are exactly
// where a router with an unusual build lands. Number("") is 0, Number(undefined)
// is NaN, and the difference decides whether a gauge reads zero or renders as
// "not reported". Both cases are reproduced here on purpose.

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mikrodash/internal/routeros"
)

// Reader is the slice of a router connection a collector uses. An interface
// rather than *routeros.Client so the fixture corpus can be replayed into the
// real collector with no router present, which is how the Go payloads are
// diffed against the Node ones.
type Reader interface {
	Do(routeros.Cmd) ([]routeros.Reply, error)
	Connected() bool
}

// Emit delivers a payload to a room. Rooms are named as the Node collectors
// name them — "page-dns" — and the caller scopes them to a router, exactly as
// buildRouterIo does in src/index.js.
type Emit func(room, event string, payload any)

// clampPoll mirrors clampPoll in src/collectors/util.js: a non-numeric input
// falls back to def, and the result is bounded by lo and hi.
func clampPoll(raw, def, lo, hi int) int {
	n := raw
	if n == 0 {
		n = def
	}
	return max(lo, min(hi, n))
}

// boolOf mirrors the collectors' `_bool`: only these two spellings are true.
// Anything else, including "1" and "on", is false — RouterOS does not emit them
// and inventing tolerance here would diverge from the Node payload silently.
func boolOf(v string) bool { return v == "true" || v == "yes" }

// numOf mirrors `_num(row[key])`, which is Number() followed by a finite check.
//
// The three cases that matter, all of them JavaScript's rather than Go's:
//
//	key absent   → Number(undefined) is NaN     → null
//	value ""     → Number("") is 0              → 0, NOT null
//	unparseable  → NaN                          → null
//
// The middle one is the trap. A router that reports a setting as present but
// empty produces a zero in the Node payload, and a Go port that treated empty
// as missing would render the field as unavailable instead.
func numOf(row routeros.Reply, key string) *float64 {
	v, ok := row[key]
	if !ok {
		return nil
	}
	s := strings.TrimSpace(v)
	if s == "" {
		z := 0.0
		return &z
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

// splitList mirrors `_split`: a comma list into trimmed, non-empty parts.
// Always a slice, never nil, because the Node payload carries [] and a nil
// slice would marshal as null — a difference the browser would see.
func splitList(v string) []string {
	out := []string{}
	if v == "" {
		return out
	}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// menuMissing reports whether an error means "stop asking this router for this
// menu", matching the substring rules the Node collectors apply on a read.
//
// Deliberately NOT routeros.Trap.Absent(). That method answers a narrower
// question — is this command unknown to this build — and is right to, because
// it is also consulted on the write path where "no such item" means a row was
// deleted rather than a menu being absent. On a read there is no such row to
// confuse it with, so the read rule is the broader one the Node collectors use.
func menuMissing(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no such") || strings.Contains(m, "unknown command") ||
		strings.Contains(m, "not enough permissions") || strings.Contains(m, "permission denied")
}

// pollInterval is a collector's poll period, changeable while it runs.
//
// ── WHY IT IS ATOMIC RATHER THAN GUARDED BY THE COLLECTOR'S MUTEX ───────────
//
// A settings save re-tunes the running collectors (`collection.PollRetunes`),
// which means writing this from the HTTP goroutine while the poll loop reads it.
// Twenty-one of the twenty-four collectors also send the value to the browser in
// their payload — the live ones send `this.pollMs`, mutated in place by the same
// route — so it cannot simply live inside the loop where the payload cannot see
// it.
//
// Three collectors (`packages`, `routing`, `talkers`) have NO mutex at all.
// Guarding this with "the collector's lock" would therefore be a rule with three
// exceptions, and the exceptions are exactly the files where a reviewer would
// not notice its absence. An atomic needs no lock and no discipline.
type pollInterval struct{ v atomic.Int64 }

func newPollInterval(ms int) *pollInterval {
	p := &pollInterval{}
	p.set(ms)
	return p
}

// ms is the current period. Safe to call from any goroutine.
func (p *pollInterval) ms() int { return int(p.v.Load()) }

// set changes it. The CLAMP stays where it always was — in `pollLoop.bounded`
// and in each constructor — so this stores what it is given: a caller that has
// already clamped (the settings route has) must not be clamped twice to a
// different range, and `collection.PollRetunes` uses 500..600000 while the
// constructors use a per-collector range.
func (p *pollInterval) set(ms int) { p.v.Store(int64(ms)) }

func (p *pollInterval) duration() time.Duration {
	return time.Duration(p.ms()) * time.Millisecond
}

// PollMs is a collector's current period, for callers outside this package.
//
// Exported on the INTERVAL rather than added to twenty-two collectors, because a
// method per type would be twenty-two chances to read the wrong field. The
// collectors expose it by having an exported-typed field only where they already
// send it in a payload.
func (p *pollInterval) PollMs() int { return p.ms() }

// pollLoop is a self-rescheduling timer, matching createPollLoop: the next
// delay is measured from the END of the previous run, so a slow reply cannot
// queue overlapping requests at a router that is already struggling. That is
// the whole reason poll mode exists (src/collection.js, issue #104).
type pollLoop struct {
	run   func()
	delay func() time.Duration

	mu      sync.Mutex
	stopped bool
	timer   *time.Timer
	lastRun time.Time
}

func newPollLoop(run func(), delay func() time.Duration) *pollLoop {
	return &pollLoop{run: run, delay: delay, stopped: true}
}

func (p *pollLoop) bounded() time.Duration {
	d := p.delay()
	return max(500*time.Millisecond, min(600*time.Second, d))
}

// start polls immediately if a whole interval has already elapsed since the
// last run, and otherwise waits out the remainder. Page navigation calls
// stop/start freely, so firing unconditionally would let a user generate one
// request per visit at a router poll mode exists to be gentle on.
func (p *pollLoop) start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stopped {
		return
	}
	p.stopped = false
	wait := p.bounded()
	if since := time.Since(p.lastRun); since >= wait {
		wait = 0
	} else {
		wait -= since
	}
	p.schedule(wait)
}

// retime applies a changed interval to the PENDING timer.
//
// ── WHY THE LOOP NEEDS THIS AT ALL ──────────────────────────────────────────
//
// `delay` is a function, re-read on every schedule, so a changed interval is
// already picked up by the NEXT tick without any help. What it does not do is
// affect the timer already running — and that is the whole point of a settings
// save: an operator moving a poll from sixty seconds to five expects five, not
// to wait out the remaining fifty-nine first.
//
// ── AND WHY IT MEASURES FROM NOW, WHERE `start` MEASURES FROM `lastRun` ─────
//
// `start` deliberately counts the elapsed time since the last run, because page
// navigation calls stop/start freely and firing unconditionally would let a user
// generate one request per visit at a router poll mode exists to be gentle on.
//
// This is a different event with a different answer. The live side does
// `clearInterval(col.timer); col.timer = setInterval(run, ...)`, which schedules
// a FULL new interval from the moment of the save — so shortening an interval
// does not fire an immediate extra poll, and lengthening one does not leave a
// timer running to an instant the operator has just replaced. Reproduced rather
// than improved: measuring from `lastRun` would poll IMMEDIATELY whenever the
// new interval is shorter than the time already elapsed, which is exactly the
// burst a settings save across a fleet must not produce.
//
// A stopped loop is left alone: `start` will compute a fresh delay when the page
// is next opened, and re-arming a timer for a collector nobody is watching would
// undo what `stop` is for.
func (p *pollLoop) retime() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.schedule(p.bounded())
}

func (p *pollLoop) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}

// schedule must be called with the lock held.
func (p *pollLoop) schedule(d time.Duration) {
	if p.stopped || p.timer != nil {
		return
	}
	p.timer = time.AfterFunc(d, p.tick)
}

func (p *pollLoop) tick() {
	p.mu.Lock()
	p.timer = nil
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.lastRun = time.Now()
	p.mu.Unlock()

	p.run()

	p.mu.Lock()
	p.schedule(p.bounded())
	p.mu.Unlock()
}

// menuAbsent and menuDenied split what menuMissing answers together.
//
// Most collectors latch on either for the same reason — stop asking — but the
// WAN page says DIFFERENT things about them: a menu this RouterOS build does not
// have is not the same as one this API user may not see, and telling an operator
// the wrong one sends them to the wrong fix.
func menuAbsent(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no such") || strings.Contains(m, "unknown command")
}

func menuDenied(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "not enough permission") ||
		strings.Contains(m, "permission denied") || strings.Contains(m, "no permissions")
}

// parseJSNumber is Number(s) for a non-empty string: the value, and whether it
// was finite.
func parseJSNumber(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}
