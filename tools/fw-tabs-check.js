'use strict';
/**
 * The Firewall sub-tabs, live against ported.
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * The port selected `[data-fwtab]`. The markup — extracted verbatim from the
 * live page — carries `data-fw`. So the selector matched nothing, no listener
 * was ever attached, and Filter/NAT/Mangle/Raw did nothing: the Firewall page
 * could only ever show the Filter table.
 *
 * Every line inside the handler was correct. That is what makes this shape hard
 * to see by reading, and easy to see by asking which attributes are rendered and
 * never read.
 *
 * ── THE TAB SET COMES FROM THE MARKUP ───────────────────────────────────────
 *
 * Not from a list in this file. A tab added to the page and forgotten here would
 * make the gate weaker without failing it, which is the failure mode the whole
 * exercise is about.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/fw-tabs-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('fw-tabs-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// THE LIFT, ONLY WHERE THERE IS A SOURCE. `liveSrc` is consumed solely by
// `liveRun`, which is called only inside a frozen closure from here on.
let liveSrc = '';
if (LIFT.hasReference(ROOT)) {
  const i = src.indexOf("document.querySelectorAll('.fw-tab').forEach(function(tab){");
  if (i === -1) throw new Error('cannot find the live sub-tab handler');
  liveSrc = src.slice(i, src.indexOf('\n});', i) + 4);
}

// THE TABS, read out of the extracted page rather than listed here.
const html = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-firewall.html'), 'utf8');
const TABS = [...html.matchAll(/<div class="fw-tab([^"]*)"\s+data-fw="([^"]+)"/g)]
  .map((m) => ({ value: m[2], active: m[1].includes('active') }));
if (TABS.length < 3) throw new Error('only ' + TABS.length + ' fw-tabs found — the markup changed');

const ENTRY = path.join(ROOT, 'testdata', '.fwtab-entry.ts');
fs.writeFileSync(ENTRY, "export { initFirewallPage } from '../web/src/pages/firewall.js';\n");
const OUT = path.join(ROOT, 'testdata', '.fwtab-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function makeTabs() {
  return TABS.map((t) => {
    const classes = new Set(['fw-tab', ...(t.active ? ['active'] : [])]);
    const node = {
      _v: t.value, _h: {},
      dataset: { fw: t.value },
      getAttribute: (k) => (k === 'data-fw' ? t.value : null),
      setAttribute() {},
      classList: {
        add: (c) => classes.add(c), remove: (c) => classes.delete(c),
        toggle: (c, on) => { if (on) classes.add(c); else classes.delete(c); },
        contains: (c) => classes.has(c), _set: classes,
      },
      addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); },
    };
    return node;
  });
}

const stateOf = (tabs, emitted) => JSON.stringify({
  active: tabs.filter((t) => t.classList.contains('active')).map((t) => t._v),
  emitted,
}, null, 1);

function liveRun(clickIdx) {
  const tabs = makeTabs();
  const emitted = [];
  const ctx = {
    document: { querySelectorAll: (sel) => (sel === '.fw-tab' ? tabs : []) },
    socket: { emit: (ev, v) => emitted.push({ ev, v }) },
    fwTab: 'filter',
    fwSyncAddSlot() {},
    renderFirewallTab() {},
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc, ctx);
  for (const f of (tabs[clickIdx]._h.click || [])) f();
  return stateOf(tabs, emitted);
}

function portRun(clickIdx) {
  const tabs = makeTabs();
  const emitted = [];
  const table = { setAttribute() {}, getAttribute: () => null };
  const saved = { document: global.document, window: global.window };
  global.document = {
    querySelectorAll: (sel) => (sel === '.fw-tab' ? tabs : []),
    // The handler also syncs the Add slot, which looks one up. Absent here: this
    // gate is about which tab becomes active and what the click announces, and
    // the slot has no bearing on either.
    querySelector: () => null,
    getElementById: (id) => (id === 'firewallTable' ? table : null),
    addEventListener() {},
    body: { classList: { add() {}, remove() {}, contains: () => false } },
  };
  global.window = {};
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initFirewallPage(
      { emit: (ev, v) => emitted.push({ ev, v }), on: () => {}, isOpen: () => true },
      () => true,
    );
    for (const f of (tabs[clickIdx]._h.click || [])) f();
  } finally {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return stateOf(tabs, emitted);
}

const bad = [];
let cases = 0;
for (let i = 0; i < TABS.length; i++) {
  cases++;
  const what = 'click the ' + TABS[i].value + ' tab';
  const a = G.live(what, () => liveRun(i));
  const b = portRun(i);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the Firewall sub-tabs differ from the live ones:\n\n' + bad.slice(0, 2).join('\n\n') + '\n');
  process.exit(1);
}
console.log('Firewall sub-tabs match the live ones (' + cases + ' tabs, read from the markup: ' +
  TABS.map((t) => t.value).join(', ') + ')');
