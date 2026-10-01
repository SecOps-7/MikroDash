// The connectivity strip: a device's reachability over a window, drawn from the
// spans `GET /api/devices/overview` returns.
//
// ── THE SERVER DECIDES, THIS ONLY DRAWS ─────────────────────────────────────
//
// `history.Spans` already tiles the window exactly - sorted, no gap, no overlap,
// adjacent states merged - and computes the time-weighted uptime. Nothing here
// re-derives either; a second copy of those rules in the browser is how the two
// would disagree.
//
// ── PERCENTAGES, AND A FLOOR FOR AN OUTAGE ─────────────────────────────────
//
// Each segment is positioned and sized as a percentage of the window, so the
// strip needs no pixel arithmetic and fits any card width. A DOWN segment sits on
// a layer above and carries a CSS `min-width` (`.dv-seg.dv-down`), so a 52-second
// outage on a 24-hour strip - a fifteenth of a pixel - is still a visible red
// mark instead of vanishing. That is the one place the strip is deliberately not
// to scale, and the tooltip carries the true time.

import { esc } from '../dom';
import { fmtTime, fmtTs } from '../timefmt';

/** One span, as `history.Span` sends it. Typed by hand: it is a REST response. */
export interface StripSpan {
  from: number;
  to: number;
  state: 'up' | 'down' | 'unmonitored';
}

/** `db.BackupBrief`. */
export interface BackupBrief {
  lastAt: number;
  lastOutcome: string;
  lastSuccessAt: number | null;
}

/** One row of `GET /api/devices/overview`. */
export interface DeviceOverview {
  routerId: string;
  spans: StripSpan[];
  uptimePct: number | null;
  monitoredMs: number;
  backup: BackupBrief | null;
}

const STATE_WORD: Record<StripSpan['state'], string> = {
  up: 'Up', down: 'Down', unmonitored: 'Not monitored',
};

/** A duration in the largest two units that matter: 52s, 7m, 2h 15m, 3d 4h. */
export function fmtDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return s + 's';
  const m = Math.round(s / 60);
  if (m < 60) return m + 'm';
  const h = Math.floor(m / 60);
  if (h < 24) return h + 'h' + (m % 60 ? ' ' + (m % 60) + 'm' : '');
  const d = Math.floor(h / 24);
  return d + 'd' + (h % 24 ? ' ' + (h % 24) + 'h' : '');
}

/** A moment, as precise as the window needs: a time for a day, a date beyond. */
function fmtMoment(ms: number, longWindow: boolean): string {
  return longWindow ? fmtTs(ms, false) : fmtTime(ms, false);
}

/** The uptime figure drawn beside a strip. "No data" when nothing was watched. */
export function uptimeLabel(pct: number | null): string {
  if (pct == null) return 'No data';
  // 99.98 reads as 100.0 at one decimal and lies; the server already rounds to
  // one decimal, so this only drops a trailing ".0" on a clean number.
  return (Number.isInteger(pct) ? String(pct) : pct.toFixed(1)) + '%';
}

/** Every outage in the window, oldest first - the modal lists these. */
export function outages(spans: readonly StripSpan[]): StripSpan[] {
  return spans.filter((s) => s.state === 'down');
}

/** What a screen reader hears instead of the colours. */
export function stripSummary(spans: readonly StripSpan[], uptimePct: number | null, windowWord: string): string {
  if (uptimePct == null) return 'Not monitored over the last ' + windowWord;
  const n = outages(spans).length;
  return uptimeLabel(uptimePct) + ' up over the last ' + windowWord
    + (n ? ', ' + n + (n === 1 ? ' outage' : ' outages') : ', no outages');
}

/**
 * The strip's markup.
 *
 * An EMPTY span list draws a single grey "Not monitored" bar rather than
 * nothing: an absent strip reads as a broken card, and "we have no data for this
 * window" is a fact worth showing.
 */
export function stripHtml(
  spans: readonly StripSpan[], from: number, to: number,
  opts: { uptimePct?: number | null; windowWord?: string } = {},
): string {
  const total = to - from;
  const longWindow = total > 36 * 3600 * 1000;
  const word = opts.windowWord || '24 hours';
  const label = esc(stripSummary(spans, opts.uptimePct ?? null, word));
  if (!spans.length || total <= 0) {
    return '<div class="dv-strip" role="img" aria-label="' + label + '">'
      + '<i class="dv-seg dv-unmonitored" style="left:0;width:100%" title="Not monitored"></i></div>';
  }
  let html = '<div class="dv-strip" role="img" aria-label="' + label + '">';
  for (const s of spans) {
    const left = ((s.from - from) / total) * 100;
    const width = ((s.to - s.from) / total) * 100;
    const title = STATE_WORD[s.state] + ' ' + fmtMoment(s.from, longWindow) + ' to '
      + fmtMoment(s.to, longWindow) + ' (' + fmtDuration(s.to - s.from) + ')';
    html += '<i class="dv-seg dv-' + s.state + '" style="left:' + left.toFixed(4)
      + '%;width:' + width.toFixed(4) + '%" title="' + esc(title) + '"></i>';
  }
  return html + '</div>';
}
