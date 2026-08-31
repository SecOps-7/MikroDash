package routers

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// EVERY COLLECTOR THIS POOL BUILDS IS ALSO STOPPED.
//
// ── THE CLASS, AND WHY THIS IS THE THIRD COPY OF IT ───────────────────────
//
// A collector that is started and never stopped keeps a self-rescheduling timer
// alive for the life of the PROCESS once its session goes. No event is emitted —
// `reader.Connected()` gates the poll — so nothing logs, nothing fails, and the
// only symptom is a process that slowly does more work than it should.
//
// This exact failure has been found four times in this port:
//
//   - `session.Manager.Release` stopped 5 of the 14 collectors it starts.
//   - `session.Manager.Shutdown` had the same defect, independently.
//   - `alertpool` had a session LEAKED by a plan that both built and rebuilt it.
//   - the pool's history bucket was never flushed on shutdown, losing a minute
//     per restart.
//
// `internal/session` and `internal/alertpool` each grew a source-derived ledger
// after theirs. THIS package had none, and it gained two collectors — the
// `traffic`/`ping` history pair — on 2026-08-30. So it gets the same ledger,
// rather than waiting for the fifth instance.
//
// READ FROM SOURCE, because the property is about code that may never run: a
// behavioural test would have to arrange a teardown for every collector, and the
// one that is forgotten is exactly the one nobody arranges.
func TestEveryPooledCollectorIsStopped(t *testing.T) {
	b, err := os.ReadFile("pool.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	built := fieldsBetween(t, src, "func (p *Pool) build(", `s\.(\w+) = collect\.New`)
	if len(built) == 0 {
		t.Fatal("no collectors found in build() — the anchor has moved and this " +
			"test is measuring nothing")
	}

	// The stop side is TWO functions: `stopCollectors` for the three ordinary
	// ones, and `setHistoryCollectors` for the pair, which is separate precisely
	// because it starts and stops on a different signal.
	stopped := map[string]bool{}
	for _, anchor := range []string{
		"func (s *poolSession) stopCollectors(",
		"func (s *poolSession) setHistoryCollectors(",
	} {
		for _, f := range fieldsBetween(t, src, anchor, `s\.(\w+)\.Stop\(\)`) {
			stopped[f] = true
		}
	}
	// `stopCollectors` calls `setHistoryCollectors(false)`, so the pair is
	// reachable from the ordinary teardown too. Asserted rather than assumed:
	// without that call the pair would stop only on a history-router change.
	if !regexp.MustCompile(`func \(s \*poolSession\) stopCollectors\([\s\S]{0,400}?setHistoryCollectors\(false\)`).
		MatchString(src) {
		t.Error("stopCollectors does not call setHistoryCollectors(false); the history " +
			"pair would then outlive the session that built it")
	}

	var leaked []string
	for _, f := range built {
		if !stopped[f] {
			leaked = append(leaked, f)
		}
	}
	sort.Strings(leaked)
	if len(leaked) > 0 {
		t.Errorf("%d collector(s) are built and never stopped: %v\n"+
			"Each keeps a self-rescheduling timer alive for the life of the process "+
			"once its session goes, emitting nothing and logging nothing.\nbuilt=%v",
			len(leaked), leaked, built)
	}
}

// fieldsBetween collects `s.<field>` names matching pat inside one function.
func fieldsBetween(t *testing.T, src, anchor, pat string) []string {
	t.Helper()
	i := strings.Index(src, anchor)
	if i < 0 {
		t.Fatalf("anchor %q not found — this test is measuring nothing", anchor)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}"); j >= 0 {
		rest = rest[:j]
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(pat).FindAllStringSubmatch(rest, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
