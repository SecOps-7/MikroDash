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
// empty card is indistinguishable from a card with nothing to say — the exact
// state the diagnostics card sat in for the whole life of the port — so a
// refusal from the endpoint is rendered as a refusal.
//
// ── THE CURSOR BLINKS ONLY WHILE THERE IS SOMETHING TO SAY ──────────────────
//
// It is decoration, and decoration beside an error reads as though the card were
// still working.

import { el } from '../dom';

/** What the server sends. Mirrors the `ai:overview` declaration. */
export interface AgentOverview {
  text: string;
  error: string;
  model: string;
  at: number;
}

/** `14:05` in the viewer's own zone; the card has no room for a date. */
function clock(at: number): string {
  if (!at) return '';
  const d = new Date(at);
  const pad = (n: number): string => (n < 10 ? '0' : '') + n;
  return pad(d.getHours()) + ':' + pad(d.getMinutes());
}

export function renderAgentCard(d: AgentOverview): void {
  const text = el('dc-agentText');
  const cursor = el('dc-agentCursor');
  const meta = el('dc-agentMeta');
  if (!text) return;

  if (d && d.error) {
    text.textContent = d.error;
    text.style.color = 'var(--accent-red, #f87171)';
    // No cursor on a failure: a blinking caret beside an error reads as though
    // the card were still thinking.
    if (cursor) cursor.style.display = 'none';
    if (meta) meta.textContent = '';
    return;
  }

  const line = (d && d.text) || '';
  text.textContent = line;
  text.style.color = '';
  if (cursor) cursor.style.display = line ? '' : 'none';

  if (meta) {
    // The model and the time, so a line that looks wrong can be attributed and
    // aged. A stale-looking sentence with no timestamp is unarguable.
    const parts: string[] = [];
    if (d && d.model) parts.push(d.model);
    const t = clock(d ? d.at : 0);
    if (t) parts.push(t);
    meta.textContent = parts.join(' · ');
  }
}
