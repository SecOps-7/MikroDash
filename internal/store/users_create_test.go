package store

// CreateUser, against a corpus produced by RUNNING the live `createUser`.
//
// The users-create corpus points `src/users.js` at a throwaway directory
// and calls it for real, then reads back the file it wrote. So the expectations
// here are not a description of what `users.js` is believed to do.
//
// EVERY PASSWORD IN THIS FILE IS INVENTED HERE. Nothing is copied from a real
// install — these files transplant into the public MikroDash repository at
// cutover, and a hash committed now is a hash published then.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type createCorpus struct {
	Roles []string `json:"roles"`
	File  struct {
		EndsWithNewline bool   `json:"endsWithNewline"`
		SecondLine      string `json:"secondLine"`
		AmpersandRaw    bool   `json:"ampersandRaw"`
	} `json:"file"`
	Accepted []struct {
		Why              string   `json:"why"`
		RecordKeys       []string `json:"recordKeys"`
		PublicKeys       []string `json:"publicKeys"`
		Username         string   `json:"username"`
		Role             string   `json:"role"`
		AllowedRouterIDs []string `json:"allowedRouterIds"`
		SaltLen          int      `json:"saltLen"`
		HashLen          int      `json:"hashLen"`
		CreatedAtType    string   `json:"createdAtType"`
		CreatedAtDigits  int      `json:"createdAtDigits"`
		PublicOmits      []string `json:"publicOmits"`
	} `json:"accepted"`
	Refused []struct {
		Why          string `json:"why"`
		Message      string `json:"message"`
		WroteNothing bool   `json:"wroteNothing"`
	} `json:"refused"`
}

func loadCreateCorpus(t *testing.T) createCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/users-create-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/users-create-cases.js", err)
	}
	var doc createCorpus
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Accepted) == 0 || len(doc.Refused) == 0 {
		t.Fatal("corpus is empty on one side")
	}
	return doc
}

// emptyStore is a /data with no users.json at all — the state a fresh install
// starts in, and the one setup exists for.
func emptyStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// keyOrderIn returns the keys of the FIRST object in a document, in the order
// they are written. Decoding into a map would lose exactly the property being
// tested.
func keyOrderIn(t *testing.T, b []byte) []string {
	t.Helper()
	re := regexp.MustCompile(`"([A-Za-z][A-Za-z0-9_]*)":`)
	var out []string
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		if seen[m[1]] {
			break // the second record has started
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// TestTheRecordShapeMatchesLive — the seven fields, in the live order.
//
// ORDER IS COMPARED, not just membership. Go sorts map keys and JavaScript does
// not, so a port using `map[string]any` here would produce a valid file with
// every field in a different place; `newUserRecord` is a struct precisely so the
// declaration order settles it. It is the one record in this package whose order
// the port controls, which is why it is worth asserting.
func TestTheRecordShapeMatchesLive(t *testing.T) {
	corpus := loadCreateCorpus(t)
	s, dir := emptyStore(t)

	if _, err := s.CreateUser(NewUser{
		Username: "ann", Password: "an-invented-password", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := corpus.Accepted[0].RecordKeys
	got := keyOrderIn(t, b)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("record keys are\n  %v\nlive writes\n  %v\n"+
			"Field order comes from the struct declaration in users_create.go.", got, want)
	}
}

// TestTheFieldsHaveTheLiveShapes — the values that are random, checked by shape.
//
// A 32-character salt, a hash from the wrong scrypt parameters, or an id that is
// not a UUID each produce a file `users.js` reads without complaint and cannot
// authenticate against. None is visible in a round trip through this port alone.
func TestTheFieldsHaveTheLiveShapes(t *testing.T) {
	corpus := loadCreateCorpus(t)
	live := corpus.Accepted[0]
	s, dir := emptyStore(t)

	before := time.Now().UnixMilli()
	pub, err := s.CreateUser(NewUser{
		Username: "ann", Password: "an-invented-password", Role: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UnixMilli()

	var recs []struct {
		ID           string          `json:"id"`
		Salt         string          `json:"salt"`
		PasswordHash string          `json:"passwordHash"`
		CreatedAt    json.RawMessage `json:"createdAt"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "users.json"))
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatal(err)
	}
	r := recs[0]

	if len(r.Salt) != live.SaltLen {
		t.Errorf("salt is %d chars, live writes %d", len(r.Salt), live.SaltLen)
	}
	if len(r.PasswordHash) != live.HashLen {
		t.Errorf("hash is %d chars, live writes %d — the scrypt parameters differ",
			len(r.PasswordHash), live.HashLen)
	}
	if !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(r.Salt + r.PasswordHash) {
		t.Error("salt or hash is not lower-case hex")
	}
	if !regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
	).MatchString(r.ID) {
		t.Errorf("id %q is not a UUID v4", r.ID)
	}

	// CREATEDAT IS A NUMBER, and a 13-digit one. This is the field a Go port gets
	// wrong by reaching for `time.Now().Format(...)`: the file stays valid and
	// `users.js` reads the string back as NaN.
	if live.CreatedAtType != "number" || live.CreatedAtDigits != 13 {
		t.Fatalf("the corpus says createdAt is a %s of %d digits; this test assumes ms epoch",
			live.CreatedAtType, live.CreatedAtDigits)
	}
	raw := string(r.CreatedAt)
	if strings.HasPrefix(raw, `"`) {
		t.Fatalf("createdAt was written as a STRING (%s). users.js reads it back as NaN.", raw)
	}
	if len(raw) != 13 {
		t.Errorf("createdAt has %d digits (%s), want 13", len(raw), raw)
	}
	// The returned view carries the same timestamp. It is a `float64` here
	// because it came back through a `map[string]any`, which is what JSON numbers
	// decode to — and is exactly representable for a 13-digit integer.
	ms, ok := pub["createdAt"].(float64)
	if !ok {
		t.Fatalf("the returned createdAt is %T, not a number", pub["createdAt"])
	}
	if int64(ms) < before || int64(ms) > after {
		t.Errorf("createdAt %d is outside the window [%d, %d]", int64(ms), before, after)
	}
}

// TestThePublicViewOmitsExactlyTheHashAndSalt.
//
// `_toPublic` is a denylist of two, and `CreateUser` returns its port —
// `PublicUser` in users_public.go — rather than assembling a struct beside it.
// So the risk here is not leaking an unmodelled field but DROPPING one, and both
// directions are checked against the live key list.
//
// THE KEY SET, NOT THE ORDER, and the difference from the on-disk record is
// deliberate. This value becomes an HTTP response body, which a browser parses;
// the record becomes a file, which a person reads and diffs. Go maps sort, so
// holding this one to insertion order would mean hand-listing the fields — the
// exact thing the denylist exists to avoid.
func TestThePublicViewOmitsExactlyTheHashAndSalt(t *testing.T) {
	corpus := loadCreateCorpus(t)
	s, _ := emptyStore(t)

	pub, err := s.CreateUser(NewUser{
		Username: "ann", Password: "an-invented-password", Role: "admin",
		AllowedRouterIDs: []string{"r1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{}
	for _, k := range corpus.Accepted[0].PublicKeys {
		want[k] = true
	}
	for k := range pub {
		if !want[k] {
			t.Errorf("the public view carries %q, which live does not return", k)
		}
	}
	for k := range want {
		if _, ok := pub[k]; !ok {
			t.Errorf("the public view is missing %q, which live returns", k)
		}
	}
	// And the two that must never appear, checked on the marshalled bytes rather
	// than on the map — this is the form that reaches a browser.
	b, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range corpus.Accepted[0].PublicOmits {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("the public view carries %s: %s", forbidden, b)
		}
	}
	if len(corpus.Accepted[0].PublicOmits) != 2 {
		t.Errorf("the corpus records %d omitted fields, expected 2",
			len(corpus.Accepted[0].PublicOmits))
	}
}

// TestAnInvalidRoleIsRefusedAndWritesNothing.
//
// The live `_validRole` throws for every one of these, an ABSENT role included —
// there is no default. `rbac.PlanUserGrants` maps an unknown role to `viewer`
// instead; the two are deliberately different and must not be merged.
//
// The second half is what matters: a port validating AFTER the append leaves a
// user with an invalid role in the file, and this check is the only thing
// between a typo and an administrator.
func TestAnInvalidRoleIsRefusedAndWritesNothing(t *testing.T) {
	corpus := loadCreateCorpus(t)
	for _, c := range corpus.Refused {
		if c.Message == "" {
			t.Fatalf("%s: the corpus says live ACCEPTED it; this test assumes it refuses", c.Why)
		}
		if !c.WroteNothing {
			t.Fatalf("%s: the corpus says live wrote a record anyway", c.Why)
		}
	}

	s, dir := emptyStore(t)
	path := filepath.Join(dir, "users.json")
	// Go has no `undefined`, so an absent role arrives as "". The live message
	// reads `Invalid role: undefined` and this one reads `Invalid role: `; the
	// WORD differs and the shape does not, which is why the assertions below are
	// on the prefix and the valid list rather than on the whole string.
	for _, role := range []string{"superuser", "", "Admin", "ADMIN", "viewer ", "root"} {
		_, err := s.CreateUser(NewUser{Username: "x", Password: "an-invented-password", Role: role})
		if err == nil {
			t.Errorf("role %q was ACCEPTED", role)
			continue
		}
		var bad *ErrInvalidRole
		if !errors.As(err, &bad) {
			t.Errorf("role %q gave %T, want *ErrInvalidRole", role, err)
			continue
		}
		if !strings.HasPrefix(err.Error(), "Invalid role: ") {
			t.Errorf("role %q: %q does not start with the live prefix", role, err)
		}
		// THE MESSAGE NAMES THE VALID LIST, which is how an operator learns a
		// role was added.
		for _, r := range corpus.Roles {
			if !strings.Contains(err.Error(), r) {
				t.Errorf("role %q: the message does not name %q: %q", role, r, err)
			}
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("role %q was refused but users.json was created anyway", role)
		}
	}

	// And the three valid ones are accepted, or the loop above proves only that
	// everything is refused.
	for _, role := range corpus.Roles {
		if _, err := s.CreateUser(NewUser{
			Username: "ok-" + role, Password: "an-invented-password", Role: role,
		}); err != nil {
			t.Errorf("role %q was refused: %v", role, err)
		}
	}
}

// TestTheRolesListMatchesLive.
//
// A hand-maintained copy would silently reject a role added upstream. Not
// hypothetical: `operator` is the third and most recent, and the live comment
// records that adding it was only safe once the coercion became validation —
// under the old `role === 'viewer' ? 'viewer' : 'admin'` it would have created
// administrators.
func TestTheRolesListMatchesLive(t *testing.T) {
	corpus := loadCreateCorpus(t)
	if strings.Join(Roles, ",") != strings.Join(corpus.Roles, ",") {
		t.Errorf("Roles is %v, live exports %v — including the ORDER, which is ascending privilege",
			Roles, corpus.Roles)
	}
}

// TestTheUsernameIsTrimmedAndTheRouterListIsNeverNull.
//
// Both are coercions the live side does on the way in. The `[]` one matters
// beyond tidiness: Go writes a nil slice as `null`, and while
// `Array.isArray(x) ? x : []` happens to read that as unrestricted — the same
// answer — it is the same answer by luck rather than by agreement.
func TestTheUsernameIsTrimmedAndTheRouterListIsNeverNull(t *testing.T) {
	s, dir := emptyStore(t)
	pub, err := s.CreateUser(NewUser{
		Username: "  di  ", Password: "an-invented-password", Role: "viewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pub["username"] != "di" {
		t.Errorf("username is %q, want %q", pub["username"], "di")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "users.json"))
	if !strings.Contains(string(b), `"allowedRouterIds": []`) {
		t.Errorf("allowedRouterIds was not written as []:\n%s", b)
	}
	if strings.Contains(string(b), `"allowedRouterIds": null`) {
		t.Errorf("allowedRouterIds was written as null:\n%s", b)
	}
}

// TestCreateUserAppendsAndLeavesEveryOtherRecordAlone.
//
// The same property `SetPassword` has, for the same reason: existing records are
// `json.RawMessage`, so they reach disk byte for byte. A port decoding them would
// reorder every key in the file the first time an administrator added a second
// user.
func TestCreateUserAppendsAndLeavesEveryOtherRecordAlone(t *testing.T) {
	st, dir := usersFixture(t, "an-invented-password")
	path := filepath.Join(dir, "users.json")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var beforeRecs []json.RawMessage
	if err := json.Unmarshal(before, &beforeRecs); err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateUser(NewUser{
		Username: "new", Password: "another-invented-password", Role: "operator",
	}); err != nil {
		t.Fatal(err)
	}

	after := assertNodeShape(t, path, "CreateUser")
	var afterRecs []json.RawMessage
	if err := json.Unmarshal(after, &afterRecs); err != nil {
		t.Fatal(err)
	}
	if len(afterRecs) != len(beforeRecs)+1 {
		t.Fatalf("%d records after, %d before", len(afterRecs), len(beforeRecs))
	}
	for i := range beforeRecs {
		assertRecordUnchanged(t, i, beforeRecs[i], afterRecs[i])
	}
}

// assertRecordUnchanged compares a record's CONTENT and its KEY ORDER, and
// deliberately not its whitespace.
//
// Re-indentation is not drift: `JSON.stringify(x, null, 2)` expands a nested
// array onto several lines exactly as Go's encoder does, so a fixture written
// with `["r-A", "r-B"]` on one line is the outlier rather than the port. What
// must not change is which keys are present, in what order, holding what — and
// key order is precisely what a byte comparison of differently-indented text
// cannot isolate.
func assertRecordUnchanged(t *testing.T, i int, before, after json.RawMessage) {
	t.Helper()
	if bo, ao := keyOrderIn(t, before), keyOrderIn(t, after); strings.Join(bo, ",") != strings.Join(ao, ",") {
		t.Errorf("record %d had its keys REORDERED:\n  before: %v\n  after:  %v", i, bo, ao)
	}
	var b, a map[string]any
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &a); err != nil {
		t.Fatal(err)
	}
	if len(b) != len(a) {
		t.Errorf("record %d has %d fields, had %d", i, len(a), len(b))
	}
	for k, want := range b {
		got, ok := a[k]
		if !ok {
			t.Errorf("record %d lost %q", i, k)
			continue
		}
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		if string(wb) != string(gb) {
			t.Errorf("record %d field %q changed: %s -> %s", i, k, wb, gb)
		}
	}
}

// TestUserCountDoesNotReadAnErrorAsZero.
//
// It is what makes `POST /api/users/setup` refuse a second call. The live
// `_readFile` returns `[]` for an unreadable file, so `userCount()` answers 0 —
// which RE-OPENS an unauthenticated route that mints an administrator. This port
// returns the error instead and leaves the refusal to the caller.
//
// A deliberate divergence, and the only one in this file.
func TestUserCountDoesNotReadAnErrorAsZero(t *testing.T) {
	s, dir := emptyStore(t)
	path := filepath.Join(dir, "users.json")

	// A MISSING file genuinely is zero users: the fresh-install state.
	n, err := s.UserCount()
	if err != nil || n != 0 {
		t.Errorf("missing file: got (%d, %v), want (0, nil)", n, err)
	}

	if _, err := s.CreateUser(NewUser{
		Username: "ann", Password: "an-invented-password", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.UserCount(); err != nil || n != 1 {
		t.Errorf("after one create: got (%d, %v), want (1, nil)", n, err)
	}

	// A CORRUPT file is not zero users. This is the case the live app gets wrong.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := s.UserCount(); err == nil {
		t.Errorf("a corrupt users.json returned (%d, nil). Answering 0 re-opens "+
			"POST /api/users/setup, which is unauthenticated and mints an administrator.", n)
	}

	// And an object where an array belongs — the shape a version wrapper would
	// introduce, which `store.go`'s header calls a security property.
	if err := os.WriteFile(path, []byte(`{"users":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := s.UserCount(); err == nil {
		t.Errorf("a wrapped users.json returned (%d, nil); it must not read as empty", n)
	}

	// AN UNREADABLE FILE, which is a different error from an unparseable one and
	// was untested until a mutation returning `(0, nil)` for it survived.
	//
	// A DIRECTORY where the file belongs, not a chmod: these tests run as ROOT in
	// the container, so `os.Chmod(path, 0)` does not stop root reading it and the
	// case would silently become a no-op. `ReadFile` on a directory fails with
	// something that is not `IsNotExist`, which is exactly the arm being checked.
	s2, dir2 := emptyStore(t)
	if err := os.Mkdir(filepath.Join(dir2, "users.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if n, err := s2.UserCount(); err == nil {
		t.Errorf("an unreadable users.json returned (%d, nil). Every error except "+
			"not-exist must surface: answering 0 re-opens POST /api/users/setup.", n)
	}
}

// TestACorruptFileIsNeverOverwritten.
//
// The live `_readFile` returns `[]` for anything that will not parse, so
// `createUser` appends to nothing and writes a one-user file over the top of a
// damaged one. Combined with the setup route, a corrupted users.json becomes an
// empty one that anybody can claim.
func TestACorruptFileIsNeverOverwritten(t *testing.T) {
	s, dir := emptyStore(t)
	path := filepath.Join(dir, "users.json")
	damaged := []byte(`[{"id":"u-1","username":"someone"`)
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(NewUser{
		Username: "attacker", Password: "an-invented-password", Role: "admin",
	}); err == nil {
		t.Error("a corrupt users.json was overwritten with a new administrator")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(damaged) {
		t.Errorf("the damaged file was modified:\n  %s", got)
	}
}
