package store

// The one write this port makes to users.json.
//
// EVERY PASSWORD AND HASH HERE IS MINTED BY THIS TEST. Nothing is copied from a
// real install — these files transplant into the public MikroDash repository at
// cutover, and a hash committed now is a hash published then.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// usersFixture writes a users.json carrying the two fields the port does NOT
// model, which is the whole point of the test below it.
func usersFixture(t *testing.T, password string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	salt := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := `[
  {
    "id": "u-1",
    "username": "someone",
    "role": "admin",
    "salt": "` + salt + `",
    "passwordHash": "` + HashPassword(password, salt) + `",
    "createdAt": 1700000000000,
    "allowedRouterIds": ["r-A", "r-B"]
  },
  {
    "id": "u-2",
    "username": "another",
    "role": "viewer",
    "salt": "` + salt + `",
    "passwordHash": "` + HashPassword("a-different-invented-password", salt) + `",
    "createdAt": 1700000000001,
    "allowedRouterIds": []
  }
]`
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st, dir
}

func readUserRecords(t *testing.T, dir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("users.json is no longer a bare array: %v\n%s", err, raw)
	}
	return out
}

// TestSetPasswordKeepsFieldsThePortDoesNotModel.
//
// ── THE BUG THIS EXISTS FOR, AND IT IS THE OBVIOUS IMPLEMENTATION ───────────
//
// `store.User` models five fields; the real file carries seven. A writer that
// decoded into `[]User` and re-encoded would drop `createdAt` and
// `allowedRouterIds` from EVERY user in the file — silently, on a password
// change. `allowedRouterIds` is the legacy per-user access list, so the blast
// radius is who can see what.
//
// Asserted on the OTHER user as well as on the one being changed, because a
// whole-file rewrite damages every record and a test reading only the target
// would miss it.
func TestSetPasswordKeepsFieldsThePortDoesNotModel(t *testing.T) {
	const pw = "an-invented-password"
	st, dir := usersFixture(t, pw)

	before := readUserRecords(t, dir)
	if len(before) != 2 {
		t.Fatalf("%d records in the fixture, want 2", len(before))
	}

	if err := st.SetPassword("u-1", "a-new-invented-password"); err != nil {
		t.Fatal(err)
	}

	after := readUserRecords(t, dir)
	if len(after) != 2 {
		t.Fatalf("%d records after the write, want 2 -- a user was lost", len(after))
	}
	for i, rec := range after {
		for _, key := range []string{"id", "username", "role", "createdAt", "allowedRouterIds"} {
			got, want := rec[key], before[i][key]
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("record %d: %s became %s, was %s. A writer that decodes into a struct "+
					"it did not define rewrites the parts of the document it does not know about",
					i, key, gotJSON, wantJSON)
			}
		}
	}
	// The UNTOUCHED user keeps their credential material exactly.
	if after[1]["salt"] != before[1]["salt"] || after[1]["passwordHash"] != before[1]["passwordHash"] {
		t.Error("changing one user's password altered another user's credentials")
	}
}

// TestSetPasswordMintsAFreshSaltAndVerifies.
func TestSetPasswordMintsAFreshSaltAndVerifies(t *testing.T) {
	const oldPw, newPw = "the-old-invented-one", "the-new-invented-one"
	st, dir := usersFixture(t, oldPw)
	before := readUserRecords(t, dir)

	if err := st.SetPassword("u-1", newPw); err != nil {
		t.Fatal(err)
	}
	after := readUserRecords(t, dir)

	if after[0]["salt"] == before[0]["salt"] {
		t.Error("the salt was reused. A fresh one per change is what stops two users who chose " +
			"the same password sharing a hash")
	}
	if after[0]["passwordHash"] == before[0]["passwordHash"] {
		t.Error("the hash did not change")
	}

	// THE NEW PASSWORD VERIFIES AND THE OLD ONE DOES NOT — through the real
	// reader, so the file is proved usable rather than merely different.
	users, err := st.Users()
	if err != nil {
		t.Fatal(err)
	}
	var u User
	for _, x := range users {
		if x.ID == "u-1" {
			u = x
		}
	}
	if !VerifyPassword(u, newPw) {
		t.Error("the new password does not verify against the rewritten record")
	}
	if VerifyPassword(u, oldPw) {
		t.Error("the OLD password still verifies")
	}
	// ...and the other user is unaffected, which the round trip above cannot show.
	for _, x := range users {
		if x.ID == "u-2" && !VerifyPassword(x, "a-different-invented-password") {
			t.Error("another user's password stopped verifying")
		}
	}
}

// TestTheFileStaysABareArrayAndOwnerOnly.
//
// Both are security properties rather than formatting. `_readFile()` returns
// `[]` for anything that is not an array, so a wrapper makes a rolled-back
// binary read ZERO users — which re-opens `POST /api/users/setup`, an
// unauthenticated route that claims the instance. And the file holds scrypt
// hashes and salts, so it is owner-only.
func TestTheFileStaysABareArrayAndOwnerOnly(t *testing.T) {
	st, dir := usersFixture(t, "an-invented-password")
	if err := st.SetPassword("u-1", "another-invented-password"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A BARE ARRAY: the first non-space byte is '['.
	trimmed := string(raw)
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\n' || trimmed[0] == '\t') {
		trimmed = trimmed[1:]
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		t.Fatalf("users.json no longer starts with '[': %.60s", raw)
	}

	info, err := os.Stat(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode is %o, want 600 -- the file holds scrypt hashes and salts", perm)
	}
	// NO TEMPORARY FILE LEFT BEHIND. A stray `.tmp` carries the same hashes at
	// whatever mode it was written with, beside the real file.
	if _, err := os.Stat(filepath.Join(dir, "users.json.tmp")); !os.IsNotExist(err) {
		t.Error("users.json.tmp survived the write")
	}
}

// TestSetPasswordRefusesAnUnknownUser, and writes nothing when it does.
func TestSetPasswordRefusesAnUnknownUser(t *testing.T) {
	st, dir := usersFixture(t, "an-invented-password")
	before, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"nobody", ""} {
		if err := st.SetPassword(id, "another-invented-password"); err != ErrNoSuchUser {
			t.Errorf("SetPassword(%q) returned %v, want ErrNoSuchUser", id, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "users.json"))
	if string(after) != string(before) {
		t.Error("a refused change rewrote the file anyway")
	}
}

// TestAFailedWriteLeavesTheOriginalIntact.
//
// ── WHAT THIS CAN AND CANNOT PROVE ──────────────────────────────────────────
//
// `writeFileAtomic` exists so a reader never sees a half-written file. That
// property — no torn read — cannot be shown in-process without a concurrent
// reader racing the write, and a test that tried would be a flake generator.
//
// What IS testable is the half an operator would notice: when the write fails,
// the old file must still be there and still be usable. A truncating write that
// died midway would leave a shorter file, and `users.js` reads anything
// unparseable as `[]` — which re-opens the unauthenticated setup route.
//
// So the mutation replacing the temp-file-and-rename with a direct write is NOT
// killed by this test, and that is recorded rather than papered over: the
// remaining gap is torn reads, and the defence against those is the rename
// itself, which is one line and reviewed rather than tested.
func TestAFailedWriteLeavesTheOriginalIntact(t *testing.T) {
	const pw = "an-invented-password"
	st, dir := usersFixture(t, pw)
	before, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}

	// ── HOW THE FAILURE IS INJECTED, AND WHY NOT PERMISSIONS ────────────
	//
	// A read-only directory was the obvious choice and does not work: the suite
	// runs as ROOT in the build container, and root ignores directory
	// permissions — the write succeeded and the test reported a damaged file
	// that was merely a successful one.
	//
	// A DIRECTORY where the temporary file belongs cannot be written over by
	// anybody, root included. It fails the same way a full disk would.
	tmpPath := filepath.Join(dir, "users.json.tmp")
	if err := os.Mkdir(tmpPath, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(tmpPath) }()

	if err := st.SetPassword("u-1", "a-new-invented-password"); err == nil {
		t.Error("SetPassword reported success when the temporary file could not be written")
	}

	after, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatalf("the original file is unreadable after a failed write: %v", err)
	}
	if string(after) != string(before) {
		t.Error("a failed write damaged the original file. users.js reads anything unparseable " +
			"as [], which re-opens the unauthenticated setup route")
	}
}
