// The Apps tab's pieces, as pure functions: an app and the page's state in,
// markup out. containers-apps.ts places and wires them;
// web/test/containers-apps.test.ts checks them.
//
// ── EVERYTHING FROM THE ROUTER IS ESCAPED, AND A LINK MUST BE http(s) ────────
//
// Names, descriptions, ports and statuses are the router's text, so each goes
// through esc(). The app's UI address and project page become links, so they
// must also be web addresses: anything that is not http or https (a
// `javascript:` URL in a custom app's YAML) is dropped, not linked.
//
// ── TILES ARE DRAWN, NOT FETCHED ────────────────────────────────────────────
//
// Most catalog apps have no icon, and those that do point at third-party hosts.
// MikroDash self-hosts its assets, so a tile is the app's initials on a gradient
// picked from its category: the same app always looks the same.

import { esc } from '../dom';
import type { AppRow } from '../gen/payloads';

/** A web address to link to, or '' when the value is not one. */
export function safeURL(u: string): string {
  return /^https?:\/\/[^\s"'<>]+$/i.test(u.trim()) ? u.trim() : '';
}

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
}

/** The tile's two hues, from the category (so a category reads as a family)
 *  nudged by the name (so two apps in it are not identical). */
export function tileHues(a: Pick<AppRow, 'name' | 'category'>): [number, number] {
  const base = hash(a.category || 'other') % 360;
  const h1 = (base + (hash(a.name) % 40) - 20 + 360) % 360;
  return [h1, (h1 + 38) % 360];
}

/** The app's initials: two letters, from its words or its first two letters. */
export function monogram(name: string): string {
  const words = name.replace(/[^A-Za-z0-9]+/g, ' ').trim().split(' ').filter(Boolean);
  if (!words.length) return '?';
  const m = words.length > 1 ? words[0]![0]! + words[1]![0]! : words[0]!.slice(0, 2);
  return m.toUpperCase();
}

export function tile(a: Pick<AppRow, 'name' | 'category'>, big = false): string {
  const [h1, h2] = tileHues(a);
  return '<span class="apps-tile' + (big ? ' is-big' : '') + '" style="background:linear-gradient(135deg,hsl(' + h1 +
    ' 78% 58%),hsl(' + h2 + ' 72% 42%))" aria-hidden="true">' + esc(monogram(a.name)) + '</span>';
}

/** A category's words, for chips and pills: "home-automation" → "Home automation". */
export function categoryLabel(c: string): string {
  const s = (c || 'other').replace(/[-_]+/g, ' ');
  return s.charAt(0).toUpperCase() + s.slice(1);
}

const STATE_WORD: Record<string, string> = {
  available: 'Available', installing: 'Installing', running: 'Running', stopped: 'Stopped', error: 'Error',
};
const STATE_KIND: Record<string, string> = {
  available: 'hs-never', installing: 'hs-info', running: 'hs-ok', stopped: 'hs-warn', error: 'hs-stale',
};

/** The state pill: by kind, as every pill on the page is. */
export function statePill(state: string): string {
  return '<span class="vpn-hs-badge ' + (STATE_KIND[state] || 'hs-never') + '">' + esc(STATE_WORD[state] || state) + '</span>';
}

/** The ports a user can reach, from firewall-redirects ("8123:8123:tcp:web,…"):
 *  the published port and its name. */
export function ports(a: Pick<AppRow, 'ports'>): string[] {
  return (a.ports || '').split(',').map((p) => p.trim()).filter(Boolean).map((p) => {
    const f = p.split(':');
    return f[0] + (f[3] ? ' ' + f[3] : '');
  });
}

/** Default credentials worth showing: not empty, not "none". */
export function credentials(a: Pick<AppRow, 'defaultCredentials'>): string {
  const c = (a.defaultCredentials || '').trim();
  return c && c.toLowerCase() !== 'none' ? c : '';
}

/** What the page knows beyond the row: an action in flight for this app. */
export interface Pending {
  verb: string;
  status: string;
  error: string;
}

/** The buttons for an app in its state; only Open for a viewer who may not manage. */
export function actions(a: AppRow, may: boolean, pending: Pending | undefined): string {
  const btn = (verb: string, label: string, cls: string): string =>
    '<button type="button" class="apps-btn ' + cls + '" data-app-do="' + verb + '" data-app="' + esc(a.name) + '">' +
    label + '</button>';
  const open = safeURL(a.uiUrl);
  const openBtn = open && a.state === 'running'
    ? '<a class="apps-btn is-primary" href="' + esc(open) + '" target="_blank" rel="noopener noreferrer">Open ↗</a>' : '';
  if (pending && !pending.error) {
    return '<button type="button" class="apps-btn is-busy" disabled><span class="apps-spin"></span>' +
      esc(pending.status || (pending.verb === 'remove' ? 'Removing…' : 'Working…')) + '</button>';
  }
  if (!may) return openBtn;
  switch (a.state) {
    case 'available': return btn('install', 'Install', 'is-primary is-install');
    case 'running': return openBtn + btn('stop', 'Stop', '') + btn('restart', 'Restart', '') + btn('remove', 'Remove', 'is-danger');
    case 'stopped': return btn('start', 'Start', 'is-primary') + btn('remove', 'Remove', 'is-danger');
    case 'error': return btn('start', 'Retry', 'is-primary') + btn('remove', 'Remove', 'is-danger');
    default: return '';
  }
}

/** One app's card. */
export function appCard(a: AppRow, may: boolean, pending: Pending | undefined): string {
  const state = pending && !pending.error ? 'installing' : a.state;
  const cred = credentials(a);
  const ps = ports(a);
  return '<article class="apps-card st-' + esc(state) + '" data-app-card="' + esc(a.name) + '">' +
    '<div class="apps-card-head">' + tile(a) +
    '<div class="apps-card-titles"><div class="apps-card-name">' + esc(a.name) + '</div>' +
    '<div class="apps-card-sub"><span class="apps-card-cat">' + esc(categoryLabel(a.category)) + '</span>' +
    statePill(state) + '</div></div></div>' +
    '<p class="apps-card-desc">' + esc(a.description || 'No description.') + '</p>' +
    '<div class="apps-card-meta">' +
    (a.defaultNetwork ? '<span class="apps-meta">' + esc(a.defaultNetwork === 'lan' ? 'On the LAN' : 'Behind NAT') + '</span>' : '') +
    ps.slice(0, 3).map((p) => '<span class="apps-meta is-mono">' + esc(p) + '</span>').join('') +
    (ps.length > 3 ? '<span class="apps-meta">+' + (ps.length - 3) + '</span>' : '') + '</div>' +
    (state === 'installing' ? '<div class="apps-shimmer"></div>' : '') +
    (pending?.error ? '<div class="apps-card-error">' + esc(pending.error) + '</div>' : '') +
    (cred && (a.state === 'running' || a.state === 'stopped')
      ? '<div class="apps-cred"><span>Default login</span><code>' + esc(cred) + '</code>' +
        '<button type="button" class="apps-copy" data-app-copy="' + esc(cred) + '" title="Copy">Copy</button></div>' : '') +
    '<div class="apps-card-actions">' + actions(a, may, pending) + '</div></article>';
}

/** The details drawer for one app. */
export function drawer(a: AppRow, may: boolean, pending: Pending | undefined): string {
  const page = safeURL(a.projectPage);
  const row = (k: string, v: string): string => (v ? '<div class="apps-kv"><span>' + esc(k) + '</span><b>' + esc(v) + '</b></div>' : '');
  return '<div class="apps-drawer-head">' + tile(a, true) + '<div><h4>' + esc(a.name) + '</h4>' +
    '<div class="apps-card-cat">' + esc(categoryLabel(a.category)) + '</div></div>' +
    '<button type="button" class="apps-close" data-app-close aria-label="Close">×</button></div>' +
    '<p class="apps-drawer-desc">' + esc(a.description || '') + '</p>' +
    (page ? '<a class="apps-link" href="' + esc(page) + '" target="_blank" rel="noopener noreferrer">Project page ↗</a>' : '') +
    '<div class="apps-kvs">' + row('State', STATE_WORD[a.state] || a.state) + row('Status', a.status) +
    row('Network', a.defaultNetwork === 'lan' ? 'On the LAN' : a.defaultNetwork ? 'Behind NAT (internal)' : '') +
    row('Ports', ports(a).join(', ')) + row('Default login', credentials(a)) + row('Memory', a.memory) +
    row('CPU', a.cpu) + row('Image size', a.appSize) + row('Data size', a.dataSize) + '</div>' +
    '<div class="apps-card-actions">' + actions(a, may, pending) + '</div>';
}

/** The filters a view applies: the text, the category, and all/installed/running. */
export interface View {
  q: string;
  cat: string;
  show: 'all' | 'installed' | 'running';
}

export function matches(a: AppRow, v: View): boolean {
  if (v.cat && a.category !== v.cat) return false;
  if (v.show === 'installed' && a.state === 'available') return false;
  if (v.show === 'running' && a.state !== 'running') return false;
  const q = v.q.trim().toLowerCase();
  return !q || a.name.toLowerCase().includes(q) || a.description.toLowerCase().includes(q) ||
    a.category.toLowerCase().includes(q);
}

/** Categories with their counts, most apps first. */
export function categories(apps: AppRow[]): Array<[string, number]> {
  const n: Record<string, number> = {};
  for (const a of apps) n[a.category || 'other'] = (n[a.category || 'other'] || 0) + 1;
  return Object.entries(n).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

/** Installed apps first (running, then the rest), then the store by name. */
export function order(apps: AppRow[]): AppRow[] {
  const rank: Record<string, number> = { running: 0, installing: 1, error: 2, stopped: 3, available: 4 };
  return [...apps].sort((a, b) => (rank[a.state] ?? 5) - (rank[b.state] ?? 5) || a.name.localeCompare(b.name));
}
