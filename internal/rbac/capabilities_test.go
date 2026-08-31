package rbac

// The first-paint capability payload.

import "testing"

// TestABuiltinRoleConfersEveryPage.
//
// ── THE BUG THIS EXISTS FOR ─────────────────────────────────────────────────
//
// `CapabilitiesFor` read `role_pages` directly. Administrator is BUILTIN and has
// no rows there — its reach is structural, so "a page added in a later release
// is covered with no data migration" — so the payload came back with an empty
// page map for the one principal guaranteed to see everything. `db.go`'s Role
// comment warns about exactly this: "a port that only read the table would deny
// an administrator everything — silently, and only on installs that have one".
//
// SILENTLY AND ONLY ON SOME INSTALLS is why nothing caught it. Every other test
// in this package builds CUSTOM roles, which do have rows, and `CanPage` — the
// authoritative per-router gate — handles builtin correctly, so the coarse
// union was the only thing wrong and nothing compared the two.
//
// It was found by running the server in standalone mode against the real /data
// and reading the payload: `readable` listed all three routers while `pages` was
// empty. The app would have drawn a navigation with nothing in it.
func TestABuiltinRoleConfersEveryPage(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"administrator", "1"}},
		grants:     [][4]string{{"user", "u-admin", "global", ""}},
		grantRoles: []string{"administrator"},
	})

	caps, err := r.CapabilitiesFor("u-admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(caps.Pages) == 0 {
		t.Fatal("a builtin administrator was given NO pages. Its reach is structural rather " +
			"than table-driven, so reading role_pages answers nothing for it")
	}
	if len(caps.Pages) != len(r.pages) {
		t.Errorf("%d pages, want all %d -- a builtin role confers every page",
			len(caps.Pages), len(r.pages))
	}
	for page, access := range caps.Pages {
		if access != "write" {
			t.Errorf("%s is %q, want write", page, access)
		}
	}
	// ...AND THE UNION AGREES WITH THE AUTHORITATIVE GATE. The two are computed
	// by different code and only one of them was wrong, which is why nothing
	// noticed: `(*conn).canPage` ANDs them, so a union that is too small
	// silently narrows an answer CanPage would have allowed.
	for page := range caps.Pages {
		if !can(t, r, "u-admin", page, "write", "r-A") {
			t.Errorf("the union offers %s at write and CanPage refuses it", page)
		}
	}
}

// TestACustomRoleConfersOnlyItsOwnPages, and WRITE BEATS READ across roles.
//
// The believability twin: without it the test above passes against an
// implementation that hands every page to everybody.
func TestACustomRoleConfersOnlyItsOwnPages(t *testing.T) {
	r := build(t, seed{
		roles: [][2]string{{"viewer", "0"}, {"dns-admin", "0"}},
		rolePages: [][3]string{
			{"viewer", "dns", "read"}, {"viewer", "wan", "read"},
			{"dns-admin", "dns", "write"},
		},
		grants: [][4]string{
			{"user", "u-1", "router", "r-A"},
			{"user", "u-1", "router", "r-B"},
		},
		grantRoles: []string{"viewer", "dns-admin"},
	})

	caps, err := r.CapabilitiesFor("u-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(caps.Pages) != 2 {
		t.Fatalf("%d pages, want 2 (dns and wan): %v", len(caps.Pages), caps.Pages)
	}
	// WRITE WINS. `dns-admin` grants write on r-B and `viewer` read on r-A; the
	// union is what the FIRST PAINT needs and takes the greater, then the socket
	// refuses the write on the router that does not allow it.
	if caps.Pages["dns"] != "write" {
		t.Errorf("dns is %q, want write -- the union takes the greater of two roles",
			caps.Pages["dns"])
	}
	if caps.Pages["wan"] != "read" {
		t.Errorf("wan is %q, want read", caps.Pages["wan"])
	}
	// ...and the union really is looser than the per-router truth, which is why
	// it may never be used as the gate on its own.
	if can(t, r, "u-1", "dns", "write", "r-A") {
		t.Error("CanPage allowed dns:write on r-A, where only the viewer role applies. The " +
			"union offering it is expected; the authoritative gate allowing it is not")
	}
}

// TestNoGrantsMeansNoPagesAndNoRouters. Empty, never everything: the resolver is
// the only thing between a principal and a page after cutover.
func TestNoGrantsMeansNoPagesAndNoRouters(t *testing.T) {
	r := build(t, seed{roles: [][2]string{{"administrator", "1"}}})
	caps, err := r.CapabilitiesFor("u-nobody")
	if err != nil {
		t.Fatal(err)
	}
	if len(caps.Pages) != 0 || len(caps.Readable) != 0 {
		t.Errorf("a principal with no grants got %d pages and %d routers",
			len(caps.Pages), len(caps.Readable))
	}
	// An empty user id is the unauthenticated case and must answer the same.
	empty, _ := r.CapabilitiesFor("")
	if len(empty.Pages) != 0 || len(empty.Readable) != 0 {
		t.Error("an empty user id was given capabilities")
	}
	// Neither may be nil: the payload is JSON-encoded, and nil marshals to null
	// where the browser expects an object and an array.
	if caps.Pages == nil || caps.Readable == nil {
		t.Error("Pages or Readable is nil -- it marshals to null and the first paint breaks")
	}
}

// TestAGrantNamingADeletedRoleConfersNothing.
//
// ON DELETE RESTRICT should make this unreachable, and the resolver fails
// closed anyway — `CanPage` does the same, and a capability payload that
// invented pages from a dangling grant would offer controls the socket then
// refuses. Added because the mutation "a deleted role confers dns:write"
// SURVIVED: no case in this package named a role that does not exist.
func TestAGrantNamingADeletedRoleConfersNothing(t *testing.T) {
	// TWO GRANTS ON THE SAME ROUTER, and that pairing is the whole case. A
	// dangling grant ALONE is unreachable here: the role confers no
	// `router:read`, so the router is not readable, so the loop that would
	// consult it never runs. It takes a VALID grant to make the router
	// readable and a dangling one beside it for the nil branch to be reached at
	// all — which is why the first version of this test left the mutation
	// "a deleted role confers dns:write" alive.
	//
	// The test DDL declares no foreign key, which is what lets the row exist.
	// On the real schema ON DELETE RESTRICT refuses it; this is the belt.
	r := build(t, seed{
		roles:     [][2]string{{"viewer", "0"}},
		rolePages: [][3]string{{"viewer", "wan", "read"}},
		grants: [][4]string{
			{"user", "u-1", "router", "r-A"},
			{"user", "u-1", "site", "site-1"},
		},
		grantRoles: []string{"viewer", "role-that-was-deleted"},
	})
	caps, err := r.CapabilitiesFor("u-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := caps.Pages["dns"]; ok {
		t.Errorf("a grant naming a deleted role conferred dns: %v", caps.Pages)
	}
	// The VALID grant beside it still works, or this passes for the wrong
	// reason — against a resolver that simply returns nothing.
	if caps.Pages["wan"] != "read" {
		t.Errorf("the valid grant conferred %v, want wan:read", caps.Pages)
	}
}

// TestReadableIsSortedAndDeduplicated. An unsorted map range makes an otherwise
// identical payload differ between calls, which defeats any caching above it and
// makes a diff of two payloads unreadable.
func TestReadableIsSortedAndDeduplicated(t *testing.T) {
	r := build(t, seed{
		roles:     [][2]string{{"viewer", "0"}},
		rolePages: [][3]string{{"viewer", "dns", "read"}},
		// A GLOBAL grant reaches every router in the list below, and a second
		// grant naming one of them must not make it appear twice.
		grants: [][4]string{
			{"user", "u-1", "global", ""},
			{"user", "u-1", "router", "r-mike"},
		},
		grantRoles: []string{"viewer", "viewer"},
	})
	// A ROUTER LIST THAT IS NOT ALREADY IN ORDER. The package's `testRouters`
	// happen to be alphabetical, so sorting them is a no-op and dropping the
	// sort SURVIVED every assertion below until this existed. The natural order
	// here is the declaration order, which is deliberately wrong.
	r.routers = func() []Router {
		return []Router{{ID: "r-zulu"}, {ID: "r-alpha"}, {ID: "r-mike"}}
	}

	first, _ := r.CapabilitiesFor("u-1")
	if len(first.Readable) != 3 {
		t.Fatalf("%d routers readable, want 3: %v", len(first.Readable), first.Readable)
	}
	for i := 1; i < len(first.Readable); i++ {
		if first.Readable[i-1] >= first.Readable[i] {
			t.Fatalf("Readable is not sorted and unique: %v", first.Readable)
		}
	}
	for i := 0; i < 5; i++ {
		again, _ := r.CapabilitiesFor("u-1")
		if len(again.Readable) != len(first.Readable) {
			t.Fatalf("two calls disagreed: %v vs %v", first.Readable, again.Readable)
		}
		for j := range again.Readable {
			if again.Readable[j] != first.Readable[j] {
				t.Fatalf("two calls disagreed: %v vs %v", first.Readable, again.Readable)
			}
		}
	}
}
