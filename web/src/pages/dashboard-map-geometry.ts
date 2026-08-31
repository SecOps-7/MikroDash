// The Connections Map's arc geometry and its highlight rule.
//
// The two exactly-comparable pieces of a 147-line card. The rest builds SVG
// nodes and seeds their animation with `Math.random()`, which needs a different
// kind of harness; these are numbers in, a string or a class out.
//
// ── THE ARC ALWAYS BOWS THE SAME WAY ────────────────────────────────────────
//
// The control point is placed along the normal to the chord, and the normal is
// FLIPPED when it points down (`ny > 0`). So every arc bows upward on screen
// whichever direction the connection runs — without that, arcs to the east and
// west of the router would curve opposite ways and the map would look like it
// was drawing two different things.
//
// ── AND THE RISE HAS A FLOOR ────────────────────────────────────────────────
//
// `Math.max(40, dist * 0.35)`. A short hop between neighbouring countries would
// otherwise be almost a straight line, and the comet animating along it would
// read as a glitch rather than a journey.
//
// ── HOT IS RELATIVE, NOT ABSOLUTE ───────────────────────────────────────────
//
// A country is `hot` at half the busiest country's count, so exactly one country
// is always hot when any traffic exists, and a quiet router lights up the same
// way a busy one does. The card answers "where is most of it going", not "is
// this a lot".

/** A quadratic path between two map points, bowing upward. Empty for a zero-length chord. */
export function mapArcD(x1: number, y1: number, x2: number, y2: number): string {
  const dx = x2 - x1, dy = y2 - y1;
  const dist = Math.sqrt(dx * dx + dy * dy);
  // A zero-length chord has no normal, so there is no arc to draw — returning ''
  // rather than a degenerate path, which the caller tests before using.
  if (!dist) return '';
  const cx = (x1 + x2) / 2, cy = (y1 + y2) / 2;
  const rise = Math.max(40, dist * 0.35);
  let nx = -dy / dist, ny = dx / dist;
  if (ny > 0) { nx = -nx; ny = -ny; }
  const cpx = cx + nx * rise, cpy = cy + ny * rise;
  // ONE decimal throughout: the path is compared against the existing attribute
  // to decide whether to rebuild the arc, so more precision would rebuild the
  // node — and restart its animation — on sub-pixel jitter.
  return 'M' + x1.toFixed(1) + ',' + y1.toFixed(1) +
    ' Q' + cpx.toFixed(1) + ',' + cpy.toFixed(1) +
    ' ' + x2.toFixed(1) + ',' + y2.toFixed(1);
}

/** Which class a country's shape should carry: '' , 'active' or 'hot'. */
export function mapHighlightClass(count: number, max: number): '' | 'active' | 'hot' {
  if (!(count > 0)) return '';
  return count >= max * 0.5 ? 'hot' : 'active';
}

/** The busiest count in a table, which is what `hot` is measured against. */
export function mapMaxCount(counts: Record<string, number>): number {
  let max = 0;
  for (const k of Object.keys(counts)) if (counts[k]! > max) max = counts[k]!;
  return max;
}

/**
 * Apply the highlight classes to a map's country shapes.
 *
 * Every known shape is visited, not only the ones with traffic — a country that
 * has gone quiet must LOSE its class, and iterating the counts alone would leave
 * it lit.
 */
export function applyMapHighlights(
  pathEls: Record<string, { classList: { add(c: string): void; remove(...c: string[]): void } }>,
  counts: Record<string, number>,
): void {
  const max = mapMaxCount(counts);
  for (const cc of Object.keys(pathEls)) {
    const el = pathEls[cc]!;
    const n = counts[cc] || 0;
    el.classList.remove('active', 'hot');
    const cls = mapHighlightClass(n, max);
    if (cls) el.classList.add(cls);
  }
}
