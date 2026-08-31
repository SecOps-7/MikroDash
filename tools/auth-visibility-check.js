'use strict';
/**
 * Who sees the principals card, and in what order its data loads.
 *
 * `_applyAuthModeVisibility` is lifted whole from `public/app.js` and run
 * against the same shim as the port. It is short and self-contained — its only
 * outside reach is `window._caps`, `window._sizePrincipalsCard`, `loadRoles`
 * and `loadUsers`, all injectable.
 *
 * ── THREE RULES, EACH WITH A CONSEQUENCE WORTH NAMING ──────────────────────
 *
 *   fail closed        `window._caps` undefined means NO. The live comment says
 *                      why: "the flash is absence rather than exposure". A port
 *                      treating it as permissive shows the principal graph for
 *                      as long as a slow fetch takes.
 *   move off the tab   the Users tab is hidden outside modern mode, and if it
 *                      was SELECTED the selection moves — an empty tab that
 *                      cannot be populated reads as a bug.
 *   roles first        users and groups render grant rows through a lookup over
 *                      the loaded roles, so loading them out of order shows
 *                      "unknown role" until the next refresh. The ORDER is what
 *                      is compared, not just that both were called.
 *
 *   node tools/auth-visibility-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('auth-visibility-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const from = src.indexOf('  function _applyAuthModeVisibility(mode) {');
if (LIFT.hasReference(ROOT)) if (from === -1) throw new Error('cannot find _applyAuthModeVisibility in public/app.js');
const to = src.indexOf('\n  }', src.indexOf('loadRoles().then', from));
if (LIFT.hasReference(ROOT)) if (to === -1) throw new Error('_applyAuthModeVisibility is never closed');
const fnSrc = G.value('fnSrc', () => src.slice(from, to + '\n  }'.length));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['fnSrc', fnSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
if (!fnSrc.includes('loadRoles')) throw new Error('the lifted function lost its load chain');

function makeNode(id, cls) {
  const classes = new Set((cls || '').split(/\s+/).filter(Boolean));
  const n = {
    id: id || '', style: {}, _clicked: 0,
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => { if (on) classes.add(c); else classes.delete(c); return classes.has(c); },
    },
    click() { n._clicked++; },
    getAttribute: () => null,
    setAttribute() {},
  };
  return n;
}

function makeDoc(usersTabActive) {
  const nodes = {
    authNoneWarn: makeNode('authNoneWarn'),
    modernAuthFields: makeNode('modernAuthFields'),
    'ptabBtn-users': makeNode('ptabBtn-users'),
    'ptab-users': makeNode('ptab-users', usersTabActive ? 'ptab-panel active' : 'ptab-panel'),
    principalsCard: makeNode('principalsCard'),
  };
  const firstTab = makeNode('', 'ptab');
  // Same recorder as the other gates: an id the live function asks for and does
  // not get is skipped in silence, which is how a gap survives a green run.
  const unknown = new Set();
  return {
    nodes, firstTab, unknown,
    getElementById: (id) => {
      if (!nodes[id]) { unknown.add(id); return null; }
      return nodes[id];
    },
    querySelector: () => firstTab,
    querySelectorAll: () => [],
    addEventListener() {},
  };
}

function snapshot(doc, order) {
  const d = (k) => (doc.nodes[k].style.display === undefined ? '<unset>' : doc.nodes[k].style.display);
  return {
    noneWarn: d('authNoneWarn'),
    modernFields: d('modernAuthFields'),
    usersTabBtn: d('ptabBtn-users'),
    card: d('principalsCard'),
    movedOffUsersTab: doc.firstTab._clicked,
    order: order.slice(),
  };
}

// ── the port ────────────────────────────────────────────────────────────────
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-authvis.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

async function runLive(mode, caps, usersTabActive) {
  const doc = makeDoc(usersTabActive);
  const order = [];
  const win = {
    _caps: caps,
    _sizePrincipalsCard() { order.push('size'); },
  };
  // ── ROLES RESOLVES LATE, AND THAT IS THE WHOLE TEST ──────────────────────
  //
  // A first version resolved it immediately, so a chained `loadRoles().then(
  // loadUsers)` and two concurrent calls produced the SAME order and the
  // mutation that drops the chain passed. Recording start and finish separately,
  // with a real gap between them, is what distinguishes:
  //
  //   chained     roles-start, roles-done, users
  //   concurrent  roles-start, users, roles-done   <- "unknown role" on screen
  const loadRoles = () => {
    order.push('roles-start');
    return new Promise((r) => setTimeout(() => { order.push('roles-done'); r(); }, 5));
  };
  const loadUsers = () => { order.push('users'); };
  new Function('document', 'window', 'loadRoles', 'loadUsers', 'setTimeout',
    fnSrc + '\n_applyAuthModeVisibility(arguments[5]);')(
    doc, win, loadRoles, loadUsers, (fn) => fn(), mode);
  await new Promise((r) => setTimeout(r, 30));
  if (doc.unknown.size) {
    console.error('the live visibility function looked up ' + [...doc.unknown].join(', ') +
                  ', which this shim does not provide — it was skipped silently');
    process.exit(1);
  }
  return snapshot(doc, order);
}

async function runPort(mode, caps, usersTabActive) {
  const doc = makeDoc(usersTabActive);
  const order = [];
  const prev = global.document;
  global.document = doc;
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).applyAuthModeVisibility(mode, {
      mayManage: caps ? caps.managePrincipals : undefined,
      loadRoles: () => {
        order.push('roles-start');
        return new Promise((r) => setTimeout(() => { order.push('roles-done'); r(); }, 5));
      },
      loadUsers: () => { order.push('users'); },
      sizeCard: () => { order.push('size'); },
    });
  } finally {
    if (prev === undefined) delete global.document; else global.document = prev;
  }
  await new Promise((r) => setTimeout(r, 30));
  return snapshot(doc, order);
}

const CASES = [
  { name: 'modern + may manage', mode: 'modern', caps: { managePrincipals: true }, active: false },
  { name: 'modern + may NOT manage', mode: 'modern', caps: { managePrincipals: false }, active: false },
  // FAIL CLOSED: the caps fetch has not resolved.
  { name: 'modern + caps unknown', mode: 'modern', caps: undefined, active: false },
  { name: 'none + may manage', mode: 'none', caps: { managePrincipals: true }, active: false },
  { name: 'none + may manage, Users tab was selected', mode: 'none', caps: { managePrincipals: true }, active: true },
  { name: 'modern + may not manage, Users tab was selected', mode: 'modern', caps: { managePrincipals: false }, active: true },
];

(async () => {
  const bad = [];
  for (const c of CASES) {
    const a = await runLive(c.mode, c.caps, c.active);
    const b = await runPort(c.mode, c.caps, c.active);
    if (JSON.stringify(a) !== JSON.stringify(b)) {
      bad.push({ name: c.name, live: JSON.stringify(a), port: JSON.stringify(b) });
    }
  }

  // The load chain must actually have run somewhere, or `order` comparisons are
  // comparing two empty lists.
  const probe = await runLive('modern', { managePrincipals: true }, false);
  if (probe.order.indexOf('roles-start') === -1 || probe.order.indexOf('users') === -1) {
    console.error('the LIVE function did not load roles and users for the permitted ' +
                  'case — the ordering comparison is checking nothing');
    process.exit(1);
  }
  if (probe.order.indexOf('roles-done') > probe.order.indexOf('users')) {
    console.error('the LIVE function loaded users before roles — the premise of this ' +
                  'check is wrong, not the port');
    process.exit(1);
  }

  if (bad.length) {
    for (const d of bad) {
      console.error('\n' + d.name);
      console.error('  live: ' + d.live);
      console.error('  port: ' + d.port);
    }
    process.exit(1);
  }
  console.log('auth-mode visibility matches the live one (' + CASES.length +
              ' cases: fail-closed caps, tab move-off, and roles-before-users)');
})();
