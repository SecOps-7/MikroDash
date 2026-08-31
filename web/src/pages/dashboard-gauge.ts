// The Dashboard's arc gauge: 28 rotated, rounded segments on a half circle.
//
// ── IT IS PURE, AND THAT IS WHY IT IS ITS OWN FILE ──────────────────────────
//
// Label, percentage and class in; an SVG string out. Nothing else on the System
// card is as easy to get subtly wrong or as easy to check exactly, so it is
// compared against the live implementation character for character.
//
// ── THE THRESHOLDS OVERRIDE THE CLASS ───────────────────────────────────────
//
// Above 75 the gauge is amber and above 90 it is red, whatever it was asked to
// be. So a CPU gauge at 95% is not blue-and-crit, it is crit — the caller's
// class is a default, not an instruction, and the percentage wins.
//
// ── THE ROUNDING IS TWO DECIMALS, EVERYWHERE ────────────────────────────────
//
// `_v` fixes every coordinate at two places. That is what makes this comparable
// at all: without it the two languages' floating point would print different
// tails for the same geometry and no string comparison could tell a real
// difference from a formatting one.

import { esc } from '../dom';

type Pt = [number, number];

/** Rotate (dx,dy) by a precomputed cos/sin and translate to (ox,oy). */
function rotPt(dx: number, dy: number, cos: number, sin: number, ox: number, oy: number): Pt {
  return [(dx * cos - dy * sin) + ox, (dx * sin + dy * cos) + oy];
}

/** Linear interpolation between two points. */
function lp(a: Pt, b: Pt, t: number): Pt {
  return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t];
}

/** A point as SVG path coordinates, two decimals. */
function v(p: Pt): string {
  return p[0].toFixed(2) + ',' + p[1].toFixed(2);
}

const COLOURS: Record<string, [string, string]> = {
  cpu: ['#38bdf8', '#818cf8'],   // sky → indigo
  mem: ['#34d399', '#34d399'],   // solid green
  hdd: ['#fb923c', '#f59f00'],   // orange → amber
  warn: ['#f59f00', '#fb923c'],  // amber → orange
  crit: ['#f87171', '#ef4444'],  // red
};

export function gauge(label: string, pct: number, cls: string): string {
  const activeCls = pct > 90 ? 'crit' : pct > 75 ? 'warn' : cls;
  const cols = COLOURS[activeCls] || COLOURS.cpu!;
  const pctCls = pct > 90 ? ' gauge-val-crit' : pct > 75 ? ' gauge-val-warn' : '';

  const SEGS = 28, START_DEG = 180, SWEEP_DEG = 180;
  const cx = 50, cy = 45, r = 38, segW = 3.2, segH = 10, RN = 0.15;
  const litSegs = Math.round((pct / 100) * SEGS);

  const r1 = parseInt(cols[0].slice(1, 3), 16), g1 = parseInt(cols[0].slice(3, 5), 16),
        b1 = parseInt(cols[0].slice(5, 7), 16);
  const r2 = parseInt(cols[1].slice(1, 3), 16), g2 = parseInt(cols[1].slice(3, 5), 16),
        b2 = parseInt(cols[1].slice(5, 7), 16);
  const hw = segW / 2, hh = segH / 2;
  const paths: string[] = [];

  for (let i = 0; i < SEGS; i++) {
    const angleDeg = START_DEG + (i + 0.5) * (SWEEP_DEG / SEGS);
    const angleRad = angleDeg * Math.PI / 180;
    const sx = cx + r * Math.cos(angleRad), sy = cy + r * Math.sin(angleRad);
    const t = SEGS > 1 ? i / (SEGS - 1) : 0;
    let colour: string, opacity: number;
    if (i < litSegs) {
      // The gradient runs across the WHOLE arc, not across the lit part, so a
      // gauge at 40% shows the first 40% of the same ramp rather than a
      // compressed copy of all of it.
      const ri = Math.round(r1 + (r2 - r1) * t), gi = Math.round(g1 + (g2 - g1) * t),
            bi = Math.round(b1 + (b2 - b1) * t);
      colour = 'rgb(' + ri + ',' + gi + ',' + bi + ')';
      opacity = 1;
    } else {
      colour = 'rgba(99,130,190,0.12)';
      opacity = 0.7;
    }
    const rotRad = (angleDeg + 90) * Math.PI / 180;
    const cos = Math.cos(rotRad), sin = Math.sin(rotRad);
    const tl = rotPt(-hw, -hh, cos, sin, sx, sy), tr = rotPt(hw, -hh, cos, sin, sx, sy);
    const br = rotPt(hw, hh, cos, sin, sx, sy), bl = rotPt(-hw, hh, cos, sin, sx, sy);
    const d = ['M', v(lp(tl, tr, RN)), 'L', v(lp(tr, tl, RN)),
      'Q', v(tr), v(lp(tr, br, RN)), 'L', v(lp(br, tr, RN)),
      'Q', v(br), v(lp(br, bl, RN)), 'L', v(lp(bl, br, RN)),
      'Q', v(bl), v(lp(bl, tl, RN)), 'L', v(lp(tl, bl, RN)),
      'Q', v(tl), v(lp(tl, tr, RN)), 'Z'].join(' ');
    paths.push('<path d="' + d + '" fill="' + colour + '" opacity="' + opacity + '"/>');
  }

  return '<div class="gauge-arc-wrap">' +
    '<svg class="gauge-arc-svg" viewBox="0 0 100 62">' +
      paths.join('') +
      '<text class="gauge-arc-pct' + pctCls + '" x="50" y="52" font-size="10">' + pct + '%</text>' +
      '<text class="gauge-arc-lbl" x="50" y="61" font-size="6">' + esc(label) + '</text>' +
    '</svg>' +
  '</div>';
}
