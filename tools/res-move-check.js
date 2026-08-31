'use strict';
/**
 * The reorder arrows, live against ported.
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * `firewall.ts` and `capsman.ts` both render `data-res-move` arrows, enabled
 * whenever the viewer may write, with comments saying the ENGINE owns the
 * behaviour — and the engine never read the attribute. Both pages drew
 * working-looking arrows that did nothing, on one of which the ORDER of a rule
 * decides what the rule does.
 *
 * ── SENDING IT IS THE EASY HALF ─────────────────────────────────────────────
 *
 * What the branch has to get right is when NOT to send, and what must happen
 * alongside:
 *
 *   a disabled arrow     the first and last rows carry one, and clicking it must
 *                        send nothing rather than asking the router to move a
 *                        rule off the end.
 *   no schema, or a      the page disables a viewer's arrows already; this is
 *   read-only one        the second check, against what the SERVER said, so a
 *                        stale render cannot send a write.
 *   a row-level data-res wins over the host's — Routes holds v4 and v6 in one
 *                        table and the family picks the RouterOS menu.
 *   IT MUST NOT ALSO     an arrow sits inside `[data-id]`, so without the early
 *   OPEN THE ROW         return the same click opens the edit dialog. Both sides
 *                        signal that by emitting `res:row`, so the comparison
 *                        sees it without needing a rendered dialog.
 *
 * That last one is why this gate is worth its length: the port has TWO click
 * listeners where the live app has one, so the ordering the original relies on
 * is not automatic here.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/res-move-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/res-move-check.js --freeze
const G = L.golden('res-move-check');
const src = L.liveSource(ROOT);

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
// The move branch AND the row-open branch after it, so the fall-through the
// early return prevents is part of what runs.
const moveSrc = slice("    var mv = e.target.closest('[data-res-move]');", '\n    }', 'the move branch');
const rowSrc = slice("    var host = e.target.closest('[data-res-rows]');", "\n      }\n    }", 'the row-open branch');
const doMoveSrc = slice('  function doMove(ack) {', '\n  }', 'doMove');

const OUT = path.join(ROOT, 'testdata', '.resmove-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'resource.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

/**
 * A click target inside a table: the arrow, its row, and the host.
 *
 * `closest` is answered from a fixed chain rather than a tree, which is enough
 * for a delegated handler: every lookup either of these branches makes is a
 * `closest` for one of these three selectors.
 */
function makeTarget(spec) {
  const host = spec.noHost ? null : {
    getAttribute: (k) => (k === 'data-res-rows' ? (spec.hostRes ?? 'firewallFilter') : null),
    contains: () => !spec.hostExcludesRow,
  };
  const row = spec.noRow ? null : {
    getAttribute: (k) => (k === 'data-id' ? (spec.id ?? '*7')
      : k === 'data-res' ? (spec.rowRes ?? null)
      : k === 'data-identity' ? (spec.identity ?? null)
      : k === 'data-res-name' ? (spec.identity ?? null) : null),
  };
  const arrow = spec.noArrow ? null : {
    disabled: !!spec.disabled,
    getAttribute: (k) => (k === 'data-res-move' ? (spec.direction ?? 'up') : null),
  };
  const chain = { '[data-res-move]': arrow, '[data-id]': row, '[data-res-rows]': host };
  return { closest: (sel) => (sel in chain ? chain[sel] : null), getAttribute: () => null };
}

function liveRun(spec, schema) {
  const sent = [];
  const ctx = {
    Object, JSON, String, Boolean,
    _schema: schema,
    _move: null,
    _retry: null,
    _cur: null,
    socket: { emit: (ev, payload) => sent.push({ ev, payload }) },
    openResource() {},
    el: () => null,
  };
  vm.createContext(ctx);
  vm.runInContext(doMoveSrc + '\nfunction __click(e){\n' + moveSrc + '\n' + rowSrc + '\n}', ctx);
  ctx.__click({ target: makeTarget(spec) });
  return JSON.stringify(sent, null, 1);
}

const settle = () => new Promise((r) => setImmediate(r));

async function portRun(spec, schema) {
  const sent = [];
  const handlers = [];
  const saved = { document: global.document, window: global.window };
  global.document = {
    addEventListener: (n, f) => { if (n === 'click') handlers.push(f); },
    getElementById: () => null,
    querySelectorAll: () => [],
    body: { classList: { add() {}, remove() {}, contains: () => false } },
  };
  global.window = { confirm: () => true };
  const socketHandlers = new Map();
  const socket = {
    emit: (ev, payload) => sent.push({ ev, payload }),
    on: (ev, fn) => { socketHandlers.set(ev, fn); },
    isOpen: () => true,
  };
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.mountRows(socket);
    // Seed the schema map through the REAL path — the `res:schema` message —
    // rather than a test-only setter. That is also what proves the key the
    // branch looks up is the key the server's schema arrives under.
    const onSchema = socketHandlers.get('res:schema');
    if (!onSchema) throw new Error('mountRows did not subscribe to res:schema');
    for (const [key, v] of Object.entries(schema)) onSchema({ key, ...v });
    sent.length = 0; // drop anything the mount itself sent
    const t = makeTarget(spec);
    for (const h of handlers) h({ target: t });
    // `openResource` resolves through a promise before it emits `res:row`, so
    // reading the log synchronously saw an empty list and made a working
    // fall-through look like a missing one. The live branch emits inline.
    await settle();
    await settle();
  } finally {
    if (saved.document === undefined) delete global.document; else global.document = saved.document;
    if (saved.window === undefined) delete global.window; else global.window = saved.window;
  }
  return JSON.stringify(sent, null, 1);
}

const bad = [];
let cases = 0;
const queued = [];
function compare(what, spec, schema) {
  queued.push(async () => {
    cases++;
    const a = G.live(G.seq(), () => liveRun(spec, schema));
    const b = await portRun(spec, schema);
    if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
  });
}

const WRITABLE = { firewallFilter: { key: 'firewallFilter', permitted: true },
                   firewallNat: { key: 'firewallNat', permitted: true } };
const READONLY = { firewallFilter: { key: 'firewallFilter', permitted: false } };

// ── The move itself ─────────────────────────────────────────────────────────
for (const direction of ['up', 'down']) {
  compare('move ' + direction, { direction }, WRITABLE);
  compare('move ' + direction + ' with an identity', { direction, identity: 'drop-invalid' }, WRITABLE);
}
compare('a direction the markup never renders', { direction: 'sideways' }, WRITABLE);
compare('an empty direction', { direction: '' }, WRITABLE);
compare('an id with a RouterOS star', { id: '*1f' }, WRITABLE);

// ── The refusals ────────────────────────────────────────────────────────────
compare('a DISABLED arrow sends nothing', { disabled: true }, WRITABLE);
compare('no schema for this resource', {}, {});
compare('a schema that says read-only', {}, READONLY);
compare('no row around the arrow', { noRow: true }, WRITABLE);
compare('no host around the arrow', { noHost: true }, WRITABLE);
compare('neither row nor host', { noRow: true, noHost: true }, WRITABLE);

// ── Which key the move goes to ──────────────────────────────────────────────
compare('a row-level data-res wins over the host', { rowRes: 'firewallNat' }, WRITABLE);
compare('a row-level data-res with no schema of its own', { rowRes: 'firewallRaw' }, WRITABLE);

// ── The fall-through ────────────────────────────────────────────────────────
//
// A click with NO arrow reaches the row-open branch, and both sides emit
// `res:row`. A click WITH an arrow must emit the move and nothing else — that
// difference is the whole reason the branch sits where it does.
compare('a click on the row itself opens it', { noArrow: true }, WRITABLE);
compare('a click on the row when the host excludes it', { noArrow: true, hostExcludesRow: true }, WRITABLE);
compare('a click on a read-only row does not open it', { noArrow: true }, READONLY);

(async () => {
  for (const run of queued) await run();
  fs.rmSync(OUT, { force: true });
  if (bad.length) {
    console.error('the reorder arrows differ from the live ones:\n\n' + bad.slice(0, 3).join('\n\n') + '\n');
    process.exit(1);
  }
  console.log('reorder arrows match the live engine (' + cases + ' cases: the move, six refusals, ' +
    'key resolution and the fall-through)');
})();
