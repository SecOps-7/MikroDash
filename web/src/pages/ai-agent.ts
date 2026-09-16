/**
 * The AI Agent page: ask about the selected router, read the answer.
 *
 * ── MODEL OUTPUT NEVER BECOMES MARKUP ───────────────────────────────────────
 *
 * Every reply goes through `renderMarkdown`, which builds DOM nodes and never
 * produces an HTML string. Nothing in this module assigns `innerHTML`, and that
 * is the rule rather than a habit: the text arrives from a model that has been
 * reading device-supplied names, so it is untrusted twice over.
 *
 * ── THE CONVERSATION IS NOT SAVED ───────────────────────────────────────────
 *
 * It lives in this array and nowhere else. No localStorage, no database, no
 * replay on reconnect. A transcript contains host names, addresses and the shape
 * of somebody's network, and the decision recorded in the design was that none
 * of it should accumulate anywhere for the sake of scrollback.
 *
 * ── ONE QUESTION AT A TIME ──────────────────────────────────────────────────
 *
 * The send button is disabled while an answer is outstanding. The server bounds
 * asks per minute anyway, but a second question sent before the first returns
 * would interleave two replies with no way to tell which answered what.
 */

import type { Socket } from '../socket';
import { el } from '../dom';
import { renderMarkdown } from '../markdown';

type Role = 'you' | 'assistant' | 'error';

export function initAiAgentPage(socket: Socket, isVisible: (page: string) => boolean): void {
  /** Kept so a page change can redraw what is on screen. Never persisted. */
  const turns: { role: Role; text: string }[] = [];
  let waiting = false;

  const log = (): HTMLElement | null => el('aiAgentLog');
  const status = (): HTMLElement | null => el('aiAgentStatus');
  const input = (): HTMLTextAreaElement | null => el<HTMLTextAreaElement>('aiAgentInput');
  const sendBtn = (): HTMLButtonElement | null => el<HTMLButtonElement>('aiAgentSend');

  function setWaiting(on: boolean): void {
    waiting = on;
    const b = sendBtn();
    if (b) b.disabled = on;
    const s = status();
    if (s) s.textContent = on ? 'Thinking…' : '';
  }

  /** One bubble. Built as nodes; the only text assignment is `textContent`. */
  function bubble(role: Role, text: string): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'ai-turn ai-turn-' + role;
    wrap.style.maxWidth = '92%';
    wrap.style.alignSelf = role === 'you' ? 'flex-end' : 'flex-start';

    const who = document.createElement('div');
    who.style.fontSize = '.68rem';
    who.style.color = 'var(--text-muted)';
    who.style.marginBottom = '.2rem';
    who.textContent = role === 'you' ? 'You' : role === 'assistant' ? 'Assistant' : 'Error';
    wrap.appendChild(who);

    const body = document.createElement('div');
    body.style.fontSize = '.8rem';
    body.style.lineHeight = '1.5';
    body.style.padding = '.55rem .75rem';
    body.style.borderRadius = '.5rem';
    body.style.border = '1px solid var(--border)';
    if (role === 'you') body.style.background = 'rgba(56,189,248,.08)';
    if (role === 'error') body.style.borderColor = 'var(--accent-red, #f87171)';

    if (role === 'assistant') {
      // THE ONLY PLACE MODEL TEXT IS RENDERED, and it goes through the subset
      // renderer rather than being set as text, so a table stays a table.
      body.appendChild(renderMarkdown(text));
    } else {
      body.textContent = text;
    }
    wrap.appendChild(body);
    return wrap;
  }

  function redraw(): void {
    const box = log();
    if (!box) return;
    const empty = el('aiAgentEmpty');
    if (empty) empty.style.display = turns.length ? 'none' : '';
    // Remove previous turns, leaving the empty-state node in place.
    box.querySelectorAll('.ai-turn').forEach((n) => n.remove());
    for (const t of turns) box.appendChild(bubble(t.role, t.text));
    box.scrollTop = box.scrollHeight;
  }

  function add(role: Role, text: string): void {
    turns.push({ role, text });
    redraw();
  }

  function send(): void {
    if (waiting) return;
    const box = input();
    const text = (box?.value || '').trim();
    if (!text) return;
    if (box) box.value = '';
    add('you', text);
    setWaiting(true);
    socket.emit('ai:ask', { text });
  }

  socket.on('ai:reply', (d) => {
    setWaiting(false);
    add('assistant', d.text || '');
    const badge = el('aiAgentModel');
    // Shown once an answer has come back, so the badge reports what actually
    // answered rather than what is configured.
    if (badge && d.model) badge.textContent = d.model;
  });

  socket.on('ai:error', (d) => {
    setWaiting(false);
    add('error', d.error || 'The request failed.');
  });

  // ── WIRED ONCE, GUARDED EVERYWHERE ────────────────────────────────────────
  //
  // The markup is composed into the document at build time, so these elements
  // exist from the start; the guards are for the tests, which mount the module
  // against a partial DOM.
  sendBtn()?.addEventListener('click', send);
  input()?.addEventListener('keydown', (e) => {
    const ev = e as KeyboardEvent;
    // ENTER SENDS, SHIFT+ENTER NEWLINES. A question is usually one line, and a
    // textarea needing a mouse click to send would be worse for the common case
    // than for the rare one.
    if (ev.key === 'Enter' && !ev.shiftKey) {
      ev.preventDefault();
      send();
    }
  });
  el('aiAgentClear')?.addEventListener('click', () => {
    turns.length = 0;
    setWaiting(false);
    redraw();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'ai-agent') redraw();
  });
  if (isVisible('ai-agent')) redraw();
}
