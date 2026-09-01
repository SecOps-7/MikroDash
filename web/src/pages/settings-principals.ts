/**
 * The Access Management card's wiring — Users, Groups and Roles.
 *
 * ── EVERY DECISION IS SOMEWHERE ELSE, AS WITH THE SITES CARD ────────────────
 *
 * `userRowHtml`, `groupTableHtml`, `roleTableHtml`, `accessSummary`,
 * `sizePrincipalsCard`, `mountPrincipalTabs` and `applyAuthModeVisibility` all
 * live in `settings.ts` and are gated against the live implementations by
 * `access-summary-check`, `principals-card-check` and `auth-visibility-check`.
 * What is here is the fetches, the caches and the listeners.
 *
 * ── THE LOAD ORDER IS LOAD-BEARING ──────────────────────────────────────────
 *
 * ROLES BEFORE USERS AND GROUPS. A grant row names its role by looking it up,
 * and `roleName` answers "unknown role" for one it cannot find — which is a REAL
 * answer for a deleted role and a WRONG one for a role that simply has not
 * arrived. `applyAuthModeVisibility` takes `loadRoles` as a promise and
 * `loadUsers` as a plain call for exactly this reason, and
 * `auth-visibility-check` gates the ordering with a deliberate delay, because
 * with an instant `loadRoles` the chained and the concurrent versions look
 * identical.
 *
 * ── AND THE CARD IS GATED ON A CAPABILITY, NOT ONLY ON AUTH MODE ────────────
 *
 * `applyAuthModeVisibility` owns that rule; this file only supplies it. Unknown
 * caps count as NO: the flash is absence rather than exposure.
 */

import { el, esc } from '../dom';
import {
  userRowHtml, groupTableHtml, roleTableHtml, sizePrincipalsCard, mountPrincipalTabs,
  applyAuthModeVisibility, rolePageRowHtml, grantEditorHtml,
  type UserView, type GroupView, type RoleView, type PrincipalLookups,
  type EditableGrant, type GrantView,
} from './settings';
import { sitesById } from './settings-sites';
import {
  userSavePlan, userSaveOutcome, groupSavePlan, groupSaveOutcome,
  roleSavePlan, roleSaveOutcome, groupMembersHtml, rolePagesFrom,
  userDeletePrompt, groupDeletePrompt, roleDeletePrompt,
  grantAddPlan, grantDeletePlan, grantOutcome,
} from './principal-forms';

interface Deps {
  /** The fleet, for turning a router-scoped grant into a label. */
  routers: () => { id: string; label?: string; host?: string }[];
  /** `window._caps.managePrincipals`, or undefined while the fetch is in flight. */
  mayManage: () => boolean | undefined;
  authMode: () => string;
}

let deps: Deps = {
  routers: () => [],
  mayManage: () => undefined,
  authMode: () => 'none',
};

let roles: RoleView[] = [];

/**
 * The caches the forms read back — the live `_allUsers` and `_groupsCache`.
 *
 * The edit buttons carry only an ID, so the row's record has to be found
 * somewhere; and the GROUP form's member checkboxes are built from the user list
 * the Users card already loaded. A second fetch for either would be a second
 * cache, and two caches of one thing drift — the same reason `sitesById` is
 * imported rather than re-fetched.
 */
let usersCache: UserView[] = [];
let groupsCache: GroupView[] = [];

/**
 * The page catalogue and the write-capable list, both from `GET /api/roles`.
 *
 * `writeCapablePages` is what greys out a Write toggle that would confer
 * nothing. The live comment is explicit that it is "derived from the projection
 * table, never restated in the client" — so it is READ FROM THE RESPONSE here
 * and not declared, which is what keeps a page gaining write actions from
 * needing a frontend change.
 */
let rolePages: { key: string; title: string }[] = [];
let writeCapablePages: string[] = [];

/**
 * The lookups every access summary needs, rebuilt on each render.
 *
 * `sitesById` is IMPORTED from the Sites card rather than fetched again — the
 * live app publishes `window._sitesById` for the same reason. Two caches of one
 * thing drift, and the symptom is a grant row naming a site the Sites table says
 * does not exist.
 */
function lookups(): PrincipalLookups {
  return { roles, sitesById, routers: deps.routers() };
}

async function getJSON(path: string): Promise<Record<string, unknown> | null> {
  try {
    const r = await fetch(path, { credentials: 'same-origin' });
    const j = await r.json();
    return r.ok && j && j.ok ? j : null;
  } catch {
    return null;
  }
}

/** Roles FIRST — see the header. Returns a promise so the order can be chained. */
export async function loadRoles(): Promise<void> {
  const j = await getJSON('/api/roles');
  roles = j ? ((j.roles as RoleView[]) || []) : [];
  // The role FORM's two inputs, from the same response. See their declarations:
  // `writeCapablePages` is derived server-side and must not be restated here.
  rolePages = j ? ((j.pages as { key: string; title: string }[]) || []) : [];
  writeCapablePages = j ? ((j.writeCapablePages as string[]) || []) : [];
  const tb = el('roleTbody');
  if (tb) tb.innerHTML = roleTableHtml(roles);
}

export async function loadUsers(): Promise<void> {
  const j = await getJSON('/api/users');
  const users: UserView[] = j ? ((j.users as UserView[]) || []) : [];
  usersCache = users;
  const tb = el('userTbody');
  if (!tb) return;
  tb.innerHTML = '';
  const look = lookups();
  for (const u of users) {
    // A REAL <tr>, with the row's id on it: `userRowHtml` returns the cells
    // only, and the live code builds the row element around them so a delegated
    // click can find which user it was.
    const tr = document.createElement('tr');
    tr.setAttribute('data-user-id', u.id);
    tr.innerHTML = userRowHtml(u, look);
    tb.appendChild(tr);
  }
}

export async function loadGroups(): Promise<void> {
  const j = await getJSON('/api/groups');
  const groups: GroupView[] = j ? ((j.groups as GroupView[]) || []) : [];
  groupsCache = groups;
  const tb = el('groupTbody');
  if (tb) tb.innerHTML = groupTableHtml(groups, lookups());
}

// ── The three forms ────────────────────────────────────────────────────────
//
// The DECISIONS are `pages/principal-forms.ts`, gated against the live
// `saveUser`/`saveGroup`/`saveRole` by the principal-forms check. What is
// here is the DOM: reading the fields, opening and closing the wrapper, and
// sending what the decision produced.

function val(id: string): string {
  return (el(id) as HTMLInputElement | null)?.value ?? '';
}

function setVal(id: string, v: string): void {
  const e = el(id) as HTMLInputElement | null;
  if (e) e.value = v;
}

function formError(id: string, msg: string): void {
  const e = el(id);
  if (e) e.textContent = msg;
}

function openForm(wrapId: string, open: boolean): void {
  el(wrapId)?.classList.toggle('open', open);
}

/**
 * Send what a save plan produced.
 *
 * Returns BOTH the HTTP status and the parsed body, because one of the three
 * forms consults each — see `principal-forms.ts` rule 5. A body that does not
 * parse is null, which every outcome function reads as a failure.
 */
async function send(plan: { method?: string; url?: string; body?: unknown }): Promise<{
  httpOk: boolean; body: { ok?: boolean; error?: string; user?: { id?: string } } | null;
}> {
  try {
    const r = await fetch(plan.url as string, {
      method: plan.method,
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(plan.body),
    });
    let body: { ok?: boolean; error?: string; user?: { id?: string } } | null = null;
    try {
      body = await r.json();
    } catch {
      // A body that will not parse stays null. See the doc comment.
    }
    return { httpOk: r.ok, body };
  } catch {
    // A NETWORK failure is not an HTTP failure, and the live forms give it its
    // own `.catch` branch. Reported as "no body", which produces the same
    // fallback message from the outcome function.
    return { httpOk: false, body: null };
  }
}

function showUserForm(user: UserView | null): void {
  setVal('uf_id', user ? user.id : '');
  setVal('uf_username', user ? user.username : '');
  const pass = el('uf_password') as HTMLInputElement | null;
  if (pass) {
    // ALWAYS CLEARED, and the placeholder says what an empty box means. That is
    // the visible half of the rule that an empty password leaves the credential
    // alone — without it, somebody editing a username would reasonably think
    // they had to retype the password, and typing one re-hashes the account.
    pass.value = '';
    pass.placeholder = user ? 'leave blank to keep current' : 'password';
  }
  formError('uf_error', '');
  renderGrants('uf_grants', 'user', user ? user.id : '', user?.grants,
    'Save the user first, then grant them access');
  const title = el('uf_title');
  if (title) title.textContent = user ? 'Edit User' : 'Add User';
  openForm('userFormWrap', true);
  (el('uf_username') as HTMLInputElement | null)?.focus();
}

async function saveUser(): Promise<void> {
  formError('uf_error', '');
  const hadId = Boolean(val('uf_id').trim());
  const typed = val('uf_username').trim();
  const plan = userSavePlan({
    id: val('uf_id'), username: val('uf_username'), password: val('uf_password'),
  });
  if (plan.error) { formError('uf_error', plan.error); return; }

  const outcome = userSaveOutcome(hadId, await send(plan));
  if (outcome.error) { formError('uf_error', outcome.error); return; }
  if (outcome.reload) await loadUsers();
  if (outcome.switchToEdit) {
    // A CREATE STAYS OPEN. The live comment: "A new user has no id until now, so
    // the grant editor had nothing to attach to. Rather than making them reopen
    // the form, switch it to edit mode on the returned record and render the
    // editor in place."
    //
    // The record is found in the RELOADED list rather than taken from the
    // response, because the list is what every other reader here uses and a
    // second source would drift from it.
    const created = usersCache.find((u) => u.username === typed);
    if (created) {
      setVal('uf_id', created.id);
      renderGrants('uf_grants', 'user', created.id, created.grants,
        'Save the user first, then grant them access');
    }
    const pass = el('uf_password') as HTMLInputElement | null;
    if (pass) pass.placeholder = 'leave blank to keep current';
    return;
  }
  if (outcome.close) openForm('userFormWrap', false);
}

function showGroupForm(group: GroupView | null): void {
  setVal('gf_id', group ? group.id : '');
  setVal('gf_name', group ? group.name : '');
  setVal('gf_description', (group && group.description) || '');
  formError('gf_error', '');

  const box = el('gf_members');
  if (box) {
    box.innerHTML = groupMembersHtml(
      usersCache.map((u) => ({ id: u.id, username: u.username })),
      (group && group.memberUserIds) || [], esc);
  }
  renderGrants('gf_grants', 'group', group ? group.id : '', group?.grants,
    'Save the group first, then grant it access');
  const title = el('gf_title');
  if (title) title.textContent = group ? 'Edit Group' : 'Add Group';
  openForm('groupFormWrap', true);
  (el('gf_name') as HTMLInputElement | null)?.focus();
}

async function saveGroup(): Promise<void> {
  const members = Array.from(
    el('gf_members')?.querySelectorAll('[data-member]:checked') || [],
  ).map((e) => e.getAttribute('data-member') || '');

  const plan = groupSavePlan({
    id: val('gf_id'), name: val('gf_name'), description: val('gf_description'),
    memberUserIds: members,
  });
  if (plan.error) { formError('gf_error', plan.error); return; }

  const outcome = groupSaveOutcome(await send(plan));
  if (outcome.error) { formError('gf_error', outcome.error); return; }
  if (outcome.close) openForm('groupFormWrap', false);
  if (outcome.reload) await loadGroups();
}

function showRoleForm(role: RoleView | null): void {
  formError('rf_error', '');
  setVal('rf_id', role ? role.id : '');
  setVal('rf_name', role ? role.name : '');
  setVal('rf_description', (role && role.description) || '');

  const access: Record<string, string> = {};
  for (const p of (role && role.pages) || []) access[p.page] = p.access;
  const box = el('rf_pages');
  if (box) {
    box.innerHTML = rolePages
      .map((p) => rolePageRowHtml(p, access[p.key], writeCapablePages)).join('');
  }
  const title = el('rf_title');
  if (title) title.textContent = role ? 'Edit Role' : 'Add Role';
  openForm('roleFormWrap', true);
}

async function saveRole(): Promise<void> {
  // READ BACK OUT OF THE DOM — "the segmented control is the state", as the live
  // comment says, so there is no model to consult. `rolePagesFrom` then drops
  // everything that is not read or write, which is what makes the matrix a
  // REPLACEMENT rather than a patch.
  const rows = Array.from(document.querySelectorAll('#rf_pages [data-page-row]')).map((row) => {
    const on = row.querySelector('.sbtn-primary[data-page-set]');
    return {
      page: row.getAttribute('data-page-row') || '',
      level: on ? (on.getAttribute('data-level') || 'none') : 'none',
    };
  });

  const plan = roleSavePlan({
    id: val('rf_id'), name: val('rf_name'), description: val('rf_description'),
    pages: rolePagesFrom(rows),
  });
  if (plan.error) { formError('rf_error', plan.error); return; }

  const outcome = roleSaveOutcome(await send(plan));
  if (outcome.error) { formError('rf_error', outcome.error); return; }
  if (outcome.close) { openForm('roleFormWrap', false); formError('rf_error', ''); }
  if (outcome.reload) await loadRoles();
}

/**
 * The grant editor, shared by the User and Group forms.
 *
 * A principal with NO ID YET gets the "save first" note instead: there is
 * nothing for a grant to name, and the live forms pass that sentence in for
 * exactly this case.
 */
function renderGrants(
  boxId: string, principalType: string, principalId: string,
  grants: GrantView[] | undefined, unsaved: string,
): void {
  const box = el(boxId);
  if (!box) return;
  // NARROWED, not cast. The editor's Remove button is built from `g.id`, and the
  // server sends one on every row — but `GrantView` makes it optional because
  // the LABEL-only readers (`accessSummary` and friends) never need it. A row
  // that somehow arrived without one is dropped rather than rendered with a
  // Remove button that cannot work, which is the failure a cast would have
  // shipped.
  const editable = (grants || []).filter((g): g is EditableGrant => g.id !== undefined);
  box.innerHTML = principalId
    ? grantEditorHtml(editable, { roles, sitesById, routers: deps.routers() })
    : '<span style="color:var(--text-muted)">' + esc(unsaved) + '</span>';
  box.setAttribute('data-principal-type', principalType);
  box.setAttribute('data-principal-id', principalId);
  box.setAttribute('data-unsaved', unsaved);

  // ── ONE HANDLER PER BOX, REPLACED not added ─────────────────────────────
  //
  // `onclick =` rather than `addEventListener`, exactly as the live editor does
  // it — and here the assignment is what makes re-rendering safe. This function
  // is called again after every add and every remove, so an `addEventListener`
  // would stack a second handler on the same element each time and the third
  // Remove click would fire three requests.
  (box as HTMLElement).onclick = (e) => {
    const t = e.target as HTMLElement | null;
    const del = t?.closest?.('[data-grant-del]') as HTMLElement | null;
    if (del) {
      void runGrant(
        grantDeletePlan(del.getAttribute('data-grant-del') || ''), 'remove',
        boxId, principalType, principalId, unsaved);
      return;
    }
    if (!t?.closest?.('[data-grant-add]')) return;
    const role = box.querySelector('[data-grant-role]') as HTMLSelectElement | null;
    const scope = box.querySelector('[data-grant-scope]') as HTMLSelectElement | null;
    const plan = grantAddPlan(principalType, principalId, role?.value || '',
      scope?.value || '', unsaved);
    if (plan.error) { grantError(boxId, plan.error); return; }
    void runGrant(plan, 'add', boxId, principalType, principalId, unsaved);
  };
}

/** Which form's error line a grant editor reports into. */
function grantError(boxId: string, msg: string): void {
  formError(boxId === 'gf_grants' ? 'gf_error' : 'uf_error', msg);
}

/**
 * Send one grant change, then RE-RENDER THE EDITOR FROM THE RELOADED LIST.
 *
 * The live `refresh()` reloads the principal list and then looks the principal
 * up in it — `opts.grantsOf(principalId)` — rather than trusting the response.
 * That matters for an ADD: the server may have stored something different from
 * what was asked for (a global grant discards the scope id it was sent), and the
 * editor showing what was REQUESTED rather than what was STORED is how a
 * mis-scoped grant looks correct.
 */
async function runGrant(
  plan: { method?: string; url?: string; body?: unknown }, kind: 'add' | 'remove',
  boxId: string, principalType: string, principalId: string, unsaved: string,
): Promise<void> {
  const outcome = grantOutcome(await send(plan), kind);
  if (outcome.error) grantError(boxId, outcome.error);
  if (!outcome.refresh) return;

  if (principalType === 'group') {
    await loadGroups();
    const g = groupsCache.find((x) => x.id === principalId);
    renderGrants(boxId, principalType, principalId, g?.grants, unsaved);
  } else {
    await loadUsers();
    const u = usersCache.find((x) => x.id === principalId);
    renderGrants(boxId, principalType, principalId, u?.grants, unsaved);
  }
}

/**
 * Mount the card.
 *
 * ── THE WRITE ACTIONS ARE WIRED AS OF 2026-08-28 ────────────────────────────
 *
 * This header said they were not, and that they "need endpoints this port does
 * not serve yet". All eleven principal write routes are served now —
 * `POST/PUT/DELETE /api/{users,groups,roles}` and `POST/DELETE /api/grants` —
 * and the decisions behind the three forms live in `pages/principal-forms.ts`,
 * checked against the live originals from one harness.
 *
 * The GRANT editor's own CREATE path is still unbound: its scope pickers and
 * `data-grant-del` wait on the next step, and `attr-audit` records that rather
 * than letting it go unnoticed.
 */
export function initPrincipalsCard(d: Deps): void {
  if (!el('userTbody') && !el('groupTbody') && !el('roleTbody')) return;
  deps = d;

  mountPrincipalTabs();
  wireForms();

  applyAuthModeVisibility(d.authMode(), {
    mayManage: d.mayManage(),
    loadRoles,
    loadUsers: () => { void loadUsers(); void loadGroups(); },
    sizeCard: sizePrincipalsCard,
  });
}

/**
 * The listeners, bound ONCE at mount.
 *
 * DELEGATED on the table bodies rather than per row, because every load replaces
 * their contents — a listener on a button is a listener on an element the next
 * refresh throws away.
 */
function wireForms(): void {
  el('addUserBtn')?.addEventListener('click', () => showUserForm(null));
  el('uf_save')?.addEventListener('click', () => { void saveUser(); });
  el('uf_cancel')?.addEventListener('click', () => openForm('userFormWrap', false));

  el('addGroupBtn')?.addEventListener('click', () => showGroupForm(null));
  el('gf_save')?.addEventListener('click', () => { void saveGroup(); });
  el('gf_cancel')?.addEventListener('click', () => openForm('groupFormWrap', false));

  el('addRoleBtn')?.addEventListener('click', () => showRoleForm(null));
  el('rf_save')?.addEventListener('click', () => { void saveRole(); });
  el('rf_cancel')?.addEventListener('click', () => openForm('roleFormWrap', false));

  // THE ROLE MATRIX's segmented control. The live handler is on `document` and
  // toggles the two classes; the state IS the markup, so nothing else records it.
  document.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const set = t?.closest?.('[data-page-set]') as HTMLButtonElement | null;
    if (!set || set.disabled) return;
    e.preventDefault();
    const row = set.closest('[data-page-row]');
    row?.querySelectorAll('[data-page-set]').forEach((b) => {
      b.classList.toggle('sbtn-primary', b === set);
      b.classList.toggle('sbtn-outline', b !== set);
    });
  });

  el('userTbody')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('[data-action]') as HTMLElement | null;
    if (!btn) return;
    const id = btn.closest('[data-user-id]')?.getAttribute('data-user-id') || '';
    const user = usersCache.find((u) => u.id === id);
    if (!user) return;
    if (btn.getAttribute('data-action') === 'edit') { showUserForm(user); return; }
    if (!confirm(userDeletePrompt(user.username))) return;
    void remove('/api/users/' + encodeURIComponent(id), loadUsers);
  });

  el('groupTbody')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)
      ?.closest?.('[data-group-action]') as HTMLElement | null;
    if (!btn) return;
    const id = btn.getAttribute('data-group-id') || '';
    const group = groupsCache.find((g) => g.id === id);
    if (!group) return;
    if (btn.getAttribute('data-group-action') === 'edit') { showGroupForm(group); return; }
    if (!confirm(groupDeletePrompt(group.name))) return;
    void remove('/api/groups/' + encodeURIComponent(id), loadGroups);
  });

  el('roleTbody')?.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const edit = t?.closest?.('[data-role-edit]') as HTMLElement | null;
    if (edit) {
      const role = roles.find((r) => r.id === edit.getAttribute('data-role-edit'));
      if (role) showRoleForm(role);
      return;
    }
    const del = t?.closest?.('[data-role-del]') as HTMLElement | null;
    if (!del) return;
    const id = del.getAttribute('data-role-del') || '';
    const role = roles.find((r) => r.id === id);
    if (!confirm(roleDeletePrompt(role ? role.name : id))) return;
    void remove('/api/roles/' + encodeURIComponent(id), loadRoles);
  });
}

/**
 * A delete, and what happens when it is refused.
 *
 * THE SERVER'S MESSAGE IS SHOWN, not swallowed. Three of these refusals are
 * things an operator must be told and cannot guess: a role still assigned by N
 * grants, a change that would leave nobody with administrator access, and
 * "Cannot delete your own account". The live code surfaces each with an `alert`
 * for that reason — the count "is more useful than a constraint error".
 *
 * THE LIST RELOADS EITHER WAY. A refusal means the row is still there, and a
 * stale table after a failed delete is how somebody concludes it worked.
 */
async function remove(url: string, reload: () => Promise<void>): Promise<void> {
  try {
    const r = await fetch(url, { method: 'DELETE', credentials: 'same-origin' });
    const j = await r.json().catch(() => null);
    if (!j || !j.ok) alert((j && j.error) || 'Delete failed');
  } catch {
    alert('Request failed');
  }
  await reload();
}

/** Re-apply when the caps or the auth mode arrive after mount. */
export function refreshPrincipalsVisibility(): void {
  applyAuthModeVisibility(deps.authMode(), {
    mayManage: deps.mayManage(),
    loadRoles,
    loadUsers: () => { void loadUsers(); void loadGroups(); },
    sizeCard: sizePrincipalsCard,
  });
}
