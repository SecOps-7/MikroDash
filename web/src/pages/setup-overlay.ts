// The first-run ROUTER overlay's pure decisions.
//
// ── WHICH WIZARD THIS IS ────────────────────────────────────────────────────
//
// The live app calls two different screens "the first-run setup wizard", and
// cutover item 17 conflated them until 2026-08-28. This is the ROUTER one:
// `#setupOverlay` in index.html, fifteen ids, shown on `setup:required` when
// there are no routers. The ACCOUNT one is `firstRunView` in login.html and is
// already complete, server and browser.
//
// ── WHY THE WIRING IS NOT HERE ──────────────────────────────────────────────
//
// The overlay's Connect button makes two requests, and BOTH are recorded cutover
// blockers:
//
//	POST /api/routers               writes routers.json, which Node caches and
//	                                rebuilds from that stale cache (blocker 4)
//	POST /api/routers/:id/activate  UNPORTED, and it writes settings.json via
//	                                switchRouter (blocker 3)
//
// So the overlay cannot be exercised while Node runs, whatever this module does.
// What CAN be done now is the part that drifts — the field defaults, the port
// flip and the list of fields that re-lock the save button — pinned by
// The setup-overlay check against the live handler.
//
// The DOM is not rebuilt: `web/src/ui/shell.html` already carries all fifteen
// ids, extracted verbatim by the extract-ui tool. Markup is never retyped.
//
// ── NOT router-form.ts, AND THE DIFFERENCE IS REAL ──────────────────────────
//
// The Add/Edit Router modal has its own collector, `collectModal`, and it is a
// far bigger function: ids, site membership reordering, backup settings, geo,
// bandwidth. It also TRIMS every field. This one does not — an overlay host
// typed with a trailing space is posted with the space. Two implementations
// upstream, two here, and merging them would change one of them.

/** The body both the test and the save send. */
export interface SetupBody {
  label: string;
  host: string;
  port: number;
  username: string;
  password: string;
  defaultIf: string;
  pingTarget: string;
  tls: boolean;
  tlsInsecure: boolean;
}

/** What the form's inputs hold, as strings and checkboxes. */
export interface SetupFields {
  label?: string;
  host?: string;
  port?: string;
  username?: string;
  password?: string;
  defaultIf?: string;
  pingTarget?: string;
  tls?: boolean;
  tlsInsecure?: boolean;
}

// The defaults, which are the part that drifts.
//
// EVERY ONE IS A `||` ON A STRING, so the EMPTY STRING takes the default — not
// only an absent field. An operator who clears the username box gets `admin`,
// not a login attempt with no user; a port using `??` would send the empty
// string and fail with a confusing authentication error.
//
// `port` is `parseInt(value || '8729', 10)`, so it defaults through falsiness
// too — and a NON-NUMERIC entry becomes NaN rather than the default. Reproduced
// as written, because that is what the live form posts and what the route then
// coerces; see internal/routers/endpoint.go for the other half of that story.
export function collectSetupBody(f: SetupFields): SetupBody {
  return {
    label: f.label || '',
    host: f.host || '',
    port: parseInt(f.port || '8729', 10),
    username: f.username || 'admin',
    password: f.password || '',
    defaultIf: f.defaultIf || 'ether1',
    pingTarget: f.pingTarget || '1.1.1.1',
    tls: !!f.tls,
    tlsInsecure: !!f.tlsInsecure,
  };
}

// Toggling TLS flips the port, and ONLY between the two API defaults.
//
// `if (checked && p === 8728) '8729'`, and the mirror. A port that assigned
// unconditionally would overwrite an operator's deliberate 8730 the moment they
// touched the toggle — which is why the live code tests for the exact previous
// default instead.
//
// Returns the new value, or null when nothing should change. Null rather than
// the unchanged string, so a caller cannot write the field back and lose a
// half-typed entry.
export function flipPortForTls(current: string, tlsChecked: boolean): string | null {
  const p = parseInt(current, 10);
  if (tlsChecked && p === 8728) return '8729';
  if (!tlsChecked && p === 8729) return '8728';
  return null;
}

// The fields that RE-LOCK the save button when edited.
//
// ── THE LIST IS THE POINT, AND IT IS NOT EVERY FIELD ────────────────────────
//
// Save is disabled until a connection test passes, and a change to any field
// deciding WHERE the connection goes or HOW it travels invalidates that result.
// `setupLabel`, `setupIf` and `setupPing` are deliberately absent: editing them
// cannot make a passing test wrong, and re-locking on them would make an
// operator re-run a test to fix a typo in a name.
//
// These are the same five fields `routers.SameEndpoint` compares on the server,
// for the same reason. Kept in step by the check rather than by memory: a field
// added to the form and not added here leaves a stale "✓ Connected" beside a
// changed host.
export const SETUP_WATCH_FIELDS = [
  'setupHost',
  'setupPort',
  'setupUser',
  'setupPass',
  'setupTls',
  'setupTlsInsecure',
] as const;

/** The test result line, both outcomes. */
export function setupTestResultText(ok: boolean, boardName: string, error: string): string {
  if (ok) return '✓ Connected' + (boardName ? ' — ' + boardName : '');
  return '✗ ' + (error || 'Failed');
}
