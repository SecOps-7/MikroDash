/**
 * HOME, STANDARD AND ADVANCED INCLUDE THE GENERATED PAGES (2026-09-18).
 *
 * The presets were built from the frozen VIEW_PRESETS and PAGE_NAV_MAP, which
 * name only the hand-built pages with a Visible Pages toggle. So the 23
 * generated pages, Tools and the AI Agent were in no preset: Advanced left their
 * toggles alone, and a role given Advanced got no access to any of them.
 *
 * `presetTiers` now builds all three once, from the frozen lists plus each
 * area's declared tier and the two hand-built pages without a toggle. The
 * operator's placement: Standard adds Tools, IP Addresses, Address Lists,
 * Interface Lists, IP Pools and DHCP Servers; everything else new is Advanced.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.presets-entry.ts');
fs.writeFileSync(ENTRY,
  "export { presetTiers } from '../web/src/presets.js';\n" +
  "export { mountViewPresets, detectViewPreset } from '../web/src/pages/settings.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n" +
  "export { PAGE_NAV_MAP, VIEW_PRESETS } from '../web/src/gen/view-presets.js';\n");
const OUT = path.join(ROOT, 'testdata', '.presets.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

// ── THE TIERS ───────────────────────────────────────────────────────────────
const t = mod.presetTiers();
const has = (list, k) => list.includes(k);
const STANDARD_NEW = ['tools', 'ip-addresses', 'address-lists', 'interface-lists', 'ip-pools', 'dhcp-servers'];

assert.deepStrictEqual([...t.home].sort(), [...mod.VIEW_PRESETS.home].sort(), 'Home changed; the operator kept it as it was');
for (const k of STANDARD_NEW) assert.ok(has(t.standard, k), 'Standard lacks ' + k);
for (const k of mod.VIEW_PRESETS.standard) assert.ok(has(t.standard, k), 'Standard lost ' + k);
// The control: the Advanced-only pages are NOT in Standard.
for (const k of ['ospf', 'containers', 'ipsec', 'ai-agent', 'routing-rules']) {
  assert.ok(!has(t.standard, k), k + ' is in Standard; the operator put it in Advanced');
  assert.ok(has(t.advanced, k), k + ' is not in Advanced');
}
// Advanced is every page a preset can name: all hand-built toggles, every
// generated page, Tools and the AI Agent.
for (const a of mod.AREAS) assert.ok(has(t.advanced, a.key), 'Advanced lacks the generated page ' + a.key);
for (const sKey of Object.keys(mod.PAGE_NAV_MAP)) assert.ok(has(t.advanced, mod.PAGE_NAV_MAP[sKey]), 'Advanced lost ' + sKey);
for (const k of t.home) assert.ok(has(t.standard, k), 'Home has ' + k + ' and Standard does not');
for (const k of t.standard) assert.ok(has(t.advanced, k), 'Standard has ' + k + ' and Advanced does not');
say('ok  Home unchanged; Standard adds the six; Advanced is every page; each tier contains the one below');

// ── VISIBLE PAGES: A PRESET SETS THE GENERATED TOGGLES, AND IS RECOGNISED ────
const navIds = Object.keys(mod.PAGE_NAV_MAP).map((k) => 's_' + k);
const doc = makeDoc(['viewPresetWrap', ...navIds], {
  query: { 'input[data-area-toggle]': mod.AREAS.map((a) => a.key) },
});
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout, localStorage: { getItem: () => null, setItem: () => {} } };
const areaBox = (k) => doc.queryNodes['input[data-area-toggle]'].find((n) => n.getAttribute('data-area-toggle') === k);
mod.mountViewPresets();
const press = (name) => doc.nodes.viewPresetWrap.fire('click', { target: { closest: () => ({ dataset: { viewPreset: name } }) } });

press('standard');
assert.strictEqual(areaBox('address-lists').checked, true, 'Standard did not switch on a Standard generated page');
assert.strictEqual(areaBox('ospf').checked, false, 'Standard switched on an Advanced generated page');
assert.strictEqual(doc.nodes['s_pageDns'].checked, true, 'Standard lost a hand-built page');
assert.strictEqual(mod.detectViewPreset(), 'standard', 'the state Standard set is not recognised as Standard');

press('advanced');
assert.ok(mod.AREAS.every((a) => areaBox(a.key).checked), 'Advanced left a generated page off');
assert.strictEqual(mod.detectViewPreset(), 'advanced');

press('home');
assert.ok(mod.AREAS.every((a) => !areaBox(a.key).checked), 'Home left a generated page on');
assert.strictEqual(mod.detectViewPreset(), 'home');

// The control: one generated toggle changed by hand is Custom, not Home.
areaBox('ospf').checked = true;
assert.strictEqual(mod.detectViewPreset(), 'custom', 'a hand-changed generated toggle was still read as Home');
say('ok  Visible Pages presets set the generated toggles, and a hand change reads as Custom');

fs.rmSync(OUT, { force: true });
