// The Packages page — a port of the Packages IIFE in public/app.js.
//
// The page that writes router configuration in a way no other page does.
// enable/disable/uninstall do not act — they SCHEDULE, undoable with
// unschedule, and inert until apply-changes reboots the router. The page is
// built around that: pending changes lead, every one has an Undo, and the reboot
// is a separate button.
//
// THE TYPED CONFIRMATION IS THE LIVE APP'S, NOT AN ADDITION. `window.prompt`
// asking for the router name back is what the live page does, and it is
// reproduced exactly — cancel behaviour included, since a null return is
// silent. It is what makes "the wrong router" a hard mistake rather than an easy
// one.

import { esc, el, renderSortHeader, sortMul, debounce, fmtBytes,
         type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';

export interface Package {
  id: string; name: string; version: string; buildTime: string;
  size: number | null; scheduled: string; scheduledAction: string;
  disabled: boolean; onServer: boolean; state: string;
}

export interface Firmware {
  isRouterboard: boolean; boardName: string; model: string; serial: string;
  firmwareType: string; currentFirmware: string; upgradeFirmware: string;
  minimumFirmware: string; upgradeAvailable: boolean;
}

export interface Update {
  channel: string; installedVersion: string; latestVersion: string;
  status: string; updateAvailable: boolean;
}

export interface PackagesPayload {
  ts: number; pollMs: number;
  packages: Package[]; firmware: Firmware; update: Update;
  counts: { total: number; installed: number; disabled: number; available: number; scheduled: number };
  pendingReboot: boolean; available: boolean;
}

export interface PackagesCaps { permitted: boolean; routerName: string }

const COLS: SortCol[] = [
  { key: 'name', label: 'Package' },
  { key: 'version', label: 'Version' },
  { key: 'size', label: 'Size' },
  { key: 'built', label: 'Built' },
  { key: 'state', label: 'State' },
  { key: 'actions', label: '' },
];

const STATE_RANK: Record<string, number> = {
  scheduled: 0, installed: 1, disabled: 2, available: 3, unknown: 4,
};

function stateCell(p: Package): string {
  if (p.scheduled) {
    return '<span class="wl-band wl-band-24" title="' + esc(p.scheduled) + '">' +
      esc(p.scheduledAction || 'scheduled') + ' pending</span>';
  }
  if (p.state === 'installed') return '<span class="wl-band wl-band-6">installed</span>';
  if (p.state === 'disabled') return '<span style="color:var(--text-muted)">disabled</span>';
  if (p.state === 'available') return '<span class="wl-band wl-band-5">available</span>';
  return '<span style="color:var(--text-muted)">unknown</span>';
}

export function initPackagesPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tbody = el('packagesTable');
  const theadRow = el('packagesThead');
  if (!tbody || !theadRow) return;

  let data: PackagesPayload | null = null;
  let caps: PackagesCaps = { permitted: false, routerName: '' };
  // Default to state, not name: what is scheduled matters most, then what is
  // actually on the router. Alphabetical order buries both under the packages
  // MikroTik merely offers.
  const sort: SortState = { col: 'state', dir: 'asc' };
  let busy = '';
  let statusTimer: ReturnType<typeof setTimeout> | null = null;

  // No global toast exists in app.js — the frequency analyzer uses a local
  // status line for the same reason, and so does this.
  function setStatus(text: string): void {
    const e = el('pkgStatus');
    if (!e) return;
    e.textContent = text || '';
    if (statusTimer) clearTimeout(statusTimer);
    if (text) statusTimer = setTimeout(() => { e.textContent = ''; }, 8000);
  }

  // The verbs offered depend on where the package currently is, so the page
  // never shows an action the router would refuse.
  function actionsFor(p: Package): string {
    if (!caps.permitted) return '';
    const dis = busy === p.name ? ' disabled' : '';
    const b = (act: string, label: string, cls?: string): string =>
      '<button class="pkg-act' + (cls ? ' ' + cls : '') + '" data-act="' + act +
      '" data-name="' + esc(p.name) + '"' + dis + '>' + label + '</button>';
    if (p.scheduled) return b('unschedule', 'Undo', 'undo');
    if (p.state === 'available') return b('enable', 'Install');
    if (p.state === 'disabled') return b('enable', 'Enable') + ' ' + b('uninstall', 'Uninstall');
    if (p.state === 'installed') return b('disable', 'Disable') + ' ' + b('uninstall', 'Uninstall');
    return '';
  }

  function render(): void {
    if (!data) return;
    const search = el<HTMLInputElement>('packagesSearch');
    const q = (search?.value || '').toLowerCase().trim();
    const all = data.packages || [];
    const rows = all.filter((p) => !q || p.name.toLowerCase().indexOf(q) !== -1);

    // The sort-key mapping is the live page's, quirks included: 'actions' sorts
    // by name, and 'state' returns -1 for anything scheduled so it leads
    // whatever its own state says.
    const f = (p: Package, k: string): string | number => {
      if (k === 'size') return p.size || 0;
      if (k === 'built') return p.buildTime || '';
      if (k === 'state') {
        if (p.scheduled) return -1;
        // Assigned first because TypeScript will not narrow an index signature
        // through the ternary the way the JavaScript original does. Same answer:
        // an unknown state sorts after every known one.
        const rank = STATE_RANK[p.state];
        return rank === undefined ? 9 : rank;
      }
      if (k === 'actions') return p.name.toLowerCase();
      return String((p as unknown as Record<string, unknown>)[k] ?? '').toLowerCase();
    };
    rows.sort((a, b) => {
      const av = f(a, sort.col), bv = f(b, sort.col);
      if (typeof av === 'string') return sortMul(sort) * av.localeCompare(bv as string);
      return sortMul(sort) * (av - (bv as number));
    });

    renderSortHeader('packagesThead', COLS, sort, () => render());

    const badge = el('packagesBadge');
    if (badge) {
      badge.textContent = String(all.length);
      badge.className = 'card-badge' + (all.length ? ' active-blue' : '');
    }
    const an = el('pkgActionNote');
    if (an) {
      an.textContent = caps.permitted
        ? '' : 'read-only — you do not have write access to this router';
    }

    tbody!.innerHTML = rows.length
      ? rows.map((p) =>
        '<tr>' +
        '<td>' + esc(p.name) + '</td>' +
        '<td>' + (p.version ? esc(p.version) : '<span style="color:var(--text-muted)">&mdash;</span>') + '</td>' +
        '<td>' + (p.size ? fmtBytes(p.size) : '<span style="color:var(--text-muted)">&mdash;</span>') + '</td>' +
        '<td style="color:var(--text-muted)">' + esc((p.buildTime || '').split(' ')[0] || '—') + '</td>' +
        '<td>' + stateCell(p) + '</td>' +
        '<td>' + actionsFor(p) + '</td>' +
        '</tr>').join('')
      : '<tr><td colspan="6" class="empty-state">' +
        (q ? 'No packages match that search.' : 'Waiting for package data…') + '</td></tr>';

    tbody!.querySelectorAll<HTMLButtonElement>('.pkg-act').forEach((btn) => {
      btn.addEventListener('click', () => {
        const act = btn.getAttribute('data-act') || '';
        const name = btn.getAttribute('data-name') || '';
        busy = name;
        render();
        socket.emit('packages:schedule', { action: act, name });
      });
    });

    renderPending();
    renderFirmware();
  }

  function renderPending(): void {
    const card = el('pkgPendingCard'), list = el('pkgPendingList');
    if (!card || !list || !data) return;
    const pending = (data.packages || []).filter((p) => p.scheduled);
    if (!pending.length) { card.style.display = 'none'; return; }
    card.style.display = '';
    list.innerHTML = pending
      .map((p) => esc(p.name) + ' — ' + esc(p.scheduledAction || 'change'))
      .join(' · ') + ' · nothing has happened yet; the router applies these on reboot';
    const btn = el<HTMLButtonElement>('pkgApplyBtn');
    if (btn) btn.disabled = !caps.permitted;
  }

  function renderFirmware(): void {
    const body = el('pkgFwBody');
    if (!body || !data) return;
    const f = data.firmware || ({} as Firmware);
    const u = data.update || ({} as Update);
    const kv = (k: string, v: string, cls?: string): string =>
      '<div class="kv-item"><div class="kv-key">' + esc(k) + '</div>' +
      '<div class="kv-val' + (cls ? ' ' + cls : '') + '">' + v + '</div></div>';

    let html = '';
    html += kv('RouterOS', esc(u.installedVersion || '—') +
      (u.updateAvailable ? ' → ' + esc(u.latestVersion) : ''), u.updateAvailable ? 'warn' : 'on');
    html += kv('Channel', esc(u.channel || '—'));
    html += kv('Update status', esc(u.status || '—'), u.updateAvailable ? 'warn' : 'off');
    if (f.isRouterboard) {
      html += kv('Firmware', esc(f.currentFirmware || '—') +
        (f.upgradeAvailable ? ' → ' + esc(f.upgradeFirmware) : ''), f.upgradeAvailable ? 'warn' : 'on');
      html += kv('Minimum firmware', esc(f.minimumFirmware || '—'));
      html += kv('Board', esc(f.boardName || '—') + (f.model ? ' (' + esc(f.model) + ')' : ''));
    }
    body.innerHTML = html;
  }

  function renderSummary(): void {
    if (!data) return;
    const c = data.counts || ({} as PackagesPayload['counts']);
    const set = (id: string, v: string): void => { const e = el(id); if (e) e.textContent = v; };
    set('pkgSumInstalled', c.installed === undefined ? '—' : String(c.installed));
    set('pkgSumAvailable', c.available === undefined ? '—' : String(c.available));
    set('pkgSumDisabled', c.disabled === undefined ? '—' : String(c.disabled));
    set('pkgSumUpdate', data.update && data.update.updateAvailable
      ? (data.update.latestVersion || 'update')
      : (data.update && data.update.installedVersion) || '—');
  }

  socket.on('packages:update', (d: PackagesPayload) => {
    if (!d) return;
    data = d;
    busy = '';
    renderSummary();
    if (isVisible('packages')) render();
  });

  socket.on('packages:caps', (d: PackagesCaps) => {
    if (!d) return;
    caps = d;
    if (isVisible('packages')) render();
  });

  socket.on('packages:ok', (d: { action?: string }) => {
    busy = '';
    if (d && d.action === 'apply') setStatus('Applying changes — the router is rebooting');
    else if (d && d.action === 'check') setStatus('Update check finished');
  });

  socket.on('packages:error', (d: { code?: string; message?: string }) => {
    busy = '';
    const msg: Record<string, string> = {
      denied: 'You do not have write access to this router',
      unavailable: 'Package collection is not running for this router',
      'bad-request': 'Invalid request',
      'no-such-package': 'That package is no longer listed',
      'router-write-policy': 'The RouterOS user needs write permission for this',
      unsupported: 'This router does not support that command',
      'confirm-mismatch': 'The router name did not match — nothing was applied',
      'nothing-scheduled': 'There are no scheduled changes to apply',
    };
    setStatus((d && d.code && msg[d.code]) || (d && d.message) || 'Action failed');
    if (isVisible('packages')) render();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'packages') return;
    // Permission is a property of this socket, not of the shared payload, so it
    // is asked for on entry rather than carried in packages:update.
    socket.emit('packages:caps', {});
    if (data) render();
  });

  const se = el<HTMLInputElement>('packagesSearch');
  if (se) se.addEventListener('input', debounce(render, 150));

  const chk = el('pkgCheckBtn');
  if (chk) {
    chk.addEventListener('click', () => {
      if (!caps.permitted) { setStatus('You do not have write access to this router'); return; }
      setStatus('Checking for updates…');
      // NO PAYLOAD, matching the live page (`../MikroDash/public/app.js:13027`).
      // The Go handler ignores `in.Data` for this event, so `{}` did nothing —
      // but an argument the original does not send is still a difference on the
      // wire, and `packages-page-check` compares the emit trail now.
      socket.emit('packages:check');
    });
  }

  const apply = el('pkgApplyBtn');
  if (apply) {
    apply.addEventListener('click', () => {
      if (!caps.permitted) return;
      const name = caps.routerName || '';
      // Typed confirmation, not an "are you sure": this reboots a production
      // router, and the name is what makes "the wrong router" a hard mistake to
      // make rather than an easy one.
      const typed = window.prompt(
        'This applies all scheduled package changes and REBOOTS the router.\n\n' +
        'Type the router name to confirm: ' + name);
      if (typed === null) return;
      socket.emit('packages:apply', { confirm: typed });
    });
  }
}
