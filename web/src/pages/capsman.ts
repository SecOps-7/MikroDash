// The CAPsMAN page — a port of TWO IIFEs in public/app.js.
//
// The CAP table (app.js 12239-12397) and the configuration card (15953-16154)
// sit three thousand lines apart there: the card was added later and was filed
// with the other resource-dialog cards rather than with the page it draws into.
// They are one module here because they render one page from one payload.
//
// Each half keeps its own guard, its own render and its own handlers, because
// the two are not interchangeable: the table half does nothing at all until a
// payload arrives, while the card half draws a waiting state. Merging their
// renders would change what the page shows before the first update.
//
// Clients are attributed to their CAP SERVER-SIDE, from the `cap` field the
// router reports on each interface — see internal/collect/capsman.go. This file
// draws rows and nothing else.
//
// ── PAGE SCOPE IS THE AUTHORISATION BOUNDARY ────────────────────────────────
//
// Every profile row carries `data-res` naming a CAPsMAN resource, while the Wifi
// Networks page's rows name the wifi ones. A role holding write on wifi but not
// capsman can override a value on one interface and cannot edit the shared
// profile every CAP follows.

import { esc, el, resRow, debounce, renderSortHeader, sortMul,
  type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';

export interface CapsRadio { radioMac: string; interface: string; disabled: boolean }

export interface CapsClient {
  mac: string; interface: string; ssid: string; signal: number | null; uptime: string;
}

export interface Cap {
  identity: string; address: string; boardName: string; serial: string; version: string;
  baseMac: string; commonName: string; state: string; connectedTime: string; uptime: string;
  radios: CapsRadio[]; clients: CapsClient[]; clientCount: number;
}

export interface CapsProvisioningRule {
  id: string; identity: string; supportedBands: string[]; action: string;
  masterConfiguration: string; slaveConfigurations: string[]; nameFormat: string;
  radioMac: string; identityRegexp: string; comment: string; disabled: boolean;
}

export interface CapsConfigProfile {
  id: string; name: string; ssid: string; mode: string; country: string; hideSsid: boolean;
  security: string; channel: string; datapath: string; manager: string;
  comment: string; disabled: boolean;
}

export interface CapsSecurityProfile {
  id: string; name: string; authTypes: string; wps: string; ft: boolean;
  comment: string; disabled: boolean;
}

export interface CapsChannelProfile {
  id: string; name: string; band: string; frequency: string; width: string;
  secondaryFrequency: string; skipDfsChannels: string; comment: string; disabled: boolean;
}

export interface CapsDatapathProfile {
  id: string; name: string; bridge: string; vlanId: string; clientIsolation: boolean;
  localForwarding: boolean; trafficProcessing: string; comment: string; disabled: boolean;
}

export interface CapsTotals {
  caps: number; capsOk: number; radios: number; clients: number;
  clientsOnCaps: number; clientsLocal: number;
}

export interface CapsmanPayload {
  ts: number; pollMs: number; role: string;
  manager: {
    enabled: boolean; interfaces: string[]; caCertificate: string; certificate: string;
    requirePeerCertificate: boolean; upgradePolicy: string; packagePath: string;
  };
  cap: {
    enabled: boolean; discoveryInterfaces: string[]; capsManAddresses: string[];
    currentAddress: string; currentIdentity: string; certificate: string; slavesDatapath: string;
  };
  caps: Cap[];
  provisioning: CapsProvisioningRule[];
  localRadios: CapsRadio[];
  totals: CapsTotals;
  profiles: {
    configuration: CapsConfigProfile[]; security: CapsSecurityProfile[];
    channel: CapsChannelProfile[]; datapath: CapsDatapathProfile[];
  };
  available: boolean;
}

const COLS: SortCol[] = [
  { key: 'identity', label: 'Identity' },
  { key: 'board', label: 'Board' },
  { key: 'version', label: 'Version' },
  { key: 'serial', label: 'Serial' },
  { key: 'state', label: 'State' },
  { key: 'connected', label: 'Connected' },
  { key: 'radios', label: 'Radios' },
  { key: 'clients', label: 'Clients' },
];

const CAPS_RES: Record<string, string> = {
  provisioning: 'capsProvisioning',
  configuration: 'capsConfig',
  security: 'capsSecurity',
  channel: 'capsChannel',
  datapath: 'capsDatapath',
};

const TBODY: Record<string, string> = {
  provisioning: 'capsCfgProvTable',
  configuration: 'capsCfgConfigTable',
  security: 'capsCfgSecurityTable',
  channel: 'capsCfgChannelTable',
  datapath: 'capsCfgDatapathTable',
};

const COLSPAN: Record<string, number> = {
  provisioning: 6, configuration: 6, security: 4, channel: 5, datapath: 5,
};

const MUTED = 'style="color:var(--text-muted)"';

export function initCapsmanPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tbodyEl = el('capsmanTable');
  const theadEl = el('capsmanThead');
  const bar = el('capsCfgTabBar');
  // Each half keeps the live guard it was written with: the table needs both its
  // tbody and its header row, the card needs its tab bar, and either can be
  // absent without the other.
  const hasTable = !!tbodyEl && !!theadEl;
  if (!hasTable && !bar) return;

  let data: CapsmanPayload | null = null;
  const sort: SortState = { col: 'identity', dir: 'asc' };
  let open: Record<string, boolean> = {};   // identity -> client list expanded

  let tab = 'provisioning';
  let writable: Record<string, boolean> = {};

  // ── The CAP table ─────────────────────────────────────────────────────────

  function sortVal(c: Cap, key: string): string | number {
    if (key === 'clients') return c.clientCount || 0;
    if (key === 'radios') return c.radios.length;
    if (key === 'board') return (c.boardName || '').toLowerCase();
    if (key === 'connected') return (c.connectedTime || '').toLowerCase();
    return String((c as unknown as Record<string, unknown>)[key] || '').toLowerCase();
  }

  function stateBadge(state: string): string {
    const ok = /^ok$/i.test(state || '');
    return '<span class="wl-band ' + (ok ? 'wl-band-6' : 'wl-band-24') + '">' +
           esc(state || 'unknown') + '</span>';
  }

  function clientRow(c: CapsClient): string {
    return '<tr class="cap-client"><td colspan="8" style="padding-left:2.2rem">' +
      '<span ' + MUTED + '>' + esc(c.interface) + '</span> &nbsp; ' +
      '<span class="mono">' + esc(c.mac) + '</span> &nbsp; ' +
      (c.ssid ? '<span class="wl-band wl-band-5">' + esc(c.ssid) + '</span> &nbsp; ' : '') +
      (c.signal === null ? '' : '<span ' + MUTED + '>' + String(c.signal) + ' dBm</span> &nbsp; ') +
      '<span ' + MUTED + '>' + esc(c.uptime) + '</span>' +
      '</td></tr>';
  }

  function render(): void {
    // No payload, no table. The live page leaves whatever is on screen alone
    // rather than drawing an empty state, INCLUDING after a router switch — the
    // rows stay until the new router's first update replaces them.
    if (!data || !tbodyEl) return;
    const tbody = tbodyEl;
    const st = data;

    const q = (el<HTMLInputElement>('capsmanSearch')?.value || '').toLowerCase().trim();
    let rows = (st.caps || []).filter((c) => {
      if (!q) return true;
      return (c.identity + ' ' + c.boardName + ' ' + c.serial).toLowerCase().indexOf(q) !== -1;
    });
    rows = rows.slice().sort((a, b) => {
      const av = sortVal(a, sort.col), bv = sortVal(b, sort.col);
      if (typeof av === 'string') return sortMul(sort) * av.localeCompare(bv as string);
      return sortMul(sort) * ((av as number) - (bv as number));
    });

    renderSortHeader('capsmanThead', COLS, sort, () => render());

    const badge = el('capsmanBadge');
    if (badge) {
      badge.textContent = String((st.caps || []).length);
      badge.className = 'card-badge' + ((st.caps || []).length ? ' active-blue' : '');
    }

    if (!rows.length) {
      // A CAP is not a broken manager, so the empty state says which it is.
      //
      // THE SEARCH RUNG GOES LAST, after the two structural ones: a CAP with a
      // search term typed into it is still a CAP, and saying so is more useful
      // than reporting the filter. Without this rung a viewer who filtered on a
      // name none of their CAPs match was told the manager has nothing
      // connected — a statement about the router rather than about what they
      // just typed. This port reproduced that faithfully and reported it as
      // ToDo #20; the fix landed live on 2026-08-25 and is adopted here, in the
      // same order.
      const msg = !st.available
        ? 'This router runs the legacy wireless package, which has no CAPsMAN here.'
        : (st.role === 'cap'
          ? 'This router is a CAP, not a manager — it has no CAPs of its own.'
          : (q ? 'No CAPs match that search.'
            : 'No CAPs are connected to this manager.'));
      tbody.innerHTML = '<tr><td colspan="8" class="empty-state">' + msg + '</td></tr>';
    } else {
      tbody.innerHTML = rows.map((c) => {
        const isOpen = !!open[c.identity];
        const head = '<tr class="cap-row" data-cap="' + esc(c.identity) + '" style="cursor:pointer">' +
          '<td>' + (c.clientCount ? (isOpen ? '▾ ' : '▸ ') : '') + esc(c.identity) + '</td>' +
          '<td>' + esc(c.boardName) + '</td>' +
          '<td>' + esc(c.version) + '</td>' +
          '<td>' + esc(c.serial) + '</td>' +
          '<td>' + stateBadge(c.state) + '</td>' +
          '<td>' + esc(c.connectedTime) + '</td>' +
          '<td>' + (c.radios.length
            ? c.radios.map((r) => '<span class="wl-band wl-band-5">' + esc(r.interface) + '</span>').join(' ')
            : '<span ' + MUTED + '>&mdash;</span>') + '</td>' +
          '<td>' + String(c.clientCount) + '</td>' +
        '</tr>';
        return head + (isOpen ? c.clients.map(clientRow).join('') : '');
      }).join('');
      tbody.querySelectorAll('.cap-row').forEach((tr) => {
        tr.addEventListener('click', () => {
          const id = tr.getAttribute('data-cap') || '';
          open[id] = !open[id];
          render();
        });
      });
    }

    // Provisioning used to be rendered here too, into a read-only card of its
    // own. The configuration card's Provisioning tab shows the same rules and
    // can edit them, so the second table was only ever something else to keep in
    // step. The payload still carries `provisioning`; the card is what went.
    renderRolePanel();
  }

  // On a CAP the manager table is empty by definition, so the page says who is
  // managing it rather than looking broken.
  function renderRolePanel(): void {
    const panel = el('capsmanRolePanel'), body = el('capsmanRoleBody');
    if (!panel || !body || !data) return;
    const c = data.cap || ({} as CapsmanPayload['cap']);
    if (data.role === 'cap' || (data.role === 'both' && c.currentIdentity)) {
      panel.style.display = '';
      body.innerHTML = '<div class="kv-grid">' +
        '<div class="kv-item"><div class="kv-key">Managed by</div><div class="kv-val on">' +
          esc(c.currentIdentity || 'discovering…') + '</div></div>' +
        '<div class="kv-item"><div class="kv-key">Manager address</div><div class="kv-val">' +
          esc(c.currentAddress || '—') + '</div></div>' +
        '<div class="kv-item"><div class="kv-key">Discovery interfaces</div><div class="kv-val">' +
          esc((c.discoveryInterfaces || []).join(', ') || '—') + '</div></div>' +
        '<div class="kv-item"><div class="kv-key">Certificate</div><div class="kv-val">' +
          esc(c.certificate || '—') + '</div></div>' +
      '</div>';
    } else {
      panel.style.display = 'none';
    }
  }

  function renderSummary(): void {
    if (!data) return;
    const t = data.totals || ({} as Partial<CapsTotals>);
    const modes: Record<string, string> = {
      manager: 'Manager', cap: 'CAP', both: 'Manager + CAP', none: 'Off',
    };
    const set = (id: string, v: string) => { const e = el(id); if (e) e.textContent = v; };
    set('capSumMode', data.available ? (modes[data.role] || '—') : 'Unsupported');
    set('capSumCaps', t.caps === undefined ? '—' : String(t.caps));
    set('capSumRadios', t.radios === undefined ? '—' : String(t.radios));
    set('capSumClients', t.clients === undefined ? '—' : String(t.clients));
  }

  // ── The configuration card ────────────────────────────────────────────────

  function dash(v: string): string {
    return v ? esc(v) : '<span class="muted-note">&mdash;</span>';
  }

  function yesNo(v: boolean): string {
    return v ? 'Yes' : '<span class="muted-note">No</span>';
  }

  /** Rows for a tab, from the one payload the collector already sends. */
  function rowsFor(t: string): unknown[] {
    if (!data) return [];
    if (t === 'provisioning') return data.provisioning || [];
    const p = data.profiles as unknown as Record<string, unknown[]> | null;
    return (p && p[t]) || [];
  }

  function provRow(p: CapsProvisioningRule, at: number, last: number, canMove: boolean): string {
    // data-res-move, not a name of this card's own: the engine owns the reorder
    // flow, including the guard prompt it can raise. Order is meaning here —
    // the first rule whose bands match a joining radio wins.
    const move = canMove
      ? '<button class="fw-move" data-res-move="up" title="Move up"' + (at === 0 ? ' disabled' : '') + '>&#9650;</button>' +
        '<button class="fw-move" data-res-move="down" title="Move down"' + (at === last ? ' disabled' : '') + '>&#9660;</button>'
      : '';
    return '<tr' + resRow(p.id, p.identity, 'capsProvisioning') +
             (p.disabled ? ' style="opacity:.5"' : '') + '>' +
      '<td>' + move + '</td>' +
      '<td>' + ((p.supportedBands || []).map((b) =>
        '<span class="wl-band wl-band-24" style="margin-right:.2rem">' + esc(b) + '</span>').join('') ||
        '<span class="muted-note">any</span>') + '</td>' +
      '<td>' + dash(p.action) + '</td>' +
      '<td>' + dash(p.masterConfiguration) + '</td>' +
      '<td>' + ((p.slaveConfigurations || []).map(esc).join(', ') ||
        '<span class="muted-note">&mdash;</span>') + '</td>' +
      '<td>' + dash(p.nameFormat) + '</td>' +
    '</tr>';
  }

  function configRow(c: CapsConfigProfile): string {
    return '<tr' + resRow(c.id, c.name, 'capsConfig') + (c.disabled ? ' style="opacity:.5"' : '') + '>' +
      '<td>' + esc(c.name) +
        // A profile carrying `manager` is the CAP-side setting MikroTik warns
        // must never be provisioned onward. Worth flagging where it appears.
        (c.manager ? '<span class="badge bg-yellow-lt" style="margin-left:.35rem">manager</span>' : '') + '</td>' +
      '<td>' + dash(c.ssid) +
        (c.hideSsid ? '<span class="badge bg-secondary-lt" style="margin-left:.35rem">Hidden</span>' : '') + '</td>' +
      '<td>' + dash(c.country) + '</td>' +
      '<td>' + dash(c.security) + '</td>' +
      '<td>' + dash(c.channel) + '</td>' +
      '<td>' + dash(c.datapath) + '</td>' +
    '</tr>';
  }

  function securityRow(s: CapsSecurityProfile): string {
    // An open profile is the thing worth noticing on this tab, so it is the one
    // value that gets a colour rather than plain text.
    const isOpen = !String(s.authTypes || '').trim();
    return '<tr' + resRow(s.id, s.name, 'capsSecurity') + (s.disabled ? ' style="opacity:.5"' : '') + '>' +
      '<td>' + esc(s.name) + '</td>' +
      '<td><span class="badge ' + (isOpen ? 'bg-red-lt' : 'bg-azure-lt') + '">' +
        esc(isOpen ? 'Open' : s.authTypes) + '</span></td>' +
      '<td>' + dash(s.wps) + '</td>' +
      '<td>' + yesNo(s.ft) + '</td>' +
    '</tr>';
  }

  function channelRow(c: CapsChannelProfile): string {
    return '<tr' + resRow(c.id, c.name, 'capsChannel') + (c.disabled ? ' style="opacity:.5"' : '') + '>' +
      '<td>' + esc(c.name) + '</td>' +
      '<td>' + dash(c.band) + '</td>' +
      '<td>' + dash(c.frequency) + '</td>' +
      '<td>' + dash(c.width) + '</td>' +
      '<td>' + dash(c.skipDfsChannels) + '</td>' +
    '</tr>';
  }

  function datapathRow(d: CapsDatapathProfile): string {
    return '<tr' + resRow(d.id, d.name, 'capsDatapath') + (d.disabled ? ' style="opacity:.5"' : '') + '>' +
      '<td>' + esc(d.name) + '</td>' +
      '<td>' + dash(d.bridge) + '</td>' +
      '<td>' + dash(d.vlanId) + '</td>' +
      '<td>' + yesNo(d.clientIsolation) + '</td>' +
      '<td>' + dash(d.trafficProcessing) + '</td>' +
    '</tr>';
  }

  function renderTab(t: string): void {
    const tbody = el(TBODY[t] || '');
    if (!tbody) return;
    const rows = rowsFor(t);

    if (!rows.length) {
      tbody.innerHTML = '<tr><td colspan="' + String(COLSPAN[t] || 0) + '" class="empty-state">' +
        (data ? 'Nothing configured here yet.' : 'Waiting for CAPsMAN data&hellip;') + '</td></tr>';
      return;
    }

    if (t === 'provisioning') {
      const canMove = !!writable['capsProvisioning'];
      const last = rows.length - 1;
      tbody.innerHTML = (rows as CapsProvisioningRule[])
        .map((p, i) => provRow(p, i, last, canMove)).join('');
      return;
    }

    const fn = t === 'configuration' ? (r: unknown) => configRow(r as CapsConfigProfile)
      : t === 'security' ? (r: unknown) => securityRow(r as CapsSecurityProfile)
        : t === 'channel' ? (r: unknown) => channelRow(r as CapsChannelProfile)
          : (r: unknown) => datapathRow(r as CapsDatapathProfile);
    tbody.innerHTML = rows.map(fn).join('');
  }

  function renderCard(): void {
    Object.keys(TBODY).forEach(renderTab);
    const note = el('capsCfgNote');
    // A CAP is not a manager, and provisioning it locally does nothing. Say so
    // rather than showing five empty tables that look like a failure.
    if (note) {
      note.textContent = (data && data.role === 'cap')
        ? 'This router is a CAP — these are set on its manager.' : '';
    }
  }

  /** Point the Add slot at the table now on screen, and redraw its buttons. */
  function syncAddSlot(): void {
    const slot = el('capsAddSlot');
    if (!slot) return;
    slot.setAttribute('data-res-add', CAPS_RES[tab] || 'capsProvisioning');
    document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
  }

  function setTab(t: string): void {
    if (!CAPS_RES[t]) return;
    tab = t;
    bar?.querySelectorAll('.stab').forEach((b) => {
      const on = (b as HTMLElement).dataset.capstab === t;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    Object.keys(TBODY).forEach((k) => {
      const panel = el('capstab-' + k);
      if (panel) panel.hidden = (k !== t);
    });
    syncAddSlot();
    renderTab(t);
  }

  // ── Wiring ────────────────────────────────────────────────────────────────

  bar?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('[data-capstab]') as HTMLElement | null;
    if (btn) setTab(btn.dataset.capstab || '');
  });

  socket.on('capsman:update', (d: CapsmanPayload) => {
    // The two halves differ here and the difference is kept: the table ignores a
    // falsy update, the card treats it as "no data" and redraws its waiting
    // state. One variable serves both, so a falsy update clears it and the table
    // simply does not render — which is what the live table does with it too.
    data = d || null;
    if (!d) { renderCard(); return; }
    renderSummary();
    if (isVisible('capsman')) render();
    renderCard();
  });

  // Every engine event is filtered by the active tab, so an acknowledgement for
  // a tab you have left cannot redraw the one you are on.
  socket.on('res:schema', (d: { key?: string; permitted?: boolean }) => {
    const key = d && d.key;
    if (!key || !CAPS_RES[tab]) return;
    if (!Object.keys(CAPS_RES).some((t) => CAPS_RES[t] === key)) return;
    writable[key] = !!d.permitted;
    if (CAPS_RES[tab] === key) renderTab(tab);
  });

  socket.on('res:error', (d: { resource?: string }) => {
    if (d && CAPS_RES[tab] === d.resource) renderTab(tab);
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'capsman') return;
    if (data) render();
    syncAddSlot();
    renderCard();
  });

  socket.on('router:switched', () => {
    // The previous router's CAPs are not this one's. Every other page that can
    // be edited from drops its state here for the same reason; this one could
    // not be edited from before the configuration card existed.
    data = null;
    open = {};
    writable = {};
    renderSummary();
    render();
    renderCard();
  });

  el<HTMLInputElement>('capsmanSearch')?.addEventListener('input', debounce(render, 150));
}
