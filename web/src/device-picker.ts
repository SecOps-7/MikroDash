// The device picker: a search box over the fleet, with what is picked shown
// as pills. ONE component for every place the app chooses devices - credential
// profiles, the MikroDash login, a deploy's targets, a site's members, a
// notification channel's scope, and DNS fleet's two lists.
//
// ── WHY IT REPLACED A CHECKBOX PER DEVICE ───────────────────────────────────
//
// Seven pickers listed every device as a checkbox. Fine for ten, unusable for
// several hundred: the list scrolls forever and what is ticked is scattered
// through it. Here nothing is listed until it is searched for, and what is
// picked sits together as pills.
//
// ── SITES MEAN ONE OF TWO THINGS, AND THE PILL SAYS WHICH ──────────────────
//
//  - `rule`: a picked site is kept AS A SITE. Its devices are in scope now and
//    as the site changes (credential profiles). They are not listed one by one.
//  - `shortcut`: picking a site adds the devices it holds NOW, as device pills.
//    A device joining the site later is not added.
//
// ── NOTHING HERE WRITES ANYTHING ────────────────────────────────────────────
//
// The picker holds a selection. Each caller decides what Apply does with it,
// exactly as the checkbox lists did.

import { esc } from './dom';

export interface PickItem {
  id: string;
  label: string;
  host?: string;
  model?: string;
  siteIds?: string[];
  /** A line under the name in the results, e.g. "also in Branch". */
  detail?: string;
}

export interface PickSite { id: string; name: string }

/** How one device's pill is drawn, beyond its name. */
export interface PillInfo {
  kind?: 'ok' | 'warn' | 'bad' | 'wait';
  /** A short state word on the pill, e.g. "refused". */
  text?: string;
  title?: string;
  /** Cannot be removed here (e.g. covered by a site, or already linked). */
  locked?: boolean;
  /** Extra markup inside the pill, e.g. a Retry button. Caller escapes it. */
  action?: string;
}

export interface PickerConfig {
  items: () => PickItem[];
  sites?: () => PickSite[];
  siteMode?: 'rule' | 'shortcut';
  /** Devices always shown as locked pills and never offered (e.g. linked). */
  fixed?: () => string[];
  info?: (id: string) => PillInfo | undefined;
  /** The order of picking matters: the first is the canary. */
  ordered?: boolean;
  placeholder?: string;
  /** What the box says before anything is picked. */
  emptyText?: string;
  onChange?: () => void;
  /** "Add all" and a site shortcut ask first above this many. */
  confirmAbove?: number;
}

export interface Selection { items: string[]; sites: string[] }

export interface PickerState extends Selection {
  q: string;
  /** The highlighted result, for the keyboard. */
  active: number;
  open: boolean;
  expanded: boolean;
}

/** How many results are drawn; "Add all" still reaches every match. */
export const RESULT_CAP = 50;
/** How many pills are drawn before "+N more". */
export const PILL_CAP = 20;

export function newState(sel?: Partial<Selection>): PickerState {
  return { items: [...(sel?.items ?? [])], sites: [...(sel?.sites ?? [])], q: '', active: 0, open: false, expanded: false };
}

/** Every search term must match the name, address, model, a site or the detail. */
export function matches(it: PickItem, q: string, siteNames: Record<string, string>): boolean {
  const terms = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (!terms.length) return true;
  const hay = [it.label, it.host, it.model, it.detail, ...(it.siteIds ?? []).map((s) => siteNames[s] ?? '')]
    .join(' ').toLowerCase();
  return terms.every((t) => hay.includes(t));
}

function siteNameMap(cfg: PickerConfig): Record<string, string> {
  const m: Record<string, string> = {};
  for (const s of cfg.sites?.() ?? []) m[s.id] = s.name;
  return m;
}

/** Devices in scope through a picked site, in `rule` mode. */
export function coveredBySites(cfg: PickerConfig, st: Selection): Set<string> {
  const out = new Set<string>();
  if (cfg.siteMode !== 'rule' || !st.sites.length) return out;
  const on = new Set(st.sites);
  for (const it of cfg.items()) if ((it.siteIds ?? []).some((s) => on.has(s))) out.add(it.id);
  return out;
}

/** What a search offers: sites first, then devices not already in. */
export function results(cfg: PickerConfig, st: PickerState): { sites: PickSite[]; items: PickItem[]; total: number } {
  const names = siteNameMap(cfg);
  const taken = new Set([...st.items, ...(cfg.fixed?.() ?? []), ...coveredBySites(cfg, st)]);
  const all = cfg.items().filter((it) => !taken.has(it.id) && matches(it, st.q, names));
  const q = st.q.trim().toLowerCase();
  const sites = cfg.siteMode
    ? (cfg.sites?.() ?? []).filter((s) => !st.sites.includes(s.id) && (!q || s.name.toLowerCase().includes(q)))
    : [];
  return { sites, items: all.slice(0, RESULT_CAP), total: all.length };
}

/** The devices a site shortcut would add: in the site, not already in. */
export function siteMembers(cfg: PickerConfig, st: Selection, siteId: string): string[] {
  const taken = new Set([...st.items, ...(cfg.fixed?.() ?? [])]);
  return cfg.items().filter((it) => (it.siteIds ?? []).includes(siteId) && !taken.has(it.id)).map((it) => it.id);
}

const RANK: Record<string, number> = { bad: 0, warn: 1 };

/** The pills, in the order drawn: sites, then devices with a problem, then the rest. */
export function pills(cfg: PickerConfig, st: Selection): { kind: 'site' | 'item'; id: string }[] {
  const out: { kind: 'site' | 'item'; id: string }[] = st.sites.map((id) => ({ kind: 'site' as const, id }));
  const seen = new Set<string>();
  const ids: string[] = [];
  const push = (id: string): void => { if (!seen.has(id)) { seen.add(id); ids.push(id); } };
  (cfg.fixed?.() ?? []).forEach(push);
  st.items.forEach(push);
  // A device in scope through a site appears on its own only when it needs
  // attention: hundreds of healthy ones are what the site pill's count is for.
  for (const id of coveredBySites(cfg, st)) {
    const k = cfg.info?.(id)?.kind;
    if (k === 'bad' || k === 'warn') push(id);
  }
  if (!cfg.ordered) {
    const rank = (id: string): number => RANK[cfg.info?.(id)?.kind ?? ''] ?? 2;
    ids.sort((a, b) => rank(a) - rank(b));
  }
  return out.concat(ids.map((id) => ({ kind: 'item' as const, id })));
}

function pillHtml(cfg: PickerConfig, p: { kind: 'site' | 'item'; id: string }, at: number,
  byId: Map<string, PickItem>, siteNames: Record<string, string>, covered: Set<string>): string {
  if (p.kind === 'site') {
    const n = cfg.items().filter((it) => (it.siteIds ?? []).includes(p.id)).length;
    const name = siteNames[p.id] ?? p.id;
    return `<span class="dp-pill dp-site" title="${esc('Every device in ' + name + ', now and as the site changes')}">`
      + `<span class="dp-pill-kind">Site</span>${esc(name)}<span class="dp-pill-n">${n}</span>`
      + `<button type="button" class="dp-x" data-dp-rmsite="${esc(p.id)}" aria-label="${esc('Remove site ' + name)}">&times;</button></span>`;
  }
  const it = byId.get(p.id);
  const name = it ? it.label : p.id;
  const inf = cfg.info?.(p.id) ?? {};
  const viaSite = covered.has(p.id) && !cfg.fixed?.().includes(p.id);
  const locked = inf.locked || viaSite || !!cfg.fixed?.().includes(p.id);
  const order = cfg.ordered
    ? (at === 0 ? '<span class="dp-pill-kind dp-canary">Canary</span>' : `<span class="dp-pill-n">#${at + 1}</span>`)
    : '';
  const title = [it?.host, inf.title, viaSite ? 'In scope through a site' : ''].filter(Boolean).join(' - ');
  return `<span class="dp-pill${inf.kind ? ' dp-' + inf.kind : ''}"${title ? ` title="${esc(title)}"` : ''}>`
    + order + esc(name)
    + (inf.text ? `<span class="dp-pill-state">${esc(inf.text)}</span>` : '')
    + (inf.action ?? '')
    + (locked ? '' : `<button type="button" class="dp-x" data-dp-rm="${esc(p.id)}" aria-label="${esc('Remove ' + name)}">&times;</button>`)
    + '</span>';
}

/** The results panel. Empty when closed. */
export function resultsHtml(cfg: PickerConfig, st: PickerState): string {
  if (!st.open) return '';
  const r = results(cfg, st);
  const names = siteNameMap(cfg);
  const rows: string[] = [];
  let i = 0;
  for (const s of r.sites) {
    const n = cfg.items().filter((it) => (it.siteIds ?? []).includes(s.id)).length;
    rows.push(`<div class="dp-opt${i === st.active ? ' is-active' : ''}" role="option" aria-selected="${i === st.active}" data-dp-addsite="${esc(s.id)}">`
      + `<span class="dp-pill-kind">Site</span><span class="dp-opt-name">${esc(s.name)}</span>`
      + `<span class="dp-opt-meta">${n} device${n === 1 ? '' : 's'}${cfg.siteMode === 'shortcut' ? ', added now' : ''}</span></div>`);
    i++;
  }
  for (const it of r.items) {
    const sites = (it.siteIds ?? []).map((s) => names[s]).filter(Boolean).join(', ');
    const meta = [it.host, it.model, sites].filter(Boolean).join(' · ');
    rows.push(`<div class="dp-opt${i === st.active ? ' is-active' : ''}" role="option" aria-selected="${i === st.active}" data-dp-add="${esc(it.id)}">`
      + `<span class="dp-opt-name">${esc(it.label)}</span>`
      + (meta ? `<span class="dp-opt-meta">${esc(meta)}</span>` : '')
      + (it.detail ? `<span class="dp-opt-detail">${esc(it.detail)}</span>` : '')
      + '</div>');
    i++;
  }
  if (!rows.length) return '<div class="dp-results" role="listbox"><div class="dp-none">No devices match.</div></div>';
  const more = r.total > r.items.length ? `<div class="dp-none">Showing ${r.items.length} of ${r.total}. Type more to narrow it.</div>` : '';
  const all = r.total > 1 ? `<button type="button" class="dp-all" data-dp-all="1">Add all ${r.total} matching devices</button>` : '';
  return `<div class="dp-results" role="listbox">${rows.join('')}${more}${all}</div>`;
}

/** The pills area. */
export function pickedHtml(cfg: PickerConfig, st: PickerState): string {
  const list = pills(cfg, st);
  const byId = new Map(cfg.items().map((it) => [it.id, it]));
  const names = siteNameMap(cfg);
  const covered = coveredBySites(cfg, st);
  if (!list.length) return `<div class="dp-empty">${esc(cfg.emptyText ?? 'Nothing picked yet. Search above to add devices.')}</div>`;
  const shown = st.expanded ? list : list.slice(0, PILL_CAP);
  let itemAt = 0;
  const html = shown.map((p) => pillHtml(cfg, p, p.kind === 'item' ? itemAt++ : 0, byId, names, covered)).join('');
  const rest = list.length - shown.length;
  const toggle = list.length > PILL_CAP
    ? `<button type="button" class="dp-more" data-dp-more="1">${rest > 0 ? '+' + rest + ' more' : 'Show fewer'}</button>` : '';
  const count = st.items.length + st.sites.length;
  const clear = count > 1 ? '<button type="button" class="dp-clear" data-dp-clear="1">Clear</button>' : '';
  return `<div class="dp-pills">${html}${toggle}${clear}</div>`;
}

export function pickerHtml(cfg: PickerConfig, st: PickerState): string {
  return '<div class="dp-box">'
    + `<input class="sform-input dp-q" type="search" role="combobox" aria-autocomplete="list" aria-expanded="${st.open}"`
    + ` data-dp-q="1" autocomplete="off" spellcheck="false" placeholder="${esc(cfg.placeholder ?? 'Search devices by name, address, model or site')}"`
    + ` value="${esc(st.q)}">`
    + `<div class="dp-res-host">${resultsHtml(cfg, st)}</div></div>`
    + `<div class="dp-picked-host">${pickedHtml(cfg, st)}</div>`;
}

export interface Picker {
  get(): Selection;
  set(sel: Partial<Selection>): void;
  /** Redraw after the caller's data (items, states) changed. */
  refresh(): void;
}

/** Mounts a picker in `host`. Every listener is on `host`, so a host whose
 *  markup is replaced wholesale takes its picker with it. */
export function mountPicker(host: HTMLElement, cfg: PickerConfig): Picker {
  const st = newState();
  const limit = cfg.confirmAbove ?? 10;
  const changed = (): void => { cfg.onChange?.(); };

  const q = (): HTMLInputElement | null => (host.querySelector ? host.querySelector<HTMLInputElement>('[data-dp-q]') : null);
  function drawResults(): void {
    const box = host.querySelector ? host.querySelector('.dp-res-host') : null;
    if (box) box.innerHTML = resultsHtml(cfg, st); else draw();
    const input = q();
    if (input) input.setAttribute('aria-expanded', String(st.open));
  }
  function drawPicked(): void {
    const box = host.querySelector ? host.querySelector('.dp-picked-host') : null;
    if (box) box.innerHTML = pickedHtml(cfg, st); else draw();
  }
  function draw(): void { host.innerHTML = pickerHtml(cfg, st); }

  function addItems(ids: string[]): void {
    for (const id of ids) if (!st.items.includes(id)) st.items.push(id);
    st.active = 0;
    drawResults();
    drawPicked();
    changed();
  }
  function addSite(id: string): void {
    if (cfg.siteMode === 'rule') {
      if (!st.sites.includes(id)) st.sites.push(id);
      drawResults();
      drawPicked();
      changed();
      return;
    }
    const ids = siteMembers(cfg, st, id);
    const name = (cfg.sites?.() ?? []).find((s) => s.id === id)?.name ?? id;
    if (ids.length > limit && !window.confirm(`Add the ${ids.length} devices in ${name}?`)) return;
    addItems(ids);
  }
  function addAll(): void {
    const names = siteNameMap(cfg);
    const taken = new Set([...st.items, ...(cfg.fixed?.() ?? []), ...coveredBySites(cfg, st)]);
    const ids = cfg.items().filter((it) => !taken.has(it.id) && matches(it, st.q, names)).map((it) => it.id);
    if (ids.length > limit && !window.confirm(`Add all ${ids.length} matching devices?`)) return;
    addItems(ids);
  }
  function pickActive(): void {
    const r = results(cfg, st);
    if (st.active < r.sites.length) { addSite(r.sites[st.active]!.id); return; }
    const it = r.items[st.active - r.sites.length];
    if (it) addItems([it.id]);
  }

  host.addEventListener('input', (e) => {
    const t = e.target as HTMLInputElement | null;
    if (!t || !t.dataset || !t.dataset.dpQ) return;
    st.q = t.value;
    st.active = 0;
    st.open = true;
    drawResults();
  });
  host.addEventListener('focusin', (e) => {
    const t = e.target as HTMLElement | null;
    if (!t || !t.dataset || !t.dataset.dpQ || st.open) return;
    st.open = true;
    drawResults();
  });
  host.addEventListener('focusout', (e) => {
    const next = (e as FocusEvent).relatedTarget as Node | null;
    if (next && host.contains && host.contains(next)) return;
    st.open = false;
    drawResults();
  });
  host.addEventListener('keydown', (e) => {
    const t = e.target as HTMLElement | null;
    if (!t || !t.dataset || !t.dataset.dpQ) return;
    const k = (e as KeyboardEvent).key;
    const r = results(cfg, st);
    const n = r.sites.length + r.items.length;
    if (k === 'ArrowDown') { st.open = true; st.active = Math.min(n - 1, st.active + 1); drawResults(); e.preventDefault(); }
    if (k === 'ArrowUp') { st.active = Math.max(0, st.active - 1); drawResults(); e.preventDefault(); }
    if (k === 'Enter') { e.preventDefault(); if (st.open && n) pickActive(); }
    if (k === 'Escape' && st.open) { e.stopPropagation(); st.open = false; drawResults(); }
  });
  // A click in the results must not blur the box first, or the list closes
  // under the pointer before the click lands.
  host.addEventListener('mousedown', (e) => {
    const t = e.target as HTMLElement | null;
    if (t && t.closest && t.closest('.dp-results')) e.preventDefault();
  });
  host.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const b = t && t.closest ? t.closest<HTMLElement>('[data-dp-add],[data-dp-addsite],[data-dp-all],[data-dp-rm],[data-dp-rmsite],[data-dp-more],[data-dp-clear]') : null;
    if (!b) return;
    e.preventDefault();
    const d = b.dataset;
    if (d.dpAdd) addItems([d.dpAdd]);
    else if (d.dpAddsite) addSite(d.dpAddsite);
    else if (d.dpAll) addAll();
    else if (d.dpRm) { st.items = st.items.filter((x) => x !== d.dpRm); drawResults(); drawPicked(); changed(); }
    else if (d.dpRmsite) { st.sites = st.sites.filter((x) => x !== d.dpRmsite); drawResults(); drawPicked(); changed(); }
    else if (d.dpMore) { st.expanded = !st.expanded; drawPicked(); }
    else if (d.dpClear) { st.items = []; st.sites = []; drawResults(); drawPicked(); changed(); }
  });

  draw();
  return {
    get: () => ({ items: [...st.items], sites: [...st.sites] }),
    set: (sel) => {
      if (sel.items) st.items = [...sel.items];
      if (sel.sites) st.sites = [...sel.sites];
      st.q = '';
      st.open = false;
      st.expanded = false;
      draw();
    },
    refresh: () => { drawResults(); drawPicked(); },
  };
}
