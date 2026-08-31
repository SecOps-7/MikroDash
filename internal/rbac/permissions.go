package rbac

// The ACTION axis, which is not the page axis.
//
// ── WHY THIS FILE HAD TO EXIST BEFORE THE AUDIT PAGE COULD BE PORTED ────────
//
// `CanPage` answers "may this user see page P on router R". The audit trail asks
// two questions that cannot be phrased that way:
//
//	includeApp  can(session, 'system:principals')            — no router at all
//	routerIds   effectiveRouterIds(session, 'router:history') — every router, filtered
//
// The first is the harder one. rbac.js:85 says `system:principals` is
// "deliberately conferred by NO page", so projecting it through `CanPage` cannot
// return true for anyone — including an Administrator, whose reach is structural
// rather than table-driven. Gating the app-scope half of the audit trail on
// CanPage would therefore have hidden every user, role, grant and settings
// change from the one person entitled to see them, and it would have looked like
// a working permission check while doing it.
//
// This is also where internal/server/reports.go's stated assumption comes due.
// That file says the port "has no permission vocabulary and does not need one
// HERE", because `router:history` happens to coincide with reports-at-read, and
// names itself as the place the assumption breaks. It breaks here: the audit
// trail needs `router:history` as a permission in its own right, across every
// router at once, and `system:principals` cannot be expressed as a page at all.
//
// ── THE ESCALATION FIREWALL IS THE POINT, NOT A DETAIL ──────────────────────
//
// A non-builtin role's permissions are PROJECTED from its page matrix through
// the two tables below — and then every GLOBAL_ONLY permission except
// `system:settings` is STRIPPED BACK OFF, whatever the projection produced. That
// is what makes "system administration is Administrator-only" a property of the
// resolver rather than of getting a table right: a future page wired to confer
// `system:principals` by mistake still confers nothing.
//
// Reproduced here in the same order, strip included. A port that projected the
// tables and skipped the strip would pass every test written against the tables
// and quietly hand principal authority to any role holding the wrong page.
//
// ── SCOPE ───────────────────────────────────────────────────────────────────
//
// Router targets only, like `CanPage` — rbac.js also accepts a site target, and
// nothing in this port asks a site question yet. The auth-mode short circuit
// stays with the caller for the same reason it does there: only the caller knows
// the mode.

import "sort"

// GlobalOnly permissions are satisfiable ONLY by a role held at global scope.
// `can()` consults the global grants for these and ignores the target entirely,
// so no site or router grant reaches them — not even one holding Administrator.
var GlobalOnly = map[string]bool{
	"system:principals": true, // users, groups, sites, grants
	"system:settings":   true, // settings, auth mode, notification channels, poll intervals
	"system:db":         true, // database stats and global purge
	"router:create":     true, // adding a router, setting the global active router
}

// Scoped permissions are evaluated against a router.
var Scoped = map[string]bool{
	"router:read":     true,
	"router:ack":      true,
	"router:history":  true, // historical reports and exports — the audit trail's router half
	"router:diagnose": true,
	"router:scan":     true,
	"router:schedule": true,
	"router:write":    true,
	"router:manage":   true,
	"router:purge":    true,
	"router:secrets":  true,
}

// readConfers and writeConfers project the page matrix onto the action
// vocabulary. Any page row at all also confers `router:read`.
var readConfers = map[string][]string{
	"reports": {"router:history"},
}

var writeConfers = map[string][]string{
	"dashboard": {"router:ack"},
	"firewall":  {"router:diagnose"},
	"wireless":  {"router:scan"},
	"reports":   {"router:schedule"},
	"devices":   {"router:manage"},
	"settings":  {"system:settings", "router:purge"},
}

// writeConfersAlways is conferred by ANY write row. `router:write` has no call
// sites in the live app yet; conferring it keeps the seeded Operator role equal
// to the one the old model produced.
var writeConfersAlways = []string{"router:write"}

// known reports whether a permission exists at all. An unknown one is refused
// before the graph is consulted, so a typo denies rather than escalating.
func known(permission string) bool { return GlobalOnly[permission] || Scoped[permission] }

// roleConfers is rbac.js's `_roleDef(id).perms.has(permission)`.
//
// NOT MEMOISED, matching the rest of this package: rbac.js caches role
// definitions behind a generation counter it bumps on every mutation, and this
// process never sees those mutations because Node makes them. A cache here could
// serve a revoked grant for as long as it lived.
func (r *Resolver) roleConfers(roleID, permission string) (bool, error) {
	role, err := r.db.RoleByID(roleID)
	if err != nil {
		return false, err
	}
	if role == nil {
		// A grant naming a deleted role confers nothing.
		return false, nil
	}
	if role.Builtin {
		// Administrator holds every permission, structurally, so a permission
		// added in a later release is covered with no data migration. The
		// firewall below does NOT apply: it exists to stop a PAGE conferring
		// system authority, and a builtin role is not projected from pages.
		return known(permission), nil
	}

	perms := map[string]bool{}
	for _, p := range role.Pages {
		perms["router:read"] = true
		for _, c := range readConfers[p.Page] {
			perms[c] = true
		}
		if p.Access == "write" {
			for _, c := range writeConfersAlways {
				perms[c] = true
			}
			for _, c := range writeConfers[p.Page] {
				perms[c] = true
			}
		}
	}
	// The escalation firewall. See the file header — this runs AFTER the
	// projection and unconditionally, which is what makes it a property of the
	// resolver rather than of the tables above being right.
	for p := range perms {
		if GlobalOnly[p] && p != "system:settings" {
			delete(perms, p)
		}
	}
	return perms[permission], nil
}

// Can is rbac.js's can(session, permission, target) for a router target, minus
// the auth-mode short circuit.
//
// A GLOBAL_ONLY permission ignores routerID deliberately. A scoped permission
// with no router is REFUSED rather than treated as unrestricted: the old model's
// "no restriction recorded" fallthrough granted everything, and that is the
// exact bug shape this fails closed against.
func (r *Resolver) Can(userID, permission, routerID string) (bool, error) {
	if !r.Available() || userID == "" {
		return false, nil
	}

	if GlobalOnly[permission] {
		grants, err := r.db.GrantsForUser(userID)
		if err != nil {
			return false, err
		}
		for _, g := range grants {
			if g.ScopeType != "global" {
				continue
			}
			ok, err := r.roleConfers(g.RoleID, permission)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}

	if !Scoped[permission] {
		return false, nil // unknown permission: deny
	}
	if routerID == "" {
		return false, nil // fail closed when the caller forgot the target
	}

	sets, err := r.roleSetsInScope(userID, routerID)
	if err != nil {
		return false, err
	}
	if sets == nil {
		return false, nil // the router does not exist
	}
	for _, roleID := range sets {
		ok, err := r.roleConfers(roleID, permission)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// EffectiveRouterIDs is every router this user holds `permission` on, sorted.
//
// SORTED because rbac.js sorts, and the audit query turns this straight into an
// `IN (...)` list that ends up in a prepared statement — a stable order keeps the
// same question producing the same statement.
//
// An error on ANY router fails the whole call rather than silently dropping that
// router from the list: a partial allow-list is indistinguishable from a smaller
// legitimate one, and this list is a permission boundary.
func (r *Resolver) EffectiveRouterIDs(userID, permission string) ([]string, error) {
	out := []string{}
	if !r.Available() {
		return out, nil
	}
	for _, rt := range r.routers() {
		ok, err := r.Can(userID, permission, rt.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, rt.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// WriteCapablePages is `Object.keys(Rbac.WRITE_CONFERS)`.
//
// It is what greys out a Write toggle that would confer nothing, and the live
// comment is explicit that it is "derived from the projection table, never
// restated in the client". Exported here rather than rebuilt at the handler for
// the same reason: a second list is one that can disagree with the table
// actually consulted when a grant is evaluated.
//
// SORTED, where JavaScript would hand back insertion order. The consumer tests
// membership rather than reading positions, so the order is not part of the
// contract — and an unsorted Go map would produce a different payload on every
// request, which turns a diff of two responses into noise.
func WriteCapablePages() []string {
	out := make([]string, 0, len(writeConfers))
	for page := range writeConfers {
		out = append(out, page)
	}
	sort.Strings(out)
	return out
}

// ConfersAtWrite reports whether a page's WRITE row hands out any permission of
// its own. Exported for the endpoint's test, which checks that every page
// offered as write-capable actually confers something — an entry that did not
// would grey in a toggle that changes nothing.
func ConfersAtWrite(page string) bool { return len(writeConfers[page]) > 0 }
