'use strict';
/**
 * The login page's script, PORT against LIVE, driven from one harness.
 *
 * ---- WHY THIS EXISTS -------------------------------------------------------
 *
 * `web/public/login.js` and `web/public/preflight.js` were byte-for-byte copies
 * of the live repo's files. The operator on 2026-08-28: "the port should stand
 * on its own without any lingering JS from the live repo." Copies cannot be
 * deleted on the strength of a careful reading — this drives BOTH the live file
 * and the port's BUILT bundle against the same stub DOM and compares every
 * observable operation.
 *
 * ---- NOT tools/login-fade-check.js ----------------------------------------
 *
 * That one text-scans three files to confirm the `justLoggedIn` hide has a
 * matching restore in `main.ts`. It executes nothing. This RUNS both
 * implementations. Both are wanted: the fade check spans a boundary this gate
 * cannot see (preflight hides, main.ts restores, in different documents).
 *
 * ---- THE BUILT BUNDLE, NOT THE TYPESCRIPT ---------------------------------
 *
 * `web/dist/login.js` is what a browser runs. Checking `web/src/entry/login.ts` would
 * leave the build itself unverified — a wrong entry point, the wrong format, a
 * bundle that never reaches the document — and every one of those failures looks
 * like a login page that does nothing.
 *
 * ---- WHAT IS COMPARED -----------------------------------------------------
 *
 * A LOG of operations, in order: which view was shown, what was focused, what
 * text landed in which error element, which classes moved, what was POSTed
 * where, what went into sessionStorage, and where the browser was sent. Not the
 * DOM at the end — the ORDER matters (a button re-enabled before the error is
 * shown looks the same at rest) and so do the calls that leave no trace.
 *
 ---- ONE EQUIVALENT MUTANT, RECORDED RATHER THAN COUNTED ------------------
 *
 * Dropping the `p.charAt(1) === '\\'` half of the protocol-relative guard
 * SURVIVES this gate, and no scenario can kill it. That is a property of the
 * URL parser, not a hole:
 *
 *   new URL('/\\x', 'http://dash.example').pathname  === '///x'
 *   new URL('/%5Cx', 'http://dash.example').pathname === '/%5Cx'
 *
 * WHATWG normalises a raw backslash in the path of a SPECIAL scheme (http,
 * https) to a forward slash, and an encoded one stays percent-encoded. So a
 * same-origin URL whose path's second character is a literal backslash cannot be
 * produced, and the branch is unreachable in any browser.
 *
 * It stays in `login.ts` because the LIVE file has it and this is a port, and
 * because the guarantee belongs to the parser rather than to this code — the day
 * `next` is fed through anything with laxer normalisation, it is the line that
 * matters. Measured 2026-08-28; not a gap in the corpus.
 *
 *   node tools/login-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('login-page-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
// FROZEN — `run()` EXECUTES this whole file, so the file itself is the lifted
// source. Freezing it keeps the live half running against every scenario,
// including any added later.
const liveSrc = G.value('the live login.js', () => LIFT.liveSource(ROOT, path.join('public', 'login.js')));
if (!liveSrc || liveSrc.length < 500) {
  throw new Error('the recorded login.js is empty — the golden is broken');
}
const portPath = path.join(ROOT, 'web', 'dist', 'login.js');
if (!fs.existsSync(portPath)) {
  console.error('web/dist/login.js is missing — run `npm run build` in web/ first.');
  process.exit(1);
}
const portSrc = fs.readFileSync(portPath, 'utf8');

// ---- The stub DOM ----------------------------------------------------------
//
// Only what these two files touch. A fuller stub would be more code with more
// places for the harness itself to differ between the two runs, and the point is
// that BOTH sides see exactly the same thing.
const IDS = ['loginView', 'firstRunView', 'loadingView', 'loginError', 'setupError',
  'loginUser', 'loginPass', 'loginBtn', 'setupUser', 'setupPass', 'setupPass2', 'setupBtn'];

function makeEnv(scenario) {
  const log = [];
  const els = {};
  for (const id of IDS) {
    const el = {
      id,
      value: scenario.values && scenario.values[id] !== undefined ? scenario.values[id] : '',
      _text: '',
      _disabled: false,
      style: new Proxy({}, {
        set(t, k, v) { log.push(`style ${id}.${String(k)}=${v}`); t[k] = v; return true; },
        get(t, k) { return t[k]; },
      }),
      classList: {
        add: (c) => { log.push(`class+ ${id}.${c}`); },
        remove: (c) => { log.push(`class- ${id}.${c}`); },
      },
      focus: () => log.push(`focus ${id}`),
      _handlers: {},
      addEventListener: (ev, fn) => {
        els[id]._handlers[ev] = els[id]._handlers[ev] || [];
        els[id]._handlers[ev].push(fn);
        log.push(`listen ${id}.${ev}`);
      },
    };
    Object.defineProperty(el, 'textContent', {
      get() { return el._text; },
      set(v) { el._text = v; log.push(`text ${id}=${JSON.stringify(v)}`); },
    });
    Object.defineProperty(el, 'disabled', {
      get() { return el._disabled; },
      set(v) { el._disabled = v; log.push(`disabled ${id}=${v}`); },
    });
    els[id] = el;
  }

  const timers = [];
  const store = {};

  const doc = {
    getElementById: (id) => els[id] || null,
    body: {
      style: new Proxy({}, {
        set(t, k, v) { log.push(`style body.${String(k)}=${v}`); t[k] = v; return true; },
        get(t, k) { return t[k]; },
      }),
    },
  };

  const win = {
    location: {
      search: scenario.search || '',
      origin: 'http://dash.example',
      replace: (u) => log.push(`replace ${u}`),
    },
  };

  const ctx = {
    document: doc,
    window: win,
    URL,
    URLSearchParams,
    JSON,
    console: { log() {}, warn() {}, error() {} },
    sessionStorage: {
      getItem: (k) => (k in store ? store[k] : null),
      setItem: (k, v) => { store[k] = String(v); log.push(`session ${k}=${v}`); },
      removeItem: (k) => { delete store[k]; log.push(`session- ${k}`); },
    },
    setTimeout: (fn, ms) => { log.push(`setTimeout ${ms}`); timers.push(fn); return timers.length; },
    fetch: (url, init) => {
      const body = init && init.body ? init.body : null;
      log.push(`fetch ${url}${body ? ' ' + body : ''}`);
      const answer = scenario.answers[url];
      if (answer === 'reject') return Promise.reject(new Error('network'));
      return Promise.resolve({ json: () => Promise.resolve(answer) });
    },
  };
  ctx.globalThis = ctx;
  ctx.self = ctx;
  return { ctx, log, els, timers };
}

/** Let queued promise callbacks run. Three turns covers fetch -> json -> then. */
async function settle() {
  for (let i = 0; i < 4; i++) await Promise.resolve();
}

/** Run one implementation through one scenario and return its operation log. */
async function run(src, scenario, label) {
  const env = makeEnv(scenario);
  vm.runInNewContext(src, env.ctx, { filename: label });
  await settle();

  for (const act of scenario.acts || []) {
    const el = env.els[act.id];
    const hs = (el && el._handlers[act.ev]) || [];
    for (const h of hs) h(act.arg || {});
    await settle();
  }
  // Fire any queued timer, so the post-login redirect is observed.
  for (const t of env.timers) t();
  return env.log;
}

// ---- The scenarios ---------------------------------------------------------
//
// Each one is a path a real person takes, and each was chosen because the two
// implementations could plausibly differ on it.
const STATUS = '/api/auth/status';
const LOGIN = '/api/auth/login';
const SETUP = '/api/users/setup';

const SCENARIOS = [
  {
    why: 'an existing install shows the sign-in form',
    answers: { [STATUS]: { firstRun: false } },
  },
  {
    why: 'a fresh install shows first-run setup',
    answers: { [STATUS]: { firstRun: true } },
  },
  {
    why: 'the status request fails — it must fall back to sign-in, not to setup',
    answers: { [STATUS]: 'reject' },
  },
  {
    why: 'a successful sign-in sets the flag, fades out and redirects',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'a rejected sign-in re-enables the button and shows the server message',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: false, error: 'Invalid credentials.' } },
    values: { loginUser: 'someone', loginPass: 'wrong' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'a rejected sign-in with NO message falls back to the default text',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: false } },
    values: { loginUser: 'someone', loginPass: 'wrong' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'a network failure on sign-in',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: 'reject' },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'an empty password never reaches the network',
    answers: { [STATUS]: { firstRun: false } },
    values: { loginUser: 'someone', loginPass: '' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'a username of spaces is trimmed to empty and never reaches the network',
    answers: { [STATUS]: { firstRun: false } },
    values: { loginUser: '   ', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'the username is TRIMMED on the way to the server',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: '  someone  ', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'Enter in the username moves to the password rather than signing in',
    answers: { [STATUS]: { firstRun: false } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginUser', ev: 'keydown', arg: { key: 'Enter' } }],
  },
  {
    why: 'Enter in the password signs in',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginPass', ev: 'keydown', arg: { key: 'Enter' } }],
  },
  {
    why: 'a key that is not Enter does nothing',
    answers: { [STATUS]: { firstRun: false } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginPass', ev: 'keydown', arg: { key: 'a' } }],
  },
  // ── ?next=, which is the security-relevant half ─────────────────────────
  {
    why: 'a same-origin path is honoured',
    search: '?next=/logs',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'a foreign origin is refused',
    search: '?next=https://evil.example/steal',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'a PROTOCOL-RELATIVE url is refused — it looks relative and is not',
    search: '?next=//evil.example/steal',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'a backslash-smuggled authority is refused',
    search: '?next=/%5Cevil.example/steal',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    // THE CASE `charAt(1)` EXISTS FOR, and the only one that reaches it: the URL
    // is genuinely SAME-ORIGIN, so the origin check above passes — but the PATH
    // it yields is `//evil.example/x`, and `location.replace` reads that as
    // protocol-relative and leaves the origin. Without this scenario the
    // `charAt(1)` guard is unreachable from the corpus and dropping it survives,
    // which is exactly what a mutation run showed on 2026-08-28.
    why: 'a same-origin url whose PATH is protocol-relative is refused',
    search: '?next=http://dash.example//evil.example/x',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    // A NEWLINE, decoded by URLSearchParams before anything else sees it. It is
    // same-origin and parses cleanly; `new URL` STRIPS the newline, so without
    // the control-character check this redirects to `/logsevil` — a path the
    // person never asked for, built out of a character that is a
    // response-splitting primitive anywhere it survives.
    why: 'a control character in ?next= is refused',
    search: '?next=/logs%0Aevil',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'a same-origin ABSOLUTE url keeps only its path',
    search: '?next=http://dash.example/reports?kind=ping',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  {
    why: 'no next at all',
    search: '',
    answers: { [STATUS]: { firstRun: false }, [LOGIN]: { ok: true } },
    values: { loginUser: 'someone', loginPass: 'a-password' },
    acts: [{ id: 'loginBtn', ev: 'click' }],
  },
  // ── First-run setup ─────────────────────────────────────────────────────
  {
    why: 'setup succeeds and hands over to the sign-in form',
    answers: { [STATUS]: { firstRun: true }, [SETUP]: { ok: true } },
    values: { setupUser: 'admin', setupPass: 'longenough', setupPass2: 'longenough' },
    acts: [{ id: 'setupBtn', ev: 'click' }],
  },
  {
    why: 'setup with mismatched passwords never reaches the network',
    answers: { [STATUS]: { firstRun: true } },
    values: { setupUser: 'admin', setupPass: 'longenough', setupPass2: 'different' },
    acts: [{ id: 'setupBtn', ev: 'click' }],
  },
  {
    why: 'a three-character password is refused',
    answers: { [STATUS]: { firstRun: true } },
    values: { setupUser: 'admin', setupPass: 'abc', setupPass2: 'abc' },
    acts: [{ id: 'setupBtn', ev: 'click' }],
  },
  {
    why: 'exactly four characters is accepted — the boundary the other way',
    answers: { [STATUS]: { firstRun: true }, [SETUP]: { ok: true } },
    values: { setupUser: 'admin', setupPass: 'abcd', setupPass2: 'abcd' },
    acts: [{ id: 'setupBtn', ev: 'click' }],
  },
  {
    why: 'setup with no username never reaches the network',
    answers: { [STATUS]: { firstRun: true } },
    values: { setupUser: '', setupPass: 'longenough', setupPass2: 'longenough' },
    acts: [{ id: 'setupBtn', ev: 'click' }],
  },
  {
    why: 'setup rejected by the server',
    answers: { [STATUS]: { firstRun: true }, [SETUP]: { ok: false, error: 'Username taken.' } },
    values: { setupUser: 'admin', setupPass: 'longenough', setupPass2: 'longenough' },
    acts: [{ id: 'setupBtn', ev: 'click' }],
  },
  {
    why: 'a network failure during setup',
    answers: { [STATUS]: { firstRun: true }, [SETUP]: 'reject' },
    values: { setupUser: 'admin', setupPass: 'longenough', setupPass2: 'longenough' },
    acts: [{ id: 'setupBtn', ev: 'click' }],
  },
  {
    why: 'Enter in the confirm field runs setup',
    answers: { [STATUS]: { firstRun: true }, [SETUP]: { ok: true } },
    values: { setupUser: 'admin', setupPass: 'longenough', setupPass2: 'longenough' },
    acts: [{ id: 'setupPass2', ev: 'keydown', arg: { key: 'Enter' } }],
  },
];

(async () => {
  const problems = [];
  let compared = 0;

  // ── BELIEVABILITY, BEFORE ANY COMPARISON ────────────────────────────────
  //
  // A harness that drove NEITHER side would report every scenario as identical.
  // So the live run alone must produce something discriminating first.
  const byWhy = (w) => SCENARIOS.find((s) => s.why.startsWith(w));
  // RE-AIMED AT THE PORT. "A harness that drove NEITHER side would report every
  // scenario as identical" — so ONE side must be shown to produce something
  // discriminating, and the port is the side that still exists.
  const probe = await run(portSrc, byWhy('a successful sign-in'), 'port');
  for (const must of ['session justLoggedIn=1', 'replace /']) {
    if (!probe.some((l) => l === must || l.startsWith(must))) {
      problems.push(`the harness never observed "${must}" on a successful `
        + 'sign-in. It is not driving the code, so every comparison below is two empty logs '
        + `agreeing. Log was:\n      ${probe.join('\n      ')}`);
    }
  }
  // RE-AIMED AT THE PORT: refusing a foreign-origin `?next=` is a property the
  // PORT must keep — it is the login page that ships — and this is the assertion
  // that stops the comparisons below being two empty logs agreeing.
  const denied = await run(portSrc, byWhy('a foreign origin is refused'), 'port');
  if (!denied.includes('replace /')) {
    problems.push('the file did not refuse a foreign-origin ?next=. Either the harness is not '
      + 'exercising safeNext, or the live rule has changed and this gate is pinning the wrong one.');
  }

  if (!problems.length) {
    for (const sc of SCENARIOS) {
      const a = await run(liveSrc, sc, 'live');
      const b = await run(portSrc, sc, 'port');
      compared++;
      if (JSON.stringify(a) !== JSON.stringify(b)) {
        const n = Math.max(a.length, b.length);
        const diff = [];
        for (let i = 0; i < n; i++) {
          if (a[i] !== b[i]) {
            diff.push(`      ${i}: live=${a[i] || '(none)'}  port=${b[i] || '(none)'}`);
          }
        }
        problems.push(`${sc.why}:\n${diff.join('\n')}`);
      }
    }
  }

  if (problems.length) {
    console.error('login-page-check: the port and the live file disagree\n');
    for (const p of problems) console.error('  - ' + p + '\n');
    process.exit(1);
  }
  console.log(`login-page-check: ${compared} scenarios, the built bundle matches the live file `
    + 'operation for operation');
})();
