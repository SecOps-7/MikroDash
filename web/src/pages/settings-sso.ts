/**
 * The SSO / OIDC tab of Access Management, and the dialog that edits a provider.
 *
 * ── WHERE THIS SITS, AND WHY IT IS NOT ON THE AUTHENTICATION CARD ──────────
 *
 * A provider decides WHO MAY SIGN IN and WITH WHAT ROLE, which is the question
 * Users, Groups and Roles answer. The Authentication card is about how THIS
 * app's own sign-in behaves. The server draws the same line: the API is behind
 * `principalsGuard`, not `maySaveSettings`.
 *
 * ── THE SECRET NEVER COMES BACK ────────────────────────────────────────────
 *
 * `providerView` has no field for it, only `hasSecret`. That has a visible
 * consequence this file has to handle honestly: editing a provider shows an
 * empty secret box, and leaving it empty KEEPS what is stored. The note under
 * the field says so, because a box that looks empty and silently keeps a value
 * is how an operator deletes their own configuration - the same rule the
 * notification channel dialog follows for webhook URLs.
 *
 * An absent `clientSecret` is what the server reads as "keep it". This file
 * therefore OMITS THE KEY rather than sending an empty string, which clears it.
 */

import { el, esc, modalTabs } from '../dom';
import type { RoleView } from './settings';

interface ProviderView {
  id: string;
  name: string;
  enabled: boolean;
  issuer: string;
  clientId: string;
  scopes: string;
  claimUsername: string;
  claimEmail: string;
  claimName: string;
  claimRoles: string;
  iconVersion: number;
  hasSecret: boolean;
  icon: string;
  redirectUri: string;
  roleMap: { claimValue: string; roleId: string }[];
}

let providers: ProviderView[] = [];
let roles: RoleView[] = [];
/** The provider being edited, or null for a new one. */
let editing: ProviderView | null = null;
/** An icon chosen but not yet uploaded. It cannot be sent until the provider
 *  has an id, so a new provider's icon goes up after the first save. */
let pendingIcon: File | null = null;
let selectTab: (which: string) => void = () => {};
/** The redirect URI as the SERVER builds it, for the read-only box. Held
 *  separately from the rows because the FIRST provider has no row to read it
 *  from, and a blank box there reads as "no base URL is set" on an install
 *  where one is. */
let redirectUri = '';

async function getJSON(path: string): Promise<Record<string, unknown> | null> {
  try {
    const r = await fetch(path, { credentials: 'same-origin' });
    const j = await r.json();
    return r.ok && j ? j : null;
  } catch {
    return null;
  }
}

function value(id: string): string {
  return el<HTMLInputElement>(id)?.value.trim() || '';
}

// ── THE TABLE ───────────────────────────────────────────────────────────────

/**
 * A provider's row.
 *
 * The status is a PILL, not a word, because that is this app's rule for a state
 * column - see "A new page's furniture" in CLAUDE.md. The role-mapping count is
 * a pill too, and a provider with none gets the warning colour: no mapping means
 * nobody can sign in through it, which reads as broken unless it is said.
 */
function rowHtml(p: ProviderView): string {
  const status = p.enabled
    ? '<span class="vpn-hs-badge ok">Enabled</span>'
    : '<span class="vpn-hs-badge off">Disabled</span>';
  const n = p.roleMap.length;
  const maps = n
    ? '<span class="vpn-hs-badge ok">' + n + (n === 1 ? ' rule' : ' rules') + '</span>'
    : '<span class="vpn-hs-badge warn" title="Nobody can sign in through this provider '
      + 'until a claim value is mapped to a role">None</span>';
  return '<tr style="border-bottom:1px solid var(--border)">'
    + '<td style="padding:.4rem .5rem">' + esc(p.name) + '</td>'
    + '<td style="padding:.4rem .5rem;color:var(--text-muted);font-family:var(--font-mono);'
    + 'font-size:.7rem;word-break:break-all">' + esc(p.issuer) + '</td>'
    + '<td style="padding:.4rem .5rem">' + status + '</td>'
    + '<td style="padding:.4rem .5rem">' + maps + '</td>'
    + '<td style="padding:.4rem .5rem;text-align:right;white-space:nowrap">'
    + '<button class="sbtn sbtn-outline" data-sso-edit="' + esc(p.id)
    + '" style="padding:.15rem .5rem;font-size:.68rem">Edit</button> '
    + '<button class="sbtn sbtn-danger" data-sso-del="' + esc(p.id)
    + '" style="padding:.15rem .5rem;font-size:.68rem">Delete</button>'
    + '</td></tr>';
}

export async function loadSSOProviders(): Promise<void> {
  const j = await getJSON('/api/sso/providers');
  providers = j ? ((j.providers as ProviderView[]) || []) : [];
  redirectUri = j && typeof j.redirectUri === 'string' ? j.redirectUri : '';
  // ── THE BASE URL BOX PRE-FILLS ─────────────────────────────────────────
  //
  // The server sends the EFFECTIVE value - what the operator stored, or the
  // address this request arrived on when they stored nothing. Showing it beats
  // an empty box, which reads as unconfigured on an install where SSO works.
  //
  // ONLY WHEN EMPTY. Overwriting a value the operator is part-way through
  // typing is the kind of thing that gets noticed once and never forgiven.
  const base = el<HTMLInputElement>('s_baseUrl');
  if (base && !base.value && j && typeof j.baseUrl === 'string') base.value = j.baseUrl;
  const tb = el('ssoTbody');
  if (!tb) return;
  tb.innerHTML = providers.length
    ? providers.map(rowHtml).join('')
    : '<tr><td colspan="5" style="padding:.75rem .5rem;color:var(--text-muted);font-size:.76rem">'
      + 'No providers yet. Add one to let people sign in with an existing account.</td></tr>';
}

// ── THE ROLE MAPPING ROWS ───────────────────────────────────────────────────

/**
 * One mapping row: a claim value and the role it confers.
 *
 * THE ROLE IS A SELECT, NEVER A TEXT BOX. A custom role's id is an opaque
 * random string, so a typed one would be wrong every time and the mapping would
 * silently match nothing - which presents as "SSO does not work" rather than as
 * a typo.
 */
function mapRowHtml(claimValue: string, roleId: string): string {
  const opts = roles.map((r) =>
    '<option value="' + esc(r.id) + '"' + (r.id === roleId ? ' selected' : '') + '>'
    + esc(r.name) + '</option>').join('');
  return '<div class="sform-row" data-sso-map style="margin-bottom:.4rem;align-items:end">'
    + '<div class="sform-group"><input class="sform-input" data-sso-claim type="text" '
    + 'autocomplete="off" spellcheck="false" placeholder="netops" value="' + esc(claimValue)
    + '"></div>'
    + '<div class="sform-group"><select class="sform-input" data-sso-role>'
    + '<option value="">Choose a role&#8230;</option>' + opts + '</select></div>'
    + '<button type="button" class="sbtn sbtn-outline" data-sso-map-del '
    + 'style="padding:.3rem .6rem;font-size:.72rem;flex:0 0 auto">Remove</button>'
    + '</div>';
}

function renderMapRows(rows: { claimValue: string; roleId: string }[]): void {
  const host = el('so_mapRows');
  if (!host) return;
  host.innerHTML = rows.length
    ? rows.map((m) => mapRowHtml(m.claimValue, m.roleId)).join('')
    : mapRowHtml('', '');
}

function readMapRows(): { claimValue: string; roleId: string }[] {
  const out: { claimValue: string; roleId: string }[] = [];
  document.querySelectorAll('#so_mapRows [data-sso-map]').forEach((row) => {
    const claimValue =
      (row.querySelector('[data-sso-claim]') as HTMLInputElement | null)?.value.trim() || '';
    const roleId =
      (row.querySelector('[data-sso-role]') as HTMLSelectElement | null)?.value || '';
    // A wholly empty row is the one the form starts with; it is not an error.
    if (claimValue || roleId) out.push({ claimValue, roleId });
  });
  return out;
}

// ── THE DIALOG ──────────────────────────────────────────────────────────────

function setText(id: string, s: string): void {
  const e = el(id);
  if (e) e.textContent = s;
}

function showIcon(p: ProviderView | null): void {
  const img = el<HTMLImageElement>('so_iconPreview');
  const clear = el('so_iconClear');
  const has = !!(p && p.icon);
  if (img) {
    img.style.display = has ? '' : 'none';
    if (has && p) img.src = p.icon;
  }
  if (clear) clear.style.display = has ? '' : 'none';
  setText('so_iconNote', has ? '' : 'No icon - the button shows the name alone.');
}

function fillModal(p: ProviderView | null): void {
  editing = p;
  pendingIcon = null;
  const set = (id: string, v: string): void => {
    const e = el<HTMLInputElement>(id);
    if (e) e.value = v;
  };
  setText('so_title', p ? 'Edit OIDC provider' : 'Add OIDC provider');
  setText('so_save', p ? 'Save provider' : 'Add provider');
  set('so_id', p?.id || '');
  set('so_name', p?.name || '');
  set('so_issuer', p?.issuer || '');
  set('so_clientId', p?.clientId || '');
  set('so_clientSecret', '');
  set('so_scopes', p?.scopes || 'openid profile email');
  set('so_claimUsername', p?.claimUsername || 'preferred_username');
  set('so_claimEmail', p?.claimEmail || 'email');
  set('so_claimName', p?.claimName || 'name');
  set('so_claimRoles', p?.claimRoles || 'groups');
  // The redirect URI comes from the SERVER's view of the Base URL setting, not
  // from location.origin: it must be the string the server will actually send,
  // or the operator registers one URI and the flow uses another.
  set('so_redirectUri', p?.redirectUri || redirectUri
    || 'Set a Base URL on the Authentication card first');
  const en = el<HTMLInputElement>('so_enabled');
  if (en) en.checked = !!p?.enabled;
  setText('so_secretNote', p && p.hasSecret
    ? 'A secret is stored. Leave this empty to keep it.'
    : '');
  showIcon(p);
  renderMapRows(p?.roleMap || []);
  const err = el('so_error');
  if (err) err.style.display = 'none';
  selectTab('general');
  el('ssoFormWrap')?.classList.add('open');
}

function showError(msg: string): void {
  const e = el('so_error');
  if (!e) return;
  e.textContent = msg;
  e.style.display = '';
}

async function uploadIcon(id: string, file: File): Promise<boolean> {
  const r = await fetch('/api/sso/providers/' + encodeURIComponent(id) + '/icon', {
    method: 'POST', credentials: 'same-origin', body: file,
  });
  if (r.ok) return true;
  const j = await r.json().catch(() => null);
  showError((j && (j.error as string)) || 'The icon could not be saved.');
  return false;
}

async function save(): Promise<void> {
  const body: Record<string, unknown> = {
    name: value('so_name'),
    enabled: !!el<HTMLInputElement>('so_enabled')?.checked,
    issuer: value('so_issuer'),
    clientId: value('so_clientId'),
    scopes: value('so_scopes'),
    claimUsername: value('so_claimUsername'),
    claimEmail: value('so_claimEmail'),
    claimName: value('so_claimName'),
    claimRoles: value('so_claimRoles'),
    roleMap: readMapRows(),
  };
  // ── ABSENT, NOT EMPTY ──────────────────────────────────────────────────
  //
  // The server reads an absent `clientSecret` as "keep what is stored" and an
  // empty string as "clear it". Sending '' here would blank the secret every
  // time somebody saved the form without retyping it.
  const secret = value('so_clientSecret');
  if (secret) body.clientSecret = secret;

  const id = value('so_id');
  const r = await fetch('/api/sso/providers' + (id ? '/' + encodeURIComponent(id) : ''), {
    method: id ? 'PUT' : 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const j = await r.json().catch(() => null);
  if (!r.ok) {
    showError((j && (j.error as string)) || 'The provider could not be saved.');
    return;
  }
  // The icon goes up second, because a new provider has no id until now.
  const savedId = (j && (j.id as string)) || id;
  if (pendingIcon && savedId && !(await uploadIcon(savedId, pendingIcon))) {
    await loadSSOProviders();
    return;
  }
  el('ssoFormWrap')?.classList.remove('open');
  await loadSSOProviders();
}

async function remove(id: string): Promise<void> {
  const p = providers.find((x) => x.id === id);
  // DELETING A PROVIDER DOES NOT DELETE THE ACCOUNTS IT CREATED, and the prompt
  // says so: they stay with their grants and simply have no way to sign in. An
  // operator expecting a cascade would otherwise leave accounts behind without
  // knowing it.
  if (!window.confirm('Delete "' + (p?.name || id) + '"?\n\n'
    + 'Anyone signing in through it loses that way in. The accounts it created '
    + 'remain, with their access - remove them on the Users tab if that is what '
    + 'you want.')) return;
  const r = await fetch('/api/sso/providers/' + encodeURIComponent(id),
    { method: 'DELETE', credentials: 'same-origin' });
  if (r.ok) await loadSSOProviders();
}

export function initSSOCard(rolesOf: () => RoleView[]): void {
  selectTab = modalTabs('ssoFormWrap', 'general');

  el('addSsoBtn')?.addEventListener('click', () => {
    roles = rolesOf();
    fillModal(null);
  });
  el('so_cancel')?.addEventListener('click', () =>
    el('ssoFormWrap')?.classList.remove('open'));
  el('so_save')?.addEventListener('click', () => void save());
  el('so_addMap')?.addEventListener('click', () => {
    const host = el('so_mapRows');
    if (host) host.insertAdjacentHTML('beforeend', mapRowHtml('', ''));
  });

  // The icon picker, the same hidden-input gesture branding-settings.ts uses.
  const file = el<HTMLInputElement>('so_iconFile');
  el('so_iconPick')?.addEventListener('click', () => file?.click());
  file?.addEventListener('change', () => {
    const chosen = file.files && file.files[0];
    if (!chosen) return;
    pendingIcon = chosen;
    setText('so_iconNote', chosen.name + ' - saved when you save the provider.');
    const img = el<HTMLImageElement>('so_iconPreview');
    if (img) {
      img.src = URL.createObjectURL(chosen);
      img.style.display = '';
    }
    el('so_iconClear')!.style.display = '';
  });
  el('so_iconClear')?.addEventListener('click', () => {
    void (async () => {
      pendingIcon = null;
      if (file) file.value = '';
      if (editing?.id) {
        await fetch('/api/sso/providers/' + encodeURIComponent(editing.id) + '/icon',
          { method: 'DELETE', credentials: 'same-origin' });
        editing.icon = '';
      }
      showIcon(editing);
      await loadSSOProviders();
    })();
  });

  // ONE DELEGATED LISTENER for the table and the mapping rows, because both
  // are rebuilt from HTML and a listener bound to a row does not survive that.
  document.addEventListener('click', (ev) => {
    const t = ev.target as HTMLElement | null;
    const edit = t?.closest?.('[data-sso-edit]');
    if (edit) {
      roles = rolesOf();
      const p = providers.find((x) => x.id === edit.getAttribute('data-sso-edit'));
      if (p) fillModal(p);
      return;
    }
    const del = t?.closest?.('[data-sso-del]');
    if (del) {
      void remove(del.getAttribute('data-sso-del') || '');
      return;
    }
    if (t?.closest?.('[data-sso-map-del]')) {
      t.closest('[data-sso-map]')?.remove();
    }
  });
}
