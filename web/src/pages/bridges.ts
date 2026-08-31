// The Bridges page — a port of the Bridges IIFE in public/app.js.
//
// Three tables from one payload: the bridges, their ports and the learned MAC
// table. The ports/hosts split is a tab strip rather than two cards, and both
// panels render from the same payload — switching is a class toggle plus a
// re-render, with no second data path to keep in step and no request behind a
// tab.

import { esc, el, resRow, debounce, renderSortHeader, sortMul, fmtMbps,
         type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';
import { mountAdds, mountRows } from '../resource';

export interface Bridge {
  id: string; name: string; protocolMode: string;
  vlanFiltering: boolean; igmpSnooping: boolean; dhcpSnooping: boolean;
  fastForward: boolean; priority: string; ageingTime: string; macAddress: string;
  mtu: number | null; running: boolean; disabled: boolean; comment: string;
  portCount: number; rxMbps: number | null; txMbps: number | null;
}

export interface BridgePort {
  id: string; bridge: string; interface: string; pvid: number | null;
  role: string; edge: string; learn: string; horizon: string;
  pathCost: number | null; frameTypes: string;
  disabled: boolean; inactive: boolean; dynamic: boolean;
}

export interface BridgeHost {
  mac: string; onInterface: string; bridge: string; vid: number | null;
  dynamic: boolean; local: boolean; external: boolean; age: string;
}

export interface BridgesPayload {
  ts: number; pollMs: number;
  bridges: Bridge[]; ports: BridgePort[]; hosts: BridgeHost[];
  hostTotal: number; hostCap: number; ratesAvailable: boolean;
  available: boolean; hostsAvailable: boolean;
}

const COLS_B: SortCol[] = [
  { key: 'name', label: 'Bridge' },
  { key: 'proto', label: 'Protocol' },
  { key: 'vlan', label: 'VLAN Filter' },
  { key: 'igmp', label: 'IGMP' },
  { key: 'mac', label: 'MAC' },
  { key: 'mtu', label: 'MTU' },
  { key: 'ports', label: 'Ports' },
  { key: 'rate', label: 'RX / TX' },
];
const COLS_P: SortCol[] = [
  { key: 'interface', label: 'Interface' },
  { key: 'bridge', label: 'Bridge' },
  { key: 'pvid', label: 'PVID' },
  { key: 'role', label: 'STP Role' },
  { key: 'edge', label: 'Edge' },
  { key: 'horizon', label: 'Horizon' },
  { key: 'state', label: 'State' },
];
const COLS_H: SortCol[] = [
  { key: 'mac', label: 'MAC Address' },
  { key: 'port', label: 'On Interface' },
  { key: 'bridge', label: 'Bridge' },
  { key: 'vid', label: 'VLAN' },
  { key: 'type', label: 'Type' },
];

function onOff(v: boolean, label: string): string {
  return v ? '<span class="wl-band wl-band-6">' + label + '</span>'
    : '<span style="color:var(--text-muted)">off</span>';
}

function rate(b: Bridge): string {
  if (b.rxMbps === null && b.txMbps === null) {
    return '<span style="color:var(--text-muted)" title="Interface rates are unavailable">&mdash;</span>';
  }
  return '<span style="color:var(--accent-rx)">' + fmtMbps(b.rxMbps || 0) + '</span> / ' +
    '<span style="color:var(--accent-tx,#f59f00)">' + fmtMbps(b.txMbps || 0) + '</span>';
}

function sorted<T>(rows: T[], sort: SortState, valFn: (row: T, key: string) => string | number): T[] {
  return rows.slice().sort((a, b) => {
    const av = valFn(a, sort.col), bv = valFn(b, sort.col);
    if (typeof av === 'string') return sortMul(sort) * av.localeCompare(bv as string);
    return sortMul(sort) * (av - (bv as number));
  });
}

export function initBridgesPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tbody = el('bridgesTable');
  const theadRow = el('bridgesThead');
  if (!tbody || !theadRow) return;

  let data: BridgesPayload | null = null;
  const sortB: SortState = { col: 'name', dir: 'asc' };
  const sortP: SortState = { col: 'interface', dir: 'asc' };
  const sortH: SortState = { col: 'mac', dir: 'asc' };
  let tab: 'ports' | 'hosts' = 'ports';

  const bind = (theadId: string, cols: SortCol[], sort: SortState) =>
    renderSortHeader(theadId, cols, sort, () => render());

  function render(): void {
    if (!data) return;

    // ── Bridges ──
    bind('bridgesThead', COLS_B, sortB);
    const bridges = sorted(data.bridges || [], sortB, (b, k) => {
      if (k === 'ports') return b.portCount;
      if (k === 'mtu') return b.mtu || 0;
      if (k === 'rate') return (b.rxMbps || 0) + (b.txMbps || 0);
      if (k === 'vlan') return b.vlanFiltering ? 1 : 0;
      if (k === 'igmp') return b.igmpSnooping ? 1 : 0;
      if (k === 'proto') return (b.protocolMode || '').toLowerCase();
      if (k === 'mac') return (b.macAddress || '').toLowerCase();
      return (b.name || '').toLowerCase();
    });
    const bBadge = el('bridgesBadge');
    if (bBadge) {
      bBadge.textContent = String(bridges.length);
      bBadge.className = 'card-badge' + (bridges.length ? ' active-blue' : '');
    }
    tbody!.innerHTML = bridges.length ? bridges.map((b) =>
      '<tr' + (b.disabled ? ' style="opacity:.55"' : '') + resRow(b.id, b.name) + '>' +
      '<td>' + esc(b.name) + (b.running ? '' : ' <span class="wl-band wl-band-24">down</span>') + '</td>' +
      '<td>' + (b.protocolMode ? '<span class="wl-band wl-band-5">' + esc(b.protocolMode) + '</span>'
        : '<span style="color:var(--text-muted)">&mdash;</span>') + '</td>' +
      '<td>' + onOff(b.vlanFiltering, 'on') + '</td>' +
      '<td>' + onOff(b.igmpSnooping, 'on') + '</td>' +
      '<td class="mono">' + esc(b.macAddress) + '</td>' +
      '<td>' + (b.mtu || '&mdash;') + '</td>' +
      '<td>' + b.portCount + '</td>' +
      '<td>' + rate(b) + '</td>' +
      '</tr>').join('') : '<tr><td colspan="8" class="empty-state">' +
      (data.available ? 'No bridges configured on this router.' : 'This router has no bridge menu.') +
      '</td></tr>';

    // ── Ports ──
    bind('bridgesPortThead', COLS_P, sortP);
    const ports = sorted(data.ports || [], sortP, (p, k) => {
      if (k === 'pvid') return p.pvid || 0;
      if (k === 'state') return (p.disabled ? 2 : p.inactive ? 1 : 0);
      return ((p as unknown as Record<string, unknown>)[k] || '').toString().toLowerCase();
    });
    const pBadge = el('bridgesPortBadge');
    if (pBadge) pBadge.textContent = String(ports.length);
    const pTable = el('bridgesPortTable');
    if (pTable) {
      pTable.innerHTML = ports.length ? ports.map((p) => {
        const state = p.disabled ? '<span class="wl-band wl-band-24">disabled</span>'
          : p.inactive ? '<span style="color:var(--text-muted)">inactive</span>'
          : '<span class="wl-band wl-band-6">active</span>';
        return '<tr' + resRow(p.id, p.interface) + '>' +
          '<td>' + esc(p.interface) + (p.dynamic ? ' <span class="wl-band wl-band-6">dyn</span>' : '') + '</td>' +
          '<td>' + esc(p.bridge) + '</td>' +
          '<td>' + (p.pvid === null ? '&mdash;' : '<span class="wl-band wl-band-24">' + p.pvid + '</span>') + '</td>' +
          // A bridge with protocol-mode=none reports no role at all, which is
          // not the same as a port whose role is unknown.
          '<td>' + (p.role ? esc(p.role) : '<span style="color:var(--text-muted)">no STP</span>') + '</td>' +
          '<td>' + esc(p.edge || '—') + '</td>' +
          '<td>' + esc(p.horizon || '—') + '</td>' +
          '<td>' + state + '</td>' +
          '</tr>';
      }).join('') : '<tr><td colspan="7" class="empty-state">No bridge ports.</td></tr>';
    }

    // ── Host table ──
    bind('bridgesHostThead', COLS_H, sortH);
    const search = el<HTMLInputElement>('bridgesHostSearch');
    const hq = (search?.value || '').toLowerCase().trim();
    let hosts = (data.hosts || []).filter((h) => {
      if (!hq) return true;
      return (h.mac + ' ' + h.onInterface + ' ' + (h.vid === null ? '' : h.vid))
        .toLowerCase().indexOf(hq) !== -1;
    });
    hosts = sorted(hosts, sortH, (h, k) => {
      if (k === 'vid') return h.vid === null ? -1 : h.vid;
      if (k === 'port') return (h.onInterface || '').toLowerCase();
      if (k === 'type') return (h.local ? 2 : h.dynamic ? 1 : 0);
      return ((h as unknown as Record<string, unknown>)[k] || '').toString().toLowerCase();
    });
    const hBadge = el('bridgesHostBadge');
    if (hBadge) hBadge.textContent = String(data.hostTotal || 0);
    const note = el('bridgesHostNote');
    if (note) {
      note.textContent = !data.hostsAvailable
        ? 'the RouterOS user cannot read the host table'
        : (data.hostTotal > (data.hostCap || 0)
          ? 'showing ' + data.hostCap + ' of ' + data.hostTotal
          : '');
    }
    const hTable = el('bridgesHostTable');
    if (hTable) {
      hTable.innerHTML = hosts.length ? hosts.map((h) => {
        const type = h.local ? '<span style="color:var(--text-muted)">local</span>'
          : h.external ? '<span class="wl-band wl-band-24">external</span>'
          : h.dynamic ? '<span class="wl-band wl-band-6">learned</span>'
          : '<span class="wl-band wl-band-5">static</span>';
        return '<tr>' +
          '<td class="mono">' + esc(h.mac) + '</td>' +
          '<td>' + esc(h.onInterface) + '</td>' +
          '<td>' + esc(h.bridge) + '</td>' +
          '<td>' + (h.vid === null ? '&mdash;' : '<span class="wl-band wl-band-24">' + h.vid + '</span>') + '</td>' +
          '<td>' + type + '</td>' +
          '</tr>';
      }).join('') : '<tr><td colspan="5" class="empty-state">' +
        (hq ? 'No hosts match that search.' : 'No learned hosts.') + '</td></tr>';
    }
  }

  function renderSummary(): void {
    if (!data) return;
    const modes: Record<string, 1> = {};
    (data.bridges || []).forEach((b) => { if (b.protocolMode) modes[b.protocolMode] = 1; });
    const m = Object.keys(modes);
    const set = (id: string, v: string) => { const n = el(id); if (n) n.textContent = v; };
    set('brSumCount', String((data.bridges || []).length));
    set('brSumPorts', String((data.ports || []).length));
    set('brSumHosts', String(data.hostTotal || 0));
    set('brSumStp', m.length ? m.join(', ') : '—');
  }

  function setTab(key: string): void {
    tab = key === 'hosts' ? 'hosts' : 'ports';
    const bar = el('brTabBar');
    bar?.querySelectorAll('.stab').forEach((b) => {
      const on = b.getAttribute('data-brtab') === tab;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    el('page-bridges')?.querySelectorAll('.brtab-panel').forEach((p) => {
      p.classList.toggle('active', p.id === 'brtab-' + tab);
    });
    // The search box and the "showing 500 of N" note belong to the host view.
    const tools = el('bridgesHostTools');
    if (tools) (tools as HTMLElement).hidden = tab !== 'hosts';
    if (data) render();
  }

  const bar = el('brTabBar');
  if (bar) {
    bar.addEventListener('click', (e) => {
      const btn = (e.target as HTMLElement).closest?.('[data-brtab]');
      if (btn) setTab(btn.getAttribute('data-brtab') || '');
    });
    // Arrow-key movement along the strip, per the ARIA tablist pattern.
    bar.addEventListener('keydown', (e) => {
      const ev = e as KeyboardEvent;
      if (ev.key !== 'ArrowLeft' && ev.key !== 'ArrowRight') return;
      ev.preventDefault();
      setTab(tab === 'ports' ? 'hosts' : 'ports');
      bar.querySelector<HTMLElement>('[data-brtab="' + tab + '"]')?.focus();
    });
  }

  socket.on('bridges:update', (d: BridgesPayload) => {
    if (!d) return;
    data = d;
    renderSummary();
    if (isVisible('bridges')) render();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'bridges' && data) render();
  });

  const hs = el<HTMLInputElement>('bridgesHostSearch');
  if (hs) hs.addEventListener('input', debounce(render, 150));

  // The write path. Both resources declare `guard: 'selfPath'`, and the server
  // runs it after a fresh read and before the write: an edit that touches the
  // interface the router sees us arriving on is refused with a warning naming
  // that interface and address, and proceeds only against the fingerprint of
  // the warning the operator actually read.
  mountAdds(socket);
  mountRows(socket);
}
