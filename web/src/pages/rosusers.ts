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
// ── USERS AND GROUPS ARE RESOURCES ─────────────────────────────────────────
//
// `rosUser` and `rosGroup` are written through the resource engine: a row opens
// the engine's dialog, the Add slot is the engine's, and the lockout guard is
// `selfAccount`, which REFUSES rather than warns. Ending a session is not a row
// write and stays here (`rossession:remove`).
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

import { esc, el, renderSortHeader, sortMul, resRow, type SortCol, type SortState, mutedDash } from '../dom';
import { mountAdds, mountRows } from '../resource';
import type { Socket } from '../socket';
import type { RosUsersPayload } from '../gen/payloads';

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
  // Whether this viewer may write, from the engine's schema answer. Ending a
  // session needs the same page-write permission as editing a user.
  const writable: Record<string, boolean> = {};
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

  /**
   * The padlock cell. It says WHICH of the two reasons applies, because "you
   * cannot edit this" without a why reads as a bug.
   */
  function lockCell(what: string): string {
    return '<span class="muted-note" title="MikroDash signs in to this router with this ' + what +
      '. Editing it here could lock the dashboard out, so it is managed in WinBox.">' +
      '&#128274; in use by MikroDash</span>';
  }

  /**
   * Which TABLE an action belongs to, for the busy key.
   *
   * `.id` values are per-menu: `/user`, `/user/group` and `/user/active` each
   * mint their own `*N` sequence, so `*3` names three unrelated rows. `busy` was
   * a bare id, which meant ending session `*3` also disabled the USER whose id
   * happened to be `*3` -- a row on a different tab, greyed out for no reason
   * the operator could see.
   */
  function tableOf(act: string): string {
    if (act.indexOf('group-') === 0) return 'group';
    if (act.indexOf('session-') === 0) return 'session';
    return 'user';
  }

  function busyKey(act: string, id: string): string { return tableOf(act) + '|' + id; }

  /**
   * What a button says while its write is in flight.
   *
   * THE POINT IS THAT SOMETHING VISIBLY HAPPENS AT THE CURSOR. Before this the
   * only feedback was a `disabled` attribute, and the outcome went to a
   * muted-note span in the card header -- so on a slow router, clicking End
   * Session looked exactly like clicking a dead button. The row is deliberately
   * NOT removed optimistically: it disappears when the router confirms, via the
   * refreshed payload, because a row that vanishes and comes back is worse than
   * one that takes a moment to go.
   */
  const PENDING: Record<string, string> = {
    'session-remove': 'Closing\u2026',
  };

  function btn(act: string, id: string, name: string, label: string, cls?: string): string {
    const pending = busy === busyKey(act, id);
    return '<button class="ru-act' + (cls ? ' ' + cls : '') + '" data-act="' + act +
      '" data-id="' + esc(id) + '" data-name="' + esc(name) + '"' +
      (pending ? ' disabled' : '') + '>' +
      (pending ? (PENDING[act] || label) : label) + '</button>';
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
      return '<tr' + (u.protected ? '' : resRow(u.id, u.name, 'rosUser')) + '>' +
        '<td>' + esc(u.name) + (u.comment ? '<div class="muted-note">' + esc(u.comment) + '</div>' : '') + '</td>' +
        '<td>' + esc(u.group) + '</td>' +
        '<td>' + (u.address ? esc(u.address) : mutedDash()) + '</td>' +
        '<td style="color:var(--text-muted)">' + (u.lastLogin ? esc(u.lastLogin) : mutedDash()) + '</td>' +
        '<td>' + status + '</td>' +
        '<td>' + (u.protected ? lockCell('account') : '') + '</td>' +
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
      return '<tr' + (g.protected ? '' : resRow(g.id, g.name, 'rosGroup')) + '>' +
        '<td>' + esc(g.name) + (g.comment ? '<div class="muted-note">' + esc(g.comment) + '</div>' : '') + '</td>' +
        '<td>' + pol + '</td>' +
        '<td>' + g.members + '</td>' +
        '<td>' + (g.protected ? lockCell('group') : '') + '</td>' +
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
      '<td>' + (x.address ? esc(x.address) : mutedDash()) + '</td>' +
      '<td>' + esc(x.via || '—') + '</td>' +
      '<td>' + esc(x.group || '—') + '</td>' +
      '<td style="color:var(--text-muted)">' + (x.when ? esc(x.when) : mutedDash()) + '</td>' +
      '<td>' + (x.protected ? lockCell('session')
        : !writable.rosUser ? ''
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

    const slot = el('ruAddSlot');
    if (slot) slot.style.display = tab === 'sessions' ? 'none' : '';
    const note = el('ruActionNote');
    // Never clears a message it did not write — see setStatus.
    if (note && !note.dataset.status) {
      note.textContent = writable.rosUser ? '' : 'read-only — you do not have write access to this router';
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

  // ── Ending a session ──────────────────────────────────────────────────────

  document.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('.ru-act');
    if (!b) return;
    const act = b.getAttribute('data-act') || '';
    const id = b.getAttribute('data-id') || '';
    const name = b.getAttribute('data-name') || '';
    if (act !== 'session-remove') return;
    if (!window.confirm('End "' + name +
        '"\u2019s session?\n\nThey will be disconnected from the router immediately.')) return;
    busy = busyKey(act, id);
    render();
    socket.emit('rossession:remove', { id, expectedName: name });
  });

  /** Point the Add slot at the table now on screen. */
  function syncAddSlot(): void {
    const slot = el('ruAddSlot');
    if (!slot || tab === 'sessions') return;
    slot.setAttribute('data-res-add', tab === 'groups' ? 'rosGroup' : 'rosUser');
    document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
  }

  mountAdds(socket);
  mountRows(socket);

  socket.on('res:schema', (d) => {
    if (!d || (d.key !== 'rosUser' && d.key !== 'rosGroup')) return;
    writable[d.key] = !!d.permitted;
    if (isVisible('users')) render();
  });

  socket.on('rosusers:ok', (d) => {
    busy = '';
    const what: Record<string, string> = {
      'session-remove': 'Ended the session for ',
    };
    // NO `render()` here, and that is the original's. It is the only reason this
    // message survives at all — until the next payload, which the write itself
    // asked for.
    setStatus(((d && d.action && what[d.action]) || 'Done: ') + ((d && d.name) || ''));
  });

  socket.on('rosusers:error', (d) => {
    busy = '';
    const code = d && d.code;
    const msg: Record<string, string> = {
      denied: 'You do not have write access to this router',
      unavailable: 'Router user collection is not running for this router',
      'bad-request': 'Invalid request',
      'stale-row': 'That row changed on the router \u2014 the page has been refreshed',
      'protected-account': 'That is the account MikroDash signs in with \u2014 manage it in WinBox',
      'self-unresolved':
        'MikroDash cannot identify its own account on this router, so changes are refused',
      'router-write-policy': 'The RouterOS user needs the "policy" permission for this',
      unsupported: 'This router does not support that command',
      // MEASURED on RouterOS 7.24 and 7.24.1, 2026-09-01: `/user/active/remove`
      // answers `action failed (6)` for BOTH `via=rest-api` and `via=api` rows,
      // issued by a full-group user, with `numbers=[find ...]` as well as a bare
      // id -- so it is a refusal by the router rather than a syntax mistake or a
      // permission gap. Such rows can then sit in /user/active for weeks, which
      // is what made this look like a dead button rather than a refusal.
      //
      // NOT claimed here: that every session type is refused. winbox and ssh
      // were not tested, because the only ones available were the operator's own
      // and this session's. The wording says what was seen and no more.
      'write-failed': 'The router refused to end that session ("action failed"). ' +
        'RouterOS keeps some session types — API and REST API sessions among them — ' +
        'and they have to be cleared from the router itself.',
    };
    const text = (code && msg[code]) || (d && d.message) || 'Action failed';
    setStatus(text);
    if (isVisible('users')) render();
  });

  socket.on('rosusers:update', (d) => {
    if (!d) return;
    data = d;
    busy = '';
    // The summary updates whether or not the page is showing; the tables only
    // when it is. The asymmetry is the original's.
    renderSummary();
    if (isVisible('users')) render();
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
      syncAddSlot();
      render();
    });
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'users' && data) render();
  });
}
