// What turns the grid's five layers into a working page.
//
// ── ONE POINTERDOWN LISTENER DECIDES BETWEEN DRAG AND RESIZE ────────────────
//
// Not one listener per handle: the grid has 23 cards with a drag handle and
// eight resize handles each, so per-handle binding would be over 200 listeners
// that must be rebound whenever a card is added. One listener on the root asks
// what the pointer landed on, which also means a card added mid-session is
// draggable immediately with nothing to rebind.
//
// ── AND EVERY ENTRY POINT IS GATED ON EDIT MODE ─────────────────────────────
//
// Both the pointerdown and the remove-button click check `isEditing` first, so
// the handles are inert on a normal dashboard even though they are in the
// markup all the time.
//
// ── LEAVING THE PAGE DISCARDS, IT DOES NOT SAVE ─────────────────────────────
//
// The MutationObserver watching `page-dashboard`'s class is what notices a
// navigation away, and it calls `exitEditMode(false)`. Navigating away mid-edit
// therefore throws the changes away rather than committing them silently — the
// conservative reading, and the live one.
//
// ── ROOMS ARE RE-SYNCED ON THREE EVENTS, NOT ONE ────────────────────────────
//
// Page activation, socket reconnect, and adding or removing a card. Room
// membership is per-socket and is lost when the connection drops, so without the
// reconnect leg a viewer who was disconnected for a moment keeps a dashboard
// whose room-gated cards never receive anything again.

import { el } from '../dom';
import { createGridEditor, type GridEditor } from './dashboard-grid-edit';
import { createGridDrag } from './dashboard-grid-drag';
import { createGridResize } from './dashboard-grid-resize';
import { applyLayout, loadLayout, mergeLayoutFromServer, syncDashRooms } from './dashboard-grid-store';

export function initDashboardGrid(): GridEditor | null {
  const gridRoot = el('dash-grid-root');
  // The Dashboard's markup is not on the page — nothing to wire. Returns rather
  // than throwing: the same guard the live `init` uses.
  if (!gridRoot) return null;

  const editor = createGridEditor(loadLayout());
  const drag = createGridDrag(editor);
  const resize = createGridResize(editor);

  applyLayout(editor.getLayout());

  const editBtn = el('dashEditBtn');
  const addPanel = el('dashAddPanel');
  const addCardBtn = el('dashAddCardBtn');

  editBtn?.addEventListener('click', () => {
    if (!editor.isEditing()) editor.enterEditMode();
  });
  el('dashSaveBtn')?.addEventListener('click', () => editor.exitEditMode(true));
  el('dashDiscardBtn')?.addEventListener('click', () => editor.exitEditMode(false));

  addCardBtn?.addEventListener('click', (e) => {
    // Stopped so the outside-click listener below does not immediately close
    // the panel this click just opened.
    //
    // BELT AND BRACES, measured: this and the `t !== addCardBtn` test in that
    // listener are individually redundant and jointly load-bearing. Removing
    // either one alone changes nothing — the other still keeps the panel open —
    // and removing BOTH means the Add button opens a panel that closes again in
    // the same click. Reproduced as the original has it, and recorded here so a
    // later tidy-up removes at most one of them.
    e.stopPropagation();
    if (addPanel?.classList.contains('open')) editor.closeAddPanel();
    else editor.openAddPanel();
  });
  document.addEventListener('click', (e) => {
    const t = e.target as Node | null;
    if (addPanel && t && !addPanel.contains(t) && t !== addCardBtn) editor.closeAddPanel();
  });

  gridRoot.addEventListener('pointerdown', (e) => {
    if (!editor.isEditing()) return;
    const target = e.target as HTMLElement | null;
    if (!target || !target.closest) return;
    const dragHandle = target.closest<HTMLElement>('.dash-drag-handle');
    const resizeHandle = target.closest<HTMLElement>('.dash-resize');
    // Drag FIRST: a resize handle is never inside a drag handle, but the order
    // is the original's and states which wins if that ever changes.
    if (dragHandle) {
      const card = dragHandle.closest<HTMLElement>('.dash-card');
      if (card) drag.startDrag(card.id, dragHandle, e as PointerEvent);
    } else if (resizeHandle) {
      const card = resizeHandle.closest<HTMLElement>('.dash-card');
      if (card) resize.startResize(card.id, resizeHandle.dataset.dir || '', resizeHandle, e as PointerEvent);
    }
  });

  gridRoot.addEventListener('click', (e) => {
    const target = e.target as HTMLElement | null;
    const btn = target && target.closest ? target.closest<HTMLElement>('.dash-remove-btn') : null;
    if (btn && editor.isEditing()) editor.removeCard(btn.dataset.card || '');
  });

  const pageDash = el('page-dashboard');
  if (typeof MutationObserver !== 'undefined' && pageDash) {
    new MutationObserver(() => {
      const active = pageDash.classList.contains('active');
      if (editBtn) editBtn.style.display = active ? 'flex' : 'none';
      // Leaving the page DISCARDS. See the header.
      if (!active && editor.isEditing()) editor.exitEditMode(false);
      syncDashRooms(editor.getLayout(), active);
    }).observe(pageDash, { attributes: true, attributeFilter: ['class'] });
  }

  document.addEventListener('socket:reconnect', () => {
    if (pageDash?.classList.contains('active')) syncDashRooms(editor.getLayout(), true);
  });

  if (typeof ResizeObserver !== 'undefined') {
    new ResizeObserver(() => {
      // Only while editing: the overlay is only drawn then, and recomputing its
      // variables on every window resize otherwise is work nobody sees.
      if (editor.isEditing()) editor.updateGridOverlay();
    }).observe(gridRoot);
  }

  // First paint: show the Edit button only on the dashboard, and join the rooms
  // its visible cards need.
  const isDash = !!pageDash && pageDash.classList.contains('active');
  if (editBtn) editBtn.style.display = isDash ? 'flex' : 'none';
  if (isDash) syncDashRooms(editor.getLayout(), true);

  // The server's copy, which is what carries a layout between browsers. It
  // arrives AFTER the local one has already painted, so a slow request costs
  // nothing and a failed one leaves the local layout alone.
  void mergeLayoutFromServer().then((merged) => {
    if (!merged) return;
    editor.setLayout(merged);
    applyLayout(merged);
  });

  return editor;
}
