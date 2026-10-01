// The device modal's markup: pure, rows in and strings out, so every section can
// be asserted without a DOM. `devices-modal.ts` owns the lifecycle and the
// socket; this owns what is drawn.

import { esc, fmtMbps } from '../dom';
import { fmtTs } from '../timefmt';
import type { RouterStatsRow, Live, TrafficPoint } from '../gen/payloads';
import { cardState, type CardState } from './devices-card';
import { fmtDuration, outages, stripHtml, uptimeLabel, type DeviceOverview } from './devices-strip';
import { gauge } from './dashboard-gauge';
import { portsHtml } from './dashboard-card-physports';
import type { AlertRow } from './reports-alerts';

export type Range = '24h' | '7d' | '30d';

export const RANGE_MS: Record<Range, number> = {
  '24h': 24 * 3600_000, '7d': 7 * 24 * 3600_000, '30d': 30 * 24 * 3600_000,
};
const RANGE_WORD: Record<Range, string> = { '24h': '24 hours', '7d': '7 days', '30d': '30 days' };

const STATE_TEXT: Record<CardState, string> = {
  unknown: 'Checking…', online: 'Online', offline: 'Offline',
};

/** The header: state, name, where it is, and the two ways out. */
export function headerHtml(r: RouterStatsRow, canEdit: boolean): string {
  const st = cardState(r);
  const sites = (r.siteNames || []).filter(Boolean);
  return '<div class="dvm-title dv-' + st + '">'
    + '<span class="dv-dot" aria-hidden="true"></span>'
    + '<div class="dvm-id"><div class="dvm-name"><h2 id="dvmName">' + esc(r.label) + '</h2>'
    + '<span class="dv-status">' + STATE_TEXT[st] + '</span>'
    + (r.isActive ? '<span class="dv-active">active</span>' : '') + '</div>'
    + '<div class="dvm-sub"><span class="dvm-mono">' + esc(r.host) + '</span>'
    + sites.map((s) => '<span class="dvm-pill">' + esc(s) + '</span>').join('') + '</div></div></div>'
    + '<div class="dvm-actions">'
    + (r.isActive ? '' : '<button type="button" class="sbtn sbtn-primary" data-dvm="dashboard">Open dashboard</button>')
    + (canEdit ? '<button type="button" class="sbtn sbtn-ghost" data-dvm="edit">Edit</button>' : '')
    + '<button type="button" class="rtr-modal-close" data-modal-close="deviceModal" aria-label="Close">&#10005;</button>'
    + '</div>';
}

/** The range tabs, the big strip with its axis, and the outages in range. */
export function connectivityHtml(o: DeviceOverview | undefined, range: Range): string {
  const tabs = (['24h', '7d', '30d'] as Range[]).map((k) =>
    '<button type="button" class="dvm-range' + (k === range ? ' active' : '') + '" data-range="' + k
    + '" aria-pressed="' + (k === range) + '">' + k + '</button>').join('');
  let body: string;
  if (!o) {
    body = '<div class="dv-strip dv-strip-loading dvm-strip" aria-hidden="true"></div>';
  } else {
    const from = o.spans[0]?.from ?? 0;
    const to = o.spans[o.spans.length - 1]?.to ?? 0;
    const mid = from + (to - from) / 2;
    const longW = range !== '24h';
    const at = (t: number): string => esc(longW ? fmtTs(t, false).slice(5, 10) : fmtTs(t, false).slice(11));
    const downs = outages(o.spans).slice().reverse();
    const monitored = o.monitoredMs > 0
      ? 'Watched for ' + fmtDuration(o.monitoredMs) + ' of the last ' + RANGE_WORD[range]
      : 'Not watched in the last ' + RANGE_WORD[range];
    body = '<div class="dvm-strip">' + stripHtml(o.spans, from, to,
      { uptimePct: o.uptimePct, windowWord: RANGE_WORD[range] }) + '</div>'
      + (to > from ? '<div class="dv-axis"><span>' + at(from) + '</span><span>' + at(mid) + '</span><span>now</span></div>' : '')
      + '<div class="dvm-legend"><span><i class="dv-up"></i>Up</span><span><i class="dv-down"></i>Down</span>'
      + '<span><i class="dv-unmonitored"></i>Not monitored</span><span class="dvm-legend-note">'
      + esc(monitored) + '. Reachability of the API session, not ICMP.</span></div>'
      + (downs.length
        ? '<ol class="dvm-outages">' + downs.slice(0, 8).map((s) =>
          '<li><span class="dv-backup-bad">Down</span><span>' + esc(fmtTs(s.from, false)) + '</span>'
          + '<span class="dvm-mono">' + fmtDuration(s.to - s.from) + '</span></li>').join('')
          + (downs.length > 8 ? '<li class="text-muted">and ' + (downs.length - 8) + ' more</li>' : '') + '</ol>'
        : (o.uptimePct != null ? '<div class="dvm-none">No outages in the last ' + RANGE_WORD[range] + '.</div>' : ''));
  }
  const pct = o ? o.uptimePct : null;
  return '<div class="dvm-sec-head"><h3>Connectivity</h3>'
    + (o ? '<span class="dv-pct ' + (pct == null ? 'dv-pct-none' : pct >= 99.9 ? 'dv-pct-good' : pct >= 99 ? 'dv-pct-warn' : 'dv-pct-bad')
      + '">' + uptimeLabel(pct) + '</span>' : '')
    + '<div class="dvm-ranges" role="group" aria-label="Range">' + tabs + '</div></div>' + body;
}

/**
 * Live usage's header: the interface, and the current Rx/Tx large. The chart
 * under it is a canvas the modal keeps for as long as it is open
 * (`devices-modal-chart.ts`), so it is not part of this markup - repainting it
 * every second is exactly the ticking the chart's frame loop exists to avoid.
 */
export function usageHeadHtml(live: Live | null, last: TrafficPoint | undefined): string {
  const head = '<div class="dvm-sec-head"><h3>Live usage</h3>'
    + (live && live.wanIf ? '<span class="dvm-pill">' + esc(live.wanIf) + '</span>' : '') + '</div>';
  if (!live) return head + '<div class="dvm-skel" style="height:56px"></div>';
  if (!last) {
    return head + '<div class="dvm-none">' + (live.connected
      ? 'Waiting for the first throughput sample\u2026' : 'Offline: no live throughput.') + '</div>';
  }
  return head + '<div class="dvm-rates">'
    + '<div><span class="dvm-rate-lbl">Rx</span><span class="dvm-rate" style="color:var(--accent-rx)">'
    + esc(fmtMbps(last.rx_mbps)) + '</span></div>'
    + '<div><span class="dvm-rate-lbl">Tx</span><span class="dvm-rate" style="color:var(--accent-tx)">'
    + esc(fmtMbps(last.tx_mbps)) + '</span></div></div>';
}

/** CPU, RAM and disk as the dashboard's gauges, plus temperature. */
export function resourcesHtml(live: Live | null): string {
  const head = '<div class="dvm-sec-head"><h3>Resources</h3>'
    + (live && live.tempC != null ? '<span class="dvm-pill">' + esc(live.tempC.toFixed(0)) + ' °C</span>' : '')
    + '</div>';
  if (!live) return head + '<div class="dvm-skel" style="height:96px"></div>';
  if (live.cpu == null) {
    return head + '<div class="dvm-none">' + (live.connected ? 'Waiting for a reading…' : 'Offline: no reading.') + '</div>';
  }
  return head + '<div class="dvm-gauges">' + gauge('CPU', live.cpu, 'cpu')
    + gauge('RAM', live.memPct ?? 0, 'mem') + gauge('Disk', live.hddPct ?? 0, 'hdd') + '</div>';
}

/** The physical ports, drawn exactly as the dashboard card draws them. */
export function portsSectionHtml(live: Live | null): string {
  const up = live ? live.ports.filter((p) => p.running && !p.disabled).length : 0;
  const head = '<div class="dvm-sec-head"><h3>Ports</h3>'
    + (live && live.portsRead && live.ports.length
      ? '<span class="dvm-pill">' + up + ' of ' + live.ports.length + ' up</span>' : '')
    + (live && live.leases != null ? '<span class="dvm-pill">' + live.leases + ' DHCP lease' + (live.leases === 1 ? '' : 's') + '</span>' : '')
    + '</div>';
  if (!live || !live.portsRead) {
    return head + (live && !live.connected
      ? '<div class="dvm-none">Offline: no port reading.</div>'
      : '<div class="dvm-skel" style="height:64px"></div>');
  }
  if (!live.ports.length) return head + '<div class="dvm-none">No ethernet ports</div>';
  return head + '<div class="if-ports-scroll dvm-ports">' + portsHtml(live.ports) + '</div>';
}

/** The newest alerts on this router; null is "may not read reports". */
export function alertsHtml(rows: AlertRow[] | null | undefined): string {
  if (rows === null) return '';
  const head = '<div class="dvm-sec-head"><h3>Recent alerts</h3></div>';
  if (rows === undefined) return head + '<div class="dvm-skel" style="height:64px"></div>';
  if (!rows.length) return head + '<div class="dvm-none">No alerts in the last 7 days.</div>';
  const newest = rows.slice().sort((a, b) => b.fired_at - a.fired_at).slice(0, 6);
  return head + '<ul class="dvm-alerts">' + newest.map((r) => {
    const open = !r.resolved_at;
    return '<li><span class="' + (open ? 'dv-alerts' : 'dvm-pill') + '">' + (open ? 'Open' : 'Resolved') + '</span>'
      + '<span class="dvm-alert-type">' + esc(r.alert_label || r.alert_type) + '</span>'
      + '<span class="dvm-alert-sub">' + esc(r.subject || r.detail || '') + '</span>'
      + '<span class="dvm-mono text-muted">' + esc(fmtTs(r.fired_at, false)) + '</span></li>';
  }).join('') + '</ul>';
}
