'use strict';
/**
 * The alert-filter toggles: the client's TYPE_MAP and its two default objects,
 * lifted from the live `public/app.js`.
 *
 * ── THE CROSS-CHECK IS THE POINT, AND IT REPLACES A COMMENT ─────────────────
 *
 * The live defaults carry this warning above them:
 *
 *   "These must match src/settings.js DEFAULTS. They govern the window between
 *    script parse and the first settings:pages broadcast, so drift means the
 *    bell can fire for categories the server has switched off. netwatch,
 *    bridge, vlan and other were all true here against false on the server."
 *
 * That last sentence is the tell: the drift had ALREADY HAPPENED once, in four
 * places at once, and was fixed by hand. A comment cannot stop it happening
 * again — nothing fails when the two lists disagree, and the symptom is a
 * browser notification for a category the operator switched off, in the seconds
 * before the first broadcast lands.
 *
 * So this generator asserts it. Every entry in TYPE_MAP names a settings key,
 * and every one of those keys has a default in
 * `internal/store/settings_tables.json` — itself generated from `src/settings.js`
 * by `tools/settings-tables.js`. A mismatch fails here rather than in a browser.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/alert-filters-tables.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'public', 'app.js'), 'utf8');

function slice(decl, close, name) {
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}

const ctx = {};
vm.createContext(ctx);
vm.runInContext(
  slice('var NOTIF_TYPES_KEY       =', ';', 'NOTIF_TYPES_KEY') + '\n' +
  slice('var NOTIF_IFACE_TYPES_KEY =', ';', 'NOTIF_IFACE_TYPES_KEY') + '\n' +
  slice('var _alertTypes      = {', '};', '_alertTypes') + '\n' +
  slice('var _alertIfaceTypes = {', '};', '_alertIfaceTypes') + '\n' +
  slice('  var TYPE_MAP = [', '\n  ];', 'TYPE_MAP')
    // TYPE_MAP's `obj` column is a REFERENCE to one of the two objects above.
    // Recorded as a NAME instead: a reference cannot be serialised, and the name
    // is what the port needs in order to route a toggle to the right object.
    .replace(/obj:\s*_alertTypes/g, "obj: 'types'")
    .replace(/obj:\s*_alertIfaceTypes/g, "obj: 'iface'") + '\n' +
  'this.OUT = { typesKey: NOTIF_TYPES_KEY, ifaceKey: NOTIF_IFACE_TYPES_KEY, ' +
  'types: _alertTypes, iface: _alertIfaceTypes, map: TYPE_MAP };',
  ctx);
const { typesKey, ifaceKey, types, iface, map } = ctx.OUT;

if (!Array.isArray(map) || map.length < 10) {
  throw new Error('TYPE_MAP lifted as ' + (map && map.length) + ' rows — the slice is wrong');
}

// ── Every toggle must resolve, both ways ────────────────────────────────────

const problems = [];
const client = { types, iface };
for (const m of map) {
  if (!client[m.obj]) { problems.push(m.id + ' names an unknown object "' + m.obj + '"'); continue; }
  if (!(m.field in client[m.obj])) {
    problems.push(m.id + ' toggles ' + m.obj + '.' + m.field + ', which has no default — ' +
                  'it would read as undefined until the first save');
  }
}
// And the other direction: a default nothing can toggle is a setting with no UI.
for (const [objName, obj] of Object.entries(client)) {
  for (const field of Object.keys(obj)) {
    if (!map.some((m) => m.obj === objName && m.field === field)) {
      problems.push(objName + '.' + field + ' has a default but no toggle in TYPE_MAP');
    }
  }
}

// ── THE CROSS-CHECK the live comment asks for ───────────────────────────────

const serverDefaults = JSON.parse(
  fs.readFileSync(path.join(ROOT, 'internal', 'store', 'settings_tables.json'), 'utf8')).defaults;

const drift = [];
for (const m of map) {
  if (!(m.key in serverDefaults)) {
    problems.push(m.id + ' saves `' + m.key + '`, which src/settings.js has no default for');
    continue;
  }
  const clientVal = !!client[m.obj][m.field];
  const serverVal = !!serverDefaults[m.key];
  if (clientVal !== serverVal) {
    drift.push(m.key + ': the browser starts ' + clientVal + ', the server default is ' + serverVal);
  }
}
if (drift.length) {
  problems.push('THE CLIENT AND SERVER DEFAULTS DISAGREE — the bell can fire for a category the ' +
    'server has switched off, in the window before the first settings broadcast:\n    ' +
    drift.join('\n    '));
}
if (problems.length) {
  console.log('alert-filters-tables FAILED:');
  for (const p of problems) console.log('  - ' + p);
  process.exit(1);
}

const out = {
  _generated: 'tools/alert-filters-tables.js — do not edit',
  typesKey, ifaceKey, types, iface, map,
  // Recorded so the agreement is visible in the diff rather than only asserted.
  serverDefaults: Object.fromEntries(map.map((m) => [m.key, !!serverDefaults[m.key]])),
};

const ts =
  '// GENERATED by tools/alert-filters-tables.js — do not edit.\n' +
  '//\n' +
  '// The alert-filter toggles and their defaults, lifted from the live\n' +
  '// public/app.js. The generator ASSERTS that every default here matches the\n' +
  '// one src/settings.js uses — the live source only says so in a comment, and\n' +
  '// that comment records four toggles that had already drifted.\n\n' +
  'export interface AlertToggle {\n' +
  "  id: string;\n  obj: 'types' | 'iface';\n  field: string;\n  key: string;\n}\n\n" +
  'export const ALERT_TOGGLES: AlertToggle[] = ' + JSON.stringify(map, null, 2) + ';\n\n' +
  'export const ALERT_TYPE_DEFAULTS: Record<string, boolean> = ' + JSON.stringify(types, null, 2) + ';\n\n' +
  'export const ALERT_IFACE_DEFAULTS: Record<string, boolean> = ' + JSON.stringify(iface, null, 2) + ';\n\n' +
  'export const NOTIF_TYPES_KEY = ' + JSON.stringify(typesKey) + ';\n' +
  'export const NOTIF_IFACE_TYPES_KEY = ' + JSON.stringify(ifaceKey) + ';\n';

const TS_OUT = path.join(ROOT, 'web', 'src', 'gen', 'alert-filters.ts');
const JSON_OUT = path.join(ROOT, 'testdata', 'alert-filters.json');

if (process.argv.includes('--check')) {
  let stale = false;
  for (const [f, want] of [[TS_OUT, ts], [JSON_OUT, JSON.stringify(out, null, 2) + '\n']]) {
    const have = fs.existsSync(f) ? fs.readFileSync(f, 'utf8') : '';
    if (have !== want) { console.log('STALE: ' + path.relative(ROOT, f)); stale = true; }
  }
  if (stale) process.exit(1);
  console.log('alert filters current (' + map.length + ' toggles, all matching the server defaults)');
} else {
  fs.writeFileSync(TS_OUT, ts);
  fs.writeFileSync(JSON_OUT, JSON.stringify(out, null, 2) + '\n');
  console.log('wrote ' + path.relative(ROOT, TS_OUT) + ' and ' + path.relative(ROOT, JSON_OUT) +
              ' (' + map.length + ' toggles, all matching the server defaults)');
}
