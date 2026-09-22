/**
 * What the User, Group and Role forms DECIDE — separated from what they touch.
 *
 * ── THREE FORMS THAT LOOK ALIKE AND ARE NOT ────────────────────────────────
 *
 * `saveUser`, `saveGroup` and `saveRole` sit within three hundred lines of each
 * other in `public/app.js` and read almost identically. They differ in six ways,
 * every one of which is invisible until it is wrong:
 *
 *  1. THE REQUIRED-FIELD MESSAGE. "Username required" for the user form and
 *     "Name is required" for the other two — not one shared string.
 *  2. THE FALLBACK MESSAGE. 'Save failed', 'Could not save the group', 'Could
 *     not save the role'. Three different ones, each shown when the server sent
 *     no `error`.
 *  3. WHICH FIELDS ARE TRIMMED. The user form trims the id; the other two do
 *     not. All three trim the name. NONE trims the password.
 *  4. THE PASSWORD IS OMITTED WHEN EMPTY, rather than sent as "". That is the
 *     client half of the server rule that an empty password means "leave the
 *     credential alone" — send `""` and the account is re-hashed on every edit.
 *  5. THE GROUP FORM CHECKS THE HTTP STATUS TOO — `r.ok && j.ok`, where the
 *     other two look only at `j.ok`. A 500 with an `ok`-less body reads as a
 *     failure there and could read as success on the other two.
 *  6. WHAT HAPPENS AFTER A SUCCESSFUL CREATE. The group and role forms close.
 *     The USER form stays open and switches to edit mode, and the live comment
 *     says why: "A new user has no id until now, so the grant editor had nothing
 *     to attach to. Rather than making them reopen the form, switch it to edit
 *     mode on the returned record and render the editor in place."
 *
 * Splitting the decision out is what lets the principal-forms check drive
 * these and the live functions from one harness and compare — the DOM half needs
 * a browser, and this half needs nothing.
 */

/** A request the form wants to make, or the message it wants to show instead. */
export interface SavePlan {
  /** Set when the form refuses before any request. */
  error?: string;
  // DELETE is here for the grant editor's Remove, which is the one plan with no
  // body. The three FORM planners only ever produce POST or PUT.
  method?: 'POST' | 'PUT' | 'DELETE';
  url?: string;
  body?: Record<string, unknown>;
}

/** What the caller does with the response. */
export interface SaveOutcome {
  /** Shown in the form's error line; empty when the save succeeded. */
  error: string;
  /** Close the form and reload the list. */
  close: boolean;
  /**
   * Keep the form open, switch it to edit mode on the returned record, and
   * render the grant editor in place. Only the USER form ever asks for this, and
   * only after a create.
   */
  switchToEdit: boolean;
  /** Reload the list behind the form. */
  reload: boolean;
}

const FAILED: SaveOutcome = { error: '', close: false, switchToEdit: false, reload: false };

/** A server response, as far as these functions care. */
export interface SaveResponse {
  /** The HTTP-level `response.ok`. Only the GROUP form consults it. */
  httpOk?: boolean;
  /** The parsed body, or null when it did not parse. */
  body?: { ok?: boolean; error?: string; user?: { id?: string } } | null;
}

// ── The user form ──────────────────────────────────────────────────────────

export function userSavePlan(f: { id: string; username: string; password: string }): SavePlan {
  // THE ID IS TRIMMED HERE and not on the other two forms. Reproduced rather
  // than harmonised: it is what decides POST against PUT, and matching the live
  // behaviour keeps that difference where it already is rather than moving it.
  const id = f.id.trim();
  const username = f.username.trim();
  if (!username) return { error: 'Username required' };

  // NO ROLE AND NO allowedRouterIds. The live comment: "access is grants now,
  // edited below." Sending either would trigger the server's legacy projection,
  // which DELETES every grant the principal holds and rebuilds them — so a
  // rename would destroy the access an administrator had just granted in the
  // editor immediately below this form.
  const body: Record<string, unknown> = { username };
  // OMITTED WHEN EMPTY, not sent as "". See the header, rule 4.
  if (f.password) body.password = f.password;

  return id
    ? { method: 'PUT', url: '/api/users/' + id, body }
    : { method: 'POST', url: '/api/users', body };
}

export function userSaveOutcome(hadId: boolean, res: SaveResponse): SaveOutcome {
  const d = res.body;
  // ONLY `d.ok` — the user form does not consult the HTTP status. See rule 5.
  if (!d || !d.ok) {
    return { ...FAILED, error: (d && d.error) || 'Save failed' };
  }
  // A CREATE THAT CAME BACK WITH A RECORD stays open in edit mode. Both
  // conditions matter: without an id there is nothing for the grant editor to
  // attach to, and without `d.user` there is nothing to switch to.
  if (!hadId && d.user) {
    return { error: '', close: false, switchToEdit: true, reload: true };
  }
  return { error: '', close: true, switchToEdit: false, reload: true };
}

// ── The group form ─────────────────────────────────────────────────────────

export function groupSavePlan(f: {
  id: string; name: string; description: string; memberUserIds: string[];
}): SavePlan {
  // NOT TRIMMED, unlike the user form's. See `userSavePlan`.
  const id = f.id;
  const body: Record<string, unknown> = {
    name: f.name.trim(),
    description: f.description.trim(),
    memberUserIds: f.memberUserIds,
  };
  if (!body.name) return { error: 'Name is required' };
  return id
    ? { method: 'PUT', url: '/api/groups/' + encodeURIComponent(id), body }
    : { method: 'POST', url: '/api/groups', body };
}

export function groupSaveOutcome(res: SaveResponse): SaveOutcome {
  const d = res.body;
  // `r.ok && j.ok` — THE ONLY ONE OF THE THREE that checks the HTTP status.
  // Reproduced as the asymmetry it is.
  const ok = res.httpOk !== false && !!(d && d.ok);
  if (!ok) {
    return { ...FAILED, error: (d && d.error) || 'Could not save the group' };
  }
  return { error: '', close: true, switchToEdit: false, reload: true };
}

// ── The role form ──────────────────────────────────────────────────────────

export interface RolePageRow { page: string; access: string }

export function roleSavePlan(f: {
  id: string; name: string; description: string; pages: RolePageRow[];
}): SavePlan {
  const id = f.id;
  const body: Record<string, unknown> = {
    name: f.name.trim(),
    description: f.description.trim(),
    // ALWAYS SENT, even when empty. The server reads an absent `pages` key as
    // "leave the role's matrix alone" and an empty array as "this role now
    // confers nothing" — see `principals.ParseRolePages`. The form always
    // submits the matrix it is showing, so an operator who cleared every page
    // gets an empty array and the revocation lands.
    pages: f.pages,
  };
  if (!body.name) return { error: 'Name is required' };
  return id
    ? { method: 'PUT', url: '/api/roles/' + encodeURIComponent(id), body }
    : { method: 'POST', url: '/api/roles', body };
}

export function roleSaveOutcome(res: SaveResponse): SaveOutcome {
  const d = res.body;
  if (!d || !d.ok) {
    return { ...FAILED, error: (d && d.error) || 'Could not save the role' };
  }
  return { error: '', close: true, switchToEdit: false, reload: true };
}

// ── The group form's member checkboxes ─────────────────────────────────────

/**
 * The member list, from the users the Users card already loaded.
 *
 * NOT a fetch of its own. The live form reads `window._allUsers`, and the reason
 * is the one the Sites card's header gives for `sitesById`: two caches of one
 * thing drift, and the symptom is a group form offering a user the Users table
 * says does not exist.
 *
 * AN EMPTY FLEET GETS A NOTE, not an empty box. A box with nothing in it reads
 * as "loading" or "broken"; the note says which.
 */
export function groupMembersHtml(
  users: { id: string; username: string }[],
  memberIds: string[],
  esc: (s: string) => string,
): string {
  if (!users.length) {
    return '<span style="color:var(--text-muted)">No users yet.</span>';
  }
  return users.map((u) =>
    '<label style="display:flex;align-items:center;gap:.4rem;margin-bottom:.2rem">'
    + '<input type="checkbox" data-member="' + esc(u.id) + '"'
    + (memberIds.indexOf(u.id) !== -1 ? ' checked' : '') + '>'
    + '<span>' + esc(u.username) + '</span></label>').join('');
}

// ── Reading the role matrix back ───────────────────────────────────────────

/**
 * `_collectRolePages`'s decision half.
 *
 * The live comment on it: "Read the matrix back out of the DOM — the segmented
 * control is the state." So there is no model to consult; whichever segment
 * carries `sbtn-primary` IS the answer, and this turns the levels that reading
 * produced into the rows the server wants.
 *
 * ── "none" IS OMITTED, NOT SENT ────────────────────────────────────────────
 *
 * A page set to None produces no row at all, which is what makes the whole
 * matrix a REPLACEMENT: the server takes the list as the complete set, so a page
 * that is absent is a page the role does not confer. Sending `access: 'none'`
 * would be rejected outright — `ParseRolePages` accepts only read and write.
 *
 * A row whose segmented control has NOTHING selected reads as `none` on the live
 * side (`on ? ... : 'none'`) and is therefore dropped. That is not a state the
 * UI can reach, but it is what the code does, so it is what this does.
 */
export function rolePagesFrom(rows: { page: string; level: string }[]): RolePageRow[] {
  const out: RolePageRow[] = [];
  for (const r of rows) {
    if (r.level === 'read' || r.level === 'write') {
      out.push({ page: r.page, access: r.level });
    }
  }
  return out;
}

// ── The grant editor ───────────────────────────────────────────────────────
//
// The editor inside the User and Group forms: a list of grants with a Remove
// button each, plus a role picker, a scope picker and an Add button.
//
// ── THE SCOPE PICKER PACKS TWO VALUES INTO ONE OPTION ──────────────────────
//
// Its options are `global:`, `site:<id>` and `router:<id>`, and the handler does
//
//	var parts = (…value || 'global:').split(':');
//	… scopeType: parts[0], scopeId: parts[1] || ''
//
// `parts[1]`, NOT `parts.slice(1).join(':')` — so a scope id containing a colon
// is TRUNCATED at the first one. Reproduced rather than fixed: ids here are
// generated (`crypto.randomUUID` for a site, the router's own id) and none has
// ever contained a colon, so changing it would be a divergence with no
// observable benefit. The principal-forms check carries the case, which
// is what stops it being quietly "corrected" later.

/** What the Add button sends, or the message it shows instead. */
export function grantAddPlan(
  principalType: string, principalId: string, roleId: string, scopeValue: string,
  unsaved: string,
): SavePlan {
  // NO PRINCIPAL, NO GRANT. A user or group that has not been saved has no id
  // for a grant to name, and the live forms pass this sentence in per form —
  // "Save the user first, then grant them access".
  if (!principalId) return { error: unsaved };

  // `|| 'global:'` — an empty picker means every router, not a malformed scope.
  const parts = (scopeValue || 'global:').split(':');
  return {
    method: 'POST',
    url: '/api/grants',
    body: {
      principalType,
      principalId,
      roleId,
      scopeType: parts[0],
      // See the header: the first segment only, colon and all.
      scopeId: parts[1] || '',
    },
  };
}

/** Removing one grant. */
export function grantDeletePlan(grantId: string): SavePlan {
  return { method: 'DELETE', url: '/api/grants/' + encodeURIComponent(grantId) };
}

/**
 * What either grant request produced.
 *
 * TWO DIFFERENT FALLBACKS, matching the two handlers: "Could not grant access"
 * and "Could not remove access". The server's own message wins when it sent one
 * — and it usually did, because the interesting refusals here are the ones only
 * it can know: "That would leave nobody with administrator access", "No such
 * site", "Invalid role".
 *
 * THE EDITOR REFRESHES EITHER WAY. The live handler calls `refresh()` on both
 * branches, and that is right: a refused Add leaves the list as it was, and a
 * stale list after a failed change is how somebody concludes it worked.
 */
export function grantOutcome(
  res: SaveResponse, fallback: 'add' | 'remove',
): { error: string; refresh: boolean } {
  const d = res.body;
  if (!d || !d.ok) {
    return {
      error: (d && d.error)
        || (fallback === 'add' ? 'Could not grant access' : 'Could not remove access'),
      refresh: true,
    };
  }
  return { error: '', refresh: true };
}

// ── The delete confirmations ───────────────────────────────────────────────
//
// Three different prompts, and the group's carries a reassurance the others do
// not. They are compared exactly, because a confirmation an operator has learned
// to read at a glance is one they stop reading — changing the words is a
// user-visible change even though nothing about the request differs.

export function userDeletePrompt(username: string): string {
  return 'Delete user "' + username + '"? This cannot be undone.';
}

export function groupDeletePrompt(name: string): string {
  return 'Delete group "' + name + '"?\n\nIts members keep any access granted to them directly.';
}

export function roleDeletePrompt(name: string): string {
  return 'Delete the role "' + name + '"?';
}
