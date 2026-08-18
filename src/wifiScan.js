'use strict';
// WiFi frequency scan runner.
//
// This is the first deliberately DISRUPTIVE thing MikroDash does. Every other
// command it issues is print/listen/monitor-traffic/ping. MikroTik's own words
// about the command this module runs:
//
//   "Running a frequency scan will disconnect all connected clients, or if the
//    interface is in station mode, it will disconnect from the AP."
//
// Almost everything below is there because of that sentence: the bounded
// duration, the wall-clock stop that does not trust the router to honour it,
// one scan per router, and a fleet-wide cap so one operator cannot walk a
// building disabling every AP in it.
//
// Not a collector, and deliberately absent from src/collection.js: that
// registry models long-lived things with a poll interval, a stream key and a
// session property. A one-off action has none of that shape.

const { stopStreamSafe } = require('./collectors/util');

const CMD                = '/interface/wifi/frequency-scan';
// Without an explicit proplist RouterOS answers every freeze-frame with a bare
// !empty and never sends a row — verified against a live 7.23.3 hAP AX3, where
// adding this turned 25 seconds of silence into 234 rows.
const PROPLIST           = '=.proplist=channel,networks,load,nf,max-signal,min-signal';
// Seconds, offered in the UI. Nothing shorter than 30 is offered: a full sweep
// of a band takes roughly 30-60s on this hardware, and on a live 7.23.3 hAP AX3
// the first rows did not arrive until 7.3s, so a 5 or 10 second scan takes the
// radio off the air and returns almost nothing for it. At the other end 120 buys
// a second full pass over the band. The wall-clock ceiling is duration + grace,
// so up to 125s of radio downtime.
const DURATIONS          = Object.freeze([30, 60, 120]);
const HARD_STOP_GRACE_MS = 5000;
const FLUSH_MS           = 250;
const MAX_CHANNELS       = 200;   // bounds memory against a device reporting per-sample
const FLEET_CAP          = 3;     // concurrent scans across all routers
const COOLDOWN_MS        = 10_000;
const IFACE_RE           = /^[A-Za-z0-9._\- ]{1,64}$/;

// RouterOS wants hh:mm:ss. The bare "10s" form may also work, but this one is
// unambiguous across builds and costs nothing.
function _hms(sec) {
  const s = Math.max(0, Math.floor(sec));
  return '00:' + String(Math.floor(s / 60)).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0');
}

function _int(v) {
  if (v === undefined || v === null || v === '') return null;
  const n = parseInt(String(v), 10);
  return Number.isFinite(n) ? n : null;
}

function _bool(v) {
  return v === true || v === 'true' || v === 'yes';
}

/**
 * Frequency in MHz -> the channel number operators actually talk in.
 *
 * Lives here rather than in the browser so it can be unit-tested: public/app.js
 * is a browser script the suite can only assert against as text.
 *
 * Returns null outside the known bands rather than a plausible-looking wrong
 * number — "5905 MHz" is honest, "channel -9" is not.
 */
function freqToChannel(mhz) {
  if (!Number.isFinite(mhz)) return null;
  if (mhz === 2484) return 14;                                   // Japan, and not on the /5 grid
  if (mhz >= 2412 && mhz <= 2472 && (mhz - 2412) % 5 === 0) return (mhz - 2407) / 5;
  // 6GHz overlaps 5GHz numbering, so it is checked first and by its own base.
  if (mhz >= 5955 && mhz <= 7115 && (mhz - 5955) % 5 === 0) return (mhz - 5950) / 5;
  if (mhz >= 5160 && mhz <= 5885 && (mhz - 5160) % 5 === 0) return (mhz - 5000) / 5;
  return null;
}

/**
 * Parse one !re row.
 *
 * `channel` is documented as an integer but the wifi stack reports compound
 * forms elsewhere (/interface/wifi/monitor returns "2427/ax/Ce"), so take the
 * leading integer and keep the raw value. A row whose channel will not parse is
 * dropped rather than rendered as NaN — a bar at x=NaN silently disappears and
 * looks like a channel that was never scanned.
 */
function parseRow(r) {
  if (!r || typeof r !== 'object') return null;
  const raw = r.channel !== undefined ? r.channel : r.freq;
  const ch  = _int(String(raw === undefined || raw === null ? '' : raw).split('/')[0]);
  if (ch === null) return null;
  return {
    ch,
    chNum:     freqToChannel(ch),
    chRaw:     raw === undefined ? null : String(raw),
    nets:      _int(r.networks),
    load:      _int(r.load),
    nf:        _int(r.nf),
    maxSig:    _int(r['max-signal']),
    minSig:    _int(r['min-signal']),
    primary:   _bool(r.primary),
    secondary: _bool(r.secondary),
  };
}

// RouterOS !trap text -> a code the browser can phrase. Anything unmatched stays
// 'router-error' and carries the sanitized message.
function classifyTrap(msg) {
  const m = String(msg || '').toLowerCase();
  if (/not enough privileges|permission denied|cannot run/.test(m)) return 'permission-denied';
  if (/no such command prefix|unknown command/.test(m))             return 'unsupported-stack';
  if (/no such item|not found/.test(m))                             return 'no-such-interface';
  if (/unknown parameter|no such argument|invalid value/.test(m))   return 'bad-parameter';
  return 'router-error';
}

function createRegistry(opts = {}) {
  const clock       = opts.clock || { setTimeout, clearTimeout, setInterval, clearInterval };
  const graceMs     = opts.hardStopGraceMs === undefined ? HARD_STOP_GRACE_MS : opts.hardStopGraceMs;
  const flushMs     = opts.flushMs === undefined ? FLUSH_MS : opts.flushMs;
  const now         = opts.now || (() => Date.now());
  const scans       = new Map();   // routerId -> entry
  const cooldowns   = new Map();   // socketId -> ts the last scan ended
  let   seq         = 0;

  const isScanning = (routerId) => scans.has(routerId);

  function stateFor(routerId) {
    const e = scans.get(routerId);
    if (!e) return null;
    return {
      scanId: e.scanId, iface: e.iface, durationSec: e.durationSec,
      startedAt: e.startedAt, endsAt: e.endsAt, scanning: true,
      currentChannelMhz: e.currentChannelMhz,
      rows: table(e), truncated: e.truncated,
    };
  }

  const table = (e) => Array.from(e.rows.values()).sort((a, b) => a.ch - b.ch);

  function flush(e) {
    if (e.settled || !e.dirty) return;
    e.dirty = false;
    e.emit('wifiscan:rows', { scanId: e.scanId, ts: now(), rows: table(e), truncated: e.truncated });
  }

  /**
   * The single exit. Every path — natural completion, the wall-clock stop, an
   * abort, a socket disconnect, a session rebuild, a dead connection — arrives
   * here, and it runs its body exactly once. Modelled on the done() guard in
   * POST /api/routers/test, which exists for the same reason: several racing
   * things can each legitimately decide the operation is over.
   */
  function finish(e, reason) {
    if (e.settled) return;
    e.settled = true;
    clock.clearTimeout(e.hardStopTimer);
    clock.clearInterval(e.flushTimer);
    // Skip stop() when the stream ended by itself: RStream.stop() opens a NEW
    // channel to write /cancel with a now-stale tag, which is one more write to
    // a device that has just finished scanning.
    if (!e.finishedNaturally) stopStreamSafe(e.stream);
    if (e.ownerSocketId) cooldowns.set(e.ownerSocketId, now());
    scans.delete(e.routerId);
    e.emit('wifiscan:done', {
      scanId: e.scanId, reason, rows: table(e),
      sampleCount: e.sampleCount, truncated: e.truncated,
    });
  }

  function openStream(e, withDuration) {
    // =.id=, not =number=. The manual documents `number`, and the binary API
    // rejects it outright with "missing =.id=".
    const params = [CMD, '=.id=' + e.ifaceId, PROPLIST];
    if (withDuration) {
      params.push('=duration=' + _hms(e.durationSec));
      params.push('=freeze-frame-interval=00:00:01');
    }
    e.usedDuration = withDuration;

    // Rows arrive through the CALLBACK, not a 'data' listener. On a streaming
    // channel node-routeros emits !re as 'stream' and 'data' never fires, so the
    // listener form silently receives nothing for this command. The callback
    // also takes the trap, which is why errors are handled here rather than on
    // an 'error' event.
    const onPacket = (err, pkt) => {
      if (e.settled) return;
      if (err) return onError(err);
      const rows = Array.isArray(pkt) ? pkt : (pkt ? [pkt] : []);
      for (const raw of rows) {
        const row = parseRow(raw);
        if (!row) continue;
        e.sampleCount++;
        // Latest wins. Each freeze-frame re-sends the whole table, so without
        // keying on channel the graph would grow a duplicate bar every second.
        if (!e.rows.has(row.ch) && e.rows.size >= MAX_CHANNELS) { e.truncated = true; continue; }
        e.rows.set(row.ch, row);
        e.dirty = true;
      }
    };

    const onError = (err) => {
      if (e.settled) return;
      const msg  = err && err.message ? err.message : String(err);
      const code = classifyTrap(msg);
      // Older builds may not know =duration=. Retry once without it and lean
      // entirely on the wall-clock stop; a second such trap is fatal.
      if (code === 'bad-parameter' && e.usedDuration && !e.retriedWithoutDuration) {
        e.retriedWithoutDuration = true;
        stopStreamSafe(e.stream);
        openStream(e, false);
        return;
      }
      e.emit('wifiscan:error', { scanId: e.scanId, code, message: opts.sanitize ? opts.sanitize(err) : msg });
      finish(e, 'error');
    };

    const stream = e.ros.stream(params, onPacket);
    e.stream = stream;
    stream.on('error', onError);
    stream.on('done', () => {
      if (e.settled) return;
      e.finishedNaturally = true;
      finish(e, 'complete');
    });
  }

  /**
   * @param ctx { routerId, ros, iface, durationSec, socketId, emit, interfaces }
   *   `interfaces` is the cached scannable-interface catalogue, or null when the
   *   wireless collector is not running for this router.
   * @returns { ok:true, scanId } | { ok:false, code, ... }
   */
  function start(ctx) {
    const { routerId, ros, iface, durationSec, socketId, emit, interfaces } = ctx;

    if (!routerId || !ros)                    return { ok: false, code: 'unavailable' };
    if (typeof iface !== 'string' || !IFACE_RE.test(iface))
      return { ok: false, code: 'bad-request', message: 'Invalid interface name' };
    if (!DURATIONS.includes(durationSec))
      return { ok: false, code: 'bad-request', message: 'Invalid duration' };

    const running = scans.get(routerId);
    if (running) {
      return { ok: false, code: 'busy', iface: running.iface, endsAt: running.endsAt };
    }
    if (scans.size >= FLEET_CAP) return { ok: false, code: 'fleet-busy' };

    const last = cooldowns.get(socketId);
    // `last !== undefined`, not a truthiness test: a timestamp of 0 is a real
    // value (epoch, or an injected clock in tests) and `if (last)` skips it.
    if (last !== undefined && now() - last < COOLDOWN_MS) {
      return { ok: false, code: 'cooldown', retryAt: last + COOLDOWN_MS };
    }

    // Validated against the catalogue the wireless collector already holds, so
    // this costs no extra RouterOS traffic — and crucially no write(), whose
    // 30s timeout closes the connection shared by every collector.
    if (interfaces === null || interfaces === undefined) return { ok: false, code: 'unavailable' };
    const rec = interfaces.find(i => i.name === iface);
    if (!rec)                return { ok: false, code: 'no-such-interface' };
    if (!rec.id)             return { ok: false, code: 'no-such-interface' };
    if (rec.capsmanManaged)  return { ok: false, code: 'capsman-managed' };
    if (!rec.master)         return { ok: false, code: 'not-a-radio' };
    if (!ros.connected)      return { ok: false, code: 'router-offline' };

    const startedAt = now();
    const e = {
      scanId: 'scan-' + (++seq) + '-' + startedAt,
      routerId, ros, iface, ifaceId: rec.id, durationSec, emit,
      ownerSocketId: socketId,
      startedAt, endsAt: startedAt + durationSec * 1000,
      currentChannelMhz: ctx.currentChannelMhz === undefined ? null : ctx.currentChannelMhz,
      rows: new Map(), sampleCount: 0, truncated: false, dirty: false,
      settled: false, finishedNaturally: false, retriedWithoutDuration: false,
      stream: null, hardStopTimer: null, flushTimer: null,
    };
    scans.set(routerId, e);

    try {
      openStream(e, true);
    } catch (err) {
      scans.delete(routerId);
      return { ok: false, code: 'router-error', message: opts.sanitize ? opts.sanitize(err) : String(err) };
    }

    // Two independent dead-man switches, and they are not redundant.
    // =duration= is the router's: it is the only thing that stops the scan if
    // this process dies mid-scan. This timer is ours, and on the wifi stack it
    // is the one that actually fires — a live 7.23.3 hAP AX3 keeps streaming
    // freeze-frames well past =duration=. So reaching it is the NORMAL end of a
    // timed burst, not an anomaly, and is reported as 'complete'; calling it a
    // timeout would put a warning on every single successful scan.
    e.hardStopTimer = clock.setTimeout(() => finish(e, 'complete'), durationSec * 1000 + graceMs);
    if (e.hardStopTimer && e.hardStopTimer.unref) e.hardStopTimer.unref();

    e.flushTimer = clock.setInterval(() => {
      if (e.settled) return;
      // Doubles as the liveness probe. A router that reboots mid-scan otherwise
      // leaves this entry sitting until the hard stop, blocking a retry.
      if (!e.ros.connected) return finish(e, 'disconnected');
      flush(e);
    }, flushMs);
    if (e.flushTimer && e.flushTimer.unref) e.flushTimer.unref();

    return { ok: true, scanId: e.scanId, endsAt: e.endsAt, startedAt };
  }

  function abort(routerId, scanId) {
    const e = scans.get(routerId);
    if (!e) return false;
    if (scanId && e.scanId !== scanId) return false;
    finish(e, 'aborted');
    return true;
  }

  function abortByOwner(socketId) {
    for (const e of Array.from(scans.values())) {
      if (e.ownerSocketId === socketId) finish(e, 'aborted');
    }
    cooldowns.delete(socketId);
  }

  function abortAllForRouter(routerId, reason) {
    const e = scans.get(routerId);
    if (e) finish(e, reason || 'session-restart');
  }

  return {
    start, abort, abortByOwner, abortAllForRouter, isScanning, stateFor,
    size: () => scans.size,
    DURATIONS,
  };
}

module.exports = { createRegistry, parseRow, classifyTrap, freqToChannel, DURATIONS, MAX_CHANNELS, FLEET_CAP };
