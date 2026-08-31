'use strict';
/**
 * `collection:config` — the payload the live server sends the browser, generated
 * by RUNNING the live `_collectionPayload`.
 *
 * ---- WHY THE PORT NEEDS THIS ----------------------------------------------
 *
 * The port never emits this event, and its client-side consumer
 * (`applyCollectionConfig` in `web/src/stale.ts`) is complete, gated by
 * `tools/stale-check.js`, and called by nothing. Found 2026-08-28 by
 * `tools/live-socket-diff.js`; see `tools/orphaned-consumer-audit.js`, which now
 * watches that class.
 *
 * The visible consequence: a collector an operator disabled on a router shows a
 * STALE dashboard card rather than `is-collector-off` — broken instead of off.
 *
 * ---- WHAT IS AND IS NOT PINNED HERE ---------------------------------------
 *
 * `resolveCollection` is already pinned, thoroughly, by `tools/collection-cases.js`.
 * This corpus is only the ten-line WRAPPER around it: the key set, and the
 * derivation of `off`. Feeding it a resolution taken from the live
 * `resolveCollection` rather than a hand-written object keeps the two honest
 * about each other without duplicating that matrix.
 *
 * ---- `off` IS ORDERED, AND THE ORDER IS NOT SORTED ------------------------
 *
 *   off: Object.keys(eff.enabled).filter(k => !eff.enabled[k])
 *
 * `Object.keys` is insertion order, and `eff.enabled` is built by walking the
 * COLLECTOR REGISTRY. So `off` comes out in registry order, which is neither
 * alphabetical nor the order the operator turned things off in. The port has the
 * same ordered registry (`collection.Collectors()`), so it can reproduce this
 * exactly rather than sorting and documenting a divergence the way
 * `writeCapablePages` had to.
 *
 * The order is recorded here so that choice is checkable.
 *
 * ---- A NOTE ON THE LIVE COMMENT, WHICH IS WRONG ---------------------------
 *
 * `off` is commented "Keys the user cannot turn off, so the UI never offers
 * them." It is the opposite: the keys that ARE off. Filed rather than fixed —
 * nothing reads `off` in `public/app.js`, so the comment misdescribes a field no
 * consumer uses, which is why it could stay wrong.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/collection-payload-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/collection-payload-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'collection-payload-cases.json');

const { resolveCollection } = require(path.join(LIVE, 'src', 'collection.js'));

// ---- Slice _collectionPayload out of index.js ------------------------------
// Anchored on CONTENT: index.js is ~7,200 lines and everything above this moves.
const src = fs.readFileSync(path.join(LIVE, 'src', 'index.js'), 'utf8');
const lines = src.split('\n');
const start = lines.findIndex((l) => l.startsWith('function _collectionPayload('));
if (start < 0) throw new Error('anchor lost: function _collectionPayload');
const end = lines.findIndex((l, i) => i > start && l === '}');
if (end < 0) throw new Error('anchor lost: the closing brace of _collectionPayload');
const slice = lines.slice(start, end + 1).join('\n');

// A wrapper that stopped deriving `off` would still evaluate and would agree
// with a port that never derived it either.
if (!slice.includes('Object.keys(eff.enabled)')) {
  throw new Error('the slice does not derive `off` from eff.enabled — the anchors drifted');
}

// The FALLBACK ARM is deliberately not exercised: it calls Settings.load() and
// Routers.getById(), which read the operator's real /data. Every case passes a
// session carrying `.collection`, which is also the live hot path — the fallback
// exists for a socket that arrives before a session does.
const ctx = Object.create(null);
const collectionPayload = vm.runInNewContext(
  `${slice}\n_collectionPayload;`, ctx, { filename: 'index.js#_collectionPayload' });

/** Resolve one router config the way the server does, then wrap it. */
function run(settings, router) {
  const eff = resolveCollection(settings, router);
  return { eff, payload: collectionPayload('r-under-test', { collection: eff }) };
}

const CASES = [
  ['a router with nothing overridden — no collector is off', {}, {}],
  // THE CASE THE PORT EXISTS TO SERVE: an operator turned two collectors off.
  ['two collectors turned off on this router', {}, { collection: { off: ['wifi', 'logs'] } }],
  // The CASCADE, which is what makes `off` more than an echo of the input:
  // bandwidth requires conns, so turning off conns turns off bandwidth too and
  // `off` carries a key the operator never named.
  ['a cascade — conns off also disables bandwidth', {}, { collection: { off: ['conns'] } }],
  // A GLOBAL kill switch, applied after the per-router list.
  ['pingEnabled false globally', { pingEnabled: false }, {}],
  ['poll mode changes delivery and no interval', {}, { collection: { mode: 'poll' } }],
];

const out = CASES.map(([why, settings, router]) => {
  const { eff, payload } = run(settings, router);
  return { why, settings, router, payload, enabledOrder: Object.keys(eff.enabled) };
});

// ---- Believability ---------------------------------------------------------
const byWhy = Object.fromEntries(out.map((c) => [c.why, c]));
const need = (k) => {
  if (!byWhy[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return byWhy[k];
};

// THE KEY SET, on every case. A payload that lost a key would be a page reading
// undefined.
const KEYS = ['routerId', 'mode', 'enabled', 'stream', 'poll', 'off'].sort();
for (const c of out) {
  const got = Object.keys(c.payload).sort();
  if (JSON.stringify(got) !== JSON.stringify(KEYS)) {
    throw new Error(`${c.why}: payload keys are ${got.join(',')}, expected ${KEYS.join(',')}`);
  }
  if (c.payload.routerId !== 'r-under-test') {
    throw new Error(`${c.why}: routerId is ${JSON.stringify(c.payload.routerId)}`);
  }
}

// `off` MUST DISCRIMINATE. A corpus where every case has an empty `off` agrees
// with a port that always sends `[]`.
const none = need('a router with nothing overridden — no collector is off');
if (none.payload.off.length !== 0) {
  throw new Error(`an unconfigured router reports ${none.payload.off.length} collectors off`);
}
const two = need('two collectors turned off on this router');
for (const k of ['wifi', 'logs']) {
  if (!two.payload.off.includes(k)) throw new Error(`${k} was turned off and is not in off`);
}
// THE CASCADE reaches `off`, which is the property that makes it worth sending
// at all — the browser cannot derive it from the operator's own list.
const casc = need('a cascade — conns off also disables bandwidth');
if (!casc.payload.off.includes('bandwidth')) {
  throw new Error('bandwidth depends on conns and did not cascade into off — either the '
    + 'dependency edge is gone or this corpus stopped exercising it');
}
if (casc.payload.enabled.bandwidth !== false) {
  throw new Error('off says bandwidth is disabled and enabled disagrees');
}

// THE ORDER IS REGISTRY ORDER, NOT SORTED. Asserted rather than assumed: if
// `off` ever came out sorted the port could sort too, and this note would be a
// lie that costs a day.
for (const c of out) {
  const expected = c.enabledOrder.filter((k) => !c.payload.enabled[k]);
  if (JSON.stringify(c.payload.off) !== JSON.stringify(expected)) {
    throw new Error(`${c.why}: off is not Object.keys(enabled) order`);
  }
}
const cascOff = casc.payload.off;
if (JSON.stringify(cascOff) === JSON.stringify([...cascOff].sort()) && cascOff.length > 1) {
  console.warn('note: every `off` list in this corpus happens to be alphabetical, so it does '
    + 'NOT discriminate registry order from sorted order. The Go side must still walk the '
    + 'registry — see the header.');
}

const json = JSON.stringify(
  { generated_from: 'src/index.js _collectionPayload + src/collection.js resolveCollection',
    cases: out }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/collection-payload-cases.json - re-run tools/collection-payload-cases.js');
    process.exit(1);
  }
  console.log(`collection-payload-cases: up to date (${out.length} cases)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${out.length} cases)`);
}
