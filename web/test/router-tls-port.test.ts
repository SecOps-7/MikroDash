/**
 * The Add/Edit Router dialog: the port follows the TLS switch.
 *
 * ── ISSUE #137 ─────────────────────────────────────────────────────────────
 *
 * `api` listens on 8728 and `api-ssl` on 8729. Turning TLS on and leaving the
 * port at 8728 speaks TLS to the plain service. Reproduced on a test router:
 *
 *	[session] CHR Test: endpoint or credentials changed, reconnecting
 *	[session] CHR Test: could not connect to router os:
 *	          tls: first record does not look like a TLS handshake
 *
 * The session DID reconnect - promptly - to the old port, so nothing ever
 * reached 8729 and the reporter, who was watching `dst-port=8729`, saw no
 * attempt at all. Deleting and re-creating the device worked because a blank
 * port falls back to 8729; editing kept the 8728 already on the record.
 *
 * ── AND A TYPED PORT IS NOT TOUCHED ────────────────────────────────────────
 *
 * The other half of the rule, and the one worth a test of its own: an operator
 * running api-ssl on 8443 must not have it rewritten because they ticked a box.
 * Only the other protocol's default is moved.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id) {
  const listeners = {};
  const node = {
    id, value: '', textContent: '', innerHTML: '', checked: false, disabled: false,
    style: {}, options: [], dataset: {},
    focus() {}, appendChild() {}, insertAdjacentHTML() {},
    setAttribute: (k, v) => { node['__' + k] = v; },
    getAttribute: (k) => (('__' + k) in node ? node['__' + k] : null),
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (listeners[ev] || []).forEach((fn) => fn(arg || {})),
    querySelectorAll: () => [], querySelector: () => null, closest: () => null,
    classList: { add() {}, remove() {}, contains: () => false, toggle() {} },
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
  'rtrModalCancelBtn', 'rtrModalCloseBtn',
];

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-tls-port.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'router-modal.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function mount() {
  const els = {};
  IDS.forEach((id) => { els[id] = makeEl(id); });
  global.document = {
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [],
    querySelector: () => null,
    addEventListener: () => {},
    createElement: () => makeEl(''),
  };
  global.window = { confirm: () => true };
  global.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  mod.initRouterModal({ sites: () => ({}), routers: () => [], onSaved: () => {} });
  return els;
}

/** Set the TLS box and fire `change`, as a click on it does. */
function setTls(els, on) {
  els.rtrModalTls.checked = on;
  els.rtrModalTls.fire('change', {});
}

let failed = 0;
function check(name, fn) {
  try {
    fn();
    say('  ok  ' + name);
  } catch (e) {
    failed += 1;
    say('  FAIL ' + name + '\n       ' + (e && e.message));
  }
}

check('turning TLS on moves the plain default to the TLS one', () => {
  const els = mount();
  els.rtrModalPort.value = '8728';
  setTls(els, true);
  assert.equal(els.rtrModalPort.value, '8729',
    'the port stayed on the plain-API default, so TLS is spoken to the api service '
    + 'and nothing ever reaches 8729 - issue #137');
});

check('turning TLS off moves it back', () => {
  const els = mount();
  els.rtrModalPort.value = '8729';
  setTls(els, false);
  assert.equal(els.rtrModalPort.value, '8728');
});

// A TYPED PORT IS THE OPERATOR'S. This is the half that stops the convenience
// becoming a surprise.
check('a custom port is never rewritten', () => {
  for (const custom of ['8443', '9999', '443']) {
    const els = mount();
    els.rtrModalPort.value = custom;
    setTls(els, true);
    assert.equal(els.rtrModalPort.value, custom,
      'a port the operator typed was rewritten by ticking TLS');
    setTls(els, false);
    assert.equal(els.rtrModalPort.value, custom);
  }
});

// A BLANK PORT IS LEFT BLANK: the server falls back to 8729 by itself, and
// filling it in here would put a number in a box the operator deliberately left
// empty.
check('a blank port is left blank', () => {
  const els = mount();
  els.rtrModalPort.value = '';
  setTls(els, true);
  assert.equal(els.rtrModalPort.value, '');
});

check('whitespace around the default still counts as the default', () => {
  const els = mount();
  els.rtrModalPort.value = ' 8728 ';
  setTls(els, true);
  assert.equal(els.rtrModalPort.value, '8729');
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
