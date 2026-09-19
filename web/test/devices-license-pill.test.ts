/**
 * THE LICENCE PILL SAYS WHAT THE ROUTER SAID.
 *
 * ── THE BUG ─────────────────────────────────────────────────────────────────
 *
 * The pill hard-coded an `L` before the level:
 *
 *     '…">L' + esc(r.licenseLevel) + '</span>'
 *
 * which is right for a RouterBOARD, where the level is a bare number and
 * MikroTik itself writes L4 and L6. It is wrong for a Cloud Hosted Router, whose
 * levels are WORDS — so the pill read "Lfree", "Lp1" and "Lp-unlimited", none of
 * which is a licence level any MikroTik product has.
 *
 * ── BOTH SIDES ARE MEASURED, NOT ASSUMED ────────────────────────────────────
 *
 * Read off real devices on 2026-09-06 rather than taken from the docs:
 *
 *     hAP (RouterOS 7)   nlevel: 6
 *     CHR 7.24.2         level:  free
 *
 * Note the two answer on DIFFERENT KEYS as well as with different value shapes;
 * `internal/collect/system.go` takes whichever is present.
 *
 * ── WHY BOTH A UNIT AND A MARKUP HALF ───────────────────────────────────────
 *
 * `licenseLabel` could be perfectly correct and simply not wired into the render
 * — "written but never called" is a failure this port has shipped before. And
 * the markup half alone would pass against a fix that dropped the prefix
 * entirely, which would break every physical router in the fleet. Each half
 * catches what the other cannot.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'testdata', '.lic.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'routers.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const doc = makeDoc(['routers-grid'], { allowUnknown: ['rsTotal', 'rsOnline', 'rsOffline', 'rsAlerting', 'routersSiteFilter', 'routersSearch', 'routersShown'] });
global.document = doc;
global.window = { addEventListener: () => {}, location: { pathname: '/devices' } };
const page = require(OUT);

function row(over) {
  return Object.assign({
    id: 'r', label: 'R', host: '198.51.100.1', isActive: false,
    connected: true, known: true, lastError: null, openAlerts: 0,
    cpu: 1, uptime: '1d', memPct: 1, hddPct: 1,
    version: '7.24', boardName: 'x', arch: null, serial: null,
    licenseLevel: null, rxMbps: null, txMbps: null, clients: null,
    siteIds: [], siteNames: [], siteId: null, siteName: null, geo: null,
  }, over || {});
}

// ── the unit half ───────────────────────────────────────────────────────────
{
  // A RouterBOARD's bare number keeps the prefix MikroTik writes.
  assert.strictEqual(page.licenseLabel('6'), 'L6');
  assert.strictEqual(page.licenseLabel('4'), 'L4');
  // A CHR's word is already a level and must be left exactly as reported.
  assert.strictEqual(page.licenseLabel('free'), 'free');
  assert.strictEqual(page.licenseLabel('p1'), 'p1');
  assert.strictEqual(page.licenseLabel('p-unlimited'), 'p-unlimited');
  say('ok  licenseLabel prefixes a number and leaves a word alone');
}

// ── the markup half ─────────────────────────────────────────────────────────
{
  page.setView('comfortable');
  page.renderRoutersStats([
    row({ id: 'chr', label: 'CHR', licenseLevel: 'free' }),
    row({ id: 'hap', label: 'hAP', licenseLevel: '6' }),
  ]);
  const grid = String(doc.nodes['routers-grid'].innerHTML);

  assert.ok(/>free</.test(grid),
    'the CHR pill does not read "free":\n' + grid);
  assert.ok(!/>Lfree</.test(grid),
    'the CHR pill still reads "Lfree" — the hard-coded L is back');
  // THE OTHER DIRECTION, and it is why both devices are rendered together: a
  // fix that simply deleted the prefix would satisfy every assertion above and
  // silently relabel every physical router in the fleet.
  assert.ok(/>L6</.test(grid),
    'the RouterBOARD pill no longer reads "L6" — the prefix was dropped ' +
    'outright rather than made conditional:\n' + grid);
  say('ok  a CHR renders "free" while a RouterBOARD still renders "L6"');
}

fs.rmSync(OUT, { force: true });
say('devices-license-pill: all checks passed');
