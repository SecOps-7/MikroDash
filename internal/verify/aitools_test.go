package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/pages"
	"mikrodash/internal/resource"
)

// The tools an assistant may call, against the registry they are derived from.
//
// ── WHY THIS IS A LEDGER AND NOT A COUNT ────────────────────────────────────
//
// `cmd/toolgen` derives the catalogue from `resource.All()`, so the two cannot
// disagree at runtime — the generator would have to be broken for that. What can
// drift is the COMMITTED RECORD, and the record is the point: the set of tools a
// model can call is a security surface, and one that changes silently when
// somebody adds a page is one nobody reviews.
//
// So this compares the artefact against the live registry in both directions. A
// resource added with no tool fails. A tool naming a resource that has been
// deleted fails. And a tool whose owning page has changed shows up as a
// mismatch rather than as nothing at all.

type toolRecord struct {
	Name     string `json:"name"`
	Resource string `json:"resource"`
	Page     string `json:"page"`
}

func loadToolArtefact(t *testing.T) []toolRecord {
	t.Helper()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "testdata", "ai-tools.json"))
	if err != nil {
		t.Fatalf("no tool record — run: go run ./cmd/toolgen: %v", err)
	}
	var f struct {
		Tools []toolRecord `json:"tools"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Tools) == 0 {
		t.Fatal("the tool record is empty — this test would pass against nothing")
	}
	return f.Tools
}

// TestEveryResourceHasATool, and every recorded tool names a live resource.
func TestEveryResourceHasATool(t *testing.T) {
	recorded := map[string]toolRecord{}
	for _, r := range loadToolArtefact(t) {
		if _, dup := recorded[r.Resource]; dup {
			t.Errorf("two tools claim resource %q", r.Resource)
		}
		recorded[r.Resource] = r
	}

	live := map[string]*resource.Resource{}
	for _, r := range resource.All() {
		live[r.Key] = r
	}
	if len(live) < 20 {
		t.Fatalf("only %d resources — the registry is not being enumerated", len(live))
	}

	var missing, orphan []string
	for key := range live {
		if _, ok := recorded[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range recorded {
		if _, ok := live[key]; !ok {
			orphan = append(orphan, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphan)

	for _, k := range missing {
		t.Errorf("resource %q has no tool — the assistant cannot read a page the operator "+
			"can. Run: go run ./cmd/toolgen", k)
	}
	for _, k := range orphan {
		t.Errorf("a tool is recorded for resource %q, which no longer exists — the record "+
			"describes a surface that is not there. Run: go run ./cmd/toolgen", k)
	}
}

// TestEveryToolIsGatedByARealPage.
//
// ── A TOOL WITH NO PAGE WOULD BE UNGATED ────────────────────────────────────
//
// `aitools.Permitted` skips the permission check when a tool's page is empty,
// because data nobody may withhold is a real case elsewhere in this app. It is
// NOT a real case here: every tool reads a RouterOS menu that some page owns, so
// an empty page would advertise a tool to a viewer denied the page it reads
// from — the assistant becoming a way around the permission matrix, which is the
// one thing the design forbids.
//
// And a page that does not exist is worse than none: `canPage` would refuse it
// for everybody, so the tool would be invisible to every viewer including an
// administrator, silently.
func TestEveryToolIsGatedByARealPage(t *testing.T) {
	livePages := map[string]bool{}
	for _, k := range pages.Keys() {
		livePages[k] = true
	}
	for _, r := range loadToolArtefact(t) {
		if r.Page == "" {
			t.Errorf("tool %q has no owning page — it would be advertised to every viewer, "+
				"including one denied the page whose menu it reads", r.Name)
			continue
		}
		if !livePages[r.Page] {
			t.Errorf("tool %q is gated on page %q, which does not exist — canPage refuses it "+
				"for everybody, so the tool is invisible to every viewer", r.Name, r.Page)
		}
	}
}

// TestNoToolAdvertisesAMutation.
//
// The read-only boundary is structural — `internal/aitools` builds one `list_`
// tool per resource and nothing else — but structure is only as good as the
// thing that notices when it changes. A create, set, remove or action verb
// appearing here is the whole feature changing character, and it must not be
// possible to do that without this failing.
func TestNoToolAdvertisesAMutation(t *testing.T) {
	// Named rather than pattern-matched, so adding one is deliberate.
	forbidden := []string{"create_", "add_", "set_", "update_", "remove_", "delete_",
		"move_", "enable_", "disable_", "apply_", "run_", "exec_"}
	for _, r := range loadToolArtefact(t) {
		for _, bad := range forbidden {
			if strings.HasPrefix(r.Name, bad) {
				t.Errorf("tool %q advertises a mutation. This slice is read-only: a model "+
					"that can call it could change a router with no human in the loop", r.Name)
			}
		}
		if !strings.HasPrefix(r.Name, "list_") {
			t.Errorf("tool %q is not a list — every tool in this catalogue reads", r.Name)
		}
	}
}
