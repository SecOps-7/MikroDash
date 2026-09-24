// The Wi-Fi Map - where the access points physically are, and who is on them.
//
// ── IT IS A DRAWING, AND THE DRAWING IS THE OPERATOR'S ──────────────────────
//
// Nothing on a router knows where anything is. So this page has two halves: an
// EDIT mode where somebody draws the site - buildings with a number of storeys,
// open areas, walls, labels - and pins each access point where it actually
// stands, and a VIEW mode that hangs the live client list off those pins.
//
// The plan is shared per router rather than per user, because where a hAP is
// bolted is a fact about the site and not a preference. It is stored through
// `/api/router-doc`; see internal/sitedoc for the shape and internal/db for why
// it is not a layout.
//
// ── WHY THERE IS NO TRIANGULATION, AND WHY THAT IS NOT A GAP ────────────────
//
// Triangulation needs three simultaneous readings of one client. RouterOS gives
// exactly ONE: the registration table lists the clients ASSOCIATED with a radio,
// and reports the signal that radio sees. A second access point in the same room
// does not report an unassociated client at all - there is no menu that does -
// so fifteen access points still produce one number per client. Two readings
// short, and no amount of arithmetic makes that up.
//
// What ONE reading does support is a DISTANCE, and that is what the two client
// modes offer:
//
//	Orbit        a fixed radius. Says "on this AP" and claims nothing else.
//	Signal ring  radius from the signal, through the log-distance model below.
//	             Says "roughly this far from this AP" and nothing about which
//	             direction, because the direction is genuinely unknown.
//
// The angle is a hash of the MAC in both modes: arbitrary, but STABLE, so a
// client does not walk around its access point every time the list refreshes.
//
// ── RECTANGLES, AND ONE EXCEPTION ───────────────────────────────────────────
//
// A site plan somebody draws in a minute, not a CAD package: a building is a
// box, a wall is a thin box, a label is text. The thing being answered is "which
// building is that AP in and how far is that client".
//
// AN AREA IS THE EXCEPTION, because a plot boundary follows a road or a fence
// and a rectangle says something false about where it ends. It is drawn by
// clicking its corners, and its vertices can be dragged afterwards.

import { esc, el, lsGet, lsSet, svgEl, attr, text } from '../dom';
import type { Socket } from '../socket';
import type { WifiPayload, WirelessPayload, WirelessClient } from '../gen/payloads';

// ── the stored document ─────────────────────────────────────────────────────
//
// Mirrors internal/sitedoc. Declared here rather than generated, because it
// travels over HTTP as an opaque document rather than through the payload
// vocabulary - `cmd/tsgen` types what crosses the WebSocket.

interface MapPoint { x: number; y: number }

interface MapObject {
  id: string;
  kind: 'building' | 'area' | 'wall' | 'label';
  label: string;
  /** The bounding box, for every shape. Recomputed from `points` when there are
   *  any, so hit-testing and the Fit button ask one question and not two. */
  x: number; y: number; w: number; h: number;
  points: MapPoint[];
  /** Storeys. BUILDINGS ONLY - see objectPanel. */
  floors: number;
  colour: string;
}

interface MapAP {
  ap: string;
  ifaces: string[];
  label: string;
  x: number; y: number;
  floor: number;
}

interface WifiMapDoc {
  metresPerUnit: number;
  objects: MapObject[];
  aps: MapAP[];
}

const emptyDoc = (): WifiMapDoc => ({ metresPerUnit: 0, objects: [], aps: [] });

/** One access point that could be pinned, as the Wifi Networks payload sees it. */
interface Candidate {
  /** `ap:<identity>` for a managed access point, `if:<radio>` for a local radio. */
  key: string;
  ap: string;
  label: string;
  ifaces: string[];
}

// ── the distance model ──────────────────────────────────────────────────────
//
// Log-distance path loss: `d = 10 ^ ((P1 - rssi) / (10n))`, where P1 is the
// signal one metre from the radio and n is how fast it decays.
//
// BOTH NUMBERS ARE TYPICAL, NOT MEASURED, and that is the honest description of
// what this mode produces. -40 dBm at a metre is what a 2.4 GHz radio at ordinary
// transmit power gives; 2.7 is the usual indoor exponent, between free space (2)
// and a building with walls in the way (3+). A client at -55 lands about 3.5 m
// out and one at -75 about 22 m, which is the right ORDER and not a measurement.
//
// The label on the mode says "estimate" for that reason, and the mode is not the
// default.
const RSSI_AT_1M = -40;
const PATH_LOSS_N = 2.7;

function metresFromRssi(dbm: number): number {
  if (!Number.isFinite(dbm) || dbm >= 0) return 0;
  return Math.pow(10, (RSSI_AT_1M - dbm) / (10 * PATH_LOSS_N));
}

/** A stable angle for a client, so it does not walk around its AP on a refresh. */
function angleOf(seed: string): number {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) >>> 0;
  return (h % 3600) / 3600 * Math.PI * 2;
}

/** The bounding box of an outline, which is what `x/y/w/h` hold for a polygon. */
function bounds(pts: MapPoint[]): { x: number; y: number; w: number; h: number } {
  const xs = pts.map((p) => p.x), ys = pts.map((p) => p.y);
  const x = Math.min(...xs), y = Math.min(...ys);
  return { x, y, w: Math.max(...xs) - x, h: Math.max(...ys) - y };
}

/** The signal bands the rest of the app uses, so a colour means one thing. */
function signalColour(dbm: number): string {
  if (dbm >= -55) return 'rgba(52,211,153,.9)';
  if (dbm >= -65) return 'rgba(56,189,248,.9)';
  if (dbm >= -75) return 'rgba(251,191,36,.9)';
  return 'rgba(248,113,113,.9)';
}

const OBJECT_FILL: Record<string, string> = {
  building: 'rgba(56,189,248,.10)',
  area: 'rgba(52,211,153,.07)',
  wall: 'rgba(148,163,184,.45)',
  label: 'transparent',
};
const OBJECT_STROKE: Record<string, string> = {
  building: 'rgba(56,189,248,.45)',
  area: 'rgba(52,211,153,.35)',
  wall: 'rgba(148,163,184,.6)',
  label: 'transparent',
};
/** The palette the colour picker offers. Small on purpose: a plan reads better
 *  with five colours than with a wheel. */
const PALETTE = ['', '#38bdf8', '#34d399', '#fbbf24', '#f87171', '#a78bfa'];

const DEFAULT_SIZE: Record<string, { w: number; h: number }> = {
  building: { w: 220, h: 150 },
  wall: { w: 240, h: 10 },
  label: { w: 120, h: 28 },
};

// ── what a client's label says ──────────────────────────────────────────────
//
// Five different answers to "who is that", and which ones are worth the ink
// depends on the site: a floor of named laptops wants the host name, a yard of
// cameras wants the note somebody wrote on the DHCP lease, a signal survey wants
// neither. So they are switches rather than a format, and each one is its own
// line - a single run of `name · ip · -54 dBm` is what the tooltip is for.
interface LabelFields {
  name: boolean; comment: boolean; ip: boolean; mac: boolean; signal: boolean;
}
const LABEL_DEFAULT: LabelFields =
  { name: true, comment: false, ip: false, mac: false, signal: false };

// ── how big things are drawn ───────────────────────────────────────────
//
// Everything on the canvas is POSITIONED in map units, and a zoom scales those
// with the drawing - which is right for a building and wrong for a name. Zoomed
// out to fit a site, a 9px label is under two pixels tall and a client dot is a
// speck: the map is there and nothing on it can be read.
//
// So the marks have their own scale. With `fixedSize` on, every size below is
// divided by the zoom, which leaves text and dots the same size ON SCREEN at any
// zoom while their positions still move with the plan. The orbit radius goes
// with them, or the dots would pile into one blob as the ring shrank.
//
// THE SIGNAL RING IS THE EXCEPTION and is left alone: its radius is a distance
// estimate in metres, and a distance that changes when you zoom is not one.
const FONT = { obj: 13, sub: 10, ap: 10, count: 11, client: 9 };

/** 9px monospace, near enough. Nothing here needs a text measurement pass. */
const CHAR_W = 5.4;
const LINE_H = 10;

interface Box { x0: number; y0: number; x1: number; y1: number }

function hits(a: Box, b: Box): boolean {
  return a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1;
}

type Anchor = 'middle' | 'start' | 'end';

/** Where a label block lands, given its anchor point, alignment and mark scale. */
function labelBox(x: number, y: number, w: number, lines: number,
                  anchor: Anchor, k: number): Box {
  const x0 = anchor === 'middle' ? x - w / 2 : anchor === 'end' ? x - w : x;
  return { x0, y0: y - 8 * k, x1: x0 + w, y1: y + (lines - 1) * LINE_H * k + 2 * k };
}

// The places a label is allowed to sit, in the order they are tried. Above the
// dot first because that is where the eye looks; the diagonals last because they
// read worst. `dy < 0` is measured from the BOTTOM of the block, so a four-line
// label sits above the dot rather than through it.
const LABEL_SPOTS: Array<{ dx: number; dy: number; anchor: Anchor }> = [
  { dx: 0, dy: -9, anchor: 'middle' },
  { dx: 0, dy: 16, anchor: 'middle' },
  { dx: 10, dy: 3, anchor: 'start' },
  { dx: -10, dy: 3, anchor: 'end' },
  { dx: 11, dy: -9, anchor: 'start' },
  { dx: -11, dy: -9, anchor: 'end' },
  { dx: 11, dy: 16, anchor: 'start' },
  { dx: -11, dy: 16, anchor: 'end' },
  { dx: 0, dy: -24, anchor: 'middle' },
  { dx: 0, dy: 31, anchor: 'middle' },
];

type Mode = 'view' | 'edit';
type ClientMode = 'orbit' | 'ring';

interface Sel { kind: 'object' | 'ap'; id: string }

export function initWifiMapPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const stage = el('wmStage');
  const svg = el<SVGSVGElement & HTMLElement>('wmSvg');
  if (!stage || !svg) return;

  const gViewport = el('wmViewport');
  const gObjects = el('wmObjects');
  const gMeasure = el('wmMeasureLayer');
  const gDraft = el('wmDraft');
  const gAps = el('wmAps');
  const gClients = el('wmClients');

  let routerID = '';
  let doc: WifiMapDoc = emptyDoc();
  /** What the server last confirmed, so Discard has something to go back to. */
  let saved: WifiMapDoc = emptyDoc();
  let dirty = false;

  let wifi: WifiPayload | null = null;
  let wireless: WirelessPayload | null = null;

  let mode: Mode = 'view';
  let clientMode: ClientMode = lsGet<ClientMode>('mkd_wifi_map_clients', 'orbit');
  let floor = 0;
  let sel: Sel | null = null;
  /** The tool armed by a `+ …` button: the next click on empty canvas places it. */
  let pending: 'building' | 'wall' | 'label' | null = null;
  /** The outline being clicked out, when the Area tool is armed. */
  let drawing: MapPoint[] | null = null;

  let labelFields: LabelFields =
    { ...LABEL_DEFAULT, ...lsGet<Partial<LabelFields>>('mkd_wifi_map_labels', {}) };
  let fixedSize = lsGet('mkd_wifi_map_fixed', true);
  /** Did the last load actually answer? A plan that never arrived must not be
   *  saved over: an empty canvas and a failed read look identical on screen. */
  let loaded = false;
  /** Clients whose access point is not on the map, from the last render. */
  let unplacedCount = 0;

  /** What to multiply a drawn size by. 1 when the marks scale with the plan. */
  function mk(): number {
    return fixedSize ? 1 / Math.max(0.05, view.k) : 1;
  }

  // ── the scale, measured ───────────────────────────────────────────────────
  //
  // `metresPerUnit` can be typed, and typing it means working out a ratio by
  // hand from a width somebody has to go and read. Measuring is the same answer
  // asked the other way round: click the two ends of a wall you know, say how
  // long it is, and the division happens here.
  let measuring = false;
  let measureFrom: { x: number; y: number } | null = null;

  const view = lsGet('mkd_wifi_map_view', { x: 40, y: 40, k: 1 });
  let rafId: number | null = null;

  // ── geometry ──────────────────────────────────────────────────────────────

  function applyView(): void {
    attr(gViewport, 'transform',
      'translate(' + view.x.toFixed(1) + ',' + view.y.toFixed(1) + ') scale(' + view.k.toFixed(3) + ')');
  }

  function saveView(): void { lsSet('mkd_wifi_map_view', view); }

  function frame(fn: () => void): void {
    if (rafId !== null) return;
    rafId = requestAnimationFrame(() => { rafId = null; fn(); });
  }

  function pt(evt: PointerEvent | WheelEvent): { x: number; y: number } {
    const rect = svg!.getBoundingClientRect();
    return {
      x: (evt.clientX - rect.left - view.x) / view.k,
      y: (evt.clientY - rect.top - view.y) / view.k,
    };
  }

  function fit(): void {
    const rect = stage!.getBoundingClientRect();
    const pts: Array<{ x: number; y: number }> = [];
    doc.objects.forEach((o) => { pts.push({ x: o.x, y: o.y }, { x: o.x + o.w, y: o.y + o.h }); });
    doc.aps.forEach((a) => pts.push({ x: a.x, y: a.y }));
    if (!pts.length || !rect.width) return;
    const xs = pts.map((p) => p.x), ys = pts.map((p) => p.y);
    const minX = Math.min(...xs), maxX = Math.max(...xs);
    const minY = Math.min(...ys), maxY = Math.max(...ys);
    const pad = 80;
    const k = Math.max(0.2, Math.min(2.5,
      Math.min((rect.width - pad) / Math.max(1, maxX - minX),
        (rect.height - pad) / Math.max(1, maxY - minY))));
    view.k = k;
    view.x = rect.width / 2 - ((minX + maxX) / 2) * k;
    view.y = rect.height / 2 - ((minY + maxY) / 2) * k;
    if (fixedSize) render(); else applyView();
    saveView();
  }

  // ── what the payloads say about access points ─────────────────────────────

  /**
   * Every access point that COULD be pinned.
   *
   * Keyed on the AP identity where there is one, so a dual-band CAP is one
   * candidate and not two - it is one box on one wall. A local radio belongs to
   * no manager and has no identity, so it is keyed on its own interface.
   */
  function candidates(): Candidate[] {
    const out = new Map<string, Candidate>();
    const st = wifi;
    if (!st) return [];
    (st.radios || []).forEach((r) => {
      const key = r.ap ? 'ap:' + r.ap : 'if:' + r.name;
      let c = out.get(key);
      if (!c) { c = { key, ap: r.ap, label: r.ap || r.name, ifaces: [] }; out.set(key, c); }
      if (c.ifaces.indexOf(r.name) === -1) c.ifaces.push(r.name);
    });
    // A virtual AP rides its master radio and has to resolve to the same pin, or
    // every guest network would be an access point of its own.
    (st.networks || []).forEach((n) => {
      const key = n.ap ? 'ap:' + n.ap : 'if:' + (n.radio || n.name);
      const c = out.get(key);
      if (c && c.ifaces.indexOf(n.name) === -1) c.ifaces.push(n.name);
    });
    return [...out.values()].sort((a, b) => a.label.localeCompare(b.label));
  }

  const pinKey = (a: MapAP): string => (a.ap ? 'ap:' + a.ap : 'if:' + (a.ifaces[0] || ''));

  /** Interface -> the AP identity broadcasting it, from the Wifi Networks payload. */
  function ifaceToAp(): Record<string, string> {
    const out: Record<string, string> = {};
    (wifi?.networks || []).forEach((n) => { out[n.name] = n.ap; });
    (wifi?.radios || []).forEach((r) => { if (!(r.name in out)) out[r.name] = r.ap; });
    return out;
  }

  /** Which pin a client belongs to, or null when its AP is not on the map. */
  function pinFor(c: WirelessClient, map: Record<string, string>): MapAP | null {
    const ap = map[c.iface] || '';
    for (const p of doc.aps) {
      if (ap && p.ap && p.ap === ap) return p;
      if (p.ifaces.indexOf(c.iface) !== -1) return p;
    }
    return null;
  }

  // ── rendering ─────────────────────────────────────────────────────────────

  function clear(g: Element | null): void {
    while (g && g.firstChild) g.removeChild(g.firstChild);
  }

  function renderMeasure(to?: { x: number; y: number }): void {
    clear(gMeasure);
    if (!gMeasure || !measureFrom || !to) return;
    gMeasure.appendChild(svgEl('line', {
      x1: measureFrom.x, y1: measureFrom.y, x2: to.x, y2: to.y,
      stroke: 'var(--accent-warn)', 'stroke-width': 1.5, 'stroke-dasharray': '5 4',
    }));
    const t = svgEl('text', {
      x: (measureFrom.x + to.x) / 2, y: (measureFrom.y + to.y) / 2 - 6,
      class: 'wm-measure-label',
    });
    text(t, Math.round(Math.hypot(to.x - measureFrom.x, to.y - measureFrom.y)) + ' units');
    gMeasure.appendChild(t);
  }

  function renderDraft(to?: MapPoint): void {
    clear(gDraft);
    if (!gDraft || !drawing) return;
    const pts = to ? [...drawing, to] : drawing;
    const k = mk();
    if (pts.length > 1) {
      gDraft.appendChild(svgEl('polyline', {
        points: pts.map((q) => q.x + ',' + q.y).join(' '),
        fill: drawing.length > 2 ? 'rgba(52,211,153,.08)' : 'none',
        stroke: 'var(--accent-rx)', 'stroke-width': 1.5 * k,
        'stroke-dasharray': (6 * k).toFixed(1) + ' ' + (5 * k).toFixed(1),
      }));
    }
    drawing.forEach((q, i) => {
      gDraft.appendChild(svgEl('circle', {
        cx: q.x, cy: q.y, r: (i === 0 ? 6 : 4) * k,
        fill: i === 0 ? 'var(--accent-rx)' : 'rgba(56,189,248,.5)',
      }));
    });
  }

  function renderObjects(): void {
    clear(gObjects);
    if (!gObjects) return;
    doc.objects.forEach((o) => {
      const g = svgEl('g', { class: 'wm-obj', 'data-id': o.id });
      const skin = {
        fill: o.colour ? o.colour + '22' : (OBJECT_FILL[o.kind] || 'none'),
        stroke: o.colour || OBJECT_STROKE[o.kind] || 'none',
        'stroke-width': 1.5,
        'stroke-dasharray': o.kind === 'area' ? '6 5' : '',
      };
      if (o.points.length >= 3) {
        g.appendChild(svgEl('polygon', {
          points: o.points.map((q) => q.x + ',' + q.y).join(' '), ...skin,
        }));
      } else if (o.kind !== 'label') {
        g.appendChild(svgEl('rect', {
          x: o.x, y: o.y, width: Math.max(1, o.w), height: Math.max(1, o.h),
          rx: o.kind === 'wall' ? 2 : 8, ...skin,
        }));
      }
      const k = mk();
      if (o.label) {
        const t = svgEl('text', {
          x: o.x + 10 * k, y: o.y + (o.kind === 'label' ? 18 : 20) * k,
          class: 'wm-obj-label', fill: o.colour || 'var(--text-main)',
          'font-size': (FONT.obj * k).toFixed(1) + 'px',
        });
        text(t, o.label);
        g.appendChild(t);
      }
      if (o.kind === 'building' && o.floors > 1) {
        const t = svgEl('text', {
          x: o.x + 10 * k, y: o.y + (o.label ? 36 : 20) * k, class: 'wm-obj-sub',
          'font-size': (FONT.sub * k).toFixed(1) + 'px',
        });
        text(t, o.floors + ' floors');
        g.appendChild(t);
      }
      if (mode === 'edit' && sel?.kind === 'object' && sel.id === o.id) {
        g.appendChild(svgEl('rect', {
          x: o.x - 3 * k, y: o.y - 3 * k,
          width: Math.max(1, o.w) + 6 * k, height: Math.max(1, o.h) + 6 * k,
          rx: 10, fill: 'none', stroke: 'var(--accent-rx)', 'stroke-width': k,
          'stroke-dasharray': (4 * k).toFixed(1) + ' ' + (3 * k).toFixed(1),
        }));
        // A polygon resizes by its CORNERS. One handle in the bottom right would
        // have to scale the whole outline, which is not what "the fence turns
        // here" means.
        if (o.points.length >= 3) {
          o.points.forEach((q, i) => {
            g.appendChild(svgEl('rect', {
              class: 'wm-handle', 'data-vertex': o.id + ':' + i,
              x: q.x - 5 * k, y: q.y - 5 * k, width: 10 * k, height: 10 * k, rx: 2,
              fill: 'var(--accent-rx)',
            }));
          });
        } else if (o.kind !== 'label') {
          g.appendChild(svgEl('rect', {
            class: 'wm-handle', 'data-handle': o.id,
            x: o.x + o.w - 6 * k, y: o.y + o.h - 6 * k, width: 12 * k, height: 12 * k, rx: 2,
            fill: 'var(--accent-rx)',
          }));
        }
      }
      gObjects.appendChild(g);
    });
  }

  function renderAps(counts: Record<string, number>): void {
    clear(gAps);
    if (!gAps) return;
    const k = mk();
    doc.aps.forEach((a) => {
      if (floor !== 0 && a.floor !== floor) return;
      const key = pinKey(a);
      const n = counts[key] || 0;
      const g = svgEl('g', { class: 'wm-ap', 'data-ap': key });
      g.appendChild(svgEl('circle', {
        cx: a.x, cy: a.y, r: 13 * k,
        fill: n ? 'rgba(56,189,248,.18)' : 'rgba(148,163,184,.15)',
        stroke: n ? 'var(--accent-rx)' : 'rgba(148,163,184,.6)', 'stroke-width': 2 * k,
      }));
      const lbl = svgEl('text', {
        x: a.x, y: a.y + 30 * k, class: 'wm-ap-label',
        'font-size': (FONT.ap * k).toFixed(1) + 'px',
      });
      text(lbl, a.label || a.ap || a.ifaces[0] || '?');
      g.appendChild(lbl);
      if (n) {
        const c = svgEl('text', {
          x: a.x, y: a.y + 4 * k, class: 'wm-ap-count',
          'font-size': (FONT.count * k).toFixed(1) + 'px',
        });
        text(c, String(n));
        g.appendChild(c);
      }
      if (mode === 'edit' && sel?.kind === 'ap' && sel.id === key) {
        g.appendChild(svgEl('circle', {
          cx: a.x, cy: a.y, r: 19 * k, fill: 'none', stroke: 'var(--accent-rx)',
          'stroke-width': k, 'stroke-dasharray': (4 * k).toFixed(1) + ' ' + (3 * k).toFixed(1),
        }));
      }
      gAps.appendChild(g);
    });
  }

  /** What this client's label says, one enabled field per line. */
  function clientLines(c: WirelessClient): string[] {
    const on = labelFields;
    if (!on.name && !on.comment && !on.ip && !on.mac && !on.signal) return [];
    const out: string[] = [];
    if (on.name && c.name) out.push(c.name);
    if (on.comment && c.comment) out.push(c.comment);
    if (on.ip && c.ip) out.push(c.ip);
    if (on.mac && c.mac) out.push(c.mac);
    if (on.signal) out.push(c.signal + ' dBm');
    // A DOT WITH NO LABEL AT ALL READS AS A BUG. Switching everything off is a
    // choice and is honoured above; a client that simply has none of the fields
    // switched on still gets the one thing every client has.
    if (!out.length) out.push(c.mac || '?');
    return out;
  }

  /**
   * The clients, around their pins.
   *
   * The radius is the whole difference between the two modes; the angle is the
   * same stable hash in both. See the file header for why the angle carries no
   * information at all.
   *
   * ── TWO PASSES, BECAUSE A DOT AND ITS LABEL FAIL DIFFERENTLY ─────────────
   *
   * Two dots in the same place are one dot, and no amount of label shuffling
   * separates them - so the first pass places every dot and nudges the angle
   * until it has room. Only then does the second pass hang the labels, choosing
   * from `LABEL_SPOTS` the first side that clears everything already on the
   * canvas: the other clients, the other access points, and its own.
   *
   * The angle nudge keeps the drawing STABLE for a given client list, which is
   * the property the hash was for: the same clients redraw in the same places.
   */
  function renderClients(groups: Map<string, { pin: MapAP; clients: WirelessClient[] }>): void {
    clear(gClients);
    if (!gClients || mode === 'edit') return;
    const perUnit = doc.metresPerUnit > 0 ? doc.metresPerUnit : 0;
    const ring = clientMode === 'ring' && perUnit > 0;
    const k = mk();

    // Everything a label must keep off: the access points and their names.
    const taken: Box[] = [];
    doc.aps.forEach((a) => {
      if (floor !== 0 && a.floor !== floor) return;
      taken.push({ x0: a.x - 16 * k, y0: a.y - 16 * k, x1: a.x + 16 * k, y1: a.y + 16 * k });
      const w = ((a.label || a.ap || a.ifaces[0] || '?').length * 5.8 + 6) * k;
      taken.push({ x0: a.x - w / 2, y0: a.y + 21 * k, x1: a.x + w / 2, y1: a.y + 34 * k });
    });

    interface Placed { c: WirelessClient; pin: MapAP; x: number; y: number; lines: string[] }
    const placed: Placed[] = [];
    groups.forEach((grp) => {
      if (floor !== 0 && grp.pin.floor !== floor) return;
      grp.clients.forEach((c, i) => {
        const lines = clientLines(c);
        // A taller label needs a wider ring, or the second row of clients draws
        // through the first row's text.
        const step = 24 + Math.max(0, lines.length - 1) * 11;
        // THE RING KEEPS ITS METRES. Its radius is a distance estimate, and a
        // distance that changes when you zoom is not one.
        const r = ring
          ? Math.min(900, Math.max(18, metresFromRssi(c.signal) / perUnit))
          : (42 + Math.floor(i / 8) * step) * k;
        let a = angleOf(c.mac || c.name || String(i));
        let x = grp.pin.x + Math.cos(a) * r;
        let y = grp.pin.y + Math.sin(a) * r;
        for (let t = 0; t < 28; t++) {
          if (!placed.some((q) => Math.hypot(q.x - x, q.y - y) < 14 * k)) break;
          a += 0.14;
          x = grp.pin.x + Math.cos(a) * r;
          y = grp.pin.y + Math.sin(a) * r;
        }
        placed.push({ c, pin: grp.pin, x, y, lines });
        taken.push({ x0: x - 7 * k, y0: y - 7 * k, x1: x + 7 * k, y1: y + 7 * k });
      });
    });

    placed.forEach(({ c, pin, x, y, lines }) => {
      const g = svgEl('g', { class: 'wm-client' });
      g.appendChild(svgEl('line', {
        x1: pin.x, y1: pin.y, x2: x, y2: y,
        stroke: 'rgba(148,163,184,.25)', 'stroke-width': k,
      }));
      g.appendChild(svgEl('circle', {
        cx: x, cy: y, r: 5 * k, fill: signalColour(c.signal),
        stroke: 'rgba(15,23,42,.7)', 'stroke-width': k,
      }));

      if (lines.length) {
        const w = (Math.max(...lines.map((t) => t.length)) * CHAR_W + 4) * k;
        let best = LABEL_SPOTS[0]!, bestBox: Box | null = null, bestHits = Infinity;
        for (const spot of LABEL_SPOTS) {
          const raw = spot.dy < 0 ? spot.dy - (lines.length - 1) * LINE_H : spot.dy;
          const dy = raw * k, dx = spot.dx * k;
          const box = labelBox(x + dx, y + dy, w, lines.length, spot.anchor, k);
          let n = 0;
          for (const t of taken) if (hits(box, t)) n++;
          if (n < bestHits) { bestHits = n; best = { anchor: spot.anchor, dx, dy }; bestBox = box; }
          if (!n) break;
        }
        if (bestBox) taken.push(bestBox);
        const t = svgEl('text', {
          x: x + best.dx, y: y + best.dy, class: 'wm-client-label',
          'text-anchor': best.anchor, 'font-size': (FONT.client * k).toFixed(1) + 'px',
        });
        lines.forEach((line, i) => {
          const span = svgEl('tspan', { x: x + best.dx, dy: i ? LINE_H * k : 0 });
          text(span, line);
          t.appendChild(span);
        });
        g.appendChild(t);
      }

      const title = svgEl('title');
      text(title, [c.name || c.mac, c.comment, c.ip, c.mac, c.signal + ' dBm', c.ssid,
        ring ? '≈' + metresFromRssi(c.signal).toFixed(1) + ' m (estimate)' : '']
        .filter(Boolean).join(' · '));
      g.appendChild(title);
      gClients.appendChild(g);
    });
  }

  function render(): void {
    const map = ifaceToAp();
    const groups = new Map<string, { pin: MapAP; clients: WirelessClient[] }>();
    const counts: Record<string, number> = {};
    let unplaced = 0;
    (wireless?.clients || []).forEach((c) => {
      const pin = pinFor(c, map);
      if (!pin) { unplaced++; return; }
      const key = pinKey(pin);
      counts[key] = (counts[key] || 0) + 1;
      let grp = groups.get(key);
      if (!grp) { grp = { pin, clients: [] }; groups.set(key, grp); }
      grp.clients.push(c);
    });

    renderObjects();
    renderAps(counts);
    renderClients(groups);
    renderPanel();
    unplacedCount = unplaced;
    renderStats();
    renderEmpty();
    applyView();
  }

  /**
   * The four numbers and the footer hint.
   *
   * `unplaced` is remembered rather than passed in by everything that wants a
   * hint redrawn: arming a tool does not change how many clients are on an
   * access point nobody has placed, and passing zero flashed the count to none.
   */
  function renderStats(): void {
    const set = (id: string, v: string): void => { const e = el(id); if (e) e.textContent = v; };
    set('wmStatAps', String(doc.aps.length));
    set('wmStatClients', String((wireless?.clients || []).length - unplacedCount));
    set('wmStatUnplaced', String(unplacedCount));
    set('wmStatScale', doc.metresPerUnit > 0
      ? doc.metresPerUnit.toFixed(2) + ' m/unit' : 'not set');
    const foot = el('wmFoot');
    if (foot) {
      const bits: string[] = [];
      if (unplacedCount) {
        bits.push(unplacedCount + ' client' + (unplacedCount === 1 ? '' : 's') +
          ' on an access point that is not on the map');
      }
      if (measuring) {
        bits.push(measureFrom
          ? 'Click the other end of the distance you are measuring.'
          : 'Click one end of something you know the length of.');
      }
      if (drawing) {
        bits.push(drawing.length < 3
          ? 'Click each corner of the area. Three at least.'
          : drawing.length + ' corners. Click the first one again, double-click ' +
            'or press Enter to close it; Esc throws it away.');
      }
      if (clientMode === 'ring' && doc.metresPerUnit <= 0) {
        bits.push('Signal ring needs a scale - set one in Edit mode. ' +
          'Drawing at a fixed radius until then.');
      }
      if (clientMode === 'ring' && doc.metresPerUnit > 0) {
        bits.push('Radius is a distance ESTIMATE from the signal. The direction ' +
          'is arbitrary: a router reports one reading per client, never three.');
      }
      foot.innerHTML = esc(bits.join('  ·  '));
    }
  }

  function renderEmpty(): void {
    const e = el('wmEmpty');
    if (!e) return;
    const blank = !doc.objects.length && !doc.aps.length;
    e.classList.toggle('show', blank);
    if (blank) {
      e.innerHTML = mode === 'edit'
        ? 'Draw the site: add a building, then place your access points on it.'
        : 'Nothing has been drawn for this router yet. Switch to Edit to start.';
    }
  }

  // ── the side panel ────────────────────────────────────────────────────────

  function field(label: string, inner: string): string {
    return '<div class="wm-field"><label class="wm-field-lbl">' + esc(label) + '</label>' +
      inner + '</div>';
  }

  function renderPanel(): void {
    const panel = el('wmPanel');
    if (!panel) return;
    // NEVER REBUILD A PANEL SOMEBODY IS USING. A wifi payload lands every few
    // seconds and a zoom now redraws too; replacing the markup under an open
    // field takes the focus and the half-typed label with it. Same guard the
    // topology map's detail panel carries, for the same reason.
    if (panel.contains(document.activeElement)) return;
    // `.open` is what slides it in - the panel is parked off-canvas otherwise,
    // which is how the topology map's detail panel works and why this reuses it.
    panel.classList.toggle('open', mode === 'edit');
    if (mode !== 'edit') {
      panel.innerHTML = '';
      return;
    }

    if (sel?.kind === 'object') {
      const o = doc.objects.find((x) => x.id === sel!.id);
      if (o) { panel.innerHTML = objectPanel(o); wireObjectPanel(o); return; }
    }
    if (sel?.kind === 'ap') {
      const a = doc.aps.find((x) => pinKey(x) === sel!.id);
      if (a) { panel.innerHTML = apPanel(a); wireApPanel(a); return; }
    }
    panel.innerHTML = trayPanel();
    wireTray();
  }

  function objectPanel(o: MapObject): string {
    const poly = o.points.length >= 3;
    return '<div class="wm-panel-title">' + esc(o.kind) + '</div>' +
      field('Label', '<input class="sform-input" id="wmfLabel" value="' + esc(o.label) + '">') +
      // STOREYS ARE A BUILDING'S. A fence and a plot have one apiece, and a
      // storey count on them put floors in the picker nothing could be on.
      (o.kind === 'building'
        ? field('Storeys', '<input class="sform-input" id="wmfFloors" type="number" min="1" max="64" value="' +
          String(o.floors) + '">')
        : '') +
      (poly
        ? '<div class="wm-panel-note">' + o.points.length + ' corners. Drag one to ' +
          'move it; drag the outline to move the whole shape.</div>'
        : field('Width', '<input class="sform-input" id="wmfW" type="number" value="' + String(Math.round(o.w)) + '">') +
          field('Height', '<input class="sform-input" id="wmfH" type="number" value="' + String(Math.round(o.h)) + '">')) +
      field('Colour', '<div class="wm-swatches" id="wmfColours">' +
        PALETTE.map((c) => '<button type="button" class="wm-swatch' +
          (c === o.colour ? ' is-on' : '') + '" data-colour="' + esc(c) + '"' +
          ' style="background:' + (c || 'transparent') + '"' +
          ' title="' + (c ? esc(c) : 'default') + '"></button>').join('') + '</div>') +
      '<button class="sbtn sbtn-danger wm-panel-btn" id="wmfDelete">Delete</button>';
  }

  function wireObjectPanel(o: MapObject): void {
    const touch = (): void => { dirty = true; render(); };
    el<HTMLInputElement>('wmfLabel')?.addEventListener('change', (e) => {
      o.label = (e.target as HTMLInputElement).value; touch();
    });
    el<HTMLInputElement>('wmfFloors')?.addEventListener('change', (e) => {
      o.floors = Math.max(1, Math.min(64, parseInt((e.target as HTMLInputElement).value, 10) || 1));
      syncFloors(); touch();
    });
    el<HTMLInputElement>('wmfW')?.addEventListener('change', (e) => {
      o.w = Math.max(4, parseFloat((e.target as HTMLInputElement).value) || o.w); touch();
    });
    el<HTMLInputElement>('wmfH')?.addEventListener('change', (e) => {
      o.h = Math.max(4, parseFloat((e.target as HTMLInputElement).value) || o.h); touch();
    });
    el('wmfColours')?.querySelectorAll('.wm-swatch').forEach((b) => {
      b.addEventListener('click', () => {
        o.colour = (b as HTMLElement).dataset.colour || '';
        touch();
      });
    });
    el('wmfDelete')?.addEventListener('click', () => {
      doc.objects = doc.objects.filter((x) => x.id !== o.id);
      sel = null; touch();
    });
  }

  function apPanel(a: MapAP): string {
    return '<div class="wm-panel-title">Access point</div>' +
      '<div class="wm-panel-sub">' + esc(a.ap || a.ifaces.join(', ')) + '</div>' +
      field('Label', '<input class="sform-input" id="wmfApLabel" value="' + esc(a.label) + '">') +
      field('Floor', '<input class="sform-input" id="wmfApFloor" type="number" min="1" max="64" value="' +
        String(a.floor) + '">') +
      '<div class="wm-panel-note">Radios: ' + esc(a.ifaces.join(', ') || '-') + '</div>' +
      '<button class="sbtn sbtn-danger wm-panel-btn" id="wmfApDelete">Take off the map</button>';
  }

  function wireApPanel(a: MapAP): void {
    el<HTMLInputElement>('wmfApLabel')?.addEventListener('change', (e) => {
      a.label = (e.target as HTMLInputElement).value; dirty = true; render();
    });
    el<HTMLInputElement>('wmfApFloor')?.addEventListener('change', (e) => {
      a.floor = Math.max(1, Math.min(64, parseInt((e.target as HTMLInputElement).value, 10) || 1));
      dirty = true; render();
    });
    el('wmfApDelete')?.addEventListener('click', () => {
      const key = pinKey(a);
      doc.aps = doc.aps.filter((x) => pinKey(x) !== key);
      sel = null; dirty = true; render();
    });
  }

  /** The default panel: the scale, and every access point waiting to be placed. */
  function trayPanel(): string {
    const placed = new Set(doc.aps.map(pinKey));
    const rows = candidates().map((c) => {
      const on = placed.has(c.key);
      return '<div class="wm-tray-row' + (on ? ' is-on' : '') + '">' +
        '<span class="wm-tray-name" title="' + esc(c.ifaces.join(', ')) + '">' +
          esc(c.label) + '</span>' +
        (on ? '<span class="wm-tray-note">placed</span>'
          : '<button class="topo-btn" data-place="' + esc(c.key) + '">Place</button>') +
      '</div>';
    }).join('');
    return '<div class="wm-panel-title">Site</div>' +
      field('Metres per canvas unit',
        '<input class="sform-input" id="wmfScale" type="number" step="0.01" min="0" value="' +
        String(doc.metresPerUnit || '') + '" placeholder="e.g. 0.25">') +
      '<div class="wm-panel-note">Draw a wall you know the length of, read its ' +
        'Width, and divide: 40 m across 160 units is 0.25. Only the Signal ring ' +
        'mode uses it.</div>' +
      '<div class="wm-panel-title" style="margin-top:.9rem">Access points</div>' +
      (rows || '<div class="wm-panel-note">No radios reported yet. Open Wifi ' +
        'Networks once so this router has answered.</div>');
  }

  function wireTray(): void {
    el<HTMLInputElement>('wmfScale')?.addEventListener('change', (e) => {
      const v = parseFloat((e.target as HTMLInputElement).value);
      doc.metresPerUnit = Number.isFinite(v) && v > 0 ? v : 0;
      dirty = true; render();
    });
    el('wmPanel')?.querySelectorAll('[data-place]').forEach((b) => {
      b.addEventListener('click', () => {
        const key = (b as HTMLElement).dataset.place || '';
        const c = candidates().find((x) => x.key === key);
        if (!c) return;
        // Placed at the middle of what is on screen, so it lands where the
        // operator is looking rather than at an origin they may have panned away
        // from.
        const rect = stage!.getBoundingClientRect();
        doc.aps.push({
          ap: c.ap, ifaces: c.ifaces.slice(), label: c.label, floor: floor || 1,
          x: (rect.width / 2 - view.x) / view.k,
          y: (rect.height / 2 - view.y) / view.k,
        });
        sel = { kind: 'ap', id: key };
        dirty = true;
        render();
      });
    });
  }

  // ── chrome ────────────────────────────────────────────────────────────────

  function syncChrome(): void {
    const on = (id: string, v: boolean): void => {
      el(id)?.classList.toggle('is-on', v);
    };
    on('wmModeView', mode === 'view');
    on('wmModeEdit', mode === 'edit');
    on('wmClientOrbit', clientMode === 'orbit');
    on('wmClientRing', clientMode === 'ring');
    on('wmLblName', labelFields.name);
    on('wmLblComment', labelFields.comment);
    on('wmLblIp', labelFields.ip);
    on('wmLblMac', labelFields.mac);
    on('wmLblSignal', labelFields.signal);
    on('wmFixedSize', fixedSize);
    const tools = el('wmEditTools'); if (tools) tools.style.display = mode === 'edit' ? '' : 'none';
    const save = el('wmSaveTools'); if (save) save.style.display = mode === 'edit' ? '' : 'none';
    const cm = el('wmClientModes'); if (cm) cm.style.display = mode === 'edit' ? 'none' : '';
    const lf = el('wmLabelFields'); if (lf) lf.style.display = mode === 'edit' ? 'none' : '';
    const btn = el('wmSave');
    if (btn) {
      btn.classList.toggle('is-dirty', dirty);
      btn.textContent = dirty ? 'Save *' : 'Save';
    }
  }

  /** The floor picker offers 1..max, from the tallest object drawn. */
  function syncFloors(): void {
    const picker = el<HTMLSelectElement>('wmFloor');
    if (!picker) return;
    const max = doc.objects.reduce(
      (m, o) => (o.kind === 'building' ? Math.max(m, o.floors || 1) : m), 1);
    const want = ['<option value="0">All floors</option>'];
    for (let i = 1; i <= max; i++) {
      want.push('<option value="' + i + '">Floor ' + i + '</option>');
    }
    const html = want.join('');
    if (picker.innerHTML !== html) picker.innerHTML = html;
    if (floor > max) floor = 0;
    picker.value = String(floor);
  }

  // ── loading and saving ────────────────────────────────────────────────────

  /** Fill in what an older stored document has no field for. */
  function adopt(got: WifiMapDoc | undefined): WifiMapDoc {
    if (!got) return emptyDoc();
    return {
      metresPerUnit: got.metresPerUnit || 0,
      objects: (got.objects || []).map((o) => ({ ...o, points: o.points || [] })),
      aps: got.aps || [],
    };
  }

  /** Say something in the footer that the next render will take back. */
  function note(msg: string): void {
    const foot = el('wmFoot');
    if (!foot || !msg) return;
    foot.innerHTML = esc(msg);
    window.setTimeout(renderStats, 6000);
  }

  function load(): void {
    if (!routerID) return;
    fetch('/api/router-doc?kind=wifi-map&routerId=' + encodeURIComponent(routerID),
      { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('unreadable'))))
      .then((d) => {
        doc = adopt((d && d.doc) as WifiMapDoc | undefined);
        saved = JSON.parse(JSON.stringify(doc)) as WifiMapDoc;
        loaded = true;
        dirty = false;
        syncFloors();
        syncChrome();
        render();
      })
      .catch(() => {
        // A PLAN THAT WILL NOT LOAD LEAVES THE PAGE EMPTY, NOT BROKEN - and not
        // saveable, because an empty canvas and a failed read look the same and
        // one Save would replace the site plan with whatever is on screen.
        loaded = false;
        note('The stored plan could not be read, so saving is off until it can.');
      });
  }

  function save(): void {
    if (!routerID) return;
    if (!loaded) {
      note('Not saving: the stored plan was never read, and this would replace it.');
      return;
    }
    fetch('/api/router-doc', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ routerId: routerID, kind: 'wifi-map', doc }),
    })
      // A REFUSAL IS NOT A SAVE. `r.ok` was not tested, so a 403 from a
      // read-only grant - or a 500 - landed on the success branch: the star came
      // off the button, `saved` became the unsaved document, and Discard could no
      // longer get the work back. The plan was gone on the next reload.
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('refused'))))
      .then((d) => {
        // THE SERVER'S COPY WINS. `sitedoc.Clean` bounds and normalises, so what
        // comes back is what every other viewer will see - keeping the local one
        // would leave this browser rendering a document the store does not hold.
        const got = (d && d.doc) as WifiMapDoc | undefined;
        if (got) doc = adopt(got);
        saved = JSON.parse(JSON.stringify(doc)) as WifiMapDoc;
        dirty = false;
        syncChrome();
        render();
      })
      .catch(() => {
        // LEFT DIRTY ON PURPOSE: nothing was stored, and the operator's drawing
        // is still the only copy of it.
        note('The plan was not saved. You may not have permission to edit it.');
      });
  }

  // ── interaction ───────────────────────────────────────────────────────────

  const newID = (): string =>
    'o' + Date.now().toString(36) + Math.random().toString(36).slice(2, 6);

  function newObject(kind: 'building' | 'wall' | 'label', x: number, y: number): void {
    const size = DEFAULT_SIZE[kind]!;
    doc.objects.push({
      id: newID(),
      kind, label: kind === 'label' ? 'Label' : '',
      x: x - size.w / 2, y: y - size.h / 2, w: size.w, h: size.h,
      points: [], floors: 1, colour: '',
    });
    dirty = true;
  }

  /** Finish the outline being clicked out, if there is enough of it. */
  function closeDraft(): void {
    const pts = drawing || [];
    drawing = null;
    clear(gDraft);
    svg!.classList.remove('wm-placing');
    // A DOUBLE-CLICK LEAVES A DUPLICATE CORNER: the first of its two clicks has
    // already landed as a point by the time the second arrives.
    if (pts.length > 1) {
      const a = pts[pts.length - 1]!, b = pts[pts.length - 2]!;
      if (Math.hypot(a.x - b.x, a.y - b.y) < 8) pts.pop();
    }
    if (pts.length >= 3) {
      const id = newID();
      doc.objects.push({
        id, kind: 'area', label: '', ...bounds(pts),
        points: pts, floors: 1, colour: '',
      });
      sel = { kind: 'object', id };
      dirty = true;
    }
    render();
  }

  // ONE ARMED TOOL AT A TIME. Two would make the next click ambiguous, and the
  // canvas gives no way to say which was meant.
  function disarm(): void {
    measuring = false;
    measureFrom = null;
    drawing = null;
    clear(gMeasure);
    clear(gDraft);
  }

  function wire(): void {
    let drag: { kind: 'object' | 'ap' | 'resize' | 'vertex' | 'pan'; id: string;
      px: number; py: number; ox: number; oy: number; vi?: number;
      pts?: MapPoint[] } | null = null;
    let moved = 0;

    svg!.addEventListener('pointerdown', (e) => {
      const target = e.target as Element | null;
      moved = 0;
      const p = pt(e);

      if (mode === 'edit' && measuring) {
        if (!measureFrom) {
          measureFrom = { x: p.x, y: p.y };
          renderStats();
          return;
        }
        const units = Math.hypot(p.x - measureFrom.x, p.y - measureFrom.y);
        measuring = false;
        measureFrom = null;
        clear(gMeasure);
        svg!.classList.remove('wm-placing');
        // A ZERO-LENGTH MEASUREMENT IS A MISCLICK, not a scale of infinity.
        if (units < 2) { render(); return; }
        const said = window.prompt('How many metres is that?', '');
        const metres = parseFloat(said || '');
        if (Number.isFinite(metres) && metres > 0) {
          doc.metresPerUnit = metres / units;
          dirty = true;
        }
        syncChrome();
        render();
        return;
      }

      if (mode === 'edit' && drawing) {
        // CLOSING ON THE FIRST CORNER is how every drawing tool ends a shape,
        // and it is the only way to finish one with the mouse alone.
        if (drawing.length >= 3 &&
            Math.hypot(p.x - drawing[0]!.x, p.y - drawing[0]!.y) < 12 * mk()) {
          closeDraft();
          return;
        }
        drawing.push({ x: p.x, y: p.y });
        renderDraft();
        renderStats();
        return;
      }

      if (mode === 'edit' && pending) {
        newObject(pending, p.x, p.y);
        pending = null;
        svg!.classList.remove('wm-placing');
        render();
        return;
      }

      const handle = target?.closest?.('.wm-handle') as SVGElement | null;
      const vertex = handle?.getAttribute('data-vertex') || '';
      const objEl = target?.closest?.('.wm-obj') as SVGElement | null;
      const apEl = target?.closest?.('.wm-ap') as SVGElement | null;

      if (mode === 'edit' && vertex) {
        const [id, idx] = vertex.split(':');
        const o = doc.objects.find((x) => x.id === id);
        const vi = parseInt(idx || '', 10);
        const q = o?.points[vi];
        if (o && q) drag = { kind: 'vertex', id: o.id, vi, px: p.x, py: p.y, ox: q.x, oy: q.y };
      } else if (mode === 'edit' && handle) {
        const id = handle.getAttribute('data-handle') || '';
        const o = doc.objects.find((x) => x.id === id);
        if (o) drag = { kind: 'resize', id, px: p.x, py: p.y, ox: o.w, oy: o.h };
      } else if (mode === 'edit' && apEl) {
        const id = apEl.getAttribute('data-ap') || '';
        const a = doc.aps.find((x) => pinKey(x) === id);
        if (a) { sel = { kind: 'ap', id }; drag = { kind: 'ap', id, px: p.x, py: p.y, ox: a.x, oy: a.y }; }
      } else if (mode === 'edit' && objEl) {
        const id = objEl.getAttribute('data-id') || '';
        const o = doc.objects.find((x) => x.id === id);
        if (o) {
          sel = { kind: 'object', id };
          drag = { kind: 'object', id, px: p.x, py: p.y, ox: o.x, oy: o.y,
            pts: o.points.map((q) => ({ ...q })) };
        }
      } else {
        drag = { kind: 'pan', id: '', px: e.clientX, py: e.clientY, ox: view.x, oy: view.y };
        if (mode === 'edit') sel = null;
      }
      if (mode === 'edit') { renderPanel(); renderObjects(); }
      try { svg!.setPointerCapture(e.pointerId); } catch { /* not all pointers capture */ }
    });

    svg!.addEventListener('pointermove', (e) => {
      if (measuring && measureFrom) {
        const p = pt(e);
        frame(() => renderMeasure(p));
        return;
      }
      if (drawing) {
        const p = pt(e);
        frame(() => renderDraft(p));
        return;
      }
      if (!drag) return;
      if (drag.kind === 'pan') {
        moved = Math.max(moved, Math.abs(e.clientX - drag.px) + Math.abs(e.clientY - drag.py));
        view.x = drag.ox + (e.clientX - drag.px);
        view.y = drag.oy + (e.clientY - drag.py);
        frame(applyView);
        return;
      }
      const p = pt(e);
      moved = Math.max(moved, Math.abs(p.x - drag.px) + Math.abs(p.y - drag.py));
      if (drag.kind === 'object') {
        const o = doc.objects.find((x) => x.id === drag!.id);
        if (o) {
          const dx = p.x - drag.px, dy = p.y - drag.py;
          o.x = drag.ox + dx; o.y = drag.oy + dy;
          // THE OUTLINE MOVES WITH THE BOX, from the corners it had when the
          // drag started: adding the delta to the live points every frame would
          // accelerate the shape away from the pointer.
          if (drag.pts) o.points = drag.pts.map((q) => ({ x: q.x + dx, y: q.y + dy }));
        }
      } else if (drag.kind === 'vertex') {
        const o = doc.objects.find((x) => x.id === drag!.id);
        const q = o?.points[drag.vi ?? -1];
        if (o && q) {
          q.x = drag.ox + (p.x - drag.px);
          q.y = drag.oy + (p.y - drag.py);
          Object.assign(o, bounds(o.points));
        }
      } else if (drag.kind === 'resize') {
        const o = doc.objects.find((x) => x.id === drag!.id);
        if (o) {
          o.w = Math.max(8, drag.ox + (p.x - drag.px));
          o.h = Math.max(8, drag.oy + (p.y - drag.py));
        }
      } else if (drag.kind === 'ap') {
        const a = doc.aps.find((x) => pinKey(x) === drag!.id);
        if (a) { a.x = drag.ox + (p.x - drag.px); a.y = drag.oy + (p.y - drag.py); }
      }
      frame(() => { renderObjects(); renderAps({}); });
    });

    const end = (e: PointerEvent): void => {
      if (drag && drag.kind === 'pan') {
        saveView();
      } else if (drag && moved >= 3) {
        // FOUR PIXELS SEPARATES A CLICK FROM A DRAG, the same threshold the
        // topology map uses: without one, selecting a building nudges it.
        dirty = true;
      }
      drag = null;
      syncChrome();
      render();
      try { svg!.releasePointerCapture(e.pointerId); } catch { /* already released */ }
    };
    svg!.addEventListener('pointerup', end);
    svg!.addEventListener('pointercancel', end);

    svg!.addEventListener('wheel', (e) => {
      e.preventDefault();
      const rect = svg!.getBoundingClientRect();
      const px = e.clientX - rect.left, py = e.clientY - rect.top;
      const k0 = view.k;
      const k1 = Math.max(0.2, Math.min(3, k0 * Math.exp(-e.deltaY * 0.0015)));
      if (k1 === k0) return;
      view.x = px - (px - view.x) * (k1 / k0);
      view.y = py - (py - view.y) * (k1 / k0);
      view.k = k1;
      // A FULL REDRAW WHEN THE MARKS ARE FIXED: their sizes and the label
      // placement both come off the zoom, so a transform alone would leave the
      // old ones on screen.
      frame(fixedSize ? render : applyView);
      saveView();
    }, { passive: false });

    const zoomBy = (f: number): void => {
      const rect = svg!.getBoundingClientRect();
      const px = rect.width / 2, py = rect.height / 2;
      const k0 = view.k, k1 = Math.max(0.2, Math.min(3, k0 * f));
      view.x = px - (px - view.x) * (k1 / k0);
      view.y = py - (py - view.y) * (k1 / k0);
      view.k = k1;
      if (fixedSize) render(); else applyView();
      saveView();
    };
    el('wmZoomIn')?.addEventListener('click', () => zoomBy(1.25));
    el('wmZoomOut')?.addEventListener('click', () => zoomBy(0.8));
    el('wmFit')?.addEventListener('click', fit);

    el('wmModeView')?.addEventListener('click', () => {
      mode = 'view'; sel = null; pending = null;
      disarm();
      svg!.classList.remove('wm-placing');
      syncChrome(); render();
    });
    el('wmModeEdit')?.addEventListener('click', () => {
      mode = 'edit'; syncChrome(); render();
    });
    el('wmClientOrbit')?.addEventListener('click', () => {
      clientMode = 'orbit'; lsSet('mkd_wifi_map_clients', clientMode); syncChrome(); render();
    });
    el('wmClientRing')?.addEventListener('click', () => {
      clientMode = 'ring'; lsSet('mkd_wifi_map_clients', clientMode); syncChrome(); render();
    });
    el('wmFixedSize')?.addEventListener('click', () => {
      fixedSize = !fixedSize;
      lsSet('mkd_wifi_map_fixed', fixedSize);
      syncChrome(); render();
    });

    ([['wmAddBuilding', 'building'], ['wmAddWall', 'wall'],
      ['wmAddLabel', 'label']] as const).forEach(([id, kind]) => {
      el(id)?.addEventListener('click', () => {
        pending = kind;
        disarm();
        svg!.classList.add('wm-placing');
        renderStats();
      });
    });

    el('wmAddArea')?.addEventListener('click', () => {
      pending = null;
      disarm();
      drawing = [];
      svg!.classList.add('wm-placing');
      renderStats();
    });

    // Finishing with the mouse. The click that opens the double has already
    // landed as a corner; closeDraft drops it.
    svg!.addEventListener('dblclick', (e) => {
      if (!drawing) return;
      e.preventDefault();
      closeDraft();
    });

    document.addEventListener('keydown', (e) => {
      if (!drawing || !isVisible('wifi-map')) return;
      if (e.key === 'Enter') { e.preventDefault(); closeDraft(); }
      if (e.key === 'Escape') {
        e.preventDefault();
        disarm();
        svg!.classList.remove('wm-placing');
        render();
      }
    });

    ([['wmLblName', 'name'], ['wmLblComment', 'comment'], ['wmLblIp', 'ip'],
      ['wmLblMac', 'mac'], ['wmLblSignal', 'signal']] as const).forEach(([id, f]) => {
      el(id)?.addEventListener('click', () => {
        labelFields = { ...labelFields, [f]: !labelFields[f] };
        lsSet('mkd_wifi_map_labels', labelFields);
        syncChrome();
        render();
      });
    });

    el('wmMeasure')?.addEventListener('click', () => {
      pending = null;
      disarm();
      measuring = true;
      svg!.classList.add('wm-placing');
      renderStats();
    });

    el<HTMLSelectElement>('wmFloor')?.addEventListener('change', (e) => {
      floor = parseInt((e.target as HTMLSelectElement).value, 10) || 0;
      render();
    });

    el('wmSave')?.addEventListener('click', save);
    el('wmDiscard')?.addEventListener('click', () => {
      doc = JSON.parse(JSON.stringify(saved)) as WifiMapDoc;
      sel = null; dirty = false;
      syncFloors(); syncChrome(); render();
    });
  }

  // ── wiring ────────────────────────────────────────────────────────────────

  wire();
  syncChrome();

  socket.on('wifi:update', (d) => {
    wifi = d || null;
    if (isVisible('wifi-map')) render();
  });
  socket.on('wireless:update', (d) => {
    wireless = d || null;
    if (isVisible('wifi-map')) render();
  });
  socket.on('router:active', (d) => {
    const id = (d && d.activeId) || '';
    if (!id || id === routerID) return;
    // A PLAN BELONGS TO ONE ROUTER, on this path too. `router:active` arrives
    // without `router:switched` when somebody else activates a router, or when
    // the active one is removed and the next is promoted - and leaving the old
    // drawing on screen invited a Save that wrote one site's buildings over
    // another's.
    switchTo(id);
  });
  function switchTo(id: string): void {
    doc = emptyDoc();
    saved = emptyDoc();
    loaded = false;
    dirty = false;
    sel = null;
    disarm();
    routerID = id;
    render();
    load();
  }

  socket.on('router:switched', (d) => switchTo((d && d.activeId) || ''));

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'wifi-map') return;
    if (!doc.objects.length && !doc.aps.length) load();
    syncFloors();
    syncChrome();
    render();
  });
}
