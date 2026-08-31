'use strict';
/**
 * The WAN page, live against ported. Built on `tools/lib/lift.js`.
 *
 * ── WHAT THIS PAGE ADDS ─────────────────────────────────────────────────────
 *
 * Almost every cell here is TRISTATE, and the third state is the interesting
 * one. `w.isPublic` is `true` (public), `false` (private) or absent (say
 * nothing) — three different renderings from a field a boolean check would
 * flatten to two. `hasDefaultRoute` false does not print a dash and stop; it
 * prints a dash carrying a REASON in its title. And `running === false` is
 * tested by identity, so an absent `running` is not "down".
 *
 * WHAT IT CANNOT SEE: layout, focus, and the action buttons' permission wiring
 * beyond the markup they produce.
 *
 * ── ONE EQUIVALENT MUTANT, WITH THE REASON ─────────────────────────────────
 *
 * Replacing `w.isPublic === true` with a truthy check survives this gate, and it
 * genuinely is equivalent rather than untested: the two branches are `=== true`
 * and `=== false`, so they differ only for a value that is TRUTHY BUT NOT TRUE.
 * The Go collector types the field `*bool` (`internal/collect/wan.go:86`), so
 * `true`, `false` and `null` are the only values that can reach the wire. The
 * corpus carries all three, and there is no fourth to add.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wan-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/wan-page-check.js --freeze
const G = L.golden('wan-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '/* ── WAN page',
  must: ['wanThead', 'leaseCell'],
  mustNot: ['DNS page', 'Queues page', 'VLANs page', 'backupsPage', 'dnsSettingsBody'],
}));
// ── TIME IS FROZEN ON BOTH SIDES ────────────────────────────────────────────
//
// `since()` renders an age from `Date.now()`, so the two runs — milliseconds
// apart — can straddle a second boundary and disagree about something neither
// implementation got wrong. A gate that flakes is worse than no gate, because
// the first reflex on a red run becomes "run it again".
//
// Frozen rather than avoided: dropping the uptime cases would leave the whole
// age ladder (seconds, minutes, hours, days) uncompared.
const NOW = Date.parse('2026-08-24T12:00:00Z');
function FrozenDate() { return new Date(NOW); }
FrozenDate.now = () => NOW;
FrozenDate.parse = Date.parse;
FrozenDate.UTC = Date.UTC;
FrozenDate.prototype = Date.prototype;

const IDS = G.value('IDS', () => L.idsFor(src, iife));

// Declare what this gate provides, for `tools/element-coverage-audit.js`. Placed
// BEFORE the bundle step so asking costs nothing: a text scan cannot see ids
// derived at runtime, and guessing at them is what the audit exists to stop.
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

const FILE_ELS = G.value('FILE_ELS', () => L.fileScopeEls(src, iife));

const ENTRY = path.join(ROOT, 'testdata', '.wan-entry.ts');
fs.writeFileSync(ENTRY, "export { initWanPage } from '../web/src/pages/wan.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wan-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const g = (id) => (n[id] ? { h: n[id].innerHTML, t: n[id].textContent } : null);
  // The dialog is compared as WELL as the table. Leaving it out let four
  // mutations of the self-cutoff body survive — the certain/uncertain wording,
  // the release-versus-renew sentence and the confirm-again line — because the
  // only element carrying them was never looked at.
  const v = (id) => (n[id] ? n[id].value : null);
  return JSON.stringify({
    table: g('wanTable'), thead: g('wanThead'), badge: g('wanBadge'),
    notice: g('wanNotice'), note: g('wanActionNote'),
    sum: ['wanSumCount', 'wanSumActive', 'wanSumPublic', 'wanSumRate'].map(g),
    // The dialog body, whether it is open, and the four hidden inputs the retry
    // is built from — an ack echoed back wrong is the failure this whole path
    // exists to prevent, so the VALUES are compared and not just the markup.
    warn: g('wanWarnBody'),
    warnOpen: n.wanWarnWrap ? n.wanWarnWrap.className : null,
    warnFields: ['wanWarnId', 'wanWarnName', 'wanWarnVerb', 'wanWarnAck'].map(v),
  });
};

function run(fire) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  return { doc, handlers, fire };
}

function liveRun(script) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    Date: FrozenDate,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: () => 0, clearTimeout: () => {}, window: {},
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtMb('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    L.declare(FILE_ELS),
    '(function () {' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['wan:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  for (const [ev, payload] of script) {
    if (!handlers[ev]) throw new Error('nothing subscribes ' + ev);
    handlers[ev](payload);
  }
  return snap(doc);
}

function portRun(script) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const prevWin = globalThis.window;
  const prevDate = globalThis.Date;
  globalThis.window = {};
  globalThis.Date = FrozenDate;
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initWanPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      for (const [ev, payload] of script) {
        if (!handlers[ev]) throw new Error('the port does not subscribe ' + ev);
        handlers[ev](payload);
      }
      return snap(doc);
    });
  } finally {
    globalThis.Date = prevDate;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    const A = JSON.parse(a), B = JSON.parse(b);
    for (const k of Object.keys(A)) {
      const x = JSON.stringify(A[k]), y = JSON.stringify(B[k]);
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k, x, y);
    }
  }
}

const W = (o) => Object.assign({
  id: '*1', name: 'ether1', type: 'ether', isTunnel: false, running: true,
  address: '203.0.113.7/24', isPublic: true, gateway: '203.0.113.1',
  hasDefaultRoute: true, routeActive: true, routeDistance: '1',
  since: null, rxMbps: 12.5, txMbps: 3.25, dhcp: null,
}, o);

// THE FIXTURE SAID `lease: null` UNTIL 2026-08-24, AND BOTH RENDERERS READ
// `w.dhcp`. So the key was dead: every one of the original 34 cases described a
// WAN with no DHCP client, `leaseCell` only ever rendered its "no DHCP client"
// branch, and when the actions column arrived it could not render a button in
// any case at all. The gate reported 53 identical while covering neither.
//
// Found by mutation, not by reading: removing the permission check from
// `actions()` changed nothing, because `!w.dhcp` returned first every time.
// A gate that has not been shown to fail has not been tested.
const D = (o) => Object.assign({
  id: '*d1', status: 'bound', server: 'dhcp1',
  primaryDns: '203.0.113.1', secondaryDns: '', expiresAfter: '23h59m', invalid: false,
}, o);
const U = (o) => Object.assign({ wans: [] }, o);

// ── ONLY `wan:update` IS DRIVEN, AND THAT IS NOT AN OVERSIGHT ───────────────
//
// The live page subscribes four events; the port subscribes one. That is a
// RECORDED state, not a defect: `wanGuard` is the single unported guard, so the
// WAN page is ported read-only and deliberately never asks for caps
// (`PORT-QUEUE.md`, and the header of `web/src/pages/wan.ts` says the same).
//
// Driving `wan:caps` with `permitted: true` on the live side would compare a
// page that offers actions against one that cannot, and report the difference as
// a port defect. It would be the harness being unfair — the same mistake the
// Backups gate made by seeding a selection on one side only. So the live page is
// driven exactly as the port is, WITHOUT caps, which is also the state a real
// viewer without write access sees.
//
// THAT HELD UNTIL 2026-08-24, AND NO LONGER DOES. `wanGuard` is ported, the
// server half is `internal/server/wan.go`, and the page now subscribes the same
// four events the live one does — so driving `wan:caps` with `permitted: true`
// is no longer unfair to either side, and the note above that said this corpus
// "must grow when they do" is being honoured rather than left standing.
//
// The control-payload cases below therefore drive caps, ok and error on BOTH
// sides from one script, which is the only arrangement that can catch the
// actions column, the disabled state and the self-cutoff dialog body.
// WHAT IT STILL DOES NOT COVER, MEASURED BY MUTATION RATHER THAN GUESSED:
// the DISABLED state on an in-flight row. `busy` is set only inside `send()`,
// which runs only from a click, and this gate drives socket events. Mutating
// the disabled attribute away is the one mutation of eight that survives, and
// covering it means firing a delegated click through the shim — a different
// harness, not a bigger corpus. Everything else in the actions column, the
// lease cell and the self-cutoff dialog is compared.
const upd = (o) => [['wan:update', U(o)]];
const caps = (o) => ['wan:caps', Object.assign({ permitted: true, routerName: 'r1' }, o || {})];

const CASES = {
  'no wans': [upd({})],
  'one wan': [upd({ wans: [W({})] })],
  'several wans': [upd({ wans: [W({}), W({ id: '*2', name: 'lte1', type: 'lte' })] })],
  // running is tested by IDENTITY: absent is not "down".
  'a down wan': [upd({ wans: [W({ running: false })] })],
  'a wan with NO running key': [upd({ wans: [W({ running: undefined })] })],
  // isPublic is TRISTATE.
  'a public address': [upd({ wans: [W({ isPublic: true })] })],
  'a private address': [upd({ wans: [W({ isPublic: false })] })],
  'an address of unknown scope': [upd({ wans: [W({ isPublic: undefined })] })],
  'a null isPublic': [upd({ wans: [W({ isPublic: null })] })],
  'no address at all': [upd({ wans: [W({ address: '' })] })],
  'no address but a known scope': [upd({ wans: [W({ address: '', isPublic: true })] })],
  // The default route cell, all three shapes.
  'an active default route': [upd({ wans: [W({ hasDefaultRoute: true, routeActive: true })] })],
  'a standby default route': [upd({ wans: [W({ hasDefaultRoute: true, routeActive: false })] })],
  'NO default route prints a reason': [upd({ wans: [W({ hasDefaultRoute: false })] })],
  'a route with no distance': [upd({ wans: [W({ routeDistance: '' })] })],
  'a route with distance 0': [upd({ wans: [W({ routeDistance: '0' })] })],
  // Type and tunnel labelling.
  'a tunnel': [upd({ wans: [W({ isTunnel: true, type: 'pppoe' })] })],
  'no type at all': [upd({ wans: [W({ type: '' })] })],
  'a tunnel with no type': [upd({ wans: [W({ isTunnel: true, type: '' })] })],
  'no gateway': [upd({ wans: [W({ gateway: '' })] })],
  // Rates and uptime.
  'zero rates': [upd({ wans: [W({ rxMbps: 0, txMbps: 0 })] })],
  'null rates': [upd({ wans: [W({ rxMbps: null, txMbps: null })] })],
  // `since` takes a RouterOS timestamp STRING, not epoch millis — my first
  // fixture passed a number and the live helper died on `ts.replace`.
  'uptime: seconds': [upd({ wans: [W({ since: '2026-08-24 11:59:30' })] })],
  'uptime: minutes': [upd({ wans: [W({ since: '2026-08-24 11:30:00' })] })],
  'uptime: hours': [upd({ wans: [W({ since: '2026-08-24 06:00:00' })] })],
  'uptime: days': [upd({ wans: [W({ since: '2026-08-20 12:00:00' })] })],
  'uptime in the FUTURE clamps to zero': [upd({ wans: [W({ since: '2026-08-25 12:00:00' })] })],
  'an unparseable uptime': [upd({ wans: [W({ since: 'not a date' })] })],
  'no uptime': [upd({ wans: [W({ since: null })] })],
  'an empty uptime': [upd({ wans: [W({ since: '' })] })],
  // The LEASE cell, which had no populated case at all until the dead `lease`
  // key above was found. Every branch of it now has one.
  'a bound lease': [upd({ wans: [W({ dhcp: D() })] })],
  'a lease that is NOT bound': [upd({ wans: [W({ dhcp: D({ status: 'searching' }) })] })],
  'a lease with no status': [upd({ wans: [W({ dhcp: D({ status: '' }) })] })],
  'an INVALID lease': [upd({ wans: [W({ dhcp: D({ invalid: true }) })] })],
  'a lease with no expiry': [upd({ wans: [W({ dhcp: D({ expiresAfter: '' }) })] })],
  'an invalid lease with no expiry': [upd({ wans: [W({ dhcp: D({ invalid: true, expiresAfter: '' }) })] })],
  'markup in a lease status': [upd({ wans: [W({ dhcp: D({ status: '<b>x</b>' }) })] })],
  // Escaping.
  'markup in a name': [upd({ wans: [W({ name: '<img src=x>' })] })],
  'a quote in a name': [upd({ wans: [W({ name: 'a"b' })] })],
  'markup in a type': [upd({ wans: [W({ type: '<b>x</b>' })] })],
  'markup in an address': [upd({ wans: [W({ address: '<i>1.2.3.4</i>' })] })],
  // ── CONTROL PAYLOADS ─────────────────────────────────────────────────────
  // With permission, a DHCP uplink grows two buttons and a static one grows
  // none — the second is the case that would pass unnoticed if `actions()`
  // ignored `w.dhcp`.
  'permitted: a dhcp uplink gets both buttons':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] })]],
  'permitted: a STATIC uplink gets none':
    [[caps(), ...upd({ wans: [W({ dhcp: null })] })]],
  'permitted: several uplinks, one static':
    [[caps(), ...upd({ wans: [W({ dhcp: D() }), W({ id: '*2', name: 'lte1', dhcp: null })] })]],
  'NOT permitted: the same payload offers nothing':
    [[caps({ permitted: false }), ...upd({ wans: [W({ dhcp: D() })] })]],
  // The action note is written from caps and must not be clobbered by a render.
  'permitted clears the read-only note':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] })]],
  // A name with a quote reaches TWO attributes here — data-name and the title —
  // so it is escaped in a context dcEsc would have got wrong. See the live
  // repo's 9c17bf5.
  'permitted: a quote in a name reaches data-name':
    [[caps(), ...upd({ wans: [W({ name: 'a"b', dhcp: D() })] })]],
  'permitted: markup in a name reaches data-name':
    [[caps(), ...upd({ wans: [W({ name: '<img src=x>', dhcp: D() })] })]],
  // wan:ok writes the status line, which render() shares with the caps note.
  'ok: a renewal is REQUESTED, not done':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:ok', { action: 'renew', name: 'ether1' }]]],
  'ok: a release is reported as released':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:ok', { action: 'release', name: 'ether1' }]]],
  // Each error code maps to its own sentence; an unknown one falls back.
  'error: denied':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'denied' }]]],
  'error: stale-row':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'stale-row' }]]],
  'error: an unknown code falls back to its message':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'weird', message: 'something else' }]]],
  'error: an unknown code with NO message':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'weird' }]]],
  // The self-cutoff dialog, both certainties and both codes.
  'self-cutoff: certain':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'self-cutoff', name: 'ether1', verb: 'renew',
      fingerprint: 'fp1', warning: { address: '192.0.2.9', wan: 'ether1', certain: true } }]]],
  'self-cutoff: NOT certain — several active default routes':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'self-cutoff', name: 'ether1', verb: 'renew',
      fingerprint: 'fp1', warning: { address: '192.0.2.9', wan: 'ether1', certain: false } }]]],
  'self-cutoff: RELEASE is worded differently from renew':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'self-cutoff', name: 'ether1', verb: 'release',
      fingerprint: 'fp1', warning: { address: '192.0.2.9', wan: 'ether1', certain: true } }]]],
  'stale-warning adds the confirm-again line':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'stale-warning', name: 'ether1', verb: 'renew',
      fingerprint: 'fp2', warning: { address: '192.0.2.9', wan: 'ether1', certain: true } }]]],
  'self-cutoff for a name that is NOT in the payload':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'self-cutoff', name: 'ether9', verb: 'renew',
      fingerprint: 'fp1', warning: { address: '192.0.2.9', wan: 'ether9', certain: true } }]]],
  'self-cutoff with markup in the warning fields':
    [[caps(), ...upd({ wans: [W({ dhcp: D() })] }), ['wan:error', { code: 'self-cutoff', name: 'ether1', verb: 'renew',
      fingerprint: 'fp1', warning: { address: '<i>x</i>', wan: '<b>y</b>', certain: true } }]]],
};

for (const [name, [script]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(script)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(script); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:6', () => liveRun(upd({ wans: [W({ dhcp: D() })] }))));
  assert.match(s.table.h, /ether1/, 'the live table rendered no row');
  assert.match(s.thead.h, /<th/, 'the sort header rendered nothing');
  assert.equal(s.badge.t, '1', 'the badge is ' + s.badge.t);
  assert.match(s.table.h, /public/, 'a public address was not labelled');
  assert.match(s.table.h, /active/, 'an active default route was not labelled');
}
{
  // The tristate really is three renderings, not two.
  const pub = JSON.parse(G.live('auto:5', () => liveRun(upd({ wans: [W({ isPublic: true })] })))).table.h;
  const priv = JSON.parse(G.live('auto:4', () => liveRun(upd({ wans: [W({ isPublic: false })] })))).table.h;
  const unk = JSON.parse(G.live('auto:3', () => liveRun(upd({ wans: [W({ isPublic: undefined })] })))).table.h;
  assert.ok(pub !== priv && priv !== unk && pub !== unk,
    'isPublic collapsed to fewer than three renderings');
  assert.ok(!/public|private/.test(unk), 'an unknown scope claimed one anyway');
}
{
  // No default route carries a REASON, not a bare dash.
  const s = JSON.parse(G.live('auto:2', () => liveRun(upd({ wans: [W({ hasDefaultRoute: false })] }))));
  assert.match(s.table.h, /No default route via this uplink/,
    'the missing-route dash lost its explanation');
}
{
  const s = JSON.parse(G.live('auto:1', () => liveRun(upd({}))));
  assert.equal(s.badge.t, '0', 'the empty badge is ' + s.badge.t);
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('wan-page-check: %d cases identical', checked);
