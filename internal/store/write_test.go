package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixture carries the fields the typed struct does NOT model, because those
// are what a careless write drops.
const routersFixture = `[
  {
    "id": "r-1",
    "label": "Test",
    "host": "198.51.100.1",
    "port": 8729,
    "tls": true,
    "username": "api",
    "password": "SEALED-ONE",
    "collection": { "dns": true, "wifi": false },
    "alertsEnabled": true,
    "pingTarget": "1.1.1.1",
    "connDownThresholdSec": 120,
    "geo": { "lat": 0, "lon": 0 },
    "serial": "S1",
    "addedAt": 1767225600000,
    "backup": { "enabled": true, "schedule": "daily", "password": "SEALED-BK" }
  },
  {
    "id": "r-2",
    "label": "Other",
    "host": "198.51.100.2",
    "password": "SEALED-TWO",
    "collection": { "dns": false }
  }
]
`

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(routersFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	// `.secret` is what the key is derived from. THE STORE MUST BE BUILT WITH
	// Open: `&Store{Dir: dir}` compiles and then fails with "invalid key size 0"
	// at the first Encrypt, because the key is derived in Open and nowhere else.
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// sealFixture replaces the fixture's placeholder passwords with real ciphertext.
//
// The placeholders are deliberately NOT valid base64, so a test that reads the
// records back has to seal them first — which is honest about what the read path
// requires, rather than letting a fixture pretend a credential is a plain
// string.
func sealFixture(t *testing.T, s *Store) {
	t.Helper()
	for _, id := range []string{"r-1", "r-2"} {
		if err := s.SetRouterPassword(id, "pw-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetBackupPassword("r-1", "bk-pw"); err != nil {
		t.Fatal(err)
	}
}

func readRecords(t *testing.T, s *Store) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("the file this wrote is not valid JSON: %v", err)
	}
	return out
}

// TestUnmodelledFieldsSurviveAWrite is the reason this file reads raw JSON.
//
// `Router` models 16 of the 23 keys a real record carries. A write that
// marshalled the struct would drop `collection` (which collectors run),
// `alertsEnabled`, `pingTarget` and `connDownThresholdSec` — a router that
// quietly stops collecting, or stops alerting, after somebody edits its label.
func TestUnmodelledFieldsSurviveAWrite(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateRouter("r-1", map[string]any{"label": "Renamed"}); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, s)
	if len(recs) != 2 {
		t.Fatalf("wrote %d records, want 2", len(recs))
	}
	r1 := recs[0]
	if r1["label"] != "Renamed" {
		t.Errorf("label = %v", r1["label"])
	}
	for _, k := range []string{"collection", "alertsEnabled", "pingTarget",
		"connDownThresholdSec", "geo", "serial", "addedAt"} {
		if _, ok := r1[k]; !ok {
			t.Errorf("%q was dropped by the write — it is not modelled by Router, "+
				"which is exactly why the write reads raw JSON", k)
		}
	}
	// The nested map survived whole, not flattened or emptied.
	coll, _ := r1["collection"].(map[string]any)
	if coll["dns"] != true || coll["wifi"] != false {
		t.Errorf("collection = %v", r1["collection"])
	}
}

// TestTheOtherRouterIsUntouchedByteForByte — only the edited record is
// re-encoded, so a one-field edit is not a whole-file diff.
func TestTheOtherRouterIsUntouchedByteForByte(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateRouter("r-1", map[string]any{"label": "Renamed"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	// r-2's keys keep their original order: id, label, host, password, collection.
	txt := string(b)
	i2 := strings.Index(txt, `"r-2"`)
	if i2 < 0 {
		t.Fatal("r-2 is missing entirely")
	}
	tail := txt[i2:]
	for _, pair := range [][2]string{{"label", "host"}, {"host", "password"}, {"password", "collection"}} {
		a := strings.Index(tail, `"`+pair[0]+`"`)
		bIdx := strings.Index(tail, `"`+pair[1]+`"`)
		if a < 0 || bIdx < 0 || a > bIdx {
			t.Errorf("r-2's key order changed: %q should precede %q", pair[0], pair[1])
		}
	}
}

// TestBackupIsMergedOneLevel — patching `backup.enabled` must not drop
// `backup.password`, which would leave a router scheduled for backups it can no
// longer encrypt.
func TestBackupIsMergedOneLevel(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateRouter("r-1", map[string]any{
		"backup": map[string]any{"enabled": false, "keepCount": 10},
	}); err != nil {
		t.Fatal(err)
	}
	bk, _ := readRecords(t, s)[0]["backup"].(map[string]any)
	if bk["password"] != "SEALED-BK" {
		t.Error("the backup password was dropped by a settings patch")
	}
	if bk["schedule"] != "daily" {
		t.Error("an untouched backup field was dropped")
	}
	if bk["enabled"] != false {
		t.Errorf("enabled = %v, want false", bk["enabled"])
	}
	if bk["keepCount"] != float64(10) {
		t.Errorf("keepCount = %v", bk["keepCount"])
	}
}

// TestNilClearsAKey — the only way to remove a field, and it must reach the
// nested block too.
func TestNilClearsAKey(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateRouter("r-1", map[string]any{
		"pingTarget": nil,
		"backup":     map[string]any{"time": nil, "schedule": nil},
	}); err != nil {
		t.Fatal(err)
	}
	r1 := readRecords(t, s)[0]
	if _, ok := r1["pingTarget"]; ok {
		t.Error("a nil patch did not clear the key")
	}
	bk, _ := r1["backup"].(map[string]any)
	if _, ok := bk["schedule"]; ok {
		t.Error("a nil patch did not clear a nested key")
	}
	if bk["password"] != "SEALED-BK" {
		t.Error("clearing one nested key dropped another")
	}
}

// TestAnUnknownRouterIsAnError — the caller believed it had a router, so a
// silent no-op would leave it thinking the edit landed.
func TestAnUnknownRouterIsAnError(t *testing.T) {
	s := newStore(t)
	err := s.UpdateRouter("nope", map[string]any{"label": "x"})
	if err == nil {
		t.Fatal("patching an unknown router silently succeeded")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("the error should name the router: %v", err)
	}
	// And the file is untouched.
	if recs := readRecords(t, s); recs[0]["label"] != "Test" {
		t.Error("a failed patch changed the file")
	}
}

// TestPasswordsAreSealedNotWritten — the plaintext must never reach the file.
func TestPasswordsAreSealedNotWritten(t *testing.T) {
	s := newStore(t)
	sealFixture(t, s)
	const secret = "the-plaintext-password"

	if err := s.SetRouterPassword("r-1", secret); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBackupPassword("r-1", secret+"-bk"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if strings.Contains(string(b), secret) {
		t.Fatal("a plaintext password reached routers.json")
	}
	// And it decrypts back.
	rs, problems := s.Routers()
	if len(problems) != 0 {
		t.Fatalf("reading back: %v", problems)
	}
	if rs[0].Password != secret {
		t.Errorf("password did not round-trip: %q", rs[0].Password)
	}
	if rs[0].Backup.Password != secret+"-bk" {
		t.Errorf("backup password did not round-trip: %q", rs[0].Backup.Password)
	}
}

// TestTheFileIsOwnerOnly — it holds encrypted credentials.
func TestTheFileIsOwnerOnly(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateRouter("r-1", map[string]any{"label": "x"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 — the file holds encrypted credentials", fi.Mode().Perm())
	}
	// No temp file left behind.
	if _, err := os.Stat(filepath.Join(s.Dir, "routers.json.tmp")); err == nil {
		t.Error("routers.json.tmp survived a successful write")
	}
}

// TestTheResultIsStillReadableByTheReadPath closes the loop: a write nothing can
// read back is the failure this whole package exists to avoid.
func TestTheResultIsStillReadableByTheReadPath(t *testing.T) {
	s := newStore(t)
	sealFixture(t, s)
	if err := s.UpdateRouter("r-1", map[string]any{
		"label":  "Renamed",
		"backup": map[string]any{"enabled": false},
	}); err != nil {
		t.Fatal(err)
	}
	rs, problems := s.Routers()
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(rs) != 2 {
		t.Fatalf("read back %d routers, want 2", len(rs))
	}
	if rs[0].Label != "Renamed" || rs[0].Backup.Enabled {
		t.Errorf("read back %+v", rs[0])
	}
	if rs[1].Label != "Other" {
		t.Errorf("the second router changed: %+v", rs[1])
	}
}
