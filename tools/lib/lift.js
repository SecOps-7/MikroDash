'use strict';
/**
 * LIFTING A LIVE RENDERER OUT OF app.js.
 *
 * The page gates all do the same four things before they can compare anything,
 * and each one got at least one of them wrong the first time. The mistakes are
 * collected here so the next gate inherits them as solved:
 *
 * 1. BOUNDING A REGION. app.js closes an IIFE as `}());` in some places and
 *    `})();` in others. Searching for one spelling ran the Queues slice 2,000
 *    lines past its page — and BOTH of that gate's structural assertions still
 *    passed, because they were INCLUSION checks and an over-long slice satisfies
 *    those. `region()` takes markers that must NOT appear, because a slice is
 *    only proved correct by what it excludes.
 *
 * 2. EXTRACTING A FUNCTION. Taking everything up to the first line beginning `}`
 *    truncates any function containing a nested block, and the result is a
 *    syntax error nowhere near the cause. `whole()` brace-matches, and refuses an
 *    ambiguous anchor rather than lifting the wrong one.
 *
 * 3. FINDING THE ELEMENTS. They come in THREE spellings and scanning a region
 *    for `$('id')` finds only the first:
 *      - `$('wlSsidList')`            — inside the region
 *      - `_renderSortHeader('wlThead', …)` — an id passed as an ARGUMENT
 *      - `var wirelessTable = $('wirelessTable');` — resolved at FILE scope and
 *        merely REFERENCED in the region
 *    Missing the second left the Queues headers written-but-unwired; missing the
 *    third cost two runs of crash-at-a-time discovery.
 *
 * 4. SAYING WHAT WAS MISSING. A page returns early when an element is absent, so
 *    a short id list yields a page that renders nothing, silently. `declare()`
 *    emits the file-scope element vars a region needs.
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

/**
 * The live source. `app.js` by default — but NOT everything lives there.
 *
 * `public/js/topology.js` is a separate 1,180-line file, and a gate looking for
 * `computeLayout` in app.js finds nothing and concludes the function does not
 * exist. Pass a relative path to read another one.
 */
// ── THE REFERENCE MAY BE GONE, AND THAT MUST NOT CRASH A GATE AT LOAD ─────
//
// Every gate lifts at MODULE SCOPE, so without `../MikroDash` it died on
// require — before `golden()` could serve a frozen output, and before any
// assertion ran. Measured 2026-08-30: `ENOENT .../public/app.js` at
// `bridges-page-check.js:57`.
//
// So when the reference is absent the lifters degrade to INERT STUBS. The gate
// still builds a `liveRun`, that `liveRun` is simply never called, because
// `golden().live()` returns the recording instead. If a gate DOES call it, it
// gets an empty renderer and its assertions fail loudly — which is the correct
// outcome for a gate that has no frozen output and no reference.
//
// `absent` is resolved ONCE. Asking per helper turns one clear cause into
// dozens of unrelated-looking failures.
let absent = null;
function referenceAbsent(root) {
  if (absent === null) {
    const live = path.resolve(process.env.MIKRODASH_SRC || path.join(root, '..', 'MikroDash'));
    absent = !fs.existsSync(path.join(live, 'public', 'app.js'));
  }
  return absent;
}

function liveSource(root, rel) {
  if (referenceAbsent(root)) return '';
  const live = path.resolve(process.env.MIKRODASH_SRC || path.join(root, '..', 'MikroDash'));
  return fs.readFileSync(path.join(live, rel || path.join('public', 'app.js')), 'utf8');
}

/**
 * Slice a page's IIFE out of app.js.
 *
 * @param {string}   src
 * @param {object}   o
 * @param {string}   [o.banner]   text that precedes the IIFE (a page banner)
 * @param {string}   [o.contains] text INSIDE the IIFE, when there is no banner
 * @param {string[]} o.must       markers the slice must contain
 * @param {string[]} o.mustNot    markers proving it stopped in time
 */
function region(src, o) {
  if (src === '') return '';
  const lines = src.split('\n');
  let open = -1;
  if (o.banner) {
    const at = src.indexOf(o.banner);
    assert.ok(at > 0, 'no banner in app.js: ' + o.banner);
    const i = src.indexOf('(function', at);
    assert.ok(i > at && i - at < 2000, 'the IIFE is not where its banner says: ' + o.banner);
    open = src.slice(0, i).split('\n').length - 1;
  } else {
    const at = lines.findIndex((l) => l.includes(o.contains));
    assert.ok(at > 0, 'not found in app.js: ' + o.contains);
    for (let j = at; j >= 0; j--) if (/^\(function\s*\(?\s*\)?\s*\{|^\(function\s+\w+\s*\(\s*\)\s*\{/.test(lines[j])) { open = j; break; }
    assert.ok(open >= 0, o.contains + ' is not inside an IIFE');
  }
  // BOTH closing spellings, whichever comes first — see rule 1.
  let close = -1;
  for (let j = open + 1; j < lines.length; j++) {
    if (/^\}\)\(\);|^\}\(\)\);/.test(lines[j])) { close = j; break; }
  }
  assert.ok(close > open, 'the IIFE never closes at column 0');
  const body = lines.slice(open + 1, close).join('\n');

  for (const m of o.must || []) {
    assert.ok(body.includes(m), 'the lifted region lost ' + m);
  }
  for (const m of o.mustNot || []) {
    assert.ok(!body.includes(m), 'the lifted region reaches into another page (found ' + m + ')');
  }
  return body;
}

/**
 * A file-scope `socket.on('ev', function (d) { … })` body.
 *
 * The FOURTH place page code lives, after an IIFE with a banner, an IIFE without
 * one, and a named function. The VPN page is written this way — the handler sits
 * at file scope and serves both the dashboard mini card and the page — so
 * `region()` cannot find it and there is no enclosing IIFE to bound.
 */
function handler(src, event, opts) {
  if (src === '') return '';
  const o = opts || {};
  const anchor = "socket.on('" + event + "'";
  const n = src.split(anchor).length - 1;
  // MORE THAN ONE HANDLER FOR AN EVENT IS NORMAL, not a mistake: `logs:history`
  // is subscribed by both the Logs page and the dashboard card. Pass
  // `opts.contains` to select by CONTENT rather than by position — an anchor
  // that silently took the first match would lift the wrong page's renderer and
  // compare it against this one's port module.
  let at;
  if (o.contains) {
    let from = 0, found = -1;
    for (;;) {
      const i = src.indexOf(anchor, from);
      if (i < 0) break;
      const fnAt = src.indexOf('function', i);
      const open = src.indexOf('{', fnAt);
      let d = 0, end = -1;
      for (let j = open; j < src.length; j++) {
        if (src[j] === '{') d++;
        else if (src[j] === '}') { d--; if (!d) { end = j; break; } }
      }
      if (end > 0 && src.slice(open, end).includes(o.contains)) { found = i; break; }
      from = i + 1;
    }
    assert.ok(found >= 0, 'no ' + event + ' handler containing ' + o.contains);
    at = found;
  } else {
    assert.equal(n, 1, 'AMBIGUOUS handler anchor (' + n + '): ' + event +
      ' — pass opts.contains to select one');
    at = src.indexOf(anchor);
  }
  const fnAt = src.indexOf('function', at);
  assert.ok(fnAt > at && fnAt - at < 200, event + ' is not handled by an inline function');
  const open = src.indexOf('{', fnAt);
  let depth = 0;
  for (let j = open; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}') { depth--; if (!depth) return src.slice(open + 1, j); }
  }
  throw new Error('unbalanced handler body for ' + event);
}

/** One whole function, brace-matched, refusing an ambiguous anchor. */
function whole(src, decl) {
  if (src === '') return '';
  const n = src.split(decl).length - 1;
  assert.equal(n, 1, 'AMBIGUOUS anchor (' + n + '): ' + decl);
  const i = src.indexOf(decl);
  const open = src.indexOf('{', i);
  let depth = 0;
  for (let j = open; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}') { depth--; if (!depth) return src.slice(i, j + 1); }
  }
  throw new Error('unbalanced body for ' + decl);
}

/** A single-line declaration, for one-liners like `function esc(s){…}`. */
function line(src, decl) {
  if (src === '') return '';
  const i = src.indexOf(decl);
  assert.ok(i > 0, 'not found: ' + decl);
  return src.slice(i, src.indexOf('\n', i));
}

/** File-scope `var x = $('id');` declarations the region references. */
function fileScopeEls(src, body) {
  if (src === '') return [];
  const out = [...src.matchAll(/^var\s+([A-Za-z_][\w]*)\s*=\s*\$\('([A-Za-z0-9_-]+)'\);/gm)]
    .filter(([, name]) => new RegExp('\\b' + name + '\\b').test(body))
    .map(([, name, id]) => ({ name, id }));
  return out;
}

/**
 * File-scope `var`/`const` declarations that lifted code REFERENCES.
 *
 * The same scavenger hunt as `fileScopeEls`, one level up: a lifted handler
 * reaches module state (`_ifaceView`, `IFACE_SPARK_LEN`, `_ifaceHistory`) and
 * discovering them one ReferenceError at a time is how three runs get spent.
 *
 * Taken FROM THE SOURCE rather than invented, because these carry defaults —
 * `_ifaceView = 'sm'` is what a viewer sees before touching anything, and a
 * guessed value tests a configuration nobody starts in.
 *
 * LITERAL INITIALISERS ONLY — a string, number, boolean, null, `{}` or `[]`.
 * The first version took any single-line declaration and swept up
 * `var socket = io(...)`, which then failed to construct. A declaration that
 * CALLS something has dependencies and side effects; one that holds a literal is
 * exactly the default this is for. Anything else is left to the caller, because
 * a partial capture is worse than an obvious absence.
 */
function fileScopeVars(src, body, skip) {
  if (src === '') return '';
  const declared = new Map();
  const LITERAL = /^(?:var|const|let)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:'[^']*'|"[^"]*"|-?\d+(?:\.\d+)?|true|false|null|\{\s*\}|\[\s*\])\s*;\s*$/;
  for (const raw of src.split('\n')) {
    // COLUMN 0 ONLY. Trimming first swept up function-local `var seen = {};`
    // declarations, which are not module state at all — the indentation IS the
    // scope marker in this file.
    if (/^\s/.test(raw)) continue;
    const m = LITERAL.exec(raw);
    if (m && !declared.has(m[1])) declared.set(m[1], raw.trim());
  }
  const out = [];
  const seen = new Set(skip || []);
  for (const [name, decl] of declared) {
    if (seen.has(name)) continue;
    // Escape EVERY metacharacter, not just `$`. A JavaScript identifier can only
    // contain [A-Za-z0-9_$] so escaping `$` alone was sufficient for this input,
    // but the completeness is an accident of the caller rather than a property of
    // this line, and the next caller will not know that.
    const esc = name.replace(/[.*+?^${}()|[\]\\-]/g, '\\$&');
    if (!new RegExp('(?:^|[^.\\w$])' + esc + '\\b').test(body)) continue;
    out.push(decl);
  }
  return out.join('\n');
}

/**
 * Element vars declared INSIDE a lifted region (`var rptFrom = $('rptFrom');`).
 *
 * `fileScopeEls` is anchored to column 0 because that is the scope marker in
 * app.js; a page written as an IIFE declares its elements indented, and those
 * are exactly the ones a lift of that IIFE needs. Both exist because both
 * spellings do — the Reports page uses this one and the Interfaces page the
 * other, and using the wrong one produces `rptFrom is not defined` several runs
 * into a gate.
 */
function regionEls(body) {
  if (body === '') return [];
  return [...body.matchAll(/var\s+([A-Za-z_][\w]*)\s*=\s*\$\('([A-Za-z0-9_-]+)'\)/g)]
    .map(([, name, id]) => ({ name, id }));
}

/** JS re-declaring those vars, to prepend to a lifted region. */
function declare(els) {
  return els.map((e) => 'var ' + e.name + ' = $("' + e.id + '");').join('\n');
}

/**
 * Every id a region needs, in all three spellings.
 * @param {string[]} [extraArgIds] ids passed as arguments that the caller knows
 *                                 about (e.g. a thead named in a helper call)
 */
function idsFor(src, body, extraArgIds) {
  if (src === '') return [];
  const ids = new Set(extraArgIds || []);
  for (const m of body.matchAll(/\$\('([A-Za-z0-9_-]+)'\)/g)) ids.add(m[1]);
  // An id passed as the FIRST argument to a helper — the spelling that left the
  // Queues sort headers unwired.
  for (const m of body.matchAll(/_?renderSortHeader\('([A-Za-z0-9_-]+)'/g)) ids.add(m[1]);
  for (const m of body.matchAll(/id="([A-Za-z0-9_-]+)"/g)) ids.add(m[1]);
  for (const e of fileScopeEls(src, body)) ids.add(e.id);
  return [...ids];
}

/**
 * IS THERE A REFERENCE TO LIFT FROM?
 *
 * Gates assert at module scope that their lift still works — "the gauge slice
 * lost its return", "cannot find MAX_CLIENT_POINTS". Those checks are valuable
 * while `../MikroDash` exists and MEANINGLESS without it: the lifters return
 * stubs, so every one of them fires for a reason that has nothing to do with
 * what it guards.
 *
 * Guarding them on this is the honest fix. The alternative — freezing the lifted
 * TEXT so the asserts still see something — would put the reference's JavaScript
 * in `testdata/`, which is the thing this whole exercise is removing.
 *
 *   if (L.hasReference(ROOT)) assert.ok(SLICE.includes('return'), '...');
 */
function hasReference(root) {
  return !referenceAbsent(root);
}

module.exports = { liveSource, region, handler, whole, line, fileScopeEls, regionEls, fileScopeVars, declare, idsFor, hasReference };

/**
 * Freeze a scenario graph so a differential gate cannot leak state between the
 * two implementations it compares.
 *
 * ── A FALSE ACCUSATION IS WORSE THAN A MISSED BUG ──────────────────────────
 *
 * These gates hand ONE case object to both `runLive` and `runPort`. A `drive()`
 * that mutates it — flipping `disabled` to model an operator re-enabling a
 * router, say — leaves the live run's mutation in place when the port runs, and
 * the gate reports the PORT as diverging. That happened on 2026-08-29 in
 * `settings-routers-check.js`, and the harness was the last thing suspected: the
 * output accuses the code, points at a real file and line, and invites somebody
 * to "fix" something that was already correct.
 *
 * So the mistake is made impossible rather than written down. A mutating drive
 * throws inside the run that did it, naming the property, instead of quietly
 * corrupting the next run.
 *
 * DEEP, because the interesting state is nested (`fleet[i].disabled`,
 * `status[id]`) and a shallow freeze catches none of the cases that have
 * actually occurred. Scenarios need no mutable scratch: the per-run world and
 * log are built fresh by each runner.
 */
function freezeCase(o, seen = new Set()) {
  if (o === null || typeof o !== 'object' || seen.has(o)) return o;
  seen.add(o);
  for (const v of Object.values(o)) freezeCase(v, seen);
  return Object.freeze(o);
}

module.exports.freezeCase = freezeCase;

/**
 * FREEZING THE REFERENCE'S OUTPUT, so a gate outlives the source it lifted from.
 *
 * ── WHY ─────────────────────────────────────────────────────────────────────
 *
 * These gates drive the LIVE renderer and the PORT's renderer from one payload
 * and compare the result. That is the right design while both exist. After
 * cutover only one does: `../MikroDash` is a frozen reference that will be
 * deleted or moved, and MEASURED on 2026-08-30, without it the sweep reports
 * `verify: 240 failing` — 118 of 136 gates among them. A contributor cloning
 * this repo today cannot run them at all.
 *
 * So the live half is recorded once, while the reference still exists, and the
 * gate then asserts the PORT reproduces that recording. A differential check
 * becomes a golden-file assertion about our own behaviour.
 *
 * ── THE THREE STATES, AND WHY THE THIRD IS NOT A SKIP ──────────────────────
 *
 *   reference + --freeze   run live, RECORD it, return it
 *   reference, no flag     run live, return it, AND compare it to the recording
 *                          so a stale golden is caught while it can still be
 *                          regenerated
 *   no reference           return the recording
 *
 * A MISSING RECORDING IS A HARD ERROR, never a skip. A gate that quietly passes
 * because it has nothing to compare against is worse than the coupling it
 * replaced — that is the "check that cannot fail" defect this repo has found
 * four times, most recently in `loop-sections-audit.js`, which reported
 * "0 items in section 1" and passed.
 *
 *   const G = golden('bridges-page-check');
 *   const a = G.live('empty bridge list', () => liveRun(payload, opts));
 *
 * Regenerate with:  node tools/<gate>.js --freeze
 */
function golden(gateName) {
  const file = path.join(__dirname, '..', '..', 'testdata', 'golden-gates', gateName + '.json');
  const freezing = process.argv.includes('--freeze');
  // RESOLVED ONCE, not per case: `liveSource` throws when the reference is
  // absent, and asking 200 times per run turns one clear failure into 200.
  let haveRef = false;
  try {
    const live = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', '..', 'MikroDash'));
    haveRef = fs.existsSync(path.join(live, 'public', 'app.js'));
  } catch { /* absent */ }

  // A SEQUENCE KEY, for converted gates where no case-name variable is in
  // scope. Guessing that a loop exposes `name` produced `ReferenceError: name is
  // not defined` in four gates across two batches; a counter needs no guess and
  // cannot collide, because the freeze run and the compare run execute the same
  // sequence. The cost is an opaque key in a drift message, which is a fair
  // trade for not fabricating a variable.
  let seq = 0;

  let store = {};
  if (fs.existsSync(file)) {
    try {
      store = JSON.parse(fs.readFileSync(file, 'utf8'));
    } catch (e) {
      throw new Error(gateName + ': ' + file + ' does not parse (' + e.message +
        '). Delete it and re-run with --freeze rather than editing it by hand.');
    }
  }
  const recorded = {};
  let drifted = 0;

  const api = {
    haveRef,
    live(caseName, fn) {
      // ── ASYNC GATES RECORD THEIR RESULT, NOT THEIR PROMISE ──────────────
      //
      // `audit-page-check` has `async function liveRun`, so `fn()` returns a
      // Promise. Recording that stored `{}` and the gate then failed without the
      // reference — caught by reading the golden rather than by trusting that a
      // freeze which printed "froze 8 case(s)" had frozen anything useful.
      //
      // Detecting a thenable keeps every call site unchanged: `await G.live(...)`
      // and `G.live(...)` both work.
      if (haveRef) {
        const value = fn();
        if (value && typeof value.then === 'function') {
          return value.then((settled) => {
            api._record(caseName, settled);
            return settled;
          });
        }
        api._record(caseName, value);
        return value;
      }
      if (!Object.prototype.hasOwnProperty.call(store, caseName)) {
        throw new Error(gateName + ': no reference repo AND no frozen output for "' +
          caseName + '". This gate cannot check anything. Run it once with ' +
          'MIKRODASH_SRC set and --freeze, and commit ' + file + '.');
      }
      return JSON.parse(store[caseName]);
    },
    /**
     * The same three states, for a lifted VALUE rather than a rendered output.
     *
     * NEEDED because the port's own harness is built from the reference too:
     * `bridges-page-check` derives its DOM from `L.idsFor(src, iife)`, so
     * stubbing the lifters emptied the PORT's scaffolding and every case
     * "differed" for a reason that had nothing to do with the port. Freezing the
     * output alone is not enough — everything derived from the reference has to
     * be frozen, or the gate compares the port against an empty document.
     */
    value(caseName, fn) {
      const k = 'value:' + caseName;
      if (haveRef) {
        const v = fn();
        const json = JSON.stringify(v);
        if (freezing) {
          recorded[k] = json;
        } else if (Object.prototype.hasOwnProperty.call(store, k) && store[k] !== json) {
          drifted++;
          process.exitCode = 1;
          console.error(gateName + ': the frozen value "' + caseName +
            '" no longer matches the reference. Re-run with --freeze if the ' +
            'reference legitimately changed.');
        }
        return v;
      }
      if (!Object.prototype.hasOwnProperty.call(store, k)) {
        throw new Error(gateName + ': no reference repo AND no frozen value for "' +
          caseName + '". Run once with MIKRODASH_SRC set and --freeze.');
      }
      return JSON.parse(store[k]);
    },
    // COMPARED AS JSON, NOT BY IDENTITY. `!==` works for the JSON strings most
    // gates return and compares OBJECT REFERENCES for the ones that return a
    // record — `connections-lists-page-check` returns `{reused, order, ...}`, so
    // every case reported drift even immediately after a freeze. 23 false
    // "no longer matches the reference" lines, from a comparison that could
    // never be true.
    seq() { return 'call:' + (++seq); },
    _record(caseName, value) {
      const json = JSON.stringify(value);
      if (freezing) {
        recorded[caseName] = json;
        return;
      }
      if (Object.prototype.hasOwnProperty.call(store, caseName) && store[caseName] !== json) {
        drifted++;
        process.exitCode = 1;
        console.error(gateName + ': the frozen output for "' + caseName +
          '" no longer matches the reference. Re-run with --freeze if the ' +
          'reference legitimately changed.');
      }
    },
    // Written at exit so a gate cannot forget to save. Only in freeze mode.
    save() {
      if (!freezing) return;
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.writeFileSync(file, JSON.stringify(recorded, null, 1) + '\n');
      console.log(gateName + ': froze ' + Object.keys(recorded).length + ' case(s) -> ' + file);
    },
    drifted: () => drifted,
  };
  process.on('exit', api.save);
  return api;
}
module.exports.golden = golden;
