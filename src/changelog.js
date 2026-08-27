'use strict';
/**
 * RouterOS release notes for the Dashboard's Update dialog.
 *
 * THE ROUTER DOES NOT HAVE THESE. Checked against live hardware rather than
 * assumed: `/system/package/update/print` returns exactly channel, mode,
 * check-certificate, ip-version, installed-version, latest-version and status,
 * and there is no `changelog` node anywhere under `/system/package/update`.
 * WinBox's "Check for Updates" window fetches the text over HTTP; it does not
 * read it off the device. So this module fetches it too.
 *
 * That makes this the SECOND outbound destination in the whole server —
 * src/notifier.js is the only other one, and it only ever talks to channels the
 * operator configured. A deliberate departure, and the reason everything here
 * fails soft: an install with no route to the internet must still be able to
 * upgrade its routers, so a failure renders "unavailable" in a box and changes
 * nothing else.
 */

const https = require('https');

const HOST = 'upgrade.mikrotik.com';

/**
 * The single most important line in this file.
 *
 * `version` arrives from a socket payload and is interpolated into a URL PATH.
 * Without an anchored whitelist that is a path traversal (`../../`) and an
 * open-redirect-shaped fetch (`//evil.example.com/`) in one. Digits and dots
 * only, anchored at both ends.
 */
const VERSION_RE = /^\d+\.\d+(\.\d+)?$/;

/** A feature-release changelog is ~38 KB; a patch is under 2 KB. */
const MAX_BYTES = 256 * 1024;
const TIMEOUT_MS = 8000;

/**
 * Released changelogs are immutable, so a hit never needs the network again.
 * Bounded because the key comes from a caller: without the cap, a socket
 * spamming distinct valid-looking versions grows this until the process dies.
 * Entries are small, so a plain FIFO trim is enough.
 */
const CACHE_MAX = 32;
const _cache = new Map();   // version -> { notes } | { error, at }

/**
 * A failure is cached too, briefly. An isolated install would otherwise pay the
 * full timeout every time the dialog is opened, which reads as the dialog being
 * broken rather than the lookup being unavailable.
 */
const NEG_TTL_MS = 60000;

function _remember(version, entry) {
  if (_cache.size >= CACHE_MAX) _cache.delete(_cache.keys().next().value);
  _cache.set(version, entry);
}

/**
 * Fetch the release notes for a RouterOS version.
 *
 * @param   {string} version e.g. '7.24' or '7.24.1'
 * @returns {Promise<string>} the changelog text
 * @throws  on a bad version, a non-200, an oversized body, or a timeout
 */
function fetchNotes(version) {
  const v = String(version == null ? '' : version).trim();
  if (!VERSION_RE.test(v)) return Promise.reject(new Error('bad version'));

  const hit = _cache.get(v);
  if (hit && hit.notes) return Promise.resolve(hit.notes);
  if (hit && hit.error && Date.now() - hit.at < NEG_TTL_MS) return Promise.reject(new Error(hit.error));

  return new Promise((resolve, reject) => {
    let settled = false;
    const fail = (msg) => {
      if (settled) return;
      settled = true;
      _remember(v, { error: msg, at: Date.now() });
      reject(new Error(msg));
    };

    const req = https.request({
      hostname: HOST,
      path:     '/routeros/' + v + '/CHANGELOG',
      method:   'GET',
      timeout:  TIMEOUT_MS,
      headers:  { Accept: 'text/plain' },
    }, (res) => {
      if (res.statusCode !== 200) {
        // Drained rather than left half-read holding the socket open.
        res.resume();
        return fail('HTTP ' + res.statusCode);
      }
      let buf = '';
      let bytes = 0;
      res.setEncoding('utf8');
      res.on('data', (c) => {
        bytes += Buffer.byteLength(c, 'utf8');
        // Enforced WHILE STREAMING, not after buffering — a check at the end is
        // not a cap, it is a report on how much was already accepted.
        if (bytes > MAX_BYTES) { res.destroy(); return fail('too large'); }
        buf += c;
      });
      res.on('end', () => {
        if (settled) return;
        settled = true;
        const notes = buf.trim();
        if (!notes) { _remember(v, { error: 'empty', at: Date.now() }); return reject(new Error('empty')); }
        _remember(v, { notes });
        resolve(notes);
      });
      res.on('error', (e) => fail((e && e.message) || 'read failed'));
    });

    req.on('timeout', () => { req.destroy(); fail('timeout'); });
    req.on('error', (e) => fail((e && e.message) || 'request failed'));
    req.end();
  });
}

/** Test seam: drop everything remembered so far. */
function _resetCache() { _cache.clear(); }

module.exports = { fetchNotes, VERSION_RE, MAX_BYTES, TIMEOUT_MS, _resetCache };
