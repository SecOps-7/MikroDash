'use strict';
/**
 * Consumers with no producer, and producers with no consumer.
 *
 * Three of these turned up in one porting pass: an upgrade dialog rendering a
 * field the system payload never sent, a topology node preferring an identity
 * nothing set, and a device count written into an element that was not in the
 * markup. None of them broke anything, which is the problem — every one is
 * guarded, so it reads as working code and stays that way.
 *
 * The two sweeps here are the general form. They are cheap and they run every
 * time, which beats finding the next one by porting it.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const P = (...p) => fs.readFileSync(path.join(__dirname, '..', ...p), 'utf8');
const APP = P('public', 'app.js');
const MARKUP = P('public', 'index.html') + P('public', 'login.html');

test('every element app.js looks up exists somewhere', () => {
  // `$('bwStats')` returned null on every render of the Bandwidth page, so the
  // device count under the filter row was computed and thrown away.
  const have = new Set([...MARKUP.matchAll(/id="([^"]+)"/g)].map(m => m[1]));
  // Ids app.js builds itself, in template strings, count as present.
  for (const m of APP.matchAll(/id\s*=\s*\\?['"]([A-Za-z][\w-]*)\\?['"]/g)) have.add(m[1]);

  const missing = new Map();
  for (const m of APP.matchAll(/\$\(\s*'([A-Za-z][\w-]*)'\s*\)/g)) {
    if (!have.has(m[1])) missing.set(m[1], (missing.get(m[1]) || 0) + 1);
  }
  // Known orphans, left in place deliberately: each is a remnant of UI that was
  // removed, and CLAUDE.md says to mention unrelated dead code rather than
  // delete it. Shrink this list, never grow it.
  const KNOWN = new Set(['connMapSub', 'ndGateway', 'ndLanCidr', 'routerRestartNotice',
                         's_routerPass', 's_routerPort', 'wanIpDisplay',
                         'wlBand24', 'wlBand5', 'wlBand6']);
  const fresh = [...missing.keys()].filter(id => !KNOWN.has(id)).sort();
  assert.deepEqual(fresh, [],
    'app.js looks up elements that exist in no markup:\n  '
    + fresh.map(id => `${id} (${missing.get(id)} site(s))`).join('\n  '));

  // And the allow-list must not rot: an id that has since been given an element
  // should come off it.
  const stale = [...KNOWN].filter(id => have.has(id)).sort();
  assert.deepEqual(stale, [],
    'these are no longer orphans and should leave the allow-list: ' + stale.join(', '));
});

test('the fields the frontend reads off a collector payload are sent', () => {
  // `d.updateChannel` and `sys.identity` were both read by code that could never
  // see a value, because no collector emitted either name.
  const SYSTEM = P('src', 'collectors', 'system.js');
  const TOPOLOGY = P('src', 'collectors', 'topology.js');

  assert.match(SYSTEM, /updateChannel:/,
    'the upgrade dialog renders updateChannel; the system collector must send it');
  assert.match(APP, /_upd\.channel/,
    'and the dialog must still be reading it');

  // The topology core node no longer prefers a field nothing sets.
  assert.ok(!/sys && sys\.identity/.test(TOPOLOGY),
    'topology must not prefer sys.identity: the system payload carries no identity');
});

// ── windowedPoints must not truncate at an out-of-order sample ──────────────
//
// Reported from the port. The walk went backwards from the newest point and
// `break`-ed at the first sample older than the cutoff, which is correct only
// while allPoints is sorted by ts. One stale sample ended the walk and took
// every older point still inside the window with it, so the chart silently
// redrew short and refilled — it looks like a slow collector, which is why it
// would never be reported from the field.
//
// Two real sources of an out-of-order sample: traffic:history is loaded
// wholesale from the server, and the timestamps are the ROUTER's — a MikroTik
// with no battery steps its clock backwards when NTP corrects a drifted RTC.
test('windowedPoints keeps in-window samples that arrive out of order', () => {
  const vm = require('node:vm');
  const src = APP.match(/function windowedPoints\(\)\{[\s\S]*?\n\}/);
  assert.ok(src, 'windowedPoints not found — did it move or get renamed?');

  const now = 1700000000000;
  const ctx = {
    Date: { now: () => now },
    windowSecs: 60,
    RIGHT_BUFFER_MS: 1000,
    // Third sample is stale but every other one sits inside the 61 s window.
    allPoints: [
      { ts: now - 50000 }, { ts: now - 40000 },
      { ts: now - 90000 },
      { ts: now - 20000 }, { ts: now - 10000 },
    ],
  };
  vm.createContext(ctx);
  vm.runInContext(src[0] + '; var __out = windowedPoints();', ctx);

  // Array.from, not .map: the vm context is a separate realm, so an array
  // built in there has a different Array.prototype and deepStrictEqual
  // fails on identical contents.
  const got = Array.from(ctx.__out, p => (now - p.ts) / 1000);
  assert.deepEqual(got, [50, 40, 20, 10],
    'the stale sample must be skipped, not treated as the end of the window');

  // The inverse, so this cannot pass by simply returning everything: a sample
  // genuinely outside the window is still excluded.
  assert.ok(!got.includes(90), 'the out-of-window sample must not be included');
});

// The third sweep: state that is written and never read.
//
// `lastLanData` was reported from the port, and generalising it found two more
// of the same species — `lastTalkers` on the very same declaration line, which
// the report believed was live, and `_lastSampleAt`. All three are remnants of
// guards removed when an empty payload stopped being treated as "nothing
// changed". The assignments stayed behind and read as live state.
//
// That is the trap this exists for: a write-only variable sitting beside a real
// one is indistinguishable at a glance, and each is harmless on its own, so
// nothing ever forces the question.
//
// espree is a devDependency (via eslint) and only resolves in the `test` stage
// of the Dockerfile — see CONTRIBUTING. Against the runtime image this file
// fails to LOAD rather than failing an assertion.
test('no module-scope variable in app.js is written but never read', () => {
  const espree = require('espree');
  const ast = espree.parse(APP, { ecmaVersion: 2020, loc: true });

  const declared = new Map();
  for (const node of ast.body) {
    if (node.type !== 'VariableDeclaration') continue;
    for (const d of node.declarations) {
      if (d.id.type === 'Identifier') declared.set(d.id.name, d.id.loc.start.line);
    }
  }

  // An identifier counts as READ unless it sits in a position that can only be
  // a write or a name. Shadowing by an inner function makes this conservative —
  // it reports fewer orphans than exist, never more — which is the right
  // direction for a test that fails the build.
  const read = new Set();
  (function walk(node, parent) {
    if (!node || typeof node.type !== 'string') return;
    if (node.type === 'Identifier') {
      let isRead = true;
      if (parent) {
        if (parent.type === 'VariableDeclarator' && parent.id === node) isRead = false;
        if (parent.type === 'AssignmentExpression' && parent.left === node && parent.operator === '=') isRead = false;
        if (parent.type === 'MemberExpression' && parent.property === node && !parent.computed) isRead = false;
        if (parent.type === 'Property' && parent.key === node && !parent.computed) isRead = false;
        if (parent.type === 'FunctionDeclaration' && parent.id === node) isRead = false;
        if ((parent.type === 'FunctionDeclaration' || parent.type === 'FunctionExpression' ||
             parent.type === 'ArrowFunctionExpression') && parent.params.includes(node)) isRead = false;
      }
      if (isRead) read.add(node.name);
      return;
    }
    for (const k of Object.keys(node)) {
      if (k === 'loc' || k === 'range') continue;
      const v = node[k];
      if (Array.isArray(v)) { for (const c of v) walk(c, node); }
      else if (v && typeof v.type === 'string') walk(v, node);
    }
  })(ast, null);

  const orphans = [];
  for (const [name, line] of declared) {
    if (read.has(name)) continue;
    // A name the markup mentions is consumed by an inline handler or attribute.
    if (MARKUP.includes(name)) continue;
    orphans.push(`${name} (app.js:${line})`);
  }

  assert.deepEqual(orphans, [],
    'written but never read — delete it, or read it: ' + orphans.join(', '));
});

// ── the second copy of the windowed walk (ToDo #24) ─────────────────────────
//
// windowedPoints was fixed and _syncBwChart was not. It is redrawChart with two
// edits and inherited the same backward-walk-and-break, so the dashboard chart
// drew a sample the Bandwidth chart dropped, off the same buffer, for the same
// second. The lesson recorded with it: sweep for a second copy of a fixed
// FUNCTION, not just a second instance of the bug shape.
test('both charts select the same points when a sample arrives out of order', () => {
  const vm = require('node:vm');
  const now = 1700000000000;
  // Newest sample first in arrival order, with a stale one wedged in front of
  // it — the shape an NTP correction on a drifted RTC produces.
  const pts = [
    { ts: now - 1000,  rx_mbps: 1, tx_mbps: 1 },
    { ts: now - 70000, rx_mbps: 5, tx_mbps: 5 },
    { ts: now - 2000,  rx_mbps: 2, tx_mbps: 2 },
  ];

  const dash = APP.match(/function windowedPoints\(\)\{[\s\S]*?\n\}/);
  assert.ok(dash, 'windowedPoints not found');
  const bw = APP.match(/var cutoff = Date\.now\(\)[^\n]*\n\s*var pts = allPoints\.filter[^\n]*/);
  assert.ok(bw, 'the _syncBwChart point selection moved or still uses the old walk');

  const runDash = () => {
    const ctx = { Date: { now: () => now }, windowSecs: 60, RIGHT_BUFFER_MS: 1000, allPoints: pts };
    vm.createContext(ctx);
    vm.runInContext(dash[0] + '; var __o = windowedPoints();', ctx);
    return Array.from(ctx.__o, p => p.ts);
  };
  const runBw = () => {
    const ctx = { Date: { now: () => now }, windowSecs: 60, RIGHT_BUFFER_MS: 1000, allPoints: pts };
    vm.createContext(ctx);
    vm.runInContext(bw[0] + '; var __o = pts;', ctx);
    return Array.from(ctx.__o, p => p.ts);
  };

  const d = runDash(), b = runBw();
  assert.ok(d.includes(now - 1000), 'the dashboard chart keeps the newest sample');
  assert.ok(b.includes(now - 1000),
    'and so must the bandwidth chart — the old walk broke before reaching it');
  assert.ok(!d.includes(now - 70000), 'the out-of-window sample is still excluded');

  // The 3 s slack is deliberate and must survive: the bandwidth keepalive prunes
  // at viewLeft - 3000, so its cutoff is wider than the dashboard's by exactly
  // that. Unifying the two would reintroduce a point flickering between frames.
  assert.ok(/RIGHT_BUFFER_MS - 3000/.test(bw[0]),
    'the bandwidth chart keeps its 3 s seeding slack');
});

// ── one audit row must not blank the table (ToDo #21) ───────────────────────
//
// The try wraps the parse only, and JSON.parse('null') does not throw: it
// returns null, and `d.changes` on the next line does. That escaped detailCell
// and render into load()'s empty .catch, so a single row whose detail column
// held the four characters `null` left the whole Audit table blank with the
// filters above it looking normal.
test('a detail column holding null does not take out the audit table', () => {
  const vm = require('node:vm');
  const guard = APP.match(/if \(!d \|\| typeof d !== 'object'\) return [^\n]*/);
  assert.ok(guard, 'the non-object guard in detailCell is missing');

  // The guard plus the line that used to throw, which is what it protects.
  const src = guard[0] + '\n var bits = []; (d.changes || []).slice(0, 4);';
  const run = (raw) => {
    const ctx = { d: JSON.parse(raw), out: null };
    vm.createContext(ctx);
    vm.runInContext('out = (function(){ ' + src + ' return "rendered"; })();', ctx);
    return ctx.out;
  };

  assert.doesNotThrow(() => run('null'),
    'null is the one parse result that is falsy AND has no properties');

  // The inverse, so the guard cannot pass by rejecting everything: a real
  // change set must still reach the renderer.
  assert.equal(run('{"changes":[{"field":"a","from":"1","to":"2"}]}'), 'rendered');
  // And the two values that always worked must keep working.
  assert.doesNotThrow(() => run('12345'));
  assert.doesNotThrow(() => run('"a string"'));
});

// ── the run-history table declares as many columns as it fills (ToDo #26) ────
//
// A regression introduced while fixing #25: replacing the recipients cell took
// the outcome cell with it, so five headers were filled by four cells and every
// value shifted one column left. Recipients rendered under "Result".
//
// That is worse than the bug it replaced. `undefined` in a count column is
// obviously wrong; a plausible number under the wrong heading is not, and the
// empty-state row still said colspan=5 so nothing looked out of place.
//
// Counting is the whole check. It is cheap, it is exact, and it would have
// caught this the moment the edit landed.
test('the report run-history row fills every column its header declares', () => {
  // Anchored on rptSchedRuns, the box this table is written into, NOT on
  // data-rs-runs: that attribute first appears on the History button in the
  // schedules table above, so slicing from it counted the wrong header.
  const block = APP.slice(APP.indexOf("$('rptSchedRuns')"));
  const head = block.match(/<thead><tr>(.*?)<\/tr><\/thead>/);
  assert.ok(head, 'the run-history table header moved or was rewritten');
  const headers = (head[1].match(/<th>/g) || []).length;

  const rowSrc = block.match(/return '<tr>[\s\S]*?<\/tr>';/);
  assert.ok(rowSrc, 'the run-history row template moved or was rewritten');
  const cells = (rowSrc[0].match(/<td[ >]/g) || []).length;

  assert.equal(cells, headers,
    'the row emits ' + cells + ' cells for ' + headers + ' headers, so every ' +
    'value after the missing one renders under the wrong heading');

  // The empty state spans the same table, so it has to agree too. If a column
  // is ever added, this fails alongside the row rather than drifting quietly.
  const span = block.match(/colspan="(\d+)" class="rpt-empty"/);
  assert.ok(span, 'the run-history empty state moved');
  assert.equal(Number(span[1]), headers, 'the empty-state colspan must match the header count');

  // And the two defences from #25 must survive, since this test sits on the
  // same lines and a future edit here is exactly how they would be lost.
  assert.ok(/recipients_n \?\? 0/.test(block),
    'recipients must keep its ?? 0 or the column prints the word "undefined"');
  assert.ok(/\(d\.runs \|\| \[\]\)/.test(block),
    'the runs array must stay guarded or a response without it throws unhandled');
});
