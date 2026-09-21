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
	Name       string `json:"name"`
	Access     string `json:"access"`
	Resource   string `json:"resource"`
	Collector  string `json:"collector"`
	Diagnostic string `json:"diagnostic"`
	Page       string `json:"page"`
}

// writeTool is the ONE tool allowed to change anything. Named here rather than
// imported so this ledger does not agree with the package it checks by
// construction: if `aitools` renames its write tool, this fails and somebody
// decides, which is the entire point of recording it.
const (
	writeTool  = "change_row"
	actionTool = "run_action"
)

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
// ── THE WRITERS ARE EXEMPT, AND THE EXEMPTION IS COUNTED ────────────────────
//
// `change_row` is not bound to one menu: the model names the resource in its
// arguments. `run_action` names a declared ACTION rather than a menu at all. So
// neither carries a `resource` and neither can be matched against the registry
// here. An unbounded "skip anything with no resource" would quietly absorb a
// read tool that had lost its key, so exactly these two are allowed and their
// names are checked.
// diagnosticPages is each diagnostic tool gated on a page other than Tools,
// and that page.
// read_file (2026-09-21) reads a file: a read of the Files page.
var diagnosticPages = map[string]string{"security_scan": "security-scan", "read_file": "files"}

func TestEveryResourceHasATool(t *testing.T) {
	recorded := map[string]toolRecord{}
	unbound := 0
	liveTools, diagTools := 0, 0
	for _, r := range loadToolArtefact(t) {
		// ── A LIVE TOOL NAMES A COLLECTOR INSTEAD OF A RESOURCE ─────────────
		//
		// It reads measurements (per-interface throughput) that are not rows of
		// any menu the registry declares. It is not an unbound tool: it has a
		// source, just a different kind, and it must be a READ tool gated on a
		// page. internal/server's TestEveryLiveToolHasAReader holds each one to
		// the code that answers it, in both directions.
		if r.Collector != "" {
			liveTools++
			if r.Resource != "" {
				t.Errorf("tool %q names both a resource and a collector", r.Name)
			}
			if r.Access != "read" || r.Page == "" {
				t.Errorf("live tool %q must be a read tool gated on a page, got access %q page %q",
					r.Name, r.Access, r.Page)
			}
			continue
		}
		// ── A DIAGNOSTIC TOOL RUNS A TOOLS PAGE DIAGNOSTIC ──────────────────
		//
		// Slice 8. It reads no menu: it probes from the router, gated on the
		// Tools page with the access the page itself needs. internal/server's
		// TestEveryDiagnosticToolHasARunner holds each one to its runner.
		if r.Diagnostic != "" {
			diagTools++
			if r.Resource != "" || r.Collector != "" {
				t.Errorf("diagnostic tool %q also names a resource or a collector", r.Name)
			}
			// Re-aimed 2026-09-19 for `security_scan`, which runs the Security
			// Scan page's check: a diagnostic is gated on the page whose check it
			// runs, pinned per tool so a new one must say which.
			want := "tools"
			if p, ok := diagnosticPages[r.Name]; ok {
				want = p
			}
			if r.Page != want {
				t.Errorf("diagnostic tool %q is gated on page %q, not %s", r.Name, r.Page, want)
			}
			continue
		}
		if r.Resource == "" {
			unbound++
			if r.Name != writeTool && r.Name != actionTool {
				t.Errorf("tool %q names no resource; only %q and %q may",
					r.Name, writeTool, actionTool)
			}
			continue
		}
		if _, dup := recorded[r.Resource]; dup {
			t.Errorf("two tools claim resource %q", r.Resource)
		}
		recorded[r.Resource] = r
	}
	if unbound != 2 {
		t.Errorf("%d tools carry no resource; exactly two (%q and %q) should",
			unbound, writeTool, actionTool)
	}
	if liveTools == 0 {
		t.Error("no live tools are recorded; list_interface_traffic has gone, or the record " +
			"no longer carries `collector`")
	}
	if diagTools == 0 {
		t.Error("no diagnostic tools are recorded; ping has gone, or the record no longer " +
			"carries `diagnostic`")
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
			// `run_action` is the same shape: its ACTION enum is built from the
			// pages this viewer may write, and the named action's page is
			// checked again per call and again at approval.
			//
			// That reasoning is only safe for a tool that declares itself a
			// writer and is the one this ledger knows about. Anything else with
			// no page is the original failure: advertised to every viewer,
			// including one denied the page whose menu it reads.
			ungated++
			if (r.Name != writeTool && r.Name != actionTool) || r.Access != "write" {
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
	if ungated != 2 {
		t.Errorf("%d tools carry no page; exactly two (%q and %q) should",
			ungated, writeTool, actionTool)
	}
}

// TestExactlyTwoToolsCanChangeAnything.
//
// ── IT SAID "NONE", THEN "ONE", AND NOW SAYS "TWO" ──────────────────────────
//
// It required every name to start `list_` and forbade a set of mutating verbs,
// because nothing advertised could write. It was re-aimed for `change_row`, and
// is re-aimed again for `run_action` (slice 3 of the MikroMCP parity work): the
// pages' own verbs — renew a lease, take a backup, schedule a package change,
// apply and reboot — are not rows, so `change_row` cannot express them.
//
// The question it answers is unchanged and is still the one that matters: how
// many tools can change a router, and WHICH. A third writer appearing, or a
// verb-shaped name other than the one declared here, is the feature changing
// character, and it must not be possible without this failing.
func TestExactlyTwoToolsCanChangeAnything(t *testing.T) {
	// Named rather than pattern-matched, so adding one is deliberate. These are
	// the shapes a per-verb tool would take — the thing `internal/aitools`
	// excludes: a RouterOS verb the model picks, rather than an action this app
	// declared and gated.
	forbidden := []string{"create_", "add_", "set_", "update_", "remove_", "delete_",
		"move_", "enable_", "disable_", "apply_", "exec_"}
	writers := []string{}
	for _, r := range loadToolArtefact(t) {
		if r.Name == actionTool {
			// The one exception to the verb rule, by name: it runs a DECLARED
			// action from a per-viewer enum, not a verb the model composes.
			writers = append(writers, r.Name)
			if r.Access != "write" {
				t.Errorf("%q does not declare write access", r.Name)
			}
			continue
		}
		for _, bad := range forbidden {
			if strings.HasPrefix(r.Name, bad) {
				t.Errorf("tool %q is shaped like a RouterOS action. Writes go through the two "+
					"declared tools, never a verb the model picks", r.Name)
			}
		}
		switch r.Access {
		case "read":
			// A diagnostic is named for what it runs (`ping`), as RouterOS names it.
			if !strings.HasPrefix(r.Name, "list_") && r.Diagnostic == "" {
				t.Errorf("tool %q declares read access but is not a list", r.Name)
			}
		case "write":
			writers = append(writers, r.Name)
		default:
			t.Errorf("tool %q declares access %q, which is neither read nor write", r.Name, r.Access)
		}
	}
	sort.Strings(writers)
	want := []string{writeTool, actionTool}
	sort.Strings(want)
	if len(writers) != len(want) || writers[0] != want[0] || writers[1] != want[1] {
		t.Errorf("the tools that can change a router are %v; exactly two (%v) should be able to",
			writers, want)
	}
}
