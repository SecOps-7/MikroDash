package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// THE SESSION MANAGER'S IDENTITY WRITER IS ATTACHED, BEFORE THE FIRST SESSION.
//
// A nil writer is silently inert by design — it is nil in every test — so
// deleting the one line in `New` would bring back the defect it fixed: Settings
// → Devices showing each router's RouterOS version from before its last
// upgrade, with a green suite.
//
// ── AND ITS POSITION IS PART OF THE CLAIM ───────────────────────────────────
//
// A session takes the writer when it is BUILT, and `syncFleetHolds` builds a
// held session for the fleet. The first version of this fix attached the writer
// forty lines after the first sync, beside the alert sink: every held router
// captured nil, the unit tests (which attach first) passed, and the live
// install went on showing `7.24`. The history recorder's comment in `New` names
// the same trap. So the attachment must come before the first sync, and this
// fails if it does not.
func TestTheSessionManagersIdentityWriterIsAttached(t *testing.T) {
	attachedBeforeFirstSync(t, "SetOnIdentity", "persistRouterIdentity",
		"A router's model, serial and RouterOS version are then written only by the "+
			"Devices pool, which excludes every router a session holds — so, in practice, never.")
}

// The public-address writer has the same shape and the same trap: attached
// after the first sync, every held router takes nil, and no router is ever
// placed on the map from its public address.
func TestTheSessionManagersPublicIPWriterIsAttached(t *testing.T) {
	attachedBeforeFirstSync(t, "SetOnPublicIP", "persistAutoGeo",
		"No router's public address ever reaches the map's automatic location.")
}

// attachedBeforeFirstSync holds that server.go calls `method(srv.arg)` before
// its first syncFleetHolds.
func attachedBeforeFirstSync(t *testing.T, method, arg, why string) {
	t.Helper()
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", b, 0)
	if err != nil {
		t.Fatal(err)
	}
	var attached, firstSync token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case method:
			// The ARGUMENT matters: `SetOnIdentity(nil)` would satisfy a call check.
			if len(call.Args) == 1 {
				if a, ok := call.Args[0].(*ast.SelectorExpr); ok && a.Sel.Name == arg {
					attached = call.Pos()
				}
			}
		case "syncFleetHolds":
			if firstSync == token.NoPos || call.Pos() < firstSync {
				firstSync = call.Pos()
			}
		}
		return true
	})
	if attached == token.NoPos {
		t.Fatalf("server.go never calls sessions.%s(srv.%s). %s", method, arg, why)
	}
	if firstSync == token.NoPos {
		t.Fatal("no syncFleetHolds call found in server.go: the ordering half of this check reads nothing")
	}
	if attached > firstSync {
		t.Errorf("%s is attached at %s, after the first syncFleetHolds at %s. "+
			"Every held session is built by that sync and takes the writer then, so all of "+
			"them hold nil.", method, fset.Position(attached), fset.Position(firstSync))
	}
}
