/**
 * The Credentials tab: RouterOS accounts MikroDash provisions across the fleet.
 *
 * ── WHAT THIS IS NOT ─────────────────────────────────────────────────────────
 *
 * It is not where MikroDash's own login is kept. A profile creates accounts for
 * PEOPLE; MikroDash signs in as itself throughout, and the server refuses per
 * router - `internal/guard/selfguard.go` - any write that could disturb that.
 * The copy on the page says so, because an operator who believes otherwise will
 * eventually try to use a profile to change MikroDash's own password.
 *
 * ── THE PASSWORD IS WRITE-ONLY ───────────────────────────────────────────────
 *
 * The server sends `hasSecret` and never the value, sealed or otherwise. So the
 * edit form's password box is EMPTY on open and blank means "leave it alone" -
 * the same contract `resource.TypeSecret` has. The help text has to say so,
 * because a box that looks empty and is not is how somebody clears a password
 * by accident.
 */

import { el, esc } from '../dom';

/** One profile, as GET /api/credentials/profiles lists it. */
export interface CredProfile {
  id: string;
  name: string;
  description: string;
  username: string;
  permKind: 'builtin' | 'custom';
  builtinGroup: string;
  groupName: string;
  policies: string[];
  hasSecret: boolean;
  links: number;
  revision: number;
}

/** One profile's standing on one router. */
export interface CredLink {
  profileId: string;
  routerId: string;
  state: string;
  code: string;
  error: string;
  appliedRevision: number;
  attempts: number;
}

/**
 * The state pills.
 *
 * ── DECLARED BY KIND, NEVER BY COLOUR ────────────────────────────────────────
 *
 * CLAUDE.md's rule for the generated pages, kept by hand here because this is a
 * hand-built panel. `refused` and `conflict` are AMBER rather than red: neither
 * is a fault, both are MikroDash declining to do something, and a red row for
 * "the guard protected your login" reads as a bug report.
 */
const STATE_KIND: Record<string, string> = {
  applied: 'ok',
  pending: 'wait',
  applying: 'wait',
  removing: 'wait',
  refused: 'warn',
  conflict: 'warn',
  orphaned: 'warn',
  failed: 'bad',
  unreachable: 'bad',
  unknown: 'bad',
};

/** What each state means, in the operator's terms rather than the code's. */
const STATE_TITLE: Record<string, string> = {
  applied: 'The account is on this router, in the right group.',
  pending: 'Queued. It will be applied the next time the router answers.',
  applying: 'Being applied now.',
  removing: 'Queued for removal. The account comes off when the router answers.',
  refused: 'MikroDash declined this write to protect its own login. It will not be retried on '
    + 'its own - fix the profile, then use Retry.',
  conflict: 'An account with that name is already on the router and MikroDash did not put it '
    + 'there. Nothing was changed.',
  orphaned: 'The account could not be taken off. The link is kept so it is not forgotten.',
  failed: 'The router refused the write.',
  unreachable: 'The router did not answer. It will be retried.',
  unknown: 'The write was accepted but could not be confirmed. It will be re-checked.',
};

export function statePill(state: string): string {
  const kind = STATE_KIND[state] ?? 'bad';
  const title = STATE_TITLE[state] ?? '';
  return `<span class="cp-pill cp-${kind}" title="${esc(title)}">${esc(state)}</span>`;
}

/**
 * How a profile's permissions read in the table.
 *
 * A custom set names its policies rather than saying "custom", because "custom"
 * tells an operator nothing about what the account can do - which is the one
 * question the column exists to answer.
 */
export function permissionText(p: CredProfile): string {
  if (p.permKind === 'builtin') return p.builtinGroup;
  if (!p.policies.length) return `${p.groupName} (no permissions)`;
  return `${p.groupName}: ${p.policies.join(', ')}`;
}

/**
 * The worst state among a profile's links, which is what the row shows.
 *
 * ── THE WORST, NOT THE COMMONEST ─────────────────────────────────────────────
 *
 * Nineteen routers applied and one refused is a row that must say `refused`. A
 * majority reading would show `applied`, and the one device that needs
 * attention would be invisible until somebody opened the profile.
 */
const STATE_RANK = ['applied', 'pending', 'applying', 'removing', 'unreachable',
  'unknown', 'failed', 'orphaned', 'conflict', 'refused'];

export function worstState(links: readonly CredLink[]): string {
  let worst = '';
  let rank = -1;
  for (const l of links) {
    const r = STATE_RANK.indexOf(l.state);
    if (r > rank) {
      rank = r;
      worst = l.state;
    }
  }
  return worst;
}

/** One table row. */
export function profileRow(p: CredProfile, links: readonly CredLink[]): string {
  const mine = links.filter((l) => l.profileId === p.id);
  const state = worstState(mine);
  // THE ROW CARRIES NO ID ATTRIBUTE. The buttons carry it; one on the row as
  // well would be decoration, and TestRenderedAttributesAreRead refuses an
  // attribute nothing reads - it is indistinguishable from a handler that was
  // deleted. (Written without the attribute's own spelling: that check does not
  // strip comments, so naming it here would make this comment fail it. The
  // traps skill calls this out - "write the example so the checker does not
  // recognise it".)
  return '<tr>'
    + `<td><strong>${esc(p.name)}</strong>`
    + (p.description ? `<div class="cfg-meta">${esc(p.description)}</div>` : '')
    + '</td>'
    + `<td><code>${esc(p.username)}</code></td>`
    + `<td>${esc(permissionText(p))}</td>`
    + `<td>${p.links}</td>`
    + `<td>${state ? statePill(state) : '<span class="cfg-meta">not linked</span>'}</td>`
    + '<td class="text-end">'
    + `<button class="cfg-btn" type="button" data-cp-edit="${esc(p.id)}">Edit</button> `
    + `<button class="cfg-btn" type="button" data-cp-links="${esc(p.id)}">Routers</button>`
    + '</td></tr>';
}

/** Draws the whole table, or the empty state. */
export function drawProfiles(profiles: readonly CredProfile[],
  links: readonly CredLink[]): void {
  const body = el('cpBody');
  const empty = el('cpEmpty');
  if (!body || !empty) return;
  body.innerHTML = profiles.map((p) => profileRow(p, links)).join('');
  empty.hidden = profiles.length > 0;
}
