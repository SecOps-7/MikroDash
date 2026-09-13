package verify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// A ROUTER SLOT IS GIVEN BACK WHEN THE COMMAND IS OVER, NOT WHEN ITS CALLER GIVES UP.
//
// `roslimit` caps the commands in flight on one router. A command that times out
// is still running on the router — routeros.Client.Do no longer cancels it,
// because cancelling ended the whole connection — so releasing its slot when Do
// returns would let the cap be exceeded by exactly the commands a slow router is
// already struggling with. Both slot-takers must hand the release to
// `Cmd.OnFinished` rather than `defer done()`.
func TestRouterSlotsAreReleasedWhenTheCommandFinishes(t *testing.T) {
	root := repoRoot(t)
	for _, file := range []string{"internal/session/session.go", "internal/routers/pool.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, file), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var body *ast.BlockStmt
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Do" || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			if id, ok := fn.Recv.List[0].Type.(*ast.Ident); ok && id.Name == "reader" {
				body = fn.Body
			}
		}
		if body == nil {
			t.Errorf("%s has no reader.Do: this check reads nothing", file)
			continue
		}
		acquires, deferredDone, onFinished := false, false, false
		ast.Inspect(body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name == "Acquire" {
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "roslimit" {
						acquires = true
					}
				}
				if x.Sel.Name == "OnFinished" {
					onFinished = true
				}
			case *ast.DeferStmt:
				if id, ok := x.Call.Fun.(*ast.Ident); ok && id.Name == "done" {
					deferredDone = true
				}
			}
			return true
		})
		if !acquires {
			t.Errorf("%s: reader.Do no longer takes a roslimit slot, so this check reads nothing", file)
		}
		if deferredDone {
			t.Errorf("%s: reader.Do releases its router slot with `defer done()`, so a timed-out "+
				"command gives its slot back while the router is still running it", file)
		}
		if !onFinished {
			t.Errorf("%s: reader.Do does not hand its slot release to Cmd.OnFinished", file)
		}
	}
}
