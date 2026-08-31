// The Router Users page — a port of the Router Users IIFE in public/app.js.
//
// RouterOS accounts, not MikroDash accounts. The two are unrelated, which is why
// this page is called Router Users and lives outside Settings.
//
// Three tabs over one card: who may log in, what each group may do, and who is
// logged in right now.
//
// ── THE PADLOCK IS A COURTESY ────────────────────────────────────────────────
//
// MikroDash signs into this router as one of these users, so editing that
// account or its group is how somebody locks the dashboard out of the device it
// manages. Those rows show a padlock and no buttons.
//
// That is ALL it is. Every refusal is enforced server-side by the lockout guard,
// which re-reads from the router rather than trusting anything on this page —
// because a page can be stale, and a request can be crafted.
//
// ── THE STATUS LINE ─────────────────────────────────────────────────────────
//
// `setStatus` writes `ruActionNote`, and so does `render()` — which runs again
// on the next payload, and the server calls RefreshNow after every write. The
// `dataset.status` mark is what stops the second from erasing the first.
//
// That mark arrived the long way round, and the route is the point. This closure
// had no `setStatus` at all until v0.7.33 — a ReferenceError on every write —
// which this port found and deliberately reproduced. PR #112 defined it; the
// port then measured that the message STILL never reached the operator, because
// render() wiped it in the same tick on a failure and one round trip later on a
// success, and reported that as ToDo item 5. `7e5ac8e` fixed it on all three
// pages, and this follows. Reproduce, report, follow — three rounds of it.

import { esc, el, renderSortHeader, sortMul, type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';

export interface RosUser {
  id: string; name: string; group: string; address: string; comment: string;
  disabled: boolean; expired: boolean; lastLogin: string;
  inactivityTimeout: string; inactivityPolicy: string; protected: boolean;
}

export interface RosGroup {
  id: string; name: string; granted: string[]; denied: string[];
  skin: string; comment: string; protected: boolean; members: number;
}

export interface RosSession {
  id: string; name: string; address: string; via: string; group: string;
  when: string; radius: boolean; protected: boolean;
}

export interface RosSelf {
  names: string[]; groups: string[]; resolved: boolean; source: string | null;
}

export interface RosUsersPayload {
  ts: number; pollMs: number;
  users: RosUser[]; groups: RosGroup[]; sessions: RosSession[];
  self: RosSelf;
  passwordPolicy: { minLength: number; minCategories: number };
  policies: string[];
  available: boolean; denied: boolean;
}

export interface RosUsersCaps { permitted: boolean; routerName: string }

// A KEYLESS COLUMN IS NOT SORTABLE — see renderSortHeader. The action column is
// the only one here that must never be.
const USER_COLS: SortCol[] = [
  { key: 'name', label: 'User' }, { key: 'group', label: 'Group' },
  { key: 'address', label: 'Allowed From' }, { key: 'lastLogin', label: 'Last Login' },
  { key: 'disabled', label: 'Status' }, { key: '', label: '' },
];
const GROUP_COLS: SortCol[] = [
  { key: 'name', label: 'Group' }, { key: 'granted', label: 'Permissions' },
  { key: 'members', label: 'Users' }, { key: '', label: '' },
];
const SESS_COLS: SortCol[] = [
  { key: 'name', label: 'User' }, { key: 'address', label: 'From' },
  { key: 'via', label: 'Via' }, { key: 'group', label: 'Group' },
  { key: 'when', label: 'Since' }, { key: '', label: '' },
];

export function initRosUsersPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const userTbEl = el('ruUserTable');
  const groupTbEl = el('ruGroupTable');
  const sessTbEl = el('ruSessionTable');
  // Bails on a page that is not in the document, exactly as the live IIFE does.
  if (!userTbEl || !groupTbEl || !sessTbEl) return;
  // Re-bound so the narrowing survives into the closures below.
  const userTb: HTMLElement = userTbEl;
  const groupTb: HTMLElement = groupTbEl;
  const sessTb: HTMLElement = sessTbEl;

  let data: RosUsersPayload | null = null;
  let caps: RosUsersCaps = { permitted: false, routerName: '' };
  let tab = 'users';
  // The id of the row with an action in flight. Cleared by the next payload or
  // by any answer from the server, so a failed action never leaves a button
  // disabled for longer than one round trip.
  let busy = '';

  // Sessions default to newest first; the other two to name, which is how an
  // operator looks for an account they already have in mind.
  const sort: Record<string, SortState> = {
    users: { col: 'name', dir: 'asc' },
    groups: { col: 'name', dir: 'asc' },
    sessions: { col: 'when', dir: 'desc' },
  };

  /**
   * One comparator for three tables of different shapes.
   *
   * An ARRAY sorts by its length — that is the Permissions column, where the
   * useful order is "how many" rather than any alphabetical reading of the first
   * element. A BOOLEAN sorts false before true. Everything else falls through to
   * localeCompare on the string form, so a null and an empty string land
   * together.
   */
  function sortRows<T>(rows: T[], st: SortState): T[] {
    const mul = sortMul(st);
    return rows.slice().sort((a, b) => {
      let x = (a as Record<string, unknown>)[st.col];
      let y = (b as Record<string, unknown>)[st.col];
      if (Array.isArray(x)) { x = x.length; y = (y as unknown[]).length; }
      if (typeof x === 'boolean') { x = x ? 1 : 0; y = y ? 1 : 0; }
      if (typeof x === 'number' && typeof y === 'number') return (x - y) * mul;
      return String(x ?? '').localeCompare(String(y ?? '')) * mul;
    });
  }

  // NO LOCAL SORT TOGGLE. The original carries its own `onSort(which)` because
  // its `_renderSortHeader` hands the clicked key back; the shared helper here
  // already does the "same column toggles direction, a new one selects it"
  // step against the SortState it was given, and calls back with nothing. The
  // behaviour is identical and the toggle has one home — which is what the
  // other eight ported pages already rely on.

  function q(): string {
    const e = el<HTMLInputElement>('ruSearch');
    return ((e && e.value) || '').toLowerCase().trim();
  }
  function dash(): string { return '<span style="color:var(--text-muted)">&mdash;</span>'; }

  /**
   * The padlock cell. It says WHICH of the two reasons applies, because "you
   * cannot edit this" without a why reads as a bug.
   */
  function lockCell(what: string): string {
    return '<span class="muted-note" title="MikroDash signs in to this router with this ' + what +
      '. Editing it here could lock the dashboard out, so it is managed in WinBox.">' +
      '&#128274; in use by MikroDash</span>';
  }

  function btn(act: string, id: string, name: string, label: string, cls?: string): string {
    return '<button class="ru-act' + (cls ? ' ' + cls : '') + '" data-act="' + act +
      '" data-id="' + esc(id) + '" data-name="' + esc(name) + '"' +
      (busy === id ? ' disabled' : '') + '>' + label + '</button>';
  }

  function renderUsers(): void {
    const term = q();
    const all = (data && data.users) || [];
    const rows = sortRows(all.filter((u) =>
      !term || (u.name + ' ' + u.group + ' ' + u.comment).toLowerCase().indexOf(term) !== -1),
    sort.users as SortState);

    renderSortHeader('ruUserThead', USER_COLS, sort.users as SortState, () => render());
    const badge = el('ruUserBadge');
    if (badge) badge.textContent = String(all.length);

    userTb.innerHTML = rows.length ? rows.map((u) => {
      const status = u.disabled ? '<span class="wl-band wl-band-24">disabled</span>'
        : u.expired ? '<span style="color:var(--text-muted)">expired</span>'
        : '<span class="wl-band wl-band-6">enabled</span>';
      return '<tr>' +
        '<td>' + esc(u.name) + (u.comment ? '<div class="muted-note">' + esc(u.comment) + '</div>' : '') + '</td>' +
        '<td>' + esc(u.group) + '</td>' +
        '<td>' + (u.address ? esc(u.address) : dash()) + '</td>' +
        '<td style="color:var(--text-muted)">' + (u.lastLogin ? esc(u.lastLogin) : dash()) + '</td>' +
        '<td>' + status + '</td>' +
        '<td>' + (u.protected ? lockCell('account')
          : !caps.permitted ? ''
          : btn('user-edit', u.id, u.name, 'Edit') + ' ' +
            btn('user-toggle', u.id, u.name, u.disabled ? 'Enable' : 'Disable') + ' ' +
            btn('user-remove', u.id, u.name, 'Remove', 'danger')) + '</td>' +
      '</tr>';
    }).join('') : '<tr><td colspan="6" class="empty-state">' +
      (term ? 'No users match that search.' : 'Waiting for user data&hellip;') + '</td></tr>';
  }

  function renderGroups(): void {
    const term = q();
    const all = (data && data.groups) || [];
    const rows = sortRows(all.filter((g) =>
      !term || (g.name + ' ' + g.granted.join(' ')).toLowerCase().indexOf(term) !== -1),
    sort.groups as SortState);

    renderSortHeader('ruGroupThead', GROUP_COLS, sort.groups as SortState, () => render());
    const badge = el('ruGroupBadge');
    if (badge) badge.textContent = String(all.length);

    groupTb.innerHTML = rows.length ? rows.map((g) => {
      // ONLY WHAT IS GRANTED. The denied half is every other policy, and listing
      // seventeen names per row would bury the four that matter.
      const pol = g.granted.length
        ? g.granted.map((p) => '<span class="wl-band wl-band-5" style="margin:0 .15rem .15rem 0">' + esc(p) + '</span>').join('')
        : '<span class="muted-note">no permissions</span>';
      return '<tr>' +
        '<td>' + esc(g.name) + (g.comment ? '<div class="muted-note">' + esc(g.comment) + '</div>' : '') + '</td>' +
        '<td>' + pol + '</td>' +
        '<td>' + g.members + '</td>' +
        '<td>' + (g.protected ? lockCell('group')
          : !caps.permitted ? ''
          : btn('group-edit', g.id, g.name, 'Edit') + ' ' +
            btn('group-remove', g.id, g.name, 'Remove', 'danger')) + '</td>' +
      '</tr>';
    }).join('') : '<tr><td colspan="4" class="empty-state">' +
      (term ? 'No groups match that search.' : 'Waiting for group data&hellip;') + '</td></tr>';
  }

  function renderSessions(): void {
    const term = q();
    const all = (data && data.sessions) || [];
    const rows = sortRows(all.filter((x) =>
      !term || (x.name + ' ' + x.address + ' ' + x.via).toLowerCase().indexOf(term) !== -1),
    sort.sessions as SortState);

    renderSortHeader('ruSessionThead', SESS_COLS, sort.sessions as SortState, () => render());
    const badge = el('ruSessionBadge');
    if (badge) badge.textContent = String(all.length);

    sessTb.innerHTML = rows.length ? rows.map((x) =>
      '<tr>' +
      '<td>' + esc(x.name) + '</td>' +
      '<td>' + (x.address ? esc(x.address) : dash()) + '</td>' +
      '<td>' + esc(x.via || '—') + '</td>' +
      '<td>' + esc(x.group || '—') + '</td>' +
      '<td style="color:var(--text-muted)">' + (x.when ? esc(x.when) : dash()) + '</td>' +
      '<td>' + (x.protected ? lockCell('session')
        : !caps.permitted ? ''
        : btn('session-remove', x.id, x.name, 'End Session', 'danger')) + '</td>' +
      '</tr>').join('') : '<tr><td colspan="6" class="empty-state">' +
      (term ? 'No sessions match that search.' : 'Nobody is logged in.') + '</td></tr>';
  }

  /**
   * Two different notices, because the two situations have different fixes.
   *
   * `denied` is the common one: the recommended monitoring group denies `policy`
   * and RouterOS gates /user behind it, so the page shows the exact command
   * rather than an empty table. `!self.resolved` is the fail-closed case — every
   * change will be refused, and saying so once beats showing buttons that get
   * refused one at a time.
   */
  function renderNotice(): void {
    const card = el('ruNoticeCard');
    const body = el('ruNotice');
    if (!card || !body) return;
    let msg = '';
    if (data && data.denied) {
      msg = 'This router\'s MikroDash account cannot read <code>/user</code>. RouterOS requires the ' +
        '<code>policy</code> permission for user management. To enable this page for this router: ' +
        '<code>/user group set [find name=&lt;group&gt;] policy=read,write,policy,api,test</code>';
    } else if (data && data.self && !data.self.resolved) {
      msg = 'MikroDash cannot identify its own account on this router, so every change here is refused. ' +
        'This is expected when the dashboard authenticates through RADIUS.';
    }
    card.style.display = msg ? '' : 'none';
    body.innerHTML = msg;
  }

  function renderSummary(): void {
    const d = data;
    const set = (id: string, v: string): void => { const e = el(id); if (e) e.textContent = v; };
    // `|| '—'` means ZERO renders as a dash, not as "0". That is the live
    // behaviour: a router with no sessions reads as "nothing to say" rather than
    // as a measured zero.
    set('ruSumUsers', String(((d && d.users) || []).length || '—'));
    set('ruSumGroups', String(((d && d.groups) || []).length || '—'));
    set('ruSumSessions', String(((d && d.sessions) || []).length || '—'));
    set('ruSumSelf', (d && d.self && d.self.names && d.self.names[0]) || '—');
  }

  function render(): void {
    // ALL THREE TABLES RENDER EVERY TIME, whichever tab is showing: the other
    // two still carry counts in their badges. The original writes it as "the
    // active one first, then the others", which comes to the same thing and is
    // kept so the ORDER of DOM writes matches.
    if (tab === 'users') renderUsers();
    else if (tab === 'groups') renderGroups();
    else renderSessions();
    if (tab !== 'users') renderUsers();
    if (tab !== 'groups') renderGroups();
    if (tab !== 'sessions') renderSessions();

    const add = el('ruAddBtn');
    if (add) {
      add.textContent = tab === 'groups' ? '+ Add Group' : '+ Add User';
      add.style.display = (caps.permitted && tab !== 'sessions') ? '' : 'none';
    }
    const note = el('ruActionNote');
    // Never clears a message it did not write — see setStatus.
    if (note && !note.dataset.status) {
      note.textContent = caps.permitted ? '' : 'read-only — you do not have write access to this router';
    }
    renderNotice();
    renderSummary();
  }

  // The status line. Eight seconds, then it clears itself — the live helper,
  // shared verbatim by WAN, Queues, Packages and this page. See the header for
  // why what it writes rarely survives to be read.
  let statusTimer: ReturnType<typeof setTimeout> | null = null;
  function setStatus(text: string): void {
    const e = el('ruActionNote');
    if (!e) return;
    e.textContent = text || '';
    // Marked while a message is showing. render() writes this same element from
    // caps.permitted and runs again on the next payload — the server calls
    // RefreshNow after every write — so unmarked it erased the message in the
    // same tick on a failure, and one round trip later on a success. The 8 s
    // timer never got to expire and the operator saw nothing either way.
    if (text) e.dataset.status = '1'; else delete e.dataset.status;
    if (statusTimer) clearTimeout(statusTimer);
    if (text) {
      statusTimer = setTimeout(() => { e.textContent = ''; delete e.dataset.status; }, 8000);
    }
  }

  // ── Dialogs ───────────────────────────────────────────────────────────────

  function formError(id: string, msg: string): void {
    const e = el(id);
    if (!e) return;
    e.textContent = msg;
    e.style.display = '';
  }

  function setVal(id: string, v: string): void {
    const e = el<HTMLInputElement>(id);
    if (e) e.value = v;
  }

  function openUserForm(u: RosUser | null): void {
    const title = el('ruf_title');
    // 'Device User', not 'Router User': #117's rename reached this dialog too.
    if (title) title.textContent = u ? 'Edit Device User' : 'Add Device User';
    setVal('ruf_id', u ? u.id : '');
    setVal('ruf_expectedName', u ? u.name : '');
    setVal('ruf_name', u ? u.name : '');
    setVal('ruf_address', u ? u.address : '');
    setVal('ruf_comment', u ? u.comment : '');
    const dis = el<HTMLInputElement>('ruf_disabled');
    if (dis) dis.checked = u ? !!u.disabled : false;
    setVal('ruf_password', '');

    const pol = (data && data.passwordPolicy) || { minLength: 0, minCategories: 0 };
    const hint = el('ruf_passHint');
    if (hint) {
      hint.textContent = u ? '(leave blank to keep)'
        : (pol.minLength ? '(at least ' + pol.minLength + ' characters)' : '(required)');
    }

    // Groups the guard would refuse are not offered. The server refuses them
    // anyway; leaving them in the list only invites the refusal.
    const sel = el<HTMLSelectElement>('ruf_group');
    if (sel) {
      sel.innerHTML = ((data && data.groups) || []).filter((g) => !g.protected)
        .map((g) => '<option value="' + esc(g.name) + '"' +
          (u && u.group === g.name ? ' selected' : '') + '>' + esc(g.name) + '</option>').join('');
    }
    const err = el('ruf_error');
    if (err) err.style.display = 'none';
    el('ruUserFormWrap')?.classList.add('open');
  }

  function openGroupForm(g: RosGroup | null): void {
    const title = el('rgf_title');
    if (title) title.textContent = g ? 'Edit Group' : 'Add Group';
    setVal('rgf_id', g ? g.id : '');
    setVal('rgf_expectedName', g ? g.name : '');
    setVal('rgf_name', g ? g.name : '');
    setVal('rgf_comment', g ? g.comment : '');

    const granted = g ? g.granted : [];
    // From the PAYLOAD, so a RouterOS that grows an eighteenth policy shows it
    // here without a frontend release.
    const box = el('rgf_policies');
    if (box) {
      box.innerHTML = ((data && data.policies) || []).map((pName) =>
        '<label style="display:flex;align-items:center;gap:.35rem;cursor:pointer">' +
        '<input type="checkbox" class="rgf-pol" value="' + esc(pName) + '"' +
        (granted.indexOf(pName) !== -1 ? ' checked' : '') + '>' + esc(pName) + '</label>').join('');
    }
    const err = el('rgf_error');
    if (err) err.style.display = 'none';
    el('ruGroupFormWrap')?.classList.add('open');
  }

  // ── Actions ───────────────────────────────────────────────────────────────

  document.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('.ru-act');
    if (!b) return;
    const act = b.getAttribute('data-act') || '';
    const id = b.getAttribute('data-id') || '';
    const name = b.getAttribute('data-name') || '';

    if (act === 'user-edit') {
      openUserForm(((data && data.users) || []).find((u) => u.id === id) || null);
      return;
    }
    if (act === 'group-edit') {
      openGroupForm(((data && data.groups) || []).find((g) => g.id === id) || null);
      return;
    }
    if (act === 'user-toggle') {
      const u = ((data && data.users) || []).find((x) => x.id === id);
      if (!u) return;
      busy = id;
      render();
      // A FULL SAVE, not a disable-only action: one write path is one place for
      // the guard to be called from.
      socket.emit('rosuser:save', {
        id: u.id, expectedName: u.name, name: u.name, group: u.group,
        address: u.address, comment: u.comment, disabled: !u.disabled,
      });
      return;
    }

    const prompts: Record<string, string> = {
      'user-remove': 'Remove the router user "' + name +
        '"?\n\nThey will no longer be able to log in to this router.',
      'group-remove': 'Remove the group "' + name +
        '"?\n\nRouterOS refuses this if any user is still in it.',
      'session-remove': 'End "' + name +
        '"\u2019s session?\n\nThey will be disconnected from the router immediately.',
    };
    if (!prompts[act]) return;
    if (!window.confirm(prompts[act])) return;
    busy = id;
    render();
    const ev = act === 'user-remove' ? 'rosuser:remove'
      : act === 'group-remove' ? 'rosgroup:remove' : 'rossession:remove';
    socket.emit(ev, { id, expectedName: name });
  });

  el('ruAddBtn')?.addEventListener('click', () => {
    if (!caps.permitted) return;
    if (tab === 'groups') openGroupForm(null); else openUserForm(null);
  });

  el('ruf_save')?.addEventListener('click', () => {
    const name = (el<HTMLInputElement>('ruf_name')?.value || '').trim();
    const group = el<HTMLSelectElement>('ruf_group')?.value || '';
    if (!name) return formError('ruf_error', 'A username is required');
    if (!group) return formError('ruf_error', 'Pick a group');
    const id = el<HTMLInputElement>('ruf_id')?.value || '';
    busy = id;
    // `undefined` rather than '' for the three optional fields: the server reads
    // `id` to decide create-or-edit and `password` to decide whether one was
    // set, and JSON.stringify drops an undefined key entirely — which is what
    // the live app's `|| undefined` achieves and what the Go side expects.
    socket.emit('rosuser:save', {
      id: id || undefined,
      expectedName: el<HTMLInputElement>('ruf_expectedName')?.value || undefined,
      name, group,
      address: (el<HTMLInputElement>('ruf_address')?.value || '').trim(),
      comment: (el<HTMLInputElement>('ruf_comment')?.value || '').trim(),
      disabled: !!el<HTMLInputElement>('ruf_disabled')?.checked,
      password: el<HTMLInputElement>('ruf_password')?.value || undefined,
    });
  });

  el('rgf_save')?.addEventListener('click', () => {
    const name = (el<HTMLInputElement>('rgf_name')?.value || '').trim();
    if (!name) return formError('rgf_error', 'A group name is required');
    const policy = Array.from(
      document.querySelectorAll<HTMLInputElement>('#rgf_policies .rgf-pol:checked'),
      (c) => c.value);
    const id = el<HTMLInputElement>('rgf_id')?.value || '';
    busy = id;
    socket.emit('rosgroup:save', {
      id: id || undefined,
      expectedName: el<HTMLInputElement>('rgf_expectedName')?.value || undefined,
      name,
      comment: (el<HTMLInputElement>('rgf_comment')?.value || '').trim(),
      policy,
    });
  });

  socket.on('rosusers:ok', (d: { action?: string; name?: string }) => {
    busy = '';
    el('ruUserFormWrap')?.classList.remove('open');
    el('ruGroupFormWrap')?.classList.remove('open');
    const what: Record<string, string> = {
      create: 'Created ', update: 'Updated ', delete: 'Removed ',
      'group-create': 'Created group ', 'group-update': 'Updated group ',
      'group-delete': 'Removed group ',
      'session-remove': 'Ended the session for ',
    };
    // NO `render()` here, and that is the original's. It is the only reason this
    // message survives at all — until the next payload, which the write itself
    // asked for.
    setStatus(((d && d.action && what[d.action]) || 'Done: ') + ((d && d.name) || ''));
  });

  socket.on('rosusers:error', (d: { code?: string; message?: string; minLength?: number }) => {
    busy = '';
    const code = d && d.code;
    const msg: Record<string, string> = {
      denied: 'You do not have write access to this router',
      unavailable: 'Router user collection is not running for this router',
      'bad-request': 'Invalid request',
      'stale-row': 'That row changed on the router \u2014 the page has been refreshed',
      'no-such-group': 'That group no longer exists on the router',
      'group-in-use': 'That group still has users in it \u2014 move them first',
      'weak-password': 'The router requires a password of at least ' +
        ((d && d.minLength) || 8) + ' characters',
      'protected-account': 'That is the account MikroDash signs in with \u2014 manage it in WinBox',
      'protected-group': 'That is the group MikroDash signs in with \u2014 manage it in WinBox',
      'protected-name-value': 'That name belongs to the account MikroDash signs in with',
      'protected-group-value': 'Users cannot be placed in the group MikroDash signs in with',
      'self-unresolved':
        'MikroDash cannot identify its own account on this router, so changes are refused',
      'router-write-policy': 'The RouterOS user needs the "policy" permission for this',
      unsupported: 'This router does not support that command',
    };
    const text = (code && msg[code]) || (d && d.message) || 'Action failed';
    // A refusal belongs in the dialog that caused it; everything else is a row
    // action and belongs in the status line. The `render()` below then wipes
    // that status line — see the header. Order preserved from the original,
    // because the order IS the behaviour.
    const open = el('ruUserFormWrap')?.classList.contains('open') ? 'ruf_error'
      : el('ruGroupFormWrap')?.classList.contains('open') ? 'rgf_error' : '';
    if (open) formError(open, text); else setStatus(text);
    if (isVisible('rosusers')) render();
  });

  socket.on('rosusers:update', (d: RosUsersPayload) => {
    if (!d) return;
    data = d;
    busy = '';
    // The summary updates whether or not the page is showing; the tables only
    // when it is. The asymmetry is the original's.
    renderSummary();
    if (isVisible('rosusers')) render();
  });

  socket.on('rosusers:caps', (d: RosUsersCaps) => {
    if (!d) return;
    caps = d;
    if (isVisible('rosusers')) render();
  });

  const search = el<HTMLInputElement>('ruSearch');
  search?.addEventListener('input', () => render());

  // ── THE TABS WERE DEAD ────────────────────────────────────────────────────
  //
  // This selected `[data-ru-tab]` and switched panes called `ruUsersPane` and
  // friends. The extracted markup carries NEITHER: the buttons are
  // `#ruTabBar .stab` with `data-rutab`, and the panes are `#rosusersCard
  // .brtab-panel` with ids `rutab-users` / `rutab-groups` / `rutab-sessions`.
  // Both selectors matched nothing, so no listener was ever attached and the
  // Groups and Sessions tabs could not be reached at all.
  //
  // The same mistake is recorded a few hundred lines away in `firewall.ts`
  // ("This selected `[data-fwtab]` — an attribute that appears nowhere in the
  // markup"). It was fixed there and repeated here, which is why the fix now
  // matches the LIVE selectors rather than a plausible-looking name.
  //
  // Panes are switched by an `active` CLASS, not by `style.display`, which is
  // what the stylesheet expects.
  const tabBtns = () => document.querySelectorAll('#ruTabBar .stab');
  tabBtns().forEach((b) => {
    b.addEventListener('click', () => {
      tab = b.getAttribute('data-rutab') || 'users';
      tabBtns().forEach((o) => {
        const on = o === b;
        o.classList.toggle('active', on);
        o.setAttribute('aria-selected', on ? 'true' : 'false');
      });
      document.querySelectorAll('#rosusersCard .brtab-panel').forEach((pnl) => {
        pnl.classList.toggle('active', (pnl as HTMLElement).id === 'rutab-' + tab);
      });
      render();
    });
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'rosusers' && data) render();
  });
}
