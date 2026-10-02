/**
 * THE DEVICE PICKER, which every dialog that chooses devices now uses.
 *
 *   - SEARCH matches the name, address, model, a site's name and the detail,
 *     every term; nothing picked, fixed or covered by a picked site is offered.
 *   - It DRAWS AT MOST 50 results but "Add all" reaches every match, and asks
 *     first ABOVE 10 (the operator's threshold). So does a site shortcut.
 *   - PILLS: sites first, then devices that need attention, then the rest; an
 *     ordered picker keeps pick order and marks the first as the canary. Past
 *     20 pills the rest fold into "+N more".
 *   - A RULE site keeps its devices off the pill list unless one needs
 *     attention, which shows locked.
 *   - Everything drawn is escaped.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'testdata', '.dp.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'device-picker.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const P = require(OUT);

const fleet = Array.from({ length: 120 }, (_, i) => ({
  id: 'r' + i, label: 'Edge-' + i, host: '198.51.100.' + i, model: i % 2 ? 'hAP ax3' : 'RB5009',
  siteIds: i < 15 ? ['home'] : ['branch'],
}));
const sites = [{ id: 'home', name: 'Home' }, { id: 'branch', name: 'Branch' }];
const cfg = (extra) => Object.assign({ items: () => fleet, sites: () => sites }, extra || {});

// ── search ──────────────────────────────────────────────────────────────────
{
  const names = { home: 'Home', branch: 'Branch' };
  assert.ok(P.matches(fleet[3], 'edge-3 rb5009', names) === false, 'every term must match');
  assert.ok(P.matches(fleet[3], 'edge-3 hap', names));
  assert.ok(P.matches(fleet[3], 'home', names), 'a site name does not match');
  assert.ok(P.matches(fleet[3], '198.51.100.3', names), 'the address does not match');
  const st = P.newState({ items: ['r0'] });
  st.q = 'edge';
  const r = P.results(cfg({ fixed: () => ['r1'] }), st);
  assert.strictEqual(r.total, 118, 'picked or fixed devices were offered');
  assert.strictEqual(r.items.length, P.RESULT_CAP, 'more than the cap was drawn');
  say('ok  search: every term, name/address/model/site; picked and fixed are not offered; capped at 50');
}

// ── rule sites ──────────────────────────────────────────────────────────────
{
  const st = P.newState({ sites: ['home'] });
  const c = cfg({ siteMode: 'rule', info: (id) => (id === 'r4' ? { kind: 'bad', text: 'failed' } : undefined) });
  assert.strictEqual(P.coveredBySites(c, st).size, 15);
  st.q = 'edge';
  assert.strictEqual(P.results(c, st).total, 105, 'a device covered by a picked site was offered');
  const list = P.pills(c, st);
  assert.deepStrictEqual(list.map((p) => p.kind + ':' + p.id), ['site:home', 'item:r4'],
    'a rule site listed its devices, or hid the one that needs attention');
  const html = P.pickedHtml(c, st);
  assert.match(html, />Home<span class="dp-pill-n">15</, 'the site pill does not count its devices');
  assert.ok(!/data-dp-rm="r4"/.test(html), 'a site-covered device can be removed on its own');
  say('ok  rule sites: one pill with a count; only a device needing attention shows, locked');
}

// ── order, collapse, escaping ───────────────────────────────────────────────
{
  const c = cfg({ info: (id) => (id === 'r9' ? { kind: 'warn' } : undefined) });
  const st = P.newState({ items: ['r1', 'r2', 'r9'] });
  assert.deepStrictEqual(P.pills(c, st).map((p) => p.id), ['r9', 'r1', 'r2'], 'a problem device is not first');
  const ord = P.pickedHtml(cfg({ ordered: true }), P.newState({ items: ['r5', 'r1'] }));
  assert.match(ord, /dp-canary">Canary<\/span>Edge-5/, 'the first picked is not the canary');
  const many = P.pickedHtml(cfg(), P.newState({ items: fleet.slice(0, 30).map((d) => d.id) }));
  assert.match(many, /\+10 more/, 'past 20 pills the rest are not folded');
  const evil = '<img src=x>';
  const bad = P.pickerHtml(cfg({ items: () => [{ id: evil, label: evil, host: evil }] }),
    Object.assign(P.newState({ items: [evil] }), { open: true, q: '' }));
  assert.ok(!/<img/.test(bad), 'a device name reached the picker unescaped');
  say('ok  pills: problems first, canary first when ordered, folded past 20, escaped');
}

// ── the controller: Add all asks above 10 ───────────────────────────────────
{
  const handlers = {};
  const host = {
    innerHTML: '',
    addEventListener: (ev, fn) => { (handlers[ev] = handlers[ev] || []).push(fn); },
    querySelector: () => null,
    contains: () => false,
  };
  const asked = [];
  let answer = false;
  global.window = { confirm: (m) => { asked.push(m); return answer; } };
  const pk = P.mountPicker(host, cfg());
  const click = (data) => (handlers.click || []).forEach((fn) => fn({
    preventDefault() {},
    target: { closest: () => ({ dataset: data }) },
  }));
  const type = (q) => (handlers.input || []).forEach((fn) => fn({ target: { value: q, dataset: { dpQ: '1' } } }));

  type('edge-1');                     // Edge-1, Edge-10..19, Edge-100..119: 31 matches
  click({ dpAll: '1' });
  assert.strictEqual(asked.length, 1, 'adding 31 at once did not ask');
  assert.strictEqual(pk.get().items.length, 0, 'a declined Add all added anyway');
  answer = true;
  click({ dpAll: '1' });
  assert.strictEqual(pk.get().items.length, 31);

  pk.set({ items: [] });
  asked.length = 0;
  type('edge-11');                    // Edge-11 and Edge-110..119: 11 matches
  click({ dpAll: '1' });
  assert.strictEqual(asked.length, 1, '11 matches did not ask (the threshold is "above 10")');
  // THE BOUNDARY: exactly ten is not "above 10".
  const ten = P.mountPicker(Object.assign({}, host, { addEventListener: (ev, fn) => {
    (handlers['ten-' + ev] = handlers['ten-' + ev] || []).push(fn); } }), cfg({ items: () => fleet.slice(0, 10) }));
  asked.length = 0;
  (handlers['ten-click'] || []).forEach((fn) => fn({ preventDefault() {}, target: { closest: () => ({ dataset: { dpAll: '1' } }) } }));
  assert.strictEqual(asked.length, 0, 'adding exactly ten asked');
  assert.strictEqual(ten.get().items.length, 10);
  say('ok  Add all asks above 10, honours a no, and does not ask for 10 or fewer');
}
fs.rmSync(OUT, { force: true });
say('device-picker: all checks passed');
