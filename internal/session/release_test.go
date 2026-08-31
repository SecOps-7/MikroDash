package session

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ── WHY THIS READS THE SOURCE ──────────────────────────────────────────────
//
// The property is "Release stops everything the connect block started", and it
// is a property of TWO LISTS that sit 200 lines apart in one file. Nothing
// observable distinguishes a stopped collector from an unstopped one on a
// session whose client is already closed — the reads fail either way and nobody
// is listening — which is exactly why the gap survived for most of the port.
//
// So the pin is the same shape as `TestTheBackgroundCollectorCountIsRecorded`,
// which already counts the connect block out of this file: measure both lists
// and compare them, and name the difference when they disagree.

func sessionSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatalf("reading session.go: %v", err)
	}
	return string(b)
}

// blockBetween returns the source between an opening anchor and the first line
// that is exactly `end` at the given indent — enough structure for two blocks
// whose shape is stable, and it FAILS rather than returning nothing when the
// anchor moves.
func blockBetween(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("anchor %q not found in session.go — this test is measuring nothing", start)
	}
	rest := src[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("end anchor %q not found after %q", end, start)
	}
	return rest[:j]
}

var collectorRe = regexp.MustCompile(`s\.([A-Za-z]+)\.(Start|Stop)\(\)`)

func namesIn(src, verb string) []string {
	seen := map[string]bool{}
	for _, m := range collectorRe.FindAllStringSubmatch(src, -1) {
		if m[2] == verb {
			seen[m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestReleaseStopsEveryCollectorTheConnectBlockStarted is the pin.
//
// THE BUG IT WAS WRITTEN FOR: Release stopped dns, bridges, vlans, wan and
// ifStatus — five of the fourteen. The other nine kept their `time.Timer`
// rescheduling forever, because `pollLoop` arms the next tick from inside the
// current one and never asks whether the session is still alive.
//
// ── BOTH TEARDOWN PATHS, NOT JUST Release ─────────────────────────────────
//
// This checked `Release` alone. `Manager.Shutdown` is the other way a session
// ends — SIGTERM — and it had the IDENTICAL defect, still stopping five of
// fourteen after Release was fixed on 2026-08-29. A test naming one function
// cannot see its sibling, and the sibling is the path every container restart
// takes.
func TestBothTeardownPathsStopEveryCollector(t *testing.T) {
	src := sessionSource(t)
	started := namesIn(blockBetween(t, src, "if first {", "\n\t\t}"), "Start")
	if len(started) == 0 {
		t.Fatal("measured nothing: the connect-block anchor has moved")
	}

	for _, path := range []struct{ name, anchor string }{
		{"Release", "func (m *Manager) Release("},
		{"Shutdown", "func (m *Manager) Shutdown("},
	} {
		t.Run(path.name, func(t *testing.T) {
			stopped := namesIn(blockBetween(t, src, path.anchor, "\n}"), "Stop")
			if len(stopped) == 0 {
				t.Fatalf("%s stops nothing — the anchor has moved", path.name)
			}
			inStopped := map[string]bool{}
			for _, n := range stopped {
				inStopped[n] = true
			}
			var leaked []string
			for _, n := range started {
				if !inStopped[n] {
					leaked = append(leaked, n)
				}
			}
			if len(leaked) > 0 {
				t.Errorf("%s does not stop %d collector(s) the connect block starts: %v\n"+
					"Each keeps a self-rescheduling timer alive on a session whose client is "+
					"closed.\nstarted=%v\nstopped=%v",
					path.name, len(leaked), leaked, started, stopped)
			}
		})
	}
}

// AND BOTH FLUSH THE OPEN HISTORY BUCKET.
//
// A minute only rolls over when the NEXT minute's first sample arrives, so a
// teardown mid-minute loses it unless something flushes. `Release` does;
// `Shutdown` did not, so every container restart dropped the current minute for
// every router — silently, as a gap in a chart rather than an error.
func TestBothTeardownPathsFlushHistory(t *testing.T) {
	src := sessionSource(t)
	for _, path := range []struct{ name, anchor string }{
		{"Release", "func (m *Manager) Release("},
		{"Shutdown", "func (m *Manager) Shutdown("},
	} {
		body := blockBetween(t, src, path.anchor, "\n}")
		if !strings.Contains(body, "m.history.Flush(") {
			t.Errorf("%s never calls m.history.Flush: the minute in progress is lost, "+
				"which renders as a quiet minute rather than an error", path.name)
		}
	}
}

// TestTheConnectBlockStartsFourteenCollectors is the number CLAUDE.md and
// PORT-QUEUE.md both quote for the coexistence argument ("Session STARTS 14
// collectors on connect where the live pool runs 3").
//
// It is pinned SEPARATELY from the test above on purpose: that one asserts the
// two lists agree, and would stay green if somebody deleted a Start and its
// matching Stop together. This one notices the count moved, which is what the
// documents claim.
func TestTheConnectBlockStartsFourteenCollectors(t *testing.T) {
	started := namesIn(blockBetween(t, sessionSource(t), "if first {", "\n\t\t}"), "Start")
	if len(started) != 14 {
		t.Errorf("the connect block starts %d collectors, not 14: %v\n"+
			"CLAUDE.md and PORT-QUEUE.md both quote 14 and derive the 4.7x "+
			"coexistence ratio from it. Update BOTH if this is deliberate.",
			len(started), started)
	}
}

// TestEverythingSuspendedOnDisconnectIsResumedOnReconnect is the third list-pair
// in this file, and it found a real bug the same way the first one did.
//
// ── THE INVARIANT IS ONE-DIRECTIONAL, AND THAT IS NOT A WEAKENING ─────────
//
// Suspended ⊆ Reconnected. The reverse does NOT hold and must not be asserted:
// netwatch, talkers and ping are resumed without being suspended, which is
// harmless because `reader.Do` fails closed on a nil client and all three gate
// on `Connected()` — they cost one no-op tick per interval while the link is
// down. Asserting equality would force a change that fixes nothing.
//
// THE BUG: `packages` and `routing` were suspended on disconnect and resumed by
// nothing. After a reconnect — usually a RouterOS upgrade, so precisely when
// somebody is watching — their pages stopped updating until the viewer navigated
// away and back. The live app restores them (`src/index.js:685`,
// `_updateAllPageStreams`).
func TestEverythingSuspendedOnDisconnectIsResumedOnReconnect(t *testing.T) {
	src := sessionSource(t)

	down := blockBetween(t, src, "s.waitUntilDown(c)", "if !down {")
	suspended := map[string]bool{}
	for _, m := range collectorRe2.FindAllStringSubmatch(down, -1) {
		suspended[m[1]] = true
	}

	back := blockBetween(t, src, "} else {", "\n\t\t}")
	resumed := map[string]bool{}
	for _, m := range reconnectRe.FindAllStringSubmatch(back, -1) {
		resumed[m[1]] = true
	}

	if len(suspended) == 0 || len(resumed) == 0 {
		t.Fatalf("measured nothing: %d suspended, %d resumed — the anchors have moved",
			len(suspended), len(resumed))
	}

	var stuck []string
	for n := range suspended {
		if !resumed[n] {
			stuck = append(stuck, n)
		}
	}
	sort.Strings(stuck)
	if len(stuck) > 0 {
		t.Errorf("%d collector(s) are suspended on disconnect and resumed by nothing: %v\n"+
			"After a reconnect they stay off until somebody focuses their page, so a "+
			"viewer already on that page sees it stop updating forever. Reconnects "+
			"usually follow a RouterOS upgrade, which is exactly when someone is watching.",
			len(stuck), stuck)
	}
}

var (
	collectorRe2 = regexp.MustCompile(`s\.([A-Za-z]+)\.(?:Suspend|Stop)\(\)`)
	// BOTH VERBS COUNT AS RESUMING. `Reconnected` is for collectors holding an
	// absent-menu latch a reboot can invalidate; `Resume` is the plain restart
	// for those that hold none. Matching only the first would report `routing`
	// as stuck forever and push somebody toward adding a `Reconnected` that has
	// no verdict to drop.
	reconnectRe = regexp.MustCompile(`s\.([A-Za-z]+)\.(?:Reconnected|Resume)\(\)`)
)
