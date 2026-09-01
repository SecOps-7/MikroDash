package server

// The two database-cleanup routes.
//
// ── THE OPTIONS HALF IS DRIVEN BY THE LIVE CORPUS ──────────────────────────
//
// The purge corpus runs the live `_purgeScope` and `_purgeOpts` and
// records what each request resolves to, INCLUDING which refusal it earns and in
// what order. `TestPurgeOptsMatchTheLiveRoute` replays the half this harness can
// drive; the permission half needs a real RBAC resolver and is covered by
// `TestDBRoutesRequireGlobalAdmin`.
//
// ── AND THE REST IS WHAT ONLY AN HTTP TEST CAN SEE ─────────────────────────
//
//   - the dry run writes NOTHING;
//   - the preview and the delete agree, because they share a predicate;
//   - a purge that matched nothing does not vacuum;
//   - the audit row outlives the purge it describes.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/db"
)

// dbAdminDDL adds the four history tables the principals fixture lacks, with
// rows straddling the boundaries the predicate can draw.
//
// `alert_events` already exists from `alertTestDDL` and is aged by `fired_at`
// rather than `ts` — which is exactly the pairing that makes `events` two
// tables. Nothing to create; only `connectivity_events` gets a row here, so the
// two halves of that type are separable.
// (No backticks in this comment: it sits inside a Go raw string.)
const dbAdminDDL = `
CREATE TABLE IF NOT EXISTS ping_samples        (router_id TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS traffic_samples     (router_id TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS bandwidth_usage     (router_id TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS connectivity_events (router_id TEXT NOT NULL, ts INTEGER NOT NULL);
INSERT INTO ping_samples (router_id, ts) VALUES
  ('rtr-1', 1699827200000), ('rtr-1', 1699996400000), ('rtr-2', 1699827200000);
INSERT INTO traffic_samples (router_id, ts) VALUES
  ('rtr-1', 1699827200000), ('rtr-2', 1699827200000);
INSERT INTO bandwidth_usage (router_id, ts) VALUES ('rtr-1', 1699827200000);
INSERT INTO connectivity_events (router_id, ts) VALUES ('rtr-2', 1699827200000);
`

func dbAdminServer(t *testing.T, sess *Session) (*Server, *http.ServeMux, string) {
	t.Helper()
	s, mux, dir := usersWriteServer(t, sess, seedUsersJSON, dbAdminDDL)
	s.registerDBAdmin(mux)
	return s, mux, dir
}

func TestDBStatsReportsTheHistory(t *testing.T) {
	_, mux, _ := dbAdminServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "GET", "/api/db/stats", "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Total    int             `json:"total"`
		ByType   map[string]int  `json:"byType"`
		OldestTS *int64          `json:"oldestTs"`
		ByRouter []db.RouterRows `json:"byRouter"`
		Bytes    int64           `json:"bytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// SEVEN ROWS: 3 ping, 2 traffic, 1 bandwidth, 1 connectivity.
	if got.Total != 7 {
		t.Errorf("total = %d, want 7", got.Total)
	}
	if got.ByType["ping"] != 3 {
		t.Errorf("byType[ping] = %d, want 3", got.ByType["ping"])
	}
	// `events` is alert_events PLUS connectivity_events, and only the second has
	// a row — which is what makes the pairing observable rather than assumed.
	if got.ByType["events"] != 1 {
		t.Errorf("byType[events] = %d, want 1 (connectivity only)", got.ByType["events"])
	}
	if got.OldestTS == nil || *got.OldestTS != 1699827200000 {
		t.Errorf("oldestTs = %v, want 1699827200000", got.OldestTS)
	}
	if len(got.ByRouter) != 2 {
		t.Errorf("%d router(s) in byRouter, want 2", len(got.ByRouter))
	}
	if got.Bytes <= 0 {
		t.Errorf("bytes = %d", got.Bytes)
	}
}

func TestPurgeRefusalsMatchTheLiveRoute(t *testing.T) {
	for _, c := range []struct {
		why  string
		body string
		want int
		msg  string
	}{
		// THE AGE PRESETS.
		{"no age at all", `{"routerId":"rtr-1"}`, 400, "Invalid age filter"},
		{"age 2 is not a preset", `{"routerId":"rtr-1","olderThanDays":2}`, 400, "Invalid age filter"},
		{"age 400 is not a preset", `{"routerId":"rtr-1","olderThanDays":400}`, 400, "Invalid age filter"},
		{"a negative age", `{"routerId":"rtr-1","olderThanDays":-1}`, 400, "Invalid age filter"},
		{"an age that is not a number", `{"routerId":"rtr-1","olderThanDays":"week"}`, 400, "Invalid age filter"},
		// `Number("7")` is 7, which IS a preset.
		{"a numeric string age", `{"routerId":"rtr-1","olderThanDays":"7","dryRun":true}`, 200, ""},
		{"age 0 means everything", `{"routerId":"rtr-1","olderThanDays":0,"dryRun":true}`, 200, ""},
		{"age 365", `{"routerId":"rtr-1","olderThanDays":365,"dryRun":true}`, 200, ""},
		// THE TYPES.
		{"an empty type list is refused", `{"olderThanDays":0,"types":[]}`, 400, "No valid data types selected"},
		{"only invalid types", `{"olderThanDays":0,"types":["nope"]}`, 400, "No valid data types selected"},
		{"invalid types filtered from a good list",
			`{"olderThanDays":0,"types":["ping","nope"],"dryRun":true}`, 200, ""},
		{"an absent type list means all", `{"olderThanDays":0,"dryRun":true}`, 200, ""},
		{"a bad age with no router", `{"olderThanDays":999}`, 400, "Invalid age filter"},
		// `Number("")` IS ZERO in JavaScript, and zero is a preset — so an empty
		// string is ACCEPTED as "everything, regardless of age". Faithful rather
		// than tightened: the app this replaces accepts it, and a port that did
		// not would refuse a request the operator's browser could still send.
		{"an empty string age is zero, not NaN", `{"olderThanDays":"","dryRun":true}`, 200, ""},
	} {
		t.Run(c.why, func(t *testing.T) {
			s, mux, _ := dbAdminServer(t, &Session{AuthMode: "none", Username: "admin"})
			before := historyRowCount(t, s)
			w := doJSON(mux, "POST", "/api/db/purge", c.body, authed)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d — %s", w.Code, c.want, w.Body.String())
			}
			if c.msg != "" && !strings.Contains(w.Body.String(), c.msg) {
				t.Errorf("message is %s, want one containing %q", w.Body.String(), c.msg)
			}
			// A REFUSAL AND A DRY RUN BOTH WRITE NOTHING.
			if after := historyRowCount(t, s); after != before {
				t.Errorf("rows went %d -> %d for a request that should not have deleted anything",
					before, after)
			}
		})
	}
}

func historyRowCount(t *testing.T, s *Server) int {
	t.Helper()
	st, err := s.auditDB.Stats()
	if err != nil {
		t.Fatal(err)
	}
	return st.Total
}

// TestTheDryRunPreviewMatchesTheDelete.
//
// The predicate is one function so that the number an operator confirms is
// exact. This drives both halves through the ROUTE, which is where they could
// still diverge — a route that built different options for the preview would
// pass every test in `internal/db`.
func TestTheDryRunPreviewMatchesTheDelete(t *testing.T) {
	for _, body := range []string{
		`{"olderThanDays":0}`,
		`{"routerId":"rtr-1","olderThanDays":0}`,
		`{"olderThanDays":0,"types":["ping"]}`,
		`{"routerId":"rtr-2","olderThanDays":0,"types":["events"]}`,
	} {
		t.Run(body, func(t *testing.T) {
			_, mux, _ := dbAdminServer(t, &Session{AuthMode: "none", Username: "admin"})

			dry := doJSON(mux, "POST", "/api/db/purge",
				body[:len(body)-1]+`,"dryRun":true}`, authed)
			if dry.Code != 200 {
				t.Fatalf("preview: status %d: %s", dry.Code, dry.Body.String())
			}
			var preview struct {
				DryRun bool `json:"dryRun"`
				Total  int  `json:"total"`
			}
			if err := json.Unmarshal(dry.Body.Bytes(), &preview); err != nil {
				t.Fatal(err)
			}
			if !preview.DryRun {
				t.Error("the preview did not report itself as a dry run")
			}

			real := doJSON(mux, "POST", "/api/db/purge", body, authed)
			if real.Code != 200 {
				t.Fatalf("purge: status %d: %s", real.Code, real.Body.String())
			}
			var result struct {
				Deleted int `json:"deleted"`
			}
			if err := json.Unmarshal(real.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Deleted != preview.Total {
				t.Errorf("the preview promised %d row(s) and the purge removed %d. They share a "+
					"predicate precisely so this cannot happen.", preview.Total, result.Deleted)
			}
		})
	}
}

// TestAPurgeThatMatchedNothingDoesNotVacuum.
//
// `result.deleted > 0 ? db.vacuum() : { before, after: before }`. A VACUUM
// rewrites the whole file, which on a real multi-gigabyte history is minutes of
// I/O — and a purge that matched nothing has nothing to reclaim.
//
// ── NEITHER OBVIOUS OBSERVABLE CAN SEE THIS, AND BOTH WERE MEASURED ────────
//
//	bytesBefore == bytesAfter   a vacuum of an already-compact database returns
//	                            the size it started with: 77824 -> 77824.
//	the file's mtime            changes on EVERY purge, vacuum or not, because
//	                            the DELETE statements run either way.
//
// A mutant flipping the guard to `deleted >= 0` survived both. The property is
// about work NOT done, and work not done leaves nothing behind to assert on — so
// this counts the entry, via `VacuumCountForTest`. The byte check stays below it
// because it fails for a different reason: it catches a route reporting numbers
// it did not take from `before`.
func TestAPurgeThatMatchedNothingDoesNotVacuum(t *testing.T) {
	s, mux, _ := dbAdminServer(t, &Session{AuthMode: "none", Username: "admin"})
	if n := s.auditDB.VacuumCountForTest(); n != 0 {
		t.Fatalf("the fixture had already vacuumed %d time(s)", n)
	}

	w := doJSON(mux, "POST", "/api/db/purge", `{"routerId":"rtr-nope","olderThanDays":0}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Deleted     int   `json:"deleted"`
		BytesBefore int64 `json:"bytesBefore"`
		BytesAfter  int64 `json:"bytesAfter"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Deleted != 0 {
		t.Fatalf("deleted %d rows for a router with none", got.Deleted)
	}
	if n := s.auditDB.VacuumCountForTest(); n != 0 {
		t.Errorf("VACUUM ran %d time(s) for a purge that deleted nothing", n)
	}
	if got.BytesBefore != got.BytesAfter {
		t.Errorf("bytes went %d -> %d with nothing deleted", got.BytesBefore, got.BytesAfter)
	}
}

// And the other direction, so the pair cannot both be satisfied by a route that
// never vacuums at all — which is what a one-sided assertion above would allow.
func TestAPurgeThatDeletedRowsDoesVacuum(t *testing.T) {
	s, mux, _ := dbAdminServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/db/purge", `{"olderThanDays":0}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if n := s.auditDB.VacuumCountForTest(); n != 1 {
		t.Errorf("VACUUM ran %d time(s) after a purge that deleted rows, want exactly 1", n)
	}
}

// A routerId of nothing but spaces means EVERY router, not a router named "   ".
//
// `String(req.body.routerId || ”).trim()`. Untrimmed, the id goes into a
// `router_id = ?` and matches nothing, so the operator asks to purge everything
// and is told nothing matched — a silent no-op on the one screen where a silent
// no-op is worst.
func TestAWhitespaceRouterIdMeansAllRouters(t *testing.T) {
	_, mux, _ := dbAdminServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/db/purge", `{"routerId":"   ","olderThanDays":0,"dryRun":true}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// The fixture holds seven rows across two routers.
	if got.Total != 7 {
		t.Errorf("total = %d, want 7. A blank routerId was treated as a router name, so the "+
			"predicate matched nothing.", got.Total)
	}
}

// ── A MEASURED GAP, recorded rather than hidden ────────────────────────────
//
// `purgeOpts` treats an ERROR from `rbac.Can` as a refusal — "an error is not a
// yes", the rule `visibleRouters` follows. Mutating that to swallow the error
// and continue SURVIVES this suite, and the reason is reachability: every test
// here runs under auth mode "none", where the scope block is skipped entirely,
// and reaching it needs a session that is a global admin AND an `rbac.Can` that
// fails — which this fixture has no way to induce without closing the database
// the route also reads.
//
// Left as a gap on purpose. It is one line, its sibling in `visibleRouters` is
// tested, and the alternative is a fixture that can break its own database.

// TestThePurgeIsAudited — and the row outlives the purge it describes.
func TestThePurgeIsAudited(t *testing.T) {
	s, mux, _ := dbAdminServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/db/purge", `{"olderThanDays":0}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	// `IncludeApp`, and it is not optional. `QueryAuditEvents` FAILS CLOSED: a
	// query naming neither `IncludeApp` nor any router id returns an empty page
	// rather than everything, so a caller that forgot to say what it may see
	// gets nothing instead of the whole trail. The first version of this test
	// asked for `{Limit: 50}` and concluded the audit row was missing.
	//
	// A GLOBAL purge has no router, so its row is scoped `app` — which is what
	// this asks for.
	page, err := s.auditDB.QueryAuditEvents(db.Query{IncludeApp: true, Limit: 50})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	found := false
	for _, r := range page.Rows {
		if r.Action == "db.purge" {
			found = true
		}
	}
	if !found {
		t.Error("no db.purge row in the audit trail. `audit_events` is deliberately absent from " +
			"PURGE_TABLES so that this row survives the purge it describes — if it is missing, " +
			"either it was never written or the purge reached a table it must not.")
	}
}

func TestDBRoutesRequireGlobalAdmin(t *testing.T) {
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/db/stats", ""},
		{"POST", "/api/db/purge", `{"olderThanDays":0}`},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			s, mux, _ := dbAdminServer(t, &Session{AuthMode: "modern", Username: "nobody"})
			before := historyRowCount(t, s)
			w := doJSON(mux, c.method, c.path, c.body, authed)
			if w.Code != http.StatusForbidden {
				t.Errorf("status %d, want 403 — %s", w.Code, w.Body.String())
			}
			if after := historyRowCount(t, s); after != before {
				t.Errorf("a forbidden request deleted rows (%d -> %d)", before, after)
			}
		})
	}
}

func TestDBRoutesRequireASession(t *testing.T) {
	_, mux, _ := dbAdminServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "GET", "/api/db/stats", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401 — %s", w.Code, w.Body.String())
	}
}

// ── The corpus, replayed through purgeOpts ─────────────────────────────────

func TestPurgeOptsMatchTheLiveRoute(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "purge-cases.json"))
	if err != nil {
		t.Fatalf("no corpus — run: node tools/purge-cases.js: %v", err)
	}
	var corpus struct {
		Options []struct {
			Why   string         `json:"why"`
			Body  map[string]any `json:"body"`
			Perms struct {
				Modern      *bool `json:"modern"`
				SystemDB    *bool `json:"systemDb"`
				RouterPurge *bool `json:"routerPurge"`
			} `json:"perms"`
			Error       *string  `json:"error"`
			RouterID    *string  `json:"routerId"`
			Types       []string `json:"types"`
			OlderThanMs *int64   `json:"olderThanMs"`
		} `json:"options"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Options) == 0 {
		t.Fatal("the corpus has no option cases")
	}

	refused := 0
	for _, c := range corpus.Options {
		if c.Error != nil {
			refused++
		}
	}
	if refused == 0 || refused == len(corpus.Options) {
		t.Fatalf("%d of %d option cases are refused; the corpus does not separate them",
			refused, len(corpus.Options))
	}

	sess := &Session{AuthMode: "none", Username: "admin"}
	replayed := 0
	for _, c := range corpus.Options {
		// ONLY THE CASES THIS HARNESS CAN DRIVE. A case whose permissions decide
		// the outcome needs a real RBAC resolver, which this fixture does not
		// wire — those are covered by `TestDBRoutesRequireGlobalAdmin` and by the
		// live corpus itself. What is replayed here is the TYPES and AGE half,
		// under auth mode none, where the scope gate is skipped entirely.
		if c.Perms.SystemDB != nil || c.Perms.RouterPurge != nil {
			if c.Perms.RouterPurge == nil || !*c.Perms.RouterPurge {
				continue
			}
		}
		replayed++
		t.Run(c.Why, func(t *testing.T) {
			s, _, _ := dbAdminServer(t, sess)
			opts, msg := s.purgeOpts(c.Body, sess)

			if c.Error != nil {
				if msg != *c.Error {
					t.Errorf("refusal = %q, live = %q", msg, *c.Error)
				}
				return
			}
			if msg != "" {
				t.Fatalf("refused with %q; the live route accepted", msg)
			}
			if c.RouterID != nil && opts.RouterID != *c.RouterID {
				t.Errorf("routerId = %q, live = %q", opts.RouterID, *c.RouterID)
			}
			if c.OlderThanMs != nil && opts.OlderThanMs != *c.OlderThanMs {
				t.Errorf("olderThanMs = %d, live = %d", opts.OlderThanMs, *c.OlderThanMs)
			}
			if len(opts.Types) != len(c.Types) {
				t.Errorf("types = %v, live = %v", opts.Types, c.Types)
			}
		})
	}
	// A FILTER THAT EXCLUDED EVERYTHING would report a clean run over nothing.
	if replayed < 10 {
		t.Errorf("only %d of %d option cases were replayed; the permission filter above is "+
			"excluding more than it should", replayed, len(corpus.Options))
	}
}
