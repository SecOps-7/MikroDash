package server

// Reordering the fleet.
//
// ── THE ORDER IS A FLEET-WIDE FACT, SO THE PERMISSION IS TOO ───────────────
//
// `routers.json`'s order decides the picker's order for every session and, until
// a primary is set, which router a fresh browser opens on. Moving a row changes
// what everyone else sees, so this is `isGlobalAdmin` and NOT `router:manage` on
// the row being moved - exactly the reasoning `routerActivate` records, and for
// exactly the same reason.
//
// ── ONE MOVE PER REQUEST ───────────────────────────────────────────────────
//
// The control is a pair of arrows, so the request is "move this one, one place".
// A whole-order PUT would be fewer round trips and would also let a stale tab
// overwrite an order it never saw: this way a move is relative to the file as it
// is now, and two admins clicking at once interleave instead of one winning.

import (
	"log"
	"net/http"
)

func (s *Server) registerRouterReorder(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/routers/{id}/move", s.routerMove)
}

func (s *Server) routerMove(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if !s.isGlobalAdmin(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "router id is required")
		return
	}
	// The direction is a query parameter rather than a body: it is one bit, the
	// route already names the subject, and a body would be the only one in this
	// group of router routes.
	dir := r.URL.Query().Get("dir")
	if dir != "up" && dir != "down" {
		writeJSONErr(w, http.StatusBadRequest, "dir must be up or down")
		return
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusInternalServerError, "no store")
		return
	}

	moved, err := s.store.MoveRouter(id, dir == "up")
	if err != nil {
		log.Printf("[routers] move %s %s: %v", id, dir, err)
		writeJSONErr(w, http.StatusNotFound, "no such router")
		return
	}
	// AT THE END, NOTHING MOVED AND NOTHING IS BROADCAST. The request was valid
	// and the answer is 200: the arrows are always drawn, so Up on the top row
	// is an ordinary click, not a mistake. `moved` lets the browser tell the two
	// apart without inferring it from an error it would have to ignore.
	if moved {
		s.broadcastRouterList()
	}
	writeJSON(w, map[string]any{"ok": true, "moved": moved})
}
