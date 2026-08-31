'use strict';
/**
 * The account modal's renderers, live against ported, by DOM equality.
 *
 * These four write markup and nothing else, so the comparison is the strongest
 * kind available: drive both from one payload, diff the innerHTML byte for byte.
 * A screenshot catches a layout change and misses an attribute; this catches
 * both, and it catches an escaping hole, which is the one that matters here —
 * every string rendered comes from an administrator-set label or a role name.
 *
 * ── THE CORPUS IS BUILT AROUND WHAT THESE STRINGS ACTUALLY ARE ──────────────
 *
 *   site and router labels   operator-supplied free text. Angle brackets,
 *                            quotes, ampersands and a script tag are in the
 *                            corpus because a label is exactly where stored
 *                            markup would arrive.
 *   role names               joined with ', ', so a role containing a comma
 *                            renders indistinguishably from two roles. That is
 *                            the live behaviour and it is pinned, not fixed.
 *   an empty access set      has its own sentence rather than an empty box, and
 *                            "empty" has four spellings here: no key at all, an
 *                            empty array, all three sections empty, and a global
 *                            array that exists but is empty — which is falsy on
 *                            `.length` and must NOT print an "Everything" row.
 *   session timestamps       go through `toLocaleString()` with no arguments,
 *                            so the format is the runtime's. Both sides run in
 *                            one process, so equality still means equality; what
 *                            it pins is that the port did not "improve" it into
 *                            a fixed format that would change what every user
 *                            outside one locale sees.
 *   a null expiry            renders "never", and 0 is NOT null — a session that
 *                            expired at the epoch is a different thing from one
 *                            that never expires.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/account-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/account-check.js --freeze
const G = L.golden('account-check');
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
// The loader and the wiring, lifted whole. Running the real wiring block means
// the LISTENERS are compared too — which control listens for what, and whether
// it is connected at all — not merely the functions behind them.
const loadSrc = slice('  function _loadAccount() {', '\n  }', '_loadAccount');
const openSrc = slice('  function openAccountModal() {', '\n  }', 'openAccountModal');
const wireSrc = slice("  if (chip) chip.addEventListener('click', function(){ openAccountModal(); });",
  '\n  }\n})();', 'the account wiring')
  // …minus the IIFE's own closing brace, which belongs to the block this slice
  // is being lifted OUT of. Trimmed rather than sliced short, so the anchor
  // stays the real end of the wiring.
  .replace(/\}\)\(\);\s*$/, '');

// Ported at Part 19 too, so it is lifted rather than stubbed. Leaving the stub
// in was the gate's own bug: the port called the real function, the live side
// called nothing, and the section's display differed for a reason that had
// nothing to do with the port.
const myAlertsSrc = slice('function _applyMyAlertsTab(enabled) {', '\n}', '_applyMyAlertsTab');

const liveSrc = [
  slice('  function _acctSay(el, ok, msg) {', '\n  }', '_acctSay'),
  slice('  function _renderAccess(a) {', '\n  }', '_renderAccess'),
  slice('  function _renderSessions(list) {', '\n  }', '_renderSessions'),
  slice('  function _setPwFormOpen(open) {', '\n  }', '_setPwFormOpen'),
].join('\n\n');
// `esc` is the live one, lifted rather than assumed equal to the port's — the
// whole escaping question rides on it.
const escSrc = slice('function esc(', '\n}', 'esc');

const OUT = path.join(ROOT, 'testdata', '.account-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'account.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const IDS = ['acct_accessBody', 'acct_sessionsBody', 'acct_pwForm', 'acct_pwPrompt',
             'acct_pwResult', 'acct_currentPassword', 'acct_newPassword', 'acct_confirmPassword',
             'acct_sessionsResult', 'acct_username', 'acct_version', 'accountModal',
             'authUsername', 'authUserChip', 'logoutBtn', 'acctMyAlerts',
             'acct_pwToggleBtn', 'acct_pwCancelBtn', 'acct_pwSaveBtn',
             'acct_signOutBtn', 'acct_signOutOthersBtn'];

function makeWorld(missing) {
  const nodes = {};
  let focused = null;
  const timers = new Map();
  let seq = 0;
  for (const id of IDS) {
    if ((missing || []).includes(id)) continue;
    const style = {};
    const classes = new Set();
    nodes[id] = {
      _id: id, innerHTML: '', textContent: '', value: '', disabled: false, dataset: {},
      style: { get display() { return style.display || ''; }, set display(v) { style.display = String(v); },
               get color() { return style.color || ''; }, set color(v) { style.color = String(v); } },
      classList: { add: (c) => classes.add(c), remove: (c) => classes.delete(c), _set: classes },
      _h: {},
      addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); },
      focus() { focused = id; },
    };
  }
  const calls = [];
  let location = '';
  let stopped = 0;
  return {
    doc: { getElementById: (id) => nodes[id] || null },
    nodes, calls,
    /** Every request, in order, with the body — so a write is compared on what
     *  it actually sends, not merely on what it does afterwards. */
    fetch(url, init) {
      calls.push({ url, method: (init && init.method) || 'GET', body: (init && init.body) || null,
                   credentials: (init && init.credentials) || null });
      const r = this._responses[url];
      if (r === undefined) return Promise.reject(new Error('no stub for ' + url));
      if (r instanceof Error) return Promise.reject(r);
      return Promise.resolve({ ok: true, json: () => Promise.resolve(r) });
    },
    _responses: {},
    respond(map) { this._responses = map; return this; },
    go(href) { location = href; },
    fire(id, name, ev) {
      const n = nodes[id];
      if (!n) return;
      for (const f of (n._h[name] || [])) f.call(n, ev || { stopPropagation: () => { stopped++; } });
    },
    setTimeout: (fn, ms) => { const id = ++seq; timers.set(id, { fn, ms }); return id; },
    flush() { const p = [...timers.values()]; timers.clear(); for (const t of p) t.fn(); },
    /** Run ONLY the earliest pending timer.
     *
     *  `flush()` cannot see the guard inside the clear callback: running both
     *  timers ends at an empty line either way, because whichever one is allowed
     *  to clear does. Firing just the first, while the second message still
     *  stands, is what separates "checks the text is still mine" from "clears
     *  whatever is there". */
    flushFirst() {
      const first = [...timers.entries()].sort((a, b) => a[0] - b[0])[0];
      if (!first) return;
      timers.delete(first[0]);
      first[1].fn();
    },
    state() {
      return JSON.stringify({
        nodes: IDS.map((id) => {
          const n = nodes[id];
          return n ? [id, n.innerHTML, n.textContent, n.value, n.style.display, n.style.color] : [id, null];
        }),
        focused,
        pending: [...timers.values()].map((t) => t.ms),
        classes: IDS.map((id) => (nodes[id] ? [id, [...nodes[id].classList._set].sort()] : null))
          .filter((x) => x && x[1].length),
        disabled: IDS.map((id) => (nodes[id] && nodes[id].disabled ? id : null)).filter(Boolean),
        dataset: IDS.map((id) => (nodes[id] && Object.keys(nodes[id].dataset).length
          ? [id, nodes[id].dataset] : null)).filter(Boolean),
        calls,
        location,
        stopped,
      }, null, 1);
    },
  };
}

/** Both sides get the same window stand-in, so a redirect is observable. */
function makeWindow(w) {
  return { get location() { return { set href(v) { w.go(v); }, get href() { return ''; } }; } };
}

function liveRun(missing, body) {
  const w = makeWorld(missing);
  const ctx = {
    document: w.doc, setTimeout: w.setTimeout, Date, String, Array, JSON, Object, Promise, Error,
    fetch: (u, i) => w.fetch(u, i),
    socket: { on() {} },
  };
  ctx.window = makeWindow(w);
  ctx.$ = (id) => w.doc.getElementById(id);
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\n' + liveSrc, ctx);
  body(ctx, w);
  return w.state();
}

function portRun(missing, body) {
  const w = makeWorld(missing);
  const keys = ['document', 'setTimeout', 'fetch', 'window'];
  const saved = {};
  for (const k of keys) saved[k] = global[k];
  global.document = w.doc;
  global.setTimeout = w.setTimeout;
  global.fetch = (u, i) => w.fetch(u, i);
  global.window = makeWindow(w);
  try {
    delete require.cache[require.resolve(OUT)];
    body(require(OUT), w);
  } finally {
    for (const k of keys) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return w.state();
}

/** Let every pending promise chain settle before reading the state. */
const settle = () => new Promise((r) => setImmediate(r));

const bad = [];
let cases = 0;
function compare(what, missing, liveBody, portBody) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(missing, liveBody));
  const b = portRun(missing, portBody);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

/** The async half: the live side is wrapped so its IIFE-scoped loader and wiring
 *  are reachable, both sides settle, then the states are diffed. */
const asyncCases = [];
function compareAsync(what, responses, liveBody, portBody) {
  asyncCases.push(async () => {
    cases++;
    // THE LIVE HALF IS FROZEN. It builds a VM from lifted source and drives
    // it; with the reference absent that source is empty and the drivers
    // fail with `c.openAccountModal is not a function` — a message about
    // the live context, not about the port. Recording `wa.state()` is what
    // lets the port still be checked when there is nothing to lift.
    const a = await G.live('async:' + what, async () => {
      const wa = makeWorld([]);
      wa.respond(responses);
      const ctxA = {
        document: wa.doc, setTimeout: wa.setTimeout, Date, String, Array, JSON, Object, Promise, Error,
        fetch: (u, i) => wa.fetch(u, i), socket: { on() {} },
      };
      ctxA.window = makeWindow(wa);
      ctxA.$ = (id) => wa.doc.getElementById(id);
      vm.createContext(ctxA);
      vm.runInContext(escSrc + '\n' + myAlertsSrc + '\n' + liveSrc + '\n' +
        'var nameEl = $("authUsername"); var chip = $("authUserChip");' +
        'var logoutBtn = $("logoutBtn"); var acctModal = $("accountModal");' +
        loadSrc + '\n' + openSrc + '\n' + wireSrc, ctxA);
      await liveBody(ctxA, wa);
      await settle(); await settle();
      return wa.state();
    });

    const wb = makeWorld([]);
    wb.respond(responses);
    const keys = ['document', 'setTimeout', 'fetch', 'window'];
    const saved = {};
    for (const k of keys) saved[k] = global[k];
    global.document = wb.doc; global.setTimeout = wb.setTimeout;
    global.fetch = (u, i) => wb.fetch(u, i); global.window = makeWindow(wb);
    try {
      delete require.cache[require.resolve(OUT)];
      const mod = require(OUT);
      mod.wireAccount();
      await portBody(mod, wb);
      await settle(); await settle();
    } finally {
      for (const k of keys) {
        if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
      }
    }
    const b = wb.state();
    if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
  });
}

// ── Access ──────────────────────────────────────────────────────────────────
const NASTY = '<script>alert("x")</script> & "quoted" \'single\' <b>';
const ACCESS = [
  ['nothing at all', {}],
  ['an empty global array — falsy on .length, so NO Everything row', { global: [] }],
  ['all three sections present but empty', { global: [], sites: [], routers: [] }],
  ['global only', { global: ['Administrator'] }],
  ['global with several roles', { global: ['Administrator', 'Operator', 'Read Only'] }],
  ['one site', { sites: [{ siteName: 'Headquarters', roles: ['Operator'] }] }],
  ['several sites', { sites: [
    { siteName: 'Headquarters', roles: ['Operator'] },
    { siteName: 'Branch', roles: ['Read Only', 'Operator'] }] }],
  ['one router', { routers: [{ routerLabel: 'Core Switch', roles: ['Read Only'] }] }],
  ['all three kinds at once', {
    global: ['Administrator'],
    sites: [{ siteName: 'HQ', roles: ['Operator'] }],
    routers: [{ routerLabel: 'edge-1', roles: ['Read Only'] }] }],
  ['a label that is markup', { sites: [{ siteName: NASTY, roles: ['Operator'] }] }],
  ['a router label that is markup', { routers: [{ routerLabel: NASTY, roles: [NASTY] }] }],
  ['a role name containing a comma', { global: ['Read, Only'] }],
  ['an empty label and an empty role', { sites: [{ siteName: '', roles: [''] }] }],
  ['a label of only whitespace', { routers: [{ routerLabel: '   ', roles: ['Operator'] }] }],
  ['many rows', { routers: Array.from({ length: 12 }, (_, i) => ({ routerLabel: 'r' + i, roles: ['Read Only'] })) }],
];
for (const [what, a] of ACCESS) {
  compare('access: ' + what, [], (c) => c._renderAccess(a), (p) => p.renderAccess(a));
}
// Rendered twice — the body is replaced, never appended to.
compare('access rendered twice', [],
  (c) => { c._renderAccess({ global: ['Administrator'] }); c._renderAccess({}); },
  (p) => { p.renderAccess({ global: ['Administrator'] }); p.renderAccess({}); });
// The element missing entirely is a no-op, not a throw.
compare('access with no body element', ['acct_accessBody'],
  (c) => c._renderAccess({ global: ['x'] }), (p) => p.renderAccess({ global: ['x'] }));

// ── Sessions ────────────────────────────────────────────────────────────────
const T0 = 1773567000000;
const SESSIONS = [
  ['null', null],
  ['undefined', undefined],
  ['an empty list', []],
  ['one session, not current', [{ createdAt: T0, expiresAt: T0 + 86400000 }]],
  ['one session, current', [{ createdAt: T0, expiresAt: T0 + 86400000, current: true }]],
  ['a null expiry renders never', [{ createdAt: T0, expiresAt: null, current: true }]],
  ['no expiry key at all', [{ createdAt: T0 }]],
  ['an expiry of 0, which is NOT null', [{ createdAt: T0, expiresAt: 0 }]],
  ['several, one of them current', [
    { createdAt: T0, expiresAt: T0 + 3600000 },
    { createdAt: T0 + 60000, expiresAt: T0 + 7200000, current: true },
    { createdAt: T0 + 120000, expiresAt: null }]],
  ['an ISO string timestamp', [{ createdAt: '2026-03-01T12:34:56.000Z', expiresAt: null }]],
  ['an unparseable timestamp', [{ createdAt: 'not a date', expiresAt: 'also not' }]],
  ['current set to a falsy value that is not false', [{ createdAt: T0, expiresAt: null, current: 0 }]],
];
for (const [what, list] of SESSIONS) {
  compare('sessions: ' + what, [], (c) => c._renderSessions(list), (p) => p.renderSessions(list));
}
compare('sessions with no body element', ['acct_sessionsBody'],
  (c) => c._renderSessions([{ createdAt: T0 }]), (p) => p.renderSessions([{ createdAt: T0 }]));

// ── The password form ───────────────────────────────────────────────────────
compare('open the password form', [], (c) => c._setPwFormOpen(true), (p) => p.setPwFormOpen(true));
compare('close the password form', [], (c) => c._setPwFormOpen(false), (p) => p.setPwFormOpen(false));
// The clearing is the point: three plaintext fields and a stale result line.
compare('type, then close', [],
  (c, w) => {
    c._setPwFormOpen(true);
    w.nodes.acct_currentPassword.value = 'hunter2';
    w.nodes.acct_newPassword.value = 'correct horse';
    w.nodes.acct_confirmPassword.value = 'correct horse';
    w.nodes.acct_pwResult.textContent = 'Password changed';
    c._setPwFormOpen(false);
  },
  (p, w) => {
    p.setPwFormOpen(true);
    w.nodes.acct_currentPassword.value = 'hunter2';
    w.nodes.acct_newPassword.value = 'correct horse';
    w.nodes.acct_confirmPassword.value = 'correct horse';
    w.nodes.acct_pwResult.textContent = 'Password changed';
    p.setPwFormOpen(false);
  });
compare('open, close, open again', [],
  (c) => { c._setPwFormOpen(true); c._setPwFormOpen(false); c._setPwFormOpen(true); },
  (p) => { p.setPwFormOpen(true); p.setPwFormOpen(false); p.setPwFormOpen(true); });
// Either half of the pair missing is a no-op — including the fields, so a
// partially-rendered modal does not throw on close.
for (const missing of [['acct_pwForm'], ['acct_pwPrompt'], ['acct_pwForm', 'acct_pwPrompt'],
                       ['acct_currentPassword'], ['acct_pwResult']]) {
  compare('form toggle without ' + missing.join('+'), missing,
    (c) => { c._setPwFormOpen(true); c._setPwFormOpen(false); },
    (p) => { p.setPwFormOpen(true); p.setPwFormOpen(false); });
}

// ── The transient message ───────────────────────────────────────────────────
for (const [what, ok, msg] of [
  ['a success', true, 'Password changed'],
  ['a failure', false, 'Current password is incorrect'],
  ['an empty success', true, ''],
  ['a message that is markup', false, NASTY],
]) {
  compare('say ' + what, [],
    (c, w) => c._acctSay(w.nodes.acct_pwResult, ok, msg),
    (p, w) => p.acctSay(w.nodes.acct_pwResult, ok, msg));
  // …and after the 5s timer. Only a success schedules one.
  compare('say ' + what + ', then the timer fires', [],
    (c, w) => { c._acctSay(w.nodes.acct_pwResult, ok, msg); w.flush(); },
    (p, w) => { p.acctSay(w.nodes.acct_pwResult, ok, msg); w.flush(); });
}
// The guard that stops one action's timer wiping another's result.
compare('a second message before the first timer fires', [],
  (c, w) => {
    c._acctSay(w.nodes.acct_pwResult, true, 'Password changed');
    c._acctSay(w.nodes.acct_pwResult, true, 'Other sessions signed out');
    w.flush();
  },
  (p, w) => {
    p.acctSay(w.nodes.acct_pwResult, true, 'Password changed');
    p.acctSay(w.nodes.acct_pwResult, true, 'Other sessions signed out');
    w.flush();
  });
// THE GUARD, seen properly. Two successes inside the five-second window: the
// first one's timer must find someone else's text and leave it alone, or the
// operator watches the confirmation for the thing they just did vanish.
compare('the first timer fires while the second message stands', [],
  (c, w) => {
    c._acctSay(w.nodes.acct_pwResult, true, 'Password changed');
    c._acctSay(w.nodes.acct_pwResult, true, 'Other sessions signed out');
    w.flushFirst();
  },
  (p, w) => {
    p.acctSay(w.nodes.acct_pwResult, true, 'Password changed');
    p.acctSay(w.nodes.acct_pwResult, true, 'Other sessions signed out');
    w.flushFirst();
  });
// And the same when the second message is a FAILURE, which schedules no timer
// of its own — so the only timer in flight belongs to the message it replaced.
compare('a failure replaces a success, then the success timer fires', [],
  (c, w) => {
    c._acctSay(w.nodes.acct_pwResult, true, 'Password changed');
    c._acctSay(w.nodes.acct_pwResult, false, 'Current password is incorrect');
    w.flushFirst();
  },
  (p, w) => {
    p.acctSay(w.nodes.acct_pwResult, true, 'Password changed');
    p.acctSay(w.nodes.acct_pwResult, false, 'Current password is incorrect');
    w.flushFirst();
  });

compare('say into a missing element', [],
  (c) => c._acctSay(null, true, 'x'), (p) => p.acctSay(null, true, 'x'));

// ── The loader and the writes, driven through the real wiring ───────────────
//
// Every case fires an EVENT at a control rather than calling a function, so a
// handler that was never attached fails here. The fetch stub records url,
// method and body, which is what makes a write comparable at all: what it sends
// is as much part of the behaviour as what it does with the answer.

const OK_ACCESS = { ok: true, access: { global: ['Administrator'] } };
const OK_SESSIONS = { ok: true, sessions: [{ createdAt: T0, expiresAt: null, current: true }] };
const BASE = {
  '/api/settings': { userNotifyEnabled: false },
  '/api/account/access': OK_ACCESS,
  '/api/account/sessions': OK_SESSIONS,
  '/healthz': { version: '0.7.33-7' },
};

compareAsync('open the account modal', BASE,
  async (c, w) => { w.nodes.authUsername.textContent = 'kschutte'; c.openAccountModal(); },
  async (p, w) => { w.nodes.authUsername.textContent = 'kschutte'; p.openAccountModal(); });
// Through the CHIP, which is how a person actually opens it.
compareAsync('click the user chip', BASE,
  async (_c, w) => { w.nodes.authUsername.textContent = 'kschutte'; w.fire('authUserChip', 'click'); },
  async (_p, w) => { w.nodes.authUsername.textContent = 'kschutte'; w.fire('authUserChip', 'click'); });
// Opened twice: the version is fetched ONCE, because it cannot change while the
// page is open — the guard is on the element still being empty.
compareAsync('open twice fetches the version once', BASE,
  async (c) => { c.openAccountModal(); await settle(); await settle(); c.openAccountModal(); },
  async (p) => { p.openAccountModal(); await settle(); await settle(); p.openAccountModal(); });
// Every failure is swallowed: three panels beat an error where the panels go.
compareAsync('the access read fails', { ...BASE, '/api/account/access': new Error('down') },
  async (c) => c.openAccountModal(), async (p) => p.openAccountModal());
compareAsync('every read fails', {
  '/api/settings': new Error('x'), '/api/account/access': new Error('x'),
  '/api/account/sessions': new Error('x'), '/healthz': new Error('x'),
}, async (c) => c.openAccountModal(), async (p) => p.openAccountModal());
compareAsync('the reads answer ok:false', {
  ...BASE, '/api/account/access': { ok: false }, '/api/account/sessions': { ok: false },
}, async (c) => c.openAccountModal(), async (p) => p.openAccountModal());
compareAsync('healthz answers without a version', { ...BASE, '/healthz': { ok: true } },
  async (c) => c.openAccountModal(), async (p) => p.openAccountModal());

// ── The password form ───────────────────────────────────────────────────────
function typePw(w, cur, nw, cf) {
  w.nodes.acct_currentPassword.value = cur;
  w.nodes.acct_newPassword.value = nw;
  w.nodes.acct_confirmPassword.value = cf;
}
// The two client-side refusals send NOTHING — checked by the recorded calls.
for (const [what, a, b, c3] of [
  ['no current password', '', 'newpass', 'newpass'],
  ['no new password', 'old', '', ''],
  ['neither', '', '', ''],
  ['a confirmation that does not match', 'old', 'newpass', 'newpazz'],
  ['a confirmation that is empty', 'old', 'newpass', ''],
]) {
  compareAsync('refuse: ' + what, BASE,
    async (_c, w) => { typePw(w, a, b, c3); w.fire('acct_pwSaveBtn', 'click'); },
    async (_p, w) => { typePw(w, a, b, c3); w.fire('acct_pwSaveBtn', 'click'); });
}
for (const [what, resp] of [
  ['plain', { ok: true }],
  ['with other sessions revoked', { ok: true, revokedOtherSessions: 3 }],
  ['with ZERO other sessions revoked, which takes the plain branch', { ok: true, revokedOtherSessions: 0 }],
  ['refused by the server', { ok: false, error: 'Current password is incorrect' }],
  ['refused with no error text', { ok: false }],
]) {
  compareAsync('change password ' + what, { ...BASE, '/api/account/password': resp },
    async (_c, w) => { typePw(w, 'old', 'newpass', 'newpass'); w.fire('acct_pwSaveBtn', 'click'); },
    async (_p, w) => { typePw(w, 'old', 'newpass', 'newpass'); w.fire('acct_pwSaveBtn', 'click'); });
}
// A rejected request must RE-ENABLE the button, or the form is dead until reload.
compareAsync('the password request rejects', { ...BASE, '/api/account/password': new Error('network') },
  async (_c, w) => { typePw(w, 'old', 'newpass', 'newpass'); w.fire('acct_pwSaveBtn', 'click'); },
  async (_p, w) => { typePw(w, 'old', 'newpass', 'newpass'); w.fire('acct_pwSaveBtn', 'click'); });
compareAsync('open then cancel the password form', BASE,
  async (_c, w) => { w.fire('acct_pwToggleBtn', 'click'); typePw(w, 'a', 'b', 'c'); w.fire('acct_pwCancelBtn', 'click'); },
  async (_p, w) => { w.fire('acct_pwToggleBtn', 'click'); typePw(w, 'a', 'b', 'c'); w.fire('acct_pwCancelBtn', 'click'); });

// ── Revoke other sessions ───────────────────────────────────────────────────
for (const [what, resp] of [
  ['several', { ok: true, revoked: 4 }],
  ['none', { ok: true, revoked: 0 }],
  ['refused', { ok: false, error: 'Not allowed' }],
  ['refused with no text', { ok: false }],
]) {
  compareAsync('revoke others: ' + what,
    { ...BASE, '/api/account/sessions/revoke-others': resp },
    async (_c, w) => w.fire('acct_signOutOthersBtn', 'click'),
    async (_p, w) => w.fire('acct_signOutOthersBtn', 'click'));
}
compareAsync('revoke others rejects',
  { ...BASE, '/api/account/sessions/revoke-others': new Error('network') },
  async (_c, w) => w.fire('acct_signOutOthersBtn', 'click'),
  async (_p, w) => w.fire('acct_signOutOthersBtn', 'click'));

// ── Signing out ─────────────────────────────────────────────────────────────
// BOTH paths redirect: a logout whose request failed still ends the session as
// far as this browser is concerned, and leaving someone on a dashboard they
// believe they have left is worse than a redundant redirect.
for (const [what, resp] of [['succeeds', { ok: true }], ['fails', new Error('network')]]) {
  compareAsync('sign out ' + what, { ...BASE, '/api/auth/logout': resp },
    async (_c, w) => w.fire('acct_signOutBtn', 'click'),
    async (_p, w) => w.fire('acct_signOutBtn', 'click'));
  // The topbar button, which must ALSO stop the click reaching the chip it sits
  // inside — otherwise signing out opens the account modal on the way past.
  compareAsync('the topbar logout ' + what, { ...BASE, '/api/auth/logout': resp },
    async (_c, w) => w.fire('logoutBtn', 'click'),
    async (_p, w) => w.fire('logoutBtn', 'click'));
}

(async () => {
  for (const run of asyncCases) await run();

  fs.rmSync(OUT, { force: true });
  if (bad.length) {
    console.error('the account modal differs from the live one:\n\n' + bad.slice(0, 2).join('\n\n') +
      (bad.length > 2 ? '\n\n\u2026 and ' + (bad.length - 2) + ' more' : '') + '\n');
    process.exit(1);
  }
  console.log('account modal matches the live one (' + cases + ' cases: renderers, the loader, ' +
    'the password change, session revocation and both sign-out paths)');
})();
