'use strict';
/**
 * The stale-detection tables, lifted from the live public/app.js.
 *
 * Three of them, and they answer different questions:
 *
 *   staleConfig        cardId -> the event that proves it is alive, and how long
 *                      it may go without one. Thresholds are ADAPTIVE: a payload
 *                      carrying `pollMs` rewrites its own card's threshold, so
 *                      the numbers here are starting points, not constants.
 *   COLLECTOR_CARDS    collector key -> the cards it feeds, for marking a card
 *                      whose collector is switched off or asleep.
 *   DASH_CARD_TABLES   cardId -> the tbody holding its rows, emptied on a router
 *                      switch. This one is not about staleness at all; see the
 *                      port's stale.ts header.
 *
 * ── THE CHECKS ──────────────────────────────────────────────────────────────
 *
 * Every card id must appear in the extracted markup, and every tbody id too. A
 * card that is not there means the sweep is counting down for an element that
 * does not exist — harmless in the live app, which guards on the lookup, but a
 * sign the tables and the pages have drifted apart.
 *
 * Cards on pages this port has not extracted are listed in ABSENT with the page
 * they belong to. The list is checked in both directions: an entry that turns up
 * in the markup has to be removed.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/stale-tables.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'public', 'app.js'), 'utf8');
const OUT = path.join(ROOT, 'testdata', 'stale-tables.json');

function block(decl, close, name) {
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
const ctx = {};
vm.createContext(ctx);
vm.runInContext([
  block('var COLLECTOR_CARDS = {', '\n};', 'COLLECTOR_CARDS'),
  block('var _DASH_CARD_TABLES = {', '\n};', '_DASH_CARD_TABLES'),
  block('var staleConfig=[', '\n];', 'staleConfig'),
  src.match(/^var STALE_GRACE = \d+;.*$/m)[0],
].join('\n'), ctx);

const tables = {
  staleGrace: ctx.STALE_GRACE,
  cards: ctx.staleConfig.map((c) => ({ cardId: c.cardId, event: c.event, threshold: c.threshold })),
  collectorCards: ctx.COLLECTOR_CARDS,
  dashCardTables: ctx._DASH_CARD_TABLES,
};
if (tables.cards.length < 15) throw new Error('staleConfig parsed as ' + tables.cards.length + ' cards');

// ── The checks ──────────────────────────────────────────────────────────────
const ui = fs.readdirSync(path.join(ROOT, 'web', 'src', 'ui'))
  .map((f) => fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', f), 'utf8')).join('\n');
const has = (id) => ui.includes('id="' + id + '"');

/** Cards and tbodies whose PAGE this port has not extracted. */
/**
 * Cards and tbodies whose page this port has not extracted.
 *
 * EMPTY, and that was a surprise worth recording: I had assumed the Dashboard
 * cards would be missing and listed six of them here on that assumption. All
 * thirty ids turned out to be in the extracted markup — the shell and the
 * ported pages between them carry every one. So the whole block applies to this
 * port rather than the subset I expected, and the check refused my guesses
 * rather than letting them stand as documentation.
 *
 * Kept as the mechanism for future drift: an id that disappears from the markup
 * has to be named here, and one named here that reappears has to be removed.
 */
const ABSENT = {};
const bad = [];
const ids = new Set();
for (const c of tables.cards) ids.add(c.cardId);
for (const list of Object.values(tables.collectorCards)) for (const id of list) ids.add(id);
for (const [card, body] of Object.entries(tables.dashCardTables)) { ids.add(card); ids.add(body); }

for (const id of [...ids].sort()) {
  const present = has(id);
  if (!present && !ABSENT[id]) {
    bad.push('`' + id + '` is in the stale tables but in no extracted page — add it to ABSENT ' +
      'with the page it belongs to, or extract that page');
  } else if (present && ABSENT[id]) {
    bad.push('`' + id + '` is listed in ABSENT but IS in the extracted markup — remove the entry, ' +
      'the sweep can reach it now');
  }
}
// Every card COLLECTOR_CARDS names must be one the sweep knows about, or marking
// it off has no countdown to suppress and the mark is decoration.
const swept = new Set(tables.cards.map((c) => c.cardId));
for (const [key, list] of Object.entries(tables.collectorCards)) {
  for (const id of list) {
    if (!swept.has(id)) {
      bad.push('COLLECTOR_CARDS.' + key + ' names `' + id + '`, which staleConfig does not sweep — ' +
        'marking it disabled suppresses a countdown that was never running');
    }
  }
}
if (bad.length) {
  console.error('the stale tables and the extracted markup disagree:\n\n' + bad.join('\n') + '\n');
  process.exit(1);
}

const body = JSON.stringify({
  note: 'Generated by tools/stale-tables.js from the LIVE public/app.js. Do not edit.',
  ...tables,
  absent: Object.keys(ABSENT).sort(),
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('testdata/stale-tables.json is stale — run: node tools/stale-tables.js');
    process.exit(1);
  }
  console.log('stale tables up to date (' + tables.cards.length + ' cards, ' +
    Object.keys(tables.collectorCards).length + ' collectors, ' +
    Object.keys(tables.dashCardTables).length + ' row tables)');
} else {
  fs.writeFileSync(OUT, body);
  console.log('wrote testdata/stale-tables.json — ' + tables.cards.length + ' swept cards, ' +
    (ids.size - Object.keys(ABSENT).length) + ' of ' + ids.size + ' ids present here');
}
