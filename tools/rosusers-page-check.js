'use strict';
/**
 * The ROUTER USERS page, live against ported. Built on `tools/lib/lift.js`.
 *
 * ── WHAT THIS PAGE CONTRIBUTES ──────────────────────────────────────────────
 *
 * Three tabs — users, groups and sessions — and `render()` draws ALL THREE every
 * time, because the two hidden tabs still carry counts in their badges. So a
 * change to any tab is visible whichever one is selected, and every case here
 * snapshots all three.
 *
 * The user status is a three-way ladder where the middle rung is easy to lose:
 * disabled, EXPIRED, enabled. An expired account is not disabled — nobody turned
 * it off — and rendering it as enabled invites someone to wonder why the login
 * fails.
 *
 * A group row lists only the GRANTED policies. The live comment says why: the
 * denied half is every other policy, and seventeen names per row would bury the
 * four that matter.
 *
 * The summary uses `|| '—'`, so a router with ZERO users shows a dash rather
 * than a nought — and zero-versus-absent is the corpus's job to pin.
 *
 * ── AND THE THREE DESTRUCTIVE BUTTONS, WHICH NOTHING DROVE ──────────────────
 *
 * Remove a router user, remove a group, end a live session. `window` was `{}` on
 * both sides, so `window.confirm` was undefined and pressing any of them would
 * have thrown — which is why no case pressed them. Both runners now record the
 * question asked and the events emitted, and the snapshot carries that trail.
 *
 * The WORDING is compared as carefully as the emit. It is the only place the
 * operator is told what they are about to lose, and a page that emitted
 * correctly while asking the wrong question would look identical.
 *
 * ── THE GROUP POLICY LIST, AND WHY IT SURVIVED ONE ROUND ───────────────────
 *
 * Replacing the group save's `policy` with `[]` passed at first, and the cause
 * was NOT the shim: `#rgf_policies` draws its boxes from `data.policies`, the
 * payload's list of what RouterOS offers, and the payload builder here did not
 * carry that key. The box rendered empty on both sides, the ticked list was
 * always `[]`, and the gate was driving the save and seeing nothing.
 *
 * A payload that lets a renderer RUN is not one that makes its RULES visible.
 * `P()` supplies six policy names now, `G({}).granted` names three of them so an
 * edit has ticked and unticked boxes, and one case carries markup in a name —
 * the escape is invisible until a name needs escaping.
 *
 * `expectedName` rides with every removal — the router-side guard against an id
 * reused since the page was drawn — so an emit that drops or mangles it removes
 * whatever now holds that id. Nine mutations killed, including a cancelled
 * confirmation still removing, `expectedName` carrying the id, group- and
 * session-remove swapped, and an unknown action falling through to a removal.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/rosusers-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/rosusers-page-check.js --freeze
const __GOLD = L.golden('rosusers-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = __GOLD.value('iife', () => L.region(src, {
  banner: '/* ── Router Users page',
  must: ['ruUserTable', 'ruGroupTable', 'ruSessionTable'],
  mustNot: ['DNS page', 'Bridges page', 'Packages page', 'backupsPage'],
}));
const IDS = __GOLD.value('IDS', () => L.idsFor(src, iife));
// ── THE FORMS ARE COMPARED NOW ─────────────────────────────────────────────
//
// This read `IDS.filter((id) => !/^r[ug]f_/.test(id))` with the note "the
// add/edit dialog is its own surface". No other gate took that surface, so
// twenty of this module's thirty-three elements were untested — and it stayed
// invisible because `element-coverage-audit` excluded the whole page under a
// stale "no gate at all" entry (2026-08-25).
//
// The forms are WRITE paths: they create and edit the users that can log in to
// a router. `openUserForm` alone decides the title, eight field values, the
// password hint's three branches and which groups are offered, and `ruf_save`
// turns all of that into one emit.
//
// The VALUE and CHECKED state matter here in a way they do not for a table, so
// the snapshot reads them for the form ids — `h`/`t`/`d` alone would compare two
// empty strings on an `<input>`.
const FORM = IDS.filter((id) => /^r[ug]f_/.test(id));
const COMPARED = IDS.filter((id) => !/^r[ug]f_/.test(id));
if (process.argv.includes('--ids')) {
  console.log(JSON.stringify(COMPARED.concat(FORM))); process.exit(0);
}

const ENTRY = path.join(ROOT, 'testdata', '.ru-entry.ts');
fs.writeFileSync(ENTRY, "export { initRosUsersPage } from '../web/src/pages/rosusers.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ru-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

/**
 * Answer `#rgf_policies .rgf-pol:checked` from what was RENDERED.
 *
 * The group save reads its policy list that way on both sides, and a shim that
 * returns nothing makes both sides send an empty list — so a save that dropped
 * the policies entirely compared equal. Measured: that mutation survived until
 * this existed.
 *
 * The boxes are parsed out of `#rgf_policies`'s markup rather than declared, so
 * a form that renders a policy this file has never heard of is still read, and
 * one that stops rendering them shows up as an empty list.
 */
function wirePolicies(doc) {
  const host = doc.nodes.rgf_policies;
  if (!host) return;
  const prev = doc.querySelectorAll.bind(doc);
  doc.querySelectorAll = (sel) => {
    if (sel !== '#rgf_policies .rgf-pol:checked') return prev(sel);
    return [...String(host.innerHTML).matchAll(/<input[^>]*class="rgf-pol"[^>]*>/g)]
      .map((m) => m[0])
      .filter((tag) => / checked[ />]/.test(tag))
      .map((tag) => ({ value: (tag.match(/value="([^"]*)"/) || [, ''])[1] }));
  };
}

const snap = (doc, trail) => {
  // ── WHAT THE PAGE ASKED THE ROUTER TO DO ────────────────────────────────
  //
  // Three of this page's buttons remove a router user, a group or a live
  // session, and each asks first. None of that is markup: the confirmation TEXT
  // and the emitted `{ id, expectedName }` are the whole action, and the page
  // looks the same whether they are right or wrong. `window` was `{}` on both
  // sides, so calling `window.confirm` would have thrown — which is why no case
  // could press these at all.
  const n = doc.nodes;
  const out = {};
  // The panes are query nodes rather than declared ids, and their `active` class
  // is the visible result of a tab switch — without it a wrong pane id prefix
  // survives, because nothing observes which pane was shown.
  for (const id of ['rutab-users', 'rutab-groups', 'rutab-sessions']) {
    out[id] = n[id] ? { active: n[id].classList.contains('active') } : null;
  }
  for (const id of COMPARED.slice().sort()) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent,
      d: n[id].style && n[id].style.display } : null;
  }
  for (const id of FORM.slice().sort()) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent,
      v: n[id].value, c: n[id].checked,
      d: n[id].style && n[id].style.display } : null;
  }
  if (trail) out.__trail = trail;
  return JSON.stringify(out);
};

function drive(doc, fire, script, o) {
  // THE TAB IS SWITCHED THE WAY A VIEWER SWITCHES IT. `render()` draws all three
  // tables whichever tab is selected — the hidden two still carry counts — but
  // the SELECTED tab is drawn by a different call than the other two, and the
  // Add button's label and visibility follow it. Without a switch, two mutations
  // in the hidden-tab path survived: they were unreachable, not correct.
  if (o.tab) {
    const btn = doc.queryNodes['#ruTabBar .stab']
      .find((n) => n.getAttribute('data-rutab') === o.tab);
    if (!btn) throw new Error('no tab button for ' + o.tab);
    btn.click();
  }
  if (o.search) doc.nodes.ruSearch.value = o.search;
  for (const [ev, p] of script) fire(ev, p);
  if (o.search) doc.nodes.ruSearch.fire('input');
  // ── THE DESTRUCTIVE BUTTONS ─────────────────────────────────────────────
  //
  // Delegated on `document` and found with `closest('.ru-act')`, so the click is
  // dispatched at document level with a target that answers `closest` — what a
  // real click does. The attributes come from the case rather than from the
  // rendered row, which is deliberate: a row whose `data-name` is wrong is a
  // DIFFERENT bug, caught by the markup half of this gate, and mixing the two
  // would let either hide the other.
  // ── THE FORM ──────────────────────────────────────────────────────────
  //
  // Opened through the SAME delegated click a viewer uses, so the row the form
  // is filled from is the one the page found — not one handed to it here.
  if (o.form) {
    if (o.form === 'add') {
      // ── ADD IS A BUTTON, NOT AN ACT ───────────────────────────────────
      //
      // `ruAddBtn` opens the user form or the group form depending on which TAB
      // is showing; there is no `user-add` in the delegated handler. Eight cases
      // here first used one, so nothing opened, both sides showed an untouched
      // form and they compared equal — the vacuous pass this gate's own
      // believability rules exist to stop. Caught by asserting the form filled.
      doc.nodes.ruAddBtn.fire('click');
    } else {
      const btn = {
        getAttribute: (k) => (k === 'data-act' ? o.form
          : k === 'data-id' ? o.formId : k === 'data-name' ? o.formName : null),
      };
      btn.closest = (sel) => (sel === '.ru-act' ? btn : null);
      doc.dispatch('click', btn);
    }
    for (const [id, v] of Object.entries(o.type || {})) {
      if (!doc.nodes[id]) throw new Error('no form field ' + id);
      if (typeof v === 'boolean') doc.nodes[id].checked = v;
      else doc.nodes[id].value = v;
    }
    if (o.save) doc.nodes[o.save].fire('click');
    // A SECOND open, after the first has been typed into.
    if (o.then) {
      const b2 = {
        getAttribute: (k) => (k === 'data-act' ? o.then.form
          : k === 'data-id' ? o.then.formId : k === 'data-name' ? o.then.formName : null),
      };
      b2.closest = (sel) => (sel === '.ru-act' ? b2 : null);
      doc.dispatch('click', b2);
    }
  }
  if (o.act) {
    const btn = {
      getAttribute: (k) => (k === 'data-act' ? o.act
        : k === 'data-id' ? o.actId : k === 'data-name' ? o.actName : null),
    };
    btn.closest = (sel) => (sel === '.ru-act' ? btn : null);
    doc.dispatch('click', btn);
  }
}

function liveRun(script, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, {
    // THE LIVE SELECTORS. `#ruTabBar .stab` carrying `data-rutab`, and the panes
    // under `#rosusersCard .brtab-panel`. Declaring the port's invented
    // `[data-ru-tab]` here would have made the gate agree with the port's bug.
    query: {
      '#ruTabBar .stab': ['users', 'groups', 'sessions'],
      // Panes are matched by ID, not by attribute — the live handler toggles
      // `active` on the one whose id is 'rutab-' + tab.
      '#rosusersCard .brtab-panel': [
        { id: 'rutab-users' }, { id: 'rutab-groups' }, { id: 'rutab-sessions' }],
    },
    queryAttr: { '#ruTabBar .stab': 'data-rutab' },
  });
  wirePolicies(doc);
  const handlers = {};
  const trail = [];
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; },
              emit: (ev, p) => { trail.push({ ev, p }); } },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    window: { confirm: (t) => { trail.push({ confirm: String(t) }); return !!o.confirm; } },
    requestAnimationFrame: (fn) => { fn(); return 0; },
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    'function _debounce(fn){return fn;}',
    L.declare(L.fileScopeEls(src, iife)),
    '(function () {' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['rosusers:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  drive(doc, (ev, p) => {
    if (!handlers[ev]) throw new Error('nothing subscribes ' + ev);
    handlers[ev](p);
  }, script, o);
  return snap(doc, (o.act || o.form) ? trail : null);
}

function portRun(script, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, {
    // THE LIVE SELECTORS. `#ruTabBar .stab` carrying `data-rutab`, and the panes
    // under `#rosusersCard .brtab-panel`. Declaring the port's invented
    // `[data-ru-tab]` here would have made the gate agree with the port's bug.
    query: {
      '#ruTabBar .stab': ['users', 'groups', 'sessions'],
      // Panes are matched by ID, not by attribute — the live handler toggles
      // `active` on the one whose id is 'rutab-' + tab.
      '#rosusersCard .brtab-panel': [
        { id: 'rutab-users' }, { id: 'rutab-groups' }, { id: 'rutab-sessions' }],
    },
    queryAttr: { '#ruTabBar .stab': 'data-rutab' },
  });
  wirePolicies(doc);
  const handlers = {};
  const trail = [];
  const prevWin = globalThis.window;
  const prevST = globalThis.setTimeout;
  const prevRaf = globalThis.requestAnimationFrame;
  globalThis.window = { confirm: (t) => { trail.push({ confirm: String(t) }); return !!o.confirm; } };
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  globalThis.requestAnimationFrame = ((fn) => { fn(); return 0; });
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initRosUsersPage({ on: (ev, fn) => { handlers[ev] = fn; },
        emit: (ev, p) => { trail.push({ ev, p }); } }, () => true);
      drive(doc, (ev, p) => {
        if (!handlers[ev]) throw new Error('the port does not subscribe ' + ev);
        handlers[ev](p);
      }, script, o);
      return snap(doc, (o.act || o.form) ? trail : null);
    });
  } finally {
    globalThis.requestAnimationFrame = prevRaf;
    globalThis.setTimeout = prevST;
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
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k,
        String(x).slice(0, 340), String(y).slice(0, 340));
    }
  }
}

// Shapes read off the live row builders, not guessed.
const U = (o) => Object.assign({
  id: '*1', name: 'admin', group: 'full', address: '', comment: '',
  // `lastLogin`, not `lastLoggedIn` — the latter was never a key either side
  // read, so this column rendered empty in every case until 2026-08-24.
  disabled: false, expired: false, lastLogin: '2026-08-24 09:00:00',
}, o);
const G = (o) => Object.assign({
  id: '*2', name: 'full', granted: ['read', 'write', 'policy'], comment: '',
}, o);
const S = (o) => Object.assign({
  id: '*3', name: 'admin', address: '198.51.100.9', via: 'winbox', group: 'full',
  when: '2026-08-24 09:00:00',
}, o);
const P = (o) => Object.assign({
  users: [], groups: [], sessions: [], self: { names: ['admin'] },
  // ── `policies` IS WHAT THE GROUP FORM DRAWS ITS BOXES FROM ─────────────
  //
  // It was absent, so `#rgf_policies` rendered EMPTY on both sides, the ticked
  // list was always `[]`, and replacing the group save's `policy` with `[]`
  // compared equal. The gate was driving the save and seeing nothing.
  //
  // A payload that lets a renderer RUN is not one that makes its RULES visible —
  // the same lesson the fixtures learned. The list is the RouterOS names the
  // form is built from, and `G({}).granted` names three of them so an edit has
  // something ticked and something not.
  policies: ['read', 'write', 'policy', 'test', 'sniff', 'romon'],
}, o);
const CAPS = { permitted: true };
const upd = (o) => [['rosusers:caps', CAPS], ['rosusers:update', P(o)]];

/**
 * The corpus.
 *
 * A case added here on 2026-08-25 reused a name three hundred lines above it,
 * the count stayed at 76, and the older case vanished from the run without a
 * word. `tools/case-name-audit.js` now checks every gate for that, so the guard
 * that briefly lived here is gone rather than duplicated thirty times.
 */
const CASES = {
  'nothing': [upd({}), {}],
  'one user': [upd({ users: [U({})] }), {}],
  'several users': [upd({ users: [U({}), U({ id: '*9', name: 'ops' })] }), {}],
  // The three-way status ladder.
  'an enabled user': [upd({ users: [U({})] }), {}],
  'a disabled user': [upd({ users: [U({ disabled: true })] }), {}],
  'an EXPIRED user is not disabled': [upd({ users: [U({ expired: true })] }), {}],
  'disabled AND expired': [upd({ users: [U({ disabled: true, expired: true })] }), {}],
  // User fields.
  'a user with an address restriction': [upd({ users: [U({ address: '198.51.100.0/24' })] }), {}],
  'a user with a comment': [upd({ users: [U({ comment: 'break glass' })] }), {}],
  'a user that has never logged in': [upd({ users: [U({ lastLoggedIn: '' })] }), {}],
  'a user with no group': [upd({ users: [U({ group: '' })] }), {}],
  // Groups.
  'one group': [upd({ groups: [G({})] }), {}],
  'a group with ONE policy': [upd({ groups: [G({ granted: ['read'] })] }), {}],
  'a group with NO policies': [upd({ groups: [G({ granted: [] })] }), {}],
  'a group with many policies': [upd({ groups: [G({ granted: [
    'read', 'write', 'policy', 'test', 'ftp', 'reboot', 'ssh'] })] }), {}],
  // Sessions.
  'one session': [upd({ sessions: [S({})] }), {}],
  'a session with no address': [upd({ sessions: [S({ address: '' })] }), {}],
  'a session with no via': [upd({ sessions: [S({ via: '' })] }), {}],
  'a session with no group': [upd({ sessions: [S({ group: '' })] }), {}],
  'several sessions': [upd({ sessions: [S({}), S({ id: '*8', name: 'ops' })] }), {}],
  // All three at once — every case draws all three tables anyway.
  'users, groups and sessions together': [upd({
    users: [U({})], groups: [G({})], sessions: [S({})] }), {}],
  // The summary's `|| '—'`: zero is shown as a dash, not a nought.
  'zero users shows a dash': [upd({ users: [] }), {}],
  'no self name': [upd({ self: {} }), {}],
  'no self at all': [upd({ self: undefined }), {}],
  'a self with several names': [upd({ self: { names: ['admin', 'root'] } }), {}],
  // Permission.
  'a viewer': [[['rosusers:caps', { permitted: false }],
    ['rosusers:update', P({ users: [U({})] })]], {}],
  // Search, across each tab's own field set.
  'search a user by name': [upd({ users: [U({}), U({ id: '*9', name: 'ops' })] }), { search: 'ops' }],
  'search a user by group': [upd({ users: [U({}), U({ id: '*9', group: 'read' })] }), { search: 'read' }],
  'search a group by policy': [upd({ groups: [G({}), G({ id: '*7', name: 'ro', granted: ['read'] })] }), { search: 'policy' }],
  'search a session by address': [upd({ sessions: [S({}), S({ id: '*8', address: '10.0.0.1' })] }), { search: '10.0.0' }],
  'search matching nothing anywhere': [upd({ users: [U({})], groups: [G({})], sessions: [S({})] }), { search: 'zzzz' }],
  // Control payloads.
  'an ok message': [[...upd({ users: [U({})] }), ['rosusers:ok', { action: 'save' }]], {}],
  'an error message': [[...upd({ users: [U({})] }), ['rosusers:error', { code: 'denied', message: 'no' }]], {}],
  // Escaping.
  'markup in a user name': [upd({ users: [U({ name: '<img src=x>' })] }), {}],
  'a quote in a comment': [upd({ users: [U({ comment: 'a"b' })] }), {}],
  'markup in a policy name': [upd({ groups: [G({ granted: ['<b>read</b>'] })] }), {}],
  // ── THE OTHER TABS ────────────────────────────────────────────────────────
  //
  // Every case above runs on the default USERS tab, so the hidden-tab render
  // path and the Add button's group label were unreachable.
  'the groups tab': [upd({ users: [U({})], groups: [G({})], sessions: [S({})] }), { tab: 'groups' }],
  'the sessions tab': [upd({ users: [U({})], groups: [G({})], sessions: [S({})] }), { tab: 'sessions' }],
  'the users tab, chosen explicitly': [upd({ users: [U({})], groups: [G({})] }), { tab: 'users' }],
  'the groups tab with nothing in it': [upd({ users: [U({})] }), { tab: 'groups' }],
  'the sessions tab for a viewer': [[['rosusers:caps', { permitted: false }],
    ['rosusers:update', P({ sessions: [S({})] })]], { tab: 'sessions' }],
  'searching from the groups tab': [upd({ users: [U({})], groups: [G({}), G({ id: '*7', name: 'ro' })] }),
    { tab: 'groups', search: 'ro' }],

  // ── THE THREE DESTRUCTIVE ACTIONS ────────────────────────────────────────
  //
  // Remove a router user, remove a group, end a live session. Each asks first,
  // and the WORDING of the question is compared as carefully as the emit: it is
  // the only place the operator is told what they are about to lose — "they will
  // no longer be able to log in", "RouterOS refuses this if any user is still in
  // it", "they will be disconnected immediately". A page that emitted correctly
  // and asked the wrong question would look identical and read as a different
  // action.
  //
  // `expectedName` rides with every one. It is the router-side guard against an
  // id that has been reused since the page was drawn, so an emit that drops or
  // mangles it removes whatever now holds that id.
  'remove a router user': [upd({ users: [U({})] }),
    { act: 'user-remove', actId: '*1', actName: 'admin', confirm: true }],
  'remove a group': [upd({ groups: [G({})] }),
    { act: 'group-remove', actId: '*2', actName: 'full', confirm: true }],
  'end a session': [upd({ sessions: [S({})] }),
    { act: 'session-remove', actId: '*3', actName: 'admin', confirm: true }],
  // CANCELLED. Nothing may reach the wire, and the row must not go busy.
  'remove a user, cancelled': [upd({ users: [U({})] }),
    { act: 'user-remove', actId: '*1', actName: 'admin', confirm: false }],
  'remove a group, cancelled': [upd({ groups: [G({})] }),
    { act: 'group-remove', actId: '*2', actName: 'full', confirm: false }],
  'end a session, cancelled': [upd({ sessions: [S({})] }),
    { act: 'session-remove', actId: '*3', actName: 'admin', confirm: false }],
  // A name with markup and one with a quote: the question is built by string
  // concatenation on both sides, so neither escapes — and both must not.
  'remove a user whose name carries markup': [upd({ users: [U({ name: '<b>a</b>' })] }),
    { act: 'user-remove', actId: '*1', actName: '<b>a</b>', confirm: true }],
  'remove a user whose name carries a quote': [upd({ users: [U({ name: 'a"b' })] }),
    { act: 'user-remove', actId: '*1', actName: 'a"b', confirm: true }],
  // An UNKNOWN action must reach neither the question nor the wire — the
  // `if (!prompts[act]) return` guard, which nothing could reach before.
  'an action neither side knows': [upd({ users: [U({})] }),
    { act: 'user-explode', actId: '*1', actName: 'admin', confirm: true }],
  // Toggling a user is a FULL SAVE on both sides and asks nothing.
  'toggle a user': [upd({ users: [U({})] }),
    { act: 'user-toggle', actId: '*1', actName: 'admin', confirm: true }],
  'toggle a user that is already disabled': [upd({ users: [U({ disabled: true })] }),
    { act: 'user-toggle', actId: '*1', actName: 'admin', confirm: true }],
  'toggle a user that is not in the payload': [upd({ users: [U({})] }),
    { act: 'user-toggle', actId: '*99', actName: 'ghost', confirm: true }],

  // ── THE USER FORM ────────────────────────────────────────────────────────
  //
  // Opening it decides a title, eight values, a password hint and a group list.
  // The hint has THREE branches and they say different things: editing says
  // "leave blank to keep", a new user under a policy says how many characters,
  // and a new user with no policy says "(required)". A form that showed the
  // wrong one tells the operator their password will be kept when it will not.
  'open the user form for a NEW user': [upd({ groups: [G({})] }), { form: 'add' }],
  'open the user form to EDIT': [upd({ users: [U({})], groups: [G({})] }),
    { form: 'user-edit', formId: '*1', formName: 'admin' }],
  'edit a user with an address and a comment':
    [upd({ users: [U({ address: '198.51.100.0/24', comment: 'noc' })], groups: [G({})] }),
     { form: 'user-edit', formId: '*1', formName: 'admin' }],
  'edit a DISABLED user': [upd({ users: [U({ disabled: true })], groups: [G({})] }),
    { form: 'user-edit', formId: '*1', formName: 'admin' }],
  // The group list omits PROTECTED groups, and marks the user's own as selected.
  'the group list hides protected groups':
    [upd({ users: [U({ group: 'ro' })],
           groups: [G({}), G({ id: '*7', name: 'ro' }), G({ id: '*8', name: 'sys', protected: true })] }),
     { form: 'user-edit', formId: '*1', formName: 'admin' }],
  'a new user under a length policy':
    [[['rosusers:caps', CAPS],
      ['rosusers:update', P({ groups: [G({})], passwordPolicy: { minLength: 12, minCategories: 2 } })]],
     { form: 'add' }],
  'a group name with markup is escaped in the picker':
    [upd({ groups: [G({ name: '<b>g</b>' })] }), { form: 'add' }],

  // SAVING. The emit is what reaches the router.
  'save a NEW user': [upd({ groups: [G({})] }),
    { form: 'add', type: { ruf_name: 'kim', ruf_group: 'full', ruf_password: 'hunter2' },
      save: 'ruf_save' }],
  // The three optional fields go as `undefined`, which JSON drops entirely —
  // the server reads `id` to decide create-or-edit and `password` to decide
  // whether one was set, so an empty string would mean something different.
  'save a new user with no password': [upd({ groups: [G({})] }),
    { form: 'add', type: { ruf_name: 'kim', ruf_group: 'full' }, save: 'ruf_save' }],
  'save an EDITED user': [upd({ users: [U({})], groups: [G({})] }),
    { form: 'user-edit', formId: '*1', formName: 'admin',
      type: { ruf_name: 'admin2' }, save: 'ruf_save' }],
  'save a user with NO NAME is refused': [upd({ groups: [G({})] }),
    { form: 'add', type: { ruf_name: '   ', ruf_group: 'full' }, save: 'ruf_save' }],
  'save a user with no GROUP is refused': [upd({ groups: [G({})] }),
    { form: 'add', type: { ruf_name: 'kim', ruf_group: '' }, save: 'ruf_save' }],
  'a name is trimmed before it is sent': [upd({ groups: [G({})] }),
    { form: 'add', type: { ruf_name: '  kim  ', ruf_group: 'full' }, save: 'ruf_save' }],
  'the disabled box rides with the save': [upd({ groups: [G({})] }),
    { form: 'add',
      type: { ruf_name: 'kim', ruf_group: 'full', ruf_disabled: true }, save: 'ruf_save' }],
  // TYPE a password, then open EDIT. `setVal('ruf_password', '')` is invisible
  // on a fresh dialog — the box is already empty — so the clear can only be seen
  // after something put a value in it. Without this, deleting that line survives.
  'Edit clears a password left in the box': [upd({ users: [U({})], groups: [G({})] }),
    { form: 'add', type: { ruf_password: 'left-behind' },
      then: { form: 'user-edit', formId: '*1', formName: 'admin' } }],
  // The Add button follows the TAB: on Groups it opens the group form.
  'Add on the groups tab opens the GROUP form': [upd({ groups: [G({})] }),
    { form: 'add', tab: 'groups' }],

  // ── THE GROUP FORM ───────────────────────────────────────────────────────
  'open the group form for a NEW group': [upd({ groups: [G({})] }), { form: 'add', tab: 'groups' }],
  'open the group form to EDIT': [upd({ groups: [G({})] }),
    { form: 'group-edit', formId: '*2', formName: 'full' }],
  'edit a group with a comment': [upd({ groups: [G({ comment: 'read only' })] }),
    { form: 'group-edit', formId: '*2', formName: 'full' }],
  'save a NEW group': [upd({ groups: [G({})] }),
    { form: 'add', tab: 'groups', type: { rgf_name: 'ro' }, save: 'rgf_save' }],
  'save a group with NO NAME is refused': [upd({ groups: [G({})] }),
    { form: 'add', tab: 'groups', type: { rgf_name: '' }, save: 'rgf_save' }],
  // The POLICY LIST is read from the ticked boxes, not from the row. Editing a
  // group whose granted list is non-empty renders them ticked, so saving must
  // send them back — a save that dropped the list would silently strip every
  // permission from the group it was opened on.
  // A policy name carrying markup. The list comes from the ROUTER via the
  // payload, so it is not this page's to trust — and escaping it is invisible
  // until a name needs escaping, which is why deleting the `esc` survived every
  // case built from real RouterOS policy names.
  'markup in a policy name, in the FORM': [[['rosusers:caps', CAPS],
    ['rosusers:update', P({ groups: [G({ granted: ['<b>x</b>'] })],
                            policies: ['read', '<b>x</b>', 'a"b'] })]],
    { form: 'group-edit', formId: '*2', formName: 'full' }],
  'save an edited group KEEPS its ticked policies': [upd({ groups: [G({})] }),
    { form: 'group-edit', formId: '*2', formName: 'full',
      type: { rgf_name: 'full' }, save: 'rgf_save' }],
};

for (const [name, [script, opts]] of Object.entries(CASES)) {
  let a, b;
  try { a = __GOLD.live(name, () => liveRun(script, opts)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(script, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(__GOLD.live('auto:11', () => liveRun(upd({ users: [U({})], groups: [G({})], sessions: [S({})] }), {})));
  assert.match(s.ruUserTable.h, /admin/, 'the live user table rendered no row');
  assert.match(s.ruGroupTable.h, /full/, 'the group table rendered no row');
  assert.match(s.ruSessionTable.h, /winbox/, 'the session table rendered no row');
  assert.equal(s.ruUserBadge.t, '1', 'the user badge is ' + s.ruUserBadge.t);
  assert.equal(s.ruSumSelf.t, 'admin', 'the self summary is ' + s.ruSumSelf.t);
}
{
  // EXPIRED is its own state. Nobody turned the account off, so calling it
  // disabled — or enabled — both mislead.
  const exp = JSON.parse(__GOLD.live('auto:10', () => liveRun(upd({ users: [U({ expired: true })] }), {}))).ruUserTable.h;
  const dis = JSON.parse(__GOLD.live('auto:9', () => liveRun(upd({ users: [U({ disabled: true })] }), {}))).ruUserTable.h;
  const on = JSON.parse(__GOLD.live('auto:8', () => liveRun(upd({ users: [U({})] }), {}))).ruUserTable.h;
  assert.match(exp, /expired/, 'an expired user did not say so');
  assert.ok(exp !== dis && exp !== on, 'expired rendered the same as disabled or enabled');
}
{
  // Only the GRANTED policies are listed.
  const s = JSON.parse(__GOLD.live('auto:7', () => liveRun(upd({ groups: [G({ granted: ['read', 'write'] })] }), {})));
  assert.match(s.ruGroupTable.h, /read/, 'a granted policy is missing');
  assert.ok(!/reboot|ftp|sniff/.test(s.ruGroupTable.h),
    'the group row listed policies it was not granted');
}
{
  // Zero users is a dash, not a nought.
  const none = JSON.parse(__GOLD.live('auto:6', () => liveRun(upd({ users: [] }), {})));
  assert.equal(none.ruSumUsers.t, '—', 'zero users summarised as ' + none.ruSumUsers.t);
  const one = JSON.parse(__GOLD.live('auto:5', () => liveRun(upd({ users: [U({})] }), {})));
  assert.equal(one.ruSumUsers.t, '1', 'one user summarised as ' + one.ruSumUsers.t);
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('rosusers-page-check: %d cases identical', checked);

// ── BELIEVABILITY: THE FORMS MUST ACTUALLY OPEN AND FILL ────────────────────
//
// Every form case above is a comparison of two snapshots, and two pages that
// never opened a form produce identical ones. That is not hypothetical: eight
// cases here first pressed a `user-add` ACT that neither side handles, and they
// passed. So the LIVE side alone is driven and the result must be discriminating.
{
  const blank = JSON.parse(__GOLD.live('auto:4', () => liveRun(upd({ groups: [G({})] }), {})));
  const added = JSON.parse(__GOLD.live('auto:3', () => liveRun(upd({ groups: [G({})] }), { form: 'add' })));
  assert.notDeepEqual(blank.ruf_title, added.ruf_title,
    'pressing Add changed no title on the LIVE side — the form never opened, and every ' +
    'form case is comparing two untouched dialogs');
  assert.match(String(added.ruf_group.h), /option/,
    'the group picker was not filled');

  const edited = JSON.parse(__GOLD.live('auto:2', () => liveRun(upd({ users: [U({})], groups: [G({})] }),
    { form: 'user-edit', formId: '*1', formName: 'admin' })));
  assert.notDeepEqual(added.ruf_title, edited.ruf_title,
    'Add and Edit produced the same title');
  assert.equal(edited.ruf_name.v, 'admin', 'Edit did not fill the name from the row');
  // The password hint's three branches must not all read the same.
  const policy = JSON.parse(__GOLD.live('auto:1', () => liveRun([['rosusers:caps', CAPS],
    ['rosusers:update', P({ groups: [G({})], passwordPolicy: { minLength: 12, minCategories: 2 } })]],
    { form: 'add' })));
  const hints = new Set([added.ruf_passHint.t, edited.ruf_passHint.t, policy.ruf_passHint.t]);
  assert.equal(hints.size, 3,
    'the password hint reads the same in two of its three branches: ' + [...hints].join(' / '));
}
