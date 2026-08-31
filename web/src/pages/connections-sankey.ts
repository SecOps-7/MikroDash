// The Connections page's flow diagram: LAN sources on the left, what they talk
// to on the right, ribbons in between.
//
// ── THE RIBBONS ARE AN APPROXIMATION, AND SAYING SO MATTERS ─────────────────
//
// There is no source-by-destination cross-matrix in the payload: the collector
// counts per source and per destination, not per pair, and building the matrix
// would mean sending one number for every combination of the two. So each
// source's bar is divided across destinations by DESTINATION weight, and each
// destination's bar across sources by SOURCE weight. The totals on both sides
// are exact; the individual ribbon between one host and one service is a
// proportional estimate, which is why hovering a ribbon names the pair rather
// than quoting a count.

const NS = 'http://www.w3.org/2000/svg';

/** Category colours, matching the svc-badge palette so a service reads the same
 *  colour in the list and in the diagram. */
const CAT_COLOUR: Record<string, string> = {
  cdn: '#38bdf8',
  cloud: '#fb923c',
  social: '#c084fc',
  streaming: '#ec4899',
  messaging: '#34d399',
  video: '#fbbf24',
  dns: '#2dd4bf',
  other: '#6382be',
};

/** Source nodes cycle a palette: they are hosts, with no category to colour by. */
const SRC_COLOURS = ['#38bdf8', '#818cf8', '#a78bfa', '#67e8f9', '#93c5fd', '#6ee7b7'];

export interface SankeySource { ip: string; name: string; count: number }
export interface SankeyDest {
  key?: string; ip?: string; count: number;
  country?: string; org?: string | null; cat?: string | null;
}

interface Node {
  label: string; count: number; cat?: string | null;
  x: number; y: number; h: number; side: 'src' | 'dst'; cursor: number;
}

function el(tag: string, attrs: Record<string, string | number>): SVGElement {
  const e = document.createElementNS(NS, tag) as SVGElement;
  for (const k of Object.keys(attrs)) e.setAttribute(k, String(attrs[k]));
  return e;
}

/**
 * One ribbon: a closed shape made of two cubic curves.
 *
 * The control points sit at the horizontal MIDPOINT, which is what gives the
 * flow its S-bend and keeps ribbons leaving one node from crossing each other
 * before they separate.
 */
export function linkPath(x0: number, y0: number, x1: number, y1: number,
  w0: number, w1: number): string {
  const mx = (x0 + x1) / 2;
  const by0 = y0 + w0, by1 = y1 + w1;
  return 'M' + x0 + ',' + y0 +
    ' C' + mx + ',' + y0 + ' ' + mx + ',' + y1 + ' ' + x1 + ',' + y1 +
    ' L' + x1 + ',' + by1 +
    ' C' + mx + ',' + by1 + ' ' + mx + ',' + by0 + ' ' + x0 + ',' + by0 +
    ' Z';
}

/**
 * Destinations, folded onto the label the diagram actually shows.
 *
 * An ORG first — "Cloudflare" is what someone recognises — then the country,
 * then the raw key. Two destinations at the same organisation are one node,
 * because a diagram with nine Google rows tells you less than one that says
 * Google is nine.
 */
export function foldDestinations(destinations: SankeyDest[]): Array<{ label: string; count: number; cat: string }> {
  const byLabel: Record<string, { label: string; count: number; cat: string }> = {};
  const order: string[] = [];
  destinations.forEach((d) => {
    const key = d.org || (d.country ? '[' + d.country + ']' : (d.key || d.ip || '?'));
    if (!byLabel[key]) {
      byLabel[key] = { label: key, count: 0, cat: d.cat || 'other' };
      order.push(key);
    }
    byLabel[key]!.count += d.count;
  });
  return order.map((k) => byLabel[k]!)
    .sort((a, b) => b.count - a.count)
    .slice(0, 10);
}

/** A label that fits: sixteen characters, then an ellipsis. */
function short(label: string): string {
  return label.length > 16 ? label.slice(0, 15) + '…' : label;
}

function nodeColour(node: Node, idx: number): string {
  if (node.side === 'dst') return CAT_COLOUR[node.cat || 'other'] || CAT_COLOUR['other']!;
  return SRC_COLOURS[idx % SRC_COLOURS.length]!;
}

/**
 * Draw the diagram into `svg`, or show `empty` when there is nothing to draw.
 *
 * `availH` lets the caller fit it to a card; without one the height grows with
 * the number of sources, because eight hosts squeezed into a fixed box is a
 * stack of hairlines.
 */
export function renderSankey(
  svg: SVGElement, empty: HTMLElement,
  sources: SankeySource[], destinations: SankeyDest[], availH?: number,
): void {
  svg.innerHTML = '';
  const total = sources.reduce((n, s) => n + s.count, 0);
  if (!total || !sources.length || !destinations.length) {
    empty.style.display = 'block';
    svg.style.display = 'none';
    return;
  }
  empty.style.display = 'none';
  svg.style.display = 'block';

  let W = (svg.parentElement && svg.parentElement.clientWidth) || 600;
  if (W < 200) W = 600;
  const NODE_W = 12, GAP = 6, PAD_X = 110, PAD_Y = 10;

  let H: number, innerH: number;
  if (availH && availH > 80) {
    H = availH;
    innerH = H - PAD_Y * 2;
  } else {
    innerH = Math.max(260, sources.length * 36 + 80);
    H = innerH + PAD_Y * 2;
  }
  void innerH;
  svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
  svg.setAttribute('height', String(H));

  const srcX = PAD_X, dstX = W - PAD_X - NODE_W;
  const drawH = H - PAD_Y * 2;
  const srcScale = (drawH - GAP * (sources.length - 1)) / total;
  const dstScale = (drawH - GAP * (destinations.length - 1)) / total;

  const srcNodes: Node[] = [];
  let y = PAD_Y;
  sources.forEach((s) => {
    // A FOUR-PIXEL FLOOR on every bar: a host with one connection among
    // thousands would otherwise be a zero-height rectangle, which is the same
    // as not drawing it at all.
    const h = Math.max(4, s.count * srcScale);
    srcNodes.push({ label: s.name || s.ip, count: s.count, x: srcX, y, h, side: 'src', cursor: y });
    y += h + GAP;
  });

  const folded = foldDestinations(destinations);
  const dstTotal = folded.reduce((n, d) => n + d.count, 0) || 1;
  const dstNodes: Node[] = [];
  let dy = PAD_Y;
  folded.forEach((d) => {
    // Rescaled against the SOURCE total, so both columns fill the same height
    // even though the folded destinations count fewer connections than the
    // sources do — the two sides are different views of one flow.
    const h = Math.max(4, (d.count / dstTotal) * total * dstScale);
    dstNodes.push({ label: d.label, count: d.count, cat: d.cat, x: dstX, y: dy, h, side: 'dst', cursor: dy });
    dy += h + GAP;
  });

  const srcSum = srcNodes.reduce((n, s) => n + s.count, 0) || 1;
  const links: Array<{ src: Node; dst: Node; sw: number; dw: number; sy: number; dy: number; cat?: string | null }> = [];
  srcNodes.forEach((src) => {
    dstNodes.forEach((dst) => {
      const sw = src.h * (dst.count / dstTotal);
      const dw = dst.h * (src.count / srcSum);
      // Under half a pixel at BOTH ends there is nothing to see, and drawing it
      // costs a path per pair on a diagram that already has eighty.
      if (sw < 0.5 && dw < 0.5) return;
      links.push({ src, dst, sw: Math.max(1, sw), dw: Math.max(1, dw),
        sy: src.cursor, dy: dst.cursor, cat: dst.cat });
      src.cursor += sw;
      dst.cursor += dw;
    });
  });

  // Links first, so the nodes sit on top of them.
  const linkG = el('g', {});
  links.forEach((lk) => {
    const colour = CAT_COLOUR[lk.cat || 'other'] || CAT_COLOUR['other']!;
    const p = el('path', {
      d: linkPath(lk.src.x + NODE_W, lk.sy, lk.dst.x, lk.dy, lk.sw, lk.dw),
      fill: colour, class: 'sk-link',
    });
    const title = document.createElementNS(NS, 'title');
    // The PAIR, not a count: the ribbon's width is an estimate, and quoting a
    // number would present it as a measurement.
    title.textContent = lk.src.label + ' → ' + lk.dst.label;
    p.appendChild(title);
    linkG.appendChild(p);
  });
  svg.appendChild(linkG);

  const drawNode = (n: Node, i: number, labelLeft: boolean): void => {
    const g = el('g', { class: 'sk-node', transform: 'translate(' + n.x + ',' + n.y + ')' });
    g.appendChild(el('rect', {
      width: NODE_W, height: Math.max(4, n.h), fill: nodeColour(n, i), rx: '3', ry: '3',
    }));
    const lbl = el('text', {
      x: labelLeft ? -6 : NODE_W + 6,
      y: Math.max(4, n.h) / 2,
      'dominant-baseline': 'middle',
      class: labelLeft ? 'sk-lbl-left' : 'sk-lbl-right',
    });
    lbl.textContent = short(n.label);
    g.appendChild(lbl);
    const title = document.createElementNS(NS, 'title');
    title.textContent = n.label + ' · ' + n.count + ' conns';
    g.appendChild(title);
    svg.appendChild(g);
  };

  srcNodes.forEach((n, i) => drawNode(n, i, true));
  dstNodes.forEach((n, i) => drawNode(n, i, false));
}

/**
 * A throttled renderer, which is how this diagram survives a three second poll.
 *
 * A full redraw builds up to eighty ribbon paths and two columns of nodes.
 * Doing that on every payload would be visible work for a picture that changes
 * shape slowly, so a render happens at most every five seconds — and an
 * unchanged fingerprint skips it entirely.
 *
 * While a COUNTRY OR CLIENT FILTER is active the caller owns the rendering: the
 * filtered view is derived from indexes this handler does not see, and letting
 * the poll re-render would wipe the filter every few seconds. The data is still
 * recorded, so lifting the filter draws the latest.
 */
export function createSankeyThrottle(
  draw: (sources: SankeySource[], destinations: SankeyDest[]) => void,
  throttleMs = 5000,
): {
  update: (sources: SankeySource[], destinations: SankeyDest[]) => void;
  setFiltered: (on: boolean) => void;
  redraw: () => void;
  redrawWith: (sources: SankeySource[], destinations: SankeyDest[]) => void;
} {
  let lastSrcs: SankeySource[] = [];
  let lastDsts: SankeyDest[] = [];
  let fp = '';
  let lastAt = 0;
  let pending = false;
  let filtered = false;

  return {
    update(sources, destinations) {
      const next = JSON.stringify(sources) + JSON.stringify(destinations);
      lastSrcs = sources;
      lastDsts = destinations;
      if (filtered) { fp = next; return; }
      if (next === fp) return;
      fp = next;
      const now = Date.now();
      if (now - lastAt >= throttleMs) {
        lastAt = now;
        draw(lastSrcs, lastDsts);
      } else if (!pending) {
        pending = true;
        setTimeout(() => {
          pending = false;
          lastAt = Date.now();
          draw(lastSrcs, lastDsts);
        }, throttleMs - (now - lastAt));
      }
    },
    setFiltered(on) { filtered = on; },
    // For a resize, or for a page change that has to redraw what is on screen.
    redraw() { draw(lastSrcs, lastDsts); },
    /**
     * The caller's own view, drawn NOW and without the throttle.
     *
     * A filter is a direct response to a click, so it must not wait up to five
     * seconds — the throttle exists to pace a poll nobody asked for, not to
     * delay something somebody did.
     */
    redrawWith(sources, destinations) {
      lastSrcs = sources;
      lastDsts = destinations;
      lastAt = Date.now();
      draw(sources, destinations);
    },
  };
}
