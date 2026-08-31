// The Dashboard's Connections Map card (dc-card-destcc): a world map with an
// arc per destination country and a comet running along each one.
//
// ── IT BUILDS ITSELF FROM THE WORLD MAP MODULE'S DATA ───────────────────────
//
// `window._worldMapPathDs` and `_worldMapCentroids` are published by the
// connections page's world map, and `worldmap:ready` says when. Until then a
// `conn:update` is held in `pending` rather than dropped — the card is on the
// Dashboard and the map data arrives with a page the viewer may not have opened.
//
// ── AN ARC IS REBUILT ONLY WHEN ITS PATH CHANGES ────────────────────────────
//
// Rebuilding restarts the comet's animation, so an arc whose geometry has not
// moved is left alone even as its count changes. That is why the `d` attribute
// is compared rather than the count, and why the geometry rounds to one decimal:
// sub-pixel jitter would restart every comet on every tick.
//
// ── THE TIMING IS RANDOMISED, ON PURPOSE ────────────────────────────────────
//
// Each comet gets a duration jittered by ±0.3s and a NEGATIVE begin offset, so
// they start mid-flight and at different points. Without it every comet would
// leave the router in lockstep, which reads as one animation rather than many
// connections. `rng` is a parameter so the gate can compare the output at all.
//
// ── A LABEL IS BLANKED, NOT REMOVED ─────────────────────────────────────────
//
// A country that goes quiet keeps its `<text>` node with an empty string. Arcs
// are removed outright. The asymmetry is the original's: a label is cheap and
// re-appears in place, an arc carries an animation that would otherwise
// accumulate.

import { el } from '../dom';
import { esc } from '../dom';
import { mapArcD, applyMapHighlights, mapMaxCount } from './dashboard-map-geometry';
import { DC_CC_NAMES } from '../gen/dccards-tables';

const SVG_NS = 'http://www.w3.org/2000/svg';

export interface MapCountry {
  cc?: string;
  count?: number;
}

interface MapWindow {
  _worldMapPathDs?: Record<string, string>;
  _worldMapCentroids?: Record<string, [number, number]>;
  _worldMapLocalCC?: string;
}

export interface ConnMap {
  init(): void;
  apply(topCountries: MapCountry[]): void;
  onConnUpdate(topCountries: MapCountry[]): void;
  isReady(): boolean;
  reset(): void;
}

export function createConnMap(rng: () => number = Math.random): ConnMap {
  let pathEls: Record<string, SVGElement> = {};
  let arcEls: Record<string, SVGElement> = {};
  let labelEls: Record<string, SVGElement> = {};
  let arcLayer: SVGElement | null = null;
  let lblLayer: SVGElement | null = null;
  let counts: Record<string, number> = {};
  let ready = false;
  let pending: MapCountry[] | null = null;

  const w = (): MapWindow => window as unknown as MapWindow;

  function updateArcs(cc2n: Record<string, number>): void {
    const centroids = w()._worldMapCentroids;
    if (!arcLayer || !centroids) return;
    const localCC = w()._worldMapLocalCC || 'ZZ';
    const src = centroids[localCC];

    // Removed FIRST, and for every arc whose country has dropped out — including
    // when `src` is missing below, so a map that cannot draw still tidies up.
    for (const cc of Object.keys(arcEls)) {
      if (!cc2n[cc] && arcEls[cc]) {
        arcEls[cc]!.parentNode?.removeChild(arcEls[cc]!);
        delete arcEls[cc];
      }
    }
    if (!src) return;

    const max = mapMaxCount(cc2n);
    for (const cc of Object.keys(cc2n)) {
      // No arc from the router to itself.
      if (cc === localCC) continue;
      const dst = centroids[cc];
      if (!dst) continue;
      const hot = cc2n[cc]! >= max * 0.5;
      const arcD = mapArcD(src[0], src[1], dst[0], dst[1]);
      if (!arcD) continue;

      const existing = arcEls[cc];
      const arcPath = existing ? existing.querySelector('path') : null;
      if (existing && arcPath && arcPath.getAttribute('d') === arcD) continue;
      if (existing) existing.parentNode?.removeChild(existing);

      const g = document.createElementNS(SVG_NS, 'g');
      const path = document.createElementNS(SVG_NS, 'path');
      path.setAttribute('d', arcD);
      path.setAttribute('class', 'map-arc' + (hot ? ' hot' : ''));

      const durSecs = hot ? 1.4 : 2.2;
      // `.toFixed(2)` returns a STRING, and the unary minus below coerces it
      // back — so `begin` is a negative offset in seconds, which starts the
      // comet mid-flight rather than at the router.
      const finalDur = Math.max(0.8, durSecs + (rng() * 0.6 - 0.3)).toFixed(2) + 's';
      const beginDelay = -Number((rng() * durSecs).toFixed(2)) + 's';

      const circle = document.createElementNS(SVG_NS, 'circle');
      circle.setAttribute('r', hot ? '3' : '2');
      circle.setAttribute('class', 'map-comet' + (hot ? ' hot' : ''));
      const anim = document.createElementNS(SVG_NS, 'animateMotion');
      anim.setAttribute('dur', finalDur);
      anim.setAttribute('repeatCount', 'indefinite');
      anim.setAttribute('begin', beginDelay);
      anim.setAttribute('path', arcD);
      circle.appendChild(anim);

      g.appendChild(path);
      g.appendChild(circle);
      arcLayer.appendChild(g);
      arcEls[cc] = g;
    }
  }

  function updateLabels(cc2n: Record<string, number>): void {
    const centroids = w()._worldMapCentroids;
    if (!lblLayer || !centroids) return;
    for (const cc of Object.keys(labelEls)) {
      if (!cc2n[cc]) labelEls[cc]!.textContent = '';
    }
    for (const cc of Object.keys(cc2n)) {
      const c = centroids[cc];
      if (!c) continue;
      let node = labelEls[cc];
      if (!node) {
        node = document.createElementNS(SVG_NS, 'text');
        node.setAttribute('class', 'map-label');
        lblLayer.appendChild(node);
        labelEls[cc] = node;
      }
      node.setAttribute('x', c[0].toFixed(1));
      // Lifted six units so the number sits ABOVE the centroid rather than on it.
      node.setAttribute('y', (c[1] - 6).toFixed(1));
      node.textContent = String(cc2n[cc]);
    }
  }

  function apply(topCountries: MapCountry[]): void {
    const cc2n: Record<string, number> = {};
    for (const e of topCountries) cc2n[e.cc as string] = e.count as number;
    counts = cc2n;
    applyMapHighlights(pathEls as unknown as Record<string, { classList: { add(c: string): void; remove(...c: string[]): void } }>, cc2n);
    updateArcs(cc2n);
    updateLabels(cc2n);
  }

  function init(): void {
    const svg = el('dc-worldMap');
    const pathDs = w()._worldMapPathDs;
    if (!svg || !pathDs) return;
    // Cleared, because the card can be removed and re-added from the Add panel
    // and would otherwise stack a second map on the first.
    while (svg.firstChild) svg.removeChild(svg.firstChild);
    pathEls = {}; arcEls = {}; labelEls = {};

    const countryLayer = document.createElementNS(SVG_NS, 'g');
    arcLayer = document.createElementNS(SVG_NS, 'g');
    lblLayer = document.createElementNS(SVG_NS, 'g');
    svg.appendChild(countryLayer);
    svg.appendChild(arcLayer);
    svg.appendChild(lblLayer);

    // One fragment for ~200 countries: appending each in turn would lay out the
    // map once per country.
    const frag = document.createDocumentFragment();
    for (const cc of Object.keys(pathDs)) {
      const p = document.createElementNS(SVG_NS, 'path');
      p.setAttribute('d', pathDs[cc]!);
      p.setAttribute('class', 'map-country');
      p.setAttribute('data-cc', cc);
      pathEls[cc] = p;
      frag.appendChild(p);
    }
    countryLayer.appendChild(frag);

    const tip = el('dc-mapTooltip');
    if (tip) {
      svg.addEventListener('mousemove', (e) => {
        const tgt = (e as MouseEvent).target as HTMLElement | null;
        if (!tgt || !tgt.dataset || !tgt.dataset.cc) { tip.style.display = 'none'; return; }
        const cc = tgt.dataset.cc, n = counts[cc] || 0;
        // `esc`, not `dcEsc` — this is the page's escaper and the original uses
        // it here, which is right: the value is interpolated into markup.
        tip.innerHTML = esc(DC_CC_NAMES[cc] || cc) +
          (n ? ' &nbsp;<span style="color:var(--accent-rx)">' + esc(String(n)) + ' conns</span>' : '');
        tip.style.display = 'block';
        const rect = (svg.parentElement as HTMLElement).getBoundingClientRect();
        tip.style.left = ((e as MouseEvent).clientX - rect.left + 10) + 'px';
        tip.style.top = ((e as MouseEvent).clientY - rect.top - 30) + 'px';
      });
      svg.addEventListener('mouseleave', () => { tip.style.display = 'none'; });
    }

    ready = true;
    if (pending) { apply(pending); pending = null; }
  }

  return {
    init, apply,
    // Held rather than dropped: the map data arrives with a page the viewer may
    // not have opened yet.
    onConnUpdate: (topCountries) => { if (ready) apply(topCountries); else pending = topCountries; },
    isReady: () => ready,
    reset: () => { counts = {}; pending = null; },
  };
}
