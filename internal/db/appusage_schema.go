package db

// The table of the Apps Usage history, migration 38. Recorded for every router
// with Reporting on (internal/server/apps_history.go).
//
//	app_usage              bytes each application moved on a router, per hour,
//	                       and the unnamed bytes under apps.UnnamedID
//
// Migration 38 also made `app_history_routers`, the routers someone had
// switched recording on for. There is no such choice any more (the operator,
// 2026-11-12: a router's Reporting setting decides), so migration 39 drops it
// (appHistoryRoutersDropDDL) and a new database never has it.
//
// Shared by `freshSchemaDDL` and `portMigrations[38]`, the rule
// monitorruns_schema.go gives: a new database and a migrated one must not be
// able to describe different tables, and `IF NOT EXISTS` makes it replayable.
//
// ── AN APP AND AN HOUR, NOTHING ELSE ────────────────────────────────────────
//
// A row is the application's catalogue id and its byte counts. No device, no
// address and no name the DNS cache gave is stored: the live page reads the
// cache on the condition that nothing from it is kept (internal/collect/apps.go).
//
// It belongs to a router and goes with it (purge.go), and ages out on the
// metric retention (prune.go).
const appUsageDDL = `
CREATE TABLE IF NOT EXISTS app_usage (
          router_id TEXT    NOT NULL,
          app       TEXT    NOT NULL,
          hour      INTEGER NOT NULL,
          rx_bytes  INTEGER NOT NULL DEFAULT 0,
          tx_bytes  INTEGER NOT NULL DEFAULT 0,
          PRIMARY KEY (router_id, app, hour)
        );
CREATE INDEX IF NOT EXISTS idx_app_usage_router_hour ON app_usage(router_id, hour);
CREATE INDEX IF NOT EXISTS idx_app_usage_hour ON app_usage(hour);
`

// appHistoryRoutersDropDDL is migration 39: the per-router opt-in is gone.
// IF EXISTS, so a database created without it replays it harmlessly.
const appHistoryRoutersDropDDL = `DROP TABLE IF EXISTS app_history_routers`
