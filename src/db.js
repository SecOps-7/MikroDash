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

/** Acknowledge every unacknowledged row for a router. Returns the affected ids
 *  so the change can be pushed to other connected browsers. */
function acknowledgeAllAlerts(routerId, username) {
  if (!_db) return [];
  const ids = _prep('SELECT id FROM alert_events WHERE router_id = ? AND acknowledged_at IS NULL')
    .all(routerId).map(r => r.id);
  if (!ids.length) return [];
  _prep('UPDATE alert_events SET acknowledged_at = ?, acknowledged_by = ? WHERE router_id = ? AND acknowledged_at IS NULL')
    .run(Date.now(), username || null, routerId);
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

function prune(retentionDays, alertRetentionDays) {
  if (!_db) return;
  const metricCutoff = Date.now() - (retentionDays      || 90)  * 86400000;
  const alertCutoff  = Date.now() - (alertRetentionDays || 365) * 86400000;
  const r1 = _prep('DELETE FROM ping_samples        WHERE ts < ?').run(metricCutoff);
  const r2 = _prep('DELETE FROM traffic_samples     WHERE ts < ?').run(metricCutoff);
  const r3 = _prep('DELETE FROM bandwidth_usage     WHERE ts < ?').run(metricCutoff);
  const r4 = _prep('DELETE FROM alert_events        WHERE fired_at < ?').run(alertCutoff);
  const r5 = _prep('DELETE FROM connectivity_events WHERE ts < ?').run(alertCutoff);
  const total = r1.changes + r2.changes + r3.changes + r4.changes + r5.changes;
  if (total > 0) console.log('%s', `[db] pruned ${total} rows (metrics: ${retentionDays}d, events: ${alertRetentionDays}d)`);
}

function startPruneInterval(getSettings) {
  if (_pruneTimer) return;
  const run = () => {
    const s = getSettings();
    prune(s.dbRetentionDays || 90, s.dbAlertRetentionDays || 365);
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
  queryOpenAlerts, countOpenAlertsByRouter, queryRecentAlerts, acknowledgeAlert, acknowledgeAllAlerts, getAlertRouterId,
  queryPingSamples, queryPingSamplesAgg,
  queryTrafficSamples, queryTrafficSamplesAgg, queryTrafficInterfaces,
  queryBandwidthSamples, queryBandwidthSamplesAgg, queryBandwidthInterfaces,
  queryTrafficSummary, queryBandwidthSummary,
  queryAlertEvents, queryConnectivityEvents, queryConnectivityEventsAgg,
  prune, startPruneInterval, deleteRouterData,
  purge, countPurge, vacuum, stats, PURGE_TYPES,
};
