package verify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ROUTEROS CODE RUNS FROM ONE PLACE.
//
// For most of its life MikroDash never executed multi-line RouterOS code:
// `internal/rawcmd` refuses a newline as its first check. Config Management
// changed that, deliberately and in one file - `internal/cfgdeploy/deploy.go`
// uploads a file MikroDash wrote itself (cfgtpl's Render, never the author's
// text) and runs `/import` on it, and `/execute` only to read a dry-run's
// report back. Everything that makes that safe lives around those two calls:
// the analyser, the restore point, the dead-man, the fresh-login proof.
//
// A second call site would have none of it. So the rule is a ledger of files,
// and it fails in both directions: a string naming `/import` or `/execute`
// anywhere else fails, and an entry whose file no longer names one fails too.
// String LITERALS are read, through the parser, so a comment explaining the
// mechanism is not a call site.
//
// cmd/importprobe is the measuring tool the whole design rests on. It refuses
// any router that does not say it is a CHR, and it is not linked into the
// binary. cmd/ztpprobe (2026-09-22) is the same kind of tool for zero-touch
// provisioning's bootstrap: CHR only, not linked, and it runs `/execute` only
// to read back what a measured script printed. The bootstrap itself is run by
// the operator on the router, never by MikroDash, so it adds no call site.
var importSites = map[string]bool{
	"internal/cfgdeploy/deploy.go": true,
	"cmd/importprobe/main.go":      true,
	"cmd/ztpprobe/main.go":         true,
}

// namesImport is a literal that could run code: the command path itself, or a
// script that calls it.
func namesImport(lit string) bool {
	return strings.Contains(lit, "/import") || lit == "/execute"
}

func TestImportHasOneCallSite(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "", func(rel string) bool {
		// .temp is gitignored scratch (lab probes among it): not the app, and
		// excluded here only, since the walk keeps untracked files on purpose
		// for the credential scan.
		return strings.HasSuffix(rel, ".go") && !isTestSource(rel) && !strings.HasPrefix(rel, "third_party/") &&
			!strings.HasPrefix(rel, ".temp/")
	})
	found := map[string]bool{}
	for rel, src := range files {
		f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && namesImport(s) {
				found[rel] = true
			}
			return true
		})
	}
	var stray []string
	for rel := range found {
		if !importSites[rel] {
			stray = append(stray, rel)
		}
	}
	sort.Strings(stray)
	for _, rel := range stray {
		t.Errorf("%s names /import or /execute. RouterOS code runs only from internal/cfgdeploy/deploy.go, "+
			"behind the analyser, the restore point and the dead-man; a second call site has none of them", rel)
	}
	for rel := range importSites {
		if !found[rel] {
			t.Errorf("importSites names %s, which no longer runs /import: the entry is stale, or the "+
				"literal moved somewhere this rule would now call stray", rel)
		}
	}
}

// In the one app file, no command path can become `/import` at run time. A
// path is a literal a reader can see; or a menu joined to a literal verb
// (`m + "/print"`), which ends in that verb whatever the menu; or the
// `writer` adapter's parameter, which carries Backups' own commands - literals
// in internal/backups, which TestImportHasOneCallSite scans.
func TestTheImportFileBuildsNoCommandPath(t *testing.T) {
	const rel = "internal/cfgdeploy/deploy.go"
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot(t), rel), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	literal := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	cmds := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if sel, ok := cl.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Cmd" {
				return true
			}
			cmds++
			for _, e := range cl.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					t.Errorf("%s: %s: a routeros.Cmd without field names; its path cannot be checked", rel, fn.Name.Name)
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Path" {
					continue
				}
				if _, ok := literal(kv.Value); ok {
					continue
				}
				if b, ok := kv.Value.(*ast.BinaryExpr); ok && b.Op == token.ADD {
					if verb, ok := literal(b.Y); ok && strings.HasPrefix(verb, "/") && !strings.Contains(verb[1:], "/") &&
						!namesImport(verb) {
						continue
					}
				}
				if id, ok := kv.Value.(*ast.Ident); ok && id.Name == "cmd" && fn.Name.Name == "writer" {
					continue
				}
				t.Errorf("%s: %s builds a routeros.Cmd path that could be anything, /import included", rel, fn.Name.Name)
			}
			return true
		})
	}
	if cmds == 0 {
		t.Errorf("%s holds no routeros.Cmd: this check reads nothing", rel)
	}
}
