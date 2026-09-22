// The Dashboard's API Diagnostics card (dc-card-diagnostics): what each of the
// three collector layers is doing for the router you have selected.
//
// ── IT NEVER WORKED IN THIS PORT ────────────────────────────────────────────
//
// The renderer, the listener and the room all existed from the start and nothing
// ever emitted `diagnostics:update`. The card rendered empty, which looks exactly
// like a card with nothing to say — so it read as "quiet" rather than "broken"
// for the whole life of the port. `internal/server/diagnostics.go` feeds it now.
//
// ── IT COSTS THE ROUTER NOTHING ─────────────────────────────────────────────
//
// Every number comes from counters the server already keeps. Adding the card
// issues no RouterOS command, which is the point: an instrument that changes
// what it measures is worthless.
//
// ── THE SHAPE IS THE ARCHITECTURE'S, NOT THE CODE'S ─────────────────────────
//
// Three sections in the order data flows, because that is the question a reader
// has when they add this card:
//
//   acquisition  what is being asked of the router, and how
//   derivation   what those rows are being turned into
//   views        who is listening, which is what decides the other two
//
// Acquisition says who asked (the command rate by source), which menus the last
// minute's commands were for (a sortable table), and what the collectors
// subscribe to. The first two cover every path that reaches the router, the
// on-demand features included; see internal/session/sources.go.
//
// ── `esc`, NOT THE `dcEsc` THE OTHER DASHBOARD CARDS USE ───────────────────
//
// Every other card on this page escapes with `dcEsc`, and does so for a reason
// that does not apply here: `dcEsc` reproduces the live app's helper exactly,
// quirks included, because those cards had live markup to match. THIS CARD NEVER
// RENDERED, so there is nothing to reproduce and no parity to lose — and two of
// `dcEsc`'s documented quirks are actively wrong for it. It leaves `"` alone, so
// it must not reach a `title=` attribute, which this card uses on every row; and
// it renders a falsy value as the EMPTY STRING, on a card whose whole subject is
// counts.
//
// The tell that made the choice concrete: `dcEsc` escapes by round-tripping
// through a text node, and the test DOM shim keeps `textContent` and `innerHTML`
// in separate stores — so under `web/test/` it returns `''` and the card renders
// nothing. `esc` is a string replace and needs no DOM.

import { el, esc, renderSortHeader, sortRows } from '../dom';
import type { SortState } from '../dom';
import type { Diagnostics, MenuRate, SourceLoad } from '../gen/payloads';

/** A number, or an em dash when the server said nothing rather than zero. */
function num(v: number | undefined): string {
  return v != null ? String(v) : '&mdash;';
}

/**
 * One labelled row.
 *
 * `active` styles the value as a live figure rather than as a resting zero,
 * which is the distinction the whole card draws: nothing running is a SUCCESS
 * here, not an absence, so zero is never painted as a problem.
 */
function row(label: string, value: string, active: boolean, title?: string): string {
  const t = title ? ' title="' + esc(title) + '"' : '';
  return '<div class="diag-row"' + t + '><span class="diag-name">' + esc(label) +
    '</span><span class="diag-count ' + (active ? 'diag-count-active' : 'diag-count-zero') +
    '">' + value + '</span></div>';
}

/**
 * A source as a coloured pill, by KIND rather than by name: the collectors'
 * steady reads are blue, a feature somebody started (a scan, an app, a tool run,
 * the AI agent, a page) is amber, and an unnamed or background source is grey.
 * A new feature therefore gets a colour without this file learning its name.
 */
function sourcePill(src: string): string {
  const cls = src === 'Collectors' ? 'hs-info'
    : src === 'Other' || src === 'Devices' ? 'hs-never' : 'hs-warn';
  return '<span class="vpn-hs-badge ' + cls + '">' + esc(src) + '</span>';
}

/** How a subscribed menu is delivered, as a pill: pushed green, polled blue. */
function deliveryPill(streamed: boolean): string {
  return streamed
    ? '<span class="vpn-hs-badge hs-ok">pushed</span>'
    : '<span class="vpn-hs-badge hs-info">polled</span>';
}

/**
 * WHO ASKED: the command rate split by source, one bar each against the total.
 * The bars share one scale, the headline, so they read as parts of it.
 */
function sourceRows(sources: SourceLoad[], total: number): string {
  return sources.map((s) => {
    const pct = total > 0 ? Math.max(2, Math.round((s.perMin / total) * 100)) : 0;
    return '<div class="diag-src">' + sourcePill(s.source) +
      '<span class="diag-bar"><span class="diag-bar-fill" style="width:' + pct + '%"></span></span>' +
      '<span class="diag-count diag-count-active">' + s.perMin + '</span></div>';
  }).join('');
}

// The busiest-menus table's sort, kept across repaints so a two-second tick
// does not undo the operator's click.
const busySort: SortState = { col: 'perMin', dir: 'desc' };
let lastBusy: MenuRate[] = [];

function busiestBody(rows: MenuRate[]): string {
  return sortRows(rows, busySort.col, busySort.dir).map((r) =>
    '<tr><td class="diag-menu" title="' + esc(r.menu) + '">' + esc(r.menu) + '</td>' +
    '<td>' + sourcePill(r.source) + '</td>' +
    '<td class="diag-num">' + r.perMin + '</td></tr>').join('');
}

function paintBusiest(): void {
  if (!el('dc-diagBusyHead')) return;
  renderSortHeader('dc-diagBusyHead', [
    { key: 'menu', label: 'Menu' },
    { key: 'source', label: 'Source' },
    { key: 'perMin', label: '/ min', cls: 'diag-num' },
  ], busySort, () => {
    const body = el('dc-diagBusyBody');
    if (body) body.innerHTML = busiestBody(lastBusy);
    paintBusiest();
  });
}

function heading(text: string, sub: string): string {
  return '<div class="diag-layer"><span class="diag-layer-name">' + esc(text) +
    '</span><span class="diag-layer-sub">' + esc(sub) + '</span></div>';
}

export function renderDiagnosticsCard(data: Diagnostics): void {
  const a = data.acquisition || {};
  const d = data.derivation || {};
  const v = data.views || {};

  // THE HEADLINE IS THE COMMAND RATE, because it is the one number that says
  // what this app costs the device. It used to be "active streams", which is a
  // level rather than a load and reads as zero on a perfectly busy router.
  const totalEl = el('dc-diagTotal');
  if (totalEl) totalEl.textContent = a.commandsPerMin != null ? String(a.commandsPerMin) : '—';

  const listEl = el('dc-diagList');
  if (!listEl) return;

  let html = '';

  html += heading('Acquisition', 'what the router is asked');
  html += row('open channels', num(a.channels), (a.channels || 0) > 0,
    'Push channels held open. A channel costs no commands, which is why it is counted separately.');
  html += row('in flight', num(a.inFlight) + ' / ' + num(a.cap), (a.inFlight || 0) > 0,
    'Commands holding a concurrency slot right now, against the per-router cap.');
  html += row('menus subscribed', num(a.menus), (a.menus || 0) > 0,
    'RouterOS menus at least one collector wants.');
  html += row('· pushed by router', num(a.streamed), (a.streamed || 0) > 0,
    'Kept current by an open channel instead of being polled.');
  html += row('· polled', num(a.polled), (a.polled || 0) > 0,
    'Read on a schedule.');

  // WHO ASKED. The headline counts every command this process sends the router:
  // the collectors' reads, and every feature that reaches it on demand (the
  // Security Scan, the Apps tab, the Tools page, the AI agent, the pages'
  // reads and writes). Without this, a scan's thirty-odd reads moved the number
  // with nothing on the card to account for them.
  const sources = a.sources || [];
  if (sources.length) {
    html += heading('Who asked', 'commands / min by source');
    html += sourceRows(sources, a.commandsPerMin || 0);
  }

  // WHAT WAS SENT, busiest first: the menus the last minute's commands were
  // for. Unlike the subscriptions below, this includes every on-demand read.
  lastBusy = a.busiest || [];
  if (lastBusy.length) {
    html += heading('Busiest menus', 'last minute');
    html += '<table class="diag-table"><thead><tr id="dc-diagBusyHead"></tr></thead>' +
      '<tbody id="dc-diagBusyBody">' + busiestBody(lastBusy) + '</tbody></table>';
    if ((a.busiestMore || 0) > 0) {
      html += row('+ ' + a.busiestMore + ' more', '', false,
        'The list is capped so the card stays a card. The counts above are complete.');
    }
  }

  // THE SUBSCRIPTIONS. What the collectors want, and how each is delivered.
  const reads = a.reads || [];
  if (reads.length) {
    html += heading('Menus subscribed', 'what the collectors want');
    html += reads.map((m) => {
      const title = m.streamed
        ? 'The router pushes this table on an open channel; it is not re-asked.'
        : 'Read on a schedule, once per cadence, however many collectors want it.';
      return '<div class="diag-row" title="' + esc(title) + '"><span class="diag-name">' +
        esc(String(m.menu || '')) + '</span>' + deliveryPill(!!m.streamed) + '</div>';
    }).join('');
    // SAID, NOT SILENT. A truncated list that does not admit it is an instrument
    // reporting less than it measured.
    if ((a.more || 0) > 0) {
      html += row('+ ' + a.more + ' more', '', false,
        'The list is capped so the card stays a card. The counts above are complete.');
    }
  }

  html += heading('Derivation', 'what the rows become');
  html += row('payloads / min', num(d.payloadsPerMin), (d.payloadsPerMin || 0) > 0,
    'Payloads built from those rows and sent to any browser watching this router.');

  html += heading('Views', 'who is listening');
  html += row('collectors running', num(v.running) + ' / ' + num(v.gated), (v.running || 0) > 0,
    'Of the collectors demand can start and stop, how many something currently wants.');
  html += row('rooms occupied', num(v.rooms), (v.rooms || 0) > 0,
    'Pages and cards with a viewer in them. This is what decides which collectors run.');
  if ((v.dormant || 0) > 0) {
    html += row('dormant', num(v.dormant), false,
      'Backed off for reporting nothing. They wake when their page is opened.');
  }
  const holds = v.holds || [];
  if (holds.length) {
    html += row('kept alive by', holds.join(', '), true,
      'Reasons other than a viewer: alerting, history recording, the Devices page, ' +
      'or merely keeping the connection warm.');
  }

  listEl.innerHTML = html;
  if (lastBusy.length) paintBusiest();
}
