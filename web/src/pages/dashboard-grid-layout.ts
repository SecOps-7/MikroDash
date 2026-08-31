// The Dashboard grid's layout arithmetic: overlap, bounds, free slots and the
// conversions between cells and pixels.
//
// ── PURE, AND SEPARATE FOR THE SAME REASON THE GAUGE WAS ────────────────────
//
// `dashboard-grid.js` is 763 lines of drag handlers, resize handles, an add
// panel and room bookkeeping. This is the part underneath all of it that is
// arithmetic — rects in, rects out — and therefore the part that can be
// compared against the live implementation exactly rather than driven through
// a DOM.
//
// ── THE LAYOUT IS PASSED IN, NOT READ FROM MODULE STATE ─────────────────────
//
// The original's `hasOverlap` and `findFreeSlot` close over a module-level
// `layout`. Here it is a parameter. That is a mechanism change with no
// behavioural one — and it is what lets the gate ask the same question of both
// implementations a few hundred times without rebuilding a page each time.
//
// ── THE MERGE IS DEFAULT-DRIVEN, WHICH IS THE POINT ─────────────────────────
//
// `mergeLayout` walks DEFAULT_LAYOUT and takes the saved entry when there is
// one. So a stored layout from before a card existed still gets that card, at
// its default position, and a stored entry for a card that has since been
// REMOVED is silently dropped. Both matter across an upgrade: the first is why
// a new card appears rather than being invisible until the layout is reset, and
// the second is why a stale id cannot resurrect a card that no longer exists.

import { COLS, DEFAULT_LAYOUT, MIN_H, MIN_W, ROWS, GAP, PAD, type GridCard } from '../gen/grid-tables';

export interface Rect { x: number; y: number; w: number; h: number }
export interface CellSize { colW: number; rowH: number }

export function cloneLayout(l: readonly GridCard[]): GridCard[] {
  return l.map((c) => Object.assign({}, c));
}

export function mergeLayout(saved: readonly GridCard[]): GridCard[] {
  const byId: Record<string, GridCard> = {};
  // LAST WINS on a duplicate id, as the original's forEach does. A stored
  // layout with the same card twice is malformed, but it must not throw.
  for (const c of saved) byId[c.id] = c;
  return DEFAULT_LAYOUT.map((def) => byId[def.id] ? byId[def.id]! : Object.assign({}, def));
}

export function rectOverlaps(a: Rect, b: Rect): boolean {
  // Strict on both edges, so cards that merely TOUCH do not overlap — which is
  // what makes a full row of adjacent cards legal.
  return a.x < b.x + b.w && a.x + a.w > b.x &&
         a.y < b.y + b.h && a.y + a.h > b.y;
}

/** Whether `candidate` collides with any VISIBLE card other than `excludeId`. */
export function hasOverlap(
  layout: readonly GridCard[], candidate: Rect, excludeId: string,
): boolean {
  for (const c of layout) {
    if (c.id === excludeId || !c.visible) continue;
    if (rectOverlaps(candidate, c)) return true;
  }
  return false;
}

export function inBounds(x: number, y: number, w: number, h: number): boolean {
  return x >= 1 && y >= 1 && x + w - 1 <= COLS && y + h - 1 <= ROWS;
}

/**
 * The first free w×h slot, scanning left-to-right then top-to-bottom.
 *
 * Falls back to 1,1 when the grid is full — deliberately a POSITION rather than
 * a refusal, so adding a card to a full dashboard puts it somewhere the user
 * can see and move rather than failing silently. It will overlap; that is the
 * lesser of the two.
 */
export function findFreeSlot(layout: readonly GridCard[], w: number, h: number): { x: number; y: number } {
  for (let row = 1; row <= ROWS - h + 1; row++) {
    for (let col = 1; col <= COLS - w + 1; col++) {
      const cand: Rect = { x: col, y: row, w, h };
      if (!hasOverlap(layout, cand, '__test__')) return { x: col, y: row };
    }
  }
  return { x: 1, y: 1 };
}

/** Cell dimensions for a grid root of the given pixel size. */
export function getCellSize(width: number, height: number): CellSize {
  return {
    colW: (width - 2 * PAD - (COLS - 1) * GAP) / COLS,
    rowH: (height - 2 * PAD - (ROWS - 1) * GAP) / ROWS,
  };
}

/** A 1-based cell rect as pixels relative to the grid root. */
export function cellToPixel(
  sz: CellSize, x: number, y: number, w: number, h: number,
): { left: number; top: number; width: number; height: number } {
  return {
    left: PAD + (x - 1) * (sz.colW + GAP),
    top: PAD + (y - 1) * (sz.rowH + GAP),
    // The gaps BETWEEN the spanned cells count as width: a 2-cell card is two
    // cells plus the one gap they straddle, not two isolated cells.
    width: w * sz.colW + (w - 1) * GAP,
    height: h * sz.rowH + (h - 1) * GAP,
  };
}

/** Grid-root-relative pixels as a 1-based column and row, clamped to the grid. */
export function ptrToCell(sz: CellSize, pxRel: number, pyRel: number): { col: number; row: number } {
  const col = Math.floor((pxRel - PAD) / (sz.colW + GAP)) + 1;
  const row = Math.floor((pyRel - PAD) / (sz.rowH + GAP)) + 1;
  return {
    col: Math.max(1, Math.min(COLS, col)),
    row: Math.max(1, Math.min(ROWS, row)),
  };
}

export { COLS, ROWS, GAP, PAD, MIN_W, MIN_H };
