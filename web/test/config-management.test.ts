/**
 * THE CONFIG MANAGEMENT LIBRARY (2026-09-21).
 *
 * The cards are pure renderers; this drives them with templates that carry
 * markup, and drives the page with a fake fetch:
 *
 *   - a template's name, description and scope are text, never markup;
 *   - the lock badge is on lock-class templates only;
 *   - the external generator is last, opens in a new tab, and carries
 *     `noopener noreferrer`, so the site it opens cannot reach back;
 *   - a placeholder is coloured as one, and its text still escaped;
 *   - the page fetches nothing until it is the page being shown.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.cfgmgmt-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { initConfigManagementPage, toLibrary } from '../web/src/pages/config-management.js';",
  "export * as cards from '../web/src/pages/config-management-cards.js';",
  "export * as editor from '../web/src/pages/config-management-editor.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.cfgmgmt.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);
fs.rmSync(OUT, { force: true });
const { cards, editor } = mod;

const tpl = (extra) => ({ id: 'x', name: 'N', description: 'D', category: 'home', kind: 'fragment', canned: true,
  version: 1, lockClass: false, scope: [], variables: 0, tags: [], baseline: null, updatedAt: 0, ...extra });

// ── TEXT IS TEXT ─────────────────────────────────────────────────────────────
const hostile = cards.templateCard(tpl({ id: '"><img src=x>', name: '<b>n</b>', description: '<script>d</script>',
  scope: ['/ip/<i>'] }));
assert.ok(!/<b>|<script>|<img|<i>/.test(hostile), 'a template field reached the markup unescaped');
assert.ok(hostile.includes('&lt;b&gt;n&lt;/b&gt;'), 'the escaped name is shown');

// ── THE LOCK BADGE ───────────────────────────────────────────────────────────
assert.ok(cards.templateCard(tpl({ lockClass: true })).includes('cfg-pill-lock'), 'a lock-class template is not marked');
assert.ok(!cards.templateCard(tpl({ lockClass: false })).includes('cfg-pill-lock'), 'a harmless template is marked');

// ── THE EXTERNAL GENERATOR ───────────────────────────────────────────────────
const grid = cards.libraryGrid([tpl({ id: 'a', name: 'Alpha' }), tpl({ id: 'b', name: 'Beta', category: 'office' })], 'all', '');
const articles = [...grid.matchAll(/<article class="([^"]*)"/g)].map((m) => m[1]);
assert.ok(articles[articles.length - 1].includes('cfg-external'), 'the generator card is not last');
const link = /<a [^>]*href="([^"]*)"[^>]*>/.exec(cards.generatorCard());
assert.ok(link, 'the generator card has no link');
assert.strictEqual(link[1], cards.GENERATOR_URL);
assert.ok(/target="_blank"/.test(link[0]) && /rel="noopener noreferrer"/.test(link[0]),
  'the external link must open in a new tab without a way back: ' + link[0]);

// ── FILTERING ───────────────────────────────────────────────────────────────
const office = cards.libraryGrid([tpl({ id: 'a', name: 'Alpha' }), tpl({ id: 'b', name: 'Beta', category: 'office' })], 'office', '');
assert.ok(office.includes('data-cfg-id="b"') && !office.includes('data-cfg-id="a"'), 'the category filter');
const search = cards.libraryGrid([tpl({ id: 'a', name: 'Alpha' }), tpl({ id: 'b', name: 'Beta', tags: ['wireguard'] })], 'all', 'WIREGUARD');
assert.ok(search.includes('data-cfg-id="b"') && !search.includes('data-cfg-id="a"'), 'search reaches tags, ignoring case');

// ── HIGHLIGHTING ─────────────────────────────────────────────────────────────
const hl = cards.highlight('/ip dns\nset servers={{dns}} comment="<x>"');
assert.ok(hl.includes('<span class="cfg-var">{{dns}}</span>'), 'a placeholder is not marked');
assert.ok(!hl.includes('<x>') && hl.includes('&lt;x&gt;'), 'highlighted text reached the markup unescaped');

// ── BOTH KINDS IN ONE SHAPE ──────────────────────────────────────────────────
const lib = mod.toLibrary(
  [{ id: 's1', name: 'Mine', description: '', kind: 'fragment', scope: '["/ip/dns"]', variables: 'not json',
    revision: 3, baseline: 'canned:dns-and-time@1', updatedAt: 5 }],
  [{ id: 'dns-and-time', name: 'DNS', description: 'd', category: 'home', version: 1, tags: [], variables: [{}, {}],
    lockClass: false, scope: ['/ip/dns'] }],
  { s1: true });
assert.deepStrictEqual(lib.map((t) => [t.id, t.category, t.canned, t.variables, t.lockClass]),
  [['canned:dns-and-time', 'home', true, 2, false], ['s1', 'custom', false, 0, true]]);
assert.deepStrictEqual(lib[1].scope, ['/ip/dns']);

// ── THE EDITOR'S SETTINGS FOLLOW THE TEXT ────────────────────────────────────
assert.deepStrictEqual(editor.usedVars('/ip dns\nset servers={{b}} a={{a}} c={{b}}'), ['b', 'a'], 'first appearance first');
const sync = editor.syncVars([{ name: 'gone', type: 'ipv4', label: 'kept' }],
  '/ip service\nset [ find name={{api_service}} ] address={{net}}', ['mgmt_src', 'api_service', 'api_user']);
assert.deepStrictEqual(sync.defs.map((d) => d.name), ['gone', 'net'],
  'a used placeholder is declared, a removed one kept, and MikroDash\'s own are never asked for');
assert.deepStrictEqual(sync.unused, ['gone'], 'the removed one is reported as unused');
assert.ok(editor.gutter('a\nb\nc', 2).includes('<span class="cfg-ln-bad">2</span>'), 'the refused line is marked');
const secret = editor.varRow({ name: 'pw', type: 'secret', default: 'hunter2' }, ['text', 'secret'], false);
assert.ok(!secret.includes('hunter2') && /data-var-field="default"[^>]*disabled/.test(secret),
  'a secret was offered a default, which would be stored with the template');
const form = editor.captureForm([{ id: 'r1', label: '<b>edge</b>' }], ['/ip/dns']);
assert.ok(!form.includes('<b>edge') && form.includes('&lt;b&gt;edge'), 'a router label reached the markup unescaped');

// ── NOTHING IS FETCHED UNTIL THE PAGE IS SHOWN ──────────────────────────────
const doc = makeDoc(['cfgTabs', 'cfgBadge', 'cfgStats', 'cfgCats', 'cfgSearch', 'cfgLibrary', 'cfgDrawer',
  'cfgDrawerTitle', 'cfgDrawerMeta', 'cfgDrawerBody', 'cfgDrawerClose', 'cfgPanel-library', 'cfgPanel-editor',
  'cfgPanel-deploy', 'cfgPanel-history', 'cfgPanel-drift', 'cfgNew', 'cfgEdNew', 'cfgCapture', 'cfgEdCapture',
  'cfgCaptureBox', 'cfgEdName', 'cfgEdDesc', 'cfgEdBody', 'cfgEdVars', 'cfgEdSave', 'cfgEdDelete', 'cfgEdClose'],
  { allowUnknown: ['#cfgTabs [data-cfgtab]'] });
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout, alert: () => {} };
const fetched = [];
global.fetch = async (url) => {
  fetched.push(url);
  return { ok: true, status: 200, json: async () => ({ ok: true, templates: [], lockClass: {},
    canned: [{ id: 'home-firewall', name: 'Home firewall', description: 'd', category: 'home', version: 1, tags: [],
      variables: [], lockClass: true, scope: ['/ip/firewall/filter'] }] }) };
};
let visible = false;
mod.initConfigManagementPage(() => visible);
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'config-management' });
assert.deepStrictEqual(fetched, [], 'the page fetched while it was not the page shown');
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'dashboard' });
assert.deepStrictEqual(fetched, [], 'another page opening made this one fetch');
visible = true;
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'config-management' });
assert.deepStrictEqual(fetched, ['/api/config/templates'], 'opening the page did not load the library');

(async () => {
  await new Promise((r) => setTimeout(r, 10));
  assert.strictEqual(doc.nodes.cfgBadge.textContent, '1', 'the count pill does not count the library');
  assert.ok(String(doc.nodes.cfgLibrary.innerHTML).includes('data-cfg-id="canned:home-firewall"'), 'the library was not drawn');
  console.log('config-management: all checks passed');
})();
