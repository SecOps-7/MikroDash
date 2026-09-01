'use strict';
/**
 * Turn testdata/settings-form-map.json into the TypeScript the renderer imports.
 *
 * A second generator rather than one, because the JSON is the differential
 * artefact — it is what a future check compares the live populate() against —
 * and the .ts is only a convenience for the bundler. Keeping them separate means
 * the JSON stays the source of truth and this file can be regenerated from it
 * without re-reading the live tree.
 *
 *   node tools/settings-form-map-ts.js            write the .ts
 *   node tools/settings-form-map-ts.js --check    exit 1 if stale
 */
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const IN = path.join(ROOT, 'testdata', 'settings-form-map.json');
const OUT = path.join(ROOT, 'web', 'src', 'gen', 'settings-form-map.ts');

const d = JSON.parse(fs.readFileSync(IN, 'utf8'));
const body = `// GENERATED from testdata/settings-form-map.json — do not edit.
//
// Rebuild this file from the committed JSON, which is frozen (its generator
// read the Node app and was deleted on 2026-09-01): \`node tools/settings-form-map-ts.js\`.
// It exists so the renderer is driven
// by the SAME table the generator captured from the live populate(), rather than
// by a second copy that can drift.

export type FieldKind = 'value' | 'checkOn' | 'checkOff' | 'checkGuarded';

export interface ValueDefault {
  kind: string;
  /** Present for the \`orNumber\` shape: what an absent value renders as. */
  fallback?: number;
  /**
   * The assignment expression as the live populate() writes it, kept verbatim.
   * The renderer does NOT read this — \`tools/settings-populate-check.js\`
   * evaluates it, so the comparison is against the original text rather than a
   * retyped copy of it.
   */
  expr?: string;
}

/** Inputs filled from a settings key, by how an ABSENT value is treated. */
export const FORM_FIELDS: Record<FieldKind, readonly string[]> = ${JSON.stringify(d.fields, null, 2)};

/** Per-field rule for an absent value; see the generator's valueKind(). */
export const VALUE_DEFAULTS: Record<string, ValueDefault> = ${JSON.stringify(d.valueDefaults, null, 2)};

/**
 * The credential inputs that are never given a value.
 *
 * populate() blanks them and uses the PLACEHOLDER to say whether one is stored.
 * \`smtpUser\` is deliberately NOT here: it is set as an ordinary value, so it
 * receives the mask and hands it back on save — which is what the server's
 * isMasked guard exists to catch.
 */
export const PLACEHOLDER_CREDENTIALS: Record<string, { whenSet: string; whenNot: string }> = ${JSON.stringify(d.placeholderCredentials, null, 2)};
`;

if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('web/src/gen/settings-form-map.ts is stale — run: node tools/settings-form-map-ts.js');
    process.exit(1);
  }
  console.log('settings form map .ts up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(ROOT, OUT));
}
