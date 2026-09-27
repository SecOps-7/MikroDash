import type { Socket } from '../socket';
import { el } from '../dom';
import type { TermCompletePayload, TermCompletion, TermEntry, TermOutputPayload,
  TermScrollbackPayload } from '../gen/payloads';

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
  /** The menu the session is in, as the SERVER reports it. Never guessed here:
   *  the server resolves the path and decides what is a menu, so the prompt
   *  cannot drift from what a command would actually run against. */
  let cwd = '';
  const history: string[] = [];
  let histIdx = -1;
  let draft = '';

  const promptAt = (at: string): string =>
    (identity ? '[' + (user || 'admin') + '@' + identity + '] ' : '') + (at || '') + '> ';
  const promptText = (): string => promptAt(cwd);

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
      // THE PROMPT THIS LINE WAS TYPED AT, not the one we are at now. A line
      // that walks into a menu is typed at the old prompt and lands at a new
      // one, and rendering the live prompt here would rewrite history every
      // time you moved - including on a replayed scrollback.
      p.textContent = promptAt(e.cwd);
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

  /** Put a block in the pane, keeping the prompt at the end. */
  function place(node: HTMLElement, replacing?: HTMLElement): void {
    const box = scrollBox();
    if (!box) return;
    // Follow only when already at the bottom, so reading back through the
    // scrollback is not yanked away by an answer arriving.
    const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 60;
    if (replacing && replacing.parentNode === box) box.replaceChild(node, replacing);
    else box.appendChild(node);
    // THE PROMPT GOES BACK TO THE END. appendChild MOVES an element that is
    // already in the document, so this is the whole mechanism: output is added
    // above the line you are typing on, exactly as a console does it.
    const live = el('terminalLive');
    if (live) box.appendChild(live);
    if (atBottom) box.scrollTop = box.scrollHeight;
  }

  function draw(e: TermEntry): void {
    const node = blockNode(e);
    place(node, e.seq > 0 ? shown.get(e.seq) : undefined);
    if (e.seq > 0) shown.set(e.seq, node);
  }

  /**
   * A BARE ENTER STILL MOVES THE LINE ON.
   *
   * Pressing Enter on an empty line used to do nothing at all, which is the
   * one thing a console never does: it prints a fresh prompt and moves down.
   * Drawn here rather than sent, because an empty line has nothing to run - it
   * costs the device nothing, earns no audit row, and is purely the terminal
   * behaving like one.
   */
  function blankLine(): void {
    const wrap = document.createElement('div');
    wrap.className = 'term-block';
    const echo = document.createElement('div');
    echo.className = 'term-echo';
    const p = document.createElement('span');
    p.className = 'term-prompt';
    p.textContent = promptText();
    echo.appendChild(p);
    wrap.appendChild(echo);
    place(wrap);
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

  /**
   * TAB COMPLETION, DONE BY THE DEVICE.
   *
   * The browser sends the partial line and the device answers with its own
   * candidates - the same engine an SSH session uses - each carrying the text,
   * the offset it splices at, its help and its style. Nothing here knows the
   * RouterOS command tree, which is why it cannot drift from the device in
   * front of it or miss a value that only that device has.
   *
   * Two presses, like a console: the first extends as far as the candidates
   * agree, the second lists them.
   */
  let lastListed = '';

  function longestCommonPrefix(xs: string[]): string {
    if (!xs.length) return '';
    let p = xs[0]!;
    for (const x of xs) {
      let i = 0;
      while (i < p.length && i < x.length && p[i] === x[i]) i++;
      p = p.slice(0, i);
    }
    return p;
  }

  function completionsBlock(line: string, cands: TermCompletion[]): TermEntry {
    // Rendered as an ordinary output block, so it scrolls with everything else
    // and the prompt stays below it.
    const width = Math.min(28, Math.max(...cands.map((c) => c.text.length)) + 2);
    const lines = cands.map((c) =>
      '  ' + c.text.padEnd(width) + (c.help ? ' ' + c.help : ''));
    return { seq: 0, at: Date.now(), command: '', lines, truncated: false, ms: 0,
             code: '', message: '' } as TermEntry;
  }

  function askComplete(): void {
    const box = el<HTMLInputElement>('terminalInput');
    if (!box || !mayRun || running) return;
    socket.emit('term:complete', { line: box.value });
  }

  function send(line?: string): void {
    if (running || !mayRun) return;
    const box = el<HTMLInputElement>('terminalInput');
    const text = (line ?? box?.value ?? '').trim();
    if (!text) {
      if (box) box.value = '';
      blankLine();
      return;
    }
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

  /**
   * A PASTED BLOCK RUNS AS ONE.
   *
   * `/execute` takes a whole script - up to 64 kB - so a block of commands does
   * not need splitting into lines and feeding in one at a time. It goes to the
   * device intact and runs there, which is both simpler and better behaved than
   * a console: no bracketed-paste quirks, no line-length ceiling, and the
   * device sees the block the way the author wrote it.
   *
   * A SINGLE-LINE paste is left alone and just lands in the input, because
   * running on paste would be a surprise. Only a block - something with a
   * newline in it, which is unambiguously more than one command - runs.
   */
  el<HTMLInputElement>('terminalInput')?.addEventListener('paste', (e) => {
    const ev = e as ClipboardEvent;
    const pasted = ev.clipboardData?.getData('text') ?? '';
    if (!/[\r\n]/.test(pasted)) return; // ordinary paste; let the browser do it
    ev.preventDefault();
    const box = el<HTMLInputElement>('terminalInput');
    const whole = ((box?.value ?? '') + pasted)
      .replace(/\r\n?/g, '\n')
      .replace(/\n+$/, '');
    if (box) box.value = '';
    send(whole);
  });

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
    if (ev.key === 'Tab') {
      // The browser's own job for Tab is to move focus. On a terminal it is
      // completion, and the page gives keyboard users the header buttons and
      // the nav to tab through instead.
      ev.preventDefault();
      askComplete();
      return;
    }
    if (ev.key === 'ArrowUp') { ev.preventDefault(); recall(-1); return; }
    if (ev.key === 'ArrowDown') { ev.preventDefault(); recall(1); return; }
    if (ev.key === '?') {
      // `?` LISTS HELP, BUT ONLY AT A TOKEN BOUNDARY.
      //
      // The console pops help on any `?`, which means it cannot be typed into
      // a comment or a regex without a fight. Here it only lists when the line
      // is empty or ends in a space or a slash - where help is what you meant -
      // and is an ordinary character everywhere else. A deliberate difference
      // from the console, in the direction of not losing what you typed.
      const v = (el<HTMLInputElement>('terminalInput')?.value ?? '');
      if (v === '' || /[\s/]$/.test(v)) {
        ev.preventDefault();
        lastListed = '';       // force the list rather than an extension
        askComplete();
        return;
      }
    }
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
    // The menu rides on every frame rather than arriving as its own event: it
    // only ever changes when a frame is being sent anyway, and a separate
    // event could land out of order with the line that caused it.
    if (d.cwd !== cwd) { cwd = d.cwd; setPrompt(); }
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

  socket.on('term:complete', (d: TermCompletePayload) => {
    const box = el<HTMLInputElement>('terminalInput');
    if (!box) return;
    // A REPLY TO AN OLDER LINE IS DROPPED. Completion is a round trip to the
    // device; splicing a candidate computed for what used to be typed would
    // corrupt the line rather than complete it.
    if (d.line !== box.value) return;
    const all = d.candidates || [];
    if (!all.length) return;

    // ── ONLY THE CANDIDATES FOR THE TOKEN BEING TYPED ──────────────────────
    //
    // The device answers about more than one position at once. For `/ip add`
    // it offers `address` at offset 4 - the word being typed - AND `/` at
    // offset 7, which is not an alternative to it but a thing that could come
    // NEXT. Treating the two as one set makes their common prefix empty, so
    // the line never completes and a list appears instead.
    //
    // The token being typed is the earliest position offered, so the lowest
    // offset wins and the rest are what-comes-next. Found in a browser against
    // a real device; every unit test here had been written with candidates
    // that shared one offset, which the device never does.
    const off = Math.min(...all.map((c) => c.offset));
    const cands = all.filter((c) => c.offset === off);
    const stem = d.line.slice(off);
    const common = longestCommonPrefix(cands.map((c) => c.text));
    // EXTEND AS FAR AS THEY AGREE. With a single candidate the common prefix
    // IS that candidate, so this covers it too - there was a separate
    // `cands.length === 1` branch here and a mutation sweep proved it
    // unreachable, which is the only reason anyone would have noticed.
    if (common.length > stem.length) {
      box.value = d.line.slice(0, off) + common;
      lastListed = '';
      return;
    }
    // They agree no further, so list them - once. Pressing again does not
    // reprint the same block.
    if (lastListed === d.line) return;
    lastListed = d.line;
    draw(completionsBlock(d.line, cands));
  });

  socket.on('term:scrollback', (d: TermScrollbackPayload) => {
    mayRun = d.mayRun;
    identity = d.identity || '';
    user = d.user || '';
    cwd = d.cwd || '';
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
    cwd = '';
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
