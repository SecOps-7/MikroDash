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

module.exports = { clampPoll, stopStreamSafe, parseBps, bpsToMbps, createStreamHealth };
