// The Dashboard's Agent Overview card (dc-card-agent): one line about this
// router, written by the configured model on a cadence.
//
// ── THE TEXT IS MODEL OUTPUT, SO IT IS NEVER MARKUP ─────────────────────────
//
// `textContent`, not `innerHTML`, and no escaping helper in between. Every other
// card on this page builds an HTML string and escapes what goes into it; that is
// right for a card rendering a table of router rows and wrong for one rendering
// a sentence a model wrote after reading device-chosen names. Assigning text is
// the only form with no way to get the escaping wrong.
//
// It is also why this card does not use `dcEsc`. The diagnostics card records
// the trap in full: `dcEsc` escapes by round-tripping through a text node, and
// the test DOM shim keeps `textContent` and `innerHTML` in separate stores, so
// under `web/test/` it returns the empty string and the card renders nothing.
//
// ── A FAILURE IS SHOWN, NOT SWALLOWED ───────────────────────────────────────
//
// The payload carries `error` as well as `text`, and the card says which. An
// empty card is indistinguishable from a card with nothing to say - the exact
// state the diagnostics card sat in for the whole life of the port - so a
// refusal from the endpoint is rendered as a refusal.
//
// ── IT TYPES ────────────────────────────────────────────────────────────────
//
// A new line is not set in one go. The prompt and a blinking cursor sit alone for
// TYPE_HOLD_MS, then the line is typed in front of the cursor a character at a
// time, the cursor holding steady while it types and blinking again once done, as
// a terminal's does. Every step still assigns `textContent`: the animation slices
// the model's text, it never builds markup from it.
//
// A line already on screen is NOT retyped. The server re-sends the saved line
// when the Dashboard is revisited, and retyping it every time would read as a new
// answer when nothing changed. Identity is the text AND its timestamp, so a
// refresh that happens to produce the same sentence still types.
//
// ── A FAILURE IS SHOWN AT ONCE, WITH NO CURSOR ─────────────────────────────
//
// A blinking caret beside an error reads as though the card were still working.

import type { Socket } from '../socket';
import { el } from '../dom';

/** What the server sends. Mirrors the `ai:overview` declaration. */
export interface AgentOverview {
  text: string;
  error: string;
  model: string;
  at: number;
  /** The operator's chosen text colour, `#rrggbb`. Validated server-side. */
  color?: string;
}

/**
 * The space between the ">" prompt and the text, present only when there IS text,
 * so an idle card reads ">_" and a typed one "> Router ...". Non-breaking, so it
 * survives at the start of an inline element.
 */
const GAP = '\u00a0';

/** How long the empty prompt and cursor sit before typing starts. */
export const TYPE_HOLD_MS = 1000;
/** A character every 35ms, faster for a long line so none takes over ~4s. */
const TYPE_CHAR_MS = 35;
const TYPE_MAX_MS = 4000;

let typingTimer: ReturnType<typeof setTimeout> | undefined;
/** The text and timestamp of the line on screen or being typed. */
let shownKey = '';

function stopTyping(): void {
  if (typingTimer !== undefined) {
    clearTimeout(typingTimer);
    typingTimer = undefined;
  }
}

/** `14:05` in the viewer's own zone; the card has no room for a date. */
function clock(at: number): string {
  if (!at) return '';
  const d = new Date(at);
  const pad = (n: number): string => (n < 10 ? '0' : '') + n;
  return pad(d.getHours()) + ':' + pad(d.getMinutes());
}

/** The model and the time, so a line that looks wrong can be attributed and aged. */
function writeMeta(d: AgentOverview | null): void {
  const meta = el('dc-agentMeta');
  if (!meta) return;
  const parts: string[] = [];
  if (d && d.model) parts.push(d.model);
  const t = clock(d ? d.at : 0);
  if (t) parts.push(t);
  meta.textContent = parts.join(' · ');
}

/** Spin the refresh icon while a requested line is outstanding. */
export function setAgentRefreshing(on: boolean): void {
  const b = el<HTMLButtonElement>('dc-agentRefresh');
  if (!b) return;
  b.classList.toggle('is-busy', on);
  b.disabled = on;
}

export function renderAgentCard(d: AgentOverview): void {
  // Any line, or any refusal, ends a refresh the operator asked for.
  setAgentRefreshing(false);
  const text = el('dc-agentText');
  const cursor = el('dc-agentCursor');
  if (!text) return;

  // THE COLOUR, applied to the whole terminal so prompt, text and cursor match.
  // Anything but `#rrggbb` falls back to the stylesheet's default.
  const term = el('dc-agentTerm');
  if (term) term.style.color = d && typeof d.color === 'string' && /^#[0-9a-fA-F]{6}$/.test(d.color) ? d.color : '';

  if (d && d.error) {
    stopTyping();
    shownKey = '';
    text.textContent = GAP + d.error;
    text.style.color = 'var(--accent-red, #f87171)';
    if (cursor) cursor.style.display = 'none';
    writeMeta(null);
    return;
  }

  const line = (d && d.text) || '';
  const key = line + '\u0000' + (d ? d.at : 0);
  if (key === shownKey) return; // on screen already, or still being typed
  stopTyping();
  shownKey = key;
  text.style.color = '';

  if (!line) {
    text.textContent = '';
    if (cursor) cursor.style.display = 'none';
    writeMeta(d);
    return;
  }

  // THE HOLD: an empty line and a blinking cursor. The attribution waits for the
  // sentence it attributes.
  text.textContent = '';
  writeMeta(null);
  if (cursor) {
    cursor.style.display = '';
    cursor.classList.remove('is-typing');
  }

  // Code points, not UTF-16 units, so an emoji or accented pair is never typed
  // as half a character.
  const chars = Array.from(line);
  const step = Math.max(8, Math.min(TYPE_CHAR_MS, Math.floor(TYPE_MAX_MS / chars.length)));
  let i = 0;
  const typeNext = (): void => {
    i++;
    text.textContent = GAP + chars.slice(0, i).join('');
    if (i < chars.length) {
      // A little unevenness, so it reads as typing rather than as a ticker.
      // CENTRED on `step` (75% to 125% of it), so the line still takes the time
      // budgeted above: adding the jitter on top made a long line take ~5s.
      typingTimer = setTimeout(typeNext, Math.round(step * (0.75 + Math.random() * 0.5)));
      return;
    }
    typingTimer = undefined;
    cursor?.classList.remove('is-typing');
    writeMeta(d);
  };
  typingTimer = setTimeout(() => {
    cursor?.classList.add('is-typing');
    typeNext();
  }, TYPE_HOLD_MS);
}

/**
 * The refresh icon: a fresh line now, skipping the saved one.
 *
 * The server restarts the interval from this moment and bounds presses by the
 * same per-minute limit as a chat question. A refusal comes back as an ordinary
 * `ai:overview` error, and `renderAgentCard` stops the spinner on any payload,
 * so it cannot spin for ever.
 */
export function initAgentRefresh(socket: Socket): void {
  el('dc-agentRefresh')?.addEventListener('click', () => {
    setAgentRefreshing(true);
    socket.emit('ai:overview:refresh', {});
  });
}
