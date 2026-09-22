// The Config Management page: its tabs, and the panels each one owns.
//
// Everything on this page is read on demand over REST (/api/config/…); there
// is no collector behind it, so nothing is fetched while it is not shown.
// The markup is built by config-management-cards.ts and -editor.ts.

import { el, esc, renderSortHeader, sortRows, type SortState } from '../dom';
import { t, ts } from '../i18n';
import type { Socket } from '../socket';
import type { CfgDeployPayload, Hunk } from '../gen/payloads';
import {
  categoryBar, drawerBody, findingRow, libraryGrid, statStrip, type LibTemplate, type TemplateDetail,
} from './config-management-cards';
import {
  HIGHLIGHT_LIMIT, captureForm, editorLayer, gutter, serverVarRows, syncVars, varRow, type VarDef,
} from './config-management-editor';
import {
  DRIFT_COLS, HISTORY_COLS, driftKey, driftRows, historyRows, sortable, type DriftCheck, type DriftRow, type RunDetail,
  type RunRow, type SortableRun,
} from './config-management-history';
import {
  defaultsFromValues, findingKey, newSecret, previewCard, readyToStart, rolloutView, routerPicker, valuesGrid,
  type RouterOpt, type RouterPreview,
} from './config-management-deploy';

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
    ...stored.map((tpl): LibTemplate => ({
      id: tpl.id, name: tpl.name, description: tpl.description, category: 'custom', kind: tpl.kind, canned: false,
      version: tpl.revision, lockClass: !!lock[tpl.id], scope: parseList(tpl.scope).map(String),
      variables: parseList(tpl.variables).length, tags: [], baseline: tpl.baseline, updatedAt: tpl.updatedAt,
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
    throw new ApiError(ts(body.error) || t('The request failed ({status})', { status: r.status }), r.status, body.line ?? 0);
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

/** A preview's reply: an addition's `prepared`, or a full export's `reset`. */
interface PreviewReply {
  kind: string;
  target: RouterPreview['target'];
  prepared?: { rendered: string; hash: string; findings: RouterPreview['findings'] };
  reset?: { bootstrap: string[]; rest: string; hash: string; findings: RouterPreview['findings'] };
  lockClass?: boolean;
}

export function initConfigManagementPage(socket: Socket, isVisible: (page: string) => boolean): void {
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
  const visible = (): boolean => isVisible('config-management');
  /** The Deploy tab's choices, until a run starts. */
  const dep = {
    tplId: '', defs: [] as VarDef[], kind: 'fragment', routers: [] as RouterOpt[], picked: [] as string[],
    values: {} as Record<string, Record<string, string>>, previews: {} as Record<string, RouterPreview>,
    acked: new Set<string>(),
    /** One generated password per secret setting, shared by every router. */
    secrets: {} as Record<string, string>, reveal: false,
  };
  let run: CfgDeployPayload | null = null;

  function show(next: Tab): void {
    tab = next;
    document.querySelectorAll<HTMLElement>('#cfgTabs [data-cfgtab]').forEach((b) => {
      const on = b.getAttribute('data-cfgtab') === next;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', String(on));
    });
    for (const tpl of TABS) {
      const p = el('cfgPanel-' + tpl);
      if (p) p.hidden = tpl !== next;
    }
    if (next === 'history') void loadHistory();
    else if (next === 'drift') void loadDrift();
  }

  // ── The Library ──────────────────────────────────────────────────────────

  function drawLibrary(): void {
    if (!visible()) return;
    const counts: Record<string, number> = { all: lib.length };
    for (const tpl of lib) counts[tpl.category] = (counts[tpl.category] ?? 0) + 1;
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
      if (grid) grid.innerHTML = '<div class="cfg-empty">' + esc(e instanceof Error ? e.message : t('The library could not be read')) + '</div>';
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
    const tpl = lib.find((x) => x.id === id);
    const d = el('cfgDrawer');
    const title = el('cfgDrawerTitle');
    const meta = el('cfgDrawerMeta');
    const body = el('cfgDrawerBody');
    if (!tpl || !d || !title || !meta || !body) return;
    title.textContent = tpl.name;
    meta.textContent = (tpl.canned ? t('Canned · v{version}', { version: tpl.version }) : t('Custom · revision {version}', { version: tpl.version })) +
      ' · ' + (tpl.scope.length === 1 ? t('1 menu') : t('{n} menus', { n: tpl.scope.length }));
    body.innerHTML = '<div class="cfg-empty">' + t('Loading…') + '</div>';
    d.classList.add('open');
    d.setAttribute('aria-hidden', 'false');
    try {
      const r = await fetchTemplate(id);
      body.innerHTML = drawerBody({ ...r, variables: varsOf(r.variables) });
    } catch (e) {
      body.innerHTML = '<div class="cfg-empty">' + esc(e instanceof Error ? e.message : t('Could not open it')) + '</div>';
    }
  }

  async function customise(id: string): Promise<void> {
    try {
      const r = await api<{ id: string }>('templates/' + encodeURIComponent(id) + '/clone', { method: 'POST' });
      await load();
      await openEditor(r.id);
    } catch (e) {
      window.alert(e instanceof Error ? e.message : t('The copy could not be made'));
    }
  }

  // ── The Editor ───────────────────────────────────────────────────────────

  function leaveDraft(): boolean {
    return !dirty || window.confirm(t('Leave this template without saving your changes?'));
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
        window.alert(e instanceof Error ? e.message : t('The template could not be opened'));
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
      meta.textContent = draft.id ? (draft.kind === 'fragment' ? t('Custom template') : t('Full export')) +
        ' · revision ' + draft.revision : t('New template, not saved yet');
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
        : '<div class="cfg-meta">' + t('Write {example} in the template to ask for a setting.', { example: '<span class="cfg-var">{{name}}</span>' }) + '</div>';
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
    if (checks) checks.innerHTML = '<div class="cfg-meta">' + t('Fix the refused line to see the checks.') + '</div>';
    setStatus('is-bad', (line ? '<span class="cfg-ln-tag">line ' + line + '</span>' : '') +
      esc(e instanceof Error ? e.message : t('The template could not be checked')));
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
          : '<div class="cfg-meta">' + t('Nothing to flag.') + '</div>';
      }
      setStatus('is-ok', t('Every line reads as RouterOS configuration.'));
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
    if (!draft?.id || !window.confirm(t('Delete "{name}"? Its deploy history is kept.', { name: draft.name }))) return;
    try {
      await api('templates/' + encodeURIComponent(draft.id), { method: 'DELETE' });
      draft = null;
      dirty = false;
      drawEditor();
      void load();
    } catch (e) {
      window.alert(e instanceof Error ? e.message : t('The template could not be deleted'));
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
    box.innerHTML = '<div class="cfg-meta">' + t('Loading routers…') + '</div>';
    try {
      const r = await fetch('/api/routers', { credentials: 'same-origin' });
      const b = (await r.json()) as { routers?: { id: string; label?: string; host?: string; disabled?: boolean }[] };
      const routers = (b.routers ?? []).filter((x) => !x.disabled)
        .map((x) => ({ id: x.id, label: x.label || x.host || x.id }));
      box.innerHTML = routers.length ? captureForm(routers, captureMenus)
        : '<div class="cfg-meta">' + t('No routers to capture from.') + '</div>';
    } catch {
      box.innerHTML = '<div class="cfg-meta">' + t('The routers could not be listed.') + '</div>';
    }
  }

  async function capture(): Promise<void> {
    const value = (n: HTMLElement | null): string => (n as HTMLInputElement | HTMLSelectElement | null)?.value ?? '';
    const go = el('cfgCapGo') as HTMLButtonElement | null;
    if (go) {
      go.disabled = true;
      go.textContent = t('Capturing…');
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
      window.alert(e instanceof Error ? e.message : t('The capture failed'));
    } finally {
      if (go) {
        go.disabled = false;
        go.textContent = t('Capture');
      }
    }
  }

  // ── Wiring ───────────────────────────────────────────────────────────────

  el('cfgTabs')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement).closest('[data-cfgtab]');
    const tab = b?.getAttribute('data-cfgtab') as Tab | null;
    if (tab && TABS.includes(tab)) show(tab);
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
    else if (act === 'deploy') void openDeploy(id);
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
    setStatus('', t('Checking…'));
    scheduleCheck();
  });
  // The highlighted layer and the gutter follow the text area's scroll.
  el('cfgEdBody')?.addEventListener('scroll', (e) => {
    const ta = e.target as HTMLTextAreaElement;
    const hl = el('cfgEdHl');
    const g = el('cfgEdGutter');
    if (hl) {
      hl.scrollTop = ta.scrollTop;
      hl.scrollLeft = ta.scrollLeft;
    }
    if (g) g.scrollTop = ta.scrollTop;
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
    void load().then(async () => {
      await loadRouters();
      drawDeploy();
    });
    socket.emit('cfgdeploy:watch', {});
  });

  // ── The Deploy tab ───────────────────────────────────────────────────────

  const labelOf = (id: string): string => dep.routers.find((r) => r.id === id)?.label ?? id;

  async function openDeploy(id: string): Promise<void> {
    show('deploy');
    await loadRouters();
    await pickTemplate(id);
  }

  async function loadRouters(): Promise<void> {
    if (dep.routers.length) return;
    try {
      const r = await fetch('/api/routers', { credentials: 'same-origin' });
      const b = (await r.json()) as { routers?: { id: string; label?: string; host?: string; disabled?: boolean }[] };
      dep.routers = (b.routers ?? []).filter((x) => !x.disabled).map((x) => ({ id: x.id, label: x.label || x.host || x.id }));
    } catch {
      dep.routers = [];
    }
  }

  async function pickTemplate(id: string): Promise<void> {
    dep.tplId = id;
    dep.previews = {};
    dep.acked.clear();
    dep.defs = [];
    if (id) {
      try {
        const tpl = await fetchTemplate(id);
        dep.defs = varsOf(tpl.variables);
        dep.kind = tpl.kind;
        dep.secrets = {};
        for (const d of dep.defs) if (d.type === 'secret') dep.secrets[d.name] = newSecret();
      } catch (e) {
        showWhy(e instanceof Error ? e.message : t('The template could not be read'));
      }
    }
    drawDeploy();
  }

  function drawDeploy(): void {
    const sel = el('cfgDepTpl') as HTMLSelectElement | null;
    if (sel) {
      sel.innerHTML = ('<option value="">' + t('Choose a template') + '</option>') + lib.map((tpl) => '<option value="' + esc(tpl.id) + '"' +
        (tpl.id === dep.tplId ? ' selected' : '') + '>' + esc(tpl.name) + (tpl.canned ? '' : ' (custom)') + '</option>').join('');
    }
    const tpl = lib.find((x) => x.id === dep.tplId);
    const meta = el('cfgDepTplMeta');
    if (meta) {
      meta.textContent = !tpl ? '' : (tpl.kind === 'full-export' ? t('Full replacement: each router is reset and rebuilt.') + ' '
        : t('An addition: merged into what each router already has.') + ' ') +
        (tpl.lockClass ? t('It can cut MikroDash off, so each router arms an automatic revert first.') : '');
    }
    const routers = el('cfgDepRouters');
    if (routers) routers.innerHTML = routerPicker(dep.routers, dep.picked);
    const picked = dep.picked.map((id) => ({ id, label: labelOf(id) }));
    const values = el('cfgDepValues');
    for (const id of dep.picked) {
      const v = (dep.values[id] ??= { ...defaultsFor() });
      for (const [k, secret] of Object.entries(dep.secrets)) if (!v[k]) v[k] = secret;
    }
    if (values) values.innerHTML = valuesGrid(dep.defs, picked, dep.values, dep.reveal);
    const save = el('cfgDepSaveDefaults') as HTMLButtonElement | null;
    if (save) save.disabled = !dep.defs.length || !dep.picked.length;
    drawPreviews();
  }

  function drawPreviews(): void {
    const box = el('cfgDepPreviews');
    if (box) {
      box.innerHTML = dep.picked.filter((id) => dep.previews[id])
        .map((id) => previewCard(labelOf(id), dep.previews[id] as RouterPreview, dep.acked)).join('');
    }
    const canary = dep.picked[0];
    const confirm = el('cfgDepConfirm') as HTMLInputElement | null;
    if (confirm) confirm.placeholder = canary ? t('Type {router} to deploy', { router: labelOf(canary) }) : t('Pick routers first');
    showWhy(dep.tplId ? readyToStart(dep.picked, dep.previews, dep.acked) : t('Choose a template'));
  }

  function showWhy(msg: string): void {
    const why = el('cfgDepWhy');
    if (why) why.textContent = msg;
    const go = el('cfgDepStart') as HTMLButtonElement | null;
    if (go) go.disabled = !!msg;
  }

  function invalidate(): void {
    dep.previews = {};
    dep.acked.clear();
    drawPreviews();
  }

  async function previewAll(): Promise<void> {
    if (!dep.tplId || !dep.picked.length) return;
    const btn = el('cfgDepPreview') as HTMLButtonElement | null;
    if (btn) { btn.disabled = true; btn.textContent = t('Reading each router…'); }
    dep.previews = {};
    dep.acked.clear();
    await Promise.all(dep.picked.map(async (rid) => {
      try {
        const r = await api<PreviewReply>('templates/' + encodeURIComponent(dep.tplId) + '/preview',
          json({ routerId: rid, values: dep.values[rid] ?? defaultsFor() }));
        const p = r.prepared ?? r.reset;
        dep.previews[rid] = { routerId: rid, hash: p?.hash, findings: p?.findings ?? [], target: r.target,
          lockClass: r.lockClass, rendered: r.prepared ? r.prepared.rendered
            : '# Phase A, the bootstrap after the reset\n' + (r.reset?.bootstrap ?? []).join('\n') +
              '\n\n# Phase B, imported once MikroDash is back\n' + (r.reset?.rest ?? '') };
      } catch (e) {
        dep.previews[rid] = { routerId: rid, error: e instanceof Error ? e.message : t('The preview failed') };
      }
    }));
    if (btn) { btn.disabled = false; btn.textContent = t('Preview again'); }
    drawPreviews();
  }

  function defaultsFor(): Record<string, string> {
    const out: Record<string, string> = { ...dep.secrets };
    for (const d of dep.defs) if (d.default && d.type !== 'secret') out[d.name] = d.default;
    return out;
  }

  /** Saves the settings as they stand for the first router picked as the
   *  template's defaults. A canned template cannot change, so they go into a
   *  new custom copy of it, which the Deploy tab then carries on with. */
  async function saveDefaults(): Promise<void> {
    const why = el('cfgDepSaveWhy');
    const say = (m: string, ok = false): void => {
      if (why) { why.textContent = m; why.classList.toggle('is-ok', ok); }
    };
    const rid = dep.picked[0];
    if (!dep.tplId || !rid) return;
    const tpl = lib.find((x) => x.id === dep.tplId);
    let id = dep.tplId;
    try {
      if (tpl?.canned) {
        if (!window.confirm(t('Canned templates cannot be changed. Save these settings in a new custom copy of "{name}"?', { name: tpl.name }))) return;
        id = (await api<{ id: string }>('templates/' + encodeURIComponent(id) + '/clone', { method: 'POST' })).id;
      }
      const d = await fetchTemplate(id);
      await api('templates/' + encodeURIComponent(id), { ...json({ name: d.name, description: d.description,
        body: d.body, variables: defaultsFromValues(dep.defs, dep.values[rid] ?? defaultsFor()),
        revision: d.revision }), method: 'PUT' });
      await load();
      if (id !== dep.tplId) await pickTemplate(id);
      say(id === dep.tplId && !tpl?.canned ? t('Saved as this template\'s defaults.')
        : t('Saved in the custom template "{name}", now selected.', { name: d.name }), true);
    } catch (e) {
      say(e instanceof Error ? e.message : t('The defaults were not saved'));
    }
  }


  function start(): void {
    const why = readyToStart(dep.picked, dep.previews, dep.acked);
    if (why) { showWhy(why); return; }
    socket.emit('cfgdeploy:start', {
      templateId: dep.tplId,
      confirm: (el('cfgDepConfirm') as HTMLInputElement | null)?.value ?? '',
      targets: dep.picked.map((rid) => {
        const p = dep.previews[rid] as RouterPreview;
        return { routerId: rid, values: dep.values[rid] ?? defaultsFor(), hash: p.hash, expect: p.target, override: '',
          acked: (p.findings ?? []).filter((f) => f.level === 'ack').map(findingKey) };
      }),
    });
  }

  function drawRun(): void {
    const box = el('cfgRollout');
    if (box) box.innerHTML = run ? rolloutView(run) : '';
    const live = !!run && ['canary', 'awaiting-canary', 'rolling'].includes(run.state);
    const setup = el('cfgDep');
    if (setup) setup.hidden = live;
  }

  socket.on('cfgdeploy:state', (p) => {
    if (p.state === 'refused') {
      showWhy(p.error);
      return;
    }
    run = p.runId ? p : null;
    if (visible()) drawRun();
  });

  el('cfgDepTpl')?.addEventListener('change', (e) => void pickTemplate((e.target as HTMLSelectElement).value));
  el('cfgDepRouters')?.addEventListener('change', (e) => {
    const box = e.target as HTMLInputElement;
    const id = box.getAttribute('data-dep-router');
    if (!id) return;
    dep.picked = box.checked ? [...dep.picked.filter((x) => x !== id), id] : dep.picked.filter((x) => x !== id);
    invalidate();
    drawDeploy();
  });
  el('cfgDepRouters')?.addEventListener('click', (e) => {
    const all = (e.target as HTMLElement).closest('[data-dep-all]')?.getAttribute('data-dep-all');
    if (all === null || all === undefined) return;
    dep.picked = all === '1' ? dep.routers.map((r) => r.id) : [];
    invalidate();
    drawDeploy();
  });
  el('cfgDepValues')?.addEventListener('input', (e) => {
    const f = e.target as HTMLInputElement;
    const rid = f.getAttribute('data-dep-val');
    const name = f.getAttribute('data-dep-var');
    const allName = f.getAttribute('data-dep-all-var');
    if (allName) {
      for (const id of dep.picked) (dep.values[id] ??= { ...defaultsFor() })[allName] = f.value;
      document.querySelectorAll<HTMLInputElement>('[data-dep-var="' + allName + '"]').forEach((x) => { x.value = f.value; });
    } else if (rid && name) {
      (dep.values[rid] ??= { ...defaultsFor() })[name] = f.value;
    }
    invalidate();
  });
  el('cfgDepValues')?.addEventListener('change', (e) => {
    if (!(e.target as HTMLElement).hasAttribute('data-dep-reveal')) return;
    dep.reveal = (e.target as HTMLInputElement).checked;
    drawDeploy();
  });
  el('cfgDepSaveDefaults')?.addEventListener('click', () => void saveDefaults());
  el('cfgDepPreview')?.addEventListener('click', () => void previewAll());
  el('cfgDepPreviews')?.addEventListener('change', (e) => {
    const box = e.target as HTMLInputElement;
    const rid = box.getAttribute('data-dep-ack');
    const key = box.getAttribute('data-key');
    if (!rid || !key) return;
    if (box.checked) dep.acked.add(rid + '|' + key);
    else dep.acked.delete(rid + '|' + key);
    drawPreviews();
  });
  el('cfgDepStart')?.addEventListener('click', start);
  el('cfgRollout')?.addEventListener('click', (e) => {
    const tgt = e.target as HTMLElement;
    if (tgt.closest('#cfgDepCancel')) {
      if (window.confirm(t('Stop the deploy before its next router? A router being changed is finished first.'))) {
        socket.emit('cfgdeploy:cancel', {});
      }
    } else if (tgt.closest('#cfgDepContinue')) {
      socket.emit('cfgdeploy:continue', { confirm: (el('cfgDepCount') as HTMLInputElement | null)?.value ?? '' });
    }
  });

  // ── History ──────────────────────────────────────────────────────────────

  let runs: SortableRun[] = [];
  const histSort: SortState = { col: 'createdAt', dir: 'desc' };
  let openRun = '';
  let openDetail: RunDetail | null = null;

  async function loadHistory(): Promise<void> {
    try {
      runs = (await api<{ runs: RunRow[] }>('runs')).runs.map(sortable);
    } catch {
      runs = [];
    }
    drawHistory();
  }

  function drawHistory(): void {
    if (!visible()) return;
    renderSortHeader('cfgHistHead', HISTORY_COLS, histSort, drawHistory);
    const body = el('cfgHistBody');
    if (body) body.innerHTML = historyRows(sortRows(runs, histSort.col, histSort.dir), openRun, openDetail);
    const empty = el('cfgHistEmpty');
    if (empty) empty.hidden = runs.length > 0;
  }

  async function toggleRun(id: string): Promise<void> {
    openRun = openRun === id ? '' : id;
    openDetail = null;
    drawHistory();
    if (!openRun) return;
    try {
      const d = await api<RunDetail>('runs/' + encodeURIComponent(id));
      if (openRun === id) openDetail = d;
    } catch {
      if (openRun === id) openRun = '';
    }
    drawHistory();
  }

  el('cfgHistBody')?.addEventListener('click', (e) => {
    const tr = (e.target as HTMLElement).closest('tr[data-run]');
    const id = tr?.getAttribute('data-run');
    if (id) void toggleRun(id);
  });
  el('cfgHistRefresh')?.addEventListener('click', () => void loadHistory());

  // ── Drift ────────────────────────────────────────────────────────────────

  let drift: DriftRow[] = [];
  const driftSort: SortState = { col: 'templateName', dir: 'asc' };
  const checks: Record<string, DriftCheck> = {};
  let openDrift = '';

  async function loadDrift(): Promise<void> {
    try {
      drift = (await api<{ baselines: DriftRow[] }>('drift')).baselines;
    } catch {
      drift = [];
    }
    drawDrift();
  }

  function drawDrift(): void {
    if (!visible()) return;
    renderSortHeader('cfgDriftHead', DRIFT_COLS, driftSort, drawDrift);
    const body = el('cfgDriftBody');
    if (body) body.innerHTML = driftRows(sortRows(drift, driftSort.col, driftSort.dir), checks, openDrift);
    const empty = el('cfgDriftEmpty');
    if (empty) empty.hidden = drift.length > 0;
  }

  interface CheckReply {
    drifted: boolean; fingerprint: string; checkedAt: number;
    diff: { hunks: Hunk[]; truncated: boolean };
  }

  async function checkDrift(row: DriftRow): Promise<void> {
    const key = driftKey(row);
    checks[key] = { state: 'checking' };
    drawDrift();
    try {
      const r = await api<CheckReply>('drift/check', json({ templateId: row.templateId, routerId: row.routerId }));
      checks[key] = { state: 'done', drifted: r.drifted, fingerprint: r.fingerprint, checkedAt: r.checkedAt,
        hunks: r.diff.hunks, truncated: r.diff.truncated };
      if (r.drifted) openDrift = key;
    } catch (e) {
      checks[key] = { state: 'error', message: e instanceof Error ? e.message : t('The check failed') };
    }
    drawDrift();
  }

  async function acceptDrift(row: DriftRow): Promise<void> {
    const key = driftKey(row);
    const c = checks[key];
    if (c?.state !== 'done') return;
    if (!window.confirm(t('Make what {router} holds now the baseline for {template}? Later checks compare against it.', { router: row.routerLabel, template: row.templateName }))) return;
    try {
      await api('drift/accept', json({ templateId: row.templateId, routerId: row.routerId, fingerprint: c.fingerprint }));
      checks[key] = { ...c, drifted: false, hunks: [] };
      openDrift = '';
      await loadDrift();
    } catch (e) {
      checks[key] = { state: 'error', message: e instanceof Error ? e.message : t('The baseline was not changed'), accept: true };
      drawDrift();
    }
  }

  async function reapply(row: DriftRow): Promise<void> {
    await openDeploy(row.templateId);
    dep.picked = dep.routers.some((r) => r.id === row.routerId) ? [row.routerId] : [];
    invalidate();
    drawDeploy();
  }

  el('cfgDriftBody')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement).closest('[data-drift-act]');
    const key = btn?.closest('[data-drift]')?.getAttribute('data-drift');
    const row = drift.find((r) => driftKey(r) === key);
    if (!btn || !row || !key) return;
    const act = btn.getAttribute('data-drift-act');
    if (act === 'check') void checkDrift(row);
    else if (act === 'diff') {
      openDrift = openDrift === key ? '' : key;
      drawDrift();
    } else if (act === 'accept') void acceptDrift(row);
    else if (act === 'reapply') void reapply(row);
  });
  el('cfgDriftRefresh')?.addEventListener('click', () => void loadDrift());
}
