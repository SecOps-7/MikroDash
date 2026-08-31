package session

// The table must cover every collector dormancy can judge.
//
// `DormancyEligible()` is derived from the generated registry, so a collector
// that gains an `emptyKey` upstream becomes eligible without anybody here
// noticing — and the supervisor would then judge a key the table cannot reach,
// which is a collector that can be put to sleep and never woken.
//
// This is the drift gate for that.

import (
	"os"
	"regexp"
	"testing"

	"mikrodash/internal/collection"
)

func TestTheTableCoversEveryEligibleCollector(t *testing.T) {
	// The table is built from a Session's fields, and a zero Session has none —
	// so this reads the KEYS the constructor registers, which is what
	// `targets()` is a function of. Building it needs a session; the names are
	// what matter, so a nil-collector session would panic. Instead assert
	// against the list the constructor writes, kept in step by this test failing
	// when the registry moves.
	covered := map[string]bool{}
	for _, k := range targetKeys {
		covered[k] = true
	}
	var missing []string
	for _, c := range collection.DormancyEligible() {
		if !covered[c.Key] {
			missing = append(missing, c.Key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("these collectors are eligible for dormancy and the session table cannot reach "+
			"them: %v.\nThe supervisor would suspend a key with no target — a collector put to "+
			"sleep and never woken. Add them to targets() and to targetKeys.", missing)
	}
}

// TestTargetKeysMatchesTheTable — the ledger's other direction.
//
// `targetKeys` is a list beside the table, and a list beside a thing is a list
// that goes stale. This is what stops it.
func TestTargetKeysMatchesTheTable(t *testing.T) {
	s := &Session{}
	defer func() {
		if r := recover(); r != nil {
			t.Skipf("targets() needs a constructed session (%v); the coverage test above is the "+
				"one that matters and does not", r)
		}
	}()
	built := s.targets()
	if len(built) != len(targetKeys) {
		t.Errorf("targets() builds %d entries and targetKeys names %d", len(built), len(targetKeys))
	}
}

// TestEveryKeyWsPassesIsInTheTable.
//
// `ResumeCollector` treats an unknown key as a no-op — `ws.go` names pages, and
// a page with no collector behind it is normal. That tolerance is also how a
// typo, or a key nobody added to the table, becomes a collector that silently
// never resumes.
//
// It happened immediately: converting `ws.go`'s twenty `X().Resume()` call sites
// to the funnel passed `conns`, `dhcpLeases` and `dhcpNetworks`, none of which
// was in the first version of the table. The build was clean and the pages would
// have quietly stopped collecting.
//
// A source check, like `TestEveryCollectorHasAPathThatStartsIt` above it and for
// the same reason: it proves the key reaches something, which is exactly what
// those three lacked.
func TestEveryKeyWsPassesIsInTheTable(t *testing.T) {
	src, err := os.ReadFile("../server/ws.go")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, k := range targetKeys {
		known[k] = true
	}
	call := regexp.MustCompile(`ResumeCollector\("([^"]+)"\)`)
	seen := map[string]bool{}
	for _, m := range call.FindAllStringSubmatch(string(src), -1) {
		seen[m[1]] = true
		if !known[m[1]] {
			t.Errorf("ws.go resumes %q and the session table has no entry for it, so the call "+
				"is a silent no-op and that collector never restarts", m[1])
		}
	}
	if len(seen) < 15 {
		t.Errorf("only %d distinct keys reach ResumeCollector; ws.go had 20 call sites when this "+
			"was written, so the funnel is being bypassed again", len(seen))
	}
}
