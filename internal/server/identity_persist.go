package server

// Recording what a router says about itself: model, serial, RouterOS version.

import (
	"log"

	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/store"
)

// persistRouterIdentity is the live `_persistRouterIdentity`.
//
//	function _persistRouterIdentity(routerId, identity) {
//	  if (!routerId) return;
//	  try {
//	    if (Routers.updateIdentity(routerId, identity)) {
//	      audit.system().record({ action: 'router.identity', ... });
//	      _broadcastRoutersList();
//	    }
//	  } catch ...
//	}
//
// ── ALL THREE EFFECTS ARE GATED ON THE WRITE ────────────────────────────────
//
// A router reports the same identity on every poll. `UpdateIdentity` answering
// false is the common case by a wide margin, and it is what stops this from
// rewriting routers.json, emitting an audit event and waking every browser
// several times a minute. A port that ungated any one of the three would look
// correct and behave like a leak.
//
// ── AND IT SWALLOWS ITS ERRORS, LIKE THE ORIGINAL ───────────────────────────
//
// This runs on a background collector's goroutine. A failure to persist what a
// router said about itself must not take down the session that is otherwise
// collecting fine — the identity will be offered again on the next poll.
//
// ── ONE CALLER: EVERY SESSION ───────────────────────────────────────────────
//
// A session's System collector reports here (session.Manager.SetOnIdentity),
// including the one-shot prime a WARM session takes on each connect - so a
// router nobody is watching still has its model, serial and RouterOS version
// recorded, and an upgrade is picked up on the reconnect that follows it.
//
// The overview pool used to report here too, until it was deleted on 2026-10-01.
// It had stopped mattering long before: it excluded every router with a live
// session, held sessions kept the whole fleet live, and so it reported for
// nobody. This file was `pool_wire.go`, and this function is all that was left
// of it worth keeping.
func (s *Server) persistRouterIdentity(routerID string, id collect.Identity) {
	if routerID == "" || s.store == nil {
		return
	}
	ident := store.Identity{Model: id.Model, Serial: id.Serial, OSVersion: id.OSVersion}
	wrote, err := s.store.UpdateIdentity(routerID, ident)
	if err != nil {
		log.Printf("[identity] %s: %v", routerID, err)
		return
	}
	if !wrote {
		return
	}
	s.auditSystem(audit.Event{
		Action:     "router.identity",
		TargetType: "router",
		TargetID:   routerID,
		RouterID:   routerID,
		After: map[string]any{
			"model": ident.Model, "serial": ident.Serial, "osVersion": ident.OSVersion,
		},
	})
	// EVERY viewer, each filtered for its own principal — see broadcastRouterList.
	s.broadcastRouterList()
}
