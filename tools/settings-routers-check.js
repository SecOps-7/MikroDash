'use strict';
/**
 * Settings → the routers table: the live renderer and its three actions,
 * against the ported ones.
 *
 * ── WHY THE ACTIONS AND NOT JUST THE ROW ────────────────────────────────────
 *
 * Two of the three DESTROY something. `delete` removes a router and, as its own
 * confirmation says, "all accumulated data (traffic history, ping history,
 * bandwidth, alerts, and connectivity events)". `toggle` disables a router the
 * fleet may depend on. A renderer diff would prove the buttons LOOK right and
 * say nothing about which id they carry, which method they send, or whether the
 * confirmation was asked at all — and the id is the whole payload here.
 *
 * So both sides are driven through the same delegated listener with the same
 * synthetic clicks, and every fetch and every confirm is compared in order.
 *
 * ── THE STATUS IS THREE-VALUED ──────────────────────────────────────────────
 *
 * `undefined` (no status yet) must render as an em dash, not as "Offline". The
 * corpus carries all three, plus a disabled router, because `disabled` overrides
 * the status cell entirely.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/settings-routers-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');
const { freezeCase } = require('./lib/lift.js');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/settings-routers-check.js --freeze
const G = L.golden('settings-routers-check');
const src = L.liveSource(ROOT, path.join('public', 'app.js'));

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

const escSrc = (() => {
  const a = src.indexOf('function esc(');
  const b = src.indexOf('\n}', a);
  return src.slice(a, b + 2);
})();

const liveSrc = [
  escSrc,
  slice('  function _renderRow(r) {', '\n  }', '_renderRow'),
  slice('  function renderTable() {', '\n  }', 'renderTable'),
  // The delegated listener, lifted as its registration so the handler this gate
  // fires is the one a browser would get.
  slice("  if (tbody) {\n    tbody.addEventListener('click', function(e) {", '\n  }\n', 'the tbody listener'),
  // The IN-PLACE badge update.
  //
  // ── LIFTED, NOT TRANSCRIBED, AND THAT CHANGE IS THE POINT ─────────────
  //
  // This was a hand-typed copy of the live statements, on the argument that in
  // the live app they sit inside the `router:status` handler alongside the
  // topbar dots and the dropdown — neither of which this gate is about. The
  // argument was right and the remedy was wrong: on 2026-08-29 upstream stopped
  // painting a DISABLED row (`d7529e0`), and this gate went on comparing the
  // port against a transcription of the old behaviour. It reported the PORT as
  // wrong for matching live.
  //
  // Every other member of this array is a `slice()` anchored on content. This
  // one now is too: the block is lifted whole and wrapped in a function that
  // supplies only what the surrounding handler would have — `data` and
  // `_routers`.
  //
  // THE WRAPPER IS ALSO THE LIMIT. Supplying `_routers` means this harness
  // cannot see a cross-IIFE reference error, the failure that killed both modal
  // buttons in #117; a stub is exactly how a stubbed-away bug survives. This
  // gate compares RENDERED OUTPUT and makes no claim about scope.
  'function updateBadge(routerId, connected) {\n' +
  // `data` is what the handler receives. `_routers` is NOT declared here on
  // purpose: it resolves to the vm context's `_routers`, the same way the live
  // block resolves to the one in its IIFE.
  '  var data = { routerId: routerId, connected: connected };\n' +
  slice('    var _r = _routers.find(', '\n    }\n', 'the router:status badge update') +
  '\n}',
].join('\n');

const OUT = path.join(ROOT, 'testdata', '.srouters-port.cjs');
const ENTRY = path.join(ROOT, 'testdata', '.srouters-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/settings-routers.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── The corpus ──────────────────────────────────────────────────────────────

const FLEET = [
  { id: 'r-active', label: 'Alpha', host: '10.0.0.2', tls: true, model: 'hAP ax3',
    serial: 'HDX0SYNTH01', osVersion: '7.24', siteIds: ['site-1'] },
  // No status yet — the em dash, not "Offline".
  { id: 'r-unknown', label: 'Beta', host: '10.0.0.4', tls: false },
  { id: 'r-off', label: 'Gamma', host: '10.0.0.5', tls: true, tlsInsecure: true,
    model: 'cAP ax', serial: 'HDX0SYNTH02', osVersion: '7.23', siteIds: ['site-1', 'site-2'] },
  // `disabled` overrides the status cell entirely, and dims the row.
  { id: 'r-disabled', label: 'Delta', host: '10.0.0.6', tls: false, disabled: true },
  // The LEGACY single-site field, which must still produce a chip.
  { id: 'r-legacy', label: 'Epsilon', host: '10.0.0.7', tls: true, siteId: 'site-2' },
  // Every field that can be missing, missing — and a label needing escaping.
  { id: 'r-bare', label: 'Ops & <Eng>', host: '10.0.0.8' },
  // A site id no site exists for: the chip must be dropped, not rendered empty.
  { id: 'r-ghostsite', label: 'Zeta', host: '10.0.0.9', tls: true, siteIds: ['site-gone'] },
];
const STATUS = { 'r-active': true, 'r-off': false, 'r-disabled': true };
const SITES = { 'site-1': { name: 'Head Office' }, 'site-2': { name: 'DR & Backup' } };

// ── The fake DOM ────────────────────────────────────────────────────────────

function makeWorld() {
  const ops = [];
  let html = '';
  let listener = null;
  const tbody = {
    _id: 'rtrTbody',
    get innerHTML() { return html; },
    set innerHTML(v) { html = String(v); ops.push(['rtrTbody', 'html', html]); },
    addEventListener(ev, fn) { if (ev === 'click') listener = fn; },
  };
  // The badges, as `[data-rtr-conn="<id>"]` addresses them. Parsed out of the
  // rendered html rather than modelled: the whole point is that the selector
  // finds what the RENDERER wrote, so building the badge list from anything else
  // would let the two drift and still agree.
  const badges = {};
  const doc = {
    getElementById: (id) => (id === 'rtrTbody' ? tbody : null),
    querySelector(sel) {
      const m = /^\[data-rtr-conn="(.*)"\]$/.exec(sel);
      if (!m) throw new Error('unexpected selector: ' + sel);
      const id = m[1];
      // FIRST match only, exactly as document.querySelector returns.
      if (!new RegExp('data-rtr-conn="' + id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '"').test(html)) {
        return null;
      }
      if (!badges[id]) {
        let cls = '', txt = '';
        badges[id] = {
          get className() { return cls; },
          set className(v) { cls = String(v); ops.push(['badge:' + id, 'class', cls]); },
          get textContent() { return txt; },
          set textContent(v) { txt = String(v); ops.push(['badge:' + id, 'text', txt]); },
        };
      }
      return badges[id];
    },
  };
  return {
    doc, ops, tbody,
    click(attrs) {
      if (!listener) throw new Error('nothing is listening on the tbody');
      // A synthetic button carrying the dataset the real markup would have. The
      // `closest` is what the handler calls, so it is what this must answer.
      const btn = { dataset: attrs, closest: (s) => (s === '[data-rtr-action]' ? btn : null) };
      listener({ target: btn });
    },
    clickOutside() {
      // A click on the table that is NOT on an action button must do nothing.
      listener({ target: { closest: () => null } });
    },
    badges,
    state() {
      return {
        html,
        badges: Object.keys(badges).sort().map((k) => [k, badges[k].className, badges[k].textContent]),
      };
    },
  };
}

function fetchFor(reply, log) {
  return (url, init) => {
    log.push(['fetch', url, (init && init.method) || 'GET',
              init && init.body ? JSON.parse(init.body) : null]);
    if (reply === 'reject') return Promise.reject(new Error('network'));
    return Promise.resolve({ json: () => Promise.resolve(reply) });
  };
}

const flush = () => new Promise((r) => setImmediate(r));

async function runLive(sc) {
  const w = makeWorld();
  const log = [];
  const ctx = {
    document: w.doc, Array, JSON, Object, String, encodeURIComponent,
    tbody: w.tbody,
    _routers: sc.fleet, _activeRouterId: sc.activeId, _routerStatus: sc.status,
    window: { _sitesById: sc.sites },
    openModal: (r) => log.push(['openModal', r ? r.id : null]),
    alert: (m) => log.push(['alert', m]),
    confirm: (m) => { log.push(['confirm', m]); return sc.confirm !== false; },
    fetch: fetchFor(sc.reply, log),
    setImmediate,
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc, ctx);
  ctx.renderTable();
  await sc.drive(w, flush, (id, up) => ctx.updateBadge(id, up), () => ctx.renderTable());
  return { log, ops: w.ops, state: w.state() };
}

async function runPort(sc) {
  const w = makeWorld();
  const log = [];
  const prev = {
    document: globalThis.document, fetch: globalThis.fetch,
    confirm: globalThis.confirm, alert: globalThis.alert,
  };
  globalThis.document = w.doc;
  globalThis.fetch = fetchFor(sc.reply, log);
  globalThis.confirm = (m) => { log.push(['confirm', m]); return sc.confirm !== false; };
  globalThis.alert = (m) => log.push(['alert', m]);
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initSettingsRoutersTable({
      routers: () => sc.fleet,
      activeId: () => sc.activeId,
      status: () => sc.status,
      sitesById: () => sc.sites,
      openModal: (r) => log.push(['openModal', r ? r.id : null]),
    });
    await sc.drive(w, flush, (id, up) => require(OUT).updateRouterStatusBadge(id, up),
      () => require(OUT).renderRoutersInto());
  } finally {
    Object.assign(globalThis, prev);
  }
  return { log, ops: w.ops, state: w.state() };
}

const BASE = { fleet: FLEET, activeId: 'r-active', status: STATUS, sites: SITES };

const SCENARIOS = [
  { name: 'the whole fleet renders', ...BASE, async drive() {} },

  // ── RE-ENABLING SHOWS THE REAL STATE, NOT A DASH ────────────────────────
  //
  // The observable that separates "recorded but not painted" from "not recorded
  // at all". While the row is disabled both look identical — the badge says
  // "Disabled" either way. The difference appears on re-enable: a recorded
  // status renders Online/Offline, an unrecorded one renders the em dash
  // `renderRouterRow` uses for `connState === undefined`.
  //
  // Contributed by the live-repo agent, who verified it in a browser on the AC2.
  //
  // EXPRESSED AS TWO RENDERS RATHER THAN A MUTATION SEQUENCE, deliberately. The
  // first attempt disabled the router, delivered a status, re-enabled it in
  // place and re-rendered. It reported the PORT as diverging — and the cause was
  // the harness: ONE scenario object is handed to both runs, so the live run's
  // mutation was still in effect when the port ran. A reset fixed that and the
  // sequence still diverged, at which point the honest read is that a
  // shared-mutable-state scenario is the wrong tool for a render property.
  //
  // AND THIS GATE CANNOT SEE THE RECORDING HALF AT ALL. `routerStatus` is owned
  // by main.ts, which records and then paints; this module only reads what it is
  // handed. So the "still recorded" half is pinned in main.ts by
  // `tools/status-record-audit.js` instead, and claiming it here would be
  // claiming coverage this harness structurally does not have.
  { name: 'a re-enabled router with a recorded status shows it, not a dash',
    ...BASE,
    fleet: FLEET.map((r) => (r.id === 'r-disabled' ? { ...r, disabled: false } : r)),
    status: { 'r-active': true, 'r-off': false, 'r-disabled': true },
    async drive() {} },
  { name: 'an enabled router with NO recorded status shows the dash',
    ...BASE,
    fleet: FLEET.map((r) => (r.id === 'r-disabled' ? { ...r, disabled: false } : r)),
    status: {},
    async drive() {} },

  { name: 'an empty fleet shows the empty state', ...BASE, fleet: [], async drive() {} },
  { name: 'no active router at all', ...BASE, activeId: '', async drive() {} },
  { name: 'no statuses have arrived yet', ...BASE, status: {}, async drive() {} },
  { name: 'no sites are known, so no chips', ...BASE, sites: {}, async drive() {} },
  {
    name: 'Edit opens the modal with that router',
    ...BASE,
    async drive(w) { w.click({ rtrAction: 'edit', rtrId: 'r-off' }); },
  },
  {
    name: 'Edit on an id the fleet no longer has does nothing',
    ...BASE,
    async drive(w) { w.click({ rtrAction: 'edit', rtrId: 'r-vanished' }); },
  },
  {
    name: 'a click that is not on an action button does nothing',
    ...BASE,
    async drive(w) { w.clickOutside(); },
  },
  {
    name: 'Disable sends disabled:true',
    ...BASE, reply: { ok: true },
    async drive(w, f) { w.click({ rtrAction: 'toggle', rtrId: 'r-off' }); await f(); await f(); },
  },
  {
    name: 'Enable sends disabled:false — the value is read, not carried',
    ...BASE, reply: { ok: true },
    async drive(w, f) { w.click({ rtrAction: 'toggle', rtrId: 'r-disabled' }); await f(); await f(); },
  },
  {
    name: 'a refused toggle alerts with the server message',
    ...BASE, reply: { ok: false, error: 'Router not permitted' },
    async drive(w, f) { w.click({ rtrAction: 'toggle', rtrId: 'r-off' }); await f(); await f(); },
  },
  {
    name: 'a refused toggle with NO message falls back',
    ...BASE, reply: { ok: false },
    async drive(w, f) { w.click({ rtrAction: 'toggle', rtrId: 'r-off' }); await f(); await f(); },
  },
  {
    name: 'a toggle that fails outright alerts Network error',
    ...BASE, reply: 'reject',
    async drive(w, f) { w.click({ rtrAction: 'toggle', rtrId: 'r-off' }); await f(); await f(); },
  },
  {
    name: 'Delete asks first, naming the router',
    ...BASE, reply: { ok: true },
    async drive(w, f) {
      w.click({ rtrAction: 'delete', rtrId: 'r-off', rtrLabel: 'Gamma' });
      await f(); await f();
    },
  },
  {
    name: 'a DECLINED delete sends nothing at all',
    ...BASE, reply: { ok: true }, confirm: false,
    async drive(w, f) {
      w.click({ rtrAction: 'delete', rtrId: 'r-off', rtrLabel: 'Gamma' });
      await f(); await f();
    },
  },
  {
    name: 'delete with no label falls back to the id in the prompt',
    ...BASE, reply: { ok: true },
    async drive(w, f) { w.click({ rtrAction: 'delete', rtrId: 'r-off' }); await f(); await f(); },
  },
  {
    name: 'a refused delete alerts with the reason',
    ...BASE, reply: { ok: false, error: 'in use' },
    async drive(w, f) {
      w.click({ rtrAction: 'delete', rtrId: 'r-off', rtrLabel: 'Gamma' });
      await f(); await f();
    },
  },
  {
    name: 'a delete that fails outright alerts Request failed',
    ...BASE, reply: 'reject',
    async drive(w, f) {
      w.click({ rtrAction: 'delete', rtrId: 'r-off', rtrLabel: 'Gamma' });
      await f(); await f();
    },
  },
  {
    name: 'a status event repaints that one badge in place',
    ...BASE,
    async drive(w, f, status) { status('r-off', true); },
  },
  {
    name: 'a status event for a router NOT in the table changes nothing',
    ...BASE,
    async drive(w, f, status) { status('r-nowhere', false); },
  },
  {
    name: 'going offline repaints the badge the other way',
    ...BASE,
    async drive(w, f, status) { status('r-active', false); },
  },
  {
    name: 'a status event OVERWRITES a Disabled badge — the live quirk',
    ...BASE,
    async drive(w, f, status) { status('r-disabled', false); },
  },
  {
    name: 'an id needing URL encoding',
    ...BASE, reply: { ok: true },
    fleet: [{ id: 'a/b c&d', label: 'Odd', host: '10.0.0.3' }],
    async drive(w, f) { w.click({ rtrAction: 'toggle', rtrId: 'a/b c&d' }); await f(); await f(); },
  },
];

(async () => {
  let bad = 0, total = 0;
  for (const sc of SCENARIOS) {
    // ── THE SCENARIO IS FROZEN, AND THAT IS NOT TIDINESS ──────────────────
    //
    // ONE scenario object is handed to BOTH runs. A `drive()` that mutates it —
    // flipping `disabled` to model an operator re-enabling a router, say —
    // leaves the live run's mutation in place when the port runs, and the gate
    // then reports the PORT as diverging. That happened on 2026-08-29 and cost
    // a debugging cycle before the harness was suspected rather than the code.
    //
    // A FALSE ACCUSATION IS WORSE THAN A MISSED BUG: it points at correct code
    // and invites somebody to "fix" it. So the mistake is made impossible rather
    // than documented — a mutating drive now throws inside the run that did it,
    // naming the scenario, instead of corrupting the next one silently.
    //
    // One definition, in lift.js, rather than a copy per gate — respelling a
    // rule at each site is how upstream `2af8164` came to fix one of four.
    freezeCase(sc);
    const live = await G.live(G.seq(), () => runLive(sc));
    const port = await runPort(sc);
    total += live.ops.length;
    if (live.ops.length === 0) {
      console.log('  UNDRIVEN     ' + sc.name + ' — the LIVE side rendered nothing');
      bad++;
      continue;
    }
    const a = JSON.stringify({ log: live.log, ops: live.ops, state: live.state }, null, 1);
    const b = JSON.stringify({ log: port.log, ops: port.ops, state: port.state }, null, 1);
    if (a === b) { console.log('  ok  ' + sc.name); continue; }
    bad++;
    console.log('  DIFF  ' + sc.name);
    const al = a.split('\n'), bl = b.split('\n');
    for (let k = 0, shown = 0; k < Math.max(al.length, bl.length) && shown < 10; k++) {
      if (al[k] !== bl[k]) {
        console.log('        live: ' + (al[k] === undefined ? '(end)' : al[k].trim().slice(0, 220)));
        console.log('        port: ' + (bl[k] === undefined ? '(end)' : bl[k].trim().slice(0, 220)));
        shown++;
      }
    }
  }
  fs.rmSync(OUT, { force: true });
  console.log('\n' + SCENARIOS.length + ' scenarios, ' + total + ' live renders compared');
  if (bad) { console.log(bad + ' FAILED'); process.exit(1); }
  console.log('all agree');
})();
