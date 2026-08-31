'use strict';
/**
 * `PUT /api/sites/:id/routers` — which devices a site gains and loses.
 *
 * ── THE BUG THIS SHAPE EXISTS TO PREVENT ───────────────────────────────────
 *
 * Before multi-site (#117), ticking a device here wrote a scalar `siteId`, and
 * the OVERWRITE WAS THE REMOVAL — there was no line that detached the device
 * from its previous site, because the previous site was never consulted. Adding
 * a device to a second site silently took it out of the first. Upstream
 * `e0c8045` is the fix.
 *
 * ── THE LOOP IS LIFTED, NOT RETYPED ────────────────────────────────────────
 *
 * The decision lives inline in the route, so it is sliced between two asserted
 * anchors and evaluated with `Routers.update` and `audit` stubbed — the two
 * things it does that are not the decision. What is recorded is which devices it
 * would have written, and with what.
 *
 * Retyping it would compare the port against a reading of the original, and this
 * loop has a reading that is easy to get wrong: it walks EVERY device, not the
 * site's members, because a device that WAS here and is no longer listed has to
 * be detached — and a per-member iteration never sees it. An implementation
 * iterating `wanted` passes every add case and fails no test that does not
 * remove something.
 *
 * Runs on the host: `src/index.js` is read, never loaded.
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.SITE_MEMBERSHIP_OUT
  || path.join(ROOT, 'testdata', 'site-membership-cases.json');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(SRC, 'src', 'index.js'), 'utf8');

// ---- THE SLICE -----------------------------------------------------------
const OPEN = '    const all = Routers.loadAll();';
const CLOSE = '\n    }';
{
  const n = src.split(OPEN).length - 1;
  assert.equal(n, 1, 'the opening anchor is ambiguous (' + n + ' matches)');
}
const from = src.indexOf(OPEN);
const to = src.indexOf(CLOSE, from);
assert.ok(to > from && to - from < 2000, 'the membership loop is not where its anchors say');
const body = src.slice(from, to + CLOSE.length);

for (const marker of [
  'shouldBeHere', 'isHere', 'before.concat', 'before.filter',
  'Routers.update', 'router.site',
]) {
  assert.ok(body.includes(marker),
    'the lifted loop has no ' + marker + ' — the slice stopped early, and this gate would '
    + 'then check the port against less than the route does');
}
// It must NOT have run past the loop into the response.
assert.ok(!body.includes('res.json'), 'the slice ran past the loop into the reply');

/** Run the live loop for one case, recording what it would have written. */
function liveChanges(all, siteId, wanted) {
  const changes = [];
  const ctx = {
    Array, String,
    req: { params: { id: siteId }, body: { routerIds: wanted } },
    // `wanted` is computed above the slice, so the harness supplies it.
    wanted: wanted.map(String),
    Routers: {
      // The slice OPENS with `const all = Routers.loadAll()`, so the fleet is
      // supplied here rather than as a context variable — a context `all` is
      // shadowed by that declaration and never read.
      loadAll: () => JSON.parse(JSON.stringify(all)),
      update(id, patch) {
        changes.push({ routerId: id, after: patch.siteIds });
      },
    },
    audit: {
      fromReq: () => ({
        record(ev) {
          const last = changes[changes.length - 1];
          assert.ok(last && last.routerId === ev.targetId,
            'an audit row was written for a device that was not just updated');
          last.before = ev.before.siteIds;
        },
      }),
    },
  };
  vm.createContext(ctx);
  // NO `let changed = 0` prepended: the slice starts one line above it and
  // brings its own declaration.
  vm.runInContext(body, ctx);
  return changes;
}

// ---- THE CASES -----------------------------------------------------------
//
// `all` is the FLEET, in fleet order. The recorded changes are in that order
// too, because the live loop walks it — a port producing them in `wanted`'s
// order writes an audit trail that reads differently for the same action.
const R = (id, sites) => (Array.isArray(sites)
  ? { id, siteIds: sites }
  : (sites ? { id, siteId: sites } : { id }));

const CASES = {
  // Nothing to do: the fleet is already in the wanted state.
  noChange: { all: [R('r1', ['s1']), R('r2', [])], siteId: 's1', wanted: ['r1'] },

  // A plain add and a plain remove.
  addOne: { all: [R('r1', []), R('r2', [])], siteId: 's1', wanted: ['r1'] },
  removeOne: { all: [R('r1', ['s1']), R('r2', [])], siteId: 's1', wanted: [] },

  // THE BUG. A device already in another site joins this one and KEEPS the
  // other. The pre-#117 write replaced it.
  addKeepsOtherSites: { all: [R('r1', ['s2'])], siteId: 's1', wanted: ['r1'] },
  // ...and leaving takes only this one.
  removeKeepsOtherSites: { all: [R('r1', ['s2', 's1', 's3'])], siteId: 's1', wanted: [] },

  // THE DETACH A PER-MEMBER LOOP CANNOT SEE. r2 is here and is not listed, so it
  // has to be removed — and it is not in `wanted`, which is what makes it a
  // removal. An implementation iterating `wanted` misses it entirely.
  detachAnUnlisted: {
    all: [R('r1', []), R('r2', ['s1'])], siteId: 's1', wanted: ['r1'],
  },

  // Both directions in one save, which is what a real edit looks like.
  addAndRemoveTogether: {
    all: [R('r1', ['s1']), R('r2', []), R('r3', ['s1', 's2'])],
    siteId: 's1', wanted: ['r2', 'r3'],
  },

  // The PRE-#117 SCALAR still reads. A record written before multi-site carries
  // `siteId` and no list.
  scalarJoinsASecondSite: { all: [R('r1', 's2')], siteId: 's1', wanted: ['r1'] },
  scalarLeavesItsOnlySite: { all: [R('r1', 's1')], siteId: 's1', wanted: [] },

  // A record with NEITHER field.
  noSiteFieldsAtAll: { all: [R('r1', null)], siteId: 's1', wanted: ['r1'] },

  // A wanted id that is not in the fleet is ignored rather than invented.
  wantedNamesAStranger: { all: [R('r1', [])], siteId: 's1', wanted: ['r1', 'ghost'] },

  // Emptying a site detaches everything in it and touches nothing else.
  emptyTheSite: {
    all: [R('r1', ['s1']), R('r2', ['s2']), R('r3', ['s1', 's2'])],
    siteId: 's1', wanted: [],
  },
};

const cases = {};
for (const [name, c] of Object.entries(CASES)) {
  cases[name] = { ...c, changes: liveChanges(c.all, c.siteId, c.wanted) };
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const ch = (k) => cases[k].changes;

  assert.deepEqual(ch('noChange'), [],
    'a fleet already in the wanted state produced changes — every device would get a '
    + 'router.site audit row on every save');

  assert.equal(ch('addOne').length, 1, 'a plain add did not change exactly one device');
  assert.deepEqual(ch('addOne')[0].after, ['s1']);
  assert.deepEqual(ch('removeOne')[0].after, []);

  // THE BUG, both directions.
  assert.deepEqual(ch('addKeepsOtherSites')[0].after, ['s2', 's1'],
    'joining a site DROPPED the other one — this is the pre-#117 overwrite, which is '
    + 'the whole defect e0c8045 fixes');
  assert.deepEqual(ch('removeKeepsOtherSites')[0].after, ['s2', 's3'],
    'leaving one site removed more than one');

  // The detach.
  const detach = ch('detachAnUnlisted');
  assert.equal(detach.length, 2, 'the unlisted member was not detached — a loop over '
    + '`wanted` can only ever ADD, and this is the case that shows it');
  const r2 = detach.find((c) => c.routerId === 'r2');
  assert.ok(r2 && r2.after.length === 0, 'r2 was not detached');

  // Fleet order, not wanted order.
  assert.deepEqual(ch('addAndRemoveTogether').map((c) => c.routerId), ['r1', 'r2'],
    'the changes are not in FLEET order');

  // The scalar.
  assert.deepEqual(ch('scalarJoinsASecondSite')[0].before, ['s2'],
    'a pre-#117 scalar was not read as a one-element list');
  assert.deepEqual(ch('scalarJoinsASecondSite')[0].after, ['s2', 's1']);
  assert.deepEqual(ch('scalarLeavesItsOnlySite')[0].after, []);

  assert.deepEqual(ch('noSiteFieldsAtAll')[0].before, []);
  assert.deepEqual(ch('noSiteFieldsAtAll')[0].after, ['s1']);

  assert.equal(ch('wantedNamesAStranger').length, 1,
    'a wanted id not in the fleet produced a change');

  assert.equal(ch('emptyTheSite').length, 2, 'emptying the site touched the wrong number');
  const kept = ch('emptyTheSite').find((c) => c.routerId === 'r3');
  assert.deepEqual(kept.after, ['s2'], 'a device in two sites lost the one it keeps');
  assert.ok(!ch('emptyTheSite').some((c) => c.routerId === 'r2'),
    'a device that was never in this site was touched');
}

const json = JSON.stringify({ cases }, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('site-membership-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  console.log('site-membership-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
