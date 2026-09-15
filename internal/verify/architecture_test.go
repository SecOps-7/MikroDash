package verify

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/session"
)

// Collector-Architecture.md is a LIVING document, and this is what makes that
// true rather than aspirational.
//
// ── WHY A DOCUMENT NEEDS A GATE AT ALL ─────────────────────────────────────
//
// CLAUDE.md names this repository's most expensive recurring defect: "a blocker
// that has been closed reads exactly like one that never was, and nothing fails
// when a premise expires". Every working document in this tree has gone stale at
// least once, and each time it was believed for a while first — a session read a
// closed exemption in `sharedmenu_test.go`, believed it, and argued a
// double-parse objection that had been measured wrong months earlier.
//
// So the architecture document states its numbers and its collector list in a
// machine-readable shape, and this re-computes them. It fails in BOTH
// directions, which is the rule every ledger here follows: a claim that has gone
// stale is a failure, and so is a collector that exists and is undescribed.

const archDoc = "Collector-Architecture.md"

// archTable pulls the "Every collector" table's first column.
var archRowRe = regexp.MustCompile("(?m)^\\| `(\\w+)` \\|")

// archFactRe pulls one row of the "Measured facts" table.
var archFactRe = regexp.MustCompile(`(?m)^\| ([^|]+?) \| (\d+) \|`)

func archSource(t *testing.T) string {
	t.Helper()
	return mustRead(t, filepath.Join(repoRoot(t), archDoc))
}

// sectionOf returns the text under one `## ` heading.
func sectionOf(t *testing.T, src, heading string) string {
	t.Helper()
	i := strings.Index(src, "\n## "+heading)
	if i < 0 {
		t.Fatalf("%s has no %q section — the document has been restructured and this "+
			"gate is reading the wrong thing", archDoc, heading)
	}
	rest := src[i+1:]
	if j := strings.Index(rest[1:], "\n## "); j >= 0 {
		rest = rest[:j+1]
	}
	return rest
}

// TestTheArchitectureDocumentNamesEveryCollector.
//
// Both directions. A collector missing from the table is one somebody added
// without describing; a row naming a collector that no longer exists is the
// document describing a shape the code has left.
func TestTheArchitectureDocumentNamesEveryCollector(t *testing.T) {
	table := sectionOf(t, archSource(t), "Every collector")

	documented := map[string]bool{}
	for _, m := range archRowRe.FindAllStringSubmatch(table, -1) {
		documented[m[1]] = true
	}
	// A BELIEVABILITY FLOOR: a table that stopped matching would let every
	// assertion below pass over an empty set, which is this gate's own version
	// of the defect it exists to catch.
	if len(documented) < 20 {
		t.Fatalf("only %d collectors were read out of the table in %s; the row shape has "+
			"changed and this gate is measuring nothing", len(documented), archDoc)
	}

	real := map[string]bool{}
	for _, key := range registryKeys(t) {
		real[key] = true
	}

	var undocumented, imaginary []string
	for key := range real {
		if !documented[key] {
			undocumented = append(undocumented, key)
		}
	}
	for key := range documented {
		if !real[key] {
			imaginary = append(imaginary, key)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(imaginary)

	if len(undocumented) > 0 {
		t.Errorf("%v exist and are not in %s. Add a row: acquisition, derivation and "+
			"views, which are the three things somebody reading it needs.",
			undocumented, archDoc)
	}
	if len(imaginary) > 0 {
		t.Errorf("%s describes %v, which the registry does not have. A document "+
			"describing a shape the code has left is worse than no document.",
			archDoc, imaginary)
	}
}

// TestTheArchitectureDocumentsFactsAreTrue re-computes every number in the
// "Measured facts" table.
//
// THE MEASUREMENTS ARE HERE, not in the document, which is the whole point: the
// document states a value and this decides whether it is one.
func TestTheArchitectureDocumentsFactsAreTrue(t *testing.T) {
	facts := sectionOf(t, archSource(t), "Measured facts")

	claimed := map[string]int{}
	for _, m := range archFactRe.FindAllStringSubmatch(facts, -1) {
		name := strings.TrimSpace(m[1])
		if name == "fact" || strings.HasPrefix(name, "-") {
			continue // the header and the separator row
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		claimed[name] = n
	}

	actual := map[string]int{
		"registry rows":                           len(registryKeys(t)),
		"collectors with a Go implementation":     len(collectorFilesWithALifecycle(t)),
		"suspendable when idle (`disableable`)":   registryCount(t, func(c archCollector) bool { return c.Disableable }),
		"dormancy-eligible":                       registryCount(t, func(c archCollector) bool { return len(c.EmptyKey) > 0 }),
		"gated by demand (`session.TargetKeys`)":  len(session.TargetKeys()),
		"menus enabled for stream delivery":       countIn(t, "internal/session/streammenus.go", `(?m)^\s*"(/[a-z0-9/-]+)":\s*"`),
		"collectors declaring rooms":              len(collect.DeclaredRoomKeys()),
		"`keepAliveFor` entries":                  countIn(t, "internal/collect/rooms.go", `(?m)^\t"(\w+)":|^\t"(\w+)": union`),
		"collectors with an extracted derivation": derivationCount(t),
	}

	// EVERY FACT MUST BE CLAIMED, or the table can shrink to the ones that pass.
	for name := range actual {
		if _, ok := claimed[name]; !ok {
			t.Errorf("%s's Measured facts table has no row for %q. A fact this gate "+
				"computes and the document omits is a number nobody is holding to "+
				"anything.", archDoc, name)
		}
	}
	for name, want := range claimed {
		got, known := actual[name]
		if !known {
			t.Errorf("%s claims %q = %d and nothing here measures it. Either add the "+
				"measurement or drop the row: an unverified number in a gated document "+
				"is worse than one in an ungated one, because it borrows the trust.",
				archDoc, name, want)
			continue
		}
		if got != want {
			t.Errorf("%s says %q is %d; it is %d.", archDoc, name, want, got)
		}
	}
}

type archCollector struct {
	Key         string `json:"key"`
	Disableable bool   `json:"disableable"`
	EmptyKey    []any  `json:"emptyKey"`
}

func archRegistry(t *testing.T) []archCollector {
	t.Helper()
	b := mustRead(t, filepath.Join(repoRoot(t), "internal", "collection", "collection_tables.json"))
	var doc struct {
		Registry []archCollector `json:"registry"`
	}
	if err := json.Unmarshal([]byte(b), &doc); err != nil {
		t.Fatalf("the registry will not parse: %v", err)
	}
	if len(doc.Registry) == 0 {
		t.Fatal("the registry is empty, so every count below would be zero and pass nothing")
	}
	return doc.Registry
}

func registryKeys(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range archRegistry(t) {
		out = append(out, c.Key)
	}
	return out
}

func registryCount(t *testing.T, pred func(archCollector) bool) int {
	t.Helper()
	n := 0
	for _, c := range archRegistry(t) {
		if pred(c) {
			n++
		}
	}
	return n
}

// collectorFilesWithALifecycle counts the files that implement a collector.
//
// `Start` OR `Resume`: `packages` and `routing` are page-gated and declared no
// Start at all, which is the same omission that hid them from the derivations
// ledger until 2026-09-10.
//
// OR THE TABLE CORE EMBEDDED: a table collector's lifecycle is promoted from
// `tableCore` (internal/collect/table.go), so its file declares none of those
// methods. table.go itself declares them on a lowercase type and is not counted.
func collectorFilesWithALifecycle(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "collect")
	re := regexp.MustCompile(`(?m)^func \([a-z]+ \*[A-Z]\w*\) (?:Start|Resume)\(\)|^\ttableCore\[\w+\]$`)
	var out []string
	for _, name := range collectGoFiles(t, dir) {
		if re.MatchString(mustRead(t, filepath.Join(dir, name))) {
			out = append(out, name)
		}
	}
	return out
}

func countIn(t *testing.T, rel, pattern string) int {
	t.Helper()
	src := mustRead(t, filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	return len(regexp.MustCompile(pattern).FindAllString(stripGoComments(src), -1))
}

// derivationCount reads the derivations ledger: an entry with a non-empty value
// is a collector whose rows-in/payload-out half is extracted.
func derivationCount(t *testing.T) int {
	src := mustRead(t, filepath.Join(repoRoot(t), "internal", "verify", "derivations_test.go"))
	// Only the ledger's own literal, which is the map this file declares.
	i := strings.Index(src, "ledger := map[string]string{")
	if i < 0 {
		t.Fatal("the derivations ledger has moved; this count is measuring nothing")
	}
	body := src[i:]
	if j := strings.Index(body, "\n\t}"); j > 0 {
		body = body[:j]
	}
	n := 0
	for _, m := range regexp.MustCompile(`"[\w.]+\.go":\s*"([^"]*)"`).FindAllStringSubmatch(body, -1) {
		if m[1] != "" {
			n++
		}
	}
	return n
}

// TestTheArchitectureDocumentDescribesThreeLayers.
//
// The document's PURPOSE is to explain the three layers and how they fit
// together, so the gate holds it to that rather than only to its numbers: a
// version that decayed into a collector list with no explanation would pass
// every count above.
func TestTheArchitectureDocumentDescribesThreeLayers(t *testing.T) {
	src := archSource(t)
	for _, heading := range []string{
		"Layer 1 — Acquisition",
		"Layer 2 — Derivation",
		"Layer 3 — Views",
		"How the three fit together",
	} {
		if !strings.Contains(src, "## "+heading) {
			t.Errorf("%s has no %q section. The document exists to explain the three "+
				"layers and how they fit together; a table of collectors without that "+
				"is a reference, not an architecture.", archDoc, heading)
		}
	}
	// And each layer must say what its job IS, not merely be present.
	for _, layer := range []string{"Acquisition", "Derivation", "Views"} {
		if !strings.Contains(src, "**Its job:") {
			t.Fatalf("no layer states its job in %s", archDoc)
		}
		_ = layer
	}
	if n := strings.Count(src, "**Its job:"); n != 3 {
		t.Errorf("%d layers state their job in %s, want 3 — every layer must say what "+
			"it is for, because that is the question the document is answering",
			n, archDoc)
	}
}
