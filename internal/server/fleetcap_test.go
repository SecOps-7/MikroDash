package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/rbac"
	"mikrodash/internal/store"

	_ "modernc.org/sqlite"
)

// A ROUTER PAST THE CAP IS REPORTED, NOT DROPPED.
//
// One request reads at most `fleetMaxRouters`, which is a fine limit and was a
// silent one: the extras simply were not in the answer. The pages build their
// columns from what came back, so a fleet of twenty compared nineteen of them
// and said nothing, and `Sync all missing` reported success for records it had
// never tried to write. `dnsFleetRouter.Error` exists precisely because a router
// missing from a fleet answer reads as a router with nothing on it.
//
// This also pins the OTHER reason a router can be absent — a caller with no
// grant on it — which is deliberately silent and must stay that way: telling the
// caller that an id they guessed exists is the cross-router probe issue #108
// closed elsewhere.

const fleetCapRoles = `
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL,
  actor_id TEXT, actor_name TEXT NOT NULL, actor_ip TEXT, action TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('app','router')), router_id TEXT,
  target_type TEXT, target_id TEXT, target_name TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN ('ok','denied','failed')), detail TEXT);
CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT, builtin INTEGER NOT NULL DEFAULT 0);
CREATE TABLE role_pages (role_id TEXT NOT NULL, page TEXT NOT NULL, access TEXT NOT NULL);
CREATE TABLE grants (
  id TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT NOT NULL, principal_id TEXT NOT NULL,
  scope_type TEXT NOT NULL, scope_id TEXT,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT, role TEXT);
CREATE TABLE group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);
INSERT INTO roles (id, name, builtin) VALUES ('role-r','r',0);
INSERT INTO role_pages (role_id, page, access) VALUES ('role-r','dns','read');
`

// fleetCapServer builds a server holding `n` routers, with the caller granted
// dns:read on all but the last. Ids sort as r00, r01 … so the order the request
// asks in is the order the answer should keep.
func fleetCapServer(t *testing.T, n int) (*Server, []string) {
	t.Helper()
	dir := t.TempDir()

	ids := make([]string, 0, n)
	routers := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("r%02d", i)
		ids = append(ids, id)
		routers = append(routers, map[string]any{
			"id": id, "label": "router-" + id, "host": "198.51.100." + fmt.Sprint(i+1),
		})
	}
	write := func(name string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("routers.json", routers)
	write("users.json", []store.User{{ID: "u-1", Username: "someone", Role: "admin"}})

	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	grants := fleetCapRoles
	// EVERY ROUTER BUT THE LAST, so the permission drop and the cap can be told
	// apart in the same answer.
	for _, id := range ids[:len(ids)-1] {
		grants += fmt.Sprintf(
			"INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)"+
				" VALUES ('user','u-1','router','%s','role-r');\n", id)
	}
	if _, err := h.Exec(grants); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	known := make([]rbac.Router, 0, n)
	for _, id := range ids {
		known = append(known, rbac.Router{ID: id})
	}
	return &Server{store: st, auditDB: database,
		rbac: rbac.New(database, func() []rbac.Router { return known })}, ids
}

func TestRoutersPastTheCapAreReportedRatherThanDropped(t *testing.T) {
	const n = fleetMaxRouters + 4
	s, ids := fleetCapServer(t, n)
	sess := &Session{Username: "someone", AuthMode: "modern"}

	kept, over := s.fleetTargets(sess, ids, "dns", "read")

	if len(kept) != fleetMaxRouters {
		t.Fatalf("%d routers read, want %d", len(kept), fleetMaxRouters)
	}
	// n-1 are granted; the cap keeps the first 16, so 3 of the granted ones are
	// over and the ungranted last one is absent from both lists.
	if len(over) != n-1-fleetMaxRouters {
		t.Fatalf("%d routers reported as unread, want %d", len(over), n-1-fleetMaxRouters)
	}
	for i, k := range kept {
		if k.ID != ids[i] {
			t.Fatalf("kept[%d] = %q, want %q — the answer must keep the order asked in",
				i, k.ID, ids[i])
		}
		if !strings.HasPrefix(k.Label, "router-") {
			t.Errorf("kept[%d] has no label to draw: %q", i, k.Label)
		}
	}

	seen := map[string]bool{}
	for _, k := range append(append([]fleetTarget{}, kept...), over...) {
		seen[k.ID] = true
	}
	if seen[ids[n-1]] {
		t.Error("a router this caller has no grant on was named in the answer")
	}
	for _, id := range ids[:n-1] {
		if !seen[id] {
			t.Errorf("%s is neither read nor reported — it vanished, which reads as a "+
				"router with nothing on it", id)
		}
	}
}
