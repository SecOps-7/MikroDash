'use strict';
/**
 * The IP tooltip, live against ported.
 *
 * Small, and worth a gate anyway: it is three document-level listeners, and a
 * listener that is never registered is exactly the failure this port has hit
 * four times — the markup renders, the control looks live, nothing happens.
 * So the harness REGISTERS through a fake document and then DISPATCHES, rather
 * than calling the handlers directly: a port that forgot `addEventListener`
 * fails here instead of passing on a function nobody would ever call.
 *
 * The `mouseleave` registration is compared WITH ITS CAPTURE FLAG. mouseleave
 * does not bubble, so a listener on `document` in the bubble phase never fires
 * and the tooltip stays on screen after the pointer leaves the window — a
 * difference invisible in any snapshot of the tooltip itself.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/iptip-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('iptip-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// THE LIFT AND ITS SLICE CHECKS, ONLY WHERE THERE IS A SOURCE. Both the section
// anchor and the five `must` strings ask the live source a question. `tipSrc` and
// `escSrc` feed `liveRunner`, which from here on runs only inside a frozen
// closure.
let tipSrc = '', escSrc = '';
if (LIFT.hasReference(ROOT)) {
  const from = src.indexOf('// ── IP tooltip ');
  if (from === -1) throw new Error('cannot find the IP tooltip section');
  const open = src.indexOf('(function(){', from);
  let depth = 0, end = -1;
  for (let i = src.indexOf('{', open); i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (!depth) { end = i; break; } }
  }
  tipSrc = src.slice(src.indexOf('{', open) + 1, end);
  for (const must of ['ip-tip', 'mouseover', 'mousemove', 'mouseleave', 'dataset.ip']) {
    assert.ok(tipSrc.includes(must), 'the tooltip slice lost ' + must);
  }
  const i = src.indexOf('function esc(');
  escSrc = src.slice(i, src.indexOf('\n}', i) + 2);
}

const ENTRY = path.join(ROOT, 'testdata', '.iptip-entry.ts');
fs.writeFileSync(ENTRY, "export { initIpTip } from '../web/src/iptip.js';\n");
const OUT = path.join(ROOT, 'testdata', '.iptip-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function makeDoc() {
  const listeners = [];
  const appended = [];
  const mk = () => ({
    className: '', style: {}, dataset: {},
    set innerHTML(v) { this._h = v; }, get innerHTML() { return this._h || ''; },
  });
  const doc = {
    createElement: () => mk(),
    body: { appendChild: (n) => { appended.push(n); return n; } },
    addEventListener: (type, fn, capture) => listeners.push({ type, fn, capture: !!capture }),
  };
  return { doc, listeners, appended, mk };
}

function fire(h, type, ev) {
  for (const l of h.listeners) if (l.type === type) l.fn(ev);
}
function snapshot(h) {
  const tip = h.appended[0];
  return JSON.stringify({
    appended: h.appended.length,
    className: tip ? tip.className : null,
    html: tip ? tip.innerHTML : null,
    display: tip ? tip.style.display : null,
    transform: tip ? tip.style.transform : null,
    listeners: h.listeners.map((l) => l.type + (l.capture ? ':capture' : '')),
  });
}

function liveRunner() {
  const h = makeDoc();
  const ctx = { String, document: h.doc };
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\n(function(){' + tipSrc + '})();', ctx);
  return h;
}
function portRunner() {
  const h = makeDoc();
  globalThis.document = h.doc;
  delete require.cache[require.resolve(OUT)];
  require(OUT).initIpTip();
  return h;
}

// A hovered element, as the card renders it.
const hit = (ds) => {
  const node = { dataset: ds };
  return { target: { closest: (sel) => (sel === '.has-ip-tip' ? node : null) }, clientX: 100, clientY: 200 };
};
const miss = (x, y) => ({ target: { closest: () => null }, clientX: x || 0, clientY: y || 0 });

const CASES = [
  ['a destination with org and cat', [['mouseover', hit({ ip: '198.51.100.7', org: 'Amazon', cat: 'cloud' })]]],
  ['an org with no cat falls back to svc-other', [['mouseover', hit({ ip: '198.51.100.7', org: 'Akamai', cat: '' })]]],
  ['no org: just the address', [['mouseover', hit({ ip: '198.51.100.7', org: '', cat: '' })]]],
  ['NO IP: hidden, and no markup written', [['mouseover', hit({ ip: '', org: 'Amazon', cat: 'cloud' })]]],
  ['markup in the values is escaped', [['mouseover', hit({ ip: '<img src=x>', org: 'A&B', cat: '"q"' })]]],
  ['hovering nothing hides it', [
    ['mouseover', hit({ ip: '198.51.100.7', org: 'Amazon', cat: 'cloud' })],
    ['mouseover', miss()],
  ]],
  ['a target with no closest at all', [['mouseover', { target: {}, clientX: 1, clientY: 2 }]]],
  ['move follows the pointer while shown', [
    ['mouseover', hit({ ip: '198.51.100.7', org: '', cat: '' })],
    ['mousemove', miss(300, 400)],
  ]],
  ['move does NOTHING while hidden', [
    ['mousemove', miss(300, 400)],
  ]],
  ['move does nothing after being hidden again', [
    ['mouseover', hit({ ip: '198.51.100.7', org: '', cat: '' })],
    ['mouseover', miss()],
    ['mousemove', miss(500, 600)],
  ]],
  ['leaving the window hides it', [
    ['mouseover', hit({ ip: '198.51.100.7', org: '', cat: '' })],
    ['mouseleave', miss()],
  ]],
];

let bad = 0, steps = 0;
for (const [name, script] of CASES) {
  // THE WHOLE LIVE RUN AS ONE ORDERED SEQUENCE. `fire` drives the live tooltip
  // step by step outside the comparison, so freezing the snapshot alone would
  // leave the driver running on replay. Recipe 3i.
  const liveSnaps = G.value(name + ' live run', () => {
    const h = liveRunner();
    return script.map(([type, ev]) => { fire(h, type, ev); return snapshot(h); });
  });
  const port = portRunner();
  script.forEach(([type, ev], i) => {
    fire(port, type, ev);
    steps++;
    const a = liveSnaps[i], b = snapshot(port);
    if (a === b) return;
    bad++;
    if (bad <= 3) console.error('\nDIFF %s — after %s (step %d)\n  live: %s\n  port: %s', name, type, i + 1, a, b);
  });
}

// BELIEVABILITY, RE-AIMED AT THE PORT: a tooltip really is built and shown.
// It asked this of the live side, which is the half that stops existing rather
// than the half that has to keep building one.
{
  const h = portRunner();
  fire(h, 'mouseover', hit({ ip: '198.51.100.7', org: 'Amazon', cat: 'cloud' }));
  const s = JSON.parse(snapshot(h));
  assert.equal(s.appended, 1, 'the tooltip was never appended to the body');
  assert.equal(s.className, 'ip-tip', 'the tooltip lost its class');
  assert.equal(s.display, 'block', 'the tooltip never showed');
  assert.match(s.html, /ip-tip-org/, 'the tooltip rendered no org');
  assert.ok(s.listeners.includes('mouseleave:capture'),
    'the mouseleave is registered in the CAPTURE phase — if this ever stops being ' +
    'true the note in iptip.ts is wrong');
}

fs.rmSync(OUT, { force: true });
if (bad) { console.error('\n%d of %d steps differ', bad, steps); process.exit(1); }
console.log('iptip-check: %d cases, %d steps identical', CASES.length, steps);
