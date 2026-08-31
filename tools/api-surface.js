'use strict';
/**
 * Every RouterOS command this codebase issues, derived from the source.
 *
 * The first artefact of the port (plan A1): before hardware behaviour can be
 * captured as fixtures, the set of things to capture has to be known. It is also
 * the specification for the Go client — the API surface it must cover — and the
 * checklist for "have we ported this collector yet".
 *
 * GENERATED, NEVER HAND-EDITED. A hand-written list of 135 command paths would
 * be the fourteenth mirror in a codebase that already pays 13 drift tests to
 * police the other thirteen. Run it; do not maintain its output.
 *
 *   node tools/api-surface.js            # rewrite docs/routeros-api-surface.md
 *   node tools/api-surface.js --check    # fail if the doc is stale (for CI)
 *
 * WHAT IT CANNOT SEE. Command paths built at runtime — `resource.menu + '/set'`
 * in the resource engine, `_QUEUE_MENUS[menu]`, `_WAN_VERBS[verb]` — are read
 * from the registry that supplies them where possible, and otherwise listed as
 * bare menus. A generator that silently dropped those would be worse than no
 * generator, so they are reported rather than omitted.
 */

const fs   = require('node:fs');
const path = require('node:path');

// This tool lives in the PORT repo and reads the LIVE one. The live repo stays
// untouched — nothing here opens it for writing, and no port artefact is written
// into it. Override with MIKRODASH_SRC where the layout differs (in the app
// container the source is at /app).
// path.resolve against the CWD, NOT used as given. require() resolves a relative
// path against the REQUIRING MODULE's directory, so a bare `../MikroDash` here
// means MikroDash_New/tools/../MikroDash — a directory that does not exist, and
// every require() below throws MODULE_NOT_FOUND. This is the same trap that
// silently broke nodecheck/helpers/fixture-replay.js after the move; it was
// fixed there and left here, so `--check` had stopped gating anything at all.
const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = path.join(__dirname, '..', 'docs', 'routeros-api-surface.md');

// The RouterOS top-level menus. Anchoring on these keeps ordinary strings —
// URL paths, file paths, socket event names — out of the results.
const MENUS = ['interface', 'ip', 'ipv6', 'system', 'tool', 'queue', 'routing',
               'ppp', 'caps-man', 'user', 'file', 'log', 'certificate', 'snmp',
               'console', 'partitions', 'disk'];

const CMD_RE  = new RegExp("'(/(?:" + MENUS.join('|') + ")[a-z0-9/._-]*)'", 'g');
// A proplist literal, INCLUDING one built by concatenating adjacent strings.
//
// `/'(=\.proplist=[^']*)'/` captured only the first literal, so a proplist
// split across two lines — which src/collectors/dns.js now does, to fit the
// fields MX, NS and SRV records need — was silently reported truncated at the
// join. A surface that under-reports is worse than one that is merely stale:
// CLAUDE.md's rule is that a stale surface means the port checklist is wrong,
// and a truncated one means the checklist is wrong while claiming to be current.
//
// The trailing group swallows any number of ` + 'more'` continuations, and the
// concatenation is undone below so the recorded value is the string RouterOS
// would actually receive.
const PROP_RE = /'(=\.proplist=[^']*)'((?:\s*\+\s*'[^']*')*)/g;

/** Re-join `'a' + 'b'` into `ab`, matching what the collector actually sends. */
function joinContinuations(head, tail) {
  if (!tail) return head;
  return head + [...tail.matchAll(/'([^']*)'/g)].map(m => m[1]).join('');
}

/** print/listen/monitor read; set/add/remove/move/enable/disable write. */
function classify(cmdPath) {
  const tail = cmdPath.split('/').pop();
  if (['print', 'getall', 'get', 'find', 'export', 'monitor'].includes(tail)) return 'read';
  if (tail === 'listen') return 'stream';
  if (['set', 'add', 'remove', 'move', 'enable', 'disable', 'unset', 'reset',
       'comment'].includes(tail)) return 'write';
  // Verbs that act rather than read: /tool/ping, /system/reboot, /file/read…
  if (['ping', 'reboot', 'shutdown', 'read', 'load', 'save', 'install',
       'check-for-updates', 'apply-changes', 'upgrade', 'scan', 'frequency-scan',
       'provision', 'refresh', 'reset-counters', 'renew', 'release',
       'make-static', 'fetch'].includes(tail)) return 'action';
  return 'menu';                       // a bare menu path, composed with a verb
}

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.js')) out.push(p);
  }
  return out;
}

function collect() {
  const files = walk(path.join(ROOT, 'src')).concat([path.join(ROOT, 'patch-routeros.js')]);
  const cmds  = new Map();              // path -> { kind, files:Set }
  const props = new Map();              // proplist -> Set(files)

  for (const file of files) {
    const rel = path.relative(ROOT, file);
    const src = fs.readFileSync(file, 'utf8');
    for (const m of src.matchAll(CMD_RE)) {
      const p = m[1];
      if (!cmds.has(p)) cmds.set(p, { kind: classify(p), files: new Set() });
      cmds.get(p).files.add(rel);
    }
    for (const m of src.matchAll(PROP_RE)) {
      const full = joinContinuations(m[1], m[2]);
      if (!props.has(full)) props.set(full, new Set());
      props.get(full).add(rel);
    }
  }
  return { cmds, props };
}

/**
 * Menus the resource engine composes verbs onto at runtime, read from the
 * registry rather than from a string literal — so the surface stays complete.
 */
function resourceMenus() {
  const { RESOURCES } = require(path.join(ROOT, 'src', 'routeros', 'resources.js'));
  return RESOURCES.map(r => ({
    menu: r.menu, key: r.key, page: r.page,
    verbs: ['add', 'set', 'remove'].concat(r.ordered ? ['move'] : [])
      .concat((r.actions || []).map(a => a.verb)),
  })).sort((a, b) => a.menu.localeCompare(b.menu) || a.key.localeCompare(b.key));
}

function render() {
  const { cmds, props } = collect();
  const res = resourceMenus();
  const byKind = (k) => [...cmds.entries()].filter(([, v]) => v.kind === k)
    .sort(([a], [b]) => a.localeCompare(b));

  const L = [];
  L.push('# RouterOS API surface');
  L.push('');
  L.push('**Generated by `node tools/api-surface.js` — do not edit.**');
  L.push('');
  L.push('Every RouterOS command MikroDash issues, derived from the source. This is the input list');
  L.push('for the fixture capture (plan A1), the specification for the Go client, and the checklist');
  L.push('for what a ported collector has to cover.');
  L.push('');
  L.push('| Kind | Count |');
  L.push('|---|---|');
  for (const k of ['read', 'stream', 'write', 'action', 'menu'])
    L.push('| ' + k + ' | ' + byKind(k).length + ' |');
  L.push('| distinct proplists | ' + props.size + ' |');
  L.push('');

  const TITLES = {
    read:   'Reads',
    stream: 'Streams (`/listen`)',
    write:  'Writes issued from a literal path',
    action: 'Actions',
    menu:   'Bare menus (a verb is appended at runtime)',
  };
  for (const kind of ['read', 'stream', 'action', 'write', 'menu']) {
    const rows = byKind(kind);
    if (!rows.length) continue;
    L.push('## ' + TITLES[kind]);
    L.push('');
    L.push('| Command | Used by |');
    L.push('|---|---|');
    for (const [p, v] of rows)
      L.push('| `' + p + '` | ' + [...v.files].sort().join(', ') + ' |');
    L.push('');
  }

  L.push('## Composed at runtime — the resource engine');
  L.push('');
  L.push('These menus never appear as a complete command in the source: `res:save` and friends');
  L.push('build `<menu>/<verb>` from the registry. Listed from `src/routeros/resources.js` so the');
  L.push('surface stays complete.');
  L.push('');
  L.push('| Menu | Resource | Page | Verbs |');
  L.push('|---|---|---|---|');
  for (const r of res)
    L.push('| `' + r.menu + '` | ' + r.key + ' | ' + r.page + ' | ' +
           [...new Set(r.verbs)].join(', ') + ' |');
  L.push('');

  L.push('## Proplists');
  L.push('');
  L.push('A proplist is the only thing keeping a credential out of a payload — see');
  L.push('`src/routeros/wifiMenus.js`. Every one of these is part of the port contract.');
  L.push('');
  L.push('| Proplist | Used by |');
  L.push('|---|---|');
  for (const [p, files] of [...props.entries()].sort(([a], [b]) => a.localeCompare(b)))
    L.push('| `' + p + '` | ' + [...files].sort().join(', ') + ' |');
  L.push('');
  return L.join('\n');
}

// Importable as well as runnable: tools/capture-fixtures.js needs the same scan,
// and a second copy of it would be the exact mistake this file exists to avoid.
module.exports = { collect, classify, resourceMenus, render };

if (require.main !== module) return;

const out = render();
if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (cur !== out) {
    console.error('docs/routeros-api-surface.md is stale — run: node tools/api-surface.js');
    process.exit(1);
  }
  console.log('docs/routeros-api-surface.md is up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log('wrote ' + path.relative(ROOT, OUT));
}
