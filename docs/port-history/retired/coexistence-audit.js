'use strict';
/**
 * Which `/data` files does this port WRITE that the running Node app CACHES?
 *
 * ── WRITTEN AFTER FINDING THE SAME HAZARD TWICE ─────────────────────────────
 *
 * `src/settings.js:load()` is `if (_cache) return _cache;` — no watcher, no
 * mtime check — and `save()` replaces that cache from its own in-memory copy. So
 * a write from this port is invisible to the running Node process AND is
 * silently reverted by its next save. That has been a recorded cutover blocker
 * for weeks.
 *
 * On 2026-08-26 the identical pattern turned up in `src/routers.js`, which this
 * port had been writing all session and which nobody had written it down for.
 * The pattern is mechanical and so is finding it; this audit does that instead
 * of waiting for a third.
 *
 * ── WHAT IT CHECKS ──────────────────────────────────────────────────────────
 *
 * For every live module holding a module-level cache returned unconditionally
 * (`if (_cache) return _cache;`), it works out which `/data` file that cache
 * backs, and fails if this port WRITES that file without a recorded entry
 * saying so. The entry has to name the consequence, not merely acknowledge it.
 *
 * ── AND IT FAILS IN BOTH DIRECTIONS ─────────────────────────────────────────
 *
 * An entry for a file this port no longer writes, or for a module that no longer
 * caches, is ALSO a failure. A reason kept past its question is how this repo's
 * `reachable-audit` entry for `pages/routers` stayed wrong for weeks.
 *
 *   node tools/coexistence-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..', '..', '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// ── the recorded blockers ───────────────────────────────────────────────────
//
// Each names WHAT BREAKS, not just that something does. Adding a file here is a
// cutover decision and should read like one.
const RECORDED = {
  'settings.json':
    'src/settings.js:load() caches the whole object at first load and never re-reads it; save() '
    + 'replaces that cache outright. A write from here is invisible to the running Node app and '
    + 'is reverted by its next save. settings.js exports NO invalidation hook at all, so there is '
    + 'no way to tell the running process to reread — this one genuinely waits for cutover. '
    + 'POST /api/settings is deliberately NOT wired.',

  'routers.json':
    'src/routers.js:loadAll() has the IDENTICAL pattern, found 2026-08-26. Affects '
    + 'POST/PUT/DELETE /api/routers, PUT /api/sites/:id/routers and DELETE /api/sites/:id (via '
    + 'store.ClearSite) — all ported, all SERVED by the Go mux rather than proxied, and all '
    + 'unsafe to exercise while Node is running. UNLIKE settings.js it exports invalidateCache(), '
    + 'so a cutover plan HAS an option other than waiting; nothing calls it today and there is no '
    + 'signal handler, so the option is latent rather than available.',

  'users.json':
    'src/users.js:_load() has the same pattern. THIS PORT NOW WRITES THE FILE — SetPassword, for '
    + 'POST /api/account/password — and the hazard is handled by REGISTERING THE ROUTE ONLY IN '
    + 'STANDALONE MODE rather than by not writing: internal/server/server.go puts '
    + 'registerAccountPassword inside the `if s.standalone` block, so it exists only where there '
    + 'is no Node process to be out of step with. '
    + 'THE CONSEQUENCE IF THAT GATE IS LOST is worse than for the two files above, which is why '
    + 'it is spelled out: a password change written while Node runs is invisible to it and '
    + 'reverted by its next save, so the operator is TOLD their password changed and it did not — '
    + 'and they may have already discarded the old one. '
    + 'TestPasswordChangeIsNotServedWhileNodeRuns pins the gate by asserting the route answers '
    + '502 (proxied) when a Node URL is configured.',
};

// ── find the caching modules ────────────────────────────────────────────────
const CACHE_SHAPE = /if\s*\(\s*_cache\s*\)\s*return\s+_cache\s*;/;

const srcDir = path.join(LIVE, 'src');
const modules = fs.readdirSync(srcDir)
  .filter((f) => f.endsWith('.js'))
  .map((f) => ({ name: f, text: fs.readFileSync(path.join(srcDir, f), 'utf8') }))
  .filter((m) => CACHE_SHAPE.test(m.text));

if (modules.length === 0) {
  console.error('coexistence-audit: NO caching module was found. The pattern this audit exists '
    + 'for is `if (_cache) return _cache;` — if it has genuinely gone from every module the '
    + 'RECORDED entries below need deleting, and if the matcher has drifted a clean run here '
    + 'proves nothing.');
  process.exit(1);
}

/** The /data file a module's cache backs, from its own FILE constant. */
function fileFor(m) {
  // e.g. `const ROUTERS_FILE = path.join(DATA_DIR, 'routers.json');`
  const hit = /path\.join\(\s*DATA_DIR\s*,\s*'([a-z._]+\.json)'\s*\)/.exec(m.text);
  return hit ? hit[1] : null;
}

// ── what this port writes ───────────────────────────────────────────────────
//
// Read from the SOURCE rather than listed, so a new writer is noticed.
function portWrites() {
  const out = new Set();
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) { walk(p); continue; }
      if (!e.name.endsWith('.go') || e.name.endsWith('_test.go')) continue;
      const text = fs.readFileSync(p, 'utf8');
      // A direct `writeAtomic(filepath.Join(dir, "x.json"), …)`.
      for (const m of text.matchAll(/writeAtomic\(\s*filepath\.Join\([^,]+,\s*"([a-z._]+\.json)"/g)) {
        out.add(m[1]);
      }
      // `write.go` builds `path` first and then writes it. Catch the pairing, or
      // every indirect write is invisible to this audit.
      if (/writeAtomic\(path,/.test(text)) {
        for (const m of text.matchAll(/path\s*:?=\s*filepath\.Join\([^,]+,\s*"([a-z._]+\.json)"/g)) {
          out.add(m[1]);
        }
      }
      // `routeradd.go` writes through its own tmp + rename.
      if (/os\.WriteFile\(tmp,/.test(text)) {
        for (const m of text.matchAll(/filepath\.Join\(s\.Dir,\s*"([a-z._]+\.json)"/g)) {
          out.add(m[1]);
        }
      }
    }
  };
  walk(path.join(ROOT, 'internal'));
  return out;
}

const written = portWrites();
if (written.size === 0) {
  console.error('coexistence-audit: this port appears to write NO /data file, which cannot be '
    + 'true — store/write.go and store/settings_save.go both do. The matcher has drifted.');
  process.exit(1);
}

const problems = [];
const cached = new Map();

for (const m of modules) {
  const file = fileFor(m);
  if (!file) {
    problems.push(m.name + ' caches unconditionally but this audit cannot tell which /data file '
      + 'it backs -- the DATA_DIR constant has moved, and an unrecognised module is not a safe one');
    continue;
  }
  cached.set(file, m.name);

  if (written.has(file) && !RECORDED[file]) {
    problems.push('THIS PORT WRITES ' + file + ', and ' + m.name + ' caches it with no watcher. '
      + 'The write is invisible to the running Node app and reverted by its next save. Record it '
      + 'as a cutover blocker with its consequence, or stop writing the file.');
  }
}

// The other direction.
for (const file of Object.keys(RECORDED)) {
  if (!cached.has(file)) {
    problems.push(file + ' is recorded as cached by a live module, and no module caches it now -- '
      + 'delete the entry rather than leaving a reason past its question');
  }
  if (!written.has(file)) {
    problems.push(file + ' is recorded as written by this port, and nothing writes it now -- '
      + 'delete the entry');
  }
}

// A module that caches a file this port only READS is fine, and worth SAYING so
// rather than passing silently: it is one write away from being a blocker.
const readOnly = [...cached.keys()].filter((f) => !written.has(f));

if (problems.length) {
  problems.forEach((p) => console.error('  ✗ ' + p));
  console.error('\ncoexistence-audit: ' + problems.length + ' problem(s)');
  process.exit(1);
}
console.log('coexistence-audit: ' + cached.size + ' live module(s) cache a /data file; '
  + Object.keys(RECORDED).length + ' written by this port and recorded'
  + (readOnly.length ? '; read-only here: ' + readOnly.join(', ') : ''));
