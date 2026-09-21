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
  "export * as deploy from '../web/src/pages/config-management-deploy.js';",
  "export * as history from '../web/src/pages/config-management-history.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.cfgmgmt.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);
fs.rmSync(OUT, { force: true });
const { cards, editor, deploy, history } = mod;

// ── History and Drift ────────────────────────────────────────────────────────
{
  const run = history.sortable({ id: 'r1', templateId: null, templateName: '<b>T</b>', revision: 2, method: 'additions',
    state: 'halted', startedBy: '<i>x</i>', createdAt: 1790000000000, finishedAt: null, error: null,
    routers: { applied: 1, failed: 1, 'not-attempted': 1 } });
  assert.strictEqual(run.total, 3, 'the routers column does not count every router');
  const closed = history.historyRows([run], '', null);
  assert.ok(!closed.includes('<b>T</b>') && !closed.includes('<i>x</i>'), 'a run row renders markup from its data');
  assert.ok(closed.includes('cfg-run-bad') && closed.includes('Stopped'), 'a halted run is not a red Stopped pill');
  assert.ok(closed.includes('1/3') && closed.includes('1 not applied'), 'the routers cell miscounts');
  assert.ok(!closed.includes('cfg-hist-open'), 'a closed run shows its detail');
  const detail = { startedBy: 'x', run: { bodyMasked: '/ip dns\nset servers={{dns}}' }, targets: [{
    routerId: 'a', label: '<s>R</s>', state: 'failed-partial', step: null, backupId: 81, dryRunOutput: null,
    importOutput: 'ok line\n<bad> line', failedLine: 2, reconnectMs: null, warning: null, error: 'stopped <here>' }] };
  const open = history.historyRows([run], 'r1', detail);
  assert.ok(open.includes('cfg-hist-open') && open.includes('Restore point #81'), 'an open run hides its restore point');
  assert.ok(open.includes('<span class="cfg-out-line is-bad">&lt;bad&gt; line</span>'),
    'the line the import stopped at is not marked, or is not escaped');
  assert.ok(!open.includes('<s>R</s>') && !open.includes('<here>'), 'a router\'s label or error renders as markup');
  assert.ok(open.includes('cfg-var'), 'what was sent is not highlighted');
  assert.ok(history.historyRows([run], 'r1', null).includes('Reading the run'), 'an open run shows nothing while loading');
  const said = (html, text) => html.split(text).length - 1;
  const halted = { ...run, error: '<s>R</s>: stopped <here>' };
  assert.strictEqual(said(history.runDetail(halted, detail), 'stopped &lt;here&gt;'), 1,
    'the run repeats the error its router already shows');
  assert.strictEqual(said(history.runDetail({ ...run, error: 'the job stopped' }, detail), 'the job stopped'), 1,
    'a run error no router gave is not shown');

  const base = { templateId: 't', templateName: '<b>N</b>', routerId: 'r', routerLabel: 'R', takenAt: 1790000000000 };
  const key = history.driftKey(base);
  assert.ok(history.driftRows([base], {}, '').includes('Not checked'), 'an unchecked baseline claims a status');
  assert.ok(!history.driftRows([base], {}, '').includes('<b>N</b>'), 'a baseline row renders markup');
  const hunks = [{ aStart: 1, aCount: 1, bStart: 1, bCount: 1, lines: [
    { op: '-', text: 'add name=<old>', aLine: 1 }, { op: '+', text: 'add name=new', bLine: 1 }] }];
  const drifted = { [key]: { state: 'done', drifted: true, fingerprint: 'f', checkedAt: 1790000000000, hunks, truncated: false } };
  const shown = history.driftRows([base], drifted, key);
  assert.ok(shown.includes('Drifted') && shown.includes('bk-del') && shown.includes('bk-add'), 'a drifted baseline hides its diff');
  assert.ok(shown.includes('&lt;old&gt;') && !shown.includes('<old>'), 'a diff line renders as markup');
  assert.ok(shown.includes('data-drift-act="reapply"') && shown.includes('data-drift-act="accept"'),
    'a drifted baseline offers no way on');
  assert.ok(!history.driftRows([base], drifted, '').includes('bk-del'), 'a closed drift still shows its diff');
  const same = { [key]: { state: 'done', drifted: false, fingerprint: 'f', checkedAt: 1, hunks: [], truncated: false } };
  const inStep = history.driftRows([base], same, key);
  assert.ok(inStep.includes('As deployed') && !inStep.includes('data-drift-act="accept"'),
    'a baseline in step offers to accept a change that is not there');
  const failed = history.driftRows([base], { [key]: { state: 'error', message: 'no <router>' } }, '');
  assert.ok(failed.includes('Could not check') && failed.includes('no &lt;router&gt;'), 'a failed check is not said, or not escaped');
}

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

// ── THE DEPLOY TAB WILL NOT START UNTIL EVERY ROUTER IS READY ────────────────
const ack = { level: 'ack', code: 'lockout-firewall', line: 13, message: 'm' };
const ok = { routerId: 'r1', hash: 'h1', findings: [ack] };
assert.strictEqual(deploy.readyToStart([], {}, new Set()), 'Pick at least one router');
assert.strictEqual(deploy.readyToStart(['r1'], {}, new Set()), 'Preview every router first');
assert.strictEqual(deploy.readyToStart(['r1'], { r1: ok }, new Set()), 'Tick OK on every check that needs it');
assert.strictEqual(deploy.readyToStart(['r1'], { r1: ok }, new Set(['r1|lockout-firewall@13'])), '', 'ready');
assert.strictEqual(deploy.readyToStart(['r1'], { r1: { routerId: 'r1', hash: 'h', findings: [{ level: 'refuse', code: 'x', message: 'no' }] } },
  new Set()), 'A check refuses this template on a router');
assert.strictEqual(deploy.findingKey(ack), 'lockout-firewall@13', 'the key must be the one cfgdeploy.FindingKey writes');
// The canary is the first picked, and marked.
const picker = deploy.routerPicker([{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }], ['b', 'a']);
assert.ok(/data-dep-router="b"[^]*?cfg-pill-canary/.test(picker.slice(picker.indexOf('data-dep-router="b"'))), 'the canary is marked');
// A secret is entered as one, and a label stays text.
const vgrid = deploy.valuesGrid([{ name: 'pw', type: 'secret' }], [{ id: 'r', label: '<b>x</b>' }], {});
assert.ok(/data-dep-var="pw" type="password"/.test(vgrid), 'a secret is entered in the clear');
assert.ok(!vgrid.includes('<b>x'), 'a router label reached the markup unescaped');
// The rollout asks for the count after the canary, and escapes what it shows.
const roll = deploy.rolloutView({ runId: 'run', state: 'awaiting-canary', templateName: '<i>t</i>', kind: 'fragment',
  startedBy: 'admin', error: '', targets: [
    { routerId: 'a', label: '<b>A</b>', canary: true, state: 'applied', step: '', code: '', applied: 'all', message: '',
      backupId: 7, reconnectMs: 1200, reverted: false, failedLine: 0 },
    { routerId: 'b', label: 'B', canary: false, state: 'pending', step: '', code: '', applied: '', message: '',
      backupId: 0, reconnectMs: 0, reverted: false, failedLine: 0 }] });
assert.ok(roll.includes('id="cfgDepCount"') && roll.includes('type <strong>2</strong>'), 'the canary decision asks for the count');
assert.ok(!roll.includes('<b>A') && !roll.includes('<i>t'), 'the rollout reached the markup unescaped');
assert.ok(roll.includes('Restore point #7'), 'the restore point is shown');

// ── NOTHING IS FETCHED UNTIL THE PAGE IS SHOWN ──────────────────────────────
const doc = makeDoc(['cfgTabs', 'cfgBadge', 'cfgStats', 'cfgCats', 'cfgSearch', 'cfgLibrary', 'cfgDrawer',
  'cfgDrawerTitle', 'cfgDrawerMeta', 'cfgDrawerBody', 'cfgDrawerClose', 'cfgPanel-library', 'cfgPanel-editor',
  'cfgPanel-deploy', 'cfgPanel-history', 'cfgPanel-drift', 'cfgNew', 'cfgEdNew', 'cfgCapture', 'cfgEdCapture',
  'cfgCaptureBox', 'cfgEdName', 'cfgEdDesc', 'cfgEdBody', 'cfgEdVars', 'cfgEdSave', 'cfgEdDelete', 'cfgEdClose',
  'cfgDepTpl', 'cfgDepRouters', 'cfgDepValues', 'cfgDepPreview', 'cfgDepPreviews', 'cfgDepStart', 'cfgRollout',
  'cfgDepTplMeta', 'cfgDepConfirm', 'cfgDepWhy', 'cfgDep', 'cfgHistHead', 'cfgHistBody', 'cfgHistEmpty',
  'cfgHistRefresh', 'cfgDriftHead', 'cfgDriftBody', 'cfgDriftEmpty', 'cfgDriftRefresh'],
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
const handlers = {};
const sent = [];
mod.initConfigManagementPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit: (ev, d) => sent.push([ev, d]) }, () => visible);
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'config-management' });
assert.deepStrictEqual(fetched, [], 'the page fetched while it was not the page shown');
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'dashboard' });
assert.deepStrictEqual(fetched, [], 'another page opening made this one fetch');
visible = true;
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'config-management' });
assert.deepStrictEqual(fetched, ['/api/config/templates'], 'opening the page did not load the library');
assert.deepStrictEqual(sent, [['cfgdeploy:watch', {}]], 'opening the page did not watch the deploy job');

(async () => {
  await new Promise((r) => setTimeout(r, 10));
  assert.strictEqual(doc.nodes.cfgBadge.textContent, '1', 'the count pill does not count the library');
  assert.ok(String(doc.nodes.cfgLibrary.innerHTML).includes('data-cfg-id="canned:home-firewall"'), 'the library was not drawn');
  console.log('config-management: all checks passed');
})();
