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

module.exports = { clampPoll, stopStreamSafe, parseBps, bpsToMbps };
