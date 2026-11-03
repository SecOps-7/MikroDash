/**
 * THE MAP BACKDROP LANDS WHERE THE ANCHOR SAYS, AT THE SCALE THE SERVER SETS.
 *
 * One canvas unit is one pixel of a zoom-19 tile, so the anchor's coordinate is
 * drawn at its canvas point at any zoom, a tile at a coarser zoom covers twice
 * the units per level, and the metres per unit match sitedoc.TileMetresPerUnit.
 * A coordinate pasted from a map app is read; anything else goes to the address
 * lookup.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.wifi-map-backdrop-entry.ts');
fs.writeFileSync(ENTRY,
  "export { worldPx, tileMetresPerUnit, tilesFor, parseLatLon, TILE_ZOOM, tileAt, esriTileMapUrl } " +
  "from '../web/src/pages/wifi-map.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wifi-map-backdrop.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
interface Tile { href: string; x: number; y: number; size: number }
const { worldPx, tileMetresPerUnit, tilesFor, parseLatLon, TILE_ZOOM, tileAt, esriTileMapUrl } = require(OUT) as {
  tileAt(lat: number, lon: number, z: number): { x: number; y: number };
  esriTileMapUrl(lat: number, lon: number, z: number): string;
  worldPx(lat: number, lon: number): { x: number; y: number };
  tileMetresPerUnit(lat: number): number;
  tilesFor(b: object, view: { x: number; y: number; k: number }, w: number, h: number): Tile[];
  parseLatLon(s: string): { lat: number; lon: number } | null;
  TILE_ZOOM: number;
};
fs.rmSync(OUT, { force: true });

const near = (a: number, b: number, eps: number, what: string): void =>
  assert.ok(Math.abs(a - b) <= eps, what + ': ' + a + ' is not within ' + eps + ' of ' + b);

// ── the projection ──────────────────────────────────────────────────────────
assert.strictEqual(TILE_ZOOM, 19, 'the zoom must match sitedoc.TileZoom');
const world = 256 * 2 ** 19;
near(worldPx(0, 0).x, world / 2, 1e-6, 'the null island is the middle of the world');
near(worldPx(0, 0).y, world / 2, 1e-6, 'the null island is the middle of the world');
near(worldPx(0, 180).x, world, 1e-6, 'the antimeridian is the right edge');
near(tileMetresPerUnit(0), 0.29858, 1e-4, 'a unit at the equator');
near(tileMetresPerUnit(55), 0.17126, 1e-4, 'a unit at 55°N, as sitedoc computes it');
console.log('  ok  one canvas unit is a zoom-19 pixel, and its metres follow the latitude');

// ── the tiles on screen ─────────────────────────────────────────────────────
const b = { floor: 0, kind: 'tiles', provider: 'osm', url: '', lat: 55.7539, lon: 37.6208, x: 100, y: 50,
  image: '', w: 0, h: 0, opacity: 1, rotation: 0, maxZoom: 0 };
const a = worldPx(b.lat, b.lon);

const at1 = tilesFor(b, { x: 0, y: 0, k: 1 }, 800, 600);
assert.ok(at1.length > 0 && at1.length <= 30, at1.length + ' tiles for an 800 × 600 view');
assert.ok(at1.every((t) => t.size === 256 && t.href.startsWith('https://tile.openstreetmap.org/19/')));
// The tile that holds the anchor puts the anchor's pixel on the anchor's canvas point.
const tx = Math.floor(a.x / 256), ty = Math.floor(a.y / 256);
const home = at1.find((t) => t.href.endsWith('/19/' + tx + '/' + ty + '.png'));
assert.ok(home, 'the tile under the anchor is on screen');
near(home!.x + (a.x - tx * 256), b.x, 1e-6, 'the anchor lands on its canvas x');
near(home!.y + (a.y - ty * 256), b.y, 1e-6, 'the anchor lands on its canvas y');
// Every tile on screen overlaps the screen.
for (const t of at1) {
  assert.ok(t.x + t.size > 0 && t.x < 800 && t.y + t.size > 0 && t.y < 600, 'off-screen tile ' + t.href);
}

const out = tilesFor(b, { x: 0, y: 0, k: 0.25 }, 800, 600);
assert.ok(out.every((t) => t.size === 1024 && t.href.includes('/17/')), 'a quarter zoom reads level 17 tiles');
const deep = tilesFor(b, { x: 0, y: 0, k: 3 }, 800, 600);
assert.ok(deep.every((t) => t.href.includes('/19/')), 'past the provider\'s deepest level its tiles are stretched');
const esri = tilesFor({ ...b, provider: 'esri' }, { x: 0, y: 0, k: 1 }, 400, 300);
assert.ok(esri.length && esri.every((t) => /\/tile\/17\/\d+\/\d+$/.test(t.href) && t.size === 1024),
  'Esri takes y before x, and stops at 17 unless told otherwise');
const esri19 = tilesFor({ ...b, provider: 'esri', maxZoom: 19 }, { x: 0, y: 0, k: 1 }, 400, 300);
assert.ok(esri19.length && esri19.every((t) => t.href.includes('/tile/19/')), 'a deepest level set on the backdrop wins');
const capped = tilesFor({ ...b, maxZoom: 16 }, { x: 0, y: 0, k: 2 }, 400, 300);
assert.ok(capped.length && capped.every((t) => t.href.includes('/16/') && t.size === 2048),
  'past the deepest level the tiles are stretched, never fetched deeper');
const custom = tilesFor({ ...b, provider: 'custom', url: 'https://t.example/{z}/{x}/{y}.jpg?k={z}' },
  { x: 0, y: 0, k: 1 }, 400, 300);
assert.ok(custom.length && custom.every((t) => /^https:\/\/t\.example\/19\/\d+\/\d+\.jpg\?k=19$/.test(t.href)),
  'a custom template has every placeholder filled');
assert.strictEqual(tilesFor({ ...b, provider: 'custom', url: '' }, { x: 0, y: 0, k: 1 }, 400, 300).length, 0);
assert.strictEqual(tilesFor(b, { x: 0, y: 0, k: 1 }, 100000, 100000).length, 0, 'too many tiles draws none');
// A turned map: every corner of the screen, turned back about the anchor, is
// inside a tile that is drawn.
for (const deg of [30, 90, -135]) {
  const turned = tilesFor({ ...b, rotation: deg }, { x: 0, y: 0, k: 1 }, 800, 300);
  const t = -deg * Math.PI / 180;
  for (const [sx, sy] of [[0, 0], [800, 0], [0, 300], [800, 300]]) {
    const cx = sx - b.x, cy = sy - b.y;
    const mx = b.x + cx * Math.cos(t) - cy * Math.sin(t), my = b.y + cx * Math.sin(t) + cy * Math.cos(t);
    assert.ok(turned.some((q) => mx >= q.x && mx <= q.x + q.size && my >= q.y && my <= q.y + q.size),
      'at ' + deg + '° the screen corner ' + sx + ',' + sy + ' has no tile under it');
  }
}
console.log('  ok  the tiles on screen are the ones under it, at the level the zoom wants, turned or not');

// ── asking Esri how deep its imagery goes ───────────────────────────────────
// Red Square's tile at 19, as Esri's tile map service was asked for it by hand:
// row 163898, column 316933.
assert.deepStrictEqual(tileAt(55.7539, 37.6208, 19), { x: 316933, y: 163898 });
assert.deepStrictEqual(tileAt(55.7539, 37.6208, 17), { x: 79233, y: 40974 });
assert.strictEqual(esriTileMapUrl(55.7539, 37.6208, 18),
  'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tilemap/18/81949/158466/1/1',
  'the tile map takes the level, then the row, then the column');
console.log('  ok  the place is asked about in the tile it is in, row before column');

// ── a coordinate typed or pasted ────────────────────────────────────────────
assert.deepStrictEqual(parseLatLon('55.7539, 37.6208'), { lat: 55.7539, lon: 37.6208 });
assert.deepStrictEqual(parseLatLon(' -33.8568 151.2153 '), { lat: -33.8568, lon: 151.2153 });
assert.deepStrictEqual(parseLatLon('59.93;30.31'), { lat: 59.93, lon: 30.31 });
for (const no of ['Red Square, Moscow', '95, 37', '55.7, 190', '55.7', '']) {
  assert.strictEqual(parseLatLon(no), null, JSON.stringify(no) + ' is not a coordinate');
}
console.log('  ok  a coordinate as a map app copies it is read, and anything else is an address');
