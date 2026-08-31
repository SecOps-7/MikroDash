'use strict';
/**
 * The principals card: its height, and its tab strip.
 *
 * Both are lifted whole from `public/app.js` and RUN against the same shim as
 * the port — the stronger gate, available here because each is a short,
 * self-contained block inside the Settings IIFE with no private helpers behind
 * it.
 *
 * ── THE SIZER IS ARITHMETIC OVER MEASUREMENTS ──────────────────────────────
 *
 * So the shim returns fixed rectangles and a fixed viewport, and what is
 * compared is the height each side computes. The interesting inputs are the ones
 * that are easy to get wrong by reading: a hidden save bar (no reservation), a
 * short viewport (the 320 floor), and a card that is off screen (no write at
 * all, because a hidden element measures as zeros and would be sized to the
 * whole viewport).
 *
 * ── THE TAB STRIP LOOKS LIKE THE SETTINGS ONE AND IS NOT ───────────────────
 *
 * It sets `aria-selected` as well as the class, because that strip is a tablist
 * and a screen reader reads the attribute. A port that copied the settings
 * switcher would look right and be silently inaccessible, so the attribute is
 * compared as well as the class.
 *
 *   node tools/principals-card-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('principals-card-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function lift(startMarker, endMarker, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const from = src.indexOf(startMarker);
  if (from === -1) throw new Error('cannot find ' + name + ' (' + startMarker + ')');
  const to = src.indexOf(endMarker, from);
  if (to === -1) throw new Error(name + ' is never closed with ' + endMarker);
  return src.slice(from, to + endMarker.length);
}

const sizerSrc = G.value('sizerSrc', () => lift('  function _sizePrincipalsCard() {', '\n  }', '_sizePrincipalsCard'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['sizerSrc', sizerSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
// FROZEN — the text is EXECUTED by `new Function` below, so the SOURCE is what
// must survive, not the outputs. A multi-line lifter call: the sixth form.
const tabsSrc = G.value('tabsSrc', () => lift("  document.addEventListener('click', function (e) {\n    var tab = e.target.closest",
                     '\n  });', 'the principal tab strip'));
if (!tabsSrc || tabsSrc.length < 40) throw new Error('the recorded tabsSrc is empty');

// ── the shim ────────────────────────────────────────────────────────────────
function makeNode(id, cls) {
  const classes = new Set((cls || '').split(/\s+/).filter(Boolean));
  const attrs = {};
  const n = {
    id: id || '', style: {}, offsetParent: {}, _classes: classes, _attrs: attrs,
    getAttribute: (k) => (k in attrs ? attrs[k] : null),
    setAttribute: (k, v) => { attrs[k] = String(v); },
    getBoundingClientRect: () => n._rect || { top: 0, height: 0 },
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => { if (on === undefined) { classes.has(c) ? classes.delete(c) : classes.add(c); } else if (on) { classes.add(c); } else { classes.delete(c); } return classes.has(c); },
    },
    closest: (sel) => (classes.has(sel.replace('.', '')) ? n : null),
  };
  return n;
}

const PTABS = ['users', 'groups', 'sites', 'roles'];

function makeDoc(opts) {
  const o = opts || {};
  const card = makeNode('principalsCard');
  card._rect = { top: o.cardTop === undefined ? 200 : o.cardTop, height: 0 };
  if (o.cardHidden) card.style.display = 'none';
  if (o.cardDetached) card.offsetParent = null;

  const actions = makeNode('settingsActions');
  actions._rect = { top: 0, height: o.actionsHeight === undefined ? 48 : o.actionsHeight };
  if (o.actionsHidden) actions.offsetParent = null;

  const tabs = PTABS.map((t) => {
    const n = makeNode('', 'ptab');
    n.setAttribute('data-ptab', t);
    return n;
  });
  const panels = PTABS.map((t) => makeNode('ptab-' + t, 'ptab-panel'));

  const byId = { principalsCard: card, settingsActions: actions };
  for (const p of panels) byId[p.id] = p;

  const listeners = [];
  // Unknown lookups are recorded rather than silently null — see the note in
  // tools/routers-grid-check.js. Verified complete today (the sizer asks only for
  // principalsCard and settingsActions, the tab strip only for .ptab selectors);
  // this keeps it that way as the live code changes.
  const unknown = new Set();
  return {
    card, actions, tabs, panels, listeners, unknown,
    getElementById: (id) => {
      if (!byId[id]) { unknown.add(id); return null; }
      return byId[id];
    },
    querySelectorAll: (sel) => (sel.includes('ptab-panel') ? panels : sel.includes('ptab') ? tabs : []),
    addEventListener: (ev, fn) => { if (ev === 'click') listeners.push(fn); },
    click(tab) { listeners.forEach((fn) => fn({ target: tab, preventDefault() {} })); },
  };
}

function snapshot(doc) {
  return {
    height: doc.card.style.height === undefined ? '' : doc.card.style.height,
    activeTabs: doc.tabs.filter((t) => t.classList.contains('active')).map((t) => t.getAttribute('data-ptab')),
    aria: doc.tabs.map((t) => t.getAttribute('aria-selected')),
    activePanels: doc.panels.filter((p) => p.classList.contains('active')).map((p) => p.id),
  };
}

// ── the port ────────────────────────────────────────────────────────────────
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-principals.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function withGlobals(doc, innerHeight, fn) {
  const pd = global.document, pw = global.window;
  global.document = doc;
  global.window = { innerHeight, addEventListener() {} };
  try { return fn(); } finally {
    if (pd === undefined) delete global.document; else global.document = pd;
    if (pw === undefined) delete global.window; else global.window = pw;
  }
}

const bad = [];
let checks = 0;

// ── the sizer ───────────────────────────────────────────────────────────────
const SIZER_CASES = [
  { name: 'ordinary viewport, save bar shown', opts: { cardTop: 200 }, innerHeight: 900 },
  { name: 'the save bar is hidden (Routers/About)', opts: { cardTop: 200, actionsHidden: true }, innerHeight: 900 },
  { name: 'a short viewport hits the 320 floor', opts: { cardTop: 600 }, innerHeight: 700 },
  { name: 'a taller save bar reserves more', opts: { cardTop: 200, actionsHeight: 120 }, innerHeight: 900 },
  { name: 'the card is display:none', opts: { cardHidden: true }, innerHeight: 900 },
  { name: 'the card has no offsetParent', opts: { cardDetached: true }, innerHeight: 900 },
];
for (const c of SIZER_CASES) {
  const liveDoc = makeDoc(c.opts);
  new Function('document', 'window', sizerSrc + '\n_sizePrincipalsCard();')(
    liveDoc, { innerHeight: c.innerHeight });

  const portDoc = makeDoc(c.opts);
  withGlobals(portDoc, c.innerHeight, () => {
    delete require.cache[require.resolve(OUT)];
    require(OUT).sizePrincipalsCard();
  });

  checks++;
  if (liveDoc.unknown.size) {
    console.error('the live sizer looked up ' + [...liveDoc.unknown].join(', ') +
                  ', which this shim does not provide — it was skipped silently');
    process.exit(1);
  }
  const a = snapshot(liveDoc).height, b = snapshot(portDoc).height;
  if (a !== b) bad.push({ what: 'sizer: ' + c.name, live: a, port: b });
}

// A HIDDEN CARD MUST NOT BE SIZED, and the check must see that the visible cases
// actually wrote something — otherwise "" === "" would pass for the wrong reason.
{
  const d = makeDoc({ cardTop: 200 });
  new Function('document', 'window', sizerSrc + '\n_sizePrincipalsCard();')(d, { innerHeight: 900 });
  if (!d.card.style.height) {
    console.error('the LIVE sizer wrote no height for a visible card — every sizer ' +
                  'case would be comparing two empty strings');
    process.exit(1);
  }
}

// ── the tab strip ───────────────────────────────────────────────────────────
for (let i = 0; i < PTABS.length; i++) {
  const liveDoc = makeDoc({ cardTop: 200 });
  new Function('document', 'window', tabsSrc)(liveDoc, { innerHeight: 900, _sizePrincipalsCard() {} });
  liveDoc.click(liveDoc.tabs[i]);

  const portDoc = makeDoc({ cardTop: 200 });
  withGlobals(portDoc, 900, () => {
    delete require.cache[require.resolve(OUT)];
    require(OUT).mountPrincipalTabs();
    portDoc.click(portDoc.tabs[i]);
  });

  checks++;
  const a = snapshot(liveDoc), b = snapshot(portDoc);
  // The height differs by construction: the live block calls window._sizePrincipalsCard
  // (stubbed above) while the port calls its own. The tab state is what is compared.
  delete a.height; delete b.height;
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    bad.push({ what: 'ptab: ' + PTABS[i], live: JSON.stringify(a), port: JSON.stringify(b) });
  }
  // RE-AIMED AT THE PORT: "the comparison is not exercising the switch" is the
  // worry, and the PORT is the strip that has to keep activating exactly one tab.
  if (b.activeTabs.length !== 1) {
    console.error('the tab strip activated ' + b.activeTabs.length + ' tabs for ' +
                  PTABS[i] + ' — the comparison is not exercising the switch');
    process.exit(1);
  }
}

if (bad.length) {
  for (const d of bad) {
    console.error('\n' + d.what);
    console.error('  live: ' + d.live);
    console.error('  port: ' + d.port);
  }
  process.exit(1);
}
console.log('principals card matches the live one (' + checks + ' checks: ' +
            SIZER_CASES.length + ' sizer, ' + PTABS.length + ' tabs incl. aria-selected)');
