// The template editor's pure parts: which placeholders a body uses, how the
// declarations follow them, and the editor's markup. config-management.ts
// wires them to the page.

import { esc } from '../dom';
import { fieldHtml, toggleHtml } from '../form';
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
/** A line number from the server: a positive whole number, or 0 for none.
 *  Anything else (a string, a fraction, markup) is not a line. */
export function lineNumber(v: unknown): number {
  return typeof v === 'number' && Number.isInteger(v) && v > 0 ? v : 0;
}

/**
 * The editor's status line. The text is the server's or an exception's message
 * and is escaped; the line pill is this code's own markup around a checked
 * number. Code scanning alert #182: the line went into the markup unescaped.
 */
export function statusHtml(text: string, line: unknown = 0): string {
  const n = lineNumber(line);
  return (n ? '<span class="pill pill-err">line ' + esc(n) + '</span>' : '') + esc(text);
}

export function gutter(body: string, badLine: number): string {
  const n = Math.max(1, body.split('\n').length);
  const out: string[] = [];
  for (let i = 1; i <= n; i++) out.push(i === badLine ? '<span class="cfg-ln-bad">' + i + '</span>' : String(i));
  return out.join('\n');
}

/**
 * One settings row: the placeholder's name, then its fields. Each control is
 * labelled rather than named by its placeholder, which is for a default. Ids
 * are the variable's name, unique in a template. Server variables are listed
 * apart, locked.
 */
export function varRow(d: VarDef, types: string[], unused: boolean): string {
  const opt = (t: string): string => '<option value="' + esc(t) + '"' + (t === d.type ? ' selected' : '') + '>' +
    esc(t) + '</option>';
  const id = (f: string): string => 'cfgVar-' + d.name + '-' + f;
  const secret = d.type === 'secret';
  return '<div class="cfg-var-row ff-grid' + (unused ? ' is-unused' : '') + '" data-var="' + esc(d.name) + '">' +
    '<div class="cfg-var-name"><span class="cfg-var">{{' + esc(d.name) + '}}</span>' +
    (unused ? '<span class="cfg-meta">not used</span>' : '') + '</div>' +
    fieldHtml({ id: id('type'), label: 'Type' }, '<select class="sform-input" id="' + esc(id('type')) +
      '" data-var-field="type">' + types.map(opt).join('') + '</select>') +
    fieldHtml({ id: id('label'), label: 'Label' }, '<input class="sform-input" id="' + esc(id('label')) +
      '" data-var-field="label" value="' + esc(d.label ?? '') + '">') +
    fieldHtml({ id: id('default'), label: 'Default' }, '<input class="sform-input" id="' + esc(id('default')) +
      '" data-var-field="default" value="' + esc(secret ? '' : d.default ?? '') + '"' +
      (secret ? ' disabled title="A secret has no default: it would be stored with the template"' : '') + '>') +
    (d.type === 'enum' ? fieldHtml({ id: id('options'), label: 'Options' }, '<input class="sform-input" id="' +
      esc(id('options')) + '" data-var-field="options" placeholder="Comma separated" value="' +
      esc((d.options ?? []).join(',')) + '">') : '') +
    fieldHtml({ id: id('required'), label: 'Required' }, toggleHtml(id('required'), !!d.required, 'data-var-field="required"')) +
    '</div>';
}

export function serverVarRows(names: string[]): string {
  const what: Record<string, string> = {
    mgmt_src: "MikroDash's address, as each router sees it",
    api_service: 'The API service MikroDash uses (api or api-ssl)',
    api_user: 'The account MikroDash logs in as',
  };
  return names.map((n) => '<div class="cfg-var-row is-locked"><div class="cfg-var-name"><span class="cfg-var">{{' +
    esc(n) + '}}</span><span class="cfg-meta">filled by MikroDash</span></div><div class="cfg-meta">' +
    esc(what[n] ?? '') + '</div></div>').join('');
}

/** The capture form: which router, the whole of it or one menu, and a name. */
export function captureForm(routers: { id: string; label: string }[], menus: string[]): string {
  return '<div class="cfg-capture">' +
    '<div class="ff-inline">' +
    fieldHtml({ id: 'cfgCapRouter', label: 'Router' }, '<select class="sform-input" id="cfgCapRouter">' +
      routers.map((r) => '<option value="' + esc(r.id) + '">' + esc(r.label) + '</option>').join('') + '</select>') +
    fieldHtml({ id: 'cfgCapKind', label: 'What' }, '<select class="sform-input" id="cfgCapKind">' +
      '<option value="fragment">One menu (for additions)</option>' +
      '<option value="full-export">The whole router (for full replacement)</option></select>') +
    // The wrapper's id is what config-management.ts hides for a whole-router
    // capture: the whole field goes, never just its box.
    fieldHtml({ id: 'cfgCapMenu', label: 'Menu', attrs: 'id="cfgCapMenuWrap"' }, '<select class="sform-input" id="cfgCapMenu">' +
      menus.map((m) => '<option value="' + esc(m) + '"' + (m === '/ip/firewall/filter' ? ' selected' : '') + '>' +
        esc(m) + '</option>').join('') + '</select>') +
    fieldHtml({ id: 'cfgCapName', label: 'Name' }, '<input class="sform-input" id="cfgCapName" placeholder="e.g. Branch firewall">') +
    '<button class="sbtn sbtn-primary" type="button" id="cfgCapGo">Capture</button></div>' +
    '<p class="cfg-meta">A menu is captured without its secrets. The whole router is captured with them, for a faithful ' +
    'replacement, and stored encrypted.</p></div>';
}
