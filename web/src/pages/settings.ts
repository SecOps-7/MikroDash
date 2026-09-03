/**
 * The Settings page — the read half.
 *
 * ── ONLY THE READ HALF, AND THE REASON IS THE OTHER PROCESS ────────────────
 *
 * `POST /api/settings` is not wired and will not be until cutover: the live
 * `settings.js` caches the whole settings object at first load and never
 * re-reads the file, so a write from this side is invisible to the running Node
 * app AND is silently reverted by its next save. See internal/server/settings_api.go.
 *
 * So this fills the form and nothing else. The form is the part that has to be
 * right on day one anyway — a field populated from the wrong default is a
 * setting the operator "changes" to the value it already had, or worse, saves
 * away without noticing.
 *
 * ── THE FIELD TABLE IS GENERATED ───────────────────────────────────────────
 *
 * `web/src/gen/settings-form-map.ts` comes from the live `populate()` via
 * The settings-form-map tool. ~100 inputs, and three defaults for an absent
 * value that look identical on screen when the setting IS set:
 *
 *   checkOn   `data[f] !== false`  — ABSENT MEANS ON
 *   checkOff  `!!data[f]`          — absent means off
 *
 * Every page toggle is `checkOn`. Porting them as `checkOff` would hide every
 * page on a fresh install, and nothing about the code would look wrong.
 */

import { el, esc } from '../dom';
// `siteIdsOf` is the ARRAY-WINS-OUTRIGHT rule, ported once in `routers.ts` and
// reused rather than restated: a second copy here would drift, and the half that
// drifts silently is the empty array — an explicit `siteIds: []` means "no
// sites", so falling through to the `siteId` mirror resurrects a membership
// just cleared.
import { siteIdsOf } from './routers';
import { PAGE_NAV_MAP, VIEW_PRESETS, VIEW_PRESET_KEY } from '../gen/view-presets';
import {
  FORM_FIELDS, VALUE_DEFAULTS, PLACEHOLDER_CREDENTIALS,
  type ValueDefault,
} from '../gen/settings-form-map';

/** The settings payload, whichever of the two the server sent. */
export type SettingsPayload = Record<string, unknown>;

/**
 * What an input shows for a value the operator has never set.
 *
 * FIVE SHAPES, because populate() has five, and they disagree exactly where it
 * matters. `orNumber` is the one that would be missed by reading: an absent
 * smtpPort renders as 587, not as an empty box, because 587 is the port the app
 * is actually using.
 */
function valueFor(raw: unknown, rule: ValueDefault | undefined): string {
  const kind = rule ? rule.kind : 'undefinedToEmpty';
  switch (kind) {
    case 'blank':
      // Never shows a stored value at all — see PLACEHOLDER_CREDENTIALS.
      return '';
    case 'orEmpty':
      // `data.X || ''` — a falsy value, INCLUDING 0 and '', becomes empty.
      return raw ? String(raw) : '';
    case 'orNumber':
      return raw ? String(raw) : String(rule && rule.fallback !== undefined ? rule.fallback : '');
    case 'stringOf':
      // `String(data.X)`, guarded by `!= null` at the call site — so a null or
      // undefined leaves the input ALONE rather than writing "null" into it.
      return raw == null ? '' : String(raw);
    case 'bare':
      // Assigned straight through. `undefined` reaching an input's value writes
      // the string "undefined" in a browser, and the original does exactly that
      // — reproduced rather than tidied, because a port that showed an empty box
      // here would disagree about what the operator sees.
      return String(raw);
    default:
      return raw !== undefined ? String(raw) : '';
  }
}

/** Fill the form from a settings payload. */
export function populateSettings(data: SettingsPayload): void {
  for (const key of FORM_FIELDS.value) {
    const input = el<HTMLInputElement>('s_' + key);
    if (!input) continue;
    const rule = VALUE_DEFAULTS[key];
    // `stringOf` is guarded at its call site: a null value leaves the input
    // untouched rather than being written as an empty string.
    if (rule && rule.kind === 'stringOf' && data[key] == null) continue;
    input.value = valueFor(data[key], rule);
  }

  for (const key of FORM_FIELDS.checkOn) {
    const input = el<HTMLInputElement>('s_' + key);
    // ABSENT MEANS ON. Only an explicit `false` unticks it.
    if (input) input.checked = data[key] !== false;
  }

  for (const key of FORM_FIELDS.checkOff) {
    const input = el<HTMLInputElement>('s_' + key);
    if (input) input.checked = !!data[key];
  }

  // ── THE SLIDERS CARRY A COMPANION LABEL ──────────────────────────────────
  //
  // `s_alertCpuThreshold` is a range input and `s_alertCpuThresholdVal` is the
  // "72%" beside it. The map generator captures the `s_<key>` assignments driven
  // by populate()'s field LISTS; these are one-off statements and were not in it,
  // so the port set the sliders and left both readouts blank. The 518-case gate
  // could not see it either — it only inspects ids the map names.
  //
  // The label is only written when the value is PRESENT, matching the original's
  // `!= null` guard: an absent threshold leaves the markup's own default rather
  // than rendering "undefined%".
  // ── GUARDED CHECKBOXES: ABSENT MEANS "LEAVE IT ALONE" ────────────────────
  //
  // The alert-type toggles are written only when the setting is PRESENT. Unlike
  // `checkOff`, an absent value does not write `false` — the checkbox keeps the
  // markup's own default. On a fresh install the difference is every alert type
  // showing as switched off while the server still has it enabled, with the push
  // channel firing and the notification bell staying empty. The live comment on
  // `_PAGE_SETTING_KEYS` records that exact failure happening once already.
  for (const key of FORM_FIELDS.checkGuarded) {
    if (data[key] === undefined) continue;
    const input = el<HTMLInputElement>('s_' + key);
    if (input) input.checked = !!data[key];
  }

  // THREE SLIDERS, TWO SUFFIXES. The two thresholds read as a percentage and the
  // cooldown reads as seconds — `' s'`, with the space. Assuming one suffix for
  // all three would put "30%" beside a control measured in seconds.
  for (const [key, labelId, suffix] of [
    ['alertCpuThreshold', 's_alertCpuThresholdVal', '%'],
    ['alertPingLoss', 's_alertPingLossVal', '%'],
    ['notifCooldownSec', 's_notifCooldownSecVal', ' s'],
  ] as const) {
    if (data[key] == null) continue;
    const label = el(labelId);
    if (label) label.textContent = String(data[key]) + suffix;
  }

  // ── THE SIGN-IN TOGGLE, WHICH NOTHING SET UNTIL 0.8.15 ───────────────────
  //
  // `s_authEnabled` has no `checked` attribute, so an unpopulated box reads OFF
  // on every load whatever the install actually does. That was merely a wrong
  // label while nothing collected the form; the moment a Save button exists it
  // becomes an install-wide open-access switch, because the collector would post
  // `authMode: 'none'` from a control the operator never touched — and once the
  // mode is `none`, `maySaveSettings` returns true for everyone.
  //
  // `authModeOf` was written for exactly this line and was never called, the
  // same shape as the Save button itself. ABSENT MEANS MODERN there, so a fresh
  // install ticks the box rather than offering to switch sign-in off.
  const auth = el<HTMLInputElement>('s_authEnabled');
  if (auth) auth.checked = authModeOf(data) !== 'none';

  // The credentials that are never pre-filled. The MASK is a presence signal,
  // not a value: the input stays empty and the placeholder carries the meaning,
  // so an operator who saves without touching the field sends nothing at all.
  for (const [key, texts] of Object.entries(PLACEHOLDER_CREDENTIALS)) {
    const input = el<HTMLInputElement>('s_' + key);
    if (!input) continue;
    input.value = '';
    input.placeholder = data[key] ? texts.whenSet : texts.whenNot;
  }
}

/**
 * The auth toggle, which is a toggle over a two-valued string.
 *
 * ANYTHING THAT IS NOT THE LITERAL 'none' COUNTS AS ON, matching the server's
 * `_authMode()`. The live comment gives the reason: a stored `''` or a legacy
 * `'basic'` must not read as "authentication disabled" — which is what a plain
 * `mode === 'modern'` test would do, silently turning the login off on screen
 * for an install that still requires it.
 */
export function authModeOf(data: SettingsPayload): 'none' | 'modern' {
  return data.authMode === 'none' ? 'none' : 'modern';
}

// ── the tab shell ───────────────────────────────────────────────────────────

/**
 * Tabs whose panes have nothing to save.
 *
 * The actions bar is HIDDEN on these rather than disabled: Routers and About
 * both write through their own controls, and a Save button that applied to
 * nothing would be a button that looks broken.
 */
const NO_SAVE_TABS = ['routers', 'about'];

let aboutFetched = false;

/**
 * Show one Settings tab.
 *
 * ── THE ORDER OF THE LAST TWO STEPS IS LOAD-BEARING ────────────────────────
 *
 * `_sizePrincipalsCard` measures the card from its own top offset, which can
 * only be read once the panel is on screen — and the card reserves room for the
 * actions bar, so it must run AFTER that bar's visibility is settled. The live
 * comment records what happened when it did not: Settings always opens on
 * Routers, so every earlier attempt found the Authentication panel still
 * `display:none`, returned early, and left the card at content height until an
 * inner tab click happened to re-run the sizer.
 */
export function activateSettingsTab(tabName: string): void {
  document.querySelectorAll('#page-settings .stab').forEach((t) => {
    (t as HTMLElement).classList.toggle('active', (t as HTMLElement).dataset.tab === tabName);
  });
  document.querySelectorAll('#page-settings .stab-panel').forEach((p) => {
    p.classList.toggle('active', p.id === 'stab-' + tabName);
  });

  const actions = el('settingsActions');
  if (actions) {
    actions.style.display = NO_SAVE_TABS.indexOf(tabName) !== -1 ? 'none' : 'flex';
  }

  const sizer = (globalThis as unknown as { _sizePrincipalsCard?: () => void })._sizePrincipalsCard;
  if (sizer) sizer();

  // ONCE PER PAGE LIFETIME. The latch is what stops a tab the operator clicks
  // between panes from re-fetching on every visit; the version cannot change
  // while the page is open.
  if (tabName === 'about' && !aboutFetched) {
    aboutFetched = true;
    fetch('/healthz')
      .then((r) => r.json())
      .then((d: { version?: string }) => {
        const v = el('stabAboutVersion');
        if (v && d.version) v.textContent = 'v' + d.version;
      })
      .catch(() => { /* the version line simply stays as it is */ });
  }
}

/**
 * Wire the tab bar.
 *
 * SETTINGS ALWAYS OPENS ON ROUTERS. It used to restore the last tab from
 * localStorage, which meant landing on whatever you happened to be editing last
 * — usually not where you want to start. The persistence was removed rather than
 * merely ignored, so nothing keeps writing a key no one reads; this port keeps
 * it removed rather than reintroducing it as a "nice to have".
 */
export function mountSettingsTabs(): void {
  document.querySelectorAll('#page-settings .stab').forEach((t) => {
    t.addEventListener('click', () => {
      activateSettingsTab((t as HTMLElement).dataset.tab || '');
    });
  });
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'settings') return;
    activateSettingsTab('routers');
  });
}

// ── the principals card ─────────────────────────────────────────────────────

/**
 * Size the principals card to the bottom of the viewport.
 *
 * MEASURED FROM THE CARD'S REAL TOP, not a CSS calc(). A calc would have to
 * hardcode the topbar, the tab strip, the Authentication card above and the save
 * bar below — and be wrong the moment any of them changes size. Reading the top
 * offset costs one layout and is right by construction.
 *
 * Returns early when the card is not on screen, because `getBoundingClientRect`
 * on a hidden element reports zeros and the card would be sized to the whole
 * viewport height the moment it appeared.
 */
export function sizePrincipalsCard(): void {
  const card = document.getElementById('principalsCard') as HTMLElement | null;
  if (!card || card.style.display === 'none' || !card.offsetParent) return;

  const actions = document.getElementById('settingsActions') as HTMLElement | null;
  // The card reserves room for the save bar, but only when that bar is actually
  // displayed — on the Routers and About tabs it is hidden, and reserving for it
  // would leave a strip of dead space.
  const reserve = (actions && actions.offsetParent
    ? actions.getBoundingClientRect().height + 12 : 0) + 24;
  const top = card.getBoundingClientRect().top;

  // A FLOOR, so a short viewport gives a usable card that scrolls rather than
  // one collapsed to nothing.
  card.style.height = Math.max(320, window.innerHeight - top - reserve) + 'px';
}

/**
 * Switch the principal tabs (Users / Groups / Sites / Roles).
 *
 * DELEGATED ON `document`, not bound to the strip, because the card starts
 * hidden and is shown by `applyCaps()` after the auth fetch resolves — a
 * listener attached at mount would find nothing to attach to.
 *
 * IT SETS `aria-selected` AS WELL AS THE CLASS, which the Settings tab switcher
 * above does not. The two look like the same function and are not: this strip is
 * a `tablist` and a screen reader reads the attribute, not the class.
 */
export function mountPrincipalTabs(): void {
  document.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const tab = t && t.closest ? (t.closest('.ptab') as HTMLElement | null) : null;
    if (!tab) return;
    e.preventDefault();

    const want = tab.getAttribute('data-ptab');
    document.querySelectorAll('.ptab').forEach((el) => {
      const on = el === tab;
      el.classList.toggle('active', on);
      el.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    document.querySelectorAll('.ptab-panel').forEach((p) => {
      p.classList.toggle('active', p.id === 'ptab-' + want);
    });

    // Panels differ in height, and the card is sized from its own top offset —
    // which does not move, but re-measuring keeps it right if the Authentication
    // card above has grown (the open-access warning appearing, say).
    sizePrincipalsCard();
  });
}

// ── auth mode and who may see the principals card ───────────────────────────

/** What `applyAuthModeVisibility` needs from the rest of the page. */
export interface AuthVisibilityDeps {
  /** `window._caps.managePrincipals`, or undefined while the caps fetch is in flight. */
  mayManage: boolean | undefined;
  /** Load the roles, then the users. Kept injectable so the ORDER can be gated. */
  loadRoles: () => Promise<void>;
  loadUsers: () => void;
  sizeCard: () => void;
}

/**
 * Show the right half of the Authentication tab for the current auth mode.
 *
 * ── AUTH MODE ALONE IS NOT ENOUGH, AND THAT IS THE POINT ───────────────────
 *
 * An operator is in `modern` mode and must still not see user management, so the
 * card is gated on the CAPABILITY as well. `applyCaps()` writes the same
 * element's display from a separate fetch, so whichever resolves last wins —
 * including the ordering that would leave the card on screen.
 *
 * ── UNKNOWN CAPS COUNT AS NO ───────────────────────────────────────────────
 *
 * While the caps fetch is in flight the answer is not "probably yes". The live
 * comment states the consequence plainly: "the flash is absence rather than
 * exposure". A port that treated undefined as permissive would show the principal
 * graph for as long as a slow fetch took, to someone who may never be allowed it.
 */
export function applyAuthModeVisibility(mode: string, deps: AuthVisibilityDeps): void {
  const noneWarn = document.getElementById('authNoneWarn') as HTMLElement | null;
  const modernFields = document.getElementById('modernAuthFields') as HTMLElement | null;
  const usersTabBtn = document.getElementById('ptabBtn-users') as HTMLElement | null;
  const usersPanel = document.getElementById('ptab-users') as HTMLElement | null;
  const userCard = document.getElementById('principalsCard') as HTMLElement | null;

  if (noneWarn) noneWarn.style.display = mode === 'none' ? '' : 'none';
  if (modernFields) modernFields.style.display = mode === 'modern' ? '' : 'none';

  const mayManage = !!deps.mayManage;
  if (userCard) userCard.style.display = mayManage ? '' : 'none';
  // Size it once it is actually on screen — offsetParent is null while hidden,
  // and the sizer returns early on that.
  if (mayManage) deps.sizeCard();

  // The Users tab is hidden outside modern mode, AND the selection is moved off
  // it if it was showing: an empty tab that cannot be populated reads as a bug.
  const usersUsable = mode === 'modern' && mayManage;
  if (usersTabBtn) usersTabBtn.style.display = usersUsable ? '' : 'none';
  if (!usersUsable && usersPanel && usersPanel.classList.contains('active')) {
    const first = document.querySelector('.ptab:not([style*="display: none"])') as HTMLElement | null;
    if (first) first.click();
  }

  // ROLES BEFORE USERS AND GROUPS. Both render grant rows through a lookup that
  // reads the loaded roles, so loading them out of order shows "unknown role"
  // until the next refresh. The chain is what enforces it — firing both and
  // hoping roles wins is the bug.
  if (mayManage) {
    void deps.loadRoles().then(() => {
      if (usersUsable) deps.loadUsers();
    });
  }
}

// ── what access a principal has, in words ───────────────────────────────────

/** A grant row as the Access Management card receives it. */
export interface GrantView {
  // OPTIONAL, because the readers that only LABEL a grant do not need it — but
  // the server sends it on every row and the editor's Remove button is built
  // from it. It was absent entirely until 2026-08-28, which made `user.grants`
  // unassignable to `EditableGrant[]` and would have forced a cast at each of
  // the three call sites: three places to be wrong about the same payload.
  id?: string | number;
  role_id?: string | null;
  role?: string | null;
  scope_type: string;
  scope_id?: string | null;
}

/** The lookups the labels resolve against, published by the loaders. */
export interface PrincipalLookups {
  roles?: { id: string; name: string }[];
  sitesById?: Record<string, { name: string }>;
  routers?: { id: string; label?: string; host?: string }[];
}

/**
 * The role a grant names.
 *
 * "unknown role" IS A REAL ANSWER, not a defensive fallback: a grant naming a
 * deleted role confers nothing, and rbac.js resolves it the same way. It is also
 * what appears when the roles have not loaded yet, which is why
 * `applyAuthModeVisibility` chains the loads rather than firing both.
 */
export function roleName(g: GrantView, look: PrincipalLookups): string {
  const r = (look.roles || []).find((x) => x.id === g.role_id);
  return r ? r.name : 'unknown role';
}

/**
 * The scope a grant applies to.
 *
 * ANYTHING THAT IS NOT `global` OR `site` IS TREATED AS A ROUTER. That is the
 * original's shape — a bare `else`, not a third comparison — so a scope type
 * this build does not know renders as "router: unknown" rather than as nothing.
 * Reproduced rather than tightened: a port that returned an empty label for an
 * unrecognised scope would render a grant that appears to apply to nothing.
 */
export function scopeLabel(g: GrantView, look: PrincipalLookups): string {
  if (g.scope_type === 'global') return 'all routers';
  if (g.scope_type === 'site') {
    const s = look.sitesById && g.scope_id ? look.sitesById[g.scope_id] : undefined;
    return 'site: ' + (s ? s.name : 'unknown');
  }
  const r = (look.routers || []).find((x) => x.id === g.scope_id);
  return 'router: ' + (r ? (r.label || r.host) : 'unknown');
}

/**
 * One line per grant, or an explicit note that there are none.
 *
 * THE "No access" PILL IS NOT AN EMPTY STATE. A principal with no grants and a
 * principal whose grants have not loaded would both render as blank, and the
 * card would be saying "this person has no access" in the second case without
 * knowing it. The pill says it deliberately; the loader's ordering is what makes
 * it true.
 */
export function accessSummary(grants: GrantView[] | undefined, look: PrincipalLookups): string {
  if (!grants || !grants.length) {
    return '<span style="padding:.1rem .5rem;border-radius:20px;font-size:.7rem;'
      + 'background:rgba(148,163,190,.1);color:var(--text-muted);'
      + 'border:1px solid rgba(148,163,190,.15)">No access</span>';
  }
  return grants.map((g) =>
    '<div style="font-size:.72rem">' + esc(roleName(g, look))
    + ' <span style="color:var(--text-muted)">— ' + esc(scopeLabel(g, look)) + '</span></div>',
  ).join('');
}

// ── the principal tables ────────────────────────────────────────────────────

/** A user row as the Users pane receives it. */
export interface UserView {
  id: string;
  username: string;
  grants?: GrantView[];
}

/**
 * One row of the Users table.
 *
 * ONE ACCESS COLUMN BUILT FROM GRANTS, spanning two columns, replacing the Role
 * badge and router pills that used to sit there. Those read the legacy
 * role/allowedRouterIds mirror, which cannot express a custom role or a grant
 * held at two different sites.
 *
 * The buttons carry `data-action` rather than inline handlers, because the row
 * is rebuilt on every refresh and the listeners are attached by the caller.
 */
export function userRowHtml(u: UserView, look: PrincipalLookups): string {
  return '<td style="padding:.45rem .5rem;font-size:.82rem">' + esc(u.username) + '</td>'
    + '<td style="padding:.45rem .5rem" colspan="2">' + accessSummary(u.grants, look) + '</td>'
    + '<td style="padding:.45rem .5rem;text-align:right;white-space:nowrap">'
    + '<button class="sbtn sbtn-ghost" style="font-size:.72rem;padding:.2rem .55rem;margin-right:.3rem" data-action="edit">Edit</button>'
    + '<button class="sbtn sbtn-danger" style="font-size:.72rem;padding:.2rem .55rem" data-action="del">Delete</button>'
    + '</td>';
}

/** A group row as the Groups pane receives it. */
export interface GroupView {
  id: string;
  name: string;
  description?: string | null;
  memberUserIds?: string[];
  grants?: GrantView[];
}

/**
 * The Groups table.
 *
 * ── IT PHRASES ACCESS DIFFERENTLY FROM THE USERS PANE, ON PURPOSE ──────────
 *
 * Three differences, all reproduced rather than unified:
 *
 *   - the users card wraps each grant in a `<div>` with role and scope in
 *     separate spans; this joins `role — scope` with `<br>`;
 *   - this ESCAPES THE COMBINED STRING, so the separator sits inside the escaped
 *     text, where the users card escapes each half independently;
 *   - the empty states differ in wording — "No access" against "no access
 *     granted" — and in markup.
 *
 * They are two views of the same data written at different times. Harmonising
 * them is a redesign, and the rule is that the rendered page does not change.
 */
export function groupTableHtml(groups: GroupView[], look: PrincipalLookups): string {
  if (!groups.length) {
    return '<tr><td colspan="4" style="padding:.75rem .5rem;color:var(--text-muted);font-size:.76rem">'
      + 'No groups yet. Add one to grant a role to several people at once.</td></tr>';
  }
  const td = 'padding:.4rem .5rem;border-bottom:1px solid var(--border)';
  return groups.map((g) => {
    const access = (g.grants || []).length
      ? (g.grants || []).map((x) => esc(roleName(x, look) + ' — ' + scopeLabel(x, look))).join('<br>')
      : '<span style="color:var(--text-muted)">no access granted</span>';
    return '<tr>'
      + '<td style="' + td + ';font-weight:600">' + esc(g.name)
        + (g.description ? '<div style="font-weight:400;font-size:.7rem;color:var(--text-muted)">' + esc(g.description) + '</div>' : '') + '</td>'
      + '<td style="' + td + ';font-family:var(--font-mono);font-size:.72rem">' + (g.memberUserIds || []).length + '</td>'
      + '<td style="' + td + ';font-size:.72rem">' + access + '</td>'
      + '<td style="' + td + ';text-align:right;white-space:nowrap">'
        + '<button class="sbtn sbtn-ghost" style="padding:.2rem .55rem;font-size:.7rem" data-group-action="edit" data-group-id="' + esc(g.id) + '">Edit</button> '
        + '<button class="sbtn sbtn-danger" style="padding:.2rem .55rem;font-size:.7rem" data-group-action="delete" data-group-id="' + esc(g.id) + '">Delete</button>'
      + '</td></tr>';
  }).join('');
}

// ── the Sites and Roles panes ───────────────────────────────────────────────

export interface SiteView { id: string; name: string; description?: string | null }

/** One row of the Sites table. */
export function siteRowHtml(s: SiteView, routerCount: number): string {
  const td = 'padding:.4rem .5rem;border-bottom:1px solid var(--border)';
  return '<tr>'
    + '<td style="' + td + ';font-weight:600">' + esc(s.name) + '</td>'
    // AN EM DASH, not an empty cell: a site with no description and one whose
    // description failed to load would otherwise look the same.
    + '<td style="' + td + ';color:var(--text-muted)">' + (s.description ? esc(s.description) : '—') + '</td>'
    + '<td style="' + td + ';font-family:var(--font-mono);font-size:.72rem">' + routerCount + '</td>'
    + '<td style="' + td + ';text-align:right;white-space:nowrap">'
      + '<button class="sbtn sbtn-ghost" style="padding:.2rem .55rem;font-size:.7rem" data-site-action="edit" data-site-id="' + esc(s.id) + '">Edit</button> '
      + '<button class="sbtn sbtn-danger" style="padding:.2rem .55rem;font-size:.7rem" data-site-action="delete" data-site-id="' + esc(s.id) + '">Delete</button>'
    + '</td></tr>';
}

/**
 * Devices-per-site, the port of `_siteRouterCounts`.
 *
 * COUNTED ON THE CLIENT from the device list the page already holds, not joined
 * on the server: the numbers are small and this keeps `GET /api/sites` a plain
 * table read.
 *
 * A DEVICE COUNTS ONCE IN EACH OF ITS SITES (#117), so the totals no longer sum
 * to the device count. That is correct rather than a rounding artefact — the
 * column answers "how many devices are in this site", and a device held at two
 * sites is in both of them.
 */
export function siteRouterCounts(
  routers: { siteIds?: string[]; siteId?: string | null }[],
): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const r of routers || []) {
    for (const id of siteIdsOf(r)) counts[id] = (counts[id] || 0) + 1;
  }
  return counts;
}

/**
 * What the operator is asked before a site is deleted.
 *
 * ── THE WARNING IS #117-AWARE, AND THAT IS THE POINT OF IT ──────────────────
 *
 * "They keep any other sites, and are not deleted." Before multi-site, a device
 * had ONE site and deleting it left the device with none — so the honest warning
 * then was about losing its only membership. Now a device may be held at several
 * and loses exactly one, which is a much smaller thing to agree to. A port that
 * kept the old wording would frighten an operator out of a safe action.
 *
 * "device(s)", not a pluralised word: the live string is literal, and the count
 * is shown even when it is 1.
 *
 * NO WARNING AT ALL when nothing is affected — an empty site is deleted with a
 * bare question, because a "0 devices will lose this site" line reads as though
 * something might.
 */
export function siteDeletePrompt(name: string, routerCount: number): string {
  const warn = routerCount
    ? '\n\n' + routerCount + ' device(s) will lose this site. They keep any other sites, '
      + 'and are not deleted.'
    : '';
  return 'Delete site "' + name + '"?' + warn;
}

/** What the site form holds when Save is pressed. */
export interface SiteFormValues {
  /** Empty when adding; the site's id when editing. */
  id: string;
  name: string;
  description: string;
  /** The town picker's value, or null for "no location". */
  place: unknown;
  /** The ids of the ticked device checkboxes, in DOM order. */
  routerIds: string[];
}

/** One HTTP call the save needs to make. */
export interface SiteSaveRequest {
  method: 'POST' | 'PUT';
  /** `:id` for the membership call is filled from the first reply — see below. */
  path: string;
  body: Record<string, unknown>;
}

export type SiteSavePlan =
  | { error: string }
  | { requests: [SiteSaveRequest, SiteSaveRequest] };

/**
 * What pressing Save on the site form sends.
 *
 * ── IT IS ALWAYS TWO REQUESTS, AND THE ORDER IS FORCED ──────────────────────
 *
 * The site's own fields and its membership live behind different endpoints
 * because they are different authorization decisions (see
 * `internal/server/sites_api.go`). The membership call has to go SECOND, and not
 * for tidiness: when adding, there is no id until the create returns one. The
 * live comment says exactly that.
 *
 * The second request's path therefore carries `{id}` rather than a real id when
 * creating, and the caller substitutes what the first reply gave it. That is
 * modelled explicitly instead of having this function take a callback, so the
 * whole decision stays pure and testable.
 *
 * ── EVERY FIELD IS ALWAYS SENT, WHICH IS NOT THE SAME AS THE API'S RULE ─────
 *
 * `ParseSiteBody` distinguishes absent from null: an absent `place` leaves the
 * location alone, a null one clears it. THIS FORM NEVER SENDS AN ABSENT ONE —
 * it reads the picker every time, so a form saved with the picker empty CLEARS
 * the location, deliberately. The absent case exists for other callers, and the
 * server has to keep supporting it; a port that "simplified" the server to match
 * this client would break a rename made through the API.
 */
export function siteSavePlan(form: SiteFormValues): SiteSavePlan {
  // TRIMMED BEFORE THE EMPTINESS TEST, matching the live `value.trim()`. A name
  // of spaces is refused here rather than by the server, so the operator is told
  // in the form instead of by a round trip.
  const name = form.name.trim();
  if (!name) return { error: 'Name is required' };

  const editing = form.id !== '';
  return {
    requests: [
      {
        method: editing ? 'PUT' : 'POST',
        path: editing ? '/api/sites/' + encodeURIComponent(form.id) : '/api/sites',
        body: { name, description: form.description.trim(), place: form.place ?? null },
      },
      {
        method: 'PUT',
        // `{id}` is a PLACEHOLDER when creating. Spelling it out beats returning
        // a half-built URL: a caller that forgets to substitute gets a 404 from
        // a path that obviously names nothing, rather than silently writing
        // membership to a site called "undefined".
        path: '/api/sites/' + (editing ? encodeURIComponent(form.id) : '{id}') + '/routers',
        body: { routerIds: form.routerIds },
      },
    ],
  };
}

/** One device in the site form's membership list. */
export interface SiteMemberDevice {
  id: string;
  label?: string | null;
  host?: string | null;
  siteIds?: string[];
  siteId?: string | null;
}

/**
 * The site form's device list — one checkbox per device in the fleet.
 *
 * ── "ALSO IN", NOT "CURRENTLY IN" ───────────────────────────────────────────
 *
 * The live comment says why, and the wording is the whole #117 change in three
 * words: ticking a device here ADDS this site, it no longer moves the device out
 * of the ones it already has. Before multi-site the same list said where the
 * device "currently" sat, because putting it here took it out of there.
 *
 * ── THE FILTER DROPS SITES THIS CLIENT CANNOT NAME ──────────────────────────
 *
 * `_sitesById[id]` is consulted, not just `id !== site.id`. A device may carry a
 * membership whose site this browser has not loaded — it was created by another
 * administrator a moment ago, or removed while this form was open. Rendering
 * `also in undefined` is worse than saying nothing, and saying nothing is what
 * the live code does.
 *
 * `site` is null when ADDING: nothing is ticked, and every membership a device
 * has counts as elsewhere.
 */
export function siteMemberRowsHtml(
  devices: SiteMemberDevice[],
  site: { id: string } | null,
  sitesById: Record<string, { name: string }>,
): string {
  if (!devices.length) {
    return '<span style="color:var(--text-muted)">No devices configured yet.</span>';
  }
  return devices.map((r) => {
    const ids = siteIdsOf(r);
    const here = !!(site && ids.indexOf(site.id) !== -1);
    // ONE LOOKUP, not filter-then-map. The live code indexes `_sitesById` twice
    // and TypeScript is right to object under `noUncheckedIndexedAccess`: the
    // two lookups are only guaranteed to agree because nothing mutates the map
    // between them, which is true here and is not a property worth relying on.
    const elsewhere: string[] = [];
    for (const id of ids) {
      if (site && id === site.id) continue;
      const known = sitesById[id];
      if (known) elsewhere.push(known.name);
    }
    const other = elsewhere.length
      ? ' <span style="color:var(--text-muted)">— also in ' + esc(elsewhere.join(', ')) + '</span>'
      : '';
    return '<label style="display:flex;align-items:center;gap:.4rem;margin-bottom:.2rem">'
      + '<input type="checkbox" data-site-router="' + esc(r.id) + '"' + (here ? ' checked' : '') + '>'
      + '<span>' + esc(r.label || r.host || '') + other + '</span></label>';
  }).join('');
}

export function siteTableHtml(sites: SiteView[], counts: Record<string, number>): string {
  if (!sites.length) {
    return '<tr><td colspan="4" style="padding:.75rem .5rem;color:var(--text-muted);font-size:.76rem">'
      + 'No sites yet. Add one to group your devices.</td></tr>';
  }
  return sites.map((s) => siteRowHtml(s, counts[s.id] || 0)).join('');
}

export interface RoleView {
  id: string;
  name: string;
  description?: string | null;
  builtin?: boolean;
  pages: { page: string; access: string }[];
  grants?: number;
}

/**
 * What a role reaches, in a phrase.
 *
 * A BUILTIN ROLE IS "every page" WITHOUT CONSULTING ITS ROWS. Its reach is
 * structural — rbac.js grants it everything rather than storing a matrix — so
 * counting `pages` would report whatever happens to be in the table, which for a
 * seeded role is often nothing.
 */
export function pageSummary(role: RoleView): string {
  if (role.builtin) return 'every page';
  if (!role.pages.length) return '<span style="color:var(--text-muted)">no pages</span>';
  const reads = role.pages.filter((p) => p.access === 'read').length;
  const writes = role.pages.length - reads;
  const bits: string[] = [];
  if (reads) bits.push(reads + ' read');
  if (writes) bits.push(writes + ' write');
  return bits.join(', ');
}

/**
 * The Roles table.
 *
 * A BUILTIN ROW SHOWS A LOCK AND NO ACTIONS. Its reach is structural, so editing
 * it would either do nothing or narrow every administrator at once.
 *
 * The grant count is pluralised, and ZERO IS AN EM DASH rather than "0 grants":
 * an unused role and a role used once are different enough to say differently.
 */
export function roleTableHtml(roles: RoleView[]): string {
  if (!roles.length) {
    return '<tr><td colspan="4" style="padding:.75rem .5rem;color:var(--text-muted)">No roles yet.</td></tr>';
  }
  return roles.map((r) => {
    const actions = r.builtin
      ? '<span style="color:var(--text-muted);font-size:.7rem">built in</span>'
      : '<button class="sbtn sbtn-outline" data-role-edit="' + esc(r.id) + '" style="padding:.15rem .5rem;font-size:.7rem">Edit</button>'
        + ' <button class="sbtn sbtn-outline" data-role-del="' + esc(r.id) + '" style="padding:.15rem .5rem;font-size:.7rem;color:#f87171;border-color:rgba(248,113,113,.35)">Delete</button>';
    return '<tr style="border-bottom:1px solid var(--border)">'
      + '<td style="padding:.4rem .5rem">' + esc(r.name)
        + (r.description ? '<div style="color:var(--text-muted);font-size:.7rem">' + esc(r.description) + '</div>' : '') + '</td>'
      + '<td style="padding:.4rem .5rem">' + pageSummary(r) + '</td>'
      + '<td style="padding:.4rem .5rem">' + (r.grants ? r.grants + ' grant' + (r.grants === 1 ? '' : 's') : '<span style="color:var(--text-muted)">—</span>') + '</td>'
      + '<td style="padding:.4rem .5rem;text-align:right;white-space:nowrap">' + actions + '</td>'
      + '</tr>';
  }).join('');
}

/**
 * One row of the role matrix: a page and a none/read/write segmented control.
 *
 * ── A PAGE WITH NO WRITE ACTIONS SHOWS THE SEGMENT DISABLED, NOT HIDDEN ────
 *
 * The matrix keeps its shape and the reason stays visible — the button carries
 * a title saying so. Hiding it would leave a ragged grid and no explanation for
 * why one row has two choices and the next has three. `writeCapable` comes from
 * the server's projection table, never from a list restated here.
 *
 * `none` is selected when there is NO access at all, which is why the test is
 * `(level === 'none' && !access) || level === access` rather than a comparison
 * against a default string.
 */
export function rolePageRowHtml(
  page: { key: string; title: string }, access: string | undefined, writeCapable: string[],
): string {
  const writable = writeCapable.indexOf(page.key) !== -1;
  const seg = ['none', 'read', 'write'].map((level) => {
    const on = (level === 'none' && !access) || level === access;
    const dead = level === 'write' && !writable;
    return '<button type="button" class="sbtn ' + (on ? 'sbtn-primary' : 'sbtn-outline') + '"'
      + ' data-page-set="' + esc(page.key) + '" data-level="' + level + '"'
      + (dead ? ' disabled title="No write actions on this page yet"' : '')
      + ' style="padding:.1rem .5rem;font-size:.68rem' + (dead ? ';opacity:.4;cursor:not-allowed' : '') + '">'
      + level.charAt(0).toUpperCase() + level.slice(1) + '</button>';
  }).join('');
  return '<div data-page-row="' + esc(page.key) + '" style="display:flex;align-items:center;'
    + 'justify-content:space-between;gap:.5rem;padding:.25rem .4rem;border-bottom:1px solid var(--border)">'
    + '<span>' + esc(page.title) + '</span>'
    + '<span style="display:flex;gap:.2rem;flex-shrink:0">' + seg + '</span>'
    + '</div>';
}

// ── the grant editor ────────────────────────────────────────────────────────

/** A grant row carrying the id the editor's Remove button needs. */
export interface EditableGrant extends GrantView { id: string | number }

/** What the editor's two pickers are built from. */
export interface GrantEditorOptions {
  roles: { id: string; name: string }[];
  sitesById: Record<string, { name: string }>;
  routers: { id: string; label?: string; host?: string }[];
}

/**
 * The grant editor's markup, shared by the Groups and Users forms.
 *
 * ── A THIRD PHRASING OF THE SAME SENTENCE ──────────────────────────────────
 *
 * This is the third place in the card that renders `role — scope`, and all three
 * differ:
 *
 *   Users card     a <div> per grant, role and scope in SEPARATE spans, each
 *                  escaped on its own
 *   Groups table   `esc(role + ' — ' + scope)` joined with <br> — the separator
 *                  is inside the escaped text
 *   this editor    `esc(role) + ' — ' + esc(scope)` in ONE flex span
 *
 * Three renderings written at different times. Reproduced rather than unified,
 * because the rule is that the rendered page does not change — and because the
 * escaping genuinely differs between them, so "unifying" would be a behaviour
 * change dressed as a tidy-up.
 *
 * ── THE PICKERS ARE BUILT FROM DATA, NOT FROM CONSTANTS ────────────────────
 *
 * Roles come from `/api/roles`. They used to be three hardcoded options, which
 * could not name a custom role at all. The scope picker's values are
 * `type:id` pairs — `global:` with an empty id is "all routers".
 *
 * The CLICK HANDLER IS NOT PORTED: removing and adding a grant are writes, and
 * the writes belong to Node until cutover. This builds the markup only.
 */
export function grantEditorHtml(
  grants: EditableGrant[] | undefined, o: GrantEditorOptions,
): string {
  const look: PrincipalLookups = { roles: o.roles, sitesById: o.sitesById, routers: o.routers };

  const rows = (grants || []).map((g) =>
    '<div style="display:flex;align-items:center;gap:.5rem;margin-bottom:.3rem">'
    + '<span style="flex:1">' + esc(roleName(g, look)) + ' — ' + esc(scopeLabel(g, look)) + '</span>'
    + '<button class="sbtn sbtn-ghost" style="padding:.1rem .45rem;font-size:.65rem" data-grant-del="' + esc(String(g.id)) + '">Remove</button>'
    + '</div>').join('');

  const sites = o.sitesById || {};
  const siteOpts = Object.keys(sites).map((id) =>
    '<option value="site:' + esc(id) + '">Site: ' + esc((sites[id] as { name: string }).name) + '</option>').join('');
  const rtrOpts = (o.routers || []).map((r) =>
    '<option value="router:' + esc(r.id) + '">Router: ' + esc(r.label || r.host) + '</option>').join('');

  // `rows || <the empty note>` — an empty string is falsy, so a principal with
  // no grants gets the note rather than a blank box.
  return (rows || '<div style="color:var(--text-muted);margin-bottom:.3rem">No access granted yet.</div>')
    + '<div style="display:flex;gap:.4rem;margin-top:.5rem">'
      + '<select class="sform-input" data-grant-role style="flex:0 0 9rem">'
        + (o.roles || []).map((r) => '<option value="' + esc(r.id) + '">' + esc(r.name) + '</option>').join('')
      + '</select>'
      + '<select class="sform-input" data-grant-scope style="flex:1">'
        + '<option value="global:">All routers</option>' + siteOpts + rtrOpts
      + '</select>'
      + '<button class="sbtn sbtn-outline" data-grant-add style="flex:0 0 auto">Add</button>'
    + '</div>';
}

// ── the view presets ────────────────────────────────────────────────────────


/**
 * Which named preset the ticked page toggles correspond to, or 'custom'.
 *
 * ── `advanced` IS DERIVED, SO A NEW PAGE JOINS IT BY EXISTING ──────────────
 *
 * The original fills it from PAGE_NAV_MAP at startup rather than listing it.
 * Freezing it into a literal would mean the next page added silently drops out
 * of Advanced, and the preset would stop matching for anyone who had selected
 * it.
 *
 * ── A TOGGLE THAT IS NOT IN THE DOM IS SKIPPED, NOT COUNTED AS OFF ─────────
 *
 * `if (!el) continue;` — a page whose checkbox is not rendered does not break
 * the match. Treating a missing element as unchecked would report 'custom' for
 * a form that is simply showing fewer rows.
 */
export function detectViewPreset(): string {
  const advanced = Object.keys(PAGE_NAV_MAP).map((k) => PAGE_NAV_MAP[k] as string);
  const named: Record<string, string[]> = {
    home: VIEW_PRESETS.home as string[],
    standard: VIEW_PRESETS.standard as string[],
    advanced,
  };

  // ── THE ORDER IS NOT LOAD-BEARING, AND I FIRST WROTE THAT IT WAS ────────
  //
  // The obvious reading is that it must be: `home` is a subset of `standard`,
  // which is a subset of `advanced`, so surely the narrowest has to be tried
  // first. It does not, because the comparison below is EXACT — every rendered
  // toggle must equal that preset's membership, so a `home` selection fails
  // `standard` on the first page `standard` adds. At most one preset can match
  // any state.
  //
  // Measured, not reasoned: reversing this list changes no answer in the gate's
  // nine cases. The order is kept as the original writes it, because matching
  // the source costs nothing and a reader comparing the two should not have to
  // wonder why they differ.
  for (const name of ['home', 'standard', 'advanced']) {
    const on: Record<string, boolean> = {};
    (named[name] as string[]).forEach((pg) => { on[pg] = true; });

    let match = true;
    for (const sKey of Object.keys(PAGE_NAV_MAP)) {
      const el = document.getElementById('s_' + sKey) as HTMLInputElement | null;
      if (!el) continue;
      if (el.checked !== !!on[PAGE_NAV_MAP[sKey] as string]) { match = false; break; }
    }
    if (match) return name;
  }
  return 'custom';
}

/**
 * Mark the chosen preset and remember it.
 *
 * The persistence is per-browser and deliberate — unlike the Settings TAB, whose
 * persistence was removed upstream. This one is a view preference rather than a
 * position in a form.
 */
export function setViewPresetUI(name: string): void {
  document.querySelectorAll('.view-preset-btn').forEach((btn) => {
    const e = btn as HTMLElement;
    e.classList.toggle('active', e.dataset.viewPreset === name);
  });
  try { localStorage.setItem(VIEW_PRESET_KEY, name); } catch { /* site data blocked */ }
}
