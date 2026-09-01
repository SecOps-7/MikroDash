// The Dashboard grid's edit mode: entering and leaving it, and the panel that
// adds a hidden card back.
//
// ── A FACTORY, BECAUSE THE STATE IS THE SUBJECT ─────────────────────────────
//
// `layout`, `isEditing` and `editSnapshot` are what this layer is ABOUT — every
// rule below is about how they change together. Module-level state would leak
// between the gate's cases and make each one depend on the ones before it, so
// the state lives in a closure and the gate builds a fresh editor per case.
// Drag and resize, which come next, mutate the same three.
//
// ── LEAVING EDIT MODE IS NOT SYMMETRIC ──────────────────────────────────────
//
// Save persists. Discard does NOT reload — it restores the snapshot taken on
// entry and re-applies it. That distinction is the whole point of the snapshot:
// the layout the user is dragging around is the live one, so without a copy
// taken on entry there would be nothing to go back to.
//
// ── AND THE ROOM CHECKS ARE NOT SYMMETRIC EITHER ────────────────────────────
//
// Both `addCard` and `removeCard` ask whether ANY OTHER visible card wants the
// room, and both exclude the card in hand — but for opposite reasons. `addCard`
// sets `visible = true` BEFORE it asks, so without the exclusion the card would
// always find itself and never join. `removeCard` sets `visible = false` first,
// so its exclusion is redundant. Reproduced on both sides: the redundant one
// costs nothing and removing it would leave the two reading differently for no
// reason a later reader could recover.
//
// ── RESET DOES NOT SAVE ─────────────────────────────────────────────────────
//
// "Reset to default layout" restores the defaults, applies them and closes the
// panel. It does not persist, so a reset followed by Discard puts the old layout
// back. That is the live behaviour and it is the forgiving one.

import { el } from '../dom';
import { cloneLayout, findFreeSlot } from './dashboard-grid-layout';
import { applyLayout, saveLayout } from './dashboard-grid-store';
import { CARD_LABELS, CARD_ROOMS, DEFAULT_LAYOUT, GAP, PAD, type GridCard } from '../gen/grid-tables';
import { getCellSize } from './dashboard-grid-layout';

export interface GridEditor {
  getLayout(): GridCard[];
  setLayout(l: GridCard[]): void;
  isEditing(): boolean;
  enterEditMode(): void;
  exitEditMode(doSave: boolean): void;
  addCard(id: string): void;
  removeCard(id: string): void;
  renderAddPanel(): void;
  openAddPanel(): void;
  closeAddPanel(): void;
  updateGridOverlay(): void;
}

function dashActive(): boolean {
  const p = el('page-dashboard');
  return !!p && p.classList.contains('active');
}

function notifyRoom(eventName: string, room: string): void {
  document.dispatchEvent(new CustomEvent(eventName, { detail: room }));
}

export function createGridEditor(
  initial: GridCard[],
  // Injectable for the same reason `syncDashRooms` takes it: no two cards in the
  // shipped table share a room, so the self-exclusion in `addCard` — which is
  // load-bearing — cannot be distinguished from its absence on any real input.
  rooms: Readonly<Record<string, string>> = CARD_ROOMS,
): GridEditor {
  let layout = initial;
  let editSnapshot: GridCard[] = [];
  let editing = false;

  const getCard = (id: string): GridCard | undefined => layout.find((c) => c.id === id);

  function updateGridOverlay(): void {
    const root = el('dash-grid-root');
    if (!root) return;
    const r = root.getBoundingClientRect();
    const sz = getCellSize(r.width, r.height);
    // The overlay draws one line per cell PITCH — cell plus gap — not per cell,
    // which is why the gap is added here and not in getCellSize.
    root.style.setProperty('--grid-cell-w', (sz.colW + GAP) + 'px');
    root.style.setProperty('--grid-cell-h', (sz.rowH + GAP) + 'px');
    root.style.setProperty('--grid-pad-x', PAD + 'px');
    root.style.setProperty('--grid-pad-y', PAD + 'px');
  }

  function enterEditMode(): void {
    editing = true;
    // Taken BEFORE anything can be dragged: this is the only copy of where the
    // cards were when the user started.
    editSnapshot = cloneLayout(layout);
    el('dash-grid-root')?.classList.add('dashboard--editing');
    const editBtn = el('dashEditBtn'), controls = el('dashEditControls');
    if (editBtn) editBtn.style.display = 'none';
    if (controls) controls.style.display = 'flex';
    updateGridOverlay();
  }

  function exitEditMode(doSave: boolean): void {
    editing = false;
    if (doSave) {
      saveLayout(layout);
    } else {
      layout = editSnapshot;
      applyLayout(layout);
    }
    el('dash-grid-root')?.classList.remove('dashboard--editing');
    const editBtn = el('dashEditBtn'), controls = el('dashEditControls');
    // 'flex', not '': the button is a flex container and the live app restores
    // it explicitly, having hidden it with an inline style on entry.
    if (editBtn) editBtn.style.display = 'flex';
    if (controls) controls.style.display = 'none';
    closeAddPanel();
  }

  /** Whether any OTHER visible card still wants `room`. See the header. */
  function othersWant(room: string, exceptId: string): boolean {
    return layout.some((x) => x.id !== exceptId && x.visible && rooms[x.id] === room);
  }

  function removeCard(id: string): void {
    const c = getCard(id);
    if (c) c.visible = false;
    applyLayout(layout);
    renderAddPanel();
    const room = rooms[id];
    // Only while the dashboard is the visible page: leaving a room the socket
    // is not in would be a no-op at best, and the focus/blur pair has to stay
    // balanced.
    if (room && dashActive() && !othersWant(room, id)) notifyRoom('dashcard:room:blur', room);
  }

  function addCard(id: string): void {
    const c = getCard(id);
    if (!c) return;
    // `|| 3` and `|| 2` are a floor for a card whose stored size is 0 — not
    // reachable from the shipped defaults, and reproduced rather than dropped
    // because a 0×0 card would be invisible and unclickable.
    const defW = c.w || 3, defH = c.h || 2;
    const slot = findFreeSlot(layout, defW, defH);
    c.x = slot.x;
    c.y = slot.y;
    c.w = defW;
    c.h = defH;
    c.visible = true;
    applyLayout(layout);
    renderAddPanel();
    const room = rooms[id];
    if (room && dashActive() && !othersWant(room, id)) notifyRoom('dashcard:room:focus', room);
  }

  function renderAddPanel(): void {
    const panel = el('dashAddPanel');
    if (!panel) return;
    panel.innerHTML = '';

    const hdr = document.createElement('div');
    hdr.className = 'dash-add-header';
    hdr.textContent = 'Hidden Cards';
    panel.appendChild(hdr);

    const hidden = layout.filter((c) => !c.visible);
    if (hidden.length === 0) {
      const empty = document.createElement('div');
      empty.className = 'dash-add-empty';
      empty.textContent = 'All cards are visible';
      panel.appendChild(empty);
    } else {
      const chips = document.createElement('div');
      chips.className = 'dash-add-chips';
      for (const c of hidden) {
        const chip = document.createElement('button');
        chip.type = 'button';
        chip.className = 'dash-add-chip';
        chip.innerHTML = '<span>+</span>' + (CARD_LABELS[c.id] || c.id);
        chip.addEventListener('click', () => addCard(c.id));
        chips.appendChild(chip);
      }
      panel.appendChild(chips);
    }

    const resetLink = document.createElement('a');
    resetLink.className = 'dash-reset-link';
    resetLink.href = '#';
    resetLink.textContent = 'Reset to default layout';
    resetLink.addEventListener('click', (e) => {
      e.preventDefault();
      // Applied, NOT saved — see the header. Discard still puts the old one back.
      layout = cloneLayout(DEFAULT_LAYOUT);
      applyLayout(layout);
      closeAddPanel();
    });
    panel.appendChild(resetLink);
  }

  function openAddPanel(): void {
    renderAddPanel();
    el('dashAddPanel')?.classList.add('open');
  }

  function closeAddPanel(): void {
    el('dashAddPanel')?.classList.remove('open');
  }

  return {
    getLayout: () => layout,
    setLayout: (l) => { layout = l; },
    isEditing: () => editing,
    enterEditMode, exitEditMode, addCard, removeCard,
    renderAddPanel, openAddPanel, closeAddPanel, updateGridOverlay,
  };
}
