'use strict';
/**
 * RouterOS release-notes lookup (the Update dialog's notes box).
 *
 * The version reaching this module comes from a SOCKET PAYLOAD and is
 * interpolated into a URL path, so most of what is tested here is the
 * whitelist. Everything else fails soft by design: an install with no route to
 * the internet must still be able to upgrade its routers.
 *
 * https.request is stubbed throughout. Nothing here touches the network, and a
 * test that did would be a test that fails on a train.
 */

const { test } = require('node:test');
const assert   = require('node:assert/strict');
const https    = require('node:https');
const { EventEmitter } = require('node:events');

const Changelog = require('../src/changelog');

// ── stub plumbing ───────────────────────────────────────────────────────────
const _realRequest = https.request;
let   calls = [];        // every options object handed to https.request
let   responder = null;  // (options) -> { status, chunks } | 'timeout' | 'error'

function stubHttps() {
  calls = [];
  https.request = function (options, cb) {
    calls.push(options);
    const req = new EventEmitter();
    req.destroy = () => {};
    req.end = () => {
      const plan = responder ? responder(options) : { status: 200, chunks: ['ok'] };
      if (plan === 'timeout') { process.nextTick(() => req.emit('timeout')); return req; }
      if (plan === 'error')   { process.nextTick(() => req.emit('error', new Error('boom'))); return req; }
      const res = new EventEmitter();
      res.statusCode  = plan.status;
      res.setEncoding = () => {};
      res.resume      = () => {};
      res.destroy     = () => {};
      process.nextTick(() => {
        cb(res);
        for (const c of (plan.chunks || [])) res.emit('data', c);
        res.emit('end');
      });
      return req;
    };
    return req;
  };
}
function restoreHttps() { https.request = _realRequest; }

function withStub(fn) {
  stubHttps();
  Changelog._resetCache();
  return Promise.resolve().then(fn).finally(() => { restoreHttps(); responder = null; });
}

// ── the whitelist, which is the whole security story ────────────────────────
//
// The version is interpolated into a URL PATH. Without an anchored whitelist
// that is a path traversal and a fetch at an attacker-chosen host in one line.
// Asserted THROUGH fetchNotes rather than against the regex directly, so what
// is pinned is reachable behaviour and not a constant sitting near the code.
test('a well-formed version is accepted and shapes the URL', async () => {
  await withStub(async () => {
    responder = () => ({ status: 200, chunks: ["What's new in 7.24.1"] });
    const notes = await Changelog.fetchNotes('7.24.1');
    assert.match(notes, /7\.24\.1/);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].hostname, 'upgrade.mikrotik.com');
    assert.equal(calls[0].path, '/routeros/7.24.1/CHANGELOG');
  });
});

test('a two-part version is accepted too', async () => {
  await withStub(async () => {
    responder = () => ({ status: 200, chunks: ['notes'] });
    await Changelog.fetchNotes('7.24');
    assert.equal(calls[0].path, '/routeros/7.24/CHANGELOG');
  });
});

test('anything that is not digits and dots never reaches the network', async () => {
  // Each is a different way out of the intended path. The assertion that
  // matters is calls.length === 0: rejecting late, after the request is built,
  // would still have made the request.
  const hostile = [
    '../../etc/passwd',
    '..%2f..%2fetc',
    '/routeros/7.24/CHANGELOG',
    '//evil.example.com/x',
    'https://evil.example.com/x',
    '7.24/../../x',
    '7.24\n/x',
    '7',                // a major alone is not a version here
    '7.24.1.2',
    'latest',
    '',
    null,
    undefined,
  ];
  await withStub(async () => {
    for (const v of hostile) {
      await assert.rejects(() => Changelog.fetchNotes(v), /bad version/,
        'accepted: ' + JSON.stringify(v));
    }
    assert.equal(calls.length, 0, 'a rejected version must not have reached https.request');
  });
});

test('surrounding whitespace is trimmed rather than rejected', async () => {
  await withStub(async () => {
    responder = () => ({ status: 200, chunks: ['notes'] });
    await Changelog.fetchNotes('  7.24.1  ');
    assert.equal(calls[0].path, '/routeros/7.24.1/CHANGELOG');
  });
});

// ── the cache ───────────────────────────────────────────────────────────────
test('a second lookup of the same version performs no second request', async () => {
  // Counted, not inferred. "It returned the same string" is also true of an
  // implementation that fetched twice.
  await withStub(async () => {
    responder = () => ({ status: 200, chunks: ['stable text'] });
    const a = await Changelog.fetchNotes('7.24.1');
    const b = await Changelog.fetchNotes('7.24.1');
    assert.equal(a, b);
    assert.equal(calls.length, 1, 'a released changelog never changes; one fetch is enough');
  });
});

test('a failure is remembered too, so an isolated install is not punished per open', async () => {
  // Without this an air-gapped install pays the full timeout every time the
  // dialog opens, which reads as the dialog being broken.
  await withStub(async () => {
    responder = () => ({ status: 404, chunks: [] });
    await assert.rejects(() => Changelog.fetchNotes('9.99.9'));
    await assert.rejects(() => Changelog.fetchNotes('9.99.9'));
    assert.equal(calls.length, 1, 'the negative result should be cached for a while');
  });
});

// ── the limits ──────────────────────────────────────────────────────────────
test('an oversized body is abandoned rather than buffered', async () => {
  // The cap is enforced WHILE STREAMING. A check after buffering is not a cap,
  // it is a report on how much was already accepted.
  await withStub(async () => {
    const chunk = 'x'.repeat(64 * 1024);
    responder = () => ({ status: 200, chunks: [chunk, chunk, chunk, chunk, chunk, chunk] });
    await assert.rejects(() => Changelog.fetchNotes('7.24.1'), /too large/);
  });
});

test('a body at a normal size is returned whole', async () => {
  // The believability twin for the cap: without it, "too large" could be
  // rejecting everything.
  await withStub(async () => {
    const body = "What's new in 7.24:\n" + '*) something - a fix;\n'.repeat(500);
    responder = () => ({ status: 200, chunks: [body] });
    const notes = await Changelog.fetchNotes('7.24');
    assert.ok(notes.length > 10000);
    assert.match(notes, /^What's new in 7\.24:/);
  });
});

test('a non-200 rejects rather than returning the error page as notes', async () => {
  await withStub(async () => {
    responder = () => ({ status: 503, chunks: ['<html>maintenance</html>'] });
    await assert.rejects(() => Changelog.fetchNotes('7.24.1'), /HTTP 503/);
  });
});

test('an empty body is a failure, not empty release notes', async () => {
  await withStub(async () => {
    responder = () => ({ status: 200, chunks: ['   \n  '] });
    await assert.rejects(() => Changelog.fetchNotes('7.24.1'), /empty/);
  });
});

test('a timeout rejects instead of hanging', async () => {
  await withStub(async () => {
    responder = () => 'timeout';
    await assert.rejects(() => Changelog.fetchNotes('7.24.1'), /timeout/);
  });
});

test('a transport error rejects', async () => {
  await withStub(async () => {
    responder = () => 'error';
    await assert.rejects(() => Changelog.fetchNotes('7.24.1'), /boom/);
  });
});

test('the request carries a timeout so a black-holed route cannot hang forever', async () => {
  await withStub(async () => {
    responder = () => ({ status: 200, chunks: ['notes'] });
    await Changelog.fetchNotes('7.24.1');
    assert.ok(calls[0].timeout > 0 && calls[0].timeout <= 15000,
      'a request with no timeout waits on the OS, which is minutes');
  });
});
