// The template editor's pure parts: which placeholders a body uses, how the
// declarations follow them, and the editor's markup. config-management.ts
// wires them to the page.

import { esc } from '../dom';
import { t } from '../i18n';
import { highlight } from './config-management-cards';

/** One declared setting, as the server stores it (cfgtpl.VarDef). */
export interface VarDef {
  name: string;
  type: string;
  label?: string;
  help?: string;
  default?: string;
  required?: boolean;
  min?: number;
  max?: number;
  options?: string[];
}

/** Above this the editor is a plain text area: highlighting a full export as
 *  the author types is slower than the typing. */
export const HIGHLIGHT_LIMIT = 64 * 1024;

/** Every placeholder a body uses, first appearance first. */
export function usedVars(body: string): string[] {
  const out: string[] = [];
  for (const m of body.matchAll(/\{\{([a-z][a-z0-9_]*)\}\}/g)) {
    const name = m[1] ?? '';
    if (name && !out.includes(name)) out.push(name);
  }
  return out;
}

/**
 * The declarations after the author's edit: every placeholder used and not
 * filled by MikroDash gets a declaration (a new one as text), and one no
 * longer used is kept but reported, so a setting the author filled in is not
 * thrown away by a typo.
 */
export function syncVars(defs: VarDef[], body: string, serverVars: string[]): { defs: VarDef[]; unused: string[] } {
  const used = usedVars(body).filter((n) => !serverVars.includes(n));
  const out = defs.slice();
  for (const n of used) {
    if (!out.some((d) => d.name === n)) out.push({ name: n, type: 'text' });
  }
  return { defs: out, unused: out.filter((d) => !used.includes(d.name)).map((d) => d.name) };
}

/** The highlighted layer under the text area. A trailing newline keeps the
 *  last line's height when the body ends in one. */
export function editorLayer(body: string): string {
  return highlight(body) + (body.endsWith('\n') ? ' ' : '');
}

/** Line numbers for the gutter, the refused line marked. */
export function gutter(body: string, badLine: number): string {
  const n = Math.max(1, body.split('\n').length);
  const out: string[] = [];
  for (let i = 1; i <= n; i++) out.push(i === badLine ? '<span class="cfg-ln-bad">' + i + '</span>' : String(i));
  return out.join('\n');
}

/** One settings row. Server variables are listed apart, locked. */
export function varRow(d: VarDef, types: string[], unused: boolean): string {
  const opt = (t: string): string => '<option value="' + esc(t) + '"' + (t === d.type ? ' selected' : '') + '>' +
    esc(t) + '</option>';
  return '<div class="cfg-var-row' + (unused ? ' is-unused' : '') + '" data-var="' + esc(d.name) + '">' +
    '<div class="cfg-var-name"><span class="cfg-var">{{' + esc(d.name) + '}}</span>' +
    (unused ? '<span class="cfg-meta">not used</span>' : '') + '</div>' +
    '<select class="form-select form-select-sm" data-var-field="type" aria-label="Type">' + types.map(opt).join('') + '</select>' +
    ('<input class="form-control form-control-sm" data-var-field="label" placeholder="' + t('Label') + '" value="') + esc(d.label ?? '') + '">' +
    ('<input class="form-control form-control-sm" data-var-field="default" placeholder="' + t('Default') + '" value="') +
    esc(d.type === 'secret' ? '' : d.default ?? '') + '"' + (d.type === 'secret' ? ' disabled title="A secret has no default: it would be stored with the template"' : '') + '>' +
    (d.type === 'enum' ? ('<input class="form-control form-control-sm" data-var-field="options" placeholder="' + t('Options, comma separated') + '" value="') +
      esc((d.options ?? []).join(',')) + '">' : '') +
    '<label class="cfg-var-req"><input type="checkbox" data-var-field="required"' + (d.required ? ' checked' : '') + '> required</label>' +
    '</div>';
}

export function serverVarRows(names: string[]): string {
  const what: Record<string, string> = {
    mgmt_src: "MikroDash's address, as each router sees it",
    api_service: t('The API service MikroDash uses (api or api-ssl)'),
    api_user: t('The account MikroDash logs in as'),
  };
  return names.map((n) => '<div class="cfg-var-row is-locked"><div class="cfg-var-name"><span class="cfg-var">{{' +
    esc(n) + ('}}</span><span class="cfg-meta">' + t('filled by MikroDash') + '</span></div><div class="cfg-meta">') +
    esc(what[n] ?? '') + '</div></div>').join('');
}

/** The capture form: which router, the whole of it or one menu, and a name. */
export function captureForm(routers: { id: string; label: string }[], menus: string[]): string {
  return '<div class="cfg-capture">' +
    ('<div class="cfg-capture-row"><label>' + t('Router') + '<select class="form-select form-select-sm" id="cfgCapRouter">') +
    routers.map((r) => '<option value="' + esc(r.id) + '">' + esc(r.label) + '</option>').join('') + '</select></label>' +
    ('<label>' + t('What') + '<select class="form-select form-select-sm" id="cfgCapKind">') +
    ('<option value="fragment">' + t('One menu (for additions)') + '</option>') +
    ('<option value="full-export">' + t('The whole router (for full replacement)') + '</option></select></label>') +
    ('<label id="cfgCapMenuWrap">' + t('Menu') + '<select class="form-select form-select-sm" id="cfgCapMenu">') +
    menus.map((m) => '<option value="' + esc(m) + '"' + (m === '/ip/firewall/filter' ? ' selected' : '') + '>' +
      esc(m) + '</option>').join('') + '</select></label>' +
    ('<label>' + t('Name') + '<input class="form-control form-control-sm" id="cfgCapName" placeholder="' + t('e.g. Branch firewall') + '"></label>') +
    ('<button class="cfg-btn cfg-btn-go" type="button" id="cfgCapGo">' + t('Capture') + '</button></div>') +
    ('<p class="cfg-meta">' + t('A menu is captured without its secrets. The whole router is captured with them, for a faithful replacement, and stored encrypted.') + '</p></div>');
}
