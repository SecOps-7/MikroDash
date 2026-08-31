// Package rbac answers "may this user do this, here" from the grant graph.
//
// WHY THIS EXISTS AT ALL. internal/server/auth.go carries a long note about a
// cutover blocker: Node gates a page with a PER-ROUTER answer, while
// /api/auth/status hands the browser `caps.pages` — the union across every
// readable router. Node itself calls that union "what the first paint needs"
// and says "the per-router answer is authoritative". Gating on the union
// over-permits in one specific case: a principal holding dns:write on router A
// and dns:read on router B is offered the write controls on B, and the write is
// then executed by this process. That had to be closed before any page could be
// cut over from Node, and this package closes it.
//
// It became possible only once internal/db landed. The grant graph is four
// tables in the SQLite database Node owns — grants, group_members, roles,
// role_pages — plus one field in routers.json. Nothing here writes any of them.
//
// A NOTE ON WHY READING THE GRAPH IS NOT A LEAK. The /api/auth/status handler
// deliberately withholds it, "which would disclose every other principal's
// access to anyone who opened devtools". That reasoning is about the BROWSER.
// This runs server-side and reads exactly what rbac.js reads, inside the same
// trust boundary Node's own resolver occupies.
//
// FAILS CLOSED, EVERYWHERE. A missing role, a grant naming a deleted role, a
// router that does not exist, an unknown page, an unknown access level: every
// one answers no. The single thing this package must never do is invent access.
package rbac

import (
	"mikrodash/internal/db"
)

// accessRank is _ACCESS_RANK in rbac.js. Anything not named here ranks 0, and a
// need of 0 is refused before the graph is consulted.
var accessRank = map[string]int{"read": 1, "write": 2}

// Router is the slice of a router record this package needs. A plain struct
// rather than an interface so the caller decides where records come from —
// today internal/store, which reads routers.json.
type Router struct {
	ID string
	// Label and Host are for DISPLAY only — `AccessSummaryFor` renders
	// `label || host`, which is what `Routers.getById` gives the live
	// `accessSummaryFor`. No permission question reads them, and none should:
	// a name is not an identity.
	Label string
	Host  string
	// SiteIDs is the device's site membership (#117). A device may belong to
	// SEVERAL sites and is reachable from a grant on ANY of them — the live
	// comment states it outright: "a device in A and B is reachable from a grant
	// on EITHER", because each site pushes its own role set and the union is
	// what the caller already computes.
	//
	// This was a single `SiteID` until 2026-08-25, which matched exactly one
	// site and DENIED access the live app grants. The failure direction was the
	// safe one — refusing a legitimate operator rather than admitting a
	// stranger — but it is still a divergence, and one nothing would have
	// reported: an operator seeing "no access" blames their own grants.
	SiteIDs []string
}

// Resolver answers authorization questions against the grant graph.
//
// IT HOLDS NO CACHE, DELIBERATELY. rbac.js caches views and role definitions
// behind a generation counter it bumps on every mutation; this process never
// sees those mutations, because Node makes them. A cache here could therefore
// serve a revoked grant for as long as it lived. A permission check is three
// indexed SELECTs against a local file, and being right is worth more than that.
type Resolver struct {
	db      *db.DB
	routers func() []Router
	pages   map[string]bool
}

// New builds a resolver. `routers` is a function rather than a captured slice so
// a router added, removed or moved between sites while the process runs is seen
// on the next question instead of the next restart.
func New(database *db.DB, routers func() []Router) *Resolver {
	pages := make(map[string]bool, len(PageKeys))
	for _, k := range PageKeys {
		pages[k] = true
	}
	return &Resolver{db: database, routers: routers, pages: pages}
}

// Available reports whether this resolver can actually answer. A caller with no
// database must keep using the coarser gate rather than treating every question
// as "no", which would lock every user out of every page.
func (r *Resolver) Available() bool { return r != nil && r.db != nil }

// CanPage is rbac.js's canPage(session, page, access, target) for a router
// target, minus the auth-mode short circuit — that belongs to the caller, which
// is the only place that knows the mode.
//
// The guards run in rbac.js's order, and the order matters: an unknown page is
// refused BEFORE the graph is consulted, so a builtin role — which confers every
// page structurally — cannot be made to confer one that does not exist.
func (r *Resolver) CanPage(userID, page, access, routerID string) (bool, error) {
	if !r.Available() || userID == "" || routerID == "" {
		return false, nil
	}
	if !r.pages[page] {
		return false, nil // unknown page: deny
	}
	need := accessRank[access]
	if need == 0 {
		return false, nil // unknown access level: deny
	}

	sets, err := r.roleSetsInScope(userID, routerID)
	if err != nil {
		return false, err
	}
	if sets == nil {
		return false, nil // the router does not exist
	}

	for _, roleID := range sets {
		role, err := r.db.RoleByID(roleID)
		if err != nil {
			return false, err
		}
		if role == nil {
			// A grant naming a role that no longer exists confers nothing.
			// ON DELETE RESTRICT should make this unreachable; failing closed
			// anyway costs nothing.
			continue
		}
		if role.Builtin {
			// Administrator's reach is structural rather than table-driven, so
			// a page added in a later release is covered with no data
			// migration. Every page at write, which satisfies any need.
			return true, nil
		}
		for _, p := range role.Pages {
			if p.Page == page && accessRank[p.Access] >= need {
				return true, nil
			}
		}
	}
	return false, nil
}

// roleSetsInScope returns every role id that applies to a router, or nil when
// the router does not exist.
//
// A ROUTER INHERITS ITS SITE'S GRANT, and a router-scoped grant never confers
// anything site-wide. Missing the site half would deny a principal whose access
// comes entirely from a site, which is the ordinary shape for a fleet.
func (r *Resolver) roleSetsInScope(userID, routerID string) ([]string, error) {
	var router *Router
	for _, rt := range r.routers() {
		if rt.ID == routerID {
			c := rt
			router = &c
			break
		}
	}
	if router == nil {
		return nil, nil
	}

	grants, err := r.db.GrantsForUser(userID)
	if err != nil {
		return nil, err
	}

	out := []string{}
	for _, g := range grants {
		switch g.ScopeType {
		case "global":
			out = append(out, g.RoleID)
		case "site":
			// ANY of the device's sites, not one. A grant is pushed once per
			// matching site, preserving the union semantics the caller relies on.
			for _, sid := range router.SiteIDs {
				if sid != "" && g.ScopeID == sid {
					out = append(out, g.RoleID)
					break
				}
			}
		case "router":
			if g.ScopeID == routerID {
				out = append(out, g.RoleID)
			}
		}
	}
	return out, nil
}

// CanPageAnywhere is `canPageAnywhere(session, page, access)`: may this
// principal see the page on AT LEAST ONE router it can read.
//
// ── THE QUESTION A ROUTERLESS REQUEST HAS TO ASK ────────────────────────────
//
// The live comment, on the dashboard layout: "a per-user preference with no
// router in the request, so a scoped check with no target would fail closed and
// lock everyone out. Requiring the page on at least one visible router is the
// equivalent question."
//
// So it is deliberately WEAKER than CanPage and must never be used where a
// router id is available. It answers "may you use this page at all", not "may
// you use it here" — and the second is the only one that gates data.
func (r *Resolver) CanPageAnywhere(userID, page, access string) (bool, error) {
	if !r.Available() || userID == "" {
		return false, nil
	}
	readable, err := r.EffectiveRouterIDs(userID, "router:read")
	if err != nil {
		return false, err
	}
	for _, rid := range readable {
		ok, cerr := r.CanPage(userID, page, access, rid)
		if cerr != nil {
			return false, cerr
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
