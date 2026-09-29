// The Config Management page: its tabs, and the panels each one owns.
//
// Everything on this page is read on demand over REST (/api/config/…); there
// is no collector behind it, so nothing is fetched while it is not shown.
// The markup is built by config-management-cards.ts and -editor.ts.

import { el, esc, renderSortHeader, sortRows, type SortState } from '../dom';
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
import {
  drawProfiles, statePill, type CredLink, type CredProfile,
} from './config-management-credentials';

const TABS = ['library', 'editor', 'deploy', 'history', 'drift', 'credentials'] as const;
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
    for (const t of TABS) {
      const p = el('cfgPanel-' + t);
      if (p) p.hidden = t !== next;
    }
    if (next === 'history') void loadHistory();
    else if (next === 'drift') void loadDrift();
    else if (next === 'credentials') void loadCredentials();
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

  // ── Credential profiles (#143) ───────────────────────────────────────────
  //
  // RouterOS accounts MikroDash puts on routers and keeps in step. NOT
  // MikroDash's own login: the server refuses that per router
  // (internal/guard/selfguard.go), and the copy on the panel says so.

  let cps: CredProfile[] = [];
  let cpLinks: CredLink[] = [];
  /** The profile open in the dialog; null when creating. */
  let cpEditing: CredProfile | null = null;
  /** The profile whose routers are open. */
  let cpLinksFor = '';
  /**
   * The seventeen RouterOS policies, AS THE SERVER SENDS THEM.
   *
   * Never a copy typed here. CLAUDE.md records what a second list costs:
   * `dnsStatic` offered six of the nine DNS record types, so a router holding an
   * MX record opened a form showing "A" and saving rewrote the record. The
   * server sends `resource.UserPolicies` with the profile list, so there is one
   * vocabulary and it cannot drift.
   */
  let cpVocabulary: string[] = [];

  async function credApi<T>(path: string, init?: RequestInit): Promise<T> {
    const r = await fetch('/api/credentials/' + path, { credentials: 'same-origin', ...init });
    const body = (await r.json().catch(() => ({}))) as T & { ok?: boolean; error?: string };
    if (!r.ok || body.ok === false) {
      throw new ApiError(body.error || 'The request failed (' + r.status + ')', r.status);
    }
    return body;
  }

  async function loadCredentials(): Promise<void> {
    if (!visible()) return;
    try {
      const got = await credApi<{ profiles: CredProfile[]; policyVocabulary: string[] }>('profiles');
      cps = got.profiles;
      cpVocabulary = got.policyVocabulary;
      const all: CredLink[] = [];
      for (const p of cps) {
        const l = await credApi<{ links: CredLink[] }>('profiles/' + p.id + '/links');
        all.push(...l.links);
      }
      cpLinks = all;
      drawProfiles(cps, cpLinks);
    } catch {
      cps = [];
      cpLinks = [];
      drawProfiles(cps, cpLinks);
    }
  }

  const cpOpen = (id: string, on: boolean): void => {
    el(id)?.classList.toggle('open', on);
  };

  function cpShowError(msg: string): void {
    const n = el('cpError');
    if (!n) return;
    n.textContent = msg;
    n.style.display = msg ? '' : 'none';
  }

  /** The seventeen RouterOS policies, rendered as checkboxes. */
  function cpDrawPolicies(chosen: readonly string[]): void {
    const host = el('cpPolicies');
    if (!host) return;
    host.innerHTML = cpVocabulary.map((p) => '<label class="cp-policy">'
      + `<input type="checkbox" data-cp-policy="${esc(p)}"${chosen.includes(p) ? ' checked' : ''}>`
      + `<span>${esc(p)}</span></label>`).join('');
  }

  function cpReadPolicies(): string[] {
    return Array.from(
      document.querySelectorAll<HTMLInputElement>('#cpPolicies [data-cp-policy]'))
      .filter((b) => b.checked)
      .map((b) => b.getAttribute('data-cp-policy') ?? '');
  }

  /** Shows the custom fields and the `full` warning for the chosen permission. */
  function cpPermChanged(): void {
    const perm = el<HTMLSelectElement>('cpPerm')?.value ?? 'read';
    const custom = el('cpCustom');
    if (custom) custom.style.display = perm === 'custom' ? '' : 'none';
    const warn = el('cpFullNote');
    if (warn) warn.style.display = perm === 'full' ? '' : 'none';
  }

  function cpOpenForm(p: CredProfile | null): void {
    cpEditing = p;
    cpShowError('');
    const title = el('cpModalTitle');
    if (title) title.textContent = p ? 'Edit credential profile' : 'New credential profile';
    const set = (id: string, v: string): void => {
      const n = el<HTMLInputElement>(id);
      if (n) n.value = v;
    };
    set('cpName', p?.name ?? '');
    set('cpDesc', p?.description ?? '');
    set('cpUser', p?.username ?? '');
    // EMPTY EVEN WHEN ONE IS SET. The server never sends the password back, so
    // there is nothing to prefill, and blank means "leave it alone".
    set('cpPass', '');
    set('cpGroup', p?.groupName ?? '');
    const perm = el<HTMLSelectElement>('cpPerm');
    if (perm) perm.value = p ? (p.permKind === 'custom' ? 'custom' : p.builtinGroup) : 'read';
    const note = el('cpPassNote');
    if (note) {
      note.textContent = p
        ? 'Leave blank to keep the current password. A new one is applied to every linked router.'
        : 'Required for a new profile.';
    }
    cpDrawPolicies(p?.policies ?? []);
    cpPermChanged();
    cpOpen('cpModal', true);
  }

  async function cpSave(): Promise<void> {
    const perm = el<HTMLSelectElement>('cpPerm')?.value ?? 'read';
    const body = {
      name: el<HTMLInputElement>('cpName')?.value ?? '',
      description: el<HTMLInputElement>('cpDesc')?.value ?? '',
      username: el<HTMLInputElement>('cpUser')?.value ?? '',
      permKind: perm === 'custom' ? 'custom' : 'builtin',
      builtinGroup: perm === 'custom' ? '' : perm,
      groupName: perm === 'custom' ? (el<HTMLInputElement>('cpGroup')?.value ?? '') : '',
      policies: perm === 'custom' ? cpReadPolicies() : [],
      password: el<HTMLInputElement>('cpPass')?.value ?? '',
    };
    try {
      const path = cpEditing ? 'profiles/' + cpEditing.id : 'profiles';
      await credApi(path, { ...json(body), method: cpEditing ? 'PUT' : 'POST' });
      cpOpen('cpModal', false);
      await loadCredentials();
    } catch (e) {
      cpShowError(e instanceof ApiError ? e.message : 'The profile could not be saved');
    }
  }

  function cpDrawLinks(): void {
    const body = el('cpLinksBody');
    const empty = el('cpLinksEmpty');
    if (!body || !empty) return;
    const mine = cpLinks.filter((l) => l.profileId === cpLinksFor);
    body.innerHTML = mine.map((l) => {
      const name = routerName(l.routerId);
      // RETRY IS OFFERED ONLY WHERE IT MEANS SOMETHING: a terminal refusal is
      // the one state nothing retries on a timer, so it is the one state with a
      // button. Offering it everywhere would suggest the others are stuck.
      const retry = l.state === 'refused' || l.state === 'conflict'
        ? `<button class="cfg-btn" type="button" data-cp-retry="${esc(l.routerId)}">Retry</button> `
        : '';
      return `<tr><td>${esc(name)}</td><td>${statePill(l.state)}</td>`
        + `<td class="cfg-meta">${esc(l.error || l.code)}</td>`
        + `<td class="text-end">${retry}`
        + `<button class="cfg-btn" type="button" data-cp-unlink="${esc(l.routerId)}">Remove</button>`
        + '</td></tr>';
    }).join('');
    empty.hidden = mine.length > 0;

    const pick = el<HTMLSelectElement>('cpAddRouter');
    if (pick) {
      const on = new Set(mine.map((l) => l.routerId));
      pick.innerHTML = dep.routers.filter((r) => !on.has(r.id))
        .map((r) => `<option value="${esc(r.id)}">${esc(r.label)}</option>`).join('');
    }
  }

  function routerName(id: string): string {
    return dep.routers.find((r) => r.id === id)?.label ?? id;
  }

  async function cpOpenLinks(id: string): Promise<void> {
    cpLinksFor = id;
    const p = cps.find((x) => x.id === id);
    const title = el('cpLinksTitle');
    if (title) title.textContent = p ? 'Routers - ' + p.name : 'Routers';
    if (!dep.routers.length) await loadRouters();
    cpDrawLinks();
    cpOpen('cpLinksModal', true);
  }

  // ── Wiring ───────────────────────────────────────────────────────────────

  el('cpNew')?.addEventListener('click', () => cpOpenForm(null));
  el('cpPerm')?.addEventListener('change', cpPermChanged);
  el('cpCancel')?.addEventListener('click', () => cpOpen('cpModal', false));
  el('cpSave')?.addEventListener('click', () => void cpSave());
  el('cpLinksClose')?.addEventListener('click', () => cpOpen('cpLinksModal', false));
  el('cpLinkAdd')?.addEventListener('click', () => {
    const id = el<HTMLSelectElement>('cpAddRouter')?.value ?? '';
    if (!id) return;
    void (async () => {
      await credApi('profiles/' + cpLinksFor + '/links', json({ routerIds: [id] }));
      await loadCredentials();
      cpDrawLinks();
    })();
  });
  el('cpBody')?.addEventListener('click', (e) => {
    const t = e.target as HTMLElement;
    const edit = t.closest('[data-cp-edit]')?.getAttribute('data-cp-edit');
    if (edit) {
      cpOpenForm(cps.find((p) => p.id === edit) ?? null);
      return;
    }
    const links = t.closest('[data-cp-links]')?.getAttribute('data-cp-links');
    if (links) void cpOpenLinks(links);
  });
  el('cpLinksBody')?.addEventListener('click', (e) => {
    const t = e.target as HTMLElement;
    const retry = t.closest('[data-cp-retry]')?.getAttribute('data-cp-retry');
    if (retry) {
      void (async () => {
        await credApi('profiles/' + cpLinksFor + '/links/' + retry + '/retry', { method: 'POST' });
        await loadCredentials();
        cpDrawLinks();
      })();
      return;
    }
    const drop = t.closest('[data-cp-unlink]')?.getAttribute('data-cp-unlink');
    if (drop) {
      void (async () => {
        await credApi('profiles/' + cpLinksFor + '/links/' + drop, { method: 'DELETE' });
        await loadCredentials();
        cpDrawLinks();
      })();
    }
  });

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
        const t = await fetchTemplate(id);
        dep.defs = varsOf(t.variables);
        dep.kind = t.kind;
        dep.secrets = {};
        for (const d of dep.defs) if (d.type === 'secret') dep.secrets[d.name] = newSecret();
      } catch (e) {
        showWhy(e instanceof Error ? e.message : 'The template could not be read');
      }
    }
    drawDeploy();
  }

  function drawDeploy(): void {
    const sel = el('cfgDepTpl') as HTMLSelectElement | null;
    if (sel) {
      sel.innerHTML = '<option value="">Choose a template</option>' + lib.map((t) => '<option value="' + esc(t.id) + '"' +
        (t.id === dep.tplId ? ' selected' : '') + '>' + esc(t.name) + (t.canned ? '' : ' (custom)') + '</option>').join('');
    }
    const t = lib.find((x) => x.id === dep.tplId);
    const meta = el('cfgDepTplMeta');
    if (meta) {
      meta.textContent = !t ? '' : (t.kind === 'full-export' ? 'Full replacement: each router is reset and rebuilt. '
        : 'An addition: merged into what each router already has. ') +
        (t.lockClass ? 'It can cut MikroDash off, so each router arms an automatic revert first.' : '');
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
    if (confirm) confirm.placeholder = canary ? 'Type ' + labelOf(canary) + ' to deploy' : 'Pick routers first';
    showWhy(dep.tplId ? readyToStart(dep.picked, dep.previews, dep.acked) : 'Choose a template');
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
    if (btn) { btn.disabled = true; btn.textContent = 'Reading each router…'; }
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
        dep.previews[rid] = { routerId: rid, error: e instanceof Error ? e.message : 'The preview failed' };
      }
    }));
    if (btn) { btn.disabled = false; btn.textContent = 'Preview again'; }
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
    const t = lib.find((x) => x.id === dep.tplId);
    let id = dep.tplId;
    try {
      if (t?.canned) {
        if (!window.confirm('Canned templates cannot be changed. Save these settings in a new custom copy of "' +
          t.name + '"?')) return;
        id = (await api<{ id: string }>('templates/' + encodeURIComponent(id) + '/clone', { method: 'POST' })).id;
      }
      const d = await fetchTemplate(id);
      await api('templates/' + encodeURIComponent(id), { ...json({ name: d.name, description: d.description,
        body: d.body, variables: defaultsFromValues(dep.defs, dep.values[rid] ?? defaultsFor()),
        revision: d.revision }), method: 'PUT' });
      await load();
      if (id !== dep.tplId) await pickTemplate(id);
      say(id === dep.tplId && !t?.canned ? 'Saved as this template\'s defaults.'
        : 'Saved in the custom template "' + d.name + '", now selected.', true);
    } catch (e) {
      say(e instanceof Error ? e.message : 'The defaults were not saved');
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
    const t = e.target as HTMLElement;
    if (t.closest('#cfgDepCancel')) {
      if (window.confirm('Stop the deploy before its next router? A router being changed is finished first.')) {
        socket.emit('cfgdeploy:cancel', {});
      }
    } else if (t.closest('#cfgDepContinue')) {
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
      checks[key] = { state: 'error', message: e instanceof Error ? e.message : 'The check failed' };
    }
    drawDrift();
  }

  async function acceptDrift(row: DriftRow): Promise<void> {
    const key = driftKey(row);
    const c = checks[key];
    if (c?.state !== 'done') return;
    if (!window.confirm('Make what ' + row.routerLabel + ' holds now the baseline for ' + row.templateName +
      '? Later checks compare against it.')) return;
    try {
      await api('drift/accept', json({ templateId: row.templateId, routerId: row.routerId, fingerprint: c.fingerprint }));
      checks[key] = { ...c, drifted: false, hunks: [] };
      openDrift = '';
      await loadDrift();
    } catch (e) {
      checks[key] = { state: 'error', message: e instanceof Error ? e.message : 'The baseline was not changed', accept: true };
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
