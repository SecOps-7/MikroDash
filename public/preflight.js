if (sessionStorage.getItem('justLoggedIn')) {
  document.documentElement.style.opacity = '0';
}

// ── Nav grouping, applied before the sidebar paints ──────────────────────────
//
// Grouping is a per-user, server-side preference, but app.js loads at the very
// bottom of index.html — thousands of lines after the nav — so applying it there
// means the sidebar paints in the default shape and then visibly regroups. Worst
// for somebody who chose the flat list and watches it collapse on every load.
//
// This file already runs in <head> before the body parses, so it is the only
// place that can prevent that. localStorage is a CACHE of the last known answer;
// the server is still the source of truth and app.js reconciles.
//
// An inline <script> after </nav> would be the obvious alternative and is
// blocked: the CSP sets script-src 'self' with no 'unsafe-inline'. Inline STYLE
// is allowed, which is why the open categories arrive as a generated stylesheet
// rather than as classes — the elements do not exist yet to put classes on.
try {
  var _nav = JSON.parse(localStorage.getItem('mkd_nav_prefs') || 'null') || {};
  var _root = document.documentElement;
  _root.setAttribute('data-nav', _nav.grouped === false ? 'flat' : 'grouped');

  // Shape-guarded, never vocabulary-guarded. This file holds no list of category
  // names and must not gain one — a copy of the taxonomy in a file with no
  // module system is one nothing could keep honest. Unknown tokens simply match
  // no element.
  var _open = (Array.isArray(_nav.expanded) ? _nav.expanded : [])
    .filter(function (k) { return /^[a-z]{2,20}$/.test(k); });
  if (_open.length) {
    var _st = document.createElement('style');
    _st.id = 'navBoot';
    // LAYOUT ONLY. The tint, the open bar and the chevron ride on .is-open,
    // which app.js adds — restating their colours here would be a second copy
    // free to drift from the stylesheet. What must not flash is rows appearing
    // and disappearing; chrome settling a moment later is not worth that risk.
    _st.textContent = _open.map(function (k) {
      return '.nav-group[data-cat="' + k + '"]>.nav-group-body{display:flex}';
    }).join('');
    document.head.appendChild(_st);
  }
} catch (e) {}
