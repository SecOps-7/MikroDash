'use strict';
// The page registry is the one definition of a page (issue #108, Phase 1).
//
// A page has to be spelled the same way in six places: src/pages.js, the
// page* key in Settings.DEFAULTS, the two allow-lists in src/index.js, the nav
// markup, and PAGE_TITLES / PAGE_NAV_MAP in app.js. Nothing checked that they
// agreed, and pageTopology was missing from two of them for a whole release —
// the Topology toggle was never persisted and never broadcast.
//
// src/index.js now spreads Pages.SETTING_KEYS rather than restating the keys, so
// the server side cannot drift by construction. The client cannot require() a
// server module (no build step), so its two maps are checked by source scan —
// which is also how the nav markup is tied back to the registry.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const path     = require('node:path');

const Pages    = require('../src/pages');
const Settings = require('../src/settings');
const { COLLECTORS } = require('../src/collection');

const root     = path.join(__dirname, '..');
const INDEX_JS = fs.readFileSync(path.join(root, 'src', 'index.js'), 'utf8');
const APP_JS   = fs.readFileSync(path.join(root, 'public', 'app.js'), 'utf8');
const HTML     = fs.readFileSync(path.join(root, 'public', 'index.html'), 'utf8');
const PREFLIGHT = fs.readFileSync(path.join(root, 'public', 'preflight.js'), 'utf8');

// ── Registry integrity ───────────────────────────────────────────────────────

test('page keys are unique and non-empty', () => {
  assert.strictEqual(new Set(Pages.KEYS).size, Pages.KEYS.length, 'duplicate page key');
  for (const k of Pages.KEYS) assert.match(k, /^[a-z]{2,20}$/, k + ' must match the page:focus guard');
});

test('the three role-only pages are the ones with no install toggle', () => {
  // These have no Settings switch, so a role is the only thing that can hide
  // them. If this list changes, applyPageVisibility's conjunction changes too.
  const noToggle = Pages.PAGES.filter(p => !p.settingsKey).map(p => p.key).sort();
  // Routers left this list when the view presets landed: it is Advanced-only,
  // which needs a real toggle for the preset to switch.
  assert.deepStrictEqual(noToggle, ['dashboard', 'reports', 'settings']);
});

test('every collector names a real page, or none at all', () => {
  for (const c of COLLECTORS) {
    assert.ok('page' in c, c.key + ' is missing the page field');
    if (c.page !== null) {
      assert.ok(Pages.BY_KEY[c.page], c.key + ' names unknown page ' + c.page);
    }
  }
});

test('only the genuinely page-less collectors have a null page', () => {
  // traffic and system drive the header gauges on every page; arp emits nothing
  // and only feeds other collectors. Anything else acquiring a null page is a
  // collector nobody can be granted or denied.
  const pageless = COLLECTORS.filter(c => c.page === null).map(c => c.key).sort();
  assert.deepStrictEqual(pageless, ['arp', 'system', 'traffic']);
});

// ── Server-side derivation ───────────────────────────────────────────────────

test('the registry and Settings agree on which page toggles exist', () => {
  const fromSettings = Object.keys(Settings.DEFAULTS).filter(k => /^page[A-Z]/.test(k)).sort();
  assert.deepStrictEqual([...Pages.SETTING_KEYS].sort(), fromSettings);
});

test('page toggles default to visible', () => {
  // A new page must not be invisible until someone finds the setting.
  for (const k of Pages.SETTING_KEYS) {
    assert.strictEqual(Settings.DEFAULTS[k], true, k + ' should default to true');
  }
});

test('index.js derives both page allow-lists rather than restating them', () => {
  // This is what makes the pageTopology class of bug impossible: neither list
  // can omit a key it does not name. If someone re-inlines the literals, the
  // spread disappears and this fails.
  const broadcast = INDEX_JS.slice(INDEX_JS.indexOf('const _PAGE_SETTING_KEYS'));
  assert.match(broadcast.slice(0, 400), /\.\.\.Pages\.SETTING_KEYS/,
    '_PAGE_SETTING_KEYS must spread Pages.SETTING_KEYS');

  const saved = INDEX_JS.slice(INDEX_JS.indexOf('const boolFields'));
  assert.match(saved.slice(0, 400), /\.\.\.Pages\.SETTING_KEYS/,
    'boolFields must spread Pages.SETTING_KEYS');
});

test('the stream-room map is derived and covers only suspendable pages', () => {
  assert.match(INDEX_JS, /const _PAGE_STREAM_ROOMS = Pages\.STREAM_ROOMS;/);
  // Suspend/resume is an efficiency mechanism, not a security boundary.
  //
  // Pinned as a LIST rather than a count, because the failure it guards against
  // is a page-scoped collector quietly declaring [] and polling the router from
  // the Dashboard forever — which is what nine of them did. A page whose
  // collector reads the router on a timer belongs here; the exceptions are the
  // ones with no page of their own.
  assert.deepStrictEqual(Object.keys(Pages.STREAM_ROOMS).sort(),
    ['bridges', 'capsman', 'dns', 'firewall', 'packages', 'ppp', 'queues',
     'rosusers', 'routing', 'topology', 'vlans', 'vpn', 'wan', 'wireless']);
  for (const [page, rooms] of Object.entries(Pages.STREAM_ROOMS)) {
    assert.ok(rooms.includes('page-' + page), page + ' must watch its own page room');
  }

  // Every page-scoped collector must be reachable by the room-driven sweep,
  // which indexes the session by the page key.
  const { COLLECTORS } = require('../src/collection');
  for (const page of Object.keys(Pages.STREAM_ROOMS)) {
    const col = COLLECTORS.find(c => c.page === page && c.sessionProp === page);
    assert.ok(col, page + ' declares stream rooms but has no collector at session.' + page);
  }

  // And _idleResume must not resume any of them by name, which would defeat the
  // gate: a collector resumed unconditionally polls whether or not its page is
  // being viewed. Only the three page-less collectors may appear there.
  const at = INDEX_JS.indexOf('function _idleResume');
  const body = INDEX_JS.slice(at, INDEX_JS.indexOf('\n}', at));
  for (const page of Object.keys(Pages.STREAM_ROOMS)) {
    assert.ok(!body.includes('session.' + page + '.resume()'),
      '_idleResume resumes ' + page + ' by name, defeating its page gate');
  }
});

// ── Client agreement (source scan — no build step to share the module) ───────

/** Keys of an object literal like `var PAGE_TITLES = {a:'A',b:'B'};` */
function objectKeys(declaration, src) {
  const at = src.indexOf(declaration);
  assert.notStrictEqual(at, -1, 'could not find ' + declaration);
  const open  = src.indexOf('{', at);
  const close = src.indexOf('};', open);
  return [...src.slice(open, close).matchAll(/([A-Za-z_]\w*)\s*:/g)].map(m => m[1]);
}

test('app.js PAGE_TITLES covers exactly the registry pages', () => {
  assert.deepStrictEqual(objectKeys('var PAGE_TITLES', APP_JS).sort(), [...Pages.KEYS].sort());
});

test('app.js PAGE_NAV_MAP covers exactly the toggleable pages', () => {
  // PAGE_NAV_MAP is keyed by settings key, valued by page key. It governs which
  // nav items the settings:pages broadcast can hide, so it must match the
  // registry's toggleable subset exactly — no more, no less.
  const at    = APP_JS.indexOf('var PAGE_NAV_MAP');
  const block = APP_JS.slice(APP_JS.indexOf('{', at), APP_JS.indexOf('};', at));
  const pairs = [...block.matchAll(/(page[A-Z]\w*)\s*:\s*'([a-z]+)'/g)].map(m => [m[1], m[2]]);

  const expected = Pages.PAGES.filter(p => p.settingsKey).map(p => [p.settingsKey, p.key]);
  assert.deepStrictEqual(pairs.sort(), expected.sort());
});

test('every nav item and page container matches a registry page', () => {
  const nav = [...HTML.matchAll(/class="nav-item[^"]*"\s+data-page="([a-z]+)"/g)].map(m => m[1]);
  assert.deepStrictEqual([...new Set(nav)].sort(), [...Pages.KEYS].sort(),
    'nav items and registry pages must be the same set');

  for (const k of Pages.KEYS) {
    assert.ok(HTML.includes('id="page-' + k + '"'), 'missing #page-' + k + ' container');
  }
});

test('the signed-in user chip is not navigation, and cannot be swept away', () => {
  // History, because the fix looks like nothing: the chip used to carry
  // data-page="settings". That made it a match for the nav sweep, which hid it
  // for every role without Settings access — everyone but Administrator —
  // taking the username and the sign-out button with it and leaving no way to
  // log out at all. It was then exempted by name inside the sweep.
  //
  // The exemption is gone because it is no longer needed: the chip carries no
  // data-page at all, so the sweep's selector cannot match it in the first
  // place. That is the stronger guarantee, and it is what this pins. Who you
  // are signed in as, and the ability to stop being signed in, are not
  // permissions.
  assert.ok(/id="authUserChip"/.test(HTML), 'the user chip is gone');

  const chipAt = HTML.indexOf('id="authUserChip"');
  const chip   = HTML.slice(chipAt, chipAt + 900);
  assert.ok(/class="nav-item"/.test(chip), 'the chip still wears .nav-item for the sidebar styling');
  assert.ok(!/data-page=/.test(chip.slice(0, chip.indexOf('</div>'))),
    'the chip must carry no data-page — that attribute is what made the sweep able to hide it');

  // …and because it has no data-page, the generic nav click loop must skip it,
  // or clicking it would call showPage(undefined) and blank the page.
  // Anchored on the navigation itself: several loops iterate .nav-item (showPage
  // clears the active class with one), and only the one that navigates matters.
  const navAt = APP_JS.indexOf('showPage(item.dataset.page)');
  assert.ok(navAt > -1, 'the nav click loop moved');
  const loopAt = APP_JS.lastIndexOf("document.querySelectorAll('.nav-item').forEach", navAt);
  const loop   = APP_JS.slice(loopAt, navAt);
  assert.ok(/closest\('#authUserChip'\)/.test(loop),
    'the nav click loop must skip the chip before it navigates; the chip opens the account modal instead');
});

test('the account modal replaced the My Alerts settings tab', () => {
  assert.ok(!/id="stabMyAlerts"/.test(HTML),  'the My Alerts tab button should be gone');
  assert.ok(!/id="stab-mynotify"/.test(HTML), 'the My Alerts tab panel should be gone');
  assert.ok(/id="accountModal"/.test(HTML),   'the account modal is missing');
  assert.ok(/id="acctMyAlerts"/.test(HTML),   'the modal has no My Alerts section');

  // Relocated, not deleted — the per-user fields and their JS must still pair up.
  for (const id of ['un_telegramBotToken', 'un_emailTo', 'saveUserNotifyBtn', 'btn-un-test-ntfy']) {
    assert.ok(HTML.includes('id="' + id + '"'), id + ' was lost in the move');
  }
  // A user picks where their alerts go, never which alerts exist, and never how
  // mail is sent. Both would be a second answer to a question the install owns.
  for (const id of ['un_notifCpu', 'un_notifIfaceWlan', 'un_smtpHost', 'un_smtpPass']) {
    assert.ok(!HTML.includes('id="' + id + '"'), id + ' must not be a per-user field');
  }
  const at = APP_JS.indexOf('function _applyMyAlertsTab');
  assert.ok(at > -1, '_applyMyAlertsTab is gone');
  assert.ok(/acctMyAlerts/.test(APP_JS.slice(at, at + 700)),
    '_applyMyAlertsTab must gate the modal section, not the removed tab');

  // Escape and backdrop-click come from the shared principal-modal handlers.
  assert.ok(/_PRINCIPAL_MODALS = \[[^\]]*'accountModal'/.test(APP_JS),
    'accountModal must join _PRINCIPAL_MODALS or it cannot be closed with Escape');
});

test('the Settings page is closed to non-admins, not merely hidden', () => {
  // showPage() had no permission check at all, so hiding the nav link was
  // cosmetic — showPage('settings') from the console rendered the whole admin
  // page. The server refused every write, but the page had no business drawing.
  const at   = APP_JS.indexOf('function showPage(');
  const body = APP_JS.slice(at, at + 500);
  assert.ok(/_settingsAllowed\(\)/.test(body), 'showPage must consult _settingsAllowed()');

  const pred = APP_JS.slice(APP_JS.indexOf('function _settingsAllowed'), APP_JS.indexOf('function showPage('));
  assert.ok(/manageSettings/.test(pred) && /managePrincipals/.test(pred),
    'the predicate must match the condition that shows #settingsNavItem, or nav and page disagree');
  // Unknown caps must permit: they arrive after first paint, and denying during
  // that gap would bounce a genuine administrator out of Settings.
  assert.ok(/if \(!window\._caps\) return true/.test(pred),
    'unknown caps must permit — otherwise an admin is locked out during the async gap');
});

// ── Canned view presets ──────────────────────────────────────────────────────
//
// Home / Standard / Advanced are bulk-editors for the Visible Pages toggles and
// for a role's page matrix. They decide nothing on their own — RBAC is still the
// ceiling — but a preset that names a page which cannot be toggled, or that
// skips a tier, is wrong in a way nothing else would notice.

test('every preset names only pages that have an install toggle', () => {
  // A preset naming `dashboard` or `settings` would look right in the list and
  // do nothing: those pages have no switch to set.
  for (const [tier, pages] of Object.entries(Pages.VIEW_PRESETS)) {
    for (const key of pages) {
      const def = Pages.BY_KEY[key];
      assert.ok(def, tier + ' names an unknown page: ' + key);
      assert.ok(def.settingsKey, tier + ' names ' + key + ', which has no install toggle');
    }
    assert.strictEqual(new Set(pages).size, pages.length, tier + ' has no duplicates');
  }
});

test('the presets nest, and Advanced is every toggleable page', () => {
  const { home, standard, advanced } = Pages.VIEW_PRESETS;
  // Nesting is the whole meaning of the tiers. Without it a page could land in
  // Home but not Standard, and "step up a level" would take something away.
  for (const k of home)     assert.ok(standard.includes(k), 'standard must include home\'s ' + k);
  for (const k of standard) assert.ok(advanced.includes(k), 'advanced must include standard\'s ' + k);
  assert.deepStrictEqual([...advanced].sort(),
    Pages.PAGES.filter(p => p.settingsKey).map(p => p.key).sort(),
    'advanced is every toggleable page — derived, so a new page joins it by existing');
  assert.ok(home.length < standard.length && standard.length < advanced.length,
    'the tiers must actually differ');
});

test('Routers is Advanced-only', () => {
  // The reason that page gained a toggle at all: fleet management is a pro
  // feature, so it must not appear in the two lower tiers.
  assert.ok(!Pages.VIEW_PRESETS.home.includes('routers'));
  assert.ok(!Pages.VIEW_PRESETS.standard.includes('routers'));
  assert.ok(Pages.VIEW_PRESETS.advanced.includes('routers'));
  assert.strictEqual(Pages.BY_KEY.routers.settingsKey, 'pageRouters');
});

test('app.js mirrors the preset definition in src/pages.js', () => {
  // Same drift guard as ALL_NAV_PAGES: the browser needs its own copy, and two
  // copies of a list is exactly how pageTopology went missing for a release.
  const m = APP_JS.match(/var VIEW_PRESETS = \{([\s\S]*?)\n  \};/);
  assert.ok(m, 'found VIEW_PRESETS in app.js');
  const read = (tier) => {
    const at = m[1].indexOf(tier + ':');
    assert.ok(at !== -1, 'app.js VIEW_PRESETS has a ' + tier + ' tier');
    const chunk = m[1].slice(at, m[1].indexOf('\n', m[1].indexOf(']', at)) + 1);
    return [...chunk.matchAll(/'([a-z]+)'/g)].map(x => x[1]).sort();
  };
  assert.deepStrictEqual(read('home'),     [...Pages.VIEW_PRESETS.home].sort());
  assert.deepStrictEqual(read('standard'), [...Pages.VIEW_PRESETS.standard].sort());
  // advanced is derived on both sides rather than listed, so there is nothing to
  // compare beyond it being derived — assert that, so nobody hand-writes it.
  assert.ok(/advanced:\s*null/.test(m[1]) && /VIEW_PRESETS\.advanced = /.test(APP_JS),
    'advanced must be derived in app.js, not typed out');
});

test('a preset cannot widen what a role allows', () => {
  // The security claim of the whole feature. Presets write install toggles;
  // _pageAllowed() ANDs the role, so turning everything on grants nothing.
  const src  = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
  const at   = src.indexOf('function _pageAllowed(');
  const body = src.slice(at, at + 700);
  assert.ok(/Rbac\.canPage\(/.test(body), '_pageAllowed must still consult the role');
  assert.ok(/settingsKey\] === false\) return false;/.test(body),
    'the install toggle must only be able to subtract');
  // And the preset UI must never write role state.
  const presetJs = APP_JS.slice(APP_JS.indexOf('function _applyViewPreset'), APP_JS.indexOf('function _applyViewPreset') + 800);
  assert.ok(!/fetch\(/.test(presetJs), 'applying a preset must not call the server by itself');
});

// ── Nav categories ───────────────────────────────────────────────────────────
// The sidebar groups 23 pages into 7 collapsible categories. src/pages.js owns
// the taxonomy; the markup, the CSS and the route all mirror it, and these are
// what stop those mirrors drifting.

test('every page declares a nav category from the registry vocabulary', () => {
  for (const p of Pages.PAGES) {
    assert.ok('category' in p, p.key + ' is missing the category field');
    if (p.category !== null) {
      assert.ok(Pages.CATEGORY_KEYS.includes(p.category),
        p.key + ' names unknown category ' + p.category);
    }
  }
  // The five at top level are a decision, not an accident — a new page landing
  // here silently means somebody forgot to file it.
  const top = Pages.PAGES.filter(p => p.category === null).map(p => p.key).sort();
  assert.deepStrictEqual(top, ['audit', 'dashboard', 'reports', 'routers', 'settings']);
  // An empty category is a header the visibility sweep has to hide and nobody
  // meant to write.
  for (const c of Pages.CATEGORY_KEYS) {
    assert.ok(Pages.PAGES.some(p => p.category === c), 'category ' + c + ' has no pages');
  }
});

test('the registry is ordered as the nav renders it', () => {
  // The docblock has always claimed this. Grouping makes it checkable: pages of
  // one category must be contiguous, and the categories must appear in
  // CATEGORIES order, or reading the registry no longer tells you what the
  // sidebar looks like.
  const seen = [];
  for (const p of Pages.PAGES) {
    if (p.category === null) continue;
    if (seen[seen.length - 1] !== p.category) {
      assert.ok(!seen.includes(p.category), p.category + ' is split into two runs');
      seen.push(p.category);
    }
  }
  assert.deepStrictEqual(seen, [...Pages.CATEGORY_KEYS]);
});

test('the nav markup groups pages exactly as the registry says', () => {
  // Order- and nesting-independent: the category is on the LEAF, so this cannot
  // be fooled by where a </div> happens to close a group.
  const nav = [...HTML.matchAll(/class="nav-item[^"]*"\s+data-page="([a-z]+)"(?:\s+data-cat="([a-z]+)")?/g)];
  const fromMarkup   = Object.fromEntries(nav.map(m => [m[1], m[2] || null]));
  const fromRegistry = Object.fromEntries(Pages.PAGES.map(p => [p.key, p.category]));
  assert.deepStrictEqual(fromMarkup, fromRegistry,
    'nav markup and src/pages.js disagree about which category a page is in');

  const wrappers = [...HTML.matchAll(/class="nav-group" data-cat="([a-z]+)"/g)].map(m => m[1]);
  assert.deepStrictEqual(wrappers, [...Pages.CATEGORY_KEYS],
    'group wrappers must match the registry categories, in order');
  for (const c of Pages.CATEGORIES) {
    assert.ok(HTML.includes('<span class="nav-label">' + c.title + '</span>'),
      c.key + ' header does not carry its registry title "' + c.title + '"');
  }
});

test('a category header is chrome, not a page', () => {
  // Three separate mechanisms key on .nav-item[data-page]: the drift regex
  // above, applyPageVisibility's sweep, and the mobile drawer-closing loop. A
  // header wearing that class would be hidden by the sweep and would slam the
  // drawer shut every time somebody expanded a category — the same failure the
  // signed-in user chip had, for the same reason.
  assert.ok(/class="nav-group-hdr"/.test(HTML), 'no group headers found');
  assert.ok(!/class="[^"]*\bnav-item\b[^"]*\bnav-group-hdr\b|class="[^"]*\bnav-group-hdr\b[^"]*\bnav-item\b/.test(HTML),
    'a header must not also be a .nav-item');
  assert.ok(!/class="nav-group-hdr"[^>]*data-page=/.test(HTML), 'a header must carry no data-page');
  // aria-expanded is the disclosure contract, and with a screen reader the
  // 52px/190px rail distinction does not exist — it is the only affordance there.
  const n = (HTML.match(/class="nav-group-hdr"[^>]*aria-expanded=/g) || []).length;
  assert.strictEqual(n, Pages.CATEGORY_KEYS.length, 'every header needs aria-expanded');
});

test('the header click loop is separate from the nav-item loop', () => {
  // Kept apart deliberately: headers navigate nowhere, and folding them into the
  // .nav-item loop would call showPage(undefined).
  assert.ok(APP_JS.includes("document.querySelectorAll('.nav-group-hdr').forEach"),
    'the header click loop is gone');
  const at = APP_JS.indexOf("document.querySelectorAll('.nav-group-hdr').forEach");
  const body = APP_JS.slice(at, at + 700);
  assert.ok(!/showPage\(/.test(body), 'expanding a category must not navigate');
});

test('preflight paints the cached nav state without holding a category list', () => {
  // preflight.js runs before the nav parses and is the only thing that can stop
  // the sidebar painting in one shape and rearranging. It must stay
  // vocabulary-free: a copy of the taxonomy in a file with no module system is
  // one nothing could keep honest.
  assert.match(PREFLIGHT, /data-nav/, 'preflight must set the grouped/flat attribute');
  assert.match(PREFLIGHT, /navBoot/, 'preflight must paint the open categories');
  for (const c of Pages.CATEGORY_KEYS) {
    assert.ok(!PREFLIGHT.includes("'" + c + "'") && !PREFLIGHT.includes('"' + c + '"'),
      'preflight.js names the category ' + c + ' — it must pass tokens through, not know them');
  }
});

test('the nav-prefs route validates categories and inherits no page guard', () => {
  const at = INDEX_JS.indexOf("app.post('/api/nav-prefs'");
  assert.ok(at > -1, 'the /api/nav-prefs POST is gone');
  const body = INDEX_JS.slice(at, at + 1400);
  assert.match(body, /Pages\.CATEGORY_KEYS/,
    'the expanded list must be filtered through the registry, not persisted as sent');
  // Unlike /api/dashboard-layout this must NOT require a page: every signed-in
  // user has a sidebar, including one whose role grants a single page.
  assert.ok(!/canPageAnywhere|requirePage|requireGlobalAdmin/.test(body),
    'a personal nav preference must not inherit a page guard');
});

test('both copies of the grouping toggle share one handler', () => {
  // Settings is unreachable without manageSettings/managePrincipals, so the
  // account-modal copy is what lets a Read Only user turn grouping off at all.
  const inputs = (HTML.match(/class="nav-grouped-input"/g) || []).length;
  assert.strictEqual(inputs, 2, 'expected the Settings and account-modal toggles');
  assert.ok(APP_JS.includes("document.querySelectorAll('.nav-grouped-input')"),
    'the toggle handler must bind by class, so both copies stay in step');
});

test('a category can be collapsed even while it holds the current page', () => {
  // Two ways this broke, both silently:
  //
  //  1. _navRender derived the open category from the active page on every
  //     render, so a collapse was undone by the very next render.
  //  2. The click keyed on membership of _navExpanded. An auto-expanded category
  //     is open WITHOUT being in that array, so the first click pushed it —
  //     expanding an already-open group and taking two clicks to shut.
  //
  // Both are invisible to a source scan unless it looks for the specific shape
  // that fixes them: a module-level _navAutoCat that the toggle consults and
  // clears.
  assert.match(APP_JS, /var _navAutoCat = null;/, '_navAutoCat must be state, not re-derived');
  assert.match(APP_JS, /function _navRender\(\)/,
    '_navRender must read _navAutoCat rather than take the active category as an argument');

  const at   = APP_JS.indexOf("document.querySelectorAll('.nav-group-hdr').forEach");
  const body = APP_JS.slice(at, at + 1200);
  assert.match(body, /if \(at !== -1 \|\| _navAutoCat === cat\)/,
    'the toggle must key on what is rendered — saved OR auto-expanded — not on membership alone');
  assert.match(body, /if \(_navAutoCat === cat\) _navAutoCat = null;/,
    'collapsing must clear the auto-expand, or the next render puts it back');
});

test('auto-expanding a category is never saved', () => {
  // Otherwise visiting one page in each category leaves every category open for
  // good, which is grouping that undoes itself.
  const at   = APP_JS.indexOf('function showPage(');
  const body = APP_JS.slice(at, APP_JS.indexOf('\n}', at));
  assert.match(body, /_navAutoCat = navGrp\.dataset\.cat/, 'navigation sets the auto-expand');
  assert.ok(!/_navSave\(/.test(body), 'navigation must not persist the auto-expand');
});
