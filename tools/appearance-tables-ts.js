'use strict';
/** testdata/appearance-tables.json -> the TypeScript the appearance layer imports. */
const fs = require('node:fs');
const path = require('node:path');
const ROOT = path.join(__dirname, '..');
const d = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'appearance-tables.json'), 'utf8'));
const OUT = path.join(ROOT, 'web', 'src', 'gen', 'appearance-tables.ts');
const body = `// GENERATED from testdata/appearance-tables.json — do not edit.
// Regenerate with \`node tools/appearance-tables.js\` then \`node tools/appearance-tables-ts.js\`.

/** r, g, b, a — the alpha is carried through brightness scaling unchanged. */
export type RGBA = [number, number, number, number];

export interface PaletteColors { main: RGBA; muted: RGBA; bgDeep: RGBA; bgCard: RGBA }

/** The neutral midpoint of all three sliders. At this level the layer REMOVES
 *  its custom properties rather than computing the base colour, so the
 *  stylesheet's own value is what applies. */
export const APPEAR_DEFAULT = ${d.appearDefault};

/** Where each preference is remembered. Appearance is per-browser, not per-user:
 *  the server never sees any of it. */
export const KEYS = ${JSON.stringify(d.keys, null, 2)} as const;

/** Order is load-bearing: the \`<select>\` renders the labels in this order and
 *  \`tools/appearance-tables.js\` pins the two against each other. */
export const FONTS: { id: string; family: string }[] = ${JSON.stringify(d.fonts, null, 2)};

/** \`px: null\` is the browser default — the layer REMOVES font-size rather than
 *  setting a number, which is not the same thing as 16px. */
export const FONT_SIZES: { id: string; px: number | null }[] = ${JSON.stringify(d.fontSizes, null, 2)};

/** Indexed by \`level - 1\`, clamped at both ends. */
export const CONTRAST_FACTORS: number[] = ${JSON.stringify(d.contrastFactors)};
export const TEXT_BRIGHT_FACTORS: number[] = ${JSON.stringify(d.textBrightFactors)};
export const BG_BRIGHT_FACTORS: number[] = ${JSON.stringify(d.bgBrightFactors)};

/** Keyed \`palette:scheme\`. A miss falls back to \`default:dark\` SILENTLY, which
 *  is why the generator checks every key against the swatch that offers it. */
export const PALETTE_COLORS: Record<string, PaletteColors> = ${JSON.stringify(d.paletteColors, null, 2)};
`;
if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('web/src/gen/appearance-tables.ts is stale — run: node tools/appearance-tables-ts.js');
    process.exit(1);
  }
  console.log('appearance tables .ts up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(ROOT, OUT));
}
