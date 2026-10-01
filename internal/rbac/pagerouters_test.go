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
