(function () {
  var loginView    = document.getElementById('loginView');
  var firstRunView = document.getElementById('firstRunView');
  var loadingView  = document.getElementById('loadingView');

  function showError(elId, msg) {
    var el = document.getElementById(elId);
    el.textContent = msg;
    el.classList.add('visible');
  }
  function clearError(elId) {
    var el = document.getElementById(elId);
    el.textContent = '';
    el.classList.remove('visible');
  }

  // Validate ?next= param — parse via URL API, assert same origin, return only path
  function safeNext() {
    try {
      var raw = new URLSearchParams(window.location.search).get('next');
      if (!raw || /[\x00-\x1f]/.test(raw)) return '/';
      var u = new URL(raw, window.location.origin);
      if (u.origin !== window.location.origin) return '/';
      var path = u.pathname + u.search + u.hash;
      if (path.charAt(0) !== '/' || path.charAt(1) === '/' || path.charAt(1) === '\\') return '/';
      return path;
    } catch (_) {}
    return '/';
  }

  // Check auth status to decide which view to show
  fetch('/api/auth/status')
    .then(function (r) { return r.json(); })
    .then(function (d) {
      loadingView.style.display = 'none';
      if (d.firstRun) {
        firstRunView.style.display = '';
        document.getElementById('setupUser').focus();
      } else {
        loginView.style.display = '';
        document.getElementById('loginUser').focus();
      }
    })
    .catch(function () {
      loadingView.style.display = 'none';
      loginView.style.display = '';
    });

  // ── Login ──────────────────────────────────────────────────────────────────
  function doLogin() {
    clearError('loginError');
    var username = document.getElementById('loginUser').value.trim();
    var password = document.getElementById('loginPass').value;
    if (!username || !password) { showError('loginError', 'Please enter username and password.'); return; }
    var btn = document.getElementById('loginBtn');
    btn.disabled = true;
    btn.textContent = 'Signing in…';
    fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: username, password: password }),
    })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (d.ok) {
          sessionStorage.setItem('justLoggedIn', '1');
          document.body.style.transition = 'opacity 1s ease';
          document.body.style.opacity = '0';
          setTimeout(function() { window.location.replace(safeNext()); }, 1000);
        } else {
          btn.disabled = false;
          btn.textContent = 'Sign In';
          showError('loginError', d.error || 'Sign in failed.');
        }
      })
      .catch(function () {
        btn.disabled = false;
        btn.textContent = 'Sign In';
        showError('loginError', 'Network error. Please try again.');
      });
  }

  document.getElementById('loginBtn').addEventListener('click', doLogin);
  document.getElementById('loginPass').addEventListener('keydown', function (e) {
    if (e.key === 'Enter') doLogin();
  });
  document.getElementById('loginUser').addEventListener('keydown', function (e) {
    if (e.key === 'Enter') document.getElementById('loginPass').focus();
  });

  // ── First-run setup ────────────────────────────────────────────────────────
  function doSetup() {
    clearError('setupError');
    var username = document.getElementById('setupUser').value.trim();
    var password = document.getElementById('setupPass').value;
    var confirm  = document.getElementById('setupPass2').value;
    if (!username) { showError('setupError', 'Username is required.'); return; }
    if (password.length < 4) { showError('setupError', 'Password must be at least 4 characters.'); return; }
    if (password !== confirm) { showError('setupError', 'Passwords do not match.'); return; }
    var btn = document.getElementById('setupBtn');
    btn.disabled = true;
    btn.textContent = 'Creating account…';
    fetch('/api/users/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: username, password: password }),
    })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (d.ok) {
          firstRunView.style.display = 'none';
          loginView.style.display    = '';
          document.getElementById('loginUser').value = username;
          document.getElementById('loginPass').focus();
        } else {
          btn.disabled = false;
          btn.textContent = 'Create Account';
          showError('setupError', d.error || 'Setup failed.');
        }
      })
      .catch(function () {
        btn.disabled = false;
        btn.textContent = 'Create Account';
        showError('setupError', 'Network error. Please try again.');
      });
  }

  document.getElementById('setupBtn').addEventListener('click', doSetup);
  document.getElementById('setupPass2').addEventListener('keydown', function (e) {
    if (e.key === 'Enter') doSetup();
  });
})();
