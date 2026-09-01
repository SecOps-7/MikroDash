// The Network Topology map — a port of public/js/topology.js.
//
// THE FIRST PAGE WHOSE RENDERER IS NOT IN app.js. The live app loads
// `/js/topology.js` after app.js so it can close over `socket`, `esc`, `$`,
// `fmtMbps` and `pageVisible` as globals; here those are ordinary imports, and
// The live-renderer tool learned to lift a whole file so the DOM comparison
// still has something to compare against.
//
// Three design choices carried over intact, each of which has a cheaper wrong
// answer:
//
//  1. Layout is a DETERMINISTIC RADIAL grouped by parentage, not a force
//     simulation. The graph is always a shallow star, so a physics sim would add
//     a frame loop and non-determinism to solve a problem that does not exist —
//     and the map would settle differently on every visit.
//
//  2. Link bandwidth is joined CLIENT-SIDE against the `ifstatus:update` payload
//     the browser already receives. The topology collector therefore stays slow
//     (30 s) while links animate at the interface cadence (~5 s), with no extra
//     router load and no duplicated data.
//
//  3. Rendering is a KEYED DIFF, never innerHTML. Replacing the SVG would cancel
//     an in-progress drag and restart every SMIL animation.
//
// This file was ported in slices — geometry and layout first, then the render
// and interaction halves. All of them landed, and `main.ts` calls
// `initTopologyPage`; the sentence that used to sit here saying it was not wired
// yet outlived the slice it described.

import { esc, el as byId, fmtMbps } from '../dom';
import type { Socket } from '../socket';
import type { TopoEdge, TopologyPayload, TopoNode, TopoClientNode } from './topology-types';

export const NS = 'http://www.w3.org/2000/svg';

/** A node position on the canvas. */
export interface Pos { x: number; y: number }

/** What the layout needs of a node: the collector sends much more. */
export interface LayoutNode {
  key: string;
  kind: string;      // core | neighbor | client
  name: string;
  parent: string | null;
  ifaces: string[];
}

/**
 * Device glyphs, drawn in a 24x24 box centred on the node.
 *
 * Deliberately the same visual vocabulary as the left nav, so a switch reads as
 * a switch in both places.
 */
export const GLYPHS: Record<string, string> = {
  router: '<circle cx="18" cy="5" r="2.4"/><circle cx="6" cy="12" r="2.4"/><circle cx="18" cy="19" r="2.4"/>' +
          '<line x1="8.2" y1="13.3" x2="15.6" y2="17.7"/><line x1="15.6" y1="6.3" x2="8.2" y2="10.7"/>',
  switch: '<rect x="2.5" y="5" width="19" height="5" rx="1"/><rect x="2.5" y="14" width="19" height="5" rx="1"/>' +
          '<circle cx="18.5" cy="7.5" r=".9" fill="currentColor" stroke="none"/>' +
          '<circle cx="18.5" cy="16.5" r=".9" fill="currentColor" stroke="none"/>',
  ap: '<path d="M5 12.55a11 11 0 0 1 14.08 0"/><path d="M1.42 9a16 16 0 0 1 21.16 0"/>' +
      '<path d="M8.53 16.11a6 6 0 0 1 6.95 0"/><circle cx="12" cy="20" r="1" fill="currentColor" stroke="none"/>',
  station: '<rect x="3" y="4" width="18" height="12" rx="1.5"/><line x1="8" y1="20" x2="16" y2="20"/>' +
           '<line x1="12" y1="16" x2="12" y2="20"/>',
  phone: '<path d="M6 3h12v18H6z"/><line x1="10" y1="18" x2="14" y2="18"/>',
  modem: '<rect x="2.5" y="9" width="19" height="8" rx="1.5"/><line x1="6" y1="13" x2="6" y2="13.01"/>' +
         '<line x1="9.5" y1="13" x2="9.5" y2="13.01"/><path d="M15 12.5h4"/>',
  repeater: '<path d="M4 10a8 8 0 0 1 16 0"/><circle cx="12" cy="15" r="2"/>',
  other: '<path d="M12 2.5l8.2 4.7v9.6L12 21.5 3.8 16.8V7.2z"/>',
  unknown: '<circle cx="12" cy="12" r="9"/><path d="M9.4 9.4a2.7 2.7 0 0 1 5.2.9c0 1.8-2.6 2.7-2.6 2.7"/>' +
           '<line x1="12" y1="17" x2="12" y2="17.01"/>',
};

export function glyph(type: string): string {
  return GLYPHS[type] || GLYPHS['unknown']!;
}

export const TYPE_LABEL: Record<string, string> = {
  router: 'Router', switch: 'Switch', ap: 'Access point', station: 'Station',
  phone: 'VoIP phone', modem: 'Modem', repeater: 'Repeater',
  other: 'Other device', unknown: 'Unidentified',
};

/** An SVG element with attributes, the one shape this file builds constantly. */
export function svgEl(tag: string, attrs?: Record<string, string | number>): SVGElement {
  const e = document.createElementNS(NS, tag) as SVGElement;
  if (attrs) for (const k of Object.keys(attrs)) e.setAttribute(k, String(attrs[k]));
  return e;
}

/** Set an attribute only when it CHANGED — the keyed diff's inner loop. */
export function attr(e: Element | null, k: string, v: string | number): void {
  if (e && e.getAttribute(k) !== String(v)) e.setAttribute(k, String(v));
}

export function text(e: Element | null, v: string | number): void {
  if (e && e.textContent !== String(v)) e.textContent = String(v);
}

export function lsGet<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(key);
    return v ? (JSON.parse(v) as T) : fallback;
  } catch {
    return fallback;
  }
}

export function lsSet(key: string, val: unknown): void {
  try { localStorage.setItem(key, JSON.stringify(val)); } catch { /* private mode */ }
}

// ── layout ──────────────────────────────────────────────────────────────────

/**
 * Group children by their parent, give each group an arc, and place nodes on a
 * ring inside it.
 *
 * `saved` holds user-dragged positions and takes precedence everywhere: the core
 * is NOT pinned to the origin — it can be dragged like anything else, and
 * everything below is positioned RELATIVE to wherever it is. A hardcoded origin
 * left the whole map fanned around the core's original spot after it was moved.
 */
export function computeLayout(nodes: LayoutNode[], saved: Record<string, Pos>): Record<string, Pos> {
  const c0 = saved['core'] || { x: 0, y: 0 };
  const out: Record<string, Pos> = { core: { x: c0.x, y: c0.y } };

  const neighbours = nodes.filter((n) => n.kind !== 'core');
  if (!neighbours.length) return out;

  // Children by parent, so a device behind a switch is drawn behind it rather
  // than beside it. The COLLECTOR decides parentage; this only draws it.
  const kids: Record<string, LayoutNode[]> = {};
  neighbours.forEach((n) => {
    const p = n.parent || 'core';
    (kids[p] = kids[p] || []).push(n);
  });
  Object.keys(kids).forEach((p) => {
    kids[p]!.sort((a, b) => {
      const ai = (a.ifaces && a.ifaces[0]) || '';
      const bi = (b.ifaces && b.ifaces[0]) || '';
      return ai === bi ? String(a.name).localeCompare(String(b.name)) : ai.localeCompare(bi);
    });
  });

  // The core's own clients are not a topology tier and must not share the ring
  // with switches and APs, so they are split out and given the arc's gap.
  const coreKids = kids['core'] || [];
  const tier1 = coreKids.filter((n) => n.kind !== 'client');
  const coreClients = coreKids.filter((n) => n.kind === 'client');

  // Tier 1 fans around the core on a CAPPED angular step. Dividing a full circle
  // would place two devices exactly opposite and make the core read as a
  // pass-through in a chain — the one thing a star must not look like.
  const MAX_ARC = Math.PI * 1.7;
  const STEP = 0.62;
  let step = STEP;
  let span = step * Math.max(0, tier1.length - 1);
  if (span > MAX_ARC) {
    step = MAX_ARC / Math.max(1, tier1.length - 1);
    span = MAX_ARC;
  }

  const base = 2.399963 - span / 2; // golden-angle base, so it is never dead flat
  const R1 = 200;

  /**
   * Where a node ACTUALLY is: a dragged position wins over the computed one.
   * Anchoring children to the computed slot is what previously left them behind
   * at the parent's original spot after a drag.
   */
  const anchor = (key: string): Pos => saved[key] || out[key] || { x: 0, y: 0 };

  const placeChildren = (parent: LayoutNode, depth: number): void => {
    const list = kids[parent.key];
    if (!list || !list.length || depth > 4) return;
    const p = anchor(parent.key);

    // Fan away from the CORE along the core→parent vector, so wherever the
    // parent is dragged its children swing round to stay outside it.
    const dx = p.x - c0.x, dy = p.y - c0.y;
    const ang = (dx || dy) ? Math.atan2(dy, dx) : 0;

    const isClient = !!list[0] && list[0]!.kind === 'client';
    const spread = isClient
      ? Math.min(Math.PI * 1.5, 0.42 * list.length)
      : Math.min(Math.PI * 0.75, 0.5 * list.length);
    const start = ang - spread / 2;
    const stepC = list.length === 1 ? 0 : spread / (list.length - 1);
    const R = (isClient ? 118 : 165) * Math.pow(0.88, depth - 1);

    list.forEach((c, i) => {
      const a = list.length === 1 ? ang : start + stepC * i;
      out[c.key] = { x: p.x + Math.cos(a) * R * 1.3, y: p.y + Math.sin(a) * R * 0.9 };
      placeChildren(c, depth + 1);
    });
  };

  tier1.forEach((n, i) => {
    const a = base + step * i;
    out[n.key] = { x: c0.x + Math.cos(a) * R1 * 1.45, y: c0.y + Math.sin(a) * R1 * 0.82 };
    placeChildren(n, 1);
  });

  // The core's clients go in the UNUSED part of the arc, on their own ring, so
  // they never sit on top of the infrastructure fan.
  if (coreClients.length) {
    const gapCentre = base + span + (Math.PI * 2 - span) / 2;
    const cSpread = Math.min(Math.PI * 1.15, 0.3 * coreClients.length);
    const cStart = gapCentre - cSpread / 2;
    const cStep = coreClients.length === 1 ? 0 : cSpread / (coreClients.length - 1);
    coreClients.forEach((c, i) => {
      const a = coreClients.length === 1 ? gapCentre : cStart + cStep * i;
      const ring = 300 + (i % 2) * 58; // stagger, so labels do not collide
      out[c.key] = { x: c0.x + Math.cos(a) * ring * 1.35, y: c0.y + Math.sin(a) * ring * 0.86 };
      placeChildren(c, 2);
    });
  }

  // Anything whose parent never resolved — which should not happen, but the
  // layout must not drop a node on the floor — gets a slot on the outer ring.
  let orphan = 0;
  neighbours.forEach((n) => {
    if (out[n.key]) return;
    const a = base + step * (tier1.length + orphan++);
    out[n.key] = { x: c0.x + Math.cos(a) * 320 * 1.45, y: c0.y + Math.sin(a) * 320 * 0.82 };
  });
  return out;
}

// ── edge geometry ───────────────────────────────────────────────────────────

/** A gently curved link. The bow is what stops two links between the same pair
 *  drawing on top of each other. */
export function edgePath(x0: number, y0: number, x1: number, y1: number): string {
  const mx = (x0 + x1) / 2, my = (y0 + y1) / 2;
  const dx = x1 - x0, dy = y1 - y0, k = 0.08;
  return 'M' + x0.toFixed(1) + ',' + y0.toFixed(1) +
    ' Q' + (mx - dy * k).toFixed(1) + ',' + (my + dx * k).toFixed(1) +
    ' ' + x1.toFixed(1) + ',' + y1.toFixed(1);
}

/**
 * Link width from absolute Mbps, LOG-SCALED.
 *
 * A utilisation percentage would need a link speed that `/interface/print` does
 * not reliably report, so it would be a guess dressed as a measurement.
 */
export function loadWidth(mbps: number): number {
  return 1.2 + 4 * Math.min(1, Math.log10(1 + Math.max(0, mbps)) / 3);
}

export type { TopoEdge };

// ── the page ────────────────────────────────────────────────────────────────

/** One interface's throughput, joined client-side from `ifstatus:update`. */
interface Rate { rx: number; tx: number; running: boolean }

interface EdgeEls {
  g: SVGElement; path: SVGElement; load: SVGElement; hit: SVGElement;
  label: SVGElement; iface: SVGElement;
}

interface FlowRec { g: SVGElement; sig: string; paths: SVGElement[] }

/** Short Mbps for an edge label, where the full `fmtMbps` would not fit. */
export function fmtShort(mbps: number): string {
  const n = Number(mbps) || 0;
  if (n >= 1000) return (n / 1000).toFixed(1) + 'G';
  if (n >= 1) return n.toFixed(1) + 'M';
  return (n * 1000).toFixed(0) + 'K';
}

/**
 * The state this page keeps, and why each piece exists.
 *
 * `pos` is where things are NOW; `saved` is only what the user dragged, and is
 * persisted; `placed` is the last laid-out spot and is what makes a node
 * independent once it is on screen — dragging a device moves only that device,
 * and its already-expanded children stay put. The pin is dropped when a node
 * leaves the canvas, so re-expanding lays out afresh around wherever the parent
 * is by then.
 */
export function initTopologyPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let data: TopologyPayload | null = null;
  let rates: Record<string, Rate> = {};
  let pos: Record<string, Pos> = {};
  const saved: Record<string, Pos> = {};
  const placed: Record<string, Pos> = {};
  let sel: string | null = null;
  let filter = '';
  let typeFilter = '';
  let vlanFilter = '';
  let vlanSig = '';
  let showFlow = true;
  let showRates = true;
  let showClients = false;
  const expanded: Record<string, boolean> = {};
  let draggingKey: string | null = null;
  let pendingRender = false;
  let rafId: number | null = null;
  let saveTimer: number | undefined;
  let rid: string | null = null;
  let view = { k: 1, x: 0, y: 0 };
  const gViewport = byId('topoViewport') as unknown as SVGElement | null;
  const svg = byId('topoSvg') as unknown as SVGSVGElement | null;

  const nodeEls: Record<string, SVGElement> = {};
  const edgeEls: Record<string, EdgeEls> = {};
  const flowEls: Record<string, FlowRec> = {};

  const gEdges = byId('topoEdges') as unknown as SVGElement | null;
  const gFlow = byId('topoFlow') as unknown as SVGElement | null;
  const gNodes = byId('topoNodes') as unknown as SVGElement | null;

  // ── filters ───────────────────────────────────────────────────────────────

  /**
   * Is this node dimmed by the current filters?
   *
   * A VLAN is a property of a CLIENT, so infrastructure is never dimmed by one —
   * dimming the switch a filtered client hangs off would hide the answer the
   * filter was asked for.
   */
  function isFiltered(n: TopoNode): boolean {
    if (n.kind === 'core') return false;
    if (typeFilter && n.type !== typeFilter) return true;
    if (vlanFilter && n.kind === 'client' &&
        (n.vlans || []).indexOf(Number(vlanFilter)) === -1) return true;
    if (!filter) return false;
    const vlanNames = n.kind === 'client' ? (n.vlanNames || []).join(' ') : '';
    const hay = [n.name, n.identity, n.ip, n.mac, n.board, n.platform, vlanNames]
      .join(' ').toLowerCase();
    return hay.indexOf(filter) === -1;
  }

  /**
   * Is this client drawn at all?
   *
   * Its parent must be expanded (or the global toggle on) — and a search or VLAN
   * match FORCES it into view, so filtering works from the collapsed state
   * rather than appearing to find nothing.
   */
  function clientShown(n: TopoNode): boolean {
    if (n.kind !== 'client') return true;
    if ((filter || vlanFilter) && !isFiltered(n)) return true;
    return showClients || !!expanded[n.parent];
  }

  function visibleNodes(nodes: TopoNode[]): TopoNode[] {
    return nodes.filter(clientShown);
  }

  /**
   * Edges whose BOTH ends are on screen.
   *
   * Rendering the raw edge list draws links to collapsed clients, which have no
   * position and so anchor at the origin — the stray grey lines that used to fan
   * out during a drag and when toggling Flow or Rates.
   */
  function visibleEdges(): TopoEdge[] {
    if (!data) return [];
    const shown: Record<string, boolean> = {};
    visibleNodes(data.nodes).forEach((n) => { shown[n.key] = true; });
    return data.edges.filter((e) => shown[e.from] && shown[e.to]);
  }

  function rateFor(iface: string): Rate | null {
    if (!iface) return null;
    return rates[iface] || null;
  }

  function applyPositions(nodes: TopoNode[]): void {
    const auto = computeLayout(nodes as unknown as LayoutNode[], saved);
    const next: Record<string, Pos> = {};
    nodes.forEach((n) => {
      // Precedence: an explicit drag, then wherever the node was last PLACED,
      // then a fresh computed slot.
      next[n.key] = saved[n.key] || placed[n.key] || auto[n.key] || { x: 0, y: 0 };
      placed[n.key] = next[n.key]!;
    });
    pos = next;
  }

  // ── nodes ─────────────────────────────────────────────────────────────────

  function buildClientNode(n: TopoNode, g: SVGElement): SVGElement {
    // Clients are deliberately much plainer than infrastructure: a dot and a
    // label. They are the numerous tier, so any extra chrome multiplies.
    g.appendChild(svgEl('circle', { class: 'topo-cdot', r: 6 }));
    g.appendChild(svgEl('text', { class: 'topo-clabel', y: 19 }));
    g.appendChild(svgEl('title'));
    gNodes?.appendChild(g);
    nodeEls[n.key] = g;
    return g;
  }

  function buildNode(n: TopoNode): SVGElement {
    const g = svgEl('g', { class: 'topo-node', 'data-key': n.key });
    if (n.kind === 'client') return buildClientNode(n, g);
    g.appendChild(svgEl('circle', { class: 'topo-halo', r: n.kind === 'core' ? 34 : 26 }));
    if (n.kind === 'core') {
      const ring = svgEl('circle', { class: 'topo-ring', r: 42 });
      ring.appendChild(svgEl('animateTransform', {
        attributeName: 'transform', type: 'rotate', from: '0 0 0', to: '360 0 0',
        dur: '24s', repeatCount: 'indefinite',
      }));
      g.appendChild(ring);
    }
    g.appendChild(svgEl('circle', { class: 'topo-disc', r: n.kind === 'core' ? 28 : 20 }));

    const gl = svgEl('g', { class: 'topo-glyph' });
    gl.innerHTML = glyph(n.type);
    const s = n.kind === 'core' ? 1.15 : 0.85;
    gl.setAttribute('transform', 'translate(' + (-12 * s) + ',' + (-12 * s) + ') scale(' + s + ')');
    g.appendChild(gl);

    // The count chip rides the BOTTOM edge of the ring, mirroring the latency
    // pill at the top-right, so the labels start below it — otherwise the chip
    // lands on top of the device name.
    const ringR = n.kind === 'core' ? 34 : 26;
    const y = ringR + 24;
    g.appendChild(svgEl('text', { class: 'topo-label', y }));
    g.appendChild(svgEl('text', { class: 'topo-sub', y: y + 13 }));

    const rttG = svgEl('g', { class: 'topo-rtt', transform: 'translate(19,-19)' });
    rttG.appendChild(svgEl('rect', { class: 'topo-rtt-bg', x: -16, y: -7, width: 32, height: 14, rx: 7 }));
    rttG.appendChild(svgEl('text', { class: 'topo-rtt-tx', y: 3 }));
    g.appendChild(rttG);

    // The client-count chip, which is ALSO the expand affordance. Only
    // infrastructure gets one; clients have no children.
    const chip = svgEl('g', { class: 'topo-chip-g', transform: 'translate(0,' + ringR + ')' });
    chip.appendChild(svgEl('rect', { class: 'topo-chip-bg', x: -15, y: -9, width: 30, height: 16, rx: 8 }));
    chip.appendChild(svgEl('text', { class: 'topo-chip-tx', y: 3 }));
    g.appendChild(chip);

    // Native tooltip via textContent — no escaping concern, and free.
    g.appendChild(svgEl('title'));
    gNodes?.appendChild(g);
    nodeEls[n.key] = g;
    return g;
  }

  function tooltipFor(n: TopoNode): string {
    const bits = [n.name || n.key, TYPE_LABEL[n.type] || n.type];
    if (n.ip) bits.push(n.ip);
    if (n.board) bits.push(n.board);
    if (n.gone) bits.push('last seen ' + new Date(n.lastSeen).toLocaleTimeString());
    return bits.join(' · ');
  }

  function clientTooltip(n: TopoClientNode): string {
    return [n.name || n.mac, n.ip,
      (n.vlanNames || []).length ? 'VLAN ' + n.vlanNames.join('/') : '',
      n.type === 'wifi-client'
        ? ('Wi-Fi' + (n.ssid ? ' · ' + n.ssid : '') + (n.signal ? ' · ' + n.signal + ' dBm' : ''))
        : 'wired',
      n.mac].filter(Boolean).join(' · ');
  }

  function renderNodes(nodes: TopoNode[]): void {
    const seen: Record<string, boolean> = {};
    nodes.forEach((n) => {
      seen[n.key] = true;
      const g = nodeEls[n.key] || buildNode(n);
      if (n.key === draggingKey) return; // never fight an in-progress drag

      const p = pos[n.key] || { x: 0, y: 0 };
      attr(g, 'transform', 'translate(' + p.x.toFixed(1) + ',' + p.y.toFixed(1) + ')');

      if (n.kind === 'client') {
        let ccls = 'topo-node is-client ' + (n.type === 'wifi-client' ? 'is-wifi' : 'is-wired');
        if (sel === n.key) ccls += ' is-sel';
        if (isFiltered(n)) ccls += ' is-dim';
        attr(g, 'class', ccls);
        text(g.querySelector('.topo-clabel'), n.name || n.mac);
        text(g.querySelector('title'), clientTooltip(n));
        return;
      }

      let cls = 'topo-node st-' + (n.status || 'unknown');
      if (n.kind === 'core') cls += ' is-core';
      if (n.gone) cls += ' is-gone';
      if (sel === n.key) cls += ' is-sel';
      if (isFiltered(n)) cls += ' is-dim';
      attr(g, 'class', cls);

      text(g.querySelector('.topo-label'), n.name || n.key);
      text(g.querySelector('.topo-sub'), n.ip || n.mac || '');

      const rttG = g.querySelector('.topo-rtt') as SVGElement | null;
      const showRtt = n.kind !== 'core' && n.rtt !== null && isFinite(n.rtt);
      if (rttG) rttG.style.display = showRtt ? '' : 'none';
      if (showRtt && rttG) {
        text(rttG.querySelector('.topo-rtt-tx'), n.rtt!.toFixed(n.rtt! < 10 ? 1 : 0) + 'ms');
      }

      const chipG = g.querySelector('.topo-chip-g') as SVGElement | null;
      if (chipG) {
        const count = (n.kind === 'core' || n.kind === 'neighbor') ? (n.clientCount || 0) : 0;
        const open = showClients || !!expanded[n.key];
        chipG.style.display = count ? '' : 'none';
        if (count) {
          text(chipG.querySelector('.topo-chip-tx'), (open ? '−' : '+') + count);
          chipG.setAttribute('class', 'topo-chip-g' + (open ? ' is-open' : ''));
        }
      }

      text(g.querySelector('title'), tooltipFor(n));
    });

    Object.keys(nodeEls).forEach((k) => {
      if (seen[k]) return;
      nodeEls[k]!.parentNode?.removeChild(nodeEls[k]!);
      delete nodeEls[k];
      if (sel === k) sel = null;
    });
  }

  // ── edges ─────────────────────────────────────────────────────────────────

  function buildEdge(e: TopoEdge): EdgeEls {
    const g = svgEl('g', { 'data-id': e.id });
    const rec: EdgeEls = {
      g,
      path: svgEl('path', { class: 'topo-edge' }),
      load: svgEl('path', { class: 'topo-edge-load' }),
      hit: svgEl('path', { class: 'topo-edge-hit' }),
      label: svgEl('text', { class: 'topo-elabel' }),
      iface: svgEl('text', { class: 'topo-iflabel' }),
    };
    g.appendChild(rec.path); g.appendChild(rec.load); g.appendChild(rec.hit);
    g.appendChild(rec.iface); g.appendChild(rec.label);
    g.appendChild(svgEl('title'));
    gEdges?.appendChild(g);
    edgeEls[e.id] = rec;
    return rec;
  }

  function edgeTooltip(e: TopoEdge, r: Rate | null): string {
    if (e.inferred) {
      // Say plainly that this link is DEDUCED, and from what: the router can see
      // that the device is behind this switch, but not which switch port.
      let tip = 'behind this device on ' + (e.viaPort || 'the same port') +
        ' — inferred: seen via MNDP/CDP only, so it is not directly attached';
      if (e.remoteIface) tip += '\nits port: ' + e.remoteIface;
      return tip;
    }
    let tip = e.iface || 'link';
    if (e.remoteIface) tip += '  →  ' + e.remoteIface;
    if (e.shared) tip += '  (shared segment — more than one device on this port)';
    if (r) tip += '  ↓' + fmtMbps(r.rx) + '  ↑' + fmtMbps(r.tx);
    return tip;
  }

  function renderEdges(edges: TopoEdge[]): void {
    const seen: Record<string, boolean> = {};
    edges.forEach((e) => {
      seen[e.id] = true;
      const rec = edgeEls[e.id] || buildEdge(e);
      const a = pos[e.from] || { x: 0, y: 0 };
      const b = pos[e.to] || { x: 0, y: 0 };
      const d = edgePath(a.x, a.y, b.x, b.y);
      attr(rec.path, 'd', d);
      attr(rec.load, 'd', d);
      attr(rec.hit, 'd', d);

      let cls = 'topo-edge';
      if (e.shared) cls += ' is-shared';
      if (e.inferred) cls += ' is-inferred';
      if (e.gone) cls += ' is-gone';
      attr(rec.path, 'class', cls);

      const r = rateFor(e.iface);
      const total = r ? r.rx + r.tx : 0;
      if (r && total > 0.01 && !e.gone) {
        attr(rec.load, 'stroke', r.rx >= r.tx ? 'var(--accent-rx)' : 'var(--accent-tx)');
        attr(rec.load, 'stroke-width', loadWidth(total).toFixed(2));
        (rec.load as SVGElement).style.display = '';
      } else {
        (rec.load as SVGElement).style.display = 'none';
      }

      const mx = (a.x + b.x) / 2 - (b.y - a.y) * 0.08;
      const my = (a.y + b.y) / 2 + (b.x - a.x) * 0.08;
      attr(rec.iface, 'x', mx.toFixed(1)); attr(rec.iface, 'y', (my - 5).toFixed(1));
      attr(rec.label, 'x', mx.toFixed(1)); attr(rec.label, 'y', (my + 7).toFixed(1));
      text(rec.iface, e.iface || '');

      if (showRates && r && total > 0.01) {
        text(rec.label, '↓' + fmtShort(r.rx) + '  ↑' + fmtShort(r.tx));
        (rec.label as SVGElement).style.display = '';
      } else {
        (rec.label as SVGElement).style.display = 'none';
      }

      text(rec.g.querySelector('title'), edgeTooltip(e, r));
      updateFlow(e, d, total);
    });

    Object.keys(edgeEls).forEach((id) => {
      if (seen[id]) return;
      const rec = edgeEls[id]!;
      rec.g.parentNode?.removeChild(rec.g);
      delete edgeEls[id];
      removeFlow(id);
    });
  }

  // ── flow animation ────────────────────────────────────────────────────────
  //
  // THE THROTTLE IS THE WHOLE POINT. `ifstatus:update` lands every ~5 s, and
  // rebuilding <animateMotion> nodes each time restarts every animation at once
  // — the entire canvas visibly jumps. So dot count and duration are bucketed
  // into a signature and the DOM is rebuilt only when that signature changes.

  function flowBudget(): number {
    const n = Object.keys(edgeEls).length;
    if (n > 60) return 0;
    if (n > 30) return 1;
    return 4;
  }

  function updateFlow(e: TopoEdge, d: string, totalMbps: number): void {
    const maxDots = flowBudget();
    if (!showFlow || !maxDots || e.gone || totalMbps <= 0.05) { removeFlow(e.id); return; }

    const dots = Math.max(1, Math.min(maxDots, Math.ceil(Math.log10(1 + totalMbps))));
    const dur = Math.max(0.9, Math.min(6, 6 / (0.5 + Math.log10(1 + totalMbps)))).toFixed(1);
    const sig = dots + ':' + dur + ':' + (e.shared ? 1 : 0);

    const rec = flowEls[e.id];
    if (rec && rec.sig === sig) {
      rec.paths.forEach((p) => attr(p, 'path', d));
      return;
    }

    removeFlow(e.id);
    const g = svgEl('g', { class: 'topo-flow-dot' });
    const paths: SVGElement[] = [];
    const r = rateFor(e.iface) || { rx: 0, tx: 0, running: false };
    const inbound = r.rx >= r.tx;
    for (let i = 0; i < dots; i++) {
      const c = svgEl('circle', { r: 2.2, fill: inbound ? 'var(--accent-rx)' : 'var(--accent-tx)' });
      const m = svgEl('animateMotion', {
        dur: dur + 's', repeatCount: 'indefinite', path: d,
        // A NEGATIVE begin de-syncs the dots deterministically, so a link's dots
        // are evenly spaced rather than leaving together.
        begin: (-(i * Number(dur)) / dots).toFixed(2) + 's',
        keyPoints: inbound ? '1;0' : '0;1', keyTimes: '0;1', calcMode: 'linear',
      });
      c.appendChild(m);
      g.appendChild(c);
      paths.push(m);
    }
    gFlow?.appendChild(g);
    flowEls[e.id] = { g, sig, paths };
  }

  function removeFlow(id: string): void {
    const rec = flowEls[id];
    if (!rec) return;
    rec.g.parentNode?.removeChild(rec.g);
    delete flowEls[id];
  }

  /** Rebuild the VLAN dropdown from whatever the router actually reported, so it
   *  never offers an option that would match nothing. */
  function syncVlanOptions(): void {
    const elVlan = byId<HTMLSelectElement>('topoVlan');
    if (!elVlan || !data) return;
    const list = data.vlans || [];
    const sig = list.map((v) => v.vid + ':' + v.name).join(',');
    if (sig === vlanSig) return;
    vlanSig = sig;
    const keep = vlanFilter;
    elVlan.innerHTML = '<option value="">All VLANs</option>' +
      list.map((v) => '<option value="' + v.vid + '">' + esc(v.name) +
        (String(v.name) === String(v.vid) ? '' : ' (' + v.vid + ')') + '</option>').join('');
    // Keep the current selection if it still exists; otherwise fall back to all.
    if (keep && list.some((v) => String(v.vid) === String(keep))) elVlan.value = keep;
    else { elVlan.value = ''; vlanFilter = ''; }
  }

  // ── stats, empty state, footer ────────────────────────────────────────────

  function renderStats(): void {
    const infra = data ? data.nodes.filter((n) => n.kind !== 'core' && n.kind !== 'client') : [];
    const clients = data ? data.clientCount || 0 : 0;
    // The INFRASTRUCTURE count stays the headline and clients ride along, so the
    // number does not silently jump when the client tier is expanded.
    text(byId('topoStatDevices'),
      infra.length ? String(infra.length) + (clients ? ' + ' + clients : '') : '—');

    const links = data ? data.edges.filter((e) => !e.client).length : 0;
    text(byId('topoStatLinks'), links ? String(links) : '—');

    let thru = 0;
    (data ? data.edges : []).forEach((e) => {
      const r = rateFor(e.iface);
      if (r) thru += r.rx + r.tx;
    });
    text(byId('topoStatThru'), thru > 0 ? fmtMbps(thru) : '—');

    let worst: number | null = null;
    infra.forEach((n) => {
      if (n.rtt !== null && isFinite(n.rtt) && (worst === null || n.rtt > worst)) worst = n.rtt;
    });
    // `n/a` and `—` say different things: the first is "this API user may not
    // measure it", the second is "nothing has answered yet".
    if (data && data.pingDenied) text(byId('topoStatRtt'), 'n/a');
    else if (worst === null) text(byId('topoStatRtt'), '—');
    else text(byId('topoStatRtt'), (worst as number).toFixed((worst as number) < 10 ? 1 : 0) + ' ms');
  }

  /**
   * An empty map that EXPLAINS ITSELF.
   *
   * Each branch is a real reason a correctly-working router reports no
   * neighbours, and saying which one applies is the difference between a page
   * that looks broken and a page that has told you what to change.
   */
  function renderEmpty(): void {
    const emptyEl = byId('topoEmpty');
    if (!emptyEl) return;
    const count = data ? data.nodes.filter((n) => n.kind !== 'core').length : 0;
    if (!data) {
      emptyEl.className = 'topo-empty show';
      emptyEl.innerHTML = '<b>Waiting for the router…</b>';
      return;
    }
    if (count) {
      emptyEl.className = 'topo-empty';
      emptyEl.innerHTML = '';
      return;
    }

    let html = '<b>No neighbouring devices discovered</b>';
    const d = data.discovery;
    if (data.permissionDenied) {
      html += '<div class="topo-empty-hint">This API user cannot read <code>/ip/neighbor</code>. ' +
        'Grant the <code>read</code> policy to see discovered devices.</div>';
    } else if (d && d.mode === 'tx-only') {
      html += '<div class="topo-empty-hint">Discovery is set to <code>tx-only</code>, so this router ' +
        'advertises itself but never records neighbours. Set it to <code>tx-and-rx</code> under ' +
        '<code>/ip/neighbor/discovery-settings</code>.</div>';
    } else if (d && d.interfaceList && d.interfaceList !== 'all') {
      html += '<div class="topo-empty-hint">Discovery only runs on the <code>' + esc(d.interfaceList) +
        '</code> interface list. Devices reached through other interfaces will not appear here.</div>';
    } else {
      html += '<div class="topo-empty-hint">Nothing is advertising LLDP, CDP or MNDP on this router\u2019s ' +
        'discovery interfaces. Unmanaged switches and most end devices stay invisible by design.</div>';
    }
    emptyEl.className = 'topo-empty show';
    emptyEl.innerHTML = html;
  }

  function renderFoot(): void {
    const footEl = byId('topoFoot');
    if (!footEl) return;
    const parts: string[] = [];
    const d = data && data.discovery;
    if (d && d.protocol && d.protocol.length) parts.push('Discovery: ' + esc(d.protocol.join(', ')));
    if (d && d.interfaceList) parts.push('on <code>' + esc(d.interfaceList) + '</code>');
    if (data && data.pingDenied) {
      parts.push('<span style="color:var(--accent-warn)">latency needs the <code>test</code> policy</span>');
    }
    const legend = ([['up', '--accent-ok'], ['warn', '--accent-warn'], ['down', '--accent-err']] as const)
      .map((p) => '<span class="topo-legend" style="color:var(' + p[1] + ')">' +
        '<span class="topo-swatch"></span>' + p[0] + '</span>').join('');
    // Say which links are OBSERVED and which are DEDUCED, rather than presenting
    // the whole map as equally certain.
    const inferred = ((data && data.edges) || []).filter((e) => e.inferred).length;
    const note = inferred
      ? '<span style="color:var(--accent-alt)">' + inferred + ' link' +
        (inferred === 1 ? '' : 's') + ' inferred</span> &middot; LLDP/CDP/MNDP'
      : 'LLDP/CDP/MNDP';
    footEl.innerHTML = legend +
      '<span style="margin-left:auto;text-align:right">' + parts.join(' &middot; ') +
      (parts.length ? ' &middot; ' : '') + note + '</span>';
  }

  // ── detail panel ──────────────────────────────────────────────────────────

  function row(k: string, v: unknown): string {
    if (v === undefined || v === null || v === '') return '';
    return '<dt>' + esc(k) + '</dt><dd>' + esc(v) + '</dd>';
  }

  /** The name of the device this one sits behind. */
  function parentName(n: TopoNode): string {
    if (!data) return '';
    if (n.kind !== 'core' && n.parent === 'core') return data.nodes[0]?.name || '';
    if (n.kind === 'core' || !n.parent) return '';
    for (const m of data.nodes) if (m.key === n.parent) return m.name || n.parent;
    return n.parent;
  }

  function statusVar(n: TopoNode): string {
    if (n.kind === 'core') return '--accent-rx';
    if (n.status === 'up') return '--accent-ok';
    if (n.status === 'warn') return '--accent-warn';
    if (n.status === 'down') return '--accent-err';
    return '--text-muted';
  }

  function selectNode(key: string | null): void {
    sel = key;
    renderPanel();
    if (data) renderNodes(visibleNodes(data.nodes));
  }

  function renderClientPanel(panel: HTMLElement, n: TopoClientNode): void {
    panel.innerHTML =
      '<div class="topo-panel-hdr" style="color:var(' +
        (n.type === 'wifi-client' ? '--accent-rx' : '--accent-tx') + ')">' +
        '<svg viewBox="0 0 24 24">' + glyph(n.type === 'wifi-client' ? 'ap' : 'station') + '</svg>' +
        '<span class="topo-panel-name">' + esc(n.name || n.mac) + '</span>' +
        '<button class="topo-panel-close" id="topoPanelClose" aria-label="Close">&times;</button>' +
      '</div>' +
      '<div class="topo-badges"><span class="topo-badge">' +
        (n.type === 'wifi-client' ? 'Wi-Fi client' : 'Wired client') + '</span>' +
        (n.vlanNames || []).map((v) => '<span class="topo-badge is-vlan">' + esc(v) + '</span>').join('') +
        // Say plainly when the attachment was DEDUCED from a shared port rather
        // than observed, so a wrong guess is visible rather than silent.
        (n.attrib === 'port'
          ? '<span class="topo-badge is-guess" title="Deduced: this device shares a ' +
            'port with that switch. The router cannot see which switch port.">inferred</span>'
          : '') +
      '</div>' +
      '<dl class="topo-kv">' +
        row('IPv4', n.ip) + row('MAC', n.mac) +
        row('VLAN', (n.vlanNames || []).join(', ')) +
        row('Connected to', parentName(n) || 'this router') +
        row('Via', n.port) + row('SSID', n.ssid) +
        row('Signal', n.signal ? n.signal + ' dBm' : '') +
        row('Uptime', n.uptime) +
      '</dl>';
  }

  function renderPanel(): void {
    const panel = byId('topoPanel');
    if (!panel) return;
    if (!sel || !data) { panel.className = 'topo-panel'; return; }
    const n = data.nodes.find((m) => m.key === sel);
    if (!n) { panel.className = 'topo-panel'; return; }

    // The rate on the link INTO this device, so the panel shows the throughput
    // of the cable it hangs off rather than the router's total.
    let rate: Rate | null = null;
    (data.edges || []).forEach((e) => {
      if (e.to === n.key) {
        const r = rateFor(e.iface);
        if (r) rate = r;
      }
    });

    const closeBtn = (): void => {
      const c = byId('topoPanelClose');
      if (c) c.addEventListener('click', () => selectNode(null));
    };

    if (n.kind === 'client') {
      renderClientPanel(panel, n);
      panel.className = 'topo-panel open';
      closeBtn();
      return;
    }

    let badges = '<span class="topo-badge">' + esc(TYPE_LABEL[n.type] || n.type) + '</span>';
    // A GUESS IS LABELLED A GUESS. `caps` means the device advertised what it
    // is; anything else means the type came from its board name or platform.
    if (n.kind !== 'core' && n.typeSource !== 'caps') {
      badges += '<span class="topo-badge is-guess" title="Inferred from the board or platform — this ' +
        'device did not advertise LLDP capabilities">guessed</span>';
    }
    if (n.gone) {
      badges += '<span class="topo-badge" style="border-color:var(--accent-err);color:var(--accent-err)">offline</span>';
    }
    (n.running || []).forEach((r) => { badges += '<span class="topo-badge">' + esc(r) + '</span>'; });

    let live = '';
    if (n.kind !== 'core') {
      live += row('Latency', data.pingDenied ? 'unavailable (test policy)'
        : (n.rtt !== null && isFinite(n.rtt) ? n.rtt.toFixed(1) + ' ms' : '—'));
      live += row('Loss', n.loss !== null && isFinite(n.loss) ? n.loss + '%' : '—');
    } else {
      live += row('CPU', n.cpuLoad !== null && isFinite(n.cpuLoad) ? n.cpuLoad + '%' : '');
      live += row('Memory', n.memPct !== null && isFinite(n.memPct) ? n.memPct + '%' : '');
    }
    if (rate) {
      live += row('Link down', fmtMbps((rate as Rate).rx));
      live += row('Link up', fmtMbps((rate as Rate).tx));
    }

    // What the device ENABLES, falling back to what it merely supports — the
    // same preference the collector applies when classifying it.
    const caps = (n.capsEnabled && n.capsEnabled.length ? n.capsEnabled : (n.caps || [])).join(', ');

    panel.innerHTML =
      '<div class="topo-panel-hdr" style="color:var(' + statusVar(n) + ')">' +
        '<svg viewBox="0 0 24 24">' + glyph(n.type) + '</svg>' +
        '<span class="topo-panel-name">' + esc(n.name || n.key) + '</span>' +
        '<button class="topo-panel-close" id="topoPanelClose" aria-label="Close">&times;</button>' +
      '</div>' +
      '<div class="topo-badges">' + badges + '</div>' +
      '<dl class="topo-kv">' +
        row('IPv4', n.ip) + row('IPv6', n.ip6) + row('MAC', n.mac) +
        row('Board', n.board) + row('Platform', n.platform) + row('Version', n.version) +
        row('Software ID', n.softwareId) + row('Uptime', n.uptime) +
      '</dl>' +
      (live ? '<div class="topo-panel-sec">Live</div><dl class="topo-kv">' + live + '</dl>' : '') +
      '<div class="topo-panel-sec">Discovery</div>' +
      '<dl class="topo-kv">' +
        row('Behind', parentName(n)) +
        row('Router port', n.port || (n.ifaces || []).join(', ')) +
        row('Remote port', n.remoteIface) +
        row('Seen via', (n.via || []).join(', ')) +
        row('Age', n.ageSec !== null && isFinite(n.ageSec) ? n.ageSec + ' s'
          : (n.gone ? 'no longer advertising' : '')) +
        row('Capabilities', caps || (n.kind === 'core' ? '' : 'none advertised')) +
        row('Description', n.description) +
      '</dl>';

    panel.className = 'topo-panel open';
    closeBtn();
  }

  // ── viewport ──────────────────────────────────────────────────────────────

  function applyView(): void {
    attr(gViewport, 'transform',
      'translate(' + view.x.toFixed(1) + ',' + view.y.toFixed(1) + ') scale(' + view.k.toFixed(3) + ')');
  }

  function saveView(): void {
    if (rid) lsSet('mikrodash.topo.view.' + rid, view);
  }

  /** Frame every node, with padding, clamped so one distant orphan cannot zoom
   *  the whole map down to nothing. */
  function fitView(): void {
    const stage = byId('topoStage');
    const keys = Object.keys(pos);
    if (!keys.length || !stage) return;
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    keys.forEach((k) => {
      const p = pos[k]!;
      minX = Math.min(minX, p.x); maxX = Math.max(maxX, p.x);
      minY = Math.min(minY, p.y); maxY = Math.max(maxY, p.y);
    });
    const pad = 80;
    const w = (maxX - minX) + pad * 2, h = (maxY - minY) + pad * 2;
    const rect = stage.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    const k = Math.min(rect.width / Math.max(w, 1), rect.height / Math.max(h, 1));
    view.k = Math.max(0.35, Math.min(1.6, k));
    view.x = rect.width / 2 - ((minX + maxX) / 2) * view.k;
    view.y = rect.height / 2 - ((minY + maxY) / 2) * view.k;
    applyView();
    saveView();
  }

  // ── persistence ───────────────────────────────────────────────────────────
  //
  // TWO STORES, DELIBERATELY. localStorage answers instantly so a returning
  // viewer sees their layout before the network does anything, and the server
  // copy is what makes a dragged map survive a different browser. The fetch
  // result overwrites the local copy because the server is the shared truth;
  // a failure is silent because the local copy is already applied.

  function loadSaved(): void {
    if (!rid) return;
    Object.assign(saved, lsGet<Record<string, Pos>>('mikrodash.topo.pos.' + rid, {}) || {});
    const v = lsGet<{ k: number; x: number; y: number } | null>('mikrodash.topo.view.' + rid, null);
    if (v && isFinite(v.k)) { view = v; applyView(); }

    fetch('/api/topology-layout?routerId=' + encodeURIComponent(rid), { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((j: { positions?: Record<string, Pos> } | null) => {
        if (!j || !j.positions) return;
        for (const k of Object.keys(saved)) delete saved[k];
        Object.assign(saved, j.positions);
        lsSet('mikrodash.topo.pos.' + rid, saved);
        render();
      })
      .catch(() => { /* the localStorage copy is already applied */ });
  }

  function savePositions(): void {
    if (!rid) return;
    lsSet('mikrodash.topo.pos.' + rid, saved);
    if (saveTimer !== undefined) clearTimeout(saveTimer);
    // Debounced: a drag emits a stream of positions and the server needs the
    // one the viewer stopped at, not every frame on the way there.
    saveTimer = setTimeout(() => {
      fetch('/api/topology-layout', {
        method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ routerId: rid, positions: saved }),
      }).catch(() => { /* the local copy stands */ });
    }, 800) as unknown as number;
  }

  function scheduleFrame(fn: () => void): void {
    if (rafId !== null) return;
    rafId = requestAnimationFrame(() => { rafId = null; fn(); });
  }

  /** The ~5 s path: geometry is unchanged, only rates moved. */
  function renderLive(): void {
    if (!data || draggingKey) return;
    renderEdges(visibleEdges());
    renderStats();
  }

  function render(): void {
    if (!data) { renderEmpty(); return; }
    // NEVER re-render mid-drag: the node would be yanked back to its computed
    // position under the pointer. The frame is deferred instead.
    if (draggingKey) { pendingRender = true; return; }
    const nodes = visibleNodes(data.nodes);
    applyPositions(nodes);
    syncVlanOptions();
    renderEdges(visibleEdges());
    renderNodes(nodes);
    renderStats();
    renderEmpty();
    renderFoot();
    renderPanel();
  }

  /**
   * Forget the placement of nodes LEAVING the canvas, so they are laid out again
   * relative to their parent's current position next time — rather than
   * reappearing where the parent used to be.
   */
  function unpinClientsOf(parentKey: string | null): void {
    if (!data) return;
    data.nodes.forEach((n) => {
      if (n.kind !== 'client') return;
      if (parentKey && n.parent !== parentKey) return;
      delete placed[n.key];
    });
  }

  function clearFlow(): void {
    Object.keys(flowEls).forEach(removeFlow);
  }

  // ── interaction ───────────────────────────────────────────────────────────

  function svgPoint(evt: PointerEvent | WheelEvent): Pos {
    const rect = svg!.getBoundingClientRect();
    return {
      x: (evt.clientX - rect.left - view.x) / view.k,
      y: (evt.clientY - rect.top - view.y) / view.k,
    };
  }

  function wireInteraction(): void {
    if (!svg) return;
    const el = svg;
    let dragStart: { px: number; py: number; nx: number; ny: number } | null = null;
    let panStart: { x: number; y: number; vx: number; vy: number } | null = null;
    let moved = 0;

    el.addEventListener('pointerdown', (e) => {
      const target = e.target as Element | null;
      const nodeEl = target?.closest?.('.topo-node') as SVGElement | null;
      moved = 0;
      // THE COUNT CHIP TOGGLES that device's clients instead of starting a drag,
      // so expanding is one click on the very thing showing the number.
      const chipEl = target?.closest?.('.topo-chip-g');
      if (chipEl && nodeEl) {
        const k = nodeEl.getAttribute('data-key') || '';
        if (expanded[k]) {
          delete expanded[k];
          unpinClientsOf(k); // so a later expand starts from where the parent is NOW
        } else {
          expanded[k] = true;
        }
        render();
        return;
      }
      if (nodeEl) {
        draggingKey = nodeEl.getAttribute('data-key');
        const p = svgPoint(e);
        const cur = pos[draggingKey || ''] || { x: 0, y: 0 };
        dragStart = { px: p.x, py: p.y, nx: cur.x, ny: cur.y };
        nodeEl.classList.add('is-dragging');
        nodeEl.classList.remove('is-animated');
      } else {
        panStart = { x: e.clientX, y: e.clientY, vx: view.x, vy: view.y };
        el.classList.add('is-panning');
      }
      try { el.setPointerCapture(e.pointerId); } catch { /* not all pointers capture */ }
    });

    el.addEventListener('pointermove', (e) => {
      if (draggingKey && dragStart) {
        const p = svgPoint(e);
        moved = Math.max(moved, Math.abs(p.x - dragStart.px) + Math.abs(p.y - dragStart.py));
        pos[draggingKey] = {
          x: dragStart.nx + (p.x - dragStart.px),
          y: dragStart.ny + (p.y - dragStart.py),
        };
        scheduleFrame(() => {
          const g = nodeEls[draggingKey || ''];
          if (!g) return;
          const q = pos[draggingKey || '']!;
          attr(g, 'transform', 'translate(' + q.x.toFixed(1) + ',' + q.y.toFixed(1) + ')');
          if (data) renderEdges(visibleEdges());
        });
      } else if (panStart) {
        moved = Math.max(moved, Math.abs(e.clientX - panStart.x) + Math.abs(e.clientY - panStart.y));
        view.x = panStart.vx + (e.clientX - panStart.x);
        view.y = panStart.vy + (e.clientY - panStart.y);
        scheduleFrame(applyView);
      }
    });

    const endPointer = (e: PointerEvent): void => {
      if (draggingKey) {
        const key = draggingKey;
        nodeEls[key]?.classList.remove('is-dragging');
        draggingKey = null;
        dragStart = null;
        // FOUR PIXELS SEPARATES A CLICK FROM A DRAG. Without a threshold every
        // selection nudged the node it selected.
        if (moved < 4) {
          selectNode(key === sel ? null : key);
        } else {
          saved[key] = pos[key]!;
          savePositions();
          // Re-lay out immediately: anything hanging off this node is positioned
          // relative to it, and without this the children only catch up when the
          // next update happens to arrive.
          pendingRender = true;
        }
        if (pendingRender) { pendingRender = false; render(); }
      } else if (panStart) {
        el.classList.remove('is-panning');
        panStart = null;
        if (moved < 4) selectNode(null);
        saveView();
      }
      try { el.releasePointerCapture(e.pointerId); } catch { /* already released */ }
    };
    el.addEventListener('pointerup', endPointer);
    el.addEventListener('pointercancel', endPointer);

    // Zoom about the POINTER, not the centre: zooming toward the cursor is what
    // makes a map feel like it is being examined rather than scaled.
    el.addEventListener('wheel', (e) => {
      e.preventDefault();
      const rect = el.getBoundingClientRect();
      const px = e.clientX - rect.left, py = e.clientY - rect.top;
      const k0 = view.k;
      const k1 = Math.max(0.35, Math.min(3, k0 * Math.exp(-e.deltaY * 0.0015)));
      if (k1 === k0) return;
      view.x = px - (px - view.x) * (k1 / k0);
      view.y = py - (py - view.y) * (k1 / k0);
      view.k = k1;
      scheduleFrame(applyView);
      saveView();
    }, { passive: false });

    const zoomBy = (f: number): void => {
      const rect = el.getBoundingClientRect();
      const px = rect.width / 2, py = rect.height / 2;
      const k0 = view.k, k1 = Math.max(0.35, Math.min(3, k0 * f));
      view.x = px - (px - view.x) * (k1 / k0);
      view.y = py - (py - view.y) * (k1 / k0);
      view.k = k1;
      applyView();
      saveView();
    };
    byId('topoZoomIn')?.addEventListener('click', () => zoomBy(1.25));
    byId('topoZoomOut')?.addEventListener('click', () => zoomBy(0.8));
    byId('topoFit')?.addEventListener('click', fitView);

    byId('topoRelayout')?.addEventListener('click', () => {
      for (const k of Object.keys(saved)) delete saved[k];
      for (const k of Object.keys(placed)) delete placed[k];
      pos = {};
      Object.keys(nodeEls).forEach((k) => nodeEls[k]!.classList.add('is-animated'));
      savePositions();
      render();
      setTimeout(fitView, 60);
    });

    // FILTERING RE-RUNS THE FULL RENDER, not just the node pass: a search has to
    // be able to surface a client whose parent is collapsed, which changes which
    // nodes exist on the canvas at all.
    const elSearch = byId<HTMLInputElement>('topoSearch');
    elSearch?.addEventListener('input', () => {
      filter = elSearch.value.toLowerCase().trim();
      render();
    });
    const elType = byId<HTMLSelectElement>('topoType');
    elType?.addEventListener('input', () => {
      typeFilter = elType.value;
      render();
    });
    const elVlan = byId<HTMLSelectElement>('topoVlan');
    elVlan?.addEventListener('input', () => {
      vlanFilter = elVlan.value;
      render();
    });

    const elFlowBtn = byId('topoFlowBtn');
    elFlowBtn?.addEventListener('click', () => {
      showFlow = !showFlow;
      elFlowBtn.classList.toggle('is-on', showFlow);
      if (!showFlow) clearFlow();
      if (data) renderEdges(visibleEdges());
    });
    const elRateBtn = byId('topoRateBtn');
    elRateBtn?.addEventListener('click', () => {
      showRates = !showRates;
      elRateBtn.classList.toggle('is-on', showRates);
      if (data) renderEdges(visibleEdges());
    });
    const elClientsBtn = byId('topoClientsBtn');
    elClientsBtn?.addEventListener('click', () => {
      showClients = !showClients;
      elClientsBtn.classList.toggle('is-on', showClients);
      // Turning the global toggle OFF also drops per-device expansions, so the
      // button always means what it says.
      if (!showClients) {
        for (const k of Object.keys(expanded)) delete expanded[k];
        unpinClientsOf(null);
      }
      render();
      setTimeout(fitView, 60);
    });

    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && sel && isVisible('network-topology')) selectNode(null);
    });
  }

  // ── animation pause discipline ────────────────────────────────────────────
  //
  // SMIL keeps running in a hidden tab otherwise, burning CPU on something
  // nobody can see. Matches how the dashboard's network diagram is handled.

  function setAnimations(on: boolean): void {
    if (!svg) return;
    try {
      if (on) svg.unpauseAnimations();
      else svg.pauseAnimations();
    } catch { /* not every engine implements SMIL control */ }
  }

  function syncAnimations(): void {
    setAnimations(!document.hidden && isVisible('network-topology'));
  }

  // ── boot ──────────────────────────────────────────────────────────────────

  applyView();
  wireInteraction();
  renderEmpty();

  socket.on('topology:update', (p: TopologyPayload & { routerId?: string }) => {
    if (!p) return;
    // A ROUTER SWITCH resets the layout, not just the data: positions are saved
    // per router, and keeping them would draw one network with another's map.
    const firstForRouter = rid !== (p.routerId || null);
    data = p;
    if (firstForRouter) {
      rid = p.routerId || null;
      pos = {};
      for (const k of Object.keys(saved)) delete saved[k];
      for (const k of Object.keys(placed)) delete placed[k];
      for (const k of Object.keys(expanded)) delete expanded[k];
      clearFlow();
      loadSaved();
    }
    if (isVisible('network-topology')) {
      render();
      if (firstForRouter) setTimeout(fitView, 40);
    }
  });

  // Link rates ride in on the INTERFACE collector, which the browser already
  // receives router-wide — no extra subscription and no extra router load.
  socket.on('ifstatus:update',
    (p: { interfaces?: Array<{ name: string; rxMbps: number; txMbps: number; running: boolean }> }) => {
      if (!p || !Array.isArray(p.interfaces)) return;
      const next: Record<string, Rate> = {};
      p.interfaces.forEach((i) => {
        next[i.name] = { rx: Number(i.rxMbps) || 0, tx: Number(i.txMbps) || 0, running: !!i.running };
      });
      rates = next;
      if (isVisible('network-topology')) scheduleFrame(renderLive);
    });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'network-topology') {
      render();
      if (data) setTimeout(fitView, 40);
    }
    syncAnimations();
  });

  document.addEventListener('visibilitychange', syncAnimations);
  socket.on('disconnect', () => setAnimations(false));
  socket.on('connect', syncAnimations);
  window.addEventListener('resize', () => {
    if (isVisible('network-topology')) scheduleFrame(applyView);
  });

  syncAnimations();
}
