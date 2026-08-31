'use strict';
/**
 * The Dashboard grid's tables and constants, lifted from public/js/dashboard-grid.js.
 *
 *   DEFAULT_LAYOUT  23 cards, each with a 1-based cell rect and a visible flag.
 *                   Nine are visible by default; the rest are the "extra" cards
 *                   the Add panel offers.
 *   CARD_LABELS     card id -> the name the Add panel shows.
 *   CARD_ROOMS      card id -> the backend room its collector needs. Only some
 *                   cards have one, and TWO CARDS CAN SHARE A ROOM, which is why
 *                   this is not a set: leaving a room because one card was
 *                   removed would silently stop the other one's data.
 *   COLS/ROWS/GAP/PAD/MIN_W/MIN_H  the grid geometry.
 *
 * ── GENERATED BECAUSE IT IS 23 RECTS ────────────────────────────────────────
 *
 * Ninety-two numbers that all look alike. Transcribing them by hand is how a
 * card ends up one column wide on first load for somebody who has never opened
 * the dashboard before, and no gate would call that wrong — it would simply be
 * the layout. So they are EXTRACTED, and `--check` fails when they drift.
 *
 * ── THE CHECKS ──────────────────────────────────────────────────────────────
 *
 * Every layout id must have a label and vice versa; every room's card must
 * exist in the layout; every default rect must fit inside the grid; and no two
 * VISIBLE defaults may overlap — a shipped default layout that collided would
 * put two cards on top of each other on first load.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/grid-tables.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'public', 'js', 'dashboard-grid.js'), 'utf8');
const OUT = path.join(ROOT, 'web', 'src', 'gen', 'grid-tables.ts');

function grab(decl, close, name) {
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}

const ctx = {};
vm.createContext(ctx);
vm.runInContext([
  grab('var COLS = 24', ';', 'the grid constants'),
  grab('var MIN_W', ';', 'the minimum card size'),
  grab('var CARD_LABELS = {', '};', 'CARD_LABELS'),
  grab('var CARD_ROOMS = {', '};', 'CARD_ROOMS'),
  grab('var DEFAULT_LAYOUT = [', '];', 'DEFAULT_LAYOUT'),
  grab("var LS_KEY = '", ";", 'LS_KEY'),
].join('\n'), ctx);

const { COLS, ROWS, GAP, PAD, MIN_W, MIN_H, CARD_LABELS, CARD_ROOMS, DEFAULT_LAYOUT, LS_KEY } = ctx;

// ── the checks ─────────────────────────────────────────────────────────────
for (const [name, v] of Object.entries({ COLS, ROWS, GAP, PAD, MIN_W, MIN_H })) {
  assert.equal(typeof v, 'number', name + ' did not extract as a number');
}
assert.ok(DEFAULT_LAYOUT.length > 20, 'only ' + DEFAULT_LAYOUT.length + ' cards extracted');
assert.match(LS_KEY, /^mikrodash_dashboard_layout_v\d+$/, 'LS_KEY has an unexpected shape: ' + LS_KEY);

const ids = new Set(DEFAULT_LAYOUT.map((c) => c.id));
const missingLabel = [...ids].filter((id) => !CARD_LABELS[id]);
const missingLayout = Object.keys(CARD_LABELS).filter((id) => !ids.has(id));
assert.deepEqual(missingLabel, [], 'layout entries with no label: ' + missingLabel);
assert.deepEqual(missingLayout, [], 'labels with no layout entry: ' + missingLayout);
const roomOrphans = Object.keys(CARD_ROOMS).filter((id) => !ids.has(id));
assert.deepEqual(roomOrphans, [], 'CARD_ROOMS names cards with no layout entry: ' + roomOrphans);

for (const c of DEFAULT_LAYOUT) {
  assert.ok(c.x >= 1 && c.y >= 1 && c.x + c.w - 1 <= COLS && c.y + c.h - 1 <= ROWS,
    c.id + ' does not fit the grid: ' + JSON.stringify(c));
  assert.ok(c.w >= MIN_W && c.h >= MIN_H, c.id + ' is smaller than the minimum card size');
}
// No two VISIBLE defaults may overlap. The hidden ones all sit at 1,1 and are
// expected to.
const vis = DEFAULT_LAYOUT.filter((c) => c.visible);
for (let i = 0; i < vis.length; i++) {
  for (let j = i + 1; j < vis.length; j++) {
    const a = vis[i], b = vis[j];
    const over = a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
    assert.ok(!over, 'the DEFAULT layout overlaps: ' + a.id + ' and ' + b.id);
  }
}

// ── the Go side's card->page map must match the LIVE resolution ────────────
//
// `internal/server/dashcard.go` gates a card room on the DASHBOARD and on the
// page the card borrows its data from. That second page is resolved here — by
// the live registry, which is the authority — and compared against the Go table.
// A room added over there with a different source page would otherwise be gated
// on `dashboard` alone in the port, which is a permission check quietly getting
// weaker.
{
  const Pages = require(path.join(LIVE, 'src', 'pages.js'));
  const resolve = (key) =>
    (Pages.BY_KEY[key] ? key : (Pages.pageForCollector(key) || 'dashboard'));
  const goSrc = fs.readFileSync(path.join(ROOT, 'internal', 'server', 'dashcard.go'), 'utf8');
  const block = goSrc.slice(goSrc.indexOf('var dashCardPages'), goSrc.indexOf('// dashCardPage resolves'));
  const goMap = {};
  for (const m of block.matchAll(/"([a-z]+)":\s*"([a-z]+)"/g)) goMap[m[1]] = m[2];
  assert.ok(Object.keys(goMap).length >= 8,
    'only ' + Object.keys(goMap).length + ' entries parsed from dashcard.go — the scan broke');

  const rooms = [...new Set(Object.values(CARD_ROOMS))].sort();
  for (const room of rooms) {
    assert.equal(goMap[room], resolve(room),
      'dashcard.go gates the "' + room + '" card on "' + goMap[room] + '", but the live app ' +
      'resolves it to "' + resolve(room) + '"');
  }
  for (const key of Object.keys(goMap)) {
    assert.ok(rooms.includes(key),
      'dashcard.go names a card room "' + key + '" that no card asks for');
  }
}

// ── emit ───────────────────────────────────────────────────────────────────
const j = (v) => JSON.stringify(v, null, 2).replace(/\n/g, '\n');
const body = `// GENERATED by tools/grid-tables.js — do not edit.
//
// The Dashboard grid's tables, lifted from public/js/dashboard-grid.js. Ninety-two
// numbers that all look alike; see that tool's header for why they are extracted
// rather than typed, and for the invariants it checks before emitting them.
//
//   MIKRODASH_SRC=../MikroDash node tools/grid-tables.js

export interface GridCard {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  visible: boolean;
}

export const COLS = ${COLS};
export const ROWS = ${ROWS};
export const GAP = ${GAP};
export const PAD = ${PAD};
export const MIN_W = ${MIN_W};
export const MIN_H = ${MIN_H};

/** Bumped only on a breaking change to the stored card-object format. */
export const LS_KEY = ${JSON.stringify(LS_KEY)};

export const DEFAULT_LAYOUT: readonly GridCard[] = ${j(DEFAULT_LAYOUT)};

export const CARD_LABELS: Readonly<Record<string, string>> = ${j(CARD_LABELS)};

/**
 * Cards that need a backend room. TWO CARDS CAN SHARE ONE — fwaction and logs
 * do not, but the live table's own comment records that they may — so leaving a
 * room must be decided by whether ANY visible card still wants it.
 */
export const CARD_ROOMS: Readonly<Record<string, string>> = ${j(CARD_ROOMS)};
`;

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== body) {
    console.error('web/src/gen/grid-tables.ts is stale — run: node tools/grid-tables.js');
    process.exit(1);
  }
  console.log('grid-tables: up to date (%d cards, %d labels, %d rooms)',
    DEFAULT_LAYOUT.length, Object.keys(CARD_LABELS).length, Object.keys(CARD_ROOMS).length);
} else {
  fs.writeFileSync(OUT, body);
  console.log('grid-tables: wrote %d cards, %d labels, %d rooms', DEFAULT_LAYOUT.length,
    Object.keys(CARD_LABELS).length, Object.keys(CARD_ROOMS).length);
}
