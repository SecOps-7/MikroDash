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
  // The LAN endpoints follow their boxes: the markup translates them by -24 and
  // +24 into the taller view, and a curve left behind would start in mid-air
  // beside the node it belongs to.
  // The LAN curves start at the boxes' right edge, which is 176 once the markup's
  // `translate(-14)` is applied. Only the vertical moved: each box is translated
  // 16 into the taller view, and growing the rects changed their height about a
  // fixed centre, so this is the centre plus that translate.
  wired: { color: '#38bdf8', a: [176, 68], b: [312, 140], ends: ['wired', 'router'] },
  wireless: { color: '#a78bfa', a: [176, 232], b: [312, 160], ends: ['wireless', 'router'] },
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
// ── THE ROUTER LEDs ARE CONSTANT ──────────────────────────────────────────
//
// They used to dim with the lane's load. A real router's port lights are on
// while the link is up; dimming them made an idle line look like a fault, and
// the card already says how busy a link is three other ways - the particles,
// their speed, and the lane opacity. The markup sets them lit and nothing
// touches them, so the ids are no longer read from here.
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

/** Drive the WAN lane, in bits per second. */
export function netFlowWan(downBits: number, upBits: number): void {
  netFlowUpdate({ wan: { down: downBits, up: upBits } });
}

/** One interface as `ifstatus:update` carries it. */
export interface FlowInterface {
  name: string;
  type: string;
  running: boolean;
  disabled: boolean;
  rxMbps: number;
  txMbps: number;
}

/**
 * The LAN lanes, MEASURED per interface.
 *
 * ── WHAT THIS REPLACED ─────────────────────────────────────────────────────
 *
 * The two LAN lanes were once the WAN pair split by CLIENT COUNT. With 5 wired
 * ports and 31 wireless clients that gave wired 13.9% of the traffic whatever
 * it was actually carrying, and it showed: measurement found the ports moving
 * 1.06 Mb/s down against wireless's 0.42, so the count had the busier side
 * backwards. One desktop out-carries thirty idle phones and a count cannot know
 * that.
 *
 * `ifstatus:update` carries `rxMbps` and `txMbps` per interface and the
 * Dashboard ALREADY subscribes to it for the Physical Ports card, so the real
 * numbers cost no extra stream.
 *
 * ── THE WAN PORT IS EXCLUDED, AND THAT IS THE TRICK ────────────────────────
 *
 * The WAN is an `ether` port like the rest, so summing every ether would count
 * the internet link as a LAN one: roughly doubling the wired lane and making it
 * track the WAN exactly. `wanName` is the traffic sample's own `ifName`, which
 * is the interface those WAN rates describe.
 *
 * DIRECTION: on a LAN port the router's TX goes toward the clients, so that is
 * `down` and RX is `up` - the opposite of the WAN side, which is why they are
 * not one formula.
 */
export function netFlowInterfaces(ifaces: readonly FlowInterface[] | null | undefined,
  wanName: string): void {
  let wRx = 0, wTx = 0, lRx = 0, lTx = 0;
  for (const i of ifaces || []) {
    if (!i.running || i.disabled) continue;
    const rx = i.rxMbps || 0, tx = i.txMbps || 0;
    if (i.type === 'ether' && i.name !== wanName) { wRx += rx; wTx += tx; continue; }
    // Both spellings: RouterOS reports the newer drivers as `wifi` and the
    // older ones as `wlan`, and one router can carry each on different bands.
    if (i.type === 'wlan' || i.type === 'wifi') { lRx += rx; lTx += tx; }
  }
  netFlowUpdate({
    wired: { down: wTx * 1e6, up: wRx * 1e6 },
    wireless: { down: lTx * 1e6, up: lRx * 1e6 },
  });
}

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
      // 50 Mbps line and a saturated gigabit one both run at full tilt.
      //
      // ── AND A STEEP LOW END, WHICH IS THE WHOLE DIFFICULTY ────────────
      //
      // A real link spends almost all its time in the bottom thousandth of its
      // capacity: 490 kb/s on a gigabit line is a load of 0.0005. A linear
      // response, or even a square root, renders that as a dot every eight
      // seconds - technically faithful and useless to look at. The 0.3 power
      // lifts 0.0005 to 0.13 and 0.1 to 0.50, so ordinary traffic reads as
      // moving and saturation still reads as faster.
      //
      // FLOOR AND CEILING ARE DELIBERATE: any traffic at all gets a visible
      // rate, because "some" and "none" is the distinction the card is for,
      // and nothing exceeds the top because a busier link must not become an
      // unreadable blur.
      // The exponent is chosen so this tracks the reference design's own curve
      // across the range, while staying relative to capacity rather than
      // absolute. Worked through at three points, taking a gigabit line:
      //
      //   490 kb/s  load .0005  a .26   ->  101 px/s   (reference: 110)
      //    60 Mb/s  load .06    a .61   ->  158 px/s   (reference: 161)
      //     1 Gb/s  load 1      a 1     ->  220 px/s   (reference: 220)
      //
      // so the speed range is the reference's 60..220 exactly.
      const a = l.load > 0 ? Math.pow(l.load, .18) : 0;
      const pps = l.load > 0 ? 1 + a * 4 : 0;        // particles/s, 1..5
      l.speed = 60 + a * 160;                        // px/s, 60..220
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
      // ── AN ABSENT FIELD MEANS UNCHANGED, NOT ZERO ─────────────────────
      //
      // THE BUG THIS EXISTS FOR. The three client-count writers push
      // `{ wired: { clients: n } }` with no rates at all, and this used to read
      // the missing `down` and `up` as zero and wipe the lane. `ifstatus:names`
      // lands just after `ifstatus:update`, so the wired rate was set from the
      // real per-port sums and then zeroed a moment later, every second: the
      // operator saw a wired lane that never moved while the wireless one did.
      //
      // Measured while chasing it: the ports were carrying 1.06 Mb/s down
      // against wireless's 0.42 - the busier lane was the dead one.
      if (x.down != null || x.up != null) {
        // `down` is toward the clients, so it is measured against the DOWNLOAD
        // capacity on every link; `up` against the upload one.
        const dl = loadFraction(x.down, cap.down);
        const ul = loadFraction(x.up, cap.up);
        lanes.forEach((l) => { if (l.key === k) l.load = l.dir > 0 ? ul : dl; });
        const a = Math.max(dl, ul);
        const tr = tracks[k];
        if (tr) {
          tr.tube.setAttribute('opacity', (.03 + .09 * a).toFixed(3));
          tr.lines.forEach((p) => p.setAttribute('opacity', (.14 + .3 * a).toFixed(2)));
        }
      }
      if (x.clients != null && k !== 'wan') {
        const ck = k as 'wired' | 'wireless';
        counts[ck] = x.clients;
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
