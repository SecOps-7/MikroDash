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
import { t } from '../i18n';
import type { Socket } from '../socket';
import type {
  CapsClient, Cap, CapsProvisioning, CapsConfigProfile, CapsSecurityProfile,
  CapsChannelProfile, CapsDatapathProfile, CapsTotals, CapsmanPayload,
} from '../gen/payloads';

const COLS: SortCol[] = [
  { key: 'identity', label: t('Identity') },
  { key: 'board', label: t('Board') },
  { key: 'version', label: t('Version') },
  { key: 'serial', label: t('Serial') },
  { key: 'state', label: t('State') },
  { key: 'connected', label: t('Connected') },
  { key: 'radios', label: t('Radios') },
  { key: 'clients', label: t('Clients') },
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

/**
 * The mark on everything that came from `/caps-man` rather than from
 * `/interface/wifi/capsman`.
 *
 * ── WHY THE PILL AND NOT A SEPARATE TABLE ───────────────────────────────────
 *
 * A fleet is read by identity, not by which of the two managers happens to hold
 * each access point, and a manager running both would otherwise show two of
 * every card. What the mark has to carry is the one thing that genuinely
 * differs: a v1 row cannot be edited here, and the title says why so nobody
 * spends a minute clicking one.
 */
const V1_PILL = '<span class="badge bg-orange-lt" style="margin-left:.35rem;font-size:.6rem"' +
  ' title="Legacy CAPsMAN (/caps-man) — shown here, configured on the router">v1</span>';

/** The v1 pill, or nothing. */
function v1(on: boolean): string { return on ? V1_PILL : ''; }

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
    // BOTH TREES' WORD FOR "WORKING". `/interface/wifi/capsman/remote-cap` says
    // `ok`; `/caps-man/remote-cap` says `Run`. Matching only the first painted
    // every legacy CAP amber on a fleet that was entirely healthy.
    const ok = /^(ok|run)$/i.test((state || '').trim());
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
        ? t('This router has neither CAPsMAN menu — no manager and no CAP mode here.')
        : (st.role === 'cap'
          ? t('This router is a CAP, not a manager — it has no CAPs of its own.')
          : (q ? t('No CAPs match that search.')
            : t('No CAPs are connected to this manager.')));
      tbody.innerHTML = '<tr><td colspan="8" class="empty-state">' + msg + '</td></tr>';
    } else {
      tbody.innerHTML = rows.map((c) => {
        const isOpen = !!open[c.identity];
        // ONLY A ROW WITH CLIENTS OPENS, and only it looks like it does. What a
        // row expands to IS its client list, so a CAP with nobody on it has
        // nothing to show — but every row carried `cursor:pointer` and invited
        // the click anyway, which reads as a row that is broken rather than a
        // row that is empty. The caret was already the honest marker; the cursor
        // now agrees with it.
        const opens = c.clientCount > 0;
        const head = '<tr' + (opens ? ' class="cap-open"' : '') +
            ' data-cap="' + esc(c.identity) + '">' +
          '<td>' + (opens ? (isOpen ? '▾ ' : '▸ ') : '') + esc(c.identity) +
            v1(c.legacy) + '</td>' +
          '<td>' + esc(c.boardName) + '</td>' +
          '<td>' + esc(c.version) + '</td>' +
          '<td>' + esc(c.serial) + '</td>' +
          '<td>' + stateBadge(c.state) + '</td>' +
          // A dash, not a blank. The legacy tree reports no connected time at
          // all, and an empty cell in a column the row next to it fills reads as
          // a rendering failure.
          '<td>' + dash(c.connectedTime) + '</td>' +
          '<td>' + (c.radios.length
            ? c.radios.map((r) => '<span class="wl-band wl-band-5">' + esc(r.interface) + '</span>').join(' ')
            : '<span ' + MUTED + '>&mdash;</span>') + '</td>' +
          '<td>' + String(c.clientCount) + '</td>' +
        '</tr>';
        return head + (isOpen ? c.clients.map(clientRow).join('') : '');
      }).join('');
      // ONE CLASS SAYS THE WHOLE THING. `cap-open` is what the stylesheet gives
      // the pointer cursor and what this binds the handler to, so the two cannot
      // come to disagree about which rows are clickable. It replaced `cap-row`,
      // which every row carried and nothing styled.
      tbody.querySelectorAll('.cap-open').forEach((tr) => {
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
    const c = data.cap;
    if (data.role === 'cap' || (data.role === 'both' && c.currentIdentity)) {
      panel.style.display = '';
      body.innerHTML = '<div class="kv-grid">' +
        ('<div class="kv-item"><div class="kv-key">' + t('Managed by') + '</div><div class="kv-val on">') +
          esc(c.currentIdentity || t('discovering…')) + '</div></div>' +
        ('<div class="kv-item"><div class="kv-key">' + t('Manager address') + '</div><div class="kv-val">') +
          esc(c.currentAddress || '—') + '</div></div>' +
        ('<div class="kv-item"><div class="kv-key">' + t('Discovery interfaces') + '</div><div class="kv-val">') +
          esc((c.discoveryInterfaces || []).join(', ') || '—') + '</div></div>' +
        ('<div class="kv-item"><div class="kv-key">' + t('Certificate') + '</div><div class="kv-val">') +
          esc(c.certificate || '—') + '</div></div>' +
      '</div>';
    } else {
      panel.style.display = 'none';
    }
  }

  function renderSummary(): void {
    if (!data) return;
    const tot = data.totals;
    const modes: Record<string, string> = {
      manager: t('Manager'), cap: 'CAP', both: t('Manager + CAP'), none: t('Off'),
    };
    const set = (id: string, v: string) => { const e = el(id); if (e) e.textContent = v; };
    // WHICH MANAGERS ARE ON, not just that one is. A router running both trees
    // reads "Manager (v1 + v2)"; one running only the old one reads
    // "Manager (v1)", which is the answer that used to be "Off".
    const trees = data.legacyManager
      ? (data.manager.enabled ? ' (v1 + v2)' : ' (v1)')
      : '';
    set('capSumMode', data.available ? ((modes[data.role] || '—') + trees) : t('Unsupported'));
    set('capSumCaps', tot.caps === undefined ? '—' : String(tot.caps));
    set('capSumRadios', tot.radios === undefined ? '—' : String(tot.radios));
    set('capSumClients', tot.clients === undefined ? '—' : String(tot.clients));
  }

  // ── The configuration card ────────────────────────────────────────────────

  function dash(v: string): string {
    return v ? esc(v) : '<span class="muted-note">&mdash;</span>';
  }

  function yesNo(v: boolean): string {
    return v ? t('Yes') : '<span class="muted-note">' + t('No') + '</span>';
  }

  /** Rows for a tab, from the one payload the collector already sends. */
  function rowsFor(tab: string): unknown[] {
    if (!data) return [];
    if (tab === 'provisioning') return data.provisioning || [];
    const p = data.profiles as unknown as Record<string, unknown[]> | null;
    return (p && p[tab]) || [];
  }

  function provRow(p: CapsProvisioning, at: number, last: number, canMove: boolean): string {
    // data-res-move, not a name of this card's own: the engine owns the reorder
    // flow, including the guard prompt it can raise. Order is meaning here —
    // the first rule whose bands match a joining radio wins.
    const move = canMove
      ? ('<button class="fw-move" data-res-move="up" title="' + t('Move up') + '"') + (at === 0 ? ' disabled' : '') + '>&#9650;</button>' +
        ('<button class="fw-move" data-res-move="down" title="' + t('Move down') + '"') + (at === last ? ' disabled' : '') + '>&#9660;</button>'
      : '';
    // A LEGACY ROW CARRIES NO ID and therefore no `data-id`, which is what makes
    // it read-only without a branch here: the resource engine only opens a row
    // that has one. See mergeLegacy in internal/collect/capsman.go.
    // THE v1 MARK GOES IN THE HANDLE COLUMN, not in Bands. That column holds the
    // reorder arrows, which a v1 rule does not have, so it is empty on exactly
    // the rows the mark belongs to — and Bands stays one kind of thing instead of
    // a pill and a badge jammed against each other.
    return '<tr' + resRow(p.id, p.identity, 'capsProvisioning') +
             (p.disabled ? ' style="opacity:.5"' : '') + '>' +
      '<td class="fw-movecell">' + (p.legacy ? v1(true) : move) + '</td>' +
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
      '<td>' + esc(c.name) + v1(c.legacy) +
        // A profile carrying `manager` is the CAP-side setting MikroTik warns
        // must never be provisioned onward. Worth flagging where it appears.
        (c.manager ? '<span class="badge bg-yellow-lt" style="margin-left:.35rem">manager</span>' : '') + '</td>' +
      '<td>' + dash(c.ssid) +
        (c.hideSsid ? '<span class="badge bg-secondary-lt" style="margin-left:.35rem">' + t('Hidden') + '</span>' : '') + '</td>' +
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
      '<td>' + esc(s.name) + v1(s.legacy) + '</td>' +
      '<td><span class="badge ' + (isOpen ? 'bg-red-lt' : 'bg-azure-lt') + '">' +
        esc(isOpen ? t('Open') : s.authTypes) + '</span></td>' +
      '<td>' + dash(s.wps) + '</td>' +
      '<td>' + yesNo(s.ft) + '</td>' +
    '</tr>';
  }

  function channelRow(c: CapsChannelProfile): string {
    return '<tr' + resRow(c.id, c.name, 'capsChannel') + (c.disabled ? ' style="opacity:.5"' : '') + '>' +
      '<td>' + esc(c.name) + v1(c.legacy) + '</td>' +
      '<td>' + dash(c.band) + '</td>' +
      '<td>' + dash(c.frequency) + '</td>' +
      '<td>' + dash(c.width) + '</td>' +
      '<td>' + dash(c.skipDfsChannels) + '</td>' +
    '</tr>';
  }

  function datapathRow(d: CapsDatapathProfile): string {
    return '<tr' + resRow(d.id, d.name, 'capsDatapath') + (d.disabled ? ' style="opacity:.5"' : '') + '>' +
      '<td>' + esc(d.name) + v1(d.legacy) + '</td>' +
      '<td>' + dash(d.bridge) + '</td>' +
      '<td>' + dash(d.vlanId) + '</td>' +
      '<td>' + yesNo(d.clientIsolation) + '</td>' +
      '<td>' + dash(d.trafficProcessing) + '</td>' +
    '</tr>';
  }

  function renderTab(tab: string): void {
    const tbody = el(TBODY[tab] || '');
    if (!tbody) return;
    const rows = rowsFor(tab);

    if (!rows.length) {
      tbody.innerHTML = '<tr><td colspan="' + String(COLSPAN[tab] || 0) + '" class="empty-state">' +
        (data ? t('Nothing configured here yet.') : 'Waiting for CAPsMAN data&hellip;') + '</td></tr>';
      return;
    }

    if (tab === 'provisioning') {
      // THE MOVE BOUNDS ARE THE EDITABLE ROWS', not the table's. A v1 rule is
      // appended to the same table and cannot be reordered from here, so
      // counting it would disable the arrow on the last rule that can be.
      const canMove = !!writable['capsProvisioning'];
      const all = rows as CapsProvisioning[];
      const editable = all.filter((p) => !p.legacy);
      const last = editable.length - 1;
      tbody.innerHTML = all
        .map((p) => provRow(p, editable.indexOf(p), last, canMove && !p.legacy)).join('');
      return;
    }

    const fn = tab === 'configuration' ? (r: unknown) => configRow(r as CapsConfigProfile)
      : tab === 'security' ? (r: unknown) => securityRow(r as CapsSecurityProfile)
        : tab === 'channel' ? (r: unknown) => channelRow(r as CapsChannelProfile)
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
        ? t('This router is a CAP — these are set on its manager.')
        : (data && data.legacyManager
          ? t('Rows marked v1 belong to the legacy /caps-man tree and are read-only here.')
          : '');
    }
  }

  /** Point the Add slot at the table now on screen, and redraw its buttons. */
  function syncAddSlot(): void {
    const slot = el('capsAddSlot');
    if (!slot) return;
    slot.setAttribute('data-res-add', CAPS_RES[tab] || 'capsProvisioning');
    document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
  }

  function setTab(tab: string): void {
    if (!CAPS_RES[tab]) return;
    tab = tab;
    bar?.querySelectorAll('.stab').forEach((b) => {
      const on = (b as HTMLElement).dataset.capstab === tab;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    Object.keys(TBODY).forEach((k) => {
      const panel = el('capstab-' + k);
      if (panel) panel.hidden = (k !== tab);
    });
    syncAddSlot();
    renderTab(tab);
  }

  // ── Wiring ────────────────────────────────────────────────────────────────

  bar?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('[data-capstab]') as HTMLElement | null;
    if (btn) setTab(btn.dataset.capstab || '');
  });

  socket.on('capsman:update', (d) => {
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
  socket.on('res:schema', (d) => {
    const key = d && d.key;
    if (!key || !CAPS_RES[tab]) return;
    if (!Object.keys(CAPS_RES).some((tab) => CAPS_RES[tab] === key)) return;
    writable[key] = !!d.permitted;
    if (CAPS_RES[tab] === key) renderTab(tab);
  });

  socket.on('res:error', (d) => {
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
