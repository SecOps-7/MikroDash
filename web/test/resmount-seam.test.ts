// Moved from the resmount-seam check when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * A CARD THAT SWAPS WHICH RESOURCE ITS ADD SLOT NAMES.
 *
 * The Firewall card's four tables share one header, and its Add button belongs
 * to whichever tab is showing. WiFi and CAPsMAN do the same. Those pages rewrite
 * `data-res-add` and announce `mikrodash:resmount`; `resource.ts` listens and
 * re-fills every slot.
 *
 * ── WHAT THIS IS, AND WHAT IT IS NOT ────────────────────────────────────────
 *
 * A BEHAVIOUR PIN on the port, not a differential. The live listener is seven
 * lines but calls `need` and `mountAdds` from inside a large IIFE, so lifting it
 * would mean lifting most of the live resource engine; that is a bigger piece of
 * work than the listener it would check. Said plainly here rather than left for
 * a reader to assume this compares two implementations — it does not.
 *
 * What it DOES prove is that the announcement changes the buttons, which is the
 * thing that was broken: this port announced `mikrodash:resmount` from three
 * pages and listened nowhere, so the Add button on a swapped tab kept the
 * PREVIOUS tab's resource — pressing Add on the NAT table opened the filter-rule
 * form. `announcement-audit` found it; nothing drove the add-slot path at all.
 *
 *   node tools/resmount-seam-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.rm-entry.ts');
fs.writeFileSync(ENTRY, "export { mountAdds } from '../web/src/resource.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rm.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// The payload the server sends: the schema itself, keyed by `key`. Not
// `{ resource, schema }` — that is the REQUEST's shape, and using it here made
// every slot stay empty while the gate looked like it was driving them.
const SCHEMA = (key, label) => ({ key, label, permitted: true, fields: [], title: label });

/**
 * Drive one run.
 *
 * `slot` is what `data-res-add` says to begin with; `then` is what the page
 * rewrites it to before announcing. The schemas arrive as the server would send
 * them, through the real `res:schema` handler.
 */
function run(slot, then, schemas, replayAfter) {
  const doc = makeDoc(['fwAddSlot'], {
    query: { '[data-res-add]': [{ id: 'fwAddSlot', value: slot }] },
  });
  // The slot's attribute is what the code reads, and it must be REWRITABLE:
  // the whole point is a page changing it between renders.
  const host = doc.queryNodes['[data-res-add]'][0];
  let attr = slot;
  host.getAttribute = (k) => (k === 'data-res-add' ? attr : null);

  const emits = [];
  const handlers = {};
  const prev = { doc: globalThis.document, win: globalThis.window };
  globalThis.document = doc;
  globalThis.window = {};
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.mountAdds({ on: (ev, fn) => { handlers[ev] = fn; },
                    emit: (ev, p) => { emits.push({ ev, p }); } });
    for (const s of schemas) if (handlers['res:schema']) handlers['res:schema'](s);
    const before = host.innerHTML;

    if (then !== undefined) {
      attr = then;                       // the page rewrites the slot...
      doc.dispatch('mikrodash:resmount'); // ...and says so
      // ── REPLAYING THE SCHEMAS AFTER THE ANNOUNCEMENT IS OPTIONAL ────────
      //
      // `res:schema` re-mounts the slots itself, so replaying hides whether the
      // LISTENER re-mounted: "asks but never re-mounts" survived every case
      // until one stopped replaying. A resource whose schema is already known
      // needs no round trip, and that is exactly the case that must re-fill from
      // the listener alone.
      if (replayAfter !== false) {
        for (const s of schemas) if (handlers['res:schema']) handlers['res:schema'](s);
      }
    }
    return { before, after: host.innerHTML, emits };
  } finally {
    if (prev.doc === undefined) delete globalThis.document; else globalThis.document = prev.doc;
    if (prev.win === undefined) delete globalThis.window; else globalThis.window = prev.win;
  }
}

const problems = [];
const labels = (html) => [...html.matchAll(/data-res-addbtn="([^"]*)"/g)].map((m) => m[1]);

// ── ONE SLOT, ONE RESOURCE ──────────────────────────────────────────────────
{
  const r = run('fwFilter', undefined, [SCHEMA('fwFilter', 'filter rule')]);
  // BELIEVABILITY: a slot that never filled makes every comparison below a
  // comparison of two empty strings.
  assert.deepEqual(labels(r.before), ['fwFilter'],
    'the Add slot did not fill for the resource it names');
  assert.match(r.before, /\+ Add filter rule/, 'the button lost its label');
}

// ── THE TAB SWAPS ───────────────────────────────────────────────────────────
{
  // NO REPLAY: both schemas are already known, so the listener alone must
  // re-fill the slot. With a replay this case cannot tell the listener's
  // `mountAddSlots()` from the one `res:schema` would have done anyway.
  const r = run('fwFilter', 'fwNat',
    [SCHEMA('fwFilter', 'filter rule'), SCHEMA('fwNat', 'NAT rule')], false);
  if (labels(r.before).join() !== 'fwFilter') problems.push('before: ' + labels(r.before));
  if (labels(r.after).join() !== 'fwNat') {
    problems.push('after the swap the slot offers ' + JSON.stringify(labels(r.after)) +
                  ', want ["fwNat"] — the Add button still belongs to the previous tab');
  }
}

// ── A SLOT NAMING TWO RESOURCES ─────────────────────────────────────────────
{
  const r = run('fwFilter', 'fwNat,fwMangle',
    [SCHEMA('fwFilter', 'filter rule'), SCHEMA('fwNat', 'NAT rule'), SCHEMA('fwMangle', 'mangle rule')],
    false);
  if (labels(r.after).join() !== 'fwNat,fwMangle') {
    problems.push('a two-resource slot offers ' + JSON.stringify(labels(r.after)));
  }
}

// ── A RESOURCE WHOSE SCHEMA HAS NOT ARRIVED ─────────────────────────────────
//
// The slot must go EMPTY rather than keep the old button: a header that still
// offers "+ Add filter rule" while the NAT table is showing is the bug.
{
  const r = run('fwFilter', 'fwRaw', [SCHEMA('fwFilter', 'filter rule')]);
  if (labels(r.after).length !== 0) {
    problems.push('a slot whose new resource is unknown still offers ' +
                  JSON.stringify(labels(r.after)));
  }
}

// ── THE ANNOUNCEMENT ASKS FOR WHAT IT DOES NOT KNOW ─────────────────────────
{
  const r = run('fwFilter', 'fwRaw', [SCHEMA('fwFilter', 'filter rule')]);
  const asked = r.emits.filter((e) => e.ev === 'res:schema' &&
    e.p && e.p.resource === 'fwRaw');
  if (!asked.length) {
    problems.push('the resmount listener never asked the server for the newly named resource');
  }
}

fs.rmSync(OUT, { force: true });
if (problems.length) {
  for (const p of problems) console.error('  ' + p);
  console.error('\nresmount-seam-check: ' + problems.length + ' problem(s)');
  process.exit(1);
}
console.log('resmount-seam-check: a slot that changes resource re-fills, asks for what it needs, ' +
            'and drops what it can no longer offer');
