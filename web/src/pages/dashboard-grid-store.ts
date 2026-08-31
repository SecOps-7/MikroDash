// The Dashboard grid's persistence, its application to the DOM, and its room
// bookkeeping.
//
// ── STORAGE IS TWO PLACES, AND THEY ARE NOT EQUALS ──────────────────────────
//
// `localStorage` is the one that is READ. The server copy is written on every
// save and read only at init, so a browser that has a cached layout uses it
// immediately and the server's copy exists to follow the operator to another
// machine. Losing the POST loses portability; losing localStorage loses the
// layout on this machine until the next fetch.
//
// The save is therefore ordered: localStorage FIRST, then the network. A failed
// POST must not cost the local layout.
//
// ── EVERY READ FAILURE STARTS CLEAN, DELIBERATELY ───────────────────────────
//
// A parse error, a private-mode `localStorage` that throws, a blob that is not
// an array, an EMPTY array: all of them fall through to the defaults.
//
// The `length > 0` and `Array.isArray` guards are BELT AND BRACES rather than
// load-bearing, and measuring said so: removing either changes nothing, because
// `mergeLayout` walks DEFAULT_LAYOUT and produces the defaults for an empty list
// anyway, and a non-array makes its `for...of` throw into the same catch. They
// are reproduced because the original has them and they state the intent — they
// are not what protects the dashboard.
//
// ── TWO CARDS CAN SHARE A ROOM ──────────────────────────────────────────────
//
// `syncDashRooms` is the piece with teeth: it decides which page-gated
// collectors run. It dedupes by ROOM rather than by card, so a room is joined
// once however many of its cards are visible. The corollary is the dangerous
// half — leaving a room must be a decision about whether ANY visible card still
// wants it, never about the one card that was just removed.
//
// ── AND `/api/dashboard-layout` IS NOT A GO ENDPOINT ────────────────────────
//
// It is unprefixed, so it reaches Node through the proxy, which serves it from
// the same SQLite the port would read. That is the same arrangement
// `/api/nav-prefs` and `/api/topology-layout` are in, and it is recorded in
// PORT-QUEUE.md as a cutover item rather than a gap: during coexistence it
// works, and duplicating it in Go before cutover would mean two writers.

import { el } from '../dom';
import { cloneLayout, mergeLayout } from './dashboard-grid-layout';
import { CARD_ROOMS, DEFAULT_LAYOUT, LS_KEY, type GridCard } from '../gen/grid-tables';

export function loadLayout(): GridCard[] {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (raw) {
      const parsed = JSON.parse(raw);
      if (parsed && Array.isArray(parsed.cards) && parsed.cards.length > 0) {
        return mergeLayout(parsed.cards);
      }
    }
  } catch { /* a corrupt or unreadable cache starts clean */ }
  return cloneLayout(DEFAULT_LAYOUT);
}

export function saveLayout(layout: readonly GridCard[]): void {
  // Local first: a failed POST must not cost the layout on this machine.
  localStorage.setItem(LS_KEY, JSON.stringify({ cards: layout }));
  void fetch('/api/dashboard-layout', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ cards: layout }),
  })
    .then((r) => {
      if (!r.ok) console.warn('[MikroDash] dashboard layout save failed — HTTP', r.status);
      else console.log('[MikroDash] dashboard layout saved to server');
    })
    .catch((e) => { console.warn('[MikroDash] dashboard layout save error:', e); });
}

/**
 * Position every card, and hide the ones that are not on the dashboard.
 *
 * A card with no element is SKIPPED, not an error: the layout carries cards for
 * features a build may not include, and a missing one must not stop the rest of
 * the grid from being positioned.
 */
export function applyLayout(l: readonly GridCard[]): void {
  for (const c of l) {
    const node = el(c.id);
    if (!node) continue;
    if (!c.visible) {
      node.style.display = 'none';
      continue;
    }
    // Cleared rather than set to a value: the stylesheet decides how a visible
    // card displays, and hard-coding `block` here would override it.
    node.style.display = '';
    node.style.gridColumn = c.x + ' / span ' + c.w;
    node.style.gridRow = c.y + ' / span ' + c.h;
  }
}

function notifyRoom(eventName: string, room: string): void {
  document.dispatchEvent(new CustomEvent(eventName, { detail: room }));
}

/**
 * Join or leave the rooms the visible room-gated cards need.
 *
 * Called when the dashboard page gains or loses focus. Deduped by ROOM: two
 * cards sharing one produce a single join, and — the half that matters — a
 * single leave.
 *
 * `rooms` is a parameter purely so that claim can be TESTED. No two cards in the
 * shipped table currently share a room, so dedupe-by-room and dedupe-by-card
 * agree on every real input — a mutation swapping one for the other SURVIVED
 * until the gate could supply a table where they differ. The live table's own
 * comment says two cards may share a room, so this is a property that has to
 * keep working, not one that happens to be unobservable today.
 */
export function syncDashRooms(
  layout: readonly GridCard[], focused: boolean, rooms: Readonly<Record<string, string>> = CARD_ROOMS,
): void {
  const sent: Record<string, boolean> = {};
  for (const c of layout) {
    if (!c.visible) continue;
    const room = rooms[c.id];
    if (!room || sent[room]) continue;
    sent[room] = true;
    notifyRoom(focused ? 'dashcard:room:focus' : 'dashcard:room:blur', room);
  }
}

/**
 * Fetch the server's copy of the layout, and warm the local cache with it.
 *
 * Resolves to null whenever there is nothing usable — no response, a bad shape,
 * an empty list, or the server being unavailable at all. The caller has already
 * painted from localStorage by the time this settles, so "nothing usable" means
 * "keep what is on screen", never "clear it".
 */
export async function mergeLayoutFromServer(): Promise<GridCard[] | null> {
  try {
    const r = await fetch('/api/dashboard-layout');
    const data = await r.json();
    if (!data || !Array.isArray(data.cards) || !data.cards.length) return null;
    const merged = mergeLayout(data.cards);
    localStorage.setItem(LS_KEY, JSON.stringify({ cards: merged }));
    return merged;
  } catch {
    // Server unavailable — the local layout stands.
    return null;
  }
}
