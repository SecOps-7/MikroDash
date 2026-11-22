// The Apps Usage page's Unnamed detail (#167): where the traffic no app was
// found for went in the last hour, by client and destination.
//
// GET /api/apps/unnamed (internal/server/apps_rules.go) answers from the apps
// collector's window in memory (internal/collect/apps.go): each row is one
// client's traffic to one address, protocol and port, with the address's
// network owner and why it went unnamed. No DNS name is shown: the router's
// cache is not a per-device log of what was asked, keeps no query counts, and
// the page reads it on the condition that no name leaves the server. The
// amounts are bytes.
//
// An administrator can pick rows and hand their networks and ports to the app
// editor (apps-manage.ts), for a new app or one that exists.

import { esc, el, pill, protoPill, fmtBytes, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import type { AppsManage, PortRule, RulePrefill } from './apps-manage';

/** One row of GET /api/apps/unnamed. */
export interface UnnamedRow {
  dst: string; proto: string; port: string; asn: number; owner: string; reason: string;
  client: string; clientName: string; clientMac: string; rxBytes: number; txBytes: number;
}

/** GET /api/apps/unnamed. */
export interface UnnamedDetail {
  from: number; to: number; rows: UnnamedRow[]; total: number; rxBytes: number; txBytes: number; partial: boolean;
}

/** Ports a rule cannot claim (internal/apps webPorts). */
const WEB_PORTS = new Set([80, 443]);

/** What picked rows can name an app by: each row's network when its owner is
 *  known, else its port when that is not a web port. Rows with neither are
 *  counted, so the page can say they were left out. Pure. */
export function prefillFrom(rows: readonly UnnamedRow[]): RulePrefill & { skipped: number } {
  const asns = new Set<number>();
  const ports = new Map<string, PortRule>();
  let skipped = 0;
  for (const r of rows) {
    const port = Number(r.port);
    if (r.asn > 0) asns.add(r.asn);
    else if (port > 0 && !WEB_PORTS.has(port) && (r.proto === 'tcp' || r.proto === 'udp')) {
      ports.set(r.proto + '/' + port, { proto: r.proto, from: port, to: port });
    } else skipped++;
  }
  return { asns: [...asns], ports: [...ports.values()], skipped };
}

const rowKey = (r: UnnamedRow): string => r.client + '|' + r.dst + '|' + r.proto + '|' + r.port;

const REASON: Record<string, [string, 'muted' | 'warn']> = {
  'no-name': ['no DNS name', 'muted'],
  'unknown-name': ['name not known', 'warn'],
};

const COLS: SortCol[] = [
  { label: '' },
  { key: 'clientLabel', label: 'Client' },
  { key: 'dst', label: 'Destination' },
  { key: 'portNum', label: 'Port', cls: 'text-end' },
  { key: 'ownerLabel', label: 'Network owner' },
  { key: 'reason', label: 'Why unnamed' },
  { key: 'rxBytes', label: 'Rx', cls: 'text-end' },
  { key: 'txBytes', label: 'Tx', cls: 'text-end' },
];

export function initAppsUnnamed(o: {
  routerId: () => string; client: () => string; canEdit: () => boolean; manage: AppsManage;
  apps: () => Array<{ id: string; name: string }>;
}): { open(): void } {
  let detail: UnnamedDetail | null = null;
  let query = '';
  const picked = new Set<string>();
  const sort: SortState = { col: 'rxBytes', dir: 'desc' };

  function rowsShown(): Array<UnnamedRow & { clientLabel: string; ownerLabel: string; portNum: number; key: string }> {
    const q = query.trim().toLowerCase();
    return (detail?.rows || []).map((r) => ({
      ...r, key: rowKey(r), clientLabel: r.clientName || r.client, portNum: Number(r.port) || 0,
      ownerLabel: r.asn ? 'AS' + r.asn + (r.owner ? ' ' + r.owner : '') : '',
    })).filter((r) => !q || [r.clientLabel, r.client, r.clientMac, r.dst, r.ownerLabel, r.port].some((s) => s.toLowerCase().includes(q)));
  }

  function render(): void {
    const note = el('appsUnnamedNote');
    if (note) {
      const parts = ['Where the traffic no app was found for went in the last hour' +
        (o.client() ? ', for this client' : '') + ': ' + (detail ? fmtBytes(detail.rxBytes + detail.txBytes) : '-') + '.',
      'DNS names are not shown, and there are no query counts: the router\'s DNS cache is not a per-device log of what was asked. Amounts are bytes.'];
      if (detail && detail.total > detail.rows.length) parts.push('Showing the busiest ' + detail.rows.length + ' of ' + detail.total + '.');
      if (detail?.partial) parts.push('Some minutes had more destinations than are kept, so a few small ones are missing here; the totals still count them.');
      if (o.canEdit()) parts.push('Pick rows to name them as an app: by their network owner, or by port when the owner is unknown.');
      note.textContent = parts.join(' ');
    }
    renderSortHeader('appsUnnamedThead', COLS, sort, render);
    const body = el('appsUnnamedTable');
    if (!body) return;
    const rows = rowsShown();
    const edit = o.canEdit();
    body.innerHTML = !detail ? '<tr><td colspan="8" class="empty-state">Loading</td></tr>'
      : !rows.length ? '<tr><td colspan="8" class="empty-state">' + (query ? 'Nothing matches' : 'No unnamed traffic in the last hour') + '</td></tr>'
        : sortRows(rows, sort.col, sort.dir).map((r) => {
          const why = REASON[r.reason] || [r.reason, 'muted'];
          return '<tr><td class="tbl-pick">' + (edit ? '<input type="checkbox" aria-label="Pick this row" data-pick="' +
              esc(r.key) + '"' + (picked.has(r.key) ? ' checked' : '') + '>' : '') + '</td>' +
            '<td>' + esc(r.clientLabel) + (r.clientName ? '<div class="muted-note mono">' + esc(r.client) + '</div>' : '') + '</td>' +
            '<td class="mono">' + esc(r.dst) + '</td>' +
            '<td class="text-end"><span class="mono">' + esc(r.port || '-') + '</span> ' + protoPill(r.proto) + '</td>' +
            '<td>' + (r.ownerLabel ? esc(r.ownerLabel) : '<span class="muted-note">unknown</span>') + '</td>' +
            '<td>' + pill(why[0], why[1], true) + '</td>' +
            '<td class="text-end mono au-rx">' + esc(fmtBytes(r.rxBytes)) + '</td>' +
            '<td class="text-end mono au-tx">' + esc(fmtBytes(r.txBytes)) + '</td></tr>';
        }).join('');
    renderAssign();
  }

  function renderAssign(): void {
    const btn = el('appsUnnamedAssign') as HTMLButtonElement | null;
    const field = el('appsUnnamedTargetField');
    const edit = o.canEdit();
    if (btn) { btn.hidden = !edit; btn.disabled = !picked.size; }
    if (field) field.hidden = !edit;
  }

  function load(): void {
    const rid = o.routerId();
    if (!rid) return;
    const client = o.client();
    fetch('/api/apps/unnamed?routerId=' + encodeURIComponent(rid) + (client ? '&client=' + encodeURIComponent(client) : ''),
      { credentials: 'same-origin' })
      .then((r) => r.json().then((d) => (r.ok ? d as UnnamedDetail : Promise.reject(new Error((d && d.error) || r.statusText)))))
      .then((d) => { detail = d; render(); })
      .catch((e: Error) => {
        const body = el('appsUnnamedTable');
        if (body) body.innerHTML = '<tr><td colspan="8" class="empty-state">' + esc('Could not be read: ' + e.message) + '</td></tr>';
      });
  }

  function open(): void {
    detail = null;
    picked.clear();
    // Where picked rows go: a new app, or any app there is.
    const target = el('appsUnnamedTarget') as HTMLSelectElement | null;
    if (target) {
      target.innerHTML = '<option value="">to a new app</option>' + o.apps().slice().sort((a, b) => a.name.localeCompare(b.name))
        .map((a) => '<option value="' + esc(a.id) + '">to ' + esc(a.name) + '</option>').join('');
    }
    render();
    el('appsUnnamedModal')?.classList.add('open');
    load();
  }

  el('appsUnnamedTable')?.addEventListener('change', (e) => {
    const t = e.target as HTMLInputElement | null;
    if (!t?.dataset?.pick) return;
    if (t.checked) picked.add(t.dataset.pick); else picked.delete(t.dataset.pick);
    renderAssign();
  });
  el('appsUnnamedSearch')?.addEventListener('input', (e) => { query = (e.target as HTMLInputElement).value; render(); });
  el('appsUnnamedAssign')?.addEventListener('click', () => {
    const rows = (detail?.rows || []).filter((r) => picked.has(rowKey(r)));
    const p = prefillFrom(rows);
    if (!p.asns.length && !p.ports.length) {
      window.alert('None of the picked rows has a known network owner or a port other than 80 or 443, so there is nothing to name them by. ' +
        'Add the app\'s domains in Manage apps instead.');
      return;
    }
    const target = (el('appsUnnamedTarget') as HTMLSelectElement | null)?.value || '';
    el('appsUnnamedModal')?.classList.remove('open');
    o.manage.openEditor(target || null, p);
  });

  return { open };
}
