'use strict';
/**
 * The Dashboard's Netwatch card, live against ported, by DOM equality.
 *
 * ── THE THIRD STATE IS THE POINT ────────────────────────────────────────────
 *
 * RouterOS answers `up` or `down`, and a host it has not probed yet answers
 * neither. Folding that into "Down" would invent an outage on every page load
 * until the first probe lands, so the card shows the raw value — escaped,
 * because it comes off the router.
 *
 * The corpus therefore carries every status shape the field can take: the two
 * known ones, an unknown word, an empty string, a missing key, and one that is
 * markup.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/netwatch-render-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/netwatch-render-check.js --freeze
const G = L.golden('netwatch-render-check');
const src = L.liveSource(ROOT);

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
const handlerSrc = slice("socket.on('netwatch:update', function(data) {", '\n});', 'the netwatch handler');
const escSrc = slice('function esc(', '\n}', 'esc');

const ENTRY = path.join(ROOT, 'testdata', '.netwatch-entry.ts');
fs.writeFileSync(ENTRY, "export { renderNetwatch } from '../web/src/pages/dashboard-netwatch.js';\n");
const OUT = path.join(ROOT, 'testdata', '.netwatch-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function liveRun(payload) {
  const tbody = { innerHTML: '' };
  const handlers = {};
  const ctx = {
    String, Array, JSON,
    socket: { on: (n, f) => { handlers[n] = f; } },
    $: (id) => (id === 'netwatchTable' ? tbody : null),
  };
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\n' + handlerSrc, ctx);
  handlers['netwatch:update'](payload);
  return tbody.innerHTML;
}

function portRun(payload) {
  const tbody = { innerHTML: '' };
  const saved = global.document;
  global.document = { getElementById: (id) => (id === 'netwatchTable' ? tbody : null) };
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).renderNetwatch(payload);
  } finally {
    if (saved === undefined) delete global.document; else global.document = saved;
  }
  return tbody.innerHTML;
}

const bad = [];
let cases = 0;
function compare(what, payload) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(payload)), b = portRun(payload);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

const H = (status, name, host) => ({ status, name, host });

compare('one host up', { hosts: [H('up', 'gateway', '10.0.0.1')] });
compare('one host down', { hosts: [H('down', 'nas', '10.0.0.9')] });
compare('a mix', { hosts: [H('up', 'a', '1.1.1.1'), H('down', 'b', '2.2.2.2'), H('unknown', 'c', '3.3.3.3')] });
// The third state, in every spelling the field can take.
compare('an unknown status word', { hosts: [H('unknown', 'x', 'h')] });
compare('an EMPTY status', { hosts: [H('', 'x', 'h')] });
compare('a missing status key', { hosts: [{ name: 'x', host: 'h' }] });
compare('a status of null', { hosts: [H(null, 'x', 'h')] });
compare('a status that is markup', { hosts: [H('<b>up</b>', 'x', 'h')] });
compare('a status that differs only in case', { hosts: [H('Up', 'x', 'h')] });
compare('a status with surrounding space', { hosts: [H(' up ', 'x', 'h')] });
// Missing name and host.
compare('no name', { hosts: [H('up', undefined, 'h')] });
compare('no host', { hosts: [H('up', 'x', undefined)] });
compare('neither', { hosts: [H('up', undefined, undefined)] });
compare('an empty name and host', { hosts: [H('up', '', '')] });
// Names and addresses are operator-supplied.
compare('a name that is markup', { hosts: [H('up', '<script>x</script>', 'h')] });
compare('a host with quotes', { hosts: [H('up', 'x', '"quoted"')] });
// Empty.
compare('no hosts', { hosts: [] });
compare('no hosts key', {});
compare('hosts null', { hosts: null });

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the Netwatch card differs from the live one:\n\n' + bad.slice(0, 2).join('\n\n') + '\n');
  process.exit(1);
}
console.log('the Netwatch card matches the live one (' + cases + ' cases: the three states, ' +
  'missing fields and markup in operator-supplied values)');
