// Moved from the sites-card check when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * The Sites card's WIRING — the parts no differential gate can reach.
 *
 * ── THIS IS A WEAKER GATE THAN THE OTHERS, AND SAYS SO ──────────────────────
 *
 * `access-summary-check` and `site-save-check` drive the LIVE implementations
 * and compare. This one cannot: the card's wiring lives inside `saveSite`,
 * `showSiteForm` and `loadSites`, which between them touch a picker, six form
 * fields, a socket and two fetches — there is no seam to lift them through. So
 * this gate drives the PORT alone against a DOM shim and asserts what it does.
 * That catches a regression; it does not catch a divergence from the original.
 * Stated here rather than left to be inferred, as `settings-write-tables.js`
 * does for the same reason.
 *
 * What IS compared against the live app lives elsewhere and is not repeated
 * here: the table markup, the device rows, the delete prompt
 * (`access-summary-check`) and both request shapes (`site-save-check`).
 *
 * ── THE ASSERTION THIS FILE EXISTS FOR ──────────────────────────────────────
 *
 * `siteSavePlan` reproduces the live `place: picker.get()`, where an EMPTY
 * picker CLEARS the location. The port has no picker wired yet, so a naive
 * caller passes null or undefined — and `?? null` turns both into a clear,
 * wiping a site's map pin on every unrelated edit. That bug was written and
 * caught in the same tick, by reading `city-picker.ts`'s header, which records
 * the identical trap for the router modal. `an edit re-sends the stored place`
 * is the case that keeps it caught.
 *
 *   node tools/sites-card-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

// ── the shim ────────────────────────────────────────────────────────────────
//
// Elements carry a real `classList` and record their innerHTML, because the
// whole card is class toggling and markup assignment. A stub would let every
// assertion below pass against a card that did nothing.
function makeEl(id) {
  const classes = new Set();
  const listeners = {};
  const node = {
    id,
    value: '',
    textContent: '',
    innerHTML: '',
    style: {},
    hidden: false,
    focus() { node.__focused = true; },
    setAttribute: (k, v) => { node[k] = v; },
    getAttribute: (k) => (k in node ? node[k] : null),
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (listeners[ev] || []).forEach((fn) => fn(arg)),
    has: (c) => classes.has(c),
    // The form's checked boxes. Parsed out of the innerHTML the port wrote, so
    // what is queried is what it actually rendered rather than a fixture.
    querySelectorAll(sel) {
      assert.equal(sel, '[data-site-router]:checked',
        'the card now queries with ' + sel + '; this shim answers only the checked-boxes '
        + 'query, and returning [] for anything else would make the membership assertions '
        + 'pass against a card that selected nothing');
      const out = [];
      const re = /data-site-router="([^"]*)"([^>]*)>/g;
      let m;
      while ((m = re.exec(node.innerHTML))) {
        // THE VALUE, not the match. `getAttribute: () => m[1]` closes over the
        // loop variable, which `exec` sets to null once it stops matching — so
        // every attribute read after the loop threw.
        const id = m[1];
        if (/\bchecked\b/.test(m[2])) out.push({ getAttribute: () => id });
      }
      return out;
    },
  };
  return node;
}

function makeDoc() {
  const ids = ['siteTbody', 'sf_id', 'sf_name', 'sf_description', 'sf_error', 'sf_routers',
    'sf_title', 'siteFormWrap', 'addSiteBtn', 'sf_save', 'sf_cancel',
    // The town picker. Present here because without them `ensurePicker()`
    // returns null and every location assertion below passes against a form
    // that has no picker at all -- which is exactly the state this gate was
    // written to move the port OUT of.
    'sf_place', 'sf_placeList', 'sf_placeClear'];
  const els = {};
  ids.forEach((id) => { els[id] = makeEl(id); });
  return {
    els,
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [],
    addEventListener: () => {},
  };
}

// ── the port ────────────────────────────────────────────────────────────────
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-sites-card.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings-sites.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const SITES = [
  { id: 's1', name: 'Depot', description: 'main' },
  { id: 's2', name: 'Annexe', description: null,
    place_name: 'Northtown', place_region: 'NR', place_cc: 'ZZ', lat: 12.5, lon: -3.25 },
];
const FLEET = [
  { id: 'r1', label: 'One', siteIds: ['s1'] },
  { id: 'r2', label: 'Two', siteIds: [] },
];

/** Mount a fresh card. `sitesReply` is what GET /api/sites answers. */
async function mount(opts) {
  const o = opts || {};
  const doc = makeDoc();
  const calls = [];
  const confirms = [];

  global.document = doc;
  global.window = { confirm: (m) => { confirms.push(m); return o.confirm !== false; } };
  global.fetch = (url, init) => {
    calls.push({
      url,
      method: (init && init.method) || 'GET',
      body: init && init.body ? JSON.parse(init.body) : null,
    });
    if (url.startsWith('/api/cities')) {
      const q = decodeURIComponent(url.split('q=')[1] || '');
      const reply = { ok: true, json: () => Promise.resolve({ cities: (o.cities || {})[q] || [] }) };
      // A case can hold one answer back, to prove a slow "ber" cannot land after
      // a fast "berlin" and repaint the list with the wrong towns.
      const hold = (o.holdCity || {})[q];
      return hold ? new Promise((r) => { hold.release = () => r(reply); }) : Promise.resolve(reply);
    }
    if (url === '/api/sites' && (!init || !init.method || init.method === 'GET')) {
      if (o.listFails) return Promise.reject(new Error('down'));
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, sites: SITES }) });
    }
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ ok: true, site: { id: 'new-id' } }),
    });
  };

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  mod.initSitesCard(() => FLEET);
  await settle();
  return { doc, calls, confirms, mod };
}

const settle = () => new Promise((r) => setImmediate(r));
// PAST THE DEBOUNCE. `CITY_DEBOUNCE_MS` is 250, and a search that has not fired
// yet looks exactly like a search that fired and found nothing.
const afterDebounce = () => new Promise((r) => setTimeout(r, 400));

// ── the cases ───────────────────────────────────────────────────────────────
const problems = [];
let checks = 0;

function check(name, fn) {
  checks++;
  try {
    return fn();
  } catch (e) {
    problems.push(name + ': ' + e.message);
    return undefined;
  }
}

(async () => {
  // 1. The table renders from what the fetch returned.
  {
    const { doc, calls } = await mount();
    check('the list is fetched', () => {
      assert.ok(calls.some((c) => c.url === '/api/sites' && c.method === 'GET'),
        'GET /api/sites was never made');
    });
    check('the table renders both sites', () => {
      const html = doc.els.siteTbody.innerHTML;
      assert.ok(html.includes('Depot') && html.includes('Annexe'), 'rendered: ' + html);
      // Believability: the count column really is filled from the fleet, so this
      // is not agreeing with an empty render.
      assert.ok(/>1</.test(html), 'no device count reached the table: ' + html);
    });
  }

  // 2. A FAILED LOAD says so, rather than showing the empty state. "No sites
  //    yet" after a failed fetch invites somebody to create one that exists and
  //    be told the name is taken.
  {
    const { doc } = await mount({ listFails: true });
    check('a failed load is visible', () => {
      assert.match(doc.els.siteTbody.innerHTML, /Could not load sites/,
        'a failed load rendered: ' + doc.els.siteTbody.innerHTML);
    });
  }

  // 3. EDIT fills the form and ticks the right devices.
  {
    const { doc } = await mount();
    doc.els.siteTbody.fire('click', {
      target: { closest: (s) => (s === '[data-site-action]'
        ? { getAttribute: (a) => (a === 'data-site-id' ? 's1' : 'edit') } : null) },
    });
    await settle();
    check('editing fills the form', () => {
      assert.equal(doc.els.sf_id.value, 's1');
      assert.equal(doc.els.sf_name.value, 'Depot');
      assert.equal(doc.els.sf_description.value, 'main');
      assert.equal(doc.els.sf_title.textContent, 'Edit Site');
      assert.ok(doc.els.siteFormWrap.has('open'), 'the form was not opened');
    });
    check('the right device is ticked', () => {
      const html = doc.els.sf_routers.innerHTML;
      assert.match(html, /data-site-router="r1" checked/, 'r1 not ticked: ' + html);
      assert.ok(!/data-site-router="r2" checked/.test(html), 'r2 wrongly ticked');
    });
  }

  // 4. ADDING opens an empty form with nothing ticked.
  {
    const { doc } = await mount();
    doc.els.addSiteBtn.fire('click');
    await settle();
    check('adding opens an empty form', () => {
      assert.equal(doc.els.sf_id.value, '');
      assert.equal(doc.els.sf_name.value, '');
      assert.equal(doc.els.sf_title.textContent, 'Add Site');
      assert.ok(!/checked/.test(doc.els.sf_routers.innerHTML), 'a device was pre-ticked');
    });
  }

  // 5. ── THE ONE THIS FILE EXISTS FOR ──────────────────────────────────────
  //    The picker is SEEDED from the site's three place_* columns when the form
  //    opens, and what it holds is what the save sends. Before the picker was
  //    mounted this case asserted a workaround — the site's stored place was
  //    re-sent — because `place ?? null` would otherwise have CLEARED the
  //    location on every unrelated edit. The workaround is gone; the hazard is
  //    not, so the case stays and now watches the real mechanism.
  {
    const { doc, calls } = await mount();
    doc.els.siteTbody.fire('click', {
      target: { closest: () => ({ getAttribute: (a) => (a === 'data-site-id' ? 's2' : 'edit') }) },
    });
    await settle();

    check('the picker is seeded from the place columns', () => {
      assert.match(doc.els.sf_place.value, /Northtown/,
        'the location box shows ' + JSON.stringify(doc.els.sf_place.value)
        + ' -- an empty box over a set location invites somebody to overwrite it');
    });

    doc.els.sf_name.value = 'Annexe 2';
    doc.els.sf_save.fire('click');
    await settle(); await settle(); await settle();

    check('an edit sends the seeded place', () => {
      const put = calls.find((c) => c.method === 'PUT' && c.url === '/api/sites/s2');
      assert.ok(put, 'no PUT to the site; calls: ' + JSON.stringify(calls.map((c) => c.url)));
      assert.ok(put.body.place, 'place was sent as ' + JSON.stringify(put.body.place)
        + ' -- a null CLEARS the location, so every unrelated edit would wipe the map pin');
      assert.equal(put.body.place.name, 'Northtown');
      assert.equal(put.body.place.lat, 12.5);
      assert.equal(put.body.name, 'Annexe 2');
    });
    check('the membership call follows the site call', () => {
      const iSite = calls.findIndex((c) => c.url === '/api/sites/s2');
      const iMem = calls.findIndex((c) => c.url === '/api/sites/s2/routers');
      assert.ok(iSite >= 0 && iMem > iSite,
        'the membership call did not follow the site call: '
        + JSON.stringify(calls.map((c) => c.url)));
    });
  }

  // 6. CLEARING the box sends null, which is how a location is removed. Paired
  //    with case 5 on purpose: without it, "always send the seeded place" would
  //    pass there and make the Clear button do nothing.
  {
    const { doc, calls } = await mount();
    doc.els.siteTbody.fire('click', {
      target: { closest: () => ({ getAttribute: (a) => (a === 'data-site-id' ? 's2' : 'edit') }) },
    });
    await settle();
    doc.els.sf_placeClear.fire('click');
    check('clearing empties the box', () => {
      assert.equal(doc.els.sf_place.value, '', 'the box still reads ' + doc.els.sf_place.value);
    });
    doc.els.sf_save.fire('click');
    await settle(); await settle(); await settle();
    check('a cleared location sends null', () => {
      const put = calls.find((c) => c.url === '/api/sites/s2');
      assert.ok(put, 'no PUT was made');
      assert.strictEqual(put.body.place, null,
        'Clear sent ' + JSON.stringify(put.body.place) + ' -- the button does nothing');
    });
  }

  // 6b. A site with NO location seeds an empty box and sends null.
  {
    const { doc, calls } = await mount();
    doc.els.siteTbody.fire('click', {
      target: { closest: () => ({ getAttribute: (a) => (a === 'data-site-id' ? 's1' : 'edit') }) },
    });
    await settle();
    doc.els.sf_save.fire('click');
    await settle(); await settle(); await settle();
    check('a site with no location sends null', () => {
      assert.equal(doc.els.sf_place.value, '', 'a site with no location seeded a box');
      const put = calls.find((c) => c.url === '/api/sites/s1');
      assert.ok(put, 'no PUT was made');
      assert.strictEqual(put.body.place, null,
        'place was ' + JSON.stringify(put.body.place) + ' for a site that has none');
    });
  }

  // ── THE SEARCH PATH ────────────────────────────────────────────────────────
  //
  // Added after three mutations SURVIVED: a stale response winning, a blur
  // committing typed text, and picking a row doing nothing. All three live in
  // the search, and nothing above typed a character — so the picker was gated
  // for its seeding and not for its use.
  const BERLIN = { name: 'Berlin', region: 'BE', cc: 'DE', lat: 52.5, lon: 13.4 };
  const BERGEN = { name: 'Bergen', region: 'VL', cc: 'NO', lat: 60.4, lon: 5.3 };

  // 6c. Typing searches, and PICKING a row commits it — box, and then the save.
  {
    const { doc, calls } = await mount({ cities: { berlin: [BERLIN] } });
    doc.els.addSiteBtn.fire('click');
    await settle();
    doc.els.sf_place.value = 'berlin';
    doc.els.sf_place.fire('input');
    await afterDebounce();

    check('typing searches and paints the list', () => {
      assert.ok(calls.some((c) => c.url.startsWith('/api/cities')), 'no search was made');
      assert.match(doc.els.sf_placeList.innerHTML, /Berlin/,
        'the list holds: ' + doc.els.sf_placeList.innerHTML);
      assert.equal(doc.els.sf_placeList.hidden, false, 'the list stayed hidden');
    });

    doc.els.sf_placeList.fire('click', {
      target: { closest: (sel) => (sel === '[data-i]' ? { getAttribute: () => '0' } : null) },
    });
    check('picking a row commits it', () => {
      assert.match(doc.els.sf_place.value, /Berlin/,
        'the box reads ' + JSON.stringify(doc.els.sf_place.value) + ' after a pick');
      assert.equal(doc.els.sf_placeList.hidden, true, 'the list stayed open after a pick');
    });

    doc.els.sf_name.value = 'New';
    doc.els.sf_save.fire('click');
    await settle(); await settle(); await settle();
    check('the picked town is what gets saved', () => {
      const post = calls.find((c) => c.method === 'POST' && c.url === '/api/sites');
      assert.ok(post, 'no POST was made');
      assert.ok(post.body.place, 'place was ' + JSON.stringify(post.body.place));
      assert.equal(post.body.place.name, 'Berlin');
      assert.equal(post.body.place.lat, 52.5);
    });
  }

  // 6d. TYPED TEXT IS NOT A LOCATION. Leaving the box restores what was
  //     committed; a half-typed name must never be saved as a place.
  {
    const { doc, calls } = await mount({ cities: { berlin: [BERLIN] } });
    doc.els.siteTbody.fire('click', {
      target: { closest: () => ({ getAttribute: (a) => (a === 'data-site-id' ? 's2' : 'edit') }) },
    });
    await settle();
    doc.els.sf_place.value = 'berl';       // typed over the seeded "Northtown"
    doc.els.sf_place.fire('blur');
    await afterDebounce();
    check('leaving the box restores the committed text', () => {
      assert.match(doc.els.sf_place.value, /Northtown/,
        'the box kept the typed text: ' + JSON.stringify(doc.els.sf_place.value));
    });
    doc.els.sf_save.fire('click');
    await settle(); await settle(); await settle();
    check('typed text is never saved as a place', () => {
      const put = calls.find((c) => c.url === '/api/sites/s2');
      assert.ok(put && put.body.place, 'no place was sent');
      assert.equal(put.body.place.name, 'Northtown',
        'the typed text became a location: ' + JSON.stringify(put.body.place));
    });
  }

  // 6e. A SLOW ANSWER FOR AN OLD QUERY IS DISCARDED. "ber" is held, "bergen"
  //     answers, then "ber" is released — the list must still be Bergen.
  {
    const held = { ber: {} };
    const { doc } = await mount({
      cities: { ber: [BERLIN], bergen: [BERGEN] }, holdCity: held,
    });
    doc.els.addSiteBtn.fire('click');
    await settle();
    doc.els.sf_place.value = 'ber';
    doc.els.sf_place.fire('input');
    await afterDebounce();
    doc.els.sf_place.value = 'bergen';
    doc.els.sf_place.fire('input');
    await afterDebounce();
    check('the fast answer is showing first', () => {
      assert.match(doc.els.sf_placeList.innerHTML, /Bergen/,
        'the list holds: ' + doc.els.sf_placeList.innerHTML);
    });
    if (held.ber.release) held.ber.release();
    await settle(); await settle();
    check('a stale answer does not repaint the list', () => {
      assert.match(doc.els.sf_placeList.innerHTML, /Bergen/,
        'a slow answer for an older query repainted the list: '
        + doc.els.sf_placeList.innerHTML);
      assert.ok(!/Berlin/.test(doc.els.sf_placeList.innerHTML),
        'the stale result is showing');
    });
  }

  // 7. An EMPTY NAME is refused in the form, with no request at all.
  {
    const { doc, calls } = await mount();
    const before = calls.length;
    doc.els.addSiteBtn.fire('click');
    await settle();
    doc.els.sf_name.value = '   ';
    doc.els.sf_save.fire('click');
    await settle(); await settle();
    check('an empty name is refused locally', () => {
      assert.equal(calls.length, before, 'a request was made for an empty name');
      assert.match(doc.els.sf_error.textContent, /Name is required/);
      assert.equal(doc.els.sf_error.style.display, 'block', 'the error was not shown');
    });
  }

  // 8. DELETE asks first, and a CANCELLED confirm makes no request.
  {
    const { doc, calls, confirms } = await mount({ confirm: false });
    const before = calls.length;
    doc.els.siteTbody.fire('click', {
      target: { closest: () => ({ getAttribute: (a) => (a === 'data-site-id' ? 's1' : 'delete') }) },
    });
    await settle(); await settle();
    check('a cancelled delete makes no request', () => {
      assert.equal(confirms.length, 1, 'the operator was not asked');
      assert.match(confirms[0], /Delete site "Depot"/);
      // s1 has one device, so the #117 warning must be in the prompt.
      assert.match(confirms[0], /keep any other sites/, 'the warning is missing: ' + confirms[0]);
      assert.equal(calls.length, before, 'a cancelled delete still called the server');
    });
  }

  // 9. ...and a CONFIRMED one deletes and reloads.
  {
    const { doc, calls } = await mount();
    doc.els.siteTbody.fire('click', {
      target: { closest: () => ({ getAttribute: (a) => (a === 'data-site-id' ? 's1' : 'delete') }) },
    });
    await settle(); await settle(); await settle();
    check('a confirmed delete calls the server and reloads', () => {
      assert.ok(calls.some((c) => c.method === 'DELETE' && c.url === '/api/sites/s1'),
        'no DELETE: ' + JSON.stringify(calls.map((c) => c.method + ' ' + c.url)));
      assert.ok(calls.filter((c) => c.url === '/api/sites' && c.method === 'GET').length >= 2,
        'the table was not reloaded after the delete');
    });
  }

  // 10. `sites:update` re-renders without a fetch — another administrator's
  //     change must not leave this tab stale, and must not cost a round trip.
  {
    const { doc, calls, mod } = await mount();
    const before = calls.length;
    mod.onSitesUpdate([{ id: 's9', name: 'Elsewhere', description: null }]);
    check('a broadcast re-renders in place', () => {
      assert.match(doc.els.siteTbody.innerHTML, /Elsewhere/);
      assert.ok(!/Depot/.test(doc.els.siteTbody.innerHTML), 'the old list survived');
      assert.equal(calls.length, before, 'the broadcast triggered a fetch');
    });
  }

  // 11. CANCEL closes the form without saving.
  {
    const { doc, calls } = await mount();
    doc.els.addSiteBtn.fire('click');
    await settle();
    const before = calls.length;
    doc.els.sf_cancel.fire('click');
    check('cancel closes without saving', () => {
      assert.ok(!doc.els.siteFormWrap.has('open'), 'the form stayed open');
      assert.equal(calls.length, before, 'cancel made a request');
    });
  }

  if (problems.length) {
    problems.forEach((p) => console.error('  ✗ ' + p));
    console.error('\nsites-card-check: ' + problems.length + ' of ' + checks + ' failed');
    process.exit(1);
  }
  console.log('sites card wiring ok (' + checks + ' checks; PORT-ONLY — see the header)');
})();
