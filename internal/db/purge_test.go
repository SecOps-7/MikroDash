package db

// The router purge, against the live `deleteRouterData`.
//
// A DRIFT GATE, in the manner of `internal/collect/drift_test.go`: it reads
// `$MIKRODASH_SRC/src/db.js` and compares the table list. Without that variable
// it SKIPS, and says so — CLAUDE.md's documented `go test` invocation mounts the
// live repo read-only at /live for exactly this reason.

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// THE REAL COLUMNS, checked by tools/schema-audit.js. These were stand-ins with
// a scratch v until 2026-08-26. The purge deletes by router_id and would pass
// either way; the reason to match anyway is that a fixture which is not the
// schema is not evidence -- learned three times in one day, twice the hard way.
// (No backticks in these SQL comments: they sit inside a Go raw string.)
const purgeDDL = `
CREATE TABLE ping_samples (id INTEGER PRIMARY KEY, router_id TEXT NOT NULL,
  target TEXT NOT NULL, rtt_ms REAL, loss_pct REAL NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE traffic_samples (id INTEGER PRIMARY KEY, router_id TEXT NOT NULL,
  interface TEXT NOT NULL, rx_mbps REAL NOT NULL, tx_mbps REAL NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE bandwidth_usage (id INTEGER PRIMARY KEY, router_id TEXT NOT NULL,
  interface TEXT NOT NULL, rx_mb REAL NOT NULL, tx_mb REAL NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE connectivity_events (id INTEGER PRIMARY KEY, router_id TEXT NOT NULL,
  connected INTEGER NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE report_schedules (id TEXT PRIMARY KEY, router_id TEXT NOT NULL,
  name TEXT NOT NULL, sections TEXT NOT NULL, interface TEXT, aggregate TEXT NOT NULL,
  channel_id TEXT NOT NULL DEFAULT '', frequency TEXT NOT NULL, send_hour INTEGER NOT NULL,
  enabled INTEGER NOT NULL, disabled_reason TEXT, created_by TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
-- id IS A TEXT UUID, matching the live schema: grants.id is TEXT PRIMARY KEY
-- and upsertGrant fills it with crypto.randomUUID(). Every fixture here declared
-- INTEGER PRIMARY KEY AUTOINCREMENT until 2026-08-26, which is not the shape on
-- disk -- GrantRow.ID was int64 and scanned happily against all of them while
-- being unable to read a single real row. The default keeps the INSERTs readable.
-- (No backticks in this comment: it sits inside a Go raw string.)
CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT NOT NULL,
  builtin INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0);
INSERT INTO roles (id, name) VALUES ('role','role');
CREATE TABLE grants (id TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT, principal_id TEXT, scope_type TEXT, scope_id TEXT,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT);
-- The one that must SURVIVE a purge.
CREATE TABLE config_backups (id INTEGER PRIMARY KEY, router_id TEXT NOT NULL,
  taken_at INTEGER NOT NULL, outcome TEXT NOT NULL, source TEXT NOT NULL, actor TEXT,
  stem TEXT, dir TEXT, fingerprint TEXT, rsc_bytes INTEGER NOT NULL,
  backup_bytes INTEGER NOT NULL, model TEXT, serial TEXT, os_version TEXT,
  ms INTEGER NOT NULL, pruned_at INTEGER, error TEXT);
` + rollupTablesDDL

// liveDeleteRouterData returns the body of the live function, or "" when the
// source is not available.
func liveDeleteRouterData(t *testing.T) string {
	t.Helper()
	root := os.Getenv("MIKRODASH_SRC")
	if root == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, "src", "db.js"))
	if err != nil {
		return ""
	}
	src := string(b)
	i := strings.Index(src, "function deleteRouterData(")
	if i < 0 {
		t.Fatal("deleteRouterData is gone from src/db.js -- this gate is stale, not passing")
	}
	end := strings.Index(src[i:], "\nfunction ")
	if end < 0 {
		return src[i:]
	}
	return src[i : i+end]
}

// TestTheRouterPurgeTablesMatchLive.
//
// FAILS IN BOTH DIRECTIONS. A table the live function clears and this one does
// not leaves rows behind for a router that no longer exists; a table this one
// clears and the live one does not destroys something the design keeps.
func TestTheRouterPurgeTablesMatchLive(t *testing.T) {
	body := liveDeleteRouterData(t)
	if body == "" {
		t.Skip("MIKRODASH_SRC is not set -- the purge drift gate did NOT run")
	}

	re := regexp.MustCompile(`DELETE FROM\s+(\w+)\s+WHERE router_id`)
	var live []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		live = append(live, m[1])
	}
	if len(live) == 0 {
		t.Fatal("no tables were found in the live function -- the matcher has drifted, " +
			"and an empty list would make every comparison below pass")
	}

	if !reflect.DeepEqual(routerDataTables, live) {
		t.Errorf("the purge list differs from the live one:\n  got  %v\n  live %v",
			routerDataTables, live)
	}

	// ── AND THE ABSENCES ────────────────────────────────────────────────────
	//
	// Checked against the live function too, so "completing" the list fails here
	// as well as above. These are the tables whose omission IS the design.
	for table, why := range routerPurgeExcluded {
		for _, got := range live {
			if got == table {
				t.Errorf("the LIVE function now clears %s, which this port records as "+
					"deliberately excluded (%s). One of the two has changed, and the "+
					"reason needs rereading rather than the list being updated", table, why)
			}
		}
		for _, got := range routerDataTables {
			if got == table {
				t.Errorf("this port clears %s, which must never be purged with a router: %s",
					table, why)
			}
		}
	}
}

// portAddedRouterPurgeTables: every table this port clears that live has no
// counterpart for, with why. Named here rather than derived from the list, so
// the list alone cannot admit a DELETE — the same arrangement, and the same
// reasoning, as portAddedPrunes.
var portAddedRouterPurgeTables = map[string]string{
	"traffic_hourly": "the hourly traffic rollup (#59). Live recorded minutes only, so the " +
		"recording holds nothing to compare against. It MUST be cleared with the router: it " +
		"is the same history as traffic_samples at lower resolution and a longer retention, " +
		"so omitting it would leave a removed router's traffic behind for the full retention",
	"bandwidth_hourly": "the hourly volume rollup (#59), the same arrangement as traffic_hourly",
}

// TestPortAddedRouterPurgeTablesAreRecorded — BOTH DIRECTIONS.
//
// An unrecorded port-added table is a DELETE nobody reviewed. A recorded one
// that is no longer cleared is a note about a table the purge has stopped
// touching. And one that has appeared in the LIVE function belongs in the
// lifted list, where the parity comparison can see it, not here where it is
// exempt from it.
func TestPortAddedRouterPurgeTablesAreRecorded(t *testing.T) {
	for _, table := range routerDataTablesPortAdded {
		if _, ok := portAddedRouterPurgeTables[table]; !ok {
			t.Errorf("%s is cleared by a port-added entry that nothing records — it is "+
				"exempt from the parity comparison, so this ledger is the only thing "+
				"that reviews it", table)
		}
		for _, lifted := range routerDataTables {
			if lifted == table {
				t.Errorf("%s is in BOTH lists, so it is compared against live AND excused "+
					"from that comparison", table)
			}
		}
	}
	for table := range portAddedRouterPurgeTables {
		found := false
		for _, got := range routerDataTablesPortAdded {
			if got == table {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is recorded as a port-added purge table but nothing clears it; "+
				"either the purge stopped covering it or this note has outlived it", table)
		}
	}
	// AND IT IS STILL ABSENT FROM LIVE. Once live gains the table, the entry
	// belongs in routerDataTables where the parity test compares it.
	if body := liveDeleteRouterData(t); body != "" {
		for table := range portAddedRouterPurgeTables {
			if strings.Contains(body, table) {
				t.Errorf("the LIVE function now clears %s, so it is no longer port-added: "+
					"move it to routerDataTables where the parity comparison covers it", table)
			}
		}
	}
}

// purgeInsert is one seed row per table, naming every NOT NULL column. Held here
// rather than built from the table name because the columns genuinely differ --
// which is the point of matching the real schema.
var purgeInsert = map[string]string{
	"ping_samples": `INSERT INTO ping_samples (router_id, target, loss_pct, ts)
	   VALUES (?, '1.1.1.1', 0, 1)`,
	"traffic_samples": `INSERT INTO traffic_samples (router_id, interface, rx_mbps, tx_mbps, ts)
	   VALUES (?, 'ether1', 1, 1, 1)`,
	"bandwidth_usage": `INSERT INTO bandwidth_usage (router_id, interface, rx_mb, tx_mb, ts)
	   VALUES (?, 'ether1', 1, 1, 1)`,
	// The port-added rollups are seeded too, or the DELETEs added for them
	// would run against empty tables and prove nothing.
	"traffic_hourly": `INSERT INTO traffic_hourly
	   (router_id, interface, ts, rx_mbps, tx_mbps, rx_max_mbps, tx_max_mbps, samples)
	   VALUES (?, 'ether1', 0, 1, 1, 1, 1, 60)`,
	"bandwidth_hourly": `INSERT INTO bandwidth_hourly
	   (router_id, interface, ts, rx_mb, tx_mb, samples) VALUES (?, 'ether1', 0, 1, 1, 60)`,
	"connectivity_events": `INSERT INTO connectivity_events (router_id, connected, ts)
	   VALUES (?, 1, 1)`,
	"alert_events": `INSERT INTO alert_events (router_id, alert_type, subject, detail, fired_at)
	   VALUES (?, 'x', 's', 'd', 1)`,
}

func purgeDB(t *testing.T) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(alertEventsDDL + purgeDDL); err != nil {
		t.Fatal(err)
	}
	for _, table := range append([]string{"ping_samples", "traffic_samples", "bandwidth_usage",
		"connectivity_events"}, routerDataTablesPortAdded...) {
		for _, rid := range []string{"r1", "r2"} {
			if _, err := h.Exec(purgeInsert[table], rid); err != nil {
				t.Fatalf("seed %s: %v", table, err)
			}
		}
	}
	for _, rid := range []string{"r1", "r2"} {
		if _, err := h.Exec(`INSERT INTO alert_events
      (router_id, alert_type, subject, detail, fired_at) VALUES (?, 'x', 's', 'd', 1)`,
			rid); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Exec(`INSERT INTO config_backups (router_id, taken_at, outcome, source,
		    stem, rsc_bytes, backup_bytes, ms) VALUES (?, 1, 'ok', 'manual', 'keep', 1, 1, 1)`,
			rid); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Exec(`INSERT INTO report_schedules (id, router_id, name, sections,
		    aggregate, channel_id, frequency, send_hour, enabled, created_at, updated_at)
		  VALUES (?, ?, 'n', '[]', 'sum', 'chan-1', 'daily', 8, 1, 1, 1)`,
			"sched-"+rid, rid); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Exec(`INSERT INTO grants
      (principal_type, principal_id, scope_type, scope_id, role_id)
      VALUES ('user','u-1','router',?, 'role')`, rid); err != nil {
			t.Fatal(err)
		}
		// A grant on a DIFFERENT SCOPE TYPE with the SAME id. Ids are opaque
		// strings and nothing stops a site being called "r1", so a removal keyed
		// on the id alone would take a site's grants with a router's. Without
		// this row every grant in the fixture is scope_type='router' and dropping
		// the type from the WHERE clause changes nothing — which is exactly how
		// that mutation survived the first time.
		if _, err := h.Exec(`INSERT INTO grants
      (principal_type, principal_id, scope_type, scope_id, role_id)
      VALUES ('user','u-1','site',?, 'role')`, rid); err != nil {
			t.Fatal(err)
		}
	}
	_ = h.Close()

	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func countIn(t *testing.T, d *DB, table, routerID string) int {
	t.Helper()
	var n int
	if err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM `+table+` WHERE router_id = ?`, routerID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestDeleteRouterDataClearsOnlyItsOwnRouter.
func TestDeleteRouterDataClearsOnlyItsOwnRouter(t *testing.T) {
	d := purgeDB(t)

	// Believability: every table has rows for BOTH routers first, or "r2 still
	// has its rows" would hold for a purge that cleared nothing.
	for _, table := range routerDataTables {
		if countIn(t, d, table, "r1") == 0 || countIn(t, d, table, "r2") == 0 {
			t.Fatalf("%s is not seeded for both routers", table)
		}
	}

	if err := d.DeleteRouterData("r1"); err != nil {
		t.Fatal(err)
	}
	for _, table := range routerDataTables {
		if n := countIn(t, d, table, "r1"); n != 0 {
			t.Errorf("%s still has %d rows for the removed router", table, n)
		}
		if n := countIn(t, d, table, "r2"); n == 0 {
			t.Errorf("%s lost the OTHER router's rows -- the purge is unscoped", table)
		}
	}
}

// TestAPurgeNeverReachesABackup.
//
// One of the two absences, asserted behaviourally rather than only by the table
// list. A restore point outlives the router's time-series data on purpose.
func TestAPurgeNeverReachesABackup(t *testing.T) {
	d := purgeDB(t)

	before := countIn(t, d, "config_backups", "r1")
	if before == 0 {
		t.Fatal("config_backups is not seeded, so the assertion below proves nothing")
	}
	if err := d.DeleteRouterData("r1"); err != nil {
		t.Fatal(err)
	}
	if after := countIn(t, d, "config_backups", "r1"); after != before {
		t.Errorf("config_backups went from %d to %d rows -- a purge reached a RESTORE "+
			"POINT, which is the one thing this list is written to avoid", before, after)
	}
}

// TestGrantsAndSchedulesAreRemovedSeparately.
//
// Neither is in `DeleteRouterData`: a grant is an authorization change and a
// schedule is a live outbound email loop, so the route removes them explicitly
// where they are visible.
func TestGrantsAndSchedulesAreRemovedSeparately(t *testing.T) {
	d := purgeDB(t)

	if err := d.DeleteRouterData("r1"); err != nil {
		t.Fatal(err)
	}

	n, err := d.DeleteGrantsForScope("router", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("removed %d grants, want 1 -- the data purge should have left it, and "+
			"only the ROUTER-scoped one should go", n)
	}
	if again, _ := d.DeleteGrantsForScope("router", "r1"); again != 0 {
		t.Errorf("a second removal reported %d", again)
	}
	// THE SAME ID UNDER A DIFFERENT SCOPE TYPE SURVIVES. Ids are opaque, so a
	// site may share a router's id, and a removal keyed on the id alone would
	// silently revoke access to something else entirely.
	var siteGrants int
	_ = d.sql.QueryRow(
		`SELECT COUNT(*) FROM grants WHERE scope_type = 'site' AND scope_id = 'r1'`).
		Scan(&siteGrants)
	if siteGrants != 1 {
		t.Errorf("a site grant sharing the router's id was removed with it (%d left) -- "+
			"the removal is keyed on the id and ignores the scope TYPE", siteGrants)
	}

	m, err := d.DeleteReportSchedulesForRouter("r1")
	if err != nil {
		t.Fatal(err)
	}
	if m != 1 {
		t.Errorf("removed %d schedules, want 1", m)
	}

	// The OTHER router keeps both.
	var grants, scheds int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM grants WHERE scope_id = 'r2'`).Scan(&grants)
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM report_schedules WHERE router_id = 'r2'`).Scan(&scheds)
	// TWO grants for r2: one router-scoped and one site-scoped sharing the id.
	// Both must survive removing r1.
	if grants != 2 || scheds != 1 {
		t.Errorf("r2 lost data: %d grants (want 2), %d schedules (want 1)", grants, scheds)
	}
}

// TestAnEmptyRouterIDIsRefused. A purge with no id would clear the whole fleet's
// time-series data if the WHERE clause were ever loosened.
func TestAnEmptyRouterIDIsRefused(t *testing.T) {
	d := purgeDB(t)
	if err := d.DeleteRouterData(""); err == nil {
		t.Error("an empty router id was accepted")
	}
	if n := countIn(t, d, "ping_samples", "r1"); n == 0 {
		t.Error("the refused purge still cleared rows")
	}
}
