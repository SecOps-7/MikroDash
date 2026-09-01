// Reports — the range controls, the tab bar, and the formatters every tab shares.
//
// ── THIS IS THE FIRST PAGE WITH AN HTTP API RATHER THAN A SOCKET ────────────
//
// Every ported page so far is fed by the WebSocket. Reports is request/response:
// the operator picks a range and presses Load. The endpoints are the Go ones at
// `/next/api/reports/*` — see internal/server/reports.go for why they are not at
// `/api/reports/*` yet.
//
// ── THE TIMEZONE RULES ARE THE ORIGINAL'S, INCLUDING THE INCONSISTENT ONE ───
//
// With `displayTimezone` set, timestamps are formatted in THAT zone. With it
// unset they are formatted in the BROWSER's zone — not UTC. Those are different
// answers and the live page gives both, depending on a setting. The port runs in
// the same browser with the same Date, so both are reproduced by construction
// rather than by arithmetic; there is nothing here to get wrong except the choice
// of branch.
//
// The preset arithmetic below is likewise all local time: `setHours(0,0,0,0)` is
// midnight where the OPERATOR is, which is what "today" has to mean on a page
// whose other end is a date picker.

import { esc, el } from '../dom';
import { renderPing, renderConn, wirePingPager, type PingRow, type ConnRow } from './reports-ping';
import {
  renderTraffic, renderBandwidth, wireBwPager,
  type TrafficRow, type BandwidthRow, type IfaceSummary,
} from './reports-traffic';
import { renderAlerts, wireAlertAck, type AlertRow } from './reports-alerts';
import { wireCapacityToggle } from './reports-charts';
import { loadSchedules, wireScheduleActions, wireScheduleForm } from './reports-schedules';


/** The saved preset survives a reload, so a range does not have to be re-picked. */
// THE LIVE APP'S KEY, EXACTLY (`../MikroDash/public/app.js:9583`). This read
// `'mikrodash.rpt.preset'` until 2026-08-25 — a name in this port's own style,
// and wrong: at cutover every operator's saved Reports preset would have been
// invisible, the page silently falling back to `last7d`. Nothing breaks, nothing
// logs, and the setting the operator chose is simply not there any more.
//
// A storage key is a CONTRACT WITH THE PAST, not an internal name. The live
// app's spelling wins even where it is inconsistent — `mkd_` here, `mikrodash_`
// two lines below in `RPT_CAP_KEY`. The storage-key audit compares every
// key against the live source for exactly this reason.
const RPT_PRESET_KEY = 'mkd_rpt_preset';

/** Set from the settings payload; empty means "use the browser's zone". */
let displayTimezone = '';

export function setReportTimezone(tz: string): void {
  displayTimezone = tz || '';
}

const p2 = (n: number): string => String(n).padStart(2, '0');

// ── Formatters ──────────────────────────────────────────────────────────────

/** A timestamp for a table cell. Em dash for a missing one, never "1970". */
export function fmtTs(ts: number): string {
  if (!ts) return '—';
  if (displayTimezone) {
    // 'sv-SE' gives '2026-01-01 00:00:00'; some engines put a T in, which the
    // original normalises away rather than depending on its absence.
    return new Intl.DateTimeFormat('sv-SE', {
      timeZone: displayTimezone,
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    }).format(new Date(ts)).replace('T', ' ');
  }
  const d = new Date(ts);
  return d.getFullYear() + '-' + p2(d.getMonth() + 1) + '-' + p2(d.getDate()) + ' ' +
    p2(d.getHours()) + ':' + p2(d.getMinutes()) + ':' + p2(d.getSeconds());
}

/**
 * A duration, coarsened as it grows: hours drop the seconds, minutes keep them.
 *
 * `!ms` catches zero as well as null, so an outage under a millisecond reads as
 * "—" rather than "0s". That is the original's behaviour and it is the right
 * one: a zero-length outage is a rounding artefact, not an event.
 */
export function fmtDuration(ms: number | null): string {
  if (!ms || ms < 0) return '—';
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return h + 'h ' + m + 'm';
  if (m > 0) return m + 'm ' + sec + 's';
  return sec + 's';
}

const HOUR = 3600000;
const DAY = 86400000;

/**
 * An X-axis tick label, scaled to the visible span:
 *   ≤ 12h → HH:MM      ≤ 3d → MM-DD HH:MM      > 3d → MM-DD
 *
 * The thresholds are the original's. A two-day chart labelled HH:MM repeats
 * every tick, and a six-hour chart labelled MM-DD says nothing at all.
 */
export function chartLabel(ts: number, spanMs: number): string {
  if (displayTimezone) {
    let opts: Intl.DateTimeFormatOptions;
    if (spanMs <= 12 * HOUR) {
      opts = { timeZone: displayTimezone, hour: '2-digit', minute: '2-digit', hour12: false };
    } else if (spanMs <= 3 * DAY) {
      opts = {
        timeZone: displayTimezone, month: '2-digit', day: '2-digit',
        hour: '2-digit', minute: '2-digit', hour12: false,
      };
    } else {
      opts = { timeZone: displayTimezone, month: '2-digit', day: '2-digit' };
    }
    return new Intl.DateTimeFormat('sv-SE', opts).format(new Date(ts));
  }
  const d = new Date(ts);
  if (spanMs <= 12 * HOUR) return p2(d.getHours()) + ':' + p2(d.getMinutes());
  if (spanMs <= 3 * DAY) {
    return p2(d.getMonth() + 1) + '-' + p2(d.getDate()) + ' ' +
      p2(d.getHours()) + ':' + p2(d.getMinutes());
  }
  return p2(d.getMonth() + 1) + '-' + p2(d.getDate());
}

/** One stat card. Both halves are escaped: a value can be a router-supplied name. */
export function statCard(val: string | number, lbl: string): string {
  return '<div class="rpt-stat-card"><div class="rpt-stat-val">' + esc(String(val)) +
    '</div><div class="rpt-stat-lbl">' + esc(lbl) + '</div></div>';
}

/**
 * A datetime-local value to an instant.
 *
 * `new Date('2026-01-01T00:00')` — no zone — is parsed as LOCAL time, which is
 * what the operator typed. The `|| 0` catches an unparseable field; an empty one
 * means "unbounded", and which end depends on which field it is.
 */
export function dateToTs(dateStr: string, endOfDay: boolean): number {
  if (!dateStr) return endOfDay ? Date.now() : 0;
  return new Date(dateStr).getTime() || 0;
}

/** A percentage for a stat card: one decimal below ten, whole above it. */
export function utilPct(v: number | null): string {
  if (v == null) return '—';
  return (v < 10 ? v.toFixed(1) : String(Math.round(v))) + '%';
}

/** What one aggregated row covers, for a column heading. */
export function bucketNoun(agg: string): string {
  if (agg === 'hour') return 'hour';
  if (agg === 'day') return 'day';
  if (agg === 'week') return 'week';
  if (agg === 'month') return 'month';
  return 'sample';
}

// ── The date presets ────────────────────────────────────────────────────────

const sod = (d: Date): Date => { const r = new Date(d); r.setHours(0, 0, 0, 0); return r; };
const eod = (d: Date): Date => { const r = new Date(d); r.setHours(23, 59, 0, 0); return r; };

/** Monday-start week, matching the periods the scheduler reports on. */
const sowMon = (d: Date): Date => {
  const r = new Date(d);
  const day = r.getDay();
  r.setDate(r.getDate() - (day === 0 ? 6 : day - 1));
  r.setHours(0, 0, 0, 0);
  return r;
};
const eowSun = (d: Date): Date => {
  const r = sowMon(d);
  r.setDate(r.getDate() + 6);
  r.setHours(23, 59, 0, 0);
  return r;
};
const som = (d: Date): Date => new Date(d.getFullYear(), d.getMonth(), 1);
/** Day 0 of the NEXT month is the last day of this one, however long it is. */
const eom = (d: Date): Date => {
  const r = new Date(d.getFullYear(), d.getMonth() + 1, 0);
  r.setHours(23, 59, 0, 0);
  return r;
};

/** The value a datetime-local input wants: local time, no zone, minute precision. */
export function dtVal(d: Date): string {
  return d.getFullYear() + '-' + p2(d.getMonth() + 1) + '-' + p2(d.getDate()) +
    'T' + p2(d.getHours()) + ':' + p2(d.getMinutes());
}

/**
 * A preset's range, or null for one this does not know.
 *
 * SPLIT OUT OF THE DOM WRITE so it can be tested and compared. The original
 * computes and assigns in one function; the arithmetic is the part that can be
 * wrong, and it is the part with month lengths and week starts in it.
 *
 * Note the two families: the `last*` presets end at NOW, while `this*` and
 * `prev*` end at a period boundary — except the `*SoFar` ones, which end at now
 * on purpose. A "this month" report covering a month that has not finished is
 * empty at the end; "this month so far" is not.
 */
export function presetRange(val: string, now: Date): { from: Date; to: Date } | null {
  let from: Date;
  let to = new Date(now);
  switch (val) {
    case 'last1h': from = new Date(+now - HOUR); break;
    case 'last3h': from = new Date(+now - 3 * HOUR); break;
    case 'last6h': from = new Date(+now - 6 * HOUR); break;
    case 'last12h': from = new Date(+now - 12 * HOUR); break;
    case 'last24h': from = new Date(+now - DAY); break;
    case 'last2d': from = sod(new Date(+now - 2 * DAY)); break;
    case 'last7d': from = sod(new Date(+now - 7 * DAY)); break;
    case 'last30d': from = sod(new Date(+now - 30 * DAY)); break;
    case 'last90d': from = sod(new Date(+now - 90 * DAY)); break;
    case 'last6mo': from = sod(new Date(now.getFullYear(), now.getMonth() - 6, now.getDate())); break;
    case 'last1y': from = sod(new Date(now.getFullYear() - 1, now.getMonth(), now.getDate())); break;
    case 'dayBeforeYesterday': {
      const d = sod(now); d.setDate(d.getDate() - 2); from = d; to = eod(new Date(d)); break;
    }
    case 'thisDayLastWeek': {
      const d = sod(now); d.setDate(d.getDate() - 7); from = d; to = eod(new Date(d)); break;
    }
    case 'prevWeek': {
      const d = new Date(+now - 7 * DAY); from = sowMon(d); to = eowSun(d); break;
    }
    case 'prevMonth': {
      const d = new Date(now.getFullYear(), now.getMonth() - 1, 1); from = som(d); to = eom(d); break;
    }
    case 'prevYear':
      from = new Date(now.getFullYear() - 1, 0, 1);
      to = new Date(now.getFullYear() - 1, 11, 31, 23, 59, 0, 0);
      break;
    case 'today': from = sod(now); to = eod(now); break;
    case 'thisWeek': from = sowMon(now); to = eowSun(now); break;
    case 'thisMonth': from = som(now); to = eom(now); break;
    case 'thisYear':
      from = new Date(now.getFullYear(), 0, 1);
      to = new Date(now.getFullYear(), 11, 31, 23, 59, 0, 0);
      break;
    case 'todaySoFar': from = sod(now); break;
    case 'thisWeekSoFar': from = sowMon(now); break;
    case 'thisMonthSoFar': from = som(now); break;
    case 'thisYearSoFar': from = new Date(now.getFullYear(), 0, 1); break;
    default: return null;
  }
  return { from, to };
}

/** Apply a preset to the two date inputs. An unknown preset leaves them alone. */
export function applyPreset(val: string): void {
  const r = presetRange(val, new Date());
  if (!r) return;
  const from = el<HTMLInputElement>('rptFrom');
  const to = el<HTMLInputElement>('rptTo');
  if (from) from.value = dtVal(r.from);
  if (to) to.value = dtVal(r.to);
}

// ── Tabs ────────────────────────────────────────────────────────────────────

/** Which tab is showing. The caller loads data for it; this only switches. */
export function showTab(name: string): void {
  const bar = el('rptTabBar');
  if (bar) {
    bar.querySelectorAll('.stab').forEach((b) => {
      b.classList.toggle('active', (b as HTMLElement).dataset.rtab === name);
    });
  }
  document.querySelectorAll('.rtab-panel').forEach((p) => p.classList.remove('active'));
  el('rtab-' + name)?.classList.add('active');
}

/** Restore the saved preset, or last 7 days. */
export function restorePreset(): string {
  let saved = 'last7d';
  try {
    saved = localStorage.getItem(RPT_PRESET_KEY) || 'last7d';
  } catch {
    // A browser with storage disabled still gets a working page.
  }
  const sel = el<HTMLSelectElement>('rptPreset');
  if (sel) sel.value = saved;
  applyPreset(saved);
  return saved;
}

export function savePreset(val: string): void {
  try {
    localStorage.setItem(RPT_PRESET_KEY, val);
  } catch {
    // Not being able to remember the choice is not a reason to refuse it.
  }
}

// ── The load flow ───────────────────────────────────────────────────────────

/**
 * Fetch every tab's data for the chosen range and render it.
 *
 * ── FIVE REQUESTS IN PARALLEL, THEN TWO MORE ────────────────────────────────
 *
 * The traffic and bandwidth endpoints answer with an INTERFACE LIST when no
 * interface is named and with samples when one is. So the first round fills both
 * pickers, and a second request per tab fetches the series for whichever
 * interface is selected. That is one extra round trip and it is what lets the
 * picker survive a reload: the previously chosen interface is kept if the new
 * list still contains it.
 *
 * ── THE ENDPOINTS ARE THE PORT'S, UNDER /next/ ──────────────────────────────
 *
 * `/next/api/reports/*`, not `/api/reports/*` — see internal/server/reports.go.
 * The one exception is the alert acknowledge write, which is still Node's and
 * reaches it through the proxy.
 */

const API = '/api/reports/';

/** Whether the operator has typed in the To field. See loadReports. */
let toIsManual = false;

interface Envelope<T> {
  ok?: boolean;
  rows?: T[];
  interfaces?: string[];
  summary?: IfaceSummary;
}

function getJSON<T>(url: string): Promise<Envelope<T>> {
  return fetch(url, { credentials: 'same-origin' }).then((r) => r.json() as Promise<Envelope<T>>);
}

/**
 * Fill an interface picker, keeping the current choice when it still exists.
 *
 * Returns the interface to load. An empty string means the router has no history
 * for this tab at all, which is different from "none selected".
 */
/**
 * Fill an interface `<select>` and KEEP the current choice if it survives.
 *
 * The preservation is the rule worth having: pressing Load re-fetches the
 * interface list, and without it the chosen interface would snap back to the
 * first one on every load — so a report on `ether5` would silently become a
 * report on `bridge` the moment the operator changed the date range.
 *
 * An interface that has GONE cannot be preserved, and then the browser's own
 * default applies: the first option, or '' when there are none. Returning
 * `sel.value` rather than the caller's guess is what makes those two cases the
 * same code path.
 *
 * Exported for the reports-iface check; the page calls it internally.
 */
export function fillIfaceSelect(id: string, ifaces: string[]): string {
  const sel = el<HTMLSelectElement>(id);
  if (!sel) return ifaces[0] || '';
  const current = sel.value;
  sel.innerHTML = ifaces.map((i) => '<option value="' + esc(i) + '">' + esc(i) + '</option>').join('');
  if (current && ifaces.indexOf(current) !== -1) sel.value = current;
  return sel.value || '';
}

/**
 * The href behind a CSV or PDF button.
 *
 * ── IT POINTS AT /next/, LIKE EVERYTHING ELSE ON THIS PAGE ──────────────────
 *
 * The live app builds `/api/reports/<type>/export`. This port's report
 * endpoints — exports included — sit under `/next/api/reports/` until the page
 * cuts over, for the reason `internal/server/reports.go` gives: `/api/*` still
 * proxies to Node, so registering there would move a page nobody has ported
 * onto an implementation nobody has compared. Linking a ported page at the
 * unported implementation would be the same mistake from the other end.
 *
 * `aggregate` is read from the select at CALL time rather than taken from the
 * load's snapshot, exactly as the original does. In practice they agree — the
 * links are set during the render that follows the load — and reproducing the
 * read keeps them agreeing for the same reason the original does.
 *
 * `from` and `to` are numbers and go in unencoded; the original does not encode
 * them either, and encoding them would change nothing except the diff.
 */
export function exportUrl(
  type: string, fmt: string, routerId: string, from: number, to: number, extra: string,
): string {
  const aggSel = el<HTMLSelectElement>('rptAggregate');
  const agg = aggSel ? aggSel.value : '';
  let q = 'routerId=' + encodeURIComponent(routerId) + '&from=' + from + '&to=' + to +
    '&format=' + fmt;
  if (agg) q += '&aggregate=' + encodeURIComponent(agg);
  if (extra) q += '&' + extra;
  return API + type + '/export?' + q;
}

/**
 * Point a report's two export buttons at the range now on screen, and reveal
 * them.
 *
 * REVEAL ONLY — there is no hiding path, and that is the original's shape. The
 * buttons ship `display:none` in the markup and appear the first time a report
 * renders; an empty report still reveals them, because "no rows in this range"
 * is a legitimate thing to export and the live renderer sets them as its last
 * statement regardless of row count.
 */
export function setExportLinks(
  csvId: string, pdfId: string, type: string, routerId: string,
  from: number, to: number, extra: string,
): void {
  const csv = el<HTMLAnchorElement>(csvId);
  if (csv) {
    csv.href = exportUrl(type, 'csv', routerId, from, to, extra);
    csv.style.display = '';
  }
  const pdf = el<HTMLAnchorElement>(pdfId);
  if (pdf) {
    pdf.href = exportUrl(type, 'pdf', routerId, from, to, extra);
    pdf.style.display = '';
  }
}

/**
 * `interface=<name>` when one is chosen, empty otherwise.
 *
 * Exported for the differential gate. It is the encoding boundary for an
 * OPERATOR-SUPPLIED name — RouterOS interface names take spaces, slashes and
 * ampersands — and it encodes exactly once, because `exportUrl` appends `extra`
 * to the query verbatim.
 */
export function ifaceExtra(selectId: string): string {
  const sel = el<HTMLSelectElement>(selectId);
  return sel && sel.value ? 'interface=' + encodeURIComponent(sel.value) : '';
}

export function loadReports(): void {
  const routerSel = el<HTMLSelectElement>('rptRouter');
  if (!routerSel || !routerSel.value) return;

  // THE To FIELD FOLLOWS THE CLOCK UNTIL SOMEBODY TOUCHES IT. Without this a
  // page left open overnight keeps reporting up to the moment it was loaded,
  // which looks like the router stopped recording.
  const toInput = el<HTMLInputElement>('rptTo');
  if (toInput && !toIsManual) toInput.value = dtVal(new Date());

  const routerId = routerSel.value;
  const fromInput = el<HTMLInputElement>('rptFrom');
  const from = dateToTs(fromInput ? fromInput.value : '', false);
  const to = dateToTs(toInput ? toInput.value : '', true);
  const aggSel = el<HTMLSelectElement>('rptAggregate');
  const agg = aggSel ? aggSel.value : '';
  const q = 'routerId=' + encodeURIComponent(routerId) + '&from=' + from + '&to=' + to +
    (agg ? '&aggregate=' + encodeURIComponent(agg) : '');

  const spinner = el('rptSpinner');
  if (spinner) spinner.style.display = '';
  const loadBtn = el<HTMLButtonElement>('rptLoadBtn');
  if (loadBtn) loadBtn.disabled = true;

  Promise.all([
    getJSON<PingRow>(API + 'ping?' + q),
    getJSON<never>(API + 'traffic?' + q),
    getJSON<never>(API + 'bandwidth?' + q),
    getJSON<AlertRow>(API + 'alerts?' + q),
    getJSON<ConnRow>(API + 'connectivity?' + q),
  ]).then((res) => {
    renderPing(res[0]?.rows || []);
    setExportLinks('rptPingCsvLink', 'rptPingPdfLink', 'ping', routerId, from, to, '');

    const iface = fillIfaceSelect('rptTrafficIface', res[1]?.interfaces || []);
    if (iface) {
      getJSON<TrafficRow>(API + 'traffic?' + q + '&interface=' + encodeURIComponent(iface))
        .then((d) => {
          renderTraffic(d.rows || [], d.summary || null, agg);
          setExportLinks('rptTrafficCsvLink', 'rptTrafficPdfLink', 'traffic', routerId, from, to,
            ifaceExtra('rptTrafficIface'));
        })
        .catch(() => { /* one tab failing must not blank the others */ });
    } else {
      renderTraffic([], null, agg);
      setExportLinks('rptTrafficCsvLink', 'rptTrafficPdfLink', 'traffic', routerId, from, to, '');
    }

    const bwIface = fillIfaceSelect('rptBwIface', res[2]?.interfaces || []);
    if (bwIface) {
      getJSON<BandwidthRow>(API + 'bandwidth?' + q + '&interface=' + encodeURIComponent(bwIface))
        .then((d) => {
          renderBandwidth(d.rows || [], d.summary || null, agg);
          setExportLinks('rptBwCsvLink', 'rptBwPdfLink', 'bandwidth', routerId, from, to,
            ifaceExtra('rptBwIface'));
        })
        .catch(() => { /* as above */ });
    } else {
      renderBandwidth([], null, agg);
      setExportLinks('rptBwCsvLink', 'rptBwPdfLink', 'bandwidth', routerId, from, to, '');
    }

    renderAlerts(res[3]?.rows || []);
    setExportLinks('rptAlertCsvLink', 'rptAlertPdfLink', 'alerts', routerId, from, to, '');
    renderConn(res[4]?.rows || [], agg);
    setExportLinks('rptConnCsvLink', 'rptConnPdfLink', 'connectivity', routerId, from, to, '');
  }).catch((e) => {
    console.warn('[reports]', e);
  }).then(() => {
    // Runs on both paths, so a failed load re-enables the button rather than
    // leaving the page looking permanently busy.
    if (spinner) spinner.style.display = 'none';
    if (loadBtn) loadBtn.disabled = false;
  });
}

/** Re-fetch one tab when its interface picker changes. */
function reloadIface(kind: 'traffic' | 'bandwidth'): void {
  const routerSel = el<HTMLSelectElement>('rptRouter');
  const sel = el<HTMLSelectElement>(kind === 'traffic' ? 'rptTrafficIface' : 'rptBwIface');
  if (!routerSel?.value || !sel?.value) return;
  const fromInput = el<HTMLInputElement>('rptFrom');
  const toInput = el<HTMLInputElement>('rptTo');
  const aggSel = el<HTMLSelectElement>('rptAggregate');
  const agg = aggSel ? aggSel.value : '';
  const q = 'routerId=' + encodeURIComponent(routerSel.value) +
    '&from=' + dateToTs(fromInput ? fromInput.value : '', false) +
    '&to=' + dateToTs(toInput ? toInput.value : '', true) +
    (agg ? '&aggregate=' + encodeURIComponent(agg) : '') +
    '&interface=' + encodeURIComponent(sel.value);
  if (kind === 'traffic') {
    getJSON<TrafficRow>(API + 'traffic?' + q)
      .then((d) => renderTraffic(d.rows || [], d.summary || null, agg))
      .catch(() => { /* leave the previous view in place */ });
  } else {
    getJSON<BandwidthRow>(API + 'bandwidth?' + q)
      .then((d) => renderBandwidth(d.rows || [], d.summary || null, agg))
      .catch(() => { /* as above */ });
  }
}

/** One row of the router list, as /api/routers sends it. */
export interface ReportRouter { id: string; label?: string; name?: string; host?: string }

/**
 * Fill the router picker.
 *
 * THE LIST IS PASSED IN, NOT FETCHED. The live page fills this select from a
 * `routers:update` socket event; this port already fetches `/api/routers` once
 * in main.ts for the nav switcher, and that response is grant-filtered by Node.
 * Asking again would be a second request for the same answer and a second place
 * for the two lists to disagree about which routers exist.
 */
function fillRouterSelect(routers: ReportRouter[]): void {
  const sel = el<HTMLSelectElement>('rptRouter');
  if (!sel) return;
  const current = sel.value;
  sel.innerHTML = routers.map((r) =>
    '<option value="' + esc(r.id) + '">' + esc(r.label || r.name || r.host || r.id) +
    '</option>').join('');
  if (current && routers.some((r) => r.id === current)) sel.value = current;
}

/** Mount the page: fill the pickers, restore the preset, wire the controls. */
export function mountReports(routers: ReportRouter[] = []): void {
  fillRouterSelect(routers);
  restorePreset();
  wirePingPager();
  wireBwPager();
  wireAlertAck();
  wireCapacityToggle();
  wireScheduleActions();
  wireScheduleForm();

  el('rptTabBar')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('[data-rtab]') as HTMLElement | null;
    if (!btn?.dataset.rtab) return;
    showTab(btn.dataset.rtab);
    // The schedule list is NOT part of loadReports(): it is configuration, not
    // report data, and it must not be re-fetched every time somebody presses
    // Load on a date range.
    if (btn.dataset.rtab === 'scheduled') loadSchedules();
  });

  el<HTMLSelectElement>('rptPreset')?.addEventListener('change', (e) => {
    const v = (e.target as HTMLSelectElement).value;
    savePreset(v);
    applyPreset(v);
    // ── THE LATCH GOES **TRUE** HERE, AND THE OPPOSITE IS A REAL BUG ──────
    //
    // `applyPreset` has just written an AUTHORITATIVE end into the To field, and
    // for nine of the presets that end is not now: prevMonth, prevYear,
    // prevWeek, dayBeforeYesterday, thisDayLastWeek, today, thisWeek, thisMonth,
    // thisYear all set an explicit `to`. Leaving the latch false lets
    // `loadReports` overwrite To with `new Date()` on the very next line, so
    // "Previous month" reports from the start of last month up to RIGHT NOW.
    //
    // This read `toIsManual = false` until 2026-08-25, with a comment reasoning
    // that a preset makes the field non-manual. The reasoning is sound and the
    // conclusion is wrong: the live page sets it TRUE
    // (`../MikroDash/public/app.js:10562`), and "manual" here means "somebody
    // chose this value deliberately", not "somebody typed it".
    toIsManual = true;
    loadReports();
  });
  el('rptTo')?.addEventListener('change', () => { toIsManual = true; });
  el('rptLoadBtn')?.addEventListener('click', () => loadReports());
  el('rptAggregate')?.addEventListener('change', () => loadReports());
  el('rptRouter')?.addEventListener('change', () => loadReports());
  el('rptTrafficIface')?.addEventListener('change', () => reloadIface('traffic'));
  el('rptBwIface')?.addEventListener('change', () => reloadIface('bandwidth'));

  // LOAD ONCE ON MOUNT, but only if there is a router to load for. The live page
  // auto-loads when Reports becomes active; doing it here means the first visit
  // shows data rather than five empty tables and a Load button.
  if (el<HTMLSelectElement>('rptRouter')?.value) loadReports();
}
