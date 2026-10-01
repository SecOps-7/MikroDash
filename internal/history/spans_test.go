package history

import (
	"reflect"
	"testing"
)

func bp(b bool) *bool { return &b }

func states(r SpanResult) []Span { return r.Spans }

// TestTimeOutsideEveryRunIsUnmonitored — the case `monitor_runs` exists for. A
// router "up" before a restart and "up" after it was NOT watched in between, and
// the strip must say so rather than infer green across the gap.
func TestTimeOutsideEveryRunIsUnmonitored(t *testing.T) {
	r := Spans(SpanInput{
		From: 0, To: 100, Before: bp(true),
		Runs: []Run{{0, 30}, {70, 100}},
	})
	want := []Span{{0, 30, SpanUp}, {30, 70, SpanUnmonitored}, {70, 100, SpanUp}}
	if !reflect.DeepEqual(states(r), want) {
		t.Errorf("spans = %+v, want %+v - the stopped stretch must be grey", r.Spans, want)
	}
	if r.MonitoredMs != 60 {
		t.Errorf("monitoredMs = %d, want 60", r.MonitoredMs)
	}
}

// TestUptimeIgnoresUnmonitoredTime — the percentage is of WATCHED time. Counting
// a gap as up flatters a router; counting it as down slanders one. The mutation
// this pins is the first.
func TestUptimeIgnoresUnmonitoredTime(t *testing.T) {
	r := Spans(SpanInput{
		From: 0, To: 100, Before: bp(true),
		Events: []ConnEvent{{20, false}, {30, true}},
		Runs:   []Run{{0, 50}}, // 50..100 not watched
	})
	// watched 50ms: up 0-20 and 30-50 (40ms), down 20-30 (10ms) => 80%.
	if r.UptimePct == nil || *r.UptimePct != 80 {
		t.Fatalf("uptime = %v, want 80 (40 up of 50 watched); a gap counted as up reads 90",
			r.UptimePct)
	}
}

// TestUptimeIsTimeWeightedNotRowCounted — the reason this function exists
// beside `ConnectivityEventsAgg`, which counts rows. Two transitions in a window
// of 1000 that is otherwise up is 99.9% here, not the 50% a row count gives.
func TestUptimeIsTimeWeightedNotRowCounted(t *testing.T) {
	r := Spans(SpanInput{
		From: 0, To: 1000, Before: bp(true),
		Events: []ConnEvent{{500, false}, {501, true}},
		Runs:   []Run{{0, 1000}},
	})
	if r.UptimePct == nil || *r.UptimePct != 99.9 {
		t.Errorf("uptime = %v, want 99.9", r.UptimePct)
	}
}

// TestTheStateBeforeTheWindowColoursItsStart — an outage that began yesterday
// and is still going must draw red from the window's first millisecond.
func TestTheStateBeforeTheWindowColoursItsStart(t *testing.T) {
	r := Spans(SpanInput{
		From: 0, To: 100, Before: bp(false),
		Events: []ConnEvent{{40, true}},
		Runs:   []Run{{-1000, 1000}},
	})
	want := []Span{{0, 40, SpanDown}, {40, 100, SpanUp}}
	if !reflect.DeepEqual(r.Spans, want) {
		t.Errorf("spans = %+v, want %+v", r.Spans, want)
	}
}

// TestNoAnchorRendersUnmonitoredNotUp — no event ever, inside a run: the state
// is unknown, and unknown is grey. Defaulting to up is the "inferred green"
// this whole mechanism removes.
func TestNoAnchorRendersUnmonitoredNotUp(t *testing.T) {
	r := Spans(SpanInput{From: 0, To: 100, Runs: []Run{{0, 100}}})
	if want := []Span{{0, 100, SpanUnmonitored}}; !reflect.DeepEqual(r.Spans, want) {
		t.Errorf("spans = %+v, want %+v", r.Spans, want)
	}
	if r.UptimePct != nil {
		t.Errorf("uptime = %v with nothing known, want nil - 0%% and 'no idea' differ", *r.UptimePct)
	}
}

// TestOverlappingRunsAreUnioned — a clock stepped backwards can open a run that
// overlaps the last; the union must not double-count or leave a seam.
func TestOverlappingRunsAreUnioned(t *testing.T) {
	r := Spans(SpanInput{
		From: 0, To: 100, Before: bp(true),
		Runs: []Run{{0, 60}, {40, 100}},
	})
	if want := []Span{{0, 100, SpanUp}}; !reflect.DeepEqual(r.Spans, want) {
		t.Errorf("spans = %+v, want one merged up span", r.Spans)
	}
	if r.MonitoredMs != 100 {
		t.Errorf("monitoredMs = %d, want 100 - an overlap was counted twice", r.MonitoredMs)
	}
}

// TestSpansTileTheWindowExactly — the property the renderer relies on: no gap,
// no overlap, sorted, adjacent states merged, sum equal to the window.
func TestSpansTileTheWindowExactly(t *testing.T) {
	r := Spans(SpanInput{
		From: 0, To: 1000, Before: bp(true),
		Events: []ConnEvent{{100, false}, {150, true}, {150, true}, {700, false}},
		Runs:   []Run{{0, 300}, {350, 1000}},
	})
	at := int64(0)
	for i, s := range r.Spans {
		if s.From != at {
			t.Fatalf("span %d starts at %d, want %d: %+v", i, s.From, at, r.Spans)
		}
		if s.To <= s.From {
			t.Fatalf("span %d is empty or reversed: %+v", i, s)
		}
		if i > 0 && r.Spans[i-1].State == s.State {
			t.Fatalf("spans %d and %d share a state and were not merged: %+v", i-1, i, r.Spans)
		}
		at = s.To
	}
	if at != 1000 {
		t.Errorf("the spans end at %d, want the window's end 1000", at)
	}
}

// TestAZeroOrReversedWindowIsEmpty — an empty array, never nil: it goes on the
// wire, and this repo's rule is that Go never sends a null array.
func TestAZeroOrReversedWindowIsEmpty(t *testing.T) {
	for _, w := range [][2]int64{{5, 5}, {9, 3}} {
		r := Spans(SpanInput{From: w[0], To: w[1]})
		if r.Spans == nil || len(r.Spans) != 0 {
			t.Errorf("window %v gave %#v, want an empty non-nil slice", w, r.Spans)
		}
	}
}
