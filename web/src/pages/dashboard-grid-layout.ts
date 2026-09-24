// The Dashboard grid's layout arithmetic: overlap, bounds, free slots and the
// conversions between cells and pixels.
//
// ── PURE, AND SEPARATE FOR THE SAME REASON THE GAUGE WAS ────────────────────
//
// `dashboard-grid.js` is 763 lines of drag handlers, resize handles, an add
// panel and room bookkeeping. This is the part underneath all of it that is
// arithmetic - rects in, rects out - and therefore the part that can be
// compared against the live implementation exactly rather than driven through
// a DOM.
//
// ── THE LAYOUT IS PASSED IN, NOT READ FROM MODULE STATE ─────────────────────
//
// The original's `hasOverlap` and `findFreeSlot` close over a module-level
// `layout`. Here it is a parameter. That is a mechanism change with no
// behavioural one - and it is what lets the gate ask the same question of both
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

/**
 * A stored card list as a usable layout: defaults filled in, damage repaired.
 *
 * ── THE REPAIR LIVES HERE BECAUSE THERE ARE TWO ENTRY POINTS ───────────────
 *
 * A layout reaches this app twice - `loadLayout` from localStorage, and
 * `mergeLayoutFromServer` from `/api/dashboard-layout` - and the second WRITES
 * WHAT IT MERGED BACK to localStorage. Repairing at one call site and not the
 * other therefore does not half-work; it does not work at all, because the
 * server path re-poisons the cache the local path just cleaned. Measured live:
 * the repair was called from `loadLayout` alone and the overlapping card came
 * straight back.
 *
 * So it is one function's job rather than two callers' responsibility. Anything
 * turning stored cards into a layout gets both halves and cannot forget one.
 */
export function mergeLayout(saved: readonly GridCard[]): GridCard[] {
  const byId: Record<string, GridCard> = {};
  // LAST WINS on a duplicate id, as the original's forEach does. A stored
  // layout with the same card twice is malformed, but it must not throw.
  for (const c of saved) byId[c.id] = c;
  return repairOverlaps(
    DEFAULT_LAYOUT.map((def) => byId[def.id] ? byId[def.id]! : Object.assign({}, def)),
  );
}

export function rectOverlaps(a: Rect, b: Rect): boolean {
  // Strict on both edges, so cards that merely TOUCH do not overlap - which is
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

/**
 * How many rows this layout actually needs.
 *
 * ── THE GRID GROWS DOWNWARD, AND `ROWS` IS ONLY ITS FLOOR ──────────────────
 *
 * `ROWS` was a hard ceiling, and a dashboard cannot hold every card in 24×22:
 * the nine cards a default install ships leave 24 free cells of 528, and none
 * of them form a rectangle big enough for ANY of the fourteen hidden ones.
 * `findFreeSlot` therefore always took its fallback and every card added from
 * the Add Card panel landed at 1,1 on top of the traffic chart - not an edge
 * case, the only outcome. Measured 2026-09-10.
 *
 * So the grid extends to cover its deepest card and the page scrolls. `ROWS`
 * stays the FLOOR, which is what keeps an existing dashboard exactly as it is:
 * a layout inside 22 rows returns 22, no track is resized and no card moves.
 *
 * Every place that used to read `ROWS` reads this instead, because a row count
 * that is right in one of them and stale in another is worse than a fixed one:
 * `getCellSize` divides the root's REAL pixel height by it, so a stale count
 * puts a dragged card in the wrong cell.
 */
export function gridRows(layout: readonly GridCard[]): number {
  let deepest = ROWS;
  for (const c of layout) {
    if (!c.visible) continue;
    const bottom = c.y + c.h - 1;
    if (bottom > deepest) deepest = bottom;
  }
  return deepest;
}

export function inBounds(x: number, y: number, w: number, h: number, rows: number = ROWS): boolean {
  return x >= 1 && y >= 1 && x + w - 1 <= COLS && y + h - 1 <= rows;
}

/**
 * The first free w×h slot, scanning left-to-right then top-to-bottom.
 *
 * ── A FRESH BAND BELOW, NEVER AN OVERLAP ───────────────────────────────────
 *
 * The whole CURRENT grid is scanned first, grown rows included, so a card fills
 * a hole before it extends the dashboard. Only when there is genuinely no room
 * does it go below everything - at column 1, where the eye already is.
 *
 * This used to return 1,1 instead, with the reasoning that a visible overlapping
 * card beats a silent refusal. That was a fair trade for a rare case and this is
 * not a rare case: see `gridRows`.
 */
export function findFreeSlot(layout: readonly GridCard[], w: number, h: number): { x: number; y: number } {
  const rows = gridRows(layout);
  for (let row = 1; row <= rows - h + 1; row++) {
    for (let col = 1; col <= COLS - w + 1; col++) {
      const cand: Rect = { x: col, y: row, w, h };
      if (!hasOverlap(layout, cand, '__test__')) return { x: col, y: row };
    }
  }
  return { x: 1, y: rows + 1 };
}

/**
 * Re-place any visible card that overlaps one before it.
 *
 * ── A ONE-TIME REPAIR FOR LAYOUTS THE OLD `findFreeSlot` DAMAGED ───────────
 *
 * Fixing the placement does nothing for a dashboard that has ALREADY been
 * damaged: the bad position is saved, so an operator who used Add Card before
 * the fix opens their dashboard afterwards and still finds a card sitting on
 * top of the traffic chart, with no idea why. Measured on this dev install.
 *
 * ── AN OVERLAP CAN ONLY HAVE COME FROM THE BUG ─────────────────────────────
 *
 * This is the premise that makes rewriting somebody's saved layout defensible,
 * and it was checked against all three ways a card can move rather than assumed:
 * drag snaps only `if (inBounds(...) && !hasOverlap(...))`, resize returns early
 * on `hasOverlap`, and `doSwap` exchanges position AND size so the two cards
 * trade whole rectangles. None of them can produce an overlap. So a card is
 * moved here only when it was never placed deliberately.
 *
 * The FIRST card of a pair keeps its place and the later one moves, which makes
 * the repair deterministic - and puts the burden on the card the bug added
 * rather than on the one that was already there.
 *
 * ── TWO PASSES, AND THE SECOND ONE IS WHY ──────────────────────────────────
 *
 * Finding the damaged cards BEFORE moving any of them is what keeps that
 * promise. A single pass re-places a damaged card into the first hole it finds,
 * which can be space an innocent card legitimately occupies later in the list -
 * and then that card is itself found "overlapping" and evicted. Caught by
 * mutation testing: A(1,1,12x11) B(13,1,12x11) C(1,1,10x6 stacked)
 * D(1,12,24x11) moved D, which nothing was wrong with, and left the dashboard
 * 33 rows long instead of 28.
 *
 * So pass one decides who is damaged and pass two moves only those, treating
 * every undamaged card as an obstacle wherever it stands. A damaged card's own
 * position is never an obstacle - it is about to move, and reserving it would
 * lengthen the page for space nobody will use.
 */
export function repairOverlaps(layout: GridCard[]): GridCard[] {
  // Pass one: who is standing on somebody.
  const damaged = new Set<string>();
  const seen: GridCard[] = [];
  for (const c of layout) {
    if (!c.visible) continue;
    if (hasOverlap(seen, c, c.id)) damaged.add(c.id);
    else seen.push(c);
  }
  if (damaged.size === 0) return layout;

  // Pass two: move only those, against everything that is staying put.
  const obstacles = layout.filter((c) => c.visible && !damaged.has(c.id));
  for (const c of layout) {
    if (!damaged.has(c.id)) continue;
    const slot = findFreeSlot(obstacles, c.w, c.h);
    c.x = slot.x;
    c.y = slot.y;
    // Added as it lands, so two damaged cards cannot be repaired onto each other.
    obstacles.push(c);
  }
  return layout;
}

/**
 * Cell dimensions for a grid root of the given pixel size.
 *
 * `rows` must be the layout's own count, not `ROWS`: the height passed in is the
 * root's REAL height, so on a grown dashboard dividing by 22 yields a row twice
 * its true size and every drag lands in the wrong cell.
 */
export function getCellSize(width: number, height: number, rows: number = ROWS): CellSize {
  return {
    colW: (width - 2 * PAD - (COLS - 1) * GAP) / COLS,
    rowH: (height - 2 * PAD - (rows - 1) * GAP) / rows,
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
export function ptrToCell(
  sz: CellSize, pxRel: number, pyRel: number, rows: number = ROWS,
): { col: number; row: number } {
  const col = Math.floor((pxRel - PAD) / (sz.colW + GAP)) + 1;
  const row = Math.floor((pyRel - PAD) / (sz.rowH + GAP)) + 1;
  return {
    col: Math.max(1, Math.min(COLS, col)),
    row: Math.max(1, Math.min(rows, row)),
  };
}

export { COLS, ROWS, GAP, PAD, MIN_W, MIN_H };
