/**
 * The Sites card's wiring.
 *
 * ── EVERY DECISION HERE IS SOMEWHERE ELSE ───────────────────────────────────
 *
 * The renderers and the two request shapes live in `settings.ts` and are gated
 * against the live implementations by the access-summary check and
 * The site-save check. What is in THIS file is only the parts a gate
 * cannot drive: the fetches, the cache, the listeners and the socket
 * subscription. Keeping it that thin is deliberate — anything with a decision in
 * it belongs where it can be compared.
 *
 * ── THE CACHE IS SHARED, AND THAT IS NOT AN OPTIMISATION ────────────────────
 *
 * The live app publishes `window._sitesById` so the device table and the device
 * modal can turn a site id into a name without re-fetching. Reproduced, because
 * a second fetch is not the cost — a second SOURCE is. Two caches drift, and the
 * symptom is a device row naming a site the form says it is not in.
 */

import { el } from '../dom';
import { mountCityPicker, type City, type CityPickerState } from './city-picker';
import {
  siteTableHtml, siteRouterCounts, siteMemberRowsHtml, siteSavePlan, siteDeletePrompt,
  type SiteView, type SiteMemberDevice,
} from './settings';

interface SiteRecord extends SiteView {
  place_name?: string | null;
  place_region?: string | null;
  place_cc?: string | null;
  lat?: number | null;
  lon?: number | null;
}

let sites: SiteRecord[] = [];

// ── THE PICKER IS MOUNTED ONCE, LAZILY ──────────────────────────────────────
//
// `_sitePickerEnsure` in the live app does the same, and the reason is the
// listeners: mounting on every form open would add a fresh `input` and `blur`
// handler each time, so the fourth edit would fire four searches per keystroke.
let picker: CityPickerState | null = null;

function ensurePicker(): CityPickerState | null {
  if (picker) return picker;
  const input = el<HTMLInputElement>('sf_place');
  const list = el('sf_placeList');
  if (!input || !list) return null;
  picker = mountCityPicker(input, list, { clearEl: el('sf_placeClear') });
  return picker;
}

/** The fleet, as the device checkboxes need it. Supplied by the caller. */
let fleetOf: () => SiteMemberDevice[] = () => [];

/** Site ids to names, shared with the device table and modal. */
export const sitesById: Record<string, SiteRecord> = {};

function cache(list: SiteRecord[]): void {
  sites = Array.isArray(list) ? list : [];
  for (const k of Object.keys(sitesById)) delete sitesById[k];
  for (const s of sites) sitesById[s.id] = s;
}

function renderTable(): void {
  const tb = el('siteTbody');
  if (!tb) return;
  tb.innerHTML = siteTableHtml(sites, siteRouterCounts(fleetOf()));
}

async function load(): Promise<void> {
  const tb = el('siteTbody');
  try {
    const r = await fetch('/api/sites', { credentials: 'same-origin' });
    const j = await r.json();
    cache(j && j.ok ? j.sites : []);
    renderTable();
  } catch {
    // THE MESSAGE IS THE LIVE ONE, in the table rather than a banner: a card
    // that silently shows "No sites yet" after a failed load invites somebody to
    // create a site that already exists and be told the name is taken.
    if (tb) {
      tb.innerHTML = '<tr><td colspan="4" style="padding:.75rem .5rem;color:var(--text-muted);'
        + 'font-size:.76rem">Could not load sites.</td></tr>';
    }
  }
}

function formError(msg: string): void {
  const e = el('sf_error');
  if (!e) return;
  e.textContent = msg || '';
  e.style.display = msg ? 'block' : 'none';
}

function hideForm(): void {
  el('siteFormWrap')?.classList.remove('open');
}

function showForm(site: SiteRecord | null): void {
  const idEl = el<HTMLInputElement>('sf_id');
  const nameEl = el<HTMLInputElement>('sf_name');
  const descEl = el<HTMLInputElement>('sf_description');
  const box = el('sf_routers');
  const wrap = el('siteFormWrap');
  const title = el('sf_title');
  if (!idEl || !nameEl || !descEl || !box || !wrap) return;

  idEl.value = site ? site.id : '';
  nameEl.value = site ? site.name : '';
  descEl.value = site && site.description ? site.description : '';
  formError('');

  // SEEDED FROM THE THREE place_* COLUMNS, not from lat/lon. A row written
  // before there was a picker has coordinates and no name, and showing an empty
  // box over a set location would invite somebody to overwrite it.
  const p = ensurePicker();
  if (p) {
    p.set(site && site.place_name
      ? {
        name: site.place_name,
        region: site.place_region || '',
        cc: site.place_cc || '',
        lat: site.lat ?? undefined,
        lon: site.lon ?? undefined,
      }
      : null);
    const box = el<HTMLInputElement>('sf_place');
    if (box) box.value = p.text();
  }

  box.innerHTML = siteMemberRowsHtml(fleetOf(), site, sitesById);
  if (title) title.textContent = site ? 'Edit Site' : 'Add Site';
  wrap.classList.add('open');
  nameEl.focus();
}

/** The ticked boxes, in DOM order — which is fleet order, since that is how the
 *  rows were rendered. */
function checkedRouterIds(): string[] {
  const box = el('sf_routers');
  if (!box) return [];
  return Array.prototype.slice
    .call(box.querySelectorAll('[data-site-router]:checked'))
    .map((n: Element) => n.getAttribute('data-site-router') || '');
}

async function save(): Promise<void> {
  const idEl = el<HTMLInputElement>('sf_id');
  const nameEl = el<HTMLInputElement>('sf_name');
  const descEl = el<HTMLInputElement>('sf_description');
  if (!idEl || !nameEl || !descEl) return;

  // THE PICKER'S VALUE, exactly as the live form sends it: an empty box CLEARS
  // the location. `get()` returns null while the box is only PREVIEWING, so a
  // reference location never becomes a manual override just because somebody
  // renamed the site.
  //
  // This replaced a workaround that re-sent the site's own stored place, which
  // was there for the one tick between mounting the card and mounting the
  // picker. Without a picker, `place ?? null` would have wiped the map pin of
  // every site anybody edited.
  const place: City | null = ensurePicker()?.get() ?? null;

  const plan = siteSavePlan({
    id: idEl.value,
    name: nameEl.value,
    description: descEl.value,
    place,
    routerIds: checkedRouterIds(),
  });
  if ('error' in plan) {
    formError(plan.error);
    return;
  }

  try {
    const [first, second] = plan.requests;
    const r1 = await fetch(first.path, {
      method: first.method, credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(first.body),
    });
    const j1 = await r1.json();
    if (!r1.ok || !j1 || !j1.ok) {
      formError((j1 && j1.error) || 'Could not save the site');
      return;
    }
    // The membership call goes SECOND because a new site has no id until now.
    const siteId = idEl.value || (j1.site && j1.site.id);
    if (!siteId) return;
    await fetch(second.path.replace('{id}', encodeURIComponent(siteId)), {
      method: second.method, credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(second.body),
    });
    hideForm();
    await load();
  } catch {
    formError('Could not save the site');
  }
}

async function remove(id: string, name: string, count: number): Promise<void> {
  if (!window.confirm(siteDeletePrompt(name, count))) return;
  try {
    await fetch('/api/sites/' + encodeURIComponent(id), {
      method: 'DELETE', credentials: 'same-origin',
    });
  } catch {
    // Swallowed, as the live handler does: the reload below shows the truth
    // either way, and a site that did not go is still in the table.
  }
  await load();
}

/**
 * Mount the card. `fleet` is read on every render rather than captured, because
 * the device list changes under this page and a snapshot taken at mount would
 * make the checkboxes stale within a minute.
 */
export function initSitesCard(fleet: () => SiteMemberDevice[]): void {
  const tbody = el('siteTbody');
  if (!tbody) return;
  fleetOf = fleet;

  // DELEGATED: the table is rebuilt on every load, so per-row listeners would be
  // lost the first time anything changed.
  tbody.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const btn = t && t.closest ? (t.closest('[data-site-action]') as HTMLElement | null) : null;
    if (!btn) return;
    const id = btn.getAttribute('data-site-id') || '';
    const site = sitesById[id];
    if (!site) return;
    if (btn.getAttribute('data-site-action') === 'edit') showForm(site);
    else void remove(id, site.name, siteRouterCounts(fleetOf())[id] || 0);
  });

  el('addSiteBtn')?.addEventListener('click', () => showForm(null));
  el('sf_save')?.addEventListener('click', () => void save());
  el('sf_cancel')?.addEventListener('click', hideForm);

  void load();
}

/** Another administrator's change must not leave this tab stale. */
export function onSitesUpdate(list: unknown): void {
  cache(Array.isArray(list) ? (list as SiteRecord[]) : []);
  renderTable();
}
