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
	Access   string `json:"access"`
	Resource string `json:"resource"`
	Page     string `json:"page"`
}

// writeTool is the ONE tool allowed to change anything. Named here rather than
// imported so this ledger does not agree with the package it checks by
// construction: if `aitools` renames its write tool, this fails and somebody
// decides, which is the entire point of recording it.
const writeTool = "change_row"

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
//
// ── THE WRITE TOOL IS EXEMPT, AND THE EXEMPTION IS COUNTED ──────────────────
//
// `change_row` is not bound to one menu: the model names the resource in its
// arguments. So it carries no `resource` and cannot be matched against the
// registry here. An unbounded "skip anything with no resource" would quietly
// absorb a read tool that had lost its key, so exactly one such tool is allowed
// and its name is checked.
func TestEveryResourceHasATool(t *testing.T) {
	recorded := map[string]toolRecord{}
	unbound := 0
	for _, r := range loadToolArtefact(t) {
		if r.Resource == "" {
			unbound++
			if r.Name != writeTool {
				t.Errorf("tool %q names no resource; only %q may", r.Name, writeTool)
			}
			continue
		}
		if _, dup := recorded[r.Resource]; dup {
			t.Errorf("two tools claim resource %q", r.Resource)
		}
		recorded[r.Resource] = r
	}
	if unbound != 1 {
		t.Errorf("%d tools carry no resource; exactly one (%q) should", unbound, writeTool)
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
	ungated := 0
	for _, r := range loadToolArtefact(t) {
		if r.Page == "" {
			// ── THE WRITE TOOL HAS NO SINGLE PAGE, AND IS NOT UNGATED ───────
			//
			// It spans every resource the viewer may change, so no one page
			// owns it. Its gate is the resource enum, which `Permitted` builds
			// from this viewer's write permissions, plus a per-call check of
			// whichever resource the model actually named.
			//
			// That reasoning is only safe for a tool that declares itself a
			// writer and is the one this ledger knows about. Anything else with
			// no page is the original failure: advertised to every viewer,
			// including one denied the page whose menu it reads.
			ungated++
			if r.Name != writeTool || r.Access != "write" {
				t.Errorf("tool %q has no owning page — it would be advertised to every viewer, "+
					"including one denied the page whose menu it reads", r.Name)
			}
			continue
		}
		if !livePages[r.Page] {
			t.Errorf("tool %q is gated on page %q, which does not exist — canPage refuses it "+
				"for everybody, so the tool is invisible to every viewer", r.Name, r.Page)
		}
	}
	if ungated != 1 {
		t.Errorf("%d tools carry no page; exactly one (%q) should", ungated, writeTool)
	}
}

// TestExactlyOneToolCanChangeAnything.
//
// ── THIS TEST USED TO SAY "NONE" ────────────────────────────────────────────
//
// It required every name to start `list_` and forbade a set of mutating verbs,
// because slice 3 advertised nothing that could write. Re-aimed deliberately for
// the write tool rather than deleted: the question it answers is still the one
// that matters, and it is now "how many, and which", not "none".
//
// A SECOND writer appearing — or a verb-shaped name, which is what an action
// tool would look like — is the feature changing character, and it must not be
// possible to do that without this failing.
func TestExactlyOneToolCanChangeAnything(t *testing.T) {
	// Named rather than pattern-matched, so adding one is deliberate. These are
	// the shapes a RouterOS ACTION tool would take, which is the thing
	// `internal/aitools` excludes by name: a verb the model chooses.
	forbidden := []string{"create_", "add_", "set_", "update_", "remove_", "delete_",
		"move_", "enable_", "disable_", "apply_", "run_", "exec_"}
	writers := []string{}
	for _, r := range loadToolArtefact(t) {
		for _, bad := range forbidden {
			if strings.HasPrefix(r.Name, bad) {
				t.Errorf("tool %q is shaped like a RouterOS action. Writes go through one "+
					"declared tool and the resource pipeline, never a verb the model picks", r.Name)
			}
		}
		switch r.Access {
		case "read":
			if !strings.HasPrefix(r.Name, "list_") {
				t.Errorf("tool %q declares read access but is not a list", r.Name)
			}
		case "write":
			writers = append(writers, r.Name)
		default:
			t.Errorf("tool %q declares access %q, which is neither read nor write", r.Name, r.Access)
		}
	}
	if len(writers) != 1 || writers[0] != writeTool {
		t.Errorf("the tools that can change a router are %v; exactly one (%q) should be able to",
			writers, writeTool)
	}
}
