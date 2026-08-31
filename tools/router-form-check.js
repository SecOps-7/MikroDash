#!/usr/bin/env node
'use strict';
/**
 * The Add/Edit Router dialog's value mapping, live against ported.
 *
 * ── WHAT IS BEING COMPARED, AND WHAT IS NOT ─────────────────────────────────
 *
 * `openModal` writes onto twenty-odd elements and then fetches a collector
 * registry. Only the WRITES are compared here: the live function is given a
 * recording fake for each element and its own `_seedGeoPicker`, `_setMode`,
 * `_syncUnitToggle`, `_collToggles` and registry loader as no-ops, because those
 * are the modal's other halves and each will get its own gate.
 *
 * That is a narrowing, not a shortcut, and it is stated so nobody reads a clean
 * run as "the modal is ported".
 *
 * ── THE DEFAULTS ARE THE POINT ──────────────────────────────────────────────
 *
 * Port 8729 is API-over-TLS and 8728 is plaintext, so the Add defaults are a
 * security decision: a port that quietly seeded 8728, or `tls` false, would
 * weaken every router added afterwards and nothing would fail. The corpus drives
 * the Add path precisely to pin them.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/router-form-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('router-form-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const START = 'function openModal(router) {';
const from = src.indexOf(START);
if (LIFT.hasReference(ROOT)) assert.ok(from > 0, 'openModal has moved in app.js');
const to = src.indexOf('\n  }', from);
// 4200, not 3000: #117 gave the dialog its site fields on 2026-08-25 and the
// function grew to ~3590 chars. The bound is a SANITY GUARD against the anchor
// matching a `\n  }` far below — not a statement about the function's size — so
// it is raised rather than removed, and left with headroom for the next field.
if (LIFT.hasReference(ROOT)) assert.ok(to > from && to - from < 4200, 'openModal is not where its anchors say');
const body = G.value('body', () => src.slice(from, to + 4));
for (const m of ['8729', "'admin'", "'ether1'", "'1.1.1.1'", '% 1000', 'connDownThresholdSec']) {
  assert.ok(body.includes(m), 'the lifted openModal lost: ' + m);
}
for (const m of ['function saveRouter', 'fetch(']) {
  if (LIFT.hasReference(ROOT)) assert.ok(!body.includes(m), 'the slice over-read and took in: ' + m);
}

const ENTRY = path.join(ROOT, 'testdata', '.rform-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.rform-port.cjs');
fs.writeFileSync(ENTRY,
  "export { routerFormValues, splitBw, joinBw, syncCollDeps, collectRouterForm, storedSiteIds, siteIdsForSave, seedGeoPicker, testResultMessage, labelAfterTest, TestGate, collectorGridHtml } from '../web/src/pages/router-form';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

// #117 gave the dialog MULTI-SITE membership: `rtrModalSites` is a multi-select
// of every site the viewer can see, and `rtrModalPrimarySite` names which one
// supplies the geo tier. Both are looked up through `$(...)` rather than closed
// over, so the harness below provides a `$` as well as the named bindings.
const IDS = ['rtrModalTitle', 'rtrModalId', 'rtrModalLabel', 'rtrModalSite', 'rtrModalHost',
  'rtrModalPort', 'rtrModalUser', 'rtrModalPass', 'rtrModalIf', 'rtrModalPing', 'rtrModalTls',
  'rtrModalTlsInsecure', 'rtrModalAlertsEnabled', 'rtrModalDownThresh', 'rtrModalBwDown',
  'rtrModalBwDownUnit', 'rtrModalBwUp', 'rtrModalBwUpUnit',
  'rtrModalSites', 'rtrModalPrimarySite'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

const mkEl = () => {
  const el = {
    value: '', textContent: '', placeholder: '', checked: false,
    classList: { add() {}, remove() {} }, focus() {},
    // `options` is what a select carries. Empty unless a case seeds it — see
    // `withSiteOptions`.
    options: [],
    // ── `innerHTML` AND `appendChild` ARE NEW ────────────────────────────
    //
    // The primary picker BUILDS its options now (upstream 76afa49) where the
    // old multi-select only marked existing ones selected. Assigning
    // `innerHTML` is how the live code clears it, so that has to empty
    // `options` or the placeholder row would accumulate across cases and the
    // counts below would drift upward without anything failing.
    appendChild(o) { el.options.push(o); return o; },
  };
  // A SETTER, because assigning innerHTML is how the live code CLEARS the
  // picker. A plain property would leave the previous options in place and the
  // list would grow every time the modal opened.
  let html = '';
  Object.defineProperty(el, 'innerHTML', {
    get: () => html,
    set(v) { html = v; el.options.length = 0; },
  });
  return el;
};

/**
 * Give the two site controls their options.
 *
 * The membership seeding walks `_mSites.options` and selects each one whose
 * value the device lists AND the viewer's cache knows, so a select with no
 * options can only ever report "nothing selected" — which would pass whatever
 * the port did.
 */
function withSiteOptions(n, sites) {
  n.rtrModalSites.options = Object.keys(sites).map((id) => ({ value: id, selected: false }));
}

function liveOpen(router, sites) {
  const n = {};
  for (const id of IDS) n[id] = mkEl();
  withSiteOptions(n, sites);
  let mode = null;
  const ctx = {
    Array, String, Number,
    window: { _sitesById: sites },
    // ── `document` IS NEW, AND ONLY BECAUSE THE MODAL STOPPED EDITING
    //    MEMBERSHIP ────────────────────────────────────────────────────────
    //
    // Upstream 76afa49 replaced the sites MULTI-SELECT with a primary picker
    // that BUILDS its options — `document.createElement('option')` — where the
    // old control only marked existing ones selected. This context had never
    // needed a document, so the gate died with a ReferenceError rather than a
    // comparison.
    //
    // Minimal on purpose: one method, doing what an option needs and nothing
    // more. A fuller shim would start answering questions this gate has not
    // asked, and a lifted function that quietly relies on one would then look
    // tested when it is not.
    document: {
      createElement(tag) {
        assert.equal(tag, 'option',
          'the modal now creates a <' + tag + '>, which this shim does not model');
        return { value: '', textContent: '', selected: false };
      },
    },
    // The dialog looks its site controls up by id rather than closing over them.
    $: (id) => n[id] || null,
    modalBg: { classList: { add() {} } },
    modalTitle: n.rtrModalTitle, modalId: n.rtrModalId, modalLabel: n.rtrModalLabel,
    modalSite: n.rtrModalSite, modalHost: n.rtrModalHost, modalPort: n.rtrModalPort,
    modalUser: n.rtrModalUser, modalPass: n.rtrModalPass, modalIf: n.rtrModalIf,
    modalPing: n.rtrModalPing, modalTls: n.rtrModalTls, modalTlsI: n.rtrModalTlsInsecure,
    modalAlerts: n.rtrModalAlertsEnabled, modalDownThresh: n.rtrModalDownThresh,
    modalBwDown: n.rtrModalBwDown, modalBwDownU: n.rtrModalBwDownUnit,
    modalBwUp: n.rtrModalBwUp, modalBwUpU: n.rtrModalBwUpUnit,
    // The modal's OTHER halves, stubbed because they are not what this compares.
    _seedGeoPicker() {}, _setMode(m) { mode = m; }, _syncUnitToggle() {},
    _collToggles: () => [], _syncCollDeps() {}, _collRegistry: true,
    _loadCollRegistry: () => ({ then(f) { f(); } }),
    hideTestResult() {}, setSaveReady() {},
  };
  vm.createContext(ctx);
  vm.runInContext(body + '\nopenModal(' + JSON.stringify(router) + ');', ctx);
  return {
    title: n.rtrModalTitle.textContent,
    id: n.rtrModalId.value, label: n.rtrModalLabel.value,
    // #117: the SELECTED OPTIONS and the primary, not a singular value — the
    // `rtrModalSite` control this used to read is gone from the markup, and the
    // live `openModal` stopped assigning it. Reading it still would have compared
    // two empty strings for ever.
    // ── WHAT POPULATE DECIDES ABOUT SITES IS NOW THE PICKER'S OPTIONS ─────
    //
    // This read `rtrModalSites.options` — the membership multi-select — which
    // upstream 76afa49 removed. The element is gone, so the extraction was
    // reporting an empty list for every case and comparing it against a port
    // that still built one. That is the failure shape this whole file exists to
    // catch, arriving in the file itself.
    //
    // The primary picker is now the only site control, and what it OFFERS is the
    // observable: exactly the sites the device is already in, and only those a
    // name is known for. `siteIds` here therefore means "what the picker offers",
    // not "what is selected".
    siteIds: n.rtrModalPrimarySite.options.map((o) => o.value).filter(Boolean),
    primarySite: n.rtrModalPrimarySite.value,
    host: n.rtrModalHost.value, port: String(n.rtrModalPort.value),
    username: n.rtrModalUser.value, passPlaceholder: n.rtrModalPass.placeholder,
    defaultIf: n.rtrModalIf.value, pingTarget: n.rtrModalPing.value,
    tls: n.rtrModalTls.checked, tlsInsecure: n.rtrModalTlsInsecure.checked,
    alertsEnabled: n.rtrModalAlertsEnabled.checked,
    downThreshold: n.rtrModalDownThresh.value,
    bwDown: { value: n.rtrModalBwDown.value, unit: n.rtrModalBwDownUnit.value },
    bwUp: { value: n.rtrModalBwUp.value, unit: n.rtrModalBwUpUnit.value },
    mode,
  };
}

const SITES = { s1: { name: 'HQ' } };
const R = (o) => Object.assign({
  id: 'r1', label: 'Core', host: '10.0.0.1', port: 8729, username: 'admin',
  defaultIf: 'ether1', pingTarget: '1.1.1.1', tls: true, tlsInsecure: false,
}, o);

const CASES = {
  'ADD — no router at all': null,
  'an ordinary edit': R({}),
  'a router in a known site': R({ siteId: 's1' }),
  'a router in a DELETED site falls back to none': R({ siteId: 'gone' }),
  // THE MIXED CASE, and the one 76afa49 turns on: a device in two sites, one of
  // which has been deleted. The picker must offer only the survivor — it has no
  // name to show for the other — while the SAVE keeps both. The populate half is
  // here; the save half is `a DELETED site survives the save` below.
  'a router in one live site and one deleted': R({ siteIds: ['s1', 'gone'] }),
  'a router whose PRIMARY was deleted': R({ siteIds: ['gone', 's1'] }),
  'tls off': R({ tls: false }),
  'tls insecure': R({ tlsInsecure: true }),
  'alerts on': R({ alertsEnabled: true }),
  'a down threshold of 0, which is NOT the default': R({ connDownThresholdSec: 0 }),
  'a down threshold of 90': R({ connDownThresholdSec: 90 }),
  'no down threshold takes 30': R({}),
  'a round-thousand downlink shows as Gbps': R({ bwDownMbps: 1000 }),
  'a non-round downlink stays Mbps': R({ bwDownMbps: 1500 }),
  'a 10G downlink': R({ bwDownMbps: 10000 }),
  'a 100M uplink': R({ bwUpMbps: 100 }),
  'both links non-round': R({ bwDownMbps: 250, bwUpMbps: 750 }),
  'a plaintext port': R({ port: 8728 }),
  'an empty label': R({ label: '' }),
  'a collection mode of poll': R({ collection: { mode: 'poll', off: ['logs'] } }),
  'no collection block at all': R({}),
};

let bad = 0, checked = 0;
for (const [name, router] of Object.entries(CASES)) {
  checked++;
  const a = liveOpen(router, SITES);
  const p = port.routerFormValues(router, SITES);
  // The live writes STRINGS into value fields; the port returns typed values.
  // Compared through String() on exactly the fields the DOM stringifies, which
  // is a mapping the caller will do too — not a loosening.
  const b = {
    title: p.title, id: p.id, label: p.label, siteIds: p.siteIds, primarySite: p.primarySite,
    host: p.host, port: String(p.port), username: p.username,
    passPlaceholder: p.passPlaceholder, defaultIf: p.defaultIf, pingTarget: p.pingTarget,
    tls: p.tls, tlsInsecure: p.tlsInsecure, alertsEnabled: p.alertsEnabled,
    downThreshold: p.downThreshold,
    bwDown: { value: p.bwDown.value, unit: p.bwDown.unit },
    bwUp: { value: p.bwUp.value, unit: p.bwUp.unit },
    mode: p.mode,
  };
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    bad++;
    console.error('%s\n  live: %j\n  port: %j', name, a, b);
  }
}

// The save-side inverse, round-tripped through the live expression.
const joinSrc = 'function join(v, u){ var n = parseInt(v, 10) || 1; return u === "gbps" ? n * 1000 : n; }';
const joinCtx = { parseInt };
vm.createContext(joinCtx);
vm.runInContext(joinSrc, joinCtx);
for (const [raw, unit] of [['1', 'mbps'], ['1', 'gbps'], ['', 'mbps'], ['abc', 'gbps'],
  ['0', 'mbps'], ['1500', 'mbps'], ['10', 'gbps'], ['-5', 'mbps'], ['2.7', 'gbps']]) {
  checked++;
  const a = joinCtx.join(raw, unit), b = port.joinBw(raw, unit);
  if (a !== b) { bad++; console.error('joinBw %j/%j\n  live: %j\n  port: %j', raw, unit, a, b); }
}

// ── the collector grid's dependency pass ───────────────────────────────────
//
// Lifted, because the rule reads `data-requires` off the markup and a
// reimplementation here would be testing the reimplementation. The fake toggles
// carry a `closest` returning their own label, which is how the original reaches
// the attribute.
const depsAt = src.indexOf('function _syncCollDeps() {');
if (LIFT.hasReference(ROOT)) assert.ok(depsAt > 0, '_syncCollDeps has moved in app.js');
const depsEnd = src.indexOf('\n  }', depsAt);
if (LIFT.hasReference(ROOT)) assert.ok(depsEnd > depsAt && depsEnd - depsAt < 1400, '_syncCollDeps is not where its anchors say');
const depsSrc = G.value('depsSrc', () => src.slice(depsAt, depsEnd + 4));
for (const m of ['data-requires', 'disabled = true', "opacity = '.5'"]) {
  if (LIFT.hasReference(ROOT)) assert.ok(depsSrc.includes(m), '_syncCollDeps lost: ' + m);
}
if (LIFT.hasReference(ROOT)) assert.ok(!depsSrc.includes('addEventListener'), '_syncCollDeps over-read into the wiring below it');

// `spec` may carry `disabled`/`dimmed`, which is the state a PREVIOUS pass left.
// The live elements keep it between runs; modelling that is what makes the
// re-enable rules observable.
function liveDeps(spec) {
  const toggles = spec.map((t) => {
    const lbl = {
      _requires: t.requires.join(','),
      getAttribute: (k) => (k === 'data-requires' ? (lbl._requires || null) : null),
      style: { opacity: '' },
    };
    if (t.dimmed) lbl.style.opacity = '.5';
    const el = {
      checked: t.checked, disabled: !!t.disabled,
      getAttribute: (k) => (k === 'data-coll' ? t.key : null),
      closest: () => lbl, _lbl: lbl,
    };
    return el;
  });
  const ctx = { _collToggles: () => toggles };
  vm.createContext(ctx);
  vm.runInContext(depsSrc + '\n_syncCollDeps();', ctx);
  return toggles.map((el, i) => ({
    key: spec[i].key, checked: el.checked, disabled: el.disabled,
    dimmed: el._lbl.style.opacity === '.5',
  }));
}

const T = (key, checked, requires) => ({ key, checked, requires: requires || [] });
for (const [name, spec] of [
  ['no dependencies at all', [T('conns', true), T('logs', false)]],
  ['a met dependency', [T('conns', true), T('bandwidth', true, ['conns'])]],
  ['an UNMET dependency disables and unchecks', [T('conns', false), T('bandwidth', true, ['conns'])]],
  ['an unmet dependency on an already-unchecked toggle',
    [T('conns', false), T('bandwidth', false, ['conns'])]],
  ['TWO dependencies, one unmet', [T('a', true), T('b', false), T('c', true, ['a', 'b'])]],
  ['two dependencies, both met', [T('a', true), T('b', true), T('c', true, ['a', 'b'])]],
  ['an UNKNOWN dependency is treated as met', [T('a', true, ['nosuch'])]],
  ['whitespace around a key', [T('a', false), T('b', true, [' a '])]],
  // The chain, and the ORDER that decides how far it propagates.
  ['a chain in dependency order', [T('c', false), T('b', true, ['c']), T('a', true, ['b'])]],
  ['the same chain REVERSED', [T('a', true, ['b']), T('b', true, ['c']), T('c', false)]],
  ['a self-reference', [T('a', true, ['a'])]],
  ['an empty grid', []],
  // ── RE-RUNS, which is how the grid is actually used: the pass is bound to
  // `change` on the container, so it runs again every time a box is clicked.
  ['a met dependency RE-ENABLES a toggle a previous pass disabled',
    [T('a', true), Object.assign(T('b', false, ['a']), { disabled: true, dimmed: true })]],
  ['a toggle with NO requires stays disabled — it is skipped, not re-enabled',
    [T('a', true), Object.assign(T('b', false), { disabled: true, dimmed: true })]],
  ['a still-unmet dependency stays disabled',
    [T('a', false), Object.assign(T('b', false, ['a']), { disabled: true, dimmed: true })]],
]) {
  checked++;
  const a = liveDeps(spec);
  const b = port.syncCollDeps(spec);
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    bad++;
    console.error('deps: %s\n  live: %j\n  port: %j', name, a, b);
  }
}
// BELIEVABILITY: the cases must produce both a disabled toggle and an enabled
// one, or the pass could be a constant.
const flat = liveDeps([T('a', false), T('b', true, ['a']), T('c', true)]);
assert.ok(flat.some((x) => x.disabled) && flat.some((x) => !x.disabled),
  'the dependency cases do not separate disabled from enabled');

// ── the save body ──────────────────────────────────────────────────────────
//
// `collectModal` is lifted whole: it is the function that decides what reaches
// routers.json, and two of its rules exist to protect data already stored there.
/** Lift one function whole, by its opening line and the dedent that ends it. */
function liveSlice(startsWith, endsWith) {
  const i = src.indexOf(startsWith);
  if (LIFT.hasReference(ROOT)) assert.ok(i > 0, 'cannot find ' + startsWith.trim());
  const j = src.indexOf(endsWith, i);
  if (LIFT.hasReference(ROOT)) assert.ok(j > i && j - i < 1200, startsWith.trim() + ' is not where its anchors say');
  return src.slice(i, j + endsWith.length);
}

const colAt = src.indexOf('function collectModal() {');
if (LIFT.hasReference(ROOT)) assert.ok(colAt > 0, 'collectModal has moved in app.js');
const colEnd = src.indexOf('\n  }', colAt);
// 5000, not 3800: #117's siteIds block took it to ~3060, and 76afa49's
// reorder-the-stored-list IIFE to ~4144. Same reasoning as openModal's bound
// above — a SANITY GUARD ON THE ANCHOR, not a size contract. It exists so a
// moved or renamed `function collectModal() {` slices half the file instead of
// silently comparing the wrong text; it is not a claim about how long the
// function ought to be, and raising it when the function legitimately grows is
// the intended maintenance rather than a weakening.
if (LIFT.hasReference(ROOT)) assert.ok(colEnd > colAt && colEnd - colAt < 5000, 'collectModal is not where its anchors say');
const colSrc = G.value('colSrc', () => src.slice(colAt, colEnd + 4));
for (const m of ['geo:', 'connDownThresholdSec', 'return undefined', 'bwDownMbps']) {
  if (LIFT.hasReference(ROOT)) assert.ok(colSrc.includes(m), 'collectModal lost: ' + m);
}
if (LIFT.hasReference(ROOT)) assert.ok(!colSrc.includes('testBtn'), 'the slice over-read into the test button below it');

function liveCollect(f) {
  const v = (s) => ({ value: s });
  // ── `_selectedModalSites` IS GONE, AND SO IS THE MULTI-SELECT ──────────
  //
  // It used to be LIFTED here rather than stubbed, because it decided what
  // membership a save carried. Upstream 76afa49 removed both: the modal no
  // longer edits membership, and `collectModal` now REORDERS the device's
  // STORED list — reading `_routers` for the record and moving the chosen
  // primary to position 0.
  //
  // So the harness supplies the FLEET instead of a selection. That is the
  // substantive change: what a save carries is now a function of what the store
  // holds, not of what a control shows, and a site the picker cannot name is
  // still kept.
  const fleet = [{
    id: f.id,
    siteIds: f.siteIds,
    siteId: f.siteId,
  }];
  const ctx = {
    parseInt, Array,
    _routers: fleet,
    $: (id) => (id === 'rtrModalPrimarySite' ? { value: f.primarySite || '' } : null),
    modalId: v(f.id), modalLabel: v(f.label),
    modalHost: v(f.host), modalPort: v(f.port), modalUser: v(f.username),
    modalPass: v(f.password), modalIf: v(f.defaultIf), modalPing: v(f.pingTarget),
    modalTls: { checked: f.tls }, modalTlsI: { checked: f.tlsInsecure },
    modalBwDown: v(f.bwDownRaw), modalBwDownU: v(f.bwDownUnit),
    modalBwUp: v(f.bwUpRaw), modalBwUpU: v(f.bwUpUnit),
    modalAlerts: { checked: f.alertsEnabled }, modalDownThresh: v(f.downThresholdRaw),
    modalMode: v(f.mode),
    _geoPicker: { get: () => f.geoPlace },
    _collToggles: () => f.toggles.map((t) => ({
      checked: t.checked, getAttribute: (k) => (k === 'data-coll' ? t.key : null),
    })),
  };
  vm.createContext(ctx);
  return vm.runInContext(colSrc + '\ncollectModal();', ctx);
}

// ── THE ABSENT-VERSUS-EMPTY RULE, ASSERTED DIRECTLY ────────────────────────
//
// The comparisons below cannot reach this. Their harness always supplies a
// record, so `siteIdsForSave` is never handed `undefined` — a mutation turning
// `return undefined` into `return []` passed all 107 of them.
//
// It is not a small difference. The server reads an ABSENT `siteIds` as "leave
// membership alone" and an EMPTY ARRAY as "remove every site this device is in",
// so the mutation un-files a device whenever the browser saves one it has no
// record for. Same falsy-versus-absent shape as `limit || 200`.
//
// Stated here as a unit assertion because that is what it is: a claim about one
// function's return, not about two implementations agreeing.
{
  const noRecord = port.siteIdsForSave(port.storedSiteIds(null), 's1');
  assert.strictEqual(noRecord, undefined,
    'a device this browser has no record for saved ' + JSON.stringify(noRecord)
    + ' rather than ABSENT — an empty array tells the server to remove every site '
    + 'the device is in');

  const noSites = port.siteIdsForSave(port.storedSiteIds({}), 's1');
  assert.deepEqual(noSites, [],
    'a KNOWN device with no sites saved ' + JSON.stringify(noSites) + ' rather than an '
    + 'empty list — it has none, which is a fact the server may act on, where absent '
    + 'means "do not touch this"');

  // ...and the two must not be the same value, or the assertions above hold for
  // an implementation that returns one thing always.
  assert.notStrictEqual(noRecord, noSites,
    'no-record and no-sites produced the same answer, so nothing here distinguishes them');
}

const F = (o) => Object.assign({
  id: 'r1', label: ' Core ', siteId: 's1', geoPlace: null,
  host: ' 10.0.0.1 ', port: '8729', username: ' admin ', password: ' pw ',
  defaultIf: ' ether1 ', pingTarget: ' 1.1.1.1 ', tls: true, tlsInsecure: false,
  bwDownRaw: '1', bwDownUnit: 'gbps', bwUpRaw: '500', bwUpUnit: 'mbps',
  alertsEnabled: false, downThresholdRaw: '30', mode: 'stream',
  toggles: [{ key: 'conns', checked: true }, { key: 'logs', checked: false }],
}, o);

for (const [name, f] of [
  ['an ordinary save', F({})],
  // ── THE RULE 76afa49 ADDED ────────────────────────────────────────────────
  //
  // Saving REORDERS the stored list; it never rebuilds it from the picker. A
  // site deleted since the device was filed is absent from the picker — it has
  // no name — and must still be in what the save carries. Rebuilding would
  // silently un-file the device from it.
  ['a DELETED site survives the save', F({ siteIds: ['s1', 'gone'], primarySite: 's1' })],
  ['a deleted site can even be the stored PRIMARY',
    F({ siteIds: ['gone', 's1'], primarySite: 's1' })],
  ['a known device with NO sites saves an EMPTY list, not an absent one',
    F({ siteIds: [], siteId: undefined })],
  ['THE GRID IS EMPTY — the collection block must be OMITTED', F({ toggles: [] })],
  ['every collector on', F({ toggles: [{ key: 'a', checked: true }] })],
  ['every collector off', F({ toggles: [{ key: 'a', checked: false }] })],
  ['a geo place is carried', F({ geoPlace: { lat: 1, lon: 2 } })],
  ['no geo place sends null', F({ geoPlace: null })],
  ['threshold 0 is KEPT, not defaulted', F({ downThresholdRaw: '0' })],
  ['threshold 300 is the top of the range', F({ downThresholdRaw: '300' })],
  ['threshold 301 falls back to 30', F({ downThresholdRaw: '301' })],
  ['a negative threshold falls back', F({ downThresholdRaw: '-1' })],
  ['an empty threshold falls back', F({ downThresholdRaw: '' })],
  ['an unparseable threshold falls back', F({ downThresholdRaw: 'abc' })],
  ['a password of only spaces is NOT trimmed', F({ password: '   ' })],
  ['no site', F({ siteId: '' })],
  ['gbps uplink', F({ bwUpRaw: '10', bwUpUnit: 'gbps' })],
  ['an empty bandwidth box floors at 1', F({ bwDownRaw: '', bwDownUnit: 'mbps' })],
  ['a poll-mode grid', F({ mode: 'poll' })],
  ['an empty mode takes stream', F({ mode: '' })],
  ['an unparseable port', F({ port: 'abc' })],
]) {
  checked++;
  const a = liveCollect(f);
  // The port takes the device's STORED membership already normalised, exactly as
  // the modal hands it over — `storedSiteIds` turns a record into an array
  // (possibly empty) and yields undefined ONLY when there is no record. The
  // harness always has one, so this never produces undefined here; the
  // no-record path is covered where the modal is, not here.
  const b = port.collectRouterForm({
    ...f,
    siteIds: port.storedSiteIds({ siteIds: f.siteIds, siteId: f.siteId }),
  });
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    bad++;
    console.error('collect: %s\n  live: %j\n  port: %j', name, a, b);
  }
}
// BELIEVABILITY: the omission rule must actually be exercised, or the case that
// protects stored collectors is decorative.
assert.strictEqual(liveCollect(F({ toggles: [] })).collection, undefined,
  'the empty-grid case does not omit the collection block — the guard is untested');
assert.ok(liveCollect(F({})).collection, 'a populated grid produced no collection block');

// ── the geo picker's seeding ───────────────────────────────────────────────
//
// Lifted whole. `esc` comes from the live source too, because the hints contain
// operator-supplied text (a site name, a WAN address) and a different escaper
// would report a difference that is the harness's — the lesson from the
// Bandwidth chart's `fmtMbps`.
const geoAt = src.indexOf('function _seedGeoPicker(router) {');
if (LIFT.hasReference(ROOT)) assert.ok(geoAt > 0, '_seedGeoPicker has moved in app.js');
const geoEnd = src.indexOf('\n  }', geoAt);
if (LIFT.hasReference(ROOT)) assert.ok(geoEnd > geoAt && geoEnd - geoAt < 2200, '_seedGeoPicker is not where its anchors say');
const geoSrc = G.value('geoSrc', () => src.slice(geoAt, geoEnd + 4));
for (const m of ['geo.place', 'geo.auto', 'place_name', 'CGNAT']) {
  if (LIFT.hasReference(ROOT)) assert.ok(geoSrc.includes(m), '_seedGeoPicker lost: ' + m);
}
if (LIFT.hasReference(ROOT)) assert.ok(!geoSrc.includes('collectModal'), 'the slice over-read');

const escSrc2 = G.value('escSrc2', () => src.slice(src.indexOf('function esc(')));
const liveEsc = new Function(escSrc2.slice(0, escSrc2.indexOf('\n')) + '\n return esc;')();

function liveGeo(router, sites) {
  const calls = [];
  const hint = { innerHTML: '' };
  const ctx = {
    esc: liveEsc, window: { _sitesById: sites },
    $: (id) => (id === 'rtrModalGeoHint' ? hint : null),
    _geoPickerEnsure: () => ({
      set: (v) => calls.push(['set', v]),
      preview: (v) => calls.push(['preview', v]),
    }),
  };
  vm.createContext(ctx);
  vm.runInContext(geoSrc + '\n_seedGeoPicker(' + JSON.stringify(router) + ');', ctx);
  const [mode, value] = calls[0] || ['none', null];
  return { mode: mode === 'set' && value === null ? 'clear' : mode, value: value ?? null, hint: hint.innerHTML };
}

const PLACE = { name: 'Berlin', region: 'BE', cc: 'DE', lat: 52.5, lon: 13.4 };
const AUTO = { name: 'Frankfurt', region: 'HE', cc: 'DE', lat: 50.1, lon: 8.7, ip: '203.0.113.9' };
const SITE_ROWS = { s1: { place_name: 'HQ Town', place_region: 'X', place_cc: 'GB', lat: 51, lon: 0 } };

for (const [name, router, sites] of [
  ['an override wins over auto', { geo: { place: PLACE, auto: AUTO } }, SITE_ROWS],
  ['an override with no auto', { geo: { place: PLACE } }, SITE_ROWS],
  ['auto alone previews', { geo: { auto: AUTO } }, SITE_ROWS],
  ['auto with NO ip', { geo: { auto: { name: 'X' } } }, SITE_ROWS],
  ['auto beats the site', { geo: { auto: AUTO }, siteId: 's1' }, SITE_ROWS],
  ['the site is used when nothing else', { geo: {}, siteId: 's1' }, SITE_ROWS],
  ['a site with no place falls through', { geo: {}, siteId: 's2' }, { s2: { } }],
  ['a site id that is not known', { geo: {}, siteId: 'gone' }, SITE_ROWS],
  ['nothing at all', { geo: {} }, SITE_ROWS],
  ['no geo block', {}, SITE_ROWS],
  ['a null router', null, SITE_ROWS],
  ['markup in a site name', { geo: {}, siteId: 's1' }, { s1: { place_name: '<b>x</b>' } }],
  ['markup in an auto ip', { geo: { auto: { name: 'X', ip: '<i>1.2.3.4</i>' } } }, SITE_ROWS],
]) {
  checked++;
  const a = liveGeo(router, sites);
  const site = (router && router.siteId && sites[router.siteId]) || null;
  const b = port.seedGeoPicker(router && router.geo, site, liveEsc);
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    bad++;
    console.error('geo: %s\n  live: %j\n  port: %j', name, a, b);
  }
}
// BELIEVABILITY: all four branches must be reached, or the precedence is
// untested.
const modes = new Set([
  liveGeo({ geo: { place: PLACE } }, {}).hint,
  liveGeo({ geo: { auto: AUTO } }, {}).hint,
  liveGeo({ geo: {}, siteId: 's1' }, SITE_ROWS).hint,
  liveGeo({ geo: {} }, {}).hint,
]);
assert.strictEqual(modes.size, 4, 'the four geo branches do not produce four distinct hints');

// ── the connection test's message, label fill and save gate ────────────────
//
// These three live inside two click handlers rather than functions, so they are
// reproduced from the exact expressions — with each one's source asserted, so a
// change over there breaks this rather than drifting past it.
const MSG_OK = "var msg = '\u2713 Connected' + (r.boardName ? ' \u2014 ' + r.boardName : '');";
const MSG_BAD = "showTestResult(false, '\u2717 ' + (r.error || 'Connection failed'));";
const FILL = 'if (r.boardName && modalLabel && !modalLabel.value.trim()) {';
const GATE = 'if (_testPassed) { _doSave(data); return; }';
const RESET = "el.addEventListener('input', function() { if (_testPassed) { setSaveReady(false); hideTestResult(); } });";
for (const [what, needle] of [['the ok message', MSG_OK], ['the error message', MSG_BAD],
  ['the label auto-fill', FILL], ['the save gate', GATE], ['the invalidate-on-input reset', RESET]]) {
  if (LIFT.hasReference(ROOT)) assert.ok(src.includes(needle), what + ' has moved in app.js');
}

const liveMsg = (ok, board, err) => (ok
  ? '\u2713 Connected' + (board ? ' \u2014 ' + board : '')
  : '\u2717 ' + (err || 'Connection failed'));
const liveFill = (cur, board) => ((board && !cur.trim()) ? board : cur);

for (const [ok, board, err] of [
  [true, 'hAP ac2', undefined], [true, '', undefined], [true, undefined, undefined],
  [false, undefined, 'bad credentials'], [false, undefined, ''], [false, undefined, undefined],
  [true, 'CCR2004', 'ignored'],
]) {
  checked++;
  const a = liveMsg(ok, board, err), b = port.testResultMessage(ok, board, err);
  if (a !== b) fail('test message ' + JSON.stringify([ok, board, err]), a, b);
}
for (const [cur, board] of [
  ['', 'hAP'], ['   ', 'hAP'], ['Mine', 'hAP'], ['', ''], ['', undefined], ['Mine', undefined],
]) {
  checked++;
  const a = liveFill(cur, board), b = port.labelAfterTest(cur, board);
  if (a !== b) fail('label fill ' + JSON.stringify([cur, board]), a, b);
}

// The gate, as a SEQUENCE — the invalidation only matters across steps.
for (const [name, steps, wantSave] of [
  ['a fresh form must test first', [], false],
  ['after a pass, save writes directly', ['pass'], true],
  ['editing after a pass forces another test', ['pass', 'edit'], false],
  ['editing twice stays invalid', ['pass', 'edit', 'edit'], false],
  ['a pass after an edit is valid again', ['pass', 'edit', 'pass'], true],
  ['editing before any pass changes nothing', ['edit'], false],
]) {
  checked++;
  let live = false;
  const liveInvalidated = [];
  for (const st of steps) {
    if (st === 'pass') { live = true; continue; }
    // The live guard: `if (_testPassed) { setSaveReady(false); hideTestResult(); }`
    if (live) { live = false; liveInvalidated.push(true); } else liveInvalidated.push(false);
  }
  const g = new port.TestGate();
  const portInvalidated = [];
  for (const st of steps) {
    if (st === 'pass') g.pass(); else portInvalidated.push(g.invalidate());
  }
  if (live !== g.maySaveDirectly() || live !== wantSave) {
    fail('gate: ' + name, { live, wantSave }, g.maySaveDirectly());
  }
  // The RETURN of invalidate drives hiding the banner, so it is compared too —
  // a version that always reported true would hide a banner that is not there.
  if (JSON.stringify(liveInvalidated) !== JSON.stringify(portInvalidated)) {
    fail('gate invalidations: ' + name, liveInvalidated, portInvalidated);
  }
}

// ── the collector grid's markup ────────────────────────────────────────────
const gridAt = src.indexOf('function _renderCollToggles() {');
if (LIFT.hasReference(ROOT)) assert.ok(gridAt > 0, '_renderCollToggles has moved in app.js');
const gridEnd = src.indexOf('\n  }', gridAt);
const gridSrc = G.value('gridSrc', () => src.slice(gridAt, gridEnd + 4));
for (const m of ['data-requires', 'stoggle-switch', 'rtrColl_', 'checked']) {
  assert.ok(gridSrc.includes(m), '_renderCollToggles lost: ' + m);
}
const escSrc3 = G.value('escSrc3', () => src.slice(src.indexOf('function esc(')));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['body', body], ['depsSrc', depsSrc], ['colSrc', colSrc], ['geoSrc', geoSrc], ['escSrc2', escSrc2], ['gridSrc', gridSrc], ['escSrc3', escSrc3]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
const gEsc = new Function(escSrc3.slice(0, escSrc3.indexOf('\n')) + '\n return esc;')();
function liveGrid(defs) {
  const holder = { innerHTML: '' };
  const ctx = { esc: gEsc, modalCollectors: holder, _collRegistry: defs };
  vm.createContext(ctx);
  vm.runInContext(gridSrc + '\n_renderCollToggles();', ctx);
  return holder.innerHTML;
}
for (const [name, defs] of [
  ['one collector, no requires', [{ key: 'dns', label: 'DNS' }]],
  ['a collector WITH requires', [{ key: 'bandwidth', label: 'Bandwidth', requires: ['conns'] }]],
  ['two requires', [{ key: 'x', label: 'X', requires: ['a', 'b'] }]],
  ['an EMPTY requires array is treated as none', [{ key: 'x', label: 'X', requires: [] }]],
  ['several collectors', [{ key: 'a', label: 'A' }, { key: 'b', label: 'B', requires: ['a'] }]],
  ['an empty registry', []],
  ['markup in a label', [{ key: 'x', label: '<b>X</b>' }]],
  ['a quote in a key', [{ key: 'a"b', label: 'X' }]],
]) {
  checked++;
  const a = liveGrid(defs), b = port.collectorGridHtml(defs, gEsc);
  if (a !== b) fail('grid: ' + name, a, b);
}

// BELIEVABILITY: both units must appear, and the Add path must differ from every
// edit — otherwise the defaults are not being exercised at all.
const units = new Set(Object.values(CASES).map((r) => liveOpen(r, SITES).bwDown.unit));
assert.strictEqual(units.size, 2, 'the corpus never produces both bandwidth units');
const add = JSON.stringify(liveOpen(null, SITES));
assert.ok(Object.values(CASES).filter(Boolean).every((r) => JSON.stringify(liveOpen(r, SITES)) !== add),
  'an edit case is indistinguishable from the Add defaults');

if (bad) {
  console.error('\nrouter-form-check: %d of %d differ', bad, checked);
  process.exit(1);
}
console.log('router-form-check: %d comparisons identical', checked);
