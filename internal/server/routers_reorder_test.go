package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `POST /api/routers/{id}/move?dir=up|down`.
//
// The order is a fleet-wide fact: it drives every session's picker and, until a
// primary is set, which router a fresh browser opens on. So the cases here are
// about who may change it and what a click at the end of the list does.

func reorderServer(t *testing.T, sess *Session) (*Server, *http.ServeMux, string) {
	t.Helper()
	s, mux, dir := usersWriteServer(t, sess, seedUsersJSON)
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(activateRouters), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.registerRouterReorder(mux)
	return s, mux, dir
}

func fleetOrder(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i] = r.ID
	}
	return strings.Join(ids, ",")
}

func TestAMoveReordersTheFleet(t *testing.T) {
	s, mux, dir := reorderServer(t, &Session{AuthMode: "none", Username: "admin"})
	_ = s
	w := doJSON(mux, "POST", "/api/routers/r-two/move?dir=up", "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct{ Moved bool }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Moved {
		t.Error("the reply says nothing moved, but r-two was not at the top")
	}
	if got := fleetOrder(t, dir); got != "r-two,r-one" {
		t.Errorf("order is %q, want \"r-two,r-one\"", got)
	}
}

// A CLICK AT THE END IS A 200 THAT MOVED NOTHING.
//
// The arrows are always drawn, so Up on the top row is an ordinary click rather
// than a mistake. It must not be an error - a browser would have to ignore it -
// and it must not rewrite the file or tell every other session to refresh.
func TestMovingPastTheEndSucceedsAndChangesNothing(t *testing.T) {
	_, mux, dir := reorderServer(t, &Session{AuthMode: "none", Username: "admin"})
	before := fleetOrder(t, dir)
	w := doJSON(mux, "POST", "/api/routers/r-one/move?dir=up", "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct{ Moved bool }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Moved {
		t.Error("moving the top router up reported a move")
	}
	if got := fleetOrder(t, dir); got != before {
		t.Errorf("a no-op move reordered the fleet: %q -> %q", before, got)
	}
}

// THE ORDER IS FLEET-WIDE, SO THE PERMISSION IS TOO.
//
// Not `router:manage` on the row being moved: moving it changes what every
// other session's picker shows, so it is the install's decision. Same reasoning
// `routerActivate` records.
func TestOnlyAGlobalAdminMayReorder(t *testing.T) {
	_, mux, dir := reorderServer(t, &Session{AuthMode: "local", Username: "viewer"})
	before := fleetOrder(t, dir)
	w := doJSON(mux, "POST", "/api/routers/r-two/move?dir=up", "", authed)
	if w.Code != 403 {
		t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
	}
	if got := fleetOrder(t, dir); got != before {
		t.Errorf("a refused move still reordered the fleet: %q -> %q", before, got)
	}
}

func TestAMoveNeedsARealRouterAndADirection(t *testing.T) {
	_, mux, dir := reorderServer(t, &Session{AuthMode: "none", Username: "admin"})
	before := fleetOrder(t, dir)

	// A direction that is neither is refused rather than guessed: defaulting to
	// one of them would move a router the operator did not ask to move.
	for _, q := range []string{"", "?dir=", "?dir=sideways", "?dir=UP"} {
		w := doJSON(mux, "POST", "/api/routers/r-two/move"+q, "", authed)
		if w.Code != 400 {
			t.Errorf("dir %q gave %d, want 400", q, w.Code)
		}
	}
	if w := doJSON(mux, "POST", "/api/routers/nope/move?dir=up", "", authed); w.Code != 404 {
		t.Errorf("an unknown router gave %d, want 404", w.Code)
	}
	if got := fleetOrder(t, dir); got != before {
		t.Errorf("a refused move reordered the fleet: %q -> %q", before, got)
	}
}
