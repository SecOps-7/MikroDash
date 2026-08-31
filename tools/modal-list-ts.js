'use strict';
/** testdata/modal-list.json -> the TypeScript the shell imports. */
const fs = require('node:fs');
const path = require('node:path');
const ROOT = path.join(__dirname, '..');
const d = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'modal-list.json'), 'utf8'));
const OUT = path.join(ROOT, 'web', 'src', 'gen', 'modals.ts');
const body = `// GENERATED from testdata/modal-list.json — do not edit.
// Regenerate with \`node tools/modal-list.js\` then \`node tools/modal-list-ts.js\`.

/**
 * Every dialog that closes on Escape and on a backdrop click.
 *
 * The live name for this is \`_PRINCIPAL_MODALS\`, which is a leftover: it began
 * as the Settings principals dialogs and has not been that for a long time. An
 * earlier version of this port read the name, believed it, and skipped the
 * behaviour on the grounds that none of the list was ported. Four of the ten
 * are. Generated so the port cannot hold an opinion about the contents.
 */
export const CLOSABLE_MODALS: readonly string[] = ${JSON.stringify(d.all, null, 2)};
`;
if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('web/src/gen/modals.ts is stale — run: node tools/modal-list-ts.js');
    process.exit(1);
  }
  console.log('modal list .ts up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(ROOT, OUT));
}
