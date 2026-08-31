'use strict';
/**
 * How the Settings form's inputs map to settings keys, captured from `populate`.
 *
 * ── WHAT DRIFTS HERE, AND WHY A TABLE ──────────────────────────────────────
 *
 * ~100 inputs, each named `s_<key>`, and three different defaults for a value
 * that is absent:
 *
 *   value      `data[f] !== undefined ? data[f] : ''`
 *   checkOff   `!!data[f]`            — absent means OFF
 *   checkOn    `data[f] !== false`    — absent means ON
 *
 * The last two are the reason this is generated rather than read. They look
 * identical on screen when the setting IS set, and differ only for a value the
 * operator has never touched — so a page toggle ported as `checkOff` would hide
 * every page on a fresh install, and nothing about the code would look wrong.
 *
 * A page added means a new `page*` toggle in that list; an alert type means a
 * new `notif*` one. A hand-copied table would silently stop filling them.
 *
 * ── THE PLACEHOLDER FIELDS ARE THE INTERESTING ONES ────────────────────────
 *
 * Four of the five credential inputs are NEVER given a value: `populate` blanks
 * them and uses the PLACEHOLDER to say whether one is stored ("leave blank to
 * keep current" against "not set" / "paste token here"). `smtpUser` is the
 * exception — it is set as an ordinary value, so it receives the MASK and hands
 * it straight back on save, which is what the server's `isMasked` guard is for.
 * That asymmetry is captured here rather than left to be rediscovered.
 *
 *   node tools/settings-form-map.js            write the map
 *   node tools/settings-form-map.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');

const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'settings-form-map.json');

const src = fs.readFileSync(path.join(LIVE, 'public', 'app.js'), 'utf8');
const start = src.indexOf('  function populate(data) {');
if (start === -1) throw new Error('cannot find populate() in public/app.js');
// To the next top-level function inside the IIFE, which is where populate ends.
const end = src.indexOf('\n  function ', start + 10);
const body = src.slice(start, end === -1 ? start + 20000 : end);

const fields = {};
const valueDefaults = {};
function add(key, kind) {
  if (!fields[key]) fields[key] = kind;
}

// `['a','b'].forEach(... el.value = ...)` and the same shape for the two
// checkbox defaults. The ASSIGNMENT decides the kind, so a list that changed
// which helper it uses is captured correctly rather than by its position.
for (const m of body.matchAll(/\[([^\]]*?)\]\s*\.forEach\(function\s*\(f\)\s*\{[^}]*?el\.(value|checked)\s*=\s*([^;]+);/gs)) {
  const [, list, prop, expr] = m;
  const kind = prop === 'value' ? 'value'
    : /!==\s*false/.test(expr) ? 'checkOn' : 'checkOff';
  for (const n of list.matchAll(/'([A-Za-z0-9_]+)'/g)) add(n[1], kind);
}
// The `var fields = [...]` list feeding the first forEach.
const listVar = body.match(/var fields = \[([\s\S]*?)\];/);
if (listVar) {
  for (const n of listVar[1].matchAll(/'([A-Za-z0-9_]+)'/g)) {
    add(n[1], 'value');
    valueDefaults[n[1]] = { kind: 'undefinedToEmpty', expr: "data[f] !== undefined ? data[f] : ''" };
  }
}

// ── THE DEFAULT FOR AN ABSENT VALUE IS NOT ONE RULE ───────────────────────
//
// `value` fields have at least five shapes in populate(), and they disagree
// exactly where it matters — on a setting the operator has never touched:
//
//   undefinedToEmpty  data[f] !== undefined ? data[f] : ''
//   orEmpty           data.X || ''
//   orNumber          data.smtpPort || 587      <- an absent port must be 587
//   bare              data.updateCheckHours     <- undefined reaches the input
//   stringOf          String(data.sessionTimeoutMs), guarded by != null
//
// Flattening these to one rule was the gap in the first version of this file.
// An absent smtpPort rendered as an empty box instead of 587, which reads as
// "no port configured" for a value the app is in fact using.
//
// An expression matching none of the five RAISES. A sixth shape must be added
// here and in the port deliberately, not absorbed by whichever branch happens
// to be last.
function valueKind(expr, key) {
  const e = expr.trim();
  if (/!==\s*undefined\s*\?/.test(e)) return { kind: 'undefinedToEmpty' };
  if (/\|\|\s*''/.test(e) || /\|\|\s*""/.test(e)) return { kind: 'orEmpty' };
  const num = e.match(/\|\|\s*(\d+)\s*$/);
  if (num) return { kind: 'orNumber', fallback: Number(num[1]) };
  if (/^String\(/.test(e)) return { kind: 'stringOf' };
  if (/^data\.[A-Za-z0-9_]+$/.test(e) || /^data\[[^\]]+\]$/.test(e)) return { kind: 'bare' };
  if (/^''$/.test(e) || /^""$/.test(e)) return { kind: 'blank' };
  throw new Error('populate() sets s_' + key + ' from an expression this generator ' +
    'does not recognise: `' + e + '`. Add the shape here AND in the port rather ' +
    'than letting it fall through to a default that is wrong for absent values.');
}

// One-off assignments: `var x = $('s_key'); if (x) x.checked = <expr>;`
for (const m of body.matchAll(/\$\('s_([A-Za-z0-9_]+)'\)[^;]*;[^;]*?\.(checked|value)\s*=\s*([^;]+);/g)) {
  const [, key, prop, expr] = m;
  if (prop === 'checked') {
    add(key, /!==\s*false/.test(expr) ? 'checkOn' : 'checkOff');
  } else {
    const v = valueKind(expr, key);
    add(key, 'value');
    // THE RAW EXPRESSION IS KEPT so a differential check can evaluate the
    // ORIGINAL rather than a retyped copy of it. Retyping is what turns a gate
    // into two implementations that agree with each other and not with the app.
    v.expr = expr.trim().replace(/;$/, '');
    valueDefaults[key] = v;
  }
}
// ── THE `ALERT_TYPE_MAP` SHAPE, WHICH IS GUARDED DIFFERENTLY ──────────────
//
// `ALERT_TYPE_MAP.forEach(function (m) { if (data[m.field] !== undefined) { …
// el.checked = !!data[m.field] } })` — a list of OBJECTS, and the assignment is
// wrapped in a presence test the other checkbox loops do not have.
//
// That guard is the whole reason this needs its own kind. `checkOff` writes
// `false` for an absent setting; these leave the checkbox ALONE, so it keeps the
// markup's own default. On a fresh install the difference is every alert type
// silently switched off in the UI while the server still has it enabled — which
// is precisely the failure the live comment on `_PAGE_SETTING_KEYS` describes
// having happened once already.
for (const m of body.matchAll(/(\w+)\.forEach\(function\s*\(m\)\s*\{[^}]*?data\[m\.field\]\s*!==\s*undefined[\s\S]*?el\.checked\s*=\s*!!data\[m\.field\]/g)) {
  const listName = m[1];
  const decl = body.indexOf('var ' + listName + ' = [');
  if (decl === -1) throw new Error('cannot find the ' + listName + ' literal');
  const listBody = body.slice(decl, body.indexOf('];', decl));
  const found = [...listBody.matchAll(/field:\s*'([A-Za-z0-9_]+)'/g)].map((x) => x[1]);
  if (found.length < 5) {
    throw new Error(listName + ' yielded only ' + found.length + ' fields — its shape changed');
  }
  for (const f of found) add(f, 'checkGuarded');
}

// Placeholder-only credentials: blanked, with the placeholder carrying presence.
const placeholders = {};
for (const m of body.matchAll(/\$\('s_([A-Za-z0-9_]+)'\);?\s*if\s*\([^)]*\)\s*\{[^}]*?\.value\s*=\s*'';[^}]*?\.placeholder\s*=\s*data\.\w+\s*\?\s*'([^']*)'\s*:\s*'([^']*)'/gs)) {
  const [, key, whenSet, whenNot] = m;
  placeholders[key] = { whenSet, whenNot };
  delete fields[key];
}

// ── EVERY ELEMENT populate() TOUCHES, SO AN OMISSION IS VISIBLE ───────────
//
// The field lists above capture the `s_<key>` assignments driven by populate()'s
// loops. They do NOT capture its one-off statements — which is how the two alert
// sliders' companion labels (`s_alertCpuThresholdVal`, `s_alertPingLossVal`) were
// missed: the port set the sliders and left both readouts blank, and the
// 518-case gate could not see it because it only inspects ids the map names.
//
// So the full set of ids populate() looks up is recorded too. The check then
// asserts the port writes to each one, or that it is listed there with a reason.
// A gap becomes a named failure instead of an empty box on screen.
// LITERAL LOOKUPS ONLY, and that is the useful set. The loops build their ids
// dynamically (`$('s_' + f)`), so they contribute nothing here — which means this
// list is precisely the statements the field-list extraction above does NOT
// cover. That is where the missing slider labels were hiding.
const touchedIds = [...new Set(
  [...body.matchAll(/\$\('([A-Za-z0-9_]+)'\)/g)].map((m) => m[1]),
)].sort();
if (touchedIds.length < 20) {
  throw new Error('populate() appears to touch only ' + touchedIds.length + ' elements by ' +
    'literal id — the lookup pattern changed and this coverage list would be nearly empty');
}

// ── COMPLETENESS AGAINST THE DEFAULTS TABLE ───────────────────────────────
//
// The patterns above recognise the SHAPES populate() uses to fill fields. They
// cannot recognise a shape nobody has written yet — and one already existed:
// `ALERT_TYPE_MAP.forEach(function (m) { … $('s_' + m.field) … })` iterates
// objects with a `field:` key, so THIRTEEN alert toggles were captured by
// nothing. The port never set them, and the coverage list added for the slider
// labels did not help either, because those lookups are dynamic rather than
// literal.
//
// So completeness is checked against data instead of against patterns: every
// settings key that populate() MENTIONS must end up classified. A key it names
// and this file does not place is a gap, and the generator refuses rather than
// writing a map that is quietly short.
const DEFAULT_KEYS = Object.keys(
  JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'internal', 'store',
    'settings_tables.json'), 'utf8')).defaults);

const mentioned = new Set(
  [...body.matchAll(/'([A-Za-z][A-Za-z0-9_]*)'/g)].map((m) => m[1]),
);
const classified = new Set([...Object.keys(fields), ...Object.keys(placeholders)]);
// Keys populate() names but deliberately does not fill: read to derive something
// else, or handled by a function of its own.
const NOT_A_FORM_FIELD = new Set([
  'authMode',   // read to derive the auth toggle; applied by _applyAuthModeVisibility
]);
const unclassified = DEFAULT_KEYS
  .filter((k) => mentioned.has(k) && !classified.has(k) && !NOT_A_FORM_FIELD.has(k));
if (unclassified.length) {
  throw new Error('populate() names these settings and this generator did not classify ' +
    'them:\n  ' + unclassified.join('\n  ') +
    '\nAdd the shape that fills them, or list each in NOT_A_FORM_FIELD with a reason.');
}

function main() {
  const check = process.argv.includes('--check');
  const n = Object.keys(fields).length;
  if (n < 50) {
    throw new Error('captured only ' + n + ' fields from populate() — the function ' +
      'changed shape and this map would silently fill almost nothing');
  }
  if (Object.keys(placeholders).length < 3) {
    throw new Error('captured ' + Object.keys(placeholders).length + ' placeholder ' +
      'credentials; the live form has four');
  }

  const byKind = {};
  for (const [k, v] of Object.entries(fields)) (byKind[v] = byKind[v] || []).push(k);
  for (const list of Object.values(byKind)) list.sort();

  const body2 = JSON.stringify({
    note: 'Generated by tools/settings-form-map.js from the LIVE public/app.js populate(). Do not edit.',
    kinds: {
      value: 'el.value = data[f] !== undefined ? data[f] : ""',
      checkOn: 'el.checked = data[f] !== false — ABSENT MEANS ON',
      checkOff: 'el.checked = !!data[f] — absent means off',
    },
    fields: Object.fromEntries(Object.entries(byKind).map(([k, v]) => [k, v])),
    // Per-field default for the `value` kind — see valueKind above.
    valueDefaults: Object.fromEntries(Object.entries(valueDefaults).sort()),
    placeholderCredentials: placeholders,
    // Every id populate() looks up — see the note above.
    touchedIds,
  }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body2) {
      console.error('testdata/settings-form-map.json is stale — run: node tools/settings-form-map.js');
      process.exit(1);
    }
    console.log('settings form map up to date (' + n + ' fields)');
    return;
  }
  fs.writeFileSync(OUT, body2);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) + ' — ' + n +
              ' fields (' + Object.entries(byKind).map(([k, v]) => v.length + ' ' + k).join(', ') +
              '), ' + Object.keys(placeholders).length + ' placeholder credentials');
}

main();
