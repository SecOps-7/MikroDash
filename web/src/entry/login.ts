/**
 * The login page's script.
 *
 * ── IT WAS THE LIVE REPO'S FILE UNTIL 2026-08-28 ────────────────────────────
 *
 * `web/public/login.js` was a byte-for-byte copy of `../MikroDash/public/login.js`.
 * The operator: "the port should stand on its own without any lingering JS from
 * the live repo." the login-page check drives this module and the live
 * file from one harness against the same DOM and compares what each does, so the
 * copy could be deleted rather than trusted.
 *
 * ── IT IS NOT PART OF `app.js` ──────────────────────────────────────────────
 *
 * `login.html` is served to somebody who has NO SESSION. Bundling this into the
 * app would ship the whole dashboard to an unauthenticated browser, and the app
 * bundle would fail on the first thing it did — there is no socket to open and
 * no `/api/*` that would answer. Separate entry point, separate document.
 *
 * ── THE THREE VIEWS ─────────────────────────────────────────────────────────
 *
 * `loadingView` is what the document ships showing, so a slow `/api/auth/status`
 * does not flash a sign-in form at somebody who is about to be sent to first-run
 * setup. Which of the other two replaces it is the SERVER's answer, and a failed
 * fetch falls back to the login form: a server that cannot answer is far more
 * likely to be busy than to be a fresh install, and offering "create the first
 * administrator" to somebody whose install already has one is the worse mistake.
 */

const byId = (id: string): HTMLElement | null => document.getElementById(id);

function showError(elId: string, msg: string): void {
  const el = byId(elId);
  if (!el) return;
  // textContent, not innerHTML. `d.error` comes from the server and is shown
  // verbatim; the live file does the same, and this is the reason.
  el.textContent = msg;
  el.classList.add('visible');
}

function clearError(elId: string): void {
  const el = byId(elId);
  if (!el) return;
  el.textContent = '';
  el.classList.remove('visible');
}

/**
 * Where to go after a successful sign-in.
 *
 * ── AN OPEN REDIRECT IS THE WHOLE POINT OF THIS FUNCTION ────────────────────
 *
 * `?next=` is attacker-controlled: anybody can send a link. Four separate checks,
 * and each one is load-bearing:
 *
 *  1. CONTROL CHARACTERS are rejected outright. A newline in a Location header
 *     is a response-splitting primitive.
 *  2. The URL is PARSED and its ORIGIN compared, rather than the string being
 *     inspected. `//evil.example` and a backslash-smuggled authority both parse
 *     to a foreign origin, and neither looks foreign to a substring test.
 *  3. Only `pathname + search + hash` is returned — never the parsed URL — so
 *     even a same-origin absolute URL cannot smuggle credentials or a port.
 *  4. `//` and `/\` at the START are rejected even after all of that, because a
 *     browser reads both as protocol-relative and would leave the origin.
 *
 * Anything that fails is `/`, which is the app. There is no error path: a bad
 * `next` is not worth a message, and saying "that redirect looked hostile" tells
 * whoever sent the link that the check exists.
 */
function safeNext(): string {
  try {
    const raw = new URLSearchParams(window.location.search).get('next');
    // Written as \u escapes: the live file spells it `[\x00-\x1f]`, and a
    // literal control character in a source file is invisible to review.
    if (!raw || /[\u0000-\u001f]/.test(raw)) return '/';
    const u = new URL(raw, window.location.origin);
    if (u.origin !== window.location.origin) return '/';
    const p = u.pathname + u.search + u.hash;
    if (p.charAt(0) !== '/' || p.charAt(1) === '/' || p.charAt(1) === '\\') return '/';
    return p;
  } catch {
    // Fall through to the default.
  }
  return '/';
}

function main(): void {
  const loginView = byId('loginView');
  const firstRunView = byId('firstRunView');
  const loadingView = byId('loadingView');

  // ── Which view ────────────────────────────────────────────────────────────
  void fetch('/api/auth/status')
    .then((r) => r.json())
    .then((d: { firstRun?: boolean }) => {
      if (loadingView) loadingView.style.display = 'none';
      if (d.firstRun) {
        if (firstRunView) firstRunView.style.display = '';
        byId('setupUser')?.focus();
      } else {
        if (loginView) loginView.style.display = '';
        byId('loginUser')?.focus();
      }
    })
    .catch(() => {
      // See the header: a server that cannot answer is far more likely to be
      // busy than to be a fresh install.
      if (loadingView) loadingView.style.display = 'none';
      if (loginView) loginView.style.display = '';
    });

  // ── Sign in ───────────────────────────────────────────────────────────────
  function doLogin(): void {
    clearError('loginError');
    const username = (byId('loginUser') as HTMLInputElement | null)?.value.trim() || '';
    const password = (byId('loginPass') as HTMLInputElement | null)?.value || '';
    if (!username || !password) {
      showError('loginError', 'Please enter username and password.');
      return;
    }
    const btn = byId('loginBtn') as HTMLButtonElement | null;
    if (btn) {
      btn.disabled = true;
      btn.textContent = 'Signing in…';
    }
    void fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    })
      .then((r) => r.json())
      .then((d: { ok?: boolean; error?: string }) => {
        if (d.ok) {
          // THE HANDOFF. `preflight.ts` reads this flag on the next document and
          // hides it, and `main.ts` fades it back in once the app has rendered.
          // All three have to agree or the app renders invisibly — which it did,
          // for one afternoon, when the port had the first two and not the third.
          sessionStorage.setItem('justLoggedIn', '1');
          document.body.style.transition = 'opacity 1s ease';
          document.body.style.opacity = '0';
          // `replace`, not `assign`: the back button must not return to a login
          // form for a session that now exists.
          //
          // The 1000ms matches the transition exactly, so the navigation happens
          // as the fade completes rather than partway through it.
          setTimeout(() => { window.location.replace(safeNext()); }, 1000);
        } else {
          if (btn) {
            btn.disabled = false;
            btn.textContent = 'Sign In';
          }
          showError('loginError', d.error || 'Sign in failed.');
        }
      })
      .catch(() => {
        if (btn) {
          btn.disabled = false;
          btn.textContent = 'Sign In';
        }
        showError('loginError', 'Network error. Please try again.');
      });
  }

  byId('loginBtn')?.addEventListener('click', doLogin);
  byId('loginPass')?.addEventListener('keydown', (e) => {
    if ((e as KeyboardEvent).key === 'Enter') doLogin();
  });
  // Enter in the USERNAME moves to the password rather than submitting. A form
  // that signed in on the first Enter would submit an empty password and answer
  // with a failure the person did not ask for.
  byId('loginUser')?.addEventListener('keydown', (e) => {
    if ((e as KeyboardEvent).key === 'Enter') byId('loginPass')?.focus();
  });

  // ── First-run setup ───────────────────────────────────────────────────────
  function doSetup(): void {
    clearError('setupError');
    const username = (byId('setupUser') as HTMLInputElement | null)?.value.trim() || '';
    const password = (byId('setupPass') as HTMLInputElement | null)?.value || '';
    const confirm = (byId('setupPass2') as HTMLInputElement | null)?.value || '';
    if (!username) { showError('setupError', 'Username is required.'); return; }
    // FOUR, not eight. Reproduced rather than improved: the server enforces its
    // own rule and this is only the early message, so raising it here would
    // reject a password the install would have accepted.
    if (password.length < 4) {
      showError('setupError', 'Password must be at least 4 characters.');
      return;
    }
    if (password !== confirm) { showError('setupError', 'Passwords do not match.'); return; }
    const btn = byId('setupBtn') as HTMLButtonElement | null;
    if (btn) {
      btn.disabled = true;
      btn.textContent = 'Creating account…';
    }
    void fetch('/api/users/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    })
      .then((r) => r.json())
      .then((d: { ok?: boolean; error?: string }) => {
        if (d.ok) {
          // NOT a redirect. The account exists but there is no session yet, so
          // this hands over to the sign-in form with the username already filled
          // and the cursor in the password — one field to type, rather than a
          // bounce through a login page that would look like the setup failed.
          if (firstRunView) firstRunView.style.display = 'none';
          if (loginView) loginView.style.display = '';
          const u = byId('loginUser') as HTMLInputElement | null;
          if (u) u.value = username;
          byId('loginPass')?.focus();
        } else {
          if (btn) {
            btn.disabled = false;
            btn.textContent = 'Create Account';
          }
          showError('setupError', d.error || 'Setup failed.');
        }
      })
      .catch(() => {
        if (btn) {
          btn.disabled = false;
          btn.textContent = 'Create Account';
        }
        showError('setupError', 'Network error. Please try again.');
      });
  }

  byId('setupBtn')?.addEventListener('click', doSetup);
  byId('setupPass2')?.addEventListener('keydown', (e) => {
    if ((e as KeyboardEvent).key === 'Enter') doSetup();
  });
}

main();
