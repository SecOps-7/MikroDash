// The Config Management page: its tabs, and the panels each one owns.
//
// Everything on this page is read on demand over REST (/api/config/…); there
// is no collector behind it, so nothing is fetched while it is not shown.
// The markup is built by config-management-cards.ts.

import { el } from '../dom';
import {
  categoryBar, drawerBody, libraryGrid, statStrip, type LibTemplate, type TemplateDetail,
} from './config-management-cards';

const TABS = ['library', 'editor', 'deploy', 'history', 'drift'] as const;
type Tab = (typeof TABS)[number];

/** A stored template as GET /api/config/templates lists it. */
interface StoredRow {
  id: string; name: string; description: string; kind: LibTemplate['kind'];
  scope: string; variables: string; revision: number; baseline: string | null; updatedAt: number;
}
/** A canned template as the same endpoint lists it. */
interface CannedRow {
  id: string; name: string; description: string; category: string; version: number;
  tags: string[]; variables: unknown[]; lockClass: boolean; scope: string[];
}

function parseList(s: string): unknown[] {
  try {
    const v: unknown = JSON.parse(s);
    return Array.isArray(v) ? v : [];
  } catch {
    return [];
  }
}

/** Both kinds of template, in one shape: canned first, in the library's order. */
export function toLibrary(stored: StoredRow[], canned: CannedRow[], lock: Record<string, boolean>): LibTemplate[] {
  return [
    ...canned.map((c): LibTemplate => ({
      id: 'canned:' + c.id, name: c.name, description: c.description, category: c.category, kind: 'fragment',
      canned: true, version: c.version, lockClass: c.lockClass, scope: c.scope, variables: c.variables.length,
      tags: c.tags, baseline: null, updatedAt: 0,
    })),
    ...stored.map((t): LibTemplate => ({
      id: t.id, name: t.name, description: t.description, category: 'custom', kind: t.kind, canned: false,
      version: t.revision, lockClass: !!lock[t.id], scope: parseList(t.scope).map(String),
      variables: parseList(t.variables).length, tags: [], baseline: t.baseline, updatedAt: t.updatedAt,
    })),
  ];
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch('/api/config/' + path, { credentials: 'same-origin', ...init });
  const body = (await r.json().catch(() => ({}))) as T & { ok?: boolean; error?: string };
  if (!r.ok || body.ok === false) throw new Error(body.error || 'The request failed (' + r.status + ')');
  return body;
}

export function initConfigManagementPage(isVisible: (page: string) => boolean): void {
  let tab: Tab = 'library';
  let lib: LibTemplate[] = [];
  let category = 'all';
  let query = '';
  /** The template the Deploy tab opens with, when a card's Deploy was pressed. */
  let deployPick = '';

  function show(next: Tab): void {
    tab = next;
    document.querySelectorAll<HTMLElement>('#cfgTabs [data-cfgtab]').forEach((b) => {
      const on = b.getAttribute('data-cfgtab') === next;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', String(on));
    });
    for (const t of TABS) {
      const p = el('cfgPanel-' + t);
      if (p) p.hidden = t !== next;
    }
  }

  function drawLibrary(): void {
    if (!isVisible('config-management')) return;
    const counts: Record<string, number> = { all: lib.length };
    for (const t of lib) counts[t.category] = (counts[t.category] ?? 0) + 1;
    const badge = el('cfgBadge');
    if (badge) badge.textContent = String(lib.length);
    const stats = el('cfgStats');
    if (stats) stats.innerHTML = statStrip(lib);
    const cats = el('cfgCats');
    if (cats) cats.innerHTML = categoryBar(category, counts);
    const grid = el('cfgLibrary');
    if (grid) grid.innerHTML = libraryGrid(lib, category, query);
  }

  async function load(): Promise<void> {
    try {
      const r = await api<{ templates: StoredRow[]; canned: CannedRow[]; lockClass: Record<string, boolean> }>('templates');
      lib = toLibrary(r.templates, r.canned, r.lockClass);
    } catch (e) {
      const grid = el('cfgLibrary');
      if (grid) grid.innerHTML = '<div class="cfg-empty">' + (e instanceof Error ? e.message : 'The library could not be read') + '</div>';
      return;
    }
    drawLibrary();
  }

  function closeDrawer(): void {
    const d = el('cfgDrawer');
    d?.classList.remove('open');
    d?.setAttribute('aria-hidden', 'true');
  }

  async function preview(id: string): Promise<void> {
    const t = lib.find((x) => x.id === id);
    const d = el('cfgDrawer');
    const title = el('cfgDrawerTitle');
    const meta = el('cfgDrawerMeta');
    const body = el('cfgDrawerBody');
    if (!t || !d || !title || !meta || !body) return;
    title.textContent = t.name;
    meta.textContent = (t.canned ? 'Canned · v' + t.version : 'Custom · revision ' + t.version) +
      ' · ' + t.scope.length + (t.scope.length === 1 ? ' menu' : ' menus');
    body.innerHTML = '<div class="cfg-empty">Loading…</div>';
    d.classList.add('open');
    d.setAttribute('aria-hidden', 'false');
    try {
      const r = await api<{ template: TemplateDetail & { variables: string | TemplateDetail['variables'] } }>(
        'templates/' + encodeURIComponent(id));
      const v = r.template.variables;
      body.innerHTML = drawerBody({ ...r.template,
        variables: (typeof v === 'string' ? parseList(v) : v) as TemplateDetail['variables'] });
    } catch (e) {
      body.innerHTML = '<div class="cfg-empty">' + (e instanceof Error ? e.message : 'Could not open it') + '</div>';
    }
  }

  async function customise(id: string): Promise<void> {
    try {
      await api('templates/' + encodeURIComponent(id) + '/clone', { method: 'POST' });
      category = 'custom';
      await load();
    } catch (e) {
      window.alert(e instanceof Error ? e.message : 'The copy could not be made');
    }
  }

  el('cfgTabs')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement).closest('[data-cfgtab]');
    const t = b?.getAttribute('data-cfgtab') as Tab | null;
    if (t && TABS.includes(t)) show(t);
  });
  el('cfgCats')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement).closest('[data-cfg-cat]');
    const c = b?.getAttribute('data-cfg-cat');
    if (c) {
      category = c;
      drawLibrary();
    }
  });
  el('cfgSearch')?.addEventListener('input', (e) => {
    query = (e.target as HTMLInputElement).value;
    drawLibrary();
  });
  el('cfgLibrary')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement).closest('[data-cfg-act]');
    const card = btn?.closest('[data-cfg-id]');
    const id = card?.getAttribute('data-cfg-id');
    if (!btn || !id) return;
    const act = btn.getAttribute('data-cfg-act');
    if (act === 'preview') void preview(id);
    else if (act === 'clone') void customise(id);
    else if (act === 'deploy') {
      deployPick = id;
      show('deploy');
    }
  });
  el('cfgDrawerClose')?.addEventListener('click', closeDrawer);
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && el('cfgDrawer')?.classList.contains('open')) closeDrawer();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'config-management') {
      closeDrawer();
      return;
    }
    if (!isVisible('config-management')) return;
    show(tab);
    void load();
  });
  void deployPick;
}
