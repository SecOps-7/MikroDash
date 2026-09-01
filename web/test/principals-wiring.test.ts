// Moved from the principals-wiring check when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * The Access Management card's WIRING — the fetches, the caches, the order.
 *
 * ── PORT-ONLY, like sites-card-check ────────────────────────────────────────
 *
 * Every DECISION this card makes is compared against the live app elsewhere:
 * `access-summary-check` (67 cases over the four tables and their summaries),
 * `principals-card-check` (the sizer and the tab strip) and
 * `auth-visibility-check` (who may see it). What is left is glue, and glue has
 * no seam to lift the original through — `loadUsers` and friends sit inside an
 * 1,850-line IIFE. This catches a regression, not a divergence, and says so.
 *
 * ── THE ASSERTION IT EXISTS FOR ─────────────────────────────────────────────
 *
 * ROLES LOAD BEFORE USERS AND GROUPS. `roleName` answers "unknown role" for a
 * role it cannot find — correct for a deleted one, WRONG for one that has not
 * arrived — so a card that fired all three fetches at once would render a
 * grant table full of "unknown role" and then quietly fix itself. The failure is
 * invisible in a screenshot taken a second later.
 *
 *   node tools/principals-wiring-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id) {
  const classes = new Set();
  const listeners = {};
  const kids = [];
  const node = {
    id, value: '', textContent: '', innerHTML: '', style: {}, hidden: false,
    children: kids,
    setAttribute: (k, v) => { node['__' + k] = v; },
    getAttribute: (k) => (('__' + k) in node ? node['__' + k] : null),
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (listeners[ev] || []).forEach((fn) => fn(arg || {})),
    appendChild: (c) => { kids.push(c); node.innerHTML += c.innerHTML; },
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    has: (c) => classes.has(c),
    querySelectorAll: () => [],
    closest: () => null,
    // The forms focus their first field on open. A stub without it throws
    // INSIDE the click handler, which reads as "the form did not open".
    focus: () => { node.focused = true; },
    getBoundingClientRect: () => ({ top: 0, height: 100, bottom: 100 }),
    offsetTop: 0, offsetHeight: 100,
  };
  return node;
}

const IDS = ['userTbody', 'groupTbody', 'roleTbody', 'ptabBtn-users', 'ptab-users',
  'principalsCard', 'principalsGraph', 'authModeWrap', 'sf_error',
  // ── THE THREE FORMS, added 2026-08-28 when their write paths were wired ──
  //
  // `element-coverage-audit` is what asked for these: the card touched twenty
  // elements and a gate covered three, which it calls "the shape that hid the
  // VPN and Logs pages". An element nothing drives is an element that can stop
  // being bound without anything noticing.
  'addUserBtn', 'uf_id', 'uf_username', 'uf_password', 'uf_error', 'uf_grants',
  'uf_save', 'uf_cancel', 'uf_title', 'userFormWrap',
  'addGroupBtn', 'gf_id', 'gf_name', 'gf_description', 'gf_error', 'gf_members',
  'gf_grants', 'gf_save', 'gf_cancel', 'gf_title', 'groupFormWrap',
  'addRoleBtn', 'rf_id', 'rf_name', 'rf_description', 'rf_error', 'rf_pages',
  'rf_save', 'rf_cancel', 'rf_title', 'roleFormWrap'];

function makeDoc() {
  const els = {};
  IDS.forEach((id) => { els[id] = makeEl(id); });
  return {
    els,
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [],
    addEventListener: () => {},
    createElement: () => makeEl(''),
  };
}

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-principals.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings-principals.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const settle = () => new Promise((r) => setImmediate(r));
const later = (ms) => new Promise((r) => setTimeout(r, ms));

const ROLES = [{ id: 'manager', name: 'Manager', description: null, builtin: false,
  pages: [{ page: 'devices', access: 'write' }], grants: 2 }];
const USERS = [{ id: 'u-1', username: 'alice',
  grants: [{ role_id: 'manager', scope_type: 'router', scope_id: 'r1' }] }];
const GROUPS = [{ id: 'g-1', name: 'Ops', description: null, memberUserIds: ['u-1'],
  grants: [{ role_id: 'manager', scope_type: 'global', scope_id: null }] }];
const PAGES = [{ key: 'devices', title: 'Devices' }, { key: 'logs', title: 'Logs' }];
// `devices` is write-capable and `logs` is not, so the matrix has one row with
// three live segments and one whose Write is disabled — the shape the live
// `_rolePageRow` renders, and the reason writeCapablePages is sent at all.
const WRITE_CAPABLE = ['devices'];

/** Mount the card. `slowRoles` delays /api/roles so the ORDER is observable. */
function mount(opts) {
  const o = opts || {};
  const doc = makeDoc();
  const order = [];
  global.document = doc;
  global.window = {};
  global.fetch = (url) => {
    const name = url.replace('/api/', '');
    order.push(name + ':start');
    const body = { roles: ROLES, users: USERS, groups: GROUPS }[name] || [];
    // `/api/roles` carries THREE things, not one: the roles, the page catalogue
    // and the write-capable list. The role FORM's matrix is built from the last
    // two, and a fixture that sent only the roles produced an empty matrix —
    // which `saveRole` would read back as "revoke every page".
    const extra = name === 'roles'
      ? { pages: PAGES, writeCapablePages: WRITE_CAPABLE }
      : {};
    const reply = {
      ok: true,
      json: () => {
        order.push(name + ':done');
        return Promise.resolve(Object.assign({ ok: true, [name]: body }, extra));
      },
    };
    if (name === 'roles' && o.slowRoles) return later(o.slowRoles).then(() => reply);
    return Promise.resolve(reply);
  };

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  mod.initPrincipalsCard({
    routers: () => [{ id: 'r1', label: 'One' }],
    mayManage: () => o.mayManage,
    authMode: () => o.authMode || 'modern',
  });
  return { doc, order, mod };
}

const problems = [];
let checks = 0;
function check(name, fn) {
  checks++;
  try { fn(); } catch (e) { problems.push(name + ': ' + e.message); }
}

(async () => {
  // 1. ── THE ORDER ─────────────────────────────────────────────────────────
  //    With a SLOW /api/roles, a chained load and a concurrent one differ. With
  //    an instant one they do not — which is the trap `auth-visibility-check`'s
  //    header already records, so the delay is not optional here either.
  {
    const { order } = mount({ mayManage: true, slowRoles: 120 });
    await later(400);
    check('roles finish before users start', () => {
      const rolesDone = order.indexOf('roles:done');
      const usersStart = order.indexOf('users:start');
      assert.ok(rolesDone >= 0, 'roles never loaded: ' + order.join(', '));
      assert.ok(usersStart >= 0, 'users never loaded: ' + order.join(', '));
      assert.ok(rolesDone < usersStart,
        'users started before roles finished -- every grant row would render '
        + '"unknown role" and then quietly fix itself: ' + order.join(', '));
    });
    check('groups wait too', () => {
      const rolesDone = order.indexOf('roles:done');
      const groupsStart = order.indexOf('groups:start');
      assert.ok(groupsStart > rolesDone, 'groups started early: ' + order.join(', '));
    });
  }

  // 2. The tables render, and the grant summary RESOLVES the role name — which
  //    is what proves the roles cache was actually consulted.
  {
    const { doc } = mount({ mayManage: true });
    await later(60);
    check('the users table renders a resolved role', () => {
      assert.match(doc.els.userTbody.innerHTML, /alice/);
      assert.match(doc.els.userTbody.innerHTML, /Manager/,
        'the grant did not resolve its role name: ' + doc.els.userTbody.innerHTML);
      assert.ok(!/unknown role/.test(doc.els.userTbody.innerHTML),
        'a grant rendered "unknown role" with the roles loaded');
    });
    check('the groups and roles tables render', () => {
      assert.match(doc.els.groupTbody.innerHTML, /Ops/);
      assert.match(doc.els.roleTbody.innerHTML, /Manager/);
    });
    check('a user row carries its id', () => {
      assert.equal(doc.els.userTbody.children.length, 1, 'no row element was appended');
      assert.equal(doc.els.userTbody.children[0].getAttribute('data-user-id'), 'u-1',
        'the row has no user id, so a delegated click could not tell which user it was');
    });
  }

  // 3. ── UNKNOWN CAPS COUNT AS NO ──────────────────────────────────────────
  //    While the caps fetch is in flight the answer is not "probably yes". The
  //    fail-open version loads the whole principal graph for as long as a slow
  //    fetch takes, for somebody who may never be allowed it.
  {
    const { order } = mount({ mayManage: undefined });
    await later(60);
    check('undefined caps load nothing', () => {
      assert.equal(order.length, 0,
        'the card fetched with caps unknown: ' + order.join(', '));
    });
  }
  {
    const { order } = mount({ mayManage: false });
    await later(60);
    check('a refused viewer loads nothing', () => {
      assert.equal(order.length, 0, 'the card fetched for a refused viewer: ' + order.join(', '));
    });
  }

  // 4. BELIEVABILITY: the permitted case DOES fetch, so the two above are about
  //    permission rather than about a card that never loads.
  {
    const { order } = mount({ mayManage: true });
    await later(60);
    check('a permitted viewer does load', () => {
      assert.ok(order.length >= 3, 'a permitted viewer fetched ' + order.length + ' times');
    });
  }

  // 5. A FAILED FETCH empties the table rather than leaving stale rows.
  //
  //    LOADED SUCCESSFULLY FIRST, on purpose: starting from an empty table makes
  //    "no stale rows" true of a card that does nothing at all, and a mutation
  //    that kept the previous rows survived exactly that version of this case.
  {
    const { doc, mod } = mount({ mayManage: true });
    await later(60);
    check('the table is populated before the failure', () => {
      assert.match(doc.els.roleTbody.innerHTML, /Manager/,
        'nothing loaded, so the assertion below would hold vacuously');
    });

    global.fetch = () => Promise.reject(new Error('down'));
    await mod.loadRoles();
    check('a failed load leaves no stale rows', () => {
      assert.ok(!/Manager/.test(doc.els.roleTbody.innerHTML),
        'a failed reload left the previous rows on screen -- another administrator\'s '
        + 'revocation would still read as access: ' + doc.els.roleTbody.innerHTML);
    });
  }

  // ── THE THREE FORMS ARE BOUND AND DO SOMETHING ────────────────────────
  //
  // `element-coverage-audit` asked for these. Each one drives a real click and
  // asserts what it changed — a listener that is registered and does nothing is
  // the failure this shape of gate exists to catch, and it is exactly what the
  // card had before its write endpoints were served.
  {
    const { doc } = mount({ mayManage: true });
    await later(60);

    check('Add User opens the form and titles it', () => {
      doc.els.addUserBtn.fire('click');
      assert.ok(doc.els.userFormWrap.has('open'), 'the user form did not open');
      assert.strictEqual(doc.els.uf_title.textContent, 'Add User');
      assert.strictEqual(doc.els.uf_id.value, '', 'a CREATE must carry no id');
      assert.strictEqual(doc.els.uf_password.placeholder, 'password',
        'the placeholder tells the operator what an empty box means');
    });

    check('Cancel closes it', () => {
      doc.els.uf_cancel.fire('click');
      assert.ok(!doc.els.userFormWrap.has('open'), 'the user form did not close');
    });

    check('Add Group opens the form and renders its member list', () => {
      doc.els.addGroupBtn.fire('click');
      assert.ok(doc.els.groupFormWrap.has('open'), 'the group form did not open');
      assert.strictEqual(doc.els.gf_title.textContent, 'Add Group');
      assert.match(doc.els.gf_members.innerHTML, /data-member="u-1"/,
        'the member checkboxes are built from the loaded user list, and saveGroup reads '
        + 'data-member back: ' + doc.els.gf_members.innerHTML);
    });

    check('Add Role opens the form and renders the page matrix', () => {
      doc.els.addRoleBtn.fire('click');
      assert.ok(doc.els.roleFormWrap.has('open'), 'the role form did not open');
      assert.strictEqual(doc.els.rf_title.textContent, 'Add Role');
      assert.match(doc.els.rf_pages.innerHTML, /data-page-row=/,
        'the matrix is what saveRole reads back; without rows it always sends an empty '
        + 'list, which REVOKES every page: ' + doc.els.rf_pages.innerHTML);
    });

    check('a blank name is refused before any request', () => {
      let called = false;
      global.fetch = () => { called = true; return Promise.reject(new Error('should not run')); };
      doc.els.gf_name.value = '';
      doc.els.gf_save.fire('click');
      assert.ok(!called, 'an empty group name reached the network');
      assert.strictEqual(doc.els.gf_error.textContent, 'Name is required');
    });

    // AWAITED OUTSIDE `check`, which is synchronous — an async callback passed
    // to it returns a promise nobody looks at, so every assertion inside would
    // be swallowed and the case would always pass.
    const sent = [];
    global.fetch = (url, init) => {
      sent.push({ url, method: init && init.method, body: JSON.parse(init.body) });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });
    };
    doc.els.gf_name.value = 'Support';
    doc.els.gf_id.value = '';
    doc.els.gf_save.fire('click');
    await later(30);
    check('a save sends what the plan produced', () => {
      assert.strictEqual(sent.length, 1, 'the save made ' + sent.length + ' request(s)');
      assert.strictEqual(sent[0].method, 'POST', 'a create must POST');
      assert.strictEqual(sent[0].url, '/api/groups');
      assert.strictEqual(sent[0].body.name, 'Support');
    });
  }

  if (problems.length) {
    problems.forEach((p) => console.error('  ✗ ' + p));
    console.error('\nprincipals-wiring-check: ' + problems.length + ' of ' + checks + ' failed');
    process.exit(1);
  }
  console.log('principals card wiring ok (' + checks + ' checks; PORT-ONLY — see the header)');
})();
