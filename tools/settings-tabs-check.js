'use strict';
/**
 * Does the port's Settings tab switcher do what the live one does?
 *
 * ── THIS ONE IS A REAL LIFT, UNLIKE populate() ─────────────────────────────
 *
 * `populate()` lives in an 1,850-line IIFE and had to be compared one level down
 * (see settings-populate-check.js). The tab switcher is its own 43-line IIFE
 * with three dependencies — `$`, `window._sizePrincipalsCard` and `fetch` — so
 * it can be lifted whole and RUN, which is the stronger gate and the one this
 * repo prefers everywhere it is available.
 *
 * The shim carries a working `classList`, because the whole behaviour is class
 * toggling. It is a real implementation of toggle/add/remove/contains rather
 * than a stub: a stubbed one would let both sides "agree" by doing nothing.
 *
 * WHAT IS COMPARED: which tab button and which panel carry `active`, whether the
 * actions bar is shown, the ORDER of the sizer call against that bar, and
 * whether /healthz is fetched more than once.
 *
 *   node tools/settings-tabs-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('settings-tabs-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// ── lift the live IIFE ──────────────────────────────────────────────────────
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const from = src.indexOf('// ── Settings tab switcher');
if (LIFT.hasReference(ROOT)) if (from === -1) throw new Error('cannot find the Settings tab switcher banner in public/app.js');
const close = src.indexOf('\n})();', from);
if (LIFT.hasReference(ROOT)) if (close === -1) throw new Error('the Settings tab switcher IIFE is never closed');
// FROZEN AS ONE VALUE — the program `new Function` EXECUTES is the text AFTER
// the replace, so the pair of steps is frozen together rather than the slice
// alone. Freezing only the first step would record text the gate never runs.
const iife = G.value('iife', () => {
  let t = src.slice(from, close + '\n})();'.length);
  t = t.replace(/\n\}\)\(\);$/, '\n  window.__activateTab = activateTab;\n})();');
  return t;
});
if (!iife || iife.length < 100) throw new Error('the recorded iife is empty');
if (!iife.includes('window.__activateTab')) throw new Error('could not publish activateTab');

// ── the shim ────────────────────────────────────────────────────────────────
//
// Nodes carry a REAL classList. A stub would let both implementations agree by
// doing nothing at all, which is the failure this whole file exists to avoid.
function makeNode(id, cls, dataset) {
  const classes = new Set((cls || '').split(/\s+/).filter(Boolean));
  // WRITES ARE RECORDED, so a node the live side touches and the snapshot does
  // not inspect becomes a named failure. That is how three slider labels hid
  // from the populate gate: the comparison was narrower than the behaviour, and
  // its green line did not say so.
  let text = '';
  const n = {
    id: id || '',
    dataset: dataset || {},
    style: {},
    __written: false,
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => { n.__written = true; if (on === undefined) { classes.has(c) ? classes.delete(c) : classes.add(c); } else if (on) { classes.add(c); } else { classes.delete(c); } return classes.has(c); },
    },
    _classes: classes,
    addEventListener() {},
  };
  Object.defineProperty(n, 'textContent', {
    get: () => text,
    set: (v) => { text = String(v); n.__written = true; },
    enumerable: true,
  });
  // `style.display = x` is the other write these functions make.
  n.style = new Proxy({}, {
    set: (t, k, v) => { t[k] = v; n.__written = true; return true; },
  });
  return n;
}

const TABS = ['routers', 'general', 'notifications', 'appearance', 'auth', 'about'];

// ── A SYNCHRONOUSLY-RESOLVING THENABLE ────────────────────────────────────
//
// The About tab sets its version inside `fetch(...).then(r => r.json()).then(d
// => …)`. With a real promise that callback runs on a later microtask, and these
// runs are synchronous — so the version node was still empty when the snapshot
// was taken, and the comparison was two empty strings on both sides. Two
// mutations (dropping the 'v' prefix, writing to the wrong element) passed
// because of it.
//
// This resolves inline, flattening a returned thenable the way a real promise
// chain does, so the whole chain completes before the snapshot.
function syncThen(value) {
  return {
    then(f) {
      if (typeof f !== 'function') return syncThen(value);
      const out = f(value);
      return out && typeof out.then === 'function' ? out : syncThen(out);
    },
    catch() { return syncThen(value); },
  };
}
// A /healthz WITHOUT a version is a real state — an older build, or a partial
// response — and the original guards on `d.version` before writing. Without a
// case for it, a port that wrote "vundefined" would pass.
function fakeFetch(count, payload) {
  return () => { count.n++; return syncThen({ json: () => syncThen(payload) }); };
}

function makeDoc() {
  const buttons = TABS.map((t) => makeNode('', 'stab', { tab: t }));
  const panels = TABS.map((t) => makeNode('stab-' + t, 'stab-panel', {}));
  const actions = makeNode('settingsActions', '', {});
  const version = makeNode('stabAboutVersion', '', {});
  const byId = { settingsActions: actions, stabAboutVersion: version };
  for (const p of panels) byId[p.id] = p;
  return {
    buttons, panels, actions, version,
    getElementById: (id) => byId[id] || null,
    querySelectorAll: (sel) => (sel.includes('.stab-panel') ? panels : sel.includes('.stab') ? buttons : []),
    addEventListener() {},
  };
}

function snapshot(doc, order) {
  return {
    activeTabs: doc.buttons.filter((b) => b.classList.contains('active')).map((b) => b.dataset.tab),
    activePanels: doc.panels.filter((p) => p.classList.contains('active')).map((p) => p.id),
    actionsDisplay: doc.actions.style.display === undefined ? '' : doc.actions.style.display,
    // THE ABOUT TAB WRITES THIS and the snapshot did not look at it — the node
    // was created by the shim and then ignored, so a port that set the wrong id,
    // dropped the 'v' prefix, or never set it at all would have passed.
    aboutVersion: doc.version.textContent,
    order: order.slice(),
  };
}

/** A stable name for a node, since the tab buttons carry no id. */
function nameOf(n) {
  return n.id || ('.stab[' + (n.dataset && n.dataset.tab) + ']');
}

/** Every node the run WROTE. */
function writtenIds(doc) {
  return [...doc.buttons, ...doc.panels, doc.actions, doc.version]
    .filter((n) => n.__written).map(nameOf);
}

/** Every node snapshot() actually reads. Kept beside it so the two drift together. */
function coveredIds(doc) {
  return [...doc.buttons, ...doc.panels, doc.actions, doc.version].map(nameOf);
}

// ── run the live one ────────────────────────────────────────────────────────
function runLive(tab, fetchCount, times, payload) {
  const doc = makeDoc();
  const order = [];
  const win = {
    _sizePrincipalsCard() { order.push('sizer:' + doc.actions.style.display); },
  };
  const fetchFn = fakeFetch(fetchCount, payload || { version: '9.9.9' });
  new Function('window', 'document', 'fetch', '$', 'times',
    iife + '\nfor (var i = 0; i < arguments[5]; i++) window.__activateTab(arguments[4]);')(
    win, doc, fetchFn, (id) => doc.getElementById(id), tab, times || 1);
  const snap = snapshot(doc, order);
  // Carried alongside so the blind-spot assertion can see what was written
  // without the caller reaching into the shim.
  Object.defineProperty(snap, '__written', { value: writtenIds(doc), enumerable: false });
  Object.defineProperty(snap, '__ids', { value: coveredIds(doc), enumerable: false });
  return snap;
}

// ── run the port ────────────────────────────────────────────────────────────
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-settings-tabs.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function runPort(tab, fetchCount, times, payload) {
  const doc = makeDoc();
  const order = [];
  const prevDoc = global.document, prevFetch = global.fetch, prevSizer = globalThis._sizePrincipalsCard;
  global.document = doc;
  global.fetch = fakeFetch(fetchCount, payload || { version: '9.9.9' });
  globalThis._sizePrincipalsCard = () => { order.push('sizer:' + doc.actions.style.display); };
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    for (let i = 0; i < (times || 1); i++) mod.activateSettingsTab(tab);
  } finally {
    if (prevDoc === undefined) delete global.document; else global.document = prevDoc;
    if (prevFetch === undefined) delete global.fetch; else global.fetch = prevFetch;
    if (prevSizer === undefined) delete globalThis._sizePrincipalsCard; else globalThis._sizePrincipalsCard = prevSizer;
  }
  return snapshot(doc, order);
}

const bad = [];
let checks = 0;
// The About tab is visited TWICE in one run as well as once: the /healthz fetch
// happens ONCE per page lifetime, and a single visit cannot tell a working latch
// from a missing one — both fetch exactly once.
const RUNS = [...TABS, 'nosuchtab'].map((t) => ({ tab: t, times: 1 }))
  .concat([
    { tab: 'about', times: 3 },
    // /healthz answering without a version — the guard's other branch.
    { tab: 'about', times: 1, payload: {}, noVersion: true },
  ]);

for (const run of RUNS) {
  const tab = run.tab;
  const lc = { n: 0 }, pc = { n: 0 };
  const a = runLive(tab, lc, run.times, run.payload),
        b = runPort(tab, pc, run.times, run.payload);
  checks++;
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    bad.push({ tab, live: JSON.stringify(a), port: JSON.stringify(b) });
  }
  // THE FETCH COUNT WAS PASSED AND NEVER COMPARED. The About tab fetches
  // /healthz ONCE per page lifetime — a port that dropped the latch would hit it
  // on every visit, and this gate collected the evidence and threw it away.
  if (lc.n !== pc.n) {
    bad.push({ tab: tab + ' (fetches)', live: String(lc.n), port: String(pc.n) });
  }
  // ── NO BLIND SPOTS: every node the LIVE run wrote must be one the snapshot
  //    inspects ──
  //
  // The snapshot reads a fixed set of nodes, so a node the switcher STARTS
  // writing would be invisible to it — which is exactly how the About version
  // node sat here, created by the shim and never compared.
  for (const id of a.__written) {
    if (a.__ids.indexOf(id) === -1) {
      console.error('the live switcher wrote ' + id + ', which snapshot() does not ' +
                    'inspect — widen it, or this comparison is narrower than it looks');
      process.exit(1);
    }
  }

  // THE ABOUT RUN MUST ACTUALLY SET THE VERSION. Without a synchronously
  // resolving fetch this field was '' on both sides and compared equal — so it
  // is asserted rather than assumed.
  if (tab === 'about' && !run.noVersion && !a.aboutVersion) {
    console.error('the live About tab left the version empty — the fetch chain is not ' +
                  'completing, and aboutVersion is comparing two empty strings');
    process.exit(1);
  }

  // THE SIZER MUST HAVE RUN, or the order check is comparing two empty lists.
  if (a.order.length === 0) {
    console.error('the LIVE switcher never called the sizer for tab ' + tab +
                  ' — the ordering check is comparing nothing');
    process.exit(1);
  }
}

if (bad.length) {
  for (const d of bad) {
    console.error('\ntab ' + d.tab);
    console.error('  live: ' + d.live);
    console.error('  port: ' + d.port);
  }
  process.exit(1);
}
console.log('settings tabs match the live switcher (' + checks + ' tabs, ' +
            'active classes, actions bar, and sizer ordering)');
