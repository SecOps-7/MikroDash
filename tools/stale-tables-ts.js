'use strict';
/** testdata/stale-tables.json -> the TypeScript the shell imports. */
const fs = require('node:fs');
const path = require('node:path');
const ROOT = path.join(__dirname, '..');
const d = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'stale-tables.json'), 'utf8'));
const OUT = path.join(ROOT, 'web', 'src', 'gen', 'stale-tables.ts');
const body = `// GENERATED from testdata/stale-tables.json — do not edit.
// Regenerate with \`node tools/stale-tables.js\` then \`node tools/stale-tables-ts.js\`.

/** Grace added on top of a collector's reported poll interval. */
export const STALE_GRACE = ${d.staleGrace};

export interface StaleCard { cardId: string; event: string; threshold: number }

/**
 * Which event proves each card is alive, and how long it may go without one.
 *
 * THE THRESHOLDS ARE STARTING POINTS. A payload carrying \`pollMs\` rewrites its
 * own card's threshold at runtime, so a collector that reports a slower interval
 * stops being called stale for keeping to it.
 */
export const STALE_CARDS: readonly StaleCard[] = ${JSON.stringify(d.cards, null, 2)};

/** Collector key -> the cards it feeds. */
export const COLLECTOR_CARDS: Record<string, string[]> = ${JSON.stringify(d.collectorCards, null, 2)};

/**
 * Card -> the tbody holding its rows.
 *
 * Nothing to do with staleness: this is what gets emptied on a router switch.
 * Upstream this list exists because switching used to clear each card's
 * in-memory guard and never the rendered rows, so a card kept showing the
 * PREVIOUS router's data until the new one produced a payload — indefinitely if
 * that collector is disabled or slow.
 */
export const DASH_CARD_TABLES: Record<string, string> = ${JSON.stringify(d.dashCardTables, null, 2)};
`;
if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('web/src/gen/stale-tables.ts is stale — run: node tools/stale-tables-ts.js');
    process.exit(1);
  }
  console.log('stale tables .ts up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(ROOT, OUT));
}
