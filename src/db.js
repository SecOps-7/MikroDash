'use strict';
const path    = require('path');
const fs      = require('fs');
const crypto  = require('node:crypto');
const BetterSqlite = require('better-sqlite3');

const DATA_DIR = process.env.DATA_DIR || '/data';
const DB_FILE  = path.join(DATA_DIR, 'mikrodash.db');

let _db = null;

// ── Prepared statements (set after open) ─────────────────────────────────────
let _stmtInsertPing        = null;
let _stmtInsertTraffic     = null;
let _stmtInsertBandwidth   = null;
let _stmtInsertAlert       = null;
let _stmtInsertConn        = null;
let _stmtResolveAlert      = null;
let _stmtInsertAudit       = null;
let _pruneTimer            = null;

// ── Migrations ────────────────────────────────────────────────────────────────
const MIGRATIONS = [
  {
    version: 1,
    up(db) {
      db.exec(`
        CREATE TABLE IF NOT EXISTS ping_samples (
          id        INTEGER PRIMARY KEY,
          router_id TEXT    NOT NULL,
          target    TEXT    NOT NULL,
          rtt_ms    REAL,
          loss_pct  REAL    NOT NULL,
          ts        INTEGER NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_ping_router_ts
          ON ping_samples(router_id, ts);

        CREATE TABLE IF NOT EXISTS traffic_samples (
          id        INTEGER PRIMARY KEY,
          router_id TEXT    NOT NULL,
          interface TEXT    NOT NULL,
          rx_mbps   REAL    NOT NULL,
          tx_mbps   REAL    NOT NULL,
          ts        INTEGER NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_traffic_router_iface_ts
          ON traffic_samples(router_id, interface, ts);

        CREATE TABLE IF NOT EXISTS alert_events (
          id          INTEGER PRIMARY KEY,
          router_id   TEXT    NOT NULL,
          alert_type  TEXT    NOT NULL,
          subject     TEXT,
          detail      TEXT,
          fired_at    INTEGER NOT NULL,
          resolved_at INTEGER
        );
        CREATE INDEX IF NOT EXISTS idx_alert_router_ts
          ON alert_events(router_id, fired_at);

        CREATE TABLE IF NOT EXISTS connectivity_events (
          id        INTEGER PRIMARY KEY,
          router_id TEXT    NOT NULL,
          connected INTEGER NOT NULL,
          ts        INTEGER NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_conn_router_ts
          ON connectivity_events(router_id, ts);
      `);
    },
  },
  {
    version: 2,
    up(db) {
      db.exec(`
        CREATE TABLE IF NOT EXISTS bandwidth_usage (
          id        INTEGER PRIMARY KEY,
          router_id TEXT    NOT NULL,
          interface TEXT    NOT NULL,
          rx_mb     REAL    NOT NULL,
          tx_mb     REAL    NOT NULL,
          ts        INTEGER NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_bw_router_iface_ts
          ON bandwidth_usage(router_id, interface, ts);
      `);
    },
  },
  {
    // Acknowledgment. `resolved_at` records what the SYSTEM observed; these two
    // record what a PERSON decided, which is a different thing — an alert can be
    // acknowledged while still open, and resolving it later must not erase who
    // acknowledged it. Nullable so every existing row stays valid.
    version: 3,
    up(db) {
      db.exec(`
        ALTER TABLE alert_events ADD COLUMN acknowledged_at INTEGER;
        ALTER TABLE alert_events ADD COLUMN acknowledged_by TEXT;
      `);
    },
  },
  {
    // Sites (issue #78). A site groups routers — a router belongs to exactly one
    // site, or none. The membership itself lives on the router record in
    // routers.json (`siteId`), not here, because that is where the rest of a
    // router's configuration already is.
    //
    // This is deliberately NOT time-series data. purge() and deleteRouterData()
    // both name their five sample/event tables explicitly, so neither can reach
    // it — a retention purge must never delete organisational structure.
    version: 4,
    up(db) {
      db.exec(`
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
        );
      `);
    },
  },
  {
    // Groups and grants (issue #78).
    //
    // A grant is a triple: (principal, role, scope). It has no natural owner —
    // hanging it off the user strands group grants, off the group strands user
    // grants, off the site strands global ones — so it gets its own table. That
    // also makes the whole authorization state one greppable, diffable place.
    version: 5,
    up(db) {
      db.exec(`
        CREATE TABLE groups (
          id          TEXT PRIMARY KEY,
          name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
          description TEXT,
          created_at  INTEGER NOT NULL
        );

        -- Users live in users.json, so user_id cannot be a foreign key. The
        -- group side can be, and is: deleting a group takes its memberships.
        CREATE TABLE group_members (
          group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
          user_id  TEXT NOT NULL,
          PRIMARY KEY (group_id, user_id)
        );

        CREATE TABLE grants (
          id             TEXT PRIMARY KEY,
          principal_type TEXT NOT NULL CHECK (principal_type IN ('user','group')),
          principal_id   TEXT NOT NULL,
          role           TEXT NOT NULL CHECK (role IN ('viewer','operator','admin')),
          scope_type     TEXT NOT NULL CHECK (scope_type IN ('global','site','router')),
          -- '' for global scope, NOT NULL. This is not cosmetic: SQLite treats
          -- NULLs as distinct in a UNIQUE index, so storing NULL here would let
          -- one principal hold two global grants and the constraint below would
          -- silently never fire.
          scope_id       TEXT NOT NULL DEFAULT '',
          created_at     INTEGER NOT NULL,
          created_by     TEXT,
          CHECK ((scope_type =  'global' AND scope_id =  '')
              OR (scope_type <> 'global' AND scope_id <> '')),
          -- One role per principal per scope. A second grant on the same scope
          -- replaces the role rather than stacking, via ON CONFLICT DO UPDATE.
          UNIQUE (principal_type, principal_id, scope_type, scope_id)
        );

        CREATE INDEX idx_grants_principal ON grants(principal_type, principal_id);
        CREATE INDEX idx_grants_scope     ON grants(scope_type, scope_id);
      `);
    },
  },
  {
    // Per-user UI layouts, previously one JSON file per user per feature.
    //
    // Those files were the only stores in the project written with a bare
    // writeFileSync — no tmp+rename, no 0600 — so a crash mid-write truncated
    // one and the reader silently fell back to empty, losing the layout with no
    // error anywhere. They also had no cleanup path: deleting a user left their
    // files behind forever.
    //
    // data is opaque JSON text. These are preferences, not something anything
    // queries into, so normalising the shapes would buy nothing.
    version: 6,
    up(db) {
      db.exec(`
        CREATE TABLE user_layouts (
          -- '_shared' when auth mode is 'none' and there is no user identity,
          -- standing in for the old unsuffixed dashboard-layout.json.
          user_id    TEXT NOT NULL,
          kind       TEXT NOT NULL CHECK (kind IN ('dashboard','topology')),
          data       TEXT NOT NULL,
          updated_at INTEGER NOT NULL,
          PRIMARY KEY (user_id, kind)
        );
      `);
    },
  },
  {
    // Custom, page-scoped roles (issue #108).
    //
    // A role stops being one of three strings compiled into rbac.js and becomes
    // a row with a page matrix, so an operator can define "NOC Tier 1 sees Logs
    // and Reports and nothing else". `grants.role` carried a CHECK constraint
    // naming the three, and SQLite cannot drop a CHECK, so the table is rebuilt.
    //
    // The seeded page lists below are FROZEN LITERALS, deliberately not derived
    // from src/pages.js. A migration must do the same thing on every install
    // forever; if it read the live page registry, adding a 15th page later would
    // silently mean something different here on a fresh install than on an
    // upgraded one. Granting an existing role a new page is an administrator's
    // decision, and Administrator covers everything structurally regardless.
    version: 7,
    up(db) {
      db.exec(`
        CREATE TABLE roles (
          id          TEXT PRIMARY KEY,
          name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
          description TEXT,
          -- 1 = Administrator: reach is structural, not table-driven, so a
          -- permission added in a later release is covered with no data change.
          builtin     INTEGER NOT NULL DEFAULT 0,
          created_at  INTEGER NOT NULL
        );

        -- Absent row = no access. 'none' is deliberately not in the vocabulary:
        -- a second way to spell the same thing is a second thing to remember.
        -- One access column rather than two booleans, because two booleans can
        -- express write-without-read, which is nonsense the DB would then hold.
        CREATE TABLE role_pages (
          role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
          page    TEXT NOT NULL,
          access  TEXT NOT NULL CHECK (access IN ('read','write')),
          PRIMARY KEY (role_id, page)
        );
      `);

      const now = Date.now();
      const role = db.prepare(
        'INSERT OR IGNORE INTO roles (id, name, description, builtin, created_at) VALUES (?, ?, ?, ?, ?)');
      role.run('administrator', 'Administrator',
        'Full access to everything, including users, groups, roles and sites.', 1, now);
      role.run('operator', 'Operator',
        'Acknowledge alerts, read reports and run diagnostics.', 0, now);
      role.run('readonly', 'Read Only',
        'View live data only. No reports, no settings.', 0, now);

      // Reproduces exactly what viewer/operator grant today — not a generous
      // approximation. Read Only has NO reports row: today's viewer holds
      // router:read and nothing else, and a reports row confers router:history,
      // which would hand every existing viewer historical reports and exports
      // they do not have. Neither role gets a settings row.
      const READ_ONLY_PAGES = ['dashboard', 'topology', 'wireless', 'interfaces', 'dhcp',
                               'vpn', 'connections', 'routing', 'bandwidth', 'firewall',
                               'logs', 'routers'];
      // Operator adds reports (router:history) and writes on the two pages whose
      // actions it holds today: dashboard (router:ack), firewall (router:diagnose).
      const OPERATOR_WRITE  = ['dashboard', 'firewall'];

      const page = db.prepare('INSERT OR IGNORE INTO role_pages (role_id, page, access) VALUES (?, ?, ?)');
      for (const p of READ_ONLY_PAGES) page.run('readonly', p, 'read');
      for (const p of READ_ONLY_PAGES.concat('reports')) {
        page.run('operator', p, OPERATOR_WRITE.includes(p) ? 'write' : 'read');
      }

      // Rebuild grants onto role_id. ON DELETE RESTRICT makes "a role in use
      // cannot be deleted" an engine guarantee rather than a check a route has
      // to remember. Nothing references grants, so the drop is safe.
      //
      // `role` survives as a write-only mirror. Without it a v6 binary opened
      // against this database reads role: undefined, which reaches
      // ROLE_PERMS[undefined].has() and throws on EVERY authorization call —
      // a locked-out instance with no way back short of hand-editing SQLite.
      db.exec(`
        CREATE TABLE grants_new (
          id             TEXT PRIMARY KEY,
          principal_type TEXT NOT NULL CHECK (principal_type IN ('user','group')),
          principal_id   TEXT NOT NULL,
          role_id        TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
          role           TEXT,
          scope_type     TEXT NOT NULL CHECK (scope_type IN ('global','site','router')),
          scope_id       TEXT NOT NULL DEFAULT '',
          created_at     INTEGER NOT NULL,
          created_by     TEXT,
          CHECK ((scope_type =  'global' AND scope_id =  '')
              OR (scope_type <> 'global' AND scope_id <> '')),
          UNIQUE (principal_type, principal_id, scope_type, scope_id)
        );

        INSERT INTO grants_new
          (id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at, created_by)
        SELECT id, principal_type, principal_id,
               CASE role WHEN 'admin'    THEN 'administrator'
                         WHEN 'operator' THEN 'operator'
                         ELSE 'readonly' END,
               role, scope_type, scope_id, created_at, created_by
          FROM grants;

        DROP TABLE grants;
        ALTER TABLE grants_new RENAME TO grants;

        CREATE INDEX idx_grants_principal ON grants(principal_type, principal_id);
        CREATE INDEX idx_grants_scope     ON grants(scope_type, scope_id);
        CREATE INDEX idx_grants_role      ON grants(role_id);
      `);
    },
  },
  {
    // Make a downgrade survivable (issue #108).
    //
    // v7 left grants.role_id NOT NULL with no default. A rolled-back v6 binary
    // reads fine — the legacy `role` mirror is still there — but every grant
    // WRITE fails with "NOT NULL constraint failed: grants.role_id", so an
    // operator who rolls back can log in and yet cannot create or edit a user,
    // group or grant. Verified against the schema, not assumed.
    //
    // A default of 'readonly' means such a write lands on least privilege
    // instead of erroring. Re-upgrading then shows that grant as Read Only,
    // which is a visible narrowing rather than a silent widening — the safe
    // direction to fail in.
    //
    // Its own migration rather than an edit to v7: v7 has already run on
    // installs tracking this branch, and editing it in place would leave their
    // schema quietly different from a fresh install's.
    version: 8,
    up(db) {
      db.exec(`
        CREATE TABLE grants_v8 (
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

        INSERT INTO grants_v8
          (id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at, created_by)
        SELECT id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at, created_by
          FROM grants;

        DROP TABLE grants;
        ALTER TABLE grants_v8 RENAME TO grants;

        CREATE INDEX idx_grants_principal ON grants(principal_type, principal_id);
        CREATE INDEX idx_grants_scope     ON grants(scope_type, scope_id);
        CREATE INDEX idx_grants_role      ON grants(role_id);
      `);
    },
  },
  {
    // Per-user notification channels (issue #109).
    //
    // Channel credentials cannot live in users.json: that file must stay a bare
    // JSON array, because _readFile() returns [] for anything else and a
    // rolled-back binary reading zero users re-opens the unauthenticated setup
    // route. So this follows user_layouts instead — one row per user, opaque
    // JSON, no foreign key (users are not in SQLite to point at).
    //
    // `data` holds the same field names src/settings.js uses for channels, so
    // notifier.send() and notifier.hasConfiguredChannel() consume a row with no
    // changes; both are stateless and work off any object carrying those keys.
    // Credential sub-fields inside it are ciphertext from Settings.encrypt.
    //
    // Deliberately unreachable from purge() and deleteRouterData(), for the same
    // reason sites, groups, grants and layouts are: a retention sweep must never
    // be able to delete what a user configured. Cleanup is by user deletion only.
    version: 9,
    up(db) {
      db.exec(`
        CREATE TABLE user_notify_config (
          user_id    TEXT PRIMARY KEY,
          data       TEXT NOT NULL,
          updated_at INTEGER NOT NULL
        );
      `);
    },
  },
  {
    // A site's location, as a picked place rather than typed coordinates (#96).
    //
    // Migration 4 already reserved sites.lat/lon for this issue, and they keep
    // their meaning — they are still what gets plotted. What changes is where
    // they come from: nobody types a coordinate any more, they choose a town, so
    // these three columns record which town it was. Without them the map can
    // draw a marker but cannot say what it is standing on, and reopening the
    // site form would show an empty picker over a set location.
    //
    // Its own migration rather than an edit to v4: v4 has already run on every
    // install tracking this branch, and editing it in place would leave their
    // schema quietly different from a fresh install's.
    //
    // Nullable, because a site with no location is the common case and must not
    // read as coordinates 0,0.
    version: 10,
    up(db) {
      db.exec(`
        ALTER TABLE sites ADD COLUMN place_name   TEXT;
        ALTER TABLE sites ADD COLUMN place_region TEXT;
        ALTER TABLE sites ADD COLUMN place_cc     TEXT;
      `);
    },
  },
  {
    // The audit trail (write actions, router actions, authentication).
    //
    // Modelled on alert_events — autoincrement id, epoch-ms timestamp, a type
    // discriminator, a nullable target, free-text payload — with three
    // differences that matter:
    //
    //   scope        'app' or 'router', and it is what lets ONE query serve two
    //                audiences: app rows (a user created, a role edited) need
    //                system:principals, router rows are filtered to the routers
    //                the reader may see. Without this column the read endpoint
    //                would have to infer audience from router_id being null,
    //                which is the same thing said less clearly.
    //   target_name  denormalised on purpose. "deleted role Ops" is useless if
    //                reading it requires the role to still exist.
    //   outcome      ok | denied | failed. A refused write is exactly what an
    //                audit log exists to show, so it is a first-class value
    //                rather than an absent row.
    //
    // DELIBERATELY UNREACHABLE FROM purge() AND deleteRouterData(), the way
    // sites, groups, grants and layouts already are — and here the reason is
    // sharper than "a sweep must not delete config": an administrator clicking
    // Purge must not be able to erase the record of doing it, and "who deleted
    // router X" has to outlive router X. Retention is handled separately in
    // prune(), which is age-based and cannot be aimed at a single event.
    version: 11,
    up(db) {
      db.exec(`
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
      `);
    },
  },
  {
    // Widen user_layouts.kind to admit the nav preference (grouped sidebar).
    //
    // SQLite cannot drop a CHECK, so the table is rebuilt — the same shape as
    // the grants rebuild in v7. The nav preference belongs in this table rather
    // than one of its own precisely because this one already solves the parts
    // that are easy to forget: the '_shared' identity for authMode 'none', and
    // the deleteLayouts() cascade when a user is removed.
    //
    // A DOWNGRADE IS SURVIVABLE, which is the part worth stating: the widened
    // CHECK lives in the database rather than the binary, and an older binary
    // never selects kind='nav', so a nav row it does not understand sits there
    // inert instead of failing a query.
    version: 12,
    up(db) {
      db.exec(`
        CREATE TABLE user_layouts_v12 (
          user_id    TEXT NOT NULL,
          kind       TEXT NOT NULL CHECK (kind IN ('dashboard','topology','nav')),
          data       TEXT NOT NULL,
          updated_at INTEGER NOT NULL,
          PRIMARY KEY (user_id, kind)
        );
        INSERT INTO user_layouts_v12 (user_id, kind, data, updated_at)
          SELECT user_id, kind, data, updated_at FROM user_layouts;
        DROP TABLE user_layouts;
        ALTER TABLE user_layouts_v12 RENAME TO user_layouts;
      `);
    },
  },
  {
    version: 13,
    up(db) {
      // Configuration backups. Metadata only — the .rsc.gz / .backup pairs live
      // on disk under /data/config-backups, because a 3.4 MB binary per restore
      // point does not belong in a database that is otherwise time-series.
      //
      // EVERY run is recorded, including ones that changed nothing: "checked
      // daily, changed on these three dates" is the useful history, and it is
      // also how the scheduler knows whether a backup is due after a restart.
      //
      // `dir` is stored rather than re-derived from the router's label, so
      // renaming a router cannot orphan its backups.
      //
      // Like audit_events and the RBAC tables, this is deliberately absent from
      // PURGE_TABLES and from deleteRouterData(). A retention sweep for metrics
      // must never delete a restore point, and removing a router is exactly
      // when its last known-good configuration matters most.
      db.exec(`
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
        CREATE INDEX idx_config_backups_router ON config_backups (router_id, taken_at DESC);
      `);
    },
  },
  {
    version: 14,
    up(db) {
      // Scheduled email reports (#60).
      //
      // Two tables rather than one, unlike config_backups. A backup IS a run,
      // so folding them together is right there. A schedule is long-lived
      // configuration with many runs against it, and putting the history in
      // columns would erase "did last month's go out?" the moment this month's
      // fires — which is the question the whole feature exists to answer.
      //
      // There is deliberately no last_run_at mirror on the schedule. Due-ness
      // reads MAX(ran_at) from report_runs; a mirror is a second source of
      // truth that can disagree with the history it claims to summarise.
      //
      // Both tables are absent from PURGE_TABLES and deleteRouterData() for the
      // same reason config_backups is: those sweep time-series data, and a
      // schedule is configuration. Removing a router does delete its schedules,
      // but explicitly in the route where it is visible, rather than as a side
      // effect of a retention sweep.
      db.exec(`
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
        CREATE INDEX idx_report_schedules_router ON report_schedules (router_id);

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
        CREATE INDEX idx_report_runs_sched ON report_runs (schedule_id, ran_at DESC);
      `);
    },
  },
];

function _runMigrations(db) {
  const appliedVersions = new Set(
    db.prepare('SELECT version FROM schema_version').all().map(r => r.version)
  );
  for (const m of MIGRATIONS) {
    if (appliedVersions.has(m.version)) continue;
    db.transaction(() => {
      m.up(db);
      db.prepare('INSERT INTO schema_version (version, applied_at) VALUES (?, ?)').run(m.version, Date.now());
    })();
    console.log('%s', `[db] migration v${m.version} applied`);
  }
}

// ── Open / close ──────────────────────────────────────────────────────────────

function open() {
  if (_db) return _db;
  try { fs.mkdirSync(DATA_DIR, { recursive: true }); } catch (_) {}
  _db = new BetterSqlite(DB_FILE);
  _db.pragma('journal_mode = WAL');
  _db.pragma('synchronous = NORMAL');
  // SQLite defaults foreign_keys to OFF, and it is a per-CONNECTION setting, not
  // a property of the file. Without this, a REFERENCES ... ON DELETE CASCADE is
  // parsed and then ignored: no integrity, no cascade, and orphan rows piling up
  // invisibly. Set before _runMigrations so a migration relying on a cascade
  // behaves the same on first run as on every run after.
  _db.pragma('foreign_keys = ON');
  _db.exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);`);
  _runMigrations(_db);
  _prepareStatements();
  console.log('%s', `[db] opened ${DB_FILE}`);
  return _db;
}

function _prepareStatements() {
  _stmtInsertPing      = _db.prepare('INSERT INTO ping_samples    (router_id, target, rtt_ms, loss_pct, ts) VALUES (?, ?, ?, ?, ?)');
  _stmtInsertTraffic   = _db.prepare('INSERT INTO traffic_samples (router_id, interface, rx_mbps, tx_mbps, ts) VALUES (?, ?, ?, ?, ?)');
  _stmtInsertBandwidth = _db.prepare('INSERT INTO bandwidth_usage  (router_id, interface, rx_mb,   tx_mb,   ts) VALUES (?, ?, ?, ?, ?)');
  _stmtInsertAlert     = _db.prepare('INSERT INTO alert_events    (router_id, alert_type, subject, detail, fired_at) VALUES (?, ?, ?, ?, ?)');
  _stmtInsertConn    = _db.prepare('INSERT INTO connectivity_events (router_id, connected, ts) VALUES (?, ?, ?)');
  _stmtInsertAudit   = _db.prepare(`
    INSERT INTO audit_events
      (ts, actor_id, actor_name, actor_ip, action, scope, router_id,
       target_type, target_id, target_name, outcome, detail)
    VALUES (@ts, @actorId, @actorName, @actorIp, @action, @scope, @routerId,
            @targetType, @targetId, @targetName, @outcome, @detail)
  `);
  _stmtResolveAlert  = _db.prepare(`
    UPDATE alert_events SET resolved_at = ?
    WHERE router_id = ? AND alert_type = ? AND subject IS ? AND resolved_at IS NULL
  `);
}

function close() {
  if (_pruneTimer) { clearInterval(_pruneTimer); _pruneTimer = null; }
  _prepCache.clear();
  if (_db) { _db.close(); _db = null; }
}

// Lazily compile + cache prepared statements by SQL text. Query statements vary
// only by bound parameters (and, for agg queries, by a fixed set of bucket SQL
// fragments), so caching by the final SQL string reuses the compiled statement
// across calls instead of re-preparing on every request.
const _prepCache = new Map();
function _prep(sql) {
  let st = _prepCache.get(sql);
  if (!st) { st = _db.prepare(sql); _prepCache.set(sql, st); }
  return st;
}

// ── Writes ────────────────────────────────────────────────────────────────────

function insertPingSample(routerId, target, rttMs, lossPct, ts) {
  if (!_db) return;
  _stmtInsertPing.run(routerId, target, rttMs != null ? rttMs : null, lossPct, ts || Date.now());
}

function insertTrafficSample(routerId, iface, rxMbps, txMbps, ts) {
  if (!_db) return;
  _stmtInsertTraffic.run(routerId, iface, rxMbps, txMbps, ts || Date.now());
}

function insertBandwidthSample(routerId, iface, rxMb, txMb, ts) {
  if (!_db) return;
  _stmtInsertBandwidth.run(routerId, iface, rxMb, txMb, ts || Date.now());
}

/**
 * Is this alert already open?
 *
 * A separate question rather than a special return from insertAlertEvent: that
 * conflated "already knew" with "no database", and a caller reading `== null`
 * then swallowed a real notification whenever the database was unavailable. Two
 * meanings on one return value is how that happens.
 *
 * The rule it encodes is "at most one unresolved row per (router, type,
 * subject)". resolveAlertEvent has always closed *every* matching open row
 * rather than one, which was the tell that duplicates were being created.
 *
 * They were: the evaluator keeps edge-detection state in memory, and
 * dropEvaluator() wipes it on a router switch, a session rebuild and — most
 * often — an idle teardown, when nobody has had the router's page open for a
 * while. The rebuilt evaluator has no memory of having reported the thing, so it
 * reports it again. For a condition that persists, like an available RouterOS
 * update, that meant a fresh unacknowledged row every time somebody came back to
 * the page, and acknowledging one did nothing about the next.
 *
 * Asking the database rather than the evaluator makes the answer survive
 * evaluator drops, process restarts and router switches, and covers every alert
 * type at once. Unknown when there is no database — callers then behave as they
 * did before, which is to say something rather than nothing.
 */
function hasOpenAlert(routerId, alertType, subject) {
  if (!_db) return false;
  // `IS` rather than `=`: subject is NULL for router-wide alerts, and `= NULL`
  // never matches. The same comparison resolveAlertEvent uses.
  return !!_prep(`
    SELECT 1 FROM alert_events
    WHERE router_id = ? AND alert_type = ? AND subject IS ? AND resolved_at IS NULL
    LIMIT 1
  `).get(routerId, alertType, subject || null);
}

function insertAlertEvent(routerId, alertType, subject, detail) {
  if (!_db) return;
  return _stmtInsertAlert.run(routerId, alertType, subject || null, detail || null, Date.now()).lastInsertRowid;
}

/**
 * Close every open row matching (router, type, subject) and return their ids.
 *
 * The ids matter: the browser bell needs to know exactly which entries just
 * resolved. Without them it would have to re-derive the match by type+subject
 * on the client — a second implementation of the rule the UPDATE already
 * encodes, and the kind of duplication this whole change exists to remove.
 * Selected before the UPDATE because the WHERE clause stops matching after it.
 */
function resolveAlertEvent(routerId, alertType, subject) {
  if (!_db) return [];
  const subj = subject || null;
  const ids = _prep(`
    SELECT id FROM alert_events
    WHERE router_id = ? AND alert_type = ? AND subject IS ? AND resolved_at IS NULL
  `).all(routerId, alertType, subj).map(r => r.id);
  if (!ids.length) return [];
  _stmtResolveAlert.run(Date.now(), routerId, alertType, subj);
  return ids;
}

/** Everything still open for a router, newest first — the bell's initial state. */
function queryOpenAlerts(routerId, limit) {
  if (!_db) return [];
  return _prep(`
    SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events
    WHERE  router_id = ? AND resolved_at IS NULL
    ORDER  BY fired_at DESC LIMIT ?
  `).all(routerId, limit || 200);
}

/**
 * How many alerts are still open, per router — `{ routerId: count }`.
 *
 * One grouped query rather than queryOpenAlerts() per router: the Routers page
 * refreshes every two seconds and asks about every router a session can see, so
 * the per-router form would be N statements on a timer. Routers with nothing
 * open are absent rather than zero, so the caller decides what "no alerts"
 * looks like. Uses the existing (router_id, fired_at) index.
 */
function countOpenAlertsByRouter() {
  if (!_db) return {};
  const out = {};
  for (const row of _prep(`
    SELECT router_id, COUNT(*) AS n
    FROM   alert_events
    WHERE  resolved_at IS NULL
    GROUP  BY router_id
  `).all()) out[row.router_id] = row.n;
  return out;
}

/** Recently resolved rows, so the bell can show what just happened as well as
 *  what is still wrong. */
function queryRecentAlerts(routerId, sinceTs, limit) {
  if (!_db) return [];
  return _prep(`
    SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events
    WHERE  router_id = ? AND resolved_at IS NOT NULL AND resolved_at >= ?
    ORDER  BY resolved_at DESC LIMIT ?
  `).all(routerId, sinceTs || 0, limit || 50);
}

/** Acknowledge one row. Returns the updated row, or null if it did not exist.
 *  Deliberately does NOT require the alert to be open — acknowledging something
 *  after it recovered is a legitimate way to say "seen it". */
function acknowledgeAlert(id, username) {
  if (!_db) return null;
  _prep('UPDATE alert_events SET acknowledged_at = ?, acknowledged_by = ? WHERE id = ? AND acknowledged_at IS NULL')
    .run(Date.now(), username || null, id);
  return _prep(`
    SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM alert_events WHERE id = ?
  `).get(id) || null;
}

/**
 * Clear a router's alert list: resolve every row still open, and acknowledge
 * them on the way past. Returns the affected ids so the change can be pushed to
 * other connected browsers.
 *
 * Resolving is what the Routers page counts (see countOpenAlertsByRouter), so
 * acknowledging alone — which is all this used to do — emptied the bell while
 * leaving the router reading "Alerting" forever. That gap is the whole reason
 * this exists: an alert whose condition went away without the evaluator ever
 * seeing it clear has no other route out of the open set.
 *
 * Rows are kept, not deleted, so Reports and the CSV export still show what
 * happened. Deleting them is a separate, deliberate act and stays where it
 * already is, in Settings -> Database.
 */
function resolveAllAlerts(routerId, username) {
  if (!_db) return [];
  const ids = _prep('SELECT id FROM alert_events WHERE router_id = ? AND resolved_at IS NULL')
    .all(routerId).map(r => r.id);
  if (!ids.length) return [];
  const now = Date.now();
  _db.transaction(() => {
    // Acknowledge as well, and only where nobody has yet: whoever clears the
    // list is the one who saw it, but a row someone else already acknowledged
    // keeps their name. Skipping this would leave rows resolved by a person and
    // attributed to nobody, which reads in Reports exactly like the evaluator
    // having resolved them on its own.
    _prep(`UPDATE alert_events SET acknowledged_at = ?, acknowledged_by = ?
           WHERE router_id = ? AND resolved_at IS NULL AND acknowledged_at IS NULL`)
      .run(now, username || null, routerId);
    _prep('UPDATE alert_events SET resolved_at = ? WHERE router_id = ? AND resolved_at IS NULL')
      .run(now, routerId);
  })();
  return ids;
}

// ts is when the state change actually happened. It defaults to now because
// most callers report live transitions, but the offline paths debounce for
// connDownThresholdSec before declaring a router down — without passing the
// original disconnect time they would record the declaration instead, making
// every outage look shorter than it was (#99).
function insertConnectivityEvent(routerId, connected, ts) {
  if (!_db) return;
  _stmtInsertConn.run(routerId, connected ? 1 : 0, ts || Date.now());
}

// ── Queries ───────────────────────────────────────────────────────────────────

// Returns {select, group} SQL fragments for a given aggregation period.
// The select expr produces the bucket start timestamp in ms; group expr is the GROUP BY key.
function _aggBucket(agg) {
  if (agg === 'hour')  return { select: '(ts / 3600000) * 3600000',    group: '(ts / 3600000)' };
  if (agg === 'day')   return { select: '(ts / 86400000) * 86400000',   group: '(ts / 86400000)' };
  if (agg === 'week')  return { select: '(ts / 604800000) * 604800000', group: '(ts / 604800000)' };
  if (agg === 'month') return {
    select: "CAST(strftime('%s', strftime('%Y-%m-01', ts/1000, 'unixepoch')) AS INTEGER) * 1000",
    group:  "strftime('%Y-%m', ts/1000, 'unixepoch')",
  };
  return null;
}

// Nearest-rank percentile for one column over a range. SQLite has no percentile
// function; ORDER BY + OFFSET is exact and needs no extra index — the existing
// (router_id, interface, ts) index narrows the range and the sort runs over that
// subset only. `table` and `col` are literals supplied by this module, never by a
// caller, so they cannot carry injection.
function _percentileCol(table, col, routerId, iface, fromTs, toTs, n, pct) {
  if (!n || n < 1) return null;
  let off = Math.ceil((n * pct) / 100) - 1;
  if (off < 0)     off = 0;
  if (off > n - 1) off = n - 1;
  const row = _prep(`
    SELECT ${col} AS v FROM ${table}
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    ORDER  BY ${col} ASC LIMIT 1 OFFSET ?
  `).get(routerId, iface, fromTs, toTs, off);
  return row ? row.v : null;
}

// Rate summary for one interface over a range, computed entirely in SQL.
//
// This exists because the report stat cards used to be reduced from whatever
// rows the API returned, which made them wrong two ways at once: aggregated rows
// are averages, so the max across them is a peak of averages rather than a real
// peak, and the row queries are capped by LIMIT so totals silently truncated on
// long ranges. Computing here is correct regardless of the aggregation setting
// and regardless of how many rows are shipped.
function queryTrafficSummary(routerId, iface, fromTs, toTs, pct) {
  const empty = { samples: 0, rxAvgMbps: null, txAvgMbps: null, rxMaxMbps: null,
                  txMaxMbps: null, rxP95Mbps: null, txP95Mbps: null };
  if (!_db) return empty;
  const from = fromTs || 0;
  const to   = toTs   || Date.now();
  const p    = Math.min(99, Math.max(1, Number(pct) || 95));
  const r = _prep(`
    SELECT COUNT(*)     AS n,
           AVG(rx_mbps) AS rx_avg, AVG(tx_mbps) AS tx_avg,
           MAX(rx_mbps) AS rx_max, MAX(tx_mbps) AS tx_max
    FROM   traffic_samples
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
  `).get(routerId, iface, from, to);
  if (!r || !r.n) return empty;
  return {
    samples:   r.n,
    rxAvgMbps: r.rx_avg,
    txAvgMbps: r.tx_avg,
    rxMaxMbps: r.rx_max,
    txMaxMbps: r.tx_max,
    rxP95Mbps: _percentileCol('traffic_samples', 'rx_mbps', routerId, iface, from, to, r.n, p),
    txP95Mbps: _percentileCol('traffic_samples', 'tx_mbps', routerId, iface, from, to, r.n, p),
  };
}

// Volume summary for one interface over a range. Kept on bandwidth_usage rather
// than derived from traffic_samples: the two are the same measurement at
// different scalings but are not reliably interconvertible, because a bandwidth
// bucket is only written when the minute actually moved bytes and a minute may
// carry fewer than 60 samples.
function queryBandwidthSummary(routerId, iface, fromTs, toTs) {
  const empty = { samples: 0, rxTotalMb: 0, txTotalMb: 0, rxMaxMb: null, txMaxMb: null };
  if (!_db) return empty;
  const r = _prep(`
    SELECT COUNT(*)   AS n,
           SUM(rx_mb) AS rx_sum, SUM(tx_mb) AS tx_sum,
           MAX(rx_mb) AS rx_max, MAX(tx_mb) AS tx_max
    FROM   bandwidth_usage
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
  `).get(routerId, iface, fromTs || 0, toTs || Date.now());
  if (!r || !r.n) return empty;
  return { samples: r.n, rxTotalMb: r.rx_sum || 0, txTotalMb: r.tx_sum || 0,
           rxMaxMb: r.rx_max, txMaxMb: r.tx_max };
}

function queryPingSamples(routerId, fromTs, toTs, limit) {
  if (!_db) return [];
  return _prep(`
    SELECT ts, rtt_ms, loss_pct, target FROM ping_samples
    WHERE  router_id = ? AND ts >= ? AND ts <= ?
    ORDER  BY ts ASC LIMIT ?
  `).all(routerId, fromTs || 0, toTs || Date.now(), limit || 100000);
}

function queryTrafficSamples(routerId, iface, fromTs, toTs, limit) {
  if (!_db) return [];
  return _prep(`
    SELECT ts, interface, rx_mbps, tx_mbps FROM traffic_samples
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    ORDER  BY ts ASC LIMIT ?
  `).all(routerId, iface, fromTs || 0, toTs || Date.now(), limit || 100000);
}

function queryTrafficInterfaces(routerId) {
  if (!_db) return [];
  return _prep('SELECT DISTINCT interface FROM traffic_samples WHERE router_id = ? ORDER BY interface').all(routerId).map(r => r.interface);
}

function queryBandwidthSamples(routerId, iface, fromTs, toTs, limit) {
  if (!_db) return [];
  return _prep(`
    SELECT ts, interface, rx_mb, tx_mb FROM bandwidth_usage
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    ORDER  BY ts ASC LIMIT ?
  `).all(routerId, iface, fromTs || 0, toTs || Date.now(), limit || 100000);
}

function queryBandwidthInterfaces(routerId) {
  if (!_db) return [];
  return _prep('SELECT DISTINCT interface FROM bandwidth_usage WHERE router_id = ? ORDER BY interface').all(routerId).map(r => r.interface);
}

function queryPingSamplesAgg(routerId, fromTs, toTs, agg) {
  if (!_db) return [];
  const b = _aggBucket(agg);
  if (!b) return [];
  return _prep(`
    SELECT ${b.select} AS ts,
           target,
           AVG(CASE WHEN rtt_ms IS NOT NULL THEN rtt_ms ELSE NULL END) AS rtt_ms,
           AVG(loss_pct) AS loss_pct,
           COUNT(*) AS sample_count
    FROM   ping_samples
    WHERE  router_id = ? AND ts >= ? AND ts <= ?
    GROUP  BY ${b.group}, target
    ORDER  BY ts ASC LIMIT 10000
  `).all(routerId, fromTs || 0, toTs || Date.now());
}

function queryTrafficSamplesAgg(routerId, iface, fromTs, toTs, agg) {
  if (!_db) return [];
  const b = _aggBucket(agg);
  if (!b) return [];
  return _prep(`
    SELECT ${b.select} AS ts,
           interface,
           AVG(rx_mbps) AS rx_mbps,
           AVG(tx_mbps) AS tx_mbps,
           MAX(rx_mbps) AS rx_max_mbps,
           MAX(tx_mbps) AS tx_max_mbps,
           COUNT(*) AS sample_count
    FROM   traffic_samples
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    GROUP  BY ${b.group}
    ORDER  BY ts ASC LIMIT 10000
  `).all(routerId, iface, fromTs || 0, toTs || Date.now());
}

function queryBandwidthSamplesAgg(routerId, iface, fromTs, toTs, agg) {
  if (!_db) return [];
  const b = _aggBucket(agg);
  if (!b) return [];
  return _prep(`
    SELECT ${b.select} AS ts,
           interface,
           SUM(rx_mb) AS rx_mb,
           SUM(tx_mb) AS tx_mb,
           MAX(rx_mb) AS rx_max_mb,
           MAX(tx_mb) AS tx_max_mb,
           COUNT(*) AS sample_count
    FROM   bandwidth_usage
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    GROUP  BY ${b.group}
    ORDER  BY ts ASC LIMIT 10000
  `).all(routerId, iface, fromTs || 0, toTs || Date.now());
}

function queryConnectivityEventsAgg(routerId, fromTs, toTs, agg) {
  if (!_db) return [];
  const b = _aggBucket(agg);
  if (!b) return [];
  return _prep(`
    SELECT ${b.select} AS ts,
           COUNT(*) AS total,
           SUM(connected) AS online,
           COUNT(*) - SUM(connected) AS offline,
           ROUND(CAST(SUM(connected) AS REAL) / COUNT(*) * 100, 1) AS uptime_pct
    FROM   connectivity_events
    WHERE  router_id = ? AND ts >= ? AND ts <= ?
    GROUP  BY ${b.group}
    ORDER  BY ts ASC LIMIT 10000
  `).all(routerId, fromTs || 0, toTs || Date.now());
}

// Which router an alert belongs to. Needed before acknowledging one by id: the
// caller supplies only the id, but a restricted user must not be able to touch
// an alert on a router they cannot see.
function getAlertRouterId(id) {
  if (!_db) return null;
  const row = _prep('SELECT router_id FROM alert_events WHERE id = ?').get(id);
  return row ? row.router_id : null;
}

// ── Audit trail ───────────────────────────────────────────────────────────────

/**
 * Append one audit row. Never throws: an audit failure must not break the action
 * it describes, so a caller can record without a try/catch and without deciding
 * what to do when the disk is full. A dropped row is logged and the action
 * proceeds — the alternative is a write path that fails because its own
 * bookkeeping did.
 */
function insertAuditEvent(ev) {
  if (!_db) return false;
  try {
    _stmtInsertAudit.run({
      ts:         ev.ts || Date.now(),
      actorId:    ev.actorId    || null,
      actorName:  ev.actorName  || 'system',
      actorIp:    ev.actorIp    || null,
      action:     ev.action,
      scope:      ev.scope === 'router' ? 'router' : 'app',
      routerId:   ev.routerId   || null,
      targetType: ev.targetType || null,
      targetId:   ev.targetId   || null,
      targetName: ev.targetName || null,
      outcome:    ev.outcome    || 'ok',
      detail:     ev.detail == null ? null
                : (typeof ev.detail === 'string' ? ev.detail : JSON.stringify(ev.detail)),
    });
    return true;
  } catch (e) {
    console.error('%s', '[db] audit insert failed:', (e && e.message) || e);
    return false;
  }
}

/**
 * Read the trail, filtered and paged.
 *
 * `routerIds` is the concrete list of routers the caller may see (from
 * Rbac.effectiveRouterIds) and `includeApp` says whether app-scope rows are
 * permitted. Both are decided by the caller — this function does not know about
 * sessions — but note the shape: passing an empty routerIds array with
 * includeApp false yields NOTHING rather than everything. The old
 * "empty means unrestricted" bug class is exactly what that avoids.
 *
 * Real paging, unlike queryAlertEvents' silent 10 000-row cap: a truncated audit
 * log is worse than a slow one, so the total is returned alongside the page.
 */
function queryAuditEvents(opts) {
  if (!_db) return { rows: [], total: 0 };
  const o = opts || {};
  const where = [];
  const args  = [];

  // Visibility first, so no later filter can widen it.
  const ids = Array.isArray(o.routerIds) ? o.routerIds : [];
  if (o.includeApp && ids.length) {
    where.push(`(scope = 'app' OR router_id IN (${ids.map(() => '?').join(',')}))`);
    args.push(...ids);
  } else if (o.includeApp) {
    where.push(`scope = 'app'`);
  } else if (ids.length) {
    where.push(`(scope = 'router' AND router_id IN (${ids.map(() => '?').join(',')}))`);
    args.push(...ids);
  } else {
    return { rows: [], total: 0 };
  }

  if (o.from)     { where.push('ts >= ?'); args.push(o.from); }
  if (o.to)       { where.push('ts <= ?'); args.push(o.to); }
  if (o.routerId) { where.push('router_id = ?'); args.push(o.routerId); }
  if (o.actor)    { where.push('actor_name = ?'); args.push(o.actor); }
  if (o.outcome)  { where.push('outcome = ?'); args.push(o.outcome); }
  // Prefix match so 'router' selects router.create/update/delete without the
  // caller needing to know the verb list.
  if (o.action)   { where.push('action LIKE ?'); args.push(o.action + '%'); }
  if (o.search) {
    where.push('(action LIKE ? OR target_name LIKE ? OR detail LIKE ? OR actor_name LIKE ?)');
    const q = '%' + o.search + '%';
    args.push(q, q, q, q);
  }

  const sql   = 'FROM audit_events WHERE ' + where.join(' AND ');
  const total = _prep('SELECT COUNT(*) AS n ' + sql).get(...args).n;
  const limit  = Math.min(Math.max(parseInt(o.limit, 10) || 200, 1), 1000);
  const offset = Math.max(parseInt(o.offset, 10) || 0, 0);
  const rows = _prep(
    'SELECT id, ts, actor_id, actor_name, actor_ip, action, scope, router_id, ' +
    'target_type, target_id, target_name, outcome, detail ' + sql +
    ' ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?'
  ).all(...args, limit, offset);
  return { rows, total, limit, offset };
}

/** Distinct actors and actions, for the filter dropdowns. */
function auditFacets() {
  if (!_db) return { actors: [], actions: [] };
  return {
    actors:  _prep('SELECT DISTINCT actor_name FROM audit_events ORDER BY actor_name').all().map(r => r.actor_name),
    actions: _prep('SELECT DISTINCT action     FROM audit_events ORDER BY action').all().map(r => r.action),
  };
}

function queryAlertEvents(routerId, fromTs, toTs, limit) {
  if (!_db) return [];
  return _prep(`
    SELECT id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events
    WHERE  router_id = ? AND fired_at >= ? AND fired_at <= ?
    ORDER  BY fired_at DESC LIMIT ?
  `).all(routerId, fromTs || 0, toTs || Date.now(), limit || 10000);
}

function queryConnectivityEvents(routerId, fromTs, toTs, limit) {
  if (!_db) return [];
  return _prep(`
    SELECT ts, connected FROM connectivity_events
    WHERE  router_id = ? AND ts >= ? AND ts <= ?
    ORDER  BY ts ASC LIMIT ?
  `).all(routerId, fromTs || 0, toTs || Date.now(), limit || 10000);
}

// ── Retention / pruning ───────────────────────────────────────────────────────

function prune(retentionDays, alertRetentionDays, auditRetentionDays) {
  if (!_db) return;
  const metricCutoff = Date.now() - (retentionDays      || 90)  * 86400000;
  const alertCutoff  = Date.now() - (alertRetentionDays || 365) * 86400000;
  // Audit rows age out on their own setting, and only here. They are absent
  // from PURGE_TABLES and from deleteRouterData() on purpose, so age is the
  // ONLY thing that can remove one — nobody can aim a delete at a single event.
  const auditCutoff  = Date.now() - (auditRetentionDays || 365) * 86400000;
  const r1 = _prep('DELETE FROM ping_samples        WHERE ts < ?').run(metricCutoff);
  const r2 = _prep('DELETE FROM traffic_samples     WHERE ts < ?').run(metricCutoff);
  const r3 = _prep('DELETE FROM bandwidth_usage     WHERE ts < ?').run(metricCutoff);
  const r4 = _prep('DELETE FROM alert_events        WHERE fired_at < ?').run(alertCutoff);
  const r5 = _prep('DELETE FROM connectivity_events WHERE ts < ?').run(alertCutoff);
  const r6 = _prep('DELETE FROM audit_events        WHERE ts < ?').run(auditCutoff);
  const total = r1.changes + r2.changes + r3.changes + r4.changes + r5.changes + r6.changes;
  if (total > 0) {
    console.log('%s', `[db] pruned ${total} rows (metrics: ${retentionDays}d, events: ${alertRetentionDays}d)`);
    // Recorded so a shrinking history has an explanation. Required lazily: db.js
    // must not depend on audit.js, which depends on db.js.
    try {
      require('./audit').system().record({ action: 'db.prune', targetType: 'database',
        extra: { deleted: total, metricsDays: retentionDays, eventsDays: alertRetentionDays,
                 auditDays: auditRetentionDays } });
    } catch (_) { /* never let bookkeeping break the sweep */ }
  }
}

function startPruneInterval(getSettings) {
  if (_pruneTimer) return;
  const run = () => {
    const s = getSettings();
    prune(s.dbRetentionDays || 90, s.dbAlertRetentionDays || 365, s.dbAuditRetentionDays || 365);
  };
  run();
  _pruneTimer = setInterval(run, 24 * 3600 * 1000);
  _pruneTimer.unref();
}

// ── On-demand cleanup ─────────────────────────────────────────────────────────

// The five stores, keyed by the labels the cleanup UI offers. 'events' covers
// both alert and connectivity history because they share a retention setting
// and users think of them as one thing.
const PURGE_TABLES = {
  ping:      [{ table: 'ping_samples',        ts: 'ts' }],
  traffic:   [{ table: 'traffic_samples',     ts: 'ts' }],
  bandwidth: [{ table: 'bandwidth_usage',     ts: 'ts' }],
  events:    [{ table: 'alert_events',        ts: 'fired_at' },
              { table: 'connectivity_events', ts: 'ts' }],
};
const PURGE_TYPES = Object.keys(PURGE_TABLES);

// Build the WHERE clause shared by the count and the delete, so a preview can
// never disagree with what the delete actually removes.
function _purgeWhere(opts, tsCol) {
  const where = [], params = [];
  if (opts.routerId) { where.push('router_id = ?'); params.push(opts.routerId); }
  if (opts.olderThanMs > 0) { where.push(tsCol + ' < ?'); params.push(Date.now() - opts.olderThanMs); }
  return { sql: where.length ? ' WHERE ' + where.join(' AND ') : '', params };
}

function _purgeTargets(types) {
  const wanted = (Array.isArray(types) && types.length) ? types : PURGE_TYPES;
  return wanted.filter(t => PURGE_TABLES[t]).flatMap(t => PURGE_TABLES[t]);
}

// Row counts a purge with these options would remove, per type. Runs the same
// predicate as purge() so the number shown before confirming is exact.
function countPurge(opts = {}) {
  if (!_db) return { total: 0, byType: {} };
  const wanted = (Array.isArray(opts.types) && opts.types.length) ? opts.types : PURGE_TYPES;
  const byType = {};
  let total = 0;
  for (const type of wanted) {
    if (!PURGE_TABLES[type]) continue;
    let n = 0;
    for (const { table, ts } of PURGE_TABLES[type]) {
      const w = _purgeWhere(opts, ts);
      n += _prep(`SELECT COUNT(*) AS n FROM ${table}${w.sql}`).get(...w.params).n;
    }
    byType[type] = n;
    total += n;
  }
  return { total, byType };
}

// Delete matching rows. opts.routerId limits to one router (omit for all),
// opts.types limits to a subset of PURGE_TYPES (omit for all), opts.olderThanMs
// keeps anything newer than that age (0 or omitted deletes regardless of age).
function purge(opts = {}) {
  if (!_db) return { deleted: 0 };
  let deleted = 0;
  _db.transaction(() => {
    for (const { table, ts } of _purgeTargets(opts.types)) {
      const w = _purgeWhere(opts, ts);
      deleted += _prep(`DELETE FROM ${table}${w.sql}`).run(...w.params).changes;
    }
  })();
  console.log('%s', `[db] purge removed ${deleted} rows (router: ${opts.routerId || 'all'}, types: ${(opts.types || PURGE_TYPES).join('+')}, olderThanMs: ${opts.olderThanMs || 0})`);
  return { deleted };
}

// SQLite keeps freed pages inside the file, so a delete alone never shrinks it
// on disk. Callers run this after a purge; it cannot go inside purge()'s
// transaction because VACUUM is not allowed within one.
function vacuum() {
  if (!_db) return { before: 0, after: 0 };
  const before = _fileSize();
  // Fold the WAL into the main file first. We run in WAL mode, so a delete's
  // freed pages sit in the -wal until a checkpoint; VACUUM on its own then has
  // nothing to reclaim and the file on disk does not shrink at all. Checkpoint
  // again afterwards so the rewritten file is what the caller measures.
  _db.pragma('wal_checkpoint(TRUNCATE)');
  _db.exec('VACUUM');
  _db.pragma('wal_checkpoint(TRUNCATE)');
  const after = _fileSize();
  console.log('%s', `[db] vacuum reclaimed ${Math.max(0, before - after)} bytes`);
  return { before, after };
}

function _fileSize() {
  let total = 0;
  for (const suffix of ['', '-wal']) {
    try { total += fs.statSync(DB_FILE + suffix).size; } catch (_) {}
  }
  return total;
}

// Size on disk plus row counts per type, overall and broken down by router, so
// the cleanup UI can show what is actually taking up space.
function stats() {
  if (!_db) return { bytes: 0, total: 0, byType: {}, oldestTs: null, byRouter: [] };
  const byType = {};
  const perRouter = new Map();
  let total = 0;
  for (const type of PURGE_TYPES) {
    let n = 0;
    for (const { table } of PURGE_TABLES[type]) {
      n += _prep(`SELECT COUNT(*) AS n FROM ${table}`).get().n;
      for (const row of _prep(`SELECT router_id, COUNT(*) AS n FROM ${table} GROUP BY router_id`).all()) {
        perRouter.set(row.router_id, (perRouter.get(row.router_id) || 0) + row.n);
      }
    }
    byType[type] = n;
    total += n;
  }
  const oldest = _prep(`
    SELECT MIN(t) AS t FROM (
      SELECT MIN(ts) AS t FROM ping_samples        UNION ALL
      SELECT MIN(ts) AS t FROM traffic_samples     UNION ALL
      SELECT MIN(ts) AS t FROM bandwidth_usage     UNION ALL
      SELECT MIN(ts) AS t FROM connectivity_events UNION ALL
      SELECT MIN(fired_at) AS t FROM alert_events
    )`).get().t;
  return {
    bytes: _fileSize(),
    total,
    byType,
    oldestTs: oldest || null,
    byRouter: [...perRouter.entries()].map(([routerId, rows]) => ({ routerId, rows }))
                                      .sort((a, b) => b.rows - a.rows),
  };
}

function deleteRouterData(routerId) {
  if (!_db) return;
  _db.transaction(() => {
    _prep('DELETE FROM ping_samples        WHERE router_id = ?').run(routerId);
    _prep('DELETE FROM traffic_samples     WHERE router_id = ?').run(routerId);
    _prep('DELETE FROM bandwidth_usage     WHERE router_id = ?').run(routerId);
    _prep('DELETE FROM alert_events        WHERE router_id = ?').run(routerId);
    _prep('DELETE FROM connectivity_events WHERE router_id = ?').run(routerId);
  })();
  console.log('%s', `[db] deleted all data for router ${routerId}`);
}

// ── Configuration backups ────────────────────────────────────────────────────
// Note what is NOT above: config_backups is absent from deleteRouterData() and
// from PURGE_TABLES, on purpose. Nothing that sweeps time-series data can reach
// a restore point. Pruning backups is its own thing, bounded by the per-router
// keepCount/keepDays, and it clears `stem` rather than the row — the history of
// when a router was checked outlives the artefacts.

function recordBackup(row) {
  if (!_db) return null;
  try {
    const info = _prep(`
      INSERT INTO config_backups
        (router_id, taken_at, outcome, source, actor, stem, dir, fingerprint,
         rsc_bytes, backup_bytes, model, serial, os_version, ms, error)
      VALUES
        (@routerId, @takenAt, @outcome, @source, @actor, @stem, @dir, @fingerprint,
         @rscBytes, @backupBytes, @model, @serial, @osVersion, @ms, @error)
    `).run({
      routerId:    row.routerId,
      takenAt:     row.takenAt || Date.now(),
      outcome:     row.outcome,
      source:      row.source || 'schedule',
      actor:       row.actor || null,
      stem:        row.stem || null,
      dir:         row.dir || null,
      fingerprint: row.fingerprint || null,
      rscBytes:    row.rscBytes || 0,
      backupBytes: row.backupBytes || 0,
      model:       (row.identity && row.identity.model) || null,
      serial:      (row.identity && row.identity.serial) || null,
      osVersion:   (row.identity && row.identity.osVersion) || null,
      ms:          row.ms || 0,
      error:       row.error || null,
    });
    return info.lastInsertRowid;
  } catch (e) {
    console.error('%s', '[db] backup record failed:', (e && e.message) || e);
    return null;
  }
}

/** Runs for one router, newest first. */
function listBackups(routerId, limit) {
  if (!_db) return [];
  return _prep(`SELECT * FROM config_backups WHERE router_id = ?
                ORDER BY taken_at DESC LIMIT ?`).all(routerId, Math.min(Number(limit) || 200, 1000));
}

function getBackup(id) {
  if (!_db) return null;
  return _prep('SELECT * FROM config_backups WHERE id = ?').get(Number(id)) || null;
}

/**
 * The configuration this router was last seen with.
 *
 * Any run that read an export has a fingerprint, whether or not it stored a
 * pair — so an unchanged run still moves this forward and a failed one leaves
 * it alone. That is what stops a transient failure from being reported as
 * drift on the next successful run.
 */
function latestFingerprint(routerId) {
  if (!_db) return null;
  const row = _prep(`SELECT fingerprint FROM config_backups
                     WHERE router_id = ? AND fingerprint IS NOT NULL
                     ORDER BY taken_at DESC LIMIT 1`).get(routerId);
  return row ? row.fingerprint : null;
}

/**
 * When this router was last attempted, at all.
 *
 * Read from the database rather than held in memory, so a restart neither
 * skips a due backup nor re-runs one taken a minute ago — the failure mode of
 * a purely in-process timer.
 */
function lastBackupRun(routerId) {
  if (!_db) return 0;
  const row = _prep(`SELECT taken_at FROM config_backups WHERE router_id = ?
                     ORDER BY taken_at DESC LIMIT 1`).get(routerId);
  return row ? row.taken_at : 0;
}

/** The stored pairs for a router, newest first — rows that still have files. */
function storedBackups(routerId) {
  if (!_db) return [];
  return _prep(`SELECT * FROM config_backups
                WHERE router_id = ? AND stem IS NOT NULL AND pruned_at IS NULL
                ORDER BY taken_at DESC`).all(routerId);
}

/**
 * Remove a backup row outright — a deliberate operator delete, not retention.
 *
 * The two are different acts and get different treatment. Retention aging a pair
 * out is something MikroDash did on its own, so markBackupPruned keeps the row
 * and the History table explains the disappearance. Pressing Delete is somebody
 * saying "I do not want this listed", and leaving a tombstone behind answers a
 * question they did not ask.
 *
 * The trail is not lost: audit_events independently records the backup.run that
 * created it and the backup.delete that removed it, and the audit table is the
 * one place deliberately hard to erase. The caller resolves the id router-first,
 * so this can only ever be aimed at a row it was already allowed to see.
 */
function deleteBackup(id) {
  if (!_db) return false;
  const info = _prep('DELETE FROM config_backups WHERE id = ?').run(Number(id));
  return info.changes > 0;
}

/** The artefacts are gone; the record of the run is not. Retention's half. */
function markBackupPruned(id, ts) {
  if (!_db) return false;
  const info = _prep('UPDATE config_backups SET pruned_at = ? WHERE id = ?')
    .run(ts || Date.now(), Number(id));
  return info.changes > 0;
}

// ── Scheduled email reports (#60) ────────────────────────────────────────────
// Same placement reasoning as config_backups above: absent from PURGE_TABLES
// and deleteRouterData(), because a schedule is configuration rather than
// telemetry. Removing a router calls deleteReportSchedulesForRouter explicitly.

function listReportSchedules() {
  if (!_db) return [];
  return _prep('SELECT * FROM report_schedules ORDER BY created_at').all();
}

function listReportSchedulesFor(routerId) {
  if (!_db) return [];
  return _prep('SELECT * FROM report_schedules WHERE router_id = ? ORDER BY created_at')
    .all(routerId);
}

function getReportSchedule(id) {
  if (!_db) return null;
  return _prep('SELECT * FROM report_schedules WHERE id = ?').get(String(id)) || null;
}

/** Insert or replace a whole schedule row. Validation belongs to the caller. */
function upsertReportSchedule(row) {
  if (!_db) return null;
  _prep(`
    INSERT INTO report_schedules
      (id, router_id, name, sections, interface, aggregate, recipients, frequency,
       send_hour, enabled, disabled_reason, created_by, created_at, updated_at)
    VALUES
      (@id, @routerId, @name, @sections, @iface, @aggregate, @recipients, @frequency,
       @sendHour, @enabled, @disabledReason, @createdBy, @createdAt, @updatedAt)
    ON CONFLICT(id) DO UPDATE SET
      name = @name, sections = @sections, interface = @iface, aggregate = @aggregate,
      recipients = @recipients, frequency = @frequency, send_hour = @sendHour,
      enabled = @enabled, disabled_reason = @disabledReason, updated_at = @updatedAt
  `).run({
    id: row.id, routerId: row.routerId, name: row.name,
    sections: JSON.stringify(row.sections || []),
    iface: row.iface || null,
    aggregate: row.aggregate || '',
    recipients: JSON.stringify(row.recipients || []),
    frequency: row.frequency,
    sendHour: row.sendHour == null ? 7 : row.sendHour,
    enabled: row.enabled ? 1 : 0,
    disabledReason: row.disabledReason || null,
    createdBy: row.createdBy || null,
    createdAt: row.createdAt || Date.now(),
    updatedAt: row.updatedAt || Date.now(),
  });
  return getReportSchedule(row.id);
}

/**
 * Switch a schedule off with a reason.
 *
 * The scheduler disables rather than deletes when a router disappears or a
 * creator loses access: the record of why it stopped is worth more than the row
 * is worth reclaiming, and a silently vanished schedule is indistinguishable
 * from one that never existed.
 */
function setReportScheduleEnabled(id, enabled, reason) {
  if (!_db) return false;
  const info = _prep(`UPDATE report_schedules
                      SET enabled = ?, disabled_reason = ?, updated_at = ?
                      WHERE id = ?`)
    .run(enabled ? 1 : 0, enabled ? null : (reason || null), Date.now(), String(id));
  return info.changes > 0;
}

function deleteReportSchedule(id) {
  if (!_db) return false;
  return _prep('DELETE FROM report_schedules WHERE id = ?').run(String(id)).changes > 0;
}

/** Called from the router-delete route: a schedule for a gone router cannot run. */
function deleteReportSchedulesForRouter(routerId) {
  if (!_db) return 0;
  return _prep('DELETE FROM report_schedules WHERE router_id = ?').run(routerId).changes;
}

/** Newest run history per schedule, bounded so it cannot grow without limit. */
const REPORT_RUN_KEEP = 100;

function recordReportRun(row) {
  if (!_db) return null;
  try {
    const info = _prep(`
      INSERT INTO report_runs
        (schedule_id, ran_at, period_from, period_to, outcome, source, actor,
         recipients_n, bytes, rows_n, ms, error)
      VALUES
        (@scheduleId, @ranAt, @periodFrom, @periodTo, @outcome, @source, @actor,
         @recipientsN, @bytes, @rowsN, @ms, @error)
    `).run({
      scheduleId: row.scheduleId,
      ranAt: row.ranAt || Date.now(),
      periodFrom: row.periodFrom || 0,
      periodTo: row.periodTo || 0,
      outcome: row.outcome,
      source: row.source || 'schedule',
      actor: row.actor || null,
      recipientsN: row.recipientsN || 0,
      bytes: row.bytes || 0,
      rowsN: row.rowsN || 0,
      ms: row.ms || 0,
      error: row.error || null,
    });
    _prep(`DELETE FROM report_runs WHERE schedule_id = ? AND id NOT IN
             (SELECT id FROM report_runs WHERE schedule_id = ? ORDER BY ran_at DESC LIMIT ?)`)
      .run(row.scheduleId, row.scheduleId, REPORT_RUN_KEEP);
    return info.lastInsertRowid;
  } catch (e) {
    console.error('%s', '[db] report run record failed:', (e && e.message) || e);
    return null;
  }
}

/**
 * What due-ness needs to know: when this schedule last ran, how that went, and
 * how many attempts already fall inside the period being considered.
 */
function reportRunHistory(scheduleId, periodFrom, periodTo) {
  if (!_db) return { lastRun: 0, lastOutcome: null, runsInPeriod: 0 };
  const last = _prep(`SELECT ran_at, outcome FROM report_runs WHERE schedule_id = ?
                      ORDER BY ran_at DESC LIMIT 1`).get(scheduleId);
  const inPeriod = _prep(`SELECT COUNT(*) AS n FROM report_runs
                          WHERE schedule_id = ? AND period_from = ? AND period_to = ?`)
    .get(scheduleId, periodFrom, periodTo) || { n: 0 };
  return {
    lastRun: last ? last.ran_at : 0,
    lastOutcome: last ? last.outcome : null,
    runsInPeriod: inPeriod.n || 0,
  };
}

function listReportRuns(scheduleId, limit) {
  if (!_db) return [];
  return _prep(`SELECT * FROM report_runs WHERE schedule_id = ?
                ORDER BY ran_at DESC LIMIT ?`)
    .all(scheduleId, Math.min(Number(limit) || 20, REPORT_RUN_KEEP));
}

/** Counts and bytes for the page header, per router. */
function backupSummary(routerId) {
  if (!_db) return { runs: 0, stored: 0, bytes: 0, lastAt: 0, lastOutcome: null };
  const agg = _prep(`
    SELECT COUNT(*) AS runs,
           SUM(CASE WHEN stem IS NOT NULL AND pruned_at IS NULL THEN 1 ELSE 0 END) AS stored,
           SUM(CASE WHEN stem IS NOT NULL AND pruned_at IS NULL
                    THEN rsc_bytes + backup_bytes ELSE 0 END) AS bytes
    FROM config_backups WHERE router_id = ?`).get(routerId) || {};
  const last = _prep(`SELECT taken_at, outcome FROM config_backups WHERE router_id = ?
                      ORDER BY taken_at DESC LIMIT 1`).get(routerId);
  return {
    runs: agg.runs || 0,
    stored: agg.stored || 0,
    bytes: agg.bytes || 0,
    lastAt: last ? last.taken_at : 0,
    lastOutcome: last ? last.outcome : null,
  };
}

// ── Sites (issue #78) ────────────────────────────────────────────────────────
// Persistence only. Validation of names, lengths and coordinate ranges lives in
// the route layer, matching how routers and users are handled.

function listSites() {
  if (!_db) return [];
  return _prep(`SELECT id, name, description, lat, lon,
                       place_name, place_region, place_cc, created_at
                FROM sites ORDER BY name COLLATE NOCASE`).all();
}

function getSite(id) {
  if (!_db) return null;
  return _prep(`SELECT id, name, description, lat, lon,
                       place_name, place_region, place_cc, created_at
                FROM sites WHERE id = ?`).get(id) || null;
}

function createSite({
  name, description = null, lat = null, lon = null,
  place_name = null, place_region = null, place_cc = null,
}) {
  if (!_db) return null;
  const id = crypto.randomUUID();
  _prep(`INSERT INTO sites (id, name, description, lat, lon,
                            place_name, place_region, place_cc, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
    .run(id, name, description, lat, lon, place_name, place_region, place_cc, Date.now());
  return getSite(id);
}

// Only the fields actually supplied are written, so a caller updating just the
// name cannot silently blank a description or a location it never sent.
function updateSite(id, fields) {
  if (!_db) return null;
  const sets = [], params = [];
  // The five location columns are written as one unit by the route layer, so a
  // half-set location — lat without lon — is not reachable from here.
  for (const col of ['name', 'description', 'lat', 'lon',
                     'place_name', 'place_region', 'place_cc']) {
    if (fields[col] !== undefined) { sets.push(`${col} = ?`); params.push(fields[col]); }
  }
  if (!sets.length) return getSite(id);
  params.push(id);
  _prep(`UPDATE sites SET ${sets.join(', ')} WHERE id = ?`).run(...params);
  return getSite(id);
}

// Returns whether a row was removed. Clearing `siteId` on the routers that
// belonged to it is the caller's job — routers live in routers.json, so SQLite
// cannot cascade into them.
function deleteSite(id) {
  if (!_db) return false;
  return _prep('DELETE FROM sites WHERE id = ?').run(id).changes > 0;
}

// ── Per-user UI layouts ──────────────────────────────────────────────────────
// Opaque JSON preference blobs, keyed by (user_id, kind). SHARED_LAYOUT_USER is
// the stand-in identity for authMode 'none', where there is no user to key on.

const SHARED_LAYOUT_USER = '_shared';

function getLayout(userId, kind) {
  if (!_db) return null;
  const row = _prep('SELECT data FROM user_layouts WHERE user_id = ? AND kind = ?')
    .get(userId || SHARED_LAYOUT_USER, kind);
  if (!row) return null;
  // A corrupt blob starts the user clean rather than 500ing a whole page over a
  // saved card position — the same forgiveness the old file readers had.
  try { return JSON.parse(row.data); } catch (_) { return null; }
}

function setLayout(userId, kind, data) {
  if (!_db) return false;
  _prep(`INSERT INTO user_layouts (user_id, kind, data, updated_at) VALUES (?, ?, ?, ?)
         ON CONFLICT (user_id, kind) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`)
    .run(userId || SHARED_LAYOUT_USER, kind, JSON.stringify(data), Date.now());
  return true;
}

/** Called when a user is deleted. The JSON files had no such path, so every
 *  deleted user left their layouts behind on disk indefinitely. */
function deleteLayouts(userId) {
  if (!_db || !userId) return 0;
  return _prep('DELETE FROM user_layouts WHERE user_id = ?').run(userId).changes;
}

function layoutCount() {
  if (!_db) return 0;
  return _prep('SELECT COUNT(*) c FROM user_layouts').get().c;
}

// ── Per-user notification channels (issue #109) ──────────────────────────────
// Same idiom as the layouts above: opaque JSON keyed by user id. Unlike layouts
// there is no `kind` dimension, so the user id alone is the primary key. No
// SHARED_LAYOUT_USER fallback either — a personal channel needs a person, and
// authMode 'none' has none, so callers pass a real user id or get nothing.

function getUserNotifyConfig(userId) {
  if (!_db || !userId) return null;
  const row = _prep('SELECT data FROM user_notify_config WHERE user_id = ?').get(userId);
  if (!row) return null;
  // A corrupt blob reads as "not configured" rather than throwing inside the
  // alert path, where it would take down delivery for every other recipient too.
  try { return JSON.parse(row.data); } catch (_) { return null; }
}

function setUserNotifyConfig(userId, data) {
  if (!_db || !userId) return false;
  _prep(`INSERT INTO user_notify_config (user_id, data, updated_at) VALUES (?, ?, ?)
         ON CONFLICT (user_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`)
    .run(userId, JSON.stringify(data), Date.now());
  return true;
}

/** Called when a user is deleted, alongside deleteLayouts. */
function deleteUserNotifyConfig(userId) {
  if (!_db || !userId) return 0;
  return _prep('DELETE FROM user_notify_config WHERE user_id = ?').run(userId).changes;
}

/** Every saved config, for the alerter's per-alert recipient resolution.
 *  Returns rows rather than users: someone who never opened the panel has no
 *  row, so on most installs this is empty and the fan-out costs nothing. */
function listUserNotifyConfigs() {
  if (!_db) return [];
  const rows = _prep('SELECT user_id, data FROM user_notify_config').all();
  const out = [];
  for (const r of rows) {
    try { out.push({ userId: r.user_id, config: JSON.parse(r.data) }); } catch (_) { /* skip corrupt */ }
  }
  return out;
}

// ── Groups and grants (issue #78) ────────────────────────────────────────────
// Persistence only; the policy that interprets these rows lives in src/rbac.js.

function listGroups() {
  if (!_db) return [];
  return _prep('SELECT id, name, description, created_at FROM groups ORDER BY name COLLATE NOCASE').all();
}

function getGroup(id) {
  if (!_db) return null;
  return _prep('SELECT id, name, description, created_at FROM groups WHERE id = ?').get(id) || null;
}

function createGroup({ name, description = null }) {
  if (!_db) return null;
  const id = crypto.randomUUID();
  _prep('INSERT INTO groups (id, name, description, created_at) VALUES (?, ?, ?, ?)')
    .run(id, name, description, Date.now());
  return getGroup(id);
}

function updateGroup(id, fields) {
  if (!_db) return null;
  const sets = [], params = [];
  for (const col of ['name', 'description']) {
    if (fields[col] !== undefined) { sets.push(`${col} = ?`); params.push(fields[col]); }
  }
  if (!sets.length) return getGroup(id);
  params.push(id);
  _prep(`UPDATE groups SET ${sets.join(', ')} WHERE id = ?`).run(...params);
  return getGroup(id);
}

// Memberships cascade via the foreign key; the group's own grants do not, since
// principal_id is polymorphic and cannot be one. Both go in a transaction so a
// group can never outlive its grants or vice versa.
function deleteGroup(id) {
  if (!_db) return false;
  let removed = false;
  _db.transaction(() => {
    _prep("DELETE FROM grants WHERE principal_type = 'group' AND principal_id = ?").run(id);
    removed = _prep('DELETE FROM groups WHERE id = ?').run(id).changes > 0;
  })();
  return removed;
}

function getGroupMembers(groupId) {
  if (!_db) return [];
  return _prep('SELECT user_id FROM group_members WHERE group_id = ?').all(groupId).map(r => r.user_id);
}

// Replace the whole membership list in one transaction — a partial write would
// silently drop people's access.
function setGroupMembers(groupId, userIds) {
  if (!_db) return [];
  const ids = Array.from(new Set((userIds || []).map(String)));
  _db.transaction(() => {
    _prep('DELETE FROM group_members WHERE group_id = ?').run(groupId);
    const ins = _prep('INSERT INTO group_members (group_id, user_id) VALUES (?, ?)');
    for (const uid of ids) ins.run(groupId, uid);
  })();
  return ids;
}

function groupIdsForUser(userId) {
  if (!_db) return [];
  return _prep('SELECT group_id FROM group_members WHERE user_id = ?').all(userId).map(r => r.group_id);
}

// ── Roles (issue #108) ───────────────────────────────────────────────────────
//
// The seed ids are stable literals, so the v7 migration's CASE is deterministic
// and tests can name them. Custom roles get a UUID. Renaming a role changes
// `name`, never `id` — every reference is by id.
const _ROLE_ID_BY_LEGACY = Object.freeze({ admin: 'administrator', operator: 'operator', viewer: 'readonly' });
const _LEGACY_BY_ROLE_ID = Object.freeze({ administrator: 'admin', operator: 'operator', readonly: 'viewer' });

/**
 * The legacy role string to mirror into `grants.role` for a given role id.
 * Only a downgraded (v6) binary ever reads it. A custom role has no legacy
 * equivalent, so it mirrors as the least-privileged value — a downgrade must
 * never widen anyone's access.
 */
function _legacyMirror(roleId) { return _LEGACY_BY_ROLE_ID[roleId] || 'viewer'; }

function listRoles() {
  if (!_db) return [];
  return _prep(`SELECT id, name, description, builtin, created_at FROM roles
                ORDER BY builtin DESC, name COLLATE NOCASE`).all();
}

function getRole(id) {
  if (!_db) return null;
  return _prep('SELECT id, name, description, builtin, created_at FROM roles WHERE id = ?').get(id) || null;
}

function createRole({ name, description = null }) {
  if (!_db) return null;
  const id = crypto.randomUUID();
  _prep('INSERT INTO roles (id, name, description, builtin, created_at) VALUES (?, ?, ?, 0, ?)')
    .run(id, name, description, Date.now());
  return getRole(id);
}

// Only name and description are mutable; `builtin` and `id` are not, so a
// custom role can never promote itself into the structural one.
function updateRole(id, fields) {
  if (!_db) return null;
  const set = [], params = [];
  for (const col of ['name', 'description']) {
    if (fields[col] !== undefined) { set.push(col + ' = ?'); params.push(fields[col]); }
  }
  if (!set.length) return getRole(id);
  params.push(id);
  _prep(`UPDATE roles SET ${set.join(', ')} WHERE id = ?`).run(...params);
  return getRole(id);
}

/**
 * Refuses on the builtin role. A role still referenced by a grant is refused by
 * the engine (ON DELETE RESTRICT) — countGrantsForRole() exists so the caller
 * can say how many rather than surfacing a bare constraint error.
 */
function deleteRole(id) {
  if (!_db) return false;
  const row = getRole(id);
  if (!row || row.builtin) return false;
  return _prep('DELETE FROM roles WHERE id = ?').run(id).changes > 0;
}

function countGrantsForRole(roleId) {
  if (!_db) return 0;
  return _prep('SELECT COUNT(*) AS n FROM grants WHERE role_id = ?').get(roleId).n;
}

function rolePages(roleId) {
  if (!_db) return [];
  return _prep('SELECT page, access FROM role_pages WHERE role_id = ? ORDER BY page').all(roleId);
}

/** Replace a role's whole matrix. Delete-then-insert, one transaction. */
function setRolePages(roleId, pages) {
  if (!_db) return [];
  _db.transaction(() => {
    _prep('DELETE FROM role_pages WHERE role_id = ?').run(roleId);
    const ins = _prep('INSERT INTO role_pages (role_id, page, access) VALUES (?, ?, ?)');
    for (const p of pages || []) {
      if (p && p.page && (p.access === 'read' || p.access === 'write')) ins.run(roleId, p.page, p.access);
    }
  })();
  return rolePages(roleId);
}

function listGrants(filter = {}) {
  if (!_db) return [];
  const where = [], params = [];
  if (filter.principalType) { where.push('principal_type = ?'); params.push(filter.principalType); }
  if (filter.principalId)   { where.push('principal_id = ?');   params.push(filter.principalId); }
  if (filter.scopeType)     { where.push('scope_type = ?');     params.push(filter.scopeType); }
  if (filter.scopeId)       { where.push('scope_id = ?');       params.push(filter.scopeId); }
  return _prep(`SELECT id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at, created_by
                FROM grants ${where.length ? 'WHERE ' + where.join(' AND ') : ''}
                ORDER BY created_at`).all(...params);
}

// One role per principal per scope: granting again on the same scope changes the
// role instead of stacking a second row that would have to be resolved later.
// Takes `roleId`, or the legacy `role` name for callers not yet migrated — one
// of the two is derived from the other, so both columns are always consistent.
function upsertGrant({ principalType, principalId, role, roleId, scopeType, scopeId = '', createdBy = null }) {
  if (!_db) return null;
  const rid = roleId || _ROLE_ID_BY_LEGACY[role] || 'readonly';
  const sid = scopeType === 'global' ? '' : String(scopeId || '');
  _prep(`INSERT INTO grants (id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at, created_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT (principal_type, principal_id, scope_type, scope_id)
         DO UPDATE SET role_id = excluded.role_id, role = excluded.role,
                       created_at = excluded.created_at, created_by = excluded.created_by`)
    .run(crypto.randomUUID(), principalType, principalId, rid, _legacyMirror(rid), scopeType, sid, Date.now(), createdBy);
  return _prep(`SELECT id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at, created_by
                FROM grants WHERE principal_type=? AND principal_id=? AND scope_type=? AND scope_id=?`)
    .get(principalType, principalId, scopeType, sid) || null;
}

function deleteGrant(id) {
  if (!_db) return false;
  return _prep('DELETE FROM grants WHERE id = ?').run(id).changes > 0;
}

function deleteGrantsForPrincipal(principalType, principalId) {
  if (!_db) return 0;
  return _prep('DELETE FROM grants WHERE principal_type = ? AND principal_id = ?')
    .run(principalType, principalId).changes;
}

function deleteGrantsForScope(scopeType, scopeId) {
  if (!_db) return 0;
  return _prep('DELETE FROM grants WHERE scope_type = ? AND scope_id = ?').run(scopeType, scopeId).changes;
}

// Every grant that applies to a user: those held directly, plus those held by
// any group they belong to. One query rather than a fetch-then-loop, because
// this runs on the hot authorization path.
function grantsForUser(userId) {
  if (!_db) return [];
  return _prep(`
    SELECT role_id, role, scope_type, scope_id FROM grants
    WHERE (principal_type = 'user'  AND principal_id = ?)
       OR (principal_type = 'group' AND principal_id IN
             (SELECT group_id FROM group_members WHERE user_id = ?))
  `).all(userId, userId);
}

// Distinct users who effectively hold admin at global scope, counting group
// membership. This is what "would this change orphan the last administrator?"
// has to ask — a count of user records cannot see a grant held by a group, and
// an empty group confers nothing.
function globalAdminUserIds() {
  if (!_db) return [];
  return _prep(`
    SELECT DISTINCT uid FROM (
      SELECT principal_id AS uid FROM grants
       WHERE principal_type = 'user'  AND scope_type = 'global'
         AND role_id IN (SELECT id FROM roles WHERE builtin = 1)
      UNION
      SELECT gm.user_id AS uid FROM grants g
        JOIN group_members gm ON gm.group_id = g.principal_id
       WHERE g.principal_type = 'group' AND g.scope_type = 'global'
         AND g.role_id IN (SELECT id FROM roles WHERE builtin = 1)
    )
  `).all().map(r => r.uid);
}

// Users and routers live in JSON, so nothing stops a grant outliving its
// subject. Called at startup with the ids that currently exist; also the repair
// path if someone hand-edits users.json.
function sweepOrphanGrants(liveUserIds, liveRouterIds) {
  if (!_db) return { grants: 0, members: 0 };
  const users   = new Set(liveUserIds || []);
  const routers = new Set(liveRouterIds || []);
  let grants = 0, members = 0;
  _db.transaction(() => {
    for (const g of _prep("SELECT id, principal_type, principal_id, scope_type, scope_id FROM grants").all()) {
      const deadUser   = g.principal_type === 'user'   && !users.has(g.principal_id);
      const deadRouter = g.scope_type     === 'router' && !routers.has(g.scope_id);
      if (deadUser || deadRouter) { _prep('DELETE FROM grants WHERE id = ?').run(g.id); grants++; }
    }
    for (const m of _prep('SELECT rowid, user_id FROM group_members').all()) {
      if (!users.has(m.user_id)) { _prep('DELETE FROM group_members WHERE rowid = ?').run(m.rowid); members++; }
    }
  })();
  if (grants || members) console.log('%s', `[rbac] swept ${grants} orphan grant(s), ${members} orphan membership(s)`);
  return { grants, members };
}

module.exports = {
  open, close,
  listSites, getSite, createSite, updateSite, deleteSite,
  getLayout, setLayout, deleteLayouts, layoutCount, SHARED_LAYOUT_USER,
  getUserNotifyConfig, setUserNotifyConfig, deleteUserNotifyConfig, listUserNotifyConfigs,
  listGroups, getGroup, createGroup, updateGroup, deleteGroup,
  getGroupMembers, setGroupMembers, groupIdsForUser,
  listRoles, getRole, createRole, updateRole, deleteRole,
  rolePages, setRolePages, countGrantsForRole,
  listGrants, upsertGrant, deleteGrant, deleteGrantsForPrincipal, deleteGrantsForScope,
  grantsForUser, globalAdminUserIds, sweepOrphanGrants,
  insertPingSample, insertTrafficSample, insertBandwidthSample,
  insertAlertEvent, resolveAlertEvent, insertConnectivityEvent,
  hasOpenAlert, queryOpenAlerts, countOpenAlertsByRouter, queryRecentAlerts, acknowledgeAlert, resolveAllAlerts, getAlertRouterId,
  queryPingSamples, queryPingSamplesAgg,
  queryTrafficSamples, queryTrafficSamplesAgg, queryTrafficInterfaces,
  queryBandwidthSamples, queryBandwidthSamplesAgg, queryBandwidthInterfaces,
  queryTrafficSummary, queryBandwidthSummary,
  queryAlertEvents, queryConnectivityEvents, queryConnectivityEventsAgg,
  insertAuditEvent, queryAuditEvents, auditFacets,
  prune, startPruneInterval, deleteRouterData,
  recordBackup, listBackups, getBackup, latestFingerprint, lastBackupRun,
  storedBackups, markBackupPruned, deleteBackup, backupSummary,
  listReportSchedules, listReportSchedulesFor, getReportSchedule, upsertReportSchedule,
  setReportScheduleEnabled, deleteReportSchedule, deleteReportSchedulesForRouter,
  recordReportRun, reportRunHistory, listReportRuns, REPORT_RUN_KEEP,
  purge, countPurge, vacuum, stats, PURGE_TYPES,
};
