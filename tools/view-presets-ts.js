'use strict';
/** testdata/view-presets.json -> the TypeScript the renderer imports. */
const fs = require('node:fs');
const path = require('node:path');
const ROOT = path.join(__dirname, '..');
const d = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'view-presets.json'), 'utf8'));
const OUT = path.join(ROOT, 'web', 'src', 'gen', 'view-presets.ts');
const body = `// GENERATED from testdata/view-presets.json — do not edit.
// Regenerate with \`node tools/view-presets.js\` then \`node tools/view-presets-ts.js\`.

/** settings key -> page key. \`pageWifi\` is the checkbox; \`wifi\` is what a preset names. */
export const PAGE_NAV_MAP: Record<string, string> = ${JSON.stringify(d.navMap, null, 2)};

/**
 * The two EXPLICIT presets. \`advanced\` is absent on purpose — it is derived from
 * PAGE_NAV_MAP at use, exactly as the original derives it, "so a page added to
 * the nav joins Advanced by existing". A frozen list here would drop the next
 * page added from the preset, silently.
 */
export const VIEW_PRESETS: Record<string, string[]> = ${JSON.stringify(d.presets, null, 2)};

/** Where the chosen preset is remembered. */
export const VIEW_PRESET_KEY = ${JSON.stringify(d.storageKey)};
`;
if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('web/src/gen/view-presets.ts is stale — run: node tools/view-presets-ts.js');
    process.exit(1);
  }
  console.log('view presets .ts up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(ROOT, OUT));
}
