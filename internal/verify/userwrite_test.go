package verify

// Every path that writes a RouterOS account runs the lockout guard.
//
// ── THE CASE THIS EXISTS FOR ────────────────────────────────────────────────
//
// `internal/server/dnsfleet_api.go` carries this in its header:
//
//	"THAT IS ONLY SAFE BECAUSE dnsStatic DECLARES NO GUARD. A guarded resource
//	would lose its guard down this path."
//
// It is exactly right, and it fails nothing. A second fleet endpoint written
// from that one as a model — for `/user`, which DOES declare `selfAccount` —
// would break MikroDash's own login on every router it touched, and the only
// thing standing in the way is that somebody reads the comment first.
//
// So the claim is made checkable: a file that writes to `/user` or
// `/user/group` must also name the guard.
//
// ── WHAT THIS DOES NOT CLAIM ────────────────────────────────────────────────
//
// It is a source scan, so it proves the guard is NAMED in the file, not that it
// is reached on every path through it. `internal/credprof`'s own
// `TestTheGuardRunsBeforeAnyWrite` checks the ORDER within that package, which
// is the stronger claim and the one that cannot be made from here. Both are
// weaker than a live write against a router, which is why CLAUDE.md calls live
// verification mandatory.

import (
	"path/filepath"
	"strings"
	"testing"
)

// userWriteVerbs are the commands that change an account or a group.
//
// `/user/active/remove` is deliberately NOT here: ending a session is not a row
// write, it is guarded by `CheckSession`, and including it would make this
// scan's subject two different things.
var userWriteVerbs = []string{
	`"/user/add"`, `"/user/set"`, `"/user/remove"`,
	`"/user/group/add"`, `"/user/group/set"`, `"/user/group/remove"`,
}

// guardMentions are the ways a file can show it consulted the guard.
var guardMentions = []string{
	"guard.CheckUser", "guard.CheckGroup", "guard.ResolveSelf",
	"selfAccountVerdict", "verdictFor", "portedGuards",
}

func TestEveryUserWritePathNamesTheGuard(t *testing.T) {
	root := repoRoot(t)

	files := readFiles(t, root, "internal/", func(rel string) bool {
		return strings.HasSuffix(rel, ".go") && !isTestSource(rel)
	})
	if len(files) < 50 {
		t.Fatalf("only %d source files read - the scan is looking at nothing, and would "+
			"report every rule satisfied", len(files))
	}

	writers, guarded := 0, 0
	for rel, body := range files {
		// THE GUARD PACKAGE ITSELF IS EXEMPT, and so is the resource registry:
		// one IS the guard, the other only declares menu strings. Without this
		// the scan would report the guard as an unguarded writer.
		if strings.HasPrefix(rel, "internal/guard/") ||
			strings.HasPrefix(rel, "internal/resource/") {
			continue
		}
		code := stripGoComments(body)
		verb := ""
		for _, v := range userWriteVerbs {
			if strings.Contains(code, v) {
				verb = v
				break
			}
		}
		if verb == "" {
			continue
		}
		writers++
		named := false
		for _, g := range guardMentions {
			if strings.Contains(code, g) {
				named = true
				break
			}
		}
		if !named {
			t.Errorf("%s writes to a RouterOS account (%s) and never names the lockout "+
				"guard.\n    internal/guard/selfguard.go: \"unlike every other guard here, "+
				"that one is unrecoverable from inside the app. Once the login is broken, "+
				"the fix is WinBox.\"", rel, verb)
			continue
		}
		guarded++
	}

	// ── THE CONTROL: THE SCAN CAN SEE A WRITER ──────────────────────────────
	//
	// Without this the test passes just as well on a pattern that matches
	// nothing, which is how a source scan quietly stops checking anything.
	if writers < 1 {
		t.Fatalf("the scan found %d file(s) writing to /user; it should find at least the "+
			"credential profile applier. The patterns have stopped matching.", writers)
	}
	t.Logf("%d of %d /user writers name the guard", guarded, writers)
}

// TestTheGuardScanCanStillSeeAKnownWriter is the companion the anchor rule
// asks for: a ledger needs a test proving it can still find a line it knows
// about, or a pattern that stopped matching reads as a clean sweep.
func TestTheGuardScanCanStillSeeAKnownWriter(t *testing.T) {
	root := repoRoot(t)
	code := stripGoComments(mustRead(t,
		filepath.Join(root, "internal", "credprof", "credprof.go")))

	seen := 0
	for _, v := range userWriteVerbs {
		if strings.Contains(code, v) {
			seen++
		}
	}
	if seen == 0 {
		t.Error("internal/credprof/credprof.go matches none of the write patterns, so the " +
			"scan above would report every file clean whatever it contained")
	}
	if !strings.Contains(code, "guard.CheckUser") {
		t.Error("internal/credprof/credprof.go names no guard, which the scan above should " +
			"have failed on")
	}

	// AND THE STRIPPER IS DOING SOMETHING. The prose at the top of this file
	// quotes several of the patterns; unstripped, this file would count as a
	// writer that names no guard, and the scan would be reading itself.
	self := mustRead(t, filepath.Join(root, "internal", "verify", "userwrite_test.go"))
	if len(stripGoComments(self)) >= len(self) {
		t.Error("stripGoComments removed nothing from this file, so every scan in this " +
			"package can match its own explanation")
	}
}
