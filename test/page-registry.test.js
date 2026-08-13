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

// ── Registry integrity ───────────────────────────────────────────────────────

test('page keys are unique and non-empty', () => {
  assert.strictEqual(new Set(Pages.KEYS).size, Pages.KEYS.length, 'duplicate page key');
  for (const k of Pages.KEYS) assert.match(k, /^[a-z]{2,20}$/, k + ' must match the page:focus guard');
});

test('the four role-only pages are the ones with no install toggle', () => {
  // These have no Settings switch, so a role is the only thing that can hide
  // them. If this list changes, applyPageVisibility's conjunction changes too.
  const noToggle = Pages.PAGES.filter(p => !p.settingsKey).map(p => p.key).sort();
  assert.deepStrictEqual(noToggle, ['dashboard', 'reports', 'routers', 'settings']);
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
  // Suspend/resume is an efficiency mechanism, not a security boundary, and it
  // only exists for the five collectors that hold an open counter stream.
  assert.deepStrictEqual(Object.keys(Pages.STREAM_ROOMS).sort(),
    ['firewall', 'routing', 'topology', 'vpn', 'wireless']);
  for (const [page, rooms] of Object.entries(Pages.STREAM_ROOMS)) {
    assert.ok(rooms.includes('page-' + page), page + ' must watch its own page room');
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
