// Boot.
//
// The slice this file wires is deliberately narrow: one router, one page. What
// it is NOT is a second copy of the live app's navigation — that would be
// porting the shell before any page had proved the stack works, which is the
// order PLAN.md B4 exists to prevent ("Nothing else moves until that slice is
// green against live hardware").
//
// So every nav entry other than the ported one hands the browser back to the
// Node app. Both are reachable in the same session on the same host, which is
// what makes a side-by-side comparison of old page and new page possible at all.

import { el } from './dom';
import { installFetchGuard, verifySessionAfterFailure } from './fetch-guard';
import { overlayOnStatus, overlayOnSwitch, wireRouterDropdown } from './router-dropdown';
import { initUpgrade } from './pages/upgrade';
import { Socket } from './socket';
import { initAppearance, wireAppearance } from './appearance';
import { initNav, navAutoExpand } from './nav';
import { initKeyboard } from './keyboard';
import { initCaps, applyPageVisibility, settingsAllowed, refreshCaps, mayManagePrincipals } from './caps';
import { wireAccount } from './account';
import { setRosBanner, onSocketConnect, onSocketDisconnect, initClock } from './banners';
import { wireModals } from './modals';
import {
  clearDashboardData, resetStaleTimers, notePayload, startStaleSweep, cardsForEvent,
  applyCollectionConfig, applyCollectionStatus,
} from './stale';
import { STALE_CARDS } from './gen/stale-tables';
import { PAGE_KEY_SET, pageTitle } from './gen/pages';
import { initialPage, initRouting, sync, type NavMode } from './routing';
import { initDnsPage } from './pages/dns';
import { initBridgesPage } from './pages/bridges';
import { initVlansPage } from './pages/vlans';
import { initWanPage } from './pages/wan';
import { initPackagesPage } from './pages/packages';
import { initRoutingPage } from './pages/routing';
import { initDhcpPage } from './pages/dhcp';
import { initPppPage } from './pages/ppp';
import { initVpnPage } from './pages/vpn';
import { initRosUsersPage } from './pages/rosusers';
import { initQueuesPage } from './pages/queues';
import { initFirewallPage } from './pages/firewall';
import { initWifiPage } from './pages/wifi';
import { initCapsmanPage } from './pages/capsman';
import { initInterfacesPage } from './pages/interfaces';
import { initLogsPage } from './pages/logs';
import { initTopologyPage } from './pages/topology';
import { initWirelessPage } from './pages/wireless';
import { initNotifications } from './pages/notifications';
import { initConnectionsPage } from './pages/connections';
import { initBandwidthPage } from './pages/bandwidth';
import { initBackupsPage } from './pages/backups';
import { mountRouters, renderRoutersStats } from './pages/routers';
import type { StoredRouter } from './pages/router-form';
import { mountReports } from './pages/reports';
import { initAuditPage } from './pages/audit';
import { initSitesCard, onSitesUpdate, sitesById } from './pages/settings-sites';
import { initPrincipalsCard, refreshPrincipalsVisibility } from './pages/settings-principals';
import { initDbCleanup } from './pages/dbcleanup';
import { initRoutersMap } from './pages/routers-map';
import { initSetupOverlay, showSetupOverlayNow } from './pages/setup-overlay-wire';
import { initPollAndBanner, applyPollSettings } from './pages/settings-poll';
import { initSettingsRoutersTable, renderRoutersInto, updateRouterStatusBadge } from './pages/settings-routers';
import { initRouterModal } from './pages/router-modal';
import { initAlertFilters } from './pages/settings-alert-filters';
import { initNotifTestButtons } from './pages/settings-notif-test';
import { mountSettingsTabs, populateSettings } from './pages/settings';
import { initDashboard, resetSysMeta, resetConnCaches, resetTraffic, resetPing, resetRoutingCards, resetBandwidthCard, resetLogsCard } from './pages/dashboard';
import { initIpTip } from './iptip';
import { initDashboardGrid } from './pages/dashboard-grid';

// The pages this bundle can render, and their header text — both from
// `internal/pages` via cmd/pagesgen, so they cannot drift from the markup
// cmd/webbuild composes or the URLs internal/server registers.
//
// They were two hand-written literals here until 2026-09-01, which made this one
// of five places the same 26 keys were spelled out.
const PORTED = PAGE_KEY_SET;

let currentPage = '';

function pageVisible(name: string): boolean {
  return currentPage === name && !document.hidden;
}

function showPage(socket: Socket, name: string, mode: NavMode = 'push'): void {
  // Hiding the nav link was never a block — showPage('settings') from the
  // console opened the whole admin page for anyone. The server refused every
  // write, but the page had no business rendering. Defence in depth, not the
  // boundary. Unknown caps PERMIT; see caps.ts.
  if (name === 'settings' && !settingsAllowed()) name = 'dashboard';
  const prev = currentPage;
  currentPage = name;
  document.querySelectorAll('.page-view').forEach((p) => p.classList.remove('active'));
  document.querySelectorAll('.nav-item').forEach((n) => n.classList.remove('active'));
  el('page-' + name)?.classList.add('active');
  const nav = document.querySelector('.nav-item[data-page="' + name + '"]');
  nav?.classList.add('active');

  document.querySelectorAll('.nav-group').forEach((g) => g.classList.remove('has-active'));
  const navGrp = nav?.closest('.nav-group') as HTMLElement | null;
  if (navGrp) {
    navGrp.classList.add('has-active');
    // Auto-expand the category holding this page, and DELIBERATELY DO NOT SAVE
    // it — see the header of nav.ts. Only when a group was actually found: a
    // page outside every category leaves the sidebar alone.
    navAutoExpand(navGrp.dataset.cat);
  }

  const title = el('pageTitle');
  if (title) title.textContent = pageTitle(name);
  const icon = el('pageTitleIcon');
  if (icon) {
    icon.innerHTML = '';
    const svg = nav?.querySelector('.nav-icon svg');
    if (svg) icon.appendChild(svg.cloneNode(true));
  }

  // THE BAR LAST, and with the name AFTER the settings rewrite above — so a
  // deep link to a page the operator may not see corrects the URL instead of
  // leaving it describing a page that is not on screen.
  //
  // The dispatch below is deliberately NOT guarded on `prev !== name`. It fires
  // on every reconnect today and around twenty page modules are built against
  // that; adding a guard here would quietly change a contract they rely on. The
  // double-fire hazard belongs to the popstate path, and `initRouting` handles
  // it there.
  sync(name, mode);
  document.dispatchEvent(new CustomEvent('mikrodash:pagechange', { detail: name }));
  if (prev && prev !== name) socket.emit('page:blur', prev);
  socket.emit('page:focus', name);
}

// `host` and `disabled` are what the TOPBAR dropdown needs — the row's subtitle
// and the switchable filter. They were absent from this type while only the
// mobile <select> was wired, which needs neither.
/**
 * One row of `/api/routers`.
 *
 * ── IT EXTENDS StoredRouter, AND THAT IS THE POINT ──────────────────────────
 *
 * This was four fields — id, label, name, host, disabled — which was everything
 * the router picker needed and nothing the Add/Edit modal does. The endpoint has
 * always sent twenty-three (see `store.PublicRouters`); the narrow type simply
 * hid the rest from TypeScript.
 *
 * Extending the modal's own record type means the two cannot drift: a field the
 * form learns to edit is a field this list is already known to carry, rather
 * than one somebody has to remember to add here as well.
 */
interface RouterRow extends StoredRouter {
  // NARROWED back to required: `StoredRouter.id` is optional because the modal
  // opens on `null` for a fresh Add, and a record that has never been saved has
  // no id. A row that came back from `/api/routers` always has one.
  id: string;
  name?: string;
  disabled?: boolean;
}

/**
 * The router list comes from Node, through the proxy.
 *
 * Not reimplemented in Go on purpose: `/api/routers` already applies this
 * principal's grants, and a second implementation of that filter is a second
 * place for it to be wrong. This is what the strangler is for — take the page,
 * leave the endpoint.
 */
async function loadRouters(): Promise<RouterRow[]> {
  const res = await fetch('/api/routers', { credentials: 'same-origin' });
  if (!res.ok) throw new Error('cannot list routers: ' + res.status);
  const body = await res.json();
  return (body.routers || body || []) as RouterRow[];
}

/**
 * Where a page request goes during coexistence.
 *
 * ONE function, called by both the nav items and the keyboard shortcuts. They
 * had the same rule written out twice for about ten minutes; two copies of a
 * routing decision drift, and the way you find out is a shortcut that blanks
 * the page.
 */
function navigate(socket: Socket, page: string): void {
  if (PORTED.has(page)) {
    showPage(socket, page);
    return;
  }
  // NOT REACHABLE FROM THE NAV any more: `initCaps` hides a page this build
  // cannot serve, so nothing offers a click that lands here. It stays because
  // `navigate` is also called by the keyboard shortcuts, which address pages by
  // name and do not consult the nav — and because "the Node app still owns this
  // page" stopped being true when the strangler prefix came off. `/` is this
  // app, so this is a bounce to the landing page rather than a hand-off.
  window.location.href = '/';
}

function wireNav(socket: Socket): void {
  document.querySelectorAll<HTMLElement>('.nav-item').forEach((item) => {
    if (item.closest('#authUserChip')) return;
    const page = item.dataset.page;
    item.addEventListener('click', (e) => {
      e.preventDefault();
      if (!page) return;
      navigate(socket, page);
    });
  });

  // The burger and the overlay belong to the shell markup, not to any page.
  //
  // ── THE CLASS NAMES ARE THE STYLESHEET'S, NOT THIS MODULE'S ───────────────
  //
  // This toggled `body.nav-open` until 2026-08-25, and **nothing styles that
  // class**: `web/public/app.css` carries six rules on `#sidenav.mobile-open`
  // and one on `#navOverlay.show`, and zero on `nav-open`. So the burger did
  // nothing, the sidenav never opened, and a MOBILE USER COULD NOT NAVIGATE AT
  // ALL — the shell is the only way to reach another page on a narrow screen.
  //
  // Found by extending `wiring-audit` to `shell.html`, which had never been
  // scanned: `burgerBtn` was mentioned by this port so it looked wired, and
  // `sidenav` — the element that actually carries the state — was not.
  //
  // A REMINDER THAT INVENTING A CLASS NAME IS NOT A FREE CHOICE. The stylesheet
  // is the live app's, extracted verbatim; the port does not get to pick its own
  // hooks into it.
  const sidenav = el('sidenav');
  const navOverlay = el('navOverlay');
  const closeNav = (): void => {
    sidenav?.classList.remove('mobile-open');
    navOverlay?.classList.remove('show');
  };
  const openNav = (): void => {
    sidenav?.classList.add('mobile-open');
    navOverlay?.classList.add('show');
  };
  el('burgerBtn')?.addEventListener('click', () => {
    if (sidenav?.classList.contains('mobile-open')) closeNav(); else openNav();
  });
  navOverlay?.addEventListener('click', closeNav);
  // Choosing a page closes the nav, but only where it overlays the content.
  // The width test is the original's: above it the sidenav is permanent and
  // closing it would hide the navigation the user is still using.
  document.querySelectorAll<HTMLElement>('.nav-item').forEach((item) => {
    item.addEventListener('click', () => {
      if (window.innerWidth <= 767) closeNav();
    });
  });

  // Chrome, like the dropdown: the Update button lives in the System card's
  // slot, which is visible from every page.
  initUpgrade(socket);
  wireModals();
}

/**
 * The banners.
 *
 * The mechanics live in banners.ts, which reproduces the live app's: a `.show`
 * class rather than an inline style, the `is-disconnected` and
 * `is-ros-disconnected` body classes the stylesheet dims the UI with, the SVG
 * pause and the blanked live rates. An earlier version of this function did none
 * of that and set `style.display` instead, which outranks the stylesheet
 * permanently and left a live-looking UI over dead data.
 */
function wireBanners(socket: Socket): void {
  socket.on('connect', onSocketConnect);
  socket.on('disconnect', onSocketDisconnect);
  // A REFUSED HANDSHAKE, which is a different event from a dropped connection:
  // the upgrade is auth-gated, so a dead session means `open` never fires and
  // nothing else in this file can notice. See verifySessionAfterFailure.
  socket.on('connect_error', () => verifySessionAfterFailure());

  // This port's server emits ONE room-scoped `router:status` where the live one
  // emits `ros:status` for the session and a global `router:status` for the
  // Routers list. Room-scoped, it answers the first question — see banners.ts.
  socket.on('router:status', (d: { connected?: boolean; reason?: string }) => {
    setRosBanner(!!(d && d.connected), d && d.reason);
  });

  socket.on('session:expired', () => { window.location.href = '/login'; });
  socket.on('access:revoked', () => { window.location.href = '/'; });
  // Not a RouterOS outage, but it uses the same banner to say so: there is
  // nothing to show and the reason is worth stating rather than leaving blank.
  socket.on('access:none', () => {
    setRosBanner(false, 'No router is readable by this account');
  });
}

/**
 * Move to another router, clearing what the last one left on screen.
 *
 * The live app clears on a `router:switching` event the server sends before the
 * new state; this port's server does not emit one — it announces `router:active`
 * and `router:switched` AFTER the move. So the clear happens where the client
 * already knows the switch is starting: here, at the moment it asks. Same
 * instant, one fewer round trip, and the rows never outlive the router they
 * belong to.
 *
 * Without it a card keeps the previous router's rows under the new router's
 * name until that router's first payload replaces them — indefinitely if the
 * collector feeding it is slow or switched off.
 */
function switchRouter(socket: Socket, id: string): void {
  clearDashboardData();
  resetStaleTimers();
  // The new router is another board. The System card's meta line is written
  // once per connection, so without this it would keep the OLD board's name,
  // RouterOS version and CPU count under the new router's live gauges — the
  // live app resets it here for exactly that reason.
  resetSysMeta();
  // Same reason, different cache: the Connections card skips a redraw when the
  // payload fingerprints the same as the last one, and two routers can easily
  // agree on their top talker and its count. Without this the new router would
  // keep showing the previous router's lists.
  resetConnCaches();
  // The chart too: another router's history is not this one's, and `currentIf`
  // names an interface that may not exist on the new device.
  resetTraffic();
  resetPing();
  resetRoutingCards();
  resetBandwidthCard();
  resetLogsCard();
  socket.emit('router:select', id);
}

async function main(): Promise<void> {
  // BEFORE ANYTHING THAT FETCHES, which is why it is the first statement here
  // and the first thing `public/app.js` does. A request made before the wrapper
  // is in place is a request whose 401 nobody sees — and with the SPA already
  // open, a dead session then leaves the page sitting there with no login
  // screen. See fetch-guard.ts.
  installFetchGuard(() => { void refreshCaps(); });

  // ── THE PAGE IS INVISIBLE UNTIL THIS RUNS ───────────────────────────────
  //
  // `preflight.js` sets `documentElement.style.opacity = '0'` when
  // `sessionStorage.justLoggedIn` is set, so the app does not flash its default
  // colours between the login redirect and the first paint. The live app fades
  // it back in (`public/app.js`, just below its socket handlers). THIS PORT DID
  // NOT, so after a successful login the whole app rendered correctly and was
  // completely invisible — an empty page. A plain reload showed it, because
  // preflight only hides when the flag is set.
  //
  // Reported by the operator on 2026-08-28: "when i log in, I now get
  // redirected to .../next/ on a empty page".
  //
  // AS EARLY AS POSSIBLE, and immediately after the fetch guard: anything that
  // throws before this line leaves the operator looking at nothing, with no way
  // to tell a crashed app from a blank one. The live app puts it near the top of
  // its file for the same reason.
  if (sessionStorage.getItem('justLoggedIn')) {
    sessionStorage.removeItem('justLoggedIn');
    setTimeout(() => {
      document.documentElement.style.transition = 'opacity 1s ease';
      document.documentElement.style.opacity = '1';
    }, 200);
  }

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const socket = new Socket(proto + '//' + location.host + '/ws');

  // FIRST, and before anything that paints: the palette, contrast and font are
  // read from localStorage and applied to <html>. Everything after this renders
  // once, in the right colours, instead of flashing the defaults first.
  initAppearance();

  wireNav(socket);
  initNav();
  // The Dashboard's cards. Subscribed once at boot rather than on navigation:
  // `system:update` feeds the top bar's gauges and the uptime chip, which are
  // chrome and are visible on every page.
  initDashboard(socket);
  // Document-level and shared: the Dashboard's Connections card and the
  // Connections page both render `.has-ip-tip` elements.
  initIpTip();
  // The Dashboard's card grid: layout, drag, resize and the Add panel. Returns
  // null when the dashboard markup is not present.
  initDashboardGrid();
  initKeyboard((page) => navigate(socket, page));
  // The chrome's permission layer. It reaches the router through this host
  // rather than importing showPage, which would be a cycle — and `go` deliberately
  // calls showPage directly, NOT navigate: being moved off a page you may not see
  // must land somewhere, and bouncing to the Node app would lose the session's
  // place for a reason that has nothing to do with coexistence.
  // ── A PAGE THIS BUILD CANNOT SERVE IS HIDDEN, NOT OFFERED ───────────────
  //
  // `navigate()` sends an unported page to `/` on the reasoning that "the Node
  // app still owns this page and still renders it correctly". That was true
  // under the `/next` strangler prefix. It is not true now: this bundle is only
  // ever served by a process that IS the whole app, so `/` is this app and the
  // click became a bounce back to the dashboard — which is what the operator
  // hit on Devices and on Settings.
  //
  // So `serves` is a fourth term in the visibility calculation, alongside the
  // install toggle, the role and the router count. A page that is not in
  // `PORTED` has no markup in this bundle and cannot be shown by anything; the
  // honest interface is to leave it out of the nav until it mounts, at which
  // point it reappears with no further change here.
  initCaps({
    current: () => currentPage,
    go: (page) => showPage(socket, page),
    serves: (page) => PORTED.has(page),
  });
  // After initCaps, because the clock reads the install's display timezone and
  // that arrives with the first settings:pages.
  initClock();
  // The install's page toggles, broadcast on connect and on every settings save.
  socket.on('settings:pages', (pages) => applyPageVisibility(pages as Record<string, unknown>));
  // The per-router collection config, sent on the router handshake.
  //
  // `applyCollectionConfig` marks a disabled collector's card `is-collector-off`
  // and suppresses its stale countdown. It existed, gated, and UNCALLED until
  // 2026-08-28: the server resolved the config and never emitted it, so a
  // collector the operator had turned off showed a stale card and read as broken.
  // The live listener is `public/app.js:3159`, which guards on `cfg.enabled` the
  // same way.
  socket.on('collection:config', (cfg: { enabled?: Record<string, unknown> } | undefined) => {
    if (cfg && cfg.enabled) applyCollectionConfig(cfg.enabled);
  });
  // The DORMANT set, sent whenever the supervisor's verdict changes.
  //
  // `applyCollectionStatus` dims the cards of collectors that have been put to
  // sleep and suppresses their stale countdown, so a paused card reads as paused
  // rather than as broken. Like its sibling it existed, gated and UNCALLED,
  // until the server had a dormancy supervisor to emit this — see
  // `internal/dormancy`. The live listener is `public/app.js:3123`, which guards
  // on `Array.isArray(st.dormant)` the same way.
  socket.on('collection:status', (st: { dormant?: unknown } | undefined) => {
    if (st) applyCollectionStatus(st.dormant);
  });
  // Another administrator adding or removing a site must not leave this tab
  // stale. Fleet-wide, like perms:changed — sites are not per-router.
  socket.on('sites:update', (list) => onSitesUpdate(list));
  // A permissions change can take the principals card away — or give it —
  // without a reload. Re-asking is cheap; leaving it on screen is not.
  socket.on('perms:changed', () => { void refreshCaps().then(refreshPrincipalsVisibility); });

  // ── THE ROUTER YOU ARE LOOKING AT WAS DISABLED ────────────────────────────
  //
  // The server tears its session down and tells everybody in its room. Without
  // this the page keeps its selection and simply stops updating — which reads as
  // a hung app rather than as a router that was turned off, and there is nothing
  // on screen to explain it.
  //
  // MOVE TO THE FIRST ENABLED ROUTER THAT IS NOT THIS ONE. Both halves matter:
  // `!r.disabled` because the list still holds the one just turned off (the
  // refreshed list has not arrived yet), and `r.id !== data.routerId` because
  // this browser's copy may not have been updated at all.
  //
  // If there is no such router, NOTHING HAPPENS — deliberately, matching the
  // live handler. An install with one router that has just been disabled has
  // nowhere to go, and switching to a disabled device would be worse than
  // staying put.
  socket.on('router:disabled', (d: { routerId?: string }) => {
    if (!d || !d.routerId) return;
    const next = routers.find((r) => !r.disabled && r.id !== d.routerId);
    if (next) switchRouter(socket, next.id);
  });
  // The account modal — opened by the chip, which `wireNav` deliberately skips.
  wireAccount();
  // A nudge, never the caps themselves: re-asking re-resolves them server-side,
  // so a forged event cannot widen anything.
  socket.on('perms:changed', () => { void refreshCaps(); });

  // ── Re-join the page room after a router switch ───────────────────────────
  //
  // Rooms are per-socket AND PER-ROUTER: `router-<id>-page-<name>`. A switch
  // drops every room this socket was in and joins the new router's BASE room
  // only, so a page-scoped collector goes on emitting into a room this browser
  // has just left. The page then shows nothing at all until the user navigates
  // away and back — which looks like the new router having no data.
  //
  // `router:active` is the one signal every path shares: the server sends it on
  // connect, on a switch, and on a hot-swap alike. Reacting to a CHANGE of id
  // re-joins through the ordinary `page:focus` handler, so the role gate is
  // re-applied against the NEW router rather than carried over from the old one.
  //
  // The FIRST one is skipped deliberately — on a fresh connect the room has
  // already been joined by the code that opened the page, and re-emitting would
  // be a second join for the room we are already in.
  let roomsRouterId = '';
  socket.on('router:active', (d: { activeId?: string } | undefined) => {
    const id = (d && d.activeId) || '';
    if (!id || id === roomsRouterId) return;
    const first = !roomsRouterId;
    roomsRouterId = id;
    if (first) return;
    socket.emit('page:focus', currentPage);
    document.dispatchEvent(new CustomEvent('socket:reconnect'));
  });

  // ── Stale detection ───────────────────────────────────────────────────────
  //
  // One subscription per distinct event, each re-arming every card that event
  // feeds — `routing:update` feeds four of them. `pollMs` on the payload retunes
  // that card's threshold, so a collector reporting a slower interval stops
  // being called stale for keeping to it.
  for (const event of [...new Set(STALE_CARDS.map((c) => c.event))]) {
    socket.on(event, (d: { pollMs?: number } | undefined) => {
      for (const cardId of cardsForEvent(event)) notePayload(cardId, d && d.pollMs);
    });
  }
  // Both of these leave the card's rooms, so nothing arrives while away and the
  // timers keep counting. Coming back to a wall of stale cards that heal a few
  // seconds later is that arithmetic, not a real stall.
  document.addEventListener('mikrodash:pagechange', () => { resetStaleTimers(); });
  socket.on('connect', resetStaleTimers);
  // Room membership is PER-SOCKET and is lost when the connection drops. The
  // grid listens for this and re-joins every visible room-gated card; without
  // it a viewer who blinked keeps a dashboard whose gated cards never receive
  // anything again. Dispatched at the same two places the live app dispatches
  // it — here, and on a router change below.
  socket.on('connect', () => document.dispatchEvent(new CustomEvent('socket:reconnect')));
  startStaleSweep();
  wireAppearance();
  wireBanners(socket);
  initDnsPage(socket, pageVisible);
  initBridgesPage(socket, pageVisible);
  initVlansPage(socket, pageVisible);
  initWanPage(socket, pageVisible);
  initPackagesPage(socket, pageVisible);
  initRoutingPage(socket, pageVisible);
  initDhcpPage(socket, pageVisible);
  initPppPage(socket, pageVisible);
  initVpnPage(socket, pageVisible);
  initRosUsersPage(socket, pageVisible);
  initQueuesPage(socket, pageVisible);
  initFirewallPage(socket, pageVisible);
  initWifiPage(socket, pageVisible);
  initCapsmanPage(socket, pageVisible);
  initInterfacesPage(socket, pageVisible);
  initLogsPage(socket, pageVisible);
  initTopologyPage(socket, pageVisible);
  initWirelessPage(socket, pageVisible);
  // The bell is shell chrome rather than a page: it initialises once, and its
  // feed follows the active router the same way every other subscription does.
  initNotifications(socket, () => activeRouterId);
  initConnectionsPage(socket, pageVisible);
  initBandwidthPage(socket, pageVisible);
  initBackupsPage(socket, pageVisible);

  let routers: RouterRow[] = [];
  try {
    routers = await loadRouters();
  } catch (e) {
    console.error(e);
  }

  // ── The Devices page ────────────────────────────────────────────────────
  //
  // `mountRouters` is the whole of it. What is NOT here is the Add/Edit modal,
  // and that is a finding rather than an omission: the Devices page has no edit
  // affordance of its own. Its table rows carry `data-router-id` and no buttons,
  // and its cards carry none either — the ONLY way into the router modal from
  // this page is the fleet map's popover and its no-location tray, both of which
  // go through `window._rtrOpenModal`, and both of which belong to the map's
  // unported SVG half.
  //
  // `rtrAddBtn`, `rtrTbody` and the modal's own trigger live on the SETTINGS
  // page (`web/src/ui/page-settings.html`). An earlier version of this file
  // wired `initRouterModal` from here anyway, complete with a
  // `[data-edit-router]` handler for an attribute nothing in the app produces —
  // The selector audit is what said so.
  //
  // THE OPENER NOW EXISTS: `settings-routers.ts` renders the Edit button the
  // live app opens the modal from, and is wired below. The page still does not
  // MOUNT (LOOP 1h), so both stay inert for now — but they are inert together
  // and for one reason, rather than one of them being permanently unreachable.
  mountRouters(socket);
  // The fleet map's SVG half, mounted from HERE so the dependency stays
  // one-way: `routers-map.ts` imports `routers.ts`, never the reverse. See the
  // note at the top of `mountRouters`.
  initRoutersMap();

  // The first-run overlay. Mounted 2026-08-29, once
  // `POST /api/routers/{id}/activate` was ported — until then its Connect button
  // made a request that 404ed, and mounting it would have put a broken button on
  // the FIRST screen an operator ever sees.
  //
  // On an install that already has routers this is inert: `setup:required` is
  // only emitted when the fleet is empty.
  initSetupOverlay(socket);

  /**
   * Re-read the fleet after `routers:update`.
   *
   * THE LIST IS REASSIGNED, not mutated, because every reader is a thunk
   * (`() => routers`) and reassignment is what those thunks are for. Mutating in
   * place would work today and break the moment one of them keeps a reference.
   *
   * `renderRoutersStats(null)` repaints the Devices table from its LAST payload
   * rather than blanking it: those rows carry live numbers this refresh does not
   * have, and the next `routers:stats` is up to two seconds away.
   */
  async function refreshRouters(): Promise<void> {
    try {
      routers = await loadRouters();
    } catch (e) {
      console.error(e);
      return;
    }
    dropdown.refresh();
    renderRoutersStats(null);
    // The Settings table too. The live app repaints it from the same event; a
    // port that refreshed only the picker would leave the table showing a
    // router that has just been deleted from under it.
    renderRoutersInto();
  }

  // `_broadcastRoutersList` fires on an add, an edit, a delete — and on a
  // background identity write, which is how a router that has just reported its
  // model reaches the picker without a reload.
  socket.on('routers:update', () => { void refreshRouters(); });

  // Reports takes no socket: it is the one page fed by HTTP, on demand, from
  // the Go report endpoints. It is mounted AFTER the router list arrives
  // because its own picker is filled from it — see mountReports.
  mountReports(routers);
  // Audit takes no socket either, and no router list: its rows are filtered
  // server-side per row, so there is nothing for a picker to choose.
  initAuditPage();

  // The Sites card, on the Settings page. `routers` is passed as a THUNK rather
  // than a value: the device checkboxes are rendered every time the form opens,
  // and a list captured here would be whatever the fleet was at page load.
  initSitesCard(() => routers);

  // Access Management, on the same page. The two accessors are THUNKS because
  // both arrive after mount: `_caps` from its own fetch and `_authMode` from
  // /api/auth/status, and whichever lands last must find a live reader rather
  // than a value captured here.
  initPrincipalsCard({
    routers: () => routers,
    mayManage: mayManagePrincipals,
    authMode: () => (globalThis as unknown as { _authMode?: string })._authMode || 'none',
  });

  // Data Cleanup, on the same page. It takes NO accessors: it fetches its own
  // router list, because the names it needs include routers that have been
  // DELETED and are still holding history — ids `routers` no longer carries.
  initDbCleanup();

  // The poll sliders, the preset profiles, the settings banner and Reset.
  //
  // The reload passed in is the POLL HALF only. The live `loadSettings` also
  // fills ~100 form fields through `populateSettings`, which is exported and
  // still uncalled — mounting the Settings page is what wires that up, and it is
  // LOOP item 1h. Passing a partial reload rather than nothing is deliberate:
  // after a Reset the sliders MUST show the defaults they were reset to, and a
  // no-op reload would leave them displaying the values that no longer apply.
  // THE MODAL, mounted at last. It has been ported and tested since well before
  // anything could open it; `settings-routers.ts` below is the Edit button the
  // live app opens it from.
  //
  // `onSaved` refreshes the fleet rather than patching the row it just wrote:
  // the server may have normalised the record (a host, a port defaulted from the
  // TLS toggle), and a patched row would show what was typed instead of what was
  // stored.
  const routerModal = initRouterModal({
    sites: () => sitesById as never,
    routers: () => routers as never,
    onSaved: () => { void refreshRouters(); },
  });

  // The alert-type toggles and the interface-kind filter card. Mounted BEFORE
  // the poll wiring only because both listen for the settings page change and
  // this one also restores from localStorage — order between them is otherwise
  // immaterial, since they share no state.
  initAlertFilters();

  // The four Test buttons. Each press SENDS one real message, so the button
  // locks for the duration of the request — a double-click is two notifications,
  // and a delivered message cannot be withdrawn.
  initNotifTestButtons();

  // ── THE SETTINGS PAGE'S SHELL AND ITS LOADER ────────────────────────────
  //
  // Mounted only now that the page is in PORTED. Both were written long before
  // anything could reach them, and BOTH WERE INERT IN A WAY NO GATE SAW: the six
  // tab buttons had no listener, so five of the six panes were unreachable, and
  // every form field was empty because nothing called `populateSettings`.
  //
  // `wiring-audit` passed throughout, because it checks that ids are BOUND and
  // the tabs are addressed by class (`.stab`), not by id. Found by opening the
  // page and clicking.
  mountSettingsTabs();

  // The live `loadSettings`: one fetch, then the form and the poll card from the
  // same payload. Called on every visit rather than once, matching the live
  // comment — "Load settings on every visit to the settings page" — because
  // another tab or another admin can change them underneath this one.
  function loadSettings(): void {
    void fetch('/api/settings', { credentials: 'same-origin' })
      .then((r) => r.json())
      .then((data) => {
        populateSettings(data);
        // AFTER `populateSettings`, and it matters: the live `populate` fills
        // the fields first and builds the sliders last, and the sliders read
        // `data` rather than the fields — so the order is not observable through
        // the DOM, only through which of the two owns `s_poll*`. Keeping the
        // live order means it stays that way if one of them ever grows a
        // dependency on the other.
        applyPollSettings(data);
      })
      .catch(() => {});
  }

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'settings') loadSettings();
  });

  initPollAndBanner(loadSettings);

  // THE MOBILE CONTROL. `#navRouterWrap` is display:none until the mobile media
  // query, so on a desktop browser this select is invisible — which is why
  // wiring only this one left the desktop with no working switcher at all.
  const sel = el<HTMLSelectElement>('navRouterSelect');
  if (sel) {
    sel.innerHTML = routers.map((r) =>
      '<option value="' + r.id + '">' + (r.label || r.name || r.id) + '</option>').join('');
    sel.addEventListener('change', () => {
      switchRouter(socket, sel.value);
      socket.emit('page:focus', currentPage);
    });
  }

  // AND THE DESKTOP ONE, which is the visible control on any screen wide enough
  // to hide the sidenav row. Both drive the same `switchRouter`; only the
  // control differs, exactly as in the live app.
  let activeRouterId = '';
  const routerStatus: Record<string, boolean | undefined> = {};
  // The Settings routers table, and with it the ONLY opener the router modal
  // has. The fleet, the active id and the live status map are all passed as
  // thunks so a `routers:update` reassigning `routers` is seen rather than
  // snapshotted.
  //
  // ── IT IS MOUNTED *HERE*, BELOW THOSE TWO DECLARATIONS, AND MUST STAY ────
  //
  // A thunk defers a READ, not a reference, and `initSettingsRoutersTable` ends
  // by rendering once — so the thunk fires synchronously during init. Mounted
  // above `let activeRouterId`, that read hits the temporal dead zone and the
  // whole of `main()` dies with "Cannot access 'activeRouterId' before
  // initialization": no dashboard, no sockets, nothing.
  //
  // Nothing in the type system says so and no gate caught it — the gate supplies
  // its own thunks, which are initialised. It was found by opening the page.
  initSettingsRoutersTable({
    routers: () => routers,
    activeId: () => activeRouterId,
    status: () => routerStatus,
    sitesById: () => sitesById,
    openModal: (r) => routerModal.open(r as never),
  });

  const dropdown = wireRouterDropdown(
    () => routers,
    () => activeRouterId,
    () => routerStatus,
    (id) => {
      // Keep the hidden select in step, so switching on desktop and then
      // narrowing the window does not show a stale selection.
      if (sel) sel.value = id;
      activeRouterId = id;
      overlay = overlayOnSwitch();
      const r = routers.find((x) => x.id === id);
      paintOverlay(r ? (r.label || r.name || r.id) : 'router');
      switchRouter(socket, id);
      socket.emit('page:focus', currentPage);
      dropdown.refresh();
    },
  );
  // The dot on each row is the live status, so a status arriving while the panel
  // is open must repaint it.
  // ── the switching overlay ─────────────────────────────────────────────────
  //
  // Opened HERE rather than on a `router:switching` event, for the same reason
  // the clearing above happens here: this port's server does not emit one, and
  // the client already knows the switch is starting at the moment it asks.
  let overlay = { open: false, falses: 0 };
  function paintOverlay(label: string): void {
    const ovl = el('rtrSwitchingOverlay');
    const lbl = el('rtrSwitchingLabel');
    if (lbl && label) lbl.textContent = 'Switching to ' + label + '…';
    if (ovl) ovl.classList.toggle('open', overlay.open);
  }

  socket.on('router:status', (d: { routerId?: string; connected?: boolean }) => {
    if (!d || !d.routerId) return;
    routerStatus[d.routerId] = !!d.connected;
    // The Settings table's badge for THIS router, in place. Without it that
    // table keeps whatever status it was rendered with until the next
    // `routers:update` — a router that went offline still reading "Online".
    updateRouterStatusBadge(d.routerId, !!d.connected);
    // BOTH dots, because there are two: the topbar one and the mobile nav's.
    // The live app updates them together from the same event, and wiring only
    // the visible-on-desktop one would leave a permanently green dot on a phone
    // — the same shape as the switcher and the burger before them.
    for (const id of ['rtrStatusDot', 'navRtrStatusDot']) {
      el(id)?.classList.toggle('offline', !d.connected);
    }
    overlay = overlayOnStatus(overlay, !!d.connected);
    paintOverlay('');
    dropdown.refresh();
  });

  // ── AN EMPTY FLEET IS A STATE, NOT AN EXIT ────────────────────────────────
  //
  // This used to `return` here, which skipped everything below it: `initRouting`,
  // the `connect` handler, and the `showPage` that honours the URL. So on an
  // install with no routers the PAGE ROUTER WAS NEVER INITIALISED. The view
  // stayed on whatever `page-view active` the static markup ships, a deep link
  // to /settings silently landed on the dashboard, and back and forward did
  // nothing. Only the sidebar worked, because nav clicks are wired elsewhere.
  //
  // That is precisely the state a NEW INSTALL is in, and it is the state in
  // which an operator most needs to reach Settings — it is where routers are
  // added. Found while fixing the unbound Add Device button on issue #124, which
  // is the same new-install blind spot one layer up.
  //
  // The fleet-dependent work is guarded individually below instead. Warning
  // still logged: an account that can read NO router on an install that HAS
  // routers is a permissions problem worth seeing.
  const first = routers[0];
  if (!first) {
    console.warn('no routers are readable by this account');
    // ── A FIRST RUN GETS THE WIZARD, NOT AN EMPTY DASHBOARD ─────────────────
    //
    // Shown from HERE because this is where the answer already is: the fleet has
    // been fetched, and it is empty. The `setup:required` event covers the OTHER
    // way a fleet empties — someone deleting their last router in another tab —
    // and reaches browsers that are already open. It cannot cover this one,
    // because a browser arriving at an install that never had a router is never
    // told anything.
    //
    // `initSetupOverlay` is mounted below and stays mounted: this shows the
    // overlay, it does not replace the wiring that makes its buttons work.
    showSetupOverlayNow();
  }
  const select = () => {
    const id = sel?.value || (first ? first.id : '');
    // NOTHING TO SELECT is not the same as selecting nothing: `switchRouter`
    // with an empty id would ask the server to make '' the active router.
    if (id) switchRouter(socket, id);
    // THE DROPDOWN'S LABEL COMES FROM HERE, and nowhere else did.
    //
    // `activeRouterId` was assigned ONLY in the dropdown's own `onChoose`, so
    // until the operator picked a router by hand it stayed '' — and
    // `refreshLabel` renders '—' when no router matches. The top-right control
    // showed a dash on every fresh load, which is what the operator reported as
    // "not displaying the active router".
    //
    // The server's `router:active` does arrive, but its only handler manages
    // ROOM membership and does not touch this. It also cannot: both
    // `activeRouterId` and `dropdown` are declared below it, so referencing them
    // from that handler is a temporal-dead-zone hazard if the event ever landed
    // early. `select()` runs after both, on first load AND on every reconnect,
    // which is exactly when the label needs to be right.
    if (id) activeRouterId = id;
    // REFRESHED EITHER WAY, so an empty fleet renders the picker's own empty
    // state rather than a stale label.
    dropdown.refresh();
    // THE PAGE THE OPERATOR IS ON, not the landing page.
    //
    // `currentPage` is '' until the first call, so the first `select()` lands on
    // `dns` and every later one re-asserts where they actually are. It used to
    // pass 'dns' unconditionally — and because this runs on EVERY connect, a
    // network blip navigated the operator off whatever page they were reading
    // and back to DNS.
    //
    // Same shape as the traffic-interface defect upstream fixed in `d7548b0`,
    // and found by taking that report's own generalisation seriously:
    // per-connection state cannot outlive the connection, a reconnect is not a
    // new user, and anything the operator CHOSE — an interface, a filter, a page
    // — needs somewhere with a longer life than the socket. The browser is that
    // place, because the page has not reloaded.
    // THE LANDING PAGE IS THE DASHBOARD, as the live app's is: `_currentPage =
    // 'dashboard'` and `<div class="page-view active" id="page-dashboard">`.
    //
    // This said 'dns' — a leftover from the first vertical slice, when DNS was
    // the ONLY ported page and landing anywhere else meant landing on nothing.
    // It outlived that by twenty-two pages, and the operator met it as "I land
    // on the DNS page".
    // THE URL DECIDES THE FIRST PAGE, and `currentPage` every time after — so a
    // refresh keeps the operator where they were, and a reconnect does not move
    // them. The first paint REPLACES: the load's own entry is the one being
    // corrected, and pushing would leave a phantom behind it.
    const firstPaint = currentPage === '';
    showPage(socket, currentPage || initialPage(PORTED, 'dashboard'),
      firstPaint ? 'replace' : 'push');
  };
  // Re-sent on every connect, because the server holds the selection on the
  // CONNECTION: a socket that has just reconnected knows nothing about which
  // router this browser was watching.
  // Back and forward. 'skip' because the browser has already moved the entry;
  // writing another would fight it.
  initRouting((key) => showPage(socket, key, 'skip'), () => currentPage);
  socket.on('connect', select);
  // And once now if the socket is already up. The router list is fetched over
  // HTTP while the socket opens in parallel, so 'connect' has usually already
  // fired by the time this runs — subscribing alone leaves the page blank until
  // the first reconnect, which is exactly what it did.
  if (socket.isOpen()) select();
}

void main();
