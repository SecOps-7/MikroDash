'use strict';
/**
 * The User, Group and Role save paths, PORT against LIVE, from one harness.
 *
 * ---- WHY THESE THREE TOGETHER ---------------------------------------------
 *
 * `saveUser`, `saveGroup` and `saveRole` sit within three hundred lines of each
 * other in `public/app.js` and read almost identically. They differ in six ways,
 * and every one is invisible until it is wrong — a different required-field
 * message, a different fallback message, a different set of trimmed fields, an
 * omitted-rather-than-empty password, one form that checks the HTTP status and
 * two that do not, and one that stays open after a create.
 *
 * Checking them one at a time is how you convince yourself they are the same
 * function. Driving all three through one harness is how the differences stay
 * visible.
 *
 * ---- NOT THE OTHER TWO PRINCIPALS GATES -----------------------------------
 *
 * `principals-card-check` covers the card's SIZER and TAB STRIP.
 * `principals-wiring-check` is port-only glue — the fetches, the caches, the
 * order — and says so in its own header. Neither touches the three save
 * functions, which were unreachable until this port served their endpoints.
 *
 * ---- THE LIVE FUNCTIONS ARE SLICED, NOT REQUIRED --------------------------
 *
 * They live inside a several-thousand-line IIFE that expects a browser, so each
 * is lifted by CONTENT ANCHOR and run with a stub for the handful of free names
 * it uses: `$`, `document.getElementById`, `fetch`, the form's error setter, and
 * the list reloader. Their bodies are the file's own text — nothing about the
 * rule is retyped here.
 *
 * ---- WHAT IS COMPARED -----------------------------------------------------
 *
 * The REQUEST (method, url, body) and the OUTCOME (error text, whether the form
 * closed, whether it switched to edit mode, whether the list reloaded). Not the
 * DOM: the port's half deliberately makes no DOM calls, which is what lets it be
 * compared at all.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/principal-forms-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('principal-forms-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const appjs = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const lines = appjs.split('\n');

/** Lift one function by its declaration line, walking to the matching brace. */
function slice(decl) {
  // NO SOURCE, NO SLICE. Its callers' results are frozen, so this is never
  // reached on replay — but without it the brace walk below indexes `lines[-1]`
  // and dies with `lines[i] is not iterable`, which is a confusing way to say
  // "there is no source".
  if (!LIFT.hasReference(ROOT)) return '';
  const at = lines.findIndex((l) => l.trim().startsWith(decl));
  if (at < 0) throw new Error(`anchor lost: ${decl}`);
  let depth = 0;
  for (let i = at; i < lines.length; i++) {
    for (const ch of lines[i]) {
      if (ch === '{') depth++;
      else if (ch === '}') depth--;
    }
    if (depth === 0 && i > at) return lines.slice(at, i + 1).join('\n');
  }
  throw new Error(`could not find the end of ${decl}`);
}

// FROZEN — each entry is EXECUTED by `new Function` below, so the three lifted
// sources are what must survive.
const LIVE_SRC = G.value('LIVE_SRC', () => ({
  user: slice('function saveUser()'),
  group: slice('function saveGroup()'),
  role: slice('function saveRole()'),
}));
for (const [__k, __v] of Object.entries(LIVE_SRC)) {
  if (typeof __v !== 'string' || __v.length < 40) {
    throw new Error('the recorded ' + __k + ' source is empty — the golden is broken');
  }
}
// The slices must still contain the things this gate is about, or an anchor
// drifted onto a shorter function and every comparison would be two no-ops.
if (LIFT.hasReference(ROOT)) if (!/Username required/.test(LIVE_SRC.user)) throw new Error('the user slice lost its message');
if (LIFT.hasReference(ROOT)) if (!/Name is required/.test(LIVE_SRC.group)) throw new Error('the group slice lost its message');
if (LIFT.hasReference(ROOT)) if (!/Name is required/.test(LIVE_SRC.role)) throw new Error('the role slice lost its message');
if (LIFT.hasReference(ROOT)) if (!/r\.ok && j\.ok/.test(LIVE_SRC.group)) {
  throw new Error('the group slice no longer checks the HTTP status — that asymmetry is half of '
    + 'what this gate exists to pin');
}

// ---- The port's module -----------------------------------------------------
//
// COMPILED WITH ESBUILD, which is what actually builds the app — not with a
// regex over the source. The first version of this gate stripped types by hand
// and got as far as `const FAILED = { ..., reload;` before failing to parse: a
// type annotation and an object property look alike enough that a pattern which
// removes one removes the other. A gate whose harness cannot read the file it is
// checking is worse than no gate, and this is the compiler the bundle uses.
const esbuild = require(path.join(ROOT, 'web', 'node_modules', 'esbuild'));
const portJs = esbuild.transformSync(
  fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'principal-forms.ts'), 'utf8'),
  { loader: 'ts', format: 'cjs' },
).code;

const moduleShim = { exports: {} };
vm.runInNewContext(
  portJs,
  { JSON, encodeURIComponent, Object, module: moduleShim, exports: moduleShim.exports },
  { filename: 'principal-forms.ts' },
);
const port = moduleShim.exports;
for (const name of ['userSavePlan', 'userSaveOutcome', 'groupSavePlan', 'groupSaveOutcome',
  'roleSavePlan', 'roleSaveOutcome', 'userDeletePrompt', 'groupDeletePrompt',
  'roleDeletePrompt', 'groupMembersHtml', 'rolePagesFrom',
  'grantAddPlan', 'grantDeletePlan', 'grantOutcome']) {
  if (typeof port[name] !== 'function') {
    throw new Error(`the port module does not export ${name}; the harness would compare against `
      + 'nothing');
  }
}

// ---- Driving the live functions -------------------------------------------
function runLive(kind, fields, response) {
  const log = { error: '', request: null, closed: false, switchedToEdit: false, reloaded: false };
  const values = {
    uf_id: fields.id, uf_username: fields.username, uf_password: fields.password,
    gf_id: fields.id, gf_name: fields.name, gf_description: fields.description,
    rf_id: fields.id, rf_name: fields.name, rf_description: fields.description,
  };
  const els = {};
  for (const id of Object.keys(values)) {
    els[id] = { value: values[id] === undefined ? '' : values[id], placeholder: '' };
  }
  for (const id of ['groupFormWrap', 'userFormWrap', 'roleFormWrap']) {
    els[id] = { classList: { remove: () => { log.closed = true; }, add: () => {} } };
  }
  els.gf_members = {
    querySelectorAll: () => (fields.memberUserIds || []).map((m) => ({ getAttribute: () => m })),
  };

  const ctx = {
    JSON, Promise, Array, Object, encodeURIComponent, String,
    console: { log() {}, warn() {}, error() {} },
    document: { getElementById: (id) => els[id] || null },
    $: (id) => els[id] || null,
    fetch: (url, init) => {
      log.request = {
        url,
        method: (init && init.method) || 'GET',
        body: init && init.body ? JSON.parse(init.body) : null,
      };
      return Promise.resolve({
        ok: response.httpOk !== false,
        json: () => Promise.resolve(response.body),
      });
    },
    _userFormError: (m) => { log.error = m; },
    _groupFormError: (m) => { log.error = m; },
    _roleFormError: (m) => { log.error = m; },
    // The grant renderer is what "switched to edit mode" looks like from here.
    _renderUserGrants: () => { log.switchedToEdit = true; },
    _collectRolePages: () => fields.pages || [],
    loadUsers: () => { log.reloaded = true; },
    loadGroups: () => { log.reloaded = true; },
    loadRoles: () => { log.reloaded = true; },
    hideRoleForm: () => { log.closed = true; },
  };
  ctx.globalThis = ctx;

  const fn = vm.runInNewContext(
    `${LIVE_SRC[kind]}\nsave${kind[0].toUpperCase()}${kind.slice(1)};`,
    ctx, { filename: `app.js#save${kind}` });
  fn();
  return log;
}

/** Settle the promise chain the live function started. */
async function settle() {
  for (let i = 0; i < 6; i++) await Promise.resolve();
}

// ---- Driving the port ------------------------------------------------------
function runPort(kind, fields, response) {
  const log = { error: '', request: null, closed: false, switchedToEdit: false, reloaded: false };
  const plan = kind === 'user'
    ? port.userSavePlan({
      id: fields.id || '', username: fields.username || '', password: fields.password || '',
    })
    : kind === 'group'
      ? port.groupSavePlan({
        id: fields.id || '', name: fields.name || '',
        description: fields.description || '', memberUserIds: fields.memberUserIds || [],
      })
      : port.roleSavePlan({
        id: fields.id || '', name: fields.name || '',
        description: fields.description || '', pages: fields.pages || [],
      });

  if (plan.error) {
    log.error = plan.error;
    return log;
  }
  log.request = { url: plan.url, method: plan.method, body: plan.body };

  const outcome = kind === 'user'
    ? port.userSaveOutcome(Boolean((fields.id || '').trim()), response)
    : kind === 'group' ? port.groupSaveOutcome(response) : port.roleSaveOutcome(response);
  log.error = outcome.error;
  log.closed = outcome.close;
  log.switchedToEdit = outcome.switchToEdit;
  log.reloaded = outcome.reload;
  return log;
}

// ---- The scenarios ---------------------------------------------------------
const OK_USER = { httpOk: true, body: { ok: true, user: { id: 'u-new' } } };
const OK = { httpOk: true, body: { ok: true } };
const DENIED = { httpOk: true, body: { ok: false, error: 'Username already exists' } };
const BARE_FAIL = { httpOk: true, body: { ok: false } };
const SERVER_ERROR = { httpOk: false, body: { ok: false } };
// A body that says OK behind a NON-OK status. Contrived, and it is the only
// response that separates the group form from the other two: with `ok: false` in
// the body, every form fails and the `r.ok &&` half of the group's check is
// unreachable. Both of the HTTP-status mutants survived until this existed.
const HTTP_ERROR_OK_BODY = { httpOk: false, body: { ok: true } };

const SCENARIOS = [
  // ── The user form ───────────────────────────────────────────────────────
  ['user', 'a create', { id: '', username: 'someone', password: 'a-password' }, OK_USER],
  ['user', 'a create with no password', { id: '', username: 'someone', password: '' }, OK_USER],
  ['user', 'an edit', { id: 'u-1', username: 'someone', password: '' }, OK],
  ['user', 'an edit that sets a password', { id: 'u-1', username: 'someone', password: 'new' }, OK],
  ['user', 'a blank username', { id: '', username: '', password: 'x' }, OK],
  ['user', 'a username of spaces', { id: '', username: '   ', password: 'x' }, OK],
  ['user', 'a username with surrounding spaces', { id: '', username: '  someone  ', password: '' }, OK_USER],
  ['user', 'an id with surrounding spaces', { id: '  u-1  ', username: 'someone', password: '' }, OK],
  ['user', 'a refusal with a message', { id: '', username: 'someone', password: 'x' }, DENIED],
  ['user', 'a refusal with NO message', { id: '', username: 'someone', password: 'x' }, BARE_FAIL],
  ['user', 'a create whose response carries no user', { id: '', username: 'someone', password: 'x' }, OK],
  ['user', 'a password of spaces is NOT trimmed away', { id: 'u-1', username: 'someone', password: '   ' }, OK],
  // AN EDIT whose response carries a record. It must NOT switch to edit mode —
  // it is already in it, and re-rendering the grant editor would discard
  // whatever the operator had part-way through. Without this case, dropping the
  // `!hadId` half of the guard survives.
  ['user', 'an edit whose response carries a record', { id: 'u-1', username: 'someone', password: '' }, OK_USER],
  // The user form does NOT consult the HTTP status, unlike the group form. See
  // HTTP_ERROR_OK_BODY.
  ['user', 'a non-OK status with an ok body still succeeds', { id: 'u-1', username: 'someone', password: '' }, HTTP_ERROR_OK_BODY],

  // ── The group form ──────────────────────────────────────────────────────
  ['group', 'a create', { id: '', name: 'Support', description: '', memberUserIds: [] }, OK],
  ['group', 'a create with members', { id: '', name: 'Support', description: 'desk', memberUserIds: ['u-1', 'u-2'] }, OK],
  ['group', 'an edit', { id: 'g-1', name: 'Support', description: '', memberUserIds: ['u-1'] }, OK],
  ['group', 'an id needing url encoding', { id: 'g/1', name: 'Support', description: '', memberUserIds: [] }, OK],
  ['group', 'a blank name', { id: '', name: '', description: 'x', memberUserIds: [] }, OK],
  ['group', 'a name of spaces', { id: '', name: '  ', description: '', memberUserIds: [] }, OK],
  ['group', 'surrounding spaces are trimmed', { id: '', name: '  Support  ', description: '  desk  ', memberUserIds: [] }, OK],
  ['group', 'a refusal with a message', { id: '', name: 'Support', description: '', memberUserIds: [] }, DENIED],
  ['group', 'a refusal with NO message', { id: '', name: 'Support', description: '', memberUserIds: [] }, BARE_FAIL],
  // THE ASYMMETRY: only this form consults the HTTP status, and only
  // HTTP_ERROR_OK_BODY can show it — with `ok: false` in the body, all three
  // forms fail and the `r.ok &&` half is unreachable.
  ['group', 'an HTTP error', { id: '', name: 'Support', description: '', memberUserIds: [] }, SERVER_ERROR],
  ['group', 'a non-OK status with an ok body FAILS here', { id: '', name: 'Support', description: '', memberUserIds: [] }, HTTP_ERROR_OK_BODY],
  // The group form does NOT trim its id, where the user form does. A hidden
  // field holding whitespace therefore produces a different URL on each — and
  // without this case, adding a trim here survives.
  ['group', 'an id with surrounding spaces is NOT trimmed', { id: '  g-1  ', name: 'Support', description: '', memberUserIds: [] }, OK],

  // ── The role form ───────────────────────────────────────────────────────
  ['role', 'a create', { id: '', name: 'Helpdesk', description: '', pages: [] }, OK],
  ['role', 'a create with a page matrix', { id: '', name: 'Helpdesk', description: '', pages: [{ page: 'dashboard', access: 'read' }] }, OK],
  ['role', 'an edit', { id: 'r-1', name: 'Helpdesk', description: 'x', pages: [] }, OK],
  ['role', 'an id needing url encoding', { id: 'r/1', name: 'Helpdesk', description: '', pages: [] }, OK],
  ['role', 'a blank name', { id: '', name: '', description: '', pages: [] }, OK],
  ['role', 'surrounding spaces are trimmed', { id: '', name: '  Helpdesk  ', description: '  x  ', pages: [] }, OK],
  ['role', 'a refusal with a message', { id: 'r-1', name: 'Helpdesk', description: '', pages: [] }, DENIED],
  ['role', 'a refusal with NO message', { id: 'r-1', name: 'Helpdesk', description: '', pages: [] }, BARE_FAIL],
];

(async () => {
  const problems = [];

  // ── BELIEVABILITY, before any comparison ──────────────────────────────
  //
  // A harness that drove neither side would report every scenario identical.
  {
    const probe = runLive('user', { id: '', username: 'someone', password: 'p' }, OK_USER);
    await settle();
    if (!probe.request || probe.request.url !== '/api/users' || probe.request.method !== 'POST') {
      problems.push('the harness never saw the LIVE user form make its request; it is not driving '
        + `the code. Log: ${JSON.stringify(probe)}`);
    }
  }
  {
    const probe = runLive('user', { id: '', username: '', password: 'p' }, OK);
    await settle();
    if (probe.error !== 'Username required') {
      problems.push(`the LIVE user form did not refuse a blank username (${probe.error || 'no '
        + 'error'}); either the harness is not reaching the check or the rule changed`);
    }
  }

  let compared = 0;
  if (!problems.length) {
    for (const [kind, why, fields, response] of SCENARIOS) {
      const a = runLive(kind, fields, response);
      await settle();
      const b = runPort(kind, fields, response);
      compared++;
      if (JSON.stringify(a) !== JSON.stringify(b)) {
        problems.push(`${kind}: ${why}\n      live=${JSON.stringify(a)}\n      port=${JSON.stringify(b)}`);
      }
    }
  }

  // ── THE DELETE PROMPTS, compared as text ──────────────────────────────
  //
  // A confirmation an operator has learned to read at a glance is one they stop
  // reading, so changing the words is a user-visible change even though nothing
  // about the request differs.
  const PROMPTS = [
    ['userDeletePrompt', port.userDeletePrompt('someone'),
      'Delete user "someone"? This cannot be undone.'],
    ['groupDeletePrompt', port.groupDeletePrompt('Support'),
      'Delete group "Support"?\n\nIts members keep any access granted to them directly.'],
    ['roleDeletePrompt', port.roleDeletePrompt('Helpdesk'),
      'Delete the role "Helpdesk"?'],
  ];
  for (const [name, got, want] of PROMPTS) {
    // The expected text is asserted to still be IN app.js, so this cannot drift
    // into a pair of matching constants that both disagree with the live app.
    const needle = want.split('"')[0];
    if (LIFT.hasReference(ROOT)) if (!appjs.includes(needle)) {
      problems.push(`${name}: the live app no longer contains ${JSON.stringify(needle)}, so the `
        + 'expected text below is stale');
    }
    if (got !== want) {
      problems.push(`${name}:\n      port=${JSON.stringify(got)}\n      live=${JSON.stringify(want)}`);
    }
  }

  // ── THE GROUP MEMBER LIST, against the live markup ────────────────────
  //
  // Lifted from `showGroupForm` rather than retyped: it is one expression, and
  // the checkbox's `data-member` attribute is what `saveGroup` reads back — so a
  // difference here breaks the save silently rather than visibly.
  {
    const memberSrc = slice('function showGroupForm(group)');
    if (LIFT.hasReference(ROOT)) if (!/data-member="/.test(memberSrc)) {
      problems.push('the group form no longer emits data-member checkboxes, which is what '
        + 'saveGroup reads back');
    }
    const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;')
      .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    const live = (users, members) => (users.length
      ? users.map((u) =>
        '<label style="display:flex;align-items:center;gap:.4rem;margin-bottom:.2rem">'
        + '<input type="checkbox" data-member="' + esc(u.id) + '"'
        + (members.indexOf(u.id) !== -1 ? ' checked' : '') + '>'
        + '<span>' + esc(u.username) + '</span></label>').join('')
      : '<span style="color:var(--text-muted)">No users yet.</span>');

    for (const [why, users, members] of [
      ['no users at all', [], []],
      ['one user, not a member', [{ id: 'u-1', username: 'alice' }], []],
      ['one user, a member', [{ id: 'u-1', username: 'alice' }], ['u-1']],
      ['two users, one a member', [{ id: 'u-1', username: 'alice' },
        { id: 'u-2', username: 'bob' }], ['u-2']],
      ['a member id that is not in the list', [{ id: 'u-1', username: 'alice' }], ['u-9']],
      ['a username needing escaping', [{ id: 'u-1', username: 'a<b>&"c' }], ['u-1']],
      ['an id needing escaping', [{ id: 'u"1', username: 'alice' }], ['u"1']],
    ]) {
      const a = live(users, members);
      const b = port.groupMembersHtml(users, members, esc);
      compared++;
      if (a !== b) {
        problems.push(`group members: ${why}\n      live=${JSON.stringify(a)}\n      port=${JSON.stringify(b)}`);
      }
    }
  }

  // ── READING THE ROLE MATRIX BACK ──────────────────────────────────────
  //
  // The live loop is DOM-bound, so what is compared is the rule it applies to
  // the levels it read: keep read and write, drop everything else.
  {
    const collectSrc = slice('function _collectRolePages()');
    if (LIFT.hasReference(ROOT)) if (!/level === 'read' \|\| level === 'write'/.test(collectSrc)) {
      throw new Error('the live _collectRolePages no longer filters on read/write; the rule this '
        + 'gate pins has changed');
    }
    const liveRule = (rows) => rows
      .filter((r) => r.level === 'read' || r.level === 'write')
      .map((r) => ({ page: r.page, access: r.level }));

    for (const [why, rows] of [
      ['an empty matrix', []],
      ['every page none', [{ page: 'a', level: 'none' }, { page: 'b', level: 'none' }]],
      ['one read', [{ page: 'a', level: 'read' }]],
      ['one write', [{ page: 'a', level: 'write' }]],
      ['a mix', [{ page: 'a', level: 'read' }, { page: 'b', level: 'none' },
        { page: 'c', level: 'write' }]],
      // NOTHING SELECTED reads as 'none' on the live side and is dropped. Not a
      // state the UI can reach; it is what the code does.
      ['a row with no segment selected', [{ page: 'a', level: 'none' }]],
      ['an unrecognised level is dropped', [{ page: 'a', level: 'admin' }]],
    ]) {
      const a = liveRule(rows);
      const b = port.rolePagesFrom(rows);
      compared++;
      if (JSON.stringify(a) !== JSON.stringify(b)) {
        problems.push(`role matrix: ${why}\n      live=${JSON.stringify(a)}\n      port=${JSON.stringify(b)}`);
      }
    }
  }

  // ── THE GRANT EDITOR'S ADD AND REMOVE ─────────────────────────────────
  //
  // The live handler is inside `_renderGrantEditor`'s `container.onclick`, which
  // cannot be lifted as a function — so the two REQUESTS it builds are compared
  // instead, against the same source text this gate asserts is still there.
  {
    const editorSrc = slice('function _renderGrantEditor(container, principalType, principalId, grants, opts)');
    if (LIFT.hasReference(ROOT)) if (!/parts\[1\] \|\| ''/.test(editorSrc)) {
      throw new Error("the live editor no longer reads the scope id as parts[1]; the truncation "
        + 'this gate pins has changed');
    }
    if (LIFT.hasReference(ROOT)) if (!/\|\| 'global:'/.test(editorSrc)) {
      throw new Error('the live editor no longer defaults the scope to global:');
    }
    if (LIFT.hasReference(ROOT) && (!/Could not grant access/.test(editorSrc)
      || !/Could not remove access/.test(editorSrc))) {
      throw new Error('the editor lost one of its two fallback messages, which are different on '
        + 'purpose');
    }

    // The live rule, transcribed from the two lines above and asserted to match
    // the file's own text.
    const liveAdd = (principalType, principalId, roleId, scopeValue) => {
      const parts = (scopeValue || 'global:').split(':');
      return {
        principalType, principalId, roleId,
        scopeType: parts[0], scopeId: parts[1] || '',
      };
    };

    for (const [why, pt, pid, role, scope] of [
      ['a global grant', 'user', 'u-1', 'readonly', 'global:'],
      ['an empty picker defaults to global', 'user', 'u-1', 'readonly', ''],
      ['a site grant', 'user', 'u-1', 'readonly', 'site:site-1'],
      ['a router grant', 'group', 'g-1', 'operator', 'router:rtr-1'],
      // THE TRUNCATION. A scope id containing a colon keeps only its first
      // segment, because the live code reads `parts[1]` rather than rejoining.
      // Reproduced, not fixed — see principal-forms.ts.
      ['a scope id containing a colon is truncated', 'user', 'u-1', 'readonly', 'site:a:b'],
      ['a global option that still carries an id', 'user', 'u-1', 'readonly', 'global:leftover'],
    ]) {
      const a = liveAdd(pt, pid, role, scope);
      const plan = port.grantAddPlan(pt, pid, role, scope, 'Save it first');
      compared++;
      if (plan.error) {
        problems.push(`grant add: ${why}: refused with ${JSON.stringify(plan.error)}`);
        continue;
      }
      if (plan.method !== 'POST' || plan.url !== '/api/grants') {
        problems.push(`grant add: ${why}: ${plan.method} ${plan.url}, want POST /api/grants`);
      }
      if (JSON.stringify(a) !== JSON.stringify(plan.body)) {
        problems.push(`grant add: ${why}\n      live=${JSON.stringify(a)}\n      port=${JSON.stringify(plan.body)}`);
      }
    }

    // AN UNSAVED PRINCIPAL is refused before any request, with the sentence the
    // form supplied.
    compared++;
    {
      const plan = port.grantAddPlan('user', '', 'readonly', 'global:', 'Save the user first');
      if (plan.error !== 'Save the user first') {
        problems.push('grant add: an unsaved principal was not refused with its own message: '
          + JSON.stringify(plan));
      }
      if (plan.url) {
        problems.push('grant add: an unsaved principal still produced a request');
      }
    }

    // REMOVE, and the url encoding the live handler applies.
    for (const [why, id, want] of [
      ['a plain id', 'g-1', '/api/grants/g-1'],
      ['an id needing encoding', 'g/1', '/api/grants/g%2F1'],
    ]) {
      compared++;
      const plan = port.grantDeletePlan(id);
      if (plan.method !== 'DELETE' || plan.url !== want) {
        problems.push(`grant remove: ${why}: ${plan.method} ${plan.url}, want DELETE ${want}`);
      }
    }

    // THE TWO FALLBACKS ARE DIFFERENT, and the server's own message wins.
    for (const [why, res, kind, want] of [
      ['add, no message', { body: { ok: false } }, 'add', 'Could not grant access'],
      ['remove, no message', { body: { ok: false } }, 'remove', 'Could not remove access'],
      ['add, no body at all', { body: null }, 'add', 'Could not grant access'],
      ['the server message wins', { body: { ok: false, error: 'No such site' } }, 'add', 'No such site'],
      ['a success reports nothing', { body: { ok: true } }, 'add', ''],
    ]) {
      compared++;
      const out = port.grantOutcome(res, kind);
      if (out.error !== want) {
        problems.push(`grant outcome: ${why}: ${JSON.stringify(out.error)}, want ${JSON.stringify(want)}`);
      }
      // THE EDITOR REFRESHES EITHER WAY — the live handler calls refresh() on
      // both branches, and a stale list after a failed change reads as success.
      if (!out.refresh) {
        problems.push(`grant outcome: ${why}: did not refresh`);
      }
    }
  }

  if (problems.length) {
    console.error('principal-forms-check: the port and the live forms disagree\n');
    for (const p of problems) console.error('  - ' + p + '\n');
    process.exit(1);
  }
  console.log(`principal-forms-check: ${compared} scenarios across three forms, plus three delete `
    + 'prompts — the port matches the live app');
})();
