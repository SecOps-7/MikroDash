'use strict';
/** The parts of testdata/pages-table.json the frontend needs, as TypeScript. */
const fs = require('node:fs');
const path = require('node:path');
const ROOT = path.join(__dirname, '..');
const d = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'pages-table.json'), 'utf8'));
const OUT = path.join(ROOT, 'web', 'src', 'gen', 'page-keys.ts');
const body = `// GENERATED from testdata/pages-table.json — do not edit.
// Rebuild with \`node tools/pages-table-ts.js\` from the committed JSON, which is frozen:
// the generator that produced it read the Node app and was deleted on 2026-09-01.

/**
 * Digit -> page: pressing 3 opens PAGE_KEYS[2].
 *
 * ORDER IS THE ENTIRE MEANING, which is why it is generated rather than typed —
 * one transposition sends two shortcuts to each other's pages and reads as
 * completely normal.
 *
 * Only the first ${d.reachableShortcuts} are reachable: the handler parses a SINGLE keypress,
 * and no keypress produces "10". The rest are kept because the list is the live
 * app's, and a tenth becoming reachable would be a change there, not here.
 */
export const PAGE_KEYS: readonly string[] = ${JSON.stringify(d.pageKeys, null, 2)};

/**
 * Every page the visibility sweep considers, in the order it considers them.
 *
 * THE ORDER IS THE FALLBACK. When the page someone is standing on is taken away
 * from them, they are sent to the first page still visible — so reordering this
 * list silently changes where a demoted user lands. Generated for that reason,
 * and pinned to the nav items that carry the same keys: an entry with no nav
 * item is a page the sweep believes it hid and did not.
 */
export const ALL_NAV_PAGES: readonly string[] = ${JSON.stringify(d.allNavPages, null, 2)};
`;
if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('web/src/gen/page-keys.ts is stale — run: node tools/pages-table-ts.js');
    process.exit(1);
  }
  console.log('page keys .ts up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(ROOT, OUT));
}
