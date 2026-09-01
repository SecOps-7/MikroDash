package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// modulesKnownUnreachable: TypeScript modules nothing imports, on purpose.
var modulesKnownUnreachable = map[string]string{}

var (
	importFrom   = regexp.MustCompile(`(?m)^\s*(?:import|export)\b[^;]*?from\s*'([^']+)'`)
	bareImport   = regexp.MustCompile(`(?m)^\s*import\s*'([^']+)'`)
	typeOnlyLine = regexp.MustCompile(`(?ms)^\s*(?:import|export)\s+type\s.*?from\s*'[^']+';`)
	declaresType = regexp.MustCompile(`(?m)^\s*export\s+(?:type|interface)\b`)
	declaresVal  = regexp.MustCompile(`(?m)^\s*export\s+(?:async\s+)?(?:function|const|let|var|class|enum|default)\b|^\s*export\s*\{`)
)

// TestEveryModuleIsReachable: every TypeScript module is reachable, by a RUNTIME
// import, from one of the build's entry points.
//
// An unreachable module is code that ships nowhere and runs never. It still
// type-checks, still passes review, and still looks maintained -- which is what
// makes it expensive: someone eventually edits it expecting an effect.
//
// ── TYPE-ONLY IMPORTS ARE NOT EDGES ─────────────────────────────────────────
//
// `import type { X } from './y'` is erased at build time and creates no runtime
// dependency, so following it would report a module as reachable when the bundler
// drops it entirely. Modules that declare ONLY types are exempt for the same
// reason: they are meant to vanish.
func TestEveryModuleIsReachable(t *testing.T) {
	root := repoRoot(t)
	srcDir := filepath.Join(root, "web", "src")

	src := map[string]string{}
	err := filepath.WalkDir(srcDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".ts") {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			src[p] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking web/src: %v", err)
	}
	if len(src) < 80 {
		t.Fatalf("only %d TypeScript modules found — the walk broke", len(src))
	}

	rel := func(p string) string {
		r, _ := filepath.Rel(srcDir, p)
		return filepath.ToSlash(r)
	}
	resolve := func(from, spec string) string {
		if !strings.HasPrefix(spec, ".") {
			return "" // a package, not ours
		}
		bare := strings.TrimSuffix(spec, ".js")
		p := filepath.Clean(filepath.Join(filepath.Dir(from), bare))
		for _, cand := range []string{p + ".ts", filepath.Join(p, "index.ts")} {
			if _, ok := src[cand]; ok {
				return cand
			}
		}
		return ""
	}

	edges := map[string][]string{}
	for f, text := range src {
		runtime := typeOnlyLine.ReplaceAllString(text, "")
		for _, re := range []*regexp.Regexp{importFrom, bareImport} {
			for _, m := range re.FindAllStringSubmatch(runtime, -1) {
				if to := resolve(f, m[1]); to != "" {
					edges[f] = append(edges[f], to)
				}
			}
		}
	}

	// The entry points are whatever the build actually bundles.
	build := mustRead(t, filepath.Join(root, "cmd", "webbuild", "main.go"))
	var entries []string
	for _, m := range regexp.MustCompile(`"(?:web/)?src/([A-Za-z0-9_/-]+\.ts)"`).
		FindAllStringSubmatch(build, -1) {
		if p := filepath.Join(srcDir, m[1]); src[p] != "" {
			entries = append(entries, p)
		}
	}
	if len(entries) == 0 {
		// Fall back to the known roots rather than silently declaring every
		// module unreachable.
		for _, name := range []string{"main.ts", "entry/login.ts", "entry/preflight.ts"} {
			if p := filepath.Join(srcDir, name); src[p] != "" {
				entries = append(entries, p)
			}
		}
	}
	if len(entries) == 0 {
		t.Fatal("no build entry points found — this test would call every module unreachable")
	}

	seen := map[string]bool{}
	var visit func(string)
	visit = func(f string) {
		if seen[f] {
			return
		}
		seen[f] = true
		for _, to := range edges[f] {
			visit(to)
		}
	}
	for _, e := range entries {
		visit(e)
	}

	var unreachable []string
	for f, text := range src {
		if seen[f] {
			continue
		}
		// A module that exports only types is erased by design.
		if declaresType.MatchString(text) && !declaresVal.MatchString(text) {
			continue
		}
		unreachable = append(unreachable, rel(f))
	}
	sort.Strings(unreachable)

	have := map[string]bool{}
	for _, m := range unreachable {
		have[m] = true
		if _, ok := modulesKnownUnreachable[m]; !ok {
			t.Errorf("%s is imported by nothing reachable from an entry point — it ships nowhere "+
				"and runs never", m)
		}
	}
	for m := range modulesKnownUnreachable {
		if !have[m] {
			t.Errorf("%s is recorded as unreachable but is reachable now — delete the entry", m)
		}
	}
	t.Logf("%d of %d modules reachable from the %d build entry point(s)",
		len(seen), len(src), len(entries))
}
