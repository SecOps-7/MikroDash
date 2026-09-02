package db

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

// schemaDDL is copied VERBATIM from migration 11 in src/db.js, CHECK constraints
// included. Copied rather than simplified on purpose: the constraints are what
// make "scope must be app or router" a property of the database instead of a
// convention, and a test schema without them would pass happily while the real
// one rejected the insert.
const schemaDDL = `
CREATE TABLE audit_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          INTEGER NOT NULL,
  actor_id    TEXT,
  actor_name  TEXT NOT NULL,
  actor_ip    TEXT,
  action      TEXT NOT NULL,
  scope       TEXT NOT NULL CHECK (scope IN ('app','router')),
  router_id   TEXT,
  target_type TEXT,
  target_id   TEXT,
  target_name TEXT,
  outcome     TEXT NOT NULL CHECK (outcome IN ('ok','denied','failed')),
  detail      TEXT
);
CREATE INDEX idx_audit_ts        ON audit_events(ts);
CREATE INDEX idx_audit_router_ts ON audit_events(router_id, ts);
CREATE INDEX idx_audit_actor_ts  ON audit_events(actor_name, ts);
`

// newDB builds a throwaway database at the given schema version and returns its
// directory. Never /data: every test here writes only into t.TempDir().
func newDB(t *testing.T, version int, withAudit bool) string {
	t.Helper()
	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := h.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if version > 0 {
		if _, err := h.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (?, 0)`, version); err != nil {
			t.Fatal(err)
		}
	}
	if withAudit {
		if _, err := h.Exec(schemaDDL); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func openTest(t *testing.T, dir string) *DB {
	t.Helper()
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// ── Open refuses what it cannot safely use ───────────────────────────────────

// ── RE-AIMED 2026-09-02, DELIBERATELY ──────────────────────────────────────
//
// This asserted that an absent database is REFUSED, and it was right while the
// Node app owned the schema: a missing file meant the wrong /data, and creating
// an empty one would have hidden that.
//
// Node is gone, and the refusal outlived its reason. Nothing created the file
// any more, so a fresh install ran with no audit, no history and no reports —
// and, because `grantFirstAdmin` needs the grants table, with a first
// administrator who held no grants and could not add a router at all. That is
// issue #124, reported by users installing from the RouterOS container
// catalogue, where /data is new by definition.
//
// The question this test asks is inverted; the two either side of it are not.
// `TestOpenRefusesOldSchema` still guards the case this port genuinely cannot
// handle — a database that EXISTS and is too old to use.
func TestOpenCreatesAMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("a fresh /data failed to open, which is an install that cannot be "+
			"configured at all: %v", err)
	}
	defer d.Close()

	// USABLE, not merely present: below MinSchema, Open would have refused the
	// database it had just written.
	v, err := d.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v < MinSchema {
		t.Errorf("created a database at schema v%d, below the v%d floor", v, MinSchema)
	}

	// And the tables the first administrator's grant needs are really there —
	// the ones whose absence made the account exist and be able to do nothing.
	if _, err := d.sql.Exec(
		`INSERT INTO roles (id, name, builtin, created_at) VALUES ('r1','Test',0,0)`); err != nil {
		t.Fatalf("roles is missing or unusable: %v", err)
	}
	if _, err := d.sql.Exec(
		`INSERT INTO grants (id, principal_type, principal_id, role_id, scope_type, scope_id, created_at)
		 VALUES ('g1','user','u1','r1','global','',0)`); err != nil {
		t.Errorf("grants is missing or unusable — grantFirstAdmin would fail: %v", err)
	}
}

// TestOpenRefusesOldSchema is the guard keeping this side out of the migration
// business: below v11 audit_events does not exist, and the honest answer is to
// refuse rather than create it and disagree with Node about what the schema is.
func TestOpenRefusesOldSchema(t *testing.T) {
	for _, v := range []int{0, 1, MinSchema - 1} {
		if _, err := Open(newDB(t, v, false)); err == nil {
			t.Errorf("opened a v%d database; want refusal", v)
		}
	}
}

func TestOpenAcceptsAtOrAboveMinSchema(t *testing.T) {
	// A floor, not an equality — Node migrates forward and this must not break
	// every time it does.
	for _, v := range []int{MinSchema, MinSchema + 3, 99} {
		d, err := Open(newDB(t, v, true))
		if err != nil {
			t.Errorf("v%d refused: %v", v, err)
			continue
		}
		if got, _ := d.SchemaVersion(); got != v {
			t.Errorf("SchemaVersion() = %d, want %d", got, v)
		}
		d.Close()
	}
}

// ── insert ───────────────────────────────────────────────────────────────────

func TestInsertRoundTrip(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	want := Event{
		TS: 1700000000000, ActorID: "u-1", ActorName: "someone", ActorIP: "198.51.100.4",
		Action: "dns.update", Scope: "router", RouterID: "r-1",
		TargetType: "dnsStatic", TargetID: "*1", TargetName: "host.example",
		Outcome: "ok", Detail: `{"changes":[{"field":"address","from":"a","to":"b"}]}`,
	}
	if err := d.InsertAuditEvent(want); err != nil {
		t.Fatalf("insert: %v", err)
	}

	page, err := d.QueryAuditEvents(Query{RouterIDs: []string{"r-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Total != 1 {
		t.Fatalf("got %d rows / total %d, want 1/1", len(page.Rows), page.Total)
	}
	got := page.Rows[0]
	if got.Action != want.Action || got.ActorName != want.ActorName || got.TS != want.TS {
		t.Errorf("row = %+v", got)
	}
	// DETAIL COMES BACK AS A STRING HOLDING JSON, not as embedded JSON.
	//
	// This assertion used to say the opposite, in those words, and passed —
	// because the code it pinned was wrong in the same direction. better-sqlite3
	// hands db.js a TEXT column as a JS string, `res.json` sends a string, and
	// the page's detailCell does `JSON.parse(raw)`. Sending an object made that
	// parse throw and every detail cell render "[object Object]". Found by the
	// DOM comparison against the lifted live renderer, which is the only check
	// positioned to see it: both halves agreed with each other and neither
	// agreed with Node.
	if got.Detail == nil {
		t.Fatal("detail is nil; want a string holding JSON")
	}
	var parsed struct {
		Changes []map[string]any `json:"changes"`
	}
	if err := json.Unmarshal([]byte(*got.Detail), &parsed); err != nil {
		t.Fatalf("detail is not JSON: %v (%s)", err, *got.Detail)
	}
	if len(parsed.Changes) != 1 {
		t.Errorf("detail.changes = %v", parsed.Changes)
	}
}

// TestEmptyStringsBecomeNull matches `ev.actorId || null`. A column holding ""
// where Node holds NULL would make the two apps' rows differ on a field the
// Audit page renders as "—".
func TestEmptyStringsBecomeNull(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	if err := d.InsertAuditEvent(Event{TS: 1, Action: "auth.login", ActorName: "x"}); err != nil {
		t.Fatal(err)
	}
	page, _ := d.QueryAuditEvents(Query{IncludeApp: true})
	r := page.Rows[0]
	for name, v := range map[string]*string{
		"actor_id": r.ActorID, "actor_ip": r.ActorIP, "router_id": r.RouterID,
		"target_type": r.TargetType, "target_id": r.TargetID, "target_name": r.TargetName,
	} {
		if v != nil {
			t.Errorf("%s = %q, want NULL", name, *v)
		}
	}
	// A NULL detail column is a nil pointer, which marshals to JSON null —
	// the same thing better-sqlite3's null becomes through `res.json`.
	if r.Detail != nil {
		t.Errorf("detail = %q, want nil", *r.Detail)
	}
}

// TestDefaultsSatisfyTheCheckConstraints is why the DDL above keeps its CHECKs:
// an unset scope or outcome must become a legal value rather than a failed
// insert, because a failed insert loses the event this table exists to hold.
func TestDefaultsSatisfyTheCheckConstraints(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	if err := d.InsertAuditEvent(Event{TS: 1, Action: "a"}); err != nil {
		t.Fatalf("insert with no actor/scope/outcome: %v", err)
	}
	if err := d.InsertAuditEvent(Event{TS: 2, Action: "b", Scope: "nonsense"}); err != nil {
		t.Fatalf("a bad scope should clamp, not fail: %v", err)
	}
	page, _ := d.QueryAuditEvents(Query{IncludeApp: true})
	for _, r := range page.Rows {
		if r.Scope != "app" {
			t.Errorf("scope = %q, want app", r.Scope)
		}
		if r.ActorName != "system" {
			t.Errorf("actor_name = %q, want system", r.ActorName)
		}
		if r.Outcome != "ok" {
			t.Errorf("outcome = %q, want ok", r.Outcome)
		}
	}
}

// ── visibility, which is the security-relevant half ──────────────────────────

// TestNoVisibilityYieldsNothing is the bug class the Node version was written to
// avoid: an empty allow-list meaning "unrestricted" instead of "nothing". A
// caller with no routers and no app scope must see zero rows, not every row.
func TestNoVisibilityYieldsNothing(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	seed(t, d)

	page, err := d.QueryAuditEvents(Query{}) // no routers, no app scope
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 0 || page.Total != 0 {
		t.Fatalf("a caller with no visibility saw %d rows (total %d) — it must see none",
			len(page.Rows), page.Total)
	}
}

func TestVisibilityScoping(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	seed(t, d)

	for _, tc := range []struct {
		name    string
		q       Query
		actions []string
	}{
		{"app only", Query{IncludeApp: true}, []string{"app.two", "app.one"}},
		{"one router only", Query{RouterIDs: []string{"r-1"}}, []string{"r1.two", "r1.one"}},
		{"app plus a router", Query{IncludeApp: true, RouterIDs: []string{"r-2"}},
			[]string{"r2.one", "app.two", "app.one"}},
		{"unknown router", Query{RouterIDs: []string{"nope"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := d.QueryAuditEvents(tc.q)
			if err != nil {
				t.Fatal(err)
			}
			got := actionsOf(page)
			if len(got) != len(tc.actions) {
				t.Fatalf("got %v, want %v", got, tc.actions)
			}
			for i := range got {
				if got[i] != tc.actions[i] {
					t.Fatalf("got %v, want %v", got, tc.actions)
				}
			}
		})
	}
}

// TestFilterCannotWidenVisibility: a routerId filter naming a router the caller
// cannot see must return nothing, not that router's rows.
func TestFilterCannotWidenVisibility(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	seed(t, d)

	page, err := d.QueryAuditEvents(Query{RouterIDs: []string{"r-1"}, RouterID: "r-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 0 {
		t.Fatalf("a filter widened visibility: %v", actionsOf(page))
	}
}

// ── filters, ordering and paging ─────────────────────────────────────────────

func TestActionIsAPrefixMatch(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	seed(t, d)
	page, _ := d.QueryAuditEvents(Query{IncludeApp: true, RouterIDs: []string{"r-1", "r-2"}, Action: "r1"})
	if got := actionsOf(page); len(got) != 2 {
		t.Errorf("action prefix 'r1' matched %v", got)
	}
}

func TestOrderingIsNewestFirst(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	// Same ts on purpose: the tiebreak is `id DESC`, so the later insert wins.
	for _, a := range []string{"first", "second"} {
		if err := d.InsertAuditEvent(Event{TS: 500, Action: a, ActorName: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	page, _ := d.QueryAuditEvents(Query{IncludeApp: true})
	if got := actionsOf(page); got[0] != "second" {
		t.Errorf("order = %v, want the later id first", got)
	}
}

func TestPagingAndLimitClamp(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	for i := 0; i < 5; i++ {
		if err := d.InsertAuditEvent(Event{TS: int64(i), Action: "a", ActorName: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	page, _ := d.QueryAuditEvents(Query{IncludeApp: true, Limit: 2, Offset: 1})
	if len(page.Rows) != 2 || page.Total != 5 {
		t.Errorf("rows=%d total=%d, want 2/5", len(page.Rows), page.Total)
	}

	// `parseInt(o.limit) || 200` — zero is falsy in JavaScript and means the
	// default, not a page of nothing.
	if p, _ := d.QueryAuditEvents(Query{IncludeApp: true, Limit: 0}); p.Limit != 200 {
		t.Errorf("limit 0 gave %d, want the 200 default", p.Limit)
	}
	if p, _ := d.QueryAuditEvents(Query{IncludeApp: true, Limit: 99999}); p.Limit != 1000 {
		t.Errorf("limit 99999 gave %d, want the 1000 cap", p.Limit)
	}
}

func TestFacets(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	seed(t, d)
	f, err := d.AuditFacets()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Actions) != 5 {
		t.Errorf("actions = %v", f.Actions)
	}
	if len(f.Actors) != 2 || f.Actors[0] != "alice" || f.Actors[1] != "bob" {
		t.Errorf("actors = %v, want [alice bob] in order", f.Actors)
	}
}

func TestFacetsOnEmptyTable(t *testing.T) {
	d := openTest(t, newDB(t, 14, true))
	f, err := d.AuditFacets()
	if err != nil {
		t.Fatal(err)
	}
	// Empty slices, never nil: these marshal to [] and the filter dropdowns
	// iterate them.
	b, _ := json.Marshal(f)
	if string(b) != `{"actors":[],"actions":[]}` {
		t.Errorf("marshalled as %s, want empty arrays", b)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func seed(t *testing.T, d *DB) {
	t.Helper()
	for _, r := range []Event{
		{TS: 10, Action: "app.one", ActorName: "alice"},
		{TS: 20, Action: "app.two", ActorName: "bob"},
		{TS: 30, Action: "r1.one", ActorName: "alice", Scope: "router", RouterID: "r-1"},
		{TS: 40, Action: "r1.two", ActorName: "bob", Scope: "router", RouterID: "r-1"},
		{TS: 50, Action: "r2.one", ActorName: "alice", Scope: "router", RouterID: "r-2"},
	} {
		if err := d.InsertAuditEvent(r); err != nil {
			t.Fatal(err)
		}
	}
}

func actionsOf(p Page) []string {
	out := make([]string, len(p.Rows))
	for i, r := range p.Rows {
		out[i] = r.Action
	}
	return out
}
