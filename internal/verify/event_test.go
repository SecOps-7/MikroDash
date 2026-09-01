package verify

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestWebSocketVocabulary: every event the server sends has a listener, and every
// event a page listens for is sent.
//
// ── THE BUG CLASS, WITH A RECENT EXAMPLE ────────────────────────────────────
//
// Both directions fail silently. A page subscribing to an event nothing emits
// simply never renders -- no error, no warning, an empty card. On 2026-08-31
// three dashboard cards sat empty for exactly that reason: `dashboard.ts` listened
// for `routing:update` and the collector only ever emitted it to `page-routing`,
// so a viewer who never opened the Routing page saw em dashes forever.
//
// The other direction is quieter still: an event nobody consumes is work the
// server does for nothing, and it looks identical to an event whose consumer was
// deleted by accident.
//
// ── WHY LEDGERS RATHER THAN A CLEAN LIST ────────────────────────────────────
//
// Some gaps are real and deliberate. They are recorded WITH THEIR REASON, and the
// check fails in BOTH directions: an unrecorded gap is a failure, and a recorded
// gap that has closed is ALSO a failure, so an entry cannot outlive the situation
// it describes. That is the property that stops a ledger becoming folklore.

// eventsUnconsumed: emitted by the server, deliberately nobody listens.
var eventsUnconsumed = map[string]string{
	"packages:applying": "vestigial in the live app too — it was emitted and nothing listened. " +
		"Reproduced rather than dropped, so a future reader finds this note instead of " +
		"'fixing' a consumer into existence.",
}

// eventsUnserved: a page listens, deliberately nothing emits it.
var eventsUnserved = map[string]string{
	"alert:fired": "the alerter holds per-router evaluator state and SENDS; the bell renders the " +
		"stored feed without it.",
	"alert:resolved": "as alert:fired.",
	"stream:health": "no collector reports stream health on this side. It is a fact about the " +
		"SERVING PROCESS, so a Go version would report on Go's streams rather than mirror the " +
		"old ones. The warning element stays empty, which is honest — a stale one would say the " +
		"wrong thing.",
	"diagnostics:update": "there is no diagnostics collector. Same reasoning: it reports on the " +
		"server, so it would describe this process, not the old one. The card renders empty.",
}

var (
	// An emit call: Send / Broadcast / emit, with the event name within the
	// first few arguments (the room usually comes first).
	goEmit = regexp.MustCompile(`(?s)\b\w*(?:Send|Broadcast|[Ee]mit)\w*\(\s*(?:(?:[^,()]|\([^()]*\))*,\s*){0,3}"([a-z][a-zA-Z0-9]*:[a-zA-Z0-9:_-]+)"`)
	tsOn   = regexp.MustCompile(`socket\.on\(\s*'([^']+)'`)
	genEv  = regexp.MustCompile(`"event": "([^"]+)"`)
)

func TestWebSocketVocabulary(t *testing.T) {
	root := repoRoot(t)

	goSrc := joined(readFiles(t, root, "internal/", func(r string) bool { return hasExt(r, ".go") }))
	tsSrc := joined(readFiles(t, root, "web/src/", func(r string) bool { return hasExt(r, ".ts") }))

	emits := map[string]bool{}
	for _, m := range goEmit.FindAllStringSubmatch(goSrc, -1) {
		emits[m[1]] = true
	}
	subs := map[string]bool{}
	for _, m := range tsOn.FindAllStringSubmatch(tsSrc, -1) {
		subs[m[1]] = true
	}
	// Generated tables name their events in data rather than in a socket.on.
	for _, m := range genEv.FindAllStringSubmatch(tsSrc, -1) {
		subs[m[1]] = true
	}
	// Socket.IO's own lifecycle events are not application vocabulary.
	for _, e := range []string{"connect", "disconnect", "connect_error"} {
		delete(subs, e)
	}

	// FLOORS. Both sides are found by regex, and a regex that stops matching
	// would leave this test comparing two empty sets and passing.
	if len(emits) < 40 {
		t.Fatalf("only %d Go emits found — the match broke, and this test is comparing nothing", len(emits))
	}
	if len(subs) < 40 {
		t.Fatalf("only %d subscriptions found — the match broke", len(subs))
	}

	var unconsumed, unserved []string
	for e := range emits {
		// A name may be referenced in TypeScript without a socket.on — a
		// re-dispatch, or a table keyed by it — and that still counts as consumed.
		if !subs[e] && !strings.Contains(tsSrc, "'"+e+"'") {
			unconsumed = append(unconsumed, e)
		}
	}
	for e := range subs {
		if !emits[e] {
			unserved = append(unserved, e)
		}
	}
	sort.Strings(unconsumed)
	sort.Strings(unserved)

	checkLedger(t, "emitted but nothing listens", unconsumed, eventsUnconsumed)
	checkLedger(t, "listened for but nothing emits", unserved, eventsUnserved)

	t.Logf("%d Go emits, %d subscriptions, %d unconsumed and %d unserved, all recorded",
		len(emits), len(subs), len(unconsumed), len(unserved))
}

// checkLedger fails in BOTH directions: a gap with no entry, and an entry whose
// gap has closed. The second half is what stops the ledger becoming folklore —
// a note that has stopped being true is deleted rather than inherited.
func checkLedger(t *testing.T, heading string, found []string, record map[string]string) {
	t.Helper()
	have := map[string]bool{}
	for _, e := range found {
		have[e] = true
		if _, ok := record[e]; !ok {
			t.Errorf("%s, and not recorded: %s\n    Add it with the reason, or wire it up.", heading, e)
		}
	}
	for e := range record {
		if !have[e] {
			t.Errorf("%q is recorded as %q, but that is no longer true — delete the entry rather "+
				"than leaving a note that has stopped describing anything.", e, heading)
		}
	}
}

func joined(files map[string]string) string {
	parts := make([]string, 0, len(files))
	for _, v := range files {
		parts = append(parts, v)
	}
	return strings.Join(parts, "\n")
}
