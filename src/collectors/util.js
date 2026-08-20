'use strict';
// Shared collector helpers — deduplicates the label builder, pollMs clamp,
// promise-safe stream teardown and bps parsing that were previously copied
// into each collector (with drifting variants).

/**
 * Clamp a poll interval to [lo, hi] ms, falling back to `def` when the input
 * is not numeric. Every collector previously inlined a variant of this.
 */
function clampPoll(raw, def, hi = 60000, lo = 500) {
  const n = Number.isFinite(Number(raw)) ? Math.trunc(Number(raw)) : def;
  return Math.max(lo, Math.min(hi, n));
}

/**
 * Stop an RStream without leaking a rejection: stop() returns a promise that
 * rejects when the /cancel write fails (e.g. connection already gone), and a
 * plain try/catch cannot catch that. Null/undefined streams are a no-op.
 */
function stopStreamSafe(stream) {
  if (!stream) return;
  try {
    const p = stream.stop();
    if (p && typeof p.catch === 'function') p.catch(() => {});
  } catch (_) {}
}

/** Parse RouterOS rate strings ('12.3Mbps', '512kbps', raw bps) to bps. */
function parseBps(val) {
  if (!val || val === '0') return 0;
  const s = String(val);
  if (s.endsWith('kbps') || s.endsWith('Kbps')) return parseFloat(s) * 1000;
  if (s.endsWith('Mbps') || s.endsWith('mbps')) return parseFloat(s) * 1_000_000;
  if (s.endsWith('Gbps') || s.endsWith('gbps')) return parseFloat(s) * 1_000_000_000;
  if (s.endsWith('bps')) return parseFloat(s);
  return parseInt(s, 10) || 0;
}

/** bps → Mbps rounded to 3 decimals (single precision everywhere). */
function bpsToMbps(bps) {
  return +((bps || 0) / 1_000_000).toFixed(3);
}

/**
 * Tracks watchdog restarts so a stream that never recovers can be reported to
 * the user instead of being silently restarted forever.
 *
 * The subtlety is what counts as "recovered". A stream that dies every 15 s
 * still delivers a burst of rows immediately after each restart, so resetting
 * the counter the moment data appears would mean it never climbs and the fault
 * stays invisible. Recovery therefore requires the stream to have been up for
 * `healthyMs`, not merely to have produced a packet.
 *
 * record* returns null when the degraded state did not change, and the new
 * boolean when it did, so callers emit only on a transition.
 */
function createStreamHealth({ degradeAfter = 3, healthyMs = 60000 } = {}) {
  let restarts = 0;
  let degraded = false;
  let since    = 0;

  return {
    /** Watchdog had to restart the stream. */
    recordRestart() {
      restarts++;
      if (degraded || restarts < degradeAfter) return null;
      degraded = true;
      since = Date.now();
      return true;
    },
    /** Watchdog tick found data flowing; streamAgeMs is how long it has been up. */
    recordHealthy(streamAgeMs) {
      if (!(streamAgeMs >= healthyMs)) return null;   // not up long enough to count
      if (!degraded && restarts === 0) return null;
      const was = degraded;
      restarts = 0;
      degraded = false;
      since = 0;
      return was ? false : null;
    },
    /** Drop all state (stream stopped deliberately, or the router reconnected). */
    reset() { restarts = 0; degraded = false; since = 0; },
    get degraded() { return degraded; },
    get restarts() { return restarts; },
    get since()    { return since; },
  };
}

/**
 * A self-rescheduling poll loop, shared by every collector that gained a poll
 * path in #105 rather than each growing its own timer, inflight guard and
 * clamp (the drift that produced the variants clampPoll was written to unify).
 *
 * Recursive setTimeout, not setInterval: the next delay is measured from the end
 * of the previous run, so a slow reply cannot queue overlapping requests at a
 * router that is already struggling — which is the whole reason poll mode
 * exists.
 *
 *   run()        async; one poll. Its rejections are swallowed here, so the
 *                caller owns error reporting.
 *   getDelayMs() read per tick, so an interval change applies without a restart.
 */
function createPollLoop(run, getDelayMs) {
  let timer = null, inflight = false, stopped = true, lastRun = 0;

  const delayMs = () => {
    const raw = Number(getDelayMs()) || 1000;
    return Math.max(500, Math.min(600000, raw));
  };
  const schedule = (ms) => {
    if (timer || stopped) return;
    timer = setTimeout(tick, ms === undefined ? delayMs() : ms);
  };
  const tick = async () => {
    timer = null;
    if (stopped) return;
    if (!inflight) {
      inflight = true;
      lastRun = Date.now();
      try { await run(); } catch (_) { /* caller reports; never kill the loop */ }
      finally { inflight = false; }
    }
    schedule();
  };

  return {
    // Poll as soon as delivery starts rather than one whole interval later. A
    // streamed `print =interval=N` hands back its first batch immediately, so the
    // old behaviour left every page-gated poll collector blank for a full period
    // on each visit — 30 s for wireless, which is exactly what "the wireless table
    // is empty" looked like.
    //
    // Still bounded: poll mode exists to be gentle on small hardware, and page
    // navigation calls suspend()/resume() freely, so a restart inside the current
    // interval waits out the remainder instead of firing another request.
    start() {
      if (!stopped) return;
      stopped = false;
      const since = Date.now() - lastRun;
      const wait  = delayMs();
      schedule(since >= wait ? 0 : wait - since);
    },
    stop()  { stopped = true; if (timer) { clearTimeout(timer); timer = null; } },
    get running() { return !stopped; },
    get pending() { return timer !== null; },
  };
}

/**
 * A /listen channel that says "something changed", nothing more.
 *
 * Written for the collectors that read SEVERAL tables per tick, where the
 * netwatch shape — one /listen whose rows ARE the state — does not fit. Here the
 * stream carries no data into the payload at all: an event marks the cached
 * tables stale and asks for a refresh, and the existing tick does the reading.
 * One parsing path, two delivery mechanisms, exactly as poll mode.
 *
 * What each mode actually buys, since it is easy to assume wrongly:
 *   stream  an open channel, and changes appear the moment the router makes
 *           them instead of up to one interval later
 *   poll    no channel at all — which is the point of #105, because concurrent
 *           channels, not data volume, are what strain a small router
 *
 *   ros, cmd   the /listen command, e.g. '/interface/bridge/port/listen'
 *   label      log prefix, already router-scoped by the caller
 *   onEvent()  called on every change; also called once on a stream restart, so
 *              a caller that missed events while the channel was down recovers
 */
function createListenRefresh({ ros, cmd, label, onEvent, retryMs = 3000 }) {
  let stream = null, restartTimer = null, restarting = false, stopped = true;

  const stop = () => {
    stopped = true;
    if (restartTimer) { clearTimeout(restartTimer); restartTimer = null; }
    restarting = false;
    if (stream) { stopStreamSafe(stream); stream = null; }
  };

  const start = () => {
    stopped = false;
    if (stream || !ros.connected) return;
    try {
      stream = ros.stream([cmd], (err) => {
        if (err) {
          // A dead channel silently stops delivering, which looks exactly like
          // "nothing has changed". Restart it, and refresh on the way back up.
          if (stream) { stopStreamSafe(stream); stream = null; }
          if (stopped || restarting || !ros.connected) return;
          restarting = true;
          restartTimer = setTimeout(() => {
            restarting = false; restartTimer = null;
            if (stopped || !ros.connected) return;
            start();
            try { onEvent(); } catch (_) { /* caller reports */ }
          }, retryMs);
          return;
        }
        try { onEvent(); } catch (_) { /* caller reports */ }
      });
      console.log('%s', label + ' streaming ' + cmd);
    } catch (e) {
      console.error('%s', label + ' listen failed:', (e && e.message) || e);
    }
  };

  return { start, stop, get open() { return !!stream; } };
}

/**
 * Decides when a collector with nothing to report should stop holding a channel
 * open, and when to look again.
 *
 * The distinction that matters is between "this router cannot do it" and "this
 * router has nothing configured". The first is a command error — durable, only
 * changed by a RouterOS upgrade or a package install — so it sleeps straight to
 * the long delay. The second is ordinary and transient: a router with no
 * netwatch hosts today may have three tomorrow, so it sleeps only after
 * `emptyThreshold` consecutive empties and re-probes on a doubling delay.
 *
 * Two subtleties, both learned the hard way in talkers.js:
 *
 *   A streak counts distinct PAYLOADS, not ticks. The supervisor polls every
 *   15 s while dhcpNetworks emits every 10 minutes, so counting ticks would
 *   condemn a slow collector on the strength of one observation. `ts` is the
 *   discriminator, which is why every collector stamps it.
 *
 *   A probe can produce nothing at all — a collector that suppresses an
 *   unchanged emit never advances `ts`, and waiting for a verdict that cannot
 *   arrive would strand it re-probing forever. `probeTimeoutMs` settles that
 *   case as "still asleep" rather than leaving it undecided.
 *
 * observe() returns 'sleep' | 'wake' | null, so the caller emits only on a
 * transition rather than on every tick.
 */
/**
 * Is every list this collector declares empty?
 *
 * `emptyKey` is a payload field name, or several — several meaning "empty only
 * if ALL are empty", because a VPN with no WireGuard peers but three IPsec SAs
 * has plenty to show.
 *
 * Returns false when the payload carries none of the named lists, which is NOT
 * the same as empty: a collector mid-construction, or one whose payload shape
 * moved under the registry, must not be condemned on a payload we cannot read.
 */
function payloadEmpty(payload, emptyKey) {
  if (!payload || !emptyKey) return false;
  const keys = Array.isArray(emptyKey) ? emptyKey : [emptyKey];
  let readable = false;
  for (const k of keys) {
    const v = payload[k];
    if (!Array.isArray(v)) continue;
    readable = true;
    if (v.length > 0) return false;
  }
  return readable;
}

function createDormancyState({
  emptyThreshold = 3,
  backoffMs      = 60000,
  maxBackoffMs   = 600000,
  probeTimeoutMs = 30000,
  restampMs      = 45000,
} = {}) {
  let streak = 0, dormant = false, probing = false;
  let lastTs = 0, wakeAt = 0, probeDeadline = 0, lastCountedAt = 0;
  let delay = backoffMs;

  const sleepFor = (ms, now) => { dormant = true; streak = 0; delay = ms; wakeAt = now + ms; };
  const backOff  = (now) => { probing = false; delay = Math.min(delay * 2, maxBackoffMs); wakeAt = now + delay; };

  return {
    /**
     * Judge one observation: { ts, empty, unsupported }. A repeated `ts` means
     * the collector has produced nothing new and is not evidence either way.
     */
    observe(obs, now = Date.now()) {
      if (!obs || !obs.ts) return null;
      const bad = obs.unsupported === true || obs.empty === true;

      // A payload that has STOPPED advancing is not "no information" when it is
      // empty. Five collectors — netwatch, vpn, firewall, routing, topology —
      // heartbeat by emitting `{ ...lastPayload, ts: Date.now() }` to the browser
      // and never reassign lastPayload, so their ts freezes the moment the data
      // settles. Requiring a fresh ts meant dormancy could never fire for any of
      // them: it worked for the 9 poll-loop collectors and silently skipped the
      // rest.
      //
      // Still rate-limited rather than counted every tick, which is what the
      // distinct-ts rule was protecting: a 10-minute collector must not be
      // condemned by a supervisor ticking every 15 s. One observation held empty
      // for restampMs is the evidence, not the tick that noticed it.
      const fresh = obs.ts !== lastTs;
      if (!fresh) {
        if (!bad || dormant) return null;
        if (!lastCountedAt || (now - lastCountedAt) < restampMs) return null;
      }
      lastTs = obs.ts;
      lastCountedAt = now;

      if (!obs.unsupported && !obs.empty) {
        streak = 0; delay = backoffMs; probing = false;
        if (!dormant) return null;
        dormant = false; wakeAt = 0;
        return 'wake';
      }
      // Still nothing to report. If we were already asleep this is a probe that
      // came back empty, so lengthen the delay rather than re-announcing sleep.
      if (dormant) { backOff(now); return null; }
      if (obs.unsupported) { sleepFor(maxBackoffMs, now); return 'sleep'; }
      if (++streak < emptyThreshold) return null;
      sleepFor(backoffMs, now);
      return 'sleep';
    },

    /** Is it time to look again? Also settles a probe that never reported back. */
    dueForProbe(now = Date.now()) {
      if (!dormant) return false;
      if (probing) {
        if (now < probeDeadline) return false;
        backOff(now);            // probe produced no fresh payload — still asleep
        return false;
      }
      return now >= wakeAt;
    },

    /** Caller has just re-probed; hold off until it reports or times out. */
    markProbed(now = Date.now()) { probing = true; probeDeadline = now + probeTimeoutMs; },

    /** Router reconnected or the session was rebuilt — forget everything. */
    reset() {
      streak = 0; dormant = false; probing = false;
      lastTs = 0; wakeAt = 0; probeDeadline = 0; lastCountedAt = 0; delay = backoffMs;
    },

    get dormant() { return dormant; },
    get probing() { return probing; },
    get streak()  { return streak; },
    get delayMs() { return delay; },
    get wakeAt()  { return wakeAt; },
  };
}

module.exports = { clampPoll, stopStreamSafe, parseBps, bpsToMbps, createStreamHealth,
                   createPollLoop, createListenRefresh, createDormancyState, payloadEmpty };
