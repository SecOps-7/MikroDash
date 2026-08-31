// Two more extra cards, both fed by `routing:update`: Routes (dc-card-routes,
// the count grid and its doughnut) and BGP Peers (dc-card-bgp).
//
// ── `undefined` IS AN ABSENCE AND `null` IS NOT ─────────────────────────────
//
// The count setter tests `v !== undefined`, so a missing key renders an em dash
// and a key that is present and NULL renders the string "null". That is not a
// distinction anyone designed; it is what `!== undefined` does. Reproduced,
// because a payload carrying an explicit null is the only case where the two
// readings differ and the live card really does print it.
//
// ── `connect` IS KNOWN, BUT IT IS NOT A SLICE ───────────────────────────────
//
// The doughnut shows static, dynamic, bgp and ospf. `connect` counts toward
// KNOWN — so it is subtracted from the total when working out what is left over
// — and is deliberately absent from the ring, because it is already shown in the
// count grid beside it. "Other" is therefore genuinely unclassified rather than
// "everything not drawn".
//
// ── AND THE OTHER SLICE ONLY EXISTS WHEN IT IS NON-ZERO ─────────────────────
//
// Not a zero-length slice: the label and colour arrays are shorter, so the
// tooltip and legend have nothing to offer for it at all.

import { el } from '../dom';

export interface RouteCounts {
  connect?: number | null;
  static?: number | null;
  dynamic?: number | null;
  bgp?: number | null;
  ospf?: number | null;
  total?: number | null;
}
export interface RoutingPayload {
  routeCounts?: RouteCounts;
  summary?: { total?: number | null; established?: number | null; down?: number | null };
}

interface ChartLike {
  data: { labels: string[]; datasets: { data: number[]; backgroundColor: string[] }[] };
  update(mode?: string): void;
  destroy(): void;
}
declare const Chart: undefined | (new (canvas: HTMLElement, cfg: unknown) => ChartLike);

const DONUT_COLOURS: Record<string, string> = {
  static: 'rgba(56,189,248,.85)', dynamic: 'rgba(251,191,36,.85)',
  bgp: 'rgba(167,139,250,.85)', ospf: 'rgba(251,113,133,.85)', other: 'rgba(99,130,190,.4)',
};
const DONUT_LABELS: Record<string, string> = {
  static: 'Static', dynamic: 'Dynamic', bgp: 'BGP', ospf: 'OSPF', other: 'Other',
};

let donut: ChartLike | null = null;
// Read by the centre-text plugin at DRAW time, not at construction — which is
// why it is module state and not a closure over the first payload.
let donutTotal = 0;

/** The count grid's setter: absent is an em dash, present-and-null is "null". */
function setCount(id: string, v: number | null | undefined): void {
  const node = el(id);
  if (node) node.textContent = v !== undefined ? String(v) : '—';
}

export function donutSlices(rc: RouteCounts): { keys: string[]; vals: number[]; colours: string[]; labels: string[] } {
  const keys = ['static', 'dynamic', 'bgp', 'ospf'];
  const known = keys.reduce((a, k) => a + (rc[k as keyof RouteCounts] || 0), 0) + (rc.connect || 0);
  // `Math.max(0, …)` is REDUNDANT and reproduced anyway: the slice is gated on
  // `other > 0` below, which already rejects a negative. Measured — a mutation
  // removing the clamp changes nothing observable. It stays because the original
  // has it and because it states the intent at the point the subtraction is
  // written, rather than three lines later.
  const other = Math.max(0, (rc.total || 0) - known);
  const dataKeys = keys.concat(other > 0 ? ['other'] : []);
  const vals = keys.map((k) => rc[k as keyof RouteCounts] || 0).concat(other > 0 ? [other] : []);
  return {
    keys: dataKeys,
    vals,
    colours: dataKeys.map((k) => DONUT_COLOURS[k]!),
    labels: dataKeys.map((k) => DONUT_LABELS[k] || k),
  };
}

/** The centre text. Exported so the gate can drive it without a real Chart.js. */
export function drawDonutCentre(chart: {
  ctx: CanvasRenderingContext2D;
  chartArea: { left: number; right: number; top: number; bottom: number };
}): void {
  const ctx = chart.ctx;
  const cx = (chart.chartArea.left + chart.chartArea.right) / 2;
  const cy = (chart.chartArea.top + chart.chartArea.bottom) / 2;
  ctx.save();
  ctx.font = "bold 26px 'JetBrains Mono',ui-monospace,monospace";
  ctx.fillStyle = getComputedStyle(document.documentElement).getPropertyValue('--text-main').trim()
    || 'rgba(200,215,240,.9)';
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  // `|| '—'` on a NUMBER: zero routes shows a dash, not a nought.
  ctx.fillText(String(donutTotal || '—'), cx, cy);
  ctx.restore();
}

export function donutConfig(rc: RouteCounts): unknown {
  const s = donutSlices(rc);
  return {
    type: 'doughnut',
    data: {
      labels: s.labels,
      datasets: [{
        data: s.vals, backgroundColor: s.colours,
        borderWidth: 1, borderColor: 'rgba(0,0,0,.15)', hoverOffset: 4,
      }],
    },
    options: {
      responsive: false, cutout: '68%',
      animation: { duration: 400 },
      plugins: {
        legend: { display: false },
        tooltip: { callbacks: { label: (c: { label: string; parsed: number }) => ' ' + c.label + ': ' + c.parsed } },
      },
    },
    plugins: [{ afterDraw: drawDonutCentre }],
  };
}

export function updateDonut(rc: RouteCounts): void {
  const canvas = el('dc-rtDonutCanvas');
  if (!canvas) return;
  donutTotal = rc.total || 0;
  if (!donut) {
    if (typeof Chart === 'undefined') return;
    donut = new Chart(canvas, donutConfig(rc));
    return;
  }
  const s = donutSlices(rc);
  donut.data.labels = s.labels;
  donut.data.datasets[0]!.data = s.vals;
  donut.data.datasets[0]!.backgroundColor = s.colours;
  donut.update('none');
}

export function renderRoutingCards(data: RoutingPayload): void {
  const rc = data.routeCounts || {};
  setCount('dc-rtConnect', rc.connect);
  setCount('dc-rtStatic', rc.static);
  setCount('dc-rtDynamic', rc.dynamic);
  setCount('dc-rtBgp', rc.bgp);
  setCount('dc-rtOspf', rc.ospf);
  updateDonut(rc);

  const sm = data.summary || {};
  setCount('dc-rtBgpTotal', sm.total);
  setCount('dc-rtBgpEstab', sm.established);
  setCount('dc-rtBgpDown', sm.down);
}

/** A switch to another router must rebuild the ring, not mutate the old one. */
export function resetRoutingCards(): void {
  if (donut) { donut.destroy(); donut = null; }
  donutTotal = 0;
}
