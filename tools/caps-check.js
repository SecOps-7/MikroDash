'use strict';
/**
 * Page visibility and capability gating, live against ported.
 *
 * This is the chrome's PERMISSION layer, so the interesting cases are the ones
 * where being wrong is a disclosure or a lockout rather than a cosmetic slip:
 *
 *   unknown must not hide    `_pageAccess` is null until caps land, and the role
 *                            half is skipped while it is. A port that treated
 *                            null as "deny" would blank the sidebar on every
 *                            first paint.
 *   unknown must not bounce  `_settingsAllowed()` PERMITS while caps are
 *                            unknown, so an administrator is not thrown off
 *                            Settings during the gap — and applyCaps re-checks
 *                            once they are known, which is the other half of
 *                            that bargain and the half easy to leave out.
 *   the move-off target      is the FIRST STILL-VISIBLE page, not the dashboard,
 *                            because a role can deny the dashboard too.
 *   hide versus disable      `data-cap-disable` disables in place; everything
 *                            else is hidden outright. Getting these the wrong
 *                            way round either shows a control that 403s or
 *                            hides a page someone may legitimately read.
 *   idempotence              every governed element is SET from the caps given,
 *                            never toggled — that is what lets a re-run after a
 *                            403 be safe.
 *
 * ── WHAT IS DELIBERATELY NOT COMPARED ───────────────────────────────────────
 *
 * The live `applyPageVisibility` ends with assignments this port has no
 * consumers for yet: alert thresholds, the alert-type maps, the ping section and
 * the My Alerts tab. They are not ported, and `KNOWN_INCOMPLETE` below asserts
 * that the gap STILL EXISTS — so porting the alert feed fails this gate and
 * forces the note to be removed rather than left lying.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/caps-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('caps-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const TABLE = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'pages-table.json'), 'utf8'));
const PRESETS = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'view-presets.json'), 'utf8'));
const ALL_NAV_PAGES = TABLE.allNavPages;
const CATS = TABLE.categories.map((c) => c.key);

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}

const visSrc = G.value('visSrc', () => slice('function applyPageVisibility(pages) {', '\n}', 'applyPageVisibility'));
const capsSrc = G.value('capsSrc', () => slice('  function applyCaps(caps) {', '\n  }', 'applyCaps'));
// applyCaps closes over five element references taken once at the top of its
// IIFE. Lifted with it rather than rebound by hand, so a rename upstream breaks
// the slice instead of quietly comparing against the wrong element.
//
// (The port re-queries inside the function instead of caching. With a static
// shell that is the same thing; it is a mechanism difference, not a behavioural
// one, and it is why the world below has every element present from the start.)
// FROZEN. A multi-line `slice(...)` call — freeze-src.py only matches
// single-line ones.
const capsRefsSrc = G.value('capsRefsSrc', () => slice("  var chip        = document.getElementById('authUserChip');",
  "  var saveSettBtn = document.getElementById('settingsSaveBtn');", 'the applyCaps element refs'));
const allowedSrc = G.value('allowedSrc', () => slice('function _settingsAllowed() {', '\n}', '_settingsAllowed'));
// Ported at Part 19, so it is lifted and run rather than stubbed. Its auth-mode
// test is `!== 'none'` on purpose: `_authMode` lands after the first
// settings:pages, so an equality test would read undefined and hide the section
// for good. Both halves of that are compared below.
const myAlertsSrc = G.value('myAlertsSrc', () => slice('function _applyMyAlertsTab(enabled) {', '\n}', '_applyMyAlertsTab'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['visSrc', visSrc], ['capsSrc', capsSrc], ['allowedSrc', allowedSrc], ['myAlertsSrc', myAlertsSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

// ── The documented gap ──────────────────────────────────────────────────────
//
// Each of these is a line applyPageVisibility runs that the port does not. The
// assertion is that they are STILL THERE: when one is ported, this fails and
// the entry has to be removed deliberately.
// Each entry is [live needle, why, port marker].
//
// THE THIRD FIELD IS NEW, and it is what makes this record fail in BOTH
// directions. The first version asserted only that the line was still in the
// LIVE function — so a gap closed on the PORT side fired nothing, and
// `vpnDashTopN` sat here as an open gap for a tick after `caps.ts` started
// reading it. The other three audits all check both directions; this one did
// not, and the difference was invisible because it looked green.
const KNOWN_INCOMPLETE = [
  ['_alertCpuThreshold = pages.alertCpuThreshold;', 'the CPU alert threshold (no alert feed yet)',
    'alertCpuThreshold'],
  ['_alertPingLoss     = pages.alertPingLoss;', 'the ping-loss threshold (no ping block yet)',
    'alertPingLoss'],
  ['_alertTypes[AT[f]]', 'the alert-type map (no browser notifications yet)', 'notifIfaceUpDown'],
  ['_alertIfaceTypes[AI[g]]', 'the alert interface-type map (no browser notifications yet)',
    'notifIfaceEther'],
];
const portSrc = fs.readFileSync(path.join(ROOT, 'web', 'src', 'caps.ts'), 'utf8');
const closed = KNOWN_INCOMPLETE.filter(([, , marker]) => portSrc.includes(marker));
if (closed.length) {
  console.error('KNOWN_INCOMPLETE is out of date — web/src/caps.ts now handles these, so they\n' +
    'are no longer gaps. Delete the entry and compare them here:\n' +
    closed.map(([, why, m]) => '  ' + why + '  (caps.ts mentions ' + m + ')').join('\n') + '\n');
  process.exit(1);
}
const stale = KNOWN_INCOMPLETE.filter(([needle]) => !visSrc.includes(needle));
if (stale.length) {
  console.error('KNOWN_INCOMPLETE is out of date — these are no longer in the live function:\n' +
    stale.map(([n, why]) => '  ' + why + '  (' + n + ')').join('\n') +
    '\nIf the port now covers them, remove the entry and compare them here.\n');
  process.exit(1);
}

const OUT = path.join(ROOT, 'testdata', '.caps-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'caps.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

// ── The world ───────────────────────────────────────────────────────────────

function makeStyle() {
  const props = {};
  return {
    _props: props,
    get display() { return props.display === undefined ? '' : props.display; },
    set display(v) { props.display = String(v); },
  };
}

/** `governed`: [id, capName, disableInsteadOfHide][] */
function makeWorld(governed, startPage) {
  const navItems = ALL_NAV_PAGES.map((page) => ({
    dataset: { page }, _page: page, style: makeStyle(),
  }));
  // Two categories' worth of groups, each holding a slice of the nav items, so
  // the "a group whose children are all hidden is chrome" rule is exercised.
  const groups = CATS.map((cat, i) => {
    const mine = navItems.filter((_, n) => n % CATS.length === i);
    return {
      dataset: { cat }, style: makeStyle(),
      querySelectorAll: (sel) => (sel === '.nav-item[data-page]' ? mine : []),
    };
  });
  const capEls = governed.map(([id, cap, disable]) => ({
    _id: id, _cap: cap, style: makeStyle(), disabled: false, title: '',
    getAttribute: (k) => (k === 'data-cap' ? cap : null),
    hasAttribute: (k) => (k === 'data-cap-disable' ? !!disable : false),
  }));
  const named = {
    acctMyAlerts: { style: makeStyle(), dataset: {} },
    rtrAddBtn: { style: makeStyle() },
    settingsSaveBtn: { style: makeStyle(), disabled: false, title: '' },
    settingsNavItem: { style: makeStyle() },
    authUsername: { textContent: '' },
    authUserChip: { style: makeStyle() },
    // The network diagram's ping block. Its four stat ids were always written by
    // this port; only the SECTION's visibility was missing, so an operator with
    // ping switched off saw an empty block where the live app shows nothing.
    // Closed 2026-08-24, and KNOWN_INCOMPLETE above is what demanded the case.
    ndPingSection: { style: makeStyle() },
  };
  const moves = [];
  let notifyLoads = 0;
  let current = startPage;
  const doc = {
    getElementById: (id) => named[id] || null,
    querySelectorAll: (sel) => {
      if (sel === '[data-cap]') return capEls;
      if (sel === '.nav-group') return groups;
      const m = sel.match(/^\.nav-item\[data-page="(.*)"\]$/);
      if (m) return navItems.filter((n) => n._page === m[1]);
      throw new Error('unexpected selector ' + sel);
    },
  };
  return {
    doc, moves,
    /** `throwOnNotify` exists for one case: the flag is set BEFORE the call, so a
     *  loader that throws leaves the panel marked loaded and is not retried.
     *  Swapping those two lines is invisible with a loader that returns
     *  normally, which is how that mutation survived the first pass. */
    throwOnNotify: false,
    countNotifyLoad() { notifyLoads++; if (this.throwOnNotify) throw new Error('loader blew up'); },
    current: () => current,
    go: (p) => { moves.push(p); current = p; },
    state() {
      return JSON.stringify({
        nav: navItems.map((n) => [n._page, n.style.display]),
        groups: groups.map((g) => [g.dataset.cat, g.style.display]),
        caps: capEls.map((e) => [e._id, e.style.display, e.disabled, e.title]),
        rtrAdd: named.rtrAddBtn.style.display,
        save: [named.settingsSaveBtn.style.display, named.settingsSaveBtn.disabled, named.settingsSaveBtn.title],
        settingsNav: named.settingsNavItem.style.display,
        chip: [named.authUserChip.style.display, named.authUsername.textContent],
        myAlerts: [named.acctMyAlerts.style.display, named.acctMyAlerts.dataset.loaded || null, notifyLoads],
        pingSection: named.ndPingSection.style.display,
        moves,
        current,
      }, null, 1);
    },
  };
}

const GOVERNED = [
  ['principalsCard', 'managePrincipals', false],
  ['someWriteBtn', 'manageSettings', true],
  ['routerAdd', 'createRouters', false],
  ['unknownCap', 'noSuchCapability', false],
  ['disabledUnknown', 'alsoNoSuch', true],
];

function liveRun(governed, startPage, body) {
  const w = makeWorld(governed, startPage);
  const ctx = {
    document: w.doc, Object, Array, JSON, String, window: {},
    PAGE_NAV_MAP: PRESETS.navMap, ALL_NAV_PAGES,
    _pageInstall: {}, _pageAccess: null, _routersMultiple: true,
    _currentPage: startPage,
    _alertTypes: {}, _alertIfaceTypes: {},
    _alertCpuThreshold: 90, _alertPingLoss: 100, _vpnDashTopN: 5, _displayTimezone: '',
    _loadUserNotify: () => w.countNotifyLoad(),
    Error,
    showPage: (p) => { w.go(p); ctx._currentPage = p; },
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(myAlertsSrc + '\n' + visSrc + '\n' + allowedSrc + '\nvar __caps = (function(){' + capsRefsSrc +
    '\n' + capsSrc + '\nreturn applyCaps; })();', ctx);
  body(ctx, w);
  return w.state();
}

function portRun(governed, startPage, body) {
  const w = makeWorld(governed, startPage);
  const saved = { document: global.document, _caps: global._caps };
  global.document = w.doc;
  delete global._caps;
  // THE PORT NO LONGER READS `_loadUserNotify`, and that is the point of the
  // change that broke this: the global was published by the LIVE app
  // (`window._loadUserNotify = loadUserNotify` in app.js), so the tab worked
  // only because the Node script was still on the page. `web/src/pages/usernotify.ts`
  // is the port's own module now.
  //
  // So the count comes from the OBSERVABLE behaviour instead — a request to the
  // endpoint — which is what the live global stood for anyway and cannot go
  // stale the same way. The stub is left in place for the LIVE side, which does
  // still read it.
  const savedFetch = global.fetch;
  global.fetch = (url) => {
    if (String(url).startsWith('/api/user-notify')) w.countNotifyLoad();
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) });
  };
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.initCaps({
      current: w.current,
      go: w.go,
      // `serves` answers "can THIS BUILD render the page" — true here, because
      // this harness is exercising the install/role/count rules, not the
      // strangler's hand-off. A false would hide pages for a reason this gate
      // is not about.
      serves: () => true,
    });
    body(mod, w);
  } finally {
    if (saved.document === undefined) delete global.document; else global.document = saved.document;
    delete global._caps;
    if (savedFetch === undefined) delete global.fetch; else global.fetch = savedFetch;
    delete global._authMode;
    if (saved._caps !== undefined) global._caps = saved._caps;
  }
  return w.state();
}

const bad = [];
let cases = 0;
function compare(what, governed, startPage, liveBody, portBody) {
  cases++;
  const a = liveRun(governed, startPage, liveBody);
  const b = portRun(governed, startPage, portBody);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

// ── 1. Install toggles alone, caps never arriving ───────────────────────────
const INSTALLS = [
  ['everything on', {}],
  ['one page off', { pageWifi: false }],
  ['several off', { pageWifi: false, pageDns: false, pageLogs: false }],
  ['a toggle explicitly true', { pageWifi: true }],
  ['a toggle that is null, which is NOT false', { pageWifi: null }],
  ['a toggle that is 0, which is NOT false either', { pageWifi: 0 }],
  ['an unknown settings key', { pageNoSuchThing: false }],
];
for (const [what, pages] of INSTALLS) {
  compare('install: ' + what, GOVERNED, 'dashboard',
    (c) => c.applyPageVisibility(pages), (p) => p.applyPageVisibility(pages));
}
// ── the ping section toggle ─────────────────────────────────────────────────
// `pingEnabled` hides the network diagram's ping block. Every value is a
// DIFFERENT state and the live code separates them with `!= null`: absent leaves
// whatever was there, explicitly false hides, explicitly true shows. A truthiness
// test would collapse absent and false, which is the bug the same guard on
// `displayTimezone` and `vpnDashTopN` exists to avoid.
for (const [what, pages] of [
  ['ping enabled', { pingEnabled: true }],
  ['ping DISABLED hides the block', { pingEnabled: false }],
  ['ping absent leaves the block alone', {}],
  ['pingEnabled null is ABSENT, not false', { pingEnabled: null }],
  ['pingEnabled 0 is false-y and hides', { pingEnabled: 0 }],
  ['pingEnabled 1 shows', { pingEnabled: 1 }],
  ['pingEnabled as the string "false" is TRUTHY', { pingEnabled: 'false' }],
]) {
  compare('ping: ' + what, GOVERNED, 'dashboard',
    (c) => c.applyPageVisibility(pages), (p) => p.applyPageVisibility(pages));
}
// Turning it off and then leaving it absent must NOT bring it back.
compare('ping: off, then a payload that does not mention it', GOVERNED, 'dashboard',
  (c) => { c.applyPageVisibility({ pingEnabled: false }); c.applyPageVisibility({ pageWifi: false }); },
  (p) => { p.applyPageVisibility({ pingEnabled: false }); p.applyPageVisibility({ pageWifi: false }); });

// Standing on a page the install then turns off.
compare('the install hides the page you are standing on', GOVERNED, 'wifi',
  (c) => c.applyPageVisibility({ pageWifi: false }),
  (p) => p.applyPageVisibility({ pageWifi: false }));
// …and on a page that is turned off when the dashboard is ALSO off, so the
// fallback cannot be the dashboard.
compare('hidden page, and the dashboard is hidden too', GOVERNED, 'wifi',
  (c) => c.applyPageVisibility({ pageWifi: false, pageDashboard: false }),
  (p) => p.applyPageVisibility({ pageWifi: false, pageDashboard: false }));

// ── 1b. The My Alerts section ───────────────────────────────────────────────
//
// Two inputs and a lazy load. The auth-mode test is `!== 'none'`, so the case
// that matters most is the one where the mode is NOT YET KNOWN: `_authMode` is
// assigned from /api/auth/status, which lands after the first settings:pages,
// and an equality test against 'modern' would read undefined and hide the
// section permanently.
for (const mode of [undefined, 'modern', 'none', 'legacy']) {
  for (const [what, enabled] of [
    ['enabled', true], ['disabled', false], ['absent', undefined],
    ['truthy but not true', 1], ['the string "true", which is NOT true', 'true'],
  ]) {
    compare('My Alerts ' + what + ' with authMode=' + mode, GOVERNED, 'dashboard',
      (c) => { if (mode === undefined) delete c._authMode; else c._authMode = mode;
               c.applyPageVisibility({ userNotifyEnabled: enabled }); },
      (p) => { if (mode === undefined) delete global._authMode; else global._authMode = mode;
               p.applyPageVisibility({ userNotifyEnabled: enabled }); });
  }
}
// Loaded ONCE, lazily. A second broadcast must not re-request the panel.
compare('My Alerts shown twice loads once', GOVERNED, 'dashboard',
  (c) => { c._authMode = 'modern';
           c.applyPageVisibility({ userNotifyEnabled: true });
           c.applyPageVisibility({ userNotifyEnabled: true }); },
  (p) => { global._authMode = 'modern';
           p.applyPageVisibility({ userNotifyEnabled: true });
           p.applyPageVisibility({ userNotifyEnabled: true }); });
// Hidden, then shown: the load happens when it first becomes visible, not before.
compare('My Alerts hidden then shown', GOVERNED, 'dashboard',
  (c) => { c._authMode = 'modern';
           c.applyPageVisibility({ userNotifyEnabled: false });
           c.applyPageVisibility({ userNotifyEnabled: true }); },
  (p) => { global._authMode = 'modern';
           p.applyPageVisibility({ userNotifyEnabled: false });
           p.applyPageVisibility({ userNotifyEnabled: true }); });
// And a later broadcast with the flag ABSENT hides it again — `undefined` is not
// `true`, so the section goes away rather than sticking on from last time.
compare('My Alerts shown then the flag goes absent', GOVERNED, 'dashboard',
  (c) => { c._authMode = 'modern';
           c.applyPageVisibility({ userNotifyEnabled: true });
           c.applyPageVisibility({ pageWifi: false }); },
  (p) => { global._authMode = 'modern';
           p.applyPageVisibility({ userNotifyEnabled: true });
           p.applyPageVisibility({ pageWifi: false }); });

// The flag is set BEFORE the loader runs, so a loader that throws still counts
// as loaded and is not retried on the next broadcast. Both sides are allowed to
// throw here; what is compared is what they left behind.
compare('My Alerts whose loader throws', GOVERNED, 'dashboard',
  (c, w) => {
    c._authMode = 'modern';
    w.throwOnNotify = true;
    try { c.applyPageVisibility({ userNotifyEnabled: true }); } catch { /* expected */ }
    w.throwOnNotify = false;
    c.applyPageVisibility({ userNotifyEnabled: true });
  },
  (p, w) => {
    global._authMode = 'modern';
    w.throwOnNotify = true;
    try { p.applyPageVisibility({ userNotifyEnabled: true }); } catch { /* expected */ }
    w.throwOnNotify = false;
    p.applyPageVisibility({ userNotifyEnabled: true });
  });

// ── 2. Caps, which add the role half ────────────────────────────────────────
const CAPSETS = [
  ['no caps at all', null],
  ['an empty object', {}],
  ['an administrator', { manageSettings: true, managePrincipals: true, createRouters: true,
    pages: Object.fromEntries(ALL_NAV_PAGES.map((p) => [p, true])) }],
  ['an operator', { createRouters: false, manageSettings: false, managePrincipals: false,
    pages: Object.fromEntries(ALL_NAV_PAGES.map((p) => [p, p !== 'settings' && p !== 'audit'])) }],
  ['a read-only user', { pages: { dashboard: true, logs: true } }],
  ['caps with no pages key at all', { manageSettings: true }],
  ['principals but not settings', { managePrincipals: true }],
  ['settings but not principals', { manageSettings: true }],
];
for (const [what, c] of CAPSETS) {
  compare('caps: ' + what, GOVERNED, 'dashboard',
    (ctx) => ctx.__caps(c), (p) => p.applyCaps(c));
  // Applied twice — idempotence is what makes a re-run after a 403 safe.
  compare('caps twice: ' + what, GOVERNED, 'dashboard',
    (ctx) => { ctx.__caps(c); ctx.__caps(c); }, (p) => { p.applyCaps(c); p.applyCaps(c); });
}
// Standing on Settings when caps say no — the re-check that is easy to omit.
for (const [what, c] of CAPSETS) {
  compare('on Settings when ' + what + ' arrives', GOVERNED, 'settings',
    (ctx) => ctx.__caps(c), (p) => p.applyCaps(c));
}
// Install first, then caps: the order the real app sees them in.
compare('install then caps', GOVERNED, 'dashboard',
  (c) => { c.applyPageVisibility({ pageWifi: false }); c.__caps({ pages: { dashboard: true, wifi: true, logs: true } }); },
  (p) => { p.applyPageVisibility({ pageWifi: false }); p.applyCaps({ pages: { dashboard: true, wifi: true, logs: true } }); });
// Caps first, then a settings broadcast — caps must SURVIVE the re-run.
compare('caps then a later install broadcast', GOVERNED, 'dashboard',
  (c) => { c.__caps({ pages: { dashboard: true, logs: true } }); c.applyPageVisibility({ pageLogs: false }); },
  (p) => { p.applyCaps({ pages: { dashboard: true, logs: true } }); p.applyPageVisibility({ pageLogs: false }); });

// ── 2b. The four things the first pass of mutations could not see ───────────
//
// Each of these survived a deliberate break, which meant the corpus was short —
// not that the behaviour did not matter. Written down because "the mutation
// survived" is the only reliable way to find a gap of this shape.

// (a) settingsAllowed() BEFORE any caps have arrived. Every case above reaches
//     it through applyCaps, which sets the caps first — so the unknown branch,
//     the one that keeps an administrator from being bounced during the gap,
//     was never run.
compare('settingsAllowed with caps still unknown', GOVERNED, 'settings',
  (c, w) => { w.moves.push('allowed=' + c._settingsAllowed()); },
  (p, w) => { w.moves.push('allowed=' + p.settingsAllowed()); });
for (const [what, c] of CAPSETS) {
  compare('settingsAllowed after ' + what, GOVERNED, 'settings',
    (ctx, w) => { ctx.__caps(c); w.moves.push('allowed=' + ctx._settingsAllowed()); },
    (p, w) => { p.applyCaps(c); w.moves.push('allowed=' + p.settingsAllowed()); });
}

// (b) The move-off target when the DASHBOARD is not reachable either. The
//     install cannot hide it — `dashboard` has no settings key — so the only
//     way to reach this is through the role half, which is exactly why the
//     first attempt at this case tested nothing.
//     AND the visible page has to come EARLIER in the sweep than the hidden
//     one. `firstVisible` is filled as the loop walks, so a case standing on an
//     early page with the only survivor late still falls back to the dashboard
//     — which is what the mutation does, so it survived. `dns` is index 10 and
//     `logs` is index 19; standing on logs is what makes the difference visible.
compare('moved off a late page, with an earlier one still visible', GOVERNED, 'logs',
  (c) => {
    c.__caps({ pages: { dns: true, logs: true } });
    c.applyPageVisibility({ pageLogs: false });
  },
  (p) => {
    p.applyCaps({ pages: { dns: true, logs: true } });
    p.applyPageVisibility({ pageLogs: false });
  });
compare('moved off an EARLY page, where nothing precedes it', GOVERNED, 'wifi',
  (c) => {
    c.__caps({ pages: { wifi: true, logs: true, dns: true } });
    c.applyPageVisibility({ pageWifi: false });
  },
  (p) => {
    p.applyCaps({ pages: { wifi: true, logs: true, dns: true } });
    p.applyPageVisibility({ pageWifi: false });
  });
compare('moved off when NOTHING is left visible', GOVERNED, 'wifi',
  (c) => { c.__caps({ pages: { wifi: true } }); c.applyPageVisibility({ pageWifi: false }); },
  (p) => { p.applyCaps({ pages: { wifi: true } }); p.applyPageVisibility({ pageWifi: false }); });

// (c) A capability going from denied to GRANTED. Re-applying the same caps
//     twice cannot see a control that is disabled and never re-enabled, which
//     is what a permissions change or a re-login produces.
for (const [a, b] of [[false, true], [true, false], [true, true], [false, false]]) {
  compare('manageSettings ' + a + ' then ' + b, GOVERNED, 'dashboard',
    (c) => { c.__caps({ manageSettings: a }); c.__caps({ manageSettings: b }); },
    (p) => { p.applyCaps({ manageSettings: a }); p.applyCaps({ manageSettings: b }); });
}
compare('createRouters granted after being denied', GOVERNED, 'dashboard',
  (c) => { c.__caps({ createRouters: false }); c.__caps({ createRouters: true }); },
  (p) => { p.applyCaps({ createRouters: false }); p.applyCaps({ createRouters: true }); });

// (d) A single-router install, where Routers is meaningless. The flag starts
//     true so the nav is not blanked before the router list loads, so nothing
//     above ever set it false.
for (const multiple of [false, true]) {
  compare('routersMultiple=' + multiple, GOVERNED, 'dashboard',
    (c) => { c._routersMultiple = multiple; c.applyPageVisibility({}); },
    (p) => { p.setRoutersMultiple(multiple); });
  compare('routersMultiple=' + multiple + ' while standing on Devices', GOVERNED, 'devices',
    (c) => { c._routersMultiple = multiple; c.applyPageVisibility({}); },
    (p) => { p.setRoutersMultiple(multiple); });
}
// And it COMPOSES with the other two rules rather than overriding them.
compare('a single router AND the role denies routers', GOVERNED, 'dashboard',
  (c) => { c.__caps({ pages: { dashboard: true, routers: true } }); c._routersMultiple = false; c.applyPageVisibility({}); },
  (p) => { p.applyCaps({ pages: { dashboard: true, routers: true } }); p.setRoutersMultiple(false); });

// ── 3. Hide versus disable ──────────────────────────────────────────────────
compare('a governed element with no matching cap', GOVERNED, 'dashboard',
  (c) => c.__caps({ manageSettings: false }), (p) => p.applyCaps({ manageSettings: false }));
compare('a cap that is truthy but not true', GOVERNED, 'dashboard',
  (c) => c.__caps({ managePrincipals: 1, manageSettings: 'yes' }),
  (p) => p.applyCaps({ managePrincipals: 1, manageSettings: 'yes' }));
compare('no governed elements at all', [], 'dashboard',
  (c) => c.__caps({ manageSettings: true }), (p) => p.applyCaps({ manageSettings: true }));

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the capability layer differs from the live one:\n\n' + bad.slice(0, 2).join('\n\n') +
    (bad.length > 2 ? '\n\n… and ' + (bad.length - 2) + ' more' : '') + '\n');
  process.exit(1);
}
console.log(`capability gating matches the live chrome (${cases} cases, ${ALL_NAV_PAGES.length} nav pages, ` +
  `${KNOWN_INCOMPLETE.length} documented gaps still open)`);
