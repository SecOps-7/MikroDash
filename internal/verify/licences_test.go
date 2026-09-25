package verify

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// EVERY MODULE IN THE BINARY HAS A RECORDED LICENCE, AND EVERY RECORD NAMES A
// REAL MODULE.
//
// ── WHY THIS IS A LEDGER AND NOT A ROUND TRIP ──────────────────────────────
//
// `Dependencies()` joins the build's own module list to a hand-written licence
// table. The join always succeeds: a module with no entry gets the zero value,
// which is an empty licence, and the About page renders a dependency with a
// blank column. Nothing errors, so only a check that reads BOTH lists notices.
//
// The reverse is quieter and worse. An entry for a module that is no longer
// linked reads as a considered attribution for something that is not shipped,
// and the next person adds to the list rather than auditing it.
//
// ── IT ASKS THE TOOLCHAIN, WHICH IS THE ONLY HONEST SOURCE ─────────────────
//
// `go list -deps ./cmd/mikrodash` is what the linker will actually pull in.
// Reading `go.mod` instead would be wrong in both directions: it names indirect
// requirements that never reach the binary, and it omits nothing only by
// accident.
func linkedModules(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("go", "list", "-deps",
		"-f", "{{if .Module}}{{.Module.Path}} {{.Module.Version}}{{end}}", "./cmd/mikrodash")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// NOT A SKIP THAT HIDES. The suite runs in the Go container, where this
		// always works; a failure here means the layout moved, and a silent
		// skip would leave the licence table unchecked for ever.
		t.Fatalf("go list -deps: %v — this ledger cannot read what the binary links", err)
	}
	mods := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "mikrodash") {
			continue
		}
		v := ""
		if len(f) > 1 {
			v = f[1]
		}
		mods[f[0]] = v
	}
	if len(mods) < 5 {
		t.Fatalf("only %d modules found — this scan has broken, and an empty result "+
			"agrees with every assertion below", len(mods))
	}
	return mods
}

// reLicenceEntry reads the table in internal/server/deps.go. The table is Go
// source rather than data because it is joined in Go; reading the source is what
// keeps this ledger from being a second copy of the same list.
var reLicenceEntry = regexp.MustCompile(`"([\w./-]+)":\s+"([^"]+)",`)

func recordedLicences(t *testing.T) map[string]string {
	t.Helper()
	src := mustRead(t, filepath.Join(repoRoot(t), "internal", "server", "deps.go"))
	start := strings.Index(src, "var moduleLicences = map[string]string{")
	if start < 0 {
		t.Fatal("moduleLicences is gone from internal/server/deps.go — this ledger " +
			"reads that table and has nothing to check")
	}
	end := strings.Index(src[start:], "\n}")
	out := map[string]string{}
	for _, m := range reLicenceEntry.FindAllStringSubmatch(src[start:start+end], -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatal("no licence entries parsed — this scan has broken")
	}
	return out
}

func TestEveryLinkedModuleHasARecordedLicence(t *testing.T) {
	linked := linkedModules(t)
	recorded := recordedLicences(t)

	var missing, stale []string
	for m := range linked {
		if recorded[m] == "" {
			missing = append(missing, m)
		}
	}
	for m := range recorded {
		if _, ok := linked[m]; !ok {
			stale = append(stale, m)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("%v are linked into the binary and have no recorded licence, so the "+
			"About page lists them with a blank column. Read the licence from the "+
			"module's OWN LICENSE file in the module cache, not from memory, and add "+
			"it to `moduleLicences`", missing)
	}
	if len(stale) > 0 {
		t.Errorf("%v have a recorded licence and are NOT in the binary — an attribution "+
			"for something no longer shipped, which reads as considered and is not", stale)
	}
	t.Logf("%d linked module(s), %d recorded licence(s)", len(linked), len(recorded))
}

// THE NOTICES FILE AND THE TABLE AGREE ABOUT THE VENDORED WEB LIBRARIES.
//
// `webLibraries` is hand-maintained because nothing in the build reports what
// the browser loads. `THIRD_PARTY_NOTICES.md` is the file that legally has to be
// right, so it is the authority: every library the About page credits must have
// a section there, or the page is crediting something the notices do not.
func TestEveryVendoredLibraryIsInTheNotices(t *testing.T) {
	root := repoRoot(t)
	notices := mustRead(t, filepath.Join(root, "THIRD_PARTY_NOTICES.md"))
	src := mustRead(t, filepath.Join(root, "internal", "server", "deps.go"))

	// BOTH LISTS. The geo databases moved out of `webLibraries` into their own
	// var when they were split into two rows, and a scan that still read only
	// the first would have stopped checking them the moment they moved -
	// silently, because the remaining entries all still passed.
	var names [][]string
	for _, decl := range []string{
		"var webLibraries = []Dependency{",
		"var geoLibraries = []Dependency{",
	} {
		start := strings.Index(src, decl)
		if start < 0 {
			t.Fatalf("%s is gone from internal/server/deps.go", decl)
		}
		end := strings.Index(src[start:], "\n}")
		found := regexp.MustCompile(`\{Name: "([^"]+)"`).
			FindAllStringSubmatch(src[start:start+end], -1)
		if len(found) == 0 {
			t.Fatalf("no libraries parsed from %s - this scan has broken", decl)
		}
		names = append(names, found...)
	}

	for _, n := range names {
		if !strings.Contains(notices, "## "+n[1]) {
			t.Errorf("the About page credits %q and THIRD_PARTY_NOTICES.md has no "+
				"`## %s` section for it", n[1], n[1])
		}
	}
	t.Logf("%d vendored librar(ies) checked against the notices", len(names))
}

// EVERY DIRECT REQUIREMENT IS RECORDED AS DIRECT, AND EVERY RECORD IS ONE.
//
// ── THE BUG THIS EXISTS FOR ────────────────────────────────────────────────
//
// The About page lists what MikroDash depends on, and it gets that list by
// filtering the build's module graph through `directModules`. A module missing
// from that map is simply not shown - no error, no blank row, nothing. A new
// dependency would be argued for in CLAUDE.md, linked into the binary, credited
// in the notices, and invisible on the page that exists to name it.
//
// The reverse is the one that rots: an entry for something no longer required
// costs nothing at runtime, because the join just never matches, so it sits
// there reading like a decision.
//
// ── GO.MOD IS THE RIGHT SOURCE HERE, UNLIKE ABOVE ──────────────────────────
//
// `linkedModules` deliberately refuses to read go.mod, because "what the binary
// links" is a question only the toolchain can answer. This is the OTHER
// question - "what did this project ask for" - and go.mod's first require block
// is its literal definition. `go list -m` would answer it too, but it resolves
// the whole graph to do so, and the file states it directly.
func TestEveryDirectRequirementIsRecordedAsDirect(t *testing.T) {
	root := repoRoot(t)
	src := mustRead(t, filepath.Join(root, "go.mod"))

	// The require blocks, in order. The FIRST is the direct one: `go mod tidy`
	// writes direct requirements into one block and indirect into another, and
	// marks every line of the second `// indirect`. Both facts are checked
	// below rather than assumed, because relying on either alone would let a
	// reformatted file pass while meaning something else.
	inBlock := false
	direct := map[string]bool{}
	indirect := 0
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "require (" {
			inBlock = true
			continue
		}
		if inBlock && trimmed == ")" {
			inBlock = false
			continue
		}
		if !inBlock || trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(trimmed, "// indirect") {
			indirect++
			continue
		}
		direct[strings.Fields(trimmed)[0]] = true
	}

	// AN EMPTY SCAN IS A BROKEN SCAN, and an empty `direct` would agree that
	// every recorded module is spurious while reporting nothing missing.
	if len(direct) < 3 || indirect < 3 {
		t.Fatalf("parsed %d direct and %d indirect requirements from go.mod - "+
			"this scan has broken", len(direct), indirect)
	}

	recorded := recordedDirect(t, root)
	var missing, spurious []string
	for path := range direct {
		if !recorded[path] {
			missing = append(missing, path)
		}
	}
	for path := range recorded {
		if !direct[path] {
			spurious = append(spurious, path)
		}
	}
	sort.Strings(missing)
	sort.Strings(spurious)

	if len(missing) > 0 {
		t.Errorf("go.mod requires %v directly, and `directModules` in "+
			"internal/server/deps.go does not list them - so they are filtered out of "+
			"Dependencies() and never appear on the About page, silently", missing)
	}
	if len(spurious) > 0 {
		t.Errorf("`directModules` lists %v, which go.mod no longer requires directly - "+
			"a stale entry matches nothing and reads like a decision", spurious)
	}
	t.Logf("%d direct requirement(s), %d indirect, all accounted for", len(direct), indirect)
}

// reDirectEntry reads the keys of `directModules`. The map is quoted-path to
// bool, one per line, which is how gofmt keeps it.
var reDirectEntry = regexp.MustCompile(`^\s*"([^"]+)":\s*true,\s*$`)

func recordedDirect(t *testing.T, root string) map[string]bool {
	t.Helper()
	src := mustRead(t, filepath.Join(root, "internal", "server", "deps.go"))
	_, rest, ok := strings.Cut(src, "var directModules = map[string]bool{")
	if !ok {
		t.Fatal("`directModules` is not in internal/server/deps.go in the shape this " +
			"ledger reads - it was renamed or restructured, and a ledger that cannot " +
			"find its subject must say so rather than pass")
	}
	body, _, _ := strings.Cut(rest, "}")
	out := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if m := reDirectEntry.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("read zero entries from `directModules` - an empty result agrees " +
			"with every assertion")
	}
	return out
}
