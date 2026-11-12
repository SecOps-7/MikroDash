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
fs.writeFileSync(ENTRY, "export { initAppsPage, APP_CAT_TONE, monogram } from '../web/src/pages/apps.js';\n");
const OUT = path.join(ROOT, 'testdata', '.apps.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['appsBadge', 'appsTabs', 'appsSearch', 'appsTotal', 'appsTotalRx', 'appsTotalTx', 'appsCount',
  'appsCountFoot', 'appsTop', 'appsTopFoot', 'appsUnnamed', 'appsUnnamedFoot', 'appsRingWrap', 'appsRing',
  'appsRingValue', 'appsRingLabel', 'appsOrbit', 'appsTiles', 'appsNote', 'appsThead', 'appsTable',
  'appsUnnamedWrap', 'appsUnnamedList', 'appsModal', 'appsModalTitle', 'appsModalBody'];

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
  return { doc, mod, send: (p) => handlers['apps:update'](p),
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
  assert.ok(tiles.includes('class="apps-rx"') && tiles.includes('class="apps-tx"'), 'Rx and Tx lost their colours:\n' + tiles);
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

fs.rmSync(OUT, { force: true });
say('apps-page: all checks passed');
