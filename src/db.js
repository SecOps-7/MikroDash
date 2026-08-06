'use strict';
const path    = require('path');
const fs      = require('fs');
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
    console.log(`[db] migration v${m.version} applied`);
  }
}

// ── Open / close ──────────────────────────────────────────────────────────────

function open() {
  if (_db) return _db;
  try { fs.mkdirSync(DATA_DIR, { recursive: true }); } catch (_) {}
  _db = new BetterSqlite(DB_FILE);
  _db.pragma('journal_mode = WAL');
  _db.pragma('synchronous = NORMAL');
  _db.exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);`);
  _runMigrations(_db);
  _prepareStatements();
  console.log(`[db] opened ${DB_FILE}`);
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

function resolveAlertEvent(routerId, alertType, subject) {
  if (!_db) return;
  _stmtResolveAlert.run(Date.now(), routerId, alertType, subject || null);
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

function queryAlertEvents(routerId, fromTs, toTs, limit) {
  if (!_db) return [];
  return _prep(`
    SELECT id, alert_type, subject, detail, fired_at, resolved_at
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
  if (total > 0) console.log(`[db] pruned ${total} rows (metrics: ${retentionDays}d, events: ${alertRetentionDays}d)`);
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
  console.log(`[db] purge removed ${deleted} rows (router: ${opts.routerId || 'all'}, types: ${(opts.types || PURGE_TYPES).join('+')}, olderThanMs: ${opts.olderThanMs || 0})`);
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
  console.log(`[db] vacuum reclaimed ${Math.max(0, before - after)} bytes`);
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
  console.log(`[db] deleted all data for router ${routerId}`);
}

module.exports = {
  open, close,
  insertPingSample, insertTrafficSample, insertBandwidthSample,
  insertAlertEvent, resolveAlertEvent, insertConnectivityEvent,
  queryPingSamples, queryPingSamplesAgg,
  queryTrafficSamples, queryTrafficSamplesAgg, queryTrafficInterfaces,
  queryBandwidthSamples, queryBandwidthSamplesAgg, queryBandwidthInterfaces,
  queryTrafficSummary, queryBandwidthSummary,
  queryAlertEvents, queryConnectivityEvents, queryConnectivityEventsAgg,
  prune, startPruneInterval, deleteRouterData,
  purge, countPurge, vacuum, stats, PURGE_TYPES,
};
