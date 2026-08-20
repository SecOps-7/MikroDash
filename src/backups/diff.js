'use strict';
/**
 * Deciding whether a configuration changed, and showing what changed.
 *
 * ── Why not git ────────────────────────────────────────────────────────────
 *
 * A repository would give diffing for free and cost a runtime dependency, a
 * working tree per router, and a second source of truth about what a backup
 * *is*. What is actually needed is narrower: a hash to answer "did anything
 * change", and a line diff to show what. Both are small and neither needs
 * history — the pairs on disk already are the history.
 *
 * ── Normalisation is the whole game ────────────────────────────────────────
 *
 * `/export` opens with a line that changes every single run:
 *
 *     # 2026-08-19 20:35:21 by RouterOS 7.24
 *     # software id = HR2S-3YN6
 *     #
 *     # model = C53UiG+5HPaxD2HPaxD
 *     # serial number = HDF08J96K1M
 *
 * Only the FIRST line moves — it carries both the timestamp and the RouterOS
 * version. The software id, model and serial are stable, and are worth keeping
 * in the hash: if any of them changes, this is not the same device, and a
 * restore point taken from it should not silently look like drift-free
 * continuity.
 *
 * Miss this line and every backup reports as drifted, which is the usual
 * reason a config-drift tool ends up ignored.
 *
 * Line endings are CRLF on the wire and are normalised to LF, so a diff never
 * reports an invisible change.
 */

const crypto = require('crypto');

/** The one volatile line: date, time and RouterOS version, rewritten each run. */
const VOLATILE_HEADER = /^# \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} by RouterOS .*$/;

/**
 * How different two exports may be before a line diff stops being useful.
 *
 * Myers' algorithm costs O((N+M)·D) where D is the number of edits, so it is
 * fast for the case that matters (a handful of changed lines in 36,000) and
 * degrades on a wholesale rewrite. Past this, the honest answer is "these are
 * not variations of one configuration" rather than ten thousand hunks nobody
 * will read.
 */
const MAX_EDITS = 4000;

/** Lines of context either side of a change, as unified diffs conventionally show. */
const CONTEXT = 3;

/**
 * Strip what changes on its own, so the hash reflects the configuration.
 *
 * Returns an array of lines, because everything downstream works in lines and
 * splitting once is cheaper than splitting three times.
 */
function normalizeLines(text) {
  const lines = String(text == null ? '' : text)
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .split('\n');
  // Only the leading header line is dropped, and only if it is that line —
  // a configuration whose first line happens to be some other comment is left
  // alone.
  if (lines.length && VOLATILE_HEADER.test(lines[0])) lines.shift();
  // A trailing newline leaves an empty final element that is not a line.
  if (lines.length && lines[lines.length - 1] === '') lines.pop();
  return lines;
}

/** The normalised text, for storage and display. */
function normalize(text) {
  return normalizeLines(text).join('\n');
}

/**
 * What "did it change" is decided by. Same normalised configuration, same
 * fingerprint — regardless of when it was taken or which RouterOS wrote it.
 */
function fingerprint(text) {
  return crypto.createHash('sha256').update(normalize(text), 'utf8').digest('hex');
}

/**
 * Myers' greedy shortest-edit-script, returning the trace of each round.
 *
 * Returns null when the two are further apart than `maxEdits`, which the
 * caller reports rather than approximates.
 */
function _shortestEdit(a, b, maxEdits) {
  const n = a.length, m = b.length;
  const max = Math.min(maxEdits, n + m);
  const v = new Map([[1, 0]]);
  const trace = [];

  for (let d = 0; d <= max; d++) {
    trace.push(new Map(v));
    for (let k = -d; k <= d; k += 2) {
      // Move down when there is no left neighbour, or the one below reaches
      // further; otherwise move right.
      const down = (k === -d) || (k !== d && (v.get(k - 1) || 0) < (v.get(k + 1) || 0));
      let x = down ? (v.get(k + 1) || 0) : (v.get(k - 1) || 0) + 1;
      let y = x - k;
      // Follow the diagonal as far as the lines agree — this is the part that
      // makes a small change in a large file cheap.
      while (x < n && y < m && a[x] === b[y]) { x++; y++; }
      v.set(k, x);
      if (x >= n && y >= m) return trace;
    }
  }
  return null;
}

/** Walk the trace backwards into a flat edit script, oldest first. */
function _backtrack(trace, a, b) {
  const ops = [];
  let x = a.length, y = b.length;

  for (let d = trace.length - 1; d > 0; d--) {
    const v = trace[d];
    const k = x - y;
    const down = (k === -d) || (k !== d && (v.get(k - 1) || 0) < (v.get(k + 1) || 0));
    const prevK = down ? k + 1 : k - 1;
    const prevX = v.get(prevK) || 0;
    const prevY = prevX - prevK;

    while (x > prevX && y > prevY) { x--; y--; ops.push({ op: ' ', text: a[x] }); }
    if (x > prevX) { x--; ops.push({ op: '-', text: a[x] }); }
    else           { y--; ops.push({ op: '+', text: b[y] }); }
  }
  while (x > 0 && y > 0) { x--; y--; ops.push({ op: ' ', text: a[x] }); }

  ops.reverse();
  return ops;
}

/**
 * Group an edit script into unified-style hunks with context.
 *
 * A run of unchanged lines longer than 2×CONTEXT is the boundary between one
 * hunk and the next; anything shorter stays inside a hunk, because splitting
 * there would show the same lines twice.
 */
function _hunks(ops) {
  const out = [];
  let cur = null;
  let aLine = 1, bLine = 1;      // 1-based, as a unified diff numbers them
  let pending = [];              // unchanged lines not yet assigned

  const open = (startA, startB) => ({ aStart: startA, bStart: startB, aCount: 0, bCount: 0, lines: [] });
  const push = (hunk, entry) => {
    hunk.lines.push(entry);
    if (entry.op !== '+') hunk.aCount++;
    if (entry.op !== '-') hunk.bCount++;
  };

  for (const o of ops) {
    if (o.op === ' ') {
      pending.push({ op: ' ', text: o.text, aLine, bLine });
      aLine++; bLine++;
      // Far enough past a change to close the hunk.
      if (cur && pending.length > CONTEXT * 2) {
        for (const p of pending.slice(0, CONTEXT)) push(cur, p);
        out.push(cur);
        cur = null;
        pending = pending.slice(-CONTEXT);
      }
      if (!cur && pending.length > CONTEXT) pending = pending.slice(-CONTEXT);
      continue;
    }

    if (!cur) {
      const lead = pending;
      cur = open(lead.length ? lead[0].aLine : aLine, lead.length ? lead[0].bLine : bLine);
      for (const p of lead) push(cur, p);
    } else {
      for (const p of pending) push(cur, p);
    }
    pending = [];

    if (o.op === '-') { push(cur, { op: '-', text: o.text, aLine }); aLine++; }
    else              { push(cur, { op: '+', text: o.text, bLine }); bLine++; }
  }

  if (cur) {
    for (const p of pending.slice(0, CONTEXT)) push(cur, p);
    out.push(cur);
  }
  return out;
}

/**
 * Compare two exports.
 *
 * Returns `{ changed, added, removed, hunks, truncated }`. `truncated` means
 * the two were further apart than MAX_EDITS and no hunks were produced — said
 * plainly rather than shown as a partial diff that looks complete.
 */
function diff(oldText, newText) {
  const a = normalizeLines(oldText);
  const b = normalizeLines(newText);

  if (a.length === b.length && a.every((line, i) => line === b[i])) {
    return { changed: false, added: 0, removed: 0, hunks: [], truncated: false };
  }

  const trace = _shortestEdit(a, b, MAX_EDITS);
  if (!trace) {
    return { changed: true, added: null, removed: null, hunks: [], truncated: true };
  }

  const ops = _backtrack(trace, a, b);
  const added = ops.filter(o => o.op === '+').length;
  const removed = ops.filter(o => o.op === '-').length;
  return { changed: added > 0 || removed > 0, added, removed, hunks: _hunks(ops), truncated: false };
}

module.exports = {
  VOLATILE_HEADER,
  MAX_EDITS,
  CONTEXT,
  normalize,
  normalizeLines,
  fingerprint,
  diff,
};
