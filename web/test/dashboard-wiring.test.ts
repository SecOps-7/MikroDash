// Moved from `tools/dashboard-wiring-check.js` when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * Is the Dashboard actually LISTENING?
 *
 * ── THE GAP THIS FILLS ──────────────────────────────────────────────────────
 *
 * Four card renderers were ported, each with a gate that drives it directly and
 * compares the DOM against the live renderer. All four passed while NOTHING
 * CALLED ANY OF THEM. A renderer that is never invoked still renders correctly
 * when a test invokes it, so a DOM gate cannot tell a wired page from an unwired
 * one — and `event-audit.js` did not catch it either, because the events were
 * emitted by the Go side and consumed by no one: a gap in the CONSUMER, not in
 * the vocabulary.
 *
 * ── IT ASKS THE PORT, IT DOES NOT GREP IT ───────────────────────────────────
 *
 * The port half RUNS `initDashboard` against a fake socket and records what it
 * subscribes. Grepping for `socket.on('x'` would be a guess about spelling, and
 * this session has lost time to nine such guesses.
 *
 * ── AND THE CARD TABLE IS FORCED TO GROW ────────────────────────────────────
 *
 * An earlier version tried to DERIVE which live events feed the Dashboard, by
 * looking for handlers that touch a Dashboard element id. It was hopeless in the
 * other direction: with 122 ids on the page and one level of call-following, it
 * claimed `disconnect`, `settings:pages` and `ros:status` as card events. A
 * check whose expected set is that noisy teaches people to ignore it.
 *
 * So the table is EXPLICIT — and kept honest by discovery instead: every
 * `dashboard-*.ts` module in the port must appear in it, so porting a new card
 * fails this gate until its event is named and wired. That is the direction the
 * failure actually comes from.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/dashboard-wiring-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// The block that compared this against the deleted implementation was removed
// when the port-parity harness was retired. It had been dead since cutover --
// `LIFT.hasReference` has answered false ever since -- so removing it changes
// nothing that ran. Everything below drives the PORT and asserts what it does.

// module basename → the event the LIVE app delivers that card on.
// `gauge` is deliberately absent and listed as a helper below: it renders no
// card and subscribes to nothing.
// A card's entry is the event it renders from, or an object when it needs more:
//   also    — further events the same module owns, allowed but not probed
//   prelude — events that must be delivered FIRST for the probe to render
const CARDS = {
  'dashboard-talkers': 'talkers:update',
  'dashboard-netwatch': 'netwatch:update',
  'dashboard-vpn': 'vpn:update',
  'dashboard-system': 'system:update',
  'dashboard-conn': 'conn:update',
  'dashboard-card-physports': 'ifstatus:update',
  'dashboard-networks': 'lan:overview',
  // The chart IGNORES a sample for an interface it is not showing — `currentIf`
  // is empty until `traffic:history` names one, and that guard is the whole
  // reason a viewer switching interfaces does not get two streams interleaved.
  // So the probe delivers history first; without the prelude the gate would
  // report a swallowed payload for behaviour that is correct.
  'dashboard-ping': { event: 'ping:update', also: ['ping:history'] },
  'dashboard-card-wireless': 'wireless:update',
  // Shares `lan:overview` with dashboard-networks; the probe below drives the
  // event once and both must write.
  'dashboard-card-iputil': 'lan:overview',
  'dashboard-card-routing': 'routing:update',
  'dashboard-card-fwactions': 'firewall:update',
  'dashboard-card-diagnostics': 'diagnostics:update',
  'dashboard-card-connlists': 'conn:update',
  'dashboard-card-logs': { event: 'logs:new', also: ['logs:history'] },
  // Shares traffic:update with the chart, and owns two more events of its own.
  'dashboard-card-bandwidth': {
    event: 'traffic:update', also: ['routers:update', 'router:active'],
    prelude: ['traffic:history'],
  },
  'dashboard-traffic': {
    event: 'traffic:update',
    also: ['traffic:history'],
    prelude: ['traffic:history'],
  },
};
// Subscriptions that are NOT a card's payload. Each belongs to a module named in
// HELPERS and needs a reason, because "not a card" is otherwise indistinguishable
// from "nobody remembered to add it to the table".
const EXTRA_EVENTS = {
  'stream:health': 'the per-card stream-degradation warning (dashboard-stream-health.ts). It ' +
    'tints a card and writes an element whose id is BUILT from the collector name, which is why ' +
    'neither shows up in a search for its literal id',
  'wan:status': 'the WAN badge (dashboard-stream-health.ts) — chrome on the Dashboard rather ' +
    'than one of the grid\'s cards',
};

const cardEvent = (v) => (typeof v === 'string' ? v : v.event);
const cardEvents = (v) => (typeof v === 'string' ? [v] : [v.event, ...(v.also || [])]);
const cardPrelude = (v) => (typeof v === 'string' ? [] : (v.prelude || []));
// Modules under pages/ that are not cards. Named, so a new one cannot be waved
// through as "probably a helper".
const HELPERS = new Set([
  'dashboard-gauge',
  'dashboard',
  // Pure buffer and clock arithmetic for the traffic chart, with no DOM and no
  // Chart.js. It is a helper TODAY and must not stay one: the card that draws
  // from it is `dashboard-traffic`, which will need a CARDS entry. Listing this
  // here does not excuse that one.
  'dashboard-traffic-buffer',
  // Pure grid arithmetic — overlap, bounds, free slots, cell/pixel conversion.
  // A helper TODAY, for the same reason and with the same caveat: the module
  // that WIRES the grid (drag, resize, the add panel, room bookkeeping) is not
  // written yet, and it does not subscribe to a card event — it is the page's
  // layout, not a card. When it lands it belongs in neither list without a
  // note explaining which.
  'dashboard-grid-layout',
  // Persistence, DOM application and room bookkeeping for the grid. Same
  // caveat as the layout arithmetic: a helper until the module that WIRES the
  // grid lands.
  'dashboard-grid-store',
  // Edit mode and the Add panel. Same caveat again: a helper until the module
  // that wires the grid lands.
  'dashboard-grid-edit',
  // Dragging a card. Same caveat: a helper until the grid's wiring module lands.
  'dashboard-grid-drag',
  // Resizing a card. Same caveat: a helper until the grid's wiring module lands.
  'dashboard-grid-resize',
  // The grid's own wiring module. Not a CARD: it subscribes to no socket event
  // — the grid is the page's LAYOUT, and its inputs are pointer events, a
  // MutationObserver and a ResizeObserver. `tools/grid-wiring-check.js` is what
  // holds it to account, the way this file does for the cards.
  'dashboard-grid',
  // Shared helpers for the fourteen EXTRA cards — escaping, flags, rate
  // splitting and the IP Utilisation arc. Pure; the cards that call them are the
  // next slices and each will need a CARDS entry of its own.
  'dashboard-cards-util',
  // The Connections Map's arc geometry and highlight rule. Pure; the card that
  // builds the SVG around them is the next slice and will need a CARDS entry.
  'dashboard-map-geometry',
  // The Connections Map card. NOT in CARDS: it does not subscribe an event of
  // its own — `dashboard.ts` feeds it from the `conn:update` handler alongside
  // the two list cards, and it initialises off a `worldmap:ready` DOM event.
  'dashboard-card-map',
  // The Connection Flow card: a wrapper around the connections page's sankey
  // renderer, fed from the same `conn:update` handler as the other three.
  'dashboard-card-connflow',
  // The stream-degradation warnings and the WAN badge: two small renderers on
  // two events of their own, neither of which is a CARD in the grid's sense.
  'dashboard-stream-health',
]);

// ── discovery: every dashboard module must be accounted for ────────────────
const modules = fs.readdirSync(path.join(ROOT, 'web', 'src', 'pages'))
  .filter((f) => /^dashboard.*\.ts$/.test(f))
  .map((f) => f.replace(/\.ts$/, ''));
assert.ok(modules.length >= 5, 'only ' + modules.length + ' dashboard modules found');

const problems = [];
for (const m of modules) {
  if (!CARDS[m] && !HELPERS.has(m)) {
    problems.push(m + '.ts exists and is neither a card in CARDS nor named in HELPERS.\n' +
      '      A new card must be wired AND named here, or it is a renderer nothing calls.');
  }
}

// ── the live app must still deliver each card on that event ────────────────
//
// GUARDED: this asks the live SOURCE a question — does it still register a
// handler for this event — and exists to catch the CARD TABLE going stale
// against an upstream that moved. With no upstream there is nothing to drift
// from. Every other check here drives the port and runs unconditionally.


// ── what the port subscribes, by running it ────────────────────────────────
const ENTRY = path.join(ROOT, 'testdata', '.dashwiring-entry.ts');
fs.writeFileSync(ENTRY, "export { initDashboard } from '../web/src/pages/dashboard.js';\n");
const OUT = path.join(ROOT, 'testdata', '.dashwiring-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const subscribed = new Map();
const docEvents = [];
const touched = new Set();
function fakeEl(id) {
  return {
    id, className: '', style: {}, children: [],
    appendChild(c) { this.children.push(c); return c; },
    // BOTH kinds of write count. Counting only innerHTML reported the traffic
    // chart as swallowing its payload: it renders the live RX/TX figures with
    // textContent, and its canvas through Chart.js, so it never sets innerHTML
    // at all. Any text-only card would have been misreported the same way.
    set innerHTML(v) { touched.add(id); this._h = v; },
    get innerHTML() { return this._h || ''; },
    set textContent(v) { touched.add(id); this._t = String(v); },
    get textContent() { return this._t === undefined ? '' : this._t; },
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener() {}, removeEventListener() {}, closest: () => null,
    getAttribute: () => null, setAttribute() {}, remove() {},
  };
}
const els = new Map();
globalThis.document = {
  hidden: false,
  getElementById: (id) => { if (!els.has(id)) els.set(id, fakeEl(id)); return els.get(id); },
  createElement: () => fakeEl(''),
  addEventListener: (type, fn) => docEvents.push({ type, fn }),
  dispatchEvent: () => true,
  querySelectorAll: () => [], querySelector: () => null,
};
const winEvents = [];
globalThis.window = {
  addEventListener: (type, fn) => winEvents.push({ type, fn }),
  devicePixelRatio: 1,
};
// QUEUED, not run inline. The traffic chart's keepalive re-books itself every
// frame, so a stub that executed callbacks synchronously recursed until the
// stack blew — which looked like the handler throwing. Frames are drained a
// bounded number of rounds instead: enough for the cards that defer their
// render by one frame, finite for the loop that never ends on its own.
const frameQueue = [];
globalThis.requestAnimationFrame = (fn) => { frameQueue.push(fn); return frameQueue.length; };
function drainFrames(rounds = 3) {
  for (let i = 0; i < rounds && frameQueue.length; i++) {
    for (const fn of frameQueue.splice(0)) fn();
  }
  frameQueue.length = 0;
}
globalThis.CustomEvent = function (type, init) { return { type, detail: init && init.detail }; };
const { initDashboard } = require(OUT);
const emitted = [];
initDashboard({ on: (e, cb) => { subscribed.set(e, cb); }, emit: (e, d) => emitted.push([e, d]) });
fs.rmSync(OUT, { force: true });

// ── every card's event is subscribed, AND its handler reaches the DOM ──────
//
// Subscribing is not enough: a handler that swallows its payload would satisfy
// a subscription check and render nothing. Each one is CALLED with a minimal
// payload and must write to at least one element.
const PROBE = {
  'talkers:update': { devices: [], talkers: [] },
  'netwatch:update': { hosts: [] },
  'vpn:update': { tunnels: [] },
  'system:update': { uptimeRaw: '1d00:00:00', cpuLoad: 1, memPct: 1, totalHdd: 0, hddPct: 0 },
  'lan:overview': { internetIfaces: [], networks: [] },
  // The chart ignores a sample for an interface it is not showing, so the probe
  // must name the one `traffic:history` selected — see the note below.
  'traffic:update': { ifName: '__probe__', ts: 1, rx_mbps: 1, tx_mbps: 1 },
  'diagnostics:update': { total: 3, collectors: [{ name: 'dns', streams: 1 }] },
  'firewall:update': { filter: [{ action: 'accept' }], nat: [], mangle: [], raw: [] },
  'logs:new': { time: '10:00:00', topics: 'system', message: 'probe', severity: 'info' },
  'routers:update': [{ id: '__probe__', bwDownMbps: 100, bwUpMbps: 50 }],
  'router:active': { activeId: '__probe__' },
  'routing:update': { routeCounts: { connect: 1, static: 2, total: 3 }, summary: { total: 1 } },
  'wireless:update': { clients: [{ signal: '-58', band: '5GHz' }] },
  'ping:update': { target: '1.1.1.1', rtt: 12, loss: 0, minRtt: 9, maxRtt: 20, ts: 1 },
  'traffic:history': { ifName: '__probe__', points: [{ ts: 1, rx_mbps: 1, tx_mbps: 1 }] },
  'conn:update': { ts: 1, total: 0, protoCounts: { tcp: 0, udp: 0, icmp: 0, other: 0 }, topSources: [], topDestinations: [] },
  // NOT `{}` and not undefined: the live handler dereferences `data` unguarded,
  // and an EMPTY interface list takes the early return before the card writes
  // anything — which this gate would then report as a handler that touched no
  // element. One real port is the smallest payload that exercises the card.
  'ifstatus:update': { interfaces: [{ name: 'ether1', type: 'ether', running: true, disabled: false, ips: [] }] },
};
for (const [m, entry] of Object.entries(CARDS)) {
  const event = cardEvent(entry);
  const cb = subscribed.get(event);
  if (!cb) {
    problems.push('nothing subscribes ' + event + ' — ' + m + ' is a renderer nothing calls');
    continue;
  }
  touched.clear();
  try {
    for (const pre of cardPrelude(entry)) {
      const preCb = subscribed.get(pre);
      if (!preCb) { problems.push('nothing subscribes ' + pre + ', the prelude for ' + event); continue; }
      preCb(PROBE[pre]);
      drainFrames();
    }
    touched.clear(); // the prelude's own writes do not count as the probe's
    cb(PROBE[event]);
    drainFrames();
  } catch (e) {
    problems.push(event + ' handler threw on a minimal payload: ' + e.message);
    continue;
  }
  if (!touched.size) {
    problems.push(event + ' is subscribed but its handler wrote to no element — ' +
      'the payload is being swallowed');
  }
}

for (const event of subscribed.keys()) {
  if (event === 'connect') continue; // lifecycle, asserted separately below
  if (EXTRA_EVENTS[event]) continue;
  if (!Object.values(CARDS).flatMap(cardEvents).includes(event)) {
    problems.push('the port subscribes ' + event + ', which is not in the card table');
  }
}

// ── the grid's room events reach the SOCKET ────────────────────────────────
//
// The grid dispatches `dashcard:room:focus`/`blur` on the document and something
// must turn those into `dashcard:focus`/`blur` on the wire. Checked by
// DISPATCHING, not by matching source: two mutations that removed the relay
// survived a regex over the TypeScript, because the strings it looked for were
// still present in code nothing calls.
{
  for (const [dispatched, expected] of [
    ['dashcard:room:focus', 'dashcard:focus'],
    ['dashcard:room:blur', 'dashcard:blur'],
  ]) {
    emitted.length = 0;
    const handler = docEvents.find((e) => e.type === dispatched);
    if (!handler) {
      problems.push('nothing listens for ' + dispatched + ' — every room the grid computes ' +
        'reaches nobody');
      continue;
    }
    handler.fn({ type: dispatched, detail: 'firewall' });
    const got = emitted.find((x) => x[0] === expected);
    if (!got) {
      problems.push(dispatched + ' did not reach the socket as ' + expected +
        ' (emitted: ' + JSON.stringify(emitted) + ')');
    } else if (got[1] !== 'firewall') {
      problems.push(expected + ' carried ' + JSON.stringify(got[1]) + ', not the room name');
    }
    // A non-string detail must be ignored rather than emitted: the room name
    // becomes part of a room key on the server.
    emitted.length = 0;
    handler.fn({ type: dispatched, detail: { not: 'a string' } });
    if (emitted.length) {
      problems.push(dispatched + ' relayed a non-string detail: ' + JSON.stringify(emitted));
    }
  }
}

// ── the System card's two non-payload signals ──────────────────────────────
if (!subscribed.has('connect')) {
  problems.push('nothing re-arms the System card meta line on connect — a reconnect to a ' +
    'different board would keep the old board name under the new gauges');
}
// The chart freezes on BLUR as well as on visibilitychange. Dropping behind
// another application does not reliably fire visibilitychange, and without the
// blur binding the keepalive keeps advancing an axis nobody can see — then the
// catch-up it exists to hide happens in full view on return.
if (!winEvents.some((e) => e.type === 'blur')) {
  problems.push('no window blur handler — the traffic chart never freezes when the browser ' +
    'drops behind another application, so its catch-up happens in full view');
}
if (!docEvents.some((e) => e.type === 'visibilitychange')) {
  problems.push('no visibilitychange handler — a tab that was hidden holds its last payload ' +
    'pending and would never render it');
}
// And the router-switch half, which lives in main.ts because that is where the
// switch happens. Source-level on purpose: booting main.ts needs the whole page.
// ── main.ts's two calls, checked against the AST rather than the text ───────
//
// These pin that a CONNECTION EXISTS, which is structural by nature: booting
// main.ts needs the whole page, so there is no cheap way to observe the effect.
// That is a different thing from pinning a behaviour's MECHANISM — the live repo
// passed back a case where a green test asserted `lastTalkers=null;` appeared in
// its source, proving an old fix's implementation rather than its effect, and
// failing on a change no user could see.
//
// The distinction is worth keeping, and so is not being fragile about it. This
// used to be `/switchRouter[\s\S]{0,400}?resetSysMeta\(\)/` — a regex that
// would break on a reformat, a longer comment, or a reordered body, none of
// which change what is wired. Parsing asks the actual question: does the body of
// `switchRouter` contain a call to `resetSysMeta`?
const ts = require(path.join(ROOT, 'web', 'node_modules', 'typescript'));
{
  const mainPath = path.join(ROOT, 'web', 'src', 'main.ts');
  const sf = ts.createSourceFile(mainPath, fs.readFileSync(mainPath, 'utf8'),
    ts.ScriptTarget.ES2022, true);

  const callsIn = (node) => {
    const out = new Set();
    const walk = (n) => {
      if (ts.isCallExpression(n) && ts.isIdentifier(n.expression)) out.add(n.expression.text);
      ts.forEachChild(n, walk);
    };
    walk(node);
    return out;
  };

  let switchRouterBody = null;
  const findFn = (n) => {
    if (ts.isFunctionDeclaration(n) && n.name && n.name.text === 'switchRouter') switchRouterBody = n;
    ts.forEachChild(n, findFn);
  };
  findFn(sf);

  if (!switchRouterBody) {
    problems.push('main.ts has no switchRouter function — this check no longer knows what to ask');
  } else if (!callsIn(switchRouterBody).has('resetSysMeta')) {
    problems.push('switchRouter does not call resetSysMeta — the new router would show the ' +
      'PREVIOUS board name, version and CPU count under its own gauges');
  }
  if (!callsIn(sf).has('initDashboard')) {
    problems.push('main.ts never calls initDashboard — nothing is wired at boot');
  }
}

if (problems.length) {
  console.error('dashboard-wiring-check: %d problem(s)\n', problems.length);
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log('dashboard-wiring-check: %d cards wired and rendering, %d modules accounted for',
  Object.keys(CARDS).length, modules.length);
