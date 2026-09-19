// The Containers page's Apps tab (2026-09-19): RouterOS's app store, with one
// click to install. A hand-built panel on the generated Containers area,
// registered with area.ts (registerAreaPanel).
//
// ── WHAT HAPPENS WHEN ───────────────────────────────────────────────────────
//
// Showing the tab asks for the store (`apps:list`). Install, Start, Stop,
// Restart and Remove send `apps:do`; the server runs it and, for anything that
// brings an app up, follows it with `apps:progress` until it runs or fails, and
// the card shows each status as it comes. When a change is done the store is
// asked for again. Remove first opens a dialog that stays disarmed until the
// app's name is typed: it deletes the app's data. A store from the router just
// left is dropped.
//
// Who may change anything is the server's to say (`mayManage`: a global admin
// with write on Containers); without it the cards show what is installed and
// how to open it, and no buttons.

import { el, esc } from '../dom';
import type { Socket } from '../socket';
import type { AppsPayload } from '../gen/payloads';
import { registerAreaPanel } from './area';
import {
  appCard, drawer, matches, categories, categoryLabel, order, type Pending, type View,
} from './containers-apps-cards';

const REFUSED: Record<string, string> = {
  denied: 'You may not do that.',
  unavailable: 'No router is connected.',
  notfound: 'This router has no app of that name.',
  timeout: 'Still not running after 10 minutes; it may still be downloading.',
};

export function initContainersApps(socket: Socket): void {
  let host: HTMLElement | null = null;
  let store: AppsPayload | null = null;
  let routerId = '';
  const view: View = { q: '', cat: '', show: 'all' };
  const pending: Record<string, Pending> = {};
  let open = '';        // the app in the drawer
  let removing = '';    // the app in the remove dialog

  function layout(h: HTMLElement): void {
    h.innerHTML =
      '<div class="apps">' +
      '<div class="apps-hero" id="appsHero"></div>' +
      '<div class="apps-toolbar">' +
      '<div class="apps-search"><input type="search" id="appsQ" placeholder="Search apps…" autocomplete="off" spellcheck="false"></div>' +
      '<div class="apps-seg" id="appsSeg"></div>' +
      '</div>' +
      '<div class="apps-cats" id="appsCats"></div>' +
      '<div class="apps-grid" id="appsGrid"><div class="empty-state">Loading the app store…</div></div>' +
      '<aside class="apps-drawer" id="appsDrawer" hidden></aside>' +
      '<div class="apps-modal" id="appsModal" hidden></div>' +
      '</div>';
  }

  function hero(): string {
    const s = store;
    if (!s) return '';
    if (s.code) return '<div class="apps-notice is-bad">' + esc(REFUSED[s.code] || s.message || 'The store could not be read.') + '</div>';
    if (!s.supported) {
      return '<div class="apps-notice">' + esc(s.reason || 'This router has no app store.') + '</div>';
    }
    const installed = s.apps.filter((a) => a.state !== 'available').length;
    const running = s.apps.filter((a) => a.state === 'running').length;
    const stats = '<div class="apps-stats">' +
      '<div><b>' + s.apps.length + '</b><span>in the store</span></div>' +
      '<div><b>' + installed + '</b><span>installed</span></div>' +
      '<div><b>' + running + '</b><span>running</span></div></div>';
    if (s.ready) {
      return '<div class="apps-hero-card"><div class="apps-hero-text"><h4>App store</h4>' +
        '<p>One click installs an app: RouterOS pulls its images and sets up its network and firewall. ' +
        'Apps are stored on <b>' + esc(s.disk) + '</b>' + (s.lanBridge ? ', joined to <b>' + esc(s.lanBridge) + '</b>' : '') +
        '. ' + (s.httpsLinks ? 'Each app gets an HTTPS link through IP Cloud.'
          : 'Apps get plain HTTP links: HTTPS links need IP Cloud (IP › Cloud) to give this router a DNS name.') +
        '</p></div>' + stats + '</div>';
    }
    // NOT SET UP: the disk (and bridge) apps live on.
    const disks = s.disks.map((d) => '<option value="' + esc(d.slot) + '">' + esc(d.slot + ' · ' + d.fs) + '</option>').join('');
    const bridges = '<option value="">None (apps behind NAT only)</option>' +
      s.bridges.map((b) => '<option value="' + esc(b) + '"' + (b === s.lanBridge ? ' selected' : '') + '>' + esc(b) + '</option>').join('');
    const body = !s.mayManage
      ? '<p>A global administrator must choose where apps are stored before any can be installed.</p>'
      : !s.disks.length
        ? '<p>Apps need a formatted, mounted disk (ext4 or btrfs). Add one under System › Disks, then come back.</p>'
        : '<div class="apps-setup-form"><label>Store apps on<select id="appsDisk">' + disks + '</select></label>' +
          '<label>LAN bridge<select id="appsBridge">' + bridges + '</select></label>' +
          '<button type="button" class="apps-btn is-primary" data-apps-setup>Set up apps</button></div>';
    return '<div class="apps-hero-card is-setup"><div class="apps-hero-text"><h4>Set up the app store</h4>' +
      '<p>Choose where apps are stored. RouterOS then handles each app’s containers, network and firewall.</p>' +
      body + '</div>' + stats + '</div>';
  }

  function draw(): void {
    if (!host) return;
    if (!store) {
      // Waiting for the store: nothing of the last router's is left on screen.
      for (const id of ['appsHero', 'appsSeg', 'appsCats']) {
        const e = el(id);
        if (e) e.innerHTML = '';
      }
      const g = el('appsGrid');
      if (g) g.innerHTML = '<div class="empty-state">Loading the app store…</div>';
      drawDrawer();
      drawModal();
      return;
    }
    const heroEl = el('appsHero');
    if (heroEl) heroEl.innerHTML = hero();
    const ok = !store.code && store.supported;
    const seg = el('appsSeg');
    if (seg) {
      seg.innerHTML = ok ? (['all', 'installed', 'running'] as const).map((s) =>
        '<button type="button" data-show="' + s + '"' + (view.show === s ? ' class="active"' : '') + '>' +
        s.charAt(0).toUpperCase() + s.slice(1) + '</button>').join('') : '';
    }
    const cats = el('appsCats');
    if (cats) {
      cats.innerHTML = ok ? '<button type="button" class="apps-chip' + (view.cat === '' ? ' active' : '') + '" data-cat="">All <span>' +
        store.apps.length + '</span></button>' + categories(store.apps).map(([c, n]) =>
        '<button type="button" class="apps-chip' + (view.cat === c ? ' active' : '') + '" data-cat="' + esc(c) + '">' +
        esc(categoryLabel(c)) + ' <span>' + n + '</span></button>').join('') : '';
    }
    const grid = el('appsGrid');
    if (grid) {
      const may = store.mayManage && store.ready;
      const shown = ok ? order(store.apps).filter((a) => matches(a, view)) : [];
      grid.innerHTML = !ok ? '' : shown.length
        ? shown.map((a) => appCard(a, may, pending[a.name])).join('')
        : '<div class="empty-state">No app matches.</div>';
    }
    drawDrawer();
  }

  function drawDrawer(): void {
    const d = el('appsDrawer');
    if (!d) return;
    const a = open && store ? store.apps.find((x) => x.name === open) : undefined;
    if (!a || !store) {
      d.hidden = true;
      return;
    }
    d.innerHTML = drawer(a, store.mayManage && store.ready, pending[a.name]);
    d.hidden = false;
  }

  function drawModal(): void {
    const m = el('appsModal');
    if (!m) return;
    if (!removing) {
      m.hidden = true;
      return;
    }
    m.innerHTML = '<div class="apps-modal-card" role="dialog" aria-modal="true">' +
      '<h4>Remove ' + esc(removing) + '?</h4>' +
      '<p>This stops the app and <b>permanently deletes its data</b> and its image. It cannot be undone.</p>' +
      '<label>Type <code>' + esc(removing) + '</code> to confirm<input type="text" id="appsConfirm" autocomplete="off" spellcheck="false"></label>' +
      '<div class="apps-modal-actions"><button type="button" class="apps-btn" data-apps-cancel>Cancel</button>' +
      '<button type="button" class="apps-btn is-danger" id="appsRemove" data-apps-remove disabled>Remove app</button></div></div>';
    m.hidden = false;
    el<HTMLInputElement>('appsConfirm')?.focus?.();
  }

  function act(name: string, verb: string, confirm = ''): void {
    pending[name] = { verb, status: verb === 'install' ? 'Starting install…' : '', error: '' };
    draw();
    socket.emit('apps:do', { name, verb, confirm });
  }

  socket.on('apps:state', (d) => {
    if (routerId && d.routerId && d.routerId !== routerId) return;
    store = d;
    draw();
  });

  socket.on('apps:progress', (d) => {
    if (routerId && d.routerId && d.routerId !== routerId) return;
    if (!d.done) {
      pending[d.name] = { verb: d.verb, status: d.status || 'Working…', error: '' };
      draw();
      return;
    }
    if (d.code) {
      pending[d.name] = { verb: d.verb, status: '', error: REFUSED[d.code] || d.message || 'That did not work.' };
    } else {
      delete pending[d.name];
    }
    draw();
    socket.emit('apps:list', {});
  });

  socket.on('router:switched', (d) => {
    routerId = d.activeId;
    store = null;
    open = '';
    removing = '';
    for (const k of Object.keys(pending)) delete pending[k];
    draw();
    if (host && host.style.display !== 'none') socket.emit('apps:list', {});
  });

  function wire(h: HTMLElement): void {
    h.addEventListener('input', (e) => {
      const t = e.target as HTMLInputElement;
      if (t === el('appsQ')) {
        view.q = t.value;
        draw();
      } else if (t === el('appsConfirm')) {
        const btn = el<HTMLButtonElement>('appsRemove');
        if (btn) btn.disabled = t.value !== removing;
      }
    });
    h.addEventListener('click', (e) => {
      const t = e.target as HTMLElement;
      const doBtn = t.closest?.('[data-app-do]');
      if (doBtn) {
        const name = doBtn.getAttribute('data-app') || '';
        const verb = doBtn.getAttribute('data-app-do') || '';
        if (verb === 'remove') {
          removing = name;
          drawModal();
        } else {
          act(name, verb);
        }
        return;
      }
      if (t.closest?.('[data-apps-remove]')) {
        const typed = el<HTMLInputElement>('appsConfirm')?.value || '';
        if (typed === removing) {
          const name = removing;
          removing = '';
          drawModal();
          act(name, 'remove', typed);
        }
        return;
      }
      if (t.closest?.('[data-apps-cancel]') || t === el('appsModal')) {
        removing = '';
        drawModal();
        return;
      }
      const copy = t.closest?.('[data-app-copy]');
      if (copy) {
        navigator.clipboard?.writeText(copy.getAttribute('data-app-copy') || '').then(() => {
          copy.textContent = 'Copied';
          setTimeout(() => { copy.textContent = 'Copy'; }, 1500);
        }, () => {});
        return;
      }
      if (t.closest?.('[data-app-close]')) {
        open = '';
        drawDrawer();
        return;
      }
      const cat = t.closest?.('[data-cat]');
      if (cat) {
        view.cat = cat.getAttribute('data-cat') || '';
        draw();
        return;
      }
      const show = t.closest?.('[data-show]');
      if (show) {
        view.show = (show.getAttribute('data-show') || 'all') as View['show'];
        draw();
        return;
      }
      if (t.closest?.('[data-apps-setup]')) {
        const disk = el<HTMLSelectElement>('appsDisk')?.value || '';
        const lanBridge = el<HTMLSelectElement>('appsBridge')?.value || '';
        socket.emit('apps:setup', { disk, lanBridge });
        return;
      }
      if (t.closest?.('a')) return; // Open and the project page are ordinary links
      const card = t.closest?.('[data-app-card]');
      if (card) {
        open = card.getAttribute('data-app-card') || '';
        drawDrawer();
      }
    });
  }

  registerAreaPanel('containers', 'apps', {
    show(h: HTMLElement) {
      if (host !== h) {
        host = h;
        layout(h);
        wire(h);
      }
      draw();
      socket.emit('apps:list', {});
    },
  });
}
