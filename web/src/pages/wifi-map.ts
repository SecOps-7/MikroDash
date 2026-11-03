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
// ── BOXES, AND OUTLINES WHERE A BOX WOULD BE WRONG ──────────────────────────
//
// A site plan somebody draws in a minute, not a CAD package: a wall is a thin
// box, a label is text, and a building is a box unless it is not. The thing
// being answered is "which building is that AP in and how far is that client".
//
// An area always, and a building when it is L-shaped or angled, is an OUTLINE:
// clicked out corner by corner. Afterwards a corner can be dragged on its own,
// the whole outline moved, or scaled from its box's corner like a rectangle.

import { esc, el, lsGet, lsSet, svgEl, attr, text, pill, bandTone } from '../dom';
import { paletteCss, paletteColour, monoFont, type PaletteToken, type PillTone } from '../palette';
import { fieldHtml } from '../form';
import type { Socket } from '../socket';
import type { WifiPayload, WirelessPayload, WirelessClient } from '../gen/payloads';
import { featureOn, onFeatures } from '../caps';
import { parseTxRate } from './wireless';

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

/** What is drawn under one floor; floor 0 is the site, under every floor. */
interface MapBackdrop {
  floor: number;
  kind: 'image' | 'tiles';
  image: string;
  /** The image's box, or for tiles the canvas point the anchor sits on. */
  x: number; y: number; w: number; h: number;
  opacity: number;
  /** Degrees clockwise, about the image's centre or the map's anchor. */
  rotation: number;
  provider: string;
  url: string;
  lat: number; lon: number;
  /** The deepest tile level fetched; 0 is the source's default. */
  maxZoom: number;
}

/** A client pinned where it stands, drawn there rather than around its AP. */
interface MapClient {
  mac: string;
  label: string;
  x: number; y: number;
  floor: number;
}

interface WifiMapDoc {
  metresPerUnit: number;
  objects: MapObject[];
  aps: MapAP[];
  backdrops: MapBackdrop[];
  clients: MapClient[];
}

const emptyDoc = (): WifiMapDoc => ({ metresPerUnit: 0, objects: [], aps: [], backdrops: [], clients: [] });

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

/** The edge of the coverage estimate where a client still gets a good link. The
 *  outer edge is WEAK_DBM. Both are drawn through the same model as the ring. */
const COVER_GOOD_DBM = -67;

// ── map tiles ───────────────────────────────────────────────────────────────
//
// A map backdrop is ordinary XYZ tiles in Web Mercator, fetched by the viewer's
// browser straight from the provider. One canvas unit is one pixel of a
// TILE_ZOOM tile (sitedoc.TileZoom), which is what fixes the scale: the server
// sets `metresPerUnit` from the anchor's latitude, and so does this page the
// moment a map is placed.
export const TILE_ZOOM = 19;

interface TileSource { label: string; url: string; maxZoom: number; credit: string }

/** The built-in sources, free to use with their credit shown. A provider whose
 *  terms want an API key is a custom template the operator brings.
 *
 *  ESRI'S DEPTH IS ASKED, NOT ASSUMED: its imagery goes to 19 or deeper in
 *  cities and stops earlier elsewhere, and a level it has no imagery for comes
 *  back as a picture saying so. Its tile map service answers which levels have
 *  imagery at a place (esriDepth below); 17, which it has nearly everywhere, is
 *  what is drawn until it has answered, and a backdrop's own deepest level
 *  overrides both. */
export const TILE_SOURCES: Record<string, TileSource> = {
  osm: {
    label: 'OpenStreetMap', url: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png',
    maxZoom: 19, credit: '© OpenStreetMap contributors',
  },
  esri: {
    label: 'Satellite (Esri World Imagery)',
    url: 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}',
    maxZoom: 17, credit: 'Imagery © Esri, Maxar, Earthstar Geographics',
  },
};
const CUSTOM_MAX_ZOOM = 20;
/** A view needing more tiles than this is too far out to read the map anyway. */
const MAX_TILES = 200;

/** A point's position in pixels of the whole world at TILE_ZOOM. */
export function worldPx(lat: number, lon: number): MapPoint {
  const n = 256 * 2 ** TILE_ZOOM;
  const s = Math.sin(lat * Math.PI / 180);
  return { x: (lon + 180) / 360 * n, y: (0.5 - Math.log((1 + s) / (1 - s)) / (4 * Math.PI)) * n };
}

/** Metres in one canvas unit at a latitude. Mirrors sitedoc.TileMetresPerUnit. */
export function tileMetresPerUnit(lat: number): number {
  return 156543.03392804097 * Math.cos(lat * Math.PI / 180) / 2 ** TILE_ZOOM;
}

export interface Tile { href: string; x: number; y: number; size: number }

/**
 * The tiles covering what is on screen, at the zoom level that matches the view.
 *
 * A turned map is drawn as tiles square to the map inside a group turned about
 * the anchor, so a tile's position here is in the map's own frame, and what
 * must be covered is the screen turned back into it: the box around its four
 * corners.
 */
export function tilesFor(b: MapBackdrop, view: { x: number; y: number; k: number },
                         width: number, height: number): Tile[] {
  const src = TILE_SOURCES[b.provider];
  const url = src ? src.url : b.url;
  if (!url) return [];
  const deepest = b.maxZoom || (src ? src.maxZoom : CUSTOM_MAX_ZOOM);
  const z = Math.max(1, Math.min(deepest, Math.round(TILE_ZOOM + Math.log2(view.k))));
  const size = 256 * 2 ** (TILE_ZOOM - z);
  const a = worldPx(b.lat, b.lon);
  // The screen's corners in world pixels at TILE_ZOOM, turned back by the rotation.
  const t = -(b.rotation || 0) * Math.PI / 180, cos = Math.cos(t), sin = Math.sin(t);
  const xs: number[] = [], ys: number[] = [];
  for (const [sx, sy] of [[0, 0], [width, 0], [0, height], [width, height]] as const) {
    const cx = (sx - view.x) / view.k - b.x, cy = (sy - view.y) / view.k - b.y;
    xs.push(a.x + cx * cos - cy * sin);
    ys.push(a.y + cx * sin + cy * cos);
  }
  const last = 2 ** z - 1;
  const tx0 = Math.max(0, Math.floor(Math.min(...xs) / size));
  const tx1 = Math.min(last, Math.floor(Math.max(...xs) / size));
  const ty0 = Math.max(0, Math.floor(Math.min(...ys) / size));
  const ty1 = Math.min(last, Math.floor(Math.max(...ys) / size));
  if (tx1 < tx0 || ty1 < ty0 || (tx1 - tx0 + 1) * (ty1 - ty0 + 1) > MAX_TILES) return [];
  const out: Tile[] = [];
  for (let ty = ty0; ty <= ty1; ty++) {
    for (let tx = tx0; tx <= tx1; tx++) {
      out.push({
        href: url.replace(/\{z\}/g, String(z)).replace(/\{x\}/g, String(tx)).replace(/\{y\}/g, String(ty)),
        x: b.x + tx * size - a.x, y: b.y + ty * size - a.y, size,
      });
    }
  }
  return out;
}

/** The tile at a coordinate, at a zoom level. */
export function tileAt(lat: number, lon: number, z: number): { x: number; y: number } {
  const p = worldPx(lat, lon), scale = 2 ** (TILE_ZOOM - z) * 256;
  return { x: Math.floor(p.x / scale), y: Math.floor(p.y / scale) };
}

/** Esri's answer to "is there imagery in this tile": `{"data":[1]}` or `[0]`. */
export function esriTileMapUrl(lat: number, lon: number, z: number): string {
  const t = tileAt(lat, lon, z);
  return 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tilemap/' +
    z + '/' + t.y + '/' + t.x + '/1/1';
}

/** "55.7539, 37.6208" or "55.7539 37.6208", the way a map app copies a point. */
export function parseLatLon(s: string): { lat: number; lon: number } | null {
  const m = /^\s*(-?\d+(?:\.\d+)?)\s*[,;\s]\s*(-?\d+(?:\.\d+)?)\s*$/.exec(s);
  if (!m) return null;
  const lat = Number(m[1]), lon = Number(m[2]);
  return Math.abs(lat) <= 85 && Math.abs(lon) <= 180 ? { lat, lon } : null;
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
  if (dbm >= -55) return paletteCss('--accent-tx', 0.9);
  if (dbm >= -65) return paletteCss('--accent-rx', 0.9);
  if (dbm >= -75) return paletteCss('--accent-warn', 0.9);
  return paletteCss('--accent-err', 0.9);
}

/** The same bands as a pill tone, for the cards. */
function signalTone(dbm: number): PillTone {
  return dbm >= -55 ? 'tx' : dbm >= -65 ? 'rx' : dbm >= -75 ? 'warn' : 'err';
}

/**
 * SVG colour goes in the style, never a fill or stroke attribute: a
 * presentation attribute cannot hold a palette colour.
 */
function paint(fill: string, stroke?: string): string {
  return 'fill:' + fill + (stroke ? ';stroke:' + stroke : '');
}

const OBJECT_FILL: Record<string, string> = {
  building: paletteCss('--accent-rx', 0.1),
  area: paletteCss('--accent-tx', 0.07),
  wall: paletteCss('--text-muted', 0.45),
  label: 'transparent',
};
const OBJECT_STROKE: Record<string, string> = {
  building: paletteCss('--accent-rx', 0.45),
  area: paletteCss('--accent-tx', 0.35),
  wall: paletteCss('--text-muted', 0.6),
  label: 'transparent',
};
/** The palette the colour picker offers. Small on purpose: a plan reads better
 *  with five colours than with a wheel. The hex is what a saved plan STORES -
 *  its key, kept so existing plans still read - and the token is what it draws. */
const PALETTE: Array<[string, PaletteToken | null]> = [
  ['', null], ['#38bdf8', '--accent-rx'], ['#34d399', '--accent-tx'],
  ['#fbbf24', '--accent-warn'], ['#f87171', '--accent-err'], ['#a78bfa', '--accent-alt'],
];

/** A stored object colour at `alpha`, drawn from the palette. A hex colour the
 *  picker never offered is drawn as stored; ANYTHING ELSE is drawn as no colour.
 *
 *  The value goes into a style, so a stored "red;background:url(...)" would add
 *  declarations of its own. The server keeps only hex colours when a plan is
 *  saved (mapColour in internal/sitedoc); this holds for plans saved before. */
export function objectColour(c: string, alpha = 1): string {
  const hit = PALETTE.find((p) => p[0] === c);
  if (hit && hit[1]) return paletteCss(hit[1], alpha);
  if (!/^#[0-9a-fA-F]{3}([0-9a-fA-F]{3})?$/.test(c)) return 'none';
  return alpha === 1 ? c : 'rgb(from ' + c + ' r g b / ' + alpha + ')';
}

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
/** The width of the ground-coloured outline that keeps a label legible over a
 *  line, a dot or a backdrop. Scaled with the marks, like the text it rings. */
const HALO = 3;

// ── weak spots (Settings -> Features) ───────────────────────────────────────
//
// An access point is coloured by its clients' AVERAGE signal, and a client below
// WEAK_DBM is listed under the map: the threshold where a phone starts dropping
// to low rates and roaming badly, which is what an operator moving an AP is
// trying to fix.
export const WEAK_DBM = -75;
const FAIR_DBM = -65;

export function apSignal(clients: WirelessClient[]): { avg: number; weak: number; tone: PaletteToken } | null {
  const sig = clients.map((c) => Number(c.signal)).filter((v) => Number.isFinite(v) && v < 0);
  if (!sig.length) return null;
  const avg = Math.round(sig.reduce((a, b) => a + b, 0) / sig.length);
  return {
    avg, weak: sig.filter((v) => v < WEAK_DBM).length,
    tone: avg < WEAK_DBM ? '--accent-err' : avg < FAIR_DBM ? '--accent-warn' : '--accent-ok',
  };
}

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
type Card = { kind: 'client' | 'ap'; id: string };
type ClientMode = 'orbit' | 'ring';

/** In Edit, what the panel is about. A backdrop's id is its floor, a pinned
 *  client's its MAC. */
interface Sel { kind: 'object' | 'ap' | 'backdrop' | 'client'; id: string }

export function initWifiMapPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const stage = el('wmStage');
  const svg = el<SVGSVGElement & HTMLElement>('wmSvg');
  if (!stage || !svg) return;

  const gViewport = el('wmViewport');
  const gBackdrop = el('wmBackdrop');
  const gCover = el('wmCoverLayer');
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
  /** What the outline being clicked out will be. */
  let drawKind: 'area' | 'building' = 'area';

  let labelFields: LabelFields =
    { ...LABEL_DEFAULT, ...lsGet<Partial<LabelFields>>('mkd_wifi_map_labels', {}) };
  let fixedSize = lsGet('mkd_wifi_map_fixed', true);
  /** Did the last load actually answer? A plan that never arrived must not be
   *  saved over: an empty canvas and a failed read look identical on screen. */
  let loaded = false;
  /** Clients whose access point is not on the map, from the last render. */
  let unplacedCount = 0;
  let weakClients: Array<{ c: WirelessClient; ap: string }> = [];

  /** The card open in View: a client by MAC, or an access point by pin key. */
  let card: Card | null = null;
  let search = '';
  /** Which of the matches Enter went to last. */
  let searchAt = -1;
  let coverage = lsGet('mkd_wifi_map_coverage', false);
  /** How deep Esri's imagery goes, by place, once asked. */
  const esriDepth = new Map<string, number>();
  /** This viewer's choice between the two built-in maps, over the plan's own;
   *  '' follows the plan. */
  let mapLook = lsGet('mkd_wifi_map_look', '');
  /** From the last render: where each client was drawn, each pin's clients, and
   *  how many labels were left off because every side of their dot was taken. */
  let drawnAt = new Map<string, MapPoint>();
  let pinClients = new Map<string, WirelessClient[]>();
  let hiddenLabels = 0;
  /** Image elements by URL. A pan redraws the tiles, and handing back the same
   *  element keeps a loaded tile on screen instead of loading it again. */
  const images = new Map<string, SVGElement>();

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
    // The tiles on screen depend on the view, so every pan and zoom asks again.
    renderBackdrop();
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
    doc.clients.forEach((c) => pts.push({ x: c.x, y: c.y }));
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

  /** Virtual AP -> the radio it rides, for a local network added after its pin. */
  function ifaceToRadio(): Record<string, string> {
    const out: Record<string, string> = {};
    (wifi?.networks || []).forEach((n) => { if (n.radio) out[n.name] = n.radio; });
    return out;
  }

  /**
   * Which pin a client belongs to, or null when its AP is not on the map.
   *
   * THE AP IDENTITY DECIDES WHEN THERE IS ONE. A pin's interface names are a
   * snapshot from when it was placed, and a manager hands `cap1`, `cap2`... out
   * in connection order, so after a reboot another CAP can hold an interface an
   * older pin still lists. Matching names first put that CAP's clients around
   * the wrong box. Names only place clients of radios with no identity.
   */
  function pinFor(c: WirelessClient, map: Record<string, string>,
                  radio: Record<string, string>): MapAP | null {
    const ap = map[c.iface] || '';
    if (ap) {
      const byAp = doc.aps.find((p) => p.ap === ap);
      if (byAp) return byAp;
    }
    const names = [c.iface, radio[c.iface] || ''].filter(Boolean);
    return doc.aps.find((p) => (!ap || !p.ap) &&
      names.some((n) => p.ifaces.indexOf(n) !== -1)) || null;
  }

  // ── rendering ─────────────────────────────────────────────────────────────

  function clear(g: Element | null): void {
    while (g && g.firstChild) g.removeChild(g.firstChild);
  }

  const backdropOf = (f: number): MapBackdrop | undefined => doc.backdrops.find((b) => b.floor === f);
  const siteMap = (): MapBackdrop | undefined => doc.backdrops.find((b) => b.kind === 'tiles');

  function imageHref(id: string): string {
    return '/api/router-doc/image?routerId=' + encodeURIComponent(routerID) + '&id=' + encodeURIComponent(id);
  }

  function image(href: string, x: number, y: number, w: number, h: number): SVGElement {
    let im = images.get(href);
    if (!im) {
      if (images.size > 600) images.clear();
      im = svgEl('image', { href, preserveAspectRatio: 'none' });
      images.set(href, im);
    }
    attr(im, 'x', x); attr(im, 'y', y); attr(im, 'width', w); attr(im, 'height', h);
    return im;
  }

  /**
   * The deepest Esri level with imagery at the backdrop's anchor. Asked once a
   * place: up from 17 while the next level has imagery, or down when 17 has
   * none. Until the answer is in, 17.
   */
  function esriDeepest(b: MapBackdrop): number {
    const key = b.lat.toFixed(4) + ',' + b.lon.toFixed(4);
    const known = esriDepth.get(key);
    if (known !== undefined) return known;
    const start = TILE_SOURCES.esri!.maxZoom;
    esriDepth.set(key, start);
    const has = (z: number): Promise<boolean> => fetch(esriTileMapUrl(b.lat, b.lon, z))
      .then((r) => (r.ok ? r.json() : null))
      .then((d: { data?: number[] } | null) => !!(d && d.data && d.data[0] === 1));
    (async () => {
      let z = start;
      if (await has(z)) {
        while (z < 21 && await has(z + 1)) z++;
      } else {
        while (z > 10 && !(await has(z - 1))) z--;
        z = Math.max(10, z - 1);
      }
      esriDepth.set(key, z);
      renderBackdrop();
    })().catch(() => { /* unanswered: 17 stays */ });
    return start;
  }

  /**
   * The site's backdrop, and over it the shown floor's own, so a storey's plan
   * keeps its place on the site. In Edit, the one being aligned takes the
   * pointer and gets an outline; the plan above it stops taking clicks until
   * Done, or a drag would move a building instead.
   */
  function renderBackdrop(): void {
    if (!gBackdrop) return;
    const rect = stage!.getBoundingClientRect();
    const k = mk();
    const shown = [backdropOf(0), floor ? backdropOf(floor) : undefined]
      .filter((b): b is MapBackdrop => !!b);
    const aligning = mode === 'edit' && sel?.kind === 'backdrop';
    svg!.classList.toggle('wm-aligning', aligning);
    let credit = '';
    clear(gBackdrop);
    shown.forEach((stored) => {
      let b = stored.kind === 'tiles' && mapLook && TILE_SOURCES[stored.provider]
        ? { ...stored, provider: mapLook } : stored;
      if (b.kind === 'tiles' && b.provider === 'esri' && !b.maxZoom) b = { ...b, maxZoom: esriDeepest(b) };
      const on = aligning && sel!.id === String(b.floor);
      const pivot = b.kind === 'tiles' ? { x: b.x, y: b.y } : { x: b.x + b.w / 2, y: b.y + b.h / 2 };
      const g = svgEl('g', { class: 'wm-backdrop' + (on ? ' is-sel' : ''), opacity: b.opacity });
      if (b.rotation) attr(g, 'transform', 'rotate(' + b.rotation + ' ' + pivot.x + ' ' + pivot.y + ')');
      if (b.kind === 'tiles') {
        const src = TILE_SOURCES[b.provider];
        credit = src ? src.credit : 'Map tiles: ' + (b.url.split('/')[2] || '');
        // One screen pixel of overlap hides the seams between tiles.
        tilesFor(b, view, rect.width, rect.height).forEach((t) =>
          g.appendChild(image(t.href, t.x, t.y, t.size + 1 / view.k, t.size + 1 / view.k)));
        if (on) {
          g.appendChild(svgEl('circle', {
            cx: b.x, cy: b.y, r: 6 * k, style: paint('none', 'var(--accent-warn)'), 'stroke-width': 2 * k,
          }));
        }
      } else {
        g.appendChild(image(imageHref(b.image), b.x, b.y, b.w, b.h));
        if (on) {
          g.appendChild(svgEl('rect', {
            x: b.x, y: b.y, width: b.w, height: b.h, style: paint('none', 'var(--accent-warn)'),
            'stroke-width': 1.5 * k, 'stroke-dasharray': (6 * k).toFixed(1) + ' ' + (4 * k).toFixed(1),
          }));
          g.appendChild(svgEl('rect', {
            class: 'wm-handle', 'data-bd-handle': String(b.floor),
            x: b.x + b.w - 7 * k, y: b.y + b.h - 7 * k, width: 14 * k, height: 14 * k, rx: 2,
            style: paint('var(--accent-warn)'),
          }));
        }
      }
      gBackdrop.appendChild(g);
    });
    const att = el('wmAttrib');
    if (att) att.textContent = credit;
  }

  /** The coverage estimate: two rings per access point, from the distance model. */
  function renderCoverage(): void {
    clear(gCover);
    if (!gCover || mode === 'edit' || !coverage || !(doc.metresPerUnit > 0)) return;
    const k = mk();
    doc.aps.forEach((a) => {
      if (floor !== 0 && a.floor !== floor) return;
      ([[WEAK_DBM, '--accent-warn', 0.04], [COVER_GOOD_DBM, '--accent-tx', 0.07]] as const).forEach(([dbm, tok, fill]) => {
        const c = svgEl('circle', {
          cx: a.x, cy: a.y, r: metresFromRssi(dbm) / doc.metresPerUnit,
          style: paint(paletteCss(tok, fill), paletteCss(tok, 0.45)),
          'stroke-width': k, 'stroke-dasharray': (5 * k).toFixed(1) + ' ' + (4 * k).toFixed(1),
        });
        const t = svgEl('title');
        text(t, 'About ' + metresFromRssi(dbm).toFixed(0) + ' m to ' + dbm + ' dBm (an estimate)');
        c.appendChild(t);
        gCover.appendChild(c);
      });
    });
  }

  function renderMeasure(to?: { x: number; y: number }): void {
    clear(gMeasure);
    if (!gMeasure || !measureFrom || !to) return;
    gMeasure.appendChild(svgEl('line', {
      x1: measureFrom.x, y1: measureFrom.y, x2: to.x, y2: to.y,
      style: paint('none', 'var(--accent-warn)'), 'stroke-width': 1.5, 'stroke-dasharray': '5 4',
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
        style: paint(drawing.length > 2 ? paletteCss('--accent-tx', 0.08) : 'none', 'var(--accent-rx)'),
        'stroke-width': 1.5 * k,
        'stroke-dasharray': (6 * k).toFixed(1) + ' ' + (5 * k).toFixed(1),
      }));
    }
    drawing.forEach((q, i) => {
      gDraft.appendChild(svgEl('circle', {
        cx: q.x, cy: q.y, r: (i === 0 ? 6 : 4) * k,
        style: paint(i === 0 ? 'var(--accent-rx)' : paletteCss('--accent-rx', 0.5)),
      }));
    });
  }

  function renderObjects(): void {
    clear(gObjects);
    if (!gObjects) return;
    doc.objects.forEach((o) => {
      const g = svgEl('g', { class: 'wm-obj', 'data-id': o.id });
      const skin = {
        // 0x22 / 255: the alpha the stored colour's '22' suffix used to give.
        style: paint(o.colour ? objectColour(o.colour, 0.133) : (OBJECT_FILL[o.kind] || 'none'),
          o.colour ? objectColour(o.colour) : (OBJECT_STROKE[o.kind] || 'none')),
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
          class: 'wm-obj-label', style: paint(o.colour ? objectColour(o.colour) : 'var(--text-main)'),
          'font-size': (FONT.obj * k).toFixed(1) + 'px', 'stroke-width': (HALO * k).toFixed(2),
        });
        text(t, o.label);
        g.appendChild(t);
      }
      if (o.kind === 'building' && o.floors > 1) {
        const t = svgEl('text', {
          x: o.x + 10 * k, y: o.y + (o.label ? 36 : 20) * k, class: 'wm-obj-sub',
          'font-size': (FONT.sub * k).toFixed(1) + 'px', 'stroke-width': (HALO * k).toFixed(2),
        });
        text(t, o.floors + ' floors');
        g.appendChild(t);
      }
      if (mode === 'edit' && sel?.kind === 'object' && sel.id === o.id) {
        g.appendChild(svgEl('rect', {
          x: o.x - 3 * k, y: o.y - 3 * k,
          width: Math.max(1, o.w) + 6 * k, height: Math.max(1, o.h) + 6 * k,
          rx: 10, style: paint('none', 'var(--accent-rx)'), 'stroke-width': k,
          'stroke-dasharray': (4 * k).toFixed(1) + ' ' + (3 * k).toFixed(1),
        }));
        // An outline has a handle on every corner, to move that corner, and
        // the box's corner handle, hollow, to scale the whole outline the way a
        // rectangle is sized.
        if (o.points.length >= 3) {
          o.points.forEach((q, i) => {
            g.appendChild(svgEl('rect', {
              class: 'wm-handle', 'data-vertex': o.id + ':' + i,
              x: q.x - 5 * k, y: q.y - 5 * k, width: 10 * k, height: 10 * k, rx: 2,
              style: paint('var(--accent-rx)'),
            }));
          });
          g.appendChild(svgEl('rect', {
            class: 'wm-handle', 'data-handle': o.id,
            x: o.x + o.w - 6 * k, y: o.y + o.h - 6 * k, width: 12 * k, height: 12 * k, rx: 2,
            style: paint(paletteCss('--bg-deep', 0.8), 'var(--accent-rx)'), 'stroke-width': 2 * k,
          }));
        } else if (o.kind !== 'label') {
          g.appendChild(svgEl('rect', {
            class: 'wm-handle', 'data-handle': o.id,
            x: o.x + o.w - 6 * k, y: o.y + o.h - 6 * k, width: 12 * k, height: 12 * k, rx: 2,
            style: paint('var(--accent-rx)'),
          }));
        }
      }
      gObjects.appendChild(g);
    });
  }

  function renderAps(counts: Record<string, number>,
                     signal: Record<string, ReturnType<typeof apSignal>>): void {
    clear(gAps);
    if (!gAps) return;
    const k = mk();
    const weakSpots = featureOn('wifiWeakSpotsEnabled');
    doc.aps.forEach((a) => {
      if (floor !== 0 && a.floor !== floor) return;
      const key = pinKey(a);
      const n = counts[key] || 0;
      const g = svgEl('g', { class: 'wm-ap', 'data-ap': key });
      const sig = weakSpots ? signal[key] : null;
      g.appendChild(svgEl('circle', {
        cx: a.x, cy: a.y, r: 13 * k,
        style: paint(n ? paletteCss('--accent-rx', 0.18) : paletteCss('--text-muted', 0.15),
          sig ? paletteCss(sig.tone) : n ? 'var(--accent-rx)' : paletteCss('--text-muted', 0.6)),
        'stroke-width': (sig ? 3 : 2) * k,
      }));
      if (sig) {
        const avg = svgEl('text', {
          x: a.x, y: a.y + 42 * k, class: 'wm-ap-label', style: paint(paletteCss(sig.tone)),
          'font-size': (FONT.client * k).toFixed(1) + 'px', 'stroke-width': (HALO * k).toFixed(2),
        });
        text(avg, 'avg ' + sig.avg + ' dBm' + (sig.weak ? ' · ' + sig.weak + ' weak' : ''));
        g.appendChild(avg);
      }
      const lbl = svgEl('text', {
        x: a.x, y: a.y + 30 * k, class: 'wm-ap-label',
        'font-size': (FONT.ap * k).toFixed(1) + 'px', 'stroke-width': (HALO * k).toFixed(2),
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
      if ((mode === 'edit' && sel?.kind === 'ap' && sel.id === key) ||
          (mode === 'view' && card?.kind === 'ap' && card.id === key)) {
        g.appendChild(svgEl('circle', {
          cx: a.x, cy: a.y, r: 19 * k, style: paint('none', 'var(--accent-rx)'),
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
   *
   * ── A LABEL WITH NO FREE SIDE IS LEFT OFF ───────────────────────────────
   *
   * Drawing it anyway, on the side it overlaps least, is what turned a busy
   * access point into a block of text nobody could read. The dot stays, and a
   * click on it opens its card. A client the search found, or whose card is
   * open, keeps its label wherever it lands.
   */
  function renderClients(groups: Map<string, { pin: MapAP; clients: WirelessClient[] }>): void {
    clear(gClients);
    drawnAt = new Map();
    hiddenLabels = 0;
    if (!gClients) return;
    if (mode === 'edit') { renderPins(); return; }
    const perUnit = doc.metresPerUnit > 0 ? doc.metresPerUnit : 0;
    const ring = clientMode === 'ring' && perUnit > 0;
    const k = mk();

    // Everything a label must keep off: the access points, their names and
    // averages, and the names written on the plan.
    const taken: Box[] = [];
    doc.aps.forEach((a) => {
      if (floor !== 0 && a.floor !== floor) return;
      taken.push({ x0: a.x - 16 * k, y0: a.y - 16 * k, x1: a.x + 16 * k, y1: a.y + 16 * k });
      const w = ((a.label || a.ap || a.ifaces[0] || '?').length * 5.8 + 6) * k;
      taken.push({ x0: a.x - w / 2, y0: a.y + 21 * k, x1: a.x + w / 2, y1: a.y + 46 * k });
    });
    doc.objects.forEach((o) => {
      if (!o.label) return;
      const x0 = o.x + 10 * k, base = o.y + (o.kind === 'label' ? 18 : 20) * k;
      taken.push({ x0, y0: base - FONT.obj * k, x1: x0 + o.label.length * 7.4 * k,
        y1: base + (o.kind === 'building' && o.floors > 1 ? 18 : 4) * k });
    });

    // A pinned client is drawn where it was pinned, connected or not: a camera
    // that has dropped off is the thing worth seeing. Pinned ones go first, so
    // the orbiting ones keep off them.
    interface Placed { c: WirelessClient | null; mac: string; pin: MapAP | null; x: number; y: number;
      lines: string[]; fixed: boolean }
    const placed: Placed[] = [];
    const live = new Map((wireless?.clients || []).map((c) => [c.mac.toUpperCase(), c]));
    const apOf = new Map<string, MapAP>();
    groups.forEach((grp) => grp.clients.forEach((c) => apOf.set(c.mac.toUpperCase(), grp.pin)));
    doc.clients.forEach((p) => {
      if (floor !== 0 && p.floor !== floor) return;
      const c = live.get(p.mac) || null;
      placed.push({ c, mac: p.mac, pin: apOf.get(p.mac) || null, x: p.x, y: p.y,
        lines: c ? clientLines(c) : [p.label || p.mac], fixed: true });
      drawnAt.set(p.mac, { x: p.x, y: p.y });
      taken.push({ x0: p.x - 7 * k, y0: p.y - 7 * k, x1: p.x + 7 * k, y1: p.y + 7 * k });
    });
    const pinnedMacs = new Set(doc.clients.map((p) => p.mac));
    groups.forEach((grp) => {
      if (floor !== 0 && grp.pin.floor !== floor) return;
      grp.clients.forEach((c, i) => {
        if (pinnedMacs.has(c.mac.toUpperCase())) return;
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
        placed.push({ c, mac: c.mac.toUpperCase(), pin: grp.pin, x, y, lines, fixed: false });
        drawnAt.set(c.mac.toUpperCase(), { x, y });
        taken.push({ x0: x - 7 * k, y0: y - 7 * k, x1: x + 7 * k, y1: y + 7 * k });
      });
    });

    placed.forEach(({ c, mac, pin, x, y, lines, fixed }) => {
      const hot = (card?.kind === 'client' && card.id === mac) || (!!c && matches(c));
      const g = svgEl('g', { class: 'wm-client', 'data-mac': mac });
      // The line to its access point, when that is drawn on this floor.
      if (pin && (floor === 0 || pin.floor === floor)) {
        g.appendChild(svgEl('line', {
          x1: pin.x, y1: pin.y, x2: x, y2: y,
          style: paint('none', paletteCss('--text-muted', 0.25)), 'stroke-width': k,
        }));
      }
      if (hot) {
        g.appendChild(svgEl('circle', {
          cx: x, cy: y, r: 10 * k, style: paint('none', 'var(--accent-warn)'), 'stroke-width': 2 * k,
        }));
      }
      if (fixed) {
        g.appendChild(svgEl('rect', {
          x: x - 8 * k, y: y - 8 * k, width: 16 * k, height: 16 * k, rx: 2,
          style: paint('none', paletteCss('--accent-alt', 0.7)), 'stroke-width': k,
        }));
      }
      g.appendChild(svgEl('circle', {
        cx: x, cy: y, r: 5 * k,
        style: c ? paint(signalColour(c.signal), paletteCss('--bg-deep', 0.7))
          : paint(paletteCss('--bg-deep', 0.7), paletteCss('--text-muted', 0.9)),
        'stroke-width': c ? k : 1.5 * k,
      }));
      // A wider target than the dot, so a click does not need a steady hand.
      g.appendChild(svgEl('circle', { cx: x, cy: y, r: 9 * k, style: paint('transparent') }));

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
        if (bestHits > 0 && !hot) {
          hiddenLabels++;
        } else {
          if (bestBox) taken.push(bestBox);
          const t = svgEl('text', {
            x: x + best.dx, y: y + best.dy, class: 'wm-client-label' + (hot ? ' is-hot' : ''),
            'text-anchor': best.anchor, 'font-size': (FONT.client * k).toFixed(1) + 'px',
            'stroke-width': (HALO * k).toFixed(2),
          });
          lines.forEach((line, i) => {
            const span = svgEl('tspan', { x: x + best.dx, dy: i ? LINE_H * k : 0 });
            text(span, line);
            t.appendChild(span);
          });
          g.appendChild(t);
        }
      }

      const title = svgEl('title');
      text(title, c
        ? [c.name || c.mac, c.comment, c.ip, c.mac, c.signal + ' dBm', c.ssid,
          ring && !fixed ? '≈' + metresFromRssi(c.signal).toFixed(1) + ' m (estimate)' : '']
          .filter(Boolean).join(' · ')
        : (lines[0] || mac) + ' · not connected now');
      g.appendChild(title);
      gClients.appendChild(g);
    });
  }

  /** The pinned clients in Edit, where they are dragged into place. */
  function renderPins(): void {
    const k = mk();
    doc.clients.forEach((p) => {
      if (floor !== 0 && p.floor !== floor) return;
      const g = svgEl('g', { class: 'wm-cpin', 'data-cpin': p.mac });
      g.appendChild(svgEl('rect', {
        x: p.x - 7 * k, y: p.y - 7 * k, width: 14 * k, height: 14 * k, rx: 2,
        style: paint(paletteCss('--accent-alt', 0.3), 'var(--accent-alt)'), 'stroke-width': 1.5 * k,
      }));
      if (sel?.kind === 'client' && sel.id === p.mac) {
        g.appendChild(svgEl('circle', {
          cx: p.x, cy: p.y, r: 14 * k, style: paint('none', 'var(--accent-rx)'), 'stroke-width': k,
          'stroke-dasharray': (4 * k).toFixed(1) + ' ' + (3 * k).toFixed(1),
        }));
      }
      const t = svgEl('text', {
        x: p.x, y: p.y + 20 * k, class: 'wm-ap-label',
        'font-size': (FONT.client * k).toFixed(1) + 'px', 'stroke-width': (HALO * k).toFixed(2),
      });
      text(t, p.label || p.mac);
      g.appendChild(t);
      gClients!.appendChild(g);
    });
  }

  /** Whether the search box finds this client. Two characters at least, so a
   *  single keystroke does not light up half the site. */
  function matches(c: WirelessClient): boolean {
    if (search.length < 2) return false;
    return [c.name, c.comment, c.ip, c.mac].some((v) => (v || '').toLowerCase().includes(search));
  }

  function render(): void {
    const map = ifaceToAp();
    const radio = ifaceToRadio();
    const groups = new Map<string, { pin: MapAP; clients: WirelessClient[] }>();
    const counts: Record<string, number> = {};
    let unplaced = 0;
    (wireless?.clients || []).forEach((c) => {
      const pin = pinFor(c, map, radio);
      if (!pin) { unplaced++; return; }
      const key = pinKey(pin);
      counts[key] = (counts[key] || 0) + 1;
      let grp = groups.get(key);
      if (!grp) { grp = { pin, clients: [] }; groups.set(key, grp); }
      grp.clients.push(c);
    });

    const signal: Record<string, ReturnType<typeof apSignal>> = {};
    weakClients = [];
    groups.forEach((grp, key) => {
      signal[key] = apSignal(grp.clients);
      grp.clients.forEach((c) => {
        if (Number(c.signal) < WEAK_DBM) weakClients.push({ c, ap: grp.pin.label || grp.pin.ap || key });
      });
    });
    weakClients.sort((a, b) => Number(a.c.signal) - Number(b.c.signal));
    pinClients = new Map([...groups].map(([key, grp]) => [key, grp.clients]));

    renderObjects();
    renderCoverage();
    renderAps(counts, signal);
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
          ? 'Click each corner of the ' + (drawKind === 'building' ? 'building' : 'area') + '. Three at least.'
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
      if (mode === 'view' && search.length >= 2) {
        const n = (wireless?.clients || []).filter(matches).length;
        bits.push(n ? n + ' client' + (n === 1 ? '' : 's') + ' match "' + search + '"; Enter goes to the next'
          : 'No client matches "' + search + '"');
      }
      if (mode === 'view' && hiddenLabels) {
        bits.push(hiddenLabels + ' label' + (hiddenLabels === 1 ? '' : 's') +
          ' left off where they would overlap; click a dot for its card');
      }
      if (mode === 'view' && coverage) {
        bits.push(doc.metresPerUnit > 0
          ? 'Coverage is an ESTIMATE from a typical indoor model: inner ring ' + COVER_GOOD_DBM +
            ' dBm, outer ' + WEAK_DBM + ' dBm. Walls and floors cut it short.'
          : 'Coverage needs a scale: measure one in Edit, or place a map.');
      }
      if (featureOn('wifiWeakSpotsEnabled') && weakClients.length) {
        bits.push(weakClients.length + ' below ' + WEAK_DBM + ' dBm: ' + weakClients.slice(0, 5).map((w) =>
          (w.c.comment || w.c.name || w.c.mac) + ' on ' + w.ap + ' ' + w.c.signal).join(', ') +
          (weakClients.length > 5 ? ', …' : ''));
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

  // The panel's fields are the app's fields (web/src/form.ts), one column wide:
  // the panel is narrow, and a field on the map should read like one anywhere.
  function fields(...parts: string[]): string {
    return '<div class="ff-grid ff-one">' + parts.join('') + '</div>';
  }

  function renderPanel(): void {
    const panel = el('wmPanel');
    if (!panel) return;
    // NEVER REBUILD A PANEL SOMEBODY IS USING. A wifi payload lands every few
    // seconds and a zoom now redraws too; replacing the markup under an open
    // field takes the focus and the half-typed label with it. Same guard the
    // topology map's detail panel carries, for the same reason.
    //
    // A FIELD, NOT A BUTTON. A clicked button keeps the focus too, and the
    // panel it changed - a backdrop placed, a card opened from a list - stayed
    // as it was until something else took the focus away.
    const active = document.activeElement;
    if (active && panel.contains(active) && active.matches('input, textarea, select')) return;
    // `.open` is what slides it in - the panel is parked off-canvas otherwise,
    // which is how the topology map's detail panel works and why this reuses it.
    if (mode !== 'edit') {
      const html = cardHtml();
      panel.classList.toggle('open', !!html);
      panel.innerHTML = html;
      wireCard();
      return;
    }
    panel.classList.add('open');

    if (sel?.kind === 'object') {
      const o = doc.objects.find((x) => x.id === sel!.id);
      if (o) { panel.innerHTML = objectPanel(o); wireObjectPanel(o); return; }
    }
    if (sel?.kind === 'ap') {
      const a = doc.aps.find((x) => pinKey(x) === sel!.id);
      if (a) { panel.innerHTML = apPanel(a); wireApPanel(a); return; }
    }
    if (sel?.kind === 'client') {
      const p = doc.clients.find((x) => x.mac === sel!.id);
      if (p) { panel.innerHTML = clientPinPanel(p); wireClientPin(p); return; }
    }
    panel.innerHTML = trayPanel();
    wireTray();
    wireBackdrop();
  }

  // ── the cards, in View ────────────────────────────────────────────────────

  function row(k: string, v: string): string {
    return v ? '<dt>' + esc(k) + '</dt><dd>' + esc(v) + '</dd>' : '';
  }

  function cardHead(title: string, colour: string): string {
    return '<div class="topo-panel-hdr" style="color:' + colour + '">' +
      '<span class="topo-panel-name">' + esc(title) + '</span>' +
      '<button class="topo-panel-close" id="wmCardClose" type="button" aria-label="Close">&times;</button></div>';
  }

  function cardHtml(): string {
    if (card?.kind === 'client') {
      const c = (wireless?.clients || []).find((x) => x.mac.toUpperCase() === card!.id);
      const fixed = doc.clients.find((x) => x.mac === card!.id);
      if (!c) {
        return fixed
          ? cardHead(fixed.label || fixed.mac, 'var(--text-muted)') +
            '<div class="topo-badges">' + pill('not connected', 'muted') + pill('pinned', 'alt') + '</div>' +
            '<dl class="topo-kv">' + row('MAC', fixed.mac) + row('Floor', String(fixed.floor)) + '</dl>'
          : '';
      }
      const key = [...pinClients].find(([, list]) => list.includes(c))?.[0] || '';
      const pin = doc.aps.find((a) => pinKey(a) === key);
      return cardHead(c.comment || c.name || c.mac, signalColour(c.signal)) +
        '<div class="topo-badges">' + pill(c.signal + ' dBm', signalTone(c.signal)) +
          (c.band ? pill(c.band, bandTone(c.band)) : '') + (c.standard ? pill(c.standard, 'muted') : '') +
          (fixed ? pill('pinned', 'alt') : '') + '</div>' +
        '<dl class="topo-kv">' +
          row('Name', c.name) + row('Comment', c.comment) + row('IP', c.ip) + row('MAC', c.mac) +
          row('SSID', c.ssid) + row('Access point', pin ? pin.label || pin.ap || key : '') +
          row('Interface', c.iface) +
          row('Distance', '≈' + metresFromRssi(c.signal).toFixed(1) + ' m from its AP (an estimate)') +
          row('Tx / Rx', c.txRate || c.rxRate ? parseTxRate(c.txRate) + ' / ' + parseTxRate(c.rxRate) : '') +
          row('Uptime', c.uptime) +
        '</dl>' +
        '<div class="wm-bd-actions">' +
          (pin ? '<button class="sbtn sbtn-outline is-sm" type="button" data-card-ap="' + esc(key) +
            '">Its access point</button>' : '') +
          (fixed ? '' : '<button class="sbtn sbtn-outline is-sm" type="button" data-card-pin="' + esc(c.mac) +
            '" title="For a camera, a sensor or anything that does not move: draw it where it stands">Pin to the map</button>') +
        '</div>';
    }
    if (card?.kind === 'ap') {
      const pin = doc.aps.find((a) => pinKey(a) === card!.id);
      if (!pin) return '';
      const clients = (pinClients.get(card.id) || []).slice().sort((a, b) => b.signal - a.signal);
      const sig = apSignal(clients);
      const radios = (wifi?.radios || []).filter((r) => (pin.ap ? r.ap === pin.ap : pin.ifaces.includes(r.name)));
      const nets = (wifi?.networks || []).filter((n) => (pin.ap ? n.ap === pin.ap
        : pin.ifaces.includes(n.name) || pin.ifaces.includes(n.radio)));
      const tone: Record<string, PillTone> = { '--accent-err': 'err', '--accent-warn': 'warn', '--accent-ok': 'ok' };
      return cardHead(pin.label || pin.ap || pin.ifaces[0] || '?', 'var(--accent-rx)') +
        (pin.ap && pin.ap !== pin.label ? '<div class="wm-panel-sub">' + esc(pin.ap) + '</div>' : '') +
        '<div class="topo-badges">' + pill(clients.length + ' client' + (clients.length === 1 ? '' : 's'), 'rx') +
          (sig ? pill('avg ' + sig.avg + ' dBm', tone[sig.tone] || 'muted') +
            (sig.weak ? pill(sig.weak + ' below ' + WEAK_DBM, 'err') : '') : '') +
          (pin.floor > 1 ? pill('floor ' + pin.floor, 'muted') : '') + '</div>' +
        (radios.length ? '<div class="topo-panel-sec">Radios</div><dl class="topo-kv">' +
          radios.map((r) => row(r.name, [r.band, r.frequency, r.channelWidth].filter(Boolean).join(' · ') +
            (r.running ? '' : ' · not running'))).join('') + '</dl>' : '') +
        (nets.length ? '<div class="topo-panel-sec">Networks</div><dl class="topo-kv">' +
          nets.map((n) => row(n.ssid || n.name, [n.name, n.band, n.clients + ' clients'].filter(Boolean)
            .join(' · '))).join('') + '</dl>' : '') +
        '<div class="wm-panel-note">Estimated reach: about ' + metresFromRssi(COVER_GOOD_DBM).toFixed(0) +
          ' m at ' + COVER_GOOD_DBM + ' dBm and ' + metresFromRssi(WEAK_DBM).toFixed(0) + ' m at ' + WEAK_DBM +
          ' dBm with nothing in the way; walls take that down.</div>' +
        (clients.length ? '<div class="topo-panel-sec">Clients</div>' + clients.map((c) =>
          '<button type="button" class="wm-card-row" data-card-mac="' + esc(c.mac) + '">' +
            '<span>' + esc(c.comment || c.name || c.mac) + '</span>' +
            '<span style="color:' + signalColour(c.signal) + '">' + c.signal + ' dBm</span></button>').join('') : '');
    }
    return '';
  }

  function wireCard(): void {
    el('wmCardClose')?.addEventListener('click', () => { card = null; render(); });
    el('wmPanel')?.querySelectorAll<HTMLElement>('[data-card-mac]').forEach((b) => {
      b.addEventListener('click', () => {
        card = { kind: 'client', id: (b.dataset.cardMac || '').toUpperCase() }; render();
      });
    });
    el('wmPanel')?.querySelectorAll<HTMLElement>('[data-card-ap]').forEach((b) => {
      b.addEventListener('click', () => { card = { kind: 'ap', id: b.dataset.cardAp || '' }; render(); });
    });
    el('wmPanel')?.querySelectorAll<HTMLElement>('[data-card-pin]').forEach((b) => {
      b.addEventListener('click', () => pinClient(b.dataset.cardPin || ''));
    });
  }

  /** Pins a client where it is drawn now and opens it in Edit, to be dragged into place. */
  function pinClient(macRaw: string): void {
    const mac = macRaw.toUpperCase();
    const c = (wireless?.clients || []).find((x) => x.mac.toUpperCase() === mac);
    if (!c || doc.clients.some((x) => x.mac === mac)) return;
    const at = drawnAt.get(mac) || centre();
    const ap = [...pinClients].find(([, list]) => list.includes(c))?.[0];
    const apPin = ap ? doc.aps.find((a) => pinKey(a) === ap) : undefined;
    doc.clients.push({ mac, label: c.comment || c.name || mac, x: at.x, y: at.y,
      floor: apPin ? apPin.floor : floor || 1 });
    dirty = true;
    card = null;
    mode = 'edit';
    sel = { kind: 'client', id: mac };
    syncChrome(); render();
    note('Drag it to where it stands, then Save.');
  }

  /** Centres the map on the next client the search finds, and opens its card. */
  function findNext(): void {
    const hits = (wireless?.clients || []).filter(matches);
    if (!hits.length) return;
    searchAt = (searchAt + 1) % hits.length;
    const c = hits[searchAt]!;
    if (!drawnAt.has(c.mac.toUpperCase()) && floor !== 0) {
      floor = 0;
      syncFloors();
      render();
    }
    const p = drawnAt.get(c.mac.toUpperCase());
    if (!p) {
      note((c.comment || c.name || c.mac) + ' is on an access point that is not on the map.');
      return;
    }
    // Left of the middle, because its card is about to cover the right side.
    const rect = stage!.getBoundingClientRect();
    view.x = rect.width * 0.4 - p.x * view.k;
    view.y = rect.height / 2 - p.y * view.k;
    saveView();
    card = { kind: 'client', id: c.mac.toUpperCase() };
    render();
  }

  // ── the backdrop, in Edit ─────────────────────────────────────────────────

  function backdropSection(): string {
    const b = backdropOf(floor);
    const aligning = sel?.kind === 'backdrop';
    let body: string;
    if (!b) {
      body = '<div class="wm-panel-note">' + (floor
        ? 'A plan of this floor, drawn over the site\'s backdrop while this floor is shown.'
        : 'A site plan or photo, or a map, drawn under every floor. A map also sets the scale.') + '</div>' +
        '<div class="wm-bd-actions">' +
          '<button class="sbtn sbtn-outline is-sm" id="wmfBdUpload" type="button">Upload image</button>' +
          (floor === 0 ? '<button class="sbtn sbtn-outline is-sm" id="wmfBdMap" type="button">Use a map</button>' : '') +
        '</div>';
    } else {
      const sources: Array<[string, string]> = [
        ...Object.entries(TILE_SOURCES).map(([key, src]): [string, string] => [key, src.label]),
        ['custom', 'Custom tile URL'],
      ];
      body = (b.kind === 'tiles'
        ? fields(
          fieldHtml({ id: 'wmfBdProvider', label: 'Map' },
            '<select class="sform-input" id="wmfBdProvider">' + sources.map(([key, label]) =>
              '<option value="' + key + '"' + (key === b.provider ? ' selected' : '') + '>' + esc(label) +
              '</option>').join('') + '</select>'),
          b.provider === 'custom'
            ? fieldHtml({
              id: 'wmfBdUrl', label: 'Tile URL',
              help: 'An https template with {z}, {x} and {y}, Web Mercator tiles. Use a source whose terms ' +
                'allow it: every viewer\'s browser fetches from it.',
            }, '<input class="sform-input" id="wmfBdUrl" value="' + esc(b.url) +
              '" placeholder="https://…/{z}/{x}/{y}.png">')
            : '',
          fieldHtml({
            id: 'wmfBdZoom', label: 'Deepest zoom',
            help: 'Past this level the map is stretched rather than fetched. Default for the ' +
              'satellite is as deep as Esri has imagery at this place; set a level if it still says "no data".',
          }, '<select class="sform-input" id="wmfBdZoom"><option value="0"' + (b.maxZoom ? '' : ' selected') +
            '>Default</option>' + [15, 16, 17, 18, 19, 20, 21].map((z) => '<option value="' + z + '"' +
            (b.maxZoom === z ? ' selected' : '') + '>' + z + '</option>').join('') + '</select>'),
          fieldHtml({
            id: 'wmfBdWhere', label: 'Location',
            help: 'Coordinates as "55.7539, 37.6208", or an address, looked up at nominatim.openstreetmap.org. ' +
              'The point is put in the middle of the screen.',
          }, '<input class="sform-input" id="wmfBdWhere" value="' + b.lat.toFixed(6) + ', ' + b.lon.toFixed(6) + '">'))
        : '<div class="wm-panel-note">An image, ' + Math.round(b.w) + ' × ' + Math.round(b.h) + ' units.</div>') +
        fields(
          fieldHtml({ id: 'wmfBdOpacity', label: 'Opacity' },
            '<input class="sform-input" id="wmfBdOpacity" type="range" min="0.1" max="1" step="0.05" value="' +
            String(b.opacity) + '">'),
          fieldHtml({
            id: 'wmfBdTurn', label: 'Rotation, degrees',
            help: 'Turn the ' + (b.kind === 'tiles' ? 'map about its anchor' : 'image about its centre') +
              ' until the buildings sit square to the plan.',
          }, '<div class="wm-bd-turn"><input class="sform-input" id="wmfBdTurn" type="range" min="-180" max="180" ' +
            'step="0.5" value="' + String(b.rotation || 0) + '">' +
            '<input class="sform-input" id="wmfBdTurnN" type="number" min="-180" max="180" step="0.5" value="' +
            String(b.rotation || 0) + '" aria-label="Rotation in degrees"></div>'),
        ) +
        '<div class="wm-bd-actions">' +
          '<button class="sbtn sbtn-outline is-sm" id="wmfBdAlign" type="button">' +
            (aligning ? 'Done' : b.kind === 'image' ? 'Move and size' : 'Move') + '</button>' +
          (b.kind === 'image' ? '<button class="sbtn sbtn-outline is-sm" id="wmfBdUpload" type="button">Replace</button>' : '') +
          '<button class="sbtn sbtn-danger is-sm" id="wmfBdRemove" type="button">Remove</button>' +
        '</div>' +
        (aligning ? '<div class="wm-panel-note">' + (b.kind === 'image'
          ? 'Drag the image to move it, and its corner to size it.'
          : 'Drag the map until the plan sits on it.') + ' The plan takes no clicks until Done.</div>' : '');
    }
    return '<div class="wm-panel-title" style="margin-top:.9rem">Backdrop</div>' +
      '<div class="wm-panel-sub">' + (floor ? 'Floor ' + floor : 'Site, under every floor') + '</div>' + body +
      '<input type="file" id="wmfBdFile" accept="image/png,image/jpeg" hidden>';
  }

  function wireBackdrop(): void {
    const b = backdropOf(floor);
    const file = el<HTMLInputElement>('wmfBdFile');
    el('wmfBdUpload')?.addEventListener('click', () => file?.click());
    file?.addEventListener('change', () => { const f = file.files?.[0]; if (f) upload(f); });
    el('wmfBdMap')?.addEventListener('click', () => {
      const where = window.prompt('Where is the site? Coordinates as "55.7539, 37.6208", or an address.', '');
      if (where) placeMap(where, null);
    });
    if (!b) return;
    el<HTMLSelectElement>('wmfBdProvider')?.addEventListener('change', (e) => {
      b.provider = (e.target as HTMLSelectElement).value;
      (e.target as HTMLSelectElement).blur();
      // The plan's own map is being chosen, so this viewer sees what was chosen.
      mapLook = '';
      lsSet('mkd_wifi_map_look', mapLook);
      dirty = true; syncChrome(); render();
    });
    el<HTMLSelectElement>('wmfBdZoom')?.addEventListener('change', (e) => {
      b.maxZoom = parseInt((e.target as HTMLSelectElement).value, 10) || 0;
      dirty = true; syncChrome(); renderBackdrop();
    });
    el<HTMLInputElement>('wmfBdUrl')?.addEventListener('change', (e) => {
      b.url = (e.target as HTMLInputElement).value.trim();
      dirty = true; syncChrome(); render();
    });
    el<HTMLInputElement>('wmfBdWhere')?.addEventListener('change', (e) => {
      placeMap((e.target as HTMLInputElement).value, b);
    });
    el<HTMLInputElement>('wmfBdOpacity')?.addEventListener('input', (e) => {
      b.opacity = parseFloat((e.target as HTMLInputElement).value) || 1;
      dirty = true; syncChrome(); renderBackdrop();
    });
    const turn = (v: string, other: string): void => {
      b.rotation = Math.max(-180, Math.min(180, parseFloat(v) || 0));
      const o = el<HTMLInputElement>(other);
      if (o) o.value = String(b.rotation);
      dirty = true; syncChrome(); renderBackdrop();
    };
    el<HTMLInputElement>('wmfBdTurn')?.addEventListener('input', (e) =>
      turn((e.target as HTMLInputElement).value, 'wmfBdTurnN'));
    el<HTMLInputElement>('wmfBdTurnN')?.addEventListener('input', (e) =>
      turn((e.target as HTMLInputElement).value, 'wmfBdTurn'));
    el('wmfBdAlign')?.addEventListener('click', () => {
      sel = sel?.kind === 'backdrop' ? null : { kind: 'backdrop', id: String(floor) };
      render();
    });
    el('wmfBdRemove')?.addEventListener('click', () => {
      doc.backdrops = doc.backdrops.filter((x) => x !== b);
      sel = null; dirty = true; syncChrome(); render();
    });
  }

  /** Where the screen's middle is, in canvas units. */
  function centre(): MapPoint {
    const rect = stage!.getBoundingClientRect();
    return { x: (rect.width / 2 - view.x) / view.k, y: (rect.height / 2 - view.y) / view.k };
  }

  function locate(q: string): Promise<{ lat: number; lon: number } | null> {
    const ll = parseLatLon(q);
    if (ll || !q.trim()) return Promise.resolve(ll);
    return fetch('https://nominatim.openstreetmap.org/search?format=json&limit=1&q=' + encodeURIComponent(q))
      .then((r) => (r.ok ? r.json() : []))
      .then((d: Array<{ lat: string; lon: string }>) => (d && d[0]
        ? { lat: Number(d[0].lat), lon: Number(d[0].lon) } : null))
      .catch(() => null);
  }

  /** Puts the place asked for in the middle of the screen, as the map's anchor. */
  function placeMap(where: string, b: MapBackdrop | null): void {
    locate(where).then((ll) => {
      if (!ll) { note('Nothing found for "' + where + '".'); return; }
      const c = centre();
      if (!b) {
        doc.backdrops = doc.backdrops.filter((x) => x.floor !== 0);
        b = { floor: 0, kind: 'tiles', image: '', x: 0, y: 0, w: 0, h: 0, opacity: 1, rotation: 0,
          provider: 'osm', url: '', lat: 0, lon: 0, maxZoom: 0 };
        doc.backdrops.push(b);
      }
      Object.assign(b, { lat: ll.lat, lon: ll.lon, x: c.x, y: c.y });
      // The scale the server sets on save, so the ring and the coverage are
      // right before then.
      doc.metresPerUnit = tileMetresPerUnit(ll.lat);
      dirty = true;
      syncChrome(); render();
    });
  }

  function upload(f: File): void {
    const target = floor;
    note('Uploading ' + f.name + '…');
    fetch('/api/router-doc/image?routerId=' + encodeURIComponent(routerID), {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': f.type || 'application/octet-stream' }, body: f,
    })
      .then((r) => r.json().then((d) => (r.ok ? d : Promise.reject(new Error((d && d.error) || r.statusText)))))
      .then((d: { id: string; w: number; h: number }) => {
        const old = backdropOf(target);
        let box: { x: number; y: number; w: number; h: number };
        if (old && old.kind === 'image') {
          // A replacement keeps the place and the width the old one was aligned to.
          box = { x: old.x, y: old.y, w: old.w, h: old.w * d.h / d.w };
        } else {
          const k = Math.min(1, 2000 / Math.max(d.w, d.h)), c = centre();
          box = { x: c.x - d.w * k / 2, y: c.y - d.h * k / 2, w: d.w * k, h: d.h * k };
        }
        doc.backdrops = doc.backdrops.filter((x) => x.floor !== target);
        doc.backdrops.push({ floor: target, kind: 'image', image: d.id, ...box, opacity: old?.opacity || 1,
          rotation: old && old.kind === 'image' ? old.rotation || 0 : 0, provider: '', url: '', lat: 0, lon: 0,
          maxZoom: 0 });
        sel = { kind: 'backdrop', id: String(target) };
        dirty = true;
        syncChrome(); render();
      })
      .catch((e: Error) => note('Not uploaded: ' + e.message));
  }

  function objectPanel(o: MapObject): string {
    const poly = o.points.length >= 3;
    return '<div class="wm-panel-title">' + esc(o.kind) + '</div>' +
      fields(
        fieldHtml({ id: 'wmfLabel', label: 'Label' },
          '<input class="sform-input" id="wmfLabel" value="' + esc(o.label) + '">'),
        // STOREYS ARE A BUILDING'S. A fence and a plot have one apiece, and a
        // storey count on them put floors in the picker nothing could be on.
        o.kind === 'building'
          ? fieldHtml({ id: 'wmfFloors', label: 'Storeys' },
            '<input class="sform-input" id="wmfFloors" type="number" min="1" max="64" value="' +
            String(o.floors) + '">')
          : '',
        poly
          ? ''
          : fieldHtml({ id: 'wmfW', label: 'Width' },
            '<input class="sform-input" id="wmfW" type="number" value="' + String(Math.round(o.w)) + '">') +
            fieldHtml({ id: 'wmfH', label: 'Height' },
              '<input class="sform-input" id="wmfH" type="number" value="' + String(Math.round(o.h)) + '">'),
        fieldHtml({ label: 'Colour' }, '<div class="wm-swatches" id="wmfColours">' +
          PALETTE.map(([c, tok]) => '<button type="button" class="wm-swatch' +
            (c === o.colour ? ' is-on' : '') + '" data-colour="' + esc(c) + '"' +
            ' style="background:' + (tok ? paletteCss(tok) : 'transparent') + '"' +
            ' title="' + (c ? esc(c) : 'default') + '"></button>').join('') + '</div>'),
      ) +
      // A note about the shape as a whole, not about one field, so it stays
      // outside the grid.
      (poly
        ? '<div class="wm-panel-note">' + o.points.length + ' corners. Drag one to ' +
          'move it, the outline to move the whole shape, or the hollow square at the corner of its ' +
          'box to scale it.</div>'
        : '') +
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
      fields(
        fieldHtml({ id: 'wmfApLabel', label: 'Label' },
          '<input class="sform-input" id="wmfApLabel" value="' + esc(a.label) + '">'),
        fieldHtml({ id: 'wmfApFloor', label: 'Floor' },
          '<input class="sform-input" id="wmfApFloor" type="number" min="1" max="64" value="' +
          String(a.floor) + '">'),
      ) +
      '<div class="wm-panel-note">Radios: ' + esc(a.ifaces.join(', ') || '-') + '</div>' +
      '<button class="sbtn sbtn-danger wm-panel-btn" id="wmfApDelete">Take off the map</button>';
  }

  function clientPinPanel(p: MapClient): string {
    return '<div class="wm-panel-title">Pinned client</div>' +
      '<div class="wm-panel-sub">' + esc(p.mac) + '</div>' +
      fields(
        fieldHtml({ id: 'wmfCpLabel', label: 'Label' },
          '<input class="sform-input" id="wmfCpLabel" value="' + esc(p.label) + '">'),
        fieldHtml({ id: 'wmfCpFloor', label: 'Floor' },
          '<input class="sform-input" id="wmfCpFloor" type="number" min="1" max="64" value="' +
          String(p.floor) + '">'),
      ) +
      '<div class="wm-panel-note">Drag it to where it stands. In View it is drawn here, with a line to the ' +
        'access point it is on, and greyed while it is not connected.</div>' +
      '<button class="sbtn sbtn-danger wm-panel-btn" id="wmfCpDelete" type="button">Unpin</button>';
  }

  function wireClientPin(p: MapClient): void {
    el<HTMLInputElement>('wmfCpLabel')?.addEventListener('change', (e) => {
      p.label = (e.target as HTMLInputElement).value; dirty = true; syncChrome(); render();
    });
    el<HTMLInputElement>('wmfCpFloor')?.addEventListener('change', (e) => {
      p.floor = Math.max(1, Math.min(64, parseInt((e.target as HTMLInputElement).value, 10) || 1));
      dirty = true; syncChrome(); render();
    });
    el('wmfCpDelete')?.addEventListener('click', () => {
      doc.clients = doc.clients.filter((x) => x !== p);
      sel = null; dirty = true; syncChrome(); render();
    });
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
          : '<button class="sbtn sbtn-outline is-sm" data-place="' + esc(c.key) + '">Place</button>') +
      '</div>';
    }).join('');
    const map = !!siteMap();
    return '<div class="wm-panel-title">Site</div>' +
      fields(fieldHtml({
        id: 'wmfScale', label: 'Metres per canvas unit',
        help: map
          ? 'Set by the map. Size the buildings to match it.'
          : 'Draw a wall you know the length of, read its Width, and divide: ' +
            '40 m across 160 units is 0.25. The Signal ring and the coverage estimate use it.',
      }, '<input class="sform-input" id="wmfScale" type="number" step="0.01" min="0" value="' +
        String(doc.metresPerUnit ? +doc.metresPerUnit.toFixed(4) : '') + '" placeholder="e.g. 0.25"' +
        (map ? ' disabled' : '') + '>')) +
      backdropSection() +
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
    on('wmCoverage', coverage);
    const map = siteMap();
    const builtIn = !!map && !!TILE_SOURCES[map.provider];
    on('wmSatellite', builtIn && (mapLook || map!.provider) === 'esri');
    const sat = el('wmSatellite'); if (sat) sat.style.display = builtIn ? '' : 'none';
    const tools = el('wmEditTools'); if (tools) tools.style.display = mode === 'edit' ? '' : 'none';
    const save = el('wmSaveTools'); if (save) save.style.display = mode === 'edit' ? '' : 'none';
    const cm = el('wmClientModes'); if (cm) cm.style.display = mode === 'edit' ? 'none' : '';
    const lf = el('wmLabelFields'); if (lf) lf.style.display = mode === 'edit' ? 'none' : '';
    const vt = el('wmViewTools'); if (vt) vt.style.display = mode === 'edit' ? 'none' : '';
    const btn = el('wmSave');
    if (btn) {
      // Unsaved changes promote Save to the primary action; the two variants
      // are swapped, not stacked, because -outline is declared after -primary.
      btn.classList.toggle('sbtn-primary', dirty);
      btn.classList.toggle('sbtn-outline', !dirty);
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
      backdrops: got.backdrops || [],
      clients: got.clients || [],
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

  // ── export ────────────────────────────────────────────────────────────────
  //
  // The view on screen as a PNG. The drawing is an SVG styled by the page's
  // stylesheet, and an SVG drawn as an image sees neither the stylesheet nor
  // anything outside itself: so each element takes its computed style inline,
  // and each image, a tile or the uploaded plan, is fetched and inlined. A tile
  // whose provider does not allow that is left out and said so.
  const EXPORT_STYLE = ['fill', 'fill-opacity', 'stroke', 'stroke-width', 'stroke-dasharray',
    'stroke-linejoin', 'stroke-opacity', 'paint-order', 'opacity', 'font-family', 'font-size',
    'font-weight', 'text-anchor', 'dominant-baseline', 'display', 'visibility'];

  function dataUrl(href: string): Promise<string> {
    return fetch(href, { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.blob() : Promise.reject(new Error(String(r.status)))))
      .then((b) => new Promise<string>((ok, fail) => {
        const fr = new FileReader();
        fr.onload = () => ok(String(fr.result));
        fr.onerror = () => fail(fr.error);
        fr.readAsDataURL(b);
      }));
  }

  async function exportPng(): Promise<void> {
    const rect = svg!.getBoundingClientRect();
    const clone = svg!.cloneNode(true) as SVGSVGElement;
    const from = svg!.querySelectorAll('*'), to = clone.querySelectorAll('*');
    from.forEach((src, i) => {
      const cs = getComputedStyle(src);
      (to[i] as Element).setAttribute('style',
        EXPORT_STYLE.map((p) => p + ':' + cs.getPropertyValue(p)).join(';'));
    });
    let missed = 0;
    await Promise.all(Array.from(clone.querySelectorAll('image')).map((im) =>
      dataUrl(im.getAttribute('href') || '').then((u) => im.setAttribute('href', u))
        .catch(() => { im.remove(); missed++; })));
    clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
    clone.setAttribute('width', String(rect.width));
    clone.setAttribute('height', String(rect.height));
    clone.insertBefore(svgEl('rect', { width: '100%', height: '100%', style: 'fill:' + paletteColour('--bg-deep') }),
      clone.firstChild);

    const url = URL.createObjectURL(new Blob([new XMLSerializer().serializeToString(clone)],
      { type: 'image/svg+xml' }));
    const img = new Image();
    await new Promise<void>((ok, fail) => { img.onload = () => ok(); img.onerror = () => fail(); img.src = url; });
    URL.revokeObjectURL(url);
    const dpr = window.devicePixelRatio || 1;
    const canvas = document.createElement('canvas');
    canvas.width = Math.round(rect.width * dpr);
    canvas.height = Math.round(rect.height * dpr);
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.scale(dpr, dpr);
    ctx.drawImage(img, 0, 0, rect.width, rect.height);
    // A map's credit goes with every copy of it.
    const credit = el('wmAttrib')?.textContent || '';
    if (credit) {
      ctx.font = '10px ' + monoFont();
      ctx.fillStyle = paletteColour('--text-muted');
      ctx.fillText(credit, 6, rect.height - 6);
    }
    const blob = await new Promise<Blob | null>((ok) => canvas.toBlob(ok, 'image/png'));
    if (!blob) return;
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'wifi-map-' + (routerID || 'site') + (floor ? '-floor-' + floor : '') + '.png';
    a.click();
    window.setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    if (missed) note(missed + ' map tile' + (missed === 1 ? ' was' : 's were') + ' left out: the provider does not allow copying them.');
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
        id, kind: drawKind, label: '', ...bounds(pts),
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
    let drag: { kind: 'object' | 'ap' | 'resize' | 'vertex' | 'pan' | 'backdrop' | 'bdresize' | 'cpin'; id: string;
      px: number; py: number; ox: number; oy: number; vi?: number;
      pts?: MapPoint[] } | null = null;
    let moved = 0;
    /** What a View click landed on, read when it turns out not to be a pan. */
    let downOn: Element | null = null;

    svg!.addEventListener('pointerdown', (e) => {
      const target = e.target as Element | null;
      moved = 0;
      downOn = target;
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

      const bdHandle = target?.closest?.('[data-bd-handle]') as SVGElement | null;
      const bdEl = target?.closest?.('.wm-backdrop.is-sel') as SVGElement | null;
      const handle = target?.closest?.('.wm-handle') as SVGElement | null;
      const vertex = handle?.getAttribute('data-vertex') || '';
      const objEl = target?.closest?.('.wm-obj') as SVGElement | null;
      const apEl = target?.closest?.('.wm-ap') as SVGElement | null;
      const cpinEl = target?.closest?.('.wm-cpin') as SVGElement | null;

      const bd = sel?.kind === 'backdrop' ? backdropOf(Number(sel.id)) : undefined;
      if (mode === 'edit' && bd && bdHandle) {
        drag = { kind: 'bdresize', id: sel!.id, px: p.x, py: p.y, ox: bd.w, oy: bd.h };
      } else if (mode === 'edit' && bd && bdEl) {
        drag = { kind: 'backdrop', id: sel!.id, px: p.x, py: p.y, ox: bd.x, oy: bd.y };
      } else if (mode === 'edit' && vertex) {
        const [id, idx] = vertex.split(':');
        const o = doc.objects.find((x) => x.id === id);
        const vi = parseInt(idx || '', 10);
        const q = o?.points[vi];
        if (o && q) drag = { kind: 'vertex', id: o.id, vi, px: p.x, py: p.y, ox: q.x, oy: q.y };
      } else if (mode === 'edit' && handle) {
        const id = handle.getAttribute('data-handle') || '';
        const o = doc.objects.find((x) => x.id === id);
        if (o) {
          drag = { kind: 'resize', id, px: p.x, py: p.y, ox: o.w, oy: o.h,
            pts: o.points.map((q) => ({ ...q })) };
        }
      } else if (mode === 'edit' && cpinEl) {
        const id = cpinEl.getAttribute('data-cpin') || '';
        const c = doc.clients.find((x) => x.mac === id);
        if (c) { sel = { kind: 'client', id }; drag = { kind: 'cpin', id, px: p.x, py: p.y, ox: c.x, oy: c.y }; }
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
          const w = Math.max(8, drag.ox + (p.x - drag.px)), h = Math.max(8, drag.oy + (p.y - drag.py));
          if (drag.pts && drag.pts.length >= 3) {
            // AN OUTLINE SCALES FROM ITS BOX'S CORNER, from the corners it had
            // when the drag started, so the shape does not drift.
            const sx = w / Math.max(1, drag.ox), sy = h / Math.max(1, drag.oy);
            const x0 = o.x, y0 = o.y;
            o.points = drag.pts.map((q) => ({ x: x0 + (q.x - x0) * sx, y: y0 + (q.y - y0) * sy }));
            Object.assign(o, bounds(o.points));
          } else {
            o.w = w;
            o.h = h;
          }
        }
      } else if (drag.kind === 'ap') {
        const a = doc.aps.find((x) => pinKey(x) === drag!.id);
        if (a) { a.x = drag.ox + (p.x - drag.px); a.y = drag.oy + (p.y - drag.py); }
      } else if (drag.kind === 'cpin') {
        const c = doc.clients.find((x) => x.mac === drag!.id);
        if (c) { c.x = drag.ox + (p.x - drag.px); c.y = drag.oy + (p.y - drag.py); }
      } else if (drag.kind === 'backdrop') {
        const b = backdropOf(Number(drag.id));
        if (b) { b.x = drag.ox + (p.x - drag.px); b.y = drag.oy + (p.y - drag.py); }
      } else if (drag.kind === 'bdresize') {
        // THE ASPECT IS KEPT: a floor plan stretched one way is a wrong plan.
        // The pull is measured along the image's own width, which a rotation
        // has turned away from the screen's.
        const b = backdropOf(Number(drag.id));
        if (b) {
          const t = (b.rotation || 0) * Math.PI / 180;
          const along = (p.x - drag.px) * Math.cos(t) + (p.y - drag.py) * Math.sin(t);
          const cx = b.x + b.w / 2, cy = b.y + b.h / 2;
          b.w = Math.max(20, drag.ox + along);
          b.h = b.w * drag.oy / drag.ox;
          // A turned image is sized about its centre, which it turns about, or
          // it would swing away under the pointer; a square one keeps its corner.
          if (b.rotation) { b.x = cx - b.w / 2; b.y = cy - b.h / 2; }
        }
      }
      frame(() => { renderBackdrop(); renderObjects(); renderAps({}, {}); renderClients(new Map()); });
    });

    const end = (e: PointerEvent): void => {
      if (drag && drag.kind === 'pan' && mode === 'view' && moved < 3) {
        // A click, not a pan: a client or an access point opens its card, and
        // anywhere else closes it.
        const c = downOn?.closest?.('.wm-client');
        const a = downOn?.closest?.('.wm-ap');
        card = c ? { kind: 'client', id: (c.getAttribute('data-mac') || '').toUpperCase() }
          : a ? { kind: 'ap', id: a.getAttribute('data-ap') || '' } : null;
      }
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
      mode = 'edit'; card = null; syncChrome(); render();
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

    ([['wmAddArea', 'area'], ['wmAddShape', 'building']] as const).forEach(([id, kind]) => {
      el(id)?.addEventListener('click', () => {
        pending = null;
        disarm();
        drawKind = kind;
        drawing = [];
        svg!.classList.add('wm-placing');
        renderStats();
      });
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
      if (siteMap()) {
        note('The scale comes from the map. Remove the map to measure one.');
        return;
      }
      pending = null;
      disarm();
      measuring = true;
      svg!.classList.add('wm-placing');
      renderStats();
    });

    el<HTMLSelectElement>('wmFloor')?.addEventListener('change', (e) => {
      floor = parseInt((e.target as HTMLSelectElement).value, 10) || 0;
      if (sel?.kind === 'backdrop') sel = null;
      render();
    });

    el<HTMLInputElement>('wmSearch')?.addEventListener('input', (e) => {
      search = (e.target as HTMLInputElement).value.trim().toLowerCase();
      searchAt = -1;
      render();
    });
    el<HTMLInputElement>('wmSearch')?.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); findNext(); }
    });
    el('wmSatellite')?.addEventListener('click', () => {
      const map = siteMap();
      if (!map) return;
      mapLook = (mapLook || map.provider) === 'esri' ? 'osm' : 'esri';
      lsSet('mkd_wifi_map_look', mapLook);
      syncChrome(); renderBackdrop();
    });
    el('wmCoverage')?.addEventListener('click', () => {
      coverage = !coverage;
      lsSet('mkd_wifi_map_coverage', coverage);
      syncChrome(); render();
    });
    el('wmExport')?.addEventListener('click', () => {
      exportPng().catch(() => note('The picture could not be made.'));
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
  onFeatures(() => { if (isVisible('wifi-map')) render(); });
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
    card = null;
    images.clear();
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
