// The device modal: a mini dashboard for one device, opened from the Devices
// page's cards, list rows and map, streaming that device's live readings only
// while it is open - and never changing the router the top bar has selected.
//
// ── PEEK ON OPEN, UNPEEK ON EVERY KIND OF CLOSE ────────────────────────────
//
// The modal closes four ways: its × (`data-modal-close`), Escape and a backdrop
// click (both `modals.ts`, through CLOSABLE_MODALS), and the page changing. The
// first three only remove the `open` class, so the class is what is watched: a
// MutationObserver turns "no longer open" into one `device:unpeek`, whichever
// path removed it. The server also unpeeks on a Devices blur and on teardown,
// so a closed tab cannot leave a stream running.
//
// ── FRAMES FOR ANOTHER DEVICE ARE IGNORED ──────────────────────────────────
//
// Switching device is unpeek-then-peek, and a frame for the old one can still
// be in flight. Each frame names its router; one that is not the open device is
// dropped rather than drawn under the wrong name.

import { el } from '../dom';
import type { Socket } from '../socket';
import type { Live, TrafficPoint } from '../gen/payloads';
import { detailsHtml } from './devices-card';
import type { DeviceOverview } from './devices-strip';
import type { AlertRow } from './reports-alerts';
import {
  CHART_MS, RANGE_MS, alertsHtml, connectivityHtml, headerHtml, portsSectionHtml,
  resourcesHtml, usageHtml, type Range,
} from './devices-modal-views';
import { lastRows, onOpenDevice, overviewOf } from './routers';

export interface DeviceModalDeps {
  /** Select this router and go to its dashboard. */
  openDashboard: (id: string) => void;
  /** Whether this viewer may edit routers - asked at paint, since caps change. */
  canEdit: () => boolean;
  /** Open the router editor. */
  edit: (id: string) => void;
}

const OVERVIEW_EVERY_MS = 60_000;
const ALERTS_WINDOW_MS = 7 * 24 * 3600_000;

interface State {
  id: string;
  range: Range;
  live: Live | null;
  points: TrafficPoint[];
  overview: DeviceOverview | undefined;
  alerts: AlertRow[] | null | undefined;
}

let st: State | null = null;
let socketRef: Socket | null = null;
let depsRef: DeviceModalDeps | null = null;
let timer: ReturnType<typeof setInterval> | null = null;

/** The open device, for tests. */
export function openDeviceId(): string { return st ? st.id : ''; }

function rowOf(id: string) { return lastRows().find((r) => r.id === id); }

/** Paint every section from the held state. */
function paint(): void {
  if (!st) return;
  const r = rowOf(st.id);
  const now = Date.now();
  const set = (id: string, html: string): void => {
    const e = el(id);
    if (e && e.innerHTML !== html) e.innerHTML = html;
  };
  if (r) {
    set('dvmHdr', headerHtml(r, !!depsRef && depsRef.canEdit()));
    // The 24h overview the page already holds serves the rail's backup line.
    set('dvmDetails', detailsHtml(r, overviewOf(st.id), now));
  }
  set('dvmConn', connectivityHtml(st.overview, st.range));
  set('dvmUsage', usageHtml(st.live, st.points, now));
  set('dvmRes', resourcesHtml(st.live));
  set('dvmPorts', portsSectionHtml(st.live));
  const alerts = el('dvmAlerts');
  if (alerts) alerts.hidden = st.alerts === null;
  set('dvmAlerts', alertsHtml(st.alerts));
}

async function loadOverview(): Promise<void> {
  if (!st) return;
  const { id, range } = st;
  const to = Date.now();
  try {
    const res = await fetch('/api/devices/overview?routerId=' + encodeURIComponent(id)
      + '&from=' + (to - RANGE_MS[range]) + '&to=' + to);
    if (!res.ok) return;
    const rows = await res.json() as DeviceOverview[];
    // Still the same question? A range click or a new device may have moved on.
    if (!st || st.id !== id || st.range !== range) return;
    st.overview = Array.isArray(rows) ? rows.find((o) => o.routerId === id) : undefined;
    paint();
  } catch { /* keep what is drawn */ }
}

async function loadAlerts(): Promise<void> {
  if (!st) return;
  const id = st.id;
  const to = Date.now();
  try {
    const res = await fetch('/api/reports/alerts?routerId=' + encodeURIComponent(id)
      + '&from=' + (to - ALERTS_WINDOW_MS) + '&to=' + to);
    if (!st || st.id !== id) return;
    // A viewer without Reports gets no section rather than an error in one.
    if (!res.ok) { st.alerts = null; paint(); return; }
    const d = await res.json() as { rows?: AlertRow[] };
    if (!st || st.id !== id) return;
    st.alerts = d.rows || [];
    paint();
  } catch { if (st && st.id === id) { st.alerts = null; paint(); } }
}

/** Open the modal on one device. Opening another moves the stream. */
export function openDeviceModal(id: string): void {
  const modal = el('deviceModal');
  if (!modal || !socketRef) return;
  if (st && st.id === id && modal.classList.contains('open')) return;
  if (st && st.id !== id) socketRef.emit('device:unpeek');
  st = { id, range: st ? st.range : '24h', live: null, points: [], overview: undefined, alerts: undefined };
  socketRef.emit('device:peek', id);
  paint();
  modal.classList.add('open');
  void loadOverview();
  void loadAlerts();
  if (timer) clearInterval(timer);
  timer = setInterval(() => { void loadOverview(); }, OVERVIEW_EVERY_MS);
  (modal.querySelector('.rtr-modal-close') as HTMLElement | null)?.focus?.();
}

/** Close it, from code. The observer below does the unpeek. */
export function closeDeviceModal(): void {
  el('deviceModal')?.classList.remove('open');
}

/** What every close path converges on. */
function closed(): void {
  if (!st) return;
  st = null;
  if (timer) { clearInterval(timer); timer = null; }
  socketRef?.emit('device:unpeek');
}

/** Merge a live frame: newer points are appended, the ring trimmed to the chart. */
export function applyLive(f: Live, now = Date.now()): void {
  if (!st || f.routerId !== st.id) return;
  const fresh = st.live === null;
  st.live = f;
  st.points = fresh ? f.points.slice() : st.points.concat(f.points);
  const from = now - CHART_MS;
  st.points = st.points.filter((p) => p.ts >= from);
  paint();
}

export function mountDeviceModal(socket: Socket, deps: DeviceModalDeps): void {
  socketRef = socket;
  depsRef = deps;
  onOpenDevice(openDeviceModal);
  socket.on('device:live', (f) => applyLive(f));
  // The page's 2s rows carry the header and the rail: repaint from them.
  socket.on('routers:stats', () => { if (st) paint(); });

  const modal = el('deviceModal');
  if (!modal) return;
  new MutationObserver(() => {
    if (!modal.classList.contains('open')) closed();
  }).observe(modal, { attributes: true, attributeFilter: ['class'] });

  modal.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const r = t && t.closest ? t.closest('[data-range]') as HTMLElement | null : null;
    if (r && st) {
      const range = r.dataset.range as Range;
      if (range !== st.range && RANGE_MS[range]) {
        st.range = range;
        st.overview = undefined;
        paint();
        void loadOverview();
      }
      return;
    }
    const a = t && t.closest ? t.closest('[data-dvm]') as HTMLElement | null : null;
    if (!a || !st) return;
    const id = st.id;
    if (a.dataset.dvm === 'dashboard') { closeDeviceModal(); deps.openDashboard(id); }
    if (a.dataset.dvm === 'edit' && deps.canEdit()) { closeDeviceModal(); deps.edit(id); }
  });

  // Leaving the Devices page closes it: the server has already unpeeked on blur.
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'devices') closeDeviceModal();
  });
}
