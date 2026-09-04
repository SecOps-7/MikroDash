/**
 * The per-router Reporting toggle in the Add/Edit Device dialog.
 *
 * ── SEEDED BUT NOT COLLECTED IS THE SHAPE THAT LOSES DATA ───────────────────
 *
 * `routerFormValues` seeds the dialog and `collectRouterForm` builds the save
 * body, and they are two separate literal field lists. A field present in the
 * first and missing from the second is SHOWN to the operator, edited by them,
 * and thrown away on save — with no error anywhere. This file's own history
 * records that shape more than once, so both directions are driven here rather
 * than only the one that renders.
 *
 * The dialog's other half is the server: absent means OFF, and the startup
 * migration is what gives an upgrading install the value it had before the flag
 * existed. So an EDIT follows the record and an ADD defaults ON, which is what
 * the two seeding cases below pin.
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
    id, value: '', textContent: '', innerHTML: '', style: {},
    hidden: false, checked: false, disabled: false, options: [], children: [],
    focus() {},
    setAttribute: (k, v) => { node['__' + k] = v; },
    getAttribute: (k) => (('__' + k) in node ? node['__' + k] : null),
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (listeners[ev] || []).forEach((fn) => fn(arg || {})),
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    querySelectorAll: () => [], querySelector: () => null,
    closest: () => null, appendChild: () => {},
  };
  return node;
}

const IDS = [
  'rtrModalBg', 'rtrModalCollectors', 'rtrModalGeoClear', 'rtrModalGeoHint', 'rtrModalGeoList',
  'rtrModalId', 'rtrModalModeWrap', 'rtrModalPrimarySite', 'rtrModalTitle', 'rtrTestResult',
  'rtrModalAlertsEnabled', 'rtrModalReportingEnabled', 'rtrModalBwDown', 'rtrModalBwDownUnit',
  'rtrModalBwUp', 'rtrModalBwUpUnit', 'rtrModalDownThresh', 'rtrModalGeo', 'rtrModalHost',
  'rtrModalIf', 'rtrModalLabel', 'rtrModalMode', 'rtrModalPass', 'rtrModalPing', 'rtrModalPort',
  'rtrModalSaveBtn', 'rtrModalTestBtn', 'rtrModalTls', 'rtrModalTlsInsecure', 'rtrModalUser',
];

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-router-reporting.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'router-modal.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function mount() {
  const els = {};
  IDS.forEach((id) => { els[id] = makeEl(id); });
  const calls = [];
  global.document = {
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener: () => {}, createElement: () => makeEl(''),
  };
  global.window = { confirm: () => true };
  global.fetch = (url, init) => {
    calls.push({ url, method: (init && init.method) || 'GET',
      body: init && init.body ? JSON.parse(init.body) : null });
    if (url === '/api/collectors') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ collectors: [] }) });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, router: {} }) });
  };
  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  const modal = mod.initRouterModal({
    sites: () => ({}), routers: () => [], onSaved: () => {},
  });
  return { els, calls, modal };
}

const settle = () => new Promise((r) => setImmediate(r));

let failed = 0;
async function check(what, fn) {
  try { await fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

(async () => {
  say('router dialog: the Reporting toggle');

  await check('editing a device with reporting OFF shows it off', async () => {
    const m = mount();
    m.modal.open({ id: 'r1', host: '198.51.100.1', reportingEnabled: false });
    assert.equal(m.els.rtrModalReportingEnabled.checked, false);
  });

  await check('editing a device with reporting ON shows it on', async () => {
    const m = mount();
    m.modal.open({ id: 'r1', host: '198.51.100.1', reportingEnabled: true });
    assert.equal(m.els.rtrModalReportingEnabled.checked, true);
  });

  // ABSENT IS OFF, matching `store.ReportingOn`. Disagreeing with the server
  // here would show a device as reporting when it records nothing.
  await check('a record that says nothing shows OFF', async () => {
    const m = mount();
    m.modal.open({ id: 'r1', host: '198.51.100.1' });
    assert.equal(m.els.rtrModalReportingEnabled.checked, false,
      'an absent setting rendered as ON, which disagrees with the server');
  });

  await check('ADDING a device defaults to ON', async () => {
    const m = mount();
    m.modal.open(null);
    assert.equal(m.els.rtrModalReportingEnabled.checked, true,
      'a newly added device would record nothing and its Reports would be empty');
  });

  // ── THE HALF THAT LOSES DATA ──────────────────────────────────────────────
  await check('the save body carries the toggle', async () => {
    const m = mount();
    m.modal.open({ id: 'r1', host: '198.51.100.1', reportingEnabled: false });
    m.els.rtrModalHost.value = '198.51.100.1';
    m.els.rtrModalReportingEnabled.checked = true;   // the operator turns it on
    m.els.rtrModalSaveBtn.fire('click');
    await settle(); await settle();

    const save = m.calls.filter((c) => c.method === 'PUT' || c.method === 'POST')
      .find((c) => c.body && 'host' in c.body);
    assert.ok(save, 'no save request was sent: ' + JSON.stringify(m.calls.map((c) => c.url)));
    assert.equal(save.body.reportingEnabled, true,
      'the save body is ' + JSON.stringify(save.body.reportingEnabled) +
      ' — the toggle is shown, edited, and thrown away');
  });

  await check('turning it OFF is carried too', async () => {
    const m = mount();
    m.modal.open({ id: 'r1', host: '198.51.100.1', reportingEnabled: true });
    m.els.rtrModalHost.value = '198.51.100.1';
    m.els.rtrModalReportingEnabled.checked = false;
    m.els.rtrModalSaveBtn.fire('click');
    await settle(); await settle();

    const save = m.calls.filter((c) => c.method === 'PUT' || c.method === 'POST')
      .find((c) => c.body && 'host' in c.body);
    assert.ok(save, 'no save request was sent');
    assert.equal(save.body.reportingEnabled, false,
      'turning reporting off did not reach the server');
  });

  if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
  say('\nall passed');
})();
