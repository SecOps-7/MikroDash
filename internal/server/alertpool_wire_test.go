package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The two pools are synced together.
//
// ── AN INVARIANT ACROSS EIGHT CALL SITES ──────────────────────────────────
//
// `syncPool` and `syncAlertPool` answer the same question — who is watching
// what — so a change that affects one affects the other. The overview pool
// excludes routers with an interactive session; the alert pool excludes those
// AND the overview pool's. Sync one without the other and the two disagree about
// who owns a router, which shows up as two connections to one device or none.
//
// This is the shape that has failed repeatedly in this port: an invariant
// enforced by remembering, across sites that grow. So it is counted rather than
// remembered.
func TestEverySyncPoolSiteAlsoSyncsTheAlertPool(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	callSite := regexp.MustCompile(`(?m)^(\s*)((?:cn\.srv|s|srv)\.)syncPool\(\)`)
	total := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range callSite.FindAllStringSubmatchIndex(src, -1) {
			total++
			// The twin must be the NEXT statement. Anywhere else in the file is
			// not the same thing: these run inside conditionals, and a twin
			// outside the branch syncs when the original did not.
			rest := src[m[1]:]
			line := rest
			if i := strings.Index(rest, "\n"); i >= 0 {
				line = rest[:i+1]
			}
			next := rest[len(line):]
			if i := strings.Index(next, "\n"); i >= 0 {
				next = next[:i]
			}
			if !strings.Contains(next, "syncAlertPool()") {
				t.Errorf("%s: a syncPool() call is not followed by syncAlertPool().\n"+
					"  next line: %q\n"+
					"The two pools divide the fleet between them; syncing one without the "+
					"other leaves them disagreeing about who owns a router.", f, strings.TrimSpace(next))
			}
		}
	}
	if total == 0 {
		t.Fatal("no syncPool() call sites found — this test is measuring nothing")
	}
	t.Logf("%d syncPool call site(s) checked", total)
}

// AND THE ALERT POOL IS SYNCED AT STARTUP, which the overview pool is not.
//
// `New` connects to nothing; `Sync` does. The overview pool can wait for
// `devicesFocus` because its rows are only wanted while that page is open. This
// one exists so a router nobody is watching is still known to be up and still
// has its alerts evaluated — a claim about the whole uptime of the process, not
// about a page.
func TestTheAlertPoolIsSyncedAtStartup(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(b), "")
	if !strings.Contains(code, "srv.syncAlertPool()") {
		t.Error("server.go never syncs the alert pool: it would connect to nothing until a " +
			"router was edited, so non-active routers read Offline and their alerts never fire")
	}
}
