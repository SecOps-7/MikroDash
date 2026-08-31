/**
 * The fleet map's SVG half.
 *
 * ── WHY IT IS A SEPARATE MODULE FROM `routers.ts` ──────────────────────────
 *
 * The map splits cleanly in two, and the split is by what can be CHECKED. The
 * arithmetic — `project`, `layout`, `popHtml`, `groupPopHtml`, `renderTray`,
 * `clampTranslate`, `fitToMarkers` — produces strings and numbers, so it was
 * ported first and is gated by `tools/routers-grid-check.js` against the live
 * functions. What is here builds SVG elements, reads `getBoundingClientRect`
 * and listens for pointer events; none of that survives a headless harness
 * faithfully, so it is verified in a browser instead.
 *
 * The dependency runs ONE WAY: this module imports `routers.ts`, and that one
 * reaches back through `onMapApply` and `onAfterTransform` rather than importing
 * this. Two modules that import each other would be a cycle, and the cycle would
 * be silent until the bundler picked an order.
 *
 * ── THE MARKERS ARE MUTATED, NEVER REBUILT ─────────────────────────────────
 *
 * `routers:stats` arrives every two seconds. Rebuilding the SVG each time would
 * drop the hover, fight the pointer mid-drag, and restart every ripple
 * animation in unison. So each group's `<g>` is created once, kept in `els`, and
 * only its attributes change; groups that leave the payload are removed
 * explicitly.
 *
 * ── EVERYTHING IS DIVIDED BY `scale` ───────────────────────────────────────
 *
 * Marker radii, stroke widths, label sizes and the collision boxes are all in
 * map units divided by the zoom, so they hold a constant SIZE ON SCREEN. That is
 * what makes zooming separate a cluster instead of magnifying it. The one
 * deliberate exception in the live app is the accuracy ring, which represents
 * real distance and is not drawn here at all.
 */

import { el } from '../dom';
import {
  layout, groupPopHtml, renderTray, clampTranslate, fitToMarkers,
  mapViewState, setMapView, applyTransform, onAfterTransform, onMapApply, lastRows,
  type RouterStatsRow, type MapGroup,
} from './routers';

const NS = 'http://www.w3.org/2000/svg';
const MIN_SCALE = 1, MAX_SCALE = 8;
const BASE_R = 5;

interface MarkerEls {
  g: SVGGElement;
  ripple: SVGCircleElement;
  dot: SVGCircleElement;
  count: SVGTextElement;
}

let ready = false;
let pending: RouterStatsRow[] | null = null;
let markerLayer: SVGGElement | null = null;
let badgeLayer: SVGGElement | null = null;
const els: Record<string, MarkerEls> = {};
let lastGroups: Record<string, MapGroup> = {};
let lastPlaced: MapGroup[] = [];
let hovered: string | null = null;
let pinned: string | null = null;
let moved = false;

const svgEl = (): SVGSVGElement | null => el('routersMap') as unknown as SVGSVGElement | null;

/** `document.createElementNS`, with attributes. The live `el(name, attrs)`. */
function svg<T extends SVGElement>(name: string, attrs: Record<string, string | number>): T {
  const e = document.createElementNS(NS, name) as T;
  for (const k of Object.keys(attrs)) e.setAttribute(k, String(attrs[k]));
  return e;
}

// ── the backdrop, built once ────────────────────────────────────────────────

/**
 * `worldmap:ready` AND an immediate call, both.
 *
 * The atlas is fetched by another module and can land either side of this one —
 * the live app registers the listener and then calls `init()` straight away for
 * exactly that reason, and `dc-worldMap` uses the same pattern. Whichever
 * happens second finds `ready` already true and returns.
 */
function init(): void {
  const s = svgEl();
  const atlas = (globalThis as unknown as { _worldMapPathDs?: Record<string, string> })._worldMapPathDs;
  if (ready || !s || !atlas) return;

  const countryLayer = svg<SVGGElement>('g', {});
  // A FRAGMENT, so ~250 country paths cause one reflow rather than 250.
  const frag = document.createDocumentFragment();
  for (const cc of Object.keys(atlas)) {
    frag.appendChild(svg('path', { d: atlas[cc] as string, class: 'map-country' }));
  }
  countryLayer.appendChild(frag);
  markerLayer = svg<SVGGElement>('g', {});
  badgeLayer = svg<SVGGElement>('g', {});
  s.appendChild(countryLayer);
  s.appendChild(markerLayer);
  s.appendChild(badgeLayer);
  ready = true;
  // A payload that arrived before the atlas is drawn now rather than dropped.
  if (pending) {
    const p = pending;
    pending = null;
    apply(p);
  }
}

// ── zoom and pan ────────────────────────────────────────────────────────────

/**
 * Zoom about a point, keeping what is under the cursor fixed.
 *
 * The early return on `s === scale` is not an optimisation: at either end of the
 * range every further wheel tick would otherwise recompute the translation from
 * an unchanged scale and drift the view sideways.
 */
function setScale(next: number, cx?: number, cy?: number): void {
  const s = svgEl();
  if (!s) return;
  const v = mapViewState();
  const want = Math.min(MAX_SCALE, Math.max(MIN_SCALE, next));
  if (want === v.scale) return;
  const rect = s.getBoundingClientRect();
  const ox = cx === undefined ? rect.width / 2 : cx - rect.left;
  const oy = cy === undefined ? rect.height / 2 : cy - rect.top;
  let tx = ox - ((ox - v.tx) / v.scale) * want;
  let ty = oy - ((oy - v.ty) / v.scale) * want;
  const c = clampTranslate(want, tx, ty);
  tx = c[0]; ty = c[1];
  setMapView(want, tx, ty);
  applyTransform();
}

// ── Auto Frame ──────────────────────────────────────────────────────────────

const AF_KEY = 'mikrodash_map_autoframe';
let autoFrame = false;

/**
 * `persist` separates a deliberate press of the button from an implicit release
 * caused by panning. Only the former is remembered — the live comment: otherwise
 * "one accidental drag turns a default-on feature off forever, and the next time
 * you open the map it silently no longer frames anything."
 */
function setAutoFrame(on: boolean, persist: boolean): void {
  autoFrame = !!on;
  if (persist) {
    try {
      localStorage.setItem(AF_KEY, autoFrame ? '1' : '0');
    } catch { /* a browser refusing storage still gets a working button */ }
  }
  const b = el('rtrMapAutoFrame');
  if (b) {
    b.classList.toggle('is-on', autoFrame);
    b.setAttribute('aria-pressed', autoFrame ? 'true' : 'false');
  }
  if (autoFrame) fitToMarkers(lastPlaced);
}

/**
 * Panning or zooming by hand switches Auto Frame off.
 *
 * The live reason: it is "an explicit statement about what you want to look at",
 * and leaving AF on would snap the view back on the next two-second tick, "which
 * reads as the map fighting you".
 */
function userMovedView(): void {
  if (autoFrame) setAutoFrame(false, false);
}

// ── the popover ─────────────────────────────────────────────────────────────

function showPop(key: string, isPin: boolean): void {
  const g = lastGroups[key];
  const pop = el('rtrMapPop');
  if (!g || !pop) return;
  pop.innerHTML = groupPopHtml(g);
  pop.hidden = false;
  pop.classList.toggle('is-pinned', !!isPin);
  positionPop();
}

/** A PINNED popover ignores hide entirely — that is what pinning means. */
function hidePop(): void {
  const pop = el('rtrMapPop');
  if (!pop || pinned) return;
  pop.hidden = true;
}

/**
 * Put the popover beside its marker, inside the card.
 *
 * Measured from the MARKER's client rect rather than from map coordinates,
 * because the marker has been through the CSS transform and the map coordinates
 * have not — computing it from `g.x`/`g.y` would place the popover correctly
 * only at scale 1.
 */
function positionPop(): void {
  const key = pinned || hovered;
  const pop = el('rtrMapPop');
  const viewport = el('rtrMapViewport');
  if (!pop || pop.hidden || !key || !els[key] || !viewport) return;
  const mr = els[key].dot.getBoundingClientRect();
  const vr = viewport.getBoundingClientRect();
  let left = mr.left - vr.left + mr.width / 2 + 12;
  let top = mr.top - vr.top - 8;
  // Kept inside the card rather than spilling off the right edge.
  left = Math.min(left, vr.width - pop.offsetWidth - 8);
  top = Math.min(Math.max(top, 4), vr.height - pop.offsetHeight - 4);
  pop.style.left = Math.max(4, left) + 'px';
  pop.style.top = Math.max(4, top) + 'px';
}

// ── the data path ───────────────────────────────────────────────────────────

interface PlaceLabel { text: string; x: number; y: number; hw: number; hh: number; ly: number }

/**
 * Drop place labels that would collide.
 *
 * COMPARES THE BOXES, not the anchors. The live comment says why: at world zoom
 * a European fleet writes several names into the same centimetre, and
 * "Berlin, BE, DE" is many times wider than the gap between two capitals — so
 * comparing anchor points would keep them all and none would be readable.
 *
 * 0.55em per character is the live app's advance-width estimate for a monospace
 * face, and being generous errs toward HIDING, which is the safe way to be wrong.
 */
function keepLabels(labelled: { text: string; x: number; y: number }[], scale: number): PlaceLabel[] {
  const fsz = 8 / scale;
  const kept: PlaceLabel[] = [];
  for (const L of labelled) {
    const box: PlaceLabel = {
      text: L.text, x: L.x, y: L.y,
      hw: (L.text.length * fsz * 0.55) / 2,
      hh: fsz * 0.75,
      ly: L.y + 11 / scale,
    };
    let clash = false;
    for (const k of kept) {
      if (Math.abs(k.x - box.x) < (k.hw + box.hw) && Math.abs(k.ly - box.ly) < (k.hh + box.hh)) {
        clash = true;
        break;
      }
    }
    if (!clash) kept.push(box);
  }
  return kept;
}

function apply(rows: RouterStatsRow[] | null): void {
  // BEFORE THE ATLAS: hold the payload rather than dropping it. Without this the
  // map is empty until the next two-second tick on a slow atlas fetch.
  if (!ready) {
    pending = rows || [];
    return;
  }
  const list = rows || [];
  const scale = mapViewState().scale;

  const located = list.filter((r) => r.geo && r.geo.lat != null && r.geo.lon != null);
  const groups = layout(located);
  lastGroups = {};
  for (const g of groups) lastGroups[g.key] = g;
  const seen: Record<string, true> = {};

  // ONE LABEL PER PLACE, keyed on the group: three routers at one address write
  // their town once. Writing it three times is exactly the noise the general
  // city layer was removed for.
  const labelled = groups
    .map((g) => ({ text: (g.routers[0]?.geo && g.routers[0].geo!.label) || '', x: g.x, y: g.y }))
    .filter((L) => !!L.text);
  const kept = keepLabels(labelled, scale);

  for (const g of groups) {
    const key = g.key;
    seen[key] = true;
    let e = els[key];
    if (!e) {
      e = els[key] = {
        g: svg<SVGGElement>('g', { class: 'rtrmap-marker' }),
        ripple: svg<SVGCircleElement>('circle', { class: 'rtrmap-ripple' }),
        dot: svg<SVGCircleElement>('circle', {}),
        count: svg<SVGTextElement>('text', { class: 'rtrmap-clustern' }),
      };
      // STAGGERED FROM THE KEY, so it is stable across re-renders instead of
      // jumping every two seconds. The live reason for staggering at all: "a
      // rack of routers all pulsing on the same beat reads as a warning rather
      // than as a heartbeat."
      let phase = 0;
      for (let ci = 0; ci < key.length; ci++) phase = (phase * 31 + key.charCodeAt(ci)) % 2600;
      e.ripple.style.animationDelay = '-' + phase + 'ms';
      e.g.appendChild(e.ripple);
      e.g.appendChild(e.dot);
      e.g.appendChild(e.count);
      markerLayer!.appendChild(e.g);
      e.g.addEventListener('mouseenter', () => { hovered = key; showPop(key, false); });
      e.g.addEventListener('mouseleave', () => { hovered = null; hidePop(); });
      e.g.addEventListener('click', (ev) => {
        ev.stopPropagation();
        // A DRAG THAT ENDED ON A MARKER IS NOT A CLICK. Without this, panning
        // the map pins whatever marker the pointer happened to be over.
        if (moved) return;
        pinned = pinned === key ? null : key;
        if (pinned) {
          showPop(key, true);
        } else {
          const pop = el('rtrMapPop');
          if (pop) pop.hidden = true;
        }
      });
    }

    // THE WORST STATE IN THE GROUP. A site with one router down is a site with a
    // problem, and a green dot hiding a red one would defeat the only thing the
    // map is really for.
    const anyDown = g.routers.some((r) => !r.connected);
    const colour = anyDown ? 'var(--accent-red,#f87171)' : 'var(--accent-green,#2fb344)';
    const n = g.routers.length;
    // SQUARE-ROOT growth, because area is what the eye judges, and capped so one
    // big site cannot swallow the map.
    const rad = Math.min(16, BASE_R + 3.2 * Math.sqrt(Math.max(0, n - 1))) / scale;

    e.dot.setAttribute('cx', String(g.x));
    e.dot.setAttribute('cy', String(g.y));
    e.dot.setAttribute('r', String(rad));
    e.dot.setAttribute('stroke-width', String(1.5 / scale));
    e.dot.setAttribute('fill', colour);
    e.dot.setAttribute('stroke', 'rgba(0,0,0,.45)');
    e.ripple.setAttribute('cx', String(g.x));
    e.ripple.setAttribute('cy', String(g.y));
    e.ripple.setAttribute('r', String(rad));
    e.ripple.setAttribute('fill', colour);

    if (n > 1) {
      e.count.setAttribute('x', String(g.x));
      e.count.setAttribute('y', String(g.y));
      // The glyphs shrink as the number lengthens, so "128" still fits the
      // circle it is written in.
      const digits = String(n).length;
      e.count.setAttribute('font-size',
        String(rad * (digits === 1 ? 1.15 : digits === 2 ? 0.95 : 0.72)));
      e.count.textContent = String(n);
      e.count.style.display = '';
    } else {
      e.count.style.display = 'none';
    }
  }

  // Groups that left the payload — filtered out by the search, or no longer
  // visible to this session. A pin on one of them goes with it.
  for (const key of Object.keys(els)) {
    if (seen[key]) continue;
    const gone = els[key];
    if (!gone) continue;
    gone.g.remove();
    delete els[key];
    if (pinned === key) {
      pinned = null;
      const pop = el('rtrMapPop');
      if (pop) pop.hidden = true;
    }
  }

  // Place names, sized against the zoom: in map units they would grow with the
  // transform until a town name spanned a continent.
  if (badgeLayer) {
    badgeLayer.innerHTML = '';
    for (const L of kept) {
      const t = svg<SVGTextElement>('text', {
        class: 'rtrmap-place', x: L.x, y: L.ly,
        'font-size': 8 / scale, 'stroke-width': 2.5 / scale,
      });
      t.textContent = L.text;
      badgeLayer.appendChild(t);
    }
  }

  lastPlaced = groups;
  // While Auto Frame is on this runs on EVERY payload, so adding a router or
  // narrowing the search re-frames to what is actually shown.
  if (autoFrame && groups.length) fitToMarkers(groups);

  // A pinned popover follows the data rather than the other way round: its
  // contents refresh so CPU and uptime stay live, but it never closes itself.
  if (pinned && lastGroups[pinned]) showPop(pinned, true);

  renderTray(list.filter((r) => !r.geo));
}

/**
 * Re-apply the last payload after a zoom.
 *
 * The live `resize()`. Marker radius depends on how many routers are in the
 * group, so the DATA PATH owns it — re-deriving it here would need the group
 * sizes and the two could disagree. Re-applying is what keeps every marker a
 * constant size on screen as the zoom changes.
 */
function resize(): void {
  // ── `lastRows()`, NOT `window._lastRtrRows` ──────────────────────────────
  //
  // The live app reads the payload back off `window._lastRtrRows`, which its own
  // routers module publishes. THIS PORT NEVER PUBLISHES IT — the rows live in a
  // module-private `lastRtrRows` — so the window read returned undefined and
  // `resize()` did nothing: every zoom would have left the markers at their
  // previous screen size, which is the one thing the constant-size arithmetic
  // exists to prevent.
  //
  // `tools/announcement-audit.js` is what said so, by flagging a `window.` read
  // with no matching writer anywhere in the port. It was not visible in a
  // browser either, because markers at a slightly wrong radius still look like
  // markers.
  const rows = lastRows();
  if (rows.length) apply(rows);
}

// ── mount ───────────────────────────────────────────────────────────────────

let mounted = false;

export function initRoutersMap(): void {
  const s = svgEl();
  if (!s || mounted) return;
  mounted = true;

  try {
    autoFrame = localStorage.getItem(AF_KEY) === '1';
  } catch { /* storage blocked: Auto Frame stays off */ }

  document.addEventListener('worldmap:ready', init);
  init();

  onMapApply(apply);
  onAfterTransform(() => { resize(); positionPop(); });

  // `passive: false` because this PREVENTS the page scrolling — a wheel over the
  // map must zoom it, and a passive listener may not call preventDefault.
  s.addEventListener('wheel', (e) => {
    e.preventDefault();
    userMovedView();
    setScale(mapViewState().scale * ((e as WheelEvent).deltaY < 0 ? 1.2 : 1 / 1.2),
      (e as WheelEvent).clientX, (e as WheelEvent).clientY);
  }, { passive: false });

  let dragging = false, sx = 0, sy = 0, stx = 0, sty = 0;
  s.addEventListener('pointerdown', (e) => {
    const pe = e as PointerEvent;
    dragging = true;
    moved = false;
    sx = pe.clientX; sy = pe.clientY;
    const v = mapViewState();
    stx = v.tx; sty = v.ty;
    s.classList.add('is-dragging');
    s.setPointerCapture(pe.pointerId);
  });
  s.addEventListener('pointermove', (e) => {
    if (!dragging) return;
    const pe = e as PointerEvent;
    const dx = pe.clientX - sx, dy = pe.clientY - sy;
    // A THREE-PIXEL DEADZONE. Below it the gesture is still a click, which is
    // what stops a shaky press from both pinning a marker and cancelling Auto
    // Frame.
    if (Math.abs(dx) > 3 || Math.abs(dy) > 3) {
      if (!moved) userMovedView();
      moved = true;
    }
    const c = clampTranslate(mapViewState().scale, stx + dx, sty + dy);
    setMapView(mapViewState().scale, c[0], c[1]);
    applyTransform();
  });
  const endDrag = (e: Event): void => {
    if (!dragging) return;
    dragging = false;
    s.classList.remove('is-dragging');
    try {
      s.releasePointerCapture((e as PointerEvent).pointerId);
    } catch { /* the capture may already be gone */ }
  };
  s.addEventListener('pointerup', endDrag);
  s.addEventListener('pointercancel', endDrag);

  const viewport = el('rtrMapViewport');
  if (viewport) {
    viewport.addEventListener('click', (e) => {
      const t = e.target as HTMLElement | null;
      const btn = t && t.closest ? t.closest('[data-map-zoom]') : null;
      if (!btn) return;
      const what = btn.getAttribute('data-map-zoom');
      const v = mapViewState();
      if (what === 'in') { userMovedView(); setScale(v.scale * 1.4); }
      if (what === 'out') { userMovedView(); setScale(v.scale / 1.4); }
      // RESET IS ZOOM-OUT, NOT FRAME. It has to release Auto Frame as well, or
      // the next payload re-frames immediately and the button looks broken —
      // which is exactly how it looked when reset was wired to "frame all
      // routers" and the view was already framed.
      if (what === 'reset') {
        setAutoFrame(false, false);
        setMapView(1, 0, 0);
        applyTransform();
      }
      if (what === 'autoframe') setAutoFrame(!autoFrame, true);
    });
  }

  // The markup ships in the off state, so a stored "on" has to be reflected on
  // load — otherwise the button reads as off while the map is still framing.
  if (autoFrame) setAutoFrame(true, false);

  const pop = el('rtrMapPop');
  if (pop) {
    pop.addEventListener('click', (e) => {
      const t = e.target as HTMLElement | null;
      const b = t && t.closest ? t.closest('[data-open-router]') : null;
      if (!b) return;
      openRouter(b.getAttribute('data-open-router') || '');
    });
  }

  const tray = el('rtrMapTray');
  if (tray) {
    tray.addEventListener('click', (e) => {
      const t = e.target as HTMLElement | null;
      const p = t && t.closest ? t.closest('[data-open-router]') : null;
      if (p) openRouter(p.getAttribute('data-open-router') || '');
    });
  }

  document.addEventListener('keydown', (e) => {
    if ((e as KeyboardEvent).key === 'Escape' && pinned) {
      pinned = null;
      hovered = null;
      const p = el('rtrMapPop');
      if (p) p.hidden = true;
    }
  });

  // Clicking empty map releases a pinned popover — but not at the end of a drag.
  s.addEventListener('click', () => {
    if (moved) return;
    if (pinned) {
      pinned = null;
      const p = el('rtrMapPop');
      if (p) p.hidden = true;
    }
  });
}

/**
 * Open the router modal from a popover or a tray pill.
 *
 * `window._rtrOpenModal` is the live app's own hand-off, published by the
 * routers page so the map's inner IIFE can reach a dialog it does not own. The
 * port keeps the same name because it is the documented producer/consumer pair
 * that `announcement-audit` checks.
 */
function openRouter(id: string): void {
  const open = (globalThis as unknown as { _rtrOpenModal?: (id: string) => void })._rtrOpenModal;
  if (id && open) open(id);
}

/** Exported for the label-collision gate below; the rest is browser-verified. */
export { keepLabels, apply as applyMapForTest };
