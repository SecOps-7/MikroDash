package history

import (
	"math"
	"sort"
)

// Spans: a router's connectivity over a window, as the strip draws it.
//
// PURE, like the rest of this package: rows in, verdict out. The database reads
// and the endpoint live elsewhere; what is decided here is the part worth
// pinning, because every mistake in it is a strip that looks plausible and is
// wrong - a gap drawn green, an outage drawn as a sliver it was not, a
// percentage that counted rows instead of time.

// ConnEvent is one `connectivity_events` row: a state change at a moment.
type ConnEvent struct {
	TS        int64
	Connected bool
}

// Run is one stretch of observation, from `monitor_runs`.
type Run struct {
	From, To int64
}

// Span states. Strings rather than an enum because they go straight onto the
// wire and into a CSS class, and a number would need a table on both sides.
const (
	SpanUp          = "up"
	SpanDown        = "down"
	SpanUnmonitored = "unmonitored"
)

// Span is one contiguous stretch of a single state.
type Span struct {
	From  int64  `json:"from"`
	To    int64  `json:"to"`
	State string `json:"state"`
}

// SpanInput is everything one router's strip is built from.
type SpanInput struct {
	From, To int64
	// Before is the state the router was in at `From`: the last event at or
	// before it. Nil means no event ever has, which is NOT "up" - it is
	// unknown, and draws unmonitored even inside a run.
	Before *bool
	// Events are the transitions inside the window. Any at or before `From`
	// are folded into the starting state, so a caller need not trim exactly.
	Events []ConnEvent
	Runs   []Run
}

// SpanResult is the strip, and the one number drawn beside it.
type SpanResult struct {
	Spans []Span `json:"spans"`
	// UptimePct is up time over MONITORED time, to one decimal - nil when
	// nothing in the window was monitored, because 0% and "no idea" are
	// different facts and a card must not show the first for the second.
	UptimePct *float64 `json:"uptimePct"`
	// MonitoredMs is how much of the window the percentage is OF.
	MonitoredMs int64 `json:"monitoredMs"`
}

// Spans builds the strip.
//
// ── TIME-WEIGHTED, NOT ROW-COUNTED ─────────────────────────────────────────
//
// `ConnectivityEventsAgg` computes uptime as a share of EVENT ROWS. With rows
// written only on transitions that is not uptime at all: a router up for a
// month with one restart reads 100%, and one that flapped twice in a minute and
// was otherwise fine reads 50%. Here every millisecond of the window is counted
// once, in exactly one state.
//
// ── THE SPANS TILE THE WINDOW EXACTLY ──────────────────────────────────────
//
// No gap, no overlap, sorted, adjacent states merged. A strip renderer then
// needs no arithmetic of its own beyond scaling, which is where a second copy of
// these rules would otherwise grow.
func Spans(in SpanInput) SpanResult {
	if in.To <= in.From {
		return SpanResult{Spans: []Span{}}
	}
	covered := unionRuns(in.Runs, in.From, in.To)

	// The starting state, then the transitions strictly inside the window.
	var state *bool
	if in.Before != nil {
		v := *in.Before
		state = &v
	}
	evs := append([]ConnEvent(nil), in.Events...)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS < evs[j].TS })
	var inside []ConnEvent
	for _, e := range evs {
		if e.TS <= in.From {
			v := e.Connected
			state = &v
			continue
		}
		if e.TS < in.To {
			inside = append(inside, e)
		}
	}

	// Every moment anything can change: the window's ends, each transition, and
	// each edge of coverage.
	cuts := []int64{in.From, in.To}
	for _, e := range inside {
		cuts = append(cuts, e.TS)
	}
	for _, r := range covered {
		cuts = append(cuts, r.From, r.To)
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i] < cuts[j] })

	var out []Span
	var upMs, downMs int64
	ei := 0
	for i := 0; i+1 < len(cuts); i++ {
		a, b := cuts[i], cuts[i+1]
		if b <= a {
			continue
		}
		for ei < len(inside) && inside[ei].TS <= a {
			v := inside[ei].Connected
			state = &v
			ei++
		}
		st := SpanUnmonitored
		if isCovered(covered, a) && state != nil {
			if *state {
				st = SpanUp
				upMs += b - a
			} else {
				st = SpanDown
				downMs += b - a
			}
		}
		if n := len(out); n > 0 && out[n-1].State == st && out[n-1].To == a {
			out[n-1].To = b
		} else {
			out = append(out, Span{From: a, To: b, State: st})
		}
	}
	if out == nil {
		out = []Span{}
	}

	res := SpanResult{Spans: out, MonitoredMs: upMs + downMs}
	if res.MonitoredMs > 0 {
		pct := math.Round(float64(upMs)/float64(res.MonitoredMs)*1000) / 10
		res.UptimePct = &pct
	}
	return res
}

// unionRuns clips every run to the window and merges overlaps. Overlapping runs
// are tolerated rather than assumed impossible: a clock stepped backwards can
// open a run whose start precedes the last one's end.
func unionRuns(runs []Run, from, to int64) []Run {
	var c []Run
	for _, r := range runs {
		a, b := r.From, r.To
		if a < from {
			a = from
		}
		if b > to {
			b = to
		}
		if b > a {
			c = append(c, Run{a, b})
		}
	}
	sort.Slice(c, func(i, j int) bool { return c[i].From < c[j].From })
	var out []Run
	for _, r := range c {
		if n := len(out); n > 0 && r.From <= out[n-1].To {
			if r.To > out[n-1].To {
				out[n-1].To = r.To
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func isCovered(runs []Run, t int64) bool {
	for _, r := range runs {
		if t >= r.From && t < r.To {
			return true
		}
	}
	return false
}
