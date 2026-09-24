package server

import (
	"testing"

	"mikrodash/internal/db"
)

// A USER'S CHANNEL ONLY HEARS ABOUT ROUTERS THEY MAY READ.
//
// ── WHY THIS IS NOT OBVIOUS, AND WAS NEARLY LOST ──────────────────────────
//
// `perUserRecipients`, which channels replace, asked
// `rbac.Can(userID, "router:read", routerID)` at SEND time — so revoking a
// grant stops delivery on the very next alert, with nothing to invalidate. The
// first version of `channelRecipients` did not carry that over: a user could
// make a channel scoped to "all routers" and be told about every router in the
// fleet, including the ones their role hides. Found by reading the old function
// before deleting it, not by any test.
//
// The install's channels are deliberately NOT asked: `_install` is not a user
// and holds no grants, so a permission check against it would refuse everything.
func TestAUserChannelIsScopedByRouterPermission(t *testing.T) {
	s, _, _ := scopedRoutersServer(t)
	if s.auditDB == nil || s.rbac == nil {
		t.Skip("this harness has no database or rbac")
	}
	if _, err := s.auditDB.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	seed := func(id, owner string) {
		t.Helper()
		if err := s.auditDB.UpsertNotifyChannel(db.NotifyChannel{
			ID: id, Owner: owner, Name: id, Kind: "webhook", Enabled: 1,
			Config: `{"urls":["ntfy://ntfy.example.net/t"]}`,
			Events: `["ping_loss"]`, Routers: `[]`, CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("inst", db.InstallOwner)
	// u-1 holds a grant on r1 in this harness; this user holds none.
	seed("mine", "u-no-grants")

	got := s.channelRecipients("r1", "ping_loss")

	var sawInstall, sawUser bool
	for _, r := range got {
		if r.ID == "chan:inst" {
			sawInstall = true
		}
		if r.ID == "chan:mine" {
			sawUser = true
		}
	}
	if !sawInstall {
		t.Error("the install's channel was refused — `_install` holds no grants and " +
			"must not be asked for one, or nothing is ever delivered")
	}
	if sawUser {
		t.Error("a user with no grant on this router received its alert: a channel " +
			"scoped to all routers must still not cross a permission boundary")
	}
}
