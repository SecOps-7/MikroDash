// The Apps Usage page's Manage apps dialog (#167), opened from the header:
// the administrators' own apps, their additions to built-in apps, and their
// categories.
//
// The rules are internal/apps/rules.go's and are kept by
// internal/server/apps_rules.go; this module lists them, edits one at a time
// and asks the server to judge it before it is saved. The button is shown only
// to a global administrator (the catalogue's `canEdit`): every write needs one.
//
// ── THE EDITOR PREVIEWS AS IT IS TYPED ──────────────────────────────────────
//
// A pause in typing sends the rule to POST /api/apps/rules/preview, which
// answers with what it shares with other rules (blocking: it cannot be saved)
// or with built-in apps (allowed: the rule comes first), and how much of the
// last hour's unnamed traffic on this router it would have named. Only counts:
// the DNS names behind them stay on the server.

import { esc, el, pill, renderSortHeader, sortRows, fmtBytes, type SortCol, type SortState } from '../dom';
import { confirmAction, notice } from '../confirm';
import { fieldHtml } from '../form';
import { paletteColour, type PillTone } from '../palette';
import { badge, catLabel, catPill, CAT_TONES, type CategoryInfo } from './apps';

export interface DomainRule { domain: string; subdomains: boolean }
export interface PortRule { proto: string; from: number; to: number }

/** internal/apps Rule. */
export interface AppRule {
  id: string; builtin: boolean; name: string; color: string; icon: string; category: string;
  domains: DomainRule[]; asns: number[]; ports: PortRule[];
}

/** One app as GET /api/apps/catalogue lists it. */
export interface CatalogueApp {
  id: string; name: string; category: string; color: string; logo: string; dark: boolean;
  icon: string; custom: boolean; rule: AppRule | null;
}

/** GET /api/apps/catalogue. */
export interface AppCatalogue { apps: CatalogueApp[]; categories: CategoryInfo[]; canEdit: boolean }

/** A conflict, as internal/apps Conflict. */
export interface Conflict { kind: string; value: string; appId: string; app: string; blocking: boolean; note: string }

/** What the Unnamed detail hands the editor: the networks and ports picked. */
export interface RulePrefill { asns: number[]; ports: PortRule[] }

/** Domains, one per line: "*.example.com" with every name under it,
 *  "example.com" that name alone. Pure. */
export function parseDomains(text: string): DomainRule[] {
  const out: DomainRule[] = [];
  for (const raw of text.split(/[\n,]+/)) {
    let d = raw.trim().toLowerCase();
    if (!d) continue;
    let subdomains = false;
    if (d.startsWith('*.')) { d = d.slice(2); subdomains = true; }
    out.push({ domain: d, subdomains });
  }
  return out;
}

/** AS numbers, one per line, "AS13335" or "13335". An error names the first
 *  line that is neither. Pure. */
export function parseASNs(text: string): number[] | string {
  const out: number[] = [];
  for (const raw of text.split(/[\n,]+/)) {
    const t = raw.trim();
    if (!t) continue;
    const m = /^(?:as)?\s*(\d{1,10})$/i.exec(t);
    if (!m) return '"' + t + '" is not an AS number';
    out.push(Number(m[1]));
  }
  return out;
}

/** Ports, one per line: "27015/udp" or "27015-27030/udp". Pure. */
export function parsePorts(text: string): PortRule[] | string {
  const out: PortRule[] = [];
  for (const raw of text.split(/[\n,]+/)) {
    const t = raw.trim().toLowerCase();
    if (!t) continue;
    const m = /^(\d{1,5})(?:\s*-\s*(\d{1,5}))?\s*\/\s*(tcp|udp)$/.exec(t);
    if (!m) return '"' + raw.trim() + '" is not a port: write 27015/udp or 27015-27030/udp';
    const from = Number(m[1]);
    out.push({ proto: m[3]!, from, to: m[2] ? Number(m[2]) : from });
  }
  return out;
}

/** A rule's matchers as the editor's three boxes show them. Pure. */
export function ruleText(r: Pick<AppRule, 'domains' | 'asns' | 'ports'>): { domains: string; asns: string; ports: string } {
  return {
    domains: r.domains.map((d) => (d.subdomains ? '*.' : '') + d.domain).join('\n'),
    asns: r.asns.map((n) => 'AS' + n).join('\n'),
    ports: r.ports.map((p) => (p.from === p.to ? String(p.from) : p.from + '-' + p.to) + '/' + p.proto).join('\n'),
  };
}

/** A rule's matchers in a few words, for the table. Pure. */
export function ruleSummary(r: AppRule | null): string {
  if (!r) return '';
  const n = (k: number, one: string, many: string): string => (k ? k + ' ' + (k === 1 ? one : many) : '');
  return [n(r.domains.length, 'domain', 'domains'), n(r.asns.length, 'network', 'networks'),
    n(r.ports.length, 'port', 'ports')].filter(Boolean).join(', ');
}

const TONE_NAME: Record<string, string> = {
  rx: 'Blue', tx: 'Green', ok: 'Emerald', warn: 'Amber', err: 'Red', alt: 'Purple', pink: 'Pink', muted: 'Grey',
};

const COLS: SortCol[] = [
  { key: 'name', label: 'App' },
  { key: 'kind', label: 'Kind' },
  { key: 'category', label: 'Category' },
  { key: 'rules', label: 'Your rules' },
  { label: '' },
];

function send<T>(url: string, method: string, body?: unknown): Promise<T> {
  return fetch(url, {
    method, credentials: 'same-origin',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then((r) => r.json().then((d) => (r.ok ? d as T
    : Promise.reject(Object.assign(new Error((d && d.error) || r.statusText), { data: d })))));
}

/** A new app's colour: the theme's blue, as the #rrggbb a colour input takes. */
function defaultColour(): string {
  const m = /(\d+)\D+(\d+)\D+(\d+)/.exec(paletteColour('--accent-rx'));
  return m ? '#' + [m[1], m[2], m[3]].map((n) => Number(n).toString(16).padStart(2, '0')).join('') : '';
}

const kindOf = (a: CatalogueApp): string => (a.custom ? 'custom' : a.rule ? 'built-in + rules' : 'built-in');
const kindTone = (a: CatalogueApp): PillTone => (a.custom ? 'alt' : a.rule ? 'rx' : 'muted');

export interface AppsManage {
  setCatalogue(c: AppCatalogue): void;
  /** Opens the dialog. */
  open(): void;
  /** Opens the editor on an app (null: a new one), with the picked networks
   *  and ports added to what it already has. */
  openEditor(id: string | null, prefill?: RulePrefill): void;
}

export function initAppsManage(o: { routerId: () => string; onChanged: () => void }): AppsManage {
  let cat: AppCatalogue = { apps: [], categories: [], canEdit: false };
  let query = '';
  const sort: SortState = { col: 'name', dir: 'asc' };
  let editing: { id: string | null; builtin: boolean } = { id: null, builtin: false };
  let editingCat: string | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;

  const appById = (id: string): CatalogueApp | undefined => cat.apps.find((a) => a.id === id);

  function render(): void {
    renderSortHeader('appsManageThead', COLS, sort, render);
    const body = el('appsManageTable');
    if (body) {
      const q = query.trim().toLowerCase();
      const rows = cat.apps
        .filter((a) => !q || a.name.toLowerCase().includes(q) || catLabel(a.category).toLowerCase().includes(q) ||
          (a.rule?.domains || []).some((d) => d.domain.includes(q)))
        .map((a) => ({ name: a.name, kind: kindOf(a), category: catLabel(a.category), rules: ruleSummary(a.rule), app: a }));
      body.innerHTML = rows.length ? sortRows(rows, sort.col, sort.dir).map((r) => {
        const a = r.app;
        const actions = a.custom
          ? '<button type="button" class="sbtn sbtn-outline" data-edit="' + esc(a.id) + '">Edit</button> ' +
            '<button type="button" class="sbtn sbtn-danger" data-del="' + esc(a.id) + '">Delete</button>'
          : '<button type="button" class="sbtn sbtn-outline" data-edit="' + esc(a.id) + '">' + (a.rule ? 'Edit' : 'Add rules') + '</button>' +
            (a.rule ? ' <button type="button" class="sbtn sbtn-danger" data-del="' + esc(a.id) + '">Remove rules</button>' : '');
        return '<tr><td><span class="au-name">' + badge(a, 'sm') + '<span>' + esc(a.name) + '</span></span></td>' +
          '<td>' + pill(r.kind, kindTone(a), true) + '</td><td>' + catPill(a.category) + '</td>' +
          '<td class="muted-note">' + esc(r.rules || '-') + '</td><td class="text-end au-actions">' + actions + '</td></tr>';
      }).join('') : '<tr><td colspan="5" class="empty-state">No app matches</td></tr>';
    }
    const cats = el('appsCategoryTable');
    if (cats) {
      cats.innerHTML = cat.categories.map((c) => '<tr><td>' + catPill(c.id) + '</td><td class="text-end mono">' + c.apps + '</td>' +
        '<td class="text-end au-actions">' + (c.builtin ? '<span class="muted-note">built-in</span>'
          : '<button type="button" class="sbtn sbtn-outline" data-cat-edit="' + esc(c.id) + '">Edit</button> ' +
            '<button type="button" class="sbtn sbtn-danger" data-cat-del="' + esc(c.id) + '"' +
            (c.apps ? ' disabled title="Move its apps to another category first"' : '') + '>Delete</button>') +
        '</td></tr>').join('');
    }
  }

  // ── the app editor ───────────────────────────────────────────────────────


  function categoryOptions(selected: string, builtin: boolean): string {
    const first = builtin ? '<option value="">Keep the catalogue\'s</option>' : '<option value="">Uncategorized</option>';
    return first + cat.categories.map((c) => '<option value="' + esc(c.id) + '"' + (c.id === selected ? ' selected' : '') + '>' +
      esc(catLabel(c.id)) + '</option>').join('');
  }

  function openEditor(id: string | null, prefill?: RulePrefill): void {
    const app = id ? appById(id) : undefined;
    const builtin = !!app && !app.custom;
    editing = { id: app ? app.id : null, builtin };
    const rule: AppRule = app?.rule ? { ...app.rule } : {
      id: app?.id || '', builtin, name: app?.custom ? app.name : '', color: app?.custom ? app.color : defaultColour(),
      icon: app?.custom ? app.icon : '', category: app?.custom ? app.category : '', domains: [], asns: [], ports: [],
    };
    if (prefill) {
      rule.asns = [...new Set([...rule.asns, ...prefill.asns])];
      const seen = new Set(rule.ports.map((p) => p.proto + p.from + '-' + p.to));
      rule.ports = [...rule.ports, ...prefill.ports.filter((p) => !seen.has(p.proto + p.from + '-' + p.to))];
    }
    const text = ruleText(rule);
    const title = el('appsRuleTitle');
    if (title) {
      title.innerHTML = app ? '<span class="au-name">' + badge(app, 'sm') + '<span>' + esc(app.name) +
        (builtin ? ': your rules' : '') + '</span></span>' : 'New app';
    }
    const logos = cat.apps.filter((a) => !a.custom && a.logo).sort((a, b) => a.name.localeCompare(b.name));
    const fields = el('appsRuleFields');
    if (fields) {
      fields.innerHTML = (builtin ? '' :
        fieldHtml({ id: 'arName', label: 'Name', required: true, help: 'As the page shows it.' },
          '<input class="sform-input" id="arName" maxlength="40" value="' + esc(rule.name) + '">') +
        fieldHtml({ id: 'arColor', label: 'Colour', help: 'Its tile, its slice of the ring, its letters or logo.' },
          '<input class="sform-input" id="arColor" type="color" value="' + esc(rule.color || defaultColour()) + '">') +
        fieldHtml({ id: 'arIcon', label: 'Logo', help: 'A logo from the built-in apps, or its initials.' },
          '<select class="sform-input" id="arIcon"><option value="">Initials</option>' +
          logos.map((a) => '<option value="' + esc(a.id) + '"' + (a.id === rule.icon ? ' selected' : '') + '>' + esc(a.name) +
            '</option>').join('') + '</select>')) +
        fieldHtml({ id: 'arCategory', label: 'Category', help: builtin ? 'Move this app to another category.' : '' },
          '<select class="sform-input" id="arCategory">' + categoryOptions(rule.category, builtin) + '</select>') +
        fieldHtml({ id: 'arDomains', label: 'Domains', full: true,
          help: 'One per line. *.example.com is every name under example.com as well; example.com alone is that name only.' },
        '<textarea class="sform-input" id="arDomains" rows="4" spellcheck="false" placeholder="*.example.com">' +
          esc(text.domains) + '</textarea>') +
        fieldHtml({ id: 'arAsns', label: 'Networks', help: 'AS numbers, one per line, such as AS13335.' },
          '<textarea class="sform-input" id="arAsns" rows="3" spellcheck="false" placeholder="AS64500">' +
          esc(text.asns) + '</textarea>') +
        fieldHtml({ id: 'arPorts', label: 'Ports', help: 'One per line: 27015/udp or 27015-27030/udp. Not 80 or 443.' },
          '<textarea class="sform-input" id="arPorts" rows="3" spellcheck="false" placeholder="27015/udp">' +
          esc(text.ports) + '</textarea>');
    }
    setErr('appsRuleErr', '');
    const prev = el('appsRulePreview');
    if (prev) prev.innerHTML = '';
    el('appsRuleModal')?.classList.add('open');
    preview();
  }

  /** The editor's rule, or an error naming what cannot be read. */
  function formRule(): AppRule | string {
    const asns = parseASNs((el<HTMLInputElement>('arAsns')?.value ?? ''));
    if (typeof asns === 'string') return asns;
    const ports = parsePorts((el<HTMLInputElement>('arPorts')?.value ?? ''));
    if (typeof ports === 'string') return ports;
    return {
      id: editing.id || '', builtin: editing.builtin, name: (el<HTMLInputElement>('arName')?.value ?? ''), color: (el<HTMLInputElement>('arColor')?.value ?? ''), icon: (el<HTMLInputElement>('arIcon')?.value ?? ''),
      category: (el<HTMLInputElement>('arCategory')?.value ?? ''), domains: parseDomains((el<HTMLInputElement>('arDomains')?.value ?? '')), asns, ports,
    };
  }

  function setErr(id: string, msg: string): void {
    const e = el(id);
    if (e) e.textContent = msg;
  }

  function showConflicts(conflicts: Conflict[], matches?: { destinations: number; bytes: number }): void {
    const box = el('appsRulePreview');
    if (!box) return;
    const lines = conflicts.map((c) => '<li>' + pill(c.blocking ? 'conflict' : 'takes', c.blocking ? 'err' : 'warn', true) +
      ' <span class="mono">' + esc(c.value) + '</span> ' +
      esc(c.blocking ? c.app + ' already has it: remove it there first.' : c.note) + '</li>');
    const m = matches
      ? '<div class="muted-note">In the last hour on this router, it would have named ' + matches.destinations +
        (matches.destinations === 1 ? ' destination' : ' destinations') + ' of unnamed traffic (' +
        esc(fmtBytes(matches.bytes)) + '). Saved, it names traffic from now on.</div>' : '';
    box.innerHTML = (lines.length ? '<ul class="au-conflicts">' + lines.join('') + '</ul>' : '') + m;
  }

  function preview(): void {
    clearTimeout(timer);
    timer = setTimeout(() => {
      const r = formRule();
      if (typeof r === 'string') { setErr('appsRuleErr', r); return; }
      if (!r.builtin && !r.name.trim()) { setErr('appsRuleErr', ''); showConflicts([]); return; }
      const mine = ++seq;
      send<{ conflicts: Conflict[]; matches: { destinations: number; bytes: number } }>('/api/apps/rules/preview', 'POST',
        { routerId: o.routerId(), rule: r, create: !editing.id })
        .then((d) => { if (mine === seq) { setErr('appsRuleErr', ''); showConflicts(d.conflicts || [], d.matches); } })
        .catch((e: Error) => { if (mine === seq) setErr('appsRuleErr', e.message); });
    }, 350);
  }

  function save(): void {
    const r = formRule();
    if (typeof r === 'string') { setErr('appsRuleErr', r); return; }
    const btn = el<HTMLButtonElement>('appsRuleSave');
    if (btn) btn.disabled = true;
    send<{ rule: AppRule }>('/api/apps/rules', 'POST', { routerId: o.routerId(), rule: r, create: !editing.id })
      .then(() => { el('appsRuleModal')?.classList.remove('open'); o.onChanged(); })
      .catch((e: Error & { data?: { conflicts?: Conflict[] } }) => {
        setErr('appsRuleErr', e.message);
        if (e.data?.conflicts) showConflicts(e.data.conflicts);
      })
      .finally(() => { if (btn) btn.disabled = false; });
  }

  function remove(id: string): void {
    const a = appById(id);
    if (!a) return;
    void confirmAction(a.custom
      ? { title: 'Delete the app?', subject: a.name, confirmLabel: 'Delete', danger: true,
        message: 'Its traffic goes back to unnamed, and its stored usage is counted as unnamed.' }
      : { title: 'Remove your rules?', subject: a.name, confirmLabel: 'Remove', danger: true,
        message: 'Remove your rules for this app? The built-in catalogue names it again.' })
      .then((yes) => {
        if (!yes) return;
        send('/api/apps/rules/' + encodeURIComponent(id), 'DELETE').then(() => o.onChanged())
          .catch((e: Error) => void notice('Could not remove', e.message));
      });
  }

  // ── the category editor ──────────────────────────────────────────────────

  function openCategory(id: string | null): void {
    editingCat = id;
    const c = id ? cat.categories.find((x) => x.id === id) : undefined;
    const title = el('appsCategoryTitle');
    if (title) title.textContent = c ? 'Edit category' : 'New category';
    const fields = el('appsCategoryFields');
    if (fields) {
      fields.innerHTML =
        fieldHtml({ id: 'acName', label: 'Name', required: true, help: 'Not the name of a built-in category.' },
          '<input class="sform-input" id="acName" maxlength="30" value="' + esc(c?.name || '') + '">') +
        fieldHtml({ id: 'acTone', label: 'Colour', help: 'Its pill and its slice of the ring.' },
          '<select class="sform-input" id="acTone">' + CAT_TONES.map((t) => '<option value="' + t + '"' +
            (t === (c?.tone || 'alt') ? ' selected' : '') + '>' + esc(TONE_NAME[t] || t) + '</option>').join('') + '</select>');
    }
    setErr('appsCategoryErr', '');
    el('appsCategoryModal')?.classList.add('open');
  }

  function saveCategory(): void {
    send('/api/apps/categories', 'POST', { category: { id: editingCat || '', name: (el<HTMLInputElement>('acName')?.value ?? ''), tone: (el<HTMLInputElement>('acTone')?.value ?? '') } })
      .then(() => { el('appsCategoryModal')?.classList.remove('open'); o.onChanged(); })
      .catch((e: Error) => setErr('appsCategoryErr', e.message));
  }

  function removeCategory(id: string): void {
    const c = cat.categories.find((x) => x.id === id);
    if (!c) return;
    void confirmAction({ title: 'Delete the category?', message: 'This category is deleted.',
      subject: c.name, confirmLabel: 'Delete', danger: true }).then((yes) => {
      if (!yes) return;
      send('/api/apps/categories/' + encodeURIComponent(id), 'DELETE').then(() => o.onChanged())
        .catch((e: Error) => void notice('Could not delete', e.message));
    });
  }

  // ── wiring ───────────────────────────────────────────────────────────────

  el('appsManage')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('button') as HTMLButtonElement | null;
    if (!b) return;
    if (b.dataset.edit) openEditor(b.dataset.edit);
    else if (b.dataset.del) remove(b.dataset.del);
    else if (b.dataset.catEdit) openCategory(b.dataset.catEdit);
    else if (b.dataset.catDel) removeCategory(b.dataset.catDel);
    else if (b.id === 'appsNewApp') openEditor(null);
    else if (b.id === 'appsNewCategory') openCategory(null);
  });
  el('appsManageSearch')?.addEventListener('input', (e) => { query = (e.target as HTMLInputElement).value; render(); });
  el('appsRuleFields')?.addEventListener('input', preview);
  el('appsRuleFields')?.addEventListener('change', preview);
  el('appsRuleSave')?.addEventListener('click', save);
  el('appsCategorySave')?.addEventListener('click', saveCategory);

  return {
    setCatalogue(c: AppCatalogue): void { cat = c; render(); },
    open(): void { render(); el('appsManageModal')?.classList.add('open'); },
    openEditor,
  };
}
