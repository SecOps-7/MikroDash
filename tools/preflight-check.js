'use strict';
/**
 * The <head> script, PORT against LIVE, driven from one harness.
 *
 * ---- WHY -------------------------------------------------------------------
 *
 * `web/public/preflight.js` was a byte-for-byte copy of the live repo's file.
 * The operator on 2026-08-28: "the port should stand on its own without any
 * lingering JS from the live repo." This runs both and compares.
 *
 * ---- WHAT IT DOES, AND WHY EACH HALF IS EASY TO GET WRONG -----------------
 *
 *  1. HIDES the document when `justLoggedIn` is set, so the app does not flash
 *     its default colours between the redirect and the first paint. `main.ts`
 *     restores it. If this half were dropped nothing would break; if the
 *     RESTORE were dropped the app renders perfectly and invisibly, which is a
 *     defect this port shipped once. `tools/login-fade-check.js` spans that
 *     boundary; this gate covers only what preflight itself does.
 *
 *  2. Sets the NAV SHAPE from a localStorage cache, before the sidebar paints.
 *     Three things could silently differ: the DEFAULT when no preference is
 *     stored (grouped, not flat — `nav.grouped === false`, not `!nav.grouped`),
 *     the SHAPE GUARD on category keys, and the exact CSS emitted.
 *
 * ---- THE GUARD IS THE SECURITY-RELEVANT PART ------------------------------
 *
 * `_open` keys go straight into a selector in a generated <style>. The live
 * filter is `/^[a-z]{2,20}$/`, which admits nothing that could close the
 * attribute or the rule. A port that widened it — or dropped it — would turn a
 * localStorage value into stylesheet injection, so the corpus carries a quote,
 * a brace, a closing bracket and a length either side of both bounds.
 *
 *   node tools/preflight-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('preflight-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
// FROZEN — `run()` EXECUTES this whole file, so the file itself is the lifted
// source. Freezing it keeps the live half running against every scenario,
// including any added later.
const liveSrc = G.value('the live preflight.js', () => LIFT.liveSource(ROOT, path.join('public', 'preflight.js')));
if (!liveSrc || liveSrc.length < 500) {
  throw new Error('the recorded preflight.js is empty — the golden is broken');
}
const portPath = path.join(ROOT, 'web', 'dist', 'preflight.js');
if (!fs.existsSync(portPath)) {
  console.error('web/dist/preflight.js is missing — run `npm run build` in web/ first.');
  process.exit(1);
}
const portSrc = fs.readFileSync(portPath, 'utf8');

function run(src, scenario, label) {
  const log = [];
  const created = [];
  const root = {
    style: new Proxy({}, {
      set(t, k, v) { log.push(`root.style.${String(k)}=${v}`); t[k] = v; return true; },
      get(t, k) { return t[k]; },
    }),
    setAttribute: (k, v) => log.push(`root.attr ${k}=${v}`),
  };
  const ctx = {
    JSON,
    Array,
    document: {
      documentElement: root,
      createElement: (tag) => {
        const el = { tagName: tag, _id: '', _text: '' };
        Object.defineProperty(el, 'id', {
          get() { return el._id; },
          set(v) { el._id = v; log.push(`create ${tag}#${v}`); },
        });
        Object.defineProperty(el, 'textContent', {
          get() { return el._text; },
          set(v) { el._text = v; log.push(`css ${JSON.stringify(v)}`); },
        });
        created.push(el);
        return el;
      },
      head: { appendChild: (el) => log.push(`append ${el.tagName}#${el.id}`) },
    },
    sessionStorage: {
      getItem: (k) => (k in scenario.session ? scenario.session[k] : null),
    },
    localStorage: {
      getItem: (k) => {
        if (scenario.localThrows) throw new Error('site data blocked');
        return k in scenario.local ? scenario.local[k] : null;
      },
    },
    console: { log() {}, warn() {}, error() {} },
  };
  ctx.globalThis = ctx;
  vm.runInNewContext(src, ctx, { filename: label });
  return log;
}

const NAV = 'mkd_nav_prefs';
const SCENARIOS = [
  { why: 'nothing stored at all', session: {}, local: {} },
  { why: 'justLoggedIn hides the document', session: { justLoggedIn: '1' }, local: {} },
  // The DEFAULT. `nav.grouped === false`, not `!nav.grouped` — an absent
  // preference means GROUPED, and reading absent as flat collapses the nav for
  // every browser that has never saved one.
  { why: 'an empty prefs object defaults to grouped', session: {}, local: { [NAV]: '{}' } },
  { why: 'grouped: true', session: {}, local: { [NAV]: '{"grouped":true}' } },
  { why: 'grouped: false is the only thing that means flat', session: {}, local: { [NAV]: '{"grouped":false}' } },
  { why: 'grouped: null is NOT false', session: {}, local: { [NAV]: '{"grouped":null}' } },
  { why: 'grouped: 0 is NOT false either — the check is strict', session: {}, local: { [NAV]: '{"grouped":0}' } },
  { why: 'the literal string "null"', session: {}, local: { [NAV]: 'null' } },
  { why: 'unparseable JSON', session: {}, local: { [NAV]: '{not json' } },
  { why: 'a JSON array rather than an object', session: {}, local: { [NAV]: '[1,2]' } },
  // ── expanded ──────────────────────────────────────────────────────────
  { why: 'one expanded category', session: {}, local: { [NAV]: '{"expanded":["network"]}' } },
  { why: 'several expanded categories keep their order', session: {}, local: { [NAV]: '{"expanded":["network","system","wireless"]}' } },
  { why: 'an empty expanded list emits no stylesheet', session: {}, local: { [NAV]: '{"expanded":[]}' } },
  { why: 'expanded is not an array', session: {}, local: { [NAV]: '{"expanded":"network"}' } },
  { why: 'expanded is an object', session: {}, local: { [NAV]: '{"expanded":{"a":1}}' } },
  // ── THE SHAPE GUARD ───────────────────────────────────────────────────
  { why: 'a key with a quote is dropped', session: {}, local: { [NAV]: '{"expanded":["a\\"]{display:none}body{"]}' } },
  { why: 'a key with a brace is dropped', session: {}, local: { [NAV]: '{"expanded":["net}work"]}' } },
  { why: 'a key with a closing bracket is dropped', session: {}, local: { [NAV]: '{"expanded":["net]work"]}' } },
  { why: 'an uppercase key is dropped', session: {}, local: { [NAV]: '{"expanded":["Network"]}' } },
  { why: 'a key with a digit is dropped', session: {}, local: { [NAV]: '{"expanded":["net2"]}' } },
  { why: 'a key with a hyphen is dropped', session: {}, local: { [NAV]: '{"expanded":["net-work"]}' } },
  { why: 'one character — under the lower bound', session: {}, local: { [NAV]: '{"expanded":["a"]}' } },
  { why: 'two characters — exactly the lower bound', session: {}, local: { [NAV]: '{"expanded":["ab"]}' } },
  { why: 'twenty characters — exactly the upper bound', session: {}, local: { [NAV]: '{"expanded":["' + 'a'.repeat(20) + '"]}' } },
  { why: 'twenty-one characters — one over', session: {}, local: { [NAV]: '{"expanded":["' + 'a'.repeat(21) + '"]}' } },
  { why: 'a non-string entry among good ones', session: {}, local: { [NAV]: '{"expanded":["network",42,"system"]}' } },
  { why: 'every entry invalid emits no stylesheet', session: {}, local: { [NAV]: '{"expanded":["A","b","c-d"]}' } },
  // ── Both halves at once, and the throwing browser ─────────────────────
  { why: 'hidden AND grouped flat AND expanded', session: { justLoggedIn: '1' }, local: { [NAV]: '{"grouped":false,"expanded":["network"]}' } },
  { why: 'localStorage throws — the page must still load', session: { justLoggedIn: '1' }, local: {}, localThrows: true },
];

const problems = [];

// ---- Believability ---------------------------------------------------------
//
// A harness driving neither side would report every scenario identical.
// RE-AIMED AT THE PORT, for the same reason: one side must be shown to drive
// something, and the port is the side that still exists.
const probe = run(portSrc, SCENARIOS.find((s) => s.why.startsWith('hidden AND')), 'port');
for (const must of ['root.style.opacity=0', 'root.attr data-nav=flat', 'append style#navBoot']) {
  if (!probe.includes(must)) {
    problems.push(`the harness never observed "${must}". It is not driving the `
      + `code, so every comparison is two empty logs agreeing. Log was:\n      ${probe.join('\n      ')}`);
  }
}
// The guard must actually drop something, or the whole guard half proves nothing.
{
  const kept = run(liveSrc, SCENARIOS.find((s) => s.why.startsWith('a key with a quote')), 'live');
  if (kept.some((l) => l.startsWith('css '))) {
    problems.push('the LIVE file emitted a stylesheet for a key containing a quote. Either the '
      + 'harness is not reaching the guard, or the guard is gone upstream.');
  }
}
// ...and must KEEP something, or "drops everything" would pass every guard case.
{
  // RE-AIMED AT THE PORT — "must KEEP something, or 'drops everything' would pass
  // every guard case" is the point, and the port is what must keep it.
  const kept = run(portSrc, SCENARIOS.find((s) => s.why.startsWith('one expanded')), 'port');
  if (!kept.some((l) => l.startsWith('css '))) {
    problems.push('the file emitted no stylesheet for a valid key, so the guard cases below '
      + 'are indistinguishable from a file that never emits one');
  }
}

let compared = 0;
if (!problems.length) {
  for (const sc of SCENARIOS) {
    const a = run(liveSrc, sc, 'live');
    const b = run(portSrc, sc, 'port');
    compared++;
    if (JSON.stringify(a) !== JSON.stringify(b)) {
      const n = Math.max(a.length, b.length);
      const diff = [];
      for (let i = 0; i < n; i++) {
        if (a[i] !== b[i]) diff.push(`      ${i}: live=${a[i] || '(none)'}  port=${b[i] || '(none)'}`);
      }
      problems.push(`${sc.why}:\n${diff.join('\n')}`);
    }
  }
}

if (problems.length) {
  console.error('preflight-check: the port and the live file disagree\n');
  for (const p of problems) console.error('  - ' + p + '\n');
  process.exit(1);
}
console.log(`preflight-check: ${compared} scenarios, the built bundle matches the live file `
  + 'operation for operation');
