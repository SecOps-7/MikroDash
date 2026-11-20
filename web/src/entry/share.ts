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
import { el } from '../dom';
import { pageTitle } from '../gen/pages';
import type { GridCard } from '../gen/grid-tables';
import { applyCaps } from '../caps';
import {
  type ShareConfig, type ShareSlide, nextSlide, prevSlide, slideSecs, themeFor, transitionMs,
  onByDefault, expiryNote, renewInMs,
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

/** Every fetch from this document: no credentials, and nothing but GET and the ticket renewal. */
function lockFetch(): void {
  const real = window.fetch.bind(window);
  window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const method = (init?.method || (input instanceof Request ? input.method : 'GET')).toUpperCase();
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const renewal = url.endsWith('/share/api/ticket') && method === 'POST';
    if (method !== 'GET' && !renewal) {
      return Promise.resolve(new Response(JSON.stringify({ ok: false, error: 'read-only' }),
        { status: 403, headers: { 'Content-Type': 'application/json' } }));
    }
    return real(input, { ...init, credentials: 'omit' });
  };
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
  lockFetch();
  setTheme(cfg.options.theme);
  const root = document.documentElement;
  root.classList.add('share-view');
  if (cfg.options.view === 'embed') root.classList.add('share-embed');
  if (cfg.kind === 'card') root.classList.add('share-card');
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

  let ticket = cfg.ticket;
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
  const cardLayout = cfg.kind === 'card' && cfg.options.card
    ? [{ id: cfg.options.card, x: 1, y: 1, w: 24, h: 8, visible: true } as GridCard]
    : null;
  initDashboard(socket);
  initIpTip();
  initDashboardGrid(cardLayout || fixed || []);
  initDnsPage(socket, pageVisible);
  initBridgesPage(socket, pageVisible);
  initVlansPage(socket, pageVisible);
  initWanPage(socket, pageVisible);
  initPackagesPage(socket, pageVisible);
  initRoutingPage(socket, pageVisible);
  initNetwatchPage(socket, pageVisible);
  initDhcpPage(socket, pageVisible);
  initPppPage(socket, pageVisible);
  initVpnPage(socket, pageVisible);
  initQueuesPage(socket, pageVisible);
  initFirewallPage(socket, pageVisible);
  initWifiPage(socket, pageVisible);
  initCapsmanPage(socket, pageVisible);
  initInterfacesPage(socket, pageVisible);
  initWireguardPeers(socket);
  initAreaPages(socket, pageVisible);
  initLogsPage(socket, pageVisible);
  initTopologyPage(socket, pageVisible);
  initWirelessPage(socket, pageVisible);
  initWifiMapPage(socket, pageVisible);
  initConnectionsPage(socket, pageVisible);
  initBandwidthPage(socket, pageVisible);
  initAppsPage(socket, pageVisible);
  initSecurityScanPage(socket, pageVisible);

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

  if (cfg.kind !== 'slideshow') {
    showPage(cfg.kind === 'card' ? 'dashboard' : cfg.page, cfg.router);
    socket.on('connect', () => { if (current) socket.emit('page:focus', current); });
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

  // On every (re)connect the server is told which slide is on screen.
  socket.on('connect', () => {
    if (idx >= 0) socket.emit('share:slide', idx);
  });
  go(nextSlide(-1, slides, skip));
}

boot();
