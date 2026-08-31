'use strict';
/**
 * Does the port fill each Settings input the way `populate()` does?
 *
 * ── HOW THIS IS A DIFFERENTIAL AND NOT A RETYPING ──────────────────────────
 *
 * The live `populate()` lives inside a 1,850-line IIFE that also owns the
 * routers, sites, groups, roles and users panes, and it calls half a dozen of
 * that closure's private helpers. Lifting it whole — the way live-renderer.js
 * lifts a page — is not practical here.
 *
 * So this compares one level down: `tools/settings-form-map.js` captures each
 * field's assignment EXPRESSION verbatim from the live source, and this
 * evaluates that expression against the same inputs the port is given. The
 * comparison is therefore against the original text, not against a copy of it
 * that somebody typed out — which is the difference between a gate and two
 * implementations agreeing with each other.
 *
 * WHAT IT DOES NOT COVER, said plainly: the ORDER populate() does things in, its
 * side effects (the view-preset detection, the auth-mode visibility), and the
 * panes that are not settings fields. Those need the browser gate.
 *
 *   node tools/settings-populate-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const MAP = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'settings-form-map.json'), 'utf8'));

// The inputs each rule has to be tried against. The absent case is the one the
// rules disagree on, so it is first and every field gets it.
const PROBES = [
  { name: 'absent', mk: () => ({}) },
  { name: 'empty string', mk: (k) => ({ [k]: '' }) },
  { name: 'zero', mk: (k) => ({ [k]: 0 }) },
  { name: 'false', mk: (k) => ({ [k]: false }) },
  { name: 'null', mk: (k) => ({ [k]: null }) },
  { name: 'a number', mk: (k) => ({ [k]: 2525 }) },
  { name: 'a string', mk: (k) => ({ [k]: 'a value' }) },
];

// Build the port's valueFor once, from the compiled bundle.
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-settings.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

// The port's populate writes into inputs, so give it a shim and read them back.
function portValue(key, data) { return portValueFrom(key, data, {}); }

function portValueFrom(key, data, initial) {
  const node = Object.assign({ value: '', checked: false, placeholder: '' }, initial);
  const doc = { getElementById: (id) => (id === 's_' + key ? node : null) };
  const prev = global.document;
  global.document = doc;
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).populateSettings(data);
  } finally {
    if (prev === undefined) delete global.document; else global.document = prev;
  }
  return node;
}

let checks = 0;
const bad = [];

// ── the value fields ────────────────────────────────────────────────────────
for (const [key, rule] of Object.entries(MAP.valueDefaults)) {
  if (rule.kind === 'blank') continue;             // covered by the placeholder pass
  // The expression as the live source writes it, with `f` bound for the
  // list-driven form.
  const fn = new Function('data', 'f', 'return (' + rule.expr + ');');
  for (const probe of PROBES) {
    const data = probe.mk(key);
    let want;
    try { want = fn(data, key); } catch (e) { want = '<threw: ' + e.message + '>'; }
    // An input's value is always a string in the DOM; the original assigns
    // whatever the expression produced and the browser coerces it.
    const wantStr = want === undefined ? 'undefined' : String(want);

    // `stringOf` is guarded by `!= null` at its call site, so a null or
    // undefined leaves the input alone — its previous value, which is ''.
    const guarded = rule.kind === 'stringOf' && (data[key] === undefined || data[key] === null);
    const expected = guarded ? '' : wantStr;

    const got = portValue(key, data).value;
    checks++;
    if (got !== expected) {
      bad.push({ what: key + ' [' + rule.kind + '] with ' + probe.name,
                 live: expected, port: got, expr: rule.expr });
    }
  }
}

// ── the checkboxes ──────────────────────────────────────────────────────────
for (const [kind, rule] of [['checkOn', (v) => v !== false], ['checkOff', (v) => !!v]]) {
  for (const key of MAP.fields[kind] || []) {
    for (const probe of PROBES) {
      const data = probe.mk(key);
      const expected = rule(data[key]);
      const got = portValue(key, data).checked;
      checks++;
      if (got !== expected) {
        bad.push({ what: key + ' [' + kind + '] with ' + probe.name, live: expected, port: got });
      }
    }
  }
}

// ── THE GUARDED CHECKBOXES ────────────────────────────────────────────────
//
// An ABSENT value must leave the checkbox untouched, so the shim starts it at
// `true` — the state a `checkOff` port would wrongly clear. A present value is
// coerced the same way `!!` coerces.
for (const key of MAP.fields.checkGuarded || []) {
  for (const probe of PROBES) {
    const data = probe.mk(key);
    const absent = !(key in data);
    const node = portValueFrom(key, data, { checked: true });
    checks++;
    const expected = absent ? true : !!data[key];
    if (node.checked !== expected) {
      bad.push({ what: key + ' [checkGuarded] with ' + probe.name,
                 live: expected, port: node.checked });
    }
  }
}

// ── the placeholder credentials ─────────────────────────────────────────────
for (const [key, texts] of Object.entries(MAP.placeholderCredentials)) {
  for (const probe of PROBES) {
    const data = probe.mk(key);
    const node = portValue(key, data);
    checks += 2;
    if (node.value !== '') {
      bad.push({ what: key + ' value with ' + probe.name, live: '(always blank)', port: node.value });
    }
    const expected = data[key] ? texts.whenSet : texts.whenNot;
    if (node.placeholder !== expected) {
      bad.push({ what: key + ' placeholder with ' + probe.name, live: expected, port: node.placeholder });
    }
  }
}

// ── COVERAGE: EVERY ELEMENT populate() TOUCHES BY LITERAL ID ──────────────
//
// The comparisons above only inspect ids the field map NAMES, so an element
// populate() writes and the port does not is invisible to them. That is exactly
// how three slider labels were missed — the port set the sliders and left their
// readouts blank, and this file reported 518 green checks.
//
// So: for each literal id the live populate() looks up, drive the port with a
// payload that has every key set and record whether it writes anything there.
// An id the port never touches must be listed below WITH A REASON.
const NOT_WRITTEN_BY_POPULATE = {
  // Read to decide visibility, never written by populate() itself.
  s_authEnabled: 'a checkbox populate() reads to derive the auth MODE; the mode is ' +
                 'applied by applyAuthModeVisibility, which is ported and gated separately',
};

{
  const everyKey = {};
  for (const kind of ['value', 'checkOn', 'checkOff']) {
    for (const k of (MAP.fields[kind] || [])) everyKey[k] = kind === 'value' ? 'x' : true;
  }
  for (const k of Object.keys(MAP.placeholderCredentials)) everyKey[k] = 'x';
  for (const k of ['alertCpuThreshold', 'alertPingLoss', 'notifCooldownSec']) everyKey[k] = 42;

  const touched = new Set();
  const node = () => {
    const n = {};
    for (const prop of ['value', 'checked', 'textContent', 'placeholder']) {
      let v;
      Object.defineProperty(n, prop, {
        get: () => v,
        set: (x) => { v = x; n.__written = true; },
        enumerable: true,
      });
    }
    return n;
  };
  const nodes = {};
  const doc = { getElementById: (id) => (nodes[id] = nodes[id] || node()) };
  const prev = global.document;
  global.document = doc;
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).populateSettings(everyKey);
  } finally {
    if (prev === undefined) delete global.document; else global.document = prev;
  }
  for (const id of Object.keys(nodes)) if (nodes[id].__written) touched.add(id);

  const missing = (MAP.touchedIds || []).filter(
    (id) => !touched.has(id) && !(id in NOT_WRITTEN_BY_POPULATE));
  if (missing.length) {
    console.error('\npopulate() writes these elements and the port does not:');
    for (const id of missing) console.error('  ' + id);
    console.error('Port them, or list each in NOT_WRITTEN_BY_POPULATE with a reason.');
    process.exit(1);
  }
  checks += (MAP.touchedIds || []).length;
}

if (bad.length) {
  for (const d of bad) {
    console.error('\n' + d.what);
    console.error('  live: ' + JSON.stringify(d.live));
    console.error('  port: ' + JSON.stringify(d.port));
    if (d.expr) console.error('  from: ' + d.expr);
  }
  process.exit(1);
}
if (checks < 200) {
  console.error('only ' + checks + ' checks ran — the map is not being read');
  process.exit(1);
}
console.log('settings form matches populate() (' + checks + ' checks across ' +
            Object.keys(MAP.valueDefaults).length + ' value fields, ' +
            (MAP.fields.checkOn || []).length + ' checkOn, ' +
            (MAP.fields.checkOff || []).length + ' checkOff, ' +
            Object.keys(MAP.placeholderCredentials).length + ' credentials)');
