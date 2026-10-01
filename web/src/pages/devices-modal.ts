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

import { el, renderSortHeader, sortRows, type SortState } from '../dom';
import type { Socket } from '../socket';
import type { Live } from '../gen/payloads';
import { detailsHtml } from './devices-card';
import type { DeviceOverview } from './devices-strip';
import type { AlertRow } from './reports-alerts';
import {
  RANGE_MS, alertsHtml, clientRows, clientsBodyHtml, connectivityHtml, headerHtml,
  portsSectionHtml, resourcesHtml, usageHeadHtml, type ClientRow, type Range,
} from './devices-modal-views';
import { pushWanPoints, startWanChart, stopWanChart, wanPoints } from './devices-modal-chart';
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
  overview: DeviceOverview | undefined;
  alerts: AlertRow[] | null | undefined;
  /** Null until the first list arrives; the server resends it only on change. */
  clients: ClientRow[] | null;
}

type Tab = 'clients' | 'alerts';
// Remembered across opens, like the range: an operator who reads alerts first
// keeps landing there.
let tab: Tab = 'clients';
const clientSort: SortState = { col: 'ipSort', dir: 'asc' };
const CLIENT_COLS = [
  { key: 'hostName', label: 'Hostname', style: '' },
  { key: 'ipSort', label: 'IP', style: '' },
  { key: 'mac', label: 'MAC', style: '' },
  { key: 'vlanId', label: 'VLAN', style: '' },
];

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
  const pts = wanPoints();
  set('dvmUsage', usageHeadHtml(st.live, pts[pts.length - 1]));
  const wrap = el('dvmChartWrap');
  if (wrap) wrap.hidden = !pts.length;
  set('dvmRes', resourcesHtml(st.live));
  set('dvmPorts', portsSectionHtml(st.live));
  set('dvmAlerts', alertsHtml(st.alerts));
  paintTabs();
}

/**
 * The Clients / Recent alerts card. A tab whose data this viewer may not read
 * is hidden (DHCP read for clients, Reports for alerts), the chosen tab falls
 * back to the other when that happens, and the card goes when neither is left.
 */
function paintTabs(): void {
  if (!st) return;
  // Before the first frame the DHCP answer is unknown: offer the tab, the
  // frame settles it.
  const clientsOk = st.live ? st.live.clientsAllowed : true;
  const alertsOk = st.alerts !== null;
  const shown: Tab | null = tab === 'clients' && clientsOk ? 'clients'
    : tab === 'alerts' && alertsOk ? 'alerts'
    : clientsOk ? 'clients' : alertsOk ? 'alerts' : null;
  const card = el('dvmTabsCard');
  if (card) card.hidden = shown === null;
  const tc = el('dvmTabClients'), ta = el('dvmTabAlerts');
  if (tc) { tc.hidden = !clientsOk; tc.classList.toggle('active', shown === 'clients'); tc.setAttribute('aria-selected', String(shown === 'clients')); }
  if (ta) { ta.hidden = !alertsOk; ta.classList.toggle('active', shown === 'alerts'); ta.setAttribute('aria-selected', String(shown === 'alerts')); }
  const pc = el('dvmClientsPane'), pa = el('dvmAlerts');
  if (pc) pc.hidden = shown !== 'clients';
  if (pa) pa.hidden = shown !== 'alerts';
  const count = el('dvmClientsCount');
  if (count) count.textContent = st.clients ? String(st.clients.length) : '';
}

/** The client table, redrawn only when the list or the sort changes, so the
 *  operator's scroll position survives the once-a-second repaint. */
function paintClients(): void {
  if (!st) return;
  renderSortHeader('dvmClientsHead', CLIENT_COLS, clientSort, paintClients);
  const body = el('dvmClientsBody');
  const html = clientsBodyHtml(st.clients ? sortRows(st.clients, clientSort.col, clientSort.dir) : null,
    !!st.live && st.live.connected);
  if (body && body.innerHTML !== html) body.innerHTML = html;
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
  stopWanChart();
  st = { id, range: st ? st.range : '24h', live: null, overview: undefined, alerts: undefined, clients: null };
  socketRef.emit('device:peek', id);
  paint();
  paintClients();
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
  stopWanChart();
  if (timer) { clearInterval(timer); timer = null; }
  socketRef?.emit('device:unpeek');
}

/**
 * Merge a live frame. The FIRST frame for a device carries the ring and builds
 * the chart; later ones carry only newer points, which the chart appends and
 * its frame loop scrolls in.
 */
export function applyLive(f: Live): void {
  if (!st || f.routerId !== st.id) return;
  const fresh = st.live === null;
  st.live = f;
  const canvas = el('dvmWanChart');
  if (fresh) {
    if (canvas) startWanChart(canvas, f.points);
  } else {
    pushWanPoints(f.points);
  }
  if (f.clientsSent) {
    st.clients = clientRows(f.clients);
    paintClients();
  } else if (fresh) {
    paintClients(); // connected state known now, for the empty message
  }
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
    const tb = t && t.closest ? t.closest('[data-dvm-tab]') as HTMLElement | null : null;
    if (tb) {
      tab = tb.dataset.dvmTab === 'alerts' ? 'alerts' : 'clients';
      paintTabs();
      return;
    }
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
