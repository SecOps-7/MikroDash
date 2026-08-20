'use strict';
/**
 * Formatting shared by every report, moved out of index.js so the HTTP export
 * routes and the scheduled-report path cannot drift apart.
 *
 * Everything here is sink-free and pure given Settings: no `res`, no database.
 * That is the whole point — a scheduled report has no HTTP response to write
 * to, and these helpers were part of why the export bodies had to live inside
 * a route handler.
 *
 * Moved verbatim from src/index.js; the comments came with them because they
 * record why the thresholds and the escaping are what they are.
 */

const Settings = require('../settings');

// Math.max(...arr) overflows the call stack above ~65k arguments — report
// queries default to a 100k row limit, so reduce instead of spreading.
const maxOf = (arr) => arr.reduce((m, v) => (v > m ? v : m), -Infinity);

// Format a stored bandwidth_usage MB value for display. Decimal thresholds are
// deliberate: rx_mb is written as Mbps/8, i.e. 10^6-based, so rendering it
// against 1024-based thresholds overstated every total by ~4.9%. Decimal is
// also the right convention here — ISP quotas are quoted decimal.
// A volume peak is per bucket, so the label has to say which bucket. Without an
// aggregation the stored granularity is one minute.
function bucketNoun(agg) {
  return agg === 'hour' ? 'Hour' : agg === 'day' ? 'Day'
       : agg === 'week' ? 'Week' : agg === 'month' ? 'Month' : 'Minute';
}

function fmtDataMB(mb) {
  const n = +mb || 0;
  if (n >= 1e6)  return (n / 1e6).toFixed(2) + ' TB';
  if (n >= 1000) return (n / 1000).toFixed(2) + ' GB';
  if (n >= 1)    return n.toFixed(1) + ' MB';
  return (n * 1000).toFixed(0) + ' KB';
}

// Pair each Offline row with the next Online row to compute outage duration.
// Single backward pass (rows are ts-ASC); null downtime = still offline.
function annotateDowntime(rows) {
  let nextOnlineTs = null;
  for (let i = rows.length - 1; i >= 0; i--) {
    if (rows[i].connected) { nextOnlineTs = rows[i].ts; rows[i].downtime_ms = null; }
    else rows[i].downtime_ms = nextOnlineTs != null ? nextOnlineTs - rows[i].ts : null;
  }
  return rows;
}

function tsFmt(ts) {
  if (!ts) return '';
  const tz = Settings.load().displayTimezone;
  if (tz) {
    return new Intl.DateTimeFormat('sv-SE', {
      timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    }).format(new Date(ts)).replace('T', ' ');
  }
  return new Date(ts).toISOString().replace('T', ' ').slice(0, 19) + ' UTC';
}

function fmtDuration(ms) {
  if (!ms || ms < 0) return '';
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return h + 'h ' + m + 'm';
  if (m > 0) return m + 'm ' + sec + 's';
  return sec + 's';
}

function toCsv(rows, columns) {
  const header = columns.join(',');
  const body   = rows.map(r => columns.map(c => {
    const v = r[c];
    if (v == null) return '';
    let s = String(v);
    // Neutralise spreadsheet formula injection: a cell that a router-controlled
    // string (interface name, ping target, alert subject) could start with
    // =, +, -, @, tab or CR is executed as a formula by Excel/Sheets. Prefix a
    // single quote so it's treated as literal text.
    if (/^[=+\-@\t\r]/.test(s)) s = "'" + s;
    return s.includes(',') || s.includes('"') || s.includes('\n') ? `"${s.replace(/"/g, '""')}"` : s;
  }).join(',')).join('\n');
  return header + '\n' + body;
}

module.exports = { maxOf, bucketNoun, fmtDataMB, annotateDowntime, tsFmt, fmtDuration, toCsv };
