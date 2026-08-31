// The shared DOM helpers, ported from public/app.js.
//
// Ported, not redesigned. Each one produces the same markup the live app
// produces, because the stylesheet and the DOM shape are the contract: a
// helper that emitted tidier HTML would render a page that no longer matches
// the one it replaced.

/** Escape for HTML text and attribute contexts, exactly as `esc` does. */
export function esc(s: unknown): string {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

export function el<T extends HTMLElement = HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

/**
 * The attributes that make a rendered row editable by the resource engine.
 *
 * `id` addresses the RouterOS row; `identity` is round-tripped so the server can
 * refuse the write if the row no longer carries it — a `.id` survives a rename,
 * which makes it the right key to address a row with and the wrong one to
 * identify it by.
 *
 * No `style` here on purpose: several of these rows already carry one, and a
 * second style attribute is invalid.
 */
export function resRow(id: string, identity: string | null, resource?: string): string {
  if (!id) return '';
  return ` data-id="${esc(id)}" data-identity="${esc(identity == null ? '' : identity)}"` +
    (resource ? ` data-res="${esc(resource)}"` : '');
}

export function debounce(fn: () => void, ms: number): () => void {
  let t: number | undefined;
  return () => {
    if (t !== undefined) clearTimeout(t);
    t = setTimeout(fn, ms) as unknown as number;
  };
}

export interface SortCol {
  key?: string;
  label: string;
  cls?: string;
  style?: string;
}

/** The direction of a sort, as a multiplier. */
export type SortDir = 'asc' | 'desc';

export interface SortState {
  col: string;
  dir: SortDir;
}

/**
 * The multiplier for a sort direction.
 *
 * Every comparator reading a SortState asks here rather than keeping a +1/-1 of
 * its own. That second convention is exactly what broke nine tables in the live
 * app: this helper owns `sortState` and calls back with no arguments, so a
 * caller that recomputed the state from a `key` it never received assigned
 * `undefined` to the column and killed the sort on the first click — while its
 * numeric `dir` produced a `sort-1` class no stylesheet defines. Fixed upstream;
 * this side follows it.
 */
export function sortMul(sortState: SortState): number {
  return sortState.dir === 'desc' ? -1 : 1;
}

/**
 * Sort rows by one column, the way every report table does.
 *
 * ── NULLS SORT TO THE FRONT ASCENDING, THE BACK DESCENDING ──────────────────
 *
 * Not "always last", which is the commoner choice: this follows the value, so
 * flipping the direction genuinely reverses the table. On the ping report that
 * matters — a null `rtt_ms` is a TIMED-OUT probe, and "sort by RTT descending"
 * putting the timeouts at the bottom is what an operator means by it.
 *
 * Numbers compare numerically and everything else compares as a lower-cased
 * string, so a column holding both does not throw — it just sorts oddly, which
 * is the original's behaviour and better than a crash on a mixed column.
 */
export function sortRows<T>(rows: readonly T[], col: string, dir: SortDir): T[] {
  return rows.slice().sort((a, b) => {
    const av = (a as Record<string, unknown>)[col];
    const bv = (b as Record<string, unknown>)[col];
    // `== null` on purpose: it catches undefined too, which is what a row
    // missing the column entirely has.
    if (av == null && bv == null) return 0;
    if (av == null) return dir === 'asc' ? -1 : 1;
    if (bv == null) return dir === 'asc' ? 1 : -1;
    if (typeof av === 'number' && typeof bv === 'number') {
      return dir === 'asc' ? av - bv : bv - av;
    }
    const as = String(av).toLowerCase();
    const bs = String(bv).toLowerCase();
    if (dir === 'asc') return as < bs ? -1 : as > bs ? 1 : 0;
    return as > bs ? -1 : as < bs ? 1 : 0;
  });
}

/**
 * A sortable table header.
 *
 * The helper owns `sortState`: it sets the column and flips the direction before
 * calling back. `onSort` therefore takes no arguments and callers just re-render.
 */
export function renderSortHeader(
  theadId: string,
  cols: SortCol[],
  sortState: SortState,
  onSort: () => void,
): void {
  const tr = el(theadId);
  if (!tr) return;
  tr.innerHTML = cols.map((c) => {
    const sortCls = c.key === sortState.col ? 'sort-' + sortState.dir : '';
    const allCls = [c.cls || '', sortCls].filter(Boolean).join(' ');
    const cls = allCls ? ` class="${allCls}"` : '';
    const base = c.key ? 'cursor:pointer;user-select:none;' : '';
    const sty = (base || c.style) ? ` style="${base}${c.style || ''}"` : '';
    return `<th${sty}${cls}>${c.label}</th>`;
  }).join('');

  tr.querySelectorAll('th').forEach((th, i) => {
    const key = cols[i]?.key;
    if (!key) return;
    th.addEventListener('click', () => {
      if (sortState.col === key) {
        sortState.dir = sortState.dir === 'asc' ? 'desc' : 'asc';
      } else {
        sortState.col = key;
        sortState.dir = 'asc';
      }
      onSort();
    });
  });
}

/**
 * A data volume in megabytes, scaled to the unit that reads best.
 *
 * The thresholds are the original's and they are DECIMAL — 1000 MB is a GB here,
 * not 1024. Changing that would move every total on the bandwidth report by two
 * and a half percent, which is exactly the size of discrepancy nobody notices
 * and everybody argues about later.
 */
export function fmtDataMB(mb: number | null | undefined): string {
  const n = +(mb ?? 0) || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(2) + ' TB';
  if (n >= 1000) return (n / 1000).toFixed(2) + ' GB';
  if (n >= 1) return n.toFixed(1) + ' MB';
  return (n * 1000).toFixed(0) + ' KB';
}

/**
 * The largest value in an array, WITHOUT spreading it.
 *
 * `Math.max(...a)` passes every element as an argument and blows the call stack
 * somewhere past sixty-odd thousand of them. A report query returns up to
 * 100,000 rows, so the spread form is not a style preference here — it is a
 * crash on a long range, and one that only appears on the ranges an operator
 * reaches for when something has gone wrong.
 *
 * The live app hit this and left a comment on `maxOf` saying so; this port used
 * the spread anyway on its first pass at the ping tab, which is why the comment
 * is repeated here rather than referenced.
 *
 * An empty array is 0, not -Infinity, so a caller can format the result.
 */
export function maxOf(a: readonly (number | null | undefined)[]): number {
  let m = -Infinity;
  for (let i = 0; i < a.length; i++) {
    const v = +(a[i] ?? 0);
    if (v > m) m = v;
  }
  return m === -Infinity ? 0 : m;
}

/** Mbps for display, matching `fmtMbps` in public/app.js exactly. */
// app.js's fmtBytes, to the digit. 1024-based; the thresholds and the decimal
// places are what the rendered cell says, so a "nearly right" byte formatter is
// a DOM difference on every row that has a size.
//
// Lives here rather than in a page, because two pages now render byte counts —
// Packages and PPP — and the Add buttons already demonstrated what happens when
// one helper acquires a second copy.
export function fmtBytes(b: number): string {
  if (b >= 1099511627776) return (b / 1099511627776).toFixed(2) + ' TB';
  if (b >= 1073741824) return (b / 1073741824).toFixed(1) + ' GB';
  if (b >= 1048576) return (b / 1048576).toFixed(1) + ' MB';
  if (b >= 1024) return (b / 1024).toFixed(1) + ' KB';
  return b + ' B';
}

/**
 * app.js's parseUptime: a RouterOS duration, spaced out for a human.
 *
 * "1w2d3h4m5s" becomes "1w 2d 3h 4m". SECONDS ARE DROPPED and a zero component
 * is omitted entirely, so a duration under a minute falls back to the raw input
 * rather than to an empty string.
 *
 * IT RETURNS A STRING, which matters more than it looks: the PPP page uses it as
 * the sort key for its Uptime column, so that column sorts lexicographically on
 * a formatted duration and puts "10m" before "2h". That is the live behaviour,
 * reproduced deliberately; reported in ../MikroDash/ToDo.md.
 */
export function parseUptime(raw: unknown): string {
  const s = String(raw || '');
  const parts: string[] = [];
  for (const unit of ['w', 'd', 'h', 'm']) {
    const v = (s.match(new RegExp('(\\d+)' + unit)) || ['', '0'])[1] as string;
    if (+v) parts.push(v + unit);
  }
  return parts.length ? parts.join(' ') : (s || '—');
}

export function fmtMbps(v: number | null | undefined): string {
  const n = Number(v) || 0;
  if (n >= 1000) return (n / 1000).toFixed(2) + ' Gbps';
  if (n >= 1) return n.toFixed(2) + ' Mbps';
  return (n * 1000).toFixed(1) + ' Kbps';
}

// ── Shared with the Wifi Clients page ───────────────────────────────────────
//
// Both pages show bands and SSIDs, and two copies of the palette would mean one
// network wearing two colours depending on which page you were looking at. The
// live app arranges this by hanging both on `window` from the Wifi Clients
// IIFE; here they are ordinary exports, and `installWifiGlobals` publishes them
// under the same names so a LIFTED live renderer finds them during a DOM
// comparison. Wifi Clients will import them rather than redefine them.

/** The band pill. Three bands, spelled as both pages spell them. */
export function bandBadge(band: string): string {
  if (!band) return '';
  const cls = band === '5GHz' ? 'wl-band-5' : band === '6GHz' ? 'wl-band-6' : 'wl-band-24';
  return '<span class="wl-band ' + cls + '">' + band + '</span>';
}

const SSID_COLOURS = [
  'var(--accent-rx)',      /* blue   */
  'rgba(52,211,153,.95)',  /* green  */
  'rgba(167,139,250,.95)', /* purple */
  'rgba(251,191,36,.95)',  /* amber  */
  'rgba(244,114,182,.95)', /* pink   */
  'rgba(45,212,191,.95)',  /* teal   */
  'rgba(251,146,60,.95)',  /* orange */
];

/**
 * One colour per SSID, HASHED rather than counted down the list.
 *
 * A dual-band SSID appears on two rows and must read as the same network on
 * both, so the colour has to come from the name rather than from position.
 * Collisions probe forward; past SSID_COLOURS.length networks the palette is
 * exhausted and colours necessarily repeat, and probing simply wraps back to the
 * preferred slot.
 *
 * `>>> 0` is carried over verbatim rather than tidied away: it coerces to
 * unsigned 32-bit, which is what keeps the hash positive. Without it `h` goes
 * negative and `h % length` picks a different slot — a different colour for the
 * same SSID.
 */
export function ssidColours(names: string[]): Record<string, string> {
  const taken: Record<number, boolean> = {};
  const out: Record<string, string> = {};
  for (const name of names) {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
    const start = h % SSID_COLOURS.length;
    let slot = start;
    for (let n = 0; n < SSID_COLOURS.length && taken[slot]; n++) {
      slot = (slot + 1) % SSID_COLOURS.length;
    }
    taken[slot] = true;
    out[name] = SSID_COLOURS[slot]!;
  }
  return out;
}

/**
 * Publish both under the names the live app uses.
 *
 * Not for this app's own benefit — its pages import them directly. It is so a
 * renderer LIFTED by tools/live-renderer.js finds them: the lifted Wifi
 * Networks code reads `window._bandBadge` and `window._ssidColours`, and
 * without them it silently takes its fallback path while the port takes the
 * real one, which would read as a rendering difference that does not exist.
 */
export function installWifiGlobals(): void {
  const w = window as unknown as {
    _bandBadge?: typeof bandBadge;
    _ssidColours?: typeof ssidColours;
  };
  w._bandBadge ??= bandBadge;
  w._ssidColours ??= ssidColours;
}
