/**
 * The Add/Edit Router dialog's Gbps/Mbps toggles.
 *
 * ── THE CONTROL WAS DRAWN AND BOUND TO NOTHING ──────────────────────────────
 *
 * `.bw-unit-toggle`, `.bw-unit-btn`, `data-unit-for` and `data-val` appeared in
 * `shell.html` and in NO TypeScript file at all. Clicking Gbps or Mbps did
 * nothing: the hidden input kept its value and the highlight never moved.
 * Reported on issue #124.
 *
 * `internal/verify`'s attribute ledger had both attributes recorded as
 * deliberately unread, filed as "markup for a feature this port has not taken
 * on" — which was never true. They are on a shipped dialog. The ledger was
 * excusing the bug in the words that stop anyone looking again, which is why
 * the entries are gone and this file exists.
 *
 * ── TWO FAILURES, AND THE SECOND IS THE QUIET ONE ───────────────────────────
 *
 * The click doing nothing is visible. But `open()` also wrote the SEEDED unit
 * into the hidden input without moving the highlight, so editing a router stored
 * as 1500 Mbps drew "Gbps" highlighted over a field that meant Mbps. The control
 * contradicted the value it displayed, and saving without touching it was
 * correct while the screen said otherwise. Both directions are checked here.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id) {
  const classes = new Set();
  const listeners = {};
  const node = {
    id,
    value: '',
    textContent: '',
    innerHTML: '',
    style: {},
    hidden: false,
    checked: false,
    disabled: false,
    options: [],
    children: [],
    parent: null,
    focus() {},
    setAttribute: (k, v) => { node['__' + k] = v; },
    getAttribute: (k) => (('__' + k) in node ? node['__' + k] : null),
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (listeners[ev] || []).forEach((fn) => fn(arg || {})),
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    has: (c) => classes.has(c),
    querySelectorAll: () => [],
    querySelector: () => null,
    // Walks a REAL parent chain. The shared shim stubs this as `() => null`,
    // which would make the delegated handler find no button and pass this file
    // against a toggle that does nothing.
    closest: (sel) => {
      let n = node;
      while (n) {
        if (sel === '[data-val]' && n.getAttribute('data-val')) return n;
        n = n.parent;
      }
      return null;
    },
    contains: (other) => {
      let n = other;
      while (n) { if (n === node) return true; n = n.parent; }
      return false;
    },
    appendChild: () => {},
  };
  return node;
}

/** One `.bw-unit-toggle` and its two buttons, wired as the markup wires them. */
function makeToggle(hiddenId, activeVal) {
  const wrap = makeEl('');
  wrap.setAttribute('data-unit-for', hiddenId);
  const buttons = ['gbps', 'mbps'].map((v) => {
    const b = makeEl('');
    b.setAttribute('data-val', v);
    b.parent = wrap;
    if (v === activeVal) b.classList.add('active');
    return b;
  });
  wrap.querySelectorAll = (sel) => (sel === '[data-val]' ? buttons : []);
  wrap.buttons = { gbps: buttons[0], mbps: buttons[1] };
  return wrap;
}

const IDS = [
  'rtrModalBg', 'rtrModalCollectors', 'rtrModalGeoClear', 'rtrModalGeoHint', 'rtrModalGeoList',
  'rtrModalId', 'rtrModalModeWrap', 'rtrModalPrimarySite', 'rtrModalTitle', 'rtrTestResult',
  'rtrModalAlertsEnabled', 'rtrModalBwDown', 'rtrModalBwDownUnit', 'rtrModalBwUp',
  'rtrModalBwUpUnit', 'rtrModalDownThresh', 'rtrModalGeo', 'rtrModalHost', 'rtrModalIf',
  'rtrModalLabel', 'rtrModalMode', 'rtrModalPass', 'rtrModalPing', 'rtrModalPort',
  'rtrModalSaveBtn', 'rtrModalTestBtn', 'rtrModalTls', 'rtrModalTlsInsecure', 'rtrModalUser',
];

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-bw-unit.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'router-modal.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function mount() {
  const els = {};
  IDS.forEach((id) => { els[id] = makeEl(id); });
  // Both hidden inputs start at `gbps`, exactly as shell.html declares them.
  els.rtrModalBwDownUnit.value = 'gbps';
  els.rtrModalBwUpUnit.value = 'gbps';

  const down = makeToggle('rtrModalBwDownUnit', 'gbps');
  const up = makeToggle('rtrModalBwUpUnit', 'gbps');
  const toggles = [down, up];

  global.document = {
    getElementById: (id) => els[id] || null,
    querySelectorAll: (sel) => (sel === '.bw-unit-toggle' ? toggles : []),
    querySelector: (sel) => toggles.find(
      (w) => sel === '.bw-unit-toggle[data-unit-for="' + w.getAttribute('data-unit-for') + '"]',
    ) || null,
    addEventListener: () => {},
    createElement: () => makeEl(''),
  };
  global.window = { confirm: () => true };
  global.fetch = (url) => {
    if (url === '/api/collectors') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ collectors: [] }) });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, cities: [] }) });
  };

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  const modal = mod.initRouterModal({
    sites: () => ({}), routers: () => [], onSaved: () => {},
  });
  // A REAL click reaches the delegated listener on the wrapper with the button
  // as its target, which is what `closest` then climbs from.
  const click = (wrap, unit) => wrap.fire('click', {
    target: wrap.buttons[unit], preventDefault: () => {},
  });
  return { els, down, up, modal, click };
}

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('router dialog: the Gbps/Mbps unit toggle');

check('clicking Mbps sets the hidden input and moves the highlight', () => {
  const m = mount();
  m.click(m.down, 'mbps');
  assert.equal(m.els.rtrModalBwDownUnit.value, 'mbps',
    'the hidden input the save reads did not change, so the click did nothing');
  assert.equal(m.down.buttons.mbps.has('active'), true, 'Mbps is not highlighted');
  assert.equal(m.down.buttons.gbps.has('active'), false, 'Gbps is still highlighted too');
});

check('clicking Gbps again puts it back', () => {
  const m = mount();
  m.click(m.down, 'mbps');
  // ASSERTED IN THE MIDDLE, or this case is vacuous: both hidden inputs START
  // at `gbps`, so a toggle bound to nothing ends in the state this checks and
  // passes while doing nothing at all. A mutation unbinding the listener sailed
  // through until this line existed.
  assert.equal(m.els.rtrModalBwDownUnit.value, 'mbps', 'it never left gbps');
  m.click(m.down, 'gbps');
  assert.equal(m.els.rtrModalBwDownUnit.value, 'gbps');
  assert.equal(m.down.buttons.gbps.has('active'), true);
  assert.equal(m.down.buttons.mbps.has('active'), false);
});

check('the two toggles are independent', () => {
  const m = mount();
  m.click(m.down, 'mbps');
  // BOTH HALVES. "Up did not change" is the initial state, so on its own it
  // passes against a dead control; it only means anything beside evidence that
  // Down DID.
  assert.equal(m.els.rtrModalBwDownUnit.value, 'mbps', 'the download unit did not change');
  assert.equal(m.els.rtrModalBwUpUnit.value, 'gbps',
    'changing the download unit also changed the upload unit');
  assert.equal(m.up.buttons.gbps.has('active'), true);
  assert.equal(m.up.buttons.mbps.has('active'), false);
});

// ── THE QUIET HALF ──────────────────────────────────────────────────────────
check('opening a router stored in Mbps highlights Mbps', () => {
  const m = mount();
  // 1500 is not a round thousand, so `splitBw` keeps it in Mbps.
  m.modal.open({ id: 'r1', host: '198.51.100.1', bwDownMbps: 1500, bwUpMbps: 1500 });
  assert.equal(m.els.rtrModalBwDownUnit.value, 'mbps');
  assert.equal(m.els.rtrModalBwDown.value, '1500');
  assert.equal(m.down.buttons.mbps.has('active'), true,
    'the box says 1500 Mbps and the control highlights Gbps — the screen '
    + 'contradicts the value it is showing');
  assert.equal(m.down.buttons.gbps.has('active'), false);
});

check('opening a router stored in whole Gbps highlights Gbps', () => {
  const m = mount();
  m.modal.open({ id: 'r1', host: '198.51.100.1', bwDownMbps: 2000, bwUpMbps: 2000 });
  assert.equal(m.els.rtrModalBwDownUnit.value, 'gbps');
  assert.equal(m.els.rtrModalBwDown.value, '2');
  assert.equal(m.down.buttons.gbps.has('active'), true);
  assert.equal(m.down.buttons.mbps.has('active'), false);
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
