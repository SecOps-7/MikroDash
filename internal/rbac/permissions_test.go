package rbac

import "testing"

func perm(t *testing.T, r *Resolver, user, permission, router string) bool {
	t.Helper()
	ok, err := r.Can(user, permission, router)
	if err != nil {
		t.Fatalf("Can(%s,%s,%s): %v", user, permission, router, err)
	}
	return ok
}

// ── The escalation firewall ──────────────────────────────────────────────────

// TestNoPageConfersPrincipalAuthority checks the PROPERTY — that the page which
// comes closest to system administration confers none of it.
//
// It does NOT test the firewall, though it reads as if it does: with today's
// tables the projection never produces those three permissions in the first
// place, so deleting the firewall leaves this passing. That is what the mutation
// showed, and it is why the test below exists. What this one is good for is the
// EXCEPTION: `settings` at write confers `system:settings`, which is global-only
// and must survive the strip, so a firewall written without the exception fails
// here.
func TestNoPageConfersPrincipalAuthority(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-settings", "0"}},
		rolePages:  [][3]string{{"role-settings", "settings", "write"}},
		grants:     [][4]string{{"user", "u1", "global", ""}},
		grantRoles: []string{"role-settings"},
	})

	if perm(t, r, "u1", "system:principals", "") {
		t.Error("a page conferred system:principals — the escalation firewall is not running")
	}
	if perm(t, r, "u1", "system:db", "") {
		t.Error("a page conferred system:db")
	}
	if perm(t, r, "u1", "router:create", "r-A") {
		t.Error("a page conferred router:create")
	}
	if !perm(t, r, "u1", "system:settings", "") {
		t.Error("system:settings is the documented exception and must survive the strip")
	}
}

// TestFirewallStripsAPageThatWiresItselfToPrincipals is the test that actually
// exercises the firewall, and it exists because the obvious version of it does
// not.
//
// THE TABLES AS THEY STAND NAME NO GLOBAL_ONLY PERMISSION except the documented
// `system:settings`. So deleting the firewall entirely leaves every other test in
// this file passing — verified by mutation, which is the only reason this one was
// written. The firewall is not a filter on today's tables; it is a guarantee
// about tomorrow's, and testing it means writing tomorrow's mistake.
//
// The mistake, concretely: someone adds a page whose write access is meant to
// confer administration and wires it straight to `system:principals`. That is a
// one-line change to a table that reads like configuration, and the firewall is
// what makes it inert.
func TestFirewallStripsAPageThatWiresItselfToPrincipals(t *testing.T) {
	writeConfers["dns"] = []string{"system:principals", "system:db", "router:create"}
	readConfers["logs"] = []string{"system:principals"}
	t.Cleanup(func() { delete(writeConfers, "dns"); delete(readConfers, "logs") })

	r := build(t, seed{
		roles:     [][2]string{{"sneaky", "0"}, {"sneaky-read", "0"}},
		rolePages: [][3]string{{"sneaky", "dns", "write"}, {"sneaky-read", "logs", "read"}},
		grants: [][4]string{
			{"user", "u1", "global", ""},
			{"user", "u2", "global", ""},
		},
		grantRoles: []string{"sneaky", "sneaky-read"},
	})

	for _, p := range []string{"system:principals", "system:db"} {
		if perm(t, r, "u1", p, "") {
			t.Errorf("a write page wired to %s escaped the firewall", p)
		}
	}
	if perm(t, r, "u1", "router:create", "r-A") {
		t.Error("a write page wired to router:create escaped the firewall")
	}
	if perm(t, r, "u2", "system:principals", "") {
		t.Error("a READ page wired to system:principals escaped the firewall")
	}
	// The role still works for everything legitimate, so the firewall is a
	// scalpel rather than a blanket refusal.
	if !perm(t, r, "u1", "router:write", "r-A") {
		t.Error("the firewall removed a permission it should have left alone")
	}
}

// TestOnlyBuiltinConfersPrincipals is the other half: after the firewall, the
// permission is reachable at all only through a builtin role. If this ever fails
// while the test above passes, a projection table has grown a second path to it.
func TestOnlyBuiltinConfersPrincipals(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"admin", "1"}},
		grants:     [][4]string{{"user", "u1", "global", ""}},
		grantRoles: []string{"admin"},
	})
	if !perm(t, r, "u1", "system:principals", "") {
		t.Fatal("a builtin role held globally must confer system:principals")
	}
}

// ── GLOBAL_ONLY ignores the target ───────────────────────────────────────────

// TestGlobalOnlyIsUnreachableFromASiteOrRouterGrant pins rbac.js's "not even one
// holding Administrator": the role here IS builtin, so it confers every
// permission — but it is held at router scope, and a global-only permission
// consults global grants exclusively.
//
// This is the test that would catch a port implementing Can() by calling
// roleSetsInScope for everything, which is the obvious simplification and is
// wrong: roleSetsInScope always includes the global grants PLUS the scoped ones,
// so an Administrator grant on one router would confer principal authority
// app-wide.
func TestGlobalOnlyIsUnreachableFromASiteOrRouterGrant(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"admin", "1"}},
		grants:     [][4]string{{"user", "u1", "router", "r-A"}, {"user", "u2", "site", "site-1"}},
		grantRoles: []string{"admin", "admin"},
	})
	if perm(t, r, "u1", "system:principals", "r-A") {
		t.Error("a router-scoped Administrator reached a global-only permission")
	}
	if perm(t, r, "u2", "system:principals", "r-A") {
		t.Error("a site-scoped Administrator reached a global-only permission")
	}
	// The same grant still works for what it IS scoped to.
	if !perm(t, r, "u1", "router:history", "r-A") {
		t.Error("the router-scoped grant should still confer a scoped permission there")
	}
}

// ── Failing closed ───────────────────────────────────────────────────────────

func TestScopedPermissionWithNoRouterIsRefused(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"admin", "1"}},
		grants:     [][4]string{{"user", "u1", "global", ""}},
		grantRoles: []string{"admin"},
	})
	// An Administrator holding everything globally still gets no answer for a
	// scoped permission with no target. "No restriction recorded" must never
	// read as "unrestricted".
	if perm(t, r, "u1", "router:history", "") {
		t.Error("a scoped permission with an empty target was granted")
	}
}

func TestUnknownPermissionIsDenied(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"admin", "1"}},
		grants:     [][4]string{{"user", "u1", "global", ""}},
		grantRoles: []string{"admin"},
	})
	if perm(t, r, "u1", "router:teleport", "r-A") {
		t.Error("an unknown permission was granted to a builtin role")
	}
	if perm(t, r, "u1", "system:teleport", "") {
		t.Error("an unknown global-shaped permission was granted")
	}
}

func TestUnavailableResolverConfersNothing(t *testing.T) {
	var r *Resolver
	if ok, err := r.Can("u1", "system:principals", ""); ok || err != nil {
		t.Errorf("nil resolver: got (%v, %v), want (false, nil)", ok, err)
	}
	ids, err := r.EffectiveRouterIDs("u1", "router:history")
	if err != nil || len(ids) != 0 {
		t.Errorf("nil resolver: got (%v, %v), want ([], nil)", ids, err)
	}
}

// ── The projection that the audit trail's router half rides on ───────────────

// TestReportsPageConfersRouterHistory pins READ_CONFERS. reports.go's header
// states this coincidence as an assumption and names itself as where it breaks;
// this is the test that fails if it ever does.
func TestReportsPageConfersRouterHistory(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"viewer", "0"}, {"dnsonly", "0"}},
		rolePages:  [][3]string{{"viewer", "reports", "read"}, {"dnsonly", "dns", "write"}},
		grants:     [][4]string{{"user", "u1", "global", ""}, {"user", "u2", "global", ""}},
		grantRoles: []string{"viewer", "dnsonly"},
	})
	if !perm(t, r, "u1", "router:history", "r-A") {
		t.Error("reports at read must confer router:history")
	}
	// A different page at WRITE must not leak it in through WRITE_CONFERS_ALWAYS.
	if perm(t, r, "u2", "router:history", "r-A") {
		t.Error("an unrelated write page conferred router:history")
	}
	if !perm(t, r, "u2", "router:write", "r-A") {
		t.Error("any write row confers router:write")
	}
}

// TestScheduleTakesWriteNotRead pins the distinction rbac.js argues for at
// length: reading a report is router:history, but SCHEDULING one mails that
// history to arbitrary addresses indefinitely, so it takes a write grant.
func TestScheduleTakesWriteNotRead(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"viewer", "0"}, {"editor", "0"}},
		rolePages:  [][3]string{{"viewer", "reports", "read"}, {"editor", "reports", "write"}},
		grants:     [][4]string{{"user", "u1", "global", ""}, {"user", "u2", "global", ""}},
		grantRoles: []string{"viewer", "editor"},
	})
	if perm(t, r, "u1", "router:schedule", "r-A") {
		t.Error("reports at READ conferred router:schedule")
	}
	if !perm(t, r, "u2", "router:schedule", "r-A") {
		t.Error("reports at write must confer router:schedule")
	}
}

// ── EffectiveRouterIDs ───────────────────────────────────────────────────────

// TestEffectiveRouterIDsIsTheFilteredFleet checks the list the audit query turns
// into its IN(...) clause: only the routers the grant actually reaches, sorted,
// and never the whole fleet by default.
func TestEffectiveRouterIDsIsTheFilteredFleet(t *testing.T) {
	r := build(t, seed{
		roles:     [][2]string{{"viewer", "0"}},
		rolePages: [][3]string{{"viewer", "reports", "read"}},
		// site-1 holds r-A, r-B and r-multi (which is also in site-2, #117);
		// r-lonely is in no site and must not appear.
		grants:     [][4]string{{"user", "u1", "site", "site-1"}},
		grantRoles: []string{"viewer"},
	})
	ids, err := r.EffectiveRouterIDs("u1", "router:history")
	if err != nil {
		t.Fatal(err)
	}
	// r-multi is included because it IS in site-1 — a device's other memberships
	// neither add nor remove it here. This test caught the fleet growing when
	// multi-site landed, which is the behaviour it is for.
	if len(ids) != 3 || ids[0] != "r-A" || ids[1] != "r-B" || ids[2] != "r-multi" {
		t.Fatalf("got %v, want [r-A r-B r-multi] sorted", ids)
	}
}

// TestNoGrantsYieldsAnEmptyFleet is the one that matters most for the audit
// page: a user with nothing must produce an EMPTY allow-list, which
// QueryAuditEvents then turns into no rows. An empty list read as "unrestricted"
// is the bug class both this and that function were written against.
func TestNoGrantsYieldsAnEmptyFleet(t *testing.T) {
	r := build(t, seed{roles: [][2]string{{"admin", "1"}}})
	ids, err := r.EffectiveRouterIDs("nobody", "router:history")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("got %v, want an empty list", ids)
	}
	if perm(t, r, "nobody", "system:principals", "") {
		t.Error("a user with no grants reached system:principals")
	}
}

// TestGroupMembershipReachesPermissions confirms the action axis walks the same
// group half CanPage does — a principal whose access is entirely a group
// membership must not resolve to nothing.
func TestGroupMembershipReachesPermissions(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"viewer", "0"}},
		rolePages:  [][3]string{{"viewer", "reports", "read"}},
		grants:     [][4]string{{"group", "g1", "global", ""}},
		grantRoles: []string{"viewer"},
		members:    [][2]string{{"g1", "u1"}},
	})
	if !perm(t, r, "u1", "router:history", "r-A") {
		t.Error("a group membership must confer the group's permissions")
	}
}

// TestAGlobalAdminCanAlwaysReadTheWholeFleet.
//
// ── WHY THIS TEST IS IN THIS PACKAGE AND NOT IN internal/server ────────────
//
// `dbStats` filters its per-router breakdown to what the caller may read, and
// the route above it is gated on `isGlobalAdmin`. A mutation removing that
// filter SURVIVED the server suite, and the reason turned out not to be a
// missing fixture: the partial case is UNREACHABLE BY CONSTRUCTION.
//
//	`system:principals` is GlobalOnly and is STRIPPED from every projected
//	role, so only a BUILTIN role confers it — and a builtin role confers
//	`known(p)` for every permission, `router:read` included. Held at global
//	scope, that resolves to the entire fleet.
//
// So a caller who reaches the route can never be missing a router, and the
// filter can only ever fire in its fail-closed direction: `visibleRouters`
// returns an EMPTY set on an RBAC error, and an empty set hides everything.
// That path is worth keeping and is tested where it lives.
//
// This pins the premise rather than the consequence. If a later release lets a
// page confer `system:principals` — exactly the escalation the firewall above
// exists to prevent — this fails, and the note in `db_api.go` calling that
// filter unreachable becomes false at the same moment.
func TestAGlobalAdminCanAlwaysReadTheWholeFleet(t *testing.T) {
	r := build(t, seed{
		roles: [][2]string{{"admin", "1"}, {"projected", "0"}},
		// The projected role holds one page at WRITE on ONE router: the closest
		// a non-builtin role can get to administration.
		rolePages: [][3]string{{"projected", "settings", "write"}},
		grants: [][4]string{
			{"user", "u-admin", "global", ""},
			{"user", "u-page", "router", "r-A"},
		},
		grantRoles: []string{"admin", "projected"},
	})

	// BELIEVABILITY: the projected user must actually resolve, or "cannot" below
	// would be true of a user the resolver simply does not know.
	if ok, err := r.Can("u-page", "router:read", "r-A"); err != nil || !ok {
		t.Fatalf("u-page cannot read r-A (%v, %v), so this fixture proves nothing", ok, err)
	}
	if ok, err := r.Can("u-page", "system:principals", ""); err != nil || ok {
		t.Fatalf("a PROJECTED role conferred system:principals (%v, %v) — the escalation "+
			"firewall is not running, and the unreachability argument in db_api.go is void",
			ok, err)
	}

	if ok, err := r.Can("u-admin", "system:principals", ""); err != nil || !ok {
		t.Fatalf("the builtin global grant does not confer system:principals (%v, %v)", ok, err)
	}
	ids, err := r.EffectiveRouterIDs("u-admin", "router:read")
	if err != nil {
		t.Fatal(err)
	}
	fleet := len(testRouters)
	if len(ids) != fleet {
		t.Errorf("a global admin reads %d of %d routers: %v. The per-viewer filter on "+
			"/api/db/stats is documented as unable to fire partially, and that is now false.",
			len(ids), fleet, ids)
	}
}
