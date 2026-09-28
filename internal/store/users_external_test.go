package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readUsersFile(t *testing.T, dir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("users.json is not a bare array: %v", err)
	}
	return out
}

// ── AN EXTERNAL ACCOUNT CANNOT USE THE PASSWORD FORM, WITH ANY PASSWORD ────
//
// THE TRAP THIS EXISTS FOR: `CreateUser` always hashes what it is given, so
// creating an SSO account by passing an empty password would store
// HashPassword(""), and somebody submitting an empty password would then MATCH.
// The second half of this test demonstrates that directly, so the reason
// CreateExternalUser exists is recorded rather than merely asserted.
func TestAnExternalUserCannotSignInWithAnyPassword(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}

	if _, err := s.CreateExternalUser("jo", "operator"); err != nil {
		t.Fatalf("creating an external user: %v", err)
	}
	users, err := s.Users()
	if err != nil || len(users) != 1 {
		t.Fatalf("users: %v, %d", err, len(users))
	}
	u := users[0]
	if u.PasswordHash != "" || u.Salt != "" {
		t.Fatalf("an external user was written with credentials: hash %q salt %q",
			u.PasswordHash, u.Salt)
	}
	for _, pw := range []string{"", " ", "password", "jo"} {
		if VerifyPassword(u, pw) {
			t.Errorf("an external account accepted the password %q", pw)
		}
	}

	// ── THE DEMONSTRATION ──────────────────────────────────────────────────
	//
	// The same account made the obvious way - CreateUser with an empty
	// password - DOES accept an empty password. That is the behaviour
	// CreateExternalUser avoids, asserted rather than described so anybody
	// tempted to collapse the two into one finds out here.
	if _, err := s.CreateUser(NewUser{Username: "trap", Password: "", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Users()
	if err != nil {
		t.Fatal(err)
	}
	var trap User
	for _, x := range after {
		if x.Username == "trap" {
			trap = x
		}
	}
	if trap.PasswordHash == "" {
		t.Fatal("CreateUser stored no hash for an empty password; the demonstration below " +
			"no longer demonstrates anything")
	}
	if !VerifyPassword(trap, "") {
		t.Error("an account created through CreateUser with an empty password no longer " +
			"accepts one - if that is now the behaviour, CreateExternalUser's comment is stale")
	}
}

// The record keeps the shape users.json has had since the Node app wrote it:
// the same keys, in the same order, with a millisecond epoch.
func TestAnExternalUserRecordHasTheOrdinaryShape(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if _, err := s.CreateExternalUser("jo", "operator"); err != nil {
		t.Fatal(err)
	}
	recs := readUsersFile(t, dir)
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	r := recs[0]
	for _, key := range []string{"id", "username", "passwordHash", "salt", "role",
		"allowedRouterIds", "createdAt"} {
		if _, ok := r[key]; !ok {
			t.Errorf("the record has no %q; users.json's shape is a compatibility contract", key)
		}
	}
	if ids, ok := r["allowedRouterIds"].([]any); !ok || len(ids) != 0 {
		t.Errorf("allowedRouterIds is %#v, want an empty array (which means unrestricted)",
			r["allowedRouterIds"])
	}
	// A millisecond epoch, not RFC 3339. A string here reads back as NaN in the
	// live app, and nothing notices until a date is rendered.
	if ts, ok := r["createdAt"].(float64); !ok || ts < 1e12 {
		t.Errorf("createdAt is %#v, want a millisecond epoch", r["createdAt"])
	}
}

// An unknown role is refused before anything is written, exactly as CreateUser
// refuses one. A role mapping naming something invalid would otherwise put a
// broken account in the file.
func TestAnExternalUserWithAnUnknownRoleIsRefused(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if _, err := s.CreateExternalUser("jo", "superuser"); err == nil {
		t.Fatal("an unknown role was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "users.json")); !os.IsNotExist(err) {
		t.Error("the refusal still wrote users.json")
	}
}
