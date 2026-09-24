/**
 * ADDING A CARD PUTS IT SOMEWHERE FREE.
 *
 * ── THE BUG THIS EXISTS FOR ─────────────────────────────────────────────────
 *
 * `findFreeSlot` scanned a fixed 24×22 grid and fell back to 1,1 when nothing
 * fit, on the reasoning that a visible overlapping card beats a silent refusal.
 * Measured on 2026-09-10: the nine cards a default install ships leave 24 free
 * cells of 528, and not one free RECTANGLE big enough for any of the fourteen
 * hidden ones. So the fallback was not the rare case - it was the ONLY case, and
 * every card added from the Add Card panel landed on top of the traffic chart.
 *
 * The grid grows downward now, `ROWS` being its floor rather than its ceiling.
 *
 * ── WHY IT IS DRIVEN THROUGH `addCard` AND NOT `findFreeSlot` ───────────────
 *
 * `grid-wiring.test.ts` opens and closes the Add panel and never clicks a chip,
 * so `addCard` had no coverage at all - which is how a placement bug this total
 * survived. Testing the arithmetic alone would have passed against the broken
 * build too, because the arithmetic was doing exactly what it was written to do.
 *
 * ── AND WHY IT USES THE SHIPPED LAYOUT, NOT A FIXTURE ───────────────────────
 *
 * The bug is a property of `DEFAULT_LAYOUT`, not of the algorithm in the
 * abstract: on a sparse synthetic grid the old code was already correct. A test
 * on invented cards would have gone green throughout.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.grid-add-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { createGridEditor } from '../web/src/pages/dashboard-grid-edit.js';",
  "export { gridRows, findFreeSlot, inBounds, rectOverlaps, cloneLayout, repairOverlaps, mergeLayout }",
  "  from '../web/src/pages/dashboard-grid-layout.js';",
  "export { DEFAULT_LAYOUT, ROWS, COLS } from '../web/src/gen/grid-tables.js';",
  '',
].join('\n'));
const OUT = path.join(ROOT, 'testdata', '.grid-add.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

let checks = 0;
function ok(cond: unknown, msg: string) { assert.ok(cond, msg); checks++; }

/** The ids `addCard` reaches for, plus one node per card so `applyLayout` runs. */
function boot() {
  const mod = require(OUT);
  const ids = ['dash-grid-root', 'dashAddPanel', 'page-dashboard']
    .concat(mod.DEFAULT_LAYOUT.map((c: any) => c.id));
  const doc = makeDoc(ids);
  const prevDoc = global.document;
  const prevCE = (global as any).CustomEvent;
  global.document = doc;
  // Room notifications go out as CustomEvents. A stub is enough: this test is
  // about placement, and `grid-wiring.test.ts` owns the room bookkeeping.
  (global as any).CustomEvent = class { type: string; detail: unknown;
    constructor(t: string, o: any) { this.type = t; this.detail = o && o.detail; } };
  const editor = mod.createGridEditor(mod.cloneLayout(mod.DEFAULT_LAYOUT));
  return {
    mod, doc, editor,
    root: doc.getElementById('dash-grid-root'),
    restore: () => { global.document = prevDoc; (global as any).CustomEvent = prevCE; },
  };
}

const overlaps = (a: any, b: any) =>
  a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;

function firstOverlap(layout: any[]): string {
  const vis = layout.filter((c) => c.visible);
  for (let i = 0; i < vis.length; i++) {
    for (let j = i + 1; j < vis.length; j++) {
      if (overlaps(vis[i], vis[j])) {
        return vis[i].id + ' (' + vis[i].x + ',' + vis[i].y + ' ' + vis[i].w + 'x' + vis[i].h +
          ') overlaps ' + vis[j].id + ' (' + vis[j].x + ',' + vis[j].y + ' ' +
          vis[j].w + 'x' + vis[j].h + ')';
      }
    }
  }
  return '';
}

// ── 1. THE PRECONDITION. If this ever stops holding, the bug below cannot be
//      reproduced and this whole file is measuring nothing - so it is asserted
//      rather than assumed.
{
  const { mod, restore } = boot();
  const vis = mod.DEFAULT_LAYOUT.filter((c: any) => c.visible);
  const hidden = mod.DEFAULT_LAYOUT.filter((c: any) => !c.visible);
  ok(vis.length > 0 && hidden.length > 0,
    'the shipped layout has no hidden cards, so Add Card has nothing to place');

  // Would any hidden card have fit inside the ORIGINAL fixed grid?
  const fitsInFixedGrid = hidden.some((c: any) => {
    const w = c.w || 3, h = c.h || 2;
    for (let row = 1; row <= mod.ROWS - h + 1; row++) {
      for (let col = 1; col <= mod.COLS - w + 1; col++) {
        if (!vis.some((v: any) => overlaps({ x: col, y: row, w, h }, v))) return true;
      }
    }
    return false;
  });
  ok(!fitsInFixedGrid,
    'a hidden card now fits inside the fixed ' + mod.COLS + 'x' + mod.ROWS + ' grid. The ' +
    'default layout has changed, so the fallback this file exists for is no longer ' +
    'certain - re-measure before trusting the rest of these checks');
  restore();
}

// ── 2. EVERY hidden card, added in panel order, lands somewhere free ────────
{
  const { mod, editor, restore } = boot();
  const hidden = editor.getLayout().filter((c: any) => !c.visible).map((c: any) => c.id);
  ok(hidden.length >= 10, 'expected the shipped layout to hide at least ten cards');

  for (const id of hidden) {
    editor.addCard(id);
    const bad = firstOverlap(editor.getLayout());
    ok(!bad, 'after adding ' + id + ': ' + bad);
  }

  const all = editor.getLayout();
  ok(all.every((c: any) => c.visible), 'a card was not made visible by addCard');
  ok(all.every((c: any) => c.x >= 1 && c.y >= 1 && c.x + c.w - 1 <= mod.COLS),
    'a card was placed outside the grid horizontally');
  restore();
}

// ── 3. A hole is filled before the dashboard gets longer ───────────────────
//
// Appending unconditionally would also produce no overlaps, and would leave the
// grown area full of gaps while the page grew for every card.
{
  const { mod, restore } = boot();
  // A grid already grown to 30 rows with a deliberate 4x4 hole at 1,23.
  const layout = [
    { id: 'a', x: 1, y: 1, w: 24, h: 22, visible: true },
    { id: 'b', x: 5, y: 23, w: 20, h: 8, visible: true },
  ];
  ok(mod.gridRows(layout) === 30, 'precondition: gridRows should be 30');
  const slot = mod.findFreeSlot(layout, 4, 4);
  ok(slot.x === 1 && slot.y === 23,
    'a 4x4 card went to ' + slot.x + ',' + slot.y + ' instead of the free hole at 1,23 - ' +
    'the grid lengthens for a card that had somewhere to go');
  restore();
}

// ── 4. No room means a fresh band below, at column 1 ───────────────────────
{
  const { mod, restore } = boot();
  const full = [{ id: 'a', x: 1, y: 1, w: 24, h: 22, visible: true }];
  const slot = mod.findFreeSlot(full, 8, 4);
  ok(slot.y === 23, 'a card on a full grid went to row ' + slot.y + ', want 23 (below everything)');
  ok(slot.x === 1, 'a card starting a new band went to column ' + slot.x + ', want 1');
  // The old behaviour, pinned so a revert is loud rather than quiet.
  ok(!(slot.x === 1 && slot.y === 1),
    'a card on a full grid was placed at 1,1 - that is the overlap this file exists to prevent');
  restore();
}

// ── 5. `ROWS` IS A FLOOR, so an untouched dashboard is untouched ───────────
//
// The cost of growing must be paid only by someone who adds a card. If the
// track list changed for everyone, every existing card would be resized.
{
  const { mod, editor, root, restore } = boot();
  ok(mod.gridRows(editor.getLayout()) === mod.ROWS,
    'the shipped layout already needs more than ' + mod.ROWS + ' rows');
  ok(!root.style.gridTemplateRows,
    'an untouched dashboard carries an inline row template, so the stylesheet is ' +
    'no longer what sizes it');

  // A layout that fits well inside the grid still reports the floor.
  ok(mod.gridRows([{ id: 'a', x: 1, y: 1, w: 2, h: 2, visible: true }]) === mod.ROWS,
    'a nearly empty layout reported fewer than ' + mod.ROWS + ' rows, which would shrink the grid');
  // A HIDDEN card cannot hold the grid open. It is not on the dashboard.
  ok(mod.gridRows([{ id: 'a', x: 1, y: 40, w: 2, h: 2, visible: false }]) === mod.ROWS,
    'a hidden card at row 40 still lengthens the grid');
  restore();
}

// ── 6. Growing writes an explicit track list, and stops when it is not needed ─
//
// Implicit tracks are sized by `grid-auto-rows`, not by the explicit `1fr`
// list, so a card in one would be a different height from every other row - and
// the drag arithmetic assumes uniform rows.
{
  const { mod, editor, root, restore } = boot();
  const hidden = editor.getLayout().filter((c: any) => !c.visible).map((c: any) => c.id);
  editor.addCard(hidden[0]);
  const rows = mod.gridRows(editor.getLayout());
  ok(rows > mod.ROWS, 'precondition: adding a card should have grown the grid');
  ok(root.style.gridTemplateRows === 'repeat(' + rows + ', minmax(40px, 1fr))',
    'the grown grid has no explicit track list, so the new card sits in an implicit row: ' +
    JSON.stringify(root.style.gridTemplateRows));

  editor.removeCard(hidden[0]);
  ok(!root.style.gridTemplateRows,
    'the grid stayed long after the card that needed it was removed');
  restore();
}

// ── 7. Bounds follow the grown grid, so the card can be dragged afterwards ──
//
// A card placed at row 23 that `inBounds` refuses is a card the operator cannot
// move - which would be a worse bug than the one being fixed.
{
  const { mod, restore } = boot();
  ok(mod.inBounds(1, 23, 8, 4, 30), 'a card in the grown area is out of bounds on a 30-row grid');
  ok(!mod.inBounds(1, 23, 8, 4, mod.ROWS),
    'the row count is being ignored: row 23 is inside a ' + mod.ROWS + '-row grid');
  ok(!mod.inBounds(1, 28, 8, 4, 30), 'a card past the bottom of a 30-row grid is in bounds');
  restore();
}

// ── 8. A layout the OLD code damaged is repaired on the way in ─────────────
//
// Fixing placement does nothing for a dashboard that is already broken: the bad
// position is saved. An operator who used Add Card before the fix would open
// their dashboard afterwards and still find a card on the traffic chart.
{
  const { mod, restore } = boot();
  // Exactly what the old fallback produced: a card stacked at 1,1.
  const damaged = [
    { id: 'traffic', x: 1, y: 1, w: 20, h: 5, visible: true },
    { id: 'conns', x: 1, y: 6, w: 8, h: 16, visible: true },
    { id: 'stacked', x: 1, y: 1, w: 6, h: 10, visible: true },
  ];
  const fixed = mod.repairOverlaps(damaged.map((c: any) => Object.assign({}, c)));
  ok(!firstOverlap(fixed), 'the repair left an overlap: ' + firstOverlap(fixed));

  // THE FIRST CARD KEEPS ITS PLACE. Moving the one that was already there would
  // rearrange a dashboard the operator did lay out.
  const traffic = fixed.find((c: any) => c.id === 'traffic');
  const conns = fixed.find((c: any) => c.id === 'conns');
  ok(traffic.x === 1 && traffic.y === 1, 'the repair moved the card that was there first');
  ok(conns.x === 1 && conns.y === 6, 'the repair moved an innocent card');
  const moved = fixed.find((c: any) => c.id === 'stacked');
  ok(!(moved.x === 1 && moved.y === 1), 'the stacked card was left where the bug put it');
}

// ── 8b. ONLY THE DAMAGED CARD MOVES ───────────────────────────────────────
//
// The case that made this two passes. `c` is the card the bug stacked; `d` sits
// at row 12 legitimately. A one-pass repair re-places `c` into the first hole it
// sees - which is row 12, because `d` has not been examined yet - and then finds
// `d` "overlapping" and evicts it. An innocent card moves, and the dashboard
// ends up 33 rows long instead of 28.
{
  const { mod, restore } = boot();
  const damaged = [
    { id: 'a', x: 1, y: 1, w: 12, h: 11, visible: true },
    { id: 'b', x: 13, y: 1, w: 12, h: 11, visible: true },
    { id: 'c', x: 1, y: 1, w: 10, h: 6, visible: true },   // stacked by the bug
    { id: 'd', x: 1, y: 12, w: 24, h: 11, visible: true },
  ];
  const fixed = mod.repairOverlaps(damaged.map((x: any) => Object.assign({}, x)));
  ok(!firstOverlap(fixed), 'the repair left an overlap: ' + firstOverlap(fixed));

  for (const id of ['a', 'b', 'd']) {
    const was = damaged.find((x: any) => x.id === id)!;
    const now = fixed.find((c: any) => c.id === id);
    ok(now.x === was.x && now.y === was.y,
      'the repair moved `' + id + '` from ' + was.x + ',' + was.y + ' to ' + now.x + ',' + now.y +
      ' - nothing was wrong with it, and only the card the bug stacked should move');
  }
  // A+B fill rows 1-11 and `d` fills 12-22 across the full width, so `c` has
  // genuinely nowhere above row 23. 28 is the floor for this shape; 33 is what
  // the one-pass version produced by evicting `d` as well.
  ok(mod.gridRows(fixed) === 28,
    'the dashboard is ' + mod.gridRows(fixed) + ' rows, want 28 - longer means a second ' +
    'card was displaced');
  restore();
}

// ── 9. AND IT LEAVES A HEALTHY LAYOUT ALONE ────────────────────────────────
//
// The other half of the same claim: a repair that rewrote a good layout would
// move cards the operator positioned by hand, which is worse than the bug.
{
  const { mod, restore } = boot();
  const healthy = mod.cloneLayout(mod.DEFAULT_LAYOUT);
  const before = JSON.stringify(healthy);
  ok(JSON.stringify(mod.repairOverlaps(healthy)) === before,
    'the repair moved a card in a layout that had no overlap');

  // A HIDDEN card at the same cell is not an overlap: it is not on the grid.
  const withHidden = [
    { id: 'a', x: 1, y: 1, w: 4, h: 4, visible: true },
    { id: 'b', x: 1, y: 1, w: 4, h: 4, visible: false },
  ];
  const out = mod.repairOverlaps(withHidden.map((c: any) => Object.assign({}, c)));
  ok(out[1].x === 1 && out[1].y === 1, 'a hidden card was moved by the repair');
  restore();
}

// ── 10. THE REPAIR IS `mergeLayout`'s JOB, NOT A CALLER'S ──────────────────
//
// This is the check for a failure that reached the running app. The repair was
// wired into `loadLayout` only, and a layout arrives TWICE - from localStorage
// and from `/api/dashboard-layout` - with the server path writing what it merged
// back to localStorage. So the unrepaired path re-poisoned the cache the
// repaired one had just cleaned, and the overlapping card came straight back on
// the next load. Repairing at one of two entry points does not half-work; it
// does not work at all.
{
  const { mod, restore } = boot();
  const stored = mod.DEFAULT_LAYOUT
    .filter((c: any) => c.visible)
    .map((c: any) => Object.assign({}, c));
  // What the old Add Card wrote: a hidden card made visible, stacked at 1,1.
  const victim = mod.DEFAULT_LAYOUT.find((c: any) => !c.visible);
  stored.push(Object.assign({}, victim, { x: 1, y: 1, visible: true }));
  ok(firstOverlap(stored), 'precondition: the stored layout should be damaged');

  const merged = mod.mergeLayout(stored);
  ok(!firstOverlap(merged),
    'mergeLayout returned a layout that still overlaps: ' + firstOverlap(merged) + '. ' +
    'Every path that turns stored cards into a layout must repair, or the one that ' +
    'does not will write the damage back over the one that does');
  restore();
}

fs.rmSync(OUT, { force: true });
say(`grid-add-card: all checks passed (${checks})`);
