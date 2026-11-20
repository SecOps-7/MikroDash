// Settings > Shared links: the only place a share link is made, listed,
// edited, revoked or deleted (internal/server/share_api.go). Global
// administrators only; the tab is hidden by caps for everyone else and the
// API refuses them whatever this file shows.
//
// ── THE TOKEN IS SHOWN ONCE ─────────────────────────────────────────────────
//
// The create response carries the full URL, and this is the only time it
// exists outside the browser that asked: the server keeps only its hash. The
// result box says so, and nothing here stores it.

import { el, esc, sortRows, renderSortHeader, type SortState } from '../dom';
import { fmtTs } from '../timefmt';

interface Opt { key: string; label: string }

interface ShareOptionsResp {
  pages: Opt[]; routers: Opt[]; cards: Opt[]; feedValues: Opt[];
  defaultViewerCap: number; maxViewerCap: number;
  minSlides: number; maxSlides: number; minSlideSecs: number; maxSlideSecs: number;
  defaultSlideSecs: number; maxWarmUpSecs: number;
  yourAddress: string; base: string; proxyUntrusted: boolean;
}

interface SlideRow { page: string; routerId: string; secs?: number; shareable?: boolean }

interface LinkOptions {
  theme?: string; view?: string; defaultSecs?: number; transition?: string; speed?: string;
  atEnd?: string; pauseOnHover?: boolean; titleBar?: boolean; progress?: boolean; controls?: boolean;
  offline?: string; warmUpSecs?: number; card?: string; values?: string[]; interfaces?: string[];
}

/** One link as GET /api/share-links lists it (shareView in Go). */
export interface ShareLinkView {
  id: string; label: string; kind: string; page: string; routerId: string;
  slides: SlideRow[]; options: LinkOptions; frameOrigins: string[] | null;
  allowedAddresses: string[] | null; viewerCap: number; viewersNow: number;
  createdBy: string; createdAt: number; expiresAt: number | null; revokedAt: number | null;
  revokeReason: string; lastUsedAt: number | null; useCount: number;
  status: string; neverExpires: boolean; warnings: string[];
}

const KIND_LABEL: Record<string, string> = {
  page: 'Page', slideshow: 'Slideshow', card: 'Card', feed: 'Sensor feed',
};

let opts: ShareOptionsResp | null = null;
let links: ShareLinkView[] = [];
let editing: ShareLinkView | null = null;
let viewTouched = false;
const sort: SortState = { col: 'createdAt', dir: 'desc' };

function labelOf(list: Opt[] | undefined, key: string): string {
  return list?.find((o) => o.key === key)?.label || key;
}

function when(ms: number | null | undefined): string {
  return ms ? fmtTs(ms, false) : '-';
}

/** What a row says it shows: the page and router, or the slideshow's span. */
export function subjectOf(l: ShareLinkView, o: ShareOptionsResp | null): string {
  if (l.kind === 'slideshow') {
    const routers = new Set(l.slides.map((s) => s.routerId));
    return l.slides.length + ' pages on ' + routers.size + (routers.size === 1 ? ' router' : ' routers');
  }
  const router = labelOf(o?.routers, l.routerId);
  if (l.kind === 'card') return labelOf(o?.cards, l.options.card || '') + ' on ' + router;
  if (l.kind === 'feed') return (l.options.values || []).length + ' values from ' + router;
  return labelOf(o?.pages, l.page) + ' on ' + router;
}

/** The status pill: Active, Never expires, Expired or Revoked. */
export function statusPill(l: ShareLinkView): string {
  if (l.status === 'revoked') return '<span class="pill pill-err">Revoked</span>';
  if (l.status === 'expired') return '<span class="pill">Expired</span>';
  if (l.neverExpires) return '<span class="pill pill-warn">Never expires</span>';
  return '<span class="pill pill-ok">Active</span>';
}

function kindPill(kind: string): string {
  return '<span class="pill pill-rx">' + esc(KIND_LABEL[kind] || kind) + '</span>';
}

async function getJSON<T>(path: string): Promise<T | null> {
  try {
    const r = await fetch(path, { credentials: 'same-origin' });
    if (!r.ok) return null;
    return (await r.json()) as T;
  } catch {
    return null;
  }
}

function render(): void {
  const tb = el('shareTbody');
  if (!tb) return;
  const cols = [
    { key: 'label', label: 'Label' }, { key: 'kind', label: 'Kind' }, { key: 'subject', label: 'Shows' },
    { key: 'status', label: 'Status' }, { key: 'expiresAt', label: 'Expires' },
    { key: 'addresses', label: 'Addresses' }, { key: 'createdBy', label: 'Created by' },
    { key: 'createdAt', label: 'Created' }, { key: 'lastUsedAt', label: 'Last used' },
    { key: 'viewers', label: 'Viewers' }, { key: '', label: '' },
  ];
  renderSortHeader('shareThead', cols, sort, render);
  const rows = links.map((l) => ({
    ...l, subject: subjectOf(l, opts),
    addresses: (l.allowedAddresses || []).join(', ') || 'any',
    viewers: l.kind === 'feed' ? -1 : l.viewersNow,
  }));
  const count = el('shareLinksCount');
  const active = links.filter((l) => l.status === 'active').length;
  if (count) count.textContent = String(active);
  const never = links.filter((l) => l.status === 'active' && l.neverExpires).length;
  const note = el('shareNeverNote');
  if (note) note.textContent = never ? never + (never === 1 ? ' never expires' : ' never expire') : '';
  if (rows.length === 0) {
    tb.innerHTML = '<tr><td class="tbl-empty" colspan="11">No shared links yet.</td></tr>';
    return;
  }
  tb.innerHTML = sortRows(rows, sort.col, sort.dir).map((l) => {
    const live = l.status === 'active';
    const warn = l.warnings.length
      ? '<div class="share-row-warn">' + l.warnings.map((w) => esc(w)).join('<br>') + '</div>' : '';
    const viewers = l.kind === 'feed' ? '-' : l.viewersNow + ' of ' + (l.viewerCap || opts?.defaultViewerCap || 5);
    const acts = live
      ? '<button class="sbtn sbtn-outline is-sm" data-share-edit="' + esc(l.id) + '">Edit</button> ' +
        '<button class="sbtn sbtn-outline is-sm sbtn-danger" data-share-revoke="' + esc(l.id) + '">Revoke</button>'
      : '<button class="sbtn sbtn-outline is-sm" data-share-delete="' + esc(l.id) + '">Delete</button>';
    return '<tr><td>' + esc(l.label) + warn + '</td><td>' + kindPill(l.kind) + '</td><td>' + esc(l.subject) +
      '</td><td>' + statusPill(l) + '</td><td>' + (l.neverExpires ? 'Never' : esc(when(l.expiresAt))) +
      '</td><td>' + esc(l.addresses) + '</td><td>' + esc(l.createdBy) + '</td><td>' + esc(when(l.createdAt)) +
      '</td><td>' + esc(when(l.lastUsedAt)) + '</td><td>' + esc(viewers) +
      '</td><td class="share-acts">' + acts + '</td></tr>';
  }).join('');
}

export async function loadShares(): Promise<void> {
  const [list, o] = await Promise.all([
    getJSON<{ links: ShareLinkView[] }>('/api/share-links'),
    opts ? Promise.resolve(opts) : getJSON<ShareOptionsResp>('/api/share-links/options'),
  ]);
  if (o) opts = o;
  links = list?.links || [];
  render();
}

function input(id: string): HTMLInputElement | null {
  return el(id) as HTMLInputElement | null;
}

function select(id: string): HTMLSelectElement | null {
  return el(id) as HTMLSelectElement | null;
}

function fillSelect(id: string, list: Opt[], placeholder: string): void {
  const s = select(id);
  if (!s) return;
  s.innerHTML = '<option value="">' + esc(placeholder) + '</option>' +
    list.map((o) => '<option value="' + esc(o.key) + '">' + esc(o.label) + '</option>').join('');
}

function setKind(kind: string): void {
  document.querySelectorAll<HTMLElement>('#shareFormWrap [data-share-for]').forEach((f) => {
    const kinds = (f.dataset.shareFor || '').split(' ');
    f.style.display = kinds.includes(kind) ? '' : 'none';
  });
  syncLayoutRow();
  const capHelp = el('sh_capHelp');
  if (capHelp && opts) capHelp.textContent = 'Up to ' + opts.maxViewerCap + '.';
}

function syncLayoutRow(): void {
  const row = el('sh_layoutRow');
  const kind = select('sh_kind')?.value;
  if (row) row.style.display = kind === 'page' && select('sh_page')?.value === 'dashboard' ? '' : 'none';
}

function slideRowHtml(s: SlideRow): string {
  const pageOpts = (opts?.pages || []).map((p) =>
    '<option value="' + esc(p.key) + '"' + (p.key === s.page ? ' selected' : '') + '>' + esc(p.label) + '</option>').join('');
  const routerOpts = (opts?.routers || []).map((r) =>
    '<option value="' + esc(r.key) + '"' + (r.key === s.routerId ? ' selected' : '') + '>' + esc(r.label) + '</option>').join('');
  return '<tr><td class="share-slide-n"></td>' +
    '<td><select class="sform-input" data-slide="page"><option value="">Page</option>' + pageOpts + '</select></td>' +
    '<td><select class="sform-input" data-slide="router"><option value="">Router</option>' + routerOpts + '</select></td>' +
    '<td><input type="number" class="sform-input" data-slide="secs" min="10" max="3600" placeholder="default" value="' +
    (s.secs ? String(s.secs) : '') + '"></td>' +
    '<td class="share-acts"><button type="button" class="sbtn sbtn-outline is-sm" data-slide-move="-1" aria-label="Move up">&#8593;</button>' +
    '<button type="button" class="sbtn sbtn-outline is-sm" data-slide-move="1" aria-label="Move down">&#8595;</button>' +
    '<button type="button" class="sbtn sbtn-outline is-sm" data-slide-remove aria-label="Remove">&#10005;</button></td></tr>';
}

function renumberSlides(): void {
  const rows = el('sh_slides')?.querySelectorAll('tr') || [];
  rows.forEach((r, i) => {
    const n = r.querySelector('.share-slide-n');
    if (n) n.textContent = String(i + 1);
  });
  const help = el('sh_slidesHelp');
  if (help && opts) help.textContent = rows.length + ' of ' + opts.minSlides + ' to ' + opts.maxSlides + ' slides, in the order shown.';
}

function readSlides(): SlideRow[] {
  const out: SlideRow[] = [];
  el('sh_slides')?.querySelectorAll('tr').forEach((r) => {
    const v = (k: string): string => (r.querySelector('[data-slide="' + k + '"]') as HTMLInputElement | null)?.value || '';
    const secs = parseInt(v('secs'), 10);
    out.push({ page: v('page'), routerId: v('router'), secs: Number.isFinite(secs) ? secs : 0 });
  });
  return out;
}

function list(id: string): string[] {
  return (input(id)?.value || '').split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);
}

function checked(id: string): boolean {
  return !!input(id)?.checked;
}

/** The warnings the form shows while it is being filled. */
export function formWarnings(base: string, origins: string[], expiry: string, proxyUntrusted: boolean, addresses: string[]): string[] {
  const w: string[] = [];
  if (base.startsWith('http://') && origins.some((o) => o.startsWith('https://'))) {
    w.push('An https page cannot embed an http link. Serve MikroDash over https (a reverse proxy) and use that address.');
  }
  if (expiry === 'never') {
    w.push('This link never expires. It works until you revoke it, or until its creator stops being an administrator.');
  }
  if (proxyUntrusted && addresses.length) {
    w.push('MikroDash is behind a proxy it does not trust, so it sees the proxy\'s address, not the viewer\'s. Set MIKRODASH_TRUSTED_PROXIES before relying on an address list.');
  }
  return w;
}

function refreshWarnings(): void {
  const box = el('sh_warn');
  if (!box) return;
  const w = formWarnings(input('sh_base')?.value || '', list('sh_origins'), select('sh_expiry')?.value || '',
    !!opts?.proxyUntrusted, list('sh_addrs'));
  if (editing && editing.kind === 'page' && (select('sh_page')?.value !== editing.page || select('sh_router')?.value !== editing.routerId)) {
    w.push('Anyone with this link will see the new page instead.');
  }
  box.innerHTML = w.map((x) => esc(x)).join('<br>');
  box.hidden = w.length === 0;
  const dateRow = el('sh_dateRow');
  if (dateRow) dateRow.style.display = select('sh_expiry')?.value === 'date' ? '' : 'none';
}

function showError(msg: string): void {
  const e = el('sh_error');
  if (!e) return;
  e.textContent = msg;
  e.hidden = !msg;
}

function openForm(link: ShareLinkView | null): void {
  if (!opts) return;
  editing = link;
  viewTouched = !!link;
  showError('');
  const result = el('sh_result');
  if (result) result.hidden = true;
  const save = el('sh_save') as HTMLButtonElement | null;
  if (save) { save.hidden = false; save.textContent = link ? 'Save changes' : 'Create link'; }
  const t = el('sh_title');
  if (t) t.textContent = link ? 'Edit shared link' : 'New shared link';
  fillSelect('sh_page', opts.pages, 'Choose a page');
  fillSelect('sh_router', opts.routers, 'Choose a router');
  fillSelect('sh_card', opts.cards, 'Choose a card');
  const vals = el('sh_values');
  if (vals) {
    vals.innerHTML = opts.feedValues.map((v) =>
      '<label><input type="checkbox" value="' + esc(v.key) + '"> ' + esc(v.label) + '</label>').join('');
  }
  const o = link?.options || {};
  const kind = select('sh_kind');
  if (kind) { kind.value = link?.kind || 'page'; kind.disabled = !!link; }
  const set = (id: string, v: string): void => { const e = input(id); if (e) e.value = v; };
  set('sh_label', link?.label || '');
  set('sh_page', link?.page || '');
  set('sh_router', link?.routerId || '');
  set('sh_card', o.card || '');
  set('sh_layout', link ? '' : 'mine');
  const keep = el('sh_layoutKeep') as HTMLOptionElement | null;
  if (keep) keep.hidden = !link;
  set('sh_ifaces', (o.interfaces || []).join(', '));
  vals?.querySelectorAll<HTMLInputElement>('input').forEach((c) => { c.checked = (o.values || []).includes(c.value); });
  set('sh_defaultSecs', String(o.defaultSecs || opts.defaultSlideSecs));
  set('sh_transition', o.transition || 'none');
  set('sh_speed', o.speed || 'normal');
  set('sh_atEnd', o.atEnd || 'loop');
  set('sh_offline', o.offline || 'skip');
  set('sh_warmUp', String(o.warmUpSecs || 0));
  const box = (id: string, v: boolean): void => { const e = input(id); if (e) e.checked = v; };
  box('sh_pause', o.pauseOnHover !== false);
  box('sh_titleBar', o.titleBar !== false);
  box('sh_progress', o.progress !== false);
  box('sh_controls', !!o.controls);
  const keepExp = select('sh_expiry')?.querySelector('option[value="keep"]') as HTMLOptionElement | null;
  if (keepExp) keepExp.hidden = !link;
  set('sh_expiry', link ? 'keep' : '7d');
  set('sh_date', '');
  set('sh_theme', o.theme || 'follow');
  set('sh_view', o.view || 'full');
  set('sh_cap', String(link?.viewerCap || opts.defaultViewerCap));
  const cap = input('sh_cap');
  if (cap) cap.max = String(opts.maxViewerCap);
  set('sh_addrs', (link?.allowedAddresses || []).join(', '));
  set('sh_origins', (link?.frameOrigins || []).join(', '));
  set('sh_base', opts.base);
  const addrHelp = el('sh_addrsHelp');
  if (addrHelp) {
    addrHelp.textContent = 'Addresses or ranges, comma separated. Empty allows any. Your address as MikroDash sees it: ' +
      (opts.yourAddress || 'unknown') + '.';
  }
  const tbody = el('sh_slides');
  if (tbody) {
    const slides = link?.slides?.length ? link.slides : [{ page: '', routerId: '' }, { page: '', routerId: '' }];
    tbody.innerHTML = slides.map(slideRowHtml).join('');
    renumberSlides();
  }
  setKind(kind?.value || 'page');
  refreshWarnings();
  el('shareFormWrap')?.classList.add('open');
}

/** The request body the form describes (shareBody in Go). */
export function bodyFromForm(): Record<string, unknown> {
  const kind = select('sh_kind')?.value || 'page';
  const num = (id: string): number => parseInt(input(id)?.value || '', 10) || 0;
  const options: LinkOptions = {
    theme: select('sh_theme')?.value === 'follow' ? '' : select('sh_theme')?.value,
    view: select('sh_view')?.value,
  };
  if (kind === 'slideshow') {
    Object.assign(options, {
      defaultSecs: num('sh_defaultSecs'), transition: select('sh_transition')?.value,
      speed: select('sh_speed')?.value, atEnd: select('sh_atEnd')?.value,
      offline: select('sh_offline')?.value, warmUpSecs: num('sh_warmUp'),
      pauseOnHover: checked('sh_pause'), titleBar: checked('sh_titleBar'),
      progress: checked('sh_progress'), controls: checked('sh_controls'),
    });
  }
  if (kind === 'card') options.card = select('sh_card')?.value;
  if (kind === 'feed') {
    options.values = Array.from(el('sh_values')?.querySelectorAll<HTMLInputElement>('input:checked') || []).map((c) => c.value);
    options.interfaces = list('sh_ifaces');
    delete options.theme;
    delete options.view;
  }
  const exp = select('sh_expiry')?.value || '7d';
  const dateV = input('sh_date')?.value;
  const body: Record<string, unknown> = {
    kind, label: input('sh_label')?.value || '', options,
    allowedAddresses: list('sh_addrs'), frameOrigins: kind === 'feed' ? [] : list('sh_origins'),
    viewerCap: kind === 'feed' ? 0 : num('sh_cap'), base: input('sh_base')?.value || '',
  };
  if (exp === 'keep') body.keepExpiry = true;
  else body.expiry = { choice: exp, dateMs: exp === 'date' && dateV ? new Date(dateV + 'T23:59:59').getTime() : 0 };
  if (kind === 'page' || kind === 'card' || kind === 'feed') body.routerId = select('sh_router')?.value || '';
  if (kind === 'page') {
    body.page = select('sh_page')?.value || '';
    body.layout = select('sh_layout')?.value || '';
  }
  if (kind === 'slideshow') body.slides = readSlides();
  return body;
}

/**
 * What the result box shows, once: the link for a page, slideshow or card;
 * for a sensor feed, the address Home Assistant polls and the header that
 * carries the token, since a feed's token never goes in a URL.
 */
export function shownOnce(kind: string, url: string): string {
  if (kind !== 'feed') return url;
  const i = url.lastIndexOf('/s/');
  if (i < 0) return url;
  return url.slice(0, i) + '/share/api/feed\nAuthorization: Bearer ' + url.slice(i + 3);
}

async function save(): Promise<void> {
  showError('');
  const body = bodyFromForm();
  const extending = editing && body.expiry && ((body.expiry as { choice: string }).choice === 'never' ||
    (editing.expiresAt !== null && (body.expiry as { choice: string }).choice !== 'keep'));
  if ((!editing && (body.expiry as { choice: string })?.choice === 'never') || extending) {
    const msg = (body.expiry as { choice: string }).choice === 'never'
      ? 'This link will never expire. It keeps working until it is revoked. Continue?'
      : 'This changes when the link expires. Continue?';
    if (!window.confirm(msg)) return;
  }
  const url = editing ? '/api/share-links/' + encodeURIComponent(editing.id) : '/api/share-links';
  let d: { ok?: boolean; error?: string; url?: string; mixedContent?: boolean } = {};
  try {
    const r = await fetch(url, {
      method: editing ? 'PUT' : 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    });
    d = await r.json().catch(() => ({}));
    if (!r.ok || !d.ok) {
      showError(d.error || 'The link could not be saved.');
      return;
    }
  } catch {
    showError('The link could not be saved.');
    return;
  }
  if (!editing && d.url) {
    const code = el('sh_url');
    if (code) code.textContent = shownOnce(String(body.kind), d.url);
    const result = el('sh_result');
    if (result) result.hidden = false;
    const btn = el('sh_save') as HTMLButtonElement | null;
    if (btn) btn.hidden = true;
  } else {
    el('shareFormWrap')?.classList.remove('open');
  }
  await loadShares();
}

async function act(id: string, how: 'revoke' | 'delete'): Promise<void> {
  const l = links.find((x) => x.id === id);
  const q = how === 'revoke'
    ? 'Revoke "' + (l?.label || id) + '"? Anyone viewing it is cut off at once, and it cannot be turned back on.'
    : 'Delete "' + (l?.label || id) + '"? Its row goes; the audit trail keeps what it did.';
  if (!window.confirm(q)) return;
  await fetch('/api/share-links/' + encodeURIComponent(id) + (how === 'revoke' ? '/revoke' : ''), {
    method: how === 'revoke' ? 'POST' : 'DELETE', credentials: 'same-origin',
  }).catch(() => null);
  await loadShares();
}

export function initSharesTab(): void {
  el('shareNewBtn')?.addEventListener('click', () => {
    void (opts ? Promise.resolve() : loadShares()).then(() => openForm(null));
  });
  el('sh_kind')?.addEventListener('change', () => setKind(select('sh_kind')?.value || 'page'));
  el('sh_page')?.addEventListener('change', () => { syncLayoutRow(); refreshWarnings(); });
  el('sh_router')?.addEventListener('change', refreshWarnings);
  el('sh_view')?.addEventListener('change', () => { viewTouched = true; });
  el('sh_origins')?.addEventListener('input', () => {
    // Embed follows the embedding sites until the administrator picks a view.
    const view = select('sh_view');
    if (view && !viewTouched) view.value = list('sh_origins').length ? 'embed' : 'full';
    refreshWarnings();
  });
  for (const id of ['sh_base', 'sh_expiry', 'sh_addrs']) {
    el(id)?.addEventListener('input', refreshWarnings);
    el(id)?.addEventListener('change', refreshWarnings);
  }
  el('sh_addSlide')?.addEventListener('click', () => {
    const tb = el('sh_slides');
    if (!tb || (opts && tb.querySelectorAll('tr').length >= opts.maxSlides)) return;
    tb.insertAdjacentHTML('beforeend', slideRowHtml({ page: '', routerId: '' }));
    renumberSlides();
  });
  el('sh_slides')?.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const row = t?.closest('tr');
    if (!t || !row) return;
    const mv = t.closest<HTMLElement>('[data-slide-move]');
    if (mv) {
      const dir = parseInt(mv.dataset.slideMove || '0', 10);
      const sib = dir < 0 ? row.previousElementSibling : row.nextElementSibling;
      if (sib) {
        if (dir < 0) sib.before(row);
        else sib.after(row);
      }
    } else if (t.closest('[data-slide-remove]')) {
      row.remove();
    }
    renumberSlides();
  });
  el('sh_save')?.addEventListener('click', () => { void save(); });
  el('sh_copy')?.addEventListener('click', () => {
    const text = el('sh_url')?.textContent || '';
    void navigator.clipboard?.writeText(text).catch(() => {});
  });
  el('shareTbody')?.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const ed = t?.closest<HTMLElement>('[data-share-edit]');
    const rv = t?.closest<HTMLElement>('[data-share-revoke]');
    const dl = t?.closest<HTMLElement>('[data-share-delete]');
    if (ed) openForm(links.find((l) => l.id === ed.dataset.shareEdit) || null);
    if (rv) void act(rv.dataset.shareRevoke || '', 'revoke');
    if (dl) void act(dl.dataset.shareDelete || '', 'delete');
  });
}
