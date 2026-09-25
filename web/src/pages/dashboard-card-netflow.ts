/**
 * The Dashboard's Network Flow card: particles flowing between the LAN links,
 * the router and the WAN.
 *
 * ── THE ANIMATION IS SCALED TO THE ROUTER'S CONFIGURED CAPACITY ────────────
 *
 * Not to an absolute curve. 40 Mbps is most of a 50 Mbps line and a rounding
 * error on a gigabit one, so a fixed "10 kb/s to 1 Gb/s" ramp would show a
 * saturated small link as idle and a busy big one as saturated. The capacity is
 * the pair of numbers the operator set per device in Settings, read through
 * `bwCapacityMbps()` so a router switch rescales with it.
 *
 * ── WAN RX AND TX DRIVE EVERYTHING ────────────────────────────────────────
 *
 * The WAN sample is what the Dashboard already receives every second, and it is
 * the number an operator recognises. The two LAN lanes are driven from the same
 * pair, split by client count, because per-interface rates would cost a second
 * subscription for a decoration. See `update`.
 *
 * ── ONE INSTANCE, AND ONLY ONE ─────────────────────────────────────────────
 *
 * `mountNetFlow` latches. It injects `<g>` elements into the SVG and starts a
 * requestAnimationFrame loop; a second instance would inject a second set and
 * run a second loop over the same nodes, which reads as the card animating at
 * double speed and never stops.
 */

import { el } from '../dom';
import { bwCapacityMbps } from './dashboard-card-bandwidth';

const NS = 'http://www.w3.org/2000/svg';

/** One link's live state, as the caller knows it. */
export interface NetFlowLink {
  clients?: number;
  /** Bits per second TOWARD the clients: router TX on the LAN, RX on the WAN. */
  down?: number;
  /** Bits per second toward the internet. */
  up?: number;
  ip?: string;
}
export interface NetFlowUpdate {
  wired?: NetFlowLink;
  wireless?: NetFlowLink;
  wan?: NetFlowLink;
}

type LinkKey = 'wired' | 'wireless' | 'wan';

const TRAIL = 7, SPACING = 3.2, LANE = 4;

interface Particle { g: SVGGElement; dots: SVGCircleElement[]; s: number; v: number; hit?: boolean }
interface Lane {
  key: LinkKey; dir: 1 | -1; path: SVGPathElement; len: number;
  /** The fraction of capacity this lane is carrying, 0..1. */
  load: number;
  acc: number; speed: number; dest: string; parts: Particle[]; pool: Particle[];
}

const LINKS: Record<LinkKey, { color: string; a: [number, number]; b: [number, number]; ends: [string, string] }> = {
  wired: { color: '#38bdf8', a: [176, 84], b: [312, 140], ends: ['wired', 'router'] },
  wireless: { color: '#a78bfa', a: [176, 216], b: [312, 160], ends: ['wireless', 'router'] },
  wan: { color: '#34d399', a: [448, 150], b: [584, 150], ends: ['router', 'wan'] },
};

const clamp = (v: number, a: number, b: number): number => Math.max(a, Math.min(b, v));

// ── THE IDS ARE WRITTEN OUT, NOT BUILT UP ─────────────────────────────────
//
// `'nf-glow-' + key` is shorter and is the reason `TestDashboardMarkupIsDriven`
// reported every one of these as dead: an id assembled at runtime cannot be
// found by grepping, by that ledger, or by the next person wondering whether a
// node in the markup is still used. Spelling them out is what makes the markup
// and this file searchable from each other.
const GLOW_ID: Record<string, string> = {
  wired: 'nf-glow-wired', wireless: 'nf-glow-wireless',
  router: 'nf-glow-router', wan: 'nf-glow-wan',
};
const LED_ID: Record<LinkKey, string> = {
  wired: 'nf-led-wired', wireless: 'nf-led-wireless', wan: 'nf-led-wan',
};
const CNT_ID = { wired: 'nf-cnt-wired', wireless: 'nf-cnt-wireless' } as const;
const WAN_IP_ID = 'nf-wan-ip';

/** Lighten a hex colour toward white, for the upload lane. */
function tint(hex: string, t: number): string {
  const n = parseInt(hex.slice(1), 16);
  const c = [n >> 16, (n >> 8) & 255, n & 255].map((v) => Math.round(v + (255 - v) * t));
  return 'rgb(' + c.join(',') + ')';
}

/**
 * How busy a lane is, as a FRACTION OF THE CONFIGURED CAPACITY.
 *
 * Exported so the scale can be asserted rather than eyeballed. A zero or
 * missing capacity returns 0 rather than dividing: a router with no capacity
 * set is "unknown", and showing unknown as saturated is the wrong direction.
 */
export function loadFraction(bitsPerSec: number | undefined, capacityMbps: number): number {
  if (!(bitsPerSec! > 0) || !(capacityMbps > 0)) return 0;
  return clamp(bitsPerSec! / (capacityMbps * 1e6), 0, 1);
}

let mounted = false;
let live: ((d: NetFlowUpdate) => void) | null = null;

/**
 * Feed the card. A NO-OP BEFORE IT IS MOUNTED, and that is the point: the
 * wired count, the wireless count and the WAN IP each arrive from a different
 * handler on a different schedule, and any of them can land before the
 * Dashboard markup exists. A caller that had to check first would be four
 * copies of the same guard.
 */
export function netFlowUpdate(d: NetFlowUpdate): void {
  if (live) { live(d); return; }
  // ── UNMOUNTED, THE CARD DEGRADES TO STATIC NUMBERS ────────────────────
  //
  // The mount is refused where there is no SVG to animate. The text nodes are
  // still in the markup, though, and the counts and the WAN address are the
  // part an operator actually reads - so they are written directly rather than
  // dropped. Without this the card showed zeros forever anywhere the animation
  // could not run, which is a worse failure than a still picture.
  //
  // It is not a second writer racing the first: this branch runs only when
  // there is no first.
  const put = (id: string, v: string | number): void => {
    const n = el(id);
    if (n) n.textContent = String(v);
  };
  if (d.wired?.clients != null) put(CNT_ID.wired, d.wired.clients);
  if (d.wireless?.clients != null) put(CNT_ID.wireless, d.wireless.clients);
  if (d.wan?.ip) put(WAN_IP_ID, d.wan.ip);
}

/**
 * Drive every lane from the WAN pair, in bits per second.
 *
 * ── THE SPLIT HAPPENS HERE BECAUSE THE COUNTS ARE HERE ────────────────────
 *
 * The two LAN lanes are the WAN pair apportioned BY CLIENT COUNT, not measured.
 * Doing it in the caller would mean keeping a second copy of the wired and
 * wireless counts in the Dashboard module, updated from two more handlers, and
 * the two copies would disagree the first time one of them missed an event.
 *
 * TODO: per-interface rates do exist - `ifstatus:update` carries `rxMbps` and
 * `txMbps` per interface - but that event goes to the Interfaces room, and
 * subscribing the Dashboard to it would open a second stream for a decoration.
 * If those numbers are ever on the Dashboard for another reason, read them
 * here instead of apportioning.
 *
 * NO CLIENTS ON EITHER SIDE is an even split rather than nothing: the traffic
 * is real and is crossing one of them, and showing both idle while the WAN is
 * busy is the reading that is certainly wrong.
 */
export function netFlowWan(downBits: number, upBits: number): void {
  if (!live) return;
  const w = liveCounts.wired, l = liveCounts.wireless;
  const total = w + l;
  const share = total > 0 ? w / total : .5;
  live({
    wan: { down: downBits, up: upBits },
    wired: { down: downBits * share, up: upBits * share },
    wireless: { down: downBits * (1 - share), up: upBits * (1 - share) },
  });
}

/** The counts the apportioning reads, mirrored out of the mounted instance. */
const liveCounts = { wired: 0, wireless: 0 };

export function mountNetFlow(): { update: (d: NetFlowUpdate) => void } | null {
  if (mounted) return null;
  const svg = el('netDiagram') as unknown as SVGSVGElement | null;
  if (!svg) return null;
  // ── IT NEEDS A REAL SVG, AND SAYS SO RATHER THAN THROWING ──────────────
  //
  // Everything below builds nodes with `createElementNS` and measures paths
  // with `getTotalLength`. A document without them - the test harness's DOM
  // shim, and any renderer that stubs part of the DOM - would throw partway
  // through, after the latch was set and after some nodes had been injected.
  // Refusing up front leaves the card as static markup, which is exactly what
  // it should be where nothing can animate it.
  if (typeof document.createElementNS !== 'function'
    || typeof svg.querySelector !== 'function') return null;
  mounted = true;

  const reduce = typeof matchMedia === 'function'
    && matchMedia('(prefers-reduced-motion: reduce)').matches;
  const pick = (id: string): Element | null =>
    (svg.getElementById ? svg.getElementById(id) : document.getElementById(id)) as Element | null;
  const mk = <T extends SVGElement>(tag: string, a: Record<string, string | number>, p?: Element): T => {
    const e = document.createElementNS(NS, tag) as T;
    for (const k in a) e.setAttribute(k, String(a[k]));
    if (p) p.appendChild(e);
    return e;
  };

  // REDUCED MOTION removes the SMIL globe too, not just the particles: the
  // request is about movement, not about which technology draws it.
  if (reduce) svg.querySelectorAll('animate').forEach((a) => a.remove());

  const nodes = pick('nf-nodes');
  const gTracks = mk<SVGGElement>('g', {});
  const gDots = mk<SVGGElement>('g', { filter: 'url(#nf-glow)' });
  if (nodes) { svg.insertBefore(gTracks, nodes); svg.insertBefore(gDots, nodes); }
  const defs = svg.querySelector('defs');

  const curve = (a: [number, number], b: [number, number], o: number): string => {
    const m = (b[0] - a[0]) / 2;
    return `M${a[0]} ${a[1] + o}C${a[0] + m} ${a[1] + o} ${b[0] - m} ${b[1] + o} ${b[0]} ${b[1] + o}`;
  };

  const lanes: Lane[] = [];
  const tracks: Record<string, { tube: SVGPathElement; lines: SVGPathElement[] }> = {};

  (Object.keys(LINKS) as LinkKey[]).forEach((key) => {
    const L = LINKS[key];
    const gid = 'nf-g-' + key;
    if (defs) {
      const g = mk<SVGElement>('linearGradient',
        { id: gid, gradientUnits: 'userSpaceOnUse', x1: L.a[0], y1: 0, x2: L.b[0], y2: 0 }, defs);
      const pair = key === 'wan' ? ['#7dd3fc', L.color] : [L.color, '#7dd3fc'];
      mk('stop', { offset: 0, 'stop-color': pair[0]! }, g);
      mk('stop', { offset: 1, 'stop-color': pair[1]! }, g);
    }
    const tube = mk<SVGPathElement>('path', {
      d: curve(L.a, L.b, 0), fill: 'none', stroke: `url(#${gid})`,
      'stroke-width': 14, 'stroke-linecap': 'round', opacity: .05,
    }, gTracks);
    tracks[key] = { tube, lines: [] };
    ([[1, -LANE], [-1, LANE]] as [1 | -1, number][]).forEach(([dir, o]) => {
      const path = mk<SVGPathElement>('path', {
        d: curve(L.a, L.b, o), fill: 'none', stroke: `url(#${gid})`, 'stroke-width': 1, opacity: .22,
      }, gTracks);
      tracks[key]!.lines.push(path);
      lanes.push({
        key, dir, path, len: path.getTotalLength(), load: 0, acc: Math.random(), speed: 70,
        dest: dir > 0 ? L.ends[1] : L.ends[0], parts: [], pool: [],
      });
      // The upload lane is the paler tint, so the two directions read apart
      // without a legend.
      path.dataset.colour = dir > 0 ? tint(L.color, .55) : L.color;
    });
  });

  function spawn(l: Lane): void {
    let p = l.pool.pop();
    if (!p) {
      const g = mk<SVGGElement>('g', {}, gDots);
      const dots: SVGCircleElement[] = [];
      const colour = l.path.dataset.colour || LINKS[l.key].color;
      for (let i = 0; i < TRAIL; i++) {
        dots.push(mk<SVGCircleElement>('circle',
          { r: (2.6 * (1 - (i / TRAIL) * .7)).toFixed(2), fill: colour }, g));
      }
      p = { g, dots, s: 0, v: 0 };
    }
    p.g.style.display = '';
    p.s = 0;
    p.v = l.speed * (.85 + Math.random() * .3);
    l.parts.push(p);
  }

  const energy: Record<string, number> = { wired: 0, wireless: 0, router: 0, wan: 0 };
  const counts = { wired: 0, wireless: 0 };
  const shown = { wired: 0, wireless: 0 };

  let last = performance.now();
  function frame(t: number): void {
    const dt = Math.min(.05, (t - last) / 1000);
    last = t;
    for (const l of lanes) {
      // ── LOAD, NOT AN ABSOLUTE RATE ────────────────────────────────────
      //
      // `load` is already 0..1 against the configured capacity, so a saturated
      // 50 Mbps line and a saturated gigabit one both run at full tilt. The
      // square root front-loads the low end: the difference between idle and
      // lightly busy is the one an operator glances for.
      const a = Math.sqrt(l.load);
      const pps = a * 6;                         // particles per second, 0..6
      l.speed = 60 + a * 170;                    // px/s, 60..230
      if (!reduce && pps > 0) {
        l.acc += pps * dt * (.6 + Math.random() * .8);
        while (l.acc >= 1) { l.acc -= 1; spawn(l); }
      }
      for (let i = l.parts.length - 1; i >= 0; i--) {
        const p = l.parts[i]!;
        p.s += p.v * dt;
        if (p.s - TRAIL * SPACING >= l.len) {
          p.g.style.display = 'none';
          l.parts.splice(i, 1);
          l.pool.push(p);
          continue;
        }
        if (p.s >= l.len && !p.hit) {
          p.hit = true;
          energy[l.dest] = Math.min(1, (energy[l.dest] || 0) + .14);
        }
        if (p.s < l.len) p.hit = false;
        p.dots.forEach((d, k) => {
          const s = p.s - k * SPACING;
          if (s < 0 || s > l.len) { d.setAttribute('opacity', '0'); return; }
          const pt = l.path.getPointAtLength(l.dir > 0 ? s : l.len - s);
          const edge = Math.min(1, s / 14, (l.len - s) / 14);
          d.setAttribute('cx', pt.x.toFixed(1));
          d.setAttribute('cy', pt.y.toFixed(1));
          d.setAttribute('opacity', (edge * Math.pow(1 - k / TRAIL, 1.6)).toFixed(2));
        });
      }
    }
    for (const k in energy) {
      energy[k] = (energy[k] || 0) * Math.exp(-dt * 2.5);
      const gid = GLOW_ID[k];
      if (gid) pick(gid)?.setAttribute('opacity', ((energy[k] || 0) * .9).toFixed(2));
    }
    (['wired', 'wireless'] as const).forEach((k) => {
      shown[k] += (counts[k] - shown[k]) * Math.min(1, dt * 6);
      const n = pick(CNT_ID[k]);
      if (n) n.textContent = String(Math.round(shown[k]));
    });
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);

  function update(d: NetFlowUpdate): void {
    const cap = bwCapacityMbps();
    (['wired', 'wireless', 'wan'] as LinkKey[]).forEach((k) => {
      const x = d[k];
      if (!x) return;
      // `down` is toward the clients, so it is measured against the DOWNLOAD
      // capacity on every link; `up` against the upload one.
      const dl = loadFraction(x.down, cap.down);
      const ul = loadFraction(x.up, cap.up);
      lanes.forEach((l) => { if (l.key === k) l.load = l.dir > 0 ? ul : dl; });
      const a = Math.max(dl, ul);
      pick(LED_ID[k])?.setAttribute('opacity', (.2 + .8 * Math.sqrt(a)).toFixed(2));
      const tr = tracks[k];
      if (tr) {
        tr.tube.setAttribute('opacity', (.03 + .09 * a).toFixed(3));
        tr.lines.forEach((p) => p.setAttribute('opacity', (.14 + .3 * a).toFixed(2)));
      }
      if (x.clients != null && k !== 'wan') {
        const ck = k as 'wired' | 'wireless';
        counts[ck] = x.clients;
        liveCounts[ck] = x.clients;
      }
    });
    if (d.wan?.ip) {
      const n = pick(WAN_IP_ID);
      if (n) n.textContent = d.wan.ip;
    }
  }

  live = update;
  return { update };
}
