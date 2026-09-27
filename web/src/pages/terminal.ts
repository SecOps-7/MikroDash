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
    if (atBottom) box.scrollTop = box.scrollHeight;
  }

  function clearScreen(): void {
    shown.clear();
    const box = scrollBox();
    if (!box) return;
    while (box.firstChild) box.removeChild(box.firstChild);
  }

  function setRunning(on: boolean): void {
    running = on;
    const btn = el<HTMLButtonElement>('terminalRun');
    const box = el<HTMLInputElement>('terminalInput');
    if (btn) {
      btn.disabled = !mayRun;
      btn.textContent = on ? 'Stop' : 'Run';
      btn.classList.toggle('sbtn-danger', on);
      btn.classList.toggle('sbtn-primary', !on);
    }
    // The latch: a second line sent before the first answers would interleave
    // two blobs with no way to tell which answered what.
    if (box) box.disabled = on || !mayRun;
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

  el<HTMLButtonElement>('terminalRun')?.addEventListener('click', () => {
    if (running) stop();
    else send();
  });
  el<HTMLButtonElement>('terminalClear')?.addEventListener('click', () => {
    clearScreen();
    note('');
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
