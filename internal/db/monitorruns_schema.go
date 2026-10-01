package db

// The record of WHEN each router was being watched, so the Devices page's
// connectivity strip can tell "up" from "nobody was looking".
//
// ── THE PROBLEM IT EXISTS FOR, MEASURED ────────────────────────────────────
//
// `connectivity_events` holds one row per state CHANGE. Between two rows the
// state is inferred, and that inference is wrong across any stretch where
// MikroDash itself was not running: nothing is written while the process is
// stopped, and on restart the first connect writes `connected = 1`. Measured on
// the dev install before this table existed: one router had 121 rows in seven
// days, 120 of them `connected = 1` and 118 consecutive same-state rows - every
// restart re-asserting "up" - so a container stopped overnight would have drawn
// those hours green.
//
// A run says "this router was observed from `started_at` until `last_seen_at`".
// Time inside no run is NOT MONITORED and draws grey.
//
// ── PER ROUTER, NOT PER PROCESS ────────────────────────────────────────────
//
// A process-wide "MikroDash was up" record looks sufficient and is not. A router
// that is DISABLED is not connected to, writes no row, and keeps its last state
// in the tracker, so a process-wide record would paint the whole disabled
// period in that router's last colour. A run opens while the router is held by
// a session whose state the tracker knows, and closes when either stops.
//
// ── `last_seen_at` IS A HEARTBEAT, ONCE A MINUTE ────────────────────────────
//
// Every open run is touched in ONE update per minute, not per router per tick,
// so a crash loses at most a minute of coverage and the cost does not grow with
// the fleet. A clean shutdown closes every run at the moment it happens.
//
// ── ONE CONSTANT, TWO PATHS ────────────────────────────────────────────────
//
// Shared by `freshSchemaDDL` and `portMigrations[34]`, the rule `cfgTablesDDL`,
// `ssoTablesDDL` and `credProfTablesDDL` follow: a new database and a migrated
// one must not be able to describe different tables. `IF NOT EXISTS` on both
// statements is what makes the migration safe to replay.
const monitorRunsDDL = `
CREATE TABLE IF NOT EXISTS monitor_runs (
          id           INTEGER PRIMARY KEY,
          router_id    TEXT    NOT NULL,
          started_at   INTEGER NOT NULL,
          last_seen_at INTEGER NOT NULL
        );
CREATE INDEX IF NOT EXISTS idx_monitor_runs_router ON monitor_runs(router_id, last_seen_at);
`
