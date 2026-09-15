/**
 * The router dialog's System Identity field, across opens.
 *
 * Adding a device hides the field and reads nothing. It used to leave whatever
 * the last edited device had put there: that router's name, enabled, with its
 * hint, and the dialog still remembering the name as loaded. Nothing wrote it,
 * because the identity is only written for a device with an id, but one device's
 * state reached another's dialog. Opening for a new device must start empty.
 */

import path from 'node:path';
import fs from 'node:fs';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id) {
  const classes = new Set();
  const listeners = {};
  const node = {
    id, value: '', textContent: '', innerHTML: '', style: {}, hidden: false,
    checked: false, disabled: false, options: [],
    focus() {},
    setAttribute: (k, v) => { node['__' + k] = v; },
    getAttribute: (k) => (('__' + k) in node ? node['__' + k] : null),
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (listeners[ev] || []).forEach((fn) => fn(arg || {})),
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c), toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    querySelectorAll: () => [], querySelector: () => null, closest: () => null, appendChild: () => {},
  };
  return node;
}

const IDS = [
  'rtrModalBg', 'rtrModalCollectors', 'rtrModalGeoClear', 'rtrModalGeoHint', 'rtrModalGeoList',
  'rtrModalId', 'rtrModalModeWrap', 'rtrModalPrimarySite', 'rtrModalTitle', 'rtrTestResult',
  'rtrModalAlertsEnabled', 'rtrModalReportingEnabled', 'rtrModalBwDown', 'rtrModalBwDownUnit', 'rtrModalBwUp',
  'rtrModalBwUpUnit', 'rtrModalDownThresh', 'rtrModalGeo', 'rtrModalHost', 'rtrModalIf',
  'rtrModalLabel', 'rtrModalMode', 'rtrModalPass', 'rtrModalPing', 'rtrModalPort',
  'rtrModalSaveBtn', 'rtrModalTestBtn', 'rtrModalTls', 'rtrModalTlsInsecure', 'rtrModalUser',
  'rtrModalIdentityWrap', 'rtrModalIdentity', 'rtrModalIdentityHint',
];

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-router-modal-identity.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'router-modal.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const settle = async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r)); };

(async () => {
  const els = {};
  IDS.forEach((id) => { els[id] = makeEl(id); });
  global.document = {
    getElementById: (id) => els[id] || null,
    querySelector: () => null, querySelectorAll: () => [], addEventListener: () => {},
    createElement: () => makeEl(''),
  };
  global.window = { confirm: () => true };
  global.fetch = (url) => {
    if (url === '/api/routers/r1/identity') {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ available: true, name: 'office-gw' }) });
    }
    if (url === '/api/collectors') {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ collectors: [] }) });
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ ok: true }) });
  };

  const mod = require(OUT);
  const modal = mod.initRouterModal({ sites: () => ({}), routers: () => [], onSaved: () => {} });
  const box = els.rtrModalIdentity;

  // Editing a device reads its name. Without this the check below could pass
  // against a field that is never filled at all.
  modal.open({ id: 'r1', label: 'Office', host: '198.51.100.1' });
  await settle();
  assert.equal(box.value, 'office-gw', 'editing a device did not fill the identity field');
  assert.equal(box.disabled, false, 'the identity field stayed disabled after a successful read');

  // Adding a device starts empty.
  modal.open(null);
  assert.equal(els.rtrModalIdentityWrap.style.display, 'none', 'Add Device shows the identity field');
  assert.equal(box.value, '', 'Add Device kept the previous router\'s identity: ' + JSON.stringify(box.value));
  assert.equal(box.disabled, true, 'Add Device left the identity field enabled');
  assert.equal(els.rtrModalIdentityHint.textContent, '', 'Add Device kept the previous hint');

  console.log('router dialog identity ok');
})().catch((e) => { console.error('  ✗ ' + e.message); process.exit(1); });
