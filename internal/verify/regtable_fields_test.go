package verify

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// EVERY REGISTRATION-TABLE FIELD LIST NAMES ONLY FIELDS THAT MENU HAS.
//
// AGENTS.md records the hazard: on these menus an unknown name in `=.proplist=`
// can make RouterOS FILTER ROWS rather than leave the field empty - `signal`
// asked of a menu that calls it `signal-strength` returned only the clients
// where it was set. A list of real names is safe and cheaper, so the lists stay
// and this holds them to the menu's actual fields:
//
//   - the modern menu's fields are the 7.25.1 router's own completion list;
//   - the legacy menu's are the RouterOS manual's table, because CHR Test does
//     not run the legacy wireless package.
//
// A name that is in use but not confirmed is recorded as UNVERIFIED, with what
// would settle it. The record fails in both directions: an unverified name no
// list uses any more must leave the record, and one later confirmed belongs in
// `fields`, not in both.
//
// Every `routeros.Cmd` literal in the application is read, so a new read with a
// field list is checked without being registered anywhere.
func TestRegistrationTableFieldListsNameRealFields(t *testing.T) {
	root := repoRoot(t)
	type menuFields struct {
		Fields     []string          `json:"fields"`
		Unverified map[string]string `json:"unverified"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal/verify/testdata/registration-table-fields.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	known := map[string]menuFields{}
	for menu, v := range doc {
		if strings.HasPrefix(menu, "_") {
			continue
		}
		var m menuFields
		if err := json.Unmarshal(v, &m); err != nil {
			t.Fatalf("%s: %v", menu, err)
		}
		if len(m.Fields) == 0 {
			t.Fatalf("%s records no fields, so every name would be refused", menu)
		}
		known[menu] = m
	}

	lists := 0
	usedUnverified := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if pruned[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || isTestSource(rel) {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isCmdType(lit.Type) {
					return true
				}
				path, args := cmdPathAndArgs(lit)
				if !strings.HasSuffix(path, "/registration-table/print") {
					return true
				}
				menu := strings.TrimSuffix(path, "/print")
				for _, a := range args {
					if !strings.HasPrefix(a, "=.proplist=") {
						continue
					}
					lists++
					k, ok := known[menu]
					if !ok {
						t.Errorf("%s asks %s for a field list, and no field set is recorded for that menu "+
							"in internal/verify/testdata/registration-table-fields.json", rel, menu)
						continue
					}
					for _, name := range strings.Split(strings.TrimPrefix(a, "=.proplist="), ",") {
						switch {
						case name == ".id" || contains(k.Fields, name):
						case k.Unverified[name] != "":
							usedUnverified[menu+" "+name] = true
						default:
							t.Errorf("%s asks %s for %q, which that menu does not have. On a "+
								"registration table an unknown name can drop whole rows (AGENTS.md).", rel, menu, name)
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if lists == 0 {
		t.Fatal("no registration-table field list was found, so this check read nothing")
	}

	var stale []string
	for menu, k := range known {
		for name := range k.Unverified {
			if contains(k.Fields, name) {
				t.Errorf("%s %q is recorded as both confirmed and unverified", menu, name)
			}
			if !usedUnverified[menu+" "+name] {
				stale = append(stale, menu+" "+name)
			}
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("%s is recorded as unverified but no field list asks for it any more: delete the record", s)
	}
	t.Logf("%d registration-table field lists checked; %d unverified names in use", lists, len(usedUnverified))
}

// isCmdType is `routeros.Cmd`, or `Cmd` inside package routeros.
func isCmdType(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == "routeros" && x.Sel.Name == "Cmd"
	case *ast.Ident:
		return x.Name == "Cmd"
	}
	return false
}

// cmdPathAndArgs reads the literal Path and the literal Args of a Cmd. An
// argument built from constants joined with `+` is folded, which is how the
// longer lists are wrapped.
func cmdPathAndArgs(lit *ast.CompositeLit) (string, []string) {
	var path string
	var args []string
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, _ := kv.Key.(*ast.Ident)
		if key == nil {
			continue
		}
		switch key.Name {
		case "Path":
			path, _ = foldString(kv.Value)
		case "Args":
			if al, ok := kv.Value.(*ast.CompositeLit); ok {
				for _, a := range al.Elts {
					if s, ok := foldString(a); ok {
						args = append(args, s)
					}
				}
			}
		}
	}
	return path, args
}

func foldString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := foldString(x.X)
		b, ok2 := foldString(x.Y)
		return a + b, ok1 && ok2
	}
	return "", false
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
