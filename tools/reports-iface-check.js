#!/usr/bin/env node
'use strict';
/**
 * The Reports page's interface selector, live against ported.
 *
 * ── REPRODUCED FROM ASSERTED SOURCE, NOT LIFTED ─────────────────────────────
 *
 * The live version is INLINE inside a fetch handler (`app.js` ~10472) rather
 * than a function, so there is nothing to lift. Its four lines are reproduced
 * here and each is asserted to still be present, so a change over there breaks
 * this gate rather than drifting past it. That is weaker than lifting and is
 * said out loud for that reason.
 *
 * ── THE RULE ────────────────────────────────────────────────────────────────
 *
 * Pressing Load re-fetches the interface list. Without the preservation step the
 * chosen interface snaps back to the first one on every load, so a report on
 * `ether5` silently becomes a report on `bridge` when the operator changes the
 * date range — a wrong answer with nothing on screen to say so.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/reports-iface-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('reports-iface-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const COVERS = ['rptTrafficIface'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

// The four lines that ARE the behaviour. Asserted individually so a partial
// change over there cannot pass silently.
const LINES = [
  'var curIface = rptTrafficIface.value;',
  'rptTrafficIface.innerHTML = ifaces.map(function(i){',
  "return '<option value=\"'+esc(i)+'\">'+esc(i)+'</option>';",
  'if (curIface && ifaces.indexOf(curIface)!==-1) rptTrafficIface.value = curIface;',
  'var iface = rptTrafficIface ? rptTrafficIface.value : (ifaces[0]||\'\');',
];
// GUARDED: each asks the live SOURCE whether it still contains a line.
if (LIFT.hasReference(ROOT)) {
  for (const l of LINES) {
    assert.ok(src.includes(l), 'the live interface selector has changed: missing ' + l);
  }
}

// FROZEN: the live `esc` definition, lifted rather than retyped. `liveFill` needs
// it, and it is one line of the live source rather than a behaviour.
const escLine = G.value('the live esc() definition', () => {
  const t = src.slice(src.indexOf('function esc('));
  return t.slice(0, t.indexOf('\n'));
});
assert.ok(/^function esc\(/.test(escLine), 'the recorded esc() definition is not one');
const esc = new Function(escLine + '\n return esc;')();

const ENTRY = path.join(ROOT, 'testdata', '.rptif-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.rptif-port.cjs');
fs.writeFileSync(ENTRY, "export { fillIfaceSelect } from '../web/src/pages/reports';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const { fillIfaceSelect } = require(OUT);

function liveFill(current, ifaces) {
  const doc = makeDoc(['rptTrafficIface']);
  const sel = doc.nodes.rptTrafficIface;
  sel.value = current;
  const curIface = sel.value;
  sel.innerHTML = ifaces.map((i) => '<option value="' + esc(i) + '">' + esc(i) + '</option>').join('');
  if (curIface && ifaces.indexOf(curIface) !== -1) sel.value = curIface;
  const iface = sel ? sel.value : (ifaces[0] || '');
  return { html: sel.innerHTML, value: sel.value, returned: iface };
}

function portFill(current, ifaces) {
  const doc = makeDoc(['rptTrafficIface']);
  doc.nodes.rptTrafficIface.value = current;
  const prev = global.document;
  global.document = doc;
  try {
    const returned = fillIfaceSelect('rptTrafficIface', ifaces);
    return { html: doc.nodes.rptTrafficIface.innerHTML, value: doc.nodes.rptTrafficIface.value, returned };
  } finally {
    if (prev === undefined) delete global.document; else global.document = prev;
  }
}

const CASES = {
  'nothing chosen, three interfaces': ['', ['ether1', 'ether2', 'bridge']],
  'the CHOICE SURVIVES a reload': ['ether2', ['ether1', 'ether2', 'bridge']],
  'the choice is GONE — it cannot be preserved': ['ether9', ['ether1', 'ether2']],
  'the choice is the first one': ['ether1', ['ether1', 'ether2']],
  'the choice is the last one': ['bridge', ['ether1', 'bridge']],
  'an empty interface list': ['ether1', []],
  'an empty list with nothing chosen': ['', []],
  'one interface': ['', ['ether1']],
  'markup in an interface name': ['', ['<img src=x>']],
  'a quote in an interface name': ['', ['a"b']],
  'a chosen name containing markup survives': ['<img src=x>', ['<img src=x>', 'ether1']],
  'names that differ only by case are distinct': ['Ether1', ['ether1', 'Ether1']],
};

let bad = 0, checked = 0;
for (const [name, [cur, ifaces]] of Object.entries(CASES)) {
  checked++;
  const a = G.live(name, () => liveFill(cur, ifaces));
  const b = portFill(cur, ifaces);
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    bad++;
    console.error('%s\n  live: %j\n  port: %j', name, a, b);
  }
}

// BELIEVABILITY, RE-AIMED AT THE PORT: preservation must actually differ from
// not-preserving, or the rule this gate exists for is untested.
//
// It asked this of the LIVE side, which is the half that stops existing and not
// the half that has to keep preserving a surviving choice. Asked of the port it
// is the same property, it outlives the reference, and it checks the code that
// ships.
const kept = portFill('ether2', ['ether1', 'ether2']);
const lost = portFill('ether9', ['ether1', 'ether2']);
assert.strictEqual(kept.value, 'ether2', 'the surviving choice was not preserved');
assert.notStrictEqual(lost.value, 'ether9', 'a vanished interface was somehow preserved');
assert.notStrictEqual(kept.value, lost.value, 'the two cases are indistinguishable');

if (bad) {
  shout('\nreports-iface-check: %d of %d differ', bad, checked);
  process.exit(1);
}
say('reports-iface-check: %d cases identical', checked);
