#!/usr/bin/env node
'use strict';
/**
 * The RouterOS upgrade dialog's three states, live against ported.
 *
 * ── WHY THE STATES ARE THE THING TO PIN ─────────────────────────────────────
 *
 * `updState` is what stops the dialog lying about an irreversible action. Once
 * the install command is out it cannot be called off, so the button disables,
 * the confirm box disables, and "Cancel" becomes "Close" — a Cancel still
 * offered at that point would promise something the app cannot do.
 *
 * The spinner runs through `issuing` AND `rebooting` because the interesting
 * wait is the reboot: the router acknowledges in milliseconds and is then
 * unreachable for a minute or two. The original records that without it the
 * dialog simply vanished and left nothing to explain the silence.
 *
 * The live function writes onto elements; the port returns what it would write.
 * So the live side is given a recording fake DOM and the two are compared field
 * for field.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/upgrade-dialog-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('upgrade-dialog-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function lift(decl, name, must, mustNot, max) {
  const at = src.indexOf(decl);
  if (LIFT.hasReference(ROOT)) assert.ok(at > 0, name + ' has moved in app.js');
  const end = src.indexOf('\n  }', at);
  if (LIFT.hasReference(ROOT)) assert.ok(end > at && end - at < max, name + ' is not where its anchors say');
  const body = src.slice(at, end + 4);
  // GUARDED with the two above: all four ask the live SOURCE whether the slice
  // still holds what it should. The RESULT is frozen by the caller.
  if (LIFT.hasReference(ROOT)) {
    for (const m of must) assert.ok(body.includes(m), name + ' lost: ' + m);
    for (const m of mustNot) assert.ok(!body.includes(m), name + ' over-read and took in: ' + m);
  }
  return body;
}

const stateSrc = G.value('stateSrc', () => lift('function updState(state) {', 'updState',
  ['sbtn-spin', 'Rebooting', 'Issuing', 'Update & Reboot'],
  ['socket.emit', 'updModal'], 2000));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['stateSrc', stateSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
// FROZEN — `lift` both slices and asserts, so its RESULT is the lifted value the
// comparison consumes.
const drawSrc = G.value('drawSrc', () => lift('function draw() {\n    var slot = el(\'sysUpdateAction\');', 'draw',
  ['sysUpdateBtn', 'sbtn-warn'], ['updState'], 600));
if (!drawSrc || drawSrc.length < 40) throw new Error('the recorded drawSrc is empty');

const ENTRY = path.join(ROOT, 'testdata', '.upgrade-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.upgrade-port.cjs');
fs.writeFileSync(ENTRY,
  "export { updView, updateSlotHtml, upgradeErrorText } from '../web/src/pages/upgrade';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

// The ids this gate's comparisons cover, for `element-coverage-audit`. Declared
// rather than guessed: it asks each gate what it drives, because a gate that
// merely IMPORTS a module can look like coverage it does not have.
const IDS = ['upd_go', 'upd_cancel', 'upd_pending', 'upd_confirm', 'sysUpdateAction'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

// THE FAKE RECORDS WRITES, it does not infer them. `display = ''` is what an
// explicit "show" looks like AND what an untouched element looks like, so
// reading the value back cannot tell them apart — an earlier version of this
// harness tried, and reported the `rebooting` state's deliberate show as
// untouched. A `_wrote` set is the only honest answer.
const mkEl = () => {
  const wrote = new Set();
  return {
    _wrote: wrote,
    _tc: '', get textContent() { return this._tc; },
    set textContent(v) { wrote.add('textContent'); this._tc = String(v); },
    disabled: false, innerHTML: '',
    style: {
      _d: '', get display() { return this._d; },
      set display(v) { wrote.add('display'); this._d = String(v); },
    },
  };
};

function liveState(state) {
  const nodes = { upd_go: mkEl(), upd_cancel: mkEl(), upd_pending: mkEl(), upd_confirm: mkEl() };
  const ctx = { el: (id) => nodes[id] || null };
  vm.createContext(ctx);
  vm.runInContext(stateSrc + '\nupdState(' + JSON.stringify(state) + ');', ctx);
  // UNTOUCHED IS NULL, matching the port's "leave it alone". The fake starts
  // every field empty, so a field the live function never writes reads back as
  // its initial value — which is how the `issuing` divergence was found: the
  // port forced Cancel and the pending box where the original writes neither.
  const txt = (n) => (n._wrote.has('textContent') ? n.textContent : null);
  const hid = (n) => (n._wrote.has('display') ? n.style.display === 'none' : null);
  return {
    goDisabled: nodes.upd_go.disabled,
    goText: nodes.upd_go.innerHTML || nodes.upd_go.textContent,
    goIsHtml: !!nodes.upd_go.innerHTML,
    confirmDisabled: nodes.upd_confirm.disabled,
    cancelText: txt(nodes.upd_cancel),
    confirmHidden: hid(nodes.upd_confirm),
    pendingText: txt(nodes.upd_pending),
    pendingHidden: hid(nodes.upd_pending),
  };
}

function liveSlot(permitted, latest) {
  const slot = mkEl();
  const ctx = { el: (id) => (id === 'sysUpdateAction' ? slot : null),
                _caps: { permitted, routerName: 'r' }, _upd: { latest } };
  vm.createContext(ctx);
  vm.runInContext(drawSrc + '\ndraw();', ctx);
  return slot.innerHTML;
}

let bad = 0, checked = 0;

for (const state of ['idle', 'issuing', 'rebooting']) {
  checked++;
  const a = liveState(state), b = port.updView(state);
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    bad++;
    console.error('state %s\n  live: %j\n  port: %j', state, a, b);
  }
}

for (const [permitted, latest] of [[true, '7.25'], [false, '7.25'], [true, ''], [false, '']]) {
  checked++;
  const a = liveSlot(permitted, latest), b = port.updateSlotHtml(permitted, latest);
  if (a !== b) { bad++; console.error('slot %j/%j\n  live: %j\n  port: %j', permitted, latest, a, b); }
}

// The error text: the live mapping is a chained ternary inside the handler, so
// it is compared by its OUTPUTS rather than lifted — every branch plus a code
// the map does not know.
const LIVE_TEXT = {
  'confirm-mismatch': 'That is not this router’s name. Type "r1".',
  'nothing-to-update': 'This router is already on the newest version it knows about.',
  denied: 'You do not have permission to update this router.',
  'router-write-policy': 'The RouterOS user MikroDash connects with lacks the write policy.',
  unknown: 'The router refused the upgrade.',
  '': 'The router refused the upgrade.',
};
for (const [code, want] of Object.entries(LIVE_TEXT)) {
  checked++;
  const got = port.upgradeErrorText(code, 'r1');
  if (got !== want) { bad++; console.error('error %s\n  live: %j\n  port: %j', code, want, got); }
}
// Every branch of that map must be reachable from the live source, or this
// block is asserting text nobody wrote.
// GUARDED: each asks whether the live SOURCE still contains the message, which is
// how LIVE_TEXT is kept from asserting text nobody wrote. With no source there is
// nothing for it to drift against; the messages themselves are still compared
// against the port below.
for (const want of LIFT.hasReference(ROOT) ? Object.values(LIVE_TEXT) : []) {
  assert.ok(src.includes(want.replace('r1', '')) || src.includes(want.split('"')[0]),
    'the live app does not contain this message: ' + want);
}

// BELIEVABILITY: the three states must actually differ, or the comparison above
// is three copies of one answer.
const seen = new Set(['idle', 'issuing', 'rebooting'].map((s) => JSON.stringify(liveState(s))));
assert.strictEqual(seen.size, 3, 'the three states do not produce three distinct views');

if (bad) {
  console.error('\nupgrade-dialog-check: %d of %d differ', bad, checked);
  process.exit(1);
}
console.log('upgrade-dialog-check: %d comparisons identical', checked);
