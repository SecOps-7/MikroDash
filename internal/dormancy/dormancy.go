// Package dormancy is the decision half of the collector dormancy supervisor.
//
// A collector that keeps reporting nothing — an empty list, or a menu this
// RouterOS does not have — is put to sleep and re-probed on a lengthening
// delay, so a router is not asked for a table it does not have every three
// seconds for the life of a session. That matters here more than in most apps:
// "the evidence in #104 points at concurrent open channels rather than data
// volume" (`src/collection.js`), and a sleeping collector holds none.
//
// ── PURE, AND DELIBERATELY SO ───────────────────────────────────────────────
//
// Observations in, verdict out. No timers, no I/O, and `now` is supplied by the
// caller rather than read from the clock — which is what makes it testable
// against a generated corpus and what keeps `--check` from being permanently
// stale. The live original is the same shape, and for the same reason the write
// guards are: the decision lives in one place rather than inline in the
// lifecycle code.
//
// ── WHAT THIS DOES NOT DO ───────────────────────────────────────────────────
//
// It is not the supervisor. Nothing here starts a timer, suspends a collector or
// emits `collection:status`; the caller does all three. Ported first because it
// is the half that can be pinned exactly, and because `applyCollectionStatus` on
// the browser side has been written, gated and uncalled since the port began.
package dormancy

// Verdict is what one observation changed, if anything.
type Verdict string

const (
	// None is the live `null`: nothing to announce. It is the usual answer, and
	// notably it is what an empty PROBE returns — a collector that is already
	// asleep and stays asleep does not re-announce it.
	None Verdict = ""
	// Sleep — stop polling this collector and re-probe after DelayMs.
	Sleep Verdict = "sleep"
	// Wake — it produced something; resume the normal interval.
	Wake Verdict = "wake"
)

// Options are the live defaults, which every caller currently takes.
type Options struct {
	EmptyThreshold int
	BackoffMs      int64
	MaxBackoffMs   int64
	ProbeTimeoutMs int64
	RestampMs      int64
}

// Defaults are `createDormancyState`'s destructuring defaults.
func Defaults() Options {
	return Options{
		EmptyThreshold: 3,
		BackoffMs:      60000,
		MaxBackoffMs:   600000,
		ProbeTimeoutMs: 30000,
		RestampMs:      45000,
	}
}

// Observation is one look at a collector's last payload.
//
// `Empty` and `Unsupported` are BOOLEANS here, where the live code tests
// `=== true`. That is safe because both come from `payloadEmpty()`, which
// returns a real boolean, and from an explicit flag — never from a truthy
// value. Worth stating because the live function is written defensively enough
// to suggest otherwise.
type Observation struct {
	// TS is the payload's timestamp. ZERO MEANS IGNORE THE OBSERVATION
	// ENTIRELY — the live guard is `if (!obs || !obs.ts) return null`, and a
	// collector that has never produced a payload must not be condemned for it.
	TS          int64
	Empty       bool
	Unsupported bool
}

// State is one collector's dormancy state. Not safe for concurrent use; the
// supervisor owns one per collector and consults them from its own goroutine.
type State struct {
	opt Options

	streak  int
	dormant bool
	probing bool

	lastTS        int64
	wakeAt        int64
	probeDeadline int64
	lastCountedAt int64
	delay         int64
}

// New builds a state with the given options.
func New(opt Options) *State { return &State{opt: opt, delay: opt.BackoffMs} }

func (s *State) sleepFor(ms, now int64) {
	s.dormant = true
	s.streak = 0
	s.delay = ms
	s.wakeAt = now + ms
}

func (s *State) backOff(now int64) {
	s.probing = false
	s.delay = min64(s.delay*2, s.opt.MaxBackoffMs)
	s.wakeAt = now + s.delay
}

// Observe judges one observation and reports what changed.
func (s *State) Observe(obs Observation, now int64) Verdict {
	if obs.TS == 0 {
		return None
	}
	bad := obs.Unsupported || obs.Empty

	// A PAYLOAD THAT HAS STOPPED ADVANCING IS STILL EVIDENCE WHEN IT IS EMPTY.
	//
	// The live comment: five collectors — netwatch, vpn, firewall, routing,
	// topology — heartbeat by re-emitting the last payload with a new `ts` and
	// never reassign it, so their ts freezes once the data settles. Requiring a
	// fresh ts meant dormancy could never fire for any of them; it worked for the
	// nine poll-loop collectors and silently skipped the rest.
	//
	// Still rate-limited rather than counted every tick, which is what the
	// distinct-ts rule was protecting: a ten-minute collector must not be
	// condemned by a supervisor ticking every fifteen seconds. ONE OBSERVATION
	// HELD EMPTY FOR RestampMs IS THE EVIDENCE, not the tick that noticed it.
	fresh := obs.TS != s.lastTS
	if !fresh {
		if !bad || s.dormant {
			return None
		}
		if s.lastCountedAt == 0 || (now-s.lastCountedAt) < s.opt.RestampMs {
			return None
		}
	}
	s.lastTS = obs.TS
	s.lastCountedAt = now

	if !bad {
		s.streak = 0
		s.delay = s.opt.BackoffMs
		s.probing = false
		if !s.dormant {
			return None
		}
		s.dormant = false
		s.wakeAt = 0
		return Wake
	}

	// Already asleep: this is a probe that came back empty, so lengthen the delay
	// rather than re-announcing sleep. The verdict is None and only the delay moves.
	if s.dormant {
		s.backOff(now)
		return None
	}
	// UNSUPPORTED SKIPS THE STREAK and sleeps for the MAXIMUM. A port that
	// treated it as one more empty would re-probe a menu this RouterOS does not
	// have every minute forever.
	if obs.Unsupported {
		s.sleepFor(s.opt.MaxBackoffMs, now)
		return Sleep
	}
	s.streak++
	if s.streak < s.opt.EmptyThreshold {
		return None
	}
	s.sleepFor(s.opt.BackoffMs, now)
	return Sleep
}

// DueForProbe reports whether it is time to look again.
//
// IT HAS A SIDE EFFECT, and that is not an accident: a probe that never reported
// back is settled here, by backing off and staying asleep. A port that made this
// a pure predicate would leave `probing` set forever and never probe again.
func (s *State) DueForProbe(now int64) bool {
	if !s.dormant {
		return false
	}
	if s.probing {
		if now < s.probeDeadline {
			return false
		}
		s.backOff(now)
		return false
	}
	return now >= s.wakeAt
}

// MarkProbed records that the caller has just re-probed; hold off until it
// reports or times out.
func (s *State) MarkProbed(now int64) {
	s.probing = true
	s.probeDeadline = now + s.opt.ProbeTimeoutMs
}

// Reset forgets everything — the router reconnected or the session was rebuilt.
func (s *State) Reset() {
	s.streak = 0
	s.dormant = false
	s.probing = false
	s.lastTS = 0
	s.wakeAt = 0
	s.probeDeadline = 0
	s.lastCountedAt = 0
	s.delay = s.opt.BackoffMs
}

func (s *State) Dormant() bool  { return s.dormant }
func (s *State) Probing() bool  { return s.probing }
func (s *State) Streak() int    { return s.streak }
func (s *State) DelayMs() int64 { return s.delay }
func (s *State) WakeAt() int64  { return s.wakeAt }

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
