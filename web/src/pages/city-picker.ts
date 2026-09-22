// The town search behind the router dialog's location box.
//
// ── WHY THIS COMES BEFORE WIRING THE MODAL ──────────────────────────────────
//
// The dialog's save sends `geo: { place: picker.get() }`, and the store reads a
// `place` of null as "clear the override". So a modal wired WITHOUT this picker
// would send null on every save and **wipe an operator's manual location each
// time they edited anything else about the router**. The picker is not the last
// piece of the modal; it is a precondition for wiring it at all.

import { esc } from '../dom';

/** A town as `/api/cities` returns it. */
export interface City {
  name: string; region?: string; cc?: string; lat?: number; lon?: number;
  // Only the AUTOMATIC location carries this — it is the WAN address the server
  // geolocated, and the hint names it. A searched town has no ip.
  ip?: string;
}

/** The shortest query that is worth a request. */
export const CITY_MIN_QUERY = 2;

/** The pause after the last keystroke before the request goes. */
export const CITY_DEBOUNCE_MS = 250;

/**
 * Whether a query is long enough to search.
 *
 * TRIMMED FIRST: a box holding two spaces is not a two-character query, and
 * sending it would ask the server to match every town. Below the floor the
 * original CLOSES the list rather than leaving the previous results up — stale
 * results under a query that no longer produced them read as matches.
 */
export function shouldSearchCity(raw: string): boolean {
  return raw.trim().length >= CITY_MIN_QUERY;
}

/**
 * Whether a response should be discarded.
 *
 * Every request takes a sequence number and only the newest may paint. Typing
 * "ber" issues three requests and they can return in any order, so without this
 * the list can settle on the answer to "be" while the box says "ber".
 */
export function cityResponseIsStale(mine: number, current: number): boolean {
  return mine !== current;
}

/**
 * The results list.
 *
 * THREE STATES, and the empty one carries two different messages: "unavailable"
 * means this install has no city database at all, which is a fact about the
 * deployment rather than about the query. Collapsing it into "No matching town"
 * would send an operator hunting for a spelling that could never match.
 *
 * `esc` HAS NO EXCEPTIONS HERE, and the original says why: the names come from a
 * local database rather than from a user, and they are still going into
 * innerHTML.
 */
export function cityListHtml(
  results: readonly City[], active: number, unavailable: boolean,
): string {
  if (!results.length) {
    return '<div class="cpick-empty">'
      + (unavailable ? 'City search is unavailable on this install.' : 'No matching town.')
      + '</div>';
  }
  return results.map((p, i) =>
    '<div class="cpick-opt' + (i === active ? ' is-active' : '') + '" role="option"'
    + ' data-i="' + i + '">' + esc(p.name)
    // Region and country are joined with a space and EMPTIES ARE DROPPED, so a
    // town with no region does not render a leading gap before its country.
    + '<span class="cpick-cc">' + esc([p.region, p.cc].filter(Boolean).join(' ')) + '</span></div>',
  ).join('');
}


/**
 * The text the box shows for a place.
 *
 * THE REGION IS DROPPED UNLESS IT STARTS WITH A LETTER. Geo databases carry
 * numeric region codes for many countries — "Berlin, 16, DE" reads as noise, so
 * only a named region earns its slot. And a region is only shown alongside a
 * NAME: a region with no town is not a location anyone recognises.
 */
export function formatPlace(p: City | null | undefined): string {
  if (!p) return '';
  const parts: string[] = [];
  if (p.name) parts.push(p.name);
  if (p.name && p.region && /^[A-Za-z]/.test(p.region)) parts.push(p.region);
  if (p.cc) parts.push(p.cc);
  return parts.join(', ');
}

/**
 * The picker's state — what is in the box, and whether it counts as a choice.
 *
 * ── `previewOnly` IS THE WHOLE SAFETY PROPERTY ──────────────────────────────
 *
 * `get()` returns null while the box is only PREVIEWING the automatic location,
 * so opening a router, changing its label and saving does not silently convert
 * that automatic location into a manual override — which would freeze it, stop
 * it following the WAN address, and say nothing on screen.
 *
 * This is the mechanism that makes wiring the router modal safe. Without it the
 * save sends whatever is in the box as `geo.place`, and every edit pins the
 * router to wherever it happened to be geolocated that day.
 *
 * ── AND RESTORING IS NOT CHOOSING ───────────────────────────────────────────
 *
 * `restoreText` puts the box back to what is committed WITHOUT touching
 * `previewOnly`. The original is explicit that this must not be `commit()`:
 * otherwise a previewed automatic location would become a manual override just
 * because someone clicked into the box and out again.
 */
export class CityPickerState {
  private chosen: City | null = null;
  private previewOnly = false;

  /** The value to submit. Null while the box is only previewing. */
  get(): City | null { return this.previewOnly ? null : this.chosen; }

  /** The operator's own choice. */
  set(place: City | null): void { this.commit(place); }

  /**
   * Show what the server worked out — editable, but not yet an override.
   *
   * `!!place` and not `true`: previewing NOTHING is not a preview, so
   * `preview(null)` leaves an empty box that is a committed emptiness. A port
   * setting it unconditionally would make a cleared box report as a preview and
   * then return null from `get()` either way — invisible until something asked
   * `isPreview`.
   */
  preview(place: City | null): void {
    this.commit(place);
    this.previewOnly = !!place;
  }

  clear(): void { this.commit(null); }

  isPreview(): boolean { return this.previewOnly; }

  /** The text the box should show — see `restoreText` above. */
  text(): string { return formatPlace(this.chosen); }

  private commit(place: City | null): void {
    this.chosen = place || null;
    // A commit is always the operator's own.
    this.previewOnly = false;
  }
}

/**
 * Mount a town picker onto an input, a results list and a Clear button.
 *
 * ── THE LIVE APP HAS ONE OF THESE AND THIS PORT CURRENTLY HAS TWO ───────────
 *
 * `_mountCityPicker(inputEl, listEl, opts)` (app.js:11225) is shared by the
 * router dialog and the site form. This port grew the modal's copy inline first,
 * before there was a second caller to justify a shared one. This function is the
 * shared one, and the SITE FORM uses it; `router-modal.ts` still has its own.
 *
 * That duplication is deliberate for exactly one reason: the modal's picker
 * wiring is NOT differentially gated — `city-picker-check.js` covers the state
 * machine below, not the fetch, the debounce or the list. Migrating it would be
 * an ungated refactor of working code, which is a worse trade than two copies
 * with the divergence written down. **The migration is the follow-up**, and when
 * it happens this comment goes with it.
 *
 * The one modal-specific thing kept OUT of here is its test gate's
 * `invalidate()`; callers get `onChange` instead.
 */
export function mountCityPicker(
  inputEl: HTMLInputElement,
  listEl: HTMLElement,
  opts: { clearEl?: HTMLElement | null; onChange?: (p: City | null) => void } = {},
): CityPickerState {
  const state = new CityPickerState();
  let results: City[] = [];
  let active = -1;
  let seq = 0;
  let debounce: ReturnType<typeof setTimeout> | null = null;

  function closeList(): void {
    listEl.hidden = true;
    listEl.innerHTML = '';
    results = [];
    active = -1;
    inputEl.setAttribute('aria-expanded', 'false');
  }

  function paintBox(): void {
    inputEl.value = state.text();
  }

  function commit(place: City | null): void {
    state.set(place);
    paintBox();
    closeList();
    if (opts.onChange) opts.onChange(state.get());
  }

  inputEl.addEventListener('input', () => {
    if (debounce) clearTimeout(debounce);
    if (!shouldSearchCity(inputEl.value)) { closeList(); return; }
    const q = inputEl.value.trim();
    debounce = setTimeout(() => {
      // A SEQUENCE NUMBER, not a cancel: a slow "ber" must not land after a fast
      // "berlin" and repaint the list with the wrong towns.
      const mine = ++seq;
      fetch('/api/cities?q=' + encodeURIComponent(q), { credentials: 'same-origin' })
        .then((r) => r.json())
        .then((j: { unavailable?: boolean; cities?: City[] }) => {
          if (cityResponseIsStale(mine, seq)) return;
          results = (j && j.cities) || [];
          active = results.length ? 0 : -1;
          listEl.innerHTML = cityListHtml(results, active, !!(j && j.unavailable));
          listEl.hidden = false;
          inputEl.setAttribute('aria-expanded', 'true');
        })
        .catch(() => { if (!cityResponseIsStale(mine, seq)) closeList(); });
    }, CITY_DEBOUNCE_MS);
  });

  listEl.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const row = t && t.closest ? (t.closest('[data-i]') as HTMLElement | null) : null;
    if (!row) return;
    const i = parseInt(row.getAttribute('data-i') || '', 10);
    const picked = results[i];
    if (picked) commit(picked);
  });

  // LEAVING THE BOX RESTORES the committed text rather than committing what was
  // typed — typed text must never become a location. And it must not COMMIT, or
  // a previewed automatic location would become an override just from a click in
  // and out. The delay lets a click on a result row land first.
  inputEl.addEventListener('blur', () => {
    setTimeout(() => { paintBox(); closeList(); }, 150);
  });

  opts.clearEl?.addEventListener('click', () => commit(null));

  return state;
}
