'use strict';
/**
 * Lift a page's renderer out of the live app so the port can be diffed against it.
 *
 * WHY THIS EXISTS. "A page that renders differently has not been ported, however
 * correct its data" (PLAN.md). Comparing screenshots catches a layout change and
 * misses an attribute; comparing the DOM catches both, but only if the original
 * renderer can be run on demand. This makes that possible: it emits the live
 * app's own renderer for one page, verbatim, wrapped so it can be driven with a
 * payload and pointed at the same DOM the ported renderer just wrote to.
 *
 * The comparison it enables is exact — `innerHTML` string equality per element —
 * which is what turns "looks the same" into a result.
 *
 * EXTRACTION IS ANCHORED ON CONTENT, NOT LINE NUMBERS. app.js is 16,086 lines
 * and moves; a slice by number silently lifts the wrong text, which would then
 * be compared against and "pass". Every anchor below is a definition line, and a
 * missing one is a hard failure.
 *
 * Output goes to web/dist/_compare/, which is build output and gitignored: this
 * is a measuring instrument, not a part of the app.
 *
 * A CHARTED PAGE NEEDS ONE EXTRA STEP. Chart.js refuses to attach twice to the
 * same canvas, so the port's chart must be released before the lifted renderer
 * runs — otherwise it throws "Canvas is already in use" and the comparison
 * reports nothing at all. The canvas is not part of the diff, since innerHTML
 * cannot see pixels, so destroying it costs the comparison nothing:
 *
 *   window.Chart.getChart(document.getElementById('rtDonutCanvas')).destroy();
 *
 * And what that means is worth stating rather than assuming: on a charted page
 * the DOM gate proves the tables and the counters, while the chart itself is
 * covered only by "same library, same configuration". A green diff does not
 * cover the drawing.
 *
 *   node tools/live-renderer.js dns
 *
 * Then, in the browser on /next/, with the page already rendered:
 *   const before = el.innerHTML;
 *   await import('/next/_compare/live-dns.js'); window.__runLiveDns(payload);
 *   before === el.innerHTML   // the whole test
 */

const fs   = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = path.join(__dirname, '..', 'web', 'dist', '_compare');

// The helpers every page renderer closes over. Taken from the live app rather
// than from the TypeScript port, or the comparison would be the port against
// itself.
const HELPERS = [
  { start: (l) => l.startsWith('function esc(s){'), single: true },
  { start: (l) => l.startsWith('function resRow(id, identity, resource){') },
  { start: (l) => l.startsWith('function _renderSortHeader(') },
  { start: (l) => l.startsWith('function _debounce(fn,ms){'), single: true },
  // Added when the live fix for the nine broken sort tables introduced it. The
  // self-check below is what caught its absence — the harness had gone on
  // building cleanly and would have thrown in the browser, which would have
  // read as "the port renders differently".
  { start: (l) => l.startsWith('function _sortMul('), single: true },
  // Added 2026-08-25, when the live Devices page grew MULTI-SITE membership: a
  // router may now carry `siteIds`/`siteNames` arrays, and these two normalise
  // the singular `siteId`/`siteName` into them. The lift broke the moment the
  // renderer started calling them, which is the self-check working — this file's
  // step one compares nothing, so an undefined helper would otherwise have
  // surfaced as "the port renders differently".
  { start: (l) => l.startsWith('function _rtrSiteIds(r) {') },
  { start: (l) => l.startsWith('function _rtrSiteNames(r) {') },
  // The generic row sorter, hoisted out of the Reports IIFE so every page could
  // share one implementation. Multi-line, so no `single`.
  { start: (l) => l.startsWith('function _sortRows(') },
  // The Reports volume formatter, also top-level. Its unit thresholds are
  // decimal (1000 MB is a GB), and a port using 1024 would differ on every row
  // above a gigabyte — a difference this harness only sees if it lifts the real
  // one rather than letting the port's copy compare against itself.
  { start: (l) => l.startsWith('function fmtDataMB(mb){'), single: true },
  { start: (l) => l.startsWith('function maxOf(a){'), single: true },
  { start: (l) => l.startsWith('function fmtMbps('), single: true },
  // The Packages page's size column. A one-liner in app.js, and lifted for the
  // same reason as the rest: a byte formatter that rounds differently is a DOM
  // difference on every row that has a size, and comparing the port's own copy
  // against itself would never show it.
  { start: (l) => l.startsWith('function fmtBytes(b){'), single: true },
  // The PPP page's uptime column. NOT caught by the undefined-helper scan below,
  // which only looks for `_`-prefixed names — and this one would not throw at
  // extraction time either, because `sortVal` guards it with
  // `parseUptime ? parseUptime(...) : 0`. A bare identifier that is not declared
  // anywhere throws ReferenceError when that guard is EVALUATED, so the page
  // would compare cleanly in its default state and blow up the moment the
  // comparison sorted by uptime.
  { start: (l) => l.startsWith('function parseUptime(raw){'), single: true },
  // The VPN dashboard card sorts its connected peers by handshake age. Same
  // trap as parseUptime: an undeclared identifier throws only when the line
  // runs, and the scan below looks for `_`-prefixed names.
  { start: (l) => l.startsWith('function parseDurationSec(s){'), single: true },
  // The Firewall page's action column. It builds three colours out of one rgba
  // string by regex, so a port that reconstructed it by hand would differ on the
  // alpha of the border before it differed on anything visible.
  { start: (l) => l.startsWith('function actionBadge(a){') },
  // The Interfaces page repopulates the traffic chart's interface picker from
  // every ifstatus payload, so the renderer calls this on the way through. It
  // closes over `_ifaceSelectKey` and `currentIf`, both lifted as page state.
  { start: (l) => l.startsWith('function _rebuildIfaceSelect(names) {') },
  // The Wifi Clients page's signal column and rate formatter. Both are
  // one-liners in app.js, and both are lifted for the same reason fmtBytes is:
  // a rate that rounds differently is a DOM difference on every row.
  { start: (l) => l.startsWith('function signalBars(dbm){'), single: true },
  { start: (l) => l.startsWith('function parseTxRate(raw){'), single: true },
];

// Where each page's IIFE begins. The banner comment above it is the anchor
// because it names the page and the IIFE line itself does not.
//
// TWO COMMENT STYLES AND TWO IIFE SPELLINGS, because app.js has both. Five pages
// open `/* ── X page` and `(function () {`; the Routing page opens
// `// ── Routing Page` and `(function(){`. Anchoring on one spelling silently
// found nothing for the other, which reads as "that page has no renderer".
const PAGE_BANNER = {
  dns: '── DNS page',
  bridges: '── Bridges page',
  vlans: '── VLANs page',
  wan: '── WAN page',
  packages: '── Packages page',
  routing: '── Routing Page',
  capsman: '── CAPsMAN page',
  ppp: '── PPP page',
  rosusers: '── Router Users page',
  queues: '── Queues page',
  // The banner reads "Wifi Networks" rather than "Wifi page", and the IIFE is
  // NAMED — `(function wifiPage() {` — which the anchor does not depend on but
  // is worth knowing when reading the lift.
  wifi: '── Wifi Networks',
  // TWO IDENTICAL BANNER LINES sit above this IIFE in app.js. The first is
  // matched and the IIFE is found after it, so the duplicate costs nothing —
  // worth knowing before someone "tidies" one away and moves the anchor.
  wireless: '── Wireless ─',
  // A THIRD banner style. This one is a box — `// ═══…` above and below a plain
  // `// Bandwidth Page` line — so the anchor is the title text rather than the
  // rule characters, which is what `includes` was always matching anyway.
  bandwidth: 'Bandwidth Page',
  // The Connections page is the SECOND built from two IIFEs, after CAPsMAN —
  // and the larger by far: a world map that owns the payload handlers, and a
  // Sankey diagram that draws the same data as a flow.
  connections: '── World Map (Connections page)',
  // Reports is the FIRST fetch-driven page in this harness. Every other one is
  // handed a payload through a socket event; this one asks for its data itself,
  // over HTTP, when somebody presses Load. See PAGE_DRIVER.
  reports: '── Reports page',
  // The SECOND fetch-driven page, and the first with no router picker at all:
  // its rows are filtered server-side per row, so there is nothing to choose.
  audit: '── Audit page',
};

// ── Pages driven by something other than a socket event ──────────────────────
//
// The tail of the emitted module normally fires `<page>:update` with the
// payload. Reports has no such event: `loadReports()` issues seven fetches and
// renders what comes back. So its driver STUBS `fetch` with the recorded
// responses and calls `loadReports()` — which exercises the real load path,
// including the interface-picker round trip, rather than calling the renderers
// directly and skipping the half of the page that decides what to render.
//
// The stub matches on the PATH, so the same payload object serves both the
// `/api/reports/traffic?…` list request and the `…&interface=WAN1` sample
// request — keyed `traffic` and `traffic:iface`.
//
// ── THE DRIVER RUNS OUTSIDE THE IIFE, SO THE IIFE HAS TO HAND IT A HANDLE ───
//
// A socket-driven page needs no such thing: the IIFE registers its handlers on
// the shared `socket` object, which is declared above it, so `_fire` reaches
// them across the boundary. A fetch-driven page has no equivalent bridge — its
// entry point is an ordinary function declared inside the closure, invisible
// from outside it. `expose` names that function; the emitter appends an
// assignment to the END of the IIFE body so the tail can call it.
// Functions lifted out of an inner IIFE so they can be compared on their own.
// See publishInside — the SVG half of the map needs a browser, the string half
// does not, and this is what separates them.
const PAGE_PUBLISH = {
  routers: { as: '__mapFns', names: ['project', 'layout', 'popHtml', 'groupPopHtml', 'renderTray',
            'clampTranslate', 'fitToMarkers'] },
};

const PAGE_DRIVER = {
  // `socket.on('routers:stats', …)` is a one-line pass-through living far from
  // the renderer, and the slicer cannot take a single-line region — `to` is
  // searched from `from + 1`, so it can never match the line it started on.
  // Calling the entry point directly is what that handler does anyway:
  //
  //   socket.on('routers:stats', function(rows) { _renderRoutersStats(rows); });
  //
  // ── ONLY THE DEFAULT VIEW IS COMPARED, AND THAT IS A STATED LIMIT ────────
  //
  // `_rtrView` starts at 'comfortable', so this run draws the GRID. The list and
  // map views are drawn by the same payload through different branches of
  // `_renderRoutersStats`, and comparing them needs a run that sets the view on
  // both sides first. Said here rather than left to be discovered: a green run
  // of this page is evidence about the grid and about nothing else.
  routers: `  _renderRoutersStats(payload);`,
  reports: `
  var _p = payload || {};
  if (typeof _drive !== 'function') throw new Error('the reports IIFE did not expose loadReports');
  window.fetch = function (url) {
    // NO REGEX HERE, deliberately: this string is a template literal, and a
    // template literal eats backslash escapes — so \\/ inside a regex literal
    // arrives as / and terminates the pattern early. The first version of this
    // driver failed to parse for exactly that reason. Splitting on the path is
    // both simpler and immune to it.
    var u = String(url);
    var after = u.split('/api/reports/')[1] || '';
    var key = after.split('?')[0].split('/')[0];
    if (key && u.indexOf('interface=') !== -1) key += ':iface';
    var body = _p[key];
    if (body === undefined) body = { ok: true, rows: [] };
    return Promise.resolve({ ok: true, json: function () { return Promise.resolve(body); } });
  };
  // The live page reads its range from the DOM, so whatever the port used is
  // already there — which is what makes this a comparison of RENDERING rather
  // than of two different queries.
  _drive();
  // The renders happen inside promise callbacks, so the caller has to await.
  return new Promise(function (r) { setTimeout(r, 400); });`,

  // Audit asks for ONE thing, so its stub needs no key at all — every fetch it
  // makes is the trail. The export links are hrefs rather than requests, so
  // nothing else reaches this.
  audit: `
  var _p = payload || {};
  if (typeof _drive !== 'function') throw new Error('the audit IIFE did not expose load');
  window.fetch = function () {
    return Promise.resolve({ ok: true, json: function () { return Promise.resolve(_p); } });
  };
  _drive();
  return new Promise(function (r) { setTimeout(r, 400); });`,
};

/** The function inside each fetch-driven IIFE that the driver calls. */
const PAGE_EXPOSE = { reports: 'loadReports', audit: 'load' };

// ── Pages built from TWO IIFEs ───────────────────────────────────────────────
//
// CAPsMAN is the first. Its page and its configuration card are separate blocks
// three thousand lines apart — the card was added later and sits with the other
// resource-dialog cards rather than with the page it belongs to. Lifting only
// the first banner would produce a renderer that draws the CAP table and leaves
// every profile tab empty, which reads as a porting gap rather than as half a
// lift.
const PAGE_EXTRA_BANNER = {
  capsman: ['── CAPsMAN configuration'],
  connections: ['── Sankey: Connection Flow'],
};

// ── WHAT THIS GATE CANNOT SEE, on a page that accumulates history ────────────
//
// The wrapper below re-executes the ENTIRE lifted IIFE on every call, so every
// `var` inside it is re-initialised. For a stateless renderer that is exactly
// right. For one that accumulates across payloads it is not: the Queues page
// keeps `_hist` for its sparklines and draws a placeholder until a series has
// two samples, so the lifted copy can never draw a sparkline at all — it is
// permanently on its first sample.
//
// The comparison therefore has to drive the PORT from a cold history too, or it
// diverges on sample count alone and reports a difference neither renderer
// would ever show a user. Emitting a row-less payload first is enough: it makes
// pushHistory drop every key.
//
// SAY WHAT THAT COSTS. The sparkline SVG path is NOT covered by this gate on
// any page that keeps history. Everything around it is — the rate values, the
// classes, the titles, the placeholder itself — but the polyline is not, and no
// amount of driving from this side will change that without teaching the
// wrapper to preserve state between calls. Worth doing if a second history-
// keeping page arrives; recorded rather than silently accepted meanwhile.

// ── Pages that are NOT an IIFE ───────────────────────────────────────────────
//
// DHCP is the first, and it is not an oversight in app.js: its renderer is
// top-level code in TWO regions, and one of them is shared. The subnet table and
// the utilisation gauge live inside the `lan:overview` handler, which ALSO draws
// the dashboard's LAN card — one handler, two pages. The leases table is a
// separate block a thousand lines away.
//
// So this page is lifted by RANGE rather than by IIFE. The anchors are the same
// kind of landmark the banner is: a line that says what it is and does not move
// when the code inside it changes.
//
// The state these regions close over is lifted too, by NAME, rather than being
// restated here. `var allLeases = [], leaseFilter = '', ...` is one line in
// app.js and copying it into this harness would be a second copy to keep in
// step — the exact mistake the whole extract-never-retype rule exists to avoid.
const PAGE_REGIONS = {
  // ROUTERS is the fifth non-IIFE page, and the whole fleet dashboard is ONE
  // contiguous run at column zero: the summary, the search helpers, the list
  // view with its sortable headers, the entry point `_renderRoutersStats`, the
  // city picker, the thin `_renderRoutersMap`, and finally the map's own IIFE
  // which installs `window._rtrMapApply`. `var _rtrView` lives inside it too,
  // so the region is self-contained.
  //
  // THE `from` ANCHOR IS THE FUNCTION, NOT ITS DOC COMMENT. The block opens with
  // a `/** … */` rather than a `── ` banner, and anchoring inside that comment
  // would start the slice at a ` * ` line whose ` */` then closes a comment that
  // was never opened — a syntax error in the lifted module.
  //
  // The `to` anchor needs its landmark: `}());` closes several IIFEs in this
  // file, so `after` pins it to the one that has just published _rtrMapApply.
  routers: [
    { from: (l) => l.startsWith('function _renderRoutersSummary('),
      to:   (l) => l === '}());',
      after: (l) => l.includes('window._rtrMapApply = apply;') },
  ],
  // VPN is the second non-IIFE page, and the same shape as DHCP: one top-level
  // handler drawing BOTH the page and a dashboard card. Its two helpers sit
  // directly above it under their own banner, so one contiguous region covers
  // the lot — banner to the `});` that closes the handler at column zero.
  vpn: [
    { from: (l) => l.includes('── VPN handshake helpers'),
      to:   (l) => l === '});',
      after: (l) => l.startsWith("socket.on('vpn:update'") },
  ],
  dhcp: [
    // The lan:overview handler through the end of renderDhcpGauge.
    { from: (l) => l.startsWith("socket.on('lan:overview'"),
      to:   (l) => l === '}',
      after: (l) => l.startsWith('function renderDhcpGauge()') },
    // The leases block, banner to banner.
    { from: (l) => l.includes('── DHCP Leases'),
      to:   (l) => l.includes('── Firewall') && l.startsWith('//'),
      exclusive: true },
  ],
  // The third non-IIFE page. One contiguous block, banner to banner: the summary
  // cards, the update handler, the in-place counter path, the search wiring, the
  // write wiring and the table renderer all sit together.
  //
  // The `from` anchor needs the trailing dash. Two other banners begin
  // `── Firewall` — the sub-tabs near the top of the file, and the write wiring
  // INSIDE this very region — and matching either would lift the wrong text and
  // then compare against it, which passes for the wrong reason.
  // TWO regions, and the gap between them is the point. app.js puts a
  // "Page Visibility" block — SVG animation pausing, rAF flush skipping —
  // between the `── Firewall` banner and the first firewall function. Lifting it
  // dragged in `_lastSampleTs`, `trafficCtx` and `_pendingSysData`, none of
  // which are lifted, so every `visibilitychange` threw a ReferenceError from
  // the compare module. It did not break the comparison, which reads innerHTML;
  // it left a broken listener registered per load and six errors in the console
  // that had nothing to do with the port.
  firewall: [
    // The banner and `var _fwSearch`, which the renderer reads.
    { from: (l) => l.includes('── Firewall ─'),
      to:   (l) => l.includes('── Page Visibility') && l.startsWith('//'),
      exclusive: true },
    // Everything from the first firewall function to the Logs banner.
    { from: (l) => l.startsWith('function fwUpdateSummary('),
      to:   (l) => l.includes('── Logs ─') && l.startsWith('//'),
      exclusive: true },
  ],
  // The fourth non-IIFE page, and the widest: SIX banners' worth of top-level
  // code in one contiguous run, because the Interfaces page is six things —
  // the per-interface state and the sparkline helpers, the list view, the tile
  // grid and its two socket handlers, the card-size switch, the type filter,
  // the Interface Types card and the Ports panel. They are adjacent in app.js
  // and they all draw into #page-interfaces, so one region covers the lot.
  //
  // `── Interface Status` is the opening anchor rather than
  // `── Interfaces: list view`, because the state the renderers close over
  // (`_lastIfaces`, `_ifaceView`, `_ifacePeaks`, `_ifaceHistory`) is declared
  // under it. Lifting from the list-view banner would leave every one of them
  // undefined — the region would throw rather than render.
  interfaces: [
    { from: (l) => l.includes('── Interface Status'),
      to:   (l) => l.includes('── Wireless ─') && l.startsWith('//'),
      exclusive: true },
  ],
  // The fifth non-IIFE page, and the smallest: one banner-to-banner region
  // holding the buffer, the two socket handlers, the filters and the badges.
  logs: [
    { from: (l) => l.includes('── Logs ─') && l.startsWith('//'),
      to:   (l) => l.includes('── Interface + window selectors') && l.startsWith('//'),
      exclusive: true },
  ],
};

// ── TWO THINGS THIS HARNESS CANNOT COMPARE ON THE FIREWALL PAGE ──────────────
//
// Both follow from the same fact as the sparkline note above: the wrapper
// RE-EXECUTES the whole lifted region on every call, so everything it declares
// is re-initialised.
//
//  1. THE SEARCHED STATE. `var _fwSearch = ''` sits inside this region, so the
//     lifted renderer's search term is wiped before every render and it can only
//     ever draw the unfiltered table. Feeding both sides an `input` event does
//     not help — each keeps its own state and the live one has just reset.
//     Compare the UNSEARCHED table here; verify the port's filtering directly
//     (row count, that positions are NOT renumbered, that the move and drag
//     controls are suppressed), which is what it is actually for.
//
//  2. A SECOND LISTENER ON A SHARED INPUT. The region calls
//     `fwSearchEl.addEventListener('input', ...)`, and that runs again on every
//     invocation — so after N calls there are N live listeners on the same box
//     as the port's. Dispatching an `input` event drives BOTH renderers and
//     whichever finishes last owns the DOM.
//
// The second is the dangerous one, and it is worth stating plainly: it produces
// a FALSE DIFFERENCE rather than a false pass. The first attempt at this page
// reported the search branch as broken when the port was filtering correctly and
// the lifted module had simply overwritten the table from a stale payload.
// Anything that dispatches a real DOM event here is comparing the harness.

// ── Pages whose renderer is NOT in app.js ────────────────────────────────────
//
// Topology is the first, and it is a whole FILE rather than a region of one:
// `public/js/topology.js` is a single IIFE, loaded after app.js so that
// `socket`, `esc`, `$`, `fmtMbps` and `pageVisible` are already globals — which
// are exactly the globals this harness already provides. So the lift is the
// whole file, with no banner to anchor on and no region to slice.
//
// The helpers and page state are still read from app.js, because that is where
// they are declared. Only the renderer moves.
const PAGE_FILE = {
  topology: ['public', 'js', 'topology.js'],
};

// The event that carries the payload, where it is not `<page>:update`.
//
// DHCP has no `dhcp:update`: the leases table is driven by `leases:list` and the
// subnet table and gauge by `lan:overview`, so the second one is passed as an
// `extra` and rendered first — it is the state the leases are then drawn
// against, which is exactly what `extra` already means here.
//
// Interfaces has no `interfaces:update` either: the page is driven by
// `ifstatus:update`, which is the collector's own event name and not the page's.
// `ifstatus:names` — the router-wide half of the split delivery — is passed as
// an `extra` when a comparison wants the picker filled.
// Connections is driven by `conn:update`, not `connections:update` — the event
// carries the COLLECTOR's name, and the page's per-country and per-source
// indexes arrive as two separate events that a comparison passes as `extra`.
const PAGE_EVENT = { dhcp: 'leases:list', vpn: 'vpn:update', interfaces: 'ifstatus:update',
                     logs: 'logs:history', connections: 'conn:update' };

// Declarations the range regions read but do not create. Lifted verbatim, for
// the same reason the helpers are.
const PAGE_STATE = {
  // `_displayTimezone` is a page-level variable the Reports IIFE READS but never
  // declares — every timestamp it renders branches on it. Without it the whole
  // load throws a ReferenceError which `loadReports` catches and logs, so the
  // lifted renderer wrote NOTHING and the comparison reported zero differences
  // for a port that had been deliberately broken. See the scan below, which was
  // widened because of this.
  reports: [
    (l) => l.startsWith('var _displayTimezone '),
  ],
  // `firewallTable`, `fwTab` and `fwData` are declared at the top of app.js,
  // hundreds of lines above the region. `firewallTable` in particular is used
  // UNGUARDED inside renderFirewallTab, so the region throws outright without it.
  firewall: [
    (l) => l.startsWith('var firewallTable '),
    (l) => l.startsWith('var fwTab = '),
  ],
  // `vpnTable` is used UNGUARDED — `vpnTable.innerHTML = ...` with no null check
  // — so the region throws outright if the dashboard card is missing. Lifting
  // the declaration is not enough on its own; the comparison has to put the
  // element in the document too, outside the region being compared.
  vpn: [
    (l) => l.startsWith('var vpnTable '),
    (l) => l.startsWith('var vpnPageCount '),
    (l) => l.startsWith('var _vpnDashTopN '),
  ],
  // Every one of these is a `$()` lookup made hundreds of lines above the
  // region, and the region uses them UNGUARDED — `ifaceGrid.innerHTML = ...`
  // with no null check — so it throws outright without them.
  // The five elements and the three filter flags, all declared at the top of
  // app.js. `logsEl`, `logSearch` and the three controls are used UNGUARDED —
  // `logSearch.addEventListener(...)` runs at region load — so the region throws
  // outright without them.
  // Both are used UNGUARDED — `wirelessTabBadge.textContent = ...` with no null
  // check — so the region throws outright without them.
  wireless: [
    (l) => l.startsWith('var wirelessTable '),
    (l) => l.startsWith('var wirelessTabBadge '),
  ],
  logs: [
    (l) => l.startsWith('var logsEl '),
    (l) => l.startsWith('var logSearch '),
    (l) => l.startsWith('var logSeverity '),
    (l) => l.startsWith('var toggleScroll '),
    (l) => l.startsWith('var clearLogs '),
    (l) => l.startsWith('var autoScroll = true, logFilter'),
  ],
  interfaces: [
    // One line declaring four things, `currentIf` and `_ifaceSelectKey` among
    // them — both read by _rebuildIfaceSelect above.
    (l) => l.startsWith('var currentIf = '),
    (l) => l.startsWith('var ifaceGrid '),
    (l) => l.startsWith('var ifaceCount '),
    (l) => l.startsWith('var ifaceTypeFilter '),
    (l) => l.startsWith('var ifaceSelect '),
  ],
  dhcp: [
    (l) => l.startsWith('var DOT ='),
    (l) => l.startsWith('var lanOverview '),
    (l) => l.startsWith('var dhcpTable '),
    (l) => l.startsWith('var dhcpTotalBadge '),
    (l) => l.startsWith('var dhcpSearch '),
    // `var lastTalkers = null, lastLanData = null;` WAS here and is gone from
    // app.js — the live side deleted both when it fixed the empty-payload guard
    // ("an empty payload is news, not silence"). This entry kept naming them, so
    // `live-renderer.js dhcp` threw, and NOTHING NOTICED: no step-two gate
    // consumes the dhcp bundle, so the lift was never run. Removed 2026-08-25.
    //
    // The port already reproduces the fixed behaviour and says so at length in
    // `dashboard-networks.ts` — the vestigial variable is deliberately not
    // carried across.
    (l) => l.startsWith('var allLeases = [], leaseFilter'),
    (l) => l.startsWith('var _dhcpTotalPoolSize ='),
    (l) => l.startsWith('var _dhcpNetworksData '),
  ],
};

// `(function () {`, `(function(){`, or a NAMED function expression such as
// `(function wifiPage() {` — the whole line, any of the three.
//
// The name is the third spelling to turn up, after the two the header above
// already warned about. It matters only here: anchoring on the anonymous form
// found nothing for the Wifi page, which reads as "that page has no renderer"
// rather than as a parser that does not know the syntax.
const IIFE_OPEN = /^\(function\s*[A-Za-z_$][\w$]*\s*\(\)\s*\{$|^\(function\s*\(\)\s*\{$/;

function findFrom(lines, pred, from = 0) {
  for (let i = from; i < lines.length; i++) if (pred(lines[i])) return i;
  return -1;
}

function block(lines, startAt, single) {
  if (single) return lines[startAt];
  // Top-level functions in app.js close on a bare `}` at column zero.
  const end = findFrom(lines, (l) => l === '}', startAt + 1);
  if (end === -1) throw new Error('unterminated block at line ' + (startAt + 1));
  return lines.slice(startAt, end + 1).join('\n');
}

function main() {
  const page = process.argv[2];
  const known = [...Object.keys(PAGE_BANNER), ...Object.keys(PAGE_REGIONS),
                 ...Object.keys(PAGE_FILE)];
  if (!page || !known.includes(page)) {
    console.error('usage: node tools/live-renderer.js <page>\nknown: ' + known.join(', '));
    process.exit(1);
  }
  const lines = fs.readFileSync(path.join(LIVE, 'public', 'app.js'), 'utf8').split('\n');

  const helpers = HELPERS.map((h) => {
    const at = findFrom(lines, h.start);
    if (at === -1) throw new Error('a helper the renderer needs is no longer in app.js');
    return block(lines, at, h.single);
  });

  // Declarations that a lifted region needs and that no single source line can
  // supply. `_ifaceSelectKey` is declared mid-line in a shared multi-name `var`
  // at the top of app.js, so lifting "the line that declares it" would drag in
  // three unrelated globals — and it is referenced from a helper EVERY page
  // includes, so it belongs in the common preamble rather than one page's state.
  // Only ever compared and assigned, so an equivalent declaration is faithful.
  const COMMON_STATE = ["var _ifaceSelectKey = '';"];

  // Per-page declarations with the same problem: named inside a shared
  // multi-name `var` far above the region, so no line-prefix predicate can lift
  // them without dragging in unrelated globals.
  //
  // The Bandwidth pair was found when the scan below started counting bare
  // references — its lifted renderer had been READING two variables that were
  // never declared, which means every Bandwidth comparison until now ran with
  // whatever a ReferenceError left behind. Nobody knew, because a renderer that
  // throws leaves the DOM untouched and the diff then reports zero.
  const EXTRA_STATE = {
    bandwidth: ['var _lastSampleTs = 0, _serverOffset = 0;'],
    // The Audit page formats timestamps from the same global the topbar clock
    // and the Reports page read. Declared here for the same reason the
    // Bandwidth pair is: it is named in a shared multi-name `var` far above the
    // region, and a renderer that throws on it leaves the DOM untouched — so
    // the diff would report zero and mean nothing.
    audit: ["var _displayTimezone = '';"],
  };

  const state = (PAGE_STATE[page] || []).map((pred) => {
    const at = findFrom(lines, pred);
    if (at === -1) throw new Error('a declaration the ' + page + ' renderer reads is no longer in app.js');
    return lines[at];
  }).concat(COMMON_STATE, EXTRA_STATE[page] || []);

  // A page whose renderer lives in its own file is that file, whole. No anchor
  // is needed and none would be honest: there is nothing else in the file to
  // exclude, and slicing it would only create a way to lift less than the live
  // app runs.
  if (PAGE_FILE[page]) {
    const src = fs.readFileSync(path.join(LIVE, ...PAGE_FILE[page]), 'utf8');
    return finish(page, helpers, state, src);
  }

  // A range page is assembled from its regions; an IIFE page from its IIFE.
  let iife;
  if (PAGE_REGIONS[page]) {
    iife = PAGE_REGIONS[page].map((r) => {
      const from = findFrom(lines, r.from);
      if (from === -1) throw new Error('a ' + page + ' region no longer starts where it did');
      // `after` lets a region end at the close of a LATER block than the one it
      // opens with — the lan:overview handler ends, then renderDhcpGauge does,
      // and the region wants both.
      const seek = r.after ? findFrom(lines, r.after, from + 1) : from;
      if (seek === -1) throw new Error('a ' + page + ' region lost its inner landmark');
      let to = findFrom(lines, r.to, seek + 1);
      if (to === -1) throw new Error('a ' + page + ' region never closes');
      if (r.exclusive) to -= 1;
      return lines.slice(from, to + 1).join('\n');
    }).join('\n\n');
    return finish(page, helpers, state, iife);
  }

  const banner = findFrom(lines, (l) => l.includes(PAGE_BANNER[page]));
  if (banner === -1) throw new Error('no banner comment for the ' + page + ' page');
  const open = findFrom(lines, (l) => IIFE_OPEN.test(l), banner);
  if (open === -1) throw new Error('the ' + page + ' IIFE is not where its banner says');

  // NESTED IIFEs EXIST, AND TAKING THE FIRST `}());` TRUNCATES THE RENDERER.
  // The Routing page closes an inner IIFE — its keyboard-navigation block —
  // sixty lines before the outer one ends. Slicing to the first close lifted a
  // renderer missing its socket handler, which would have thrown at compare
  // time and read as "the port renders differently".
  //
  // COLUMN ZERO IS THE ANCHOR, not brace depth. Every page IIFE in app.js opens
  // and closes at column 0 while everything nested inside is indented — checked
  // across all six. Counting `(function(){` occurrences instead looked more
  // rigorous and was worse: it matched `setTimeout(function () {`, an ordinary
  // callback, and the depth never came back to zero.
  const sliceIife = (bannerText) => {
    const at = findFrom(lines, (l) => l.includes(bannerText));
    if (at === -1) throw new Error('no banner comment matching ' + bannerText);
    const o = findFrom(lines, (l) => IIFE_OPEN.test(l), at);
    if (o === -1) throw new Error('the IIFE after ' + bannerText + ' is not where its banner says');
    const c = findFrom(lines, (l) => /^\}\)\(\);?$|^\}\(\)\);?$/.test(l), o + 1);
    if (c === -1) throw new Error('the IIFE after ' + bannerText + ' never closes at column 0');
    return lines.slice(o, c + 1).join('\n');
  };

  const close = findFrom(lines, (l) => /^\}\)\(\);?$|^\}\(\)\);?$/.test(l), open + 1);
  if (close === -1) throw new Error('the ' + page + ' IIFE never closes at column 0');
  iife = lines.slice(open, close + 1).join('\n');
  // A page built from more than one IIFE gets them appended in declaration
  // order — see PAGE_EXTRA_BANNER.
  for (const extra of PAGE_EXTRA_BANNER[page] || []) {
    iife += '\n\n' + sliceIife(extra);
  }
  return finish(page, helpers, state, iife);
}

// Assemble, syntax-check, reference-check and write. Shared by both extraction
// modes so a range page cannot skip a check an IIFE page gets.
function finish(page, helpers, state, iife) {

  // EVERY handler is captured, not just <page>:update. A page whose renderer
  // changes with a second event — packages hides its action buttons until
  // packages:caps says the session may write — would otherwise only ever be
  // compared in one state, and it would be the state with no buttons in it. The
  // buttons are the part worth comparing.
  //
  // socket.emit is a no-op: the renderer calls it, and a verification run must
  // not reach a router.
  //
  // ONE EVENT, MANY LISTENERS. A page built from more than one IIFE registers
  // the SAME event twice — CAPsMAN's table and its configuration card both take
  // `capsman:update` — and a map of ev -> cb silently kept the last one. The
  // lifted module then rendered the card and never touched the table, so the
  // table compared port-against-port and a deliberate mutation in it did not
  // bite. A real emitter calls every listener, so this one does too.
  // Append `_drive = <name>;` immediately before the IIFE's own closing line, so
  // the assignment happens in the closure's scope with everything defined.
  function exposeInside(text, name) {
    const lines = text.split('\n');
    for (let i = lines.length - 1; i >= 0; i--) {
      if (/^\s*\}\(\)\);?\s*$/.test(lines[i]) || /^\s*\}\)\(\);?\s*$/.test(lines[i])) {
        lines.splice(i, 0, '  _drive = ' + name + ';');
        return lines.join('\n');
      }
    }
    throw new Error('cannot find the closing line of the ' + name + ' IIFE to expose from');
  }

  // PUBLISH FUNCTIONS OUT OF AN INNER IIFE, so they can be compared directly.
  //
  // The Routers map is half SVG construction and half string building, and the
  // string half — the popover, the group popover, the unlocated tray — is
  // ordinary innerHTML that a document shim can compare. It is unreachable from
  // outside because it lives inside the map's own IIFE, which is exactly what
  // `exposeInside` already solves for the reports page. This is the same trick
  // with a different shape: a whole object rather than one driver.
  //
  // The injected line goes before the LAST closing line of the region, which for
  // routers is the map IIFE's own — so the assignment runs in that closure with
  // everything defined.
  function publishInside(text, spec) {
    const lines = text.split('\n');
    const assign = '  window.' + spec.as + ' = { '
      + spec.names.map((n) => n + ': ' + n).join(', ') + ' };';
    for (let i = lines.length - 1; i >= 0; i--) {
      if (/^\s*\}\(\)\);?\s*$/.test(lines[i]) || /^\s*\}\)\(\);?\s*$/.test(lines[i])) {
        lines.splice(i, 0, assign);
        return lines.join('\n');
      }
    }
    throw new Error('cannot find a closing IIFE line to publish ' + spec.as + ' from');
  }

  const body = `// GENERATED by tools/live-renderer.js — do not edit, do not commit.
// The live app's own ${page} renderer, verbatim, with the helpers it closes over
// and a fake socket so it can be driven with one payload.
//
// ── THE STATE IS OUTSIDE THE EXPORTED FUNCTION, DELIBERATELY ────────────────
//
// It used to be inside: every declaration the page owns was re-declared on each
// call, so a second call was a FRESH PAGE rather than a second payload to the
// same one. Any rule that only fires on a RE-RENDER — "the chosen server
// vanished, fall back to All" is the worked example — was unreachable, and a
// gate that tried looked like it covered the rule while a mutation deleting it
// survived.
//
// Freshness per CASE is unaffected: every gate re-evaluates this whole source
// for each case, which is what gives a clean page. What changed is that TWO
// CALLS WITHIN ONE EVALUATION now share state, exactly as a real page does.
(function () {
  var $ = function (id) { return document.getElementById(id); };
${helpers.join('\n')}
${state.join('\n')}
  function pageVisible() { return true; }
  var _handlers = {};
  var socket = {
    on: function (ev, cb) { (_handlers[ev] = _handlers[ev] || []).push(cb); },
    emit: function () {},
  };
  var _fire = function (ev, arg) {
    (_handlers[ev] || []).forEach(function (cb) { cb(arg); });
  };
${PAGE_EXPOSE[page] ? '  var _drive;' : ''}
${PAGE_EXPOSE[page] ? exposeInside(iife, PAGE_EXPOSE[page])
   : PAGE_PUBLISH[page] ? publishInside(iife, PAGE_PUBLISH[page]) : iife}
  window.__runLive_${page} = window.__runLiveDns = function (payload, extra) {
    // Extras first: they are state the payload is then rendered against.
    if (extra) Object.keys(extra).forEach(function (ev) { _fire(ev, extra[ev]); });
${PAGE_DRIVER[page] || `    _fire('${PAGE_EVENT[page] || page + ':update'}', payload);`}
  };
}());
`;
  // Parsed here rather than discovered broken in a browser console.
  new Function(body);

  // AND checked for references it cannot satisfy. Syntax is not the failure mode
  // that matters: the live app grows a shared helper, a renderer starts calling
  // it, this harness keeps extracting cleanly, and the only symptom is a
  // ReferenceError at compare time — which looks exactly like the port
  // rendering differently. `_`-prefixed names are app.js's convention for its
  // shared helpers, so an undefined one is the signal.
  // EVERY NAME IN A DECLARATION, not just the first. `var _tipCc=null,
  // _mapWrapRect=null;` declares two, and a regex anchored on the keyword sees
  // one — which made the widened scan below report five perfectly well-declared
  // variables on the Connections page. The declarator list is split on commas at
  // depth zero, so an initialiser containing a comma — `var a = f(x, y), b` —
  // does not split the wrong thing.
  const defined = new Set(body.match(/function\s+([_a-zA-Z][\w]*)/g)?.map(
    (m) => m.replace(/function\s+/, '')) || []);
  for (const m of body.matchAll(/\b(?:var|let|const)\s+([^;\n]*)/g)) {
    let depth = 0;
    let start = 0;
    const list = m[1];
    for (let i = 0; i <= list.length; i++) {
      const c = list[i];
      if (c === '(' || c === '[' || c === '{') depth++;
      else if (c === ')' || c === ']' || c === '}') depth--;
      if (i === list.length || (c === ',' && depth === 0)) {
        const name = list.slice(start, i).trim().match(/^([_a-zA-Z$][\w$]*)/);
        if (name) defined.add(name[1]);
        start = i + 1;
      }
    }
  }
  // BARE REFERENCES COUNT, NOT ONLY CALLS. The first version of this scan matched
  // `_name(` — a call — and a renderer that merely READS a shared variable
  // slipped through it. `_displayTimezone` did exactly that on the Reports page:
  // the module built cleanly, threw at compare time, had its error swallowed by
  // the page's own catch, and the comparison then reported ZERO DIFFERENCES for a
  // port with a deliberate mutation in it. A gate that cannot fail is worse than
  // no gate, and this is how one was nearly shipped.
  //
  // `\b` after the name so `_foo` does not match inside `_foobar`, and the
  // leading class still excludes property access like `x._foo`.
  const missing = [...new Set([...body.matchAll(/(?:^|[^.\w])(_[a-z][\w]*)\b/g)].map((m) => m[1]))]
    .filter((n) => !defined.has(n));
  if (missing.length) {
    console.error('the extracted renderer calls helpers this harness does not define: ' +
                  missing.join(', ') + '\nadd them to HELPERS in ' + __filename);
    process.exit(1);
  }

  fs.mkdirSync(OUT, { recursive: true });
  const file = path.join(OUT, 'live-' + page + '.js');
  fs.writeFileSync(file, body);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), file) +
              ' (' + iife.split('\n').length + ' renderer lines, ' + helpers.length + ' helpers)');
}

main();
