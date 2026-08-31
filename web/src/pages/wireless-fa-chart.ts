/**
 * The Frequency Analyser's spectrum CANVAS.
 *
 * ── THIS IS GLUE, AND THAT IS THE WHOLE POINT ──────────────────────────────
 *
 * Every decision belongs to `wireless-fa.ts` and is gated there: `spectrumConfig`
 * and `spectrumData` against the live `makeChart` (`tools/fa-chart-check.js`),
 * the tooltip body and the band geometry (`tools/fa-spectrum-check.js`). What is
 * left — and what stayed unported longest — is constructing a Chart.js instance
 * against an element and registering a plugin. That needs a browser and a
 * charting library rather than a decision, which is why it is here and why it is
 * thin.
 *
 * ── IT NEVER STARTS A SCAN ─────────────────────────────────────────────────
 *
 * Worth stating plainly, because the item this closes is the one carrying the
 * warning: a spectral scan takes a radio OFF THE AIR and drops every client on
 * it. That is `/interface/wireless/spectral-scan`, issued by the scan button and
 * the server; nothing in this file asks for one. It draws rows that have already
 * arrived.
 */

import { el } from '../dom';
import {
  spectrumConfig, spectrumData, spectrumBandGeometry, FA_BAND_LEGEND,
  type FaRow, type LegendItem,
} from './wireless-fa';

interface ChartArea { top: number; bottom: number }
interface ChartCtx {
  save(): void;
  restore(): void;
  fillRect(x: number, y: number, w: number, h: number): void;
  strokeRect(x: number, y: number, w: number, h: number): void;
  fillStyle: string;
  strokeStyle: string;
  lineWidth: number;
}
interface ChartLike {
  destroy(): void;
  update(mode?: string): void;
  data: {
    labels: (number | null)[];
    datasets: { data: unknown[]; backgroundColor?: string[] }[];
  };
  ctx: ChartCtx;
  chartArea: ChartArea;
  scales: { x: { getPixelForValue(i: number): number } };
  getDatasetMeta(i: number): { data?: { x: number; width?: number }[] };
}
declare const Chart: undefined | (new (canvas: HTMLElement, cfg: unknown) => ChartLike);

let chart: ChartLike | null = null;

/**
 * The band marking the radio's OWN channel.
 *
 * `beforeDatasetsDraw`, so it lands UNDER the bars. The live comment says what
 * that replaced: colouring the current channel's bar blue, which marked it "at
 * the cost of the one congestion colour you most want to see — your own".
 *
 * The geometry is `spectrumBandGeometry`, which is gated: the width comes from
 * the BAR rather than the category, because Chart.js insets bars within their
 * category and a category-wide band sits visibly proud of them.
 */
function bandPlugin(currentChannelMhz: () => number | null) {
  return {
    id: 'faChannelBand',
    beforeDatasetsDraw(c: ChartLike): void {
      const mhz = currentChannelMhz();
      if (mhz == null) return;
      const idx = c.data.labels.indexOf(mhz);
      if (idx < 0) return;
      const bar = (c.getDatasetMeta(0).data || [])[idx] || null;
      const xs = c.scales.x;
      // The live guard is `if (!el && !xs) return` — both missing. With a scale
      // but no laid-out bar the fallback geometry applies, which is the case
      // that exists on the very first frame.
      if (!bar && !xs) return;
      const g = spectrumBandGeometry(bar, c.data.labels.length, (i) => xs.getPixelForValue(i), idx);
      const a = c.chartArea;
      const ctx = c.ctx;
      ctx.save();
      ctx.fillStyle = 'rgba(56,189,248,.20)';
      ctx.fillRect(g.x - g.w / 2, a.top, g.w, a.bottom - a.top);
      ctx.strokeStyle = 'rgba(56,189,248,.85)';
      ctx.lineWidth = 1.5;
      ctx.strokeRect(g.x - g.w / 2, a.top, g.w, a.bottom - a.top);
      ctx.restore();
    },
  };
}

/**
 * Build the chart, replacing any previous one.
 *
 * DESTROYED FIRST. Chart.js keeps a registry keyed by canvas, and constructing a
 * second instance over a live one leaves the first attached to the same element —
 * two charts drawing the same pixels, and the tooltip belonging to whichever
 * registered last.
 */
export function makeSpectrumChart(deps: {
  rows: () => FaRow[];
  currentChannelMhz: () => number | null;
  legendLabels: (chart: unknown) => LegendItem[];
  legendClick: (e: unknown, item: LegendItem, legend: unknown) => void;
}): void {
  if (chart) {
    chart.destroy();
    chart = null;
  }
  const canvas = el('faSpectrum');
  // NO CHART.JS IS A SUPPORTED STATE, not an error. The dialog's stat boxes and
  // channel grid are rendered from the same rows and are the parts that answer
  // "which channel should I use" — the live code returns here too rather than
  // failing the whole dialog.
  if (!canvas || typeof Chart === 'undefined') return;
  chart = new Chart(canvas, {
    ...spectrumConfig(deps),
    plugins: [bandPlugin(deps.currentChannelMhz)],
  });
}

/**
 * Push the current rows into the chart.
 *
 * `update('none')` — no animation. The rows are replaced wholesale on every scan
 * frame, and an animated transition between two unrelated spectra reads as the
 * chart lagging rather than as motion.
 */
export function renderSpectrum(rows: FaRow[]): void {
  if (!chart) return;
  const d = spectrumData(rows);
  const [signal, noise] = chart.data.datasets;
  // BOTH DATASETS OR NEITHER. `spectrumConfig` declares exactly two, so a
  // missing one means the config and this writer have drifted — and writing only
  // the bars would leave the noise floor showing the PREVIOUS scan's line under
  // the current one's bars, which reads as a real measurement.
  if (!signal || !noise) return;
  chart.data.labels = d.labels;
  signal.data = d.signal;
  signal.backgroundColor = d.colours;
  noise.data = d.noise;
  chart.update('none');
}

/** Tear the chart down when the dialog closes. */
export function destroySpectrumChart(): void {
  if (!chart) return;
  chart.destroy();
  chart = null;
}

/** The legend item the band plugin has no dataset for. Re-exported for the mount. */
export { FA_BAND_LEGEND };
