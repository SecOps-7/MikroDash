// Package verify holds the repository's static self-checks.
//
// ── WHAT THIS PACKAGE IS, AND WHY IT IS ONLY TESTS ──────────────────────────
//
// These were 171 JavaScript gates under `tools/`, run by `tools/verify.sh`. The
// large majority of them asserted one thing: that the port still reproduced a
// frozen recording of the Node implementation it replaced. That question stopped
// being worth asking when the port ended -- this is the product now, not a
// reproduction of one, and a gate that fails whenever the rendering changes taxes
// every deliberate change.
//
// The ones that moved here asked a DIFFERENT question. They read the CURRENT
// source and assert a property that is still true and still worth holding, with
// no reference to what Node did. Three of them caught real defects in the weeks
// before this migration, which is why they were relocated rather than deleted.
//
// There is deliberately no non-test file in this package. A check is not code the
// binary should be able to link, and keeping it `_test.go`-only makes that
// structural rather than a convention someone has to remember.
//
// ── SCANNING TYPESCRIPT AS TEXT ─────────────────────────────────────────────
//
// Several of these read `web/src/**/*.ts` with regexes rather than a parser. That
// is not a shortcut taken in the move: the JavaScript originals did exactly the
// same thing, in a language that HAS the TypeScript compiler available. Where a
// check genuinely needed real semantic resolution -- `orphan-check`, which
// distinguishes a read from a read through a shadowing local -- it stayed in
// JavaScript under `web/test/` rather than being ported to a weaker heuristic
// here. Knowing which of those two a check needs is the whole reason the split
// runs along mechanism instead of filename.
package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the repository root from this package's directory.
//
// `go test` always runs a test with its own package directory as the working
// directory, so the two levels up are deterministic. It is still CHECKED rather
// than assumed: a silently wrong root would make every scan below read an empty
// tree and pass, which is the exact shape of vacuous success these checks exist
// to prevent.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod — this package moved and the ..\\.. is now wrong", root)
	}
	return root
}

// pruned are directories no check reads: version control, dependencies, build
// output and local scratch. Everything else in the working tree is fair game.
var pruned = map[string]bool{
	".git": true, "node_modules": true, ".screenshots": true,
	"dist": true, ".test-out": true,
}

// tracked walks the working tree and returns every file, relative and
// slash-separated.
//
// ── WHY A WALK AND NOT `git ls-files` ───────────────────────────────────────
//
// The JavaScript original asked git for the tracked set, which was right for it:
// it ran on the host, where git exists. These tests run inside
// `golang:1.25-alpine`, which has NO git, while CI runs them on a runner that
// does. Asking git would mean the same check reading a different set of files in
// the two places -- and a check that disagrees with CI about what it looked at is
// worse than no check.
//
// Walking is also the safe direction for the one check where the difference
// matters. `TestNoCommittedCredential` scans a SUPERSET of the tracked files
// this way: an untracked local file with a real token in it is still worth
// hearing about, even though it is not yet the exposure vector.
func tracked(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if pruned[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatal("the walk returned nothing — the scan is broken, not the tree")
	}
	return files
}

// readFiles returns the contents of every tracked file under `dir` whose name
// passes `keep`. A directory that matches nothing is a FAILURE, not an empty
// result: every caller here is asserting something about a body of code, and a
// scan that silently found no code would pass by looking at nothing.
func readFiles(t *testing.T, root, dir string, keep func(string) bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, rel := range tracked(t, root) {
		if dir != "" && !strings.HasPrefix(rel, dir) {
			continue
		}
		if !keep(rel) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		out[rel] = string(b)
	}
	if len(out) == 0 {
		t.Fatalf("no files matched under %q — the scan stopped seeing its subject", dir)
	}
	return out
}

func hasExt(rel string, exts ...string) bool {
	for _, e := range exts {
		if strings.HasSuffix(rel, e) {
			return true
		}
	}
	return false
}
