'use strict';
/**
 * Where backups live on disk, and which ones stop living there.
 *
 * ── A pair is the unit ─────────────────────────────────────────────────────
 *
 *     /data/config-backups/<router-slug>/
 *         2026-08-19T203521.rsc.gz      gzipped export — diffable, no secrets
 *         2026-08-19T203521.backup      aes-sha256 binary — restorable
 *
 * The two files share a stem and are always created, removed and counted
 * together. A `.rsc` without its `.backup` is a diff you cannot act on; a
 * `.backup` without its `.rsc` is a restore point you cannot inspect. Neither
 * half is useful alone, so neither is kept alone.
 *
 * `/export` masks private keys (verified against a live AX3: only WireGuard
 * *public* keys appear), so the `.rsc` carries no secrets and is stored
 * plainly. It still describes the whole network, which is why reading one
 * needs page write rather than page read.
 *
 * ── The directory name, and why the database does not trust it ─────────────
 *
 * The directory is named from the router's label because a human browsing
 * `/data` should be able to tell whose backups these are. Labels change, so
 * the *database* records the directory that was used; nothing resolves an old
 * backup by re-deriving a slug from the current label. Renaming a router
 * starts a new directory and leaves the old one findable.
 */

const fs = require('fs');
const path = require('path');
const zlib = require('zlib');

/**
 * Resolved per call, not frozen at require time.
 *
 * A constant here meant the test suite wrote real backup pairs into the real
 * /data/config-backups — it left a `test-router` directory on a production
 * instance, which is how this was found. Reading the environment when the path
 * is actually needed lets a test point DATA_DIR somewhere disposable.
 */
function baseDir() {
  return path.join(process.env.DATA_DIR || '/data', 'config-backups');
}

/** Anything outside this is replaced, so a label can never escape the base directory. */
const _UNSAFE = /[^a-z0-9]+/g;

/**
 * A directory name from a router label.
 *
 * Deliberately lossy: lowercase, dashes, nothing else. A label of only
 * punctuation would slug to nothing, so it falls back to a fixed name rather
 * than producing '' and writing into the base directory itself.
 */
function slugFor(label) {
  const slug = String(label == null ? '' : label)
    .toLowerCase()
    .replace(_UNSAFE, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 60);
  return slug || 'router';
}

/** Absolute path of a router's backup directory. */
function dirFor(slug) {
  return path.join(baseDir(), slug);
}

/**
 * The filename stem for a moment in time, in UTC.
 *
 * UTC because a local-time stem repeats itself for an hour every autumn, and
 * two backups that sort as equal are two backups that can overwrite each
 * other. Seconds are included for the same reason at a smaller scale: a
 * scheduled run and a manual one can land in the same minute.
 */
function stemFor(ts) {
  const d = new Date(ts);
  const p = (n, w) => String(n).padStart(w || 2, '0');
  return d.getUTCFullYear() + '-' + p(d.getUTCMonth() + 1) + '-' + p(d.getUTCDate()) +
         'T' + p(d.getUTCHours()) + p(d.getUTCMinutes()) + p(d.getUTCSeconds());
}

function rscPath(dir, stem)    { return path.join(dir, stem + '.rsc.gz'); }
function backupPath(dir, stem) { return path.join(dir, stem + '.backup'); }

function ensureDir(dir) {
  fs.mkdirSync(dir, { recursive: true });
}

/**
 * Write both halves of a pair.
 *
 * The binary lands first. If the process dies between the two writes, what
 * survives is a `.backup` with no `.rsc` — and `listPairs()` ignores it,
 * because a half-written pair is not a backup. The other order would leave a
 * diffable export that claims a restore point exists.
 */
function writePair(dir, stem, rscText, backupBuf) {
  ensureDir(dir);
  const gz = zlib.gzipSync(Buffer.from(rscText, 'utf8'));
  fs.writeFileSync(backupPath(dir, stem), backupBuf);
  fs.writeFileSync(rscPath(dir, stem), gz);
  return {
    rscPath: rscPath(dir, stem),
    backupPath: backupPath(dir, stem),
    rscBytes: gz.length,
    backupBytes: backupBuf.length,
  };
}

/** The stored export, gunzipped. */
function readRsc(dir, stem) {
  return zlib.gunzipSync(fs.readFileSync(rscPath(dir, stem))).toString('utf8');
}

/** The stored binary, as it will be pushed back to the router. */
function readBackup(dir, stem) {
  return fs.readFileSync(backupPath(dir, stem));
}

function hasPair(dir, stem) {
  return fs.existsSync(rscPath(dir, stem)) && fs.existsSync(backupPath(dir, stem));
}

/**
 * Remove a pair. Missing halves are not an error — pruning has to be
 * idempotent, because it runs after a crash as readily as after a success.
 */
function removePair(dir, stem) {
  let removed = 0;
  for (const p of [rscPath(dir, stem), backupPath(dir, stem)]) {
    try { fs.unlinkSync(p); removed++; }
    catch (e) { if (e.code !== 'ENOENT') throw e; }
  }
  return removed;
}

/**
 * Every complete pair in a directory, newest first.
 *
 * Sorted by stem, which sorts chronologically because the stem is a fixed-width
 * UTC timestamp — no parsing, and no dependence on filesystem mtime, which a
 * copy or a restore from a host backup would rewrite.
 */
function listPairs(dir) {
  let names;
  try { names = fs.readdirSync(dir); }
  catch (e) { if (e.code === 'ENOENT') return []; throw e; }

  const stems = names
    .filter(n => n.endsWith('.rsc.gz'))
    .map(n => n.slice(0, -'.rsc.gz'.length))
    .filter(stem => names.includes(stem + '.backup'));

  return stems.sort().reverse().map(stem => {
    const stat = (p) => { try { return fs.statSync(p).size; } catch (_) { return 0; } };
    return {
      stem,
      rscBytes: stat(rscPath(dir, stem)),
      backupBytes: stat(backupPath(dir, stem)),
    };
  });
}

/** '2026-08-19T203521' back to epoch ms. Returns NaN for anything else. */
function _stemToMs(stem) {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2})(\d{2})(\d{2})$/.exec(String(stem));
  if (!m) return NaN;
  return Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]);
}

/**
 * Which pairs retention should remove.
 *
 * Pure, so the rule can be tested without a filesystem — and so a preview can
 * never disagree with what the sweep actually deletes.
 *
 * Both limits apply and the stricter one wins, because they answer different
 * questions: keepCount bounds disk, keepDays bounds relevance. A zero or
 * missing limit means that limit is not applied at all.
 *
 * **The newest pair is never removed.** A router whose configuration has been
 * stable for longer than keepDays would otherwise age out its only restore
 * point precisely because nothing has gone wrong — the case where losing it
 * matters most. Pairs are written only when the configuration changed, so the
 * newest one is the current configuration however old it is.
 */
function selectForPruning(pairs, opts, now) {
  const keepCount = Number(opts && opts.keepCount) || 0;
  const keepDays = Number(opts && opts.keepDays) || 0;
  const sorted = pairs.slice().sort((a, b) => (a.stem < b.stem ? 1 : a.stem > b.stem ? -1 : 0));
  if (sorted.length <= 1) return [];

  const doomed = new Set();
  if (keepCount > 0) for (const p of sorted.slice(keepCount)) doomed.add(p.stem);
  if (keepDays > 0) {
    const cutoff = (now == null ? Date.now() : now) - keepDays * 86400000;
    for (const p of sorted) if (_stemToMs(p.stem) < cutoff) doomed.add(p.stem);
  }
  doomed.delete(sorted[0].stem);
  return sorted.filter(p => doomed.has(p.stem)).map(p => p.stem);
}

/** Total bytes a router's backups occupy, for the page's footer. */
function usageOf(dir) {
  return listPairs(dir).reduce((sum, p) => sum + p.rscBytes + p.backupBytes, 0);
}

module.exports = {
  baseDir,
  slugFor,
  dirFor,
  stemFor,
  rscPath,
  backupPath,
  ensureDir,
  writePair,
  readRsc,
  readBackup,
  hasPair,
  removePair,
  listPairs,
  selectForPruning,
  usageOf,
  _stemToMs,
};
