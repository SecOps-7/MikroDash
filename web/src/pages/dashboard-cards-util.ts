// Shared helpers for the Dashboard's fourteen EXTRA cards.
//
// These sit under a ~570-line section of the live `app.js` that this port had
// not touched — the cards hidden by default, which is why nothing missed them
// until an id sweep counted them.
//
// ── THEY LOOK LIKE HELPERS THIS PORT ALREADY HAS, AND THEY ARE NOT ──────────
//
// `dcEsc` is not `esc`, `dcFlag` is not `iso2Flag`, and `dcDrawGauge` is not the
// DHCP page's `renderDhcpGauge`. Each differs from its near-twin in a way that is
// visible on screen, and reusing the twin would change what a card renders. The
// differences are recorded on each function rather than resolved, because the
// live app really does carry both and a port that unified them would be showing
// something the app it replaces does not.

import { el } from '../dom';

/**
 * Escape by round-tripping through a text node, NOT by substitution.
 *
 * `esc()` in `dom.ts` replaces five characters. This sets `textContent` and reads
 * `innerHTML` back, which is what the browser's own escaper does — and the two
 * disagree: the browser leaves `'` and `"` alone in text position, so
 * `O'Brien` survives as `O'Brien` here and becomes `O&#039;Brien` there.
 *
 * Reproduced rather than unified. These values land in text nodes, and the live
 * card shows the apostrophe.
 */
// ── DO NOT PUT THIS IN AN ATTRIBUTE ─────────────────────────────────────────
//
// It escapes `&`, `<` and `>` and leaves `"` and `'` alone, because that is what
// a text node does. Correct for text position and WRONG inside `title="…"` — a
// value containing a double quote closes the attribute early.
//
// The live app has exactly that defect in two `title` attributes on the Physical
// Ports card, reported as ToDo #16 on 2026-08-24, and it is why that card is not
// ported yet. Use `esc()` from `dom.ts` for anything that lands in an attribute.
export function dcEsc(s: unknown): string {
  const d = document.createElement('div');
  // `|| ''` AND NOT `?? ''`. The difference is not stylistic: 0 is falsy, so the
  // live helper renders a zero as the EMPTY STRING, and `??` would render "0".
  // These cards pass counts through here, so the two disagree on a value they
  // actually see. Reproduced; a port that modernised this would show a 0 where
  // the live card shows nothing.
  d.textContent = String(s || '');
  return d.innerHTML;
}

/**
 * A country code as a regional-indicator flag, with a GLOBE fallback.
 *
 * `iso2Flag` in `connections-map.ts` returns the EMPTY STRING for anything that
 * is not two characters; this returns 🌐. A row with no country renders a globe
 * on these cards and nothing on the connections map, and both are deliberate:
 * one is a list where the column must hold its width, the other an overlay where
 * an absent flag should take no space.
 */
export function dcFlag(cc: string | null | undefined): string {
  if (!cc || cc.length !== 2) return '🌐';
  const a = cc.toUpperCase().charCodeAt(0) - 65 + 0x1F1E6;
  const b = cc.toUpperCase().charCodeAt(1) - 65 + 0x1F1E6;
  return String.fromCodePoint(a) + String.fromCodePoint(b);
}

/**
 * A rate split into a number and its unit, for the two-part readouts.
 *
 * The thresholds step at 1000, 1 and 0.001 Mbps, and the PRECISION changes with
 * them: two decimals for Gbps and Mbps, one for Kbps. Below 0.001 there is no
 * number at all — an em dash and an empty unit, so the card shows a dash rather
 * than `0.00 Kbps` on an idle link.
 *
 * `+mbps || 0` coerces, so a non-numeric value reads as zero rather than NaN —
 * and note that it also turns a legitimate `0` into `0`, which is the same
 * answer, so nothing is lost by the sloppiness.
 */
export function dcSplitRate(mbps: unknown): { num: string; unit: string } {
  const n = Number(mbps) || 0;
  if (n >= 1000) return { num: (n / 1000).toFixed(2), unit: 'Gbps' };
  if (n >= 1) return { num: n.toFixed(2), unit: 'Mbps' };
  if (n >= 0.001) return { num: (n * 1000).toFixed(1), unit: 'Kbps' };
  return { num: '—', unit: '' };
}

/**
 * The IP Utilisation card's arc gauge.
 *
 * Same geometry as the DHCP page's — centre (100,105), r=72, 120° from 210° —
 * and NOT the same function. Two differences, both visible:
 *
 *  1. It writes the `dc-`prefixed ids, which are a different card.
 *  2. Its percentage text is `pct > 0 ? pct+'%' : '—'`, where the page's is
 *     `totalPool > 0 ? ... : '—'`. So a router at genuinely 0% utilisation shows
 *     an em dash on this card and `0%` on the page. That is the live behaviour
 *     of both, and they disagree with each other.
 *
 * The `> 0.5` degree threshold is what stops a sub-half-degree arc rendering as
 * a stray dot at the gauge's start.
 */
export function dcDrawGauge(pct: number): void {
  const gaugeFill = el('dc-dhcpGaugeFill');
  const gaugeTrack = el('dc-dhcpGaugeTrack');
  const gaugePct = el('dc-dhcpGaugePct');
  if (!gaugeFill || !gaugeTrack) return;

  const cx = 100, cy = 105, r = 72, startDeg = 210, totalDeg = 120;
  // `+(...).toFixed(2)` — two places, then back to a NUMBER, so 37.60 renders as
  // "37.6". The `d` attribute is compared character by character.
  const xy = (deg: number): { x: number; y: number } => {
    const rad = deg * Math.PI / 180;
    return { x: +(cx + r * Math.cos(rad)).toFixed(2), y: +(cy + r * Math.sin(rad)).toFixed(2) };
  };
  const sa = xy(startDeg), ea = xy(startDeg + totalDeg);
  gaugeTrack.setAttribute('d', 'M' + sa.x + ',' + sa.y + ' A' + r + ',' + r + ' 0 0,1 ' + ea.x + ',' + ea.y);

  const fillDeg = totalDeg * (Math.min(100, pct) / 100);
  if (fillDeg > 0.5) {
    const fa = xy(startDeg + fillDeg);
    // The large-arc flag is computed from a sweep that can never exceed 120°, so
    // it is always 0. Reproduced as the original writes it rather than folded to
    // a constant: the geometry is what would change if the sweep ever widened.
    gaugeFill.setAttribute('d', 'M' + sa.x + ',' + sa.y + ' A' + r + ',' + r + ' 0 ' +
      (fillDeg > 180 ? 1 : 0) + ',1 ' + fa.x + ',' + fa.y);
  } else {
    gaugeFill.setAttribute('d', '');
  }

  const colour = pct >= 90 ? '#f87171' : pct >= 70 ? '#fbbf24' : '#38bdf8';
  gaugeFill.setAttribute('stroke', colour);
  if (gaugePct) {
    gaugePct.textContent = pct > 0 ? (pct + '%') : '—';
    gaugePct.setAttribute('fill', colour);
  }
}
