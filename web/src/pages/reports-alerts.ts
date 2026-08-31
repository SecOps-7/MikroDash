// The Alerts tab: what fired, whether it resolved, and who acknowledged it.
//
// ── ACKNOWLEDGE IS NOT RESOLVE ──────────────────────────────────────────────
//
// `resolved_at` is what the SYSTEM says: the condition went away. Acknowledging
// is what an OPERATOR says: seen, being handled. They are independent, which is
// why an alert can be open and acknowledged at once, and why the table gives
// them separate columns rather than one status.
//
// ── THE ACK WRITE STILL GOES TO NODE ────────────────────────────────────────
//
// The button POSTs to `/api/alerts/:id/ack`, which this server does not
// implement — the Go side proxies every unported /api/* path, so the request
// reaches the real endpoint with the session cookie and works unchanged. That is
// the strangler working as intended: this page renders from the Go read
// endpoints while its one write is still served by the app being replaced.

import { esc, el, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import { fmtTs, fmtDuration, statCard } from './reports';

export interface AlertRow {
  id: number | string;
  alert_type: string;
  alert_label?: string;
  subject?: string | null;
  detail?: string | null;
  fired_at: number;
  resolved_at?: number | null;
  acknowledged_at?: number | null;
  acknowledged_by?: string | null;
  /** Derived here, not sent. See renderAlerts. */
  downtime_ms?: number | null;
}

const ALERT_COLS: SortCol[] = [
  { key: 'fired_at', label: 'Fired At', style: '' },
  { key: 'alert_type', label: 'Type', style: '' },
  { key: 'subject', label: 'Subject', style: '' },
  { key: 'detail', label: 'Detail', style: '' },
  { key: 'resolved_at', label: 'Resolved At', style: '' },
  { key: 'acknowledged_at', label: 'Acknowledged', style: '' },
  { key: 'downtime_ms', label: 'Down Time', style: 'text-align:right' },
];

let alertRaw: AlertRow[] = [];
const alertSort: SortState = { col: 'fired_at', dir: 'desc' };

function applyAlertSort(): void {
  const sorted = sortRows(alertRaw, alertSort.col, alertSort.dir);
  const tbody = el('rptAlertTbody');
  if (tbody) {
    tbody.innerHTML = sorted.length
      ? sorted.map((r) => {
        const res = r.resolved_at
          ? esc(fmtTs(r.resolved_at))
          : '<span style="color:var(--accent-warn)">Open</span>';
        const dt = r.resolved_at ? fmtDuration(r.resolved_at - r.fired_at) : '—';
        // An open alert offers the button; a resolved one that was never
        // acknowledged shows a dash, because acknowledging something already
        // over is not a thing anyone needs to do.
        const ack = r.acknowledged_at
          ? esc(fmtTs(r.acknowledged_at)) +
            (r.acknowledged_by ? ' · ' + esc(r.acknowledged_by) : '')
          : (r.resolved_at ? '—'
            : '<button class="sbtn sbtn-ghost" style="padding:.15rem .5rem;font-size:.65rem"' +
              ' data-ack-id="' + esc(String(r.id)) + '">Acknowledge</button>');
        return '<tr>' +
          '<td style="font-family:var(--font-mono);font-size:.71rem;color:var(--text-muted)">' +
          esc(fmtTs(r.fired_at)) + '</td>' +
          '<td style="font-size:.71rem">' + esc(r.alert_label || r.alert_type || '') + '</td>' +
          '<td style="font-family:var(--font-mono);font-size:.71rem;color:var(--text-muted)">' +
          esc(r.subject || '—') + '</td>' +
          '<td style="font-size:.71rem;max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' +
          esc(r.detail || '—') + '</td>' +
          '<td style="font-family:var(--font-mono);font-size:.71rem">' + res + '</td>' +
          '<td style="font-family:var(--font-mono);font-size:.71rem;color:var(--text-muted)">' + ack + '</td>' +
          '<td style="font-family:var(--font-mono);font-size:.71rem;text-align:right">' +
          esc(dt) + '</td></tr>';
      }).join('')
      : '<tr><td colspan="7" class="rpt-empty">No alerts for this range.</td></tr>';
  }
  renderSortHeader('rptAlertThead', ALERT_COLS, alertSort, applyAlertSort);
}

/**
 * The Alerts tab.
 *
 * ── downtime_ms IS DERIVED HERE, BECAUSE THE QUERY NEVER SENDS IT ───────────
 *
 * The "Down Time" header sorts on `downtime_ms`, and `queryAlertEvents` returns
 * no such column — so clicking it compared undefined against undefined and did
 * nothing at all. The live app fixed that by deriving the value from the two
 * columns the row does carry, matching exactly what the cell renders, and this
 * follows it.
 *
 * An OPEN alert keeps a null, not a zero: with the sort's null handling that
 * puts the still-firing alerts at one end rather than having them pretend to be
 * outages of zero length.
 */
export function renderAlerts(rows: AlertRow[]): void {
  const open = rows.filter((r) => !r.resolved_at).length;
  const resolved = rows.length - open;

  const typeCounts: Record<string, number> = {};
  rows.forEach((r) => {
    typeCounts[r.alert_type] = (typeCounts[r.alert_type] || 0) + 1;
  });
  // `sort` on the keys, then take the first: the original's, and it inherits
  // JavaScript's stable sort, so ties keep insertion order — which for an object
  // built by iterating the rows is the order they first fired.
  const topType = Object.keys(typeCounts)
    .sort((a, b) => (typeCounts[b] as number) - (typeCounts[a] as number))[0] || '—';

  const stats = el('rptAlertStats');
  if (stats) {
    stats.innerHTML =
      statCard(rows.length, 'Total') +
      statCard(open, 'Open') +
      statCard(resolved, 'Resolved') +
      statCard(topType, 'Top Type');
  }

  alertRaw = (rows || []).map((r) => ({
    ...r,
    downtime_ms: r.resolved_at ? (r.resolved_at - r.fired_at) : null,
  }));
  alertSort.col = 'fired_at';
  alertSort.dir = 'desc';
  applyAlertSort();
}

/**
 * Wire the acknowledge button once.
 *
 * DELEGATED, because every sort replaces the whole tbody — a handler bound to a
 * button would be thrown away the first time anyone clicked a column header.
 */
export function wireAlertAck(): void {
  el('rptAlertTbody')?.addEventListener('click', (e) => {
    const target = e.target as HTMLElement | null;
    const btn = target?.closest?.('[data-ack-id]') as HTMLButtonElement | null;
    if (!btn) return;
    const id = btn.getAttribute('data-ack-id');
    if (!id) return;
    btn.disabled = true;
    fetch('/api/alerts/' + encodeURIComponent(id) + '/ack', { method: 'POST' })
      .then((r) => r.json())
      .then((j: { ok?: boolean; alert?: { acknowledgedAt?: number; acknowledgedBy?: string } }) => {
        if (!j || !j.ok) throw new Error('ack failed');
        // PATCH THE ROW IN PLACE rather than reloading the report: the range and
        // the sort the operator set are worth more than a round trip.
        const a = j.alert || {};
        alertRaw.forEach((row) => {
          if (String(row.id) === String(id)) {
            row.acknowledged_at = a.acknowledgedAt || Date.now();
            row.acknowledged_by = a.acknowledgedBy || '';
          }
        });
        applyAlertSort();
      })
      .catch(() => {
        // Re-enable so it can be tried again. A button that stays dead after a
        // failed request looks like the alert was acknowledged.
        btn.disabled = false;
      });
  });
}
