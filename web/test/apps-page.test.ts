/**
 * THE APPS USAGE PAGE, DRIVEN THROUGH ITS REAL MODULE.
 *
 * Boots web/src/pages/apps.ts against the DOM shim and feeds it `apps:update`
 * payloads shaped like internal/collect/apps.go's, so the field names, the
 * renderers and the logo rules are exercised together.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.apps-entry.ts');
fs.writeFileSync(ENTRY, "export { initAppsPage, APP_CAT_TONE, monogram, historyView, historyBars, fillingNote } from '../web/src/pages/apps.js';\n" +
  "export { applyPageVisibility } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'testdata', '.apps.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['appsBadge', 'appsTabs', 'appsSearch', 'appsTotal', 'appsTotalRx', 'appsTotalTx', 'appsCount',
  'appsCountFoot', 'appsTop', 'appsTopFoot', 'appsUnnamed', 'appsUnnamedFoot', 'appsRingWrap', 'appsRing',
  'appsRingValue', 'appsRingLabel', 'appsOrbit', 'appsTiles', 'appsNote', 'appsThead', 'appsTable',
  'appsModal', 'appsModalTitle', 'appsModalBody', 'appsHistChart'];

const MB = 1048576;
const HOUR = 3600000;
function app(over) {
  return Object.assign({ id: 'netflix', name: 'Netflix', category: 'streaming', color: '#E50914', logo: 'mask',
    dark: false, rxBytes: 40 * MB, txBytes: 2 * MB, totalBytes: 42 * MB, share: 84, devices: 2, via: 'name',
    spark: Array.from({ length: 60 }, (_, i) => i * MB / 10), top: 'living-room-tv' }, over);
}
/** The last hour, as internal/collect/apps.go sends it: bytes, a whole hour covered. */
function payload(over) {
  return Object.assign({ ts: 1, from: 10 * HOUR, to: 11 * HOUR, rxBytes: 46 * MB, txBytes: 4 * MB, unnamedBytes: 4 * MB,
    apps: [app({}),
      app({ id: 'teams', name: 'Microsoft Teams', category: 'conferencing', color: '#6264A7', logo: 'image',
        rxBytes: 2 * MB, txBytes: 2 * MB, totalBytes: 4 * MB, share: 8, devices: 1, top: '' }),
      app({ id: 'facetime', name: 'FaceTime', category: 'conferencing', color: '#00C853', logo: '',
        rxBytes: 512, txBytes: 0, totalBytes: 512, share: 0, devices: 1, top: '' }),
      app({ id: 'steam', name: 'Steam', category: 'gaming', color: '#000000', logo: 'mask', dark: true,
        rxBytes: MB, txBytes: 0, totalBytes: MB, share: 2, devices: 1, top: '' })],
    devices: { netflix: [{ ip: '192.168.88.10', name: '<img src=x onerror=alert(1)>', mac: '02:00:00:00:00:10', rxBytes: 30 * MB, txBytes: MB }] },
    dnsNames: 120, dnsAt: 5 }, over);
}

function boot() {
  const doc = makeDoc(IDS, { allowUnknown: [] });
  const handlers = {};
  const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} };
  const prev = { d: global.document, w: global.window };
  global.document = doc;
  global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
  const mod = require(OUT);
  mod.initAppsPage(socket, () => true);
  return { doc, mod, send: (p) => handlers['apps:update'](p), on: (ev, d) => handlers[ev](d),
    restore: () => { global.document = prev.d; global.window = prev.w; } };
}

// ── 1. every logo kind renders, in its colour ───────────────────────────────
{
  const { doc, send, restore } = boot();
  send(payload({}));
  const tiles = String(doc.nodes['appsTiles'].innerHTML);
  restore();
  assert.ok(/app-logo app-logo-lg is-mask"[^>]*--app:#E50914;-webkit-mask-image:url\(\/vendor\/app-icons\/netflix\.svg\)/.test(tiles),
    'Netflix did not render as a brand-coloured mask of its logo:\n' + tiles);
  assert.ok(tiles.includes('src="/vendor/app-icons/teams.svg"') && tiles.includes('is-image'),
    'Teams, a full-colour logo, was not rendered as an image:\n' + tiles);
  assert.ok(/is-mono"[^>]*>FA</.test(tiles), 'FaceTime, with no logo, did not get its monogram:\n' + tiles);
  assert.ok(/app-logo app-logo-lg is-dark is-mask/.test(tiles), 'Steam\'s black logo was not marked dark:\n' + tiles);
  assert.ok(tiles.includes('class="au-rx"') && tiles.includes('class="au-tx"'), 'Rx and Tx lost their colours:\n' + tiles);
  say('ok  logos: mask, image, monogram and dark all render');
}

// ── 2. the last hour: bytes on the cards and ring, the tab first, no owner list ─
{
  const { doc, send, restore } = boot();
  send(payload({}));
  assert.strictEqual(doc.nodes['appsTotal'].textContent, '50.0 MB', 'the total is not the hour\'s bytes');
  assert.strictEqual(doc.nodes['appsCount'].textContent, '4');
  assert.strictEqual(doc.nodes['appsCountFoot'].textContent, 'in the last hour');
  assert.strictEqual(doc.nodes['appsUnnamed'].textContent, '8%');
  assert.strictEqual(doc.nodes['appsUnnamedFoot'].textContent, '4.0 MB not named');
  assert.strictEqual(doc.nodes['appsRingValue'].textContent, '50.0 MB');
  assert.strictEqual(doc.nodes['appsNote'].hidden, true, 'a whole hour and a filled DNS cache must not show the note');
  assert.ok(String(doc.nodes['appsOrbit'].innerHTML).includes('data-app="netflix"'), 'the busiest app is not on the ring');
  const tiles = String(doc.nodes['appsTiles'].innerHTML);
  assert.ok(tiles.includes('42.0 MB') && tiles.includes('2 devices, mostly living-room-tv'), 'the Netflix tile is not its hour:\n' + tiles);
  assert.ok(!/Mbps/.test(tiles + doc.nodes['appsTotal'].textContent), 'a rate is still drawn:\n' + tiles);
  send(payload({ dnsNames: 0 }));
  assert.strictEqual(doc.nodes['appsNote'].hidden, false, 'an empty DNS cache must be explained');
  restore();
  const html = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-apps.html'), 'utf8');
  const tabs = [...html.matchAll(/data-appstab="([a-z]+)"/g)].map((m) => m[1]);
  assert.deepStrictEqual(tabs, ['hour', 'day', 'week', 'month'], 'Last hour is not the first tab: ' + tabs);
  assert.ok(/class="stab active"[^>]*data-appstab="hour">Last hour</.test(html), 'Last hour is not the default tab');
  assert.ok(!/appsUnnamedWrap|appsUnnamedList|appsRecord/.test(html), 'the owner list or the record button is back in the markup');
  say('ok  the last hour: bytes on the cards, ring and tiles; Last hour first; no owner list');
}

// ── 2b. the hour still filling says so ─────────────────────────────────────
{
  const { doc, mod, send, restore } = boot();
  send(payload({ from: 10 * HOUR, to: 10 * HOUR + 12 * 60000 }));
  assert.strictEqual(doc.nodes['appsNote'].hidden, false, 'a 12-minute window did not say the hour is filling');
  assert.ok(/still filling: 12 minutes/.test(doc.nodes['appsNote'].textContent), doc.nodes['appsNote'].textContent);
  restore();
  assert.ok(/less than a minute/.test(mod.fillingNote(payload({ from: 0, to: 30000 }))), 'the first minute is not described');
  assert.strictEqual(mod.fillingNote(payload({})), '', 'a whole hour still reads as filling');
  say('ok  a filling hour is explained');
}

// ── 3. the table sorts ─────────────────────────────────────────────────────
{
  const { doc, send, restore } = boot();
  send(payload({}));
  const names = () => [...String(doc.nodes['appsTable'].innerHTML).matchAll(/<span>([^<]+)<\/span><\/span><\/td>/g)].map((m) => m[1]);
  assert.deepStrictEqual(names(), ['Netflix', 'Microsoft Teams', 'Steam', 'FaceTime'], 'busiest first by default');
  const ths = doc.nodes['appsThead'].querySelectorAll('th');
  ths[0].click();
  assert.deepStrictEqual(names(), ['FaceTime', 'Microsoft Teams', 'Netflix', 'Steam'], 'sorting by name');
  assert.ok(/au-rx">2\.0 MB/.test(String(doc.nodes['appsTable'].innerHTML)), 'the table is not in bytes');
  restore();
  say('ok  the table sorts, in bytes');
}

// ── 4. every catalogue category has a tone ─────────────────────────────────
{
  const { mod, restore } = boot();
  restore();
  const cat = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'apps', 'catalogue.json'), 'utf8'));
  for (const c of cat.categories) assert.ok(mod.APP_CAT_TONE[c], 'category "' + c + '" has no pill tone');
  for (const c of Object.keys(mod.APP_CAT_TONE)) assert.ok(cat.categories.includes(c), 'tone for unknown category "' + c + '"');
  assert.strictEqual(mod.monogram('Microsoft Teams'), 'MT');
  assert.strictEqual(mod.monogram('FaceTime'), 'FA');
  say('ok  categories and tones agree; monograms');
}

// ── 5. the stored tabs: on by default, no opt-in, totals in bytes ─────────
//
// A stored tab draws the same cards, ring, tiles and table from
// /api/apps/history, with a stacked bar per bucket in the apps' own colours.
// There is no per-router switch: with the feature on, the history is fetched
// for whichever router is active. Off, the tab says so and fetches nothing.
const HIST = {
  from: 0, to: 2,
  apps: [
    { id: 'netflix', name: 'Netflix', category: 'streaming', color: '#E50914', logo: 'mask', dark: false, rxBytes: 3 * 1073741824, txBytes: 0 },
    { id: 'teams', name: 'Microsoft Teams', category: 'conferencing', color: '#6264A7', logo: 'image', dark: false, rxBytes: 512 * 1048576, txBytes: 512 * 1048576 },
  ],
  buckets: [
    { ts: 1_800_000_000_000, apps: { netflix: 1073741824 } },
    { ts: 1_800_086_400_000, apps: { netflix: 2 * 1073741824, teams: 1073741824 } },
  ],
};
(async () => {
  const tick = () => new Promise((r) => setTimeout(r, 0));
  const calls = [];
  global.fetch = (url, init) => {
    const method = (init && init.method) || 'GET';
    calls.push(method + ' ' + url);
    return Promise.resolve({ ok: true, statusText: 'OK', json: () => Promise.resolve(HIST) });
  };
  const { doc, mod, send, on, restore } = boot();
  const clickTab = (t) => doc.nodes['appsTabs'].fire('click', { target: { closest: () => ({ dataset: { appstab: t } }) } });
  on('router:active', { activeId: 'r1' });
  send(payload({}));

  // Switched off: the tab explains, nothing is fetched.
  clickTab('week');
  await tick();
  assert.ok(/switched off/.test(doc.nodes['appsNote'].textContent) && !doc.nodes['appsNote'].hidden,
    'a stored tab with the feature off does not say so: ' + doc.nodes['appsNote'].textContent);
  assert.deepStrictEqual(calls, [], 'the feature is off and the page still asked the server: ' + calls);

  // On (the default): the week is fetched for the active router, no switch first.
  const pd = global.document;
  global.document = makeDoc([], { allowUnknown: ['*'] }); // the sweep walks the whole nav; not this page's concern
  mod.applyPageVisibility({ appHistoryEnabled: true });
  global.document = pd;
  await tick(); await tick(); await tick();
  assert.deepStrictEqual(calls, ['GET /api/apps/history?routerId=r1&period=week'], 'the week was not fetched, or something else was: ' + calls);
  assert.strictEqual(doc.nodes['appsNote'].hidden, true, 'the note stayed up with the feature on');
  assert.strictEqual(doc.nodes['appsTotal'].textContent, '4.0 GB', 'the total is not the stored bytes');
  assert.strictEqual(doc.nodes['appsCountFoot'].textContent, 'in the last 7 days');
  assert.strictEqual(doc.nodes['appsUnnamed'].textContent, '-', 'unnamed traffic is not stored, so it is not shown');
  const tiles = String(doc.nodes['appsTiles'].innerHTML);
  assert.ok(tiles.includes('3.0 GB') && tiles.includes('75% of recorded'), 'the Netflix tile is not its stored total:\n' + tiles);
  const table = String(doc.nodes['appsTable'].innerHTML);
  assert.ok(/au-rx">512\.0 MB/.test(table) && /<td class="text-end mono">-<\/td>/.test(table),
    'the table is not in bytes, or claims devices it does not store:\n' + table);
  const chart = String(doc.nodes['appsHistChart'].innerHTML);
  assert.strictEqual(doc.nodes['appsHistChart'].hidden, false, 'the bars are hidden on a stored tab');
  assert.strictEqual((chart.match(/<rect /g) || []).length, 3, 'one segment per app per day with bytes:\n' + chart);
  assert.ok(chart.includes('fill:#E50914') && chart.includes('fill:#6264A7'), 'a bar is not in its app\'s colour');
  const hexes = [...new Set(chart.match(/#[0-9A-Fa-f]{3,8}\b/g) || [])];
  assert.deepStrictEqual(hexes.sort(), ['#6264A7', '#E50914'], 'the chart has a colour that is neither an app\'s nor a token: ' + hexes);

  // Back to Last hour: the bars go and the payload is drawn again.
  clickTab('hour');
  await tick();
  assert.strictEqual(doc.nodes['appsHistChart'].hidden, true, 'the bars stayed up on Last hour');
  assert.strictEqual(doc.nodes['appsTotal'].textContent, '50.0 MB', 'Last hour did not go back to the payload');
  restore();

  // The view itself: shares are of the recorded bytes, sparklines follow the buckets.
  const v = mod.historyView(HIST, 'week');
  assert.strictEqual(v.apps[0].share, 75);
  assert.deepStrictEqual(v.apps[1].spark, [0, 1073741824]);
  say('ok  the stored tabs: on by default, no opt-in, totals in bytes, bars in brand colours');

  fs.rmSync(OUT, { force: true });
  say('apps-page: all checks passed');
})().catch((e) => { console.error(e); process.exitCode = 1; });
