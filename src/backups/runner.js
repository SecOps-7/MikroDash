'use strict';
/**
 * The conversation with the router that produces one backup.
 *
 * ── The transport, and why it is not the obvious one ───────────────────────
 *
 * `/export` over the binary API returns an empty array — it runs, and the text
 * never comes back. `/file/print`'s `contents` is only populated for files of
 * a few KB. `/tool/fetch upload=yes` refuses anything but [s]ftp, so the
 * router cannot POST the file to us either. All three were tried against a
 * live hAP AX3.
 *
 * What works is `/file/read`, in 32768-byte chunks, at about 795 KB/s. It
 * returns raw bytes, so it needs a connection whose receiver does not decode
 * them as UTF-8 — see `_applyRawBytes` in src/routeros/client.js. That is why
 * a backup opens its own short-lived connection instead of borrowing the
 * session's: the decode is a property of the connection, and every collector
 * on the shared one wants text.
 *
 * ── The router's flash is not a scratchpad ─────────────────────────────────
 *
 * Both files are removed in a `finally`, and each run also sweeps anything
 * left by a previous run that died mid-flight — so the worst a failed attempt
 * can do is occupy the flash for the length of one run.
 *
 * How much that is varies enormously: 5.2 MB on a busy AX3, 45.7 KiB on a hAP
 * ac2. Nothing here guesses at it. See the note above the constants.
 */

const crypto = require('crypto');
const Diff = require('./diff');
const Store = require('./store');

/** Names MikroDash creates on the router. Distinctive enough to sweep by prefix. */
const FILE_PREFIX = 'mikrodash-backup-';

/** Read this many bytes per `/file/read`. RouterOS refuses more: `1..32768`. */
const CHUNK = 32768;

/**
 * There is deliberately NO free-space threshold here.
 *
 * There was one — 8 MB, extrapolated from an AX3 whose export and binary came
 * to 5.2 MB. That number described one router's configuration, not a rule: a
 * hAP ac2 produces a 45.7 KiB backup and was refused by a check demanding two
 * hundred times what it needed. Any constant would be wrong for someone,
 * because backup size tracks how much is configured, not what the hardware is.
 *
 * So the router decides. It is the only thing that knows what it has room for,
 * it already refuses when it cannot write, and its refusal names the reason.
 * We attempt the export, report what it says, and sweep in the `finally` —
 * which makes a failed attempt self-healing rather than something that leaves
 * a half-written file behind.
 *
 * Free space is still read, and logged when a run fails, so "no space left"
 * arrives with the number that explains it.
 */

const _sleep = (ms) => new Promise(r => setTimeout(r, ms));

/**
 * A password for the encrypted binary.
 *
 * RouterOS will happily write an UNENCRYPTED backup, and an unencrypted one
 * contains every key on the device in the clear. base64url so it survives an
 * API word without quoting.
 */
function generatePassword() {
  return crypto.randomBytes(24).toString('base64url');
}

/** Wait for a file to appear and stop growing — `/export` returns before it finishes. */
async function _settled(write, name, timeoutMs) {
  const deadline = Date.now() + (timeoutMs || 60000);
  let last = -1, stable = 0;
  while (Date.now() < deadline) {
    const rows = (await write('/file/print', ['=.proplist=name,size'])) || [];
    const f = rows.find(r => r.name === name);
    const size = f ? Number(f.size) : -1;
    if (size > 0 && size === last) {
      if (++stable >= 2) return size;
    } else {
      stable = 0;
    }
    last = size;
    await _sleep(300);
  }
  throw new Error('timed out waiting for ' + name);
}

/**
 * Pull a file off the router whole.
 *
 * `data` arrives as a latin1 string on a rawBytes connection — one code unit
 * per byte — so this is byte-exact for a binary as well as for text. Verified
 * against a live AX3 by pushing a known blob and reading it back to a matching
 * sha256.
 *
 * A short read is an error rather than a shorter file: the length is the only
 * check available, and a truncated backup that restores is worse than one that
 * refuses to.
 */
async function _readFile(write, name, size) {
  const parts = [];
  let off = 0;
  while (off < size) {
    const res = await write('/file/read', ['=file=' + name, '=offset=' + off,
                                           '=chunk-size=' + CHUNK]);
    const row = Array.isArray(res) ? (res[0] || {}) : (res || {});
    if (row.data === undefined) break;
    const buf = Buffer.from(String(row.data), 'latin1');
    if (!buf.length) break;
    parts.push(buf);
    off += buf.length;
  }
  const out = Buffer.concat(parts);
  if (out.length !== size) {
    throw new Error('read ' + out.length + ' of ' + size + ' bytes from ' + name);
  }
  return out;
}

/** Remove everything MikroDash left on the router, including from earlier runs. */
async function _sweep(write, log) {
  let removed = 0;
  try {
    const rows = (await write('/file/print', ['=.proplist=name'])) || [];
    for (const r of rows) {
      if (String(r.name || '').indexOf(FILE_PREFIX) !== 0) continue;
      try { await write('/file/remove', ['=numbers=' + r.name]); removed++; }
      catch (e) { log('could not remove ' + r.name + ': ' + ((e && e.message) || e)); }
    }
  } catch (e) {
    log('could not sweep temp files: ' + ((e && e.message) || e));
  }
  return removed;
}

/** Model, serial and RouterOS version, as the restore guard will compare them. */
async function _identity(write) {
  const out = { model: '', serial: '', osVersion: '', freeBytes: 0, totalBytes: 0 };
  const res = ((await write('/system/resource/print',
    ['=.proplist=board-name,version,free-hdd-space,total-hdd-space'])) || [])[0] || {};
  out.model = res['board-name'] || '';
  out.osVersion = String(res.version || '').split(' ')[0];
  out.freeBytes = Number(res['free-hdd-space'] || 0);
  out.totalBytes = Number(res['total-hdd-space'] || 0);
  try {
    const rb = ((await write('/system/routerboard/print', ['=.proplist=serial-number'])) || [])[0] || {};
    out.serial = rb['serial-number'] || '';
  } catch (_) { /* x86 and CHR have no routerboard; serial stays empty */ }
  return out;
}

/**
 * Take one backup.
 *
 * `connect()` must return a CONNECTED client with `rawBytes` set, plus a
 * `stop()`; the caller owns its lifetime so this stays testable with a fake.
 * `previousFingerprint` decides whether a pair is written at all — one restore
 * point per distinct configuration, not one per timer tick. That is also what
 * makes a daily schedule cheap: an unchanged router costs one export read and
 * no disk.
 *
 * Never throws. Returns an outcome, because "the router was unreachable" is a
 * result worth recording, not an exception to lose.
 */
async function run({ router, connect, previousFingerprint, now, log }) {
  const started = now == null ? Date.now() : now;
  const say = log || (() => {});
  const stem = Store.stemFor(started);
  const base = FILE_PREFIX + stem;
  const result = {
    outcome: 'failed', stem, fingerprint: null, changed: false,
    rscBytes: 0, backupBytes: 0, identity: {}, ms: 0, error: null, dir: null,
  };

  let ros = null;
  try {
    ros = await connect();
    const write = (cmd, args) => ros.write(cmd, args || []);

    const identity = await _identity(write);
    result.identity = { model: identity.model, serial: identity.serial,
                        osVersion: identity.osVersion };
    // Kept off `identity` because that is what gets recorded as the device's
    // identity; free space is a fact about this moment, used only to explain a
    // failure.
    result.freeBytes = identity.freeBytes;

    // Anything left by a run that died before its finally.
    const swept = await _sweep(write, say);
    if (swept) say('swept ' + swept + ' file(s) left by an earlier run');

    // ── The export, for diffing ─────────────────────────────────────────────
    await write('/export', ['=file=' + base]);
    const rscSize = await _settled(write, base + '.rsc');
    const rscBuf = await _readFile(write, base + '.rsc', rscSize);
    const rscText = rscBuf.toString('utf8');
    result.fingerprint = Diff.fingerprint(rscText);

    // Same configuration as last time: the run is worth recording, a second
    // identical restore point is not.
    if (previousFingerprint && previousFingerprint === result.fingerprint) {
      result.outcome = 'unchanged';
      say('configuration unchanged');
      return result;
    }

    // ── The binary, for restoring ───────────────────────────────────────────
    const password = router.backup && router.backup.password;
    if (!password) throw new Error('no backup password configured for this router');
    await write('/system/backup/save', ['=name=' + base, '=password=' + password,
                                        '=encryption=aes-sha256']);
    const bakSize = await _settled(write, base + '.backup');
    const bakBuf = await _readFile(write, base + '.backup', bakSize);

    const dir = Store.dirFor(Store.slugFor(router.label));
    const written = Store.writePair(dir, stem, Diff.normalize(rscText), bakBuf);
    result.rscBytes = written.rscBytes;
    result.backupBytes = written.backupBytes;
    result.dir = dir;
    result.outcome = 'changed';
    result.changed = true;
    say('stored ' + stem + ' (' + Math.round(written.rscBytes / 1024) + ' KB export, ' +
        Math.round(written.backupBytes / 1024) + ' KB binary)');
  } catch (e) {
    if (result.outcome !== 'skipped') result.outcome = 'failed';
    result.error = (e && e.message) || String(e);
    // Free space is not a gate, but it IS the explanation when the router
    // refused for want of it — so a failure carries the number with it rather
    // than making someone go and look.
    const free = result.freeBytes;
    say('failed: ' + result.error + (free ? ' (' + Math.round(free / 1024) + ' KB free)' : ''));
  } finally {
    // The router does not keep our temp files, whatever happened above.
    if (ros) {
      try { await _sweep((cmd, args) => ros.write(cmd, args || []), say); }
      catch (_) { /* already failing; nothing more to try */ }
      try { ros.stop(); } catch (_) { /* nothing left to do */ }
    }
    result.ms = Date.now() - started;
  }
  return result;
}

module.exports = {
  FILE_PREFIX,
  CHUNK,
  generatePassword,
  run,
  _readFile,
  _settled,
  _sweep,
  _identity,
};
