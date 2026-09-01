package rbac

import "mikrodash/internal/pages"

// PageKeys is every page the permission system knows, read from `internal/pages`.
//
// ── IT WAS A HAND-WRITTEN COPY, AND THAT COST AN OUTAGE ─────────────────────
//
// This was a literal list guarded by `TestPageKeysMatchLive`, which compared it
// against the Node app's `pages.js`. That test SKIPS without `MIKRODASH_SRC`, and
// `MIKRODASH_SRC` has pointed at a deleted repository since the v0.8.0 cutover —
// so the guard has not run for anything in months.
//
// On 2026-09-01 six page keys were renamed and this list was missed. Nothing went
// red: the build was green, every test passed, and four pages silently showed
// nothing at all, because `CanPage` denies an unknown page BEFORE it looks at any
// role — even for an administrator. A page key is not only a URL, a markup id and
// a room name; it is also a PERMISSION key, and an unrecognised one is a locked
// door.
//
// Reading the same list the rest of the app reads makes that drift impossible
// rather than merely detectable.
var PageKeys = pages.Keys()
