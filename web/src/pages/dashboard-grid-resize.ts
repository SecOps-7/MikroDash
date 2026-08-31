// Resizing a Dashboard card by its edge handles.
//
// ── EVERY DELTA IS FROM THE ORIGIN, NOT THE LAST FRAME ──────────────────────
//
// `origX/origY/origW/origH` are captured once at pointer-down and every move
// recomputes from them plus the total pointer delta. Dragging a handle out and
// back therefore returns the card EXACTLY to where it started — an incremental
// implementation would accumulate rounding on every frame and leave the card a
// cell off after a long wiggle.
//
// ── AN INVALID SIZE IS REFUSED, NOT SNAPPED ─────────────────────────────────
//
// This is the opposite of dragging, and deliberately so. A drag keeps its last
// VALID cell and commits that on drop; a resize simply RETURNS when the result
// would overlap or leave the grid, so the card stops growing and the pointer
// carries on without it. Applied live, with no placeholder — there is nothing to
// commit later, so there is nothing to hold back.
//
// ── WEST AND NORTH MOVE THE ORIGIN ──────────────────────────────────────────
//
// Dragging the left edge does not move the card: it computes a new x and
// derives the width from it, so the RIGHT edge stays put. The clamp is on the
// new origin rather than on the width, which is what stops the left edge
// crossing the right one — `origX + origW - MIN_W` is the furthest right it may
// go, leaving a card exactly MIN_W wide.
//
// ── THE DIRECTION IS A SUBSTRING TEST ───────────────────────────────────────
//
// `dir` is one of n/s/e/w or a corner like 'se', and each axis is tested with
// `includes`. That is what makes a corner handle work without a case of its own.

import { el } from '../dom';
import { getCellSize, hasOverlap, inBounds } from './dashboard-grid-layout';
import { applyLayout } from './dashboard-grid-store';
import { COLS, GAP, MIN_H, MIN_W, ROWS, type GridCard } from '../gen/grid-tables';
import type { GridEditor } from './dashboard-grid-edit';

interface ResizeState {
  cardId: string;
  dir: string;
  origX: number;
  origY: number;
  origW: number;
  origH: number;
  ptrStartX: number;
  ptrStartY: number;
  handle: HTMLElement;
  ptId: number;
}

export interface GridResize {
  startResize(cardId: string, dir: string, handleEl: HTMLElement, e: PointerEvent): void;
  onResizeMove(e: PointerEvent): void;
  onResizeEnd(e: PointerEvent): void;
  isResizing(): boolean;
}

export function createGridResize(editor: GridEditor): GridResize {
  let resizeState: ResizeState | null = null;
  const getCard = (id: string): GridCard | undefined => editor.getLayout().find((c) => c.id === id);

  function onResizeMove(e: PointerEvent): void {
    if (!resizeState) return;
    const rs = resizeState;
    const c = getCard(rs.cardId);
    // The original has no such guard and would throw. A card cannot be removed
    // mid-resize — that needs the Add panel, which is not reachable while a
    // pointer is captured — so this is unreachable either way, and returning is
    // the harmless reading of an impossible state.
    if (!c) return;
    const root = el('dash-grid-root');
    if (!root) return;
    const r = root.getBoundingClientRect();
    const sz = getCellSize(r.width, r.height);

    const dx = e.clientX - rs.ptrStartX;
    const dy = e.clientY - rs.ptrStartY;
    // ROUNDED, so the card snaps to the nearest cell as the pointer passes the
    // half-way mark rather than only once a full cell has been crossed.
    const dCols = Math.round(dx / (sz.colW + GAP));
    const dRows = Math.round(dy / (sz.rowH + GAP));

    let nx = rs.origX, ny = rs.origY, nw = rs.origW, nh = rs.origH;

    if (rs.dir.includes('e')) {
      nw = Math.max(MIN_W, Math.min(COLS - rs.origX + 1, rs.origW + dCols));
    }
    if (rs.dir.includes('s')) {
      nh = Math.max(MIN_H, Math.min(ROWS - rs.origY + 1, rs.origH + dRows));
    }
    if (rs.dir.includes('w')) {
      // Clamped on the ORIGIN, not the width: this is what stops the left edge
      // crossing the right one.
      const newX = Math.max(1, Math.min(rs.origX + rs.origW - MIN_W, rs.origX + dCols));
      nw = rs.origX + rs.origW - newX;
      nx = newX;
    }
    if (rs.dir.includes('n')) {
      const newY = Math.max(1, Math.min(rs.origY + rs.origH - MIN_H, rs.origY + dRows));
      nh = rs.origY + rs.origH - newY;
      ny = newY;
    }

    // REFUSED, not clamped. See the header.
    if (!inBounds(nx, ny, nw, nh)) return;
    if (hasOverlap(editor.getLayout(), { x: nx, y: ny, w: nw, h: nh }, rs.cardId)) return;

    c.x = nx; c.y = ny;
    c.w = nw; c.h = nh;
    applyLayout(editor.getLayout());
  }

  function onResizeEnd(_e: PointerEvent): void {
    if (!resizeState) return;
    const rs = resizeState;
    rs.handle.removeEventListener('pointermove', onResizeMove as EventListener);
    rs.handle.removeEventListener('pointerup', onResizeEnd as EventListener);
    rs.handle.removeEventListener('pointercancel', onResizeEnd as EventListener);
    try { rs.handle.releasePointerCapture(rs.ptId); } catch { /* already gone */ }
    resizeState = null;
  }

  function startResize(cardId: string, dir: string, handleEl: HTMLElement, e: PointerEvent): void {
    e.preventDefault();
    // STOPPED as well as prevented: the resize handles sit inside the card, and
    // without this the card's own drag handler would start a drag at the same
    // time and the two would fight over the same pointer.
    e.stopPropagation();

    const c = getCard(cardId);
    if (!c) return;

    resizeState = {
      cardId, dir,
      origX: c.x, origY: c.y, origW: c.w, origH: c.h,
      ptrStartX: e.clientX, ptrStartY: e.clientY,
      handle: handleEl, ptId: e.pointerId,
    };

    handleEl.setPointerCapture(e.pointerId);
    handleEl.addEventListener('pointermove', onResizeMove as EventListener);
    handleEl.addEventListener('pointerup', onResizeEnd as EventListener);
    handleEl.addEventListener('pointercancel', onResizeEnd as EventListener);
  }

  return { startResize, onResizeMove, onResizeEnd, isResizing: () => !!resizeState };
}
