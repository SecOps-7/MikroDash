// The Tools page's traceroute map: the route drawn as it is found.
//
// ── A PLAN, THEN A PLAYER ───────────────────────────────────────────────────
//
// `planTrace` is pure: a result in, the ordered steps to draw out. A step is a
// hop that answered, placed where the geo database puts it, or where the route
// already is when the database does not know it (a private or CGNAT hop). The
// player keeps how many steps it has queued, and each frame of the run appends
// only the new ones, so a table that grows hop by hop animates hop by hop and
// a step once drawn is never redrawn.
//
// A step's hop is SETTLED before it is planned: the table the router re-sends
// holds the hop still being probed as a row with no address, so a hop is
// planned once it has an address, has timed out, or has a hop after it.
//
// ── THE LINE DRAWS ITSELF BEHIND A COMET ────────────────────────────────────
//
// Each line is a path whose dash offset runs from its length to zero while a
// comet rides its head (`getPointAtLength`), on one requestAnimationFrame loop.
// Steps play one after another, so a burst of hops arriving together still
// draws in order. The hop ripples where the comet lands.
//
// ── THE VIEW FOLLOWS THE ROUTE ──────────────────────────────────────────────
//
// After each landing the viewBox eases towards the box around every point so
// far, padded, at least a region wide, and kept at the map's 2:1. Marks are
// sized by `--k`, the view's scale, so they stay the same size on screen at any
// zoom.
//
// ── AND THE OPERATOR CAN TAKE IT OVER ───────────────────────────────────────
//
// Wheel, the + and − buttons and dragging are the Connections map's own
// (`attachMapZoom`, a CSS transform over the fitted view). Once the operator
// zooms, the view stops following the route, which would otherwise pull the
// map out from under them at the next hop; Fit hands it back. `--k` divides by
// their zoom too, so the marks do not swell as they zoom in.
//
// Hovering a mark shows every hop at that point: a private hop has no place of
// its own and sits on the one before it, so one dot can stand for several.

import { loadCountries, attachMapZoom, bindZoomButtons } from './connections-worldmap';
import { project } from './connections-map';
import { esc, iso2Flag } from '../dom';
import type { TracerouteResult } from '../gen/payloads';

const NS = 'http://www.w3.org/2000/svg';
const WORLD = { x: 0, y: 0, w: 1000, h: 500 };

// THE PACE, in one place. Slowed by about half on the operator's request
// (2026-09-19): a hop's line took under a second and a burst of hops blurred.
const LINE_MS_MIN = 1100;   // a short hop's flight
const LINE_MS_PER = 8;      // plus this per map unit of line
const LINE_MS_MAX = 3200;   // an ocean crossing
const PAUSE_MS = 400;       // between one landing and the next flight
const ZOOM_MS = 1000;       // the view easing to the route

type Pt = [number, number];

export interface TraceStep {
  /** The hop number; 0 is the router itself. */
  hop: number;
  at: Pt;
  /** Where its line starts; null when it draws none (the first point, or a hop
   *  at the same place as the one before it). */
  from: Pt | null;
  /** Whether the hop has a place of its own, or sits where the route already is. */
  located: boolean;
}

const same = (a: Pt, b: Pt): boolean => Math.abs(a[0] - b[0]) < 0.3 && Math.abs(a[1] - b[1]) < 0.3;

/** The steps a result draws, in order. Append-only as the run grows. */
export function planTrace(r: TracerouteResult): TraceStep[] {
  const steps: TraceStep[] = [];
  let cur: Pt | null = null;
  if (r.origin) {
    cur = project(r.origin.lon, r.origin.lat);
    steps.push({ hop: 0, at: cur, from: null, located: true });
  }
  for (let i = 0; i < r.hops.length; i++) {
    const h = r.hops[i]!;
    if (!h.timedOut && !h.address && i === r.hops.length - 1) break; // still being probed
    if (h.timedOut || !h.address) continue; // no answer: listed, not drawn
    if (h.lat != null && h.lon != null) {
      const p = project(h.lon, h.lat);
      steps.push({ hop: h.hop, at: p, from: cur && !same(cur, p) ? cur : null, located: true });
      cur = p;
    } else if (cur) {
      steps.push({ hop: h.hop, at: cur, from: null, located: false });
    }
  }
  return steps;
}

/** A shallow arc from a to b, rising in proportion to its length. */
function arcD(a: Pt, b: Pt): string {
  const dx = b[0] - a[0], dy = b[1] - a[1];
  const d = Math.hypot(dx, dy) || 1;
  const rise = d * 0.22;
  let nx = -dy / d, ny = dx / d;
  if (ny > 0) { nx = -nx; ny = -ny; }
  const cx = (a[0] + b[0]) / 2 + nx * rise, cy = (a[1] + b[1]) / 2 + ny * rise;
  return 'M' + a[0].toFixed(1) + ',' + a[1].toFixed(1) + ' Q' + cx.toFixed(1) + ',' + cy.toFixed(1) +
    ' ' + b[0].toFixed(1) + ',' + b[1].toFixed(1);
}

export interface Box { x: number; y: number; w: number; h: number }

/** The view around these points: padded, at least a region wide, 2:1. */
export function fitBox(points: Pt[]): Box {
  if (!points.length) return { ...WORLD };
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const [x, y] of points) {
    x0 = Math.min(x0, x); y0 = Math.min(y0, y); x1 = Math.max(x1, x); y1 = Math.max(y1, y);
  }
  let w = Math.max(60, (x1 - x0) * 1.5), h = Math.max(30, (y1 - y0) * 1.5);
  if (w / h < 2) w = h * 2; else h = w / 2;
  if (w >= WORLD.w) return { ...WORLD };
  const cx = (x0 + x1) / 2, cy = (y0 + y1) / 2;
  const x = Math.min(Math.max(cx - w / 2, 0), WORLD.w - w);
  const y = Math.min(Math.max(cy - h / 2, 0), WORLD.h - h);
  return { x, y, w, h };
}

function svgEl(tag: string, attrs: Record<string, string | number>): SVGElement {
  const e = document.createElementNS(NS, tag) as SVGElement;
  for (const k of Object.keys(attrs)) e.setAttribute(k, String(attrs[k]));
  return e;
}

const reduced = (): boolean =>
  typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;

function fmtMs(v: number | null | undefined): string {
  return v == null ? '' : (v < 1 ? v.toFixed(2) : v.toFixed(1)) + ' ms';
}

export interface TraceMap {
  update: (r: TracerouteResult) => void;
  clear: () => void;
}

/** The card's elements: the map, its wrapper, the hop list, the empty note,
 *  the hover tip and the zoom buttons. */
export interface TraceMapEls {
  svg: SVGSVGElement;
  wrap: HTMLElement;
  list: HTMLElement;
  empty: HTMLElement | null;
  tip: HTMLElement | null;
  zoomIn: HTMLElement | null;
  zoomOut: HTMLElement | null;
  fit: HTMLElement | null;
}

export function createTraceMap(els: TraceMapEls): TraceMap {
  const { svg, wrap, list, empty, tip } = els;
  const countries = svgEl('g', { class: 'trace-countries' });
  const lines = svgEl('g', {});
  const marks = svgEl('g', {});
  const labels = svgEl('g', {});
  svg.append(countries, lines, marks, labels);
  const countryEls: Record<string, SVGElement> = {};
  const lit = new Set<string>();
  loadCountries().then((cs) => {
    for (const c of cs) {
      const p = svgEl('path', { d: c.d, class: 'map-country', 'data-cc': c.cc });
      countryEls[c.cc] = p;
      countries.appendChild(p);
    }
    for (const cc of lit) countryEls[cc]?.classList.add('active');
  }).catch(() => { /* no atlas: the route still draws, over an empty sea */ });

  let gen = 0;               // bumped by clear(), so a stale animation stops
  let queued = 0;            // steps handed to the player
  const queue: TraceStep[] = [];
  let playing = false;
  const points: Pt[] = [];
  const landed = new Set<number>();
  const drawn: TraceStep[] = [];
  let last: TracerouteResult | null = null;
  let view: Box = { ...WORLD };
  let manual = false;        // the operator zoomed: stop following the route
  let userScale = 1;         // their zoom, read back from attachMapZoom's transform

  function setView(b: Box): void {
    view = b;
    svg.setAttribute('viewBox', b.x.toFixed(2) + ' ' + b.y.toFixed(2) + ' ' + b.w.toFixed(2) + ' ' + b.h.toFixed(2));
    svg.style.setProperty('--k', (b.w / WORLD.w / userScale).toFixed(4));
  }
  setView(WORLD);

  const zoom = attachMapZoom(wrap, svg);
  bindZoomButtons(wrap, els.zoomIn, els.zoomOut);
  // Registered after attachMapZoom's own, so it reads the zoom just applied.
  wrap.addEventListener('wheel', () => {
    manual = true;
    const m = /scale\(([\d.]+)\)/.exec(svg.style.transform || '');
    userScale = m ? Number(m[1]) : 1;
    setView(view);
  }, { passive: true });
  const refit = (): void => {
    manual = false;
    userScale = 1;
    zoom.reset();
    setView(view);
    zoomTo(fitBox(points));
  };
  els.fit?.addEventListener('click', refit);

  // ── THE HOVER TIP ─────────────────────────────────────────────────────────
  function tipFor(at: Pt): string {
    const here = drawn.filter((d) => same(d.at, at));
    return here.map((d) => {
      if (d.hop === 0) return '<div class="trace-tip-row"><b>This router</b> · ' + esc(last?.origin?.label || '') + '</div>';
      const h = last?.hops.find((x) => x.hop === d.hop);
      if (!h) return '';
      const where = h.city || h.country
        ? (h.country ? iso2Flag(h.country) + ' ' : '') + esc([h.city, h.country].filter(Boolean).join(', '))
        : 'private or unknown address';
      return '<div class="trace-tip-row"><b>Hop ' + h.hop + '</b> · ' + where + '<br><span class="trace-tip-ip">' + esc(h.address) +
        '</span> · ' + fmtMs(h.lastMs) + (h.bestMs != null && h.worstMs != null && h.bestMs !== h.worstMs
          ? ' (best ' + fmtMs(h.bestMs) + ', worst ' + fmtMs(h.worstMs) + ')' : '') + ' · ' + h.lossPct + '% loss</div>';
    }).join('');
  }
  svg.addEventListener('pointermove', (e) => {
    if (!tip) return;
    const mark = (e.target as Element | null)?.closest?.('[data-hop]');
    const at = mark?.getAttribute('data-at');
    if (!at) { tip.style.display = 'none'; return; }
    const [x, y] = at.split(',').map(Number) as [number, number];
    tip.innerHTML = tipFor([x, y]);
    const r = wrap.getBoundingClientRect();
    tip.style.display = 'block';
    tip.style.left = Math.min(e.clientX - r.left + 14, r.width - tip.offsetWidth - 6) + 'px';
    tip.style.top = Math.max(6, e.clientY - r.top - tip.offsetHeight - 10) + 'px';
  });
  svg.addEventListener('pointerleave', () => { if (tip) tip.style.display = 'none'; });

  function zoomTo(to: Box): void {
    const g = gen, from = { ...view };
    if (reduced() || typeof requestAnimationFrame !== 'function') { setView(to); return; }
    const t0 = performance.now();
    const step = (now: number): void => {
      if (g !== gen) return;
      const k = Math.min(1, (now - t0) / ZOOM_MS);
      const e = k < 0.5 ? 2 * k * k : 1 - Math.pow(-2 * k + 2, 2) / 2;
      setView({ x: from.x + (to.x - from.x) * e, y: from.y + (to.y - from.y) * e,
        w: from.w + (to.w - from.w) * e, h: from.h + (to.h - from.h) * e });
      if (k < 1) requestAnimationFrame(step);
    };
    requestAnimationFrame(step);
  }

  function land(s: TraceStep): void {
    const hop = s.hop === 0 ? null : last?.hops.find((h) => h.hop === s.hop);
    drawn.push(s);
    if (s.located) {
      marks.appendChild(svgEl('circle', { cx: s.at[0], cy: s.at[1], class: s.hop === 0 ? 'trace-home' : 'trace-dot',
        'data-hop': s.hop, 'data-at': s.at[0] + ',' + s.at[1] }));
      const text = svgEl('text', { x: s.at[0], y: s.at[1], class: 'trace-label' });
      text.textContent = s.hop === 0 ? (last?.origin?.label || 'This router') : String(s.hop);
      labels.appendChild(text);
      points.push(s.at);
      if (hop?.country) {
        lit.add(hop.country);
        countryEls[hop.country]?.classList.add('active');
      }
    }
    if (s.hop !== 0 && !reduced()) {
      const ripple = svgEl('circle', { cx: s.at[0], cy: s.at[1], class: 'trace-ripple' });
      marks.appendChild(ripple);
      setTimeout(() => ripple.remove(), 2300);
    }
    landed.add(s.hop);
    renderList();
    if (s.located && !manual) zoomTo(fitBox(points));
  }

  function play(): void {
    if (playing) return;
    const s = queue.shift();
    if (!s) return;
    playing = true;
    const g = gen;
    const next = (): void => { if (g !== gen) return; playing = false; play(); };
    if (!s.from || reduced() || typeof requestAnimationFrame !== 'function') {
      if (s.from) lines.appendChild(svgEl('path', { d: arcD(s.from, s.at), class: 'trace-line' }));
      land(s);
      setTimeout(next, reduced() ? 0 : PAUSE_MS);
      return;
    }
    const path = svgEl('path', { d: arcD(s.from, s.at), class: 'trace-line' }) as SVGPathElement;
    lines.appendChild(path);
    const len = path.getTotalLength();
    path.style.strokeDasharray = String(len);
    path.style.strokeDashoffset = String(len);
    const comet = svgEl('circle', { class: 'trace-comet' });
    marks.appendChild(comet);
    const dur = Math.min(LINE_MS_MAX, LINE_MS_MIN + len * LINE_MS_PER);
    const t0 = performance.now();
    const step = (now: number): void => {
      if (g !== gen) return;
      const k = Math.min(1, (now - t0) / dur);
      const e = 1 - Math.pow(1 - k, 2);
      path.style.strokeDashoffset = String(len * (1 - e));
      const p = path.getPointAtLength(len * e);
      comet.setAttribute('cx', p.x.toFixed(2));
      comet.setAttribute('cy', p.y.toFixed(2));
      if (k < 1) { requestAnimationFrame(step); return; }
      path.style.strokeDasharray = '';
      path.style.strokeDashoffset = '';
      comet.remove();
      land(s);
      setTimeout(next, PAUSE_MS);
    };
    requestAnimationFrame(step);
  }

  function renderList(): void {
    if (!last) { list.innerHTML = ''; return; }
    const rows: string[] = [];
    if (last.origin) {
      rows.push('<div class="trace-hop is-home' + (landed.has(0) ? ' is-landed' : '') + '"><span class="trace-hop-n">⌂</span>' +
        '<span class="trace-hop-where">' + esc(last.origin.label || 'This router') + '</span><span></span><span></span></div>');
    }
    for (const h of last.hops) {
      const where = h.timedOut ? '<span class="muted-note">no reply</span>'
        : !h.address ? '<span class="muted-note">probing…</span>'
          : h.city || h.country ? (h.country ? iso2Flag(h.country) + ' ' : '') + esc(h.city || h.country)
            : '<span class="muted-note">private</span>';
      rows.push('<div class="trace-hop' + (landed.has(h.hop) ? ' is-landed' : '') + (h.timedOut ? ' is-timeout' : '') + '">' +
        '<span class="trace-hop-n">' + h.hop + '</span><span class="trace-hop-where">' + where + '</span>' +
        '<span class="trace-hop-ip">' + (h.timedOut ? '?' : esc(h.address)) + '</span>' +
        '<span class="trace-hop-ms">' + fmtMs(h.lastMs) + '</span></div>');
    }
    list.innerHTML = rows.join('');
  }

  return {
    update(r) {
      last = r;
      if (empty) empty.style.display = 'none';
      const steps = planTrace(r);
      for (let i = queued; i < steps.length; i++) queue.push(steps[i]!);
      queued = Math.max(queued, steps.length);
      renderList();
      play();
    },
    clear() {
      gen++;
      queued = 0;
      queue.length = 0;
      playing = false;
      points.length = 0;
      drawn.length = 0;
      landed.clear();
      manual = false;
      userScale = 1;
      zoom.reset();
      if (tip) tip.style.display = 'none';
      for (const cc of lit) countryEls[cc]?.classList.remove('active');
      lit.clear();
      last = null;
      lines.replaceChildren();
      marks.replaceChildren();
      labels.replaceChildren();
      list.innerHTML = '';
      if (empty) empty.style.display = '';
      setView(WORLD);
    },
  };
}
