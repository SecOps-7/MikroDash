// The Dashboard's API Diagnostics card (dc-card-diagnostics): how many streams
// each collector is holding open, and whether geo lookups work.
//
// ── THE GEO ROW EXISTS BECAUSE ABSENCE LOOKS LIKE QUIET ─────────────────────
//
// A failed `geoip-lite` load makes the world map and every country breakdown
// render empty — which is exactly what a quiet network looks like. The row says
// so instead, and only appears when geo is explicitly unavailable: `data.geo &&
// !data.geo.available`, so a payload with no geo key at all shows nothing rather
// than claiming a failure it has not been told about.
//
// ── `!= null` ON THE TOTAL, WHICH IS NOT THE CARD'S USUAL RULE ──────────────
//
// The Routes card treats `null` as a value and prints it; this one treats null
// and undefined alike and shows an em dash. Two cards in the same IIFE, two
// readings of "no value" — reproduced rather than harmonised, because they are
// what the live cards do.
//
// ── THE title ATTRIBUTE USES `esc`, NOT `dcEsc` ─────────────────────────────
//
// `dcEsc` escapes by round-tripping through a text node, so it leaves quotes
// alone — correct in text position and wrong inside `title="…"`, where a value
// containing a double quote closes the attribute early. Reported as ToDo #16,
// fixed upstream the same day, and this is the port following: the pinned case
// carrying a quoted reason turned red the moment it landed.
//
// The collector NAME below stays on `dcEsc` — it is text position, which is what
// that helper is for.

import { el } from '../dom';
import { esc } from '../dom';
import { dcEsc } from './dashboard-cards-util';

export interface DiagCollector {
  name?: string;
  streams?: number;
}
export interface DiagnosticsPayload {
  total?: number | null;
  collectors?: DiagCollector[];
  geo?: { available?: boolean; reason?: string };
}

export function renderDiagnosticsCard(data: DiagnosticsPayload): void {
  const totalEl = el('dc-diagTotal');
  if (totalEl) totalEl.textContent = data.total != null ? String(data.total) : '—';

  const listEl = el('dc-diagList');
  if (!listEl) return;
  const cols = data.collectors || [];

  let geoRow = '';
  if (data.geo && !data.geo.available) {
    geoRow = '<div class="diag-row" title="' +
      esc(data.geo.reason || 'geoip-lite failed to load') + '">' +
      '<span class="diag-name">geo lookups</span>' +
      '<span class="diag-count diag-count-zero">unavailable</span></div>';
  }

  listEl.innerHTML = geoRow + cols.map((c) => {
    // `> 0`, so a collector holding no streams is styled as zero rather than as
    // active — which is the whole point of the card.
    const cls = (c.streams as number) > 0 ? 'diag-count-active' : 'diag-count-zero';
    return '<div class="diag-row"><span class="diag-name">' + dcEsc(c.name) + '</span>' +
      '<span class="diag-count ' + cls + '">' + c.streams + '</span></div>';
  }).join('');
}
