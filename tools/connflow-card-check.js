'use strict';
/**
 * The Connection Flow card, live against ported.
 *
 * ── THIS ONE IS A WRAPPER, SO THE GATE CHECKS THE WRAPPING ──────────────────
 *
 * The diagram itself is `connections-sankey.ts`, already ported and gated
 * elsewhere. What is new here is which elements it draws into, how much height
 * it is given, and how the payload is sliced — so the renderer is REPLACED by a
 * recorder on both sides and the ARGUMENTS are compared. Comparing the drawn SVG
 * would mostly re-test the renderer and would hide a wrapper that passed the
 * right things in the wrong order.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/connflow-card-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('connflow-card-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function grab(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  return src.slice(i, src.indexOf(close, i) + close.length);
}
const dcSrc = grab('function renderDc(', '\n  }', 'renderDc');
if (LIFT.hasReference(ROOT)) assert.ok(dcSrc.includes('dc-sankeySvg'), 'the renderDc slice lost its element');
if (LIFT.hasReference(ROOT)) assert.ok(dcSrc.includes('clientHeight'), 'the renderDc slice lost its height measurement');
// The slicing lives in the shared handler over there.
const handlerAt = src.indexOf("socket.on('conn:update'", src.indexOf('function renderDc('));
const slices = src.slice(handlerAt, handlerAt + 300);
const mSrc = /topSources\|\|\[\]\)\.slice\(0,\s*(\d+)\)/.exec(slices);
const mDst = /topDestinations\|\|\[\]\)\.slice\(0,\s*(\d+)\)/.exec(slices);
if (LIFT.hasReference(ROOT)) assert.ok(mSrc && mDst, 'cannot find the slice limits in the live handler');
// FROZEN, NOT GUARDED. These are VALUES lifted from the live source and the port
// comparison consumes them — guarding would leave them undefined and make the
// slice assertions vacuous. The non-emptiness check below validates the
// recording, the way `MAX > 0` does in notif-bell (LOOP.md 3o).
const LIMITS = G.value('the live slice limits',
  () => ({ sources: Number(mSrc[1]), dests: Number(mDst[1]) }));
assert.ok(LIMITS.sources > 0 && LIMITS.dests > 0,
  'the recorded slice limits are not positive numbers: ' + JSON.stringify(LIMITS));
say('  live slice limits: %d sources, %d destinations', LIMITS.sources, LIMITS.dests);

const ENTRY = path.join(ROOT, 'testdata', '.connflow-entry.ts');
fs.writeFileSync(ENTRY, "export { renderConnFlowCard } from '../web/src/pages/dashboard-card-connflow.js';\n");
const OUT = path.join(ROOT, 'testdata', '.connflow-port.cjs');
// The renderer is stubbed by REPLACING the module esbuild would bundle, so what
// is compared is the wrapper's call and not the diagram.
// Both the stub and the copy live BESIDE the original so its other relative
// imports (`../dom`) still resolve. Removed immediately after the bundle.
const PAGES = path.join(ROOT, 'web', 'src', 'pages');
const STUB = path.join(PAGES, '.connflow-stub.ts');
fs.writeFileSync(STUB,
  "export interface SankeySource { count: number }\n" +
  "export interface SankeyDest { count: number }\n" +
  "export const calls: unknown[] = [];\n" +
  "export function renderSankey(svg: unknown, empty: unknown, s: unknown, d: unknown, h?: number) {\n" +
  "  calls.push({ svg: (svg as { id?: string }).id, empty: (empty as { id?: string }).id, s, d, h });\n" +
  "}\n");
// The substitution is a text rewrite of the module's own import, bundled from a
// COPY. esbuild's `--alias:` rejects a relative specifier and its plugin API is
// async-only, and neither is worth restructuring this tool around: what is
// wanted is one import redirected, which is exactly what this does — and it
// asserts the rewrite happened, so a renamed import cannot silently bundle the
// real renderer and make this gate compare the diagram instead of the wrapper.
const COPY = path.join(PAGES, '.connflow-copy.ts');
{
  const original = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'dashboard-card-connflow.ts'), 'utf8');
  const rewritten = original.replace(/from '\.\/connections-sankey'/, "from './.connflow-stub'");
  assert.notEqual(rewritten, original,
    'the sankey import was not rewritten — this gate would compare the real diagram rather than ' +
    'the wrapper that is under test');
  fs.writeFileSync(COPY, rewritten);
}
fs.writeFileSync(ENTRY, "export { renderConnFlowCard } from '../web/src/pages/.connflow-copy.js';\n" +
  "export { calls } from '../web/src/pages/.connflow-stub.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
fs.rmSync(COPY, { force: true });

function makeDom(withEmpty, parentH) {
  const byId = new Map();
  const mk = (id) => ({ id, style: {}, set innerHTML(v) { this._h = v; }, get innerHTML() { return this._h || ''; } });
  const svg = mk('dc-sankeySvg');
  svg.parentElement = parentH === null ? null : { clientHeight: parentH };
  byId.set('dc-sankeySvg', svg);
  if (withEmpty) byId.set('dc-sankeyEmpty', mk('dc-sankeyEmpty'));
  return byId;
}
function liveRun(srcs, dsts, withEmpty = true, parentH = 300) {
  const byId = makeDom(withEmpty, parentH);
  const calls = [];
  const ctx = {
    document: { getElementById: (id) => byId.get(id) || null },
    render: (s, d, svg, empty, h) => calls.push({ svg: svg && svg.id, empty: empty && empty.id, s, d, h }),
  };
  vm.createContext(ctx);
  vm.runInContext(dcSrc, ctx);
  // The live wrapper takes ALREADY-SLICED arrays; the handler slices first.
  ctx.renderDc((srcs || []).slice(0, LIMITS.sources), (dsts || []).slice(0, LIMITS.dests));
  return JSON.stringify(calls);
}
function portRun(srcs, dsts, withEmpty = true, parentH = 300) {
  const byId = makeDom(withEmpty, parentH);
  globalThis.document = { getElementById: (id) => byId.get(id) || null };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.renderConnFlowCard(srcs, dsts);
  // The stub's `calls` is re-exported through the bundle.
  const stub = require(OUT);
  return JSON.stringify(stub.calls || []);
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) shout('DIFF %s\n  live: %s\n  port: %s', what, a.slice(0, 300), b.slice(0, 300));
}

const S = (n) => Array.from({ length: n }, (_, i) => ({ ip: 'a' + i, count: n - i }));
const D = (n) => Array.from({ length: n }, (_, i) => ({ key: 'd' + i, count: n - i }));

const CASES = [
  ['a small payload', S(3), D(4)],
  ['exactly at the limits', S(8), D(10)],
  ['over the limits — sliced', S(20), D(30)],
  ['one under', S(7), D(9)],
  ['empty arrays', [], []],
  ['undefined payloads', undefined, undefined],
  ['sources only', S(5), []],
  ['destinations only', [], D(5)],
];
for (const [name, s, d] of CASES) cmp(name, G.live(name, () => liveRun(s, d)), portRun(s, d));

// The height, which is the wrapper's own contribution.
for (const h of [0, 1, 300, 1000]) {
  cmp('parent height ' + h, G.live('parent height ' + h, () => liveRun(S(2), D(2), true, h)),
    portRun(S(2), D(2), true, h));
}
cmp('no parent element', G.live('no parent element', () => liveRun(S(2), D(2), true, null)),
  portRun(S(2), D(2), true, null));

// ── believability ──────────────────────────────────────────────────────────
// RE-AIMED AT THE PORT: that the wrapper calls the renderer, draws into the right
// svg, slices to the limits and passes the height are all properties the PORT has
// to keep.
{
  const parsed = JSON.parse(portRun(S(20), D(30)));
  assert.equal(parsed.length, 1, 'the wrapper did not call the renderer');
  assert.equal(parsed[0].svg, 'dc-sankeySvg', 'the wrapper drew into ' + parsed[0].svg);
  assert.equal(parsed[0].s.length, LIMITS.sources, 'sources were not sliced to ' + LIMITS.sources);
  assert.equal(parsed[0].d.length, LIMITS.dests, 'destinations were not sliced to ' + LIMITS.dests);
  assert.equal(parsed[0].h, 300, 'the wrapper passed height ' + parsed[0].h);
}
{
  // A MISSING SVG MUST DRAW NOTHING AT ALL — asked of the PORT.
  //
  // It ran the LIFTED live renderer in a vm with a document that returns null for
  // every id, and relied on the renderer throwing if it drew. Once the reference
  // is gone there is nothing to run, and the property is one the PORT must keep
  // anyway. Driven through `portRun` with no svg in the DOM, the check is that it
  // records NO calls.
  globalThis.document = { getElementById: () => null };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.renderConnFlowCard([], []);
  const calls = require(OUT).calls || [];
  assert.deepEqual(calls, [], 'the card drew without an svg: ' + JSON.stringify(calls));
}

fs.rmSync(OUT, { force: true });
fs.rmSync(STUB, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('connflow-card-check: %d cases identical', checked);
