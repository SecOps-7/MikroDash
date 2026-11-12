package db

// The tables of the Apps Usage history, an optional feature (Settings ->
// Features), migration 38.
//
//	app_usage              bytes each application moved on a router, per hour
//	app_history_routers    the routers whose app usage is recorded
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
// Both belong to a router and go with it (purge.go); `app_usage` ages out on
// the metric retention (prune.go).
const appUsageDDL = `
CREATE TABLE IF NOT EXISTS app_usage (
          router_id TEXT    NOT NULL,
          app       TEXT    NOT NULL,
          hour      INTEGER NOT NULL,
          rx_bytes  INTEGER NOT NULL DEFAULT 0,
          tx_bytes  INTEGER NOT NULL DEFAULT 0,
          PRIMARY KEY (router_id, app, hour)
        );
CREATE TABLE IF NOT EXISTS app_history_routers (
          router_id TEXT PRIMARY KEY
        );
CREATE INDEX IF NOT EXISTS idx_app_usage_router_hour ON app_usage(router_id, hour);
CREATE INDEX IF NOT EXISTS idx_app_usage_hour ON app_usage(hour);
`
