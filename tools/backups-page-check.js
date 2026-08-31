'use strict';
/**
 * The BACKUPS page, live against ported.
 *
 * Second of the gaps `tools/page-gate-audit.js` recorded, and the other one
 * that had no coverage at all.
 *
 * ── THE SHIM LEARNED SOMETHING HERE ────────────────────────────────────────
 *
 * `renderSettings` writes the header buttons as an innerHTML STRING, and
 * `_syncBulk` then looks them up by id to set `disabled`, `textContent` and
 * `title` — "Delete (3)", "Restore takes a single restore point", and so on.
 * With the usual shim `el('bkDelete')` returns null on BOTH sides, `_syncBulk`
 * no-ops on BOTH sides, and the comparison passes while seeing none of it. That
 * is a fair comparison of nothing: the gate would be green whatever the port did
 * with the bulk bar.
 *
 * So assigning innerHTML here REGISTERS any `id="…"` it contains as a real node.
 * Eight lines, and it turns the whole selection/bulk-button behaviour from
 * invisible into compared. What the shim still cannot see is stated below.
 *
 * WHAT IT CANNOT SEE: anything that is not innerHTML, textContent, value,
 * checked, disabled or title. Layout and focus.
 *
 * ── THE THREE BULK BUTTONS ARE PRESSED NOW ──────────────────────────────────
 *
 * "real event dispatch" was on that list, and it hid the two most consequential
 * paths in the application. `deleteSelected`, `restoreSelected` and `runNow`
 * were lifted from the live source as EMPTY STUBS — `function deleteSelected(){}`
 * — because this gate only compared markup. A stub is a rewrite: both sides
 * rendered the buttons, both wired them inside `render()`, and pressing one did
 * nothing on either side and compared equal.
 *
 * Restore REPLACES a router's entire configuration and reboots it. Delete
 * removes stored files that "cannot be recovered". Neither had a single case.
 *
 * Three things had to change and each was hiding the next: the shim's
 * `addEventListener` was a no-op, so nothing could be pressed; `window` did not
 * exist, so a press would have thrown; and the four real functions had to be
 * lifted instead of stubbed. The snapshot now carries a TRAIL — every question
 * asked, with its text, and every event emitted with its payload.
 *
 * The wording is compared because it is the only place the operator is told what
 * they are about to lose, and ten mutations die on it — including Restore
 * sending the router's own label instead of what was typed (which would confirm
 * any restore automatically), Restore accepting two picked rows, `acceptVersion`
 * forced true, and each warning sentence deleted in turn.

 *
 *   MIKRODASH_SRC=../MikroDash node tools/backups-page-check.js
 *
 * ---- SAVE, AND A GATE THAT LOOKED COMPLETE (2026-08-25) --------------------
 *
 * `saveSettings` was stubbed here — `function saveSettings(){}` — under a comment
 * saying it reads a form "which this gate does not build" and that
 * `backups-settings-check` owns it. Both halves were false: the form IS built and
 * compared, and that gate does not exist. `#bkSave` was the one element on this
 * page `element-coverage-audit` could not see, and that was the symptom.
 *
 * Lifting it exposed a SECOND gap. The live page has TWO document `change`
 * listeners and only the `bkPickAll` one was lifted; the other re-runs
 * `_syncBkTime` as the frequency changes. Nothing had noticed because no case had
 * ever fired a change at the settings form. The first one that did showed the
 * live side leaving the time field enabled under an hourly schedule while the
 * port disabled it — the port was right and this gate was half-lifted.
 *
 * MUTATIONS (six, all killed):
 *   drop the disabled time from the emit        8/66
 *   send rendered STATE instead of the input    8/66
 *   do not wire the Save click                  8/66
 *   offer Save to a viewer                      3/66
 *   stop re-syncing on a schedule change        2/66
 *   never render the Save button                kills by throwing `no button
 *                                               bkSave` from `press` rather than
 *                                               as a case diff — loud and named,
 *                                               and the same throw is what the
 *                                               believability block uses to
 *                                               assert a VIEWER has no Save.
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('backups-page-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// Bounded to the backups IIFE. Unbounded, `function render()` matches on nearly
// every page — the Bandwidth gate hit exactly that and the guard refused it.
const region = G.value('region', () => {
  const from = src.indexOf('(function backupsPage() {');
  assert.ok(from > 0, 'no backupsPage IIFE in app.js');
  const close = src.indexOf('\n})();', from);
  assert.ok(close > from, 'the backupsPage IIFE never closes');
  return src.slice(from, close);
});

function braceBodyIn(text, from) {
  const open = text.indexOf('{', from);
  let depth = 0;
  for (let i = open; i < text.length; i++) {
    if (text[i] === '{') depth++;
    else if (text[i] === '}') { depth--; if (!depth) return text.slice(open + 1, i); }
  }
  throw new Error('unbalanced body');
}
function slice(decl) {
  const n = region.split(decl).length - 1;
  assert.equal(n, 1, 'AMBIGUOUS anchor (' + n + '): ' + decl);
  return decl + '{' + braceBodyIn(region, region.indexOf(decl)) + '}';
}
function block(startsWith, endsWith) {
  const i = region.indexOf(startsWith);
  assert.ok(i > 0, 'not found: ' + startsWith);
  const j = region.indexOf(endsWith, i);
  assert.ok(j > i, 'never closed: ' + startsWith);
  return region.slice(i, j + endsWith.length);
}

// The change listener that OWNS the selection. Two of them are registered in
// this IIFE — one syncs the time field when the schedule changes — so it is
// selected by CONTENT rather than by position: an anchor that matched the wrong
// one would leave the live side silently unable to register a pick, which is
// precisely the failure this replaced.
function listenerContaining(marker) {
  const anchor = "document.addEventListener('change', function (ev) {";
  let from = 0;
  for (;;) {
    const i = region.indexOf(anchor, from);
    assert.ok(i > 0, 'no change listener containing ' + marker);
    const body = braceBodyIn(region, i + anchor.length - 1);
    if (body.includes(marker)) return anchor + body + '});';
    from = i + 1;
  }
}

const LIVE_FNS = [
  slice('function fmtWhen(ts) '),
  block('var OUTCOME = {', '};'),
  slice('function renderSummary(st) '),
  slice('function renderSettings(st) '),
  slice('function _syncBkTime(st) '),
  slice('function renderRows(st) '),
  slice('function render() '),
  slice('function renderDiff(d) '),
  slice('function _prunePicked(st) '),
  slice('function _syncBulk() '),
  slice('function _pickBoxes() '),
  listenerContaining('bkPickAll'),
  // THE SECOND document `change` listener, and the reason `listenerContaining`
  // takes a marker at all. Frequency drives whether the time applies, so the
  // live page re-evaluates it as the select changes rather than waiting for the
  // next payload. Only the `bkPickAll` listener was lifted, so that behaviour
  // had NO coverage — and it stayed invisible because no case had ever fired a
  // change at the settings form. The first one that did showed the live side
  // leaving the time field enabled under an hourly schedule while the port
  // disabled it: the port was right and this gate was half-lifted.
  listenerContaining('_syncBkTime'),
  // ── THE DESTRUCTIVE THREE, LIFTED RATHER THAN STUBBED ──────────────────
  //
  // These were `function deleteSelected(){}` and friends — empty stubs, because
  // this gate only compared markup. A stub is a rewrite: the buttons rendered,
  // both sides wired them inside `render()`, and pressing one did nothing on
  // either side and compared equal. Restore REPLACES a router's entire
  // configuration and reboots it; Delete removes stored files that "cannot be
  // recovered". Neither had a single case.
  // ── AND SAVE, FOR THE SAME REASON ──────────────────────────────────────
  //
  // `saveSettings` was `function saveSettings(){}` too, with a comment saying it
  // reads a settings form "which this gate does not build" and that
  // `backups-settings-check` owns it. BOTH halves were false. This gate DOES
  // build the form — `bkEnabled`, `bkSchedule`, `bkTime`, `bkKeepCount` and
  // `bkKeepDays` are in IDS and compared in every snapshot — and
  // `backups-settings-check` DOES NOT EXIST and never has: the only mention of
  // that name anywhere in the repo was the comment itself.
  //
  // So the Save emit was unowned while LOOKING owned, which is worse than the
  // acknowledged gap above it. `#bkSave` was also the one element on this page
  // `element-coverage-audit` could not see, and that was the symptom.
  slice('function saveSettings() '),
  slice('function runNow() '),
  slice('function deleteSelected() '),
  slice('function restoreSelected() '),
  slice('function askRestore(id, acceptVersion, versionNote) '),
].join('\n');
assert.ok(LIVE_FNS.includes('bk-id-pill'), 'the row slice lost its id pill');
assert.ok(LIVE_FNS.includes('bk-diff-empty'), 'the diff slice is missing');
// The lifted question must carry its warnings, or the comparison below is of two
// strings that both lost the same sentence.
assert.ok(LIVE_FNS.includes('REPLACES the entire configuration'),
  'the restore question lost its warning — the askRestore slice is wrong');
assert.ok(LIVE_FNS.includes('cannot be recovered'),
  'the delete question lost its warning — the deleteSelected slice is wrong');

const grab = (decl) => { const i = src.indexOf(decl); return src.slice(i, src.indexOf('\n', i)); };
const escSrc = G.value('escSrc', () => grab('function esc('));
const fmtBytesSrc = G.value('fmtBytesSrc', () => {
  const i = src.indexOf('function fmtBytes(');
  return src.slice(i, src.indexOf('\n}', i) + 2);
});
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['escSrc', escSrc], ['region', region], ['fmtBytesSrc', fmtBytesSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.bk-entry.ts');
fs.writeFileSync(ENTRY, "export { initBackupsPage } from '../web/src/pages/backups.js';\n");
const OUT = path.join(ROOT, 'testdata', '.bk-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['bkSumLast', 'bkSumStored', 'bkSumBytes', 'bkSumSchedule', 'bkRouterName',
  'bkEnabled', 'bkSchedule', 'bkTime', 'bkKeepCount', 'bkKeepDays', 'bkTimeHint',
  'bkSettingsActions', 'bkHistoryActions', 'bkBadge', 'bkNote', 'bkTable',
  'bkDiffTitle', 'bkDiffSummary', 'bkDiffBody', 'bkDiffModal', 'bkPickAll'];

function makeDoc() {
  const nodes = {};
  const mk = (id) => {
    const store = { innerHTML: '', textContent: '' };
    // ── LISTENERS ARE RECORDED, and `fire` dispatches them ────────────────
    //
    // `addEventListener` was a no-op here. Both sides wire Delete, Restore and
    // Run inside `render()`, so a no-op meant the three most consequential
    // buttons on the page could not be pressed at all — and because neither side
    // could, nothing looked missing.
    const listeners = {};
    const n = {
      id, style: {},
      addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
      fire: (ev) => { for (const fn of (listeners[ev] || []).slice()) fn({ target: n }); },
      classList: { _s: new Set(), add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); },
        contains(c) { return this._s.has(c); } },
    };
    Object.defineProperty(n, 'innerHTML', {
      get: () => store.innerHTML,
      set: (v) => {
        store.innerHTML = String(v);
        // ── ids inside assigned markup become real nodes ──────────────────
        // Without this, `_syncBulk` cannot find the buttons `renderSettings`
        // just wrote and the entire bulk bar goes uncompared. See the header.
        for (const m of store.innerHTML.matchAll(/\bid="([A-Za-z0-9_-]+)"/g)) {
          if (!nodes[m[1]]) nodes[m[1]] = mk(m[1]);
        }
      },
    });
    // The ONE selector `_pickBoxes` uses. Without it the select-all tri-state —
    // disabled / checked / indeterminate — is never compared on either side,
    // which is the same blind spot the id-registration above exists to close.
    n.querySelectorAll = (sel) => {
      const onlyEnabled = sel.includes(':not([disabled])');
      const out = [];
      for (const m of store.innerHTML.matchAll(/<input\b[^>]*data-bk-pick="([^"]*)"[^>]*>/g)) {
        const tag = m[0];
        const disabled = / disabled[ >]/.test(tag);
        if (onlyEnabled && disabled) continue;
        out.push({
          checked: / checked[ >]/.test(tag), disabled,
          getAttribute: (a) => (a === 'data-bk-pick' ? m[1] : null),
          closest: () => null, classList: { toggle() {} },
        });
      }
      return out;
    };
    Object.defineProperty(n, 'textContent', {
      get: () => store.textContent, set: (v) => { store.textContent = String(v); },
    });
    // ── THE IDL TYPES, BECAUSE A REAL DOM ENFORCES THEM ──────────────────────
    //
    // `value` and `title` are DOMString; `checked` and `disabled` are boolean.
    // The live page assigns `el('bkKeepCount').value = st.settings.keepCount` —
    // a NUMBER — and the browser stores "10". A shim that kept the number
    // reported all 42 cases as differing against a port that assigns a string,
    // which is a false failure of exactly the damaging kind: it looks like a
    // real find, and reading it as one would have "fixed" correct code.
    for (const [k, coerce] of [['value', String], ['title', String],
                               ['checked', Boolean], ['disabled', Boolean]]) {
      store[k] = coerce('');
      Object.defineProperty(n, k, {
        get: () => store[k], set: (v) => { store[k] = coerce(v); }, enumerable: true,
      });
    }
    return n;
  };
  for (const id of IDS) nodes[id] = mk(id);
  const listeners = {};
  return {
    nodes, listeners,
    getElementById: (id) => nodes[id] || null,
    querySelectorAll: () => [],
    addEventListener(ev, fn) { (listeners[ev] = listeners[ev] || []).push(fn); },
    createElement: () => mk(''),
  };
}

// ── DRIVING A SELECTION ─────────────────────────────────────────────────────
//
// The first version seeded the LIVE `_picked` set directly and gave the port
// nothing, so five cases "differed" because only one side had a selection. That
// is a harness fault dressed as a finding.
//
// Both sides read the selection from a `change` event on a checkbox carrying
// `data-bk-pick`, so both are driven that way. Note `Number()`: the id in
// `_picked` is NUMERIC on both sides, which is why the corpus uses numeric row
// ids — a string id becomes NaN and every selection silently collapses to one
// entry.
function pick(doc, ids) {
  for (const id of ids) {
    const box = {
      id: '', checked: true,
      hasAttribute: (a) => a === 'data-bk-pick',
      getAttribute: (a) => (a === 'data-bk-pick' ? String(id) : null),
      closest: () => null,
      classList: { toggle() {} },
    };
    for (const fn of doc.listeners.change || []) fn({ target: box });
  }
}

// Everything the two sides can disagree about, in one string.
function snap(doc, trail) {
  const n = doc.nodes;
  const pick = (id, keys) => {
    const e = n[id];
    if (!e) return null;
    const o = {};
    for (const k of keys) o[k] = e[k];
    return o;
  };
  return JSON.stringify({
    summary: {
      last: n.bkSumLast.textContent, stored: n.bkSumStored.textContent,
      bytes: n.bkSumBytes.textContent, schedule: n.bkSumSchedule.textContent,
      name: n.bkRouterName.textContent,
    },
    settings: {
      enabled: n.bkEnabled.checked, schedule: n.bkSchedule.value, time: n.bkTime.value,
      keepCount: n.bkKeepCount.value, keepDays: n.bkKeepDays.value,
      hint: n.bkTimeHint.textContent,
      disabled: [n.bkEnabled, n.bkSchedule, n.bkTime, n.bkKeepCount, n.bkKeepDays].map((e) => e.disabled),
      actions: n.bkSettingsActions.innerHTML,
      history: n.bkHistoryActions.innerHTML,
    },
    rows: { badge: n.bkBadge.textContent, note: n.bkNote.textContent, table: n.bkTable.innerHTML },
    bulk: { save: pick('bkSave', ['disabled', 'textContent']),
            all: pick('bkPickAll', ['disabled', 'checked', 'indeterminate']),
            del: pick('bkDelete', ['disabled', 'textContent', 'title']),
            rst: pick('bkRestore', ['disabled', 'title']),
            run: pick('bkRun', ['disabled']) },
    diff: { title: n.bkDiffTitle.textContent, summary: n.bkDiffSummary.textContent,
            body: n.bkDiffBody.innerHTML, open: n.bkDiffModal.classList.contains('open') },
    // ── WHAT THE PAGE ASKED, AND WHAT IT SENT ────────────────────────────
    //
    // Restore replaces a router's whole configuration and reboots it; Delete
    // removes files that "cannot be recovered". The question is the only place
    // the operator is told either of those, and none of it is markup.
    trail: trail || undefined,
  });
}

/**
 * Press one of the bulk buttons.
 *
 * BOTH SIDES WIRE THEM INSIDE `render()`, so firing the button drives the same
 * path on each — no asymmetry between calling a function here and clicking there.
 */
/**
 * Type into the settings form the way a viewer does, before a press.
 *
 * `saveSettings` reads the LIVE VALUES out of the inputs rather than the state it
 * rendered from, so a case that only sets state proves nothing about what Save
 * sends. `checked` and `value` are set separately because the enabled box is a
 * checkbox and the rest are not.
 */
function edit(doc, o) {
  if (!o.edit) return;
  for (const [id, v] of Object.entries(o.edit)) {
    const n = doc.nodes[id];
    if (!n) throw new Error('no settings field ' + id);
    if (typeof v === 'boolean') n.checked = v; else n.value = String(v);
    // AND FIRE THE CHANGE, because a viewer's edit does. Both sides listen for
    // it on the DOCUMENT and re-run `syncTime`, which is what disables the time
    // field for an hourly schedule. Setting `.value` alone left the field
    // enabled, so the "hourly still sends the disabled time" case was not
    // testing a disabled field at all — it silently became a case about an
    // ordinary one. Caught by asserting the field WAS disabled, not by the
    // comparison, which was perfectly happy.
    n.id = id;
    for (const fn of doc.listeners.change || []) fn({ target: n });
  }
}

function press(doc, o) {
  if (!o.press) return;
  const n = doc.nodes ? doc.nodes[o.press] : doc.getElementById(o.press);
  if (!n) throw new Error('no button ' + o.press);
  n.fire ? n.fire('click') : n.click();
}

function liveRun(state, opts) {
  const o = opts || {};
  const doc = makeDoc();
  const trail = [];
  const ctx = {
    String, Array, Math, Number, Object, Set, Date, JSON, encodeURIComponent, parseInt,
    document: doc,
    socket: { emit: (ev, p) => { trail.push({ ev, p }); }, on() {} },
    window: {
      confirm: (t) => { trail.push({ confirm: String(t) }); return !!o.answer; },
      prompt: (t, d) => { trail.push({ prompt: String(t), dflt: d }); return o.answer ?? null; },
    },
  };
  vm.createContext(ctx);
  vm.runInContext([
    escSrc, fmtBytesSrc,
    'function el(id){return document.getElementById(id);}',
    'var _busy = false;',
    'var _picked = new Set();',
    'var _restorable = new Set();',
    'var _state = null;',
    'var _pendingRestore = null;',
    LIVE_FNS,
    'function __state(st){ _state = st; _busy = st.running || false; render(); }',
    'function __running(){ _busy = true; render(); }',
    'function __diff(d){ renderDiff(d); }',
  ].join('\n'), ctx);
  if (state) ctx.__state(state);
  if (o.picked) { pick(doc, o.picked); ctx.__state(state); }
  if (o.afterRunning) ctx.__running();
  if (o.thenState) ctx.__state(o.thenState);
  if (o.diff) ctx.__diff(o.diff);
  edit(doc, o);
  press(doc, o);
  return snap(doc, o.press ? trail : null);
}

function portRun(state, opts) {
  const o = opts || {};
  const doc = makeDoc();
  const handlers = {};
  const trail = [];
  const socket = { on: (ev, fn) => { handlers[ev] = fn; },
                   emit: (ev, p) => { trail.push({ ev, p }); } };
  const prev = globalThis.document;
  const prevWin = globalThis.window;
  globalThis.document = doc;
  globalThis.window = {
    confirm: (t) => { trail.push({ confirm: String(t) }); return !!o.answer; },
    prompt: (t, d) => { trail.push({ prompt: String(t), dflt: d }); return o.answer ?? null; },
  };
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initBackupsPage(socket, () => true);
    if (state) handlers['backups:state'](state);
    if (o.picked) { pick(doc, o.picked); handlers['backups:state'](state); }
    if (o.afterRunning) handlers['backups:running']();
    if (o.thenState) handlers['backups:state'](o.thenState);
    if (o.diff) handlers['backups:diff'](o.diff);
    edit(doc, o);
    press(doc, o);
  } finally {
    if (prev === undefined) delete globalThis.document; else globalThis.document = prev;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
  return snap(doc, o.press ? trail : null);
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) shout('DIFF %s\n  live: %s\n  port: %s', what, a, b);
}

const S = (o) => Object.assign({
  permitted: true, label: 'hAP ax3', routerId: 'r1',
  settings: { enabled: true, schedule: 'daily', time: '02:00', keepCount: 10, keepDays: 30, timezone: 'UTC' },
  summary: { lastAt: 1756000000000, stored: 4, bytes: 123456 },
  rows: [],
}, o);
const R = (o) => Object.assign({
  id: 1, takenAt: 1756000000000, outcome: 'changed', stem: '2026-08-24T020000',
  source: 'schedule', actor: null, osVersion: '7.24', bytes: 4096, pruned: false, error: null,
}, o);

const CASES = {
  'no rows': [S({}), {}],
  'one stored row': [S({ rows: [R({})] }), {}],
  // Every outcome, including one the table has never seen.
  'outcome changed': [S({ rows: [R({ outcome: 'changed' })] }), {}],
  'outcome unchanged': [S({ rows: [R({ outcome: 'unchanged', stem: null })] }), {}],
  'outcome skipped': [S({ rows: [R({ outcome: 'skipped', stem: null })] }), {}],
  'outcome failed': [S({ rows: [R({ outcome: 'failed', stem: null, error: 'timeout' })] }), {}],
  'an UNKNOWN outcome still renders': [S({ rows: [R({ outcome: 'wedged' })] }), {}],
  // Permission is the biggest branch on the page.
  'a viewer gets no buttons': [S({ permitted: false, rows: [R({})] }), {}],
  'a viewer sees disabled inputs': [S({ permitted: false }), {}],
  'a viewer cannot pick rows': [S({ permitted: false, rows: [R({}), R({ id: 2 })] }), {}],
  // Pruned rows.
  'a pruned row': [S({ rows: [R({ pruned: true })] }), {}],
  'a pruned row with a stem': [S({ rows: [R({ pruned: true, stem: 'x' })] }), {}],
  'a row with no stem and not pruned': [S({ rows: [R({ stem: null })] }), {}],
  // Selection and the bulk bar — the part the shim change made visible.
  'one row picked': [S({ rows: [R({}), R({ id: 2 })] }), { picked: [1] }],
  'two rows picked': [S({ rows: [R({}), R({ id: 2 })] }), { picked: [1, 2] }],

  // ── SAVE, PRESSED ────────────────────────────────────────────────────────
  //
  // `saveSettings` reads the INPUTS, not the state it rendered from, so every
  // case here edits the form first. The emitted `backups:settings` payload is
  // what the trail compares — the WRITE side of the contract whose read side
  // `backup-normalize-cases` already pins.
  'save the settings as rendered': [S({}), { press: 'bkSave' }],
  'save after editing every field': [S({}), {
    press: 'bkSave',
    edit: { bkEnabled: true, bkSchedule: 'weekly', bkTime: '03:15',
            bkKeepCount: '7', bkKeepDays: '30' },
  }],
  'save with the schedule turned off': [S({}), {
    press: 'bkSave', edit: { bkEnabled: false },
  }],
  // THE QUIRK BOTH SIDES DOCUMENT: Hourly DISABLES the time field, and the time
  // is sent anyway, so a chosen time survives a trip through Hourly and back
  // rather than being silently discarded. A port that skipped a disabled input
  // would look tidier and lose the value.
  'hourly still sends the disabled time': [S({}), {
    press: 'bkSave', edit: { bkSchedule: 'hourly', bkTime: '04:45' },
  }],
  // Empty and out-of-range values go to the wire AS TYPED. Nothing on this page
  // validates them — the server's normaliser does, and it is pinned separately.
  // What the port must not do is start validating here.
  'save with an empty time': [S({}), { press: 'bkSave', edit: { bkTime: '' } }],
  'save with a zero keep count': [S({}), { press: 'bkSave', edit: { bkKeepCount: '0' } }],
  'save with a negative keep count': [S({}), { press: 'bkSave', edit: { bkKeepCount: '-1' } }],
  'save with non-numeric keeps': [S({}), {
    press: 'bkSave', edit: { bkKeepCount: 'many', bkKeepDays: '' },
  }],

  // ── THE THREE BULK BUTTONS, PRESSED ──────────────────────────────────────
  //
  // `answer` is what `window.confirm`/`window.prompt` returns: `true`/a string
  // to go ahead, `false`/`null` (the default) to cancel.
  'delete one restore point': [S({ rows: [R({}), R({ id: 2 })] }),
    { picked: [1], press: 'bkDelete', answer: true }],
  'delete several restore points': [S({ rows: [R({}), R({ id: 2 })] }),
    { picked: [1, 2], press: 'bkDelete', answer: true }],
  // The SINGULAR and PLURAL questions are different sentences, and the count in
  // the plural one is what tells the operator how much they are about to lose.
  'delete, cancelled': [S({ rows: [R({}), R({ id: 2 })] }),
    { picked: [1, 2], press: 'bkDelete', answer: false }],
  // Nothing picked: neither side may ask, and neither may send.
  'delete with nothing picked': [S({ rows: [R({})] }), { press: 'bkDelete', answer: true }],

  // RESTORE — the most consequential path in the app.
  'restore one backup': [S({ rows: [R({}), R({ id: 2 })] }),
    { picked: [1], press: 'bkRestore', answer: 'hAP ax3' }],
  'restore, confirmed with the WRONG name — the server decides':
    [S({ rows: [R({})] }), { picked: [1], press: 'bkRestore', answer: 'not-this-router' }],
  'restore, cancelled': [S({ rows: [R({})] }), { picked: [1], press: 'bkRestore', answer: null }],
  'restore, confirmed with an empty string':
    [S({ rows: [R({})] }), { picked: [1], press: 'bkRestore', answer: '' }],
  // TWO picked is not one: Restore takes exactly one and must refuse otherwise.
  'restore with two picked': [S({ rows: [R({}), R({ id: 2 })] }),
    { picked: [1, 2], press: 'bkRestore', answer: 'hAP ax3' }],
  'restore with nothing picked': [S({ rows: [R({})] }), { press: 'bkRestore', answer: 'hAP ax3' }],
  // The question names the ROUTER, so a label with markup or a quote in it has
  // to survive into the prompt unescaped on both sides.
  'restore on a router whose label carries markup':
    [S({ label: '<b>r</b>', rows: [R({})] }),
     { picked: [1], press: 'bkRestore', answer: '<b>r</b>' }],

  'run a backup now': [S({ rows: [R({})] }), { press: 'bkRun', answer: true }],
  // Run while one is ALREADY running: the button is disabled, and a disabled
  // button that still fires is a second backup nobody asked for.
  'run while already running': [S({ running: true, rows: [R({})] }),
    { press: 'bkRun', answer: true }],
  'a picked row that cannot be restored': [
    S({ rows: [R({ id: 1, stem: null })] }), { picked: [1] }],
  'a picked id that no longer exists': [S({ rows: [R({ id: 2 })] }), { picked: [999] }],
  // BUSY ARRIVES TWO WAYS, and they are not interchangeable. `backups:state`
  // assigns `busy = st.running || false` on BOTH sides, so a state payload
  // RESETS it — the first version of this harness injected a sticky `_busy` on
  // the live side and drove the port through that reset, then reported the
  // resulting difference as a port defect. It was the harness.
  'busy from the state payload': [S({ running: true, rows: [R({})] }), {}],
  'busy from a run starting after the state': [S({ rows: [R({})] }), { afterRunning: true }],
  'a state payload CLEARS a stale busy': [S({ running: false, rows: [R({})] }), {}],
  'busy AND picked': [S({ running: true, rows: [R({})] }), { picked: [1] }],
  // The case that made a mutant survive: nothing in the corpus ever CLEARED a
  // busy flag that was actually set. `busy = busy || st.running` passed happily,
  // and it is the exact shape that leaves "Backing up…" on screen forever after
  // a run finishes — the run ends, the fresh state says running:false, and a
  // sticky flag ignores it.
  'a finished run clears busy': [S({ rows: [R({})] }),
    { afterRunning: true, thenState: S({ running: false, rows: [R({})] }) }],
  // Settings and the time hint.
  'schedule hourly hides the time': [S({ settings: { enabled: true, schedule: 'hourly', time: '02:00', keepCount: 5, keepDays: 7, timezone: 'UTC' } }), {}],
  'no time set': [S({ settings: { enabled: true, schedule: 'daily', time: '', keepCount: 5, keepDays: 7, timezone: 'UTC' } }), {}],
  'no timezone falls back to server time': [S({ settings: { enabled: true, schedule: 'daily', time: '03:00', keepCount: 5, keepDays: 7, timezone: '' } }), {}],
  'backups disabled reads Off': [S({ settings: { enabled: false, schedule: 'weekly', time: '02:00', keepCount: 5, keepDays: 7, timezone: 'UTC' } }), {}],
  'schedule capitalised': [S({ settings: { enabled: true, schedule: 'weekly', time: '02:00', keepCount: 5, keepDays: 7, timezone: 'UTC' } }), {}],
  'keepCount zero is not absent': [S({ settings: { enabled: true, schedule: 'daily', time: '02:00', keepCount: 0, keepDays: 0, timezone: 'UTC' } }), {}],
  // Summary edge cases.
  'never run': [S({ summary: { lastAt: 0, stored: 0, bytes: 0 } }), {}],
  'no summary key': [S({ summary: undefined }), {}],
  'no label': [S({ label: '' }), {}],
  // Row detail and escaping.
  'an error containing markup': [S({ rows: [R({ outcome: 'failed', stem: null, error: '<b>&x</b>' })] }), {}],
  'a stem containing a quote': [S({ rows: [R({ stem: 'a"b' })] }), {}],
  'a manual run with an actor': [S({ rows: [R({ source: 'manual', actor: 'kim' })] }), {}],
  'a manual run with no actor': [S({ rows: [R({ source: 'manual', actor: null })] }), {}],
  'no os version': [S({ rows: [R({ osVersion: null })] }), {}],
  'no bytes': [S({ rows: [R({ bytes: 0 })] }), {}],
  'a routerId needing encoding': [S({ routerId: 'a b&c', rows: [R({})] }), {}],
  'several rows': [S({ rows: [R({}), R({ id: 2, outcome: 'unchanged', stem: null }), R({ id: 3, pruned: true })] }), {}],
  // The diff modal, all four branches.
  'diff: baseline': [S({}), { diff: { baseline: true, hunks: [] } }],
  'diff: truncated': [S({}), { diff: { truncated: true, hunks: [] } }],
  'diff: no differences': [S({}), { diff: { hunks: [], added: 0, removed: 0 } }],
  'diff: one hunk': [S({}), { diff: { added: 1, removed: 1, hunks: [
    { aStart: 1, aCount: 2, bStart: 1, bCount: 2, lines: [
      { op: '-', text: 'old', aLine: 1 }, { op: '+', text: 'new', bLine: 1 },
      { op: ' ', text: 'same', aLine: 2 }] } ] } }],
  'diff: markup in a line': [S({}), { diff: { added: 1, removed: 0, hunks: [
    { aStart: 1, aCount: 1, bStart: 1, bCount: 1,
      lines: [{ op: '+', text: '<script>x</script>', bLine: 1 }] } ] } }],
  'diff: a line with no number': [S({}), { diff: { added: 1, removed: 0, hunks: [
    { aStart: 0, aCount: 0, bStart: 1, bCount: 1, lines: [{ op: '+', text: 'x' }] } ] } }],
};

for (const [name, [state, opts]] of Object.entries(CASES)) {
  cmp(name, liveRun(state, opts), portRun(state, opts));
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(liveRun(S({ rows: [R({})] }), {}));
  assert.match(s.rows.table, /bk-id-pill/, 'the live table produced no rows');
  assert.match(s.settings.history, /bkRestore/, 'the history buttons are missing');
  assert.ok(s.bulk.del, 'the shim did not register bkDelete — _syncBulk saw nothing');
  assert.equal(s.bulk.del.disabled, true, 'Delete is enabled with nothing selected');
}
{
  // The shim change is load-bearing: with two picked, Delete must COUNT them.
  const s = JSON.parse(liveRun(S({ rows: [R({}), R({ id: 2 })] }), { picked: [1, 2] }));
  assert.equal(s.bulk.del.textContent, 'Delete (2)', 'Delete did not count: ' + s.bulk.del.textContent);
  assert.match(s.bulk.rst.title, /select just one/, 'Restore did not refuse a multi-selection');
}
{
  const s = JSON.parse(liveRun(S({ permitted: false, rows: [R({})] }), {}));
  assert.equal(s.settings.actions, '', 'a viewer was offered a Save button');
  assert.equal(s.settings.history, '', 'a viewer was offered history buttons');
  assert.ok(!/data-bk-diff/.test(s.rows.table) === false, 'a viewer lost the Changes button');
}
{
  // ── SAVE ACTUALLY EMITS ──────────────────────────────────────────────────
  //
  // This is the assertion that would have caught the stub. `saveSettings` was
  // `function saveSettings(){}` here, so pressing Save emitted NOTHING and every
  // case comparing the trail would have agreed on an empty one.
  const s = JSON.parse(liveRun(S({}), {
    press: 'bkSave',
    edit: { bkEnabled: true, bkSchedule: 'weekly', bkTime: '03:15',
            bkKeepCount: '7', bkKeepDays: '30' },
  }));
  const sent = (s.trail || []).find((t) => t.ev === 'backups:settings');
  assert.ok(sent, 'the LIVE Save button emitted no backups:settings — it is stubbed or unwired, ' +
    'and every save case above is comparing two silences');
  assert.deepEqual(sent.p, {
    enabled: true, schedule: 'weekly', time: '03:15', keepCount: '7', keepDays: '30',
  }, 'the LIVE Save sent something other than the values typed into the form');

  // And the QUIRK, asserted on the live side so the case that pins it is known
  // to be pinning a real behaviour: Hourly disables the time field, and the time
  // goes to the wire regardless.
  const h = JSON.parse(liveRun(S({}), {
    press: 'bkSave', edit: { bkSchedule: 'hourly', bkTime: '04:45' },
  }));
  const hs = (h.trail || []).find((t) => t.ev === 'backups:settings');
  assert.equal(hs && hs.p.time, '04:45',
    'the LIVE Save dropped the time when the schedule was hourly — both sides comment that it ' +
    'is sent on purpose, so a chosen time survives a trip through Hourly and back');
  assert.equal(h.settings.disabled[2], true,
    'the time field was NOT disabled by hourly — the quirk above is then not the quirk, ' +
    'and this case is testing nothing');

  // A VIEWER HAS NO SAVE BUTTON AT ALL. `press` throws if it is missing, which
  // is the check: it must not be there.
  assert.throws(() => liveRun(S({ permitted: false }), { press: 'bkSave' }),
    /no button bkSave/, 'a viewer was offered a working Save button');
}
{
  const s = JSON.parse(liveRun(S({}), { diff: { baseline: true, hunks: [] } }));
  assert.match(s.diff.body, /No earlier backup/, 'the baseline branch did not render');
  assert.equal(s.diff.open, true, 'the diff modal did not open');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('backups-page-check: %d cases identical', checked);
