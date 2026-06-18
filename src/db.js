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

function insertConnectivityEvent(routerId, connected) {
  if (!_db) return;
  _stmtInsertConn.run(routerId, connected ? 1 : 0, Date.now());
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
  queryAlertEvents, queryConnectivityEvents, queryConnectivityEventsAgg,
  prune, startPruneInterval, deleteRouterData,
};
