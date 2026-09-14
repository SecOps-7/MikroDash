package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
	"time"
)

// A ROUTER'S STATUS REACHES ONLY THE BROWSERS THAT MAY READ IT.
//
// `router:status` used to go to every signed-in browser with `BroadcastAll`, so
// a viewer whose role grants none of the fleet still received every router's
// connection state and last error. It is now sent per socket, filtered by that
// socket's `visibleRouters`, the way the router list already was.
func TestTheFleetStatusIsSentOnlyToThoseWhoMayReadTheRouter(t *testing.T) {
	s := devicesServerWithPool(t)

	unrestricted := devicesConn(s, "a")
	// A connection whose session may read NOTHING.
	restricted := devicesConn(s, "b")
	restricted.sess = nil

	s.connsMu.Lock()
	s.conns[unrestricted.c] = unrestricted
	s.conns[restricted.c] = restricted
	s.connsMu.Unlock()

	s.sendFleetStatus(map[string]any{"routerId": "r1", "connected": false, "reason": "unreachable"})

	select {
	case raw := <-unrestricted.c.Send:
		var env struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.Event != "router:status" || env.Data["routerId"] != "r1" {
			t.Errorf("the permitted connection received %s %v, want router:status for r1", env.Event, env.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("the connection that may read every router received no status: nothing here " +
			"distinguishes a filtered send from no send at all")
	}

	select {
	case raw := <-restricted.c.Send:
		t.Errorf("a connection that may read no router received %s: a router's state and last "+
			"error still reach principals without access to it", raw)
	case <-time.After(200 * time.Millisecond):
	}
}

// THE SESSION MANAGER IS GIVEN THE FILTERED SEND.
//
// Unset, the manager sends no status beyond a router's own room, which is safe
// and wrong: the Settings badges and the router picker would stop following any
// router but the one on screen. So `server.go` must attach it.
func TestTheSessionManagersFleetStatusIsAttached(t *testing.T) {
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
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "SetFleetStatus" && len(call.Args) == 1 {
			// The ARGUMENT matters: `SetFleetStatus(nil)` would satisfy a call check.
			if arg, ok := call.Args[0].(*ast.SelectorExpr); ok && arg.Sel.Name == "sendFleetStatus" {
				attached = true
			}
		}
		return true
	})
	if !attached {
		t.Fatal("server.go never calls sessions.SetFleetStatus(srv.sendFleetStatus). Router " +
			"statuses then stay in each router's own room, and the Settings badges and the " +
			"router picker stop following every other router.")
	}
}
