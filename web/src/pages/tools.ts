// The Tools page: diagnostics run on the selected router (slice 8).
//
// Each tool is a form, a Run button and a result. The bounds — how many packets,
// how many hops, how long — are the server's (internal/diag), so the form offers
// only what the server would run anyway, and a value edited past them is clamped
// there.
//
// ── ONE RUN AT A TIME ───────────────────────────────────────────────────────
//
// The server runs one diagnostic per connection, so every Run button is
// disabled while any tool is running, and a result is accepted only by the
// tool that is waiting for one.
//
// ── A RESULT BELONGS TO THE ROUTER IT WAS ASKED OF ──────────────────────────
//
// A run takes seconds. If the operator switches router meanwhile, the result
// that lands is about the old one, and drawing it under the new router's name
// would be a wrong answer that looks right. So a switch forgets the pending run
// and clears what is shown, and a result nobody is waiting for is dropped.

import type { Socket } from '../socket';
import { esc, el } from '../dom';
import type { PingResult, TracerouteResult } from '../gen/payloads';

const REFUSED: Record<string, string> = {
  denied: 'You may not run this tool on this router.',
  unavailable: 'No router is connected.',
  busy: 'Another tool is still running.',
};

/** One tool: the ids its markup uses, spelled out so each can be found, and
 *  what its form asks the server for. */
interface Tool {
  key: 'ping' | 'traceroute';
  form: string;
  run: string;
  status: string;
  summary: string;
  rows: string;
  cols: number;
  request(): Record<string, unknown> | null;
}

const TOOLS: Tool[] = [
  {
    key: 'ping', form: 'pingForm', run: 'pingRun', status: 'pingStatus', summary: 'pingSummary', rows: 'pingRows', cols: 6,
    request: () => {
      const address = (el<HTMLInputElement>('pingAddress')?.value || '').trim();
      return address ? { address, count: Number(el<HTMLSelectElement>('pingCount')?.value || 4) } : null;
    },
  },
  {
    key: 'traceroute', form: 'traceForm', run: 'traceRun', status: 'traceStatus', summary: 'traceSummary',
    rows: 'traceRows', cols: 7,
    request: () => {
      const address = (el<HTMLInputElement>('traceAddress')?.value || '').trim();
      return address ? { address, maxHops: Number(el<HTMLSelectElement>('traceHops')?.value || 15) } : null;
    },
  },
];

function ms(v: number | null | undefined): string {
  return v == null ? '—' : (v < 1 ? v.toFixed(3) : v.toFixed(1)) + ' ms';
}

function notRun(t: Tool): string {
  return '<tr><td colspan="' + t.cols + '" class="empty-state">Not run yet</td></tr>';
}

function clearResult(t: Tool): void {
  const summary = el(t.summary);
  if (summary) summary.textContent = '';
  const rows = el(t.rows);
  if (rows) rows.innerHTML = notRun(t);
}

function renderPing(r: PingResult): void {
  const summary = el('pingSummary');
  if (summary) {
    summary.textContent = r.sent + ' sent, ' + r.received + ' received, ' + r.lossPct + '% loss' +
      (r.avgMs == null ? '' : ' · min ' + ms(r.minMs) + ', avg ' + ms(r.avgMs) + ', max ' + ms(r.maxMs));
  }
  const rows = el('pingRows');
  if (!rows) return;
  if (!r.replies.length) {
    rows.innerHTML = '<tr><td colspan="6" class="empty-state">No replies</td></tr>';
    return;
  }
  rows.innerHTML = r.replies.map((p) => {
    const lost = p.status !== '';
    return '<tr>' +
      '<td>' + p.seq + '</td>' +
      '<td>' + esc(p.host) + '</td>' +
      '<td>' + (lost ? '—' : ms(p.rttMs)) + '</td>' +
      '<td>' + (lost ? '—' : p.ttl) + '</td>' +
      '<td>' + (lost ? '—' : p.size) + '</td>' +
      '<td>' + (lost ? '<span class="wg-down">' + esc(p.status) + '</span>' : '<span class="wg-up">reply</span>') + '</td>' +
      '</tr>';
  }).join('');
}

function renderTraceroute(r: TracerouteResult): void {
  const summary = el('traceSummary');
  if (summary) summary.textContent = r.hops.length + (r.hops.length === 1 ? ' hop' : ' hops') + (r.error ? ' · ' + r.error : '');
  const rows = el('traceRows');
  if (!rows) return;
  if (!r.hops.length) {
    rows.innerHTML = '<tr><td colspan="7" class="empty-state">No hops</td></tr>';
    return;
  }
  rows.innerHTML = r.hops.map((h) =>
    '<tr>' +
    '<td>' + h.hop + '</td>' +
    '<td>' + (h.address ? esc(h.address) : '—') + '</td>' +
    '<td>' + h.lossPct + '%</td>' +
    '<td>' + (h.timedOut ? '<span class="wg-down">timeout</span>' : ms(h.lastMs)) + '</td>' +
    '<td>' + ms(h.bestMs) + '</td>' +
    '<td>' + ms(h.worstMs) + '</td>' +
    '<td>' + esc(h.status) + '</td>' +
    '</tr>').join('');
}

export function initToolsPage(socket: Socket): void {
  let pending: Tool | null = null;

  function setRunning(t: Tool | null, note: string): void {
    pending = t;
    for (const x of TOOLS) {
      const btn = el<HTMLButtonElement>(x.run);
      if (btn) btn.disabled = t !== null;
    }
    if (t) {
      const status = el(t.status);
      if (status) status.textContent = note;
    }
  }

  function settle(t: Tool, d: { code: string; message: string }, draw: () => void): void {
    if (pending !== t) return;
    setRunning(null, '');
    const status = el(t.status);
    if (status) status.textContent = '';
    if (!d.code) {
      draw();
      return;
    }
    if (status) status.textContent = REFUSED[d.code] || d.message || 'The tool did not run.';
  }

  for (const t of TOOLS) {
    el(t.form)?.addEventListener('submit', (e) => {
      e.preventDefault();
      if (pending) return;
      const req = t.request();
      if (!req) return;
      // THE LAST RESULT GOES AT ONCE, so a run that fails does not leave the
      // previous address's result standing under its error.
      clearResult(t);
      setRunning(t, 'Running…');
      socket.emit('tools:' + t.key, req);
    });
  }
  const [ping, trace] = TOOLS as [Tool, Tool];
  socket.on('tools:ping', (d) => settle(ping, d, () => { if (d.result) renderPing(d.result); }));
  socket.on('tools:traceroute', (d) => settle(trace, d, () => { if (d.result) renderTraceroute(d.result); }));

  socket.on('router:switched', () => {
    if (pending) {
      const status = el(pending.status);
      if (status) status.textContent = '';
    }
    setRunning(null, '');
    for (const t of TOOLS) clearResult(t);
  });

  // The tab strip: one panel shown at a time.
  el('toolsTabs')?.addEventListener('click', (e) => {
    const tab = (e.target as HTMLElement | null)?.closest?.('[data-tooltab]');
    const key = tab?.getAttribute('data-tooltab');
    if (!key) return;
    for (const t of TOOLS) {
      const panel = el('toolPanel-' + t.key);
      if (panel) panel.style.display = t.key === key ? '' : 'none';
    }
    document.querySelectorAll('#toolsTabs [data-tooltab]').forEach((b) => {
      const on = b.getAttribute('data-tooltab') === key;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', on ? 'true' : 'false');
    });
  });
}
