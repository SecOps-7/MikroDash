package db

import (
	"testing"
)

func seedProvider(t *testing.T, d *DB, id string, enabled bool) SSOProvider {
	t.Helper()
	p := SSOProvider{
		ID: id, Name: "Entra " + id, Enabled: enabled,
		Issuer: "https://issuer.example/" + id, ClientID: "cid-" + id,
		ClientSecret: "sealed-" + id, Scopes: "openid profile email",
		ClaimUsername: "preferred_username", ClaimEmail: "email",
		ClaimName: "name", ClaimRoles: "groups", CreatedBy: "u-admin",
	}
	if err := d.UpsertSSOProvider(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSSOProvidersRoundTrip(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProvider(t, d, "p1", true)
	seedProvider(t, d, "p2", false)

	all, err := d.SSOProviders()
	if err != nil || len(all) != 2 {
		t.Fatalf("providers: %v, %d", err, len(all))
	}
	got, err := d.SSOProviderByID("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientSecret != "sealed-p1" || got.ClaimRoles != "groups" {
		t.Errorf("round trip lost a field: %+v", got)
	}
}

// ── ONLY ENABLED PROVIDERS REACH AN UNAUTHENTICATED CALLER ─────────────────
//
// The login page asks this before anybody has signed in, so "enabled" is the
// whole of what decides whether a stranger learns a provider exists.
func TestOnlyEnabledProvidersAreOffered(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProvider(t, d, "on", true)
	seedProvider(t, d, "off", false)

	got, err := d.SSOProvidersEnabled()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "on" {
		t.Fatalf("the login page would be offered %d providers: %+v", len(got), got)
	}
}

// An edit changes what a provider is, never who first configured it or when.
func TestAnEditDoesNotChangeWhoCreatedAProvider(t *testing.T) {
	d := openTest(t, t.TempDir())
	p := seedProvider(t, d, "p1", true)
	first, err := d.SSOProviderByID("p1")
	if err != nil {
		t.Fatal(err)
	}

	p.Name = "Renamed"
	p.CreatedBy = "somebody-else"
	p.CreatedAt = 1
	if err := d.UpsertSSOProvider(p); err != nil {
		t.Fatal(err)
	}
	after, err := d.SSOProviderByID("p1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "Renamed" {
		t.Error("the edit did not apply")
	}
	if after.CreatedBy != first.CreatedBy || after.CreatedAt != first.CreatedAt {
		t.Errorf("an edit rewrote the provenance: created_by %q->%q, created_at %d->%d",
			first.CreatedBy, after.CreatedBy, first.CreatedAt, after.CreatedAt)
	}
}

// ── THE ROLE MAP IS REPLACED, NOT MERGED ───────────────────────────────────
//
// The form edits the set as a whole. Merging would make a row the operator
// REMOVED indistinguishable from one the browser did not send.
func TestReplacingTheRoleMapRemovesWhatIsNoLongerThere(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProvider(t, d, "p1", true)

	if err := d.ReplaceSSORoleMap("p1", []SSORoleMapping{
		{ClaimValue: "netops", RoleID: "operator"},
		{ClaimValue: "helpdesk", RoleID: "readonly"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.SSORoleMap("p1"); len(got) != 2 {
		t.Fatalf("seeded %d mappings, want 2", len(got))
	}
	// The operator removes one and re-points the other.
	if err := d.ReplaceSSORoleMap("p1", []SSORoleMapping{
		{ClaimValue: "netops", RoleID: "administrator"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := d.SSORoleMap("p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ClaimValue != "netops" || got[0].RoleID != "administrator" {
		t.Errorf("after a replace the map is %+v; the removed row should be gone and the "+
			"kept one re-pointed", got)
	}
}

// ── A SUBJECT IS NEVER QUIETLY REPOINTED AT ANOTHER ACCOUNT ────────────────
//
// This is the same takeover the callback's collision rule exists to prevent.
// Leaving user_id updatable here would put a second door beside the locked one:
// anyone who could reach this write could move an existing external identity
// onto the local admin's account.
func TestAnIdentityCannotBeRepointedAtAnotherUser(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProvider(t, d, "p1", true)
	if err := d.UpsertSSOIdentity("p1", "sub-1", "u-1"); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertSSOIdentity("p1", "sub-1", "u-ADMIN"); err != nil {
		t.Fatal(err)
	}
	got, err := d.SSOIdentityFor("p1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "u-1" {
		t.Errorf("the subject now resolves to %q; an existing identity was moved onto "+
			"another account", got)
	}
}

// An unknown subject is not an error - it is the first sign-in, which is the
// commonest case there is.
func TestAnUnknownSubjectIsNotAnError(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProvider(t, d, "p1", true)
	got, err := d.SSOIdentityFor("p1", "never-seen")
	if err != nil {
		t.Fatalf("a first sign-in reported an error: %v", err)
	}
	if got != "" {
		t.Errorf("an unknown subject resolved to %q", got)
	}
}

// A deleted account's external identities go with it. Without this the subject
// would still resolve, and the next sign-in would hand back an id RBAC no longer
// knows anything about.
func TestDeletingAUserUnbindsTheirIdentities(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedProvider(t, d, "p1", true)
	if err := d.UpsertSSOIdentity("p1", "sub-1", "u-1"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteSSOIdentitiesForUser("u-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.SSOIdentityFor("p1", "sub-1"); got != "" {
		t.Errorf("a deleted user's subject still resolves to %q", got)
	}
}
