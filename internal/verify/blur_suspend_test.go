package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoPageSuspendsACollectorByName is the re-aimed blur-suspend audit.
//
// ── WHAT IT USED TO ASSERT ──────────────────────────────────────────────────
//
// `pageBlur` held a 19-case switch that stopped a page's collectors when the
// operator navigated away. That is correct only while the page is the
// collector's ONLY audience, and several collectors also feed a dashboard card -
// so blurring the page starved the card. This test matched
// `cn.rsession.X().Suspend()` inside that switch and objected when the collector
// behind X emitted to more than one room, insisting on the guarded form.
//
// It caught the same defect five times: dhcpNetworks, bandwidth, vpn, firewall,
// and routing on 2026-09-01 when it gained `page-dashboard` as a second room.
//
// ── WHY IT ASSERTS SOMETHING STRONGER NOW ───────────────────────────────────
//
// Phase 4.2b deleted the switchboard. `applyDemand` asks
// `collect.DemandRooms(key)` for every collector at once, so a page can no
// longer suspend a collector it does not own - the shape that made the defect
// possible is gone rather than guarded.
//
// The old assertion would now pass by inspecting an empty switch, which is worse
// than useless: a green check that cannot fail. So it is re-aimed at the one way
// the defect can come back, which is somebody adding a by-name suspend to a
// page handler again. That is a REFUSAL of a shape rather than a check of a
// list, which is the same move `TestNoBlurGuardNamesARoomItself` made in 4.2 and
// for the same reason: the old check could only catch a disagreement after
// somebody wrote one.
func TestNoPageSuspendsACollectorByName(t *testing.T) {
	root := repoRoot(t)
	wsSrc := mustRead(t, filepath.Join(root, "internal", "server", "ws.go"))
	cardSrc := mustRead(t, filepath.Join(root, "internal", "server", "dashcard.go"))

	// The two shapes the switchboard used. Comments are stripped first, or this
	// test fails on the paragraphs above the call sites that describe what was
	// removed - the third-time-hit trap `stripGoComments` exists for.
	byName := regexp.MustCompile(`(?:cn\.rsession|rs)\.(\w+)\(\)\.Suspend\(\)`)
	byKey := regexp.MustCompile(`\.SuspendCollector\("(\w+)"\)`)

	for _, f := range []struct{ name, src string }{
		{"ws.go", wsSrc}, {"dashcard.go", cardSrc},
	} {
		flat := strings.Join(strings.Fields(stripGoComments(f.src)), " ")
		for _, m := range byName.FindAllStringSubmatch(flat, -1) {
			t.Errorf("%s suspends %s() by name. A page or card handler must not decide "+
				"which collectors stop - it leaves its room, and applyDemand re-asks "+
				"collect.DemandRooms for every collector. Naming one here is how a "+
				"dashboard card silently stopped updating five times.", f.name, m[1])
		}
		for _, m := range byKey.FindAllStringSubmatch(flat, -1) {
			t.Errorf("%s calls SuspendCollector(%q) directly. The only caller is "+
				"suspendAfterGrace in demand.go, which re-asks whether anything still "+
				"wants it; a direct call skips both the question and the grace.",
				f.name, m[1])
		}
	}

	// ── THE TEST MUST PROVE ITS OWN DATA IS REAL ────────────────────────────
	//
	// Finding nothing is the PASSING state, so a scan that had stopped reading
	// the files would look identical to a clean repository. These two anchors are
	// what the refusal is about: the handlers must still exist, and the demand
	// call must still be in them.
	for _, f := range []struct{ name, src, fn string }{
		{"ws.go", wsSrc, "func (cn *conn) pageBlur("},
		{"dashcard.go", cardSrc, "func (cn *conn) dashCardBlur("},
	} {
		if !strings.Contains(f.src, f.fn) {
			t.Fatalf("%s no longer holds %s - this test is reading the wrong file", f.name, f.fn)
		}
		if !strings.Contains(f.src, "applyDemand(") {
			t.Fatalf("%s does not call applyDemand; the handlers have stopped driving "+
				"demand at all and this refusal is guarding nothing", f.name)
		}
	}
}

// TestDemandIsTheOnlyCallerOfSuspendCollector.
//
// The other half, and the one that keeps the refusal above from being evaded by
// moving the call somewhere else in the package. `SuspendCollector` exists for
// `applyDemand`; any second caller is a collector stopped without asking whether
// anything still wants it.
func TestDemandIsTheOnlyCallerOfSuspendCollector(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "server")
	found := 0
	for _, name := range goFiles(t, dir) {
		flat := stripGoComments(mustRead(t, filepath.Join(dir, name)))
		n := strings.Count(flat, "SuspendCollector(")
		if n == 0 {
			continue
		}
		found += n
		if name != "demand.go" {
			t.Errorf("%s calls SuspendCollector %d time(s); demand.go is the only place "+
				"a collector may be stopped, because it is the only place the question "+
				"'does anything still want this' is asked", name, n)
		}
	}
	if found == 0 {
		t.Fatal("no call to SuspendCollector was found anywhere in internal/server - " +
			"the scan has stopped matching and this check is measuring nothing")
	}
}

// `collectorRooms`, `suspendReceivers` and `sessionAccessors` lived here and
// were deleted with the audits they served. They resolved a `X().Suspend()` call
// in `pageBlur` back to the collector behind it - accessor to Go type to source
// file to emit strings - so the audit could ask how many rooms that collector
// fed. Nothing suspends by accessor any more, and the room question is answered
// by `collect.RoomsOf` directly.

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			out = append(out, n)
		}
	}
	return out
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// sliceBetween returns the text from `from` up to `to`, failing loudly when
// either anchor is gone - an anchor that silently misses would leave every
// assertion below inspecting the wrong code.
func sliceBetween(t *testing.T, src, from, to string) string {
	t.Helper()
	i := strings.Index(src, from)
	if i < 0 {
		t.Fatalf("anchor lost: %q", from)
	}
	j := strings.Index(src[i:], to)
	if j < 0 {
		t.Fatalf("anchor lost: %q, which should follow %q", to, from)
	}
	return src[i : i+j]
}

// TestGuardedSuspendCoversEveryDashboardRoom lived here, and phase 4.2b removed
// the shape it inspected. Recorded rather than silently dropped, because it is
// the check that caught this project's most expensive silent defect and a reader
// should be able to find out where the property went.
//
// ── WHAT IT ASSERTED ────────────────────────────────────────────────────────
//
// A guarded suspend named its collector by KEY and suspended it by ACCESSOR, and
// nothing connected the two: `collect.Others("vpn", …)` beside
// `Wireless().Suspend` compiled, read perfectly, and consulted the wrong
// collector's rooms. Before that it asserted the room LIST was complete, after
// `suspendConnsIfIdle` passed the two page rooms and not
// `dash-card-connections` - so returning to the dashboard from either page
// suspended the collector with a viewer still watching the card.
//
// ── WHERE THE PROPERTY WENT ─────────────────────────────────────────────────
//
// Both halves are structural now. There is no key-and-accessor pair to mismatch,
// because `applyDemand` iterates `session.TargetKeys()` and passes each key to
// both sides of the question. There is no room list to be incomplete, because
// `collect.DemandRooms` derives it.
//
// What is DRIVEN rather than derived is in `internal/server/demand_behaviour_test.go`:
// `TestEveryDeclaredRoomAloneIsEnough` puts one viewer in each declared room in
// turn and asserts the collector is wanted, which is the same question this test
// asked - can a room be dropped without anybody noticing - asked of the rule
// that replaced the guards.
