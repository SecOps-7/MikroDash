package collect

// Stream health: whether a stream the watchdog keeps restarting has actually
// recovered, or is failing over and over.
//
// ── WHAT COUNTS AS "RECOVERED" IS THE WHOLE PROBLEM ────────────────────────
//
// The live helper (`createStreamHealth`, src/collectors/util.js) states it: a
// stream that dies every fifteen seconds still delivers a burst of rows
// immediately after each restart. Resetting the counter the moment data appears
// would mean it never climbs, so a permanently broken stream stays invisible
// while being restarted for ever — which is the failure this exists to surface.
//
// Recovery therefore requires the stream to have been UP for `healthyMs`, not
// merely to have produced a packet. That one rule is why this is a type rather
// than an int.
//
// ── AND WHY THE RETURN VALUE IS A TRANSITION, NOT A STATE ──────────────────
//
// Both record methods report whether the degraded flag CHANGED. The caller
// emits only on a change, so a stream that is degraded and staying degraded
// does not push a frame to every browser on every five-second tick. The live
// code returns `null` for "no change" and the new boolean otherwise; Go says
// the same thing with a second return value.

// Defaults, from the live helper's own parameters.
const (
	streamDegradeAfter = 3
	streamHealthyMs    = 60_000
)

// StreamHealth counts watchdog restarts for one stream. Not goroutine-safe: it
// belongs to a collector and is touched from that collector's own lock.
type StreamHealth struct {
	// DegradeAfter and HealthyMs are zero-defaulted to the constants above, so
	// the zero value behaves like the live helper called with no arguments.
	DegradeAfter int
	HealthyMs    int64

	restarts int
	degraded bool
	since    int64
}

func (h *StreamHealth) degradeAfter() int {
	if h.DegradeAfter > 0 {
		return h.DegradeAfter
	}
	return streamDegradeAfter
}

func (h *StreamHealth) healthyMs() int64 {
	if h.HealthyMs > 0 {
		return h.HealthyMs
	}
	return streamHealthyMs
}

// RecordRestart notes that the watchdog had to restart the stream, and reports
// whether that tipped it into the degraded state.
//
// `now` is only stored, for `Since`; the decision is on the COUNT. A restart
// while already degraded changes nothing and reports no transition.
func (h *StreamHealth) RecordRestart(now int64) (degraded, changed bool) {
	h.restarts++
	if h.degraded || h.restarts < h.degradeAfter() {
		return h.degraded, false
	}
	h.degraded = true
	h.since = now
	return true, true
}

// RecordHealthy notes a tick that found data flowing, `streamAgeMs` after the
// stream opened, and reports whether that cleared the degraded state.
//
// TWO GUARDS, and both are load-bearing:
//
//	the age test is the anti-flap rule in the header — a stream that has been
//	up for four seconds has not recovered, however much data it just produced;
//	and a stream that has never been restarted has nothing to recover FROM, so
//	it reports no transition rather than a spurious "recovered".
func (h *StreamHealth) RecordHealthy(streamAgeMs int64) (degraded, changed bool) {
	if streamAgeMs < h.healthyMs() {
		return h.degraded, false
	}
	if !h.degraded && h.restarts == 0 {
		return false, false
	}
	was := h.degraded
	h.restarts = 0
	h.degraded = false
	h.since = 0
	// A stream that had restarts but never reached `degraded` is now clean, and
	// that is not a transition anybody was told about — nothing was ever sent
	// saying it was degraded, so nothing is sent saying it is not.
	return false, was
}

// Reset drops everything: the stream was stopped deliberately, or the router
// reconnected and the count no longer describes anything.
func (h *StreamHealth) Reset() {
	h.restarts = 0
	h.degraded = false
	h.since = 0
}

func (h *StreamHealth) Degraded() bool { return h.degraded }
func (h *StreamHealth) Restarts() int  { return h.restarts }
func (h *StreamHealth) Since() int64   { return h.since }
