package rbac

// The permission vocabulary and the page→permission projection, pinned to
// src/rbac.js.
//
// ── WHY THE TABLES ARE NOT GENERATED, ONLY GATED ───────────────────────────
//
// `permissions.go` carries these by hand and the comments beside them earn their
// place — the one explaining why `reports` confers `router:schedule` and NOT
// `router:write` is a security argument, not decoration. Generating the tables
// would delete that reasoning. So the Go maps stay the source and this only
// checks they still agree with the original.
//
// The failure this catches has no other symptom: a page added upstream would
// confer nothing here, a permission removed there would keep being granted here,
// and every other test in this package would still pass — the port would simply
// answer a different question than the app it mirrors.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type rbacTables struct {
	GlobalOnly         []string            `json:"globalOnly"`
	Scoped             []string            `json:"scoped"`
	ReadConfers        map[string][]string `json:"readConfers"`
	WriteConfers       map[string][]string `json:"writeConfers"`
	WriteConfersAlways []string            `json:"writeConfersAlways"`
}

func loadRBACTables(t *testing.T) rbacTables {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "rbac-tables.json"))
	if err != nil {
		t.Fatalf("no tables — run: node tools/rbac-tables.js: %v", err)
	}
	var f rbacTables
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.WriteConfers) == 0 || len(f.Scoped) == 0 {
		t.Fatal("the tables are empty; the corpus is not being read")
	}
	return f
}

func sameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if len(g) != len(w) {
		t.Errorf("%s: port has %v, the live module has %v", what, g, w)
		return
	}
	for i := range g {
		if g[i] != w[i] {
			t.Errorf("%s: port has %v, the live module has %v", what, g, w)
			return
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestThePermissionVocabularyMatches. An extra permission here is one this port
// would accept and the live app would refuse; a missing one is a check that
// silently denies.
func TestThePermissionVocabularyMatches(t *testing.T) {
	f := loadRBACTables(t)
	sameSet(t, "GLOBAL_ONLY", keys(GlobalOnly), f.GlobalOnly)
	sameSet(t, "SCOPED", keys(Scoped), f.Scoped)
}

// TestTheProjectionMatches — which permissions a page's READ or WRITE row hands
// out. This is what a role edit in the UI actually grants.
func TestTheProjectionMatches(t *testing.T) {
	f := loadRBACTables(t)

	for page, want := range f.ReadConfers {
		sameSet(t, "READ_CONFERS["+page+"]", readConfers[page], want)
	}
	for page := range readConfers {
		if _, ok := f.ReadConfers[page]; !ok {
			t.Errorf("READ_CONFERS: this port projects %q and the live module does not", page)
		}
	}

	for page, want := range f.WriteConfers {
		sameSet(t, "WRITE_CONFERS["+page+"]", writeConfers[page], want)
	}
	for page := range writeConfers {
		if _, ok := f.WriteConfers[page]; !ok {
			t.Errorf("WRITE_CONFERS: this port projects %q and the live module does not — "+
				"a write grant here would confer a permission the live app never gives", page)
		}
	}

	sameSet(t, "WRITE_CONFERS_ALWAYS", writeConfersAlways, f.WriteConfersAlways)
}

// TestEveryConferredPermissionIsKnown — a projection naming a permission that is
// in neither set would be dead here, and `known()` would refuse it. That is a
// silent denial rather than an error, so it is worth asserting directly.
func TestEveryConferredPermissionIsKnown(t *testing.T) {
	for page, perms := range writeConfers {
		for _, p := range perms {
			if !known(p) {
				t.Errorf("WRITE_CONFERS[%q] names %q, which known() does not recognise — "+
					"every grant through that page would silently confer nothing", page, p)
			}
		}
	}
	for page, perms := range readConfers {
		for _, p := range perms {
			if !known(p) {
				t.Errorf("READ_CONFERS[%q] names %q, which known() does not recognise", page, p)
			}
		}
	}
	for _, p := range writeConfersAlways {
		if !known(p) {
			t.Errorf("writeConfersAlways names %q, which known() does not recognise", p)
		}
	}
}

// TestSettingsIsTheOnlyPageConferringAGlobalPermission is the shape worth
// stating on its own: `system:settings` is GLOBAL_ONLY, so a page row that
// conferred another global permission would hand out app-wide reach from a
// per-router grant. Only `settings` does it, and rbac.js has a matching special
// case (`if (GLOBAL_ONLY.has(p) && p !== 'system:settings') def.perms.delete(p)`).
func TestSettingsIsTheOnlyPageConferringAGlobalPermission(t *testing.T) {
	for page, perms := range writeConfers {
		for _, p := range perms {
			if GlobalOnly[p] && p != "system:settings" {
				t.Errorf("WRITE_CONFERS[%q] confers the GLOBAL permission %q — a "+
					"per-router grant would become app-wide reach", page, p)
			}
		}
	}
}
