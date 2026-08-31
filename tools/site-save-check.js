'use strict';
/**
 * What pressing Save on the Sites form actually SENDS.
 *
 * ── A STEP-TWO GATE: IT DRIVES BOTH SIDES AND COMPARES ──────────────────────
 *
 * `saveSite` is lifted out of `public/app.js` and RUN against a shimmed form and
 * a recording `fetch`, so what is compared is the requests themselves — method,
 * path and body — not a reading of the source. The port's `siteSavePlan` is
 * bundled and asked the same question from the same form state.
 *
 * ── WHAT MAKES THIS WORTH GATING ────────────────────────────────────────────
 *
 * The save is TWO requests behind two different authorization decisions, and the
 * membership one has to go second because a new site has no id until the create
 * returns one. Get that backwards and creating a site silently drops its device
 * assignment — with both requests succeeding and nothing to see.
 *
 * The other half is subtler: this form sends `place` on EVERY save, so an empty
 * picker CLEARS the location. `ParseSiteBody` also accepts an ABSENT place,
 * which leaves it alone. Those are different requests for what looks like the
 * same form, and only the server distinguishes them — so the client's choice has
 * to be pinned here or a later "simplification" of either side goes unnoticed.
 *
 *   node tools/site-save-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { freezeCase } = require('./lib/lift.js');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('site-save-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// ── lift ────────────────────────────────────────────────────────────────────
function lift(open, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const n = src.split(open).length - 1;
  if (n !== 1) throw new Error('ambiguous anchor (' + n + '): ' + name);
  const from = src.indexOf(open);
  const to = src.indexOf(close, from);
  if (to < 0) throw new Error('unclosed: ' + name);
  return src.slice(from, to + close.length);
}
const saveSrc = G.value('saveSrc', () => lift('  function saveSite() {', '\n  }', 'saveSite'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['saveSrc', saveSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
for (const marker of ['/api/sites', '/routers', 'data-site-router', 'Name is required']) {
  if (!saveSrc.includes(marker)) {
    throw new Error('the lifted saveSite has no ' + marker + ' -- the slice stopped early');
  }
}

// ── the shim ────────────────────────────────────────────────────────────────
//
// `fetch` RECORDS rather than stubs, because the requests are the answer. The
// first reply carries a site id so the second request can be formed; the live
// code reads it out of `j.site.id` when creating.
const NEW_ID = 'created-id';

function runLive(form) {
  const calls = [];
  let error = null;

  const fields = {
    sf_id: { value: form.id },
    sf_name: { value: form.name },
    sf_description: { value: form.description },
    sf_error: { textContent: '', style: {} },
    sf_routers: {
      querySelectorAll(sel) {
        if (sel !== '[data-site-router]:checked') {
          throw new Error('the live code now selects with ' + sel + ' -- this shim answers only '
            + 'the checked-boxes query, and returning [] for anything else would make every '
            + 'membership comparison below pass vacuously');
        }
        return form.routerIds.map((id) => ({ getAttribute: () => id }));
      },
    },
    siteFormWrap: { classList: { remove() {}, add() {} } },
  };

  const fakeFetch = (url, opts) => {
    calls.push({
      method: (opts && opts.method) || 'GET',
      path: url,
      body: opts && opts.body ? JSON.parse(opts.body) : null,
    });
    const reply = { ok: true, site: { id: NEW_ID } };
    return Promise.resolve({ ok: true, json: () => Promise.resolve(reply) });
  };

  const ctx = {
    $: (id) => fields[id],
    _sitePicker: { get: () => form.place },
    _siteFormError: (m) => { if (m) error = m; },
    hideSiteForm: () => {},
    loadSites: () => {},
    fetch: fakeFetch,
    JSON, Promise, Array, encodeURIComponent, String,
  };
  const args = Object.keys(ctx);
  // eslint-disable-next-line no-new-func
  const fn = new Function(...args, saveSrc + '\nreturn saveSite;')(...args.map((k) => ctx[k]));
  fn();
  // The chain is promise-based; two microtask drains settle both requests.
  return Promise.resolve().then(() => Promise.resolve()).then(() => Promise.resolve())
    .then(() => ({ calls, error }));
}

// ── the port ────────────────────────────────────────────────────────────────
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-site-save.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const port = require(OUT);

function runPort(form) {
  const plan = port.siteSavePlan(form);
  if (plan.error) return { calls: [], error: plan.error };
  // The placeholder is substituted the way the caller must: with the id the
  // first reply returned.
  const calls = plan.requests.map((r) => ({
    method: r.method, path: r.path.replace('{id}', NEW_ID), body: r.body,
  }));
  return { calls, error: null };
}

// ── the cases ───────────────────────────────────────────────────────────────
const PLACE = { name: 'Northtown', region: 'NR', cc: 'ZZ', lat: 12.5, lon: -3.25 };
const CASES = [
  ['creating a site', { id: '', name: 'Warehouse', description: 'north', place: null,
    routerIds: ['r1', 'r2'] }],
  ['editing a site', { id: 'site-a', name: 'Depot', description: 'main', place: PLACE,
    routerIds: ['r1'] }],
  // NAME REQUIRED, and trimmed BEFORE the test -- so spaces are refused in the
  // form rather than by a round trip.
  ['an empty name', { id: '', name: '', description: '', place: null, routerIds: [] }],
  ['a whitespace name', { id: '', name: '   ', description: '', place: null, routerIds: [] }],
  ['a padded name is trimmed and kept', { id: '', name: '  Depot  ', description: '',
    place: null, routerIds: [] }],
  ['a padded description is trimmed', { id: 'site-a', name: 'D', description: '  x  ',
    place: null, routerIds: [] }],
  // NO DEVICES TICKED still sends the membership call -- with an empty array,
  // which is how a site is emptied. Skipping it would make "remove the last
  // device" impossible from this form.
  ['no devices ticked', { id: 'site-a', name: 'D', description: '', place: null, routerIds: [] }],
  // AN ID NEEDING ESCAPING, in both requests.
  ['an id with a slash', { id: 'a/b', name: 'D', description: '', place: null,
    routerIds: ['r1'] }],
  ['a location being cleared', { id: 'site-a', name: 'D', description: '', place: null,
    routerIds: ['r1'] }],
];

const bad = [];
let checks = 0;

(async () => {
  for (const [name, form] of CASES) {
    // One form object reaches BOTH runs; a mutating run would leak its state
    // into the other and make the gate accuse correct code. See
    // lift.js:freezeCase — this happened once and was hard to see.
    freezeCase(form);
    checks++;
    const a = await runLive(form);
    const b = runPort(form);
    const as = JSON.stringify(a, null, 1), bs = JSON.stringify(b, null, 1);
    if (as !== bs) bad.push({ name, live: as, port: bs });
  }

  // ── BELIEVABILITY ─────────────────────────────────────────────────────────
  {
    const created = await runLive(CASES[0][1]);
    if (created.calls.length !== 2) {
      throw new Error('a create made ' + created.calls.length + ' requests, not 2 -- the '
        + 'membership call is the one that goes missing, and every comparison above would '
        + 'then agree about one request');
    }
    if (!/\/routers$/.test(created.calls[1].path)) {
      throw new Error('the SECOND request is not the membership one; the order is what this '
        + 'gate exists to pin');
    }
    if (!created.calls[1].path.includes(NEW_ID)) {
      throw new Error('the membership call did not use the id the create returned');
    }
    if (created.calls[0].method !== 'POST') {
      throw new Error('creating did not POST');
    }
    const edited = await runLive(CASES[1][1]);
    if (edited.calls[0].method !== 'PUT' || !edited.calls[0].path.endsWith('site-a')) {
      throw new Error('editing did not PUT to the site');
    }
    // The refusal really refuses: no request at all.
    const refused = await runLive(CASES[2][1]);
    if (refused.calls.length !== 0 || !refused.error) {
      throw new Error('an empty name still sent ' + refused.calls.length + ' request(s)');
    }
    // `place` is present on EVERY save, which is what makes an empty picker a
    // CLEAR rather than a no-op. If the live code ever stops sending it, the
    // port's comment about the absent case becomes wrong.
    if (!('place' in created.calls[0].body)) {
      throw new Error('the live form no longer sends `place` on a create -- the absent-versus-'
        + 'null rule the port documents has changed and needs rereading');
    }
  }

  if (bad.length) {
    for (const b of bad) {
      console.error('\n' + b.name + '\n  live: ' + b.live + '\n  port: ' + b.port);
    }
    console.error('\nsite-save-check: ' + bad.length + ' of ' + checks + ' differ');
    process.exit(1);
  }
  console.log('site save matches the live form (' + checks + ' cases, both requests compared)');
})();
