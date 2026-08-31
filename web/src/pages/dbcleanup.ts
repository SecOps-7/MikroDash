/**
 * Settings → Data Cleanup.
 *
 * ── THE PREVIEW IS A SAFETY INTERLOCK, NOT A CONVENIENCE ────────────────────
 *
 * `Delete data` starts disabled and is re-disabled by ANY change to the
 * selection, so the button can only ever fire against a count the operator
 * actually saw on screen. The live app spends the preview on click
 * (`_pendingCount = 0` before the request goes out) so a double-click cannot
 * delete twice off one confirmation.
 *
 * That interlock is the whole reason this card is not just two fetches. Every
 * piece of it is reproduced here, including the order the live code does it in:
 * the count is zeroed BEFORE the request, not in the response handler, because a
 * response handler does not run if the operator clicks again first.
 *
 * ── A ROW CAN OUTLIVE ITS ROUTER ────────────────────────────────────────────
 *
 * History is keyed by router id and a router can be deleted while its rows
 * remain. `routerName` names those explicitly — "Removed router (a1b2c3d4…)" —
 * and `scopeIds` keeps them SELECTABLE, which is what makes the orphaned data
 * reachable at all. A card that only listed known routers would leave rows no
 * operator could ever purge.
 *
 * ── WHAT IS PURE AND WHAT IS NOT ────────────────────────────────────────────
 *
 * Everything with a decision in it is exported and driven by
 * `tools/dbcleanup-check.js`, which runs the LIVE IIFE and this module against
 * one shared fake DOM and diffs what each wrote. What is left unexported is the
 * fetching and the listener registration.
 */

import { el, esc, fmtBytes } from '../dom';

export interface DbRouter { id: string; label?: string | null; host?: string | null }
export interface DbRouterRows { routerId: string; rows: number }

export interface DbStats {
  ok?: boolean;
  bytes?: number;
  total?: number;
  oldestTs?: number | null;
  byRouter?: DbRouterRows[];
}

export interface DbPurgeOpts { routerId: string; types: string[]; olderThanDays: number }

export interface DbPurgeReply {
  ok?: boolean;
  error?: string;
  total?: number;
  byType?: Record<string, number>;
  deleted?: number;
  bytesBefore?: number;
  bytesAfter?: number;
}

/** The live `TYPE_LABELS`. The ORDER of a summary follows the checkbox order, not this. */
export const TYPE_LABELS: Record<string, string> = {
  traffic: 'Traffic graphs',
  ping: 'Ping history',
  bandwidth: 'Bandwidth usage',
  events: 'Alerts & connectivity',
};

/**
 * The live `routerName`.
 *
 * NOT ESCAPED HERE. Its two callers differ — `renderStats` puts it through
 * `esc`, `renderScope` hands it to `option.text` which escapes on assignment —
 * and escaping inside would double-encode an ampersand in a router label on both
 * paths. Reproduced as the live app has it.
 */
export function routerName(id: string, known: DbRouter[]): string {
  const m = known.find((x) => x.id === id);
  if (m) return m.label || m.host || id;
  return 'Removed router (' + String(id).slice(0, 8) + '…)';
}

/**
 * The scope dropdown's ids, in order: every known router, then any id that only
 * the stats know about.
 *
 * The live loop pushes onto the same array it tests with `indexOf`, so a router
 * appearing twice in `byRouter` is added once.
 */
export function scopeIds(known: DbRouter[], byRouter: DbRouterRows[]): string[] {
  const ids = known.map((r) => r.id);
  (byRouter || []).forEach((r) => {
    if (ids.indexOf(r.routerId) === -1) ids.push(r.routerId);
  });
  return ids;
}

/** The `dbcByRouter` list. */
export function byRouterHtml(byRouter: DbRouterRows[], known: DbRouter[]): string {
  return (byRouter || [])
    .map(
      (r) =>
        '<div class="dbc-router"><span>' +
        esc(routerName(r.routerId, known)) +
        '</span><b>' +
        r.rows.toLocaleString() +
        ' rows</b></div>',
    )
    .join('');
}

/**
 * The three headline stats.
 *
 * `oldestTs` of 0 renders as the em dash rather than 1 January 1970 — `s.oldestTs ?`
 * is falsy for both null and zero, and the server already returns null for an
 * empty database. Kept as the live truthiness test rather than a null check,
 * because the two disagree exactly on zero and the live answer is the dash.
 */
export function statsText(s: DbStats): { size: string; rows: string; oldest: string } {
  return {
    size: fmtBytes(s.bytes || 0),
    rows: (s.total || 0).toLocaleString(),
    oldest: s.oldestTs ? new Date(s.oldestTs).toLocaleDateString() : '—',
  };
}

/**
 * The preview summary.
 *
 * Returns null when nothing matched, which is a DIFFERENT message and — more
 * importantly — leaves the delete button disabled.
 *
 * A type with a zero count is dropped from the breakdown (`filter(t => j.byType[t])`)
 * so "Traffic graphs 0" never appears; the total above it is already the answer.
 */
export function summaryHtml(
  j: DbPurgeReply,
  opts: DbPurgeOpts,
  known: DbRouter[],
): string | null {
  if (!j.total) return null;
  const byType = j.byType || {};
  const parts = opts.types
    .filter((t) => byType[t])
    .map((t) => TYPE_LABELS[t] + ' <b>' + (byType[t] as number).toLocaleString() + '</b>');
  const where = opts.routerId ? routerName(opts.routerId, known) : 'all routers';
  const when = opts.olderThanDays
    ? 'older than ' + opts.olderThanDays + ' day' + (opts.olderThanDays === 1 ? '' : 's')
    : 'of any age';
  return (
    'Will delete <b>' +
    j.total.toLocaleString() +
    '</b> rows from ' +
    esc(where) +
    ', ' +
    when +
    '.<br>' +
    parts.join(' &middot; ')
  );
}

/**
 * The line after a successful delete.
 *
 * `Math.max(0, before - after)` — a VACUUM can leave the file marginally LARGER,
 * and "freed -4.0 KB" reads as a bug in the thing that just worked.
 */
export function deletedText(j: DbPurgeReply): string {
  const freed = Math.max(0, (j.bytesBefore || 0) - (j.bytesAfter || 0));
  return '✓ Deleted ' + (j.deleted || 0).toLocaleString() + ' rows, freed ' + fmtBytes(freed) + '.';
}

// ── The wiring ──────────────────────────────────────────────────────────────

let known: DbRouter[] = [];
let pendingCount = 0;
let previewLabel = '';
let deleteLabel = '';

function nodes() {
  return {
    scope: el<HTMLSelectElement>('dbcScope'),
    age: el<HTMLSelectElement>('dbcAge'),
    types: el('dbcTypes'),
    prevBtn: el<HTMLButtonElement>('dbcPreviewBtn'),
    delBtn: el<HTMLButtonElement>('dbcPurgeBtn'),
    summary: el('dbcSummary'),
    result: el('dbcResult'),
  };
}

function selectedTypes(types: HTMLElement): string[] {
  return Array.prototype.slice
    .call(types.querySelectorAll('input:checked'))
    .map((i: HTMLInputElement) => i.value);
}

function currentOpts(): DbPurgeOpts {
  const n = nodes();
  return {
    routerId: n.scope!.value || '',
    types: selectedTypes(n.types!),
    olderThanDays: parseInt(n.age!.value, 10),
  };
}

function post(body: unknown): Promise<DbPurgeReply> {
  return fetch('/api/db/purge', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify(body),
  }).then((r) => r.json() as Promise<DbPurgeReply>);
}

function say(cls: string, msg: string): void {
  const result = nodes().result!;
  result.className = 'dbc-result ' + cls;
  result.textContent = msg;
}

/**
 * Both buttons are locked for the duration of a request.
 *
 * The delete button's re-enable is CONDITIONAL — `on || pendingCount === 0` —
 * so clearing the busy state cannot hand back a delete the interlock had
 * disabled.
 */
function setBusy(on: boolean, which?: string, msg?: string): void {
  const n = nodes();
  n.prevBtn!.disabled = on;
  n.delBtn!.disabled = on || pendingCount === 0;
  n.prevBtn!.textContent = on && which === 'preview' ? 'Checking…' : previewLabel;
  n.delBtn!.textContent = on && which === 'delete' ? 'Deleting…' : deleteLabel;
  if (on) {
    n.result!.className = 'dbc-result busy';
    n.result!.innerHTML = '<span class="dbc-spin"></span>' + esc(msg || '');
  }
}

function renderScope(byRouter: DbRouterRows[]): void {
  const scope = nodes().scope!;
  // The current selection is restored AFTER the rebuild. If the selected router
  // is gone from the list the assignment finds no option and the select falls
  // back to "All routers" — the safe direction to fail, and what the live card
  // does.
  const keep = scope.value;
  scope.innerHTML = '<option value="">All routers</option>';
  scopeIds(known, byRouter).forEach((id) => {
    const o = document.createElement('option');
    o.value = id;
    o.text = routerName(id, known);
    scope.appendChild(o);
  });
  scope.value = keep;
}

function renderStats(s: DbStats): void {
  const t = statsText(s);
  el('dbcSize')!.textContent = t.size;
  el('dbcRows')!.textContent = t.rows;
  el('dbcOldest')!.textContent = t.oldest;
  el('dbcByRouter')!.innerHTML = byRouterHtml(s.byRouter || [], known);
  renderScope(s.byRouter || []);
}

/**
 * The router list first, then the stats.
 *
 * ORDER MATTERS: `renderStats` turns ids into names, so a stats response that
 * arrived first would render every router as "Removed router (…)". The live
 * chain has the same shape, and its empty `.catch` on the router fetch is
 * deliberate — a failed router list must still let the stats render, just with
 * ids for names.
 */
function loadStats(): Promise<void> {
  return fetch('/api/routers', { credentials: 'same-origin' })
    .then((r) => r.json())
    .then((j) => {
      known = (j && j.routers) || [];
    })
    .catch(() => {})
    .then(() => fetch('/api/db/stats', { credentials: 'same-origin' }))
    .then((r) => r.json())
    .then((j: DbStats) => {
      if (j && j.ok) renderStats(j);
    })
    .catch(() => {});
}

/** Any change to the selection spends the preview. */
function invalidate(): void {
  const n = nodes();
  pendingCount = 0;
  n.delBtn!.disabled = true;
  n.summary!.innerHTML = '';
  n.result!.textContent = '';
  n.result!.className = 'dbc-result';
}

export function initDbCleanup(): void {
  const n = nodes();
  // The live guard, and it is load-bearing rather than defensive: this card is
  // hidden from anyone who is not a global admin, so on most sessions these
  // elements are simply absent.
  if (!n.scope || !n.prevBtn || !n.delBtn) return;

  previewLabel = n.prevBtn.textContent || '';
  deleteLabel = n.delBtn.textContent || '';

  n.scope.addEventListener('change', invalidate);
  n.age!.addEventListener('change', invalidate);
  n.types!.addEventListener('change', invalidate);

  n.prevBtn.addEventListener('click', () => {
    const opts = currentOpts();
    if (!opts.types.length) {
      say('err', 'Select at least one data type.');
      return;
    }
    pendingCount = 0;
    setBusy(true, 'preview', 'Counting matching rows…');
    post({
      routerId: opts.routerId,
      types: opts.types,
      olderThanDays: opts.olderThanDays,
      dryRun: true,
    })
      .then((j) => {
        setBusy(false);
        if (!j || !j.ok) {
          say('err', (j && j.error) || 'Preview failed');
          return;
        }
        say('', '');
        const html = summaryHtml(j, opts, known);
        if (html === null) {
          nodes().summary!.innerHTML = 'Nothing matches that selection.';
          return;
        }
        pendingCount = j.total || 0;
        nodes().summary!.innerHTML = html;
        nodes().delBtn!.disabled = false;
      })
      .catch(() => {
        setBusy(false);
        say('err', 'Preview failed');
      });
  });

  n.delBtn.addEventListener('click', () => {
    const opts = currentOpts();
    if (!opts.types.length) return;
    if (!confirm('Delete this data permanently? This cannot be undone.')) return;
    const count = pendingCount;
    pendingCount = 0; // the preview is spent either way
    setBusy(
      true,
      'delete',
      'Deleting ' + count.toLocaleString() + ' rows and compacting the database…',
    );
    post({ routerId: opts.routerId, types: opts.types, olderThanDays: opts.olderThanDays })
      .then((j) => {
        setBusy(false);
        if (!j || !j.ok) {
          say('err', (j && j.error) || 'Delete failed');
          return;
        }
        say('ok', deletedText(j));
        nodes().summary!.innerHTML = '';
        loadStats();
      })
      .catch(() => {
        setBusy(false);
        say('err', 'Delete failed');
      });
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'settings') {
      invalidate();
      loadStats();
    }
  });
}
