// Two small Dashboard renderers that share nothing but their size: the stream
// degradation warnings, and the WAN status badge.
//
// ── THE WARNING ELEMENT'S ID IS BUILT, NOT LISTED ───────────────────────────
//
// `STREAM_WARN_CARDS[collector] + 'Warn'`. That is worth knowing before
// searching for it: grepping `public/app.js` for `trafficCardWarn` finds
// NOTHING, because the id only exists at runtime. It is why these two elements
// sat in this port's coverage ledger looking like orphaned markup.
//
// ── DEGRADED IS A CARD STATE AND A SENTENCE ─────────────────────────────────
//
// The card gets `is-degraded` and the warning element gets text naming the
// restart count. Recovery clears BOTH — a card left tinted after the stream
// recovered would be a permanent warning about a transient fault.
//
// ── THE WAN BADGE HAS THREE STATES, AND DISABLED WINS ───────────────────────
//
// Disabled is checked before running, so an interface that is administratively
// down reads `disabled` even if the router still reports it running. That is the
// operator's own setting and it is the more useful thing to say.

import { el } from '../dom';

/** Collector key to the card it feeds. The warning element is that id + `Warn`. */
const STREAM_WARN_CARDS: Record<string, string> = {
  traffic: 'trafficCard',
  connections: 'connCard',
};

export interface StreamHealth {
  collector?: string;
  degraded?: boolean;
  restarts?: number;
}

export function renderStreamHealth(h: StreamHealth | undefined): void {
  if (!h || !STREAM_WARN_CARDS[h.collector as string]) return;
  const cardId = STREAM_WARN_CARDS[h.collector as string]!;
  const card = el(cardId);
  const warn = el(cardId + 'Warn');
  // BOTH must exist. A card without its warning element would take the tint
  // with no explanation, which is worse than neither.
  if (!card || !warn) return;
  if (h.degraded) {
    warn.textContent = '⚠ Data incomplete — stream restarted ' + h.restarts +
      ' times without recovering';
    card.classList.add('is-degraded');
  } else {
    warn.textContent = '';
    card.classList.remove('is-degraded');
  }
}

export interface WanStatus {
  ifName?: string;
  disabled?: boolean;
  running?: boolean;
}

export function renderWanStatus(s: WanStatus): void {
  const badge = el('wanStatusBadge');
  // The original has no guard here and would throw. The element is in the
  // Dashboard's own markup, so its absence means the page is not there at all —
  // and this side may render before that markup is injected, which the live app
  // never does.
  if (!badge) return;
  badge.className = 'wan-badge';
  // `|| '?'` on the NAME only: a status with no interface still says up or down,
  // because which interface it is matters less than whether the link works.
  if (s.disabled) {
    badge.className += ' wan-disabled';
    badge.textContent = (s.ifName || '?') + ' · disabled';
  } else if (s.running) {
    badge.className += ' wan-up';
    badge.textContent = (s.ifName || '?') + ' · up';
  } else {
    badge.className += ' wan-down';
    badge.textContent = (s.ifName || '?') + ' · down';
  }
}
