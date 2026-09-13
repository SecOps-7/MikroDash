package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// A ROUTER EDIT REACHES THE LIVE SESSION'S PING TARGET.
//
// `reconfigureLiveSession` runs on every router save and re-reads the record,
// so it is where the record's `pingTarget` is handed to the live session. The
// Dashboard session of a router held for alerting or recording is never
// rebuilt, so without this a changed target would not take effect until a
// restart.
func TestARouterEditReachesTheLivePingTarget(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "routers_api.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "reconfigureLiveSession" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("routers_api.go has no reconfigureLiveSession: this check reads nothing")
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ApplyPingTarget" || len(call.Args) != 2 {
			return true
		}
		if arg, ok := call.Args[1].(*ast.SelectorExpr); ok && arg.Sel.Name == "PingTarget" {
			found = true
		}
		return true
	})
	if !found {
		t.Error("reconfigureLiveSession does not hand the record's PingTarget to the live session " +
			"(sessions.ApplyPingTarget(id, rec.PingTarget)), so editing a router's ping target " +
			"does nothing until a restart")
	}
}
