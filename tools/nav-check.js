'use strict';
/**
 * The sidebar's grouping, live against ported.
 *
 * The live implementation is a top-level BLOCK, not a function — declaring its
 * state, wiring the headers, firing the GET and painting, all as the script
 * parses. So it is lifted whole and RUN, once per case, in a fresh context:
 * running it IS calling `initNav()`, and the two are compared on everything
 * they leave behind — the `data-nav` attribute, each group's `is-open` class and
 * `aria-expanded`, the grouping checkboxes, localStorage, and the POST bodies
 * that reach the server after the debounce.
 *
 * ── THE CASES ARE THE ONES THAT MISBEHAVED IN THE LIVE APP ──────────────────
 *
 * Two of the live comments describe bugs that were fixed there, and both are
 * reachable only through a SEQUENCE. A gate that called the render function with
 * hand-set state would miss both:
 *
 *   collapse the group you are standing in
 *                     the click removes it from the saved set and the next
 *                     render must NOT put it back — which is why the
 *                     auto-expanded category is state that a click can clear
 *                     rather than something derived from the active page.
 *   the first click on an auto-expanded group
 *                     must CLOSE it. Keying the toggle on the saved set instead
 *                     of what is on screen made that first click expand an
 *                     already-open group, so it took two clicks to shut.
 *
 * And one that is invisible without watching the network:
 *
 *   the debounce      two clicks in quick succession are ONE POST, not two, and
 *                     the body carries the SORTED set.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/nav-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/nav-check.js --freeze
const G = L.golden('nav-check');
const src = L.liveSource(ROOT);
const CATS = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'pages-table.json'), 'utf8'))
  .categories.map((c) => c.key);

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

const navSrc = slice("var NAV_KEY = 'mkd_nav_prefs';", '\n_navRender();', 'the nav grouping block');
// The auto-expand, lifted from showPage rather than re-typed. Its guard there is
// `if (navGrp)` — a page in no category leaves the sidebar alone — so the shim
// hands it a stand-in group carrying the category under test.
const autoSrc = slice('  var navGrp = nav && nav.closest ? nav.closest(\'.nav-group\') : null;',
  '_navRender(); }', 'the showPage auto-expand');

const OUT = path.join(ROOT, 'testdata', '.nav-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'nav.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

// ── The world ───────────────────────────────────────────────────────────────

function makeNode(extra) {
  return Object.assign({
    _h: {},
    addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); },
  }, extra);
}

/** `serverResp`: null for a 404, or the object GET /api/nav-prefs answers. */
function makeWorld(seedStore, serverResp) {
  const attrs = {};
  const docEl = {
    getAttribute: (k) => (attrs[k] === undefined ? null : attrs[k]),
    setAttribute: (k, v) => { attrs[k] = String(v); },
    _attrs: attrs,
  };
  const groups = CATS.map((cat) => {
    const classes = new Set();
    const g = makeNode({
      dataset: { cat },
      classList: { toggle: (c, on) => { if (on) classes.add(c); else classes.delete(c); }, _set: classes },
      _aria: null,
    });
    g._hdr = makeNode({
      parentNode: g,
      setAttribute(k, v) { if (k === 'aria-expanded') g._aria = v; },
    });
    g.querySelector = (sel) => (sel === '.nav-group-hdr' ? g._hdr : null);
    return g;
  });
  // TWO of them: the sidebar's own switch and the copy on the Settings page.
  // Both must end up showing the same thing, which a single-checkbox world
  // could not catch.
  const boxes = [makeNode({ checked: false }), makeNode({ checked: false })];

  let bootRemoved = false;
  const navBoot = { parentNode: { removeChild: () => { bootRemoved = true; } } };

  const store = Object.assign({}, seedStore);
  const posts = [];
  const timers = new Map();
  let seq = 0;

  const doc = {
    documentElement: docEl,
    getElementById: (id) => (id === 'navBoot' ? (bootRemoved ? null : navBoot) : null),
    querySelectorAll: (sel) => {
      if (sel === '.nav-group') return groups;
      if (sel === '.nav-group-hdr') return groups.map((g) => g._hdr);
      if (sel === '.nav-grouped-input') return boxes;
      throw new Error('unexpected selector ' + sel);
    },
  };
  const localStorage = {
    getItem: (k) => (store[k] === undefined ? null : store[k]),
    setItem: (k, v) => { store[k] = String(v); },
  };
  const fetchImpl = (url, init) => {
    if (init && init.method === 'POST') {
      posts.push({ url, body: init.body, headers: init.headers, credentials: init.credentials });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });
    }
    if (serverResp === undefined) return Promise.reject(new Error('network down'));
    if (serverResp === null) return Promise.resolve({ ok: false, json: () => Promise.resolve(null) });
    return Promise.resolve({ ok: true, json: () => Promise.resolve(serverResp) });
  };
  const setTimeoutImpl = (fn, ms) => { const id = ++seq; timers.set(id, { fn, ms }); return id; };
  const clearTimeoutImpl = (id) => { timers.delete(id); };

  return {
    doc, localStorage, store, posts, groups, boxes, fetch: fetchImpl,
    setTimeout: setTimeoutImpl, clearTimeout: clearTimeoutImpl,
    /** Run every pending timer, which is what makes the debounced POST land. */
    flush() {
      const pending = [...timers.entries()].sort((a, b) => a[0] - b[0]);
      timers.clear();
      for (const [, t] of pending) t.fn();
    },
    pendingTimers: () => timers.size,
    click(cat) {
      const g = groups.find((x) => x.dataset.cat === cat);
      if (!g) throw new Error('no group ' + cat);
      for (const f of (g._hdr._h.click || [])) f.call(g._hdr, {});
    },
    setGrouped(on) {
      for (const b of boxes) b.checked = on;
      for (const f of (boxes[0]._h.change || [])) f.call(boxes[0], {});
    },
    state() {
      return JSON.stringify({
        attrs,
        bootRemoved,
        groups: groups.map((g) => ({
          cat: g.dataset.cat, open: g.classList._set.has('is-open'), aria: g._aria,
        })),
        boxes: boxes.map((b) => b.checked),
        store,
        posts,
        pending: timers.size,
      }, null, 1);
    },
  };
}

const settle = () => new Promise((r) => setImmediate(r));

async function liveRun(seed, serverResp, body) {
  const w = makeWorld(seed, serverResp);
  const ctx = {
    document: w.doc, localStorage: w.localStorage, fetch: w.fetch,
    setTimeout: w.setTimeout, clearTimeout: w.clearTimeout,
    JSON, Array, Object, String, Promise, console: { error() {} },
  };
  vm.createContext(ctx);
  vm.runInContext(navSrc, ctx);                    // running it IS initNav()
  // `closest` returns NULL when the page is in no category — which is the whole
  // point of showPage's `if (navGrp)` guard. The first version of this shim
  // always handed back a group, so "a page outside every category" ran the
  // inside of the guard with an undefined cat and modelled nothing. A mutation
  // that clobbered the auto-expanded category survived because of it.
  vm.runInContext('var __auto = function (cat) {' +
    'var nav = { closest: function () { return cat ? ' +
    '{ classList: { add: function () {} }, dataset: { cat: cat } } : null; } };' +
    autoSrc + '};', ctx);
  await settle();
  if (body) await body(ctx, w);
  return w.state();
}

async function portRun(seed, serverResp, body) {
  const w = makeWorld(seed, serverResp);
  const saved = {};
  for (const k of ['document', 'localStorage', 'fetch', 'setTimeout', 'clearTimeout']) saved[k] = global[k];
  global.document = w.doc; global.localStorage = w.localStorage; global.fetch = w.fetch;
  global.setTimeout = w.setTimeout; global.clearTimeout = w.clearTimeout;
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.initNav();
    await settle();
    if (body) await body(mod, w);
  } finally {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return w.state();
}

const bad = [];
let cases = 0;
async function compare(what, seed, serverResp, liveBody, portBody) {
  cases++;
  const a = await G.live(G.seq(), () => liveRun(seed, serverResp, liveBody));
  const b = await portRun(seed, serverResp, portBody);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}
/** Most cases act only through the world, so both sides run the same body. */
function both(what, seed, serverResp, act) {
  return compare(what, seed, serverResp,
    async (_c, w) => { await act(w); }, async (_p, w) => { await act(w); });
}

const K = 'mkd_nav_prefs';

async function main() {
  // ── Boot, from every shape the cache and the server can be in ─────────────
  const BOOTS = [
    ['nothing cached, server says nothing', {}, null],
    ['nothing cached, the request fails outright', {}, undefined],
    ['a cache the server then overrides', { [K]: '{"grouped":false,"expanded":["network"]}' },
      { grouped: true, expanded: ['system', 'traffic'] }],
    ['a cache the server cannot confirm', { [K]: '{"grouped":false,"expanded":["network"]}' }, null],
    ['a corrupt cache', { [K]: 'not json at all' }, null],
    ['a cache whose expanded is not an array', { [K]: '{"grouped":true,"expanded":"nope"}' }, null],
    ['a cache with no grouped key (absent means grouped)', { [K]: '{"expanded":["ipsvc"]}' }, null],
    ['the server answers with no grouped key', {}, { expanded: ['tunnels'] }],
    ['the server answers with a non-array expanded', {}, { grouped: false, expanded: 7 }],
    ['the server answers an empty object', {}, {}],
    ['the server names a category twice', {}, { grouped: true, expanded: ['system', 'system'] }],
  ];
  for (const [what, seed, resp] of BOOTS) {
    await both('boot: ' + what, seed, resp, () => {});
  }

  // ── Clicking a header, for every category ─────────────────────────────────
  for (const cat of CATS) {
    await both('expand ' + cat, {}, null, (w) => { w.click(cat); w.flush(); });
    await both('expand then collapse ' + cat, {}, null,
      (w) => { w.click(cat); w.click(cat); w.flush(); });
  }
  // Several open at once, and the POST body must carry them SORTED.
  await both('expand three, in an order that is not sorted', {}, null, (w) => {
    w.click('wireless'); w.click('ipsvc'); w.click('network'); w.flush();
  });

  // ── The two sequence bugs the live comments describe ──────────────────────
  //
  // One click on an auto-expanded category must CLOSE it, and the close must
  // SURVIVE the next render. Both are the same defect seen from two sides.
  for (const cat of CATS) {
    await compare('one click closes the auto-expanded ' + cat, {}, null,
      async (c, w) => { c.__auto(cat); w.click(cat); w.flush(); },
      async (p, w) => { p.navAutoExpand(cat); w.click(cat); w.flush(); });
  }
  await compare('the collapse holds across a later render', {}, null,
    async (c, w) => { c.__auto('network'); w.click('network'); c.__auto('system'); w.flush(); },
    async (p, w) => { p.navAutoExpand('network'); w.click('network'); p.navAutoExpand('system'); w.flush(); });
  await compare('auto-expand, then click a DIFFERENT header', {}, null,
    async (c, w) => { c.__auto('network'); w.click('system'); w.flush(); },
    async (p, w) => { p.navAutoExpand('network'); w.click('system'); w.flush(); });
  // Navigating into a category that is ALREADY saved-open changes nothing, and
  // clicking it then has to clear both.
  await compare('auto-expand a category that is already saved open', {},
    { grouped: true, expanded: ['traffic'] },
    async (c, w) => { c.__auto('traffic'); w.click('traffic'); w.flush(); },
    async (p, w) => { p.navAutoExpand('traffic'); w.click('traffic'); w.flush(); });
  // ── A page in NO category must leave the sidebar exactly as it was ────────
  //
  // Not merely "must not collapse it now": showPage does not even render in that
  // case, so the auto-expanded category has to SURVIVE in state and still be
  // open at the next render, whatever causes it. Checking only the immediate
  // DOM let a version that cleared the category on the way past go unnoticed —
  // the group looked open until the next unrelated repaint closed it.
  await compare('a page outside every category', {}, { grouped: true, expanded: ['ipsvc'] },
    async (c, w) => { c.__auto(undefined); w.flush(); },
    async (p, w) => { p.navAutoExpand(undefined); w.flush(); });
  await compare('navigating out of a category and back to a render', {}, null,
    async (c, w) => { c.__auto('network'); c.__auto(undefined); w.setGrouped(true); w.flush(); },
    async (p, w) => { p.navAutoExpand('network'); p.navAutoExpand(undefined); w.setGrouped(true); w.flush(); });
  await compare('navigating out of a category, then collapsing it by hand', {}, null,
    async (c, w) => { c.__auto('network'); c.__auto(null); w.click('network'); w.flush(); },
    async (p, w) => { p.navAutoExpand('network'); p.navAutoExpand(null); w.click('network'); w.flush(); });

  // ── The grouping switch ───────────────────────────────────────────────────
  await both('turn grouping off', {}, null, (w) => { w.setGrouped(false); w.flush(); });
  await both('off then on again', {}, null,
    (w) => { w.setGrouped(false); w.setGrouped(true); w.flush(); });
  await both('turn grouping off with categories open', {},
    { grouped: true, expanded: ['network', 'system'] },
    (w) => { w.setGrouped(false); w.flush(); });

  // ── The debounce ──────────────────────────────────────────────────────────
  await both('two clicks in quick succession are ONE post', {}, null, (w) => {
    w.click('network'); w.click('system');
    if (w.pendingTimers() !== 1) throw new Error('expected one pending timer, got ' + w.pendingTimers());
    w.flush();
  });
  await both('a save that has not been flushed has posted NOTHING yet', {}, null,
    (w) => { w.click('network'); });
  await both('two flushed rounds are two posts', {}, null, (w) => {
    w.click('network'); w.flush(); w.click('system'); w.flush();
  });

  fs.rmSync(OUT, { force: true });
  if (bad.length) {
    console.error('the nav grouping differs from the live one:\n\n' + bad.slice(0, 3).join('\n\n') +
      (bad.length > 3 ? '\n\n… and ' + (bad.length - 3) + ' more' : '') + '\n');
    process.exit(1);
  }
  console.log(`nav grouping matches the live sidebar (${cases} cases across ${CATS.length} categories)`);
}

main().catch((e) => { fs.rmSync(OUT, { force: true }); console.error(e); process.exit(1); });
