package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A move rewrites routers.json, which holds every router's credentials and
// settings. These pin that it moves exactly one record and changes nothing
// else about any of them.

func orderFixture(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	// `quirk` is the point of the fixture: a key this package's Router struct
	// does not model. If a move ever decodes a record, it disappears.
	const src = `[
  {"id":"a","label":"Alpha","host":"198.51.100.1","quirk":{"kept":true,"n":7}},
  {"id":"b","label":"Bravo","host":"198.51.100.2"},
  {"id":"c","label":"Charlie","host":"198.51.100.3"}
]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Store{Dir: dir}
}

func orderOf(t *testing.T, s *Store) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func TestAMoveReordersExactlyOneRouter(t *testing.T) {
	s := orderFixture(t)

	moved, err := s.MoveRouter("c", true)
	if err != nil || !moved {
		t.Fatalf("move c up: moved=%v err=%v", moved, err)
	}
	if got := strings.Join(orderOf(t, s), ","); got != "a,c,b" {
		t.Errorf("after moving c up the order is %q, want \"a,c,b\"", got)
	}

	moved, err = s.MoveRouter("a", false)
	if err != nil || !moved {
		t.Fatalf("move a down: moved=%v err=%v", moved, err)
	}
	if got := strings.Join(orderOf(t, s), ","); got != "c,a,b" {
		t.Errorf("after moving a down the order is %q, want \"c,a,b\"", got)
	}
}

// AT THE ENDS IT IS A NO-OP, NOT AN ERROR, and it must not write.
//
// The arrows are always drawn, so Up on the top row is an ordinary click. The
// false tells the caller to skip the write and the audit entry; if this
// returned true the file would be rewritten and an audit row recorded for a
// move that did not happen.
func TestMovingPastAnEndDoesNothing(t *testing.T) {
	s := orderFixture(t)
	path := filepath.Join(s.Dir, "routers.json")
	before, _ := os.ReadFile(path)

	for _, c := range []struct {
		id string
		up bool
	}{{"a", true}, {"c", false}} {
		moved, err := s.MoveRouter(c.id, c.up)
		if err != nil {
			t.Fatalf("move %s: %v", c.id, err)
		}
		if moved {
			t.Errorf("moving %s past the end reported a move", c.id)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a no-op move rewrote routers.json")
	}
}

// A FIELD THIS PACKAGE DOES NOT MODEL SURVIVES.
//
// The records are carried as json.RawMessage and never decoded, the idiom
// `appendRouter` and `UpdateRouter` already use. A move that decoded them would
// silently drop every key the Router struct has no field for - and this file
// holds credentials, backup settings and per-router alert configuration.
func TestAMoveKeepsFieldsThisPackageDoesNotModel(t *testing.T) {
	s := orderFixture(t)
	if _, err := s.MoveRouter("a", false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	var alpha map[string]any
	for _, r := range recs {
		if r["id"] == "a" {
			alpha = r
		}
	}
	if alpha == nil {
		t.Fatal("router a is gone after being moved")
	}
	q, ok := alpha["quirk"].(map[string]any)
	if !ok {
		t.Fatalf("`quirk` did not survive the move: %+v", alpha)
	}
	if q["kept"] != true || q["n"] != float64(7) {
		t.Errorf("`quirk` survived but changed: %+v", q)
	}
}

func TestMovingARouterThatIsNotThereIsAnError(t *testing.T) {
	s := orderFixture(t)
	if _, err := s.MoveRouter("nope", true); err == nil {
		t.Error("moving an unknown router succeeded - the caller believed it had a router")
	}
	if _, err := s.MoveRouter("", true); err == nil {
		t.Error("moving an empty id succeeded")
	}
}

// THE MIGRATION RUNS TWICE AND WRITES ONCE.
//
// It is the old behaviour written down: an install where nobody pressed
// Activate opened on `routers[0]`, so `routers[0]` becomes its primary. A
// second start must change nothing, or every restart would drag the primary
// back to the top of the list and undo the operator's choice.
func TestAdoptingAPrimaryIsIdempotent(t *testing.T) {
	s := orderFixture(t)
	if err := os.WriteFile(filepath.Join(s.Dir, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	id, wrote, err := s.AdoptPrimaryRouter()
	if err != nil || !wrote || id != "a" {
		t.Fatalf("first run: id=%q wrote=%v err=%v, want a/true/nil", id, wrote, err)
	}
	if got := s.ActiveRouterID(); got != "a" {
		t.Fatalf("the primary was not recorded: %q", got)
	}

	// The operator now chooses a different one, as the checkbox does.
	cfg, _ := s.Settings()
	merged, kept := Merge(cfg, os.LookupEnv, s)
	merged["activeRouterId"] = "c"
	if err := SaveSettings(s.Dir, merged, Settings{"activeRouterId": "c"}, kept, s); err != nil {
		t.Fatal(err)
	}

	id, wrote, err = s.AdoptPrimaryRouter()
	if err != nil || wrote {
		t.Fatalf("second run: wrote=%v err=%v - a restart must not move the primary", wrote, err)
	}
	if got := s.ActiveRouterID(); got != "c" {
		t.Errorf("a restart dragged the primary back to %q, losing the operator's choice", got)
	}
}

// A PRIMARY NAMING A DELETED ROUTER IS REPAIRED.
//
// Same broken state by a different route: the setting survives the router it
// names. Left alone, every session would fall back and the checkbox would show
// nothing ticked on any row.
func TestAPrimaryThatNoLongerExistsIsReplaced(t *testing.T) {
	s := orderFixture(t)
	if err := os.WriteFile(filepath.Join(s.Dir, "settings.json"),
		[]byte(`{"activeRouterId":"deleted-long-ago"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, wrote, err := s.AdoptPrimaryRouter()
	if err != nil || !wrote || id != "a" {
		t.Fatalf("id=%q wrote=%v err=%v, want a/true/nil", id, wrote, err)
	}
}

// AN EMPTY FLEET IS NOT AN ERROR AND MUST NOT WRITE.
//
// A first run has no routers at all. Writing an empty primary would put a key
// in settings.json that names nothing, and the adoption would then think it had
// already run.
func TestAdoptingAPrimaryOnAnEmptyFleetDoesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: dir}
	id, wrote, err := s.AdoptPrimaryRouter()
	if err != nil || wrote || id != "" {
		t.Fatalf("id=%q wrote=%v err=%v, want \"\"/false/nil", id, wrote, err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "settings.json")); serr == nil {
		t.Error("adopting on an empty fleet created settings.json")
	}
}
