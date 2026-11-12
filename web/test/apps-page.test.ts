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
fs.writeFileSync(ENTRY, "export { initAppsPage, APP_CAT_TONE, monogram, historyView, historyBars } from '../web/src/pages/apps.js';\n" +
  "export { applyPageVisibility } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'testdata', '.apps.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['appsBadge', 'appsTabs', 'appsSearch', 'appsTotal', 'appsTotalRx', 'appsTotalTx', 'appsCount',
  'appsCountFoot', 'appsTop', 'appsTopFoot', 'appsUnnamed', 'appsUnnamedFoot', 'appsRingWrap', 'appsRing',
  'appsRingValue', 'appsRingLabel', 'appsOrbit', 'appsTiles', 'appsNote', 'appsThead', 'appsTable',
  'appsUnnamedWrap', 'appsUnnamedList', 'appsModal', 'appsModalTitle', 'appsModalBody', 'appsRecord', 'appsHistChart'];

function app(over) {
  return Object.assign({ id: 'netflix', name: 'Netflix', category: 'streaming', color: '#E50914', logo: 'mask',
    dark: false, rxMbps: 40, txMbps: 2, totalMbps: 42, share: 84, devices: 2, via: 'name',
    spark: [10, 30, 42], top: 'living-room-tv' }, over);
}
function payload(over) {
  return Object.assign({ ts: 1, pollMs: 5000, rxMbps: 46, txMbps: 4, unnamedMbps: 4,
    apps: [app({}),
      app({ id: 'teams', name: 'Microsoft Teams', category: 'conferencing', color: '#6264A7', logo: 'image',
        rxMbps: 2, txMbps: 2, totalMbps: 4, share: 8, devices: 1, top: '' }),
      app({ id: 'facetime', name: 'FaceTime', category: 'conferencing', color: '#00C853', logo: '',
        rxMbps: 0, txMbps: 0, totalMbps: 0, share: 0, devices: 0, spark: [1, 0], top: '' }),
      app({ id: 'steam', name: 'Steam', category: 'gaming', color: '#000000', logo: 'mask', dark: true,
        rxMbps: 1, txMbps: 0, totalMbps: 1, share: 2, devices: 1, top: '' })],
    unnamed: [{ org: 'Example Net', rxMbps: 4, txMbps: 0 }],
    devices: { netflix: [{ ip: '192.168.88.10', name: '<img src=x onerror=alert(1)>', mac: '02:00:00:00:00:10', rxMbps: 30, txMbps: 1 }] },
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
  assert.ok(/data-app="facetime" style="--app:#00C853;opacity:\.55"/.test(tiles), 'a quiet app was not dimmed:\n' + tiles);
  say('ok  logos: mask, image, monogram and dark all render');
}

// ── 2. cards, ring, unnamed, note ───────────────────────────────────────────
{
  const { doc, send, restore } = boot();
  send(payload({}));
  assert.strictEqual(doc.nodes['appsTotal'].textContent, '50.00 Mbps');
  assert.strictEqual(doc.nodes['appsCount'].textContent, '3', 'three apps had traffic');
  assert.strictEqual(doc.nodes['appsUnnamed'].textContent, '8%');
  assert.ok(String(doc.nodes['appsUnnamedList'].innerHTML).includes('Example Net'));
  assert.strictEqual(doc.nodes['appsNote'].hidden, true, 'a filled DNS cache must not show the note');
  assert.ok(String(doc.nodes['appsOrbit'].innerHTML).includes('data-app="netflix"'), 'the busiest app is not on the ring');
  send(payload({ dnsNames: 0 }));
  assert.strictEqual(doc.nodes['appsNote'].hidden, false, 'an empty DNS cache must be explained');
  restore();
  say('ok  cards, ring, unnamed traffic and the DNS note');
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
  restore();
  say('ok  the table sorts');
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

// ── 5. the stored tabs: Record this router, then the totals in bytes ──────
//
// A stored tab draws the same cards, ring, tiles and table from
// /api/apps/history, in bytes, with a stacked bar per bucket in the apps' own
// colours. Off, it says how to turn recording on and fetches nothing.
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
  let recording = false;
  global.fetch = (url, init) => {
    const method = (init && init.method) || 'GET';
    calls.push(method + ' ' + url);
    let body = HIST;
    if (url.includes('/status')) body = { recording };
    if (url.includes('/record')) { recording = JSON.parse(init.body).on; body = { ok: true, recording }; }
    return Promise.resolve({ ok: true, statusText: 'OK', json: () => Promise.resolve(body) });
  };
  const { doc, mod, send, on, restore } = boot();
  const clickTab = (t) => doc.nodes['appsTabs'].fire('click', { target: { closest: () => ({ dataset: { appstab: t } }) } });
  on('router:active', { activeId: 'r1' });
  send(payload({}));

  // Feature off: the button is hidden, the tab explains, nothing is fetched.
  clickTab('week');
  await tick();
  assert.strictEqual(doc.nodes['appsRecord'].style.display, 'none', 'the button shows with the feature off');
  assert.ok(/Settings/.test(doc.nodes['appsNote'].textContent) && !doc.nodes['appsNote'].hidden,
    'a stored tab with the feature off does not say how to turn it on: ' + doc.nodes['appsNote'].textContent);
  assert.deepStrictEqual(calls, [], 'the feature is off and the page still asked the server: ' + calls);

  // The feature on: the nav sweep is the switch's only path to the page.
  const pd = global.document;
  global.document = makeDoc([], { allowUnknown: ['*'] }); // the sweep walks the whole nav; not this page's concern
  mod.applyPageVisibility({ appHistoryEnabled: true });
  global.document = pd;
  await tick(); await tick();
  assert.strictEqual(doc.nodes['appsRecord'].style.display, '', 'the button is hidden with the feature on');
  assert.strictEqual(doc.nodes['appsRecord'].textContent, 'Record this router');
  assert.ok(/not being recorded/.test(doc.nodes['appsNote'].textContent), 'an unrecorded router is not explained: ' +
    doc.nodes['appsNote'].textContent);
  assert.ok(!calls.some((c) => c.startsWith('GET /api/apps/history?')), 'an unrecorded router\'s history was fetched');

  // Record: POSTed for this router, then the week is fetched and drawn.
  doc.nodes['appsRecord'].fire('click');
  await tick(); await tick(); await tick();
  assert.ok(calls.includes('POST /api/apps/history/record'), 'the button did not POST: ' + calls);
  assert.strictEqual(doc.nodes['appsRecord'].textContent, 'Stop recording');
  assert.ok(calls.includes('GET /api/apps/history?routerId=r1&period=week'), 'the week was not fetched: ' + calls);
  assert.strictEqual(doc.nodes['appsNote'].hidden, true, 'the note stayed up with recording on');
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

  // Back to Live: the bars go and the live payload is drawn again.
  clickTab('live');
  await tick();
  assert.strictEqual(doc.nodes['appsHistChart'].hidden, true, 'the bars stayed up on Live');
  assert.strictEqual(doc.nodes['appsTotal'].textContent, '50.00 Mbps', 'Live did not go back to the payload');
  restore();

  // The view itself: shares are of the recorded bytes, sparklines follow the buckets.
  const v = mod.historyView(HIST, 'week');
  assert.strictEqual(v.apps[0].share, 75);
  assert.deepStrictEqual(v.apps[1].spark, [0, 1073741824]);
  say('ok  the stored tabs: record this router, totals in bytes, bars in brand colours');

  fs.rmSync(OUT, { force: true });
  say('apps-page: all checks passed');
})().catch((e) => { console.error(e); process.exitCode = 1; });
