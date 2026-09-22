// The Tools page's bandwidth-test gauges: Receive and Transmit, live.
//
// Two speedometers in the app's rx blue and tx green, driven by each report's
// CURRENT rate (the averages lag a second behind what the link is doing), with
// the whole run's averages written beneath them. The arc and the needle move by
// CSS transition, so a report a second apart glides rather than jumps.
//
// ── THE SCALE FOLLOWS THE PEAK ──────────────────────────────────────────────
//
// A fixed scale is wrong for every link but one: 1 Gbit/s leaves a DSL test as
// a sliver, and 100 Mbit/s pins a fibre one. The scale is the next 1, 2 or 5 ×
// 10ⁿ Mbit/s step at or above the highest rate this run has shown, and it only
// grows during a run, so the needle never pins and the dial never shrinks under
// it.

import { el, fmtMbps } from '../dom';
import { t } from '../i18n';
import type { BtestResult } from '../gen/payloads';

/** The dial's full scale, in Mbit/s: the next 1-2-5 step at or above `peak`. */
export function niceMax(peakMbps: number): number {
  if (!(peakMbps > 0)) return 1;
  let step = Math.pow(10, Math.floor(Math.log10(peakMbps)));
  for (;;) {
    for (const m of [1, 2, 5]) {
      if (m * step >= peakMbps) return m * step;
    }
    step *= 10;
  }
}

let peak = 0;

function setGauge(key: 'Rx' | 'Tx', mbps: number, avgMbps: number, scale: number): void {
  const pct = Math.max(0, Math.min(1, mbps / scale));
  // The arc's pathLength is 100, so the dash offset is the unlit share.
  el('btestArc' + key)?.setAttribute('stroke-dashoffset', (100 - pct * 100).toFixed(1));
  const needle = el('btestNeedle' + key);
  if (needle) needle.style.transform = 'rotate(' + (pct * 180 - 90).toFixed(1) + 'deg)';
  const val = el('btestVal' + key);
  if (val) val.textContent = fmtMbps(mbps);
  const avg = el('btestAvg' + key);
  if (avg) avg.textContent = 'avg ' + fmtMbps(avgMbps);
}

/** Draws the gauges for a test so far; null resets them for a new one. */
export function renderBtestCards(r: BtestResult | null): void {
  const rx = r ? r.rxNowBps / 1e6 : 0, tx = r ? r.txNowBps / 1e6 : 0;
  peak = r ? Math.max(peak, rx, tx) : 0;
  const scale = niceMax(peak);
  for (const id of ['btestScaleRx', 'btestScaleTx']) {
    const s = el(id);
    if (s) s.textContent = fmtMbps(scale);
  }
  setGauge('Rx', rx, r ? r.rxBps / 1e6 : 0, scale);
  setGauge('Tx', tx, r ? r.txBps / 1e6 : 0, scale);
  const lost = el('btestLostVal');
  if (lost) lost.textContent = r ? String(r.lostPackets) : '—';
  const cpu = el('btestCpuVal');
  if (cpu) cpu.textContent = r ? r.localCpu + '% / ' + r.remoteCpu + '%' : '—';
}
