// The WAN Flow Sankey's arithmetic, pure so it can be tested without a DOM:
// what each uplink carries now, how thick its ribbon is, where its lanes run,
// and how fast its particles move. `wan-flow.ts` draws what this computes.
//
// ── THE SHAPE ───────────────────────────────────────────────────────────────
//
//   this router ══ribbon══▶ uplink ══ribbon══▶ Internet
//
// One ribbon per uplink, as thick as its share of all WAN traffic now, split
// into two lanes: upload (Tx, green) on top, flowing out, and download (Rx,
// blue) below it, flowing in. So the ribbons' widths ARE the split across ECMP,
// and the numbers on the nodes say how much it is.

import type { WANPayload } from '../gen/payloads';

export interface FlowRow {
  name: string;
  active: boolean;
  isTunnel: boolean;
  rx: number | null;
  tx: number | null;
  /** Percent of all WAN traffic (Rx + Tx) now: 0 when nothing moves, never NaN. */
  share: number;
}

export interface Flow {
  rx: number;
  tx: number;
  /** Uplinks whose default route is active: two or more is ECMP. */
  ecmp: number;
  rows: FlowRow[];
}

/**
 * The payload's uplinks as the diagram needs them, in the page's own order
 * (carrying traffic first, then by distance). The totals are the WAN page's own
 * Throughput sum, null counting as 0, so the card and the summary above it
 * cannot disagree.
 */
export function flowOf(wans: Pick<WANPayload, 'wans'>['wans']): Flow {
  let rx = 0, tx = 0, ecmp = 0;
  for (const w of wans) {
    rx += w.rxMbps || 0;
    tx += w.txMbps || 0;
    if (w.routeActive) ecmp++;
  }
  const total = rx + tx;
  const rows = wans.map((w) => ({
    name: w.name, active: w.routeActive, isTunnel: w.isTunnel, rx: w.rxMbps, tx: w.txMbps,
    share: total > 0 ? (((w.rxMbps || 0) + (w.txMbps || 0)) / total) * 100 : 0,
  }));
  return { rx, tx, ecmp, rows };
}

export interface Lane { top: number; h: number }
export interface Band {
  name: string;
  active: boolean;
  /** The ribbon's y at the router and at the Internet, where bands stack
   *  without gaps, and at the uplink node, where they are spread apart. */
  y0: number;
  y1: number;
  /** Its thickness at the ends, and where it enters its node: a ribbon
   *  narrows to fit INSIDE the node box, so a busy uplink's ribbon cannot
   *  spill over it or run across the gap between nodes. */
  h: number;
  hn: number;
  /** Tx on top, Rx below, relative to the band's own top, at the ends. At
   *  the node both are scaled by hn / h. */
  tx: Lane;
  rx: Lane;
}

export interface Layout {
  width: number;
  height: number;
  /** The three columns' x: the router's right edge, the uplink node's left and
   *  right edges, and the Internet's left edge. */
  xRouter: number;
  xNodeL: number;
  xNodeR: number;
  xNet: number;
  /** The stacked bands' span on the router and Internet side. */
  stackTop: number;
  stackH: number;
  bands: Band[];
}

/** No ribbon thinner than this: an idle uplink is still an uplink. */
export const MIN_BAND = 3;
/** Vertical room each uplink node needs for its name and rates. */
export const NODE_H = 58;
const NODE_GAP = 26;
/** The Dashboard card's gap: it is scaled to fit, and every unit of height
 *  spent between nodes is text made smaller. */
const NODE_GAP_COMPACT = 12;
/** How far inside the node box a ribbon enters it, top and bottom. */
const NODE_INSET = 6;
/** Room around the drawing. The top must hold the ECMP bracket's margin and
 *  its label above it (found live: at 18 the label was cut off). */
const PAD = 30;

/**
 * Where everything goes, for a drawing `width` pixels wide.
 *
 * The node column is laid out first: one node per uplink, NODE_H tall, spaced
 * evenly. The ribbons' thickness shares the stack height out by each uplink's
 * share, with MIN_BAND for any that carries nothing, and the stack sits
 * centred on the router and Internet side, so all of it pours into one point
 * at each end, which is what makes it read as a flow.
 */
export function layout(rows: FlowRow[], width: number, compact = false): Layout {
  const n = Math.max(rows.length, 1);
  const gap = compact ? NODE_GAP_COMPACT : NODE_GAP;
  const height = Math.max(compact ? 0 : 240, PAD * 2 + n * NODE_H + (n - 1) * gap);
  const nodeW = Math.min(230, Math.max(150, width * 0.24));
  const colW = 14;
  const xRouter = PAD + colW;
  const xNet = width - PAD - colW;
  const mid = (xRouter + xNet) / 2;
  const xNodeL = mid - nodeW / 2, xNodeR = mid + nodeW / 2;

  const stackH = Math.min(height - PAD * 2, Math.max(60, height * 0.62));
  const stackTop = (height - stackH) / 2;
  const floors = rows.length * MIN_BAND;
  const spare = Math.max(0, stackH - floors);
  const total = rows.reduce((a, r) => a + r.share, 0);

  const bands: Band[] = [];
  let y0 = stackTop;
  const nodeTop = (height - (rows.length * NODE_H + (rows.length - 1) * gap)) / 2;
  rows.forEach((r, i) => {
    // Shares, not rates: a zero total draws every band at the floor, and the
    // spare height is shared out only among uplinks that carry something.
    const h = MIN_BAND + (total > 0 ? (r.share / total) * spare : spare / rows.length);
    const t = (r.tx || 0), x = (r.rx || 0);
    const txH = t + x > 0 ? (t / (t + x)) * h : h / 2;
    const hn = Math.min(h, NODE_H - 2 * NODE_INSET);
    const nodeY = nodeTop + i * (NODE_H + gap) + (NODE_H - hn) / 2;
    bands.push({
      name: r.name, active: r.active, y0, y1: nodeY, h, hn,
      tx: { top: 0, h: txH }, rx: { top: txH, h: h - txH },
    });
    y0 += h;
  });
  return { width, height, xRouter, xNodeL, xNodeR, xNet, stackTop, stackH, bands };
}

/**
 * A ribbon from (xa, ya), `ha` thick, to (xb, yb), `hb` thick, as an SVG path:
 * two cubic curves whose handles reach a third of the way across, so a ribbon
 * leaves and arrives level and bends only in the middle.
 */
export function ribbon(xa: number, ya: number, xb: number, yb: number, ha: number, hb = ha): string {
  const d = (xb - xa) / 3;
  const f = (v: number): string => v.toFixed(1);
  return 'M' + f(xa) + ',' + f(ya) +
    'C' + f(xa + d) + ',' + f(ya) + ' ' + f(xb - d) + ',' + f(yb) + ' ' + f(xb) + ',' + f(yb) +
    'L' + f(xb) + ',' + f(yb + hb) +
    'C' + f(xb - d) + ',' + f(yb + hb) + ' ' + f(xa + d) + ',' + f(ya + ha) + ' ' + f(xa) + ',' + f(ya + ha) + 'Z';
}

/** The centre line of a lane, for its particles to travel along. */
export function laneLine(xa: number, ya: number, xb: number, yb: number): string {
  const d = (xb - xa) / 3;
  const f = (v: number): string => v.toFixed(1);
  return 'M' + f(xa) + ',' + f(ya) + 'C' + f(xa + d) + ',' + f(ya) + ' ' + f(xb - d) + ',' + f(yb) +
    ' ' + f(xb) + ',' + f(yb);
}

/**
 * How fast a lane's particles travel, in pixels a second. Logarithmic, so a
 * 1 Gb/s uplink is visibly faster than a 1 Mb/s one without either being a
 * blur or a crawl; 0 for a lane carrying nothing.
 */
export function particleSpeed(mbps: number | null): number {
  if (!mbps || mbps <= 0) return 0;
  return Math.min(260, 28 + 34 * Math.log10(1 + mbps * 10));
}

/** Ease a drawn value toward its target: smooth on every frame, and settled
 *  (not creeping for ever) once within a hundredth of a pixel. */
export function ease(cur: number, target: number, k = 0.14): number {
  const next = cur + (target - cur) * k;
  return Math.abs(target - next) < 0.01 ? target : next;
}

/** The WAN page's own rate format: 'Gb/s' and 'kb/s', not dom.ts's fmtMbps.
 *  Here so the table and the diagram share it without importing each other. */
export function fmtMb(v: number): string {
  return v >= 1000 ? (v / 1000).toFixed(2) + ' Gb/s'
    : v >= 1 ? v.toFixed(1) + ' Mb/s'
    : (v * 1000).toFixed(0) + ' kb/s';
}
