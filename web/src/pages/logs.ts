// The Logs page — a port of the `── Logs` region in public/app.js.
//
// The only page fed by a PUSH STREAM. `logs:history` arrives once with the
// backlog; `logs:new` arrives one line at a time as the router writes it. The
// two paths are deliberately different and both are reproduced:
//
//   history  rebuilds the buffer and re-renders the whole view
//   new      appends ONE line to the DOM and trims the head
//
// Re-rendering everything on each arrival would be simpler and wrong: a busy
// router writes several lines a second, and rebuilding a two-thousand-line view
// that often loses the scroll position, the text selection, and any hope of
// reading it.
//
// ── EVERY LINE IS PRE-RENDERED ──────────────────────────────────────────────
//
// The buffer holds `{html, severity, text}`, not the entry. The HTML is built
// once on arrival and the lowercased search text with it, so filtering is a
// string test over a prepared array rather than an escape-and-format pass per
// keystroke. That is the live design and it is why the search stays responsive
// at two thousand lines.

import { esc, el, debounce } from '../dom';
import type { Socket } from '../socket';

export interface LogEntry {
  ts: number; time: string; topics: string; message: string; severity: string;
}

interface Buffered { html: string; severity: string; text: string }

const MAX_LOG_LINES = 2000;

// A topic gets a colour so the eye can group without reading. Substring
// matching on the whole list, in this order — a line carries several topics and
// the first branch that matches wins.
function topicClass(t: string): string {
  const s = String(t).toLowerCase();
  if (s.includes('firewall') || s.includes('forward')) return 'log-firewall';
  if (s.includes('dhcp')) return 'log-dhcp';
  if (s.includes('wireless') || s.includes('wifi') || s.includes('wlan')) return 'log-wireless';
  if (s.includes('system')) return 'log-system';
  return 'log-topic';
}

function sevClass(s: string): string {
  return s === 'error' ? 'log-error'
    : s === 'warning' ? 'log-warning'
      : s === 'debug' ? 'log-debug' : 'log-info';
}

function buildLogHtml(l: LogEntry): string {
  return '<div class="log-line"><span class="log-time">' + esc(l.time) + '</span> ' +
    '<span class="' + topicClass(l.topics) + '">[' + esc(l.topics) + ']</span> ' +
    '<span class="' + sevClass(l.severity) + '">' + esc(l.message) + '</span></div>';
}

export function initLogsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const logsEl = el('logs');
  const logSearch = el<HTMLInputElement>('logSearch');
  const logSeverity = el<HTMLSelectElement>('logSeverity');
  const toggleScroll = el('toggleScroll');
  const clearLogs = el('clearLogs');
  if (!logsEl) return;

  const logCountEls: Record<string, HTMLElement | null> = {
    error: el('logCountError'), warning: el('logCountWarning'),
    info: el('logCountInfo'), debug: el('logCountDebug'),
  };

  let logBuffer: Buffered[] = [];
  let autoScroll = true;
  let logFilter = '';
  let logLevel = '';

  function updateLogCounts(): void {
    const counts: Record<string, number> = { error: 0, warning: 0, info: 0, debug: 0 };
    logBuffer.forEach((e) => { if (counts[e.severity] !== undefined) counts[e.severity]!++; });
    Object.keys(counts).forEach((sev) => {
      const e = logCountEls[sev];
      if (!e) return;
      const n = counts[sev]!;
      // "1 error" but "2 errors", and `info`/`debug` never pluralise — they are
      // the level's name, not a count of things with a plural.
      e.textContent = n + ' ' + (sev === 'error' && n !== 1 ? 'errors'
        : sev === 'warning' && n !== 1 ? 'warnings' : sev);
    });
  }

  function flushLogs(): void {
    const f = logBuffer.filter((e) => {
      if (logLevel && e.severity !== logLevel) return false;
      if (logFilter && e.text.indexOf(logFilter) === -1) return false;
      return true;
    });
    logsEl!.innerHTML = f.map((e) => e.html).join('');
    if (autoScroll) logsEl!.scrollTop = logsEl!.scrollHeight;
    updateLogCounts();
  }

  function bufferedOf(line: LogEntry): Buffered {
    return {
      html: buildLogHtml(line),
      severity: line.severity,
      // Lowercased ONCE, on arrival: the search runs over this, not over the
      // rendered HTML, so a filter cannot match a class name or an escape.
      text: (line.time + ' [' + line.topics + '] ' + line.message).toLowerCase(),
    };
  }

  // Initialise the badge labels to "0 …" immediately, so the header is not blank
  // before the first payload.
  updateLogCounts();

  socket.on('logs:history', (data: LogEntry[] | { entries?: LogEntry[] }) => {
    const lines = Array.isArray(data) ? data : ((data && data.entries) || []);
    logBuffer = lines.map(bufferedOf);
    if (logBuffer.length > MAX_LOG_LINES) {
      logBuffer.splice(0, logBuffer.length - MAX_LOG_LINES);
    }
    flushLogs();
  });

  socket.on('logs:new', (line: LogEntry) => {
    const entry = bufferedOf(line);
    logBuffer.push(entry);
    if (logBuffer.length > MAX_LOG_LINES) logBuffer.shift();
    // The COUNTS move even when the line is filtered out — a hidden error is
    // still an error, and the badge is how you find out it happened.
    updateLogCounts();
    if (logLevel && entry.severity !== logLevel) return;
    if (logFilter && entry.text.indexOf(logFilter) === -1) return;
    logsEl!.insertAdjacentHTML('beforeend', entry.html);
    while (logsEl!.children.length > MAX_LOG_LINES) {
      logsEl!.removeChild(logsEl!.firstElementChild!);
    }
    if (autoScroll) logsEl!.scrollTop = logsEl!.scrollHeight;
  });

  logSearch?.addEventListener('input', debounce(() => {
    logFilter = (logSearch.value || '').trim().toLowerCase();
    flushLogs();
  }, 200));

  logSeverity?.addEventListener('change', () => {
    logLevel = logSeverity.value;
    Object.keys(logCountEls).forEach((s) => {
      logCountEls[s]?.classList.toggle('active', s === logLevel);
    });
    flushLogs();
  });

  toggleScroll?.addEventListener('click', () => {
    autoScroll = !autoScroll;
    toggleScroll.textContent = autoScroll ? 'Pause' : 'Resume';
  });

  // Clear empties the VIEW and the buffer, not the router's log. The next
  // history replay refills it, which is the intended escape hatch: it is a way
  // to stop reading what has already been read.
  clearLogs?.addEventListener('click', () => {
    logBuffer = [];
    logsEl!.innerHTML = '';
    updateLogCounts();
  });

  // A badge doubles as a filter toggle: clicking the active one clears it.
  Object.keys(logCountEls).forEach((sev) => {
    const e = logCountEls[sev];
    if (!e) return;
    e.addEventListener('click', () => {
      if (logLevel === sev) {
        logLevel = '';
        if (logSeverity) logSeverity.value = '';
      } else {
        logLevel = sev;
        if (logSeverity) logSeverity.value = sev;
      }
      Object.keys(logCountEls).forEach((s) => {
        logCountEls[s]?.classList.toggle('active', s === logLevel);
      });
      flushLogs();
    });
  });

  void isVisible;
}
