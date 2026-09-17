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
 * ── THE CONVERSATION IS SAVED, PER PERSON PER ROUTER ────────────────────────
 *
 * It used not to be, and every question was a first question: the assistant
 * could not be asked a follow-up. The server now keeps the visible turns (never
 * tool calls or their results) in the database and replays the last ten
 * exchanges with each question. This page shows the same ten, loaded whenever a
 * router becomes active, so what is on screen is what the assistant remembers.
 * Clear deletes the thread on the server, not just this copy of it.
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
import { warningText } from '../resource';

type Role = 'you' | 'assistant' | 'error';

/**
 * What the assistant is doing while you wait.
 *
 * One is chosen at random and swapped every couple of seconds, so a slow answer
 * reads as work in progress rather than as a page that has stopped. They are
 * jokes about networking because that is what the person reading them does.
 */
const PUNS: readonly string[] = [
  'Wiring bits', 'Sorting Frames', 'Handling Packets', 'Hauling Bytes', 'Routing Crumbs',
  'Counting Collisions', 'Bending Light', 'Surfing Radio Waves', 'Chasing Broadcasts',
  'Flooding Unknowns', 'Aging MAC Tables', 'Poisoning Routes', 'Splitting Horizons',
  'Summarizing Prefixes', 'Electing Root Bridges', 'Converging Topology',
  'Reconverging Anyway', 'Trunking VLANs', 'Untagging Frames', 'Decrementing TTL',
  'Fragmenting Packets', 'Reassembling Packets', 'Shaping Traffic', 'Dropping Tail',
  'Queueing Politely', 'Buffering Bloat', 'Herding Datagrams', 'Draining Buckets',
  'Marking DSCP', 'Hopping Channels', 'Dodging Interference', 'Negotiating Beacons',
  'Roaming Aimlessly', 'Measuring RSSI', 'Blaming Microwaves', 'Surviving 2.4GHz',
  'Steering Bands', 'Counting Retries', 'Deauthing Nobody', 'Polishing Fiber',
  'Terminating Cables', 'Untangling Patch Leads', 'Crimping RJ45s', 'Reversing Polarity',
  'Warming Transceivers', 'Wiggling SFPs', 'Blowing Dust', 'Chasing Attenuation',
  'Blaming DNS', 'Asking Upstream', 'Leasing Addresses', 'Renewing Leases', 'Shouting ARP',
  'Resolving Eventually', 'Caching Negatively', 'Expiring TTLs', 'Doing Kessel Runs',
  'Pinging the Void', 'Consulting the Oracle', 'Rerouting Auxiliary Power',
  'Engaging Warp Cores', 'Finding the Way', 'Dividing by Zero Safely', 'Turning It Off And On',
  'Waiting on SNMP', 'Politely Polling', 'Counting Octets', 'Averaging Nonsense',
  'Interpolating Gaps', 'Arguing With RouterOS', 'Reading Winbox Tea Leaves', 'Tailing Logs',
  'Ignoring Warnings',
];


export function initAiAgentPage(socket: Socket, isVisible: (page: string) => boolean): void {
  /** What is on screen. The saved copy is the server's; see `ai:history`. */
  const turns: { role: Role; text: string }[] = [];
  let waiting = false;

  const log = (): HTMLElement | null => el('aiAgentLog');
  const input = (): HTMLTextAreaElement | null => el<HTMLTextAreaElement>('aiAgentInput');
  const sendBtn = (): HTMLButtonElement | null => el<HTMLButtonElement>('aiAgentSend');
  const thinking = (): HTMLElement | null => el('aiAgentThinking');

  let punTimer: ReturnType<typeof setInterval> | undefined;
  let lastPun = -1;

  /** A different one each time, never the same one twice running. */
  function rollPun(): void {
    const label = el('aiAgentThinkingLabel');
    if (!label) return;
    let n = Math.floor(Math.random() * PUNS.length);
    if (n === lastPun) n = (n + 1) % PUNS.length;
    lastPun = n;
    label.textContent = PUNS[n]!;
  }

  function setWaiting(on: boolean): void {
    waiting = on;
    const b = sendBtn();
    if (b) b.disabled = on;

    const box = thinking();
    if (box) {
      // SHOWN, NEVER MOVED. It lives outside the transcript, pinned to the
      // window's corner by CSS, so nothing redraw does can displace it.
      box.hidden = !on;
      if (on) rollPun();
    }
    if (punTimer !== undefined) { clearInterval(punTimer); punTimer = undefined; }
    if (on) punTimer = setInterval(rollPun, 2400);

    const l = log();
    if (l && on) l.scrollTop = l.scrollHeight;
  }

  /** One bubble. Built as nodes; the only text assignment is `textContent`. */
  function bubble(role: Role, text: string): HTMLElement {
    const wrap = document.createElement('div');
    // LAYOUT IS THE STYLESHEET'S. Every turn spans one centered reading column,
    // and `.ai-turn-you` right-aligns within it; see `.ai-turn` in app.css.
    wrap.className = 'ai-turn ai-turn-' + role;

    const who = document.createElement('div');
    who.style.fontSize = '.68rem';
    who.style.color = 'var(--text-muted)';
    who.style.marginBottom = '.2rem';
    who.textContent = role === 'you' ? 'You' : role === 'assistant' ? 'Assistant' : 'Error';
    wrap.appendChild(who);

    const body = document.createElement('div');
    body.className = 'ai-turn-body';
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

  // ── A CHANGE THE ASSISTANT WANTS TO MAKE ──────────────────────────────────
  //
  // The model's turn has already ended by the time this arrives: it proposed,
  // was told the operator would be asked, and said so. Nothing here is fed back
  // to it. The answer goes to the server, which runs the whole write pipeline
  // afresh — so approving is not replaying a decision, it is making one.
  let proposal = '';

  function closeProposal(): void {
    proposal = '';
    const box = el('aiProposeBox');
    if (box) box.hidden = true;
  }

  socket.on('ai:propose', (d) => {
    proposal = d.token;
    const what = el('aiProposeWhat');
    if (what) {
      // TEXT, NEVER MARKUP. `name` is a row identity the router supplied and
      // `label` comes from the registry, but this whole panel exists because a
      // model chose what goes in it.
      const verb = d.action === 'create' ? 'Create a ' : d.action === 'delete' ? 'Delete the ' : 'Change the ';
      what.textContent = verb + d.label + (d.name ? ' \u201c' + d.name + '\u201d' : '');
    }
    const cmd = el('aiProposeCmd');
    if (cmd) cmd.textContent = d.command || '(no command could be built)';

    const vals = el('aiProposeValues');
    if (vals) {
      const pairs = Object.entries(d.values || {});
      vals.textContent = pairs.length
        ? pairs.map(([k, v]) => k + ' = ' + v).join('   ·   ')
        : '';
    }

    const warn = el('aiProposeWarn');
    if (warn) {
      if (d.warnCode) {
        // ── innerHTML HERE, AND ONLY HERE ─────────────────────────────────
        //
        // `warningText` is the SAME function the resource form's own guard
        // dialog uses, and it escapes every value it interpolates. Writing a
        // second vocabulary for lockout warnings would mean the assistant and
        // the form describing one danger in two different ways — and the one
        // nobody re-reads would be the one that goes stale.
        //
        // Its input is a server-built guard verdict, not model output. Every
        // model-chosen string on this panel is set with textContent above.
        const { headline, why } = warningText(d.warnCode, d.warning || {});
        warn.innerHTML = '<strong>' + headline + '</strong><br>' + why;
        warn.hidden = false;
      } else {
        warn.textContent = '';
        warn.hidden = true;
      }
    }

    // A delete says so on the button too: "Apply this change" undersells it.
    const approve = el('aiProposeApprove');
    if (approve) approve.textContent = d.action === 'delete' ? 'Delete it' : 'Apply this change';

    const box = el('aiProposeBox');
    if (box) box.hidden = false;
  });

  socket.on('ai:written', (d) => {
    closeProposal();
    // Shown as a turn so the outcome sits in the transcript beside what was
    // proposed, rather than as a toast that is gone before it is read.
    add(d.applied ? 'assistant' : 'error', d.text || '');
  });

  el<HTMLButtonElement>('aiProposeApprove')?.addEventListener('click', () => {
    if (!proposal) return;
    const token = proposal;
    // CLOSED FIRST. The token is single use server-side, but a second press
    // before the reply lands should not send a second frame at all.
    closeProposal();
    socket.emit('ai:write:approve', { token });
  });

  el<HTMLButtonElement>('aiProposeReject')?.addEventListener('click', () => {
    if (!proposal) return;
    const token = proposal;
    closeProposal();
    socket.emit('ai:write:reject', { token });
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
    // THE SERVER'S COPY TOO. Emptying only this array would leave the assistant
    // remembering a conversation the operator has just watched disappear.
    socket.emit('ai:clear', {});
  });

  // ── THE SAVED THREAD ──────────────────────────────────────────────────────
  //
  // Asked for on `router:active` rather than on connect, because the server
  // sends that only once it has processed the select: asking earlier would
  // fetch the thread for no router, or for the previous one.
  socket.on('ai:history', (d) => {
    // AN ANSWER IN FLIGHT KEEPS THE SCREEN. Replacing it would drop the question
    // bubble that answer belongs under; the reply arrives and is appended.
    if (waiting) return;
    turns.length = 0;
    for (const t of d.turns) {
      turns.push({ role: t.role === 'user' ? 'you' : 'assistant', text: t.text });
    }
    redraw();
  });
  let historyRouter = '';
  socket.on('router:active', (d) => {
    const id = (d && d.activeId) || '';
    if (!id || id === historyRouter) return;
    historyRouter = id;
    // ANOTHER ROUTER, ANOTHER CONVERSATION. An answer still outstanding belongs
    // to the device it was asked about and is saved there by the server.
    setWaiting(false);
    socket.emit('ai:history', {});
  });
  socket.on('disconnect', () => { historyRouter = ''; });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'ai-agent') redraw();
  });
  if (isVisible('ai-agent')) redraw();
}
