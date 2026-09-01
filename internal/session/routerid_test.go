package session

// Every collector that STAMPS a router id must be GIVEN this session's.
//
// ── WHY THIS IS A SOURCE CHECK ──────────────────────────────────────────────
//
// `topology:update` went out with no `routerId` for the life of this port. The
// field was declared, tagged `omitempty`, and never assigned — because
// `NewTopology` had no parameter to be told one through. Nothing caught it:
//
//   - the fixture differential compares against a Node golden, and the golden
//     had no `routerId` either, because `fixture-replay.js` passed the live
//     collector no `rid` and `JSON.stringify` drops undefined. Two silences
//     agreeing.
//   - `internal/collect`'s own tests construct the collector with a literal, so
//     they cannot see what the CALL SITE passes. A test that builds the object
//     itself is a seam that bypasses the path it stands in for.
//
// It was found by the live-socket-diff tool, watching what the two servers
// actually send. That tool needs a login, two running servers and a router, so
// it cannot be a gate. This can.
//
// Like `TestEveryCollectorHasAPathThatStartsIt` above it, this proves a path
// exists in the source rather than that it runs — which is exactly the thing
// that was missing.
//
// ── AND THE LIST CANNOT GO STALE ────────────────────────────────────────────
//
// The second half matters more than the first. A ledger naming two constructors
// would pass forever while a third collector was added, given a routerID
// parameter, and passed nothing. So the list is CHECKED AGAINST the collect
// package: any `New*` taking a `routerID` and not named here fails.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The constructors that accept a router id, and what the call site must hand
// them. `rec` is the router record the session was opened for, so `rec.ID` is
// the only correct answer — "" compiles and stamps every payload with nothing.
var stampsARouterID = []string{"NewIfStatus", "NewTopology"}

func TestEveryRouterIDStampingCollectorIsGivenTheSessionsRouter(t *testing.T) {
	src, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	for _, ctor := range stampsARouterID {
		// The call as written, up to the closing paren of its argument list.
		call := regexp.MustCompile(`collect\.` + ctor + `\(([^)]*)\)`)
		m := call.FindStringSubmatch(text)
		if m == nil {
			t.Errorf("%s is not called in session.go at all — if the collector was "+
				"removed, drop it from stampsARouterID; if it was renamed, rename it here", ctor)
			continue
		}
		args := strings.Split(m[1], ",")
		found := false
		for _, a := range args {
			if strings.TrimSpace(a) == "rec.ID" {
				found = true
			}
		}
		if !found {
			t.Errorf("collect.%s is called with (%s) — none of which is rec.ID.\n"+
				"Its payload carries a routerId, and a client uses it to decide whether an "+
				"update belongs to the router it is watching. An empty one is not a "+
				"harmless default: it is how topology:update shipped with no routerId.",
				ctor, strings.TrimSpace(m[1]))
		}
	}
}

// TestTheStampingListIsComplete — the direction that keeps this honest.
func TestTheStampingListIsComplete(t *testing.T) {
	files, err := filepath.Glob("../collect/*.go")
	if err != nil {
		t.Fatal(err)
	}
	// A constructor whose parameter list mentions routerID takes one, whatever
	// it goes on to do with it.
	decl := regexp.MustCompile(`func (New\w+)\([^)]*\brouterID\b`)

	listed := map[string]bool{}
	for _, c := range stampsARouterID {
		listed[c] = true
	}

	var missing []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(b), -1) {
			if !listed[m[1]] {
				missing = append(missing, m[1]+" ("+filepath.Base(f)+")")
			}
			delete(listed, m[1])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these collectors take a routerID and are not in stampsARouterID, so "+
			"nothing checks what the session passes them: %s", strings.Join(missing, ", "))
	}
	// AND THE OTHER WAY: a name in the list that no longer exists would make the
	// first test fail loudly, but only if it is still called. This catches the
	// constructor that lost its parameter and kept its name.
	for name := range listed {
		t.Errorf("%s is in stampsARouterID but no longer takes a routerID parameter — "+
			"remove it, or the list is describing code that is gone", name)
	}
}
