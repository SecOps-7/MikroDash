'use strict';
/**
 * Undo and redo, and the drag that shares their machinery.
 *
 * The inversion model is pure and gets ordinary assertions. The handlers are
 * covered by source scan, for the reason given at the top of
 * test/resource-writes.test.js: what matters about them is structural — that a
 * gate is present, that the read is fresh, that the browser cannot name a
 * position.
 */

const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const H = require('../src/routeros/history');
const R = require('../src/routeros/resources');

const SRC = (...p) => fs.readFileSync(path.join(__dirname, '..', 'src', ...p), 'utf8');
const APP = () => fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
const HTML = () => fs.readFileSync(path.join(__dirname, '..', 'public', 'index.html'), 'utf8');
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

const route = R.byKey('route');
const fw    = R.byKey('fwFilter');

// ── Every write knows how to undo itself ─────────────────────────────────────

test('a create is undone by removing what it made', () => {
  const e = H.buildEntry({ resource: route, what: 'create', id: '*A3',
                           identity: '192.0.2.0/24', after: { gateway: '10.0.0.1' } });
  assert.equal(e.forward.op, 'add');
  assert.equal(e.reverse.op, 'remove');
  assert.equal(e.reverse.id, '*A3');
  assert.deepEqual(e.forward.values, { gateway: '10.0.0.1' });
});

test('a delete is undone by putting the row back where it was', () => {
  const e = H.buildEntry({ resource: fw, what: 'delete', id: '*7',
                           identity: 'input', before: { chain: 'input', action: 'drop' },
                           anchorBefore: '*8' });
  assert.equal(e.forward.op, 'remove');
  assert.equal(e.reverse.op, 'add');
  assert.deepEqual(e.reverse.values, { chain: 'input', action: 'drop' });
  assert.equal(e.reverse.anchor, '*8', 'position must be restored, not appended');
});

test('an edit is undone by writing the previous values back', () => {
  const e = H.buildEntry({ resource: route, what: 'update', id: '*A3', identity: 'x',
                           before: { distance: '1' }, after: { distance: '9' } });
  assert.equal(e.forward.op, 'set');
  assert.equal(e.reverse.op, 'set');
  assert.deepEqual(e.forward.values, { distance: '9' });
  assert.deepEqual(e.reverse.values, { distance: '1' });
  assert.equal(e.forward.id, e.reverse.id);
});

test('a move is undone by the anchor it came from', () => {
  const e = H.buildEntry({ resource: fw, what: 'move', id: '*7', identity: 'x',
                           anchorBefore: '*2', anchorAfter: '*9' });
  assert.equal(e.reverse.anchor, '*2');
  assert.equal(e.forward.anchor, '*9');
});

test('enable and disable invert each other', () => {
  const on  = H.buildEntry({ resource: fw, what: 'enable',  id: '*7', identity: 'x' });
  const off = H.buildEntry({ resource: fw, what: 'disable', id: '*7', identity: 'x' });
  assert.equal(on.reverse.op, 'disable');
  assert.equal(off.reverse.op, 'enable');
});

test('an action with no inverse is not recorded at all', () => {
  // make-static cannot be undone: there is no "make dynamic".
  assert.equal(H.buildEntry({ resource: R.byKey('dhcpLease'), what: 'makeStatic',
                              id: '*1', identity: 'AA:BB' }), null);
});

test('every operation an entry can carry is one index.js knows', () => {
  for (const what of ['create', 'delete', 'update', 'move', 'enable', 'disable']) {
    const e = H.buildEntry({ resource: fw, what, id: '*1', identity: 'x',
                             before: {}, after: {} });
    for (const op of [e.forward, e.reverse])
      assert.ok(H.OPS.includes(op.op), `${what} produced unknown op "${op.op}"`);
  }
});

test('re-adding a row rebinds both halves to its new id', () => {
  // RouterOS assigns the id, so the remove that redoes a restored delete has to
  // be told what to address.
  const e = H.buildEntry({ resource: fw, what: 'delete', id: '*7', identity: 'x',
                           before: { chain: 'input' } });
  H.rebind(e, '*99');
  assert.equal(e.forward.id, '*99');
  assert.equal(e.reverse.op, 'add', 'an add still has no id of its own');
});

test('the label reads as a sentence, with no raw separator in it', () => {
  const l = H.label(fw, 'delete', ['input', 'drop', '', '', 'block lan'].join(H.SEP));
  assert.ok(l.startsWith('delete of '));
  assert.ok(!l.includes(H.SEP), 'the composite separator must not reach a tooltip');
  assert.ok(l.includes('block lan'));
});

// ── The handlers ─────────────────────────────────────────────────────────────

function histBody(src) {
  const at = src.indexOf('── Undo / redo');
  assert.ok(at > 0, 'the history block is gone');
  const end = src.indexOf('const _resolve', at);
  assert.ok(end > at);
  return src.slice(at, end);
}

test('undo and redo are registered, queued and gated', () => {
  const src = SRC('index.js');
  assert.ok(/socket\.on\('res:undo', _histRun\('undo'\)\)/.test(src));
  assert.ok(/socket\.on\('res:redo', _histRun\('redo'\)\)/.test(src));
  const body = stripComments(histBody(src));
  assert.ok(/_routerWriteQueue\(socket\.routerId/.test(body), 'an undo is a write like any other');
  assert.ok(/!_resMayWrite\(rid, resource\)/.test(body), 'both gates');
  assert.ok(/audit\.fromSocket\(socket\)\.denied\(/.test(body));
  assert.ok(/audit\.fromSocket\(socket\)\.record\(/.test(body));
  assert.ok(!body.includes('lastPayload'), 'the row must come from a fresh read');
});

test('an undo runs the same guard as the write it reverses', () => {
  // Undoing the deletion of a `drop` rule puts that rule back, and it can lock
  // us out exactly as the original did.
  const body = stripComments(histBody(SRC('index.js')));
  const gate = body.indexOf('_resAckGate');
  const apply = body.indexOf('_applyOp(session, resource, op)');
  assert.ok(gate > 0 && apply > 0);
  assert.ok(gate < apply, 'the guard must run before the operation');
});

test('a history that no longer describes the router is dropped, not applied', () => {
  const body = stripComments(histBody(SRC('index.js')));
  assert.ok(/Resources\.identityOf\(resource, row\) !== entry\.identity/.test(body),
    'the row must still be the row the entry is about');
  assert.ok(/op\.anchor && !rows\.some/.test(body),
    'an anchor that has gone cannot restore a position');
  const drops = (body.match(/_histDrop\(resource\.key\)/g) || []).length;
  assert.ok(drops >= 2, `expected the whole stack to be dropped on staleness, found ${drops}`);
});

test('a new action forks the timeline', () => {
  const body = stripComments(histBody(SRC('index.js')));
  assert.ok(/h\.redo\.length = 0/.test(body),
    'what was undone cannot be redone on top of something else');
});

test('the stack is bounded and dies with the session', () => {
  const src = SRC('index.js');
  assert.ok(/_HIST_DEPTH = \d+/.test(src), 'an unbounded stack is a leak');
  assert.ok(/h\.undo\.length > _HIST_DEPTH\) h\.undo\.shift\(\)/.test(src));
  // Every entry names a row on the router being left.
  assert.ok(/socket\._resHist = \{\};/.test(src), 'a router switch must drop the history');
});

test('history is per resource, so undo on one card cannot reach another', () => {
  const body = stripComments(histBody(SRC('index.js')));
  assert.ok(/socket\._resHist\[key\]/.test(body), 'the stacks must be keyed by resource');
});

test('an add finds its new row by diffing, not by assuming it landed last', () => {
  const body = stripComments(histBody(SRC('index.js')));
  assert.ok(/const seen = new Set\(\(await _resRead\(session, resource\)\)\.map/.test(body),
    'the table must be read before the add, to diff against');
  assert.ok(/!seen\.has\(\w+\['\.id'\]\)/.test(body),
    'RouterOS assigns the id; "usually last" is not a thing to build an undo on');
});

// ── Drag ─────────────────────────────────────────────────────────────────────

function dragBody() {
  const app = APP();
  const at = app.indexOf('── Drag to reorder');
  assert.ok(at > 0, 'the drag block is gone');
  // Bounded by what follows it, not by a character count — the block grows.
  const end = app.indexOf("var saveBtn = el('res_save')", at);
  assert.ok(end > at, 'the drag block no longer ends where it did');
  return app.slice(at, end);
}

test('the drag uses pointer events, as the rest of this codebase does', () => {
  const body = dragBody();
  assert.ok(/pointerdown/.test(body) && /pointermove/.test(body) && /pointerup/.test(body));
  assert.ok(!/dragstart|dataTransfer/.test(body),
    'HTML5 drag-and-drop does not work on touch and is not what dashboard-grid.js uses');
  assert.ok(/setPointerCapture/.test(body));
});

test('a drag reports an anchor, and an empty one means the end of the table', () => {
  const body = dragBody();
  assert.ok(/anchor: next \? next\.getAttribute\('data-id'\) : ''/.test(body),
    "the end of the table is '' — the server tells it from absent by the key");
  assert.ok(!/position|toIndex/.test(stripComments(body)),
    'the browser must not compute an ordinal');
});

test('only a viewer who may write can start a drag', () => {
  assert.ok(/!_schema\[key\] \|\| !_schema\[key\]\.permitted/.test(dragBody()));
});

test('the server accepts an anchor and still refuses an index', () => {
  const src = SRC('index.js');
  const at = src.indexOf("socket.on('res:move'");
  const body = stripComments(src.slice(at, src.indexOf('── WiFi frequency analyzer', at)));
  assert.ok(/hasOwnProperty\.call\(r, 'anchor'\)/.test(body));
  assert.ok(!/r\.position|r\.index|r\.destination|toIndex/.test(body));
});

// ── The buttons ──────────────────────────────────────────────────────────────

test('undo and redo are grey until there is something to reverse', () => {
  const app = APP();
  assert.ok(/function histButton/.test(app), 'the buttons are gone');
  const body = app.slice(app.indexOf('function histButton'), app.indexOf('function histButton') + 700);
  assert.ok(/on \? 'sbtn-primary' : 'sbtn-ghost'/.test(body), 'blue when live, grey when not');
  assert.ok(/\(on \? '' : ' disabled'\)/.test(body), 'a dead button must not be clickable');
  assert.ok(/\.res-hist:disabled\{[^}]*pointer-events:none/.test(HTML()));
});

test('the page never decides what undo does — it only asks', () => {
  const app = APP();
  const at = app.indexOf('function doHist');
  assert.ok(at > 0);
  const body = app.slice(at, at + 300);
  // The browser names a resource and nothing else; which entry, and what
  // reversing it means, is the server's to know.
  assert.ok(/socket\.emit\('res:' \+ kind, \{ resource: key/.test(body));
  assert.ok(!/values|op:|anchor/.test(body), 'the browser must not describe the operation');
});

test('a card with two resources undoes the one that has something to undo', () => {
  const app = APP();
  assert.ok(/function histTarget/.test(app));
  const body = app.slice(app.indexOf('function histTarget'), app.indexOf('function histTarget') + 400);
  assert.ok(/h\.canUndo \|\| h\.canRedo/.test(body));
});

// ── The pulse ────────────────────────────────────────────────────────────────

test('a moved row is pulsed, including when the move came from undo', () => {
  const app = APP();
  assert.ok(/_fwPulse/.test(app), 'the pulse cue is gone');
  assert.ok(/d\.action==='move'\|\|d\.action==='undo'\|\|d\.action==='redo'/.test(app),
    'an undo moves a row too, and needs the same cue');
  assert.ok(/_fwPulse=null;/.test(app), 'one pulse per move, not one per counter tick');
  const html = HTML();
  assert.ok(/@keyframes fwPulse/.test(html));
  assert.ok(/prefers-reduced-motion/.test(html),
    'an animation that cannot be turned off is an accessibility problem');
});

test('the server tells the page which row moved', () => {
  const src = SRC('index.js');
  assert.ok(/movedId: String\(r\.id\)/.test(src), 'a move must say what moved');
  assert.ok(/movedId: out\.id \|\| null/.test(src), 'so must an undo');
});
