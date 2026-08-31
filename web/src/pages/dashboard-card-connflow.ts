// The Dashboard's Connection Flow card (dc-card-flow).
//
// ── IT IS THE CONNECTIONS PAGE'S SANKEY, DRAWN SMALLER ──────────────────────
//
// `connections-sankey.ts` already has the renderer; this only points it at the
// card's own elements and tells it how much height it has. The live app splits
// it the same way — one `render()` and a `renderDc()` wrapper — which is why the
// diagram cannot drift between the page and the card.
//
// ── THE HEIGHT COMES FROM THE PARENT, NOT THE SVG ───────────────────────────
//
// `svg.parentElement.clientHeight`. The svg itself is sized by the diagram it is
// about to draw, so asking it how tall it is would return the LAST render's
// height and the card would never change size. `|| 0` covers a parent that is
// not laid out yet, which the renderer reads as "no constraint".
//
// ── EIGHT AND TEN ───────────────────────────────────────────────────────────
//
// The card takes the top eight sources and top ten destinations. The live app
// slices in the shared `conn:update` handler because its page and card draw from
// one payload; this side has separate handlers, so the numbers live here — which
// is also where they can be read next to the thing they constrain.

import { el } from '../dom';
import { renderSankey, type SankeyDest, type SankeySource } from './connections-sankey';

export function renderConnFlowCard(
  sources: SankeySource[] | undefined,
  destinations: SankeyDest[] | undefined,
): void {
  const svg = el('dc-sankeySvg') as unknown as SVGElement | null;
  const empty = el('dc-sankeyEmpty');
  // The live wrapper guards on the SVG alone and would throw if the empty-state
  // element were missing. Both live in the same markup block, so one without the
  // other is not reachable — guarding both is the same behaviour with one fewer
  // way to crash.
  if (!svg || !empty) return;
  const parent = (svg as unknown as { parentElement: HTMLElement | null }).parentElement;
  // The `: 0` is belt and braces — `avail || 0` below absorbs an undefined just
  // as well, and a mutation between the two is not observable. Kept because the
  // original has it and because it states at the measurement what the fallback
  // is, rather than three tokens later.
  const avail = parent ? parent.clientHeight : 0;
  renderSankey(svg, empty, (sources || []).slice(0, 8), (destinations || []).slice(0, 10), avail || 0);
}
