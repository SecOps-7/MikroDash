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
// ── LIVE: PROGRESS, THEN THE RESULT, ON ONE EVENT ───────────────────────────
//
// The server streams each run: its event arrives with `done: false` as rows come
// in — the run so far, folded exactly as the finished one is — and once with
// `done: true` to end it. Both are drawn by the tool's one renderer, so the last
// progress frame and the result cannot disagree about what a row looks like. A
// progress frame leaves the run pending; only the done one settles it.
//
// ── A RESULT BELONGS TO THE ROUTER IT WAS ASKED OF ──────────────────────────
//
// A run takes seconds. If the operator switches router meanwhile, the result
// that lands is about the old one, and drawing it under the new router's name
// would be a wrong answer that looks right. So a switch forgets the pending run
// and clears what is shown, and a result nobody is waiting for is dropped.

import type { Socket } from '../socket';
import { esc, el, fmtMbps } from '../dom';
import type { PingResult, TracerouteResult, TorchResult, BtestResult } from '../gen/payloads';

const REFUSED: Record<string, string> = {
  denied: 'You may not run this tool on this router.',
  unavailable: 'No router is connected.',
  busy: 'Another tool is still running.',
};

/** One tool: the ids its markup uses, spelled out so each can be found, and
 *  what its form asks the server for. */
interface Tool {
  key: 'ping' | 'traceroute' | 'torch' | 'btest';
  /** Needs write access to Tools: loads the router or a link. */
  write: boolean;
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
    key: 'ping', write: false, form: 'pingForm', run: 'pingRun', status: 'pingStatus', summary: 'pingSummary', rows: 'pingRows', cols: 6,
    request: () => {
      const address = (el<HTMLInputElement>('pingAddress')?.value || '').trim();
      return address ? { address, count: Number(el<HTMLSelectElement>('pingCount')?.value || 4) } : null;
    },
  },
  {
    key: 'traceroute', write: false, form: 'traceForm', run: 'traceRun', status: 'traceStatus', summary: 'traceSummary',
    rows: 'traceRows', cols: 7,
    request: () => {
      const address = (el<HTMLInputElement>('traceAddress')?.value || '').trim();
      return address ? { address, maxHops: Number(el<HTMLSelectElement>('traceHops')?.value || 15) } : null;
    },
  },
  {
    key: 'torch', write: true, form: 'torchForm', run: 'torchRun', status: 'torchStatus', summary: 'torchSummary',
    rows: 'torchRows', cols: 5,
    request: () => {
      const iface = el<HTMLSelectElement>('torchInterface')?.value || '';
      return iface ? { interface: iface, seconds: Number(el<HTMLSelectElement>('torchSeconds')?.value || 5) } : null;
    },
  },
  {
    key: 'btest', write: true, form: 'btestForm', run: 'btestRun', status: 'btestStatus', summary: 'btestSummary',
    rows: 'btestRows', cols: 2,
    request: () => {
      const address = (el<HTMLInputElement>('btestAddress')?.value || '').trim();
      if (!address) return null;
      const pass = el<HTMLInputElement>('btestPassword');
      const req = {
        address,
        user: (el<HTMLInputElement>('btestUser')?.value || '').trim(),
        password: pass?.value || '',
        seconds: Number(el<HTMLSelectElement>('btestSeconds')?.value || 5),
        protocol: el<HTMLSelectElement>('btestProtocol')?.value || 'tcp',
        direction: el<HTMLSelectElement>('btestDirection')?.value || 'both',
      };
      // TYPED PER RUN: the field is emptied the moment the run is sent, so the
      // password is not sitting in the page for the next person at the screen.
      if (pass) pass.value = '';
      return req;
    },
  },
];

const bps = (v: number): string => fmtMbps(v / 1e6);

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

function renderTorch(r: TorchResult): void {
  const summary = el('torchSummary');
  if (summary) {
    summary.textContent = 'Watched ' + r.interface + ' for ' + r.seconds + ' s · average rx ' + bps(r.totalRxBps) +
      ', tx ' + bps(r.totalTxBps) + (r.omitted ? ' · ' + r.omitted + ' quieter flows not shown' : '');
  }
  const rows = el('torchRows');
  if (!rows) return;
  if (!r.flows.length) {
    rows.innerHTML = '<tr><td colspan="5" class="empty-state">No traffic seen</td></tr>';
    return;
  }
  const end = (a: string, p: string): string => esc(a) + (p ? ':' + esc(p) : '');
  rows.innerHTML = r.flows.map((f) =>
    '<tr>' +
    '<td>' + esc(f.protocol) + '</td>' +
    '<td>' + end(f.srcAddress, f.srcPort) + '</td>' +
    '<td>' + end(f.dstAddress, f.dstPort) + '</td>' +
    '<td>' + bps(f.rxBps) + '</td>' +
    '<td>' + bps(f.txBps) + '</td>' +
    '</tr>').join('');
}

function renderBtest(r: BtestResult): void {
  const summary = el('btestSummary');
  // A report still connecting has no duration or direction yet.
  if (summary) {
    summary.textContent = 'Tested to ' + r.address + (r.duration ? ' for ' + r.duration : '') +
      (r.direction ? ', ' + r.direction : '');
  }
  const rows = el('btestRows');
  if (!rows) return;
  const line = (k: string, v: string): string => '<tr><th>' + k + '</th><td>' + v + '</td></tr>';
  rows.innerHTML = line('Receive (average)', bps(r.rxBps)) + line('Transmit (average)', bps(r.txBps)) +
    line('Lost packets', String(r.lostPackets)) +
    line('CPU load', 'this router ' + r.localCpu + '%, the far one ' + r.remoteCpu + '%');
}

export function initToolsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let pending: Tool | null = null;
  // WHETHER THIS VIEWER MAY RUN THE WRITE TOOLS, from `tools:caps`. False until
  // it arrives, so a Run button that would only be refused is never offered.
  let mayWrite = false;

  function setRunning(t: Tool | null, note: string): void {
    pending = t;
    for (const x of TOOLS) {
      const btn = el<HTMLButtonElement>(x.run);
      if (btn) btn.disabled = t !== null || (x.write && !mayWrite);
    }
    if (t) {
      const status = el(t.status);
      if (status) status.textContent = note;
    }
  }

  function settle(t: Tool, d: { code: string; message: string; done: boolean }, draw: () => void): void {
    if (pending !== t) return;
    if (!d.done) {
      // The run so far. Still pending: the status keeps saying it is running.
      draw();
      return;
    }
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
  const [ping, trace, torch, btest] = TOOLS as [Tool, Tool, Tool, Tool];
  socket.on('tools:ping', (d) => settle(ping, d, () => { if (d.result) renderPing(d.result); }));
  socket.on('tools:traceroute', (d) => settle(trace, d, () => { if (d.result) renderTraceroute(d.result); }));
  socket.on('tools:torch', (d) => settle(torch, d, () => { if (d.result) renderTorch(d.result); }));
  socket.on('tools:btest', (d) => settle(btest, d, () => { if (d.result) renderBtest(d.result); }));

  socket.on('tools:caps', (d) => {
    mayWrite = d.mayWrite;
    const sel = el<HTMLSelectElement>('torchInterface');
    if (sel) sel.innerHTML = d.interfaces.map((n) => '<option>' + esc(n) + '</option>').join('');
    setRunning(pending, '');
    if (!pending) {
      for (const t of TOOLS) {
        const status = el(t.status);
        if (t.write && status) status.textContent = mayWrite ? '' : 'Needs write access to Tools.';
      }
    }
  });
  // ASKED FOR WHEN THE PAGE OPENS, and again on a router switch while it is
  // open: the permission and the interfaces are both per router.
  const askCaps = (): void => socket.emit('tools:caps', {});
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'tools') askCaps();
  });

  socket.on('router:switched', () => {
    if (pending) {
      const status = el(pending.status);
      if (status) status.textContent = '';
    }
    mayWrite = false;
    setRunning(null, '');
    for (const t of TOOLS) clearResult(t);
    if (isVisible('tools')) askCaps();
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
