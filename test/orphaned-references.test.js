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

// ── Two arrays zipped by index must stay the same length ────────────────────
//
// `routers:stats` sends siteIds and siteNames as parallel arrays and the Devices
// page's site filter pairs them POSITIONALLY. The server built the names with
// `.filter(Boolean)`, which removes an element from the middle of one array and
// nothing from the other, so from the first unresolvable id onward every name
// attached to the wrong site.
//
// An id fails to resolve when a site is deleted while a device still lists it.
// That is a state the app expects to be in: router-store-sites.test.js has a
// case for a dangling membership on the singular field.
//
// The worked example, for a device in ['siteA', 'deleted', 'siteC'] where the
// middle site is gone and the names resolve to ['Depot', 'Annexe']:
//
//     siteA   -> "Depot"    correct
//     deleted -> "Annexe"   WRONG, that is siteC's name, on a dead site
//     siteC   -> "siteC"    the raw id, because nm[2] is undefined
//
// So a live site hides under an opaque id while a deleted one wears a real
// site's name, and picking it filters to the deleted site.
test('the site name array is not compacted away from the id array', () => {
  const INDEX = P('src', 'index.js');
  const at = INDEX.indexOf('siteNames:');
  assert.ok(at > 0, 'siteNames moved or was renamed');
  const line = INDEX.slice(at, INDEX.indexOf('\n', at));

  assert.ok(!/\.filter\(/.test(line),
    'siteNames must not be filtered: it is zipped against siteIds by index, so ' +
    'dropping an entry shifts every later name onto the wrong site');
  assert.match(line, /\|\|\s*''/,
    'an unresolvable id must send an empty placeholder so the arrays stay aligned');
});

test('the client falls back to the id for a name it was not given', () => {
  // The other half of the contract. The placeholder is only safe because the
  // consumer treats a blank as "no name" and shows the raw id instead.
  const at = APP.indexOf('function _syncRoutersSiteFilter');
  assert.ok(at > 0, '_syncRoutersSiteFilter moved or was renamed');
  const body = APP.slice(at, at + 1200);
  assert.match(body, /names\[id\]\s*=\s*nm\[i\]\s*\|\|\s*id/,
    'a blank name must fall back to the site id, not render empty');
});

// ── A function called from outside the IIFE that declares it ────────────────
//
// app.js is a series of top-level IIFEs, so a `function _foo()` inside one is
// invisible to the others. Calling it anyway is a ReferenceError at CALL time,
// not at load, so the file parses, the page renders, and the failure waits for
// whoever clicks the thing.
//
// It has bitten twice. `_syncPrimarySiteSelect` threw every time the device
// modal opened, and was patched by exporting it onto `window`. That export is
// exactly why the second one was missed: `_selectedModalSites`, its sibling in
// the same IIFE, stayed bare — and it sat as the first statement of
// collectModal(), which is the first statement of BOTH the Save and Test
// Connection handlers. Both buttons did nothing, silently, and the operator who
// reported it had no way to tell why.
//
// Neither was caught by any test here, because every other check in this file
// reads the source as text and both calls LOOK fine as text. The scope is the
// thing that is wrong.
test('no function is called from outside the IIFE that declares it', () => {
  const espree = require('espree');
  const ast = espree.parse(APP, { ecmaVersion: 2020, loc: true, range: true });

  // Top-level IIFEs, and the functions each one declares directly.
  const iifes = [];
  for (const node of ast.body) {
    if (node.type !== 'ExpressionStatement') continue;
    let call = node.expression;
    if (call.type === 'UnaryExpression') call = call.argument;
    if (call.type !== 'CallExpression') continue;
    const fn = call.callee;
    if (!fn || (fn.type !== 'FunctionExpression' && fn.type !== 'ArrowFunctionExpression')) continue;
    // EVERY binding anywhere inside the IIFE, not just the ones at its top
    // level. A function declared inside another function is still in scope for
    // its siblings, and collecting only the outer layer reported six such
    // helpers as cross-IIFE calls when they were nothing of the kind.
    const declared = new Set();
    JSON.stringify(fn, (k, v) => {
      if (v && v.type === 'FunctionDeclaration' && v.id) declared.add(v.id.name);
      if (v && v.type === 'VariableDeclarator' && v.id && v.id.type === 'Identifier') declared.add(v.id.name);
      // Parameters bind too: a callback named `cb` is in scope for its own body
      // and is not a reference to some other IIFE's `cb`.
      if (v && v.params) for (const prm of v.params) if (prm.type === 'Identifier') declared.add(prm.name);
      return v;
    });
    iifes.push({ range: node.range, declared });
  }
  assert.ok(iifes.length > 5, 'expected app.js to be built from top-level IIFEs; found ' + iifes.length);

  // Names visible to everyone: declared at module scope, or hung on window.
  const moduleScope = new Set();
  for (const node of ast.body) {
    if (node.type === 'FunctionDeclaration' && node.id) moduleScope.add(node.id.name);
    if (node.type === 'VariableDeclaration') {
      for (const d of node.declarations) if (d.id.type === 'Identifier') moduleScope.add(d.id.name);
    }
  }
  const onWindow = new Set();
  JSON.stringify(ast, (k, v) => {
    if (v && v.type === 'AssignmentExpression' && v.left.type === 'MemberExpression'
        && v.left.object.type === 'Identifier' && v.left.object.name === 'window'
        && v.left.property.type === 'Identifier') onWindow.add(v.left.property.name);
    return v;
  });

  // Browser globals. `confirm` is the one that matters here: it is window.confirm
  // at every call site, and it only looked suspicious because another IIFE
  // happens to bind a local of the same name.
  const GLOBALS = new Set(['confirm', 'alert', 'prompt', 'fetch', 'setTimeout', 'clearTimeout',
    'setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'encodeURIComponent',
    'decodeURIComponent', 'parseInt', 'parseFloat', 'isNaN', 'isFinite', 'String', 'Number', 'Boolean',
    'Array', 'Object', 'Date', 'Math', 'JSON', 'RegExp', 'Error', 'Promise', 'Set', 'Map', 'io', 'structuredClone']);

  const owner = (pos) => iifes.findIndex(i => pos >= i.range[0] && pos < i.range[1]);
  const offenders = [];
  JSON.stringify(ast, (k, v) => {
    if (v && v.type === 'CallExpression' && v.callee && v.callee.type === 'Identifier') {
      const name = v.callee.name;
      if (moduleScope.has(name) || onWindow.has(name) || GLOBALS.has(name)) return v;
      const here = owner(v.range[0]);
      if (here === -1) return v;
      if (iifes[here].declared.has(name)) return v;
      const declaredElsewhere = iifes.some((i, idx) => idx !== here && i.declared.has(name));
      if (declaredElsewhere) offenders.push(name + ' at line ' + v.loc.start.line);
    }
    return v;
  });

  assert.deepEqual(offenders, [],
    'these calls reach a function declared in a DIFFERENT top-level IIFE, which throws ' +
    'ReferenceError when the handler runs rather than when the file loads');
});

// ── The device modal chooses a primary; it does not edit membership ─────────
//
// Which sites a device belongs to decides WHO CAN REACH IT, so it is an
// authorization decision and lives in Access Management behind the
// administrator gate — the same reasoning that made PUT /api/routers/:id strip
// the field for anyone without system:principals. The modal keeps only a
// primary picker, which chooses where the device is drawn on the map.
//
// The risk this pins is specific: a modal that rebuilt siteIds from its own
// picker would silently DROP any site missing from that picker, and a site
// deleted since the device was filed has no name and is deliberately absent
// from it. Reordering the stored list cannot lose one; rebuilding can.
test('the device modal has no membership control', () => {
  assert.ok(!/id="rtrModalSites"/.test(MARKUP),
    'the multi-select is gone; membership is set in Access Management');
  assert.match(MARKUP, /id="rtrModalPrimarySite"/, 'the primary picker stays');
  assert.ok(!/_selectedModalSites|_populateSiteSelect|_syncPrimarySiteSelect/.test(
    APP.replace(/^\s*\/\/.*$/gm, '')),
    'the helpers that drove the removed select must be deleted, not left guarded');
});

test('saving reorders the stored site list rather than rebuilding it', () => {
  const at = APP.indexOf('siteIds:     (function () {');
  assert.ok(at > 0, 'the siteIds builder moved or was renamed');
  const block = APP.slice(at, at + 900);

  // It must read the DEVICE RECORD, not the picker's options.
  assert.match(block, /_routers\.filter/,
    'the list must come from the stored record, so a nameless site is not dropped');
  assert.match(block, /\.concat\(have\.filter/,
    'the primary is moved to the front of the existing list');
  assert.ok(!/rtrModalPrimarySite'\)\.value\s*\]/.test(block),
    'the list must never be built FROM the primary picker alone');

  // An unknown device sends undefined, which the server reads as "leave
  // membership alone". `|| []` here would wipe every site the device is in.
  assert.match(block, /if \(!_rec\) return undefined;/,
    'a device the client has not loaded must not send an empty membership');
});

// ── The release-notes box in the Update dialog ──────────────────────────────
//
// The text in this box is the only content in MikroDash fetched from a third
// party — the router does not carry its own changelog, so it comes from
// mikrotik.com over HTTP (see src/changelog.js). That makes it external
// untrusted input rendered into the DOM, which is exactly what the esc() rule
// in CLAUDE.md exists for.
test('the release notes are escaped before they reach the DOM', () => {
  const at = APP.indexOf('function _setNotes(');
  assert.ok(at > 0, '_setNotes moved or was renamed');
  const body = APP.slice(at, at + 500);
  assert.match(body, /innerHTML\s*=\s*esc\(/,
    'notes come from a third party and must be escaped, not assigned raw');
  assert.ok(!/innerHTML\s*=\s*text\b/.test(body),
    'assigning the fetched text directly is the bug this pins');
});

test('a notes failure never speaks on the upgrade error channel', () => {
  // The dialog renders `packages:error` with code `denied` as "You do not have
  // permission to update this router". A notes lookup that failed for a
  // completely different reason must not produce that sentence: it would be
  // false, and alarming, for an operator who can update perfectly well and
  // merely cannot be shown a changelog.
  const INDEX = P('src', 'index.js');
  const at = INDEX.indexOf("socket.on('packages:notes'");
  assert.ok(at > 0, 'the notes handler moved or was renamed');
  // Bounded at the NEXT handler, not by a character count. The one after this
  // uses _pkgErr legitimately, and a fixed window ran straight into it.
  const nextAt = INDEX.indexOf("socket.on('", at + 20);
  assert.ok(nextAt > at, 'expected another handler after this one');
  // Comments stripped first. The handler's own comment says "NEVER _pkgErr()
  // from here", and prose describing a rule must not fail the test enforcing
  // it — the same trap the PDF glyph scan hit.
  const block = INDEX.slice(at, nextAt).replace(/^\s*\/\/.*$/gm, '');

  assert.ok(!/_pkgErr\(/.test(block),
    'the notes handler must answer on packages:notes, never on packages:error');
  assert.match(block, /packages:notes'?,\s*\{\s*version,\s*error/,
    'a failure still has to reach the browser, on its own channel');
  assert.match(block, /sanitizeErr\(/,
    'raw error text must not reach the browser');
});

test('the notes reply is matched against the version the dialog is showing', () => {
  // A slow reply for a router the operator has since switched away from would
  // otherwise paint the previous router's changelog under the new router's
  // version numbers, which is worse than showing nothing at all.
  const at = APP.indexOf("socket.on('packages:notes'");
  assert.ok(at > 0, 'the notes listener moved or was renamed');
  const body = APP.slice(at, at + 600);
  assert.match(body, /d\.version\s*!==\s*_notesFor/,
    'a reply for a different version must be discarded');
});

test('the notes are requested on open, not on the update-available tick', () => {
  // mikrodash:updateavailable fires on every poll tick. Requesting there would
  // be a fetch per tick for everyone, including people who never open the
  // dialog — the same path whose unconditional rebuild made the update strip
  // flash before _lastUpdateRowHtml was added.
  const at = APP.indexOf("document.addEventListener('mikrodash:updateavailable'");
  assert.ok(at > 0, 'the updateavailable listener moved');
  const body = APP.slice(at, at + 400);
  assert.ok(!/packages:notes/.test(body),
    'the notes must not be fetched from the per-tick path');

  const openAt = APP.indexOf("e.target.closest('#sysUpdateBtn')");
  assert.ok(openAt > 0, 'the open handler moved');
  assert.match(APP.slice(openAt, openAt + 900), /emit\('packages:notes'/,
    'they are requested when the dialog opens');
});

// ── A reconnect must not silently rechoose the operator's interface ─────────
//
// The server keys its traffic subscription on socket.id, so a RECONNECT is a
// new socket and `bindSocket()` re-subscribes it to `defaultIf`. The client
// then receives history for the default and follows it. Net effect: a network
// blip, an idle timeout or a server restart silently moved the operator back to
// the WAN interface, minutes after they picked something else.
//
// Reported on #119 as "it seems to switch to ether2 after some time. Maybe when
// it refreshes interface list" — the list refresh was a coincidence of timing,
// and ether2 was that router's defaultIf. Reproduced in a browser before the
// fix: pick ether2, force one reconnect, land back on WAN1.
//
// The distinction the tests below pin is the whole design: a reconnect is the
// same operator on the same router and their choice should survive it; a ROUTER
// SWITCH is a different fleet of interfaces and carrying a name across would
// either miss or, worse, match something unrelated that shares it.
test('the operator\'s interface choice survives a reconnect', () => {
  assert.match(APP, /var _userPickedIf = '';/,
    'the choice has to live somewhere that connect does not clear');

  // Recorded on the change event, which is the operator acting.
  const at = APP.indexOf("ifaceSelect.addEventListener('change'");
  assert.ok(at > 0, 'the interface picker listener moved');
  assert.match(APP.slice(at, at + 400), /_userPickedIf = ifaceSelect\.value/,
    'an explicit pick is what gets remembered');

  // Restored when history arrives naming something else.
  const h = APP.indexOf("socket.on('traffic:history'");
  assert.ok(h > 0, 'the history handler moved');
  const body = APP.slice(h, h + 1200);
  assert.match(body, /data\.ifName !== _userPickedIf/,
    'a reconnect arrives with the server default; the pick has to be re-requested');
  assert.match(body, /emit\('traffic:select', \{ ifName: _userPickedIf \}\)/,
    'and re-requested through the same path a manual selection uses');
});

test('the restore does not fight the auto-switch when an interface goes down', () => {
  // _rebuildIfaceSelect moves off an interface that has left the list. If the
  // restore ignored that, the two would argue every tick: one moving away from
  // a downed interface, the other dragging back to it. The guard is that the
  // pick must still be selectable.
  const h = APP.indexOf("socket.on('traffic:history'");
  const body = APP.slice(h, h + 1200);
  assert.match(body, /ifaceSelect\.options/,
    'the restore must check the pick is still in the list');

  // And the auto-switch must not overwrite the remembered choice, or one flap
  // would permanently rewrite what the operator asked for.
  //
  // A LEDGER OVER THE WHOLE FILE, not a window around _rebuildIfaceSelect.
  // `_userPickedIf` is a file-level var, so any of app.js's 16,000 lines can
  // assign it — and the window version was worse than merely narrow: it covered
  // characters 137121-138021, where NONE of the three assignments live. It was
  // vacuously true from the moment it was written, and none of the mutations
  // that accompanied it happened to test this rule.
  //
  // Reported by the Go port, whose own version of this rule is structural: its
  // equivalent is module-private and the auto-switch lives in another module, so
  // it cannot reach it at all. We cannot have that here, so the ledger stands in
  // for it: every assignment must be one of the two sanctioned sites.
  const assigns = [...APP.matchAll(/_userPickedIf\s*=/g)].map((m) => {
    const declAt  = APP.lastIndexOf('var _userPickedIf', m.index);
    const isDecl  = declAt === m.index - 4;
    const ownerAt = Math.max(APP.lastIndexOf("socket.on('", m.index),
                             APP.lastIndexOf('addEventListener(', m.index));
    return { isDecl, owner: APP.slice(ownerAt, APP.indexOf('\n', ownerAt)).trim() };
  });

  assert.equal(assigns.length, 3,
    'expected the declaration plus exactly two assignments; a third place writing ' +
    'this variable is a third opinion about what the operator chose');

  const writers = assigns.filter(a => !a.isDecl).map(a => a.owner);
  assert.equal(writers.length, 2, 'expected exactly two writers');
  assert.ok(writers.some(w => /addEventListener\('change'/.test(w)),
    'the operator picking an interface must be one of them');
  assert.ok(writers.some(w => /router:switching/.test(w)),
    'clearing it on a router switch must be the other');
  const stray = writers.filter(w => !/addEventListener\('change'|router:switching/.test(w));
  assert.deepEqual(stray, [],
    'something other than an explicit pick or a router switch writes the remembered ' +
    'choice: ' + stray.join(' | '));
});

test('a router switch clears the choice, a reconnect does not', () => {
  // The counter-case, and the one most likely to be broken by a later tidy-up:
  // somebody adding `_userPickedIf = ''` beside the connect handler's
  // `currentIf = ''` would reintroduce the whole bug.
  // Anchored on WHERE the clear lives, not on a distance from a handler. There
  // are four router:switching handlers in this file and indexOf finds the first,
  // which is not the one that owns the traffic chart — a fixed window made this
  // test fail for a reason that had nothing to do with the rule.
  const clearAt = APP.indexOf("_userPickedIf = ''", APP.indexOf("var _userPickedIf") + 20);
  assert.ok(clearAt > 0, 'nothing clears the remembered choice');
  const ownerAt = APP.lastIndexOf("socket.on('", clearAt);
  const owner   = APP.slice(ownerAt, APP.indexOf('\n', ownerAt));
  assert.match(owner, /router:switching/,
    'the clear belongs to router:switching and nowhere else — a different router ' +
    'has different interfaces, so the name means nothing there. It is currently ' +
    'owned by: ' + owner.trim());

  const cn = APP.indexOf("socket.on('connect',function()");
  assert.ok(cn > 0, 'the connect handler moved');
  const connectBody = APP.slice(cn, cn + 700);
  assert.match(connectBody, /currentIf=''/, 'connect still resets the streaming interface');
  assert.ok(!/_userPickedIf\s*=\s*''/.test(connectBody),
    'clearing the pick on connect is exactly the bug this fixes');
});
