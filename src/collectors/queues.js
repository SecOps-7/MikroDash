'use strict';
/**
 * Queues collector — RouterOS traffic shaping.
 *
 *   /queue/simple   per-target bandwidth limits, ordered, bidirectional
 *   /queue/tree     mangle-driven HTB shaping, unordered, one direction per node
 *
 * Two menus with genuinely different row shapes, not one shape with gaps — all
 * verified against a live router:
 *
 *   simple   pairs everywhere ("15000000/20000000"), `packet-marks` plural,
 *            and a `dynamic` flag
 *   tree     single values ("10000000"), `packet-mark` singular, and NO
 *            `dynamic` field at all
 *
 * Three more things the probe settled, each of which would otherwise be a bug:
 *
 *   STATISTICS ARRIVE UNASKED. `rate`, `bytes`, `packets`, `dropped` and the
 *   queued-* fields come back on a plain /print — no `=stats=` flag. The code
 *   still checks which fields actually arrived rather than assuming, because
 *   that was true of one RouterOS build on one day.
 *
 *   THE API ANSWERS IN RAW BPS. The CLI's "15M/20M" reads back as
 *   "15000000/20000000". Input accepts suffixes; output never carries them.
 *
 *   UNLIMITED IS 0, NOT ABSENT. An unlimited queue reads back as "0/0" rather
 *   than omitting the field. So 0 means "explicitly unlimited" and null means
 *   "the router said nothing", and the page draws those differently.
 *
 * THIS COLLECTOR ONLY READS. Every write lives in the socket actions in
 * index.js, gated on the page and on router:write. A test asserts the mutating
 * command paths never appear here.
 */

const { clampPoll, createPollLoop, createListenRefresh } = require('./util');
// Rate parsing lives with the guard that judges limits, so the number the page
// prints and the number the warning reasons about cannot drift apart.
const { parseRate, parsePair } = require('../routeros/queueGuard');

const SIMPLE_CMD = '/queue/simple/print';
const TREE_CMD   = '/queue/tree/print';

// A queue's byte counter stops moving the moment traffic stops. Past this, a
// stale delta would be reported as live throughput, so the rate is forced to
// zero. More trustworthy here than for a WireGuard peer (ppp.js has the same
// constant): unchanged queue bytes genuinely means nothing matched the queue.
const IDLE_AFTER_SEC = 10;

const _bool = (v) => v === true || v === 'true' || v === 'yes';
const _int  = (v) => { const n = parseInt(v, 10); return Number.isFinite(n) ? n : null; };

/** Simple queues report a pair; trees report one number in the same field. */
function _pairInt(raw) {
  if (raw === null || raw === undefined || raw === '') return { up: null, down: null };
  const parts = String(raw).split('/');
  const up   = _int(parts[0]);
  return { up, down: parts.length > 1 ? _int(parts[1]) : up };
}

/**
 * Which statistics this router actually returned.
 *
 * 'none' is what lets the page print one line instead of forty unexplained
 * em-dashes — the same idea as rosusers' `available`, asked of a different
 * question.
 */
function _statsLevel(rows) {
  const r = (rows || []).find(x => x && x['.id']);
  if (!r) return 'full';                       // nothing to judge; assume the best
  if (r.bytes === undefined) return 'none';
  return r.rate === undefined ? 'counters' : 'full';
}

/**
 * Per-second rate from a byte counter, per direction.
 *
 * The idiom is ppp.js's and the two subtleties are load-bearing:
 *
 *   The stored timestamp advances ONLY when bytes actually moved, so dtSec
 *   always spans a real measurement window rather than the gap between two
 *   reads that happened to see the same counter.
 *
 *   The first sample is null, not 0 — there is no window yet, and 0 would claim
 *   an idle queue that may be saturating the line.
 *
 * Not hoisted into util.js: ppp returns null on the first sample and vpn returns
 * 0, each with a comment arguing for its choice, so a shared helper would have to
 * silently change one of them to serve this page. If a fifth diffing caller ever
 * appears, that is the moment to hoist — taking ppp's null semantics, with vpn
 * migrating deliberately in a change of its own.
 */
function _deriveRate(key, bytes, routerRate, prev, now) {
  const store = prev.get(key);
  const out = { up: null, down: null };
  let source = null, windowMs = null;

  if (store && now > store.ts) {
    const dtSec = (now - store.ts) / 1000;
    const same  = bytes.up === store.up && bytes.down === store.down;
    if (same && dtSec > IDLE_AFTER_SEC) {
      out.up = 0; out.down = 0;
    } else if (!same) {
      out.up   = Math.max(0, ((bytes.up   - store.up)   * 8) / dtSec);
      out.down = Math.max(0, ((bytes.down - store.down) * 8) / dtSec);
    } else {
      // Counter unchanged but not yet idle: hold the last known rate rather than
      // claiming either zero or a fresh measurement.
      out.up = store.rateUp; out.down = store.rateDown;
    }
    source = 'delta';
    windowMs = now - store.ts;
  } else if (routerRate && (routerRate.up !== null || routerRate.down !== null)) {
    // First sample only. RouterOS's own `rate` is an average in BYTES per second
    // over a window it does not disclose — good enough to avoid an empty cell on
    // the first tick, not good enough to keep using once we can measure. Labelled
    // so the page can say which it is showing.
    out.up   = routerRate.up   === null ? null : routerRate.up   * 8;
    out.down = routerRate.down === null ? null : routerRate.down * 8;
    source = 'router';
  }

  if (bytes.up !== null && bytes.down !== null &&
      (!store || bytes.up !== store.up || bytes.down !== store.down)) {
    prev.set(key, { up: bytes.up, down: bytes.down, ts: now,
                    rateUp: out.up, rateDown: out.down });
  }
  return { rateBps: out, rateSource: source, rateWindowMs: windowMs };
}

/**
 * A key that survives a rename but not a recreation.
 *
 * RouterOS reuses `*N` ids after a removal. Keyed on the id alone, a newly
 * created queue would inherit a deleted one's byte baseline and report a
 * fabricated multi-gigabit first sample.
 */
const _rateKey = (menu, r) => menu + ':' + (r['.id'] || '') + '|' + (r.name || '');

/** One simple-queue row. */
function _simpleRow(r, i, prev, now) {
  const bytes = _pairInt(r.bytes);
  const rate  = _deriveRate(_rateKey('s', r), bytes, parsePair(r.rate), prev, now);
  return {
    id:      r['.id'] || '',
    order:   i,                       // print order, and it is semantic — see buildQueueRows
    name:    String(r.name || ''),
    target:  r.target || '',
    parent:  r.parent && r.parent !== 'none' ? r.parent : '',
    packetMarks: r['packet-marks'] || '',
    priority:    r.priority || '',
    queueType:   r.queue || '',
    limitAt:  parsePair(r['limit-at']),
    maxLimit: parsePair(r['max-limit']),
    burstLimit: parsePair(r['burst-limit']),
    bytes,
    packets:     _pairInt(r.packets),
    dropped:     _pairInt(r.dropped),
    queuedBytes: _pairInt(r['queued-bytes']),
    disabled: _bool(r.disabled),
    invalid:  _bool(r.invalid),
    dynamic:  _bool(r.dynamic),
    comment:  r.comment || '',
    rateBps:      rate.rateBps,
    rateSource:   rate.rateSource,
    rateWindowMs: rate.rateWindowMs,
  };
}

/** One queue-tree row. Single values where simple has pairs. */
function _treeRow(r, i, prev, now) {
  const b = _int(r.bytes);
  const bytes = { up: b, down: b };
  const rr = parseRate(r.rate);
  const rate = _deriveRate(_rateKey('t', r), bytes, { up: rr, down: rr }, prev, now);
  return {
    id:     r['.id'] || '',
    order:  i,
    name:   String(r.name || ''),
    parent: r.parent || '',
    packetMark: r['packet-mark'] || '',
    priority:   r.priority || '',
    queueType:  r.queue || '',
    limitAt:  parseRate(r['limit-at']),
    maxLimit: parseRate(r['max-limit']),
    burstLimit: parseRate(r['burst-limit']),
    bytes:   b,
    packets: _int(r.packets),
    dropped: _int(r.dropped),
    queuedBytes: _int(r['queued-bytes']),
    disabled: _bool(r.disabled),
    invalid:  _bool(r.invalid),
    // A tree row has no `dynamic` field. Reported as false rather than left
    // undefined so the frontend has one shape to render.
    dynamic:  false,
    comment:  r.comment || '',
    // FastTrack bypasses a tree parented to `global`, but NOT one parented to an
    // interface — confirmed twice in the MikroTik docs, and a tree on an
    // interface is accepted by the router. The page needs that per row, so it is
    // derived once here rather than three times in app.js.
    fasttrackBypassable: (r.parent || '') === 'global',
    rateBps:      rate.rateBps.up,
    rateSource:   rate.rateSource,
    rateWindowMs: rate.rateWindowMs,
  };
}

/**
 * Build both tables. Pure and exported so the rate maths is testable without a
 * router — the way parsePppSessions and buildVlanRows already are.
 *
 * `prev` is the caller's Map and is mutated; `now` is injected.
 *
 * ORDER IS PRESERVED, NOT SORTED. Simple queues are walked in list order and the
 * first match wins, so a queue's position changes what it does. Sorting these
 * alphabetically by default would misrepresent the router.
 */
function buildQueueRows(simpleRows, treeRows, prev, now) {
  const sRows = (simpleRows || []).filter(r => r && r['.id']);
  const tRows = (treeRows   || []).filter(r => r && r['.id']);

  // Baselines for rows that no longer exist are dropped BEFORE building, so a
  // recreated queue reusing a RouterOS `*N` id cannot inherit the old counter.
  const live = new Set([].concat(sRows.map(r => _rateKey('s', r)), tRows.map(r => _rateKey('t', r))));
  for (const k of Array.from(prev.keys())) if (!live.has(k)) prev.delete(k);

  return {
    simple: sRows.map((r, i) => _simpleRow(r, i, prev, now)),
    tree:   tRows.map((r, i) => _treeRow(r, i, prev, now)),
  };
}

/**
 * Is a FastTrack rule swallowing the traffic these queues are meant to shape?
 *
 * Pure, and fed raw firewall rows so a test can exercise it directly. Only
 * `chain=forward` counts, and a disabled rule counts for nothing — a rule that
 * is not in force bypasses nothing.
 */
function activeFasttrack(filterRows) {
  const hits = (filterRows || []).filter(r =>
    r && r.action === 'fasttrack-connection' && r.chain === 'forward' && !_bool(r.disabled));
  return {
    state:  hits.length ? 'active' : 'clear',
    count:  hits.length,
    // A rule narrowed by address or interface bypasses only part of the traffic,
    // which is worth saying rather than implying every queue is dead.
    scoped: hits.some(r => r.srcAddress || r.dstAddress || r.inInterface ||
                           r['src-address'] || r['dst-address'] || r['in-interface']),
  };
}

class QueuesCollector {
  constructor({ ros, io, state, pollMs, streamMode, firewall }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 5000, 60000, 2000);
    this.streamMode = streamMode !== false;
    // Borrowed by reference, never fetched — vlans.js is the precedent. Only a
    // SUMMARY leaves this collector: a reader holding `queues` but not
    // `firewall` learns that FastTrack is on, which is a fact about this page's
    // own correctness, not a firewall listing.
    this.firewall = firewall || null;
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][queues]` : '[queues]';

    this._poll   = createPollLoop(() => this._tick(), () => this.pollMs);
    this._prev   = new Map();          // rate baselines, keyed by menu:id|name
    this._lastFp = '';
    this._simpleAvailable = undefined;
    this._treeAvailable   = undefined;
    this._denied = false;
    this._listens = [];
    this.lastPayload = null;
  }

  async _read(cmd, flag) {
    if (this[flag] === false) return [];
    try {
      const rows = await this.ros.write(cmd, []);
      this[flag] = true;
      return (rows || []).filter(r => r && Object.keys(r).length);
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      if (msg.includes('no such') || msg.includes('unknown command')) this[flag] = false;
      else if (msg.includes('not enough permission') || msg.includes('permission denied') ||
               msg.includes('no permissions')) { this[flag] = false; this._denied = true; }
      else this.state.lastQueuesErr = e && e.message ? e.message : String(e);
      return [];
    }
  }

  /** Re-read now, after an action, so the page shows what the router did. */
  async refreshNow() {
    if (this.ros.connected) await this._tick();
  }

  /**
   * Forget every rate baseline.
   *
   * `set` and `reset-counters` can drop a counter. The Math.max(0, …) clamp
   * hides that as a zero, but the window AFTER it would be measured against a
   * baseline the router no longer agrees with. Called by the write handlers.
   */
  forgetRates() { this._prev.clear(); }

  _fasttrack() {
    const fw = this.firewall;
    // One guard, three cases: the collector is a null stub for a router where
    // the operator switched Firewall collection off (#105), it has not started
    // yet, or its start swallowed a failure. All three mean "cannot say", and
    // that must degrade the banner rather than blank the page — which is also
    // why the registry entry declares requires: [].
    if (!fw || fw.disabled || !fw.lastPayload) return { state: 'unknown', count: 0, scoped: false };
    // A SUSPENDED firewall collector still answers. Its suspend() reassigns
    // `this._filter = []` rather than emptying the array in place, so
    // lastPayload keeps pointing at the last emitted set and we report the last
    // known state instead of a false 'clear'. An optimisation to mutate in place
    // would silently break that.
    return activeFasttrack(fw.lastPayload.filter || []);
  }

  async _tick() {
    if (!this.ros.connected) return;

    const [simpleRows, treeRows] = await Promise.all([
      this._read(SIMPLE_CMD, '_simpleAvailable'),
      this._read(TREE_CMD,   '_treeAvailable'),
    ]);

    const now = Date.now();
    const built = buildQueueRows(simpleRows, treeRows, this._prev, now);
    const payload = {
      ts: now, pollMs: this.streamMode ? 0 : this.pollMs,
      ...built,
      fasttrack: this._fasttrack(),
      stats: _statsLevel(simpleRows.length ? simpleRows : treeRows),
      available: this._simpleAvailable !== false,
      denied: this._denied,
    };
    this.lastPayload = payload;
    this.state.lastQueuesTs = now;

    // Byte counters are excluded from the fingerprint on purpose: they move
    // every tick on a busy queue, and emitting for that alone would defeat the
    // dirty check. Rates are included, rounded to kbit, because they are what
    // changes visibly on screen.
    const fp = JSON.stringify({
      s: built.simple.map(q => [q.id, q.name, q.target, q.disabled, q.dynamic,
                                q.maxLimit.up, q.maxLimit.down, q.limitAt.up, q.limitAt.down,
                                Math.round((q.rateBps.up || 0) / 1000), Math.round((q.rateBps.down || 0) / 1000)]),
      t: built.tree.map(q => [q.id, q.name, q.parent, q.packetMark, q.disabled,
                              q.maxLimit, Math.round((q.rateBps || 0) / 1000)]),
      f: payload.fasttrack,
      st: payload.stats,
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-queues').emit('queues:update', payload);
  }

  _startListens() {
    if (!this.streamMode || this._listens.length) return;
    // The channel carries no data — it marks the tables stale and the ordinary
    // tick reads them. Structural edits (someone adding a queue in WinBox) show
    // up immediately; counters still ride the tick.
    for (const [cmd, label] of [['/queue/simple/listen', 'simple'], ['/queue/tree/listen', 'tree']]) {
      this._listens.push(createListenRefresh({
        ros: this.ros, cmd, label: `${this._lbl}[${label}]`,
        onEvent: () => { this._tick().catch(() => {}); },
      }));
    }
    this._listens.forEach(l => l.start());
  }

  _stopListens() {
    this._listens.forEach(l => l.stop());
    this._listens = [];
  }

  async start() {
    // The tick runs on its own rather than waiting for stream data. On a router
    // with no queues at all — which is every router until somebody makes one —
    // a /listen would never fire, and the page would sit on "waiting for data"
    // forever instead of saying there are none.
    if (this.ros.connected) await this._tick();
    this._startListens();
    this._poll.start();
    this.ros.on('close', () => { this._poll.stop(); this._stopListens(); });
    this.ros.on('connected', async () => {
      this._poll.stop();
      this._stopListens();
      this._lastFp = '';
      this._denied = false;
      this._prev.clear();
      this._simpleAvailable = this._treeAvailable = undefined;
      await this._tick();
      this._startListens();
      this._poll.start();
    });
  }

  suspend() { this._poll.stop(); this._stopListens(); }
  resume()  { if (this.ros.connected) { this._startListens(); this._poll.start(); } }

  stop() { this._poll.stop(); this._stopListens(); this._lastFp = ''; this._prev.clear(); }
}

QueuesCollector.buildQueueRows  = buildQueueRows;
QueuesCollector.activeFasttrack = activeFasttrack;
QueuesCollector.IDLE_AFTER_SEC  = IDLE_AFTER_SEC;
module.exports = QueuesCollector;
