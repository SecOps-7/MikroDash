// The share link's shell: /s/<token> (internal/server/share_view.go).
//
// ── NOT THE APP ─────────────────────────────────────────────────────────────
//
// A separate document and bundle, like login.ts. It has no session and asks
// for none: no /api/auth, no caps, no router list, no nav. It mounts the same
// page modules the app does, against a socket to /share/ws whose server side
// fixes the router and page from the link, so nothing this file sends can
// widen what is shown. Read-only by construction on the server; this file only
// hides the controls that would be refused anyway.
//
// ── THE TICKET, NOT A COOKIE ────────────────────────────────────────────────
//
// The view ticket is inlined in the page and kept in memory. It rides the
// socket handshake as a subprotocol and is renewed a minute before it lapses.
// A renewal refused means the link ended: the shell reloads /s/<token>, which
// re-runs every check and shows the one refusal page. Every fetch from this
// document is sent WITHOUT credentials, so a browser that is also signed in
// to MikroDash lends this page nothing of that account.

import { Socket } from '../socket';
import { shareFetch } from '../share-fetch';
import { el } from '../dom';
import { pageTitle } from '../gen/pages';
import { DEFAULT_LAYOUT, type GridCard } from '../gen/grid-tables';
import { applyCaps } from '../caps';
import {
  type ShareConfig, type ShareSlide, nextSlide, prevSlide, slideSecs, themeFor, transitionMs,
  onByDefault, expiryNote, renewInMs, sharedDashboardLayout,
} from '../share-logic';
import { initDashboard } from '../pages/dashboard';
import { initDashboardGrid } from '../pages/dashboard-grid';
import { initIpTip } from '../iptip';
import { initDnsPage } from '../pages/dns';
import { initBridgesPage } from '../pages/bridges';
import { initVlansPage } from '../pages/vlans';
import { initWanPage } from '../pages/wan';
import { initPackagesPage } from '../pages/packages';
import { initRoutingPage } from '../pages/routing';
import { initNetwatchPage } from '../pages/netwatch';
import { initDhcpPage } from '../pages/dhcp';
import { initPppPage } from '../pages/ppp';
import { initVpnPage } from '../pages/vpn';
import { initQueuesPage } from '../pages/queues';
import { initFirewallPage } from '../pages/firewall';
import { initWifiPage } from '../pages/wifi';
import { initCapsmanPage } from '../pages/capsman';
import { initInterfacesPage } from '../pages/interfaces';
import { initAreaPages } from '../pages/area';
import { initWireguardPeers } from '../pages/wireguard-peers';
import { initLogsPage } from '../pages/logs';
import { initTopologyPage } from '../pages/topology';
import { initWirelessPage } from '../pages/wireless';
import { initWifiMapPage } from '../pages/wifi-map';
import { initConnectionsPage } from '../pages/connections';
import { initBandwidthPage } from '../pages/bandwidth';
import { initAppsPage } from '../pages/apps';
import { initSecurityScanPage } from '../pages/security-scan';
import { applyView, mountRouters } from '../pages/routers';
import { initRoutersMap } from '../pages/routers-map';

const INVALID = 'This shared link is no longer valid. Ask whoever shared it for a new one.';

function readConfig(): ShareConfig | null {
  const node = document.getElementById('share-config');
  if (!node || !node.textContent) return null;
  try {
    return JSON.parse(node.textContent) as ShareConfig;
  } catch {
    return null;
  }
}

function setTheme(option: string | undefined): void {
  const mq = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;
  const apply = (): void => {
    const t = themeFor(option, mq ? mq.matches : true);
    document.documentElement.setAttribute('data-theme', t);
    document.documentElement.setAttribute('data-bs-theme', t);
  };
  apply();
  if (mq && option !== 'light' && option !== 'dark') mq.addEventListener('change', apply);
}

function notice(text: string): void {
  const p = el('shareNoticeText');
  if (p) p.textContent = text;
  const box = el('shareNotice');
  if (box) box.hidden = false;
}

function clearNotice(): void {
  const box = el('shareNotice');
  if (box) box.hidden = true;
}

function layoutOf(raw: unknown): GridCard[] | null {
  if (Array.isArray(raw)) return raw as GridCard[];
  if (raw && typeof raw === 'object' && Array.isArray((raw as { cards?: unknown }).cards)) {
    return (raw as { cards: GridCard[] }).cards;
  }
  return null;
}

function boot(): void {
  const cfg = readConfig();
  if (!cfg) {
    notice(INVALID);
    return;
  }
  let ticket = cfg.ticket;
  window.fetch = shareFetch(window.fetch.bind(window), cfg.reads || [], () => ticket, location.origin);
  setTheme(cfg.options.theme);
  const root = document.documentElement;
  root.classList.add('share-view');
  if (cfg.options.view === 'embed') root.classList.add('share-embed');
  if (cfg.kind === 'card') root.classList.add('share-card');
  // Only a slideshow has a strip (and only with its Title bar option on): a
  // page or card link is the page or card alone (the operator, 2026-11-20).
  if (cfg.kind !== 'slideshow') el('shareStrip')?.classList.add('share-strip-hidden');
  // Every capability off: the controls a share must not offer are hidden the
  // way a read-only account's are. The server would refuse them anyway.
  applyCaps({ pages: {}, manageSettings: false, managePrincipals: false, createRouters: false });

  const title = el('shareTitle');
  const router = el('shareRouter');
  const expiry = el('shareExpiry');
  if (expiry) expiry.textContent = expiryNote(cfg.expiresAt);

  if (cfg.full) {
    notice('This shared view has as many viewers as it allows. Try again later.');
    return;
  }

  const renew = (exp: number): void => {
    window.setTimeout(() => {
      void fetch('/share/api/ticket', { method: 'POST', headers: { Authorization: 'Share ' + ticket } })
        .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
        .then((d: { ticket?: string; ticketExp?: number }) => {
          if (!d.ticket || !d.ticketExp) throw new Error('no ticket');
          ticket = d.ticket;
          renew(d.ticketExp);
        })
        .catch((status) => {
          // 429 is a burst, not an ending: try again shortly. Anything else
          // means the link no longer stands; /s/<token> says so properly.
          if (status === 429) renew(Date.now() + 90_000);
          else location.reload();
        });
    }, renewInMs(exp, Date.now()));
  };
  renew(cfg.ticketExp);

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const socket = new Socket(proto + '//' + location.host + '/share/ws', () => ['mikrodash-share', ticket]);

  let current = '';
  const pageVisible = (name: string): boolean => current === name && !document.hidden;

  // The page modules, as the app mounts them. Each renders only while its
  // page is the visible one, so mounting all of them costs nothing on screen.
  const fixed = cfg.kind === 'slideshow' ? null : layoutOf(cfg.layout);
  // A card link: every card in the catalogue, only the shared one visible.
  // mergeLayout fills a card missing from a layout with its default, which is
  // visible, so a one-entry layout would draw the whole Dashboard.
  const cardLayout = cfg.kind === 'card' && cfg.options.card
    ? DEFAULT_LAYOUT.map((c) => (c.id === cfg.options.card
      ? { ...c, x: 1, y: 1, w: 24, visible: true } : { ...c, visible: false }))
    : null;
  // Each page module is mounted only when its page's markup is in this
  // document, which holds the SHAREABLE pages only (cmd/webbuild): un-sharing
  // a page removes its markup, and its module must not run against nothing.
  // Sharing a page again needs no change here.
  const mount = (page: string, init: () => void): void => {
    if (!document.getElementById('page-' + page)) return;
    try { init(); } catch (e) { console.error('[share] mounting', page, e); }
  };
  mount('dashboard', () => initDashboard(socket));
  initIpTip();
  mount('dashboard', () => initDashboardGrid(cardLayout || sharedDashboardLayout(DEFAULT_LAYOUT, fixed, cfg.cards || [])));
  mount('dns', () => initDnsPage(socket, pageVisible));
  mount('bridges', () => initBridgesPage(socket, pageVisible));
  mount('vlans', () => initVlansPage(socket, pageVisible));
  mount('wan', () => initWanPage(socket, pageVisible));
  mount('packages', () => initPackagesPage(socket, pageVisible));
  mount('routing', () => initRoutingPage(socket, pageVisible));
  mount('netwatch', () => initNetwatchPage(socket, pageVisible));
  mount('dhcp', () => initDhcpPage(socket, pageVisible));
  mount('ppp', () => initPppPage(socket, pageVisible));
  mount('vpn', () => initVpnPage(socket, pageVisible));
  mount('queues', () => initQueuesPage(socket, pageVisible));
  mount('firewall', () => initFirewallPage(socket, pageVisible));
  mount('wifi-networks', () => initWifiPage(socket, pageVisible));
  mount('capsman', () => initCapsmanPage(socket, pageVisible));
  mount('interfaces', () => initInterfacesPage(socket, pageVisible));
  mount('wireguard', () => initWireguardPeers(socket));
  // The generated pages: initAreaPages finds its own markup per area.
  try { initAreaPages(socket, pageVisible); } catch (e) { console.error('[share] mounting the generated pages', e); }
  mount('logs', () => initLogsPage(socket, pageVisible));
  mount('network-topology', () => initTopologyPage(socket, pageVisible));
  mount('wifi-clients', () => initWirelessPage(socket, pageVisible));
  mount('wifi-map', () => initWifiMapPage(socket, pageVisible));
  mount('connections', () => initConnectionsPage(socket, pageVisible));
  mount('bandwidth', () => initBandwidthPage(socket, pageVisible));
  mount('apps', () => initAppsPage(socket, pageVisible));
  mount('security-scan', () => initSecurityScanPage(socket, pageVisible));
  // EVERY DEVICE, in the view the link sets (the operator, 2026-11-23). The
  // switcher and the site filter are hidden: the view is the link's choice, and
  // a share is sent no sites. Applied after mountRouters, which first restores
  // this browser's own choice from storage.
  mount('devices', () => {
    mountRouters(socket);
    initRoutersMap();
    applyView(cfg.options.devicesView || 'comfortable');
    for (const id of ['routersView', 'routersSiteFilter']) {
      const n = el(id);
      if (n) n.hidden = true;
    }
  });

  const reduced = !!window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  const fadeMs = transitionMs(cfg.options, reduced);
  const stage = el('shareStage');
  if (stage && fadeMs > 0) {
    stage.style.setProperty('--share-fade', fadeMs + 'ms');
    stage.classList.add(cfg.options.transition === 'slide' ? 'share-anim-slide' : 'share-anim-fade');
  }

  /** Puts one page on screen, the way the app's showPage does. */
  const showPage = (name: string, routerLabel: string): void => {
    current = name;
    document.querySelectorAll('.page-view').forEach((p) => p.classList.remove('active'));
    el('page-' + name)?.classList.add('active');
    if (title) title.textContent = cfg.kind === 'card' ? cfg.label : pageTitle(name);
    if (router) router.textContent = routerLabel;
    document.dispatchEvent(new CustomEvent('mikrodash:pagechange', { detail: name }));
    socket.emit('page:focus', name);
  };

  socket.on('share:end', () => {
    socket.close();
    notice(INVALID);
  });
  socket.on('share:reload', () => { location.reload(); });

  // Shown once the socket is open: a page's REST reads are answered only
  // for an open view (share_read.go).
  if (cfg.kind !== 'slideshow') {
    socket.on('connect', () => {
      if (current) socket.emit('page:focus', current);
      else showPage(cfg.kind === 'card' ? 'dashboard' : cfg.page, cfg.router);
    });
    return;
  }

  // ── The slideshow ──────────────────────────────────────────────────────
  const o = cfg.options;
  const slides: ShareSlide[] = cfg.slides;
  const skip = new Set<number>();
  slides.forEach((s, i) => { if (!s.shareable) skip.add(i); });
  if (!onByDefault(o.titleBar)) el('shareStrip')?.classList.add('share-strip-hidden');
  const progress = el('shareProgress');
  const bar = el('shareProgressBar');
  const count = el('shareCount');
  const paused = el('sharePaused');
  if (onByDefault(o.progress)) {
    if (progress) progress.hidden = false;
    if (count) count.hidden = false;
  }
  let idx = -1;
  let timer: number | undefined;
  let endsAt = 0;
  let remaining = 0;
  let held = false;
  let resumeTimer: number | undefined;
  let stopped = false;
  const offlineUntil = new Map<number, number>();

  window.setInterval(() => {
    const cur = slides[idx];
    if (!bar || !cur) return;
    const total = slideSecs(cur, o) * 1000;
    const left = held ? remaining : Math.max(0, endsAt - Date.now());
    bar.style.width = (100 - (left / total) * 100).toFixed(2) + '%';
  }, 250);

  const skipped = (): Set<number> => {
    const now = Date.now();
    const s = new Set(skip);
    offlineUntil.forEach((until, i) => { if (until > now) s.add(i); });
    return s;
  };

  let warmTimer: number | undefined;
  const schedule = (ms: number): void => {
    if (timer !== undefined) window.clearTimeout(timer);
    if (warmTimer !== undefined) window.clearTimeout(warmTimer);
    endsAt = Date.now() + ms;
    timer = window.setTimeout(advance, ms);
    // Warm-up: the server starts the next slide's data this many seconds early.
    const warm = (o.warmUpSecs || 0) * 1000;
    if (warm > 0 && ms > warm) {
      warmTimer = window.setTimeout(() => {
        const n = nextSlide(idx, slides, skipped(), o.atEnd);
        if (n >= 0) socket.emit('share:warm', n);
      }, ms - warm);
    }
  };

  const go = (i: number): void => {
    if (i < 0) {
      if (skipped().size >= slides.length) {
        notice(skip.size >= slides.length ? INVALID : 'No routers in this slideshow are reachable.');
        window.setTimeout(() => { offlineUntil.clear(); go(nextSlide(-1, slides, skip)); }, 30_000);
        return;
      }
      // "Stop" at the end: the last slide holds, live.
      stopped = true;
      if (timer !== undefined) window.clearTimeout(timer);
      return;
    }
    const s = slides[i];
    if (!s) return;
    clearNotice();
    const swap = (): void => {
      idx = i;
      socket.emit('share:slide', i);
      showPage(s.page, s.router);
      if (count) count.textContent = (i + 1) + ' / ' + slides.length;
      schedule(slideSecs(s, o) * 1000);
      stage?.classList.remove('share-leaving');
    };
    if (fadeMs > 0 && idx >= 0) {
      stage?.classList.add('share-leaving');
      window.setTimeout(swap, fadeMs);
    } else {
      swap();
    }
  };

  function advance(): void {
    if (held || stopped) return;
    go(nextSlide(idx, slides, skipped(), o.atEnd));
  }

  // A router reported offline while its slide is on screen.
  socket.on('router:status', (st) => {
    if (idx < 0 || st.connected || st.online) return;
    if (o.offline === 'notice') {
      notice((slides[idx]?.router || 'This router') + ' is offline');
      schedule(10_000);
    } else {
      offlineUntil.set(idx, Date.now() + 30_000);
      advance();
    }
  });

  if (onByDefault(o.pauseOnHover) && stage) {
    const hold = (): void => {
      if (stopped || idx < 0) return;
      if (!held) {
        held = true;
        remaining = Math.max(0, endsAt - Date.now());
        if (timer !== undefined) window.clearTimeout(timer);
        if (paused) paused.hidden = false;
      }
      if (resumeTimer !== undefined) window.clearTimeout(resumeTimer);
      resumeTimer = window.setTimeout(() => {
        held = false;
        if (paused) paused.hidden = true;
        schedule(remaining);
      }, 10_000);
    };
    stage.addEventListener('pointermove', hold);
    stage.addEventListener('pointerdown', hold);
  }

  if (o.controls) {
    const prev = el('sharePrev');
    const next = el('shareNext');
    if (prev) prev.hidden = false;
    if (next) next.hidden = false;
    const step = (dir: number): void => {
      stopped = false;
      held = false;
      if (paused) paused.hidden = true;
      go(dir > 0 ? nextSlide(idx, slides, skipped()) : prevSlide(idx, slides, skipped()));
    };
    prev?.addEventListener('click', () => step(-1));
    next?.addEventListener('click', () => step(1));
    document.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowLeft') step(-1);
      if (e.key === 'ArrowRight') step(1);
    });
  }

  // On every (re)connect the server is told which slide is on screen; the
  // first connect starts the show.
  socket.on('connect', () => {
    if (idx >= 0) socket.emit('share:slide', idx);
    else go(nextSlide(-1, slides, skip));
  });
}

boot();
