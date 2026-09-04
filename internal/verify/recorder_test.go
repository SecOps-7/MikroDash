package verify

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryRecorderEntryPointHasAProductionCaller.
//
// ── A PORTED STATE MACHINE THAT NOTHING DROVE ──────────────────────────────
//
// `internal/historywire` exports the calls that write the history tables. Three
// of them — `Connected`, `Disconnected` and `Forget` — had NO caller outside
// their own package for the whole life of the Go port. `Record` did, so ping and
// traffic kept being written while `connectivity_events` stopped dead at the
// cutover, and the Reports page, reading a table frozen mid-outage, showed every
// router Down at about 2% uptime.
//
// Nothing failed. `internal/history/connectivity.go` is complete and pinned by
// its own corpus; `internal/historywire/conn_test.go` drives the wire directly
// and passes. Both halves were correct and unconnected, and the whole suite was
// green — which is exactly the shape a unit test cannot see.
//
// So this asks the one question those tests cannot: is anything in the SHIPPED
// binary calling this? An exported entry point on a recorder is not a library
// for tests to exercise; if no production code reaches it, either it is dead or
// something is not being recorded, and both need saying out loud.
//
// ── THE LEDGER FAILS IN BOTH DIRECTIONS ─────────────────────────────────────
//
// An unrecorded gap is a failure, and an entry recorded as unwired that HAS
// acquired a caller is also a failure — otherwise the list becomes a place to
// file things rather than a record of them. That is the rule the attribute
// ledger broke: `data-val` sat in it excused as "a feature this port has not
// taken on" while being a plain bug on a shipped page.
var recorderUnwired = map[string]string{
	// `Tick` advances the disconnect debounce, and nothing drives it BY DESIGN.
	// The two callers pass a threshold of zero, which `internal/history`'s rule
	// 4 makes its own branch: it records on every close and needs no timer. A
	// non-zero threshold has nowhere to come from either — `connDownThresholdSec`
	// is in routers.json and is not modelled on `store.Router`. Wiring a ticker
	// means adding that field first, and this entry is what will fail when
	// somebody does.
	"Tick": "no ticker: both callers use a zero threshold, which is its own " +
		"branch in internal/history and needs no debounce",
	// `Records` is a PREDICATE, not a writer, and this check is about writers:
	// its question is "does a table stop being written because nothing calls
	// this". `Record` consults it on every traffic sample from inside the
	// package, which the scan cannot see because it excludes the recorder's own
	// source. It is exported so a caller can ask what a declaration means
	// without reproducing the empty-list rule — which is the half that is easy
	// to get backwards — and `SetRecordedInterfaces`, the entry point that
	// actually matters, IS called from the fleet syncs and is checked here.
	"Records": "a predicate consulted by Record inside the package; exported for " +
		"callers to ask rather than to be driven",
}

func TestEveryRecorderEntryPointHasAProductionCaller(t *testing.T) {
	root := repoRoot(t)

	// The exported methods on the recorder, read from its own source.
	wireSrc := joined(readFiles(t, root, "internal/historywire/", func(r string) bool {
		return hasExt(r, ".go") && !isTestSource(r)
	}))
	methodRe := regexp.MustCompile(`func \(w \*Wire\) ([A-Z]\w*)\(`)
	var entry []string
	for _, m := range methodRe.FindAllStringSubmatch(wireSrc, -1) {
		entry = append(entry, m[1])
	}
	sort.Strings(entry)
	if len(entry) < 4 {
		t.Fatalf("found %d exported Wire methods — the recorder's shape changed and "+
			"this check is scanning nothing, which would pass for ever", len(entry))
	}

	// ── AND THE SCAN MUST NOT READ ITSELF ───────────────────────────────────
	//
	// `isTestSource` excludes `internal/verify/`, so the method names quoted in
	// the ledger above do not count as callers. Without that this proves any
	// name it mentions is wired, which is the trap this package has hit three
	// times.
	callers := joined(readFiles(t, root, "internal/", func(r string) bool {
		return hasExt(r, ".go") && !isTestSource(r) &&
			!strings.HasPrefix(r, "internal/historywire/")
	}))
	callers += joined(readFiles(t, root, "cmd/", func(r string) bool {
		return hasExt(r, ".go") && !isTestSource(r)
	}))

	// ── THE RECEIVERS ARE DISCOVERED, NOT LISTED ────────────────────────────
	//
	// A bare `\.Tick\(` matches any receiver, and half the collectors have a
	// `Tick` — which reported the recorder's own `Tick` as wired on the first
	// run of this check. So the names holding a `*historywire.Wire` are read out
	// of the source first, and only those count.
	// STRUCT FIELDS ONLY — anchored to the start of a line, so a PARAMETER
	// named `w` in `SetHistoryWire(w *historywire.Wire)` is not collected. It
	// was on the first run, and `w.Tick()` in half a dozen collectors then
	// reported the recorder's own Tick as wired. The field names are the ones a
	// caller actually reaches it through.
	recvRe := regexp.MustCompile(`(?m)^\s*(\w+)\s+\*historywire\.Wire`)
	recvs := map[string]bool{}
	for _, m := range recvRe.FindAllStringSubmatch(callers, -1) {
		recvs[m[1]] = true
	}
	// The local the server builds it into, which is a short assignment rather
	// than a typed declaration.
	for _, m := range regexp.MustCompile(`(\w+)\s*:=\s*\w+\.buildHistoryWire`).
		FindAllStringSubmatch(callers, -1) {
		recvs[m[1]] = true
	}
	if len(recvs) == 0 {
		t.Fatal("no variable of type *historywire.Wire was found — this check " +
			"would report every entry point as unwired")
	}
	names := make([]string, 0, len(recvs))
	for r := range recvs {
		names = append(names, regexp.QuoteMeta(r))
	}
	sort.Strings(names)
	recvAlt := strings.Join(names, "|")

	var missing []string
	for _, name := range entry {
		// Optionally qualified — `s.historyWire`, `m.history`, or a bare local.
		used := regexp.MustCompile(`(?:\w+\.)?(?:` + recvAlt + `)\.` + name + `\(`).
			MatchString(callers)
		reason, recorded := recorderUnwired[name]
		switch {
		case used && recorded:
			t.Errorf("Wire.%s is recorded as unwired (%q) and something calls it now — "+
				"delete the entry rather than leaving a note that has stopped being true",
				name, reason)
		case !used && !recorded:
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Errorf("no production code calls: Wire.%s\n"+
			"An entry point on the recorder that nothing reaches means a table is "+
			"not being written, and no unit test can see it: the state machine and "+
			"the wire both have their own passing tests. That is how "+
			"connectivity_events stopped being written at the cutover while ping "+
			"and traffic carried on. Wire it, or record it above with the reason.",
			strings.Join(missing, ", Wire."))
	}
	t.Logf("%d recorder entry points, %d recorded as deliberately unwired",
		len(entry), len(recorderUnwired))
}
