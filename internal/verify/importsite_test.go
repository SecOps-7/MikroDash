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
//
// ── AND SINCE THE TERMINAL PAGE, A SECOND KIND OF CALL SITE ─────────────────
//
// internal/server/terminal.go runs `/execute` on a line a person typed, and it
// genuinely has none of deploy.go's four protections. That is not an oversight
// and widening this ledger is not a way of making the failure quiet, so the
// difference is written down here rather than left to be inferred.
//
// The four protections exist because MIKRODASH COMPOSED THE CODE. cfgtpl
// renders a template into RouterOS the operator never reads line by line, so
// the analyser reads it for them, the restore point undoes it, the dead-man
// reverts a deploy that cuts the management path, and the fresh login proves
// the router still answers. Every one of them is a substitute for an operator
// who cannot see what is about to run.
//
// On the Terminal the operator IS the author. They typed the line, they can
// read it, and there is nothing for an analyser to tell them that they do not
// already know. So the controls are different in kind rather than absent:
//
//   - a SIGNED-IN GLOBAL ADMINISTRATOR, not merely someone who may write a page
//   - the terminalEnabled SETTING, off unless an install turned it on
//   - WRITE ACCESS TO THE terminal PAGE, per router
//   - the ROUTER'S OWN USER POLICY, which is the real backstop: the README's
//     recommended credential is `read,api,test`, so on a default install the
//     page cannot write at all because the router refuses
//   - every line audited, whether or not it worked
//
// What keeps this entry honest rather than a hole is its companion below,
// TestTheTerminalFileRunsOnlyWhatWasTyped: this file may name `/execute`, and
// that is ALL it may do with it - it cannot build some other path, and nothing
// may come between the typed text and the wire.
var importSites = map[string]bool{
	"internal/cfgdeploy/deploy.go": true,
	"internal/server/terminal.go":  true,
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

// TestTheTerminalFileRunsOnlyWhatWasTyped is what earns terminal.go its place
// in importSites.
//
// Two properties, both of which a future change would break silently:
//
//  1. THE ONLY PATHS IT NAMES are `/execute` and the identity read the prompt
//     needs. A file allowed to say `/execute` must not quietly grow the ability
//     to say anything else, or the ledger above has conceded the whole tree.
//
//  2. THE SCRIPT ARGUMENT IS THE TYPED TEXT, concatenated and nothing else.
//     This pins the NEGATIVE, deliberately: "the terminal does not validate its
//     input" is a design decision that reads, to anyone arriving later, exactly
//     like an omission - and "add some validation to the terminal" is the most
//     plausible well-meaning change anybody will ever make to that file. If it
//     is to be parsed one day, that is a decision to take in the open, and this
//     test is what forces the conversation instead of letting it happen.
func TestTheTerminalFileRunsOnlyWhatWasTyped(t *testing.T) {
	const rel = "internal/server/terminal.go"
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot(t), rel), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"/execute": true, "/system/identity/print": true}

	paths, scripts := 0, 0
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if sel, ok := cl.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Cmd" {
			return true
		}
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				t.Errorf("%s: a routeros.Cmd without field names; its path cannot be checked", rel)
				continue
			}
			k, ok := kv.Key.(*ast.Ident)
			if !ok || k.Name != "Path" {
				continue
			}
			paths++
			lit, ok := kv.Value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: a Cmd path that is not a literal; this file may name /execute and nothing else", rel)
				continue
			}
			s, _ := strconv.Unquote(lit.Value)
			if !allowed[s] {
				t.Errorf("%s: names %q. The one file allowed to run /execute may not reach other menus too", rel, s)
			}
		}
		return true
	})
	if paths == 0 {
		t.Errorf("%s holds no routeros.Cmd path: this check reads nothing", rel)
	}

	// The `=script=` word is built from the caller's text by concatenation, with
	// nothing in between. A call there - rawcmd.Parse, a sanitiser, an escaper -
	// is the change this test exists to catch.
	ast.Inspect(f, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if !ok || b.Op != token.ADD {
			return true
		}
		lit, ok := b.X.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(lit.Value); err != nil || s != "=script=" {
			return true
		}
		scripts++
		if _, ok := b.Y.(*ast.Ident); !ok {
			t.Errorf("%s: the =script= word is built from %T, not straight from the typed text. "+
				"If the terminal is to start vetting what it sends, say so in the open", rel, b.Y)
		}
		return true
	})
	if scripts != 1 {
		t.Errorf("%s builds the =script= word %d times, want exactly 1", rel, scripts)
	}
}
