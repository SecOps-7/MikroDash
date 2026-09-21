// The Config Management page: its tabs, and the panels each one owns.
//
// Everything on this page is read on demand over REST (/api/config/…); there
// is no collector behind it, so nothing is fetched while it is not shown.
// The markup is built by config-management-cards.ts and -editor.ts.

import { el, esc } from '../dom';
import {
  categoryBar, drawerBody, findingRow, libraryGrid, statStrip, type LibTemplate, type TemplateDetail,
} from './config-management-cards';
import {
  HIGHLIGHT_LIMIT, captureForm, editorLayer, gutter, serverVarRows, syncVars, varRow, type VarDef,
} from './config-management-editor';

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
interface ListReply {
  templates: StoredRow[]; canned: CannedRow[]; lockClass: Record<string, boolean>;
  captureMenus: string[]; varTypes: string[]; serverVars: string[];
}
interface Finding { level: string; code: string; line?: number; message: string }

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

/** An error the server gave, with the line it points at when it gave one. */
class ApiError extends Error {
  constructor(msg: string, readonly status: number, readonly line = 0) { super(msg); }
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch('/api/config/' + path, { credentials: 'same-origin', ...init });
  const body = (await r.json().catch(() => ({}))) as T & { ok?: boolean; error?: string; line?: number };
  if (!r.ok || body.ok === false) {
    throw new ApiError(body.error || 'The request failed (' + r.status + ')', r.status, body.line ?? 0);
  }
  return body;
}

const json = (v: unknown): RequestInit => ({ method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(v) });

/** The template open in the editor. */
interface Draft {
  id: string | null; revision: number; kind: LibTemplate['kind']; name: string; description: string;
  body: string; vars: VarDef[];
}

export function initConfigManagementPage(isVisible: (page: string) => boolean): void {
  let tab: Tab = 'library';
  let lib: LibTemplate[] = [];
  let category = 'all';
  let query = '';
  let captureMenus: string[] = [];
  let varTypes: string[] = ['text'];
  let serverVars: string[] = [];
  let draft: Draft | null = null;
  let dirty = false;
  let checkTimer: ReturnType<typeof setTimeout> | null = null;
  /** The template the Deploy tab opens with, when a card's Deploy was pressed. */
  let deployPick = '';
  const visible = (): boolean => isVisible('config-management');

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

  // ── The Library ──────────────────────────────────────────────────────────

  function drawLibrary(): void {
    if (!visible()) return;
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
      const r = await api<ListReply>('templates');
      lib = toLibrary(r.templates, r.canned, r.lockClass);
      captureMenus = r.captureMenus ?? [];
      varTypes = r.varTypes?.length ? r.varTypes : varTypes;
      serverVars = r.serverVars ?? [];
    } catch (e) {
      const grid = el('cfgLibrary');
      if (grid) grid.innerHTML = '<div class="cfg-empty">' + esc(e instanceof Error ? e.message : 'The library could not be read') + '</div>';
      return;
    }
    drawLibrary();
  }

  function closeDrawer(): void {
    const d = el('cfgDrawer');
    d?.classList.remove('open');
    d?.setAttribute('aria-hidden', 'true');
  }

  type Detail = Omit<TemplateDetail, 'variables'> & { id: string; name: string; kind: LibTemplate['kind'];
    revision: number; variables: string | VarDef[] };

  async function fetchTemplate(id: string): Promise<Detail> {
    return (await api<{ template: Detail }>('templates/' + encodeURIComponent(id))).template;
  }

  const varsOf = (v: string | VarDef[]): VarDef[] => (typeof v === 'string' ? parseList(v) as VarDef[] : v);

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
      const r = await fetchTemplate(id);
      body.innerHTML = drawerBody({ ...r, variables: varsOf(r.variables) });
    } catch (e) {
      body.innerHTML = '<div class="cfg-empty">' + esc(e instanceof Error ? e.message : 'Could not open it') + '</div>';
    }
  }

  async function customise(id: string): Promise<void> {
    try {
      const r = await api<{ id: string }>('templates/' + encodeURIComponent(id) + '/clone', { method: 'POST' });
      await load();
      await openEditor(r.id);
    } catch (e) {
      window.alert(e instanceof Error ? e.message : 'The copy could not be made');
    }
  }

  // ── The Editor ───────────────────────────────────────────────────────────

  function leaveDraft(): boolean {
    return !dirty || window.confirm('Leave this template without saving your changes?');
  }

  async function openEditor(id: string | null): Promise<void> {
    if (draft && !leaveDraft()) return;
    if (id === null) {
      draft = { id: null, revision: 0, kind: 'fragment', name: '', description: '',
        body: '# What this template does, in a line or two.\n/ip dns\nset servers={{dns_servers}}\n',
        vars: [{ name: 'dns_servers', type: 'ipv4-list', label: 'DNS servers', default: '1.1.1.1,9.9.9.9' }] };
    } else {
      try {
        const r = await fetchTemplate(id);
        draft = { id: r.id, revision: r.revision, kind: r.kind, name: r.name, description: r.description,
          body: r.body, vars: varsOf(r.variables) };
      } catch (e) {
        window.alert(e instanceof Error ? e.message : 'The template could not be opened');
        return;
      }
    }
    dirty = false;
    show('editor');
    drawEditor();
    scheduleCheck(0);
  }

  function closeEditor(): void {
    if (!leaveDraft()) return;
    draft = null;
    dirty = false;
    drawEditor();
  }

  function drawEditor(): void {
    const empty = el('cfgEdEmpty');
    const ed = el('cfgEd');
    if (!empty || !ed) return;
    empty.hidden = !!draft;
    ed.hidden = !draft;
    if (!draft) return;
    (el('cfgEdName') as HTMLInputElement).value = draft.name;
    (el('cfgEdDesc') as HTMLInputElement).value = draft.description;
    (el('cfgEdBody') as HTMLTextAreaElement).value = draft.body;
    const meta = el('cfgEdMeta');
    if (meta) {
      meta.textContent = draft.id ? (draft.kind === 'fragment' ? 'Custom template' : 'Full export') +
        ' · revision ' + draft.revision : 'New template, not saved yet';
    }
    const del = el('cfgEdDelete');
    if (del) del.hidden = !draft.id;
    drawBody(0);
    drawVars();
  }

  function drawBody(badLine: number): void {
    if (!draft) return;
    const hl = el('cfgEdHl');
    const big = draft.body.length > HIGHLIGHT_LIMIT;
    el('cfgEd')?.classList.toggle('is-plain', big);
    if (hl) hl.innerHTML = big ? '' : editorLayer(draft.body);
    const g = el('cfgEdGutter');
    if (g) g.innerHTML = gutter(draft.body, badLine);
  }

  function drawVars(): void {
    if (!draft) return;
    const synced = syncVars(draft.vars, draft.body, serverVars);
    draft.vars = synced.defs;
    const box = el('cfgEdVars');
    if (box) {
      box.innerHTML = draft.vars.length ? draft.vars.map((d) => varRow(d, varTypes, synced.unused.includes(d.name))).join('')
        : '<div class="cfg-meta">Write <span class="cfg-var">{{name}}</span> in the template to ask for a setting.</div>';
    }
    const sv = el('cfgEdServerVars');
    if (sv) sv.innerHTML = serverVarRows(serverVars.filter((n) => draft?.body.includes('{{' + n + '}}')));
  }

  function setStatus(cls: string, html: string): void {
    const s = el('cfgEdStatus');
    if (!s) return;
    s.className = 'cfg-ed-status ' + cls;
    s.innerHTML = html;
  }

  /** The declarations to send: unused ones dropped, as a save would refuse them. */
  function outVars(): VarDef[] {
    if (!draft) return [];
    const unused = syncVars(draft.vars, draft.body, serverVars).unused;
    return draft.vars.filter((d) => !unused.includes(d.name)).map((d) => ({ ...d,
      default: d.type === 'secret' ? undefined : d.default || undefined, label: d.label || undefined,
      options: d.type === 'enum' ? d.options : undefined }));
  }

  function scheduleCheck(ms = 500): void {
    if (checkTimer) clearTimeout(checkTimer);
    checkTimer = setTimeout(() => void check(), ms);
  }

  function refused(e: unknown): void {
    const line = e instanceof ApiError ? e.line : 0;
    drawBody(line);
    // The last checks were of text that no longer reads; do not let them stand.
    const checks = el('cfgEdChecks');
    if (checks) checks.innerHTML = '<div class="cfg-meta">Fix the refused line to see the checks.</div>';
    setStatus('is-bad', (line ? '<span class="cfg-ln-tag">line ' + line + '</span>' : '') +
      esc(e instanceof Error ? e.message : 'The template could not be checked'));
  }

  async function check(): Promise<void> {
    if (!draft || !visible()) return;
    const at = draft.body;
    try {
      const r = await api<{ findings: Finding[] }>('check', json({ name: draft.name || 'draft', body: draft.body,
        variables: outVars(), kind: draft.kind }));
      if (!draft || draft.body !== at) return;
      drawBody(0);
      const checks = el('cfgEdChecks');
      if (checks) {
        checks.innerHTML = r.findings.length ? '<ul class="cfg-findings">' + r.findings.map(findingRow).join('') + '</ul>'
          : '<div class="cfg-meta">Nothing to flag.</div>';
      }
      setStatus('is-ok', 'Every line reads as RouterOS configuration.');
    } catch (e) {
      if (draft && draft.body === at) refused(e);
    }
  }

  async function save(): Promise<void> {
    if (!draft) return;
    const body = { name: draft.name, description: draft.description, body: draft.body, variables: outVars(),
      revision: draft.revision };
    try {
      if (draft.id) {
        await api('templates/' + encodeURIComponent(draft.id), { ...json(body), method: 'PUT' });
      } else {
        draft.id = (await api<{ id: string }>('templates', json(body))).id;
      }
      dirty = false;
      draft.revision = (await fetchTemplate(draft.id)).revision;
      drawEditor();
      setStatus('is-ok', 'Saved.');
      void load();
    } catch (e) {
      refused(e);
    }
  }

  async function remove(): Promise<void> {
    if (!draft?.id || !window.confirm('Delete "' + draft.name + '"? Its deploy history is kept.')) return;
    try {
      await api('templates/' + encodeURIComponent(draft.id), { method: 'DELETE' });
      draft = null;
      dirty = false;
      drawEditor();
      void load();
    } catch (e) {
      window.alert(e instanceof Error ? e.message : 'The template could not be deleted');
    }
  }

  async function openCapture(): Promise<void> {
    if (draft && !leaveDraft()) return;
    draft = null;
    dirty = false;
    show('editor');
    drawEditor();
    const box = el('cfgCaptureBox');
    if (!box) return;
    box.hidden = false;
    box.innerHTML = '<div class="cfg-meta">Loading routers…</div>';
    try {
      const r = await fetch('/api/routers', { credentials: 'same-origin' });
      const b = (await r.json()) as { routers?: { id: string; label?: string; host?: string; disabled?: boolean }[] };
      const routers = (b.routers ?? []).filter((x) => !x.disabled)
        .map((x) => ({ id: x.id, label: x.label || x.host || x.id }));
      box.innerHTML = routers.length ? captureForm(routers, captureMenus)
        : '<div class="cfg-meta">No routers to capture from.</div>';
    } catch {
      box.innerHTML = '<div class="cfg-meta">The routers could not be listed.</div>';
    }
  }

  async function capture(): Promise<void> {
    const value = (n: HTMLElement | null): string => (n as HTMLInputElement | HTMLSelectElement | null)?.value ?? '';
    const go = el('cfgCapGo') as HTMLButtonElement | null;
    if (go) {
      go.disabled = true;
      go.textContent = 'Capturing…';
    }
    try {
      const kind = value(el('cfgCapKind'));
      const r = await api<{ id: string }>('capture', json({ routerId: value(el('cfgCapRouter')), kind,
        menu: kind === 'fragment' ? value(el('cfgCapMenu')) : '', name: value(el('cfgCapName')) }));
      const box = el('cfgCaptureBox');
      if (box) box.hidden = true;
      await load();
      await openEditor(r.id);
    } catch (e) {
      window.alert(e instanceof Error ? e.message : 'The capture failed');
    } finally {
      if (go) {
        go.disabled = false;
        go.textContent = 'Capture';
      }
    }
  }

  // ── Wiring ───────────────────────────────────────────────────────────────

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
    else if (act === 'edit') void openEditor(id);
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

  for (const id of ['cfgNew', 'cfgEdNew']) el(id)?.addEventListener('click', () => void openEditor(null));
  for (const id of ['cfgCapture', 'cfgEdCapture']) el(id)?.addEventListener('click', () => void openCapture());
  el('cfgCaptureBox')?.addEventListener('click', (e) => {
    if ((e.target as HTMLElement).closest('#cfgCapGo')) void capture();
  });
  el('cfgCaptureBox')?.addEventListener('change', (e) => {
    if ((e.target as HTMLElement).id === 'cfgCapKind') {
      const wrap = el('cfgCapMenuWrap');
      if (wrap) wrap.hidden = (e.target as HTMLSelectElement).value !== 'fragment';
    }
  });
  el('cfgEdName')?.addEventListener('input', (e) => {
    if (draft) {
      draft.name = (e.target as HTMLInputElement).value;
      dirty = true;
    }
  });
  el('cfgEdDesc')?.addEventListener('input', (e) => {
    if (draft) {
      draft.description = (e.target as HTMLInputElement).value;
      dirty = true;
    }
  });
  el('cfgEdBody')?.addEventListener('input', (e) => {
    if (!draft) return;
    draft.body = (e.target as HTMLTextAreaElement).value;
    dirty = true;
    drawBody(0);
    drawVars();
    setStatus('', 'Checking…');
    scheduleCheck();
  });
  // The highlighted layer and the gutter follow the text area's scroll.
  el('cfgEdBody')?.addEventListener('scroll', (e) => {
    const t = e.target as HTMLTextAreaElement;
    const hl = el('cfgEdHl');
    const g = el('cfgEdGutter');
    if (hl) {
      hl.scrollTop = t.scrollTop;
      hl.scrollLeft = t.scrollLeft;
    }
    if (g) g.scrollTop = t.scrollTop;
  });
  el('cfgEdVars')?.addEventListener('input', (e) => {
    const f = e.target as HTMLInputElement | HTMLSelectElement;
    const row = f.closest('[data-var]');
    const d = draft?.vars.find((v) => v.name === row?.getAttribute('data-var'));
    const field = f.getAttribute('data-var-field');
    if (!d || !field) return;
    if (field === 'required') d.required = (f as HTMLInputElement).checked;
    else if (field === 'options') d.options = f.value.split(',').map((x) => x.trim()).filter(Boolean);
    else if (field === 'type') {
      d.type = f.value;
      drawVars();
    } else if (field === 'label') d.label = f.value;
    else if (field === 'default') d.default = f.value;
    dirty = true;
    scheduleCheck();
  });
  el('cfgEdSave')?.addEventListener('click', () => void save());
  el('cfgEdDelete')?.addEventListener('click', () => void remove());
  el('cfgEdClose')?.addEventListener('click', closeEditor);

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'config-management') {
      closeDrawer();
      return;
    }
    if (!visible()) return;
    show(tab);
    void load();
  });
  void deployPick;
}
