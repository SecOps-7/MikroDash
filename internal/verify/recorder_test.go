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
// of them - `Connected`, `Disconnected` and `Forget` - had NO caller outside
// their own package for the whole life of the Go port. `Record` did, so ping and
// traffic kept being written while `connectivity_events` stopped dead at the
// cutover, and the Reports page, reading a table frozen mid-outage, showed every
// router Down at about 2% uptime.
//
// Nothing failed. `internal/history/connectivity.go` is complete and pinned by
// its own corpus; `internal/historywire/conn_test.go` drives the wire directly
// and passes. Both halves were correct and unconnected, and the whole suite was
// green - which is exactly the shape a unit test cannot see.
//
// So this asks the one question those tests cannot: is anything in the SHIPPED
// binary calling this? An exported entry point on a recorder is not a library
// for tests to exercise; if no production code reaches it, either it is dead or
// something is not being recorded, and both need saying out loud.
//
// ── THE LEDGER FAILS IN BOTH DIRECTIONS ─────────────────────────────────────
//
// An unrecorded gap is a failure, and an entry recorded as unwired that HAS
// acquired a caller is also a failure - otherwise the list becomes a place to
// file things rather than a record of them. That is the rule the attribute
// ledger broke: `data-val` sat in it excused as "a feature this port has not
// taken on" while being a plain bug on a shipped page.
var recorderUnwired = map[string]string{
	// `Tick` WAS HERE, excused as "no ticker: both callers use a zero threshold".
	// The excuse expired in two stages - `startConnTicker` gave it a clock, and
	// `connDownThresholdSec` reached it - and the method itself has since left
	// this package for `internal/connstate`. The entry is deleted rather than
	// reworded, and the loop below now fails on a ledger entry naming a method
	// that no longer exists, which is what would have caught it sitting here.
	// `Records` is a PREDICATE, not a writer, and this check is about writers:
	// its question is "does a table stop being written because nothing calls
	// this". `Record` consults it on every traffic sample from inside the
	// package, which the scan cannot see because it excludes the recorder's own
	// source. It is exported so a caller can ask what a declaration means
	// without reproducing the empty-list rule - which is the half that is easy
	// to get backwards - and `SetRecordedInterfaces`, the entry point that
	// actually matters, IS called from the fleet syncs and is checked here.
	"Records": "a predicate consulted by Record inside the package; exported for " +
		"callers to ask rather than to be driven",
	// Same shape as `Records` directly above: `Record` and `RecordConn` both
	// consult it from inside the package, which the scan cannot see because it
	// excludes the recorder's own source. `SetReporting` is the entry point that
	// matters here, and it IS called from both fleet syncs.
	"Reporting": "a predicate consulted by Record and RecordConn inside the " +
		"package; exported for callers to ask rather than to be driven",
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
		t.Fatalf("found %d exported Wire methods - the recorder's shape changed and "+
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
	// `Tick` - which reported the recorder's own `Tick` as wired on the first
	// run of this check. So the names holding a `*historywire.Wire` are read out
	// of the source first, and only those count.
	// STRUCT FIELDS ONLY - anchored to the start of a line, so a PARAMETER
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
		t.Fatal("no variable of type *historywire.Wire was found - this check " +
			"would report every entry point as unwired")
	}
	names := make([]string, 0, len(recvs))
	for r := range recvs {
		names = append(names, regexp.QuoteMeta(r))
	}
	sort.Strings(names)
	recvAlt := strings.Join(names, "|")

	// ── AND THE LEDGER ITSELF IS CHECKED AGAINST THE SOURCE ────────────────
	//
	// The loop below reads the METHODS and asks the ledger about each, so an
	// entry naming a method that no longer exists is never consulted and never
	// fails. `Tick` sat here for exactly that reason after its excuse had
	// expired. A ledger that cannot go stale has to be read both ways.
	have := map[string]bool{}
	for _, name := range entry {
		have[name] = true
	}
	for name, reason := range recorderUnwired {
		if !have[name] {
			t.Errorf("the ledger excuses Wire.%s (%q), which is no longer a method "+
				"on the recorder. Delete the entry.", name, reason)
		}
	}

	var missing []string
	for _, name := range entry {
		// Optionally qualified - `s.historyWire`, `m.history`, or a bare local.
		//
		// A METHOD VALUE COUNTS, which is the second alternative: the server
		// wires `RecordConn` by handing it to `connstate.New` as the tracker's
		// row sink, so it is never spelled with a `(` after it. Bounded to a
		// following comma or close paren so it matches an argument rather than
		// any mention of the name.
		used := regexp.MustCompile(`(?:\w+\.)?(?:` + recvAlt + `)\.` + name + `(?:\(|\s*[,)])`).
			MatchString(callers)
		reason, recorded := recorderUnwired[name]
		switch {
		case used && recorded:
			t.Errorf("Wire.%s is recorded as unwired (%q) and something calls it now - "+
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
