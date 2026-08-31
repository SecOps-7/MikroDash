// The VLANs page — a port of the VLANs IIFE in public/app.js.
//
// The rate column carries a sparkline per VLAN, and its history is kept HERE
// rather than server-side: it is presentation state, and the collector already
// ships every sample the line needs. Pushed once per update and never on
// re-render, or sorting or typing in the search box would forge samples the
// router never sent.

import { esc, el, resRow, debounce, renderSortHeader, sortMul, fmtMbps,
         type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';
import { mountAdds, mountRows } from '../resource';

export interface VlanInterface {
  id: string; name: string; parent: string; mtu: number | null;
  running: boolean; disabled: boolean; comment: string;
  rxMbps: number | null; txMbps: number | null;
}

export interface Vlan {
  vlanId: number; interfaces: VlanInterface[];
  tagged: string[]; untagged: string[]; bridges: string[];
  clients: number; rxMbps: number | null; txMbps: number | null; name: string;
}

export interface BridgeVlanRow {
  bridge: string; raw: string; ids: number[]; ranges: number[][]; truncated: boolean;
  tagged: string[]; untagged: string[]; currentTagged: string[];
  dynamic: boolean; disabled: boolean;
}

export interface VlansPayload {
  ts: number; pollMs: number;
  vlans: Vlan[]; bridgeVlans: BridgeVlanRow[]; ports: unknown[];
  dynamicCount: number; ratesAvailable: boolean;
}

const COLS: SortCol[] = [
  { key: 'vlanId', label: 'VLAN' },
  { key: 'name', label: 'Interface' },
  { key: 'parent', label: 'Parent' },
  { key: 'mtu', label: 'MTU' },
  { key: 'tagged', label: 'Tagged Ports' },
  { key: 'untagged', label: 'Untagged Ports' },
  { key: 'clients', label: 'Clients' },
  { key: 'rate', label: 'RX / TX' },
];

const HIST_MAX = 40;

function sortVal(v: Vlan, key: string): string | number {
  if (key === 'vlanId') return v.vlanId;
  if (key === 'clients') return v.clients || 0;
  if (key === 'mtu') return (v.interfaces[0] && v.interfaces[0].mtu) || 0;
  if (key === 'rate') return (v.rxMbps || 0) + (v.txMbps || 0);
  if (key === 'name') return (v.name || '').toLowerCase();
  if (key === 'parent') return ((v.interfaces[0] && v.interfaces[0].parent) || '').toLowerCase();
  if (key === 'tagged') return v.tagged.length;
  return v.untagged.length;
}

function ports(list: string[]): string {
  if (!list.length) return '<span style="color:var(--text-muted)">&mdash;</span>';
  return list.map((n) => '<span class="wl-band wl-band-5">' + esc(n) + '</span>').join(' ');
}

// Stroked with currentColor and coloured by class, so the line cannot drift
// from the value above it.
function spark(history: number[], dir: string): string {
  if (!history || history.length < 2) return '<span class="vlan-spark-slot"></span>';
  const w = 56, h = 14, pad = 1.5;
  const max = Math.max.apply(null, history) || 1;   // baseline always zero, so
  const pts = history.map((v, i) => {               // a rise reads as a rise
    const x = pad + (i / (history.length - 1)) * (w - pad * 2);
    const y = h - pad - (v / max) * (h - pad * 2);
    return x.toFixed(1) + ',' + y.toFixed(1);
  });
  return '<svg class="vlan-spark ' + dir + '" width="' + w + '" height="' + h +
    '" viewBox="0 0 ' + w + ' ' + h + '">' +
    '<polyline points="' + pts.join(' ') + '" fill="none" stroke="currentColor"' +
    ' stroke-width="1.3" stroke-linejoin="round" stroke-linecap="round"/></svg>';
}

function rateLine(dir: string, mbps: number | null, history: number[]): string {
  const cls = mbps ? dir : 'zero';
  return '<div class="vlan-rate-line">' +
    '<span class="vlan-rate-arrow ' + cls + '">' + (dir === 'rx' ? '↓' : '↑') + '</span>' +
    '<span class="vlan-rate-val ' + cls + '">' + fmtMbps(mbps || 0) + '</span>' +
    // The line keeps its direction colour even at zero: it is showing the trend
    // that got here, not the current reading.
    spark(history, dir) +
    '</div>';
}

export function initVlansPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tbody = el('vlansTable');
  const theadRow = el('vlansThead');
  if (!tbody || !theadRow) return;

  let data: VlansPayload | null = null;
  const sort: SortState = { col: 'vlanId', dir: 'asc' };
  let showDynamic = false;
  const hist: Record<number, { rx: number[]; tx: number[] }> = {};

  function pushHistory(d: VlansPayload): void {
    const live: Record<number, boolean> = {};
    d.vlans.forEach((v) => {
      live[v.vlanId] = true;
      const h = hist[v.vlanId] || (hist[v.vlanId] = { rx: [], tx: [] });
      // A null rate is "not reported", so the line holds its last value rather
      // than dropping to zero and drawing a cliff that never happened.
      if (v.rxMbps !== null) h.rx.push(v.rxMbps);
      if (v.txMbps !== null) h.tx.push(v.txMbps);
      if (h.rx.length > HIST_MAX) h.rx.splice(0, h.rx.length - HIST_MAX);
      if (h.tx.length > HIST_MAX) h.tx.splice(0, h.tx.length - HIST_MAX);
    });
    // Drop VLANs that no longer exist, so a deleted-and-recreated VLAN does not
    // inherit the old one's trend line.
    Object.keys(hist).forEach((k) => { if (!live[Number(k)]) delete hist[Number(k)]; });
  }

  // null is "the router did not report a rate", which is not the same as idle.
  function rate(v: Vlan): string {
    if (v.rxMbps === null && v.txMbps === null) {
      return '<span style="color:var(--text-muted)" title="Interface rates are unavailable">&mdash;</span>';
    }
    const h = hist[v.vlanId] || { rx: [], tx: [] };
    return '<div class="vlan-rate">' + rateLine('rx', v.rxMbps, h.rx) +
      rateLine('tx', v.txMbps, h.tx) + '</div>';
  }

  function render(): void {
    if (!data) return;
    const search = el<HTMLInputElement>('vlansSearch');
    const q = (search?.value || '').toLowerCase().trim();
    let rows = data.vlans.filter((v) => {
      if (!q) return true;
      return String(v.vlanId).indexOf(q) === 0 || (v.name || '').toLowerCase().indexOf(q) !== -1 ||
        v.tagged.concat(v.untagged).some((p) => p.toLowerCase().indexOf(q) !== -1);
    });
    rows = rows.slice().sort((a, b) => {
      const av = sortVal(a, sort.col), bv = sortVal(b, sort.col);
      if (typeof av === 'string') return sortMul(sort) * av.localeCompare(bv as string);
      return sortMul(sort) * (av - (bv as number));
    });

    renderSortHeader('vlansThead', COLS, sort, () => render());

    const badge = el('vlansBadge');
    if (badge) {
      badge.textContent = String(data.vlans.length);
      badge.className = 'card-badge' + (data.vlans.length ? ' active-blue' : '');
    }

    tbody!.innerHTML = rows.length ? rows.map((v) => {
      const i0 = v.interfaces[0];
      // A VLAN that exists only at layer 2 — membership via a bridge port's
      // pvid, with no /interface/vlan row — has nothing to edit, so it gets no
      // data-id and is simply not clickable.
      return '<tr' + resRow(i0 ? i0.id : '', i0 ? i0.name : null) + '>' +
        '<td><span class="wl-band wl-band-24">' + v.vlanId + '</span></td>' +
        '<td>' + (v.name ? esc(v.name) : '<span style="color:var(--text-muted)">no L3 interface</span>') + '</td>' +
        '<td>' + esc(i0 ? i0.parent : '') + '</td>' +
        '<td>' + (i0 && i0.mtu ? i0.mtu : '&mdash;') + '</td>' +
        '<td>' + ports(v.tagged) + '</td>' +
        '<td>' + ports(v.untagged) + '</td>' +
        '<td>' + (v.clients || 0) + '</td>' +
        '<td>' + rate(v) + '</td>' +
        '</tr>';
    }).join('') : '<tr><td colspan="8" class="empty-state">No VLANs configured on this router.</td></tr>';

    renderBridge();
  }

  function renderBridge(): void {
    const tb = el('vlansBridgeTable');
    if (!tb || !data) return;
    // Dynamic rows are filtered HERE, at render. They are kept in the join
    // because on a real router most VLAN membership comes from them —
    // filtering them earlier would show every VLAN with no tagged ports.
    const rows = data.bridgeVlans.filter((r) => showDynamic || !r.dynamic);
    const badge = el('vlansBridgeBadge');
    if (badge) badge.textContent = String(rows.length);
    const chip = el('vlansDynChip');
    if (chip) chip.textContent = String(data.dynamicCount);
    tb.innerHTML = rows.length ? rows.map((r) =>
      // Dynamic rows carry the same weight as static ones: the "dynamic" pill
      // already says which is which, and dimming them made real membership
      // harder to read for no added information.
      '<tr>' +
      '<td>' + esc(r.bridge) + '</td>' +
      '<td><span class="wl-band wl-band-24">' + esc(r.raw) + '</span></td>' +
      '<td>' + ports(r.tagged) + '</td>' +
      '<td>' + ports(r.untagged) + '</td>' +
      '<td>' + (r.dynamic ? '<span class="wl-band wl-band-6">dynamic</span>'
        : '<span class="wl-band wl-band-5">static</span>') + '</td>' +
      '</tr>').join('') : '<tr><td colspan="5" class="empty-state">No bridge VLAN entries.</td></tr>';
  }

  function renderSummary(): void {
    if (!data) return;
    const tagged = new Set<string>(), untagged = new Set<string>();
    let rx = 0, tx = 0, any = false;
    data.vlans.forEach((v) => {
      v.tagged.forEach((p) => tagged.add(p));
      v.untagged.forEach((p) => untagged.add(p));
      if (v.rxMbps !== null) { rx += v.rxMbps; any = true; }
      if (v.txMbps !== null) { tx += v.txMbps; any = true; }
    });
    const set = (id: string, v: string) => { const n = el(id); if (n) n.textContent = v; };
    set('vlSumCount', String(data.vlans.length));
    set('vlSumTagged', String(tagged.size));
    set('vlSumUntagged', String(untagged.size));
    const r = el('vlSumRate');
    if (r) r.innerHTML = any ? fmtMbps(rx + tx) : '&mdash;';
  }

  socket.on('vlans:update', (d: VlansPayload) => {
    if (!d) return;
    data = d;
    pushHistory(d);
    renderSummary();
    if (isVisible('vlans')) render();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'vlans' && data) render();
  });

  const se = el<HTMLInputElement>('vlansSearch');
  if (se) se.addEventListener('input', debounce(render, 150));
  const dyn = el<HTMLInputElement>('vlansShowDynamic');
  if (dyn) dyn.addEventListener('change', () => { showDynamic = dyn.checked; renderBridge(); });

  // The vlan resource declares guard: 'selfPath' — an edit naming the interface
  // the router sees us on is refused with a warning that names it.
  mountAdds(socket);
  mountRows(socket);
}
