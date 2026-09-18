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

  // ── STREAMING ─────────────────────────────────────────────────────────────
  //
  // `ai:chunk` delivers an answer as it is written. The first piece opens an
  // assistant bubble; later pieces repaint only that bubble, at most once a
  // frame, rather than rebuilding the whole transcript per token. A `reset`
  // starts a new model round (the last one ended in tool calls), so what it had
  // written is dropped. `ai:reply` then settles the bubble on the whole answer,
  // which is also what the server saved. An endpoint that does not stream sends
  // no chunks, and the reply simply lands as before.
  let streamIdx = -1;
  let streamText = '';
  let streamPaint = false;

  function endStream(): void {
    streamIdx = -1;
    streamText = '';
  }

  function paintStream(): void {
    streamPaint = false;
    const box = log();
    if (!box || streamIdx === -1) return;
    const bubbles = box.querySelectorAll('.ai-turn');
    const body = bubbles[bubbles.length - 1]?.querySelector('.ai-turn-body');
    if (!body) return;
    // FOLLOW THE TEXT only when the reader is already at the bottom: someone who
    // scrolled up to re-read an earlier answer is not dragged back down.
    const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 60;
    body.replaceChildren(renderMarkdown(streamText));
    if (atBottom) box.scrollTop = box.scrollHeight;
  }

  function schedulePaint(): void {
    if (streamPaint) return;
    streamPaint = true;
    if (typeof requestAnimationFrame === 'function') requestAnimationFrame(paintStream);
    else setTimeout(paintStream, 16);
  }

  socket.on('ai:chunk', (d) => {
    // A chunk with no question outstanding belongs to an answer this page has
    // already abandoned (a router switch, or Clear).
    if (!waiting) return;
    if (d.reset) {
      streamText = '';
      if (streamIdx !== -1) {
        turns.splice(streamIdx, 1);
        streamIdx = -1;
        redraw();
      }
      return;
    }
    streamText += d.text || '';
    if (!streamText) return;
    if (streamIdx === -1) {
      turns.push({ role: 'assistant', text: streamText });
      streamIdx = turns.length - 1;
      redraw();
      return;
    }
    turns[streamIdx]!.text = streamText;
    schedulePaint();
  });

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
    if (streamIdx !== -1) {
      // THE WHOLE ANSWER WINS over what was streamed: it is what was saved, and
      // it is the text a later question replays.
      turns[streamIdx]!.text = d.text || '';
      endStream();
      redraw();
    } else {
      add('assistant', d.text || '');
    }
    const badge = el('aiAgentModel');
    // Updated from what actually answered, in case the setting changed since
    // the page loaded and seeded it.
    if (badge && d.model) badge.textContent = d.model;
  });

  socket.on('ai:error', (d) => {
    setWaiting(false);
    // Whatever streamed before the failure stays on screen, and the error
    // follows it; it is simply no longer being written to.
    endStream();
    add('error', d.error || 'The request failed.');
  });

  // ── A CHANGE THE ASSISTANT WANTS TO MAKE ──────────────────────────────────
  //
  // The model's turn has already ended by the time this arrives: it proposed,
  // was told the operator would be asked, and said so. Nothing here is fed back
  // to it. The answer goes to the server, which runs the whole write pipeline
  // afresh — so approving is not replaying a decision, it is making one.
  let proposal = '';

  // The router name an action proposal wants typed back, "" when it wants none.
  let proposalTyped = '';

  function closeProposal(): void {
    proposal = '';
    proposalTyped = '';
    const box = el('aiProposeBox');
    if (box) box.hidden = true;
    const typed = el('aiProposeTyped');
    if (typed) typed.hidden = true;
    const confirm = el<HTMLInputElement>('aiProposeConfirm');
    if (confirm) confirm.value = '';
  }

  /** The approve button is live only once a typed name, if one is wanted, matches. */
  function syncApprove(): void {
    const approve = el<HTMLButtonElement>('aiProposeApprove');
    if (!approve) return;
    if (!proposalTyped) {
      approve.disabled = false;
      return;
    }
    const got = (el<HTMLInputElement>('aiProposeConfirm')?.value || '').trim().toLowerCase();
    approve.disabled = got !== proposalTyped.trim().toLowerCase();
  }

  socket.on('ai:propose', (d) => {
    proposal = d.token;
    const isAction = d.kind === 'action';
    const what = el('aiProposeWhat');
    if (what) {
      // TEXT, NEVER MARKUP. `name` is a row identity the router supplied and
      // `label` comes from the registry, but this whole panel exists because a
      // model chose what goes in it.
      //
      // An ACTION is not a row: its label is the verb itself ("Apply package
      // changes and reboot"), so it reads as one rather than as "Change the …".
      const verb = d.action === 'create' ? 'Create a ' : d.action === 'delete' ? 'Delete the ' : 'Change the ';
      what.textContent = isAction
        ? d.label + (d.name ? ' \u2014 ' + d.name : '')
        : verb + d.label + (d.name ? ' \u201c' + d.name + '\u201d' : '');
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

    // A delete says so on the button too: "Apply this change" undersells it, and
    // so does it for an action that reboots a router.
    const approve = el('aiProposeApprove');
    if (approve) {
      approve.textContent = d.action === 'delete' ? 'Delete it'
        : d.typedReason === 'code' ? 'Apply this code change'
        : d.typedName ? 'Run it and reboot'
        : isAction ? 'Run it' : 'Apply this change';
    }

    // ── THE SECOND GATE, FOR AN ACTION THAT REBOOTS ────────────────────────
    //
    // The same one the Packages page uses, and for the same reason: the point is
    // to prove the operator knows which router this is. The word is sent on the
    // approval frame and checked SERVER-SIDE against the router's own label, so
    // this box is a prompt rather than the check.
    proposalTyped = d.typedName ? (d.routerName || '') : '';
    const typed = el('aiProposeTyped');
    if (typed) typed.hidden = !proposalTyped;
    const label = el('aiProposeTypedLabel');
    if (label && proposalTyped) {
      // WHY a name is asked for is part of the prompt: a code change does not
      // reboot anything, and saying it does would train people to skim both.
      label.textContent = (d.typedReason === 'code'
        ? 'This changes code the router runs. Type its name \u2014 '
        : 'This reboots the router. Type its name \u2014 ') + proposalTyped + ' \u2014 to confirm.';
    }
    syncApprove();

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
    const confirm = (el<HTMLInputElement>('aiProposeConfirm')?.value || '').trim();
    // CLOSED FIRST. The token is single use server-side, but a second press
    // before the reply lands should not send a second frame at all.
    closeProposal();
    socket.emit('ai:write:approve', confirm ? { token, confirm } : { token });
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
  el<HTMLInputElement>('aiProposeConfirm')?.addEventListener('input', syncApprove);
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
    endStream();
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
    // THE MODEL BADGE, SEEDED FROM SETTINGS, so it names the model from the
    // moment the page loads rather than showing a dash until the first answer.
    const seed = el('aiAgentModel');
    if (seed && d.model) seed.textContent = d.model;
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
    endStream();
    setWaiting(false);
    socket.emit('ai:history', {});
  });
  socket.on('disconnect', () => { historyRouter = ''; });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'ai-agent') redraw();
  });
  if (isVisible('ai-agent')) redraw();
}
