// WAN Flow: a live Sankey of where the WAN traffic goes, drawn on the WAN page
// and in the Dashboard card of the same name.
//
//   this router ══▶ each uplink ══▶ Internet
//
// Each uplink's ribbon is as thick as its share of the traffic now, split into
// an upload lane (green, particles flowing out) and a download lane (blue,
// particles flowing in); the busier a lane, the faster its particles. Uplinks
// sharing the default route sit inside an ECMP bracket; a standby uplink is
// dimmed. The geometry is `wan-flow-layout.ts`; this draws it.
//
// NO ROUTER COST beyond the WAN collector the view already needs. Everything
// comes from `wan:update`, whose rates are borrowed from the interface-status
// collector; a new reading lands at the WAN poll interval, and between readings
// the ribbons ease toward their new widths and the particles keep flowing.
//
// ── ONE ENGINE, TWO PLACES ──────────────────────────────────────────────────
//
// `createWanFlow` makes one drawing, bound to its own elements. The two differ
// only in how they are sized:
//   rows  the WAN page: as wide as the card, as tall as the uplinks need;
//   box   the Dashboard card: laid out at the card's own aspect ratio and
//         scaled to fill it, so ribbons, nodes and text grow and shrink with
//         the card's edges and nothing is distorted or cut off.
//
// ── ONE LOOP EACH ───────────────────────────────────────────────────────────
//
// A single requestAnimationFrame loop (about 30 frames a second) per drawing,
// running while its view is visible and the data is live. Under reduced motion
// there are no particles and the ribbons change at once.

import { el } from '../dom';
import { t } from '../i18n';
import type { Socket } from '../socket';
import type { WANPayload } from '../gen/payloads';
import { isRosDisconnected } from '../banners';
import {
  ease, flowOf, fmtMb, layout, particleSpeed, ribbon, NODE_H, type Band, type Flow, type Layout,
} from './wan-flow-layout';

const NS = 'http://www.w3.org/2000/svg';

export interface WanFlowOptions {
  /** Prefix for this drawing's SVG gradient and filter ids, which must be
   *  unique in the document. */
  id: string;
  /** Its elements, by id: named in full by the caller, so each id is written
   *  once as a literal where a reader (and the static checks) can find it. */
  cardId: string;
  wrapId: string;
  svgId: string;
  emptyId: string;
  fit: 'rows' | 'box';
  /** Whether the view holding it is on screen. */
  visible: () => boolean;
  /** What to say when there are no uplinks. */
  noUplinks: string;
}

export interface WanFlow {
  note(p: WANPayload): void;
  clear(): void;
  /** Draw again, at the current size: on returning to the view. */
  redraw(): void;
}

interface Lanes { tx: SVGPathElement; rx: SVGPathElement }
interface Drawn {
  left: Lanes; right: Lanes; dotsTx: SVGPathElement; dotsRx: SVGPathElement;
  node: SVGGElement; share: SVGTextElement; rates: SVGTextElement; bar: SVGRectElement; track: number;
  /** What is on screen now, eased toward the layout's band every frame. */
  cur: Band;
  offTx: number; offRx: number;
}

const reducedMotion = (): boolean =>
  typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;

function svg<K extends keyof SVGElementTagNameMap>(tag: K, attrs: Record<string, string | number>,
  parent?: Element): SVGElementTagNameMap[K] {
  const e = document.createElementNS(NS, tag);
  for (const k of Object.keys(attrs)) e.setAttribute(k, String(attrs[k]));
  parent?.appendChild(e);
  return e;
}

/** A lane's centre line, router to Internet, straight across the node. */
function laneThrough(L: Layout, b: Band, lane: 'tx' | 'rx'): string {
  const k = b.h > 0 ? b.hn / b.h : 1;
  const off = b[lane].top + b[lane].h / 2;
  const ya = b.y0 + off, yn = b.y1 + off * k;
  const d1 = (L.xNodeL - L.xRouter) / 3, d2 = (L.xNet - L.xNodeR) / 3;
  const f = (v: number): string => v.toFixed(1);
  return 'M' + f(L.xRouter) + ',' + f(ya) +
    'C' + f(L.xRouter + d1) + ',' + f(ya) + ' ' + f(L.xNodeL - d1) + ',' + f(yn) + ' ' + f(L.xNodeL) + ',' + f(yn) +
    'L' + f(L.xNodeR) + ',' + f(yn) +
    'C' + f(L.xNodeR + d2) + ',' + f(yn) + ' ' + f(L.xNet - d2) + ',' + f(ya) + ' ' + f(L.xNet) + ',' + f(ya);
}

/** Put one band's ribbons and particle lines where `b` says. */
function place(L: Layout, d: Drawn, b: Band): void {
  // Full thickness at the ends, narrowed by hn / h into the node.
  const s = b.h > 0 ? b.hn / b.h : 1;
  const lane = (k: 'tx' | 'rx'): [string, string] => [
    ribbon(L.xRouter, b.y0 + b[k].top, L.xNodeL, b.y1 + b[k].top * s, b[k].h, b[k].h * s),
    ribbon(L.xNodeR, b.y1 + b[k].top * s, L.xNet, b.y0 + b[k].top, b[k].h * s, b[k].h),
  ];
  const [ltx, rtx] = lane('tx'), [lrx, rrx] = lane('rx');
  d.left.tx.setAttribute('d', ltx); d.right.tx.setAttribute('d', rtx);
  d.left.rx.setAttribute('d', lrx); d.right.rx.setAttribute('d', rrx);
  d.dotsTx.setAttribute('d', laneThrough(L, b, 'tx'));
  d.dotsRx.setAttribute('d', laneThrough(L, b, 'rx'));
  // A particle is about half its lane, and never lost or fat.
  d.dotsTx.setAttribute('stroke-width', String(Math.max(1.6, Math.min(4, b.tx.h * 0.55))));
  d.dotsRx.setAttribute('stroke-width', String(Math.max(1.6, Math.min(4, b.rx.h * 0.55))));
  d.node.setAttribute('transform', 'translate(' + L.xNodeL.toFixed(1) + ',' +
    (b.y1 + b.hn / 2 - NODE_H / 2).toFixed(1) + ')');
}

/**
 * The layout for a `box` drawing: at the box's aspect ratio, in the units the
 * page drawing uses, so it scales into the box without distortion. The height
 * is whatever the uplinks need; the width follows the box, and never so narrow
 * that the columns collide (a very tall, thin box letterboxes instead).
 */
export function boxLayout(flow: Flow, boxW: number, boxH: number): Layout {
  const h = layout(flow.rows, 1000, true).height;
  const w = Math.max(420, boxH > 0 ? (h * boxW) / boxH : 1000);
  return layout(flow.rows, Math.round(w), true);
}

export function createWanFlow(socket: Socket, o: WanFlowOptions): WanFlow {
  const ids = { card: o.cardId, wrap: o.wrapId, svg: o.svgId, empty: o.emptyId };
  let flow: Flow = { rx: 0, tx: 0, ecmp: 0, rows: [] };
  let target: Layout | null = null;
  let drawn: Drawn[] = [];
  let shapeKey = '';
  let ratesAvailable = true;
  let socketUp = true;
  let looping = false;
  let lastT = 0;
  const live = (): boolean => o.visible() && socketUp && !isRosDisconnected() && !document.hidden;

  /** Build the drawing for this set of uplinks at this size. */
  function build(host: SVGSVGElement, L: Layout): void {
    host.innerHTML = '';
    host.setAttribute('viewBox', '0 0 ' + L.width + ' ' + L.height);
    if (o.fit === 'rows') host.setAttribute('height', String(L.height));
    const defs = svg('defs', {}, host);
    // Each lane is brightest where it meets the router and the Internet, and
    // softest at the uplink, so the eye follows it across.
    const grad = (id: string, colour: string): void => {
      const g = svg('linearGradient', { id, x1: 0, x2: 1, y1: 0, y2: 0 }, defs);
      svg('stop', { offset: '0%', 'stop-color': colour, 'stop-opacity': 0.55 }, g);
      svg('stop', { offset: '50%', 'stop-color': colour, 'stop-opacity': 0.18 }, g);
      svg('stop', { offset: '100%', 'stop-color': colour, 'stop-opacity': 0.55 }, g);
    };
    grad(o.id + 'Tx', 'var(--accent-tx)');
    grad(o.id + 'Rx', 'var(--accent-rx)');
    const glow = svg('filter', { id: o.id + 'Glow', x: '-50%', y: '-50%', width: '200%', height: '200%' }, defs);
    svg('feGaussianBlur', { stdDeviation: 1.6, result: 'b' }, glow);
    const merge = svg('feMerge', {}, glow);
    svg('feMergeNode', { in: 'b' }, merge);
    svg('feMergeNode', { in: 'SourceGraphic' }, merge);

    // THE ECMP BRACKET, behind the uplinks sharing the default route. They
    // come first in the page's order, so they are contiguous.
    const active = L.bands.filter((b) => b.active);
    if (active.length >= 2) {
      const top = active[0]!.y1 + active[0]!.hn / 2 - NODE_H / 2 - 12;
      const last = active[active.length - 1]!;
      const bottom = last.y1 + last.hn / 2 + NODE_H / 2 + 12;
      svg('rect', { class: 'wf-ecmp', x: L.xNodeL - 12, y: top, width: L.xNodeR - L.xNodeL + 24,
        height: bottom - top, rx: 16 }, host);
      const t = svg('text', { class: 'wf-ecmp-label', x: L.xNodeL - 4, y: top - 5 }, host);
      t.textContent = 'ECMP ×' + active.length;
    }

    // The two ends: a glowing pillar each, where every ribbon meets.
    const pillar = (x: number, label: string, cls: string): void => {
      svg('rect', { class: 'wf-pillar ' + cls, x, y: L.stackTop - 6, width: 14, height: L.stackH + 12, rx: 7 }, host);
      const t = svg('text', { class: 'wf-end-label', x: x + 7, y: L.stackTop - 16, 'text-anchor': 'middle' }, host);
      t.textContent = label;
    };
    pillar(L.xRouter - 14, 'ROUTER', 'is-router');
    pillar(L.xNet, 'INTERNET', 'is-net');

    const ribbons = svg('g', { class: 'wf-ribbons' }, host);
    const dots = svg('g', { class: 'wf-dots', filter: 'url(#' + o.id + 'Glow)' }, host);
    const nodes = svg('g', { class: 'wf-nodes' }, host);
    drawn = L.bands.map((b) => {
      const standby = b.active ? '' : ' is-standby';
      const lanes = (): Lanes => ({
        tx: svg('path', { class: 'wf-lane is-tx' + standby, fill: 'url(#' + o.id + 'Tx)' }, ribbons),
        rx: svg('path', { class: 'wf-lane is-rx' + standby, fill: 'url(#' + o.id + 'Rx)' }, ribbons),
      });
      const left = lanes(), right = lanes();
      const dotsTx = svg('path', { class: 'wf-dot is-tx', 'stroke-dasharray': '0 22', 'stroke-linecap': 'round' }, dots);
      const dotsRx = svg('path', { class: 'wf-dot is-rx', 'stroke-dasharray': '0 22', 'stroke-linecap': 'round' }, dots);
      const w = L.xNodeR - L.xNodeL;
      const node = svg('g', { class: 'wf-node' + standby }, nodes);
      svg('rect', { class: 'wf-node-box', width: w, height: NODE_H, rx: 12 }, node);
      const name = svg('text', { class: 'wf-node-name', x: 12, y: 21 }, node);
      name.textContent = b.name;
      const rates = svg('text', { class: 'wf-node-rates', x: 12, y: 40 }, node);
      const share = svg('text', { class: 'wf-node-share', x: w - 12, y: 23, 'text-anchor': 'end' }, node);
      svg('rect', { class: 'wf-node-track', x: 12, y: NODE_H - 10, width: w - 24, height: 3, rx: 1.5 }, node);
      const bar = svg('rect', { class: 'wf-node-bar', x: 12, y: NODE_H - 10, width: 0, height: 3, rx: 1.5 }, node);
      const d: Drawn = { left, right, dotsTx, dotsRx, node, share, rates, bar, track: w - 24,
        cur: { ...b, tx: { ...b.tx }, rx: { ...b.rx } }, offTx: 0, offRx: 0 };
      place(L, d, d.cur);
      return d;
    });
  }

  /** Each node's numbers and the empty state. Text, not geometry: no easing. */
  function writeText(): void {
    const rate = (v: number | null): string => (v === null ? '—' : fmtMb(v));
    flow.rows.forEach((r, i) => {
      const d = drawn[i];
      if (!d) return;
      d.rates.textContent = '↓ ' + rate(r.rx) + '   ↑ ' + rate(r.tx);
      d.share.textContent = Math.round(r.share) + '%';
      d.bar.setAttribute('width', (d.track * r.share / 100).toFixed(1));
    });
    const empty = el(ids.empty);
    const why = !flow.rows.length ? o.noUplinks
      : !ratesAvailable ? t('Waiting for interface rates. They come from the interface status collector.') : '';
    if (empty) { empty.textContent = why; empty.hidden = !why; }
    el(ids.card)?.classList.toggle('is-paused', !live());
  }

  /** Lay out and draw for the current reading, at the current size. */
  function draw(): void {
    if (!o.visible()) return;
    const host = el(ids.svg) as unknown as SVGSVGElement | null;
    const wrap = el(ids.wrap);
    if (!host || !wrap) return;
    const w = wrap.clientWidth, h = wrap.clientHeight;
    if (o.fit === 'box' && (!w || !h)) return;
    target = o.fit === 'box' ? boxLayout(flow, w, h) : layout(flow.rows, Math.max(320, w || 800));
    const key = flow.rows.map((r) => r.name + (r.active ? '+' : '-')).join('\u0001') + '|' + target.width;
    if (key !== shapeKey) {
      build(host, target);
      shapeKey = key;
    }
    if (reducedMotion()) drawn.forEach((d, i) => { d.cur = target!.bands[i]!; place(target!, d, d.cur); });
    writeText();
    if (!looping && live()) {
      looping = true;
      lastT = 0;
      requestAnimationFrame(frame);
    }
  }

  /** Ease every band toward its target, and move the particles. */
  function frame(t: number): void {
    if (!live() || !target) { looping = false; el(ids.card)?.classList.add('is-paused'); return; }
    if (t - lastT >= 33) {
      const dt = lastT ? Math.min(0.1, (t - lastT) / 1000) : 0;
      lastT = t;
      if (!reducedMotion()) {
        drawn.forEach((d, i) => {
          const b = target!.bands[i];
          if (!b) return;
          const c = d.cur;
          c.y0 = ease(c.y0, b.y0); c.y1 = ease(c.y1, b.y1); c.h = ease(c.h, b.h); c.hn = ease(c.hn, b.hn);
          c.tx.h = ease(c.tx.h, b.tx.h); c.rx.top = ease(c.rx.top, b.rx.top); c.rx.h = ease(c.rx.h, b.rx.h);
          place(target!, d, c);
          const r = flow.rows[i];
          // Upload flows out along the path; download flows back against it.
          d.offTx -= particleSpeed(r ? r.tx : 0) * dt;
          d.offRx += particleSpeed(r ? r.rx : 0) * dt;
          d.dotsTx.setAttribute('stroke-dashoffset', d.offTx.toFixed(1));
          d.dotsRx.setAttribute('stroke-dashoffset', d.offRx.toFixed(1));
          d.dotsTx.style.opacity = r && r.tx ? '1' : '0';
          d.dotsRx.style.opacity = r && r.rx ? '1' : '0';
        });
      }
    }
    requestAnimationFrame(frame);
  }

  socket.on('disconnect', () => { socketUp = false; writeText(); });
  socket.on('connect', () => { socketUp = true; draw(); });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) draw(); });
  // The size decides the layout: a resized window, or a resized dashboard
  // card, redraws at its new size.
  const wrap = el(ids.wrap);
  if (wrap && typeof ResizeObserver === 'function') {
    let lastW = 0, lastH = 0;
    new ResizeObserver(() => {
      const w = wrap.clientWidth, h = wrap.clientHeight;
      if (Math.abs(w - lastW) > 4 || (o.fit === 'box' && Math.abs(h - lastH) > 4)) {
        lastW = w; lastH = h;
        draw();
      }
    }).observe(wrap);
  }

  return {
    note(p: WANPayload): void {
      flow = flowOf(p.wans);
      ratesAvailable = p.ratesAvailable;
      // THE TEXT STATE ON EVERY READING, the drawing only on screen: the empty
      // message and the paused marker are true wherever the card is, and cost
      // nothing to write.
      writeText();
      draw();
    },
    clear(): void {
      flow = { rx: 0, tx: 0, ecmp: 0, rows: [] };
      shapeKey = '';
      drawn = [];
      target = null;
      draw();
    },
    redraw(): void {
      shapeKey = '';
      draw();
    },
  };
}
