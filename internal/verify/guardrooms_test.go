package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoServerHandlerNamesARoomItself is the other half of 4.2's completeness
// rule, carried forward through the switchboard's deletion.
//
// ── WHAT IT REPLACED, TWICE ─────────────────────────────────────────────────
//
// Until 4.2 it compared two lists: the rooms `pageBlur` waited on, spelled out by
// hand at each call site, against the rooms the collector emitted to. They had
// disagreed FIVE times - dhcpNetworks, bandwidth, vpn, firewall, and routing on
// 2026-08-31 - each time as a dashboard card that silently stopped updating for
// anybody who had visited the owning page and left.
//
// 4.2 made the wait list derived, so the two could not disagree, and this test
// became a refusal: no call site may go back to writing its own list. 4.2b
// deleted the call sites entirely - there is one question now,
// `collect.DemandRooms(key)`, asked in one place. The refusal is unchanged and
// simply covers more ground: no handler in `internal/server` may name a room a
// collector feeds.
//
// A weaker-sounding property and a stronger position, for the reason 4.2 gave:
// the old check could only catch a disagreement after somebody wrote one, and
// this one refuses the shape that makes disagreement possible.
func TestNoServerHandlerNamesARoomItself(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "server")

	// A `page-` or `dash-card-` room written as a literal. The room PREFIX
	// (`"router-" + routerID + "-page-" + page`) is how a handler joins and
	// leaves its own room and is not what this forbids - that is a room name
	// being BUILT, not a collector's audience being restated.
	lit := regexp.MustCompile(`"(page-[a-z-]+|dash-card-[a-z]+)"`)

	found := 0
	for _, name := range goFilesIn(t, dir) {
		flat := stripGoComments(mustRead(t, filepath.Join(dir, name)))
		for _, m := range lit.FindAllStringSubmatch(flat, -1) {
			found++
			t.Errorf("%s names the room %q. Rooms are declared in "+
				"internal/collect/rooms.go and demand reads collect.DemandRooms(); a "+
				"literal here is the second statement that phase 4.2 removed, and it "+
				"is how a card silently stops updating.", name, m[1])
		}
	}

	// And the derivation must actually be in use, or this check passes by
	// looking at a package that no longer decides anything.
	// THE RULE MOVED TO internal/session IN 6.3. It was stated on both sides -
	// the server asked about rooms, the session asked about holds, and for a
	// session with no viewer those are the same question.
	rule := stripGoComments(mustRead(t, filepath.Join(repoRoot(t), "internal", "session", "needs.go")))
	if !strings.Contains(rule, "collect.DemandRooms(") {
		t.Fatal("the demand rule no longer calls collect.DemandRooms; it has stopped " +
			"deriving its rooms and this check would not notice")
	}
	if found == 0 {
		t.Logf("no room literal in %d server files", len(goFilesIn(t, dir)))
	}
}

// goFilesIn lists a package's non-test .go files. `goFiles` in
// blur_suspend_test.go does the same thing; this one exists because that file
// may be re-aimed again and a shared helper between two audits is a coupling
// neither wants.
func goFilesIn(t *testing.T, dir string) []string {
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

// stripGoComments drops // lines, so a comment describing the shape this test
// forbids does not fail it.
//
// The third time this trap has been hit here: the credential scanner reading a
// comment about proplists, a dormancy gate reading its own explanation, and this.
func stripGoComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
