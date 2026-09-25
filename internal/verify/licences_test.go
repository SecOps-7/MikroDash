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

	start := strings.Index(src, "var webLibraries = []Dependency{")
	if start < 0 {
		t.Fatal("webLibraries is gone from internal/server/deps.go")
	}
	end := strings.Index(src[start:], "\n}")
	names := regexp.MustCompile(`\{Name: "([^"]+)"`).
		FindAllStringSubmatch(src[start:start+end], -1)
	if len(names) == 0 {
		t.Fatal("no vendored libraries parsed — this scan has broken")
	}

	for _, n := range names {
		if !strings.Contains(notices, "## "+n[1]) {
			t.Errorf("the About page credits %q and THIRD_PARTY_NOTICES.md has no "+
				"`## %s` section for it", n[1], n[1])
		}
	}
	t.Logf("%d vendored librar(ies) checked against the notices", len(names))
}
