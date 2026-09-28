// The Tools pages: diagnostics run on the selected router (slice 8).
//
// Each tool is a form, a Run button and a result. The bounds - how many packets,
// how many hops, how long - are the server's (internal/diag), so the form offers
// only what the server would run anyway, and a value edited past them is clamped
// there.
//
// ── FIVE PAGES, ONE MODULE, AND THAT IS NOT AN OVERSIGHT ────────────────────
//
// Ping, Traceroute, Torch and Bandwidth Test were four tabs on one `tools` page
// until 2026-09-27 and are pages under a Tools nav category now, with the Packet
// Sniffer beside them. They are still ONE module because the state they share is
// real and not incidental: the server runs one diagnostic per CONNECTION, not per
// page, so the pending run, the busy slot and the caps frame belong to all five
// at once. Five modules would need a sixth to hold that, which is the same code
// with a seam in it.
//
// Every page's markup is composed into the one document, so all five forms exist
// whatever page is on screen; the ids below find them the same way they always
// did.
//
// ── THE SNIFFER IS THE ONE THAT RUNS UNTIL IT IS STOPPED ────────────────────
//
// The other four have a length: a packet count, a hop limit, a number of
// seconds. A capture has none - it is on until Stop, or until the hour cap every
// continuous run has - so its Start button is a Stop for the whole capture
// rather than for an overrun, and its frames are whole snapshots rather than
// increments. Everything else about it is the shared lifecycle.
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
// in - the run so far, folded exactly as the finished one is - and once with
// `done: true` to end it. Ping, traceroute and bandwidth test draw cards or a
// map above their tables from the same frames (tools-ping-cards.ts,
// tools-trace-map.ts, tools-btest-cards.ts).
//
// ── RUN BECOMES STOP ────────────────────────────────────────────────────────
//
// While a tool runs, its own button is a red Stop, and pressing it sends
// `tools:stop`. The server cancels the command on the router and ends the run
// with code `stopped` and the run so far, which is drawn like any other frame.
// Leaving the page stops a run too: a continuous ping nobody is watching would
// otherwise hold a channel on the router for its whole hour. Both are drawn by the tool's one renderer, so the last
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
import { esc, el, fmtBytes, fmtMbps, protoPill, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import type { PingResult, TracerouteResult, TorchResult, BtestResult, SnifferResult } from '../gen/payloads';
import { renderPingCards } from './tools-ping-cards';
import { renderBtestCards } from './tools-btest-cards';
import { renderSnifferCards } from './tools-sniffer-cards';
import { createTraceMap, type TraceMap } from './tools-trace-map';

/** What the page says when the DEVICE refuses the sniffer, which is not a
 *  permission and cannot be granted from here. */
const DEVICE_REFUSES = 'This device does not allow the packet sniffer: it is switched off in ' +
  'device-mode, which can only be changed at the device itself.';

const NEEDS_WRITE = 'Needs write access to this page.';

const REFUSED: Record<string, string> = {
  denied: 'You may not run this tool on this router.',
  unavailable: 'No router is connected.',
  busy: 'Another tool is still running.',
  'device-mode': DEVICE_REFUSES,
};

/** One tool: the page it lives on, the ids its markup uses, spelled out so each
 *  can be found, and what its form asks the server for. */
interface Tool {
  key: 'ping' | 'traceroute' | 'torch' | 'btest' | 'sniffer';
  /** The page key - the URL, the markup id and the PERMISSION key. */
  page: string;
  /** Needs write access to its own page: loads the router or a link. */
  write: boolean;
  form: string;
  run: string;
  status: string;
  summary: string;
  rows: string;
  cols: number;
  /**
   * The count pill beside the title, or '' for the Bandwidth Test page, which
   * lists one run's measurements rather than a set of rows and has none.
   */
  badge: string;
  /** The Run button's own word, which it goes back to after Stop. */
  label: string;
  request(): Record<string, unknown> | null;
}

/** A count picker's choice: a number, or its last option, Continuous, which
 *  runs until stopped (0 goes to the server as "no count"). */
function pick(id: string, fallback: number): { n: number; continuous: boolean } {
  const v = el<HTMLSelectElement>(id)?.value || '';
  return v === 'continuous' ? { n: 0, continuous: true } : { n: Number(v || fallback), continuous: false };
}

const TOOLS: Tool[] = [
  {
    key: 'ping', page: 'tools-ping', write: false, form: 'pingForm', run: 'pingRun', status: 'pingStatus',
    summary: 'pingSummary', rows: 'pingRows', cols: 6, badge: 'pingBadge', label: 'Ping',
    request: () => {
      const address = (el<HTMLInputElement>('pingAddress')?.value || '').trim();
      const c = pick('pingCount', 10);
      return address ? { address, count: c.n, continuous: c.continuous } : null;
    },
  },
  {
    key: 'traceroute', page: 'tools-traceroute', write: false, form: 'traceForm', run: 'traceRun',
    status: 'traceStatus', summary: 'traceSummary', rows: 'traceRows', cols: 7, badge: 'traceBadge', label: 'Trace',
    request: () => {
      const address = (el<HTMLInputElement>('traceAddress')?.value || '').trim();
      return address ? { address, maxHops: Number(el<HTMLSelectElement>('traceHops')?.value || 15) } : null;
    },
  },
  {
    key: 'torch', page: 'tools-torch', write: true, form: 'torchForm', run: 'torchRun', status: 'torchStatus',
    summary: 'torchSummary', rows: 'torchRows', cols: 5, badge: 'torchBadge', label: 'Watch',
    request: () => {
      const iface = el<HTMLSelectElement>('torchInterface')?.value || '';
      const c = pick('torchSeconds', 5);
      return iface ? { interface: iface, seconds: c.n, continuous: c.continuous } : null;
    },
  },
  {
    key: 'btest', page: 'tools-btest', write: true, form: 'btestForm', run: 'btestRun', status: 'btestStatus',
    summary: 'btestSummary', rows: 'btestRows', cols: 2, badge: '', label: 'Test',
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
  {
    key: 'sniffer', page: 'tools-sniffer', write: true, form: 'snifferForm', run: 'snifferRun',
    status: 'snifferStatus', summary: 'snifferSummary', rows: 'snifferRows', cols: 8,
    badge: 'snifferBadge', label: 'Start',
    // FIVE FILTERS OUT OF ROUTEROS'S TWENTY-FIVE. Which five, and why the rest
    // are left out, is internal/diag/sniffer.go's - the server validates them
    // and is the only place that can say what the router will accept.
    request: () => ({
      interface: el<HTMLSelectElement>('snifferInterface')?.value || '',
      ipProtocol: el<HTMLSelectElement>('snifferProtocol')?.value || '',
      port: (el<HTMLInputElement>('snifferPort')?.value || '').trim(),
      address: (el<HTMLInputElement>('snifferAddress')?.value || '').trim(),
      direction: el<HTMLSelectElement>('snifferDirection')?.value || 'any',
    }),
  },
];

// ── THE CAPTURE TABLE SORTS, AND STARTS NEWEST FIRST ────────────────────────
//
// The server hands the packets over newest first, which is the order somebody
// watching a capture wants; `num` descending is that same order stated as a
// sort, so the first click on the column reverses it rather than appearing to
// do nothing. It is not an ORDERED table in the Queues sense - no rule here
// decides anything by being first - so it sorts like every other table.
const SNIFF_COLS: SortCol[] = [
  { key: 'num', label: '#' },
  { key: 'time', label: 'Time' },
  { key: 'interface', label: 'Interface' },
  { key: 'direction', label: 'Dir' },
  { key: 'source', label: 'Source' },
  { key: 'dest', label: 'Destination' },
  { key: 'protocol', label: 'Protocol' },
  { key: 'size', label: 'Size' },
];
const sniffSort: SortState = { col: 'num', dir: 'desc' };
let sniffRows: SnifferResult | null = null;
// clearing latches the Clear button while the router is being asked, so a second
// press cannot send a second set of three commands into the write queue.
let clearing = false;

const bps = (v: number): string => fmtMbps(v / 1e6);

function ms(v: number | null | undefined): string {
  return v == null ? '-' : (v < 1 ? v.toFixed(3) : v.toFixed(1)) + ' ms';
}

function notRun(t: Tool): string {
  return '<tr><td colspan="' + t.cols + '" class="empty-state">Not run yet</td></tr>';
}

let traceMap: TraceMap | null = null;

/** The count pill beside a page's title: blue when it counts something, as
 *  every generated page's is (web/src/pages/area.ts). */
function setBadge(t: Tool, n: number): void {
  if (!t.badge) return;
  const badge = el(t.badge);
  if (!badge) return;
  badge.textContent = String(n);
  badge.className = 'card-badge' + (n > 0 ? ' active-blue' : '');
}

function clearResult(t: Tool): void {
  const summary = el(t.summary);
  if (summary) summary.textContent = '';
  const rows = el(t.rows);
  if (rows) rows.innerHTML = notRun(t);
  setBadge(t, 0);
  if (t.key === 'ping') renderPingCards(null);
  if (t.key === 'btest') renderBtestCards(null);
  if (t.key === 'traceroute') traceMap?.clear();
  if (t.key === 'sniffer') {
    sniffRows = null;
    renderSnifferCards(null);
  }
}

// ── A LONG RUN SCROLLS INSIDE ITS TABLE ─────────────────────────────────────
//
// A hundred pings, or an hour of them, would push the page past the bottom of
// the screen (reported by the operator). The table's scroller is held to the
// space left below it, and a ping table that is scrolled to its end follows the
// newest reply; scrolled up, it stays where the operator put it.
function fitHeight(wrap: HTMLElement): void {
  if (typeof wrap.getBoundingClientRect !== 'function' || typeof window === 'undefined' || !window.innerHeight) return;
  const top = wrap.getBoundingClientRect().top;
  wrap.style.maxHeight = Math.max(220, Math.floor(window.innerHeight - top - 24)) + 'px';
}

function drawBounded(scrollId: string, follow: boolean, draw: () => void): void {
  const wrap = el(scrollId);
  const atEnd = !wrap || wrap.scrollTop + wrap.clientHeight >= wrap.scrollHeight - 8;
  draw();
  if (!wrap) return;
  fitHeight(wrap);
  if (follow && atEnd) wrap.scrollTop = wrap.scrollHeight;
}

function renderPing(r: PingResult): void {
  const summary = el('pingSummary');
  if (summary) {
    summary.textContent = r.sent + ' sent, ' + r.received + ' received, ' + r.lossPct + '% loss' +
      (r.avgMs == null ? '' : ' · min ' + ms(r.minMs) + ', avg ' + ms(r.avgMs) + ', max ' + ms(r.maxMs)) +
      // A continuous run carries its latest replies; the totals are the run's.
      (r.replies.length < r.sent ? ' · showing the latest ' + r.replies.length : '');
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
      '<td>' + (lost ? '-' : ms(p.rttMs)) + '</td>' +
      '<td>' + (lost ? '-' : p.ttl) + '</td>' +
      '<td>' + (lost ? '-' : p.size) + '</td>' +
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
    '<td>' + (h.address ? esc(h.address) : '-') + '</td>' +
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
    summary.textContent = (r.continuous ? 'Watching ' + r.interface + ' · the last ' + r.reports + ' s'
      : 'Watched ' + r.interface + ' for ' + r.seconds + ' s') + ' · average rx ' + bps(r.totalRxBps) +
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
    '<td>' + protoPill(f.protocol) + '</td>' +
    '<td>' + end(f.srcAddress, f.srcPort) + '</td>' +
    '<td>' + end(f.dstAddress, f.dstPort) + '</td>' +
    '<td style="color:var(--accent-rx)">' + bps(f.rxBps) + '</td>' +
    '<td style="color:var(--accent-tx)">' + bps(f.txBps) + '</td>' +
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

/** The file name out of a Content-Disposition, or '' when there is none.
 *  The server chose it from the capture's own magic bytes, so it is the one
 *  thing that knows whether this is a .pcapng or a .pcap. */
export function filenameOf(header: string | null): string {
  const m = /filename="([^"]+)"/.exec(header || '');
  return m ? m[1]! : '';
}

/** The response body, counted as it arrives.
 *
 *  A SHORT READ IS A FAILED FETCH, NOT A SHORTER FILE. The server declares
 *  Content-Length before the first chunk and stops writing the moment the
 *  router's own length check fails, so a truncated capture rejects here and
 *  never reaches the download - which is the whole reason the server may stream
 *  a file it has not finished verifying (internal/backups/read.go).
 *
 *  `res.body` is absent in a few places - some older browsers, and the test
 *  shim - so the whole-blob read stays as the fallback. It shows no progress,
 *  which is exactly what the page did before, rather than nothing at all. A
 *  second guard on `getReader` was written and then removed: a Response that
 *  has a body always has one, and a mutation sweep found the branch unreachable
 *  rather than untested. */
export function readWithProgress(res: Response, total: number,
  onProgress: (got: number, total: number) => void): Promise<Blob> {
  const body = res.body;
  if (!body) return res.blob();
  const reader = body.getReader();
  const parts: BlobPart[] = [];
  let got = 0;
  onProgress(0, total);
  const pump = (): Promise<Blob> => reader.read().then(({ done, value }) => {
    if (done) return new Blob(parts);
    if (value) {
      parts.push(value as unknown as BlobPart);
      got += value.length;
      onProgress(got, total);
    }
    return pump();
  });
  return pump();
}

/** The capture's packet table, in the current sort. */
function renderSniffer(r: SnifferResult): void {
  const summary = el('snifferSummary');
  if (summary) {
    // THE TABLE IS USUALLY SHORTER THAN THE CAPTURE, for two reasons at once -
    // the frame carries the latest few hundred rows, and the router's own memory
    // buffer has already dropped the oldest - so it says which rows these are
    // rather than claiming to know why the rest are missing.
    summary.textContent = (r.running ? 'Capturing' : 'Capture stopped') + ' \u00b7 ' +
      r.totalPackets + (r.totalPackets === 1 ? ' packet' : ' packets') + ' seen' +
      (r.packets.length < r.totalPackets ? ' \u00b7 showing the latest ' + r.packets.length : '');
  }
  const rows = el('snifferRows');
  if (!rows) return;
  if (!r.packets.length) {
    rows.innerHTML = '<tr><td colspan="8" class="empty-state">No packets yet</td></tr>';
    return;
  }
  // rx blue and tx green, the app's fixed directions, as a pill rather than a
  // bare word - a direction is a state, which is what a pill is for.
  const dirPill = (d: string): string => '<span class="sniff-dir sniff-dir-' +
    (d === 'tx' ? 'tx' : 'rx') + '">' + esc(d) + '</span>';
  rows.innerHTML = sortRows(r.packets, sniffSort.col, sniffSort.dir).map((p) =>
    '<tr>' +
    '<td>' + p.num + '</td>' +
    '<td>' + p.time.toFixed(3) + '</td>' +
    '<td>' + esc(p.interface) + '</td>' +
    '<td>' + dirPill(p.direction) + '</td>' +
    '<td>' + esc(p.source) + '</td>' +
    '<td>' + esc(p.dest) + '</td>' +
    '<td>' + protoPill(p.protocol) + (p.tcpFlags ? ' <small>' + esc(p.tcpFlags) + '</small>' : '') + '</td>' +
    '<td>' + p.size + '</td>' +
    '</tr>').join('');
}

export function initToolsPage(socket: Socket, isVisible: (page: string) => boolean,
  activeId: () => string): void {
  let pending: Tool | null = null;
  // WHETHER THIS VIEWER MAY RUN EACH WRITE TOOL, from `tools:caps`. ONE FLAG
  // PER TOOL, because torch, the bandwidth test and the sniffer are separate
  // pages and so separate grants. False until the frame arrives, so a Run button
  // that would only be refused is never offered.
  //
  // `blocked` is the SENTENCE for a button that is off, and it is separate
  // because the sniffer has two ways to be off that need different words: the
  // viewer may not write this page, or the DEVICE refuses the tool - which is
  // nobody's permission and cannot be granted from here. One flag holding both
  // would have to pick one of them to be wrong about.
  const mayRun: Record<string, boolean> = { torch: false, btest: false, sniffer: false };
  const blocked: Record<string, string> = {};

  // THE RUNNING TOOL'S BUTTON IS ITS STOP; every other Run button is disabled,
  // as is a write tool's for a viewer who may not write.
  function setRunning(t: Tool | null, note: string): void {
    pending = t;
    // Clear reads `pending`, so its state is refreshed at the end of this
    // function - after every button above has been settled.
    for (const x of TOOLS) {
      const btn = el<HTMLButtonElement>(x.run);
      if (!btn) continue;
      const running = x === t;
      btn.disabled = t !== null ? !running : (x.write && !mayRun[x.key]);
      btn.textContent = running ? 'Stop' : x.label;
      btn.classList.toggle('sbtn-danger', running);
      btn.classList.toggle('sbtn-primary', !running);
    }
    if (t) {
      const status = el(t.status);
      if (status) status.textContent = note;
    }
    setExportEnabled();
  }

  // The export asks the router to write the capture to a file, so it needs the
  // same write access the capture did - and there is no point offering it until
  // there is something captured to write.
  function setExportEnabled(): void {
    const off = !mayRun.sniffer || !sniffRows || sniffRows.totalPackets === 0;
    const btn = el<HTMLButtonElement>('snifferExport');
    if (btn) btn.disabled = off;
    // ── CLEAR IS NOT GATED ON HAVING SOMETHING ON SCREEN ─────────────────────
    //
    // The capture lives in the ROUTER's memory, and this page may be showing
    // nothing at all while the device still holds one - after a reload, or
    // after this app restarted mid-run. Disabling Clear until a capture is on
    // screen would hide the button in exactly the state it is needed. It is
    // gated on write access and on nothing else.
    //
    // IT IS OFF WHILE ANY RUN IS PENDING, though. A tool holds the router one
    // at a time, so a clear sent mid-capture comes back "Another tool is still
    // running" - true, and baffling when the other tool is this page's own
    // capture. While one is running the button to press is Stop.
    const clr = el<HTMLButtonElement>('snifferClear');
    if (clr) clr.disabled = !mayRun.sniffer || clearing || pending !== null;
  }

  function stop(): void {
    if (!pending) return;
    const btn = el<HTMLButtonElement>(pending.run);
    if (btn) btn.disabled = true;
    const status = el(pending.status);
    if (status) status.textContent = 'Stopping…';
    socket.emit('tools:stop', {});
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
    if (!d.code || d.code === 'stopped') {
      draw();
      if (d.code && status) status.textContent = 'Stopped.';
      return;
    }
    if (status) status.textContent = REFUSED[d.code] || d.message || 'The tool did not run.';
  }

  for (const t of TOOLS) {
    el(t.form)?.addEventListener('submit', (e) => {
      e.preventDefault();
      if (pending === t) { stop(); return; }
      if (pending) return;
      const req = t.request();
      if (!req) return;
      // THE LAST RESULT GOES AT ONCE, so a run that fails does not leave the
      // previous address's result standing under its error.
      clearResult(t);
      setExportEnabled();
      setRunning(t, 'Running…');
      socket.emit('tools:' + t.key, req);
    });
  }
  // ── THE CAPTURE'S SORTABLE HEADER AND ITS EXPORT BUTTON ──────────────────
  //
  // The header is drawn once and redrawn by the helper on every click; the sort
  // is applied when the table is rendered, so re-sorting does not wait for the
  // next poll.
  renderSortHeader('snifferHead', SNIFF_COLS, sniffSort, () => {
    if (sniffRows) renderSniffer(sniffRows);
  });
  el('snifferClear')?.addEventListener('click', () => {
    if (clearing) return;
    clearing = true;
    setExportEnabled();
    const status = el('snifferExportStatus');
    if (status) status.textContent = 'Clearing the capture on the router…';
    socket.emit('tools:sniffer-clear', {});
  });
  socket.on('tools:sniffer-cleared', (d) => {
    clearing = false;
    const status = el('snifferExportStatus');
    if (d.code) {
      if (status) status.textContent = d.message || REFUSED[d.code] || 'The capture could not be cleared.';
      setExportEnabled();
      return;
    }
    // THE ROUTER IS EMPTY, SO THE PAGE IS TOO. Drawing the last frame under a
    // "cleared" message would be a table of packets the device no longer has.
    clearResult(sniffer);
    setExportEnabled();
    if (status) status.textContent = 'Cleared.';
    const runStatus = el('snifferStatus');
    if (runStatus) runStatus.textContent = '';
  });
  el('snifferExport')?.addEventListener('click', () => {
    const rid = activeId();
    if (!rid) return;
    const btn = el<HTMLButtonElement>('snifferExport');
    // ITS OWN LINE, NOT THE FORM'S: a running capture writes "Running…" to
    // snifferStatus on every frame, which overwrote the byte count about twice
    // a second while the bar carried on moving.
    const status = el('snifferExportStatus');
    const track = el('snifferExportProgress');
    const fill = el('snifferExportBar');
    // ── THE BAR HAS TWO PHASES, AND THEY ARE DIFFERENT CLAIMS ───────────────
    //
    // `is-wait` is before the response headers: the router is still writing the
    // capture to a file and nothing knows how big it will be. `is-on` alone is
    // the read, where Content-Length is known and the width is real. A bar that
    // sat at 0% through the first phase would be indistinguishable from one
    // that had stalled.
    const showProgress = (got: number, total: number): void => {
      track?.classList.add('is-on');
      track?.classList.toggle('is-wait', total <= 0);
      if (fill && total > 0) fill.style.width = Math.min(100, Math.round((got / total) * 100)) + '%';
      if (status) {
        status.textContent = total > 0
          ? 'Downloading the capture… ' + fmtBytes(got) + ' of ' + fmtBytes(total)
          : 'Preparing the capture…';
      }
    };
    const clearProgress = (): void => {
      track?.classList.remove('is-on', 'is-wait');
      if (fill) fill.style.width = '0%';
      if (btn) btn.disabled = false;
    };
    if (btn) btn.disabled = true;
    showProgress(0, 0);
    // FETCHED RATHER THAN LINKED, unlike the Backups page's two download links.
    // A plain <a href> cannot show a refusal in the page's own status line, and
    // cannot count what has arrived. The file's NAME comes from the server,
    // which read the magic bytes - RouterOS 7.20 and later write PCAPNG
    // whatever the file is called.
    void fetch('/api/tools/sniffer/pcap?routerId=' + encodeURIComponent(rid),
      { credentials: 'same-origin' })
      .then((res) => {
        if (!res.ok) {
          return res.json().catch(() => ({ error: '' }))
            .then((d: { error?: string }) => { throw new Error(d.error || 'The capture could not be exported.'); });
        }
        const name = filenameOf(res.headers.get('content-disposition')) || 'capture.pcapng';
        const total = Number(res.headers.get('content-length')) || 0;
        return readWithProgress(res, total, showProgress).then((blob) => {
          const a = document.createElement('a');
          a.href = URL.createObjectURL(blob);
          a.download = name;
          a.click();
          URL.revokeObjectURL(a.href);
          clearProgress();
          if (status) status.textContent = 'Saved ' + name + '.';
        });
      })
      .catch((e: Error) => { clearProgress(); if (status) status.textContent = e.message; });
  });

  const svg = el('traceMap') as unknown as SVGSVGElement | null;
  const hops = el('traceHopList');
  const wrap = el('traceMapWrap');
  if (svg && hops && wrap) {
    traceMap = createTraceMap({ svg, wrap, list: hops, empty: el('traceMapEmpty'), tip: el('traceTip'),
      zoomIn: el('traceZoomIn'), zoomOut: el('traceZoomOut'), fit: el('traceZoomFit') });
  }
  if (typeof window !== 'undefined') {
    window.addEventListener('resize', () => {
      for (const id of ['pingScroll', 'torchScroll', 'snifferScroll']) {
        const wrap = el(id);
        if (wrap) fitHeight(wrap);
      }
    });
  }
  const [ping, trace, torch, btest, sniffer] = TOOLS as [Tool, Tool, Tool, Tool, Tool];
  socket.on('tools:ping', (d) => settle(ping, d, () => {
    if (!d.result) return;
    const r = d.result;
    drawBounded('pingScroll', true, () => renderPing(r));
    renderPingCards(r);
    setBadge(ping, r.replies.length);
  }));
  socket.on('tools:traceroute', (d) => settle(trace, d, () => {
    if (!d.result) return;
    renderTraceroute(d.result);
    traceMap?.update(d.result);
    setBadge(trace, d.result.hops.length);
  }));
  socket.on('tools:torch', (d) => settle(torch, d, () => {
    const r = d.result;
    if (!r) return;
    drawBounded('torchScroll', false, () => renderTorch(r));
    setBadge(torch, r.flows.length);
  }));
  socket.on('tools:btest', (d) => settle(btest, d, () => {
    if (!d.result) return;
    renderBtest(d.result);
    renderBtestCards(d.result);
  }));
  socket.on('tools:sniffer', (d) => settle(sniffer, d, () => {
    const r = d.result;
    if (!r) return;
    sniffRows = r;
    drawBounded('snifferScroll', false, () => renderSniffer(r));
    renderSnifferCards(r);
    setBadge(sniffer, r.packets.length);
    setExportEnabled();
  }));

  socket.on('tools:caps', (d) => {
    mayRun.torch = d.mayTorch;
    mayRun.btest = d.mayBtest;
    // TWO CONDITIONS FOR THE SNIFFER, and the device's is the one that is not a
    // grant: a device whose device-mode sniffer flag is off refuses the tool
    // however the permissions read, and only the reset button changes that.
    mayRun.sniffer = d.maySniff && d.snifferAllowed;
    blocked.torch = d.mayTorch ? '' : NEEDS_WRITE;
    blocked.btest = d.mayBtest ? '' : NEEDS_WRITE;
    blocked.sniffer = !d.snifferAllowed ? DEVICE_REFUSES : (d.maySniff ? '' : NEEDS_WRITE);
    const sel = el<HTMLSelectElement>('torchInterface');
    if (sel) sel.innerHTML = d.interfaces.map((n) => '<option>' + esc(n) + '</option>').join('');
    // The sniffer's picker keeps its "All interfaces" entry, which is what an
    // empty `filter-interface` means to RouterOS and is the useful default for
    // a capture.
    const sniffSel = el<HTMLSelectElement>('snifferInterface');
    if (sniffSel) {
      sniffSel.innerHTML = '<option value="">All interfaces</option>' +
        d.interfaces.map((n) => '<option>' + esc(n) + '</option>').join('');
    }
    setExportEnabled();
    setRunning(pending, '');
    if (!pending) {
      for (const t of TOOLS) {
        const status = el(t.status);
          if (t.write && status) status.textContent = mayRun[t.key] ? '' : (blocked[t.key] || NEEDS_WRITE);
      }
    }
  });
  // ASKED FOR WHEN ANY OF THE FIVE PAGES OPENS, and again on a router switch
  // while one is open: the permissions, the interfaces and the device's own
  // sniffer flag are all per router, and one frame answers all five.
  const askCaps = (): void => socket.emit('tools:caps', {});
  const isToolPage = (page: string): boolean => TOOLS.some((t) => t.page === page);
  document.addEventListener('mikrodash:pagechange', (e) => {
    const to = (e as CustomEvent).detail as string;
    // LEAVING THE RUNNING TOOL'S OWN PAGE STOPS THE RUN (decided 2026-09-19):
    // nobody is watching it, and a continuous one would hold its channel for
    // the hour. Moving from Ping to Torch leaves the ping page as surely as
    // moving to Settings does, which is why this compares the PENDING tool's
    // page rather than asking whether the destination is a tool page at all.
    if (pending && pending.page !== to) stop();
    if (isToolPage(to)) askCaps();
  });

  socket.on('router:switched', () => {
    if (pending) {
      const status = el(pending.status);
      if (status) status.textContent = '';
    }
    mayRun.torch = false;
    mayRun.btest = false;
    mayRun.sniffer = false;
    setRunning(null, '');
    for (const t of TOOLS) clearResult(t);
    setExportEnabled();
    if (TOOLS.some((t) => isVisible(t.page))) askCaps();
  });
}
