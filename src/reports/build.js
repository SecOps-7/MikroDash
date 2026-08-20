'use strict';
/**
 * One report section, assembled once for both consumers.
 *
 * This code used to live inline in the five `/api/reports/*​/export` route
 * handlers. A scheduled report needs exactly the same columns, rows and stat
 * boxes, and a second copy would drift from the first the moment either was
 * touched — so the routes and the scheduler now call this instead.
 *
 * Each builder returns BOTH shapes rather than one, because they genuinely
 * differ today and collapsing them would change what the CSV contains: the ping
 * CSV emits `ts,target,rtt_ms,loss_pct` over the labelled rows, while the ping
 * PDF emits `Timestamp,Target,RTT (ms),Loss (%)` over a remapped object.
 * Returning both from one function is what keeps them honest.
 */

const db = require('../db');
const Routers = require('../routers');
const F = require('./format');

const SECTIONS = Object.freeze(['ping', 'traffic', 'bandwidth', 'alerts', 'connectivity']);

/** Sections that report on a single interface and cannot be built without one. */
const NEEDS_INTERFACE = Object.freeze(['traffic', 'bandwidth']);

/**
 * The PDF table stops here.
 *
 * Samples are 1-minute bucketed by db-writer, so an unaggregated month is
 * ~43,200 rows per series — at roughly 43 rows to a page that is a
 * thousand-page document, rendered synchronously on the same event loop that
 * serves live dashboards. The CSV is left uncapped: it is plain text, and a
 * spreadsheet user asking for a month wants the month.
 */
const MAX_PDF_ROWS = 5000;

/** Chart series are thinned to this many points before drawing. */
const CHART_POINTS = 150;

/**
 * Traffic and bandwidth stats come from SQL over the whole range, not from the
 * returned rows: those are averages once an aggregation is selected, so a max
 * over them is a peak of averages, and they are capped by the query LIMIT.
 */
function ifaceSummary(routerId, iface, from, to) {
  const t = db.queryTrafficSummary(routerId, iface, from, to, 95);
  const b = db.queryBandwidthSummary(routerId, iface, from, to);
  const r = Routers.getById(routerId);
  const capDown = Math.max(1, parseInt(r && r.bwDownMbps, 10) || 1000);
  const capUp   = Math.max(1, parseInt(r && r.bwUpMbps,   10) || 1000);
  const pct = (v, cap) => (v == null ? null : +((v / cap) * 100).toFixed(1));
  return {
    ...t, ...b,
    capacityDownMbps: capDown,
    capacityUpMbps:   capUp,
    rxPeakPct: pct(t.rxMaxMbps, capDown), txPeakPct: pct(t.txMaxMbps, capUp),
    rxP95Pct:  pct(t.rxP95Mbps, capDown), txP95Pct:  pct(t.txP95Mbps, capUp),
  };
}

/** Every Nth row, so a long range still draws a readable line. */
function _thin(rows) {
  const step = rows.length > CHART_POINTS ? Math.ceil(rows.length / CHART_POINTS) : 1;
  return rows.filter((_, i) => i % step === 0);
}

function _routerLabel(routerId) {
  const r = Routers.getById(routerId);
  return r ? (r.label || r.host) : routerId;
}

/**
 * Cap the PDF table, and say so in the table itself.
 *
 * A note in the rows rather than a seventh stat box: _render lays the boxes out
 * with lineBreak:false, so a seventh starts truncating values instead of
 * wrapping, and traffic and bandwidth already use all six.
 */
function _capRows(rows, columns) {
  if (rows.length <= MAX_PDF_ROWS) return { rows, truncated: false };
  const kept = rows.slice(0, MAX_PDF_ROWS);
  const note = {};
  columns.forEach((c, i) => {
    note[c] = i === 0
      ? '… showing the first ' + MAX_PDF_ROWS.toLocaleString() + ' of ' +
        rows.length.toLocaleString() + ' rows — narrow the range or aggregate for the rest'
      : '';
  });
  return { rows: kept.concat([note]), truncated: true };
}

const BUILDERS = {
  ping({ routerId, from, to, aggregate }) {
    const rows = aggregate
      ? db.queryPingSamplesAgg(routerId, from, to, aggregate)
      : db.queryPingSamples(routerId, from, to);
    const label = rows.map(r => ({ ...r, ts: F.tsFmt(r.ts) }));

    const rtts   = rows.filter(r => r.rtt_ms != null).map(r => r.rtt_ms);
    const losses = rows.map(r => r.loss_pct);
    const avgRtt = rtts.length   ? (rtts.reduce((a,b)=>a+b,0)/rtts.length).toFixed(1) : '—';
    const maxRtt = rtts.length   ? F.maxOf(rtts).toFixed(1) : '—';
    const avgLoss= losses.length ? (losses.reduce((a,b)=>a+b,0)/losses.length).toFixed(1) : '—';
    const uptime = losses.length ? ((losses.filter(l=>l<1).length/losses.length)*100).toFixed(1)+'%' : '—';
    const sub    = _thin(rows);

    const columns = ['Timestamp', 'Target', 'RTT (ms)', 'Loss (%)'];
    const capped  = _capRows(label.map(r => ({
      Timestamp: r.ts, Target: r.target, 'RTT (ms)': r.rtt_ms ?? '', 'Loss (%)': r.loss_pct,
    })), columns);

    return {
      title: 'Ping Stability Report',
      csvFilename: 'ping-report.csv',
      rowCount: rows.length,
      truncated: capped.truncated,
      csv: { columns: ['ts', 'target', 'rtt_ms', 'loss_pct'], rows: label },
      pdf: {
        columns,
        rows: capped.rows,
        meta: {
          router: _routerLabel(routerId), from, to,
          stats: [
            { label: 'Uptime',   value: uptime },
            { label: 'Avg RTT',  value: avgRtt !== '—' ? avgRtt+' ms' : '—' },
            { label: 'Max RTT',  value: maxRtt !== '—' ? maxRtt+' ms' : '—' },
            { label: 'Avg Loss', value: avgLoss !== '—' ? avgLoss+'%' : '—' },
            { label: 'Samples',  value: rows.length.toLocaleString() },
          ],
          chartData: { yLabel: 'ms / %', lines: [
            { label: 'RTT ms',  color: '#38bdf8', pts: sub.filter(r=>r.rtt_ms!=null).map(r=>({ x:r.ts, y:r.rtt_ms })) },
            { label: 'Loss %',  color: '#f87171', pts: sub.map(r=>({ x:r.ts, y:r.loss_pct })) },
          ]},
        },
      },
    };
  },

  traffic({ routerId, iface, from, to, aggregate }) {
    const rows = aggregate
      ? db.queryTrafficSamplesAgg(routerId, iface, from, to, aggregate)
      : db.queryTrafficSamples(routerId, iface, from, to);
    const label = rows.map(r => ({ ...r, ts: F.tsFmt(r.ts),
      rx_mbps: +r.rx_mbps.toFixed(1), tx_mbps: +r.tx_mbps.toFixed(1) }));

    const s     = ifaceSummary(routerId, iface, from, to);
    const n1    = (v) => (v == null ? '—' : v.toFixed(1));
    const avgRx = n1(s.rxAvgMbps);
    const avgTx = n1(s.txAvgMbps);
    const peakRx= n1(s.rxMaxMbps);
    const peakTx= n1(s.txMaxMbps);
    const sub   = _thin(rows);

    const columns = ['Timestamp', 'Interface', 'RX (Mbps)', 'TX (Mbps)'];
    const capped  = _capRows(label.map(r => ({
      Timestamp: r.ts, Interface: r.interface, 'RX (Mbps)': r.rx_mbps, 'TX (Mbps)': r.tx_mbps,
    })), columns);

    return {
      title: 'Traffic History Report',
      csvFilename: 'traffic-report.csv',
      rowCount: rows.length,
      truncated: capped.truncated,
      csv: { columns: ['ts', 'interface', 'rx_mbps', 'tx_mbps'], rows: label },
      pdf: {
        columns,
        rows: capped.rows,
        meta: {
          router: _routerLabel(routerId), from, to,
          stats: [
            { label: 'Peak RX',   value: peakRx !== '—' ? peakRx+' Mbps' : '—' },
            { label: 'Peak TX',   value: peakTx !== '—' ? peakTx+' Mbps' : '—' },
            { label: 'Avg RX',    value: avgRx  !== '—' ? avgRx +' Mbps' : '—' },
            { label: 'Avg TX',    value: avgTx  !== '—' ? avgTx +' Mbps' : '—' },
            { label: '95th RX',   value: n1(s.rxP95Mbps) !== '—' ? n1(s.rxP95Mbps)+' Mbps' : '—' },
            // Utilisation against the router's configured line capacity, not
            // clamped at 100 — over-capacity is the signal worth seeing.
            { label: 'Peak Util', value: s.rxPeakPct == null ? '—'
                                    : Math.round(s.rxPeakPct)+'% / '+Math.round(s.txPeakPct)+'%' },
          ],
          chartData: { yLabel: 'Mbps', lines: [
            { label: 'RX Mbps', color: '#38bdf8', pts: sub.map(r=>({ x:r.ts, y:r.rx_mbps })) },
            { label: 'TX Mbps', color: '#4ade80', pts: sub.map(r=>({ x:r.ts, y:r.tx_mbps })) },
          ]},
        },
      },
    };
  },

  bandwidth({ routerId, iface, from, to, aggregate }) {
    const rows = aggregate
      ? db.queryBandwidthSamplesAgg(routerId, iface, from, to, aggregate)
      : db.queryBandwidthSamples(routerId, iface, from, to);
    const label = rows.map(r => ({ ...r, ts: F.tsFmt(r.ts),
      rx_mb: +r.rx_mb.toFixed(1), tx_mb: +r.tx_mb.toFixed(1) }));

    const s   = ifaceSummary(routerId, iface, from, to);
    const sub = _thin(rows);

    const columns = ['Timestamp', 'Interface', 'Download (MB)', 'Upload (MB)'];
    const capped  = _capRows(label.map(r => ({
      Timestamp: r.ts, Interface: r.interface, 'Download (MB)': r.rx_mb, 'Upload (MB)': r.tx_mb,
    })), columns);

    return {
      title: 'Bandwidth Usage Report',
      csvFilename: 'bandwidth-report.csv',
      rowCount: rows.length,
      truncated: capped.truncated,
      csv: { columns: ['ts', 'interface', 'rx_mb', 'tx_mb'], rows: label },
      pdf: {
        columns,
        rows: capped.rows,
        meta: {
          router: _routerLabel(routerId), from, to,
          // Six boxes maximum — _render draws them with lineBreak:false, so a
          // seventh starts truncating values rather than wrapping.
          stats: [
            { label: 'Total Download', value: F.fmtDataMB(s.rxTotalMb) },
            { label: 'Total Upload',   value: F.fmtDataMB(s.txTotalMb) },
            { label: 'Total',          value: F.fmtDataMB((s.rxTotalMb || 0) + (s.txTotalMb || 0)) },
            { label: 'Busiest ' + F.bucketNoun(aggregate) + ' ↓', value: s.rxMaxMb == null ? '—' : F.fmtDataMB(s.rxMaxMb) },
            { label: 'Busiest ' + F.bucketNoun(aggregate) + ' ↑', value: s.txMaxMb == null ? '—' : F.fmtDataMB(s.txMaxMb) },
            { label: aggregate ? 'Buckets' : 'Samples', value: s.samples.toLocaleString() },
          ],
          chartData: { yLabel: 'MB/min', lines: [
            { label: 'Download MB', color: '#38bdf8', pts: sub.map(r=>({ x:r.ts, y:r.rx_mb })) },
            { label: 'Upload MB',   color: '#4ade80', pts: sub.map(r=>({ x:r.ts, y:r.tx_mb })) },
          ]},
        },
      },
    };
  },

  alerts({ routerId, from, to }) {
    const rows  = db.queryAlertEvents(routerId, from, to);
    const label = rows.map(r => ({
      ...r,
      fired_at:    F.tsFmt(r.fired_at),
      resolved_at: F.tsFmt(r.resolved_at),
      down_time:   r.resolved_at ? F.fmtDuration(r.resolved_at - r.fired_at) : '',
    }));

    const open     = rows.filter(r => !r.resolved_at).length;
    const resolved = rows.filter(r =>  r.resolved_at).length;
    const typeCounts = {};
    rows.forEach(r => { typeCounts[r.alert_type] = (typeCounts[r.alert_type]||0)+1; });
    const topEntry = Object.entries(typeCounts).sort((a,b)=>b[1]-a[1])[0];

    const columns = ['Fired At', 'Type', 'Subject', 'Detail', 'Resolved At', 'Down Time'];
    const capped  = _capRows(label.map(r => ({
      'Fired At': r.fired_at, Type: r.alert_type, Subject: r.subject || '',
      Detail: r.detail || '', 'Resolved At': r.resolved_at, 'Down Time': r.down_time || '—',
    })), columns);

    return {
      title: 'Alert Events Report',
      csvFilename: 'alerts-report.csv',
      rowCount: rows.length,
      truncated: capped.truncated,
      csv: { columns: ['fired_at', 'alert_type', 'subject', 'detail', 'resolved_at', 'down_time'], rows: label },
      pdf: {
        columns,
        rows: capped.rows,
        // No chartData: alert events are discrete, not a series.
        meta: {
          router: _routerLabel(routerId), from, to,
          stats: [
            { label: 'Total',    value: rows.length.toLocaleString() },
            { label: 'Open',     value: open.toLocaleString() },
            { label: 'Resolved', value: resolved.toLocaleString() },
            { label: 'Top Type', value: topEntry ? topEntry[0] : '—' },
          ],
        },
      },
    };
  },

  connectivity({ routerId, from, to }) {
    const rows = F.annotateDowntime(db.queryConnectivityEvents(routerId, from, to));
    const label = rows.map(r => ({
      ts:            F.tsFmt(r.ts),
      status:        r.connected ? 'Online' : 'Offline',
      down_duration: (!r.connected && r.downtime_ms != null) ? F.fmtDuration(r.downtime_ms)
                   : (!r.connected)                          ? 'Ongoing'
                   : '',
    }));

    const offlineRows   = rows.filter(r => !r.connected);
    const resolvedMs    = offlineRows.filter(r => r.downtime_ms != null).map(r => r.downtime_ms);
    const totalDownMs   = resolvedMs.reduce((a, b) => a + b, 0);
    const longestDownMs = resolvedMs.length ? F.maxOf(resolvedMs) : null;

    const columns = ['Timestamp', 'Status', 'Down Duration'];
    const capped  = _capRows(label.map(r => ({
      Timestamp: r.ts, Status: r.status, 'Down Duration': r.down_duration || '—',
    })), columns);

    return {
      title: 'Connectivity Report',
      csvFilename: 'connectivity-report.csv',
      rowCount: rows.length,
      truncated: capped.truncated,
      csv: { columns: ['ts', 'status', 'down_duration'], rows: label },
      pdf: {
        columns,
        rows: capped.rows,
        meta: {
          router: _routerLabel(routerId), from, to,
          stats: [
            { label: 'Total Events',   value: rows.length.toLocaleString() },
            { label: 'Offline Events', value: offlineRows.length.toLocaleString() },
            { label: 'Total Downtime', value: totalDownMs ? F.fmtDuration(totalDownMs) : '—' },
            { label: 'Longest Outage', value: longestDownMs != null ? F.fmtDuration(longestDownMs) : '—' },
          ],
        },
      },
    };
  },
};

/**
 * Build one section.
 *
 * Throws on an unknown section or a missing interface rather than returning an
 * empty report: both mean the caller asked for something that cannot exist, and
 * a silently blank PDF is worse than an error.
 */
function build(section, opts) {
  const fn = BUILDERS[section];
  if (!fn) throw new Error('unknown report section: ' + section);
  if (NEEDS_INTERFACE.includes(section) && !(opts && opts.iface)) {
    throw new Error(section + ' reports need an interface');
  }
  return fn(opts || {});
}

module.exports = { build, ifaceSummary, SECTIONS, NEEDS_INTERFACE, MAX_PDF_ROWS, CHART_POINTS };
