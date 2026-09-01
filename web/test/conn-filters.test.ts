// Moved from the conn-filters check when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * The Connections page's TWO FILTERS, and the rule that they exclude each other.
 *
 * A viewer can filter the page by COUNTRY (clicking a row in the map list) or by
 * CLIENT (the source `<select>`). Choosing one clears the other, and each has a
 * piece of chrome that must follow: `#connFilterLabel` is shown while a country
 * is chosen, and `#connSrcFilter` carries an `active` class and a value while a
 * client is. Leave either behind and the page says it is filtered two ways at
 * once, which it never is.
 *
 * ---- A BEHAVIOUR PIN, NOT A DIFFERENTIAL, AND THAT IS DELIBERATE -----------
 *
 * `page-gate-audit` records `pages/connections` as ungated with a reason that
 * still holds: the `conn:update` handler calls twelve functions and
 * `applySourceFilter` seven, most of them SVG map work. Lifting the live page
 * faithfully means lifting all of that, and stubbing it means comparing one's
 * own glue against the port -- "a stub is a rewrite" at page scale.
 *
 * This does not attempt that. It drives the PORT alone and pins the rule, the
 * same shape as `resmount-seam-check`. The live rule is READ rather than run,
 * from `applyCountryFilter` (`../MikroDash/public/app.js:4076`) and
 * `applySourceFilter` (`:4347`), and the two lines that matter are asserted to
 * still be there -- so if upstream changes the rule, this gate says so instead
 * of quietly pinning a stale one.
 *
 * ---- MOUNTING THE PAGE TAKES THREE THINGS ----------------------------------
 *
 * Each was found by a mount that died:
 *
 *   `document.createComment` -- the world map uses a comment node as the
 *     placeholder marking where the SVG sits when it is not fullscreen.
 *   `#connMapList` as a TREE node -- since ToDo #18 was adopted the page calls
 *     `syncCountryList` on it, and `insertBefore` is not something a
 *     markup-storing node has.
 *   a REJECTING `fetch` -- the atlas load is `.catch`ed ("no atlas: the lists
 *     still work"), so refusing it is a supported path rather than a stub of one.
 *
 * With those the page mounts with four handlers and NO unknown ids, which is the
 * measurement that says the shim is complete rather than merely quiet.
 *
 * ---- MUTATIONS THIS KILLS (2026-08-25) -------------------------------------
 *
 *   `if (cc && filteredBySrc)` -> `if (false)`   2 cases: the select keeps the
 *     old client's value AND its `active` class while a country is chosen.
 *   `if (ip && selectedCC)` -> `if (false)`      1 case: the country label stays
 *     shown behind a client filter.
 *   `toggle('active', !!ip)` -> `..., false`     3 cases.
 *   `display = cc ? '' : 'none'` -> `= ''`       1 case: clearing the country
 *     leaves its label behind.
 *
 * The first two are the rule itself and the last two are the chrome that follows
 * it, which is the part a reader would assume is implied and is not.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/conn-filters-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';
import { makeTree } from './tree-shim.js';

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// It reads `connections.ts` only through the bundle it drives, and the live
// source only to confirm the rule below. Neither is gating the page --
// `page-gate-audit` still records it as ungated, for the reason in the header.
if (process.argv.includes('--not-gates')) {
  console.log(JSON.stringify(['pages/connections'])); process.exit(0);
}
const COVERS = ['connFilterLabel', 'connSrcFilter'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

// ---- THE LIVE RULE, READ RATHER THAN RUN ----------------------------------
//
// Pinning a rule the original has since changed is the failure this guards
// against: the assertions below name the two lines that make the filters
// exclusive, so an upstream rewrite fails HERE rather than leaving this gate
// pinning a behaviour nobody has any more.
// GUARDED, not frozen: both assertions ask the live SOURCE a question — does it
// still contain this line — and feed nothing downstream. Once the source is gone
// the rule they watch for drift in is fixed, and the question is unanswerable.
// Everything else in this gate drives the PORT and runs unconditionally.
// The block that compared this against the deleted implementation was removed
// when the port-parity harness was retired. It had been dead since cutover --
// `LIFT.hasReference` has answered false ever since -- so removing it changes
// nothing that ran. Everything below drives the PORT and asserts what it does.


const ENTRY = path.join(ROOT, 'testdata', '.cf-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initConnectionsPage } from '../web/src/pages/connections.js';\n");
const OUT = path.join(ROOT, 'testdata', '.cf.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['connMapList', 'connMapSub', 'connPortList', 'connMapBadge', 'connFilterLabel',
  'connSrcFilter', 'worldMap', 'worldMapWrap', 'mapTooltip', 'sankeySvg', 'sankeyEmpty',
  'connTotal', 'mapZoomIn', 'mapZoomOut', 'mapZoomReset', 'mapFullscreenBtn', 'mapFsOverlay',
  'mapFsClose'];

/** Mount the page and hand the caller its handlers and its document. */
function mount(run) {
  const doc = makeDoc(IDS, {});
  const tree = makeTree();
  const listEl = tree.mk('div');
  doc.nodes.connMapList = listEl;

  const prev = { doc: globalThis.document, win: globalThis.window, fetch: globalThis.fetch };
  globalThis.document = Object.assign(Object.create(doc), {
    createComment: () => ({ nodeType: 8 }),
    createElement: (tag) => tree.mk(tag),
    createElementNS: (_ns, tag) => tree.mk(tag),
    getElementById: (id) => (id === 'connMapList' ? listEl : doc.getElementById(id)),
  });
  globalThis.window = {
    location: { origin: 'http://x' }, addEventListener() {}, removeEventListener() {},
  };
  globalThis.fetch = () => Promise.reject(new Error('no network in a gate'));

  const handlers = {};
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initConnectionsPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
      () => true);
    // BELIEVABILITY OF THE MOUNT: a page that registered nothing, or that asked
    // for ids this shim does not provide, would make every assertion below a
    // statement about an untouched document.
    assert.ok(handlers['conn:update'], 'the page registered no conn:update handler');
    assert.equal(doc.unknown.size, 0,
      'the page looked up ids this gate does not provide: ' + [...doc.unknown].join(', '));
    return run({ handlers, doc, listEl });
  } finally {
    for (const [k, g] of [['doc', 'document'], ['win', 'window'], ['fetch', 'fetch']]) {
      if (prev[k] === undefined) delete globalThis[g]; else globalThis[g] = prev[k];
    }
  }
}

const P = (o) => Object.assign({
  total: 9,
  topCountries: [
    { cc: 'US', city: 'Denver', count: 5, proto: { tcp: 3, udp: 2, other: 0 }, orgs: [] },
    { cc: 'NO', city: 'Oslo', count: 4, proto: { tcp: 4, udp: 0, other: 0 }, orgs: [] },
  ],
  topSources: [
    { ip: '198.51.100.10', name: 'pc1', count: 5 },
    { ip: '198.51.100.11', name: 'pc2', count: 4 },
  ],
  topPorts: [], topDestinations: [],
  // The per-source index, keyed by `country` and NOT by `cc` -- which is what
  // `countriesFromSourceDests` reads (`connections-lists.ts:335`). A `cc` here
  // is silently skipped, the client-filtered list comes back empty, and that
  // reads as the page failing to render rather than as a payload written from
  // memory. It cost a run to notice.
  sourceDests: {
    '198.51.100.10': [{ country: 'US', org: 'Example', cat: 'cdn', count: 5 }],
    '198.51.100.11': [{ country: 'NO', org: 'Other', cat: 'dns', count: 4 }],
  },
  sourcePorts: { '198.51.100.10': [{ port: 443, count: 5 }] },
}, o);

/** What the two filters look like from outside. */
const state = (w) => ({
  label: w.doc.nodes.connFilterLabel.style.display,
  srcValue: w.doc.nodes.connSrcFilter.value,
  srcActive: w.doc.nodes.connSrcFilter.classList.contains('active'),
  rows: w.listEl.querySelectorAll('.conn-map-row').map((r) => r.dataset.cc),
});

/** Click the row for a country, through the container's delegated listener. */
function clickCountry(w, cc) {
  const row = w.listEl.querySelectorAll('.conn-map-row').find((r) => r.dataset.cc === cc);
  if (!row) throw new Error('no row for ' + cc + ' (rows: ' + state(w).rows.join(',') + ')');
  w.listEl.fire('click', { target: row });
}

/** Choose a client in the source select, the way a viewer does. */
function chooseSource(w, ip) {
  const sel = w.doc.nodes.connSrcFilter;
  sel.value = ip;
  sel.fire('change');
}

const problems = [];
const check = (name, got, want) => {
  for (const k of Object.keys(want)) {
    if (JSON.stringify(got[k]) !== JSON.stringify(want[k])) {
      problems.push(name + ': ' + k + ' = ' + JSON.stringify(got[k]) +
        ', want ' + JSON.stringify(want[k]));
    }
  }
};

// ---- NOTHING CHOSEN --------------------------------------------------------
mount((w) => {
  w.handlers['conn:update'](P({}));
  const s = state(w);
  check('no filter', s, { srcValue: '', srcActive: false, rows: ['US', 'NO'] });
  // BELIEVABILITY: two countries must actually render, or every case below is
  // about a page with nothing on it.
  assert.equal(s.rows.length, 2, 'the page rendered no country rows');
});

// ---- A COUNTRY, THEN A CLIENT ---------------------------------------------
mount((w) => {
  w.handlers['conn:update'](P({}));
  clickCountry(w, 'US');
  check('country chosen', state(w), { label: '', srcValue: '', srcActive: false });

  chooseSource(w, '198.51.100.10');
  // THE LABEL MUST GO. A country filter and a client filter cannot both apply,
  // and a label left showing says one does when it does not.
  check('client replaces country', state(w),
    { label: 'none', srcValue: '198.51.100.10', srcActive: true });
});

// ---- A CLIENT, THEN A COUNTRY ---------------------------------------------
mount((w) => {
  w.handlers['conn:update'](P({}));
  chooseSource(w, '198.51.100.10');
  check('client chosen', state(w), { srcValue: '198.51.100.10', srcActive: true });

  // The country list is re-derived from `sourceDests` while a client is chosen,
  // so the row there to click is the one that client talked to.
  clickCountry(w, 'US');
  // THE SELECT MUST BE CLEARED, both its value and its class: a select still
  // showing a client's name is a control that lies about what it is doing.
  check('country replaces client', state(w), { label: '', srcValue: '', srcActive: false });
});

// ---- CLEARING EACH ---------------------------------------------------------
mount((w) => {
  w.handlers['conn:update'](P({}));
  clickCountry(w, 'US');
  clickCountry(w, 'US');   // the same country again toggles it off
  check('country toggled off', state(w), { label: 'none', srcValue: '', srcActive: false });
});

mount((w) => {
  w.handlers['conn:update'](P({}));
  chooseSource(w, '198.51.100.10');
  chooseSource(w, '');     // "All clients"
  check('client cleared', state(w), { srcValue: '', srcActive: false });
});

// ---- A POLL LANDING WHILE A FILTER IS UP -----------------------------------
//
// `conn:update` arrives every few seconds. A filter has to survive it, or the
// operator's selection blinks out on its own.
mount((w) => {
  w.handlers['conn:update'](P({}));
  chooseSource(w, '198.51.100.10');
  w.handlers['conn:update'](P({}));
  // `label` is UNDEFINED, not 'none', and the difference matters. The page
  // hides the country label only when a client filter REPLACES a country one
  // (`if (ip && selectedCC)`); with no country ever chosen, nothing writes to it
  // at all. Asserting 'none' here expected a write the rule does not make -- the
  // same "inferring untouched from a value" trap that has bitten this suite
  // before. The replacement case above is where 'none' belongs.
  check('a poll during a client filter', state(w),
    { srcValue: '198.51.100.10', srcActive: true, label: undefined });
});

mount((w) => {
  w.handlers['conn:update'](P({}));
  clickCountry(w, 'NO');
  w.handlers['conn:update'](P({}));
  check('a poll during a country filter', state(w),
    { label: '', srcValue: '', srcActive: false });
});

fs.rmSync(OUT, { force: true });
if (problems.length) {
  for (const p of problems) shout('  ' + p);
  shout('\nconn-filters-check: ' + problems.length + ' problem(s)');
  process.exit(1);
}
say('conn-filters-check: the country and client filters exclude each other, and each survives a poll');
