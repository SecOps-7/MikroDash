// The sidebar's grouping: 23 pages collapsed into 7 categories.
//
// Two things are remembered per user, SERVER-SIDE — whether grouping is on at
// all, and which categories are open. That makes this different from the
// appearance layer next door, which never leaves the browser: localStorage here
// is only a cache, read before the nav paints so the sidebar never renders in
// one shape and then rearranges.
//
// ── THE AUTO-EXPANDED CATEGORY IS STATE, NOT A DERIVATION ───────────────────
//
// Navigating into a page opens the category holding it, and that opening is
// deliberately NOT saved: persisting it would mean visiting one page in each
// category leaves every category open forever, which is grouping that undoes
// itself. So the rendered open set is the saved set PLUS this one.
//
// It has to be stored rather than re-derived from the active page at render
// time, and the live comment says why — deriving it is what made collapsing the
// group you are standing in a no-op, because the click removed it from the
// saved set and the very next render put it straight back. Reproduced as state
// for exactly that reason.
//
// ── THE TOGGLE KEYS ON WHAT IS ON SCREEN ────────────────────────────────────
//
// A header click asks "is this category open?", not "is it in the saved set?".
// An auto-expanded category is open without being saved, so keying on
// membership made the first click PUSH it — expanding an already-open group and
// taking two clicks to shut it.

import { el } from './dom.js';

const NAV_KEY = 'mkd_nav_prefs';

let grouped = true;
/** The SAVED set. The rendered set may add `autoCat`. */
let expanded: string[] = [];
let autoCat: string | null = null;
let saveTimer: ReturnType<typeof setTimeout> | null = null;

/**
 * Paint the nav from the three pieces of state.
 *
 * Takes over from preflight's `#navBoot` stylesheet, which exists only because
 * the elements it needed to class did not exist yet when it ran — so the first
 * thing this does is remove it. Leaving it would pin the sidebar to whatever
 * the cache said and make every toggle below inert.
 */
export function navRender(): void {
  const boot = document.getElementById('navBoot');
  if (boot && boot.parentNode) boot.parentNode.removeChild(boot);

  document.documentElement.setAttribute('data-nav', grouped ? 'grouped' : 'flat');
  document.querySelectorAll<HTMLElement>('.nav-group').forEach((g) => {
    const cat = g.dataset.cat;
    const open = (cat !== undefined && expanded.indexOf(cat) !== -1) ||
      (autoCat !== null && cat === autoCat);
    g.classList.toggle('is-open', open);
    // Set next to the class toggle, the way the router dropdown does it — it is
    // the whole disclosure contract for a screen reader, where the collapsed and
    // expanded widths do not exist.
    const hdr = g.querySelector('.nav-group-hdr');
    if (hdr) hdr.setAttribute('aria-expanded', open ? 'true' : 'false');
  });
  document.querySelectorAll<HTMLInputElement>('.nav-grouped-input')
    .forEach((cb) => { cb.checked = grouped; });
}

/** Cache locally now, tell the server shortly. */
export function navSave(): void {
  const payload = { grouped, expanded: expanded.slice().sort() };
  try { localStorage.setItem(NAV_KEY, JSON.stringify(payload)); } catch { /* private mode */ }
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    void fetch('/api/nav-prefs', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }).catch(() => {
      // Fire and forget, and the UI does NOT revert. A sidebar that snaps shut
      // because a request failed is worse than a preference that did not sync;
      // localStorage already has it, and the next save will carry it.
    });
  }, 400);
}

/**
 * Called by showPage: open the category holding the page just navigated to.
 *
 * Renders only when there is a category — a page outside every group leaves the
 * sidebar exactly as it was rather than collapsing whatever was auto-open.
 */
export function navAutoExpand(cat: string | null | undefined): void {
  if (!cat) return;
  autoCat = cat;
  navRender();
}

/** Read the cache. Server state arrives later and overwrites this. */
function readCache(): void {
  try {
    const cached = JSON.parse(localStorage.getItem(NAV_KEY) || 'null');
    if (cached) {
      // `!== false` rather than truthiness: a blob with no `grouped` key at all
      // means grouped, which is the default the sidebar ships in.
      grouped = cached.grouped !== false;
      expanded = Array.isArray(cached.expanded) ? cached.expanded.slice() : [];
    }
  } catch { /* a corrupt blob is the same as no blob */ }
}

/**
 * Wire the headers and the grouping checkboxes, then paint.
 *
 * A SEPARATE loop from the `.nav-item` one, because a header is not a nav item:
 * it carries no `data-page`, and it must not join the mobile drawer-closing
 * loop either, or expanding a category would slam the drawer shut.
 */
export function initNav(): void {
  readCache();

  document.querySelectorAll<HTMLElement>('.nav-group-hdr').forEach((hdr) => {
    hdr.addEventListener('click', () => {
      const cat = (hdr.parentNode as HTMLElement | null)?.dataset?.cat;
      if (!cat) return;
      const at = expanded.indexOf(cat);
      if (at !== -1 || autoCat === cat) {
        if (at !== -1) expanded.splice(at, 1);
        // Collapsing the category holding the current page is allowed, and holds
        // until you navigate into it again. Clearing the auto-expand is what
        // makes that stick — otherwise the next render puts it straight back.
        if (autoCat === cat) autoCat = null;
      } else {
        expanded.push(cat);
      }
      navRender();
      navSave();
    });
  });

  document.querySelectorAll<HTMLInputElement>('.nav-grouped-input').forEach((cb) => {
    cb.addEventListener('change', () => {
      grouped = cb.checked;
      navRender();
      navSave();
    });
  });

  // The server is the source of truth; the cache above only bought a flash-free
  // first paint. A failure here keeps whatever the cache said.
  //
  // It deliberately does NOT clear the auto-expanded category: the answer that
  // arrives is the SAVED set, and the page the user is standing on is still the
  // page they are standing on.
  void fetch('/api/nav-prefs', { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : null))
    .then((d) => {
      if (!d) return;
      grouped = d.grouped !== false;
      expanded = Array.isArray(d.expanded) ? d.expanded.slice() : [];
      try {
        localStorage.setItem(NAV_KEY, JSON.stringify({ grouped, expanded }));
      } catch { /* private mode */ }
      navRender();
    })
    .catch(() => { /* keep the cache */ });

  navRender();
}
