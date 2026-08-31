'use strict';
/**
 * What the Access Management card says a principal can do.
 *
 * `_roleName`, `_scopeLabel` and `_accessSummary` are lifted from `app.js` and
 * run against the same inputs as the port. All three are pure string builders,
 * so the comparison is on the markup itself.
 *
 * ── THE CASES ARE THE ONES THAT MISSTATE ACCESS ────────────────────────────
 *
 * This card is read to answer "what can this person do", so every wrong answer
 * is a wrong answer about somebody's access:
 *
 *   no grants          must say "No access" EXPLICITLY. Blank would be
 *                      indistinguishable from grants that have not loaded.
 *   a deleted role     "unknown role" is correct — such a grant confers nothing,
 *                      and rbac.js resolves it the same way.
 *   roles not loaded   the same words, which is why the loader chains rather
 *                      than firing both fetches at once.
 *   an unknown scope   falls through to the ROUTER branch, because the original
 *                      ends in a bare `else` — "router: unknown", not blank.
 *   a router with no
 *   label              falls back to its host.
 *
 *   node tools/access-summary-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('access-summary-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function lift(start, end, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(start);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(end, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + end.length);
}
// siteMemberRows slices the device-list builder out of `showSiteForm` and wraps
// it in a callable. The anchors are asserted, so a refactor upstream fails this
// gate loudly instead of silently comparing the port against nothing.
function siteMemberRows() {
  const OPEN = '    box.innerHTML = routers.length';
  // A MARKER, and then the rest of ITS LINE. Spelling the closing line out would
  // mean escaping a nest of quotes through this file, and one wrong backslash
  // makes the slice silently short.
  const END = 'No devices configured yet.';
  const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
  const n = src.split(OPEN).length - 1;
  if (n !== 1) throw new Error('the device-list anchor is ambiguous (' + n + ' matches)');
  const from = src.indexOf(OPEN);
  const mark = src.indexOf(END, from);
  if (mark < 0) throw new Error('the device-list slice has no closing anchor');
  const to = src.indexOf('\n', mark);
  let body = src.slice(from, to);
  for (const marker of ['also in', 'data-site-router', '_siteIdsOf', '_sitesById']) {
    if (!body.includes(marker)) {
      throw new Error('the device-list slice has no ' + marker + ' -- it stopped early, and '
        + 'this gate would then compare the port against less than the form renders');
    }
  }
  // `box.innerHTML = …` becomes a return.
  body = body.replace(OPEN, '    return routers.length');
  return '  function _siteMemberRows(routers, site) {\n' + body + '\n  }';
}

// FROZEN AS ONE VALUE. Every entry is a lift, and one of them
// (`siteMemberRows()`) is a whole function that slices and rewrites the live
// source — three syntactic forms that `freeze-src.py` matches none of. The
// joined result is what `vm` executes, so freezing it keeps the live half
// RUNNING without a reference.
const liveSrc = G.value('the lifted live source', () => [
  lift('  function _roleName(g) {', '\n  }', '_roleName'),
  lift('  function _scopeLabel(g) {', '\n  }', '_scopeLabel'),
  lift('  function _accessSummary(u) {', '\n  }', '_accessSummary'),
  // The Groups table, which phrases the SAME data differently — see the note in
  // the port. Lifted alongside so the asymmetry is compared rather than assumed.
  lift('  function _renderGroupTable() {', '\n  }', '_renderGroupTable'),
  // The user ROW, which builds a <tr> and then attaches listeners. Only its
  // markup is compared here; the listener wiring needs a browser.
  lift('  function renderUserRow(u) {', '\n  }', 'renderUserRow'),
  // The Sites and Roles panes, and the role matrix row.
  lift('  function _renderSiteRow(s, routerCount) {', '\n  }', '_renderSiteRow'),
  // The TABLE around that row, and the two functions that feed it. The row alone
  // was gated for several sessions and the EMPTY STATE was not — which is where
  // the `routers` -> `devices` rename (upstream `063a414`) drifted, because a
  // fixture with sites in it never renders that branch.
  lift('  function _siteIdsOf(r) {', '\n  }', '_siteIdsOf'),
  lift('  function _siteRouterCounts() {', '\n  }', '_siteRouterCounts'),
  // The site form's device list. It lives INSIDE `showSiteForm`, which touches
  // a picker, four form fields and the DOM, so the map callback is sliced out
  // and given a name rather than the whole function being lifted.
  siteMemberRows(),
  // The delete confirmation, whose warning is the #117 change in one sentence.
  lift('  function deleteSite(id, name, routerCount) {', '\n  }', 'deleteSite'),
  lift('  function _renderSiteTable() {', '\n  }', '_renderSiteTable'),
  lift('  function _pageSummary(role) {', '\n  }', '_pageSummary'),
  lift('  function _renderRoleTable() {', '\n  }', '_renderRoleTable'),
  lift('  function _rolePageRow(page, access) {', '\n  }', '_rolePageRow'),
  // The grant editor. Only its MARKUP is compared — the click handler it
  // installs performs writes, which belong to Node until cutover.
  lift('  function _renderGrantEditor(container, principalType, principalId, grants, opts) {',
       '\n  }', '_renderGrantEditor'),
].join('\n'));
if (!liveSrc || liveSrc.length < 200) {
  throw new Error('the recorded live source is empty — the golden is broken');
}
for (const fn of ['_roleName', '_scopeLabel', '_accessSummary', '_renderGroupTable', 'renderUserRow',
                  '_renderSiteRow', '_siteIdsOf', '_siteRouterCounts', '_renderSiteTable',
                  '_siteMemberRows', 'deleteSite',
                  '_pageSummary', '_renderRoleTable', '_rolePageRow',
                  '_renderGrantEditor']) {
  if (!liveSrc.includes('function ' + fn)) throw new Error('lost ' + fn + ' in the lift');
}

// `esc` is the live helper these call. Lifted too, rather than reimplemented —
// the escaping is part of what is being compared.
const escSrc = G.value('escSrc', () => lift('function esc(', '\n}', 'esc'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['escSrc', escSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

const ROLES = [{ id: 'role-op', name: 'Operator' }, { id: 'role-view', name: 'Viewer' }];
const SITES = { 's1': { name: 'Berlin' } };
const ROUTERS = [
  { id: 'r1', label: 'Branch Office', host: '198.51.100.2' },
  { id: 'r2', label: '', host: '198.51.100.3' },
];

const CASES = [
  ['no grants at all', undefined],
  ['an empty grant list', []],
  ['a global grant', [{ role_id: 'role-op', scope_type: 'global', scope_id: null }]],
  ['a site grant', [{ role_id: 'role-view', scope_type: 'site', scope_id: 's1' }]],
  ['a site that no longer exists', [{ role_id: 'role-op', scope_type: 'site', scope_id: 'gone' }]],
  ['a router grant', [{ role_id: 'role-op', scope_type: 'router', scope_id: 'r1' }]],
  ['a router with no label falls back to its host',
    [{ role_id: 'role-op', scope_type: 'router', scope_id: 'r2' }]],
  ['a router that no longer exists', [{ role_id: 'role-op', scope_type: 'router', scope_id: 'gone' }]],
  ['a DELETED role', [{ role_id: 'role-removed', scope_type: 'global', scope_id: null }]],
  // The bare `else` in _scopeLabel: an unrecognised scope is a ROUTER, not blank.
  ['an unknown scope type', [{ role_id: 'role-op', scope_type: 'galaxy', scope_id: 'x' }]],
  ['several grants at once', [
    { role_id: 'role-op', scope_type: 'global', scope_id: null },
    { role_id: 'role-view', scope_type: 'site', scope_id: 's1' },
    { role_id: 'role-op', scope_type: 'router', scope_id: 'r1' },
  ]],
  // ESCAPING: a role or router named by a hostile record must not become markup.
  ['a role name carrying markup', [{ role_id: 'role-xss', scope_type: 'global', scope_id: null }]],
];

const XSS_ROLES = ROLES.concat([{ id: 'role-xss', name: '<img src=x onerror=alert(1)>' }]);

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-access.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
delete require.cache[require.resolve(OUT)];
const port = require(OUT);

function runLive(grants) {
  const win = { _allRoles: XSS_ROLES, _sitesById: SITES, _allRouters: ROUTERS };
  return new Function('window', 'document',
    escSrc + '\n' + liveSrc + '\nreturn _accessSummary({ grants: arguments[2] });')(
    win, { createElement: () => ({}) }, grants);
}

const look = { roles: XSS_ROLES, sitesById: SITES, routers: ROUTERS };
const bad = [];
let checks = 0;
let withGrants = 0;

for (const [name, grants] of CASES) {
  checks++;
  const a = runLive(grants);
  const b = port.accessSummary(grants, look);
  if (grants && grants.length) withGrants++;
  if (a !== b) bad.push({ name, live: a, port: b });
}

// GUARDS. "No access" for everything would agree with a port that did the same,
// and a corpus of only-empty grant lists would never exercise the labels.
if (withGrants < 5) {
  console.error('only ' + withGrants + ' cases carry grants — the label paths are barely run');
  process.exit(1);
}
{
  const one = runLive([{ role_id: 'role-op', scope_type: 'global', scope_id: null }]);
  if (one.indexOf('Operator') === -1 || one.indexOf('all routers') === -1) {
    console.error('the LIVE summary did not name the role and scope for an ordinary ' +
                  'grant — the lift is not working and every case is comparing rubbish');
    process.exit(1);
  }
  const xss = runLive([{ role_id: 'role-xss', scope_type: 'global', scope_id: null }]);
  if (xss.indexOf('<img') !== -1) {
    console.error('the LIVE summary emitted an unescaped tag — refusing to record ' +
                  'agreement with that');
    process.exit(1);
  }
}

// THE REPORT MOVED TO THE END OF THE FILE. It used to sit here, before the
// group-table and user-row comparisons were added below — so their failures were
// collected into `bad` and never read, and the script printed success whatever
// they found. Caught by noticing that a deliberately broken helper still passed.
// ── the two TABLES, which phrase the same data differently ────────────────
//
// The users pane wraps each grant in a div with role and scope in separate
// spans; the groups table joins `role — scope` with <br> and escapes the
// COMBINED string. Two views written at different times, and a port that
// unified them would be a redesign.
const GROUP_CASES = [
  ['no groups at all', []],
  ['a group with no grants', [{ id: 'g1', name: 'Ops', description: null, memberUserIds: [], grants: [] }]],
  ['a group with members and grants', [{
    id: 'g1', name: 'Ops', description: 'the on-call rota', memberUserIds: ['u1', 'u2'],
    grants: [{ role_id: 'role-op', scope_type: 'global', scope_id: null },
             { role_id: 'role-view', scope_type: 'site', scope_id: 's1' }],
  }]],
  ['a group whose name and description carry markup', [{
    id: '<id>', name: '<b>bold</b>', description: '<script>x</script>',
    memberUserIds: [], grants: [{ role_id: 'role-xss', scope_type: 'global', scope_id: null }],
  }]],
  ['several groups', [
    { id: 'g1', name: 'A', description: null, memberUserIds: ['u1'], grants: [] },
    { id: 'g2', name: 'B', description: null, memberUserIds: [], grants: [{ role_id: 'role-op', scope_type: 'router', scope_id: 'r1' }] },
  ]],
];

function runLiveGroups(groups) {
  const tb = { innerHTML: '' };
  const win = { _allRoles: XSS_ROLES, _sitesById: SITES, _allRouters: ROUTERS };
  new Function('window', 'document', '$', '_groupsCache',
    escSrc + '\n' + liveSrc + '\n_renderGroupTable();')(
    win, { createElement: () => ({}) }, () => tb, groups);
  return tb.innerHTML;
}

for (const [name, groups] of GROUP_CASES) {
  checks++;
  const a = runLiveGroups(groups);
  const b = port.groupTableHtml(groups, look);
  if (a !== b) bad.push({ name: 'groups: ' + name, live: a, port: b });
}

// The two panes MUST NOT agree on the empty state — if they ever do, one of them
// has been quietly harmonised and this check would stop noticing.
{
  const usersEmpty = port.accessSummary([], look);
  const groupsEmpty = runLiveGroups([{ id: 'g', name: 'n', description: null, memberUserIds: [], grants: [] }]);
  if (usersEmpty.indexOf('No access') === -1 || groupsEmpty.indexOf('no access granted') === -1) {
    console.error('the two panes no longer phrase "no access" differently — one has been ' +
                  'harmonised, and this check was written on the assumption they differ');
    process.exit(1);
  }
}

// ── the user ROW markup ───────────────────────────────────────────────────
function runLiveUserRow(u) {
  const tr = { innerHTML: '', querySelector: () => ({ addEventListener() {} }) };
  const win = { _allRoles: XSS_ROLES, _sitesById: SITES, _allRouters: ROUTERS };
  return new Function('window', 'document',
    escSrc + '\n' + liveSrc + '\nreturn renderUserRow(arguments[2]).innerHTML;')(
    win, { createElement: () => tr }, u);
}
const USER_ROWS = [
  ['a user with no grants', { id: 'u1', username: 'alice', grants: [] }],
  ['a user with two grants', { id: 'u2', username: 'bob', grants: [
    { role_id: 'role-op', scope_type: 'global', scope_id: null },
    { role_id: 'role-view', scope_type: 'router', scope_id: 'r2' }] }],
  ['a username carrying markup', { id: 'u3', username: '<img src=x>', grants: [] }],
];
for (const [name, u] of USER_ROWS) {
  checks++;
  const a = runLiveUserRow(u);
  const b = port.userRowHtml(u, look);
  if (a !== b) bad.push({ name: 'user row: ' + name, live: a, port: b });
}

// ── the Sites pane ────────────────────────────────────────────────────────
const SITE_ROWS = [
  ['an ordinary site', { id: 's1', name: 'Berlin', description: 'the DC' }, 4],
  // AN EM DASH, not an empty cell — no description and a failed load would
  // otherwise look the same.
  ['a site with no description', { id: 's2', name: 'Nowhere', description: null }, 0],
  ['a site with an empty description', { id: 's3', name: 'Blank', description: '' }, 1],
  ['a site whose name carries markup', { id: '<id>', name: '<b>x</b>', description: '<i>y</i>' }, 2],
];
function runLiveSiteRow(s, n) {
  return new Function('window', 'document',
    escSrc + '\n' + liveSrc + '\nreturn _renderSiteRow(arguments[2], arguments[3]);')(
    { _allRoles: XSS_ROLES, _sitesById: SITES, _allRouters: ROUTERS },
    { createElement: () => ({}) }, s, n);
}
for (const [name, s, n] of SITE_ROWS) {
  checks++;
  const a = runLiveSiteRow(s, n);
  const b = port.siteRowHtml(s, n);
  if (a !== b) bad.push({ name: 'site row: ' + name, live: a, port: b });
}

// ── the sites TABLE, including the branch a populated fixture never reaches ──
//
// `_renderSiteTable` writes into `$('siteTbody')`, so both sides are driven from
// ONE payload and their innerHTML compared. The empty case is listed FIRST
// because it is the one that drifted: the row renderer was gated and this branch
// was not, and "No sites yet. Add one to group your devices." kept saying
// "routers" here for as long as the rename had been upstream.
const SITE_TABLES = [
  ['no sites at all', [], []],
  ['one site with one device', [{ id: 's1', name: 'Depot', description: 'main' }],
    [{ id: 'r1', siteIds: ['s1'] }]],
  // #117: a device in TWO sites counts once in EACH, so these totals do not sum
  // to the device count. A port that counted devices instead of memberships
  // gives 1 and 0 here.
  ['a device held at two sites',
    [{ id: 's1', name: 'Depot', description: null }, { id: 's2', name: 'Annexe', description: '' }],
    [{ id: 'r1', siteIds: ['s1', 's2'] }]],
  // The PRE-#117 SCALAR still counts. A record written before multi-site carries
  // `siteId` and no list.
  ['a pre-#117 scalar record', [{ id: 's1', name: 'Depot', description: null }],
    [{ id: 'r1', siteId: 's1' }]],
  // An EXPLICITLY EMPTY array is "no sites", NOT "fall back to the mirror". A
  // port that tested `siteIds.length` instead of `Array.isArray` counts this
  // device into s1 and the column reads 1 where the live one reads 0.
  ['an emptied membership beside a stale mirror', [{ id: 's1', name: 'Depot', description: null }],
    [{ id: 'r1', siteIds: [], siteId: 's1' }]],
  ['a site nobody is in', [{ id: 's9', name: 'Empty', description: null }],
    [{ id: 'r1', siteIds: ['s1'] }]],
];

function runLiveSiteTable(sites, routers) {
  const tb = { innerHTML: '' };
  new Function('window', 'document', '$', '_sitesCache',
    escSrc + '\n' + liveSrc + '\n_renderSiteTable();')(
    { _allRoles: XSS_ROLES, _sitesById: SITES, _allRouters: routers },
    { createElement: () => ({}) }, () => tb, sites);
  return tb.innerHTML;
}

for (const [name, sites, routers] of SITE_TABLES) {
  checks++;
  const a = runLiveSiteTable(sites, routers);
  const b = port.siteTableHtml(sites, port.siteRouterCounts(routers));
  if (a !== b) bad.push({ name: 'site table: ' + name, live: a, port: b });
}

// BELIEVABILITY. Two ways this loop could pass while comparing nothing.
{
  const empty = runLiveSiteTable([], []);
  const full = runLiveSiteTable([{ id: 's1', name: 'Depot', description: null }],
    [{ id: 'r1', siteIds: ['s1'] }]);
  if (empty === full) {
    throw new Error('the live sites table renders the same thing with and without sites, '
      + 'so every case above compares two identical strings');
  }
  if (!/devices/i.test(empty)) {
    throw new Error('the live empty state no longer mentions devices -- the wording this '
      + 'gate was written for has moved, and the corpus needs rereading rather than the '
      + 'expectation being updated');
  }
  // And the COUNT must actually vary, or the column is unexercised.
  const one = runLiveSiteTable([{ id: 's1', name: 'Depot', description: null }],
    [{ id: 'r1', siteIds: ['s1'] }, { id: 'r2', siteIds: ['s1'] }]);
  if (one === full) {
    throw new Error('one device and two produce the same table, so the count column is '
      + 'not being compared');
  }
}

// ── the site form's device list ─────────────────────────────────────────────
//
// The copy here IS the #117 change: "also in", not "currently in", because
// ticking a device ADDS this site rather than moving the device out of the ones
// it already has. That distinction lives in three words of markup, which is
// exactly the kind of thing a hand-port gets subtly wrong and no fixture
// notices.
const MEMBER_SITES = {
  s1: { id: 's1', name: 'Depot' },
  s2: { id: 's2', name: 'Annexe' },
  s3: { id: 's3', name: 'Overflow' },
};
const MEMBER_CASES = [
  ['no devices at all', [], { id: 's1' }],
  ['a device in this site is ticked', [{ id: 'r1', label: 'One', siteIds: ['s1'] }], { id: 's1' }],
  ['a device in no site is not', [{ id: 'r1', label: 'One', siteIds: [] }], { id: 's1' }],
  // The "also in" line, one site and several. Several is not decoration: the
  // join is what a port gets wrong, and one entry cannot show a separator.
  ['in this site and one other', [{ id: 'r1', label: 'One', siteIds: ['s1', 's2'] }], { id: 's1' }],
  ['in this site and two others',
    [{ id: 'r1', label: 'One', siteIds: ['s1', 's2', 's3'] }], { id: 's1' }],
  ['in another site only', [{ id: 'r1', label: 'One', siteIds: ['s2'] }], { id: 's1' }],
  // ADDING a site: `site` is null, nothing is ticked, and every membership
  // counts as elsewhere.
  ['adding a site, device already placed', [{ id: 'r1', label: 'One', siteIds: ['s2'] }], null],
  ['adding a site, device unplaced', [{ id: 'r1', label: 'One', siteIds: [] }], null],
  // A membership naming a site this browser has not loaded. Rendering
  // "also in undefined" is worse than saying nothing.
  ['a membership this client cannot name',
    [{ id: 'r1', label: 'One', siteIds: ['s2', 's-unknown'] }], { id: 's1' }],
  // The pre-#117 SCALAR still reads.
  ['a pre-#117 scalar record', [{ id: 'r1', label: 'One', siteId: 's2' }], { id: 's1' }],
  // No label: the HOST is shown.
  ['a device with no label', [{ id: 'r1', host: '198.51.100.9', siteIds: [] }], { id: 's1' }],
  // ESCAPING, in the label and in the other site's name.
  ['markup in a label and a site name',
    [{ id: '<id>', label: '<b>x</b>', siteIds: ['s-xss'] }], { id: 's1' }],
  ['several devices at once', [
    { id: 'r1', label: 'One', siteIds: ['s1'] },
    { id: 'r2', label: 'Two', siteIds: ['s2'] },
    { id: 'r3', label: 'Three', siteIds: [] },
  ], { id: 's1' }],
];
const MEMBER_SITES_XSS = { ...MEMBER_SITES, 's-xss': { id: 's-xss', name: '<i>evil</i>' } };

function runLiveMemberRows(routers, site) {
  return new Function('window', 'document',
    escSrc + '\n' + liveSrc + '\nreturn _siteMemberRows(arguments[2], arguments[3]);')(
    { _allRoles: XSS_ROLES, _sitesById: MEMBER_SITES_XSS, _allRouters: routers },
    { createElement: () => ({}) }, routers, site);
}

for (const [name, routers, site] of MEMBER_CASES) {
  checks++;
  const a = runLiveMemberRows(routers, site);
  const b = port.siteMemberRowsHtml(routers, site, MEMBER_SITES_XSS);
  if (a !== b) bad.push({ name: 'site members: ' + name, live: a, port: b });
}

// BELIEVABILITY.
{
  const ticked = runLiveMemberRows([{ id: 'r1', label: 'One', siteIds: ['s1'] }], { id: 's1' });
  const unticked = runLiveMemberRows([{ id: 'r1', label: 'One', siteIds: [] }], { id: 's1' });
  if (ticked === unticked) {
    throw new Error('a device in the site renders the same as one outside it, so every case '
      + 'above compares two identical strings');
  }
  if (!/checked/.test(ticked)) throw new Error('nothing is ever ticked');
  const alsoIn = runLiveMemberRows([{ id: 'r1', label: 'One', siteIds: ['s1', 's2'] }], { id: 's1' });
  if (!/also in/.test(alsoIn)) {
    throw new Error('the live device list no longer says "also in" -- the #117 wording this '
      + 'gate was written for has moved, and the port needs rereading rather than the '
      + 'expectation being updated');
  }
  if (/currently in/.test(alsoIn)) {
    throw new Error('the live device list says "currently in", which is the PRE-#117 wording');
  }
}

// ── the delete confirmation ─────────────────────────────────────────────────
//
// Lifted and RUN with `confirm` recording what it was asked, so the string
// compared is the one the operator sees. The warning is the #117 change in a
// sentence: a device now loses ONE membership rather than its only one, and a
// port keeping the pre-#117 wording would frighten an operator out of a safe act.
const DELETE_CASES = [
  ['an empty site', 'Depot', 0],
  ['one device', 'Depot', 1],
  ['several devices', 'Depot', 4],
  // The name is interpolated RAW into a confirm() -- no escaping, because it is
  // not markup. A quote in the name is therefore visible, and that is the live
  // behaviour rather than an oversight to fix here.
  ['a name with a quote', 'The "Depot"', 2],
  ['a name with markup', '<b>x</b>', 1],
];

function runLiveDeletePrompt(name, count) {
  let asked = null;
  new Function('window', 'document', 'confirm', 'fetch', 'encodeURIComponent', 'loadSites',
    escSrc + '\n' + liveSrc + '\ndeleteSite(arguments[6], arguments[7], arguments[8]);')(
    { _allRoles: XSS_ROLES, _sitesById: SITES, _allRouters: ROUTERS },
    { createElement: () => ({}) },
    (msg) => { asked = msg; return false; },   // ALWAYS CANCEL: no request is made
    () => { throw new Error('deleteSite fetched despite a cancelled confirm'); },
    encodeURIComponent, () => {},
    's1', name, count);
  if (asked === null) throw new Error('deleteSite did not ask for confirmation at all');
  return asked;
}

for (const [label, name, count] of DELETE_CASES) {
  checks++;
  const a = runLiveDeletePrompt(name, count);
  const b = port.siteDeletePrompt(name, count);
  if (a !== b) bad.push({ name: 'site delete prompt: ' + label, live: a, port: b });
}

// BELIEVABILITY.
{
  const none = runLiveDeletePrompt('Depot', 0);
  const some = runLiveDeletePrompt('Depot', 3);
  if (none === some) {
    throw new Error('the device count does not change the prompt, so every case above compares '
      + 'two identical strings');
  }
  if (/0 device/.test(none)) {
    throw new Error('an empty site is warned about "0 device(s)", which reads as though '
      + 'something might be affected');
  }
  if (!/keep any other sites/.test(some)) {
    throw new Error('the live warning no longer says devices keep their other sites -- the #117 '
      + 'wording this gate was written for has moved, and the port needs rereading rather than '
      + 'the expectation being updated');
  }
}

// ── the Roles pane ────────────────────────────────────────────────────────
const ROLE_SETS = [
  ['no roles at all', []],
  ['a builtin role shows a lock and no actions', [
    { id: 'r1', name: 'Administrator', description: null, builtin: true, pages: [], grants: 3 }]],
  // ZERO GRANTS IS AN EM DASH, not "0 grants".
  ['an unused custom role', [
    { id: 'r2', name: 'Auditor', description: 'read only', builtin: false,
      pages: [{ page: 'logs', access: 'read' }], grants: 0 }]],
  ['exactly one grant is singular', [
    { id: 'r3', name: 'One', description: null, builtin: false, pages: [], grants: 1 }]],
  ['several grants are plural', [
    { id: 'r4', name: 'Many', description: null, builtin: false, pages: [], grants: 5 }]],
  ['a role mixing read and write pages', [
    { id: 'r5', name: 'Mixed', description: null, builtin: false, grants: 2,
      pages: [{ page: 'logs', access: 'read' }, { page: 'dns', access: 'write' },
              { page: 'wan', access: 'read' }] }]],
  ['a role name carrying markup', [
    { id: 'r6', name: '<img src=x>', description: '<script>y</script>', builtin: false,
      pages: [], grants: 0 }]],
];
function runLiveRoleTable(roles) {
  const tb = { innerHTML: '' };
  new Function('window', 'document', '$',
    escSrc + '\n' + liveSrc + '\n_renderRoleTable();')(
    { _allRoles: roles, _sitesById: SITES, _allRouters: ROUTERS },
    { createElement: () => ({}) }, () => tb);
  return tb.innerHTML;
}
for (const [name, roles] of ROLE_SETS) {
  checks++;
  const a = runLiveRoleTable(roles);
  const b = port.roleTableHtml(roles);
  if (a !== b) bad.push({ name: 'role table: ' + name, live: a, port: b });
}

// ── the role matrix row ───────────────────────────────────────────────────
const WRITE_CAPABLE = ['dashboard', 'firewall', 'wireless', 'reports', 'devices', 'settings'];
const MATRIX = [
  ['no access selects none', { key: 'logs', title: 'Logs' }, undefined],
  ['read selected', { key: 'logs', title: 'Logs' }, 'read'],
  ['write selected on a write-capable page', { key: 'settings', title: 'Settings' }, 'write'],
  // DISABLED, NOT HIDDEN: the matrix keeps its shape and the reason is visible.
  ['a page with no write actions', { key: 'logs', title: 'Logs' }, undefined],
  ['a write-capable page with no access', { key: 'devices', title: 'Devices' }, undefined],
  ['a page title carrying markup', { key: 'x', title: '<b>t</b>' }, 'read'],
];
function runLiveMatrixRow(page, access) {
  return new Function('window', 'document', '_rolesMeta',
    escSrc + '\n' + liveSrc + '\nreturn _rolePageRow(arguments[3], arguments[4]);')(
    { _allRoles: XSS_ROLES, _sitesById: SITES, _allRouters: ROUTERS },
    { createElement: () => ({}) }, { writeCapable: WRITE_CAPABLE }, page, access);
}
for (const [name, page, access] of MATRIX) {
  checks++;
  const a = runLiveMatrixRow(page, access);
  const b = port.rolePageRowHtml(page, access, WRITE_CAPABLE);
  if (a !== b) bad.push({ name: 'matrix row: ' + name, live: a, port: b });
}

// A guard: the disabled state must actually appear somewhere, or the
// "disabled not hidden" rule is untested.
{
  const dead = runLiveMatrixRow({ key: 'logs', title: 'Logs' }, undefined);
  if (dead.indexOf('disabled') === -1) {
    console.error('the LIVE matrix row never emitted a disabled segment — the ' +
                  '"disabled not hidden" rule is not being exercised');
    process.exit(1);
  }
}

// ── the grant editor's markup ─────────────────────────────────────────────
const EDITOR_OPTS = { roles: XSS_ROLES, sitesById: SITES, routers: ROUTERS };
const EDITOR_CASES = [
  ['no grants yet', undefined],
  ['an empty grant list', []],
  ['one global grant', [{ id: 1, role_id: 'role-op', scope_type: 'global', scope_id: null }]],
  ['grants at every scope', [
    { id: 1, role_id: 'role-op', scope_type: 'global', scope_id: null },
    { id: 2, role_id: 'role-view', scope_type: 'site', scope_id: 's1' },
    { id: 3, role_id: 'role-op', scope_type: 'router', scope_id: 'r2' },
  ]],
  ['a grant whose role was deleted', [{ id: 4, role_id: 'gone', scope_type: 'global', scope_id: null }]],
  ['a role name carrying markup', [{ id: 5, role_id: 'role-xss', scope_type: 'global', scope_id: null }]],
];
function runLiveEditor(grants) {
  const container = { innerHTML: '', onclick: null };
  new Function('window', 'document',
    escSrc + '\n' + liveSrc +
    '\n_renderGrantEditor(arguments[2], "user", "u1", arguments[3], {});')(
    { _allRoles: XSS_ROLES, _sitesById: SITES, _allRouters: ROUTERS },
    { createElement: () => ({}) }, container, grants);
  return container.innerHTML;
}
for (const [name, grants] of EDITOR_CASES) {
  checks++;
  const a = runLiveEditor(grants);
  const b = port.grantEditorHtml(grants, EDITOR_OPTS);
  if (a !== b) bad.push({ name: 'grant editor: ' + name, live: a, port: b });
}

// ── THE THREE PHRASINGS MUST STAY DISTINCT ────────────────────────────────
//
// The Users card, the Groups table and this editor each render `role — scope`
// differently, and the escaping differs between them. If two ever produced the
// same markup for the same grant, one has been quietly harmonised — a behaviour
// change dressed as a tidy-up — and these cases would stop meaning anything.
{
  const g = [{ id: 1, role_id: 'role-op', scope_type: 'global', scope_id: null }];
  const users = port.accessSummary(g, look);
  const groups = runLiveGroups([{ id: 'g', name: 'n', description: null, memberUserIds: [], grants: g }]);
  const editor = runLiveEditor(g);
  if (users === editor) {
    console.error('the Users card and the grant editor now render a grant identically — ' +
                  'one has been harmonised, and these checks assume they differ');
    process.exit(1);
  }
  if (groups.indexOf('<br>') === -1 && groups.indexOf('Operator — all routers') === -1) {
    console.error('the Groups table no longer joins with <br> or escapes the combined ' +
                  'string — its distinct phrasing is gone');
    process.exit(1);
  }
}

if (bad.length) {
  for (const d of bad) {
    console.error('\n' + d.name);
    console.error('  live: ' + d.live);
    console.error('  port: ' + d.port);
  }
  process.exit(1);
}

console.log('access summaries match the live card (' + checks + ' cases, ' +
            withGrants + ' with grants, the users/groups/sites/roles tables, escaping included)');
