'use strict';
/**
 * Settings → Data Cleanup: the live card against the ported one.
 *
 * ── WHY A DRIVEN GATE AND NOT A RENDERER DIFF ───────────────────────────────
 *
 * Almost nothing on this card is a render. It is an INTERLOCK: `Delete data`
 * starts disabled, a preview enables it, and any change to the selection takes
 * it away again. Every rule is about a sequence, and a sequence is invisible to
 * a gate that compares one payload to one blob of HTML.
 *
 * So both implementations are driven through the same scripted sessions against
 * the same fake DOM, and what is compared is the ORDERED LOG of everything each
 * one did — every property write, every fetch body, every confirm — plus the
 * final state of the document. A port that reaches the right end state by the
 * wrong route fails here, which matters because the wrong route on this card is
 * a delete the operator did not confirm.
 *
 * ── THE SEQUENCES ───────────────────────────────────────────────────────────
 *
 *   preview then delete        the happy path, and the only one that may delete
 *   preview then change scope  the delete must be locked again: the count on
 *                              screen is no longer the count that would go
 *   double-clicked delete      the second click must find the preview spent
 *   preview matching nothing   a different message AND no enable
 *   confirm declined           nothing is sent at all
 *   a failed preview           the error is shown and the delete stays locked
 *   a router that is gone      its rows are still listed and still selectable
 *
 *   MIKRODASH_SRC=../MikroDash node tools/dbcleanup-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { freezeCase } = require('./lib/lift.js');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('dbcleanup-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// ── Lift the live IIFE ──────────────────────────────────────────────────────
const START = "(function(){\n  var scope   = $('dbcScope'),";
// GUARDED: both ask whether the live SOURCE still holds the anchor. `i` and `j`
// feed the frozen slice below, which does not run on replay.
const i = src.indexOf(START);
if (LIFT.hasReference(ROOT) && i === -1) {
  throw new Error('the Data Cleanup IIFE has moved or been rewritten — ' +
                  'this gate must be re-anchored before it can be trusted');
}
const j = src.indexOf('\n})();', i);
if (LIFT.hasReference(ROOT) && j === -1) throw new Error('the Data Cleanup IIFE is never closed');
// FROZEN — a plain two-index slice, the third form freeze-src.py misses.
const liveSrc = G.value('liveSrc', () => src.slice(i, j + '\n})();'.length));
if (!liveSrc || liveSrc.length < 200) {
  throw new Error('the recorded live source is empty — the golden is broken');
}

// The two helpers it borrows from app.js's top level. Lifted rather than
// re-implemented: `fmtBytes` decides every size string on the card, and a gate
// that supplied its own would be comparing the port against this file.
function lift(decl, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const a = src.indexOf(decl);
  if (a === -1) throw new Error('cannot find ' + name);
  const b = src.indexOf('\n', a);
  return src.slice(a, b);
}
const fmtBytesSrc = G.value('fmtBytesSrc', () => lift('function fmtBytes(b){', 'fmtBytes'));
const escSrc = G.value('escSrc', () => {
  const a = src.indexOf('function esc(');
  if (a === -1) throw new Error('cannot find esc');
  const b = src.indexOf('\n}', a);
  if (b === -1) return src.slice(a, src.indexOf('\n', a));
  return src.slice(a, b + 2);
});
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['fmtBytesSrc', fmtBytesSrc], ['escSrc', escSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

// ── Bundle the port ─────────────────────────────────────────────────────────
const ENTRY = path.join(ROOT, 'testdata', '.dbcleanup-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/dbcleanup.js';\n");
const OUT = path.join(ROOT, 'testdata', '.dbcleanup-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── The fake DOM ────────────────────────────────────────────────────────────
//
// Every write is LOGGED as well as stored. The stored value answers "does it end
// up right"; the log answers "did it get there the same way", and on this card
// the second question is the one with the safety property in it.

function makeWorld() {
  const ops = [];
  const listeners = {};   // "<id>:<event>" -> fn
  const nodes = {};

  function node(id, extra) {
    let text = '', html = '', cls = '', disabled = false;
    const n = {
      _id: id,
      get textContent() { return text; },
      set textContent(v) { text = String(v); ops.push([id, 'text', text]); },
      get innerHTML() { return html; },
      set innerHTML(v) { html = String(v); ops.push([id, 'html', html]); },
      get className() { return cls; },
      set className(v) { cls = String(v); ops.push([id, 'class', cls]); },
      get disabled() { return disabled; },
      set disabled(v) { disabled = !!v; ops.push([id, 'disabled', disabled]); },
      addEventListener(ev, fn) { listeners[id + ':' + ev] = fn; },
    };
    // `defineProperties` with the DESCRIPTORS, never `Object.assign`.
    //
    // `Object.assign` reads a getter and copies its RESULT, so every accessor in
    // `extra` — `dbcScope.value`, `dbcAge.value` — landed as a frozen data
    // property holding whatever it returned at construction. The select then
    // ignored every `setScope`/`setAge` this file performs, and four scenarios
    // that read as "change the age, then preview" were quietly previewing the
    // default. Two mutants survived on exactly that and nothing else.
    return Object.defineProperties(n, Object.getOwnPropertyDescriptors(extra || {}));
  }

  // The scope select. `innerHTML = '<option…>'` then appendChild for the rest,
  // which is what both implementations do, so the option list is rebuilt from
  // the log rather than parsed out of the HTML string.
  let scopeValue = '';
  let options = [];
  const scope = node('dbcScope', {
    get value() { return scopeValue; },
    set value(v) {
      // A select whose value is set to something no option carries falls back to
      // ''. Reproduced, because that is exactly the case `renderScope` relies on
      // when the selected router has disappeared.
      const want = String(v);
      scopeValue = (want === '' || options.some((o) => o.value === want)) ? want : '';
      ops.push(['dbcScope', 'value', scopeValue]);
    },
    appendChild(o) {
      options.push({ value: o.value, text: o.text });
      ops.push(['dbcScope', 'option', o.value, o.text]);
    },
  });
  // Rebuilding the list clears the options it was holding.
  const rawHtml = Object.getOwnPropertyDescriptor(scope, 'innerHTML');
  Object.defineProperty(scope, 'innerHTML', {
    get: rawHtml.get,
    // A REAL select loses its selection when its options are replaced, and
    // `renderScope` depends on that: it reads `.value` first and writes it back
    // afterwards precisely because the rebuild clears it. A fake that kept the
    // value made the write a no-op, so removing it changed nothing.
    set(v) { options = []; scopeValue = ''; rawHtml.set.call(scope, v); },
  });
  nodes.dbcScope = scope;

  let ageValue = '30';
  nodes.dbcAge = node('dbcAge', {
    get value() { return ageValue; },
    set value(v) { ageValue = String(v); },
  });

  // The type checkboxes.
  const boxes = [
    { value: 'traffic', checked: true }, { value: 'ping', checked: true },
    { value: 'bandwidth', checked: true }, { value: 'events', checked: true },
  ];
  nodes.dbcTypes = node('dbcTypes', {
    querySelectorAll(sel) {
      if (sel !== 'input:checked') throw new Error('unexpected selector: ' + sel);
      return boxes.filter((b) => b.checked);
    },
  });

  nodes.dbcPreviewBtn = node('dbcPreviewBtn');
  nodes.dbcPurgeBtn = node('dbcPurgeBtn');
  nodes.dbcSummary = node('dbcSummary');
  nodes.dbcResult = node('dbcResult');
  nodes.dbcSize = node('dbcSize');
  nodes.dbcRows = node('dbcRows');
  nodes.dbcOldest = node('dbcOldest');
  nodes.dbcByRouter = node('dbcByRouter');
  // The markup's own starting state, from page-settings.html. Not an operation —
  // both implementations READ these (the button labels are captured at init), so
  // they are set before the log is cleared.
  nodes.dbcPreviewBtn.textContent = 'Preview';
  nodes.dbcPurgeBtn.textContent = 'Delete data';
  nodes.dbcPurgeBtn.disabled = true;
  ops.length = 0;

  const doc = {
    getElementById: (id) => nodes[id] || null,
    createElement: () => ({ value: '', text: '' }),
    addEventListener(ev, fn) { listeners['document:' + ev] = fn; },
  };

  return {
    doc, ops, boxes, listeners, nodes,
    fire(id, ev, arg) {
      const fn = listeners[id + ':' + ev];
      if (!fn) throw new Error('nothing listening for ' + id + ':' + ev);
      return fn(arg);
    },
    setScope(v) { scopeValue = v; },
    setAge(v) { ageValue = v; },
    state() {
      return {
        options,
        nodes: Object.keys(nodes).sort().map((id) => [
          id, nodes[id].textContent, nodes[id].innerHTML, nodes[id].className, nodes[id].disabled,
        ]),
      };
    },
  };
}

// ── The scripted responses ──────────────────────────────────────────────────
//
// Keyed by URL and, for the purge, by whether the body asked for a dry run, so
// one scenario can script a preview and the delete that follows it.

const ROUTERS = {
  routers: [
    { id: 'r-alpha', label: 'Alpha', host: '10.0.0.2' },
    { id: 'r-beta', label: '', host: '10.0.0.4' },     // falls back to the host
    { id: 'r-amp', label: 'Ops & Eng', host: '10.0.0.5' },  // needs escaping
  ],
};
const STATS = {
  ok: true, bytes: 1548288, total: 41234, oldestTs: 1699827200000,
  byRouter: [
    { routerId: 'r-alpha', rows: 30000 },
    { routerId: 'r-gone', rows: 11234 },   // a router that no longer exists
    { routerId: 'r-amp', rows: 0 },
  ],
};

function fetchFor(script, log) {
  return (url, init) => {
    const body = init && init.body ? JSON.parse(init.body) : null;
    log.push(['fetch', url, body]);
    let key = url;
    if (url === '/api/db/purge') key = body && body.dryRun ? 'preview' : 'delete';
    const r = script[key];
    if (r === undefined) throw new Error('no scripted response for ' + key);
    if (r === 'reject') return Promise.reject(new Error('network'));
    return Promise.resolve({ json: () => Promise.resolve(r) });
  };
}

const flush = () => new Promise((r) => setImmediate(r));

// ── The two runners ─────────────────────────────────────────────────────────

async function runLive(scenario) {
  const w = makeWorld();
  const log = [];
  const ctx = {
    document: w.doc, Array, JSON, Object, String, Math, Date, Promise, parseInt, Number,
    setImmediate,
    $: (id) => w.doc.getElementById(id),
    fetch: fetchFor(scenario.script, log),
    confirm: (msg) => { log.push(['confirm', msg]); return scenario.confirm !== false; },
  };
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\n' + fmtBytesSrc + '\n' + liveSrc, ctx);
  await scenario.drive(w, flush);
  return { log, ops: w.ops, state: w.state() };
}

async function runPort(scenario) {
  const w = makeWorld();
  const log = [];
  const prev = {
    document: globalThis.document, fetch: globalThis.fetch, confirm: globalThis.confirm,
  };
  globalThis.document = w.doc;
  globalThis.fetch = fetchFor(scenario.script, log);
  globalThis.confirm = (msg) => { log.push(['confirm', msg]); return scenario.confirm !== false; };
  try {
    // FRESH EACH TIME. The module holds `known`, `pendingCount` and the two
    // button labels at module scope, exactly as the live IIFE holds them in its
    // closure — a cached module would carry one scenario's spent preview into
    // the next and make the interlock look weaker or stronger than it is.
    delete require.cache[require.resolve(OUT)];
    require(OUT).initDbCleanup();
    await scenario.drive(w, flush);
  } finally {
    globalThis.document = prev.document;
    globalThis.fetch = prev.fetch;
    globalThis.confirm = prev.confirm;
  }
  return { log, ops: w.ops, state: w.state() };
}

// ── The scenarios ───────────────────────────────────────────────────────────

const OK_PREVIEW = {
  ok: true, dryRun: true, total: 1234,
  // `bandwidth: 0` is deliberate: a zero must be dropped from the breakdown.
  byType: { traffic: 1000, ping: 234, bandwidth: 0 },
};
const OK_DELETE = { ok: true, deleted: 1234, bytesBefore: 1548288, bytesAfter: 1024000 };
const BASE = { '/api/routers': ROUTERS, '/api/db/stats': STATS };

async function pageChange(w, f) {
  w.listeners['document:mikrodash:pagechange']({ detail: 'settings' });
  await f(); await f(); await f(); await f();
}

const SCENARIOS = [
  {
    name: 'the page opens: stats render and the scope list is built',
    script: BASE,
    drive: pageChange,
  },
  {
    name: 'a router that no longer exists is named and stays selectable',
    script: BASE,
    async drive(w, f) {
      await pageChange(w, f);
      w.setScope('r-gone');
      w.fire('dbcScope', 'change');
      await pageChange(w, f);   // a second load must keep the selection
    },
  },
  {
    name: 'preview then delete',
    script: { ...BASE, preview: OK_PREVIEW, delete: OK_DELETE },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
      w.fire('dbcPurgeBtn', 'click'); await f(); await f(); await f(); await f(); await f();
    },
  },
  {
    name: 'preview, then a change of scope locks the delete again',
    script: { ...BASE, preview: OK_PREVIEW },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
      w.setScope('r-alpha');
      w.fire('dbcScope', 'change');
    },
  },
  {
    name: 'preview, then a change of age locks the delete again',
    script: { ...BASE, preview: OK_PREVIEW },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
      w.setAge('365');
      w.fire('dbcAge', 'change');
    },
  },
  {
    name: 'the delete is clicked twice: the second finds the preview spent',
    script: { ...BASE, preview: OK_PREVIEW, delete: OK_DELETE },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
      w.fire('dbcPurgeBtn', 'click');
      w.fire('dbcPurgeBtn', 'click');   // no await: the first is still in flight
      await f(); await f(); await f(); await f(); await f();
    },
  },
  {
    name: 'the confirm is declined: nothing is sent',
    script: { ...BASE, preview: OK_PREVIEW },
    confirm: false,
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
      w.fire('dbcPurgeBtn', 'click'); await f(); await f();
    },
  },
  {
    name: 'the preview matches nothing',
    script: { ...BASE, preview: { ok: true, dryRun: true, total: 0, byType: {} } },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'the preview is refused by the server',
    script: { ...BASE, preview: { ok: false, error: 'Invalid age filter' } },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'the preview request fails outright',
    script: { ...BASE, preview: 'reject' },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'the delete is refused by the server',
    script: { ...BASE, preview: OK_PREVIEW, delete: { ok: false, error: 'Router not permitted' } },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
      w.fire('dbcPurgeBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'every type is unchecked',
    script: BASE,
    async drive(w, f) {
      await pageChange(w, f);
      w.boxes.forEach((b) => { b.checked = false; });
      w.fire('dbcPreviewBtn', 'click'); await f();
    },
  },
  {
    name: 'one type only, and a router whose label needs escaping',
    script: { ...BASE, preview: { ok: true, dryRun: true, total: 7, byType: { ping: 7 } } },
    async drive(w, f) {
      await pageChange(w, f);
      w.boxes.forEach((b) => { b.checked = b.value === 'ping'; });
      w.setScope('r-amp');
      w.fire('dbcScope', 'change');
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'everything, of any age',
    script: {
      ...BASE,
      preview: { ok: true, dryRun: true, total: 41234, byType: { traffic: 30000, ping: 11234 } },
    },
    async drive(w, f) {
      await pageChange(w, f);
      w.setAge('0');
      w.fire('dbcAge', 'change');
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'one day, so the message is singular',
    script: { ...BASE, preview: { ok: true, dryRun: true, total: 3, byType: { events: 3 } } },
    async drive(w, f) {
      await pageChange(w, f);
      w.setAge('1');
      w.fire('dbcAge', 'change');
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
    },
  },
  {
    name: 'a delete after which the file GREW: freed must not go negative',
    script: {
      ...BASE, preview: OK_PREVIEW,
      delete: { ok: true, deleted: 5, bytesBefore: 1000, bytesAfter: 2000 },
    },
    async drive(w, f) {
      await pageChange(w, f);
      w.fire('dbcPreviewBtn', 'click'); await f(); await f(); await f();
      w.fire('dbcPurgeBtn', 'click'); await f(); await f(); await f(); await f(); await f();
    },
  },
  {
    name: 'the router list fails but the stats still render',
    script: { ...BASE, '/api/routers': 'reject' },
    drive: pageChange,
  },
];

// ── Run and compare ─────────────────────────────────────────────────────────

(async () => {
  let bad = 0;
  let totalOps = 0;
  for (const sc of SCENARIOS) {
    // One case object reaches BOTH runs; a mutating drive would leak the live
    // run's state into the port's and make the gate accuse correct code.
    // See lift.js:freezeCase — this happened once and was hard to see.
    freezeCase(sc);
    const live = await runLive(sc);
    const port = await runPort(sc);
    totalOps += live.ops.length;

    // BELIEVABILITY. Two implementations that both did nothing agree perfectly,
    // and this gate would then report a clean run over a card that never
    // mounted. The LIVE side alone has to have done something first.
    if (live.ops.length === 0) {
      console.log('  UNDRIVEN     ' + sc.name +
                  ' — the LIVE card performed no DOM operation at all');
      bad++;
      continue;
    }

    const a = JSON.stringify({ log: live.log, ops: live.ops, state: live.state }, null, 1);
    const b = JSON.stringify({ log: port.log, ops: port.ops, state: port.state }, null, 1);
    if (a === b) {
      console.log('  ok  ' + sc.name + '  (' + live.ops.length + ' ops)');
      continue;
    }
    bad++;
    console.log('  DIFF  ' + sc.name);
    const al = a.split('\n'), bl = b.split('\n');
    for (let k = 0, shown = 0; k < Math.max(al.length, bl.length) && shown < 14; k++) {
      if (al[k] !== bl[k]) {
        console.log('        live: ' + (al[k] === undefined ? '(end)' : al[k].trim()));
        console.log('        port: ' + (bl[k] === undefined ? '(end)' : bl[k].trim()));
        shown++;
      }
    }
  }
  fs.rmSync(OUT, { force: true });
  console.log('\n' + SCENARIOS.length + ' scenarios, ' + totalOps +
              ' live DOM operations compared');
  if (bad) {
    console.log(bad + ' FAILED');
    process.exit(1);
  }
  console.log('all agree');
})();
