package server

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE ROUTE IS ACTUALLY REGISTERED.
//
// `TestEveryCalledEndpointIsServed` reads `mux.Handle...` calls out of this
// package's source, so it is satisfied by the registration LINE existing —
// whether or not anything ever calls the function holding it. Proven by
// mutation: deleting `s.registerInterfaceHistory(mux)` from server.go left that
// gate green, because the handler line was still there to find.
//
// Which is the shape `prune_scheduler_test.go` was written for: "test the call
// site, not the callee, when the defect is 'somebody forgot to call it'". An
// unregistered route answers Go's own 404 and the panel loads for ever.
func TestTheInterfaceHistoryRouteIsRegistered(t *testing.T) {
	src := read(t, "server.go")
	if !regexp.MustCompile(`s\.registerInterfaceHistory\(mux\)`).MatchString(src) {
		t.Error("nothing calls registerInterfaceHistory in server.go, so /api/interfaces/history " +
			"is never mounted and the Interfaces modal's history panel loads for ever")
	}
}

// THE RANGES THE PANEL OFFERS ARE THE RANGES THE SERVER ACCEPTS — BOTH WAYS.
//
// The server refuses an unknown range rather than defaulting to an hour, which
// is right (a chart labelled 30 days and filled with one hour is worse than an
// error) and means a button the server does not know is a button that does
// nothing. The reverse matters too: a range the server accepts and nothing
// offers is dead code that reads as a feature.
//
// ── EXCEPT THE LIVE ONES, WHICH THE SERVER NEVER SEES ──────────────────────
//
// Live through 30 min are drawn from a buffer in the browser, fed by the
// payload the page already receives, so they work for an interface nothing is
// recording. They are declared in their own array for exactly this reason: the
// two sets are checked against different things, and one list holding both
// would have to guess which half it was looking at.
func TestTheOfferedRangesAreTheAcceptedRanges(t *testing.T) {
	ts := read(t, "../../web/src/pages/interfaces-history.ts")
	history := rangeKeys(t, ts, "HISTORY_RANGES")
	live := rangeKeys(t, ts, "LIVE_RANGES")

	for k := range ifaceHistoryRanges {
		if !history[k] {
			t.Errorf("the server accepts range %q and the panel offers no button for it", k)
		}
	}
	for k := range history {
		if _, ok := ifaceHistoryRanges[k]; !ok {
			t.Errorf("the panel offers range %q as a SERVER range and the server refuses it "+
				"with a 400, so the button does nothing", k)
		}
	}
	// AND A LIVE RANGE IS NOT A SERVER RANGE. If one ever became both, the
	// panel would draw the buffer for it while the server answered something
	// else for the same key — two pictures under one label.
	for k := range live {
		if _, ok := ifaceHistoryRanges[k]; ok {
			t.Errorf("%q is declared as a live range AND accepted by the server; it has two "+
				"sources and the button would mean whichever one ran last", k)
		}
	}
	if len(live) == 0 {
		t.Error("no live ranges declared — Live is the default and must work for an " +
			"interface that is not being recorded")
	}
}

// rangeKeys reads one declared range table out of the panel's source.
//
// It FAILS on an empty result rather than returning one, because an empty set
// agrees with every assertion above and would turn this ledger into a test that
// reads nothing and passes.
func rangeKeys(t *testing.T, src, name string) map[string]bool {
	t.Helper()
	i := strings.Index(src, "const "+name)
	if i < 0 {
		t.Fatalf("%s is gone from interfaces-history.ts — this gate is stale, not passing", name)
	}
	end := strings.Index(src[i:], "];")
	if end < 0 {
		t.Fatalf("%s is not a closed array literal", name)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`key:\s*'([^']+)'`).
		FindAllStringSubmatch(src[i:i+end], -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("no keys parsed out of %s; the scan broke", name)
	}
	return out
}

// EVERY RANGE IS DRAWN AT AN AGGREGATION THE DATABASE KNOWS, and the longer
// ones are aggregated at all.
//
// An unknown aggregation string makes `aggBucket` return no rows — deliberately,
// since the caller asked for something that does not exist — so a typo here is
// an empty chart rather than an error. And an unaggregated 30-day range would
// ask for up to 43,200 rows to draw a panel a few hundred pixels wide.
func TestEveryRangeIsDrawnAtAKnownAggregation(t *testing.T) {
	known := map[string]bool{"": true, "hour": true, "day": true, "week": true, "month": true}
	raw := []string{}
	for k, r := range ifaceHistoryRanges {
		if !known[r.Agg] {
			t.Errorf("range %q is drawn at aggregation %q, which aggBucket does not know: "+
				"the query returns no rows and the panel shows an empty chart", k, r.Agg)
		}
		if r.Agg == "" {
			raw = append(raw, k)
		}
		if r.Span <= 0 {
			t.Errorf("range %q has a span of %v", k, r.Span)
		}
	}
	// ONLY THE SHORTEST IS RAW. Recorded as a list so widening it is a
	// deliberate edit rather than a quiet extra 40,000 rows on the wire.
	sort.Strings(raw)
	if strings.Join(raw, ",") != "1h" {
		t.Errorf("unaggregated ranges are %v, want just the 1h one — every longer range "+
			"has to be bucketed or the panel downloads minutes it cannot draw", raw)
	}
}

// THE SWITCH EXTENDS WHAT IS STORED, NOT WHAT IS RESOLVED.
//
// Found by reading a live reply: `recordedIfaces` was the RESOLVED set, which
// always contains the default interface. The panel sends that array back with
// one name appended, so recording ether5 would also have written today's WAN
// into the stored list — and it would have stayed there after the operator
// pointed `defaultIf` somewhere else, quietly recording an interface nobody
// asked for.
//
// Read out of the source because the reply is assembled from a store this test
// has no fixture for; what is being asserted is which of the two lists the
// field is filled from, and they are one identifier apart.
func TestTheRecordingSwitchIsGivenTheStoredList(t *testing.T) {
	src := read(t, "interfaces_history.go")
	if !regexp.MustCompile(`out\.RecordedIfaces = stored`).MatchString(src) {
		t.Error("the reply's recordedIfaces is not the STORED list. If it is the resolved " +
			"one, switching an interface on also writes the current default into the " +
			"stored array, where it outlives the setting that put it there")
	}
	// AND THE RESOLVED SET IS STILL WHAT ANSWERS "is this recorded", or the
	// WAN reads as unrecorded while its rows arrive every minute.
	if !regexp.MustCompile(`for _, n := range rec \{`).MatchString(src) {
		t.Error("the `recorded` flag is no longer computed from the resolved set")
	}
}
