// Dragging a card around the Dashboard grid.
//
// ── THE SNAP IS THE LAST VALID POSITION, NOT THE POINTER'S ──────────────────
//
// `snapX`/`snapY` are updated ONLY when the cell under the pointer is in bounds
// and free. Drag over an occupied area and the placeholder stays where it last
// legally was; let go there and the card lands THERE, not under the cursor.
// That is what makes an illegal drop impossible without a modal or a rejection —
// the drop cannot fail, because the position it commits was validated when it
// was chosen.
//
// ── HOVERING LONG ENOUGH IS A SWAP ──────────────────────────────────────────
//
// Dwell over another card for 1.5s and the two exchange position AND size. The
// timer's closure captures the hovered id rather than reading `swapTarget` when
// it fires — the pointer can move to a third card inside those 1.5 seconds, and
// reading the state at fire time would swap with whichever card is hovered THEN,
// which is not the one whose highlight the user was watching.
//
// ── AND THE CLEANUP IS SHARED BECAUSE THERE ARE TWO ENDINGS ─────────────────
//
// A drag ends by dropping or by swapping, and both must remove the ghost, clear
// the highlight, hide the placeholder, restore the card's opacity, unbind three
// listeners and release the pointer capture. `endDrag` is the one place that
// happens, which is why a swap calls it rather than repeating it.

import { el } from '../dom';
import { cellToPixel, getCellSize, hasOverlap, inBounds, ptrToCell } from './dashboard-grid-layout';
import { applyLayout } from './dashboard-grid-store';
import { CARD_LABELS, COLS, PAD, ROWS, type GridCard } from '../gen/grid-tables';
import type { GridEditor } from './dashboard-grid-edit';

/** How long the ghost must dwell over a card before the two swap. */
export const SWAP_DWELL_MS = 1500;

interface DragState {
  cardId: string;
  ptrOffX: number;
  ptrOffY: number;
  snapX: number;
  snapY: number;
  ghost: HTMLElement;
  handle: HTMLElement;
  ptId: number;
  swapTarget: string | null;
  swapTimer: ReturnType<typeof setTimeout> | null;
}

export interface GridDrag {
  startDrag(cardId: string, handleEl: HTMLElement, e: PointerEvent): void;
  onDragMove(e: PointerEvent): void;
  onDragEnd(e: PointerEvent): void;
  updatePlaceholder(x: number, y: number, w: number, h: number): void;
  isDragging(): boolean;
}

export function createGridDrag(editor: GridEditor): GridDrag {
  let dragState: DragState | null = null;
  const getCard = (id: string): GridCard | undefined => editor.getLayout().find((c) => c.id === id);

  function updatePlaceholder(x: number, y: number, w: number, h: number): void {
    const ph = el('dash-placeholder');
    const root = el('dash-grid-root');
    if (!ph || !root) return;
    const r = root.getBoundingClientRect();
    const pos = cellToPixel(getCellSize(r.width, r.height), x, y, w, h);
    ph.style.left = pos.left + 'px';
    ph.style.top = pos.top + 'px';
    ph.style.width = pos.width + 'px';
    ph.style.height = pos.height + 'px';
  }

  function clearSwapPending(): void {
    if (!dragState) return;
    const ds = dragState;
    if (ds.swapTimer) { clearTimeout(ds.swapTimer); ds.swapTimer = null; }
    if (ds.swapTarget) {
      el(ds.swapTarget)?.classList.remove('dash-swap-pending');
      ds.swapTarget = null;
    }
  }

  /** Exchange position AND size, then end the drag so both cards settle. */
  function doSwap(draggingId: string, targetId: string): void {
    const a = getCard(draggingId), b = getCard(targetId);
    if (!a || !b) return;
    const ax = a.x, ay = a.y, aw = a.w, ah = a.h;
    a.x = b.x; a.y = b.y; a.w = b.w; a.h = b.h;
    b.x = ax; b.y = ay; b.w = aw; b.h = ah;
    applyLayout(editor.getLayout());
    endDrag();
  }

  function endDrag(): void {
    if (!dragState) return;
    const ds = dragState;
    const cardEl = el(ds.cardId);
    clearSwapPending();
    if (cardEl) cardEl.style.opacity = '';
    ds.ghost.remove();
    const ph = el('dash-placeholder');
    if (ph) ph.style.display = 'none';
    ds.handle.removeEventListener('pointermove', onDragMove as EventListener);
    ds.handle.removeEventListener('pointerup', onDragEnd as EventListener);
    ds.handle.removeEventListener('pointercancel', onDragEnd as EventListener);
    // Throws if the capture was already released — by a pointercancel, or by the
    // element being detached. Swallowed, as the original swallows it: the drag is
    // over either way and there is nothing to tell the user.
    try { ds.handle.releasePointerCapture(ds.ptId); } catch { /* already gone */ }
    dragState = null;
  }

  function onDragMove(e: PointerEvent): void {
    if (!dragState) return;
    const ds = dragState;
    const c = getCard(ds.cardId);
    if (!c) return;
    const ghost = ds.ghost;
    const root = el('dash-grid-root');
    if (!root) return;
    const gridRect = root.getBoundingClientRect();

    const ghostLeft = e.clientX - ds.ptrOffX;
    const ghostTop = e.clientY - ds.ptrOffY;
    ghost.style.left = ghostLeft + 'px';
    ghost.style.top = ghostTop + 'px';

    // The original computes `relLeft = ghostLeft - gridRect.left - PAD` and then
    // passes `relLeft + PAD`, so the padding cancels exactly. Written the same
    // way rather than folded, so this reads as the original does — the two
    // halves belong to different ideas (the card's inner-area offset, and
    // ptrToCell's own padding handling) and collapsing them hides that.
    const relLeft = ghostLeft - gridRect.left - PAD;
    const relTop = ghostTop - gridRect.top - PAD;
    const sz = getCellSize(gridRect.width, gridRect.height);
    const cell = ptrToCell(sz, relLeft + PAD, relTop + PAD);
    // Clamped so the card's FAR edge stays on the grid, not just its origin.
    const col = Math.max(1, Math.min(COLS - c.w + 1, cell.col));
    const row = Math.max(1, Math.min(ROWS - c.h + 1, cell.row));

    const candidate = { x: col, y: row, w: c.w, h: c.h };
    if (inBounds(col, row, c.w, c.h) && !hasOverlap(editor.getLayout(), candidate, ds.cardId)) {
      ds.snapX = col;
      ds.snapY = row;
    }
    updatePlaceholder(ds.snapX, ds.snapY, c.w, c.h);

    // Which visible card the ghost's CENTRE is over — the centre, not the
    // pointer, so a swap is about where the card would land rather than where
    // the user happens to be holding it.
    const ghostCx = ghostLeft + parseFloat(ghost.style.width) / 2;
    const ghostCy = ghostTop + parseFloat(ghost.style.height) / 2;
    let hoveredId: string | null = null;
    for (const lc of editor.getLayout()) {
      if (!lc.visible || lc.id === ds.cardId) continue;
      const node = el(lc.id);
      if (!node) continue;
      const er = node.getBoundingClientRect();
      if (ghostCx >= er.left && ghostCx <= er.right && ghostCy >= er.top && ghostCy <= er.bottom) {
        hoveredId = lc.id;
        break;
      }
    }

    if (hoveredId !== ds.swapTarget) {
      clearSwapPending();
      ds.swapTarget = hoveredId;
      if (hoveredId) {
        el(hoveredId)?.classList.add('dash-swap-pending');
        // `tid` is captured so the closure is stable: the pointer may reach a
        // third card before this fires, and reading swapTarget at fire time
        // would swap with THAT one instead of the one being highlighted.
        const tid = hoveredId;
        ds.swapTimer = setTimeout(() => {
          if (dragState && dragState.swapTarget === tid) doSwap(dragState.cardId, tid);
        }, SWAP_DWELL_MS);
      }
    }
  }

  function onDragEnd(_e: PointerEvent): void {
    if (!dragState) return;
    const ds = dragState;
    const c = getCard(ds.cardId);
    // The LAST VALID snap, not wherever the pointer was let go. See the header.
    if (c) { c.x = ds.snapX; c.y = ds.snapY; }
    applyLayout(editor.getLayout());
    endDrag();
  }

  function startDrag(cardId: string, handleEl: HTMLElement, e: PointerEvent): void {
    e.preventDefault();
    const cardEl = el(cardId);
    const c = getCard(cardId);
    if (!cardEl || !c || !c.visible) return;

    const rect = cardEl.getBoundingClientRect();
    const ghost = document.createElement('div');
    ghost.id = 'dash-ghost';
    ghost.style.width = rect.width + 'px';
    ghost.style.height = rect.height + 'px';
    ghost.style.left = rect.left + 'px';
    ghost.style.top = rect.top + 'px';
    // An unlabelled card gets an EMPTY ghost rather than its raw id: the ghost
    // is a user-facing rectangle, and an id would read as a bug.
    ghost.textContent = CARD_LABELS[cardId] || '';
    document.body.appendChild(ghost);

    cardEl.style.opacity = '0.2';

    dragState = {
      cardId,
      ptrOffX: e.clientX - rect.left,
      ptrOffY: e.clientY - rect.top,
      snapX: c.x,
      snapY: c.y,
      ghost,
      handle: handleEl,
      ptId: e.pointerId,
      swapTarget: null,
      swapTimer: null,
    };

    handleEl.setPointerCapture(e.pointerId);
    handleEl.addEventListener('pointermove', onDragMove as EventListener);
    handleEl.addEventListener('pointerup', onDragEnd as EventListener);
    handleEl.addEventListener('pointercancel', onDragEnd as EventListener);

    const ph = el('dash-placeholder');
    if (ph) ph.style.display = 'block';
    updatePlaceholder(c.x, c.y, c.w, c.h);
  }

  return { startDrag, onDragMove, onDragEnd, updatePlaceholder, isDragging: () => !!dragState };
}
