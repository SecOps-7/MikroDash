'use strict';
/**
 * The grid's persistence, DOM application and room bookkeeping, live vs ported.
 *
 * ── THE ROOMS ARE THE HALF WITH TEETH ───────────────────────────────────────
 *
 * `syncDashRooms` decides which page-gated collectors run. It dedupes by ROOM,
 * so two cards sharing one produce a single join — and a single leave. The
 * corpus therefore carries a layout with TWO CARDS IN ONE ROOM in every
 * combination of visibility, because that is where a per-card implementation
 * and a per-room one diverge: hide one of the pair and a per-card version leaves
 * a room the other card still needs, silently stopping its data.
 *
 * Both sides are compared on the ORDERED list of events dispatched, not on a
 * set: a duplicate join is a real difference even though the rooms match.
 *
 * ── AND EVERY WAY THE CACHE CAN BE UNUSABLE IS A CASE ───────────────────────
 *
 * `loadLayout` swallows everything. Corrupt JSON, a blob that is not an array,
 * an EMPTY array, a `localStorage` that throws on read (private mode) — all of
 * them must reach the defaults. The empty array is the one worth stating: a
 * layout with no cards is a valid-looking object, and honouring it renders an
 * empty dashboard with no way back except clearing site data.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/grid-store-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

// The gate's OWN output channel. `portIn` replaces globalThis.console.log so the
// port's save-success line can be compared, and an earlier version never put it
// back — the gate's own result line went into a captured array and the tool
// printed absolutely nothing while exiting 0.
const say = console.log.bind(console);
const shout = console.error.bind(console);

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/grid-store-check.js --freeze
const G = LIFT.golden('grid-store-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'js', 'dashboard-grid.js'));

function grab(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
const fn = (name) => grab('function ' + name + '(', '\n  }', name);

const ENTRY = path.join(ROOT, 'testdata', '.gridstore-entry.ts');
fs.writeFileSync(ENTRY,
  "export * from '../web/src/pages/dashboard-grid-store.js';\n" +
  "export { DEFAULT_LAYOUT, LS_KEY, CARD_ROOMS } from '../web/src/gen/grid-tables.js';\n");
const OUT = path.join(ROOT, 'testdata', '.gridstore-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── the shared fake world ──────────────────────────────────────────────────
function makeWorld(storage) {
  const dispatched = [];
  const nodes = new Map();
  const fetches = [];
  const logs = [];
  const doc = {
    getElementById: (id) => nodes.get(id) || null,
    dispatchEvent: (e) => { dispatched.push(e.type + ':' + e.detail); return true; },
  };
  const ls = {
    getItem: (k) => {
      if (storage.throwOnRead) throw new Error('private mode');
      return Object.prototype.hasOwnProperty.call(storage, k) ? storage[k] : null;
    },
    setItem: (k, v) => { storage[k] = v; },
  };
  return { dispatched, nodes, fetches, logs, doc, ls, storage };
}
function addNode(w, id) {
  const n = { id, style: {} };
  w.nodes.set(id, n);
  return n;
}
function nodeState(w) {
  const out = {};
  for (const [id, n] of w.nodes) {
    out[id] = { display: n.style.display, gridColumn: n.style.gridColumn, gridRow: n.style.gridRow };
  }
  return out;
}

function liveCtx(w) {
  const ctx = {
    Math, Object, JSON, Array, Error, console: { warn: (...a) => w.logs.push('warn:' + a.join(' ')), log: (...a) => w.logs.push('log:' + a.join(' ')) },
    document: w.doc, localStorage: w.ls,
    CustomEvent: function (type, init) { return { type, detail: init && init.detail }; },
    fetch: (url, opts) => {
      w.fetches.push({ url, method: opts && opts.method, body: opts && opts.body, credentials: opts && opts.credentials, headers: opts && opts.headers });
      return Promise.resolve(w.response || { ok: true, status: 200 });
    },
    layout: [],
  };
  vm.createContext(ctx);
  vm.runInContext([
    grab('var COLS = 24', ';', 'constants'), grab('var MIN_W', ';', 'MIN_W'),
    grab('var CARD_LABELS = {', '};', 'CARD_LABELS'),
    grab('var CARD_ROOMS = {', '};', 'CARD_ROOMS'),
    grab('var DEFAULT_LAYOUT = [', '];', 'DEFAULT_LAYOUT'),
    grab("var LS_KEY = '", ';', 'LS_KEY'),
    fn('cloneLayout'), fn('mergeLayout'), fn('loadLayout'), fn('saveLayout'),
    fn('applyLayout'), fn('_notifyRoom'), fn('syncDashRooms'),
  ].join('\n'), ctx);
  return ctx;
}
function portIn(w) {
  globalThis.document = w.doc;
  globalThis.localStorage = w.ls;
  globalThis.CustomEvent = function (type, init) { return { type, detail: init && init.detail }; };
  globalThis.fetch = (url, opts) => {
    w.fetches.push({ url, method: opts && opts.method, body: opts && opts.body, credentials: opts && opts.credentials, headers: opts && opts.headers });
    return Promise.resolve(w.response || { ok: true, status: 200 });
  };
  globalThis.console.warn = (...a) => w.logs.push('warn:' + a.join(' '));
  // Captured, not printed: the save's success line is behaviour worth comparing
  // and noise worth keeping out of the gate's own output.
  globalThis.console.log = (...a) => w.logs.push('log:' + a.join(' '));
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  return { m };
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (JSON.stringify(a) === JSON.stringify(b)) return;
  bad++;
  if (bad <= 5) shout('DIFF %s\n  live: %j\n  port: %j', what, a, b);
}

async function run() {
  const port = (() => { const w = makeWorld({}); return portIn(w).m; })();
  const KEY = port.LS_KEY;

  // ── loadLayout: every way the cache can be unusable ────────────────────────
  {
    const stores = {
      'nothing stored': {},
      'valid, one card': { [KEY]: JSON.stringify({ cards: [{ id: 'card-system', x: 2, y: 2, w: 6, h: 6, visible: true }] }) },
      'valid, several': { [KEY]: JSON.stringify({ cards: port.DEFAULT_LAYOUT.map((c) => ({ ...c, x: 1, y: 1 })) }) },
      'an EMPTY cards array': { [KEY]: JSON.stringify({ cards: [] }) },
      'cards is not an array': { [KEY]: JSON.stringify({ cards: { id: 'x' } }) },
      'no cards key at all': { [KEY]: JSON.stringify({ layout: [] }) },
      'null': { [KEY]: 'null' },
      'a bare array, not wrapped': { [KEY]: JSON.stringify([{ id: 'card-system' }]) },
      'corrupt JSON': { [KEY]: '{not json' },
      'an empty string': { [KEY]: '' },
      'a number': { [KEY]: '42' },
      'a card that no longer exists': { [KEY]: JSON.stringify({ cards: [{ id: 'card-gone', x: 1, y: 1, w: 2, h: 2, visible: true }] }) },
      'localStorage throws on read': { throwOnRead: true },
    };
    for (const [name, store] of Object.entries(stores)) {
      const lw = makeWorld({ ...store }), pw = makeWorld({ ...store });
      const L = liveCtx(lw);
      const P = portIn(pw).m;
      cmp('loadLayout(' + name + ')', G.live('loadLayout(' + name + ')', () => L.loadLayout()), P.loadLayout());
    }
  }

  // ── saveLayout: local first, then the POST ─────────────────────────────────
  {
    for (const [name, lay] of Object.entries({
      'the default': port.DEFAULT_LAYOUT,
      'empty': [],
      'one card': [{ id: 'card-system', x: 3, y: 3, w: 4, h: 4, visible: true }],
    })) {
      const pw = makeWorld({});
      const P = portIn(pw).m;
      // ONE FROZEN VALUE FOR THE WHOLE LIVE HALF. The drive (`L.saveLayout()`)
      // and its three readings are separated by an await — `logs` only fills on
      // the next microtask — so the readings cannot be frozen one at a time.
      const live = await G.live('saveLayout:' + name, async () => {
        const lw = makeWorld({});
        const L = liveCtx(lw); L.layout = lay;
        L.saveLayout();
        const storage = lw.storage, fetches = lw.fetches;
        await new Promise((r) => setImmediate(r));
        return { storage, fetches, logs: lw.logs };
      });
      P.saveLayout(lay);
      cmp('saveLayout(' + name + ') localStorage', live.storage, pw.storage);
      cmp('saveLayout(' + name + ') request', live.fetches, pw.fetches);
      await new Promise((r) => setImmediate(r));
      cmp('saveLayout(' + name + ') logs', live.logs, pw.logs);
    }
  }

  // A FAILED save. The live handler warns with the status and the port must too —
  // the only observable difference between handling the response and ignoring it,
  // which is why a mutation dropping the warn survived until this case existed.
  {
    for (const status of [400, 403, 500]) {
      const pw = makeWorld({});
      pw.response = { ok: false, status };
      const P = portIn(pw).m;
      const liveLogs = await G.live('saveLayout(HTTP ' + status + ') logs', async () => {
        const lw = makeWorld({});
        lw.response = { ok: false, status };
        const L = liveCtx(lw); L.layout = port.DEFAULT_LAYOUT;
        L.saveLayout();
        await new Promise((r) => setImmediate(r));
        return lw.logs;
      });
      P.saveLayout(port.DEFAULT_LAYOUT);
      await new Promise((r) => setImmediate(r));
      cmp('saveLayout(HTTP ' + status + ') logs', liveLogs, pw.logs);
    }
  }

  // ── applyLayout ────────────────────────────────────────────────────────────
  {
    const cases = {
      'all visible': port.DEFAULT_LAYOUT.filter((c) => c.visible),
      'a hidden card': [{ id: 'card-system', x: 1, y: 1, w: 2, h: 2, visible: false }],
      'mixed': [
        { id: 'card-system', x: 9, y: 6, w: 8, h: 4, visible: true },
        { id: 'card-traffic', x: 1, y: 1, w: 20, h: 5, visible: false },
      ],
      'a card with no element': [{ id: 'card-not-in-the-page', x: 1, y: 1, w: 2, h: 2, visible: true }],
      'a 1x1 at the far corner': [{ id: 'card-system', x: 24, y: 22, w: 1, h: 1, visible: true }],
    };
    for (const [name, lay] of Object.entries(cases)) {
      const pw = makeWorld({});
      for (const c of lay) if (c.id !== 'card-not-in-the-page') addNode(pw, c.id);
      const P = portIn(pw).m;
      const live = G.live('applyLayout(' + name + ')', () => {
        const lw = makeWorld({});
        for (const c of lay) if (c.id !== 'card-not-in-the-page') addNode(lw, c.id);
        liveCtx(lw).applyLayout(lay);
        return nodeState(lw);
      });
      P.applyLayout(lay);
      cmp('applyLayout(' + name + ')', live, nodeState(pw));
    }
  }

  // ── syncDashRooms: the half with teeth ─────────────────────────────────────
  {
    const roomed = Object.keys(port.CARD_ROOMS);
    assert.ok(roomed.length >= 4, 'only ' + roomed.length + ' room-gated cards');
    // Find two cards that SHARE a room, or synthesise the pair — the live table's
    // own comment says two cards may share one, and that is the case a per-card
    // implementation gets wrong.
    const byRoom = {};
    for (const id of roomed) (byRoom[port.CARD_ROOMS[id]] ||= []).push(id);
    const shared = Object.values(byRoom).find((ids) => ids.length > 1);
    const pair = shared || [roomed[0], roomed[1]];
    const sameRoom = !!shared;

    const V = (id, visible) => ({ id, x: 1, y: 1, w: 2, h: 2, visible });
    const cases = {
      'nothing visible': roomed.map((id) => V(id, false)),
      'everything visible': roomed.map((id) => V(id, true)),
      'one roomed card': [V(roomed[0], true)],
      'a card with no room': [{ id: 'card-system', x: 1, y: 1, w: 2, h: 2, visible: true }],
      'a hidden roomed card among visible plain ones': [
        V(roomed[0], false), { id: 'card-system', x: 1, y: 1, w: 2, h: 2, visible: true },
      ],
      'the same card twice': [V(roomed[0], true), V(roomed[0], true)],
      'both of a pair visible': [V(pair[0], true), V(pair[1], true)],
      'first of a pair hidden': [V(pair[0], false), V(pair[1], true)],
      'second of a pair hidden': [V(pair[0], true), V(pair[1], false)],
      'empty layout': [],
    };
    for (const [name, lay] of Object.entries(cases)) {
      for (const focused of [true, false]) {
        const pw = makeWorld({});
        const P = portIn(pw).m;
        const key = 'syncDashRooms(' + name + ',focused=' + focused + ')';
        const live = G.live(key, () => {
          const lw = makeWorld({});
          const L = liveCtx(lw); L.layout = lay;
          L.syncDashRooms(focused);
          return lw.dispatched;
        });
        P.syncDashRooms(lay, focused, P.CARD_ROOMS);
        cmp(key, live, pw.dispatched);
      }
    }

    // ── A TABLE WHERE TWO CARDS DO SHARE A ROOM ──────────────────────────────
    //
    // The shipped table has no such pair today, so dedupe-by-room and
    // dedupe-by-card agree on every real input and a mutation between them
    // SURVIVED. The live table's comment says two cards may share a room, so the
    // property must keep working. Both sides are handed the same synthetic table:
    // the live one through its scope, the port through its parameter.
    {
      const SYN = { 'card-a': 'firewall', 'card-b': 'firewall', 'card-c': 'logs' };
      const synCases = {
        'both sharers visible': [V('card-a', true), V('card-b', true), V('card-c', true)],
        'first sharer hidden': [V('card-a', false), V('card-b', true)],
        'second sharer hidden': [V('card-a', true), V('card-b', false)],
        'both sharers hidden': [V('card-a', false), V('card-b', false), V('card-c', true)],
        'reversed order': [V('card-b', true), V('card-a', true)],
        'three in one room': [V('card-a', true), V('card-b', true), V('card-d', true)],
      };
      for (const [name, lay] of Object.entries(synCases)) {
        for (const focused of [true, false]) {
          const pw = makeWorld({});
          const P = portIn(pw).m;
          const key = 'syncDashRooms[shared room](' + name + ',focused=' + focused + ')';
          const live = G.live(key, () => {
            const lw = makeWorld({});
            const L = liveCtx(lw);
            L.CARD_ROOMS = SYN; L.layout = lay;
            L.syncDashRooms(focused);
            return lw.dispatched;
          });
          P.syncDashRooms(lay, focused, SYN);
          cmp(key, live, pw.dispatched);
        }
      }
      // And the property itself, stated directly: two visible cards in one room
      // produce ONE event.
      // THE PROPERTY STATED DIRECTLY, ON THE PORT. It was asserted against the
      // LIVE side, which is the half that stops existing — and the live side is
      // not what needs to keep deduping. Restated against the port, it survives
      // the reference going away AND checks the implementation that ships.
      {
        const w = makeWorld({});
        const P = portIn(w).m;
        P.syncDashRooms([V('card-a', true), V('card-b', true)], true, SYN);
        assert.deepEqual(w.dispatched, ['dashcard:room:focus:firewall'],
          'two cards in one room did not dedupe to a single event: ' + JSON.stringify(w.dispatched));
      }
    }
    // Believability: the shared-room case must actually produce ONE event, or
    // the case above is not testing what its name claims.
    if (sameRoom) {
      const w = makeWorld({});
      const L = liveCtx(w); L.layout = [V(pair[0], true), V(pair[1], true)];
      L.syncDashRooms(true);
      assert.equal(w.dispatched.length, 1,
        'two cards in one room produced ' + w.dispatched.length + ' events — dedupe is not by room');
    } else {
      say('  note: no two cards currently share a room; the pair cases use two DIFFERENT rooms');
    }
  }

}

run().then(() => {
  fs.rmSync(OUT, { force: true });
  if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
  say('grid-store-check: %d comparisons identical', checked);
});
