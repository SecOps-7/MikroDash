// The PPP page — a port of the PPP IIFE in public/app.js (issue #32).
//
// Rates are derived SERVER-SIDE from byte deltas, because RouterOS reports
// cumulative bytes only. A null rate means "no measurement window yet", not
// idle, and this page renders that as an em dash rather than as zero — the
// distinction is the whole reason the collector makes the field nullable.
//
// The markup strings, class names and em dashes are the live app's. The
// acceptance criterion is that it renders identically, not that it renders
// correctly.

import { esc, el, resRow, debounce, renderSortHeader, sortMul, fmtMbps, fmtBytes, parseUptime, type SortCol, type SortState, mutedDash } from '../dom';
import { t } from '../i18n';
import type { Socket } from '../socket';
import { mountAdds, mountRows } from '../resource';
/*
 * PPPSecret is one /ppp/secret row — an ACCOUNT, not a session.
 *
 * THERE IS NO PASSWORD FIELD, AND THERE MUST NEVER BE ONE. The collector's
 * proplist does not ask the router for it, so nothing on this side could carry
 * one; the edit form writes a password through the resource engine's `secret`
 * field type, which travels to the router and never back.
 *
 * `connected` is joined server-side against /ppp/active by name — it is not a
 * property of the account.
 */
import type { PPPSession, PPPSecret, PPPPayload } from '../gen/payloads';

/**
 * Which resource each tab's Add button means, and which panel it shows.
 *
 * `servers` maps to the EMPTY STRING deliberately: `mountAddSlots` renders no
 * button for a slot naming nothing, which is exactly right for a table this page
 * does not let you edit. Giving it a resource would offer an Add that the write
 * path would then have to refuse.
 */
const PPP_TAB_RES: Record<string, string> = {
  secrets: 'pppSecret',
  profiles: 'pppProfile',
  servers: '',
};

const COLS_SECRET: SortCol[] = [
  { key: 'state', label: t('State') },
  { key: 'name', label: t('User') },
  { key: 'service', label: t('Service') },
  { key: 'profile', label: t('Profile') },
  { key: 'localAddress', label: t('Local') },
  { key: 'remoteAddress', label: t('Remote') },
  { key: 'comment', label: t('Comment') },
];

const COLS: SortCol[] = [
  { key: 'name', label: t('User') },
  { key: 'service', label: t('Service') },
  { key: 'address', label: t('Address') },
  { key: 'callerId', label: t('Caller ID') },
  { key: 'uptime', label: t('Uptime') },
  { key: 'rate', label: 'RX / TX' },
  { key: 'total', label: t('Total In / Out') },
];

export function initPppPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tbodyEl = el('pppTable');
  const theadRow = el('pppThead');
  // Bails on a page that is not in the document, exactly as the live IIFE does.
  if (!tbodyEl || !theadRow) return;
  // Re-bound so the narrowing survives into the closures below; `render` is
  // called from four places and none of them can re-check.
  const tbody: HTMLElement = tbodyEl;

  let data: PPPPayload | null = null;
  const sort: SortState = { col: 'name', dir: 'asc' };
  // The secrets table sorts independently of the sessions table above it: they
  // are different lists answering different questions, and one shared SortState
  // would have a click on either header reorder both.
  const secretSort: SortState = { col: 'name', dir: 'asc' };
  let cfgTab = 'secrets';

  /**
   * The sort key for one column.
   *
   * UPTIME SORTS ON A FORMATTED STRING, and that is faithful rather than sloppy:
   * `parseUptime` returns "1w 2d 3h", the live `sortVal` returns it unchanged,
   * and the comparator below then takes the string branch. So the column orders
   * lexicographically and puts "10m" before "2h". Reported upstream; reproduced
   * here because the ordering is on screen.
   */
  function sortVal(s: PPPSession, key: string): string | number {
    if (key === 'rate') return (s.rxRate || 0) + (s.txRate || 0);
    if (key === 'total') return s.rx + s.tx;
    if (key === 'uptime') return parseUptime(s.uptime);
    return String((s as unknown as Record<string, unknown>)[key] || '').toLowerCase();
  }

  /**
   * The sort key for one secrets column.
   *
   * `state` orders by what the pill says rather than by any single field:
   * online, then offline, then disabled. Sorting on `disabled` alone would put
   * a connected account and an idle one in the same bucket, which is the
   * distinction the column exists to draw.
   */
  function secretSortVal(s: PPPSecret, key: string): string | number {
    if (key === 'state') return s.disabled ? 2 : s.connected ? 0 : 1;
    return String((s as unknown as Record<string, unknown>)[key] || '').toLowerCase();
  }

  function render(): void {
    if (!data) return;
    const search = el<HTMLInputElement>('pppSearch');
    const q = ((search && search.value) || '').toLowerCase().trim();

    const rows = data.sessions.filter((s) => {
      if (!q) return true;
      return (s.name + ' ' + s.address + ' ' + s.callerId).toLowerCase().indexOf(q) !== -1;
    }).slice().sort((a, b) => {
      const av = sortVal(a, sort.col);
      const bv = sortVal(b, sort.col);
      if (typeof av === 'string') return sortMul(sort) * av.localeCompare(bv as string);
      return sortMul(sort) * ((av as number) - (bv as number));
    });

    renderSortHeader('pppThead', COLS, sort, () => render());

    const badge = el('pppBadge');
    if (badge) {
      badge.textContent = String(data.sessions.length);
      badge.className = 'card-badge' + (data.sessions.length ? ' active-blue' : '');
    }

    // Two different empty states, and the difference is the point: a router with
    // no PPP service is not the same as a router whose sessions have all gone.
    const empty = data.available
      ? t('No active PPP sessions. They appear here when a PPPoE, L2TP, SSTP or PPTP client connects.')
      : t('This router has no PPP service configured.');

    tbody.innerHTML = rows.length ? rows.map((s) => {
      const r = s.rxRate === null
        ? '<span style="color:var(--text-muted)" title="No measurement window yet">&mdash;</span>'
        : '<span style="color:var(--accent-rx)">' + fmtMbps((s.rxRate * 8) / 1e6) + '</span> / ' +
          '<span style="color:var(--accent-tx,#f59f00)">' + fmtMbps(((s.txRate as number) * 8) / 1e6) + '</span>';
      return '<tr>' +
        '<td>' + esc(s.name) + '</td>' +
        '<td><span class="vpn-proto-pill">' + esc(s.service || 'PPP') + '</span></td>' +
        '<td>' + esc(s.address) + '</td>' +
        '<td>' + esc(s.callerId) + '</td>' +
        '<td>' + esc(s.uptime) + '</td>' +
        '<td>' + r + '</td>' +
        '<td>' + fmtBytes(s.rx) + ' / ' + fmtBytes(s.tx) + '</td>' +
      '</tr>';
    }).join('') : '<tr><td colspan="7" class="empty-state">' + esc(empty) + '</td></tr>';

    renderConfig();
  }

  /**
   * The subscriber accounts (issue #125).
   *
   * THREE STATES, NOT TWO. "disabled" and "offline" are different facts about an
   * account and an operator acts on them differently: a disabled account is one
   * somebody switched off, an offline one is simply not dialled in right now.
   * Collapsing them into a single "not online" pill would hide the only thing
   * the enable/disable action changes.
   */
  function renderSecrets(): void {
    const tb = el('pppSecretTable');
    if (!tb || !data) return;
    const search = el<HTMLInputElement>('pppSecretSearch');
    const q = ((search && search.value) || '').toLowerCase().trim();
    const all = data.secrets || [];

    const rows = all.filter((s) => {
      if (!q) return true;
      return (s.name + ' ' + s.profile + ' ' + s.localAddress + ' ' +
        s.remoteAddress + ' ' + s.callerId + ' ' + s.comment)
        .toLowerCase().indexOf(q) !== -1;
    }).slice().sort((a, b) => {
      const av = secretSortVal(a, secretSort.col);
      const bv = secretSortVal(b, secretSort.col);
      if (typeof av === 'string') return sortMul(secretSort) * av.localeCompare(bv as string);
      return sortMul(secretSort) * ((av as number) - (bv as number));
    });

    renderSortHeader('pppSecretThead', COLS_SECRET, secretSort, () => renderSecrets());

    const badge = el('pppSecretBadge');
    // THE UNFILTERED COUNT, like every other badge here: it says how many
    // accounts exist, not how many survived the search box.
    if (badge) {
      badge.textContent = String(all.length);
      badge.className = 'card-badge' + (all.length ? ' active-blue' : '');
    }

    const empty = data.available
      ? t('No PPP secrets. Add one to let a subscriber connect.')
      : t('This router has no PPP service configured.');

    tb.innerHTML = rows.length ? rows.map((s) => {
      const pill = s.disabled
        ? '<span class="lease-pill expired">disabled</span>'
        : s.connected
          ? '<span class="lease-pill bound">online</span>'
          : '<span class="lease-pill">offline</span>';
      const dash = mutedDash();
      const cell = (v: string): string => '<td>' + (v ? esc(v) : dash) + '</td>';
      return '<tr' + (s.disabled ? ' style="opacity:.55"' : '') + resRow(s.id, s.name) + '>' +
        '<td>' + pill + '</td>' +
        '<td>' + esc(s.name) + '</td>' +
        '<td><span class="vpn-proto-pill">' + esc(s.service || 'any') + '</span></td>' +
        cell(s.profile) + cell(s.localAddress) + cell(s.remoteAddress) + cell(s.comment) +
      '</tr>';
    }).join('') : '<tr><td colspan="7" class="empty-state">' +
      esc(q ? t('No secrets match that search.') : empty) + '</td></tr>';
  }

  function renderProfiles(): void {
    const tb = el('pppProfileTable');
    if (!tb || !data) return;
    const rows = data.profiles || [];
    const badge = el('pppProfileBadge');
    if (badge) {
      badge.textContent = String(rows.length);
      badge.className = 'card-badge' + (rows.length ? ' active-blue' : '');
    }
    const dash = mutedDash();
    const cell = (v: string): string => '<td>' + (v ? esc(v) : dash) + '</td>';
    tb.innerHTML = rows.length ? rows.map((p) =>
      '<tr' + resRow(p.id, p.name) + '>' +
      '<td>' + esc(p.name) + '</td>' +
      cell(p.localAddress) + cell(p.remoteAddress) + cell(p.rateLimit) + cell(p.encryption) +
      '</tr>').join('')
      : '<tr><td colspan="5" class="empty-state">' + t('No PPP profiles.') + '</td></tr>';
  }

  function renderServers(): void {
    const tb = el('pppServerTable');
    if (!tb || !data) return;
    const rows = data.servers || [];
    const dash = mutedDash();
    const cell = (v: string): string => '<td>' + (v ? esc(v) : dash) + '</td>';
    tb.innerHTML = rows.length ? rows.map((s) =>
      '<tr>' +
      '<td>' + esc(s.serviceName || '(unnamed)') + '</td>' +
      cell(s.interface) +
      cell(s.maxSessions ? 'max ' + s.maxSessions : '') +
      '<td>' + (s.disabled
        ? '<span class="lease-pill expired">disabled</span>'
        : '<span class="lease-pill bound">enabled</span>') + '</td>' +
      '</tr>').join('')
      : '<tr><td colspan="4" class="empty-state">' + t('No PPPoE servers.') + '</td></tr>';
  }

  function renderConfig(): void {
    renderSecrets();
    renderProfiles();
    renderServers();
  }

  function renderSummary(): void {
    if (!data) return;
    const count = el('pppSumCount');
    if (count) count.textContent = String(data.sessions.length);

    // Plain lexicographic sort, not localeCompare: `Object.keys(...).sort()` in
    // the original, which is the bare comparison.
    // `byService` is a Go map and may be null; `svc` is empty then.
    const byService = data.byService || {};
    const svc = Object.keys(byService).sort();
    const services = el('pppSumServices');
    if (services) {
      services.textContent = svc.length
        ? svc.map((k) => k + ' ' + byService[k]).join('  ') : '—';
    }

    const toMbps = (v: number | null): number | null => (v === null ? null : (v * 8) / 1e6);
    const rx = toMbps(data.totalRxRate);
    const tx = toMbps(data.totalTxRate);
    const rxEl = el('pppSumRx');
    const txEl = el('pppSumTx');
    // innerHTML, not textContent: the placeholder is an entity, and the live app
    // assigns it the same way.
    if (rxEl) rxEl.innerHTML = rx === null ? '&mdash;' : fmtMbps(rx);
    if (txEl) txEl.innerHTML = tx === null ? '&mdash;' : fmtMbps(tx);
  }

  socket.on('ppp:update', (d) => {
    if (!d) return;
    data = d;
    // The summary updates whether or not the page is showing; the table only
    // when it is. That asymmetry is the original's, and it is why arriving on
    // the page fires a render of its own, below.
    renderSummary();
    if (isVisible('ppp')) render();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'ppp' && data) render();
  });

  const se = el<HTMLInputElement>('pppSearch');
  se?.addEventListener('input', debounce(render, 150));

  // The secrets box filters only its own table, so it re-renders only that one.
  const sse = el<HTMLInputElement>('pppSecretSearch');
  sse?.addEventListener('input', debounce(renderSecrets, 150));

  /** Point the Add slot at the table now on screen, and redraw its buttons. */
  function syncAddSlot(): void {
    const slot = el('pppAddSlot');
    if (!slot) return;
    slot.setAttribute('data-res-add', PPP_TAB_RES[cfgTab] ?? 'pppSecret');
    // WITHOUT THIS THE BUTTON KEEPS THE PREVIOUS TAB'S RESOURCE. Three pages
    // announced this event and nothing listened, so pressing Add on the NAT
    // table opened the filter-rule form; `resource.ts` listens now and
    // `web/test/resmount-seam.test.ts` is what keeps it listening.
    document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
  }

  function setCfgTab(t: string): void {
    if (!(t in PPP_TAB_RES)) return;
    cfgTab = t;
    document.querySelectorAll<HTMLElement>('[data-ppptab]').forEach((b) => {
      const on = b.dataset.ppptab === t;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    Object.keys(PPP_TAB_RES).forEach((k) => {
      const panel = el('ppptab-' + k);
      if (panel) panel.hidden = k !== t;
    });
    // The search box belongs to the Secrets list and filters nothing else, so
    // it goes away rather than sitting there implying a filter that does not
    // happen. Secrets is the only tab that can run to hundreds of rows.
    const box = el('pppSecretSearch');
    if (box) box.hidden = t !== 'secrets';
    syncAddSlot();
  }

  el('pppCfgTabBar')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('[data-ppptab]') as HTMLElement | null;
    if (btn) setCfgTab(btn.dataset.ppptab || '');
  });

  // The row and the Add button both open the resource form. The row carries the
  // `.id` and the identity that resRow() wrote onto it, which is what lets the
  // server refuse a write against a row that has changed underneath.
  mountAdds(socket);
  mountRows(socket);
}
