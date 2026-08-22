'use strict';
/**
 * How bytes and text cross the RouterOS connection.
 *
 * Two encodings, deliberately disagreeing. Every collector wants text, so the
 * receiver decodes UTF-8. A caller reading a FILE wants bytes, and a UTF-8
 * decode destroys them — invalid bytes become U+FFFD, one per byte, so the
 * reassembled length still matches the file size exactly and nothing looks
 * wrong. On a live AX3 a known blob came back with a different sha256 and 177
 * of its 256 distinct byte values intact.
 *
 * So the encoding is a property of the CONNECTION, set by _applyRawBytes() and
 * read by Patch 3 in patch-routeros.js. Most of what follows guards the join
 * between those two files, because that is where this fails silently.
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const path = require('path');

const ROS = require('../src/routeros/client');
const { PATCH_MARKERS, resolveDistPath } = require('../src/routeros/patchVerification');

const ROOT = path.join(__dirname, '..');
const patchSource = fs.readFileSync(path.join(ROOT, 'patch-routeros.js'), 'utf8').replace(/\r\n/g, '\n');

/** Every `replace:` string in patch-routeros.js — what it WRITES, not what it looks for. */
const replacements = patchSource
  .split('\n')
  .filter(line => line.trim().startsWith('replace:'));

/** A connection whose receiver carries the patched decode. */
function fakeConn({ patched = true, receiver = true } = {}) {
  const rx = {
    processRawData: patched
      ? function () { return this.rawBytes ? 'latin1' : 'utf8'; }
      : function () { return 'utf8'; },
  };
  return { connector: { receiver: receiver ? rx : null } };
}

// ── The flag itself ────────────────────────────────────────────────────────

test('an ordinary connection is left alone', () => {
  const ros = new ROS({ host: '192.0.2.1' });
  ros.conn = fakeConn();
  ros._applyRawBytes();
  assert.equal(ros.conn.connector.receiver.rawBytes, undefined,
    'nothing should be set on a connection that did not ask for bytes');
});

test('a rawBytes connection has the flag set on its receiver', () => {
  const ros = new ROS({ host: '192.0.2.1', rawBytes: true });
  ros.conn = fakeConn();
  ros._applyRawBytes();
  assert.equal(ros.conn.connector.receiver.rawBytes, true);
});

test('an unpatched receiver refuses rather than returning the wrong bytes', () => {
  // The failure this exists to prevent: patch-routeros.js only WARNS when a
  // library update moves its target, so an unpatched receiver would accept the
  // flag, ignore it, and hand back a file of the right length and wrong content.
  const ros = new ROS({ host: '192.0.2.1', rawBytes: true });
  ros.conn = fakeConn({ patched: false });
  assert.throws(() => ros._applyRawBytes(), /unpatched/i);
});

test('a connection with no receiver refuses too', () => {
  const ros = new ROS({ host: '192.0.2.1', rawBytes: true });
  ros.conn = fakeConn({ receiver: false });
  assert.throws(() => ros._applyRawBytes(), /no receiver/i);
});

test('the flag is applied on every connect, because the receiver is rebuilt', () => {
  // A reconnect builds a new Receiver, so setting this once at construction
  // would work until the first disconnect and then quietly stop working.
  const source = fs.readFileSync(path.join(ROOT, 'src', 'routeros', 'client.js'), 'utf8');
  const loop = source.slice(source.indexOf('async connectLoop()'));
  assert.ok(/await this\.conn\.connect\(\);\s*\n\s*this\._applyRawBytes\(\);/.test(loop),
    '_applyRawBytes must be called from connectLoop, immediately after connect');
});

// ── The join between the patcher and the verifier ──────────────────────────

test('every verified marker is actually written by patch-routeros.js', () => {
  // The drift this catches, from the session that added it: Patch 3 was renamed
  // UTF8_ENCODING -> RAW_BYTES in the patcher and not in the verifier, and the
  // server refused to start until the two agreed again.
  for (const marker of PATCH_MARKERS) {
    const description = marker.replace('MIKRODASH_PATCHED_', '');
    const genericPatch = patchSource.includes(`'${description}'`)
      && patchSource.includes("'MIKRODASH_PATCHED_' + description");
    const dedicatedPatch = patchSource.includes(marker);
    assert.ok(genericPatch || dedicatedPatch,
      `${marker} is verified at startup but patch-routeros.js writes no such patch`);
  }
});

test('each marker is looked for in the file its patch actually edits', () => {
  assert.equal(resolveDistPath('MIKRODASH_PATCHED_EMPTY_REPLY'), 'Channel.js');
  assert.equal(resolveDistPath('MIKRODASH_PATCHED_RAW_BYTES'),
               path.join('connector', 'Receiver.js'));
  // Patch 6 lives in a third file, which the old substring mapping could not
  // express — it assumed anything not matching EMPTY was in Receiver.js.
  assert.equal(resolveDistPath('MIKRODASH_PATCHED_UTF8_ENCODE'),
               path.join('connector', 'Transmitter.js'));
});

test('both halves of the encoding are verified at startup', () => {
  assert.ok(PATCH_MARKERS.includes('MIKRODASH_PATCHED_RAW_BYTES'),
    'the receiver decode must be verified: without it, files come back corrupt');
  assert.ok(PATCH_MARKERS.includes('MIKRODASH_PATCHED_UTF8_ENCODE'),
    'the transmitter encode must be verified: without it, non-ASCII writes become "?"');
});

// ── What the patches write ─────────────────────────────────────────────────

test('every decode the patcher writes branches on the connection flag', () => {
  // latin1 maps each byte to one code unit, so Buffer.from(s, 'latin1')
  // recovers a file exactly. A decode pinned to either encoding is a bug:
  // utf8 corrupts files, latin1 mojibakes every collector.
  const decodes = replacements.filter(line => line.includes('iconv.decode'));
  assert.ok(decodes.length >= 2, 'both decode sites in Receiver.js must be patched');
  for (const line of decodes) {
    assert.ok(line.includes(`this.rawBytes ? 'latin1' : 'utf8'`),
      'a decode is pinned to one encoding: ' + line.trim());
  }
});

test('the transmitter no longer encodes as win1252', () => {
  // win1252 has no representation for Cyrillic, Greek or CJK, so iconv
  // substituted '?' before the word left the process. Verified on a live AX3:
  // a Cyrillic comment written through the resource engine was stored as
  // '???????'.
  const encodes = replacements.filter(line => line.includes('iconv.encode'));
  assert.ok(encodes.length >= 1, 'the transmitter encode site must be patched');
  for (const line of encodes) {
    assert.ok(line.includes(`'utf8'`) && !line.includes('win1252'),
      'an outgoing encode still uses win1252: ' + line.trim());
  }
});
