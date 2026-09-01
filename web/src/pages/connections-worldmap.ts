// The Connections page's world map.
//
// A KEYED DIFF OVER LIVE SVG NODES, not a redraw. The country paths are built
// once from the atlas and then only ever have classes toggled; arcs are recreated
// only when their geometry actually changes, because recreating one restarts its
// comet animation and a map where every dot jumps back to the start on each poll
// looks broken rather than live.
//
// ── WHY THE ATLAS IS FETCHED RATHER THAN BUNDLED ────────────────────────────
//
// `/vendor/world-atlas/countries-110m.json` is ~100 KB of TopoJSON that changes
// when borders do, which is to say almost never. Bundling it would put it in
// every page load of an app whose other pages never show a map. It is served by
// the Node app today and proxied, which is the strangler-fig arrangement doing
// its job.

import { CC_NAMES, NUM_TO_ISO2, centroidOf, coordsToD, iso2Flag, makeArcD } from './connections-map';
import { esc } from '../dom';

const NS = 'http://www.w3.org/2000/svg';

const MAP_URL = '/vendor/world-atlas/countries-110m.json';

/** A decoded country: its code, its outline, and where to put its label. */
export interface MapCountry {
  cc: string;
  d: string;
  centroid: [number, number] | null;
  /** The decoded rings. Carried so the decode can be checked against the real
   *  topojson-client without comparing through the path builder, which both
   *  sides share and which would therefore hide a shared bug. */
  rings: number[][][];
}

/**
 * TopoJSON, decoded here rather than by the vendored client.
 *
 * The live app loads `/vendor/topojson-client.min.js` for one function. That
 * function is an arc-delta decode — quantised integer deltas accumulated along
 * each arc, then assembled into rings — and it is thirty lines. Reimplementing
 * it removes a script tag from the page and a dependency from the port, and it
 * is verified against the live output rather than trusted.
 */
interface Topology {
  arcs: number[][][];
  transform?: { scale: [number, number]; translate: [number, number] };
  objects: Record<string, TopoObject>;
}

interface TopoObject {
  type: string;
  geometries?: TopoGeometry[];
}

interface TopoGeometry {
  type: string;
  id?: string | number;
  arcs?: unknown;
}

/** One arc, dequantised into absolute coordinates. */
function decodeArc(topo: Topology, i: number): number[][] {
  const arc = topo.arcs[i]!;
  const t = topo.transform;
  const out: number[][] = [];
  let x = 0, y = 0;
  for (const p of arc) {
    if (t) {
      // Quantised: each point is a DELTA on the previous one, in grid units.
      x += p[0]!;
      y += p[1]!;
      out.push([x * t.scale[0] + t.translate[0], y * t.scale[1] + t.translate[1]]);
    } else {
      out.push([p[0]!, p[1]!]);
    }
  }
  return out;
}

/**
 * A ring from its arc indexes.
 *
 * A NEGATIVE INDEX MEANS THE ARC RUN BACKWARDS, encoded as `~i` — that is how
 * TopoJSON shares one border between two countries without storing it twice,
 * and getting it wrong turns a shared border inside out.
 */
function ringFrom(topo: Topology, arcs: number[]): number[][] {
  const out: number[][] = [];
  for (const idx of arcs) {
    const points = idx < 0 ? decodeArc(topo, ~idx).slice().reverse() : decodeArc(topo, idx);
    // The first point of each arc repeats the last of the previous one.
    out.push(...(out.length ? points.slice(1) : points));
  }
  return out;
}

/** Every country in the atlas, as a path and a centroid. */
export function decodeCountries(topo: Topology, objectName = 'countries'): MapCountry[] {
  const obj = topo.objects[objectName];
  if (!obj || !obj.geometries) return [];
  const out: MapCountry[] = [];
  for (const g of obj.geometries) {
    const numId = parseInt(String(g.id ?? ''), 10);
    const cc = NUM_TO_ISO2[numId] || ('N' + String(g.id ?? ''));
    let rings: number[][][] = [];
    let d = '';
    if (g.type === 'Polygon') {
      rings = (g.arcs as number[][]).map((r) => ringFrom(topo, r));
      d = coordsToD(rings);
    } else if (g.type === 'MultiPolygon') {
      for (const poly of g.arcs as number[][][]) {
        const polyRings = poly.map((r) => ringFrom(topo, r));
        rings = rings.concat(polyRings);
        d += coordsToD(polyRings);
      }
    }
    if (!d) continue;
    out.push({ cc, d, rings, centroid: centroidOf(cc, rings) });
  }
  return out;
}

function svgEl(tag: string, attrs?: Record<string, string | number>): SVGElement {
  const e = document.createElementNS(NS, tag) as SVGElement;
  if (attrs) for (const k of Object.keys(attrs)) e.setAttribute(k, String(attrs[k]));
  return e;
}

export interface WorldMap {
  /** Colour the countries by connection count. */
  highlight: (counts: Record<string, number>) => void;
  /** Draw an arc from this router's country to each of these. */
  arcs: (counts: Record<string, number>, localCC: string) => void;
  /** Put the count above each country. */
  labels: (counts: Record<string, number>) => void;
  /** Pick one country out and dim the rest, or clear with null. */
  select: (cc: string | null, counts: Record<string, number>, localCC: string) => void;
  centroids: Record<string, [number, number]>;
  /** Whether a country path was drawn for this code. Used by the tooltip. */
  hasCountry: (cc: string) => boolean;
  /**
   * Flash the countries that just gained connections.
   *
   * The stylesheet has carried `.map-country.pulse{animation:mapPulse ...}`
   * since the CSS was extracted, and until 2026-08-29 NOTHING in this port ever
   * added the class — the animation existed and could not fire. Reported by the
   * operator: "the connections map animation no longer pulses the country the
   * comet lands on like it does on the live app."
   */
  pulse: (ccs: string[]) => void;
  ready: boolean;
}

/** What the tooltip says about one country, supplied by the page. */
export interface MapTipInfo {
  count: number;
  city: string;
  proto: Record<string, number>;
}

/**
 * Build the map into an <svg>, and return the handles that update it.
 *
 * `onReady` fires once the atlas has been fetched and decoded — the page draws
 * whatever it already has at that point, because the payload usually arrives
 * first and a map that waited for the next poll would sit blank for one.
 */
export function createWorldMap(svg: SVGElement, onReady: () => void): WorldMap {
  const countryLayer = svgEl('g');
  const arcLayer = svgEl('g');
  const labelLayer = svgEl('g');
  svg.appendChild(countryLayer);
  svg.appendChild(arcLayer);
  svg.appendChild(labelLayer);

  const pathEls: Record<string, SVGElement> = {};
  const arcEls: Record<string, SVGElement> = {};
  const labelEls: Record<string, SVGElement> = {};
  const centroids: Record<string, [number, number]> = {};

  const map: WorldMap = {
    centroids,
    hasCountry: (cc) => !!pathEls[cc],
    ready: false,
    highlight(counts) {
      // "hot" is at least half the busiest country, "active" is anything else
      // with traffic. A fixed threshold would leave every country pale on a
      // quiet network and every one hot on a busy one.
      const max = Math.max(0, ...Object.values(counts));
      for (const cc of Object.keys(pathEls)) {
        const el = pathEls[cc]!;
        const n = counts[cc] || 0;
        el.classList.remove('active', 'hot');
        if (n > 0) el.classList.add(n >= max * 0.5 ? 'hot' : 'active');
      }
    },
    pulse(ccs) {
      for (const cc of ccs) {
        const el = pathEls[cc];
        if (!el) continue;
        // Removed first so a country that pulses on consecutive payloads
        // restarts the animation instead of being ignored as already-classed.
        el.classList.remove('pulse');
        // THE DOUBLE rAF IS THE LIVE APP'S, and its comment says why: it "lets
        // browser commit style removal before re-adding, avoiding a forced
        // synchronous layout reflow". A single frame re-adds the class before
        // the removal is committed and the animation never restarts.
        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            el.classList.add('pulse');
            // 750ms, matching the live timeout. The CSS animation is .7s and
            // `forwards`, so the class must be taken off or the final frame
            // sticks.
            setTimeout(() => el.classList.remove('pulse'), 750);
          });
        });
      }
    },
    arcs(counts, localCC) {
      const src = centroids[localCC];
      for (const cc of Object.keys(arcEls)) {
        if (!counts[cc]) {
          arcEls[cc]!.parentNode?.removeChild(arcEls[cc]!);
          delete arcEls[cc];
        }
      }
      if (!src) return;
      const max = Math.max(0, ...Object.values(counts));
      for (const cc of Object.keys(counts)) {
        if (cc === localCC) continue;
        const dst = centroids[cc];
        if (!dst) continue;
        const hot = counts[cc]! >= max * 0.5;
        const d = makeArcD(src[0], src[1], dst[0], dst[1]);

        // ONLY REBUILD WHEN THE GEOMETRY MOVED. Recreating an arc restarts its
        // comet, and a map where every dot jumps back to the start on each poll
        // reads as broken rather than live.
        const existing = arcEls[cc];
        if (existing && existing.querySelector('path')?.getAttribute('d') === d) continue;
        existing?.parentNode?.removeChild(existing);

        const g = svgEl('g');
        g.appendChild(svgEl('path', { d, class: 'map-arc' + (hot ? ' hot' : '') }));
        // The comet's duration is JITTERED and its start is negative, so arcs
        // drawn in the same frame do not pulse in lockstep — which would read as
        // one animation rather than many links.
        const base = hot ? 1.4 : 2.2;
        const dur = Math.max(0.8, base + (Math.random() * 0.6 - 0.3)).toFixed(2) + 's';
        const circle = svgEl('circle', { r: hot ? 3 : 2, class: 'map-comet' + (hot ? ' hot' : '') });
        circle.appendChild(svgEl('animateMotion', {
          dur, repeatCount: 'indefinite',
          begin: '-' + (Math.random() * base).toFixed(2) + 's', path: d,
        }));
        g.appendChild(circle);
        arcLayer.appendChild(g);
        arcEls[cc] = g;
      }
    },
    labels(counts) {
      // A stale label is EMPTIED rather than removed: the element is cheap, and
      // keeping it means a country that comes back does not have to be rebuilt.
      for (const cc of Object.keys(labelEls)) {
        if (!counts[cc]) labelEls[cc]!.textContent = '';
      }
      for (const cc of Object.keys(counts)) {
        const c = centroids[cc];
        if (!c) continue;
        let el = labelEls[cc];
        if (!el) {
          el = svgEl('text', { class: 'map-label' });
          labelLayer.appendChild(el);
          labelEls[cc] = el;
        }
        el.setAttribute('x', c[0].toFixed(1));
        el.setAttribute('y', (c[1] - 6).toFixed(1));
        el.textContent = String(counts[cc]);
      }
    },
    select(cc, counts, localCC) {
      if (!cc) {
        map.highlight(counts);
        map.arcs(counts, localCC);
        return;
      }
      for (const c of Object.keys(pathEls)) {
        pathEls[c]!.classList.remove('active', 'hot');
        if (c === cc) pathEls[c]!.classList.add('hot');
      }
      map.arcs({ [cc]: counts[cc] || 0 }, localCC);
    },
  };

  fetch(MAP_URL)
    .then((r) => (r.ok ? r.json() : null))
    .then((world: Topology | null) => {
      if (!world) return;
      const frag = document.createDocumentFragment();
      for (const c of decodeCountries(world)) {
        const path = svgEl('path', { d: c.d, class: 'map-country', 'data-cc': c.cc });
        pathEls[c.cc] = path;
        if (c.centroid) centroids[c.cc] = c.centroid;
        frag.appendChild(path);
      }
      countryLayer.appendChild(frag);
      map.ready = true;

      // ── PUBLISH THE DECODED ATLAS, AND SAY SO ────────────────────────────
      //
      // The DASHBOARD's Connections Map card reuses these paths and centroids
      // rather than decoding the atlas a second time, and `worldmap:ready` is
      // how it learns they exist (`dashboard.ts` listens for it, and falls back
      // to checking `_worldMapPathDs` for the case where it mounts late).
      //
      // Neither was published here, so the listener never fired and the fallback
      // was never true: **the dashboard's map card could not initialise at all.**
      // Found by inventorying the CustomEvent vocabulary — the port listened for
      // an announcement it never made, which is the browser twin of the
      // `router:active` bug `event-audit` found in the socket vocabulary.
      //
      // The live app does this in the same place, immediately before its own
      // ready callback (`../MikroDash/public/app.js:4412`).
      const w = window as unknown as {
        _worldMapPathDs?: Record<string, string>;
        _worldMapCentroids?: Record<string, [number, number]>;
      };
      const ds: Record<string, string> = {};
      for (const cc of Object.keys(pathEls)) {
        ds[cc] = pathEls[cc]!.getAttribute('d') || '';
      }
      w._worldMapPathDs = ds;
      w._worldMapCentroids = centroids;
      document.dispatchEvent(new CustomEvent('worldmap:ready'));

      onReady();
    })
    .catch(() => { /* no atlas: the lists still work, the map stays empty */ });

  return map;
}

/**
 * Zoom and pan, as a CSS transform on the whole <svg>.
 *
 * Not an SVG viewBox: the transform is composited by the browser, so a drag
 * stays smooth on a map with two hundred country paths, where re-laying out a
 * viewBox on every pointer move would not.
 */
export function attachMapZoom(
  wrap: HTMLElement, svg: SVGElement,
): { reset: () => void; retarget: (next: HTMLElement) => void } {
  let scale = 1, tx = 0, ty = 0;
  const MIN = 1, MAX = 8;

  const clamp = (s: number, x: number, y: number): [number, number] => {
    // Panning is bounded to the map's own edges: at scale 1 there is nowhere to
    // go, and past that only as far as the overflow allows. Otherwise a drag
    // can lose the map off the side of its card entirely.
    const w = svg.clientWidth || 1000, h = svg.clientHeight || 500;
    const maxX = (s - 1) * w, maxY = (s - 1) * h;
    return [Math.max(-maxX, Math.min(0, x)), Math.max(-maxY, Math.min(0, y))];
  };

  // THE TARGET IS MUTABLE because fullscreen moves the SVG out of this wrapper
  // and into a body-level overlay. The handlers have to follow it: left on the
  // wrapper, panning would answer to an element the map is no longer inside.
  let target: HTMLElement = wrap;

  const apply = (): void => {
    [tx, ty] = clamp(scale, tx, ty);
    svg.style.transform = 'translate(' + tx + 'px,' + ty + 'px) scale(' + scale + ')';
    svg.style.transformOrigin = '0 0';
    target.style.cursor = scale > 1 ? 'grab' : 'default';
  };

  const zoomAt = (factor: number, cx: number, cy: number): void => {
    const next = Math.max(MIN, Math.min(MAX, scale * factor));
    if (next === scale) return;
    // Toward the cursor, so the point under the pointer stays under it.
    tx = cx - (cx - tx) * (next / scale);
    ty = cy - (cy - ty) * (next / scale);
    scale = next;
    apply();
  };

  let dragging = false, sx = 0, sy = 0, dtx = 0, dty = 0;
  const onWheel = (e: WheelEvent): void => {
    e.preventDefault();
    const rect = target.getBoundingClientRect();
    zoomAt(e.deltaY < 0 ? 1.15 : 1 / 1.15, e.clientX - rect.left, e.clientY - rect.top);
  };
  const onDown = (e: PointerEvent): void => {
    // ── A PRESS ON THE MAP'S OWN BUTTONS IS NOT THE START OF A PAN ────────
    //
    // The zoom controls sit INSIDE the wrapper this listens on, so without this
    // pressing Zoom In while zoomed began a drag: the click still worked, and
    // any mouse movement before releasing then panned the map. The live app
    // refuses the same way and says why — "don't swallow their events"
    // (`../MikroDash/public/app.js:4467`).
    //
    // Found on 2026-08-25 by the map-zoom check, which had to give its
    // event target a `tagName` and a `closest` before the live slice would run
    // at all — the guard was invisible until the shim was real enough to reach it.
    const t = e.target as HTMLElement | null;
    if (t && (t.tagName === 'BUTTON' || (t.closest && t.closest('button')))) return;
    if (scale <= 1) return;
    dragging = true;
    sx = e.clientX; sy = e.clientY; dtx = tx; dty = ty;
    target.style.cursor = 'grabbing';
  };
  const onMove = (e: PointerEvent): void => {
    if (!dragging) return;
    tx = dtx + (e.clientX - sx);
    ty = dty + (e.clientY - sy);
    apply();
  };
  const end = (): void => {
    dragging = false;
    target.style.cursor = scale > 1 ? 'grab' : 'default';
  };

  // Held as a list so the same references can be REMOVED. An inline arrow could
  // be added and never taken off again, which on a retarget would leave the old
  // element still panning a map it no longer contains.
  const bindings: Array<[string, EventListener, AddEventListenerOptions | undefined]> = [
    ['wheel', onWheel as EventListener, { passive: false }],
    ['pointerdown', onDown as EventListener, undefined],
    ['pointermove', onMove as EventListener, undefined],
    ['pointerup', end as EventListener, undefined],
    ['pointercancel', end as EventListener, undefined],
  ];
  const bind = (t: HTMLElement): void => {
    for (const [n, f, o] of bindings) t.addEventListener(n, f, o);
  };
  const unbind = (t: HTMLElement): void => {
    for (const [n, f, o] of bindings) t.removeEventListener(n, f, o);
  };
  bind(wrap);

  return {
    reset() {
      scale = 1; tx = 0; ty = 0;
      apply();
    },
    /** Move the pan and zoom handlers to another element, for fullscreen. */
    retarget(next: HTMLElement) {
      unbind(target);
      // The cursor belongs to whichever element is driving, so the one being
      // left does not keep a grab cursor over a map that is elsewhere.
      target.style.cursor = '';
      target = next;
      bind(target);
      apply();
    },
  };
}

/** Country name and flag, for the tooltip. */
export function countryLabel(cc: string): string {
  return iso2Flag(cc) + ' ' + (CC_NAMES[cc] || cc);
}

/**
 * The country tooltip.
 *
 * ── THE RECT IS CACHED, AND THAT IS THE WHOLE REASON THIS IS NOT INLINE ─────
 *
 * `getBoundingClientRect` forces a layout, and a mousemove handler that called
 * it on every tick would do so sixty times a second over an SVG that has just
 * been re-diffed. It is measured once and invalidated on two events: a resize,
 * and a change of tooltip content — the second because the box changes size when
 * the text does, and a stale rect then places it against the wrong edge.
 *
 * ── THE CONTENT ONLY CHANGES WHEN THE COUNTRY DOES ──────────────────────────
 *
 * Position updates every move; innerHTML is rewritten only when the pointer
 * crosses into a different country. Rewriting it per move would be a parse on
 * every tick for a string that almost never differs.
 *
 * `info` and `hasCountry` are passed in because the counts, city and protocol
 * breakdown belong to the PAGE's payload, not to the map: the map knows where
 * countries are, and the page knows what is happening in them.
 */
export function bindMapTooltip(
  mapEl: HTMLElement,
  tooltipEl: HTMLElement,
  info: (cc: string) => MapTipInfo,
  hasCountry: (cc: string) => boolean,
): void {
  let tipCc: string | null = null;
  let wrapRect: DOMRect | null = null;

  window.addEventListener('resize', () => { wrapRect = null; });

  mapEl.addEventListener('mousemove', (e) => {
    const tgt = e.target as HTMLElement;
    const cc = tgt?.dataset?.cc;
    if (!cc) {
      if (tipCc) { tooltipEl.style.display = 'none'; tipCc = null; }
      return;
    }
    const { count, city, proto } = info(cc);
    // UNREACHABLE IN PRACTICE, and reproduced anyway: only country PATHS carry
    // `data-cc` — arcs and labels do not — so a code read off the target is
    // always one the map drew. The original carries the same guard.
    if (!count && !hasCountry(cc)) return;

    if (cc !== tipCc) {
      tipCc = cc;
      wrapRect = null; // the box resizes with its text
      const flag = iso2Flag(cc);
      tooltipEl.innerHTML = flag + ' <strong>' + esc(CC_NAMES[cc] || cc) + '</strong>' +
        (city ? ' · ' + esc(city) : '') +
        (count ? ' &nbsp;<span style="color:var(--accent-rx)">' + count + ' conns</span>' : '') +
        ((proto.tcp || proto.udp)
          ? '<br><span style="color:var(--text-muted);font-size:.6rem">TCP:' +
            (proto.tcp || 0) + ' UDP:' + (proto.udp || 0) + '</span>'
          : '');
      tooltipEl.style.display = 'block';
    }
    if (!wrapRect) wrapRect = mapEl.parentElement?.getBoundingClientRect() ?? null;
    if (!wrapRect) return;
    tooltipEl.style.left = (e.clientX - wrapRect.left + 10) + 'px';
    tooltipEl.style.top = (e.clientY - wrapRect.top - 30) + 'px';
  });

  mapEl.addEventListener('mouseleave', () => {
    tooltipEl.style.display = 'none';
    tipCc = null;
    wrapRect = null;
  });
}

/**
 * Fullscreen: portal the SVG into a body-level overlay.
 *
 * ── WHY A PORTAL AND NOT A CSS CLASS ────────────────────────────────────────
 *
 * The map lives inside a card, and a card sits in a stacking context with a
 * scroll parent. Growing it in place fights both. Moving the node to the body
 * escapes them, which is what the live app does and why the overlay is a
 * body-level element in the shell rather than part of the page.
 *
 * ── THE PLACEHOLDER IS A COMMENT NODE ───────────────────────────────────────
 *
 * It marks the exact slot the SVG left, so closing puts it back between the same
 * two siblings. Remembering the parent alone would append it at the end, and the
 * map would come back below the controls that sit under it.
 *
 * ── THE ZOOM HANDLERS FOLLOW THE MAP ────────────────────────────────────────
 *
 * They are bound to the WRAPPER, not the SVG, so a portal without a retarget
 * leaves panning answering to an element the map is no longer inside. The live
 * app rebinds its touch handlers for the same reason.
 */
export function bindMapFullscreen(
  wrap: HTMLElement,
  svg: SVGElement,
  zoom: { retarget: (next: HTMLElement) => void },
  els: { btn: HTMLElement | null; overlay: HTMLElement | null; close: HTMLElement | null },
): void {
  const { btn, overlay, close } = els;
  if (!btn || !overlay) return;
  const placeholder = document.createComment('map-svg-placeholder');
  // TWO STATED DIFFERENCES from the original, both unreachable through the UI and
  // both pinned in tools/map-fs-check.js. The live app has no such flag, so
  // opening twice puts its placeholder inside the overlay beside the SVG already
  // there, and closing before opening dereferences a placeholder with no parent
  // and throws. The button that opens is hidden while the overlay is up and the
  // one that closes lives inside it, so neither happens — this simply does not
  // depend on that being true.
  let open = false;

  const onKey = (e: KeyboardEvent): void => { if (e.key === 'Escape') closeFs(); };

  function openFs(): void {
    if (open) return;
    open = true;
    svg.parentNode?.insertBefore(placeholder, svg);
    overlay!.appendChild(svg);
    overlay!.classList.add('active');
    zoom.retarget(overlay!);
    // The page behind must not scroll while a full-screen map is over it.
    document.body.style.overflow = 'hidden';
    document.addEventListener('keydown', onKey);
  }

  function closeFs(): void {
    if (!open) return;
    open = false;
    placeholder.parentNode?.insertBefore(svg, placeholder);
    placeholder.parentNode?.removeChild(placeholder);
    overlay!.classList.remove('active');
    zoom.retarget(wrap);
    document.body.style.overflow = '';
    document.removeEventListener('keydown', onKey);
  }

  btn.addEventListener('click', openFs);
  close?.addEventListener('click', closeFs);
}
