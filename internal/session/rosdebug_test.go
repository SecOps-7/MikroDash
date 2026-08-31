package session

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/store"
)

// "RouterOS debug" MUST REACH THE CLIENT.
//
// It was rendered, validated and persisted, and read by nobody — the same shape
// as `topN` and the three retention settings, and found by the audit written
// after those (`tools/settings-consumer-audit.js`).
//
// Two claims, tested separately, because the second is the one that was wrong:
// the value resolves correctly, AND the dial config is actually given it.
func TestRosDebugIsRead(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  store.Settings
		want bool
	}{
		{"on", store.Settings{"rosDebug": true}, true},
		{"off", store.Settings{"rosDebug": false}, false},
		{"absent", store.Settings{}, false},
		{"nil settings", nil, false},
		// A DELIBERATE DIVERGENCE, and the only one. Live consumes this as
		// `if (this.cfg.debug)`, so the four characters "false" would turn
		// tracing ON there — the `dd6173b` class. The validated write path types
		// this as a boolean, so the difference is reachable only through a
		// hand-edited file, and the safe direction is the one the person who
		// typed "false" meant.
		{`the string "false" does not enable it`, store.Settings{"rosDebug": "false"}, false},
		{`the string "true" does not either`, store.Settings{"rosDebug": "true"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := rosDebugOn(c.cfg); got != c.want {
				t.Errorf("rosDebugOn(%v) = %v, want %v", c.cfg, got, c.want)
			}
		})
	}
}

// AND THE DIAL CONFIG IS GIVEN IT — the call site, which is where the defect was.
//
// `rosDebugOn` returning true proves nothing about whether anything asks. The
// setting sat unread for the life of the port with a perfectly good `Debug`
// field available on the config the whole time.
func TestTheSessionPassesRosDebugToTheClient(t *testing.T) {
	src, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`Debug:\s*rosDebugOn\(cfgSettings\)`).Match(src) {
		t.Error("the session's routeros.Config is not given rosDebugOn(cfgSettings). " +
			"An operator ticking \"RouterOS debug\" then gets no tracing, which is the " +
			"state this was written to fix.")
	}
}

// ONE DIAL SITE IN THE WHOLE TREE, matching live.
//
// Live has five `new ROS(` call sites and sets `debug` on exactly one — the
// page-serving session at `src/index.js:444`. The alert sessions, the overview
// sessions and the connection test are untraced there, so the pools here are
// untraced too: tracing them would produce output the live app never produces,
// continuously, from routers nobody is looking at.
//
// SCANNED ACROSS PACKAGES, not just this file. A first version read `session.go`
// alone, which would have passed with `internal/routers/pool.go` and
// `internal/alertpool/pool.go` each quietly enabling it — and those two are
// exactly the sites where the cost would be continuous rather than per-page.
func TestOnlyOneDialSiteEnablesTracing(t *testing.T) {
	roots := []string{"..", "../../cmd"}
	seen := map[string]int{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			// `Debug:` inside a routeros.Config literal. The field exists only
			// there, so naming it is the same as building a traced client.
			if n := len(regexp.MustCompile(`\bDebug:\s*\S`).FindAll(b, -1)); n > 0 {
				seen[filepath.Clean(path)] += n
			}
			return nil
		})
	}
	total := 0
	for _, n := range seen {
		total += n
	}
	if total != 1 {
		t.Errorf("%d dial site(s) set Debug, want exactly 1 (the page-serving session): %v",
			total, seen)
	}
}
