package server

// Constructing the background pool — the caller `internal/routers` was written
// without.
//
// ── WHAT WAS BLOCKING IT, AND WHAT LIFTED ───────────────────────────────────
//
// `PORT-QUEUE.md` item 2: the Devices page's rows need numbers for routers
// nobody has open, and Node runs the same pool against the same fleet. Two pools
// mean two connections to every router at once, and the documented bottleneck on
// a MikroTik is concurrent API channels, not CPU (`../MikroDash/src/collection.js:8`).
// So the code was written, pinned and constructed by NOBODY, pending the
// operator's call.
//
// The operator's instruction on 2026-08-28: "port whatever is still missing now.
// This app can now run independently so we can compare it to the live version
// side by side. It should run on its own with no dependencies on the live
// environment." A Devices page with no rows is a dependency on the live
// environment, so the pool runs.
//
// ── IT IS BOUND TO `standalone`, NOT TO A NEW FLAG ──────────────────────────
//
// The reason a Go pool must not run is that a NODE pool is running, and a Node
// pool running is exactly what `-node` configures. Binding to it means the rule
// cannot be set wrong in one place and right in the other — the same argument
// `standalone` itself is derived by, a few lines up in `New`.
//
// **What that binding does NOT cover, stated because it is currently true:** the
// operator is running this process standalone WHILE the live app is also up, on
// its own copy of /data, precisely to compare them. Both pools are therefore
// live against the same three physical routers, and each router carries three
// extra channels for as long as somebody has the Devices page open. That is a
// real cost the flag below exists to drop, and it is bounded — the pool is
// SUSPENDED whenever nobody is looking at the page.
//
// ── THE POOL IS NOT STARTED HERE ────────────────────────────────────────────
//
// `NewPool` connects to nothing; `Sync` does, and the only caller of `Sync` is
// `devicesFocus`, which fires when a browser opens the page. Construction is
// therefore free, which is what makes binding it to a mode rather than a
// lifecycle honest.

import (
	"log"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/routeros"
	"mikrodash/internal/routers"
	"mikrodash/internal/store"
)

// poolRetry is the re-dial backoff, matching the live pool's.
const poolRetry = 5 * time.Second

// buildPool returns the background pool, or nil when this process must not run
// one.
func (s *Server) buildPool(enabled bool) *routers.Pool {
	if !enabled || s.store == nil {
		return nil
	}
	// The GLOBAL settings map, the low half of #105's precedence. Read once at
	// construction, as the live builder does — a per-Sync read would make the
	// pool's intervals change under a running session, which the live one does
	// not do either.
	var settings map[string]any
	if cfg, err := s.store.Settings(); err == nil {
		settings = cfg
	} else {
		log.Printf("[pool] settings unreadable, using collector defaults: %v", err)
	}

	return routers.NewPool(dialForPool, poolRetry, s.persistRouterIdentity, settings)
}

// dialForPool adapts `routeros.Dial` to the pool's `Dialer`.
//
// A FUNCTION rather than the method value, because `Dial` returns `*Client` and
// the pool wants the `Conn` interface — Go will not convert the return type of a
// func value implicitly, and a `*Client` typed nil returned as a `Conn` would be
// a non-nil interface holding nil, which is the classic way an error path stops
// looking like one.
func dialForPool(cfg routeros.Config) (routers.Conn, error) {
	c, err := routeros.Dial(cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

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
func (s *Server) persistRouterIdentity(routerID string, id collect.Identity) {
	if routerID == "" || s.store == nil {
		return
	}
	ident := store.Identity{Model: id.Model, Serial: id.Serial, OSVersion: id.OSVersion}
	wrote, err := s.store.UpdateIdentity(routerID, ident)
	if err != nil {
		log.Printf("[pool] identity for %s: %v", routerID, err)
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

// syncHistoryRouter points the pool's history pair at the ACTIVE router.
//
// Called at startup and after every activation. `setActiveRouter` writes a
// settings key and returns — it does not re-sync the pool, and `Pool.Sync` does
// not rebuild a session that already exists — so without this a pool built while
// router A was active would go on recording A after the operator switched to B.
func (s *Server) syncHistoryRouter() {
	if s.pool == nil || s.store == nil {
		return
	}
	s.pool.SetHistoryRouter(s.activeRouterID())
}
