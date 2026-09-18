// The Tools page: diagnostics run on the selected router (slice 8).
//
// Each tool is a form, a Run button and a result. The bounds — how many packets,
// how long — are the server's (internal/diag), so the form offers only what the
// server would run anyway, and a value edited past them is clamped there.
//
// ── A RESULT BELONGS TO THE ROUTER IT WAS ASKED OF ──────────────────────────
//
// A run takes seconds. If the operator switches router meanwhile, the result
// that lands is about the old one, and drawing it under the new router's name
// would be a wrong answer that looks right. So a switch forgets the pending run
// and clears what is shown, and a result nobody is waiting for is dropped.

import type { Socket } from '../socket';
import { esc, el } from '../dom';
import type { ToolsPingPayload, PingResult } from '../gen/payloads';

const REFUSED: Record<string, string> = {
  denied: 'You may not run tools on this router.',
  unavailable: 'No router is connected.',
  busy: 'Another tool is still running.',
};

const NOT_RUN = '<tr><td colspan="6" class="empty-state">Not run yet</td></tr>';

function ms(v: number | null | undefined): string {
  return v == null ? '—' : (v < 1 ? v.toFixed(3) : v.toFixed(1)) + ' ms';
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

function clearResult(): void {
  const summary = el('pingSummary');
  if (summary) summary.textContent = '';
  const rows = el('pingRows');
  if (rows) rows.innerHTML = NOT_RUN;
}

export function initToolsPage(socket: Socket): void {
  let pending = false;

  function setRunning(on: boolean, note: string): void {
    pending = on;
    const btn = el<HTMLButtonElement>('pingRun');
    if (btn) btn.disabled = on;
    const status = el('pingStatus');
    if (status) status.textContent = note;
  }

  el('pingForm')?.addEventListener('submit', (e) => {
    e.preventDefault();
    if (pending) return;
    const address = (el<HTMLInputElement>('pingAddress')?.value || '').trim();
    const count = Number(el<HTMLSelectElement>('pingCount')?.value || 4);
    if (!address) return;
    // THE LAST RESULT GOES AT ONCE, so a run that fails does not leave the
    // previous address's replies standing under its error.
    clearResult();
    setRunning(true, 'Running…');
    socket.emit('tools:ping', { address, count });
  });

  socket.on('tools:ping', (d: ToolsPingPayload) => {
    if (!pending) return;
    setRunning(false, '');
    if (d.result) {
      renderPing(d.result);
      return;
    }
    const status = el('pingStatus');
    if (status) status.textContent = REFUSED[d.code] || d.message || 'The ping did not run.';
  });

  socket.on('router:switched', () => {
    setRunning(false, '');
    clearResult();
  });
}
