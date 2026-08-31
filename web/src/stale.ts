// Cards that stopped receiving data, and cards showing another router's.
//
// ── TWO PROBLEMS, AND THE SECOND ONE IS WORSE ───────────────────────────────
//
// Staleness is cosmetic-adjacent: a card holds its last payload and says so with
// an amber scrim. The router switch is not. Without `clearDashboardData`, moving
// to another router leaves every rendered row exactly where it was, under the
// new router's name, until that router's first payload replaces them — and
// indefinitely if the collector feeding that card is disabled or slow. That is
// wrong data attributed to the wrong device, which is a different category from
// old data labelled old. Upstream fixed it and left the reason in a comment;
// this port had the same bug because it had none of this block.
//
// ── ZERO MEANS "DO NOT COUNT" ───────────────────────────────────────────────
//
// A timer of 0 is not "stale since the epoch" — the sweep skips it. That is how
// a collector the operator switched off, or one the server has put to sleep,
// stops counting down toward a fault it cannot commit.
//
// ── OFF AND DORMANT ARE DIFFERENT, AND ONLY ONE IS WORTH SAYING ─────────────
//
// A collector the OPERATOR disabled needs to announce itself: nothing else
// distinguishes that card from one that is merely empty, so its overlay is
// rewritten to "collection disabled". A DORMANT collector — the server found no
// such menu, or nothing configured — needs no announcement, because the card
// body already reads "No devices". A marker class and nothing else: an earlier
// treatment there made an ordinary empty card stand out from its neighbours and
// read as a fault.
//
// The overlay text for a dormant card is deliberately left alone. It reads
// "● stale" from the markup and must keep reading that, or a card that later
// goes genuinely stale announces itself with the wrong sentence.
//
// ── THE SWEEP RE-ASSERTS BOTH MARKINGS EVERY PASS ───────────────────────────
//
// `collection:status` and `collection:config` arrive once per transition, but
// cards get re-rendered afterwards, which wipes the class and lets the card fall
// into a false stale. Re-asserting in the sweep costs nothing and self-heals any
// later re-render.

import { el } from './dom.js';
import {
  STALE_GRACE, STALE_CARDS, COLLECTOR_CARDS, DASH_CARD_TABLES,
} from './gen/stale-tables.js';

/** cardId -> when its last payload arrived, or 0 for "do not count". */
const timers: Record<string, number> = {};
/** Thresholds start from the table and are rewritten by any payload with pollMs. */
const thresholds: Record<string, number> = {};
/** cardId -> the operator switched its collector off for this router. */
let collectionOff: Record<string, boolean> = {};
/** cardId -> the SERVER put its collector to sleep. Kept apart from the above
 *  because only one of them is worth announcing. */
let collectionDormant: Record<string, boolean> = {};

for (const c of STALE_CARDS) {
  timers[c.cardId] = 0;
  thresholds[c.cardId] = c.threshold;
}
timers['trafficCard'] = 0;

const mark = (cardId: string): HTMLElement | null => el(cardId);

/** Empty every rendered row. Called while the switching overlay is up, so the
 *  empty state is never seen; the new router's first payload repopulates. */
export function clearDashboardData(): void {
  for (const bodyId of Object.values(DASH_CARD_TABLES)) {
    const body = el(bodyId);
    if (body) body.innerHTML = '';
  }
}

/**
 * Give every card a fresh window before it may be called stale.
 *
 * Staleness means "this data stopped arriving", measured from the last payload —
 * but payloads only arrive while the socket is in the card's room, and rooms are
 * left whenever the connection drops or the page changes. After either, the
 * elapsed time says nothing about the collector; it is how long nobody was
 * listening. Restarting the clock is what makes the measurement mean what it
 * claims.
 *
 * A card whose collector is switched off is NOT re-armed: it keeps its 0.
 */
export function resetStaleTimers(): void {
  for (const c of STALE_CARDS) {
    timers[c.cardId] = collectionOff[c.cardId] ? 0 : Date.now();
    mark(c.cardId)?.classList.remove('is-stale');
  }
  timers['trafficCard'] = Date.now();
  mark('trafficCard')?.classList.remove('is-stale');
}

/** A payload arrived for this card. `pollMs`, when present, retunes it. */
export function notePayload(cardId: string, pollMs?: number): void {
  timers[cardId] = Date.now();
  mark(cardId)?.classList.remove('is-stale');
  // pollMs === 0 means STREAMED, not polled — the fixed threshold stays, because
  // the heartbeat cadence is what stale detection is measuring against.
  if (pollMs) thresholds[cardId] = pollMs + STALE_GRACE;
}

export function applyCollectionStatus(dormantKeys: unknown): void {
  if (!Array.isArray(dormantKeys)) return;
  collectionDormant = {};
  for (const key of dormantKeys) {
    for (const cardId of (COLLECTOR_CARDS[key as string] || [])) collectionDormant[cardId] = true;
  }
  for (const list of Object.values(COLLECTOR_CARDS)) {
    for (const cardId of list) {
      const card = mark(cardId);
      if (!card) continue;
      // A collector the user disabled outranks dormancy. The server does not even
      // judge a disabled collector, so this is belt and braces against an
      // ordering surprise between the two events on first load.
      if (collectionOff[cardId]) continue;
      const isDormant = !!collectionDormant[cardId];
      card.classList.toggle('is-dormant', isDormant);
      if (isDormant) {
        // A sleeping collector is not late. That countdown is what made an empty
        // card go stale on a router answering perfectly well.
        timers[cardId] = 0;
        card.classList.remove('is-stale');
      } else if (timers[cardId] === 0) {
        timers[cardId] = Date.now();
      }
    }
  }
}

export function applyCollectionConfig(enabled: Record<string, unknown> | undefined): void {
  if (!enabled) return;
  collectionOff = {};
  for (const [key, list] of Object.entries(COLLECTOR_CARDS)) {
    const isOff = enabled[key] === false;
    for (const cardId of list) {
      if (isOff) collectionOff[cardId] = true;
      const card = mark(cardId);
      if (!card) continue;
      card.classList.toggle('is-collector-off', isOff);
      const ov = card.querySelector('.stale-overlay');
      if (isOff) {
        timers[cardId] = 0;
        card.classList.remove('is-stale');
        if (ov) ov.textContent = '● collection disabled';
      } else {
        if (ov) ov.textContent = '● stale';
        if (timers[cardId] === 0) timers[cardId] = Date.now();
      }
    }
  }
}

/** One pass of the 3-second sweep, exported so the gate can step it. */
export function sweepStale(now: number): void {
  for (const c of STALE_CARDS) {
    const card = mark(c.cardId);
    if (!card) continue;
    if (collectionOff[c.cardId]) {
      card.classList.add('is-collector-off');
      card.classList.remove('is-stale');
      timers[c.cardId] = 0;
      const ov = card.querySelector('.stale-overlay');
      // Only when it does not already say so — rewriting an unchanged string
      // every three seconds is work for nothing.
      if (ov && (ov.textContent || '').indexOf('disabled') === -1) {
        ov.textContent = '● collection disabled';
      }
      continue;
    }
    if (card.classList.contains('is-collector-off')) card.classList.remove('is-collector-off');
    if (collectionDormant[c.cardId]) {
      card.classList.add('is-dormant');
      card.classList.remove('is-stale');
      timers[c.cardId] = 0;
      continue;
    }
    if (card.classList.contains('is-dormant')) card.classList.remove('is-dormant');
    const last = timers[c.cardId] || 0;
    if (last > 0 && now - last > (thresholds[c.cardId] || c.threshold)) {
      card.classList.add('is-stale');
    }
  }
}

/** cardId for an event name, so a socket subscription can find its card. */
export function cardsForEvent(event: string): string[] {
  return STALE_CARDS.filter((c) => c.event === event).map((c) => c.cardId);
}

export function startStaleSweep(): void {
  setInterval(() => sweepStale(Date.now()), 3000);
}
