package db

// The schema a fresh install starts from.
//
// ── WHY THIS FILE EXISTS ────────────────────────────────────────────────────
//
// `Open` used to refuse a /data with no database and say "the Node app creates
// and migrates it". That stopped being true at cutover: nothing creates it any
// more, and nothing in this tree contained a CREATE TABLE outside tests. A new
// install therefore ran permanently degraded -- and worse than degraded, because
// `grantFirstAdmin` needs the grants table, so the first administrator held no
// grants and could not add a router at all. That is issue #124.
//
// ── IT IS THE FINAL SHAPE, NOT A REPLAY OF THE MIGRATIONS ───────────────────
//
// The Node app reached this through 15 migrations, three of which REBUILD a
// table and rename it into place (`grants` twice, `user_layouts` once). Replaying
// that history would mean porting rebuild steps whose only purpose was to
// preserve data no fresh install has. What a new database needs is the shape
// those migrations end at.
//
// ── AND IT WAS EXTRACTED, NOT TRANSCRIBED ───────────────────────────────────
//
// Dumped from `sqlite_master` of a database the Node app actually migrated, so
// this is what RouterOS installs in the field are running rather than a reading
// of the migration source. That matters for the two renamed tables: SQLite
// stores their DDL with the name QUOTED (`CREATE TABLE "grants"`), which a
// hand-copy from db.js would have got wrong -- it would have created a table
// called `grants_new`.
//
// 18 tables and 14 indexes, at schema version schemaVersion. `sqlite_sequence`
// is deliberately absent: SQLite maintains it for AUTOINCREMENT tables and
// refuses an explicit one.
const freshSchemaDDL = `
CREATE TABLE alert_events (
          id          INTEGER PRIMARY KEY,
          router_id   TEXT    NOT NULL,
          alert_type  TEXT    NOT NULL,
          subject     TEXT,
          detail      TEXT,
          fired_at    INTEGER NOT NULL,
          resolved_at INTEGER
        , acknowledged_at INTEGER, acknowledged_by TEXT);

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

CREATE TABLE bandwidth_usage (
          id        INTEGER PRIMARY KEY,
          router_id TEXT    NOT NULL,
          interface TEXT    NOT NULL,
          rx_mb     REAL    NOT NULL,
          tx_mb     REAL    NOT NULL,
          ts        INTEGER NOT NULL
        );

CREATE TABLE config_backups (
          id           INTEGER PRIMARY KEY AUTOINCREMENT,
          router_id    TEXT    NOT NULL,
          taken_at     INTEGER NOT NULL,
          outcome      TEXT    NOT NULL,
          source       TEXT    NOT NULL DEFAULT 'schedule',
          actor        TEXT,
          stem         TEXT,
          dir          TEXT,
          fingerprint  TEXT,
          rsc_bytes    INTEGER NOT NULL DEFAULT 0,
          backup_bytes INTEGER NOT NULL DEFAULT 0,
          model        TEXT,
          serial       TEXT,
          os_version   TEXT,
          ms           INTEGER NOT NULL DEFAULT 0,
          pruned_at    INTEGER,
          error        TEXT
        );

CREATE TABLE connectivity_events (
          id        INTEGER PRIMARY KEY,
          router_id TEXT    NOT NULL,
          connected INTEGER NOT NULL,
          ts        INTEGER NOT NULL
        );

CREATE TABLE "grants" (
          id             TEXT PRIMARY KEY,
          principal_type TEXT NOT NULL CHECK (principal_type IN ('user','group')),
          principal_id   TEXT NOT NULL,
          role_id        TEXT NOT NULL DEFAULT 'readonly' REFERENCES roles(id) ON DELETE RESTRICT,
          role           TEXT,
          scope_type     TEXT NOT NULL CHECK (scope_type IN ('global','site','router')),
          scope_id       TEXT NOT NULL DEFAULT '',
          created_at     INTEGER NOT NULL,
          created_by     TEXT,
          CHECK ((scope_type =  'global' AND scope_id =  '')
              OR (scope_type <> 'global' AND scope_id <> '')),
          UNIQUE (principal_type, principal_id, scope_type, scope_id)
        );

CREATE TABLE group_members (
          group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
          user_id  TEXT NOT NULL,
          PRIMARY KEY (group_id, user_id)
        );

CREATE TABLE groups (
          id          TEXT PRIMARY KEY,
          name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
          description TEXT,
          created_at  INTEGER NOT NULL
        );

CREATE TABLE ping_samples (
          id        INTEGER PRIMARY KEY,
          router_id TEXT    NOT NULL,
          target    TEXT    NOT NULL,
          rtt_ms    REAL,
          loss_pct  REAL    NOT NULL,
          ts        INTEGER NOT NULL
        );

CREATE TABLE report_runs (
          id           INTEGER PRIMARY KEY AUTOINCREMENT,
          schedule_id  TEXT    NOT NULL REFERENCES report_schedules(id) ON DELETE CASCADE,
          ran_at       INTEGER NOT NULL,
          period_from  INTEGER NOT NULL,
          period_to    INTEGER NOT NULL,
          outcome      TEXT    NOT NULL,
          source       TEXT    NOT NULL DEFAULT 'schedule',
          actor        TEXT,
          recipients_n INTEGER NOT NULL DEFAULT 0,
          bytes        INTEGER NOT NULL DEFAULT 0,
          rows_n       INTEGER NOT NULL DEFAULT 0,
          ms           INTEGER NOT NULL DEFAULT 0,
          error        TEXT
        );

CREATE TABLE report_schedules (
          id              TEXT PRIMARY KEY,
          router_id       TEXT    NOT NULL,
          name            TEXT    NOT NULL,
          sections        TEXT    NOT NULL,
          interface       TEXT,
          aggregate       TEXT    NOT NULL DEFAULT '',
          recipients      TEXT    NOT NULL,
          frequency       TEXT    NOT NULL CHECK (frequency IN ('daily','weekly','monthly')),
          send_hour       INTEGER NOT NULL DEFAULT 7,
          enabled         INTEGER NOT NULL DEFAULT 1,
          disabled_reason TEXT,
          created_by      TEXT,
          created_at      INTEGER NOT NULL,
          updated_at      INTEGER NOT NULL
        );

CREATE TABLE role_pages (
          role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
          page    TEXT NOT NULL,
          access  TEXT NOT NULL CHECK (access IN ('read','write')),
          PRIMARY KEY (role_id, page)
        );

CREATE TABLE roles (
          id          TEXT PRIMARY KEY,
          name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
          description TEXT,
          -- 1 = Administrator: reach is structural, not table-driven, so a
          -- permission added in a later release is covered with no data change.
          builtin     INTEGER NOT NULL DEFAULT 0,
          created_at  INTEGER NOT NULL
        );

CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);

CREATE TABLE sites (
          id          TEXT PRIMARY KEY,
          -- NOCASE so "Berlin DC" and "berlin dc" collide. These are human
          -- labels picked from a list; two differing only in case are a
          -- mistake, not a distinction.
          name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
          description TEXT,
          -- Reserved for the Routers-page map (issue #96). Nullable: most
          -- installs will never set them, and an unset location must not read
          -- as coordinates 0,0 in the Gulf of Guinea.
          lat         REAL,
          lon         REAL,
          created_at  INTEGER NOT NULL
        , place_name   TEXT, place_region TEXT, place_cc     TEXT);

CREATE TABLE traffic_samples (
          id        INTEGER PRIMARY KEY,
          router_id TEXT    NOT NULL,
          interface TEXT    NOT NULL,
          rx_mbps   REAL    NOT NULL,
          tx_mbps   REAL    NOT NULL,
          ts        INTEGER NOT NULL
        );

CREATE TABLE "user_layouts" (
          user_id    TEXT NOT NULL,
          kind       TEXT NOT NULL CHECK (kind IN ('dashboard','topology','nav')),
          data       TEXT NOT NULL,
          updated_at INTEGER NOT NULL,
          PRIMARY KEY (user_id, kind)
        );

CREATE TABLE user_notify_config (
          user_id    TEXT PRIMARY KEY,
          data       TEXT NOT NULL,
          updated_at INTEGER NOT NULL
        );

CREATE INDEX idx_alert_router_ts
          ON alert_events(router_id, fired_at);

CREATE INDEX idx_audit_actor_ts  ON audit_events(actor_name, ts);

CREATE INDEX idx_audit_router_ts ON audit_events(router_id, ts);

CREATE INDEX idx_audit_ts        ON audit_events(ts);

CREATE INDEX idx_bw_router_iface_ts
          ON bandwidth_usage(router_id, interface, ts);

CREATE INDEX idx_config_backups_router ON config_backups (router_id, taken_at DESC);

CREATE INDEX idx_conn_router_ts
          ON connectivity_events(router_id, ts);

CREATE INDEX idx_grants_principal ON grants(principal_type, principal_id);

CREATE INDEX idx_grants_role      ON grants(role_id);

CREATE INDEX idx_grants_scope     ON grants(scope_type, scope_id);

CREATE INDEX idx_ping_router_ts
          ON ping_samples(router_id, ts);

CREATE INDEX idx_report_runs_sched ON report_runs (schedule_id, ran_at DESC);

CREATE INDEX idx_report_schedules_router ON report_schedules (router_id);

CREATE INDEX idx_traffic_router_iface_ts
          ON traffic_samples(router_id, interface, ts);
`
