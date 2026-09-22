// The Config Management page's markup, as pure functions: data in, HTML out.
// config-management.ts owns the wiring; everything here is testable without a
// socket, a fetch or a page.

import { esc } from '../dom';
import { t } from '../i18n';
import { tokenizeRouterOS } from '../markdown';

/** One template as the Library shows it: canned or stored, one shape. */
export interface LibTemplate {
  id: string;
  name: string;
  description: string;
  /** home, office, security, monitoring, network, or custom. */
  category: string;
  kind: 'fragment' | 'full-export' | 'full-binary';
  canned: boolean;
  version: number;
  lockClass: boolean;
  scope: string[];
  variables: number;
  tags: string[];
  baseline: string | null;
  updatedAt: number;
}

export const CATEGORIES: { key: string; label: string }[] = [
  { key: 'all', label: t('All') },
  { key: 'home', label: t('Home') },
  { key: 'office', label: t('Office') },
  { key: 'security', label: t('Security') },
  { key: 'monitoring', label: t('Monitoring') },
  { key: 'network', label: t('Network') },
  { key: 'custom', label: t('Custom') },
];

/** The external generator. A plain link that never carries data. */
export const GENERATOR_URL = 'https://zille-skill-mikrotik.lovable.app/';

/** Each category's glyph: a 24px stroke icon. */
const GLYPHS: Record<string, string> = {
  home: '<path d="M3 11.5 12 4l9 7.5"/><path d="M5.5 10v9.5h13V10"/><path d="M10 19.5v-5h4v5"/>',
  office: '<rect x="4" y="3.5" width="16" height="17" rx="1.5"/><path d="M8 8h2M14 8h2M8 12h2M14 12h2M10.5 20.5v-3.5h3v3.5"/>',
  security: '<path d="M12 21.5s7.5-3.6 7.5-9.5V5.5L12 2.8 4.5 5.5V12c0 5.9 7.5 9.5 7.5 9.5z"/><path d="M8.8 12.2l2.2 2.2 4.3-4.6"/>',
  monitoring: '<path d="M3 12h3.5l2.5-6 4 12 2.5-6H21"/>',
  network: '<circle cx="12" cy="5" r="2.2"/><circle cx="5" cy="18.5" r="2.2"/><circle cx="19" cy="18.5" r="2.2"/><path d="M12 7.2v4.3M12 11.5 6.4 16.7M12 11.5l5.6 5.2"/>',
  custom: '<path d="M4 20h4L19.5 8.5a2.1 2.1 0 0 0-3-3L5 17z"/><path d="M14.5 7.5l3 3"/>',
};

export function glyph(category: string): string {
  return '<svg viewBox="0 0 24 24" aria-hidden="true">' + (GLYPHS[category] ?? GLYPHS.custom) + '</svg>';
}

/** Highlighted template text: RouterOS's colours, placeholders in their own. */
export function highlight(src: string): string {
  const out: string[] = [];
  const re = /\{\{[a-z][a-z0-9_]*\}\}/g;
  let last = 0;
  const plain = (s: string): void => {
    for (const tok of tokenizeRouterOS(s)) {
      out.push(tok.cls ? '<span class="' + tok.cls + '">' + esc(tok.text) + '</span>' : esc(tok.text));
    }
  };
  for (let m = re.exec(src); m; m = re.exec(src)) {
    plain(src.slice(last, m.index));
    out.push('<span class="cfg-var">' + esc(m[0]) + '</span>');
    last = m.index + m[0].length;
  }
  plain(src.slice(last));
  return out.join('');
}

function kindPill(tpl: LibTemplate): string {
  if (tpl.canned) return '<span class="vpn-hs-badge cfg-pill-canned">' + t('Canned') + '</span>';
  if (tpl.kind === 'full-export') return '<span class="vpn-hs-badge cfg-pill-full">' + t('Full export') + '</span>';
  if (tpl.kind === 'full-binary') return '<span class="vpn-hs-badge cfg-pill-full">' + t('Binary clone') + '</span>';
  return '<span class="vpn-hs-badge cfg-pill-custom">' + t('Custom') + '</span>';
}

/** One Library card. */
export function templateCard(tpl: LibTemplate): string {
  const scope = tpl.scope.slice(0, 3).map((m) => '<span class="cfg-chip">' + esc(m) + '</span>').join('') +
    (tpl.scope.length > 3 ? '<span class="cfg-chip cfg-chip-more">+' + (tpl.scope.length - 3) + '</span>' : '');
  const lock = tpl.lockClass
    ? '<span class="vpn-hs-badge cfg-pill-lock" title="Deploying it arms an automatic revert: if MikroDash cannot ' +
      'log back in, the router puts itself back.">May cut MikroDash off · auto-revert</span>'
    : '';
  const vars = tpl.variables
    ? '<span class="cfg-meta">' + tpl.variables + (tpl.variables === 1 ? ' setting' : ' settings') + '</span>'
    : '<span class="cfg-meta">' + t('No settings') + '</span>';
  return '<article class="cfg-tpl cfg-cat-' + esc(tpl.category) + '" data-cfg-id="' + esc(tpl.id) + '">' +
    '<div class="cfg-tpl-rail"></div>' +
    '<header class="cfg-tpl-head"><span class="cfg-glyph">' + glyph(tpl.category) + '</span>' +
    '<div class="cfg-tpl-titles"><h4 class="cfg-tpl-name">' + esc(tpl.name) + '</h4>' +
    '<div class="cfg-tpl-pills">' + kindPill(tpl) + (tpl.canned ? '<span class="cfg-meta">v' + tpl.version + '</span>' : '') +
    '</div></div></header>' +
    '<p class="cfg-tpl-desc">' + esc(tpl.description || t('No description.')) + '</p>' +
    (lock ? '<div class="cfg-tpl-lock">' + lock + '</div>' : '') +
    '<div class="cfg-tpl-scope">' + scope + '</div>' +
    '<footer class="cfg-tpl-foot">' + vars +
    '<span class="cfg-tpl-actions">' +
    (tpl.canned ? ('<button class="cfg-btn" type="button" data-cfg-act="preview">' + t('Preview') + '</button>') +
      ('<button class="cfg-btn" type="button" data-cfg-act="clone">' + t('Customise') + '</button>')
      : ('<button class="cfg-btn" type="button" data-cfg-act="edit">' + t('Edit') + '</button>') +
      ('<button class="cfg-btn" type="button" data-cfg-act="clone">' + t('Duplicate') + '</button>')) +
    ('<button class="cfg-btn cfg-btn-go" type="button" data-cfg-act="deploy">' + t('Deploy') + '</button>') +
    '</span></footer></article>';
}

/** The external generator: always last, always marked as leaving MikroDash. */
export function generatorCard(): string {
  return '<article class="cfg-tpl cfg-external">' +
    '<header class="cfg-tpl-head"><span class="cfg-glyph">' +
    '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 4h6v6"/><path d="M20 4l-9 9"/>' +
    '<path d="M18 14v5a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 4 19V7.5A1.5 1.5 0 0 1 5.5 6H10"/></svg></span>' +
    ('<div class="cfg-tpl-titles"><h4 class="cfg-tpl-name">' + t('MikroTik config generator') + '</h4>') +
    ('<div class="cfg-tpl-pills"><span class="vpn-hs-badge cfg-pill-external">' + t('External site') + '</span></div></div></header>') +
    '<p class="cfg-tpl-desc">Describe a setup in plain words and get a RouterOS configuration to start from. ' +
    'It opens in a new tab and needs internet access. MikroDash has not reviewed it: paste what it gives you ' +
    'into a new template, and the editor checks every line before anything reaches a router.</p>' +
    ('<footer class="cfg-tpl-foot"><span class="cfg-meta">' + t('Nothing is sent to it from here') + '</span>') +
    '<span class="cfg-tpl-actions"><a class="cfg-btn cfg-btn-go" href="' + GENERATOR_URL + '" target="_blank" ' +
    'rel="noopener noreferrer">Open generator</a></span></footer></article>';
}

/** The Library grid for one category and search. */
export function libraryGrid(all: LibTemplate[], category: string, query: string): string {
  const q = query.trim().toLowerCase();
  const shown = all.filter((tpl) => (category === 'all' || tpl.category === category) &&
    (!q || (tpl.name + ' ' + tpl.description + ' ' + tpl.tags.join(' ') + ' ' + tpl.scope.join(' ')).toLowerCase().includes(q)));
  const cards = shown.map(templateCard).join('');
  const empty = shown.length ? '' : '<div class="cfg-empty">' +
    (category === 'custom' && !q ? 'No custom templates yet. Customise a canned one, capture one from a router, ' +
      'or start a new one.' : t('Nothing matches.')) + '</div>';
  return '<div class="cfg-grid">' + cards + generatorCard() + '</div>' + empty;
}

/** The segmented control, the active one marked. */
export function categoryBar(active: string, counts: Record<string, number>): string {
  return CATEGORIES.map((c) => '<button type="button" class="cfg-seg' + (c.key === active ? ' active' : '') +
    '" data-cfg-cat="' + c.key + '" aria-pressed="' + (c.key === active) + '">' + esc(c.label) +
    '<span class="cfg-seg-n">' + (counts[c.key] ?? 0) + '</span></button>').join('');
}

/** The stat strip's tiles. */
export function statStrip(all: LibTemplate[]): string {
  const canned = all.filter((tpl) => tpl.canned).length;
  const tile = (n: number | string, label: string, cls = ''): string =>
    '<div class="cfg-stat ' + cls + '"><div class="cfg-stat-n">' + n + '</div><div class="cfg-stat-l">' +
    esc(label) + '</div></div>';
  return tile(canned, t('Canned'), 'cfg-stat-canned') + tile(all.length - canned, t('Custom'), 'cfg-stat-custom') +
    tile(all.filter((tpl) => tpl.lockClass).length, t('With auto-revert'), 'cfg-stat-lock') +
    tile(new Set(all.map((tpl) => tpl.category)).size, t('Categories'));
}

/** A template read in full, as the preview drawer shows it. */
export interface TemplateDetail {
  description: string;
  body: string;
  variables: { name: string; type: string; label?: string; default?: string; required?: boolean }[];
  findings: { level: string; code: string; line?: number; message: string }[];
}

const LEVEL_LABEL: Record<string, string> = { refuse: t('Refused'), ack: t('Needs your OK'), warn: t('Note') };

/** One finding: its level as a pill, its line, and what it means. */
export function findingRow(f: TemplateDetail['findings'][number]): string {
  return '<li class="cfg-finding cfg-lvl-' + esc(f.level) + '"><span class="vpn-hs-badge cfg-pill-' + esc(f.level) + '">' +
    esc(LEVEL_LABEL[f.level] ?? f.level) + '</span>' + (f.line ? '<span class="cfg-meta">line ' + f.line + '</span>' : '') +
    '<span class="cfg-finding-msg">' + esc(f.message) + '</span></li>';
}

export function drawerBody(d: TemplateDetail): string {
  const vars = d.variables.length
    ? ('<table class="table table-sm cfg-vars"><thead><tr><th>' + t('Setting') + '</th><th>' + t('Type') + '</th><th>' + t('Default') + '</th></tr></thead><tbody>') +
      d.variables.map((v) => '<tr><td><span class="cfg-var">{{' + esc(v.name) + '}}</span>' +
        (v.label ? '<div class="cfg-meta">' + esc(v.label) + '</div>' : '') + '</td><td>' + esc(v.type) +
        (v.required ? ' <span class="cfg-meta">required</span>' : '') + '</td><td>' +
        (v.default ? '<code>' + esc(v.default) + '</code>' : '<span class="cfg-meta">—</span>') + '</td></tr>').join('') +
      '</tbody></table>'
    : '<div class="cfg-meta">' + t('This template has no settings.') + '</div>';
  const findings = d.findings.length
    ? '<ul class="cfg-findings">' + d.findings.map(findingRow).join('') + '</ul>'
    : '<div class="cfg-meta">' + t('Nothing to flag.') + '</div>';
  return '<p class="cfg-drawer-desc">' + esc(d.description) + '</p>' +
    ('<h5 class="cfg-drawer-h">' + t('Settings') + '</h5>') + vars +
    ('<h5 class="cfg-drawer-h">' + t('Checks') + '</h5>') + findings +
    ('<h5 class="cfg-drawer-h">' + t('Template') + '</h5><pre class="cfg-code md-ros">') + highlight(d.body) + '</pre>';
}
