// Moved from `tools/router-modal-picker-check.js` when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * The router dialog's TOWN PICKER wiring.
 *
 * ── WRITTEN TO MAKE A REFACTOR SAFE, NOT TO FIND A BUG ──────────────────────
 *
 * The live app has ONE `_mountCityPicker` (app.js:11225) shared by this dialog
 * and the site form. This port grew the dialog's copy inline first, and added a
 * shared `mountCityPicker` later when the site form needed one — so there are
 * two implementations where the original has one.
 *
 * Migrating the dialog onto the shared mount was REFUSED on 2026-08-26 for a
 * stated reason: `city-picker-check` gates the picker's STATE MACHINE, not the
 * fetch, the debounce, the list or the blur, so the dialog's wiring was ungated
 * and moving it would have been an unverifiable refactor of working code. This
 * file removes that objection. It is written and passing BEFORE the migration,
 * so the migration has something to be checked against.
 *
 * ── PORT-ONLY, LIKE sites-card-check ────────────────────────────────────────
 *
 * `_mountCityPicker` cannot be lifted usefully: it closes over the dialog's own
 * elements and its opts, and the live side's behaviour is already pinned for the
 * STATE by `city-picker-check`. What is asserted here is what the wiring does.
 * That catches a regression, not a divergence.
 *
 *   node tools/router-modal-picker-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

// ── the shim ────────────────────────────────────────────────────────────────
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
    closest: () => null,
    appendChild: () => {},
  };
  return node;
}

const IDS = [
  'rtrModalBg', 'rtrModalCollectors', 'rtrModalGeoClear', 'rtrModalGeoHint', 'rtrModalGeoList',
  'rtrModalId', 'rtrModalModeWrap', 'rtrModalPrimarySite', 'rtrModalTitle', 'rtrTestResult',
  'rtrModalAlertsEnabled', 'rtrModalBwDown', 'rtrModalBwDownUnit', 'rtrModalBwUp',
  'rtrModalBwUpUnit', 'rtrModalDownThresh', 'rtrModalGeo', 'rtrModalHost', 'rtrModalIf',
  'rtrModalLabel', 'rtrModalMode', 'rtrModalPass', 'rtrModalPing', 'rtrModalPort',
  'rtrModalSaveBtn', 'rtrModalTestBtn', 'rtrModalTls', 'rtrModalTlsInsecure', 'rtrModalUser',
];

function makeDoc() {
  const els = {};
  IDS.forEach((id) => { els[id] = makeEl(id); });
  return {
    els,
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [],
    addEventListener: () => {},
    createElement: () => makeEl(''),
  };
}

// ── the port ────────────────────────────────────────────────────────────────
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-router-modal.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'router-modal.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const settle = () => new Promise((r) => setImmediate(r));
const afterDebounce = () => new Promise((r) => setTimeout(r, 400));

const BERLIN = { name: 'Berlin', region: 'BE', cc: 'DE', lat: 52.5, lon: 13.4 };
const BERGEN = { name: 'Bergen', region: 'VL', cc: 'NO', lat: 60.4, lon: 5.3 };

function mount(opts) {
  const o = opts || {};
  const doc = makeDoc();
  const calls = [];

  global.document = doc;
  global.window = { confirm: () => true };
  global.fetch = (url, init) => {
    calls.push({ url, method: (init && init.method) || 'GET',
      body: init && init.body ? JSON.parse(init.body) : null });
    if (url.startsWith('/api/cities')) {
      const q = decodeURIComponent(url.split('q=')[1] || '');
      const reply = { ok: true, json: () => Promise.resolve({ cities: (o.cities || {})[q] || [] }) };
      const hold = (o.holdCity || {})[q];
      return hold ? new Promise((r) => { hold.release = () => r(reply); }) : Promise.resolve(reply);
    }
    if (url === '/api/collectors') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ collectors: [] }) });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, router: {} }) });
  };

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  const modal = mod.initRouterModal({
    sites: () => ({}),
    routers: () => [],
    onSaved: () => {},
  });
  return { doc, calls, modal };
}

const problems = [];
let checks = 0;
function check(name, fn) {
  checks++;
  try { fn(); } catch (e) { problems.push(name + ': ' + e.message); }
}

(async () => {
  // 1. TYPING SEARCHES, and picking a row commits it into the box.
  {
    const { doc, calls } = mount({ cities: { berlin: [BERLIN] } });
    doc.els.rtrModalGeo.value = 'berlin';
    doc.els.rtrModalGeo.fire('input');
    await afterDebounce();

    check('typing searches', () => {
      assert.ok(calls.some((c) => c.url.startsWith('/api/cities')), 'no search was made');
      assert.match(doc.els.rtrModalGeoList.innerHTML, /Berlin/,
        'the list holds: ' + doc.els.rtrModalGeoList.innerHTML);
      assert.equal(doc.els.rtrModalGeoList.hidden, false, 'the list stayed hidden');
    });

    doc.els.rtrModalGeoList.fire('click', {
      target: { closest: (sel) => (sel === '[data-i]' ? { getAttribute: () => '0' } : null) },
    });
    check('picking a row commits it', () => {
      assert.match(doc.els.rtrModalGeo.value, /Berlin/,
        'the box reads ' + JSON.stringify(doc.els.rtrModalGeo.value) + ' after a pick');
      assert.equal(doc.els.rtrModalGeoList.hidden, true, 'the list stayed open');
    });
  }

  // 2. A QUERY TOO SHORT does not search. `shouldSearchCity` is gated on its own;
  //    what is asserted here is that the wiring CONSULTS it.
  {
    const { doc, calls } = mount({ cities: { b: [BERLIN] } });
    doc.els.rtrModalGeo.value = 'b';
    doc.els.rtrModalGeo.fire('input');
    await afterDebounce();
    check('a one-character query does not search', () => {
      assert.ok(!calls.some((c) => c.url.startsWith('/api/cities')),
        'a single character searched: ' + JSON.stringify(calls.map((c) => c.url)));
      assert.equal(doc.els.rtrModalGeoList.hidden, true, 'the list was opened anyway');
    });
  }

  // 3. TYPED TEXT IS NOT A LOCATION. Leaving the box restores the committed
  //    text; it must NOT commit, or a previewed automatic location would become
  //    a manual override just from a click in and out.
  {
    const { doc } = mount({ cities: { berlin: [BERLIN] } });
    doc.els.rtrModalGeo.value = 'berlin';
    doc.els.rtrModalGeo.fire('input');
    await afterDebounce();
    doc.els.rtrModalGeoList.fire('click', {
      target: { closest: () => ({ getAttribute: () => '0' }) },
    });
    doc.els.rtrModalGeo.value = 'berg';        // typed over the committed Berlin
    doc.els.rtrModalGeo.fire('blur');
    await afterDebounce();
    check('leaving the box restores the committed text', () => {
      assert.match(doc.els.rtrModalGeo.value, /Berlin/,
        'the box kept the typed text: ' + JSON.stringify(doc.els.rtrModalGeo.value));
    });
  }

  // 4. CLEAR empties the box.
  {
    const { doc } = mount({ cities: { berlin: [BERLIN] } });
    doc.els.rtrModalGeo.value = 'berlin';
    doc.els.rtrModalGeo.fire('input');
    await afterDebounce();
    doc.els.rtrModalGeoList.fire('click', {
      target: { closest: () => ({ getAttribute: () => '0' }) },
    });
    doc.els.rtrModalGeoClear.fire('click');
    check('clear empties the box', () => {
      assert.equal(doc.els.rtrModalGeo.value, '',
        'the box still reads ' + JSON.stringify(doc.els.rtrModalGeo.value));
      assert.equal(doc.els.rtrModalGeoList.hidden, true, 'the list stayed open after a clear');
    });
  }

  // 5. A SLOW ANSWER FOR AN OLD QUERY IS DISCARDED — a "ber" landing after
  //    "bergen" must not repaint the list with the wrong towns.
  {
    const held = { ber: {} };
    const { doc } = mount({ cities: { ber: [BERLIN], bergen: [BERGEN] }, holdCity: held });
    doc.els.rtrModalGeo.value = 'ber';
    doc.els.rtrModalGeo.fire('input');
    await afterDebounce();
    doc.els.rtrModalGeo.value = 'bergen';
    doc.els.rtrModalGeo.fire('input');
    await afterDebounce();
    check('the fast answer shows first', () => {
      assert.match(doc.els.rtrModalGeoList.innerHTML, /Bergen/,
        'the list holds: ' + doc.els.rtrModalGeoList.innerHTML);
    });
    if (held.ber.release) held.ber.release();
    await settle(); await settle();
    check('a stale answer does not repaint the list', () => {
      assert.ok(!/Berlin/.test(doc.els.rtrModalGeoList.innerHTML),
        'a slow answer for an older query repainted the list: '
        + doc.els.rtrModalGeoList.innerHTML);
    });
  }

  // 6. BELIEVABILITY: the box really does start empty and really does change, so
  //    the assertions above are not agreeing with a picker that never runs.
  {
    const { doc } = mount({ cities: { berlin: [BERLIN] } });
    check('the box starts empty', () => {
      assert.equal(doc.els.rtrModalGeo.value, '');
      assert.equal(doc.els.rtrModalGeoList.innerHTML, '');
    });
  }

  if (problems.length) {
    problems.forEach((p) => console.error('  ✗ ' + p));
    console.error('\nrouter-modal-picker-check: ' + problems.length + ' of ' + checks + ' failed');
    process.exit(1);
  }
  console.log('router dialog picker ok (' + checks + ' checks; PORT-ONLY — see the header)');
})();
