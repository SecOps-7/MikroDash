package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EVERY `Evaluate` RESULT REACHES A SINK, COUNTED OUT OF THE SOURCE.
//
// ── THE DEFECT THIS EXISTS FOR IS THE ONE THAT ALREADY HAPPENED ───────────
//
// `alertwire.Wire.Evaluate` returns `[]Fired`, and until 2026-08-30 BOTH call
// sites threw it away. Every alert was recorded and none was ever sent, for the
// three days `-alert-dispatch` had been announcing the opposite at startup
// (LOOP.md 0k). No test failed, because every test asked the callee what it
// returned and none asked the callers what they did with it.
//
// So this is the call-site test, and it counts the CLASS rather than naming the
// two sites known today: a third `Evaluate` added tomorrow and left discarding
// its result fails here on the day it is written, which is the only moment the
// author is in a position to know why it matters.
//
// ── WHY AN AST AND NOT A GREP ─────────────────────────────────────────────
//
// A grep for "Evaluate" matches this file's own prose. That is not hypothetical:
// four tests in this session had to be corrected rather than the code, and two
// of them were fooled by a comment I had just written. `ast.Inspect` sees
// statements.
func TestEveryAlertEvaluationReachesASink(t *testing.T) {
	root := "../.."
	type site struct{ file, why string }
	var discarded []site
	checked := 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			// Does this function evaluate alerts at all?
			var evalNames []string
			bare := false
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				switch st := m.(type) {
				case *ast.ExprStmt:
					// A CALL AS A WHOLE STATEMENT IS A DISCARDED RESULT — the
					// exact shape of the original defect.
					if isEvaluateCall(st.X) {
						bare = true
					}
				case *ast.AssignStmt:
					for i, rhs := range st.Rhs {
						if isEvaluateCall(rhs) && i < len(st.Lhs) {
							if id, ok := st.Lhs[i].(*ast.Ident); ok {
								evalNames = append(evalNames, id.Name)
							}
						}
					}
				}
				return true
			})
			if bare {
				discarded = append(discarded, site{path,
					"the call is a whole statement, so its []Fired is thrown away"})
			}
			for _, name := range evalNames {
				checked++
				if !identIsPassedOn(fn.Body, name) {
					discarded = append(discarded, site{path,
						"`" + name + "` is assigned and never passed to anything"})
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// THE AUDIT MUST HAVE FOUND THE CALL SITES. A refactor that renames
	// `Evaluate` would otherwise leave this passing over nothing at all, which is
	// the "check that cannot be told from a broken one" shape.
	if checked < 2 {
		t.Fatalf("found only %d alert evaluation(s); there are two (the pooled "+
			"router path and the live-session emit closure). This audit has "+
			"stopped seeing them and is no longer checking anything", checked)
	}
	for _, d := range discarded {
		t.Errorf("%s: %s — the alert is recorded and NOBODY IS TOLD. That is "+
			"LOOP.md 0k, which shipped for three days behind a startup banner "+
			"promising the opposite.", d.file, d.why)
	}
}

func isEvaluateCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Evaluate"
}

// identIsPassedOn reports whether `name` is used as a call argument anywhere in
// the body — which is what "reaches a sink" means here. Deliberately loose about
// WHICH call: naming `dispatchFired` would make this a test of one wiring rather
// than of the class, and the sink is reached through a function VALUE
// (`m.onFired`) at the session site, which has no name to match on.
func identIsPassedOn(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, a := range call.Args {
			if id, ok := a.(*ast.Ident); ok && id.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

// AND THE SESSION SINK IS ACTUALLY ATTACHED.
//
// The audit above proves the session's emit closure passes its alerts to
// `m.onFired`. It cannot prove anybody ever SETS `onFired` — and a nil sink is
// silently inert, by design, because the field is also nil in every test and in
// every build where dispatch is off. Deleting one line in `New` would restore
// the 0k symptom exactly: rows filed, nothing sent, green suite.
//
// So the wiring gets its own assertion, at the seam where it is decided.
func TestTheSessionManagersAlertSinkIsAttached(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "server.go", b, 0)
	if err != nil {
		t.Fatal(err)
	}
	attached := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "SetAlertSink" || len(call.Args) != 1 {
			return true
		}
		// The ARGUMENT matters: `SetAlertSink(nil)` would satisfy a call check
		// while sending nothing.
		if arg, ok := call.Args[0].(*ast.SelectorExpr); ok && arg.Sel.Name == "dispatchFired" {
			attached = true
		}
		return true
	})
	if !attached {
		t.Error("server.go never calls sessions.SetAlertSink(srv.dispatchFired). " +
			"Alerts from a live page session are recorded and never sent — the " +
			"pooled path would still notify, so the symptom is 'notifications " +
			"only for routers nobody is looking at', which is the hardest " +
			"possible version of this bug to notice.")
	}
}
