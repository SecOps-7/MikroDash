'use strict';
/**
 * The first-run ROUTER overlay, live against ported.
 *
 * ── WHY A DRIVEN GATE ──────────────────────────────────────────────────────
 *
 * This screen is an INTERLOCK, like the Data Cleanup card: Connect is locked
 * until a connection test passes, and a change to any field that could make that
 * test wrong locks it again. Every rule is about a sequence, and the sequence is
 * what a renderer diff cannot see.
 *
 * It also matters more than most: it is the FIRST screen an operator ever sees,
 * it runs when nothing else works, and its Connect button makes two requests
 * whose order and payloads decide whether the install ends up with a router that
 * is added but not selected.
 *
 * ── NOTHING IS SENT TO A ROUTER ────────────────────────────────────────────
 *
 * `fetch` is scripted. What is compared is the REQUEST each implementation would
 * have made, in order, plus every DOM write.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/setup-overlay-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { freezeCase } = require('./lib/lift.js');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('setup-overlay-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const START = "(function(){\n  var overlay   = $('setupOverlay');";
const i = src.indexOf(START);
if (LIFT.hasReference(ROOT)) if (i === -1) throw new Error('the setup overlay IIFE has moved or been rewritten');
const j = src.indexOf('\n})();', i);
if (LIFT.hasReference(ROOT)) if (j === -1) throw new Error('the setup overlay IIFE is never closed');
// FROZEN — this is the program `vm.runInContext` EXECUTES, so the source is
// what must survive. Freezing the executed text keeps the live half running.
const liveSrc = G.value('liveSrc', () => src.slice(i, j + '\n})();'.length));
if (!liveSrc || liveSrc.length < 100) throw new Error('the recorded liveSrc is empty');

const OUT = path.join(ROOT, 'testdata', '.setup-port.cjs');
const ENTRY = path.join(ROOT, 'testdata', '.setup-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/setup-overlay-wire.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const TEXT_FIELDS = ['setupLabel', 'setupHost', 'setupPort', 'setupUser', 'setupPass',
  'setupIf', 'setupPing'];
const CHECKS = ['setupTls', 'setupTlsInsecure'];

function makeWorld(form) {
  const ops = [];
  const nodes = {};
  const listeners = {};
  const bodyClasses = new Set();
  let paused = 0;

  function node(id, extra) {
    let text = '', disabled = false, title = '';
    const style = new Proxy({}, {
      set(t, k, v) { t[k] = v; ops.push([id, 'style.' + String(k), String(v)]); return true; },
      get(t, k) { return t[k]; },
    });
    const n = {
      _id: id, style,
      get textContent() { return text; },
      set textContent(v) { text = String(v); ops.push([id, 'text', text]); },
      get disabled() { return disabled; },
      set disabled(v) { disabled = !!v; ops.push([id, 'disabled', disabled]); },
      get title() { return title; },
      set title(v) { title = String(v); ops.push([id, 'title', title]); },
      addEventListener(ev, fn) { listeners[id + ':' + ev] = fn; },
    };
    return Object.defineProperties(n, Object.getOwnPropertyDescriptors(extra || {}));
  }

  for (const id of ['setupOverlay', 'setupError', 'setupTestBtn', 'setupSaveBtn',
                    'setupTestResult']) {
    nodes[id] = node(id);
  }
  nodes.setupTestBtn.textContent = 'Test Connection';
  nodes.setupSaveBtn.textContent = 'Connect';
  for (const id of TEXT_FIELDS) {
    let v = form[id] === undefined ? '' : String(form[id]);
    nodes[id] = node(id, { type: 'text', get value() { return v; }, set value(x) { v = String(x); } });
  }
  for (const id of CHECKS) {
    let c = !!form[id];
    nodes[id] = node(id, { type: 'checkbox', get checked() { return c; }, set checked(x) { c = !!x; } });
  }
  nodes.netDiagram = node('netDiagram', { pauseAnimations() { paused++; } });
  ops.length = 0;

  const doc = {
    getElementById: (id) => nodes[id] || null,
    body: { classList: { add: (c) => bodyClasses.add(c), remove: (c) => bodyClasses.delete(c) } },
  };
  return {
    doc, ops, nodes, listeners,
    fire(id, ev) {
      const fn = listeners[id + ':' + ev];
      if (!fn) throw new Error('nothing listening for ' + id + ':' + ev);
      return fn();
    },
    state() {
      return {
        body: [...bodyClasses].sort(), paused,
        nodes: Object.keys(nodes).sort().map((id) => [
          id, nodes[id].textContent, nodes[id].disabled, nodes[id].title,
          nodes[id].value === undefined ? null : nodes[id].value,
          nodes[id].checked === undefined ? null : nodes[id].checked,
        ]),
      };
    },
  };
}

function fetchFor(script, log) {
  return (url, init) => {
    const b = init && init.body ? JSON.parse(init.body) : null;
    log.push(['fetch', url, (init && init.method) || 'GET', b]);
    const key = Object.keys(script).find((k) => url === k || url.startsWith(k));
    const r = key === undefined ? undefined : script[key];
    if (r === undefined) throw new Error('no scripted response for ' + url);
    if (r === 'reject') return Promise.reject(new Error('network'));
    return Promise.resolve({ json: () => Promise.resolve(r) });
  };
}

const flush = () => new Promise((r) => setImmediate(r));

async function runLive(sc) {
  const w = makeWorld(sc.form || {});
  const log = [];
  const handlers = {};
  const ctx = {
    document: w.doc, JSON, Object, String, parseInt, Promise, setImmediate, Error,
    $: (id) => w.doc.getElementById(id),
    socket: { on: (n, f) => { handlers[n] = f; } },
    fetch: fetchFor(sc.script || {}, log),
    _rosCurrentlyDisconnected: false,
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc, ctx);
  await sc.drive({ required: () => handlers['setup:required']() }, w, flush);
  return { log, ops: w.ops, state: w.state(), ros: ctx._rosCurrentlyDisconnected };
}

async function runPort(sc) {
  const w = makeWorld(sc.form || {});
  const log = [];
  const handlers = {};
  const prev = { document: globalThis.document, fetch: globalThis.fetch };
  globalThis.document = w.doc;
  globalThis.fetch = fetchFor(sc.script || {}, log);
  globalThis._rosCurrentlyDisconnected = false;
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initSetupOverlay({ on: (n, f) => { handlers[n] = f; } });
    await sc.drive({ required: () => handlers['setup:required']() }, w, flush);
  } finally {
    Object.assign(globalThis, prev);
  }
  return { log, ops: w.ops, state: w.state(), ros: globalThis._rosCurrentlyDisconnected };
}

const FORM = { setupHost: '198.51.100.7', setupPort: '8729', setupUser: 'admin',
  setupPass: 'synthetic', setupLabel: 'Edge', setupIf: 'ether1', setupPing: '1.1.1.1',
  setupTls: true };
const OK_TEST = { ok: true, boardName: 'hAP ax3' };
const OK_ADD = { ok: true, router: { id: 'r-new' } };
const OK_ACT = { ok: true };

// ── THE FIELD DEFAULTS AND THE PORT FLIP ────────────────────────────────────
//
// RESTORED 2026-08-29. An earlier version of this file drove
// `collectSetupBody`, `flipPortForTls` and `SETUP_WATCH_FIELDS` directly — "12
// field cases, 8 port flips, the 6-field re-lock list, 10 mutations all killed",
// per Changes.md — and it was overwritten by accident.
//
// Rebuilt as SCENARIOS rather than as direct calls, which is stronger: each one
// sets the form, presses Test, and compares the REQUEST BODY. That proves the
// default actually reaches the wire, not merely that a pure function returns it.
// A default that is right in `collectSetupBody` and dropped by the caller passes
// the old shape and fails this one.
const FIELD_CASES = [
  ['an empty username defaults to admin', { setupUser: '' }],
  ['an empty interface defaults to ether1', { setupIf: '' }],
  ['an empty ping target defaults to 1.1.1.1', { setupPing: '' }],
  ['an empty port defaults to 8729', { setupPort: '' }],
  ['an empty label stays empty', { setupLabel: '' }],
  ['an empty password stays empty', { setupPass: '' }],
  ['a NON-NUMERIC port becomes NaN, not the default', { setupPort: 'abc' }],
  ['a port with trailing junk takes the leading number', { setupPort: '8729x' }],
  ['tls off', { setupTls: false }],
  ['tlsInsecure on', { setupTlsInsecure: true }],
  ['every optional field empty at once',
    { setupUser: '', setupIf: '', setupPing: '', setupPort: '', setupLabel: '' }],
  ['a username of spaces is NOT empty, so no default applies', { setupUser: '   ' }],
];

const PORT_FLIPS = [
  ['8728 with tls ON becomes 8729', '8728', false, true],
  ['8729 with tls OFF becomes 8728', '8729', true, false],
  ['8729 with tls ON is unchanged', '8729', false, true],
  ['8728 with tls OFF is unchanged', '8728', true, false],
  ['a custom port is never touched, tls on', '8730', false, true],
  ['a custom port is never touched, tls off', '8730', true, false],
  ['an empty port is never touched', '', false, true],
  ['a non-numeric port is never touched', 'abc', true, false],
];

const SCENARIOS = [
  {
    name: 'the overlay is shown, and the app goes into its disconnected state',
    form: FORM,
    async drive(api) { api.required(); },
  },
  {
    name: 'a passing test unlocks Connect',
    form: FORM, script: { '/api/routers/test': OK_TEST },
    async drive(api, w, f) { api.required(); w.fire('setupTestBtn', 'click'); await f(); await f(); },
  },
  {
    name: 'a passing test WITHOUT a board name',
    form: FORM, script: { '/api/routers/test': { ok: true } },
    async drive(api, w, f) { api.required(); w.fire('setupTestBtn', 'click'); await f(); await f(); },
  },
  {
    name: 'a failing test leaves Connect locked',
    form: FORM, script: { '/api/routers/test': { ok: false, error: 'auth failed' } },
    async drive(api, w, f) { api.required(); w.fire('setupTestBtn', 'click'); await f(); await f(); },
  },
  {
    name: 'a failing test with NO message',
    form: FORM, script: { '/api/routers/test': { ok: false } },
    async drive(api, w, f) { api.required(); w.fire('setupTestBtn', 'click'); await f(); await f(); },
  },
  {
    name: 'a test whose request fails outright',
    form: FORM, script: { '/api/routers/test': 'reject' },
    async drive(api, w, f) { api.required(); w.fire('setupTestBtn', 'click'); await f(); await f(); },
  },
  {
    name: 'NO HOST: the test is refused and the button comes back',
    form: { ...FORM, setupHost: '' }, script: {},
    async drive(api, w, f) { api.required(); w.fire('setupTestBtn', 'click'); await f(); },
  },
  {
    name: 'editing the HOST after a pass re-locks Connect',
    form: FORM, script: { '/api/routers/test': OK_TEST },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.nodes.setupHost.value = '198.51.100.8';
      w.fire('setupHost', 'input');
    },
  },
  {
    name: 'editing the LABEL after a pass does NOT re-lock',
    form: FORM, script: { '/api/routers/test': OK_TEST },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.nodes.setupLabel.value = 'Renamed';
      // No listener at all on this field — firing one would throw, which is
      // itself the assertion.
      if (w.listeners['setupLabel:input']) throw new Error('setupLabel re-locks, and must not');
    },
  },
  {
    name: 'toggling TLS off flips 8729 to 8728',
    form: FORM,
    async drive(api, w) {
      api.required();
      w.nodes.setupTls.checked = false;
      w.fire('setupTls', 'change');
    },
  },
  {
    name: 'toggling TLS does NOT touch a custom port',
    form: { ...FORM, setupPort: '8730' },
    async drive(api, w) {
      api.required();
      w.nodes.setupTls.checked = false;
      w.fire('setupTls', 'change');
    },
  },
  {
    name: 'the full first run: test, then connect',
    form: FORM,
    script: { '/api/routers/test': OK_TEST, '/api/routers/r-new/activate': OK_ACT,
              '/api/routers': OK_ADD },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.fire('setupSaveBtn', 'click'); await f(); await f(); await f(); await f();
    },
  },
  {
    name: 'connect when the ADD is refused',
    form: FORM,
    script: { '/api/routers/test': OK_TEST, '/api/routers': { ok: false, error: 'duplicate host' } },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.fire('setupSaveBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'connect when the add returns no id',
    form: FORM,
    script: { '/api/routers/test': OK_TEST, '/api/routers': { ok: true, router: {} } },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.fire('setupSaveBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'ACTIVATION answering `switching` is success, not failure',
    form: FORM,
    script: { '/api/routers/test': OK_TEST, '/api/routers/r-new/activate': { switching: true },
              '/api/routers': OK_ADD },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.fire('setupSaveBtn', 'click'); await f(); await f(); await f(); await f();
    },
  },
  {
    name: 'activation refused outright',
    form: FORM,
    script: { '/api/routers/test': OK_TEST, '/api/routers': OK_ADD,
              '/api/routers/r-new/activate': { ok: false, error: 'unreachable' } },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.fire('setupSaveBtn', 'click'); await f(); await f(); await f(); await f();
    },
  },
  {
    name: 'Connect pressed WITHOUT a passing test sends nothing',
    form: FORM, script: {},
    async drive(api, w, f) { api.required(); w.fire('setupSaveBtn', 'click'); await f(); },
  },
  ...FIELD_CASES.map(([why, over]) => ({
    name: 'field default: ' + why,
    form: { ...FORM, ...over },
    script: { '/api/routers/test': OK_TEST },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
    },
  })),
  ...PORT_FLIPS.map(([why, port, from, to]) => ({
    name: 'port flip: ' + why,
    form: { ...FORM, setupPort: port, setupTls: from },
    async drive(api, w) {
      api.required();
      w.nodes.setupTls.checked = to;
      w.fire('setupTls', 'change');
    },
  })),
  // THE RE-LOCK LIST, one scenario per field, so a field added to the watch list
  // that should not be there fails as loudly as one missing from it.
  ...['setupHost', 'setupPort', 'setupUser', 'setupPass'].map((id) => ({
    name: 're-lock: editing ' + id + ' after a pass locks Connect again',
    form: FORM, script: { '/api/routers/test': OK_TEST },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.nodes[id].value = 'changed';
      w.fire(id, 'input');
    },
  })),
  ...['setupTls', 'setupTlsInsecure'].map((id) => ({
    name: 're-lock: toggling ' + id + ' after a pass locks Connect again',
    form: FORM, script: { '/api/routers/test': OK_TEST },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.nodes[id].checked = !w.nodes[id].checked;
      w.fire(id, 'change');
    },
  })),
  // AND THE THREE THAT MUST NOT. Asserted by the ABSENCE of a listener, which is
  // how the live app expresses it: those fields are simply not in `watchFields`.
  ...['setupLabel', 'setupIf', 'setupPing'].map((id) => ({
    name: 'no re-lock: ' + id + ' has no watcher at all',
    form: FORM, script: { '/api/routers/test': OK_TEST },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      if (w.listeners[id + ':input'] || w.listeners[id + ':change']) {
        throw new Error(id + ' has a watcher and must not — editing it would make an '
          + 'operator re-run a connection test to fix a typo in a name');
      }
    },
  })),
  {
    name: 'Connect without a passing test sends nothing — with the route SCRIPTED',
    form: FORM,
    // SCRIPTED ON PURPOSE. With no script an unwanted request THROWS, and both
    // sides throw the same way, so the guard could be removed and nothing would
    // differ. Scripting it makes the unwanted request VISIBLE in the log instead.
    script: { '/api/routers': OK_ADD, '/api/routers/r-new/activate': OK_ACT },
    async drive(api, w, f) {
      api.required();
      w.fire('setupSaveBtn', 'click');
      await f(); await f(); await f();
    },
  },
  {
    name: 'a field edited DURING a save leaves Connect locked when it finishes',
    form: FORM,
    script: { '/api/routers/test': OK_TEST, '/api/routers': OK_ADD,
              '/api/routers/r-new/activate': { ok: false, error: 'unreachable' } },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      w.fire('setupSaveBtn', 'click');
      // Mid-flight: the operator corrects the host. The save is already going,
      // and when it fails `setBusy(false)` must NOT hand Connect back — the
      // test that unlocked it no longer describes what is in the form.
      w.nodes.setupHost.value = '198.51.100.99';
      w.fire('setupHost', 'input');
      await f(); await f(); await f(); await f();
    },
  },
  {
    name: 'editing a field after a FAILED test leaves the failure on screen',
    form: FORM, script: { '/api/routers/test': { ok: false, error: 'auth failed' } },
    async drive(api, w, f) {
      api.required();
      w.fire('setupTestBtn', 'click'); await f(); await f();
      // The message is the only thing telling the operator WHY. Clearing it on
      // the first keystroke of the fix wipes it before it has been acted on.
      w.nodes.setupHost.value = '198.51.100.8';
      w.fire('setupHost', 'input');
    },
  },
  {
    name: 'the empty form takes every default',
    form: {}, script: { '/api/routers/test': OK_TEST },
    async drive(api, w, f) {
      api.required();
      w.nodes.setupHost.value = '198.51.100.9';
      w.fire('setupTestBtn', 'click'); await f(); await f();
    },
  },
];

(async () => {
  let bad = 0, total = 0;
  for (const sc of SCENARIOS) {
    // One case object reaches BOTH runs; a mutating drive would leak the live
    // run's state into the port's and make the gate accuse correct code.
    // See lift.js:freezeCase — this happened once and was hard to see.
    freezeCase(sc);
    const live = await runLive(sc);
    const port = await runPort(sc);
    total += live.ops.length;
    if (live.ops.length === 0) {
      console.log('  UNDRIVEN     ' + sc.name + ' — the LIVE side did nothing');
      bad++;
      continue;
    }
    const a = JSON.stringify({ log: live.log, ops: live.ops, state: live.state, ros: live.ros }, null, 1);
    const b = JSON.stringify({ log: port.log, ops: port.ops, state: port.state, ros: port.ros }, null, 1);
    if (a === b) { console.log('  ok  ' + sc.name + '  (' + live.ops.length + ' ops)'); continue; }
    bad++;
    console.log('  DIFF  ' + sc.name);
    const al = a.split('\n'), bl = b.split('\n');
    for (let k = 0, shown = 0; k < Math.max(al.length, bl.length) && shown < 12; k++) {
      if (al[k] !== bl[k]) {
        console.log('        live: ' + (al[k] === undefined ? '(end)' : al[k].trim()));
        console.log('        port: ' + (bl[k] === undefined ? '(end)' : bl[k].trim()));
        shown++;
      }
    }
  }
  fs.rmSync(OUT, { force: true });
  console.log('\n' + SCENARIOS.length + ' scenarios, ' + total + ' operations compared');
  if (bad) { console.log(bad + ' FAILED'); process.exit(1); }
  console.log('all agree');
})();
