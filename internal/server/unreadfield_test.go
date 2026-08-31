package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// isServerReceiver reports whether an expression denotes the Server: the bare
// receiver `s`/`srv`, or anything ending in `.srv` such as `cn.srv`.
func isServerReceiver(x ast.Expr) bool {
	switch v := x.(type) {
	case *ast.Ident:
		return v.Name == "s" || v.Name == "srv"
	case *ast.SelectorExpr:
		return v.Sel.Name == "srv"
	}
	return false
}

// A FIELD THE SERVER WRITES AND NEVER READS IS A COMPONENT THAT DOES NOTHING.
//
// ── FOUR INSTANCES OF THIS CLASS, NONE CAUGHT BY THE COMPILER ─────────────
//
// Go warns about an unused local and says nothing about a struct field that is
// assigned and never read. So a component can be constructed, started, and left
// with an unreachable `Stop` — or with no caller at all:
//
//   - the BACKUP SCHEDULER was built and never `Start`ed, so `-backup-scheduler`
//     switched on something that could not act.
//   - the RETENTION SWEEP was assigned and never read, so its `Stop` was
//     unreachable and a 24-hour ticker outlived every server.
//   - `alertwire.Wire.Drop` exists and is called from nowhere.
//   - the ALERT DISPATCHER is built and never invoked, so no notification has
//     ever been sent — LOOP.md 0k, and the one with real consequences.
//
// ── WHY THE AST AND NOT A GREP ────────────────────────────────────────────
//
// A grep over the package found `pruneSched` and MISSED `dispatch`, because by
// then a COMMENT explaining the dispatcher defect contained the string
// `srv.dispatch` — the documentation of the bug hid it from the detector. This
// walks selector expressions, so comments and unrelated same-named methods on
// other types (`cn.dispatch(in)` is a method on `conn`) cannot fool it.
func TestNoServerFieldIsWrittenAndNeverRead(t *testing.T) {
	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0) // 0 = comments DISCARDED, which is the point
	if err != nil {
		t.Fatal(err)
	}
	p := pkg["server"]
	if p == nil {
		t.Fatal("package server did not parse — this test is measuring nothing")
	}

	// The declared unexported fields of `Server`.
	declared := map[string]bool{}
	for _, f := range p.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Server" {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				for _, nm := range fld.Names {
					if !nm.IsExported() {
						declared[nm.Name] = true
					}
				}
			}
			return false
		})
	}
	if len(declared) == 0 {
		t.Fatal("no Server fields found — the type moved and this measures nothing")
	}

	// Reads: any `X.field` selector that is NOT the left side of an assignment.
	read := map[string]bool{}
	for _, f := range p.Files {
		assignedHere := map[ast.Node]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range as.Lhs {
					assignedHere[lhs] = true
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || assignedHere[ast.Node(sel)] {
				return true
			}
			// ── THE RECEIVER MAY BE NESTED, AND MISSING THAT COST A PASS ──
			//
			// Reads come in two shapes: `s.field` on a method receiver, and
			// `cn.srv.field` from a connection. The first version accepted only
			// a bare Ident and flagged four fields that are read constantly
			// through the second — `devicesMu` is `cn.srv.devicesMu.Lock()`.
			//
			// `cn.dispatch(in)` still does not match: its receiver is `cn`,
			// which is neither `s`/`srv` nor a selector ending in `srv`.
			if !isServerReceiver(sel.X) {
				return true
			}
			if declared[sel.Sel.Name] {
				read[sel.Sel.Name] = true
			}
			return true
		})
	}

	// RECORDED EXCEPTIONS. `dispatch` is genuinely unread — that IS the open
	// finding — so it is named here rather than left to fail every run. When
	// LOOP.md 0k is closed this entry must go, and the test says so.
	// EMPTY, and it was not on 2026-08-30. `dispatch` sat here because the
	// dispatcher was built and never invoked; `alert_send.go` gave it a reader
	// and this test failed until the entry was deleted, which is the mechanism
	// working rather than a chore.
	expected := map[string]string{}

	var unread []string
	for f := range declared {
		if !read[f] && expected[f] == "" {
			unread = append(unread, f)
		}
	}
	sort.Strings(unread)
	for _, f := range unread {
		t.Errorf("Server.%s is written and never read. Whatever it holds is constructed "+
			"and does nothing — a scheduler that never ticks, a Stop nothing can call, or "+
			"a component with no caller. Four of those have been found here.", f)
	}
	for f, why := range expected {
		if read[f] {
			t.Errorf("Server.%s is recorded as unread but something reads it now. "+
				"Delete the entry: %s", f, why)
		}
	}
}
