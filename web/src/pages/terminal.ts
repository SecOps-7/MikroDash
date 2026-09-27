import type { Socket } from '../socket';
import { el } from '../dom';
import type { TermEntry, TermOutputPayload, TermScrollbackPayload } from '../gen/payloads';

/**
 * The Terminal page.
 *
 * ── NOTHING HERE IS EVER PARSED AS MARKUP ───────────────────────────────────
 *
 * Every string this page renders came off a router: the console text, and the
 * device's own identity in the prompt. 0.7.35 shipped because an interface name
 * could inject markup, so the whole pane is built with `createElement` and
 * `textContent` and there is no `innerHTML` in this file. The Logs page escapes
 * strings and then assigns HTML; that works there because it composes the
 * markup itself, and it is the wrong shape for arbitrary multi-line console
 * output.
 *
 * ── IT READS AS A TERMINAL BECAUSE THE PROMPT IS IN THE SCROLLBACK ──────────
 *
 * The live line is the scroller's LAST CHILD, and `draw` moves it back there
 * after every write - `appendChild` relocates an element that is already in the
 * document, so that one call is the whole mechanism. Output lands above the
 * line you are typing on, the caret scrolls with the content, and there is no
 * separate bordered region at the foot of the card. Clicking the pane puts the
 * caret back, unless something is selected, because copying output is most of
 * what anyone does with a terminal.
 *
 * ── THE SERVER OWNS THE SCROLLBACK ──────────────────────────────────────────
 *
 * The hub drops a frame a slow browser cannot take, which is safe for every
 * other payload because they are snapshots that re-emit. A terminal answer is
 * not: it is sent once and nothing re-derives it. So the server keeps the pane
 * and replays it on `page:focus`, and `term:scrollback` REPLACES what is on
 * screen rather than merging into it - one authority, so a dropped frame heals
 * by opening the page again and nothing can be drawn twice.
 */

/** A refusal code from the server, as a sentence. The server sends codes, not
 *  prose, so the wording lives on the side that renders it. */
const REFUSED: Record<string, string> = {
  denied: 'The Terminal is not available to you on this device.',
  unavailable: 'No device is selected.',
  busy: 'A command is still running.',
  toolong: 'That line is too long to send.',
  limited: 'Too many commands in the last minute. Wait a moment.',
  timeout: 'The device did not answer in time, so the command was cancelled.',
  failed: 'The command did not run.',
  stopped: 'Stopped.',
};

const HISTORY_MAX = 200;

export function initTerminalPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let mayRun = false;
  let identity = '';
  let user = '';
  let running = false;
  const history: string[] = [];
  let histIdx = -1;
  let draft = '';

  const promptText = (): string =>
    identity ? '[' + (user || 'admin') + '@' + identity + '] > ' : '> ';

  function setPrompt(): void {
    const p = el('terminalPrompt');
    if (p) p.textContent = promptText();
  }

  function note(text: string): void {
    const s = el('terminalStatus');
    if (s) s.textContent = text;
  }

  /** One command and its answer, built as nodes. */
  function blockNode(e: TermEntry): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'term-block';

    if (e.command) {
      const echo = document.createElement('div');
      echo.className = 'term-echo';
      const p = document.createElement('span');
      p.className = 'term-prompt';
      p.textContent = promptText();
      const c = document.createElement('span');
      c.className = 'term-cmd';
      c.textContent = e.command;
      echo.appendChild(p);
      echo.appendChild(c);
      wrap.appendChild(echo);
    }

    if (e.lines && e.lines.length) {
      // A <pre>: RouterOS aligns its output with spaces, so collapsing
      // whitespace would destroy the answer rather than just its look.
      const body = document.createElement('pre');
      body.className = 'term-out';
      body.textContent = e.lines.join('\n');
      wrap.appendChild(body);
    }
    if (e.truncated) {
      const t = document.createElement('div');
      t.className = 'term-trim';
      t.textContent = 'Output was longer than this page keeps and was cut short.';
      wrap.appendChild(t);
    }
    if (e.code) {
      const err = document.createElement('div');
      err.className = 'term-error';
      err.textContent = e.message || REFUSED[e.code] || REFUSED.failed!;
      wrap.appendChild(err);
    }
    return wrap;
  }

  function scrollBox(): HTMLElement | null {
    return el('terminalScroll');
  }

  /**
   * One rendered block per run, addressed by the server's `seq`.
   *
   * A run arrives TWICE: the echo frame when the line is sent, so a thirty-second
   * command is visible immediately, and the answer when it returns. The second
   * must REPLACE the first - appending both printed every command twice, once
   * bare and once above its output, which is what the browser showed before this
   * map existed. A refusal carries seq 0 and always appends, because it is not a
   * run and there is nothing of its own to replace.
   */
  const shown = new Map<number, HTMLElement>();

  function draw(e: TermEntry): void {
    const box = scrollBox();
    if (!box) return;
    // Follow only when already at the bottom, so reading back through the
    // scrollback is not yanked away by an answer arriving.
    const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 60;
    const node = blockNode(e);
    const prev = e.seq > 0 ? shown.get(e.seq) : undefined;
    if (prev && prev.parentNode === box) box.replaceChild(node, prev);
    else box.appendChild(node);
    if (e.seq > 0) shown.set(e.seq, node);
    // THE PROMPT GOES BACK TO THE END. appendChild MOVES an element that is
    // already in the document, so this is the whole mechanism: output is added
    // above the line you are typing on, exactly as a console does it.
    const live = el('terminalLive');
    if (live) box.appendChild(live);
    if (atBottom) box.scrollTop = box.scrollHeight;
  }

  /** Empty the pane on screen. The live prompt is not output and survives. */
  function clearScreen(): void {
    shown.clear();
    const box = scrollBox();
    const live = el('terminalLive');
    if (!box) return;
    while (box.firstChild) box.removeChild(box.firstChild);
    if (live) box.appendChild(live);
  }

  function setRunning(on: boolean): void {
    running = on;
    // NO RUN BUTTON. Enter runs a line, the way a console does; a button beside
    // the prompt is the thing that made this read as a form. Stop is here
    // because Ctrl-C alone is not an affordance anybody can see, and it appears
    // only while there is something to stop.
    const stop = el<HTMLButtonElement>('terminalStop');
    if (stop) stop.hidden = !on;
    const box = el<HTMLInputElement>('terminalInput');
    // The latch: a second line sent before the first answers would interleave
    // two blobs with no way to tell which answered what.
    if (box) box.disabled = on || !mayRun;
    const live = el('terminalLive');
    if (live) live.classList.toggle('is-busy', on);
  }

  function pushHistory(text: string): void {
    if (history[history.length - 1] !== text) history.push(text);
    if (history.length > HISTORY_MAX) history.shift();
    histIdx = -1;
    draft = '';
  }

  function recall(delta: number): void {
    const box = el<HTMLInputElement>('terminalInput');
    if (!box || !history.length) return;
    if (histIdx === -1) {
      draft = box.value;
      histIdx = history.length;
    }
    histIdx = Math.min(history.length, Math.max(0, histIdx + delta));
    box.value = histIdx === history.length ? draft : history[histIdx]!;
    const n = box.value.length;
    if (box.setSelectionRange) box.setSelectionRange(n, n);
  }

  function send(): void {
    if (running || !mayRun) return;
    const box = el<HTMLInputElement>('terminalInput');
    const text = (box?.value || '').trim();
    if (!text) return;
    if (box) box.value = '';
    pushHistory(text);
    setRunning(true);
    note('Running…');
    socket.emit('term:run', { line: text });
  }

  function stop(): void {
    if (!running) return;
    note('Stopping…');
    socket.emit('term:stop', {});
  }

  el<HTMLButtonElement>('terminalStop')?.addEventListener('click', () => stop());
  el<HTMLButtonElement>('terminalClear')?.addEventListener('click', () => {
    clearScreen();
    note('');
    // THE SERVER MUST FORGET IT TOO, or walking to another page and back
    // replays the pane this just emptied and undoes the clear.
    socket.emit('term:clear', {});
    el<HTMLInputElement>('terminalInput')?.focus();
  });

  // CLICKING THE PANE PUTS THE CARET BACK ON THE PROMPT, which is what a
  // terminal does - but not while text is selected, or copying output would
  // be impossible: mousedown-drag-release ends in a click.
  el('terminalScroll')?.addEventListener('mouseup', () => {
    const sel = typeof window !== 'undefined' && window.getSelection
      ? String(window.getSelection() || '') : '';
    if (!sel) el<HTMLInputElement>('terminalInput')?.focus();
  });

  /**
   * TYPING ANYWHERE ON THIS PAGE TYPES AT THE PROMPT.
   *
   * The click handler above only covers the scroller. Focus lands outside it
   * easily - the card header, the page background, a drag that selected a
   * character - and once it does, nothing brings it back: keys go nowhere and
   * the page looks broken while working perfectly. Reported as "hitting enter
   * does nothing", which is exactly what it would look like.
   *
   * So the page takes the keystroke the way a terminal does: it focuses the
   * prompt AND KEEPS THE CHARACTER, rather than focusing and swallowing the
   * first one.
   *
   * What it deliberately does NOT take:
   *
   *	another field        an input, textarea or select has its own text
   *	a button or a link   Enter and Space must still activate what you tabbed to
   *	Ctrl / Cmd / Alt     reload, copy, the browser's own keys
   *	anything multi-char  Tab, Escape, F-keys, arrows - so the page is still
   *	                     navigable by keyboard, which Tab in particular needs
   *
   * The app's digit and `/` shortcuts therefore do not fire while this page is
   * up, and that is correct rather than a casualty: they already do not fire
   * when the prompt has focus (see keyboard.ts, which skips INPUT), and a
   * terminal where `1` changed the page instead of typing `1` would be a
   * terminal you could not use.
   */
  document.addEventListener('keydown', (e) => {
    const ev = e as KeyboardEvent;
    if (!isVisible('terminal')) return;
    const inp = el<HTMLInputElement>('terminalInput');
    if (!inp || inp.disabled) return;
    if (document.activeElement === inp) return; // its own handler has this
    const tag = ((ev.target as HTMLElement | null)?.tagName || '').toUpperCase();
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' ||
        tag === 'BUTTON' || tag === 'A') return;
    if (ev.ctrlKey || ev.metaKey || ev.altKey) return;

    if (ev.key === 'Enter') {
      ev.preventDefault();
      inp.focus();
      send();
      return;
    }
    if (ev.key === 'Backspace') {
      ev.preventDefault();
      inp.focus();
      inp.value = inp.value.slice(0, -1);
      return;
    }
    if (ev.key.length === 1) {
      ev.preventDefault();
      inp.focus();
      inp.value += ev.key; // KEPT, not dropped
    }
  });

  el<HTMLInputElement>('terminalInput')?.addEventListener('keydown', (e) => {
    const ev = e as KeyboardEvent;
    if (ev.key === 'Enter') { ev.preventDefault(); send(); return; }
    if (ev.key === 'ArrowUp') { ev.preventDefault(); recall(-1); return; }
    if (ev.key === 'ArrowDown') { ev.preventDefault(); recall(1); return; }
    if ((ev.ctrlKey || ev.metaKey) && (ev.key === 'c' || ev.key === 'C')) {
      // CTRL-C IS STILL COPY WHEN SOMETHING IS SELECTED. Taking it
      // unconditionally would break copying the device's own output, which is
      // most of what anyone does with a terminal. Only an empty selection
      // means interrupt.
      const sel = typeof window !== 'undefined' && window.getSelection
        ? String(window.getSelection() || '') : '';
      if (sel) return;
      ev.preventDefault();
      if (running) stop();
      else {
        const box = el<HTMLInputElement>('terminalInput');
        if (box) box.value = '';
        histIdx = -1;
      }
    }
  });

  socket.on('term:output', (d: TermOutputPayload) => {
    if (d.running) {
      // The echo, sent before the command leaves, so a long command shows up
      // as soon as it is sent.
      draw({ ...d.entry, lines: [] });
      return;
    }
    draw(d.entry);
    if (d.done) {
      setRunning(false);
      note('');
    }
  });

  socket.on('term:scrollback', (d: TermScrollbackPayload) => {
    mayRun = d.mayRun;
    identity = d.identity || '';
    user = d.user || '';
    setPrompt();
    // REPLACE, never merge: the server's copy is the authority, so replaying it
    // cannot draw anything twice.
    clearScreen();
    for (const e of d.entries) draw(e);
    if (d.trimmed) note('Older commands were dropped from this pane.');
    else note(d.mayRun ? '' : d.why || '');
    setRunning(d.running);
    if (mayRun && !d.running) el<HTMLInputElement>('terminalInput')?.focus();
  });

  socket.on('router:switched', () => {
    // A result belongs to the device it was asked of. Drawing one device's
    // console text under another's prompt is a wrong answer that looks right.
    mayRun = false;
    identity = '';
    user = '';
    history.length = 0;
    histIdx = -1;
    draft = '';
    clearScreen();
    setPrompt();
    note('');
    setRunning(false);
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'terminal') {
      // The server answers page:focus with term:scrollback, which is what
      // draws the pane and enables the input.
      return;
    }
    // Leaving stops the line in flight: a command nobody is watching still
    // holds a channel on the device.
    stop();
  });

  if (isVisible('terminal')) setPrompt();
}
