'use strict';
/**
 * The AUDIT page, live against ported. Built on `tools/lib/lift.js`.
 *
 * ── DRIVEN THROUGH `fetch`, AND THE QUERY STRING IS COMPARED TOO ────────────
 *
 * This page has no socket handler: it builds a query string from four controls,
 * fetches, and renders. The URL is as much a contract as the markup — a filter
 * that silently stops being sent returns MORE rows, which looks like data rather
 * than a fault — so the stubbed fetch records what was asked for and both sides'
 * URLs are compared alongside their DOM.
 *
 * ── THE DETAIL CELL PARSES STORED JSON ──────────────────────────────────────
 *
 * `detailCell` reads a JSON blob and renders "field: from → to". Malformed JSON
 * falls back to a truncated raw string rather than throwing, which matters
 * because the blob is written by an older version of the app than the one
 * reading it. Both paths are in the corpus, and so is the four-change cap.
 *
 * Timestamps run under BOTH timezone settings, as in the Reports gate: the
 * no-timezone branch is a separate implementation and it is the default.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/audit-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/audit-page-check.js --freeze
const G = L.golden('audit-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '/* ── Audit page',
  must: ['auditTable', 'outcomeCell', 'detailCell'],
  mustNot: ['Routing page', 'CAPsMAN page', 'Bridges page', 'backupsPage'],
}));
const IDS = G.value('IDS', () => L.idsFor(src, iife));
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

const ENTRY = path.join(ROOT, 'testdata', '.au-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { initAuditPage } from '../web/src/pages/audit.js';",
  "export { setReportTimezone } from '../web/src/pages/reports.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.au-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── THE `/next/` PREFIX IS AN INTENDED DIFFERENCE ───────────────────────────
//
// The port stages its endpoints under `/next/` while it runs beside the Node app
// — `PORT-QUEUE.md` records it for Audit, Reports and Settings, and says the
// prefix comes off at cutover. So the URLs are compared with that prefix
// normalised away: everything else about the query string still has to match,
// which is the part a DOM comparison cannot see.
//
// Normalised rather than ignored: dropping the URL comparison entirely would
// take the filters with it, and a filter that silently stops being sent returns
// MORE rows, which reads as data rather than as a fault.
const normUrl = (u) => String(u).replace(/^\/next\//, '/');

const snap = (doc, urls) => {
  const n = doc.nodes;
  const out = { urls: urls.map(normUrl) };
  for (const id of IDS.slice().sort()) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent,
      v: n[id].value, d: n[id].style && n[id].style.display } : null;
  }
  return JSON.stringify(out);
};

const ZONES = ['UTC', ''];

/**
 * Let every pending promise settle.
 *
 * A FIXED number of turns is a guess at how deep a `.then` chain is, and
 * guessing short renders nothing — which the Schedules gate learned once and
 * this one repeated. `setImmediate` runs after all pending microtasks; several
 * of them cover a chain that awaits a fetch, then a json(), then renders.
 */
const drain = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r));
};

/** Enter the page, which is what makes either side fetch. */
function pageEnter(doc) {
  for (const fn of (doc.listeners['mikrodash:pagechange'] || []).slice()) {
    fn({ detail: 'audit' });
  }
}

function fetchStub(payload, urls) {
  return (url) => {
    urls.push(String(url));
    return Promise.resolve({ ok: true, json: () => Promise.resolve(payload) });
  };
}

async function liveRun(payload, opts, TZ) {
  const o = opts || {};
  const doc = makeDoc(IDS, {});
  for (const [id, v] of Object.entries(o.filters || {})) {
    if (doc.nodes[id]) doc.nodes[id].value = v;
  }
  const urls = [];
  const ctx = {
    String, Array, Math, Number, Object, JSON, Date, Promise, parseInt, parseFloat,
    isFinite, encodeURIComponent, URLSearchParams, Intl,
    document: doc,
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    window: { location: { origin: 'http://x' } },
    fetch: fetchStub(payload, urls),
    __load: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    // These were missing, and the failure was SILENT: `load()` ends in
    // `.catch(function(){})`, so a ReferenceError inside `render` was swallowed
    // and the table simply stayed empty. The gate saw "the port rendered an
    // empty state and live rendered nothing" — which reads as a port defect and
    // was a missing lift. A swallowed error is worse than a thrown one exactly
    // here: there is nothing to read.
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    L.whole(src, 'function fmtBytes('),
    L.line(src, 'var _displayTimezone'),
    'function $(id){return document.getElementById(id);}',
    'function _debounce(fn){return fn;}',
    'function pageVisible(){return true;}',
    L.declare(L.fileScopeEls(src, iife)),
    '(function () {' + iife + '\n__load = load;\n})();',
  ].join('\n'), ctx);
  assert.ok(ctx.__load, 'the region did not publish its loader');
  vm.runInContext('_displayTimezone = ' + JSON.stringify(TZ) + ';', ctx);
  // THROUGH THE REAL TRIGGER, not the loader directly. Both sides fetch on
  // `mikrodash:pagechange` — the live comment says why: the trail is history,
  // and a page that reloads itself while being read is worse than one that does
  // not. Calling `load()` here while the port waited for the event made the port
  // look like it fetched nothing.
  pageEnter(doc);
  await drain();
  return snap(doc, urls);
}

async function portRun(payload, opts, TZ) {
  const o = opts || {};
  const doc = makeDoc(IDS, {});
  for (const [id, v] of Object.entries(o.filters || {})) {
    if (doc.nodes[id]) doc.nodes[id].value = v;
  }
  const urls = [];
  const prevWin = globalThis.window;
  const prevFetch = globalThis.fetch;
  const prevST = globalThis.setTimeout;
  globalThis.window = { location: { origin: 'http://x' } };
  globalThis.fetch = fetchStub(payload, urls);
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  try {
    const prevDoc = globalThis.document;
    globalThis.document = doc;
    try {
      delete require.cache[require.resolve(OUT)];
      const mod = require(OUT);
      if (mod.setReportTimezone) mod.setReportTimezone(TZ);
      mod.initAuditPage();
      pageEnter(doc);
      await drain();
      return snap(doc, urls);
    } finally {
      if (prevDoc === undefined) delete globalThis.document; else globalThis.document = prevDoc;
    }
  } finally {
    globalThis.setTimeout = prevST;
    globalThis.fetch = prevFetch;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    const A = JSON.parse(a), B = JSON.parse(b);
    for (const k of Object.keys(A)) {
      const x = JSON.stringify(A[k]), y = JSON.stringify(B[k]);
      if (x !== y) {
        // Report the first DIVERGING fragment, not the first 320 characters: an
        // audit row is long and the difference is usually in the last cell.
        let i = 0;
        while (i < Math.min(x.length, y.length) && x[i] === y[i]) i++;
        shout('DIFF %s [%s] at char %d\n  live: …%s\n  port: …%s', what, k, i,
          x.slice(Math.max(0, i - 20), i + 100), y.slice(Math.max(0, i - 20), i + 100));
      }
    }
  }
}

const TS = 1756000000000;
// Read off `flat()`, not guessed: the row is SNAKE_CASE from the database —
// `actor_name`, `actor_ip`, `target_name` falling back to `target_id`. My first
// fixture used the flattened names and every row rendered blank cells that the
// port matched exactly.
const R = (o) => Object.assign({
  id: 1, ts: TS, actor_name: 'kim', actor_ip: '198.51.100.9',
  action: 'settings.save', outcome: 'ok', target_name: 'router1', target_id: 'r1',
  detail: '', router_id: 'r1', router_name: 'core',
}, o);
const P = (o) => Object.assign({
  ok: true, rows: [], total: 0,
  facets: { actors: ['kim', 'sam'], actions: ['settings.save', 'backup.run'] },
}, o);

const CASES = {
  'no rows': [P({}), {}],
  'one row': [P({ rows: [R({})], total: 1 }), {}],
  'several rows': [P({ rows: [R({}), R({ id: 2, actor_name: 'sam' })], total: 2 }), {}],
  // Outcomes.
  'an ok outcome': [P({ rows: [R({ outcome: 'ok' })], total: 1 }), {}],
  'a DENIED outcome': [P({ rows: [R({ outcome: 'denied' })], total: 1 }), {}],
  'a FAILED outcome': [P({ rows: [R({ outcome: 'failed' })], total: 1 }), {}],
  'an unknown outcome reads as ok': [P({ rows: [R({ outcome: 'weird' })], total: 1 }), {}],
  // The detail cell parses stored JSON.
  'no detail': [P({ rows: [R({ detail: '' })], total: 1 }), {}],
  'one change': [P({ rows: [R({ detail: JSON.stringify({ changes: [
    { field: 'pollMs', from: 1000, to: 2000 }] }) })], total: 1 }), {}],
  'four changes': [P({ rows: [R({ detail: JSON.stringify({ changes: Array.from(
    { length: 4 }, (_, i) => ({ field: 'f' + i, from: i, to: i + 1 })) }) })], total: 1 }), {}],
  'FIVE changes are capped at four': [P({ rows: [R({ detail: JSON.stringify({ changes: Array.from(
    { length: 5 }, (_, i) => ({ field: 'f' + i, from: i, to: i + 1 })) }) })], total: 1 }), {}],
  'MALFORMED json falls back to raw': [P({ rows: [R({ detail: 'not json at all' })], total: 1 }), {}],
  'malformed json longer than 120 chars': [P({ rows: [R({ detail: 'x'.repeat(200) })], total: 1 }), {}],
  'json with no changes key': [P({ rows: [R({ detail: '{"other":1}' })], total: 1 }), {}],
  // VALID JSON THAT IS NOT AN OBJECT. `JSON.parse` succeeds on a bare string or
  // number, so the malformed-JSON branch is not reached and a second `typeof d
  // !== 'object'` guard catches it — a separate 120-char truncation that the
  // unparseable case cannot exercise.
  'detail that is a valid JSON STRING': [P({ rows: [R({ detail: JSON.stringify('x'.repeat(200)) })], total: 1 }), {}],
  'detail that is a valid JSON number': [P({ rows: [R({ detail: '12345' })], total: 1 }), {}],
  // NO `null` CASE, and that is deliberate. Live's `try` wraps only the parse,
  // so `null.changes` throws, the exception escapes into `render`, and `load`'s
  // `.catch(function(){})` swallows it — the whole table stays blank. The port
  // does NOT reproduce that (see the note in `detailCell`), so the two
  // legitimately differ and a case here would pin a crash as correct. Reported
  // as ToDo #21; when it is fixed upstream, add the case back.

  'a change with markup in it': [P({ rows: [R({ detail: JSON.stringify({ changes: [
    { field: '<b>f</b>', from: '<i>a</i>', to: 'b' }] }) })], total: 1 }), {}],
  // Row fields.
  'no actor': [P({ rows: [R({ actor_name: '' })], total: 1 }), {}],
  'no target': [P({ rows: [R({ target_name: '', target_id: '' })], total: 1 }), {}],
  // `target_name || target_id`: a target the app knows by name shows the name,
  // and one it only has an id for still shows something.
  'no target NAME falls back to the id': [P({ rows: [R({ target_name: '' })], total: 1 }), {}],
  // The `system` actor is rendered differently from a person.
  'the system actor': [P({ rows: [R({ actor_name: 'system' })], total: 1 }), {}],
  'no ip': [P({ rows: [R({ actor_ip: '' })], total: 1 }), {}],
  'no timestamp': [P({ rows: [R({ ts: 0 })], total: 1 }), {}],
  'markup in an actor': [P({ rows: [R({ actor_name: '<img src=x>' })], total: 1 }), {}],
  // THE QUERY STRING — a filter that stops being sent returns more rows, which
  // looks like data rather than a fault.
  'filter by actor': [P({ rows: [R({})], total: 1 }), { filters: { auActor: 'kim' } }],
  'filter by action': [P({ rows: [R({})], total: 1 }), { filters: { auAction: 'backup.run' } }],
  'filter by outcome': [P({ rows: [R({})], total: 1 }), { filters: { auOutcome: 'denied' } }],
  'a search term': [P({ rows: [R({})], total: 1 }), { filters: { auSearch: 'router1' } }],
  'a search term with spaces is trimmed': [P({ rows: [R({})], total: 1 }), { filters: { auSearch: '  router1  ' } }],
  'every filter at once': [P({ rows: [R({})], total: 1 }), { filters: {
    auActor: 'kim', auAction: 'backup.run', auOutcome: 'ok', auSearch: 'x' } }],
  'no filters sends none': [P({ rows: [R({})], total: 1 }), {}],
  // Paging and the summary.
  'a large total pages': [P({ rows: [R({})], total: 250 }), {}],
  'facets fill the selects': [P({ rows: [], total: 0,
    facets: { actors: ['a', 'b', 'c'], actions: ['x.y'] } }), {}],
  'no facets': [P({ rows: [], total: 0, facets: undefined }), {}],
  'a not-ok response renders nothing': [P({ ok: false, rows: [R({})], total: 1 }), {}],
};

(async () => {
  for (const TZ of ZONES) {
    const tag = TZ ? ' [tz=UTC]' : ' [tz=local]';
    for (const [name, [payload, opts]] of Object.entries(CASES)) {
      let a, b;
      try { a = await G.live(name + tag, () => liveRun(payload, opts, TZ)); }
      catch (e) { shout('LIVE THREW on %s%s: %s', name, tag, e.message); bad++; checked++; continue; }
      try { b = await portRun(payload, opts, TZ); }
      catch (e) { shout('PORT THREW on %s%s: %s', name, tag, e.message); bad++; checked++; continue; }
      cmp(name + tag, a, b);
    }
  }

  // ── believability ────────────────────────────────────────────────────────
  {
    const s = JSON.parse(await G.live('auto:5', () => liveRun(P({ rows: [R({})], total: 1 }), {}, 'UTC')));
    assert.match(s.auditTable.h, /kim/, 'the live table rendered no row');
    assert.ok(s.urls.length === 1, 'the loader did not fetch exactly once: ' + s.urls.length);
    assert.match(s.urls[0], /\/api\/audit\?/, 'the fetch went somewhere unexpected: ' + s.urls[0]);
  }
  {
    // A filter really reaches the URL. This is the half a DOM comparison cannot
    // see: a filter that stops being sent returns MORE rows, which reads as data.
    const none = JSON.parse(await G.live('auto:4', () => liveRun(P({ rows: [R({})], total: 1 }), {}, 'UTC')));
    const one = JSON.parse(await G.live('auto:3', () => liveRun(P({ rows: [R({})], total: 1 }),
      { filters: { auActor: 'kim' } }, 'UTC')));
    assert.ok(!/actor=/.test(none.urls[0]), 'an empty filter was sent anyway: ' + none.urls[0]);
    assert.match(one.urls[0], /actor=kim/, 'the actor filter never reached the URL: ' + one.urls[0]);
  }
  {
    // Malformed JSON does not throw, and is truncated.
    const s = JSON.parse(await G.live('auto:2', () => liveRun(P({ rows: [R({ detail: 'x'.repeat(200) })], total: 1 }), {}, 'UTC')));
    assert.match(s.auditTable.h, /x{120}/, 'the raw fallback did not render');
    assert.ok(!/x{130}/.test(s.auditTable.h), 'the raw fallback was not truncated at 120');
  }
  {
    // The four-change cap.
    const five = JSON.parse(await G.live('auto:1', () => liveRun(P({ rows: [R({ detail: JSON.stringify({ changes: Array.from(
      { length: 5 }, (_, i) => ({ field: 'f' + i, from: i, to: i + 1 })) }) })], total: 1 }), {}, 'UTC')));
    assert.match(five.auditTable.h, /f3/, 'the fourth change is missing');
    assert.ok(!/f4/.test(five.auditTable.h), 'a fifth change rendered past the cap');
  }

  fs.rmSync(OUT, { force: true });
  if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
  say('audit-page-check: %d cases identical', checked);
})();
