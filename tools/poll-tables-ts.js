'use strict';
/** testdata/poll-tables.json -> the poll sliders and profiles the Settings page draws. */
const fs = require('node:fs');
const path = require('node:path');
const ROOT = path.join(__dirname, '..');
const SRC = path.join(ROOT, 'testdata', 'poll-tables.json');
const OUT = path.join(ROOT, 'web', 'src', 'gen', 'poll-tables.ts');

function render(d) {
  return `// GENERATED from testdata/poll-tables.json — do not edit.
// Rebuild with \`node tools/poll-tables-ts.js\` from the committed JSON, which is frozen:
// the generator that produced it lifted the tables from the Node app's public/app.js,
// and that source is gone.
//
// Every one of these tables fails SILENTLY when it drifts — a missing slider is
// simply not drawn, a missing profile key leaves that collector where it was — so
// the TypeScript is written from the JSON rather than retyped, and verify checks it.

export interface PollSlider {
  key: string;
  label: string;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  streamed?: boolean;
}

export const POLL_SLIDERS: PollSlider[] = ${JSON.stringify(d.sliders, null, 2)};

export const POLL_PROFILES: Record<string, Record<string, number>> = ${JSON.stringify(d.profiles, null, 2)};

/** The localStorage key the live app remembers the chosen profile under. */
export const POLL_PROFILE_KEY = ${JSON.stringify(d.profileKey)};
`;
}

module.exports = { render };

if (require.main === module) {
  const body = render(JSON.parse(fs.readFileSync(SRC, 'utf8')));
  if (process.argv.includes('--check')) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('web/src/gen/poll-tables.ts is stale — run: node tools/poll-tables-ts.js');
      process.exit(1);
    }
    console.log('poll tables .ts up to date');
  } else {
    fs.mkdirSync(path.dirname(OUT), { recursive: true });
    fs.writeFileSync(OUT, body);
    console.log('wrote ' + path.relative(ROOT, OUT));
  }
}
