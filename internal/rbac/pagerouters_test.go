package rbac

import "testing"

// TestPageRouterIDsAgreesWithCanPage — the batch form is an ORACLE test against
// the single one, over a matrix of every grant scope and every refusal.
//
// The two share `setsConfer` and `scopedRoles`, so they agree by construction
// today; this is what keeps "by construction" true when somebody optimises one
// of them. Every user x page x access is asked both ways, on every router, and a
// single disagreement fails - including the DENIALS, because a batch that
// granted more than CanPage would be the worse of the two directions.
func TestPageRouterIDsAgreesWithCanPage(t *testing.T) {
	r := build(t, seed{
		roles: [][2]string{{"admin", "1"}, {"viewer", "0"}, {"operator", "0"}},
		rolePages: [][3]string{
			{"viewer", "backups", "read"},
			{"operator", "backups", "write"}, {"operator", "devices", "read"},
		},
		grants: [][4]string{
			{"user", "u-site", "site", "site-1"},
			{"user", "u-router", "router", "r-lonely"},
			{"user", "u-global", "global", ""},
			{"user", "u-admin", "router", "r-A"},
			{"group", "g1", "site", "site-2"},
		},
		grantRoles: []string{"viewer", "operator", "viewer", "admin", "operator"},
		members:    [][2]string{{"g1", "u-grp"}},
	})

	users := []string{"u-site", "u-router", "u-global", "u-admin", "u-grp", "u-none", ""}
	pages := []string{"backups", "devices", "reports", "no-such-page"}
	accesses := []string{"read", "write", "bogus"}
	asked, granted := 0, 0
	for _, u := range users {
		for _, p := range pages {
			for _, a := range accesses {
				batch, err := r.PageRouterIDs(u, p, a)
				if err != nil {
					t.Fatalf("PageRouterIDs(%q,%q,%q): %v", u, p, a, err)
				}
				for _, rt := range testRouters {
					one, err := r.CanPage(u, p, a, rt.ID)
					if err != nil {
						t.Fatalf("CanPage: %v", err)
					}
					asked++
					if one {
						granted++
					}
					if batch[rt.ID] != one {
						t.Errorf("%s on %s/%s at %s: batch=%v, CanPage=%v",
							u, rt.ID, p, a, batch[rt.ID], one)
					}
				}
			}
		}
	}
	// THE CONTROL: a matrix in which nothing is ever granted would agree
	// perfectly and prove nothing.
	if granted == 0 || granted == asked {
		t.Fatalf("%d of %d questions granted; the matrix must contain both answers", granted, asked)
	}
}

// TestEffectiveRouterIDsAgreesWithCan is the same oracle for the router list:
// the batch answer against `Can` asked per router, over every grant scope, a
// global-only permission, an unknown one and a user with nothing. Issue #162
// made EffectiveRouterIDs a batch, so this is what keeps it `Can`'s answer.
func TestEffectiveRouterIDsAgreesWithCan(t *testing.T) {
	r := build(t, pageRouterSeed())
	asked, granted := 0, 0
	globalYes := false
	for _, u := range []string{"u-site", "u-router", "u-global", "u-admin", "u-grp", "u-root", "u-none", ""} {
		for _, perm := range []string{"router:read", "router:history", "router:diagnose", "router:create",
			"system:settings", "system:principals", "no:such"} {
			got, err := r.EffectiveRouterIDs(u, perm)
			if err != nil {
				t.Fatalf("EffectiveRouterIDs(%q,%q): %v", u, perm, err)
			}
			batch := map[string]bool{}
			for _, id := range got {
				batch[id] = true
			}
			for _, rt := range testRouters {
				one, err := r.Can(u, perm, rt.ID)
				if err != nil {
					t.Fatalf("Can: %v", err)
				}
				asked++
				if one {
					granted++
				}
				if batch[rt.ID] != one {
					t.Errorf("%s holds %s on %s: batch=%v, Can=%v", u, perm, rt.ID, batch[rt.ID], one)
				}
				if one && GlobalOnly[perm] {
					globalYes = true
				}
			}
		}
	}
	if granted == 0 || granted == asked {
		t.Fatalf("%d of %d questions granted; the matrix must contain both answers", granted, asked)
	}
	if !globalYes {
		t.Fatal("no global-only permission was granted; its branch is not exercised")
	}
}

// TestCanPageAnywhereIsCanPageOnAReadableRouter: the batch form against its
// definition, CanPage on at least one router the user may read.
func TestCanPageAnywhereIsCanPageOnAReadableRouter(t *testing.T) {
	r := build(t, pageRouterSeed())
	both := 0
	for _, u := range []string{"u-site", "u-router", "u-global", "u-admin", "u-grp", "u-none", ""} {
		for _, p := range []string{"backups", "devices", "reports", "no-such-page"} {
			for _, a := range []string{"read", "write"} {
				got, err := r.CanPageAnywhere(u, p, a)
				if err != nil {
					t.Fatal(err)
				}
				want := false
				for _, rt := range testRouters {
					read, _ := r.Can(u, "router:read", rt.ID)
					on, _ := r.CanPage(u, p, a, rt.ID)
					want = want || (read && on)
				}
				if got != want {
					t.Errorf("CanPageAnywhere(%s,%s,%s) = %v, want %v", u, p, a, got, want)
				}
				if got {
					both++
				}
			}
		}
	}
	if both == 0 {
		t.Fatal("nothing was granted anywhere; the matrix proves nothing")
	}
}

// TestTheRouterListReadsTheFleetOnce. "Which routers may this user read" read
// the fleet once per router before issue #162: 201 routers was 202 reads, each
// opening every credential. It is one read now, however large the fleet.
func TestTheRouterListReadsTheFleetOnce(t *testing.T) {
	r := build(t, pageRouterSeed())
	reads := 0
	r.routers = func() []Router { reads++; return testRouters }
	if _, err := r.EffectiveRouterIDs("u-site", "router:read"); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Errorf("EffectiveRouterIDs read the fleet %d times for %d routers, want once", reads, len(testRouters))
	}
	reads = 0
	if _, err := r.CanPageAnywhere("u-none", "backups", "read"); err != nil {
		t.Fatal(err)
	}
	if reads > 2 {
		t.Errorf("CanPageAnywhere read the fleet %d times, want at most two", reads)
	}
	// Session validation runs CapabilitiesFor on every request: six lists and
	// the page union, seven reads however many routers the user can read.
	reads = 0
	if _, err := r.CapabilitiesFor("u-root"); err != nil {
		t.Fatal(err)
	}
	if reads != 7 {
		t.Errorf("CapabilitiesFor read the fleet %d times for %d readable routers, want 7", reads, len(testRouters))
	}
}

func pageRouterSeed() seed {
	return seed{
		roles: [][2]string{{"admin", "1"}, {"viewer", "0"}, {"operator", "0"}},
		rolePages: [][3]string{
			{"viewer", "backups", "read"},
			{"operator", "backups", "write"}, {"operator", "devices", "read"},
		},
		grants: [][4]string{
			{"user", "u-site", "site", "site-1"},
			{"user", "u-router", "router", "r-lonely"},
			{"user", "u-global", "global", ""},
			{"user", "u-admin", "router", "r-A"},
			{"group", "g1", "site", "site-2"},
			{"user", "u-root", "global", ""},
		},
		grantRoles: []string{"viewer", "operator", "viewer", "admin", "operator", "admin"},
		members:    [][2]string{{"g1", "u-grp"}},
	}
}
