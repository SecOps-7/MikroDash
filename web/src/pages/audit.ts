/**
 * The Audit page.
 *
 * Rows are filtered SERVER-SIDE, per row: app-scope events need system
 * administration, router events need history on that router. This page shows
 * whatever came back and says so when that is nothing — an empty trail for a
 * reader who may not see it is a legitimate answer, not an error, so there is no
 * error state to distinguish here.
 *
 * ── FETCHED ON ENTRY, NOT STREAMED ──────────────────────────────────────────
 *
 * Every other ported page rides the WebSocket. This one does not, and the live
 * comment gives the reason: "the trail is history, and a page that reloads
 * itself while being read is worse than one that does not."
 *
 * ── fmtTs IS IMPORTED FROM reports.ts, WHERE THE LIVE PAGE DUPLICATES IT ────
 *
 * The live Audit page defines its own copy, explaining that it would rather
 * format locally than hoist a function it is the only other user of — and both
 * copies then read the SAME `_displayTimezone` global.
 *
 * That shared global is the part that matters, so this imports instead. The two
 * implementations are identical (including the `.replace('T',' ')` that a stale
 * comment in the live file still describes as a miscopy — it was fixed there),
 * and importing means ONE module-level zone rather than two, which is the shape
 * the single global already had. Duplicating would produce a second variable and
 * a second thing to remember to wire.
 *
 * KNOWN GAP, NOT INTRODUCED HERE: `setReportTimezone` is exported by reports.ts
 * and never called, because this port has no handler for the `pages` payload
 * that carries `displayTimezone` (app.js:2883). So both pages currently format
 * in the BROWSER's zone, where the live app uses the configured one for an
 * operator who set it. That is the nav-and-shell queue item's job; when it lands
 * it fixes both pages at once, which it could not do if this file kept its own.
 */

import { esc, el, debounce, renderSortHeader, sortMul,
         type SortCol, type SortState } from '../dom';
import { fmtTs } from './reports';

/**
 * `/next/api/audit`, not `/api/audit` — see internal/server/audit_api.go. The
 * live Audit page is still served by Node through this proxy, so the endpoints
 * stay staged until this page cuts over; the prefix comes off in one commit that
 * can be reverted in one.
 */
const API = '/api/audit';

interface AuditRow {
  ts: number;
  actor_name: string;
  actor_ip: string | null;
  action: string;
  outcome: string;
  target_id: string | null;
  target_name: string | null;
  router_id: string | null;
  router_name: string;
  detail: unknown;
}

interface Facets { actors: string[]; actions: string[] }

/** One row flattened to the keys the table sorts and renders by. */
interface FlatRow {
  ts: number;
  actor: string;
  ip: string;
  action: string;
  target: string;
  outcome: string;
  detail: unknown;
  router_id: string;
  router_name: string;
}

const PAGE = 200;

const COLS: SortCol[] = [
  { key: 'ts', label: 'When' },
  { key: 'actor', label: 'Who' },
  { key: 'ip', label: 'From' },
  { key: 'action', label: 'Action' },
  { key: 'target', label: 'Target' },
  { key: 'outcome', label: 'Result' },
  { key: 'detail', label: 'Detail' },
];

const MUTED = '<span style="color:var(--text-muted)">&mdash;</span>';

function outcomeCell(o: string): string {
  if (o === 'denied') return '<span class="wl-band wl-band-24">refused</span>';
  if (o === 'failed') return '<span class="wl-band wl-band-24">failed</span>';
  return '<span class="wl-band wl-band-6">ok</span>';
}

/** A value, shortened. Not a string becomes its JSON, as `String(v)` would not. */
function short(v: unknown): string {
  if (v === null || v === undefined) return '—';
  const s = typeof v === 'string' ? v : JSON.stringify(v);
  return s.length > 40 ? s.slice(0, 40) + '…' : s;
}

/**
 * The stored detail is JSON. Rendered as "field: from → to" so a settings change
 * reads as a change rather than as a blob — and a redacted credential shows the
 * marker the server wrote, never a value.
 *
 * A detail that will not parse falls back to its first 120 characters ESCAPED,
 * which is the one path here where unparsed router-supplied text reaches the
 * page.
 */
function detailCell(raw: unknown): string {
  if (!raw) return MUTED;
  let d: Record<string, unknown>;
  try {
    // PARSED, NEVER ACCEPTED AS AN OBJECT. An earlier version here also took an
    // already-decoded object, which looks like tolerance and is not: the server
    // was sending embedded JSON where Node sends a string, and accepting both
    // made this page render correctly from a payload the live page renders as
    // "[object Object]". The DOM comparison found it; matching the original
    // exactly is what keeps it found. See internal/db/db.go's Detail field.
    d = JSON.parse(raw as string);
  } catch {
    return esc(String(raw).slice(0, 120));
  }
  // ── THE GUARD THE LIVE APP NOW HAS TOO ────────────────────────────────────
  //
  // ToDo #21: live's `try` wrapped only the PARSE, so `null.changes` threw, the
  // exception escaped `detailCell` into `render`, and `load`'s empty `.catch`
  // swallowed it — one row whose `detail` held the four characters `null`
  // blanked the ENTIRE audit table with the filters above it looking normal.
  //
  // This port refused to reproduce that (a crash that hides the page is not
  // worth parity) and guarded on `d === null` ALONE, because everything else
  // reached live's em dash by the ordinary route. That was true and is no
  // longer: the fix landed on 2026-08-25 as `!d || typeof d !== 'object'`, which
  // also changes what a STRING renders. `Object.keys("abc")` is `['0','1','2']`,
  // so a detail stored as `"a string"` used to print `0 a · 1 b · 2 c` — the
  // port faithfully reproduced that, and `audit-page-check` went red the moment
  // the live side stopped.
  //
  // Widened to match. The port had the narrower half of this right first, which
  // is why the entry was filed at all.
  if (!d || typeof d !== 'object') return '<span style="color:var(--text-muted)">&mdash;</span>';

  const bits: string[] = [];
  const changes = (d.changes as { field: string; from: unknown; to: unknown }[]) || [];
  changes.slice(0, 4).forEach((c) => {
    bits.push('<span style="color:var(--text-muted)">' + esc(c.field) + '</span> ' +
      esc(short(c.from)) + ' &rarr; ' + esc(short(c.to)));
  });
  if (changes.length > 4) bits.push('+' + (changes.length - 4) + ' more');
  Object.keys(d).forEach((k) => {
    if (k === 'changes' || k === 'note') return;
    bits.push('<span style="color:var(--text-muted)">' + esc(k) + '</span> ' + esc(short(d[k])));
  });
  if (d.note) bits.push('<span style="color:var(--text-muted)">' + esc(String(d.note)) + '</span>');
  return bits.join(' &middot; ') || MUTED;
}

function flat(r: AuditRow): FlatRow {
  return {
    ts: r.ts,
    actor: r.actor_name || '',
    ip: r.actor_ip || '',
    action: r.action || '',
    target: r.target_name || r.target_id || '',
    outcome: r.outcome || '',
    detail: r.detail || '',
    router_id: r.router_id || '',
    router_name: r.router_name || '',
  };
}

export function initAuditPage(): void {
  const tbody = el('auditTable');
  const theadRow = el('auditThead');
  if (!tbody || !theadRow) return;

  let rows: AuditRow[] = [];
  let total = 0;
  let offset = 0;
  let facets: Facets = { actors: [], actions: [] };
  const sort: SortState = { col: 'ts', dir: 'desc' };

  function render(): void {
    renderSortHeader('auditThead', COLS, sort, () => render());

    // SORTED IN PLACE ON A MAPPED COPY, and deliberately NOT via dom.ts's
    // sortRows: this comparator is the live page's, which lower-cases nothing
    // and sends every string through localeCompare. sortRows nulls-to-the-front
    // rule would reorder a column of empty ip cells differently.
    const list = rows.map(flat).sort((a, b) => {
      const av = a[sort.col as keyof FlatRow];
      const bv = b[sort.col as keyof FlatRow];
      if (typeof av === 'string') return sortMul(sort) * av.localeCompare(bv as string);
      return sortMul(sort) * (((av as number) || 0) - ((bv as number) || 0));
    });

    const badge = el('auditBadge');
    if (badge) {
      badge.textContent = String(total);
      badge.className = 'card-badge' + (total ? ' active-blue' : '');
    }

    tbody!.innerHTML = list.length ? list.map((r) =>
      '<tr>' +
      '<td>' + esc(fmtTs(r.ts)) + '</td>' +
      '<td>' + (r.actor === 'system'
        ? '<span style="color:var(--text-muted)">system</span>' : esc(r.actor)) + '</td>' +
      '<td class="mono" style="color:var(--text-muted)">' + esc(r.ip || '—') + '</td>' +
      '<td>' + esc(r.action) + '</td>' +
      '<td>' + (r.target ? esc(r.target) : MUTED) +
        // The pill names the DEVICE. It used to read the literal word "router" —
        // a scope marker telling the reader nothing the Action column did not.
        // A router deleted since the event was recorded has no name left, so it
        // falls back to that old generic marker rather than to a bare uuid.
        (r.router_id
          ? ' <span class="wl-band wl-band-5">' + esc(r.router_name || 'router') + '</span>'
          : '') + '</td>' +
      '<td>' + outcomeCell(r.outcome) + '</td>' +
      '<td>' + detailCell(r.detail) + '</td>' +
      '</tr>').join('')
      : '<tr><td colspan="7" class="empty-state">' +
        (offset ? 'No more events.' : 'No audit events visible to you yet.') + '</td></tr>';

    const lbl = el('auPageLbl');
    if (lbl) {
      const first = total ? offset + 1 : 0;
      lbl.textContent = total
        ? (first + '–' + Math.min(offset + PAGE, total) + ' of ' + total) : '';
    }
    const prev = el<HTMLButtonElement>('auPrev');
    const next = el<HTMLButtonElement>('auNext');
    if (prev) prev.disabled = offset <= 0;
    if (next) next.disabled = offset + PAGE >= total;
  }

  function renderSummary(): void {
    const set = (id: string, v: string): void => {
      const e = el(id);
      if (e) e.textContent = v;
    };
    set('auSumTotal', String(total));
    set('auSumDenied', String(rows.filter((r) => r.outcome === 'denied').length));
    set('auSumActors', String(facets.actors.length));
    // The NEWEST row, which is rows[0] because the query orders ts DESC — not
    // the newest of the sorted view, which the operator may have reversed.
    set('auSumNewest', rows.length ? fmtTs(rows[0]!.ts) : '—');
  }

  function query(): URLSearchParams {
    const p = new URLSearchParams();
    const v = (id: string): string => el<HTMLInputElement>(id)?.value || '';
    if (v('auActor')) p.set('actor', v('auActor'));
    if (v('auAction')) p.set('action', v('auAction'));
    if (v('auOutcome')) p.set('outcome', v('auOutcome'));
    if (v('auSearch')) p.set('search', v('auSearch').trim());
    p.set('limit', String(PAGE));
    p.set('offset', String(offset));
    return p;
  }

  function fillFacets(): void {
    ([['auActor', facets.actors, 'All actors'],
      ['auAction', facets.actions, 'All actions']] as const).forEach(([id, values, all]) => {
      const sel = el<HTMLSelectElement>(id);
      if (!sel) return;
      // The current choice is restored after the rebuild. A facet list that no
      // longer contains it leaves the select on its first option, which is the
      // "all" entry — the filter clears rather than silently keeping a value the
      // dropdown no longer shows.
      const keep = sel.value;
      sel.innerHTML = '<option value="">' + all + '</option>' +
        values.map((x) => '<option value="' + esc(x) + '">' + esc(x) + '</option>').join('');
      sel.value = keep;
    });
  }

  function load(): void {
    fetch(API + '?' + query().toString(), { credentials: 'same-origin' })
      .then((r) => r.json())
      .then((d) => {
        if (!d || !d.ok) return;
        rows = d.rows || [];
        total = d.total || 0;
        if (d.facets) { facets = d.facets; fillFacets(); }
        const note = el('auditNote');
        // Say plainly that the view is partial rather than letting a paged list
        // look like the whole trail.
        if (note) note.textContent = total > PAGE ? 'showing ' + PAGE + ' at a time' : '';
        renderSummary();
        render();
        // The export links carry the FILTERS but not the paging, so a download
        // is the filtered view rather than the page being looked at.
        const ex = new URLSearchParams(query());
        ex.delete('limit');
        ex.delete('offset');
        const csv = el<HTMLAnchorElement>('auCsvLink');
        const pdf = el<HTMLAnchorElement>('auPdfLink');
        if (csv) csv.href = API + '/export?format=csv&' + ex.toString();
        if (pdf) pdf.href = API + '/export?format=pdf&' + ex.toString();
      })
      .catch(() => {});
  }

  ['auActor', 'auAction', 'auOutcome'].forEach((id) => {
    el(id)?.addEventListener('change', () => { offset = 0; load(); });
  });
  el('auSearch')?.addEventListener('input', debounce(() => { offset = 0; load(); }, 250));
  el('auPrev')?.addEventListener('click', () => { offset = Math.max(0, offset - PAGE); load(); });
  el('auNext')?.addEventListener('click', () => { offset = offset + PAGE; load(); });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'audit') { offset = 0; load(); }
  });
}
