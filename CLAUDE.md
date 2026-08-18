# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **Full context lives in `AI_CONTEXT.md`** — it covers the collector pattern, RouterOS quirks, security invariants, and testing conventions in detail. Read it before making architectural decisions.

---

## Code navigation and editing

This repo is indexed by **Serena** (MCP). Use its symbolic tools for code — they are cheaper and more
precise than whole-file reads. `src/index.js` is 2800 lines and `public/app.js` is 8000; reading
either in full wastes most of a context window for no benefit.

| Task | Use | Not |
|---|---|---|
| Understand a file | `get_symbols_overview`, then `find_symbol` with `include_body` on the symbols you actually need | `Read` on the whole file |
| Find a definition | `find_symbol` / `find_declaration` | `Grep` then `Read` |
| Find callers before changing a signature | `find_referencing_symbols` | `Grep` |
| Replace a whole function / class / method | `replace_symbol_body` | `Edit` |
| Change a few lines inside a larger symbol | `replace_content` (regex mode: `start.*?end` beats quoting the block) | `Edit` |
| Same edit across many files | `replace_in_files` (`dry_run: true` first) | a batch of `Edit`s |
| Rename a symbol everywhere | `rename_symbol` | hand-rolled find-and-replace |

- `Glob` and `Grep` are fine for **discovery** — locating candidate files. The follow-up read or
  reference search should go through Serena.
- **Scope:** `src/**` and `public/*.js`. This does *not* apply to markdown, JSON, YAML, `.gitignore`,
  `Dockerfile`, or shell scripts — Serena has no symbol model for those, so `Read`/`Edit`/`Write` are
  the correct tools there. `public/vendor/` is read-only; never edit it.
- When `rename_symbol` or `safe_delete_symbol` returns success the refactor is already applied
  consistently across all references — don't re-read files or re-run the suite just to confirm it
  propagated.
- Serena is **per-session**: call `initial_instructions` once per conversation, and expect one Serena
  process (and one dashboard port, 24282+) per Claude session. Two sessions on this project share
  `.serena/memories/` with no locking — concurrent memory writes clobber each other.
- Project memories live in `.serena/memories/` (gitignored). `mem:core` is the entry point and links
  to `mem:tech_stack`, `mem:suggested_commands`, `mem:conventions`, `mem:task_completion`,
  `mem:frontend/core`. They point into `AI_CONTEXT.md` rather than duplicating it — keep it that way,
  and retire a memory when the thing it describes changes.

---

## Commands

```bash
# Rebuild and restart the container
# (a Stop hook does this automatically at the end of each turn — run it by hand
#  only when you need the rebuilt container mid-turn, e.g. to run the tests)
docker compose build && docker compose up -d

# View live logs
docker logs -f mikrodash

# Run all tests (test/ is excluded from the image — copy first)
# Quote the glob: Node 24's test runner treats a bare directory argument as a
# module to load and fails with MODULE_NOT_FOUND.
docker cp test/. mikrodash:/app/test
docker exec mikrodash node --test '/app/test/*.test.js'

# Run a single test file
docker exec mikrodash node --test /app/test/production-resilience-regressions.test.js

# Run locally without Docker (after npm install + node patch-routeros.js)
node src/index.js
```

---

## Architecture

MikroDash is a **single-process Node.js server** (no build step, plain CommonJS). The browser gets a static SPA; all live data flows over a single Socket.IO connection. There are no REST endpoints for live data — everything is pushed server→client.

```
RouterOS binary API (TCP)
        │
   src/routeros/client.js   ← ROS class: connectLoop, write(), stream()
        │
   src/collectors/          ← 16 domain collectors, orchestrated by index.js
        │                                        │
   Socket.IO emit            ← one named event   src/db-writer.js → src/db.js (SQLite)
        │                      per collector        time-series: traffic, ping, bandwidth
   public/app.js             ← ALL frontend logic in one file
```

**`src/index.js`** is the hub:
- `buildSession(routerCfg)` — creates ROS + all 16 collectors wired together
- `teardownSession(session)` — clean shutdown for hot-swap
- `sendInitialState(socket)` — replays `lastPayload` from every collector on new connect
- `connTableCache` — shared between `connections.js` and `bandwidth.js`
- All REST endpoints (settings, routers, dashboard layout, auth)

**Collectors** follow a strict contract: `start()`, `stop()`, `lastPayload`, `pollMs`, `state.last<n>Ts`, `state.last<n>Err`. See `AI_CONTEXT.md` → "Collector delivery model" for the streaming-vs-polling breakdown for each collector.

**Settings** are AES-256-GCM encrypted at `/data/settings.json` — managed by `src/settings.js` (`load`, `save`, `getPublic`, `isMasked`). Router configs live at `/data/routers.json` via `src/routers.js`; `activeRouterId` in settings points to the active entry.

**Database** (`src/db.js`) — SQLite via `better-sqlite3`, opened at `/data/mikrodash.db`. Schema is managed by numbered migrations in `MIGRATIONS[]`. Stores time-series data: `ping_samples`, `traffic_samples`, `bandwidth_usage`, `alert_events`, `connectivity_events` — plus the RBAC tables `sites`, `groups`, `group_members`, `grants` (issue #78), which are **not** time-series and are deliberately unreachable from `purge()` / `deleteRouterData()`, both of which name the five sample/event tables explicitly. A retention sweep must never be able to delete authorization state. `src/db-writer.js` is the write facade: it accumulates raw per-second traffic/bandwidth samples into 1-minute bucketed averages before flushing, so the DB never sees raw per-second rows. Call `db.open()` once at startup; `db.close()` on shutdown.

**Auth** — two modes, resolved by `_authMode()` in `index.js` (`settings.authMode`, anything other than `'none'` means `'modern'`):
- `modern` (default): session auth (`src/auth/sessionStore.js` + `src/users.js`) — cookie-based (`mikrodash_sid`), users stored in `/data/users.json` with scrypt-hashed passwords.

  **`users.json` must stay a bare JSON array, and must not move into SQLite.** `_readFile()` (`src/users.js:30`) returns `[]` for anything that is not an array. So if the file gained a version wrapper, or users moved into the database, a binary rolled back to an earlier release would read **zero users** — which re-opens `POST /api/users/setup`, an unauthenticated route (`_MODERN_PUBLIC`). Anyone able to reach the instance could claim it. Keeping the file exactly as it is makes a downgrade degrade safe-closed. This is a security property, not a preference: sites, groups, roles, grants and layouts live in SQLite precisely because none of them has this failure mode.

  **Authorization is RBAC (`src/rbac.js`), and `Rbac.can(session, permission, target)` is the only place a permission decision is made.** A principal (a user, or a group of users) holds a **role** over a **scope**: global, a site, or a single router. Grants live in SQLite; sites group routers (one site per router, or none). Never add a second authorization helper: `_requireAdmin`, `_routerPermitted` and `_scopeRouterId` were deleted precisely so no route can reach for one.
  - **Roles are rows, not code** (issue #108). A role is a matrix of `page → read|write` in `role_pages`; an absent row means no access, and `write` implies `read`. `src/pages.js` is the one definition of a page — `PAGE_NAV_MAP`, `_PAGE_STREAM_ROOMS` and `_PAGE_SETTING_KEYS` all derive from it, because they used to drift (the Topology toggle silently did nothing for a release). Three roles are seeded: **Administrator** (`builtin=1`, reach is structural so future permissions are covered with no data migration — it deliberately has *zero* `role_pages` rows), **Operator** and **Read Only**, both editable.
  - **Resolution is a union, never a rank.** Custom roles have no total order, so `viewFor()` accumulates a *set* of role ids per scope and `can()` returns true if any confers the permission. Do not reintroduce a `_stronger()`-style ranking.
  - `READ_CONFERS` / `WRITE_CONFERS` project the page matrix onto the action vocabulary, so existing `requirePerm()` call sites keep working. **`_roleDef()` then strips every `GLOBAL_ONLY` permission except `system:settings`** — that strip, not the tables, is what makes system administration unreachable from a page. `router:secrets` is conferred by no page.
  - **`Rbac.canPage()` must not consult `Settings`.** The install-wide page toggle is ANDed with the role in `_pageAllowed()` (`src/index.js`) so the two stay separately testable. Permissions gate *delivery*, never *collection*: collectors are per-router-session and shared across sockets, so `sendInitialState`'s replays and the `page:focus`/`dashcard:focus` room joins are the enforcement points.
  - Any write to `roles`/`role_pages` must call `Rbac.bump()` — a role edit changes the answer for every principal holding it, and a missed bump is silent until restart.
  - `GLOBAL_ONLY` permissions (`system:principals`, `system:settings`, `system:db`, `router:create`) can **never** be satisfied by a site- or router-scoped grant — that is what makes a site scope a security boundary rather than a default view.
  - A scoped permission called with no target **denies**. The old `allowedRouterIds.length === 0` fallthrough meant "no restriction recorded" granted everything; do not reintroduce that shape.
  - `role` and `allowedRouterIds` survive on the user record, and `grants.role` survives as a column, purely as **downgrade mirrors** — written, never read. Nothing decides anything with them: `Users.adminCount()` was deleted rather than left unused, because a count of records cannot see an administrator whose grant is held through a group. Ask `Rbac.wouldOrphanGlobalAdmin()` instead. `Rbac.syncUserGrants()` still projects the legacy pair onto grants, but **only when a caller actually sends those fields** — it deletes every grant the user holds and rebuilds them, so running it on an ordinary username edit would wipe access granted in the editor. Keep the mirrors: without `grants.role`, a rolled-back binary reads `role: undefined` and throws on every authorization call. Login UI at `public/login.html` + `public/login.js`; `public/preflight.js` is the client-side auth gate loaded before `app.js`.
- `none`: no authentication at all (every request is implicitly admin) — intended for trusted LANs only; the server logs a loud warning.

(Legacy HTTP Basic Auth was removed in 0.5.45 — `src/auth/basicAuth.js` and the `BASIC_AUTH_*` env vars no longer exist. A stored `authMode` of `'basic'` or an absent value is migrated to `'modern'` at startup.)

---

## Hard constraints

- **No build step.** CommonJS only — no TypeScript, no bundler, no transpiler.
- **No new runtime deps** without explicit approval. (`better-sqlite3` is approved and in use.)
- **Streaming-first — but per-router polling is a supported escape hatch.** New code still prefers
  `/listen` (event-driven) over `=interval=N` (timed push) over `setInterval` (polling), and Stream
  remains the default. Since #105 each router carries its own `collection` block (mode, disabled
  collectors, interval overrides), because concurrent open channels — not data volume — are what
  overwhelm small hardware such as a hAP ac2. **Every new pollable collector must therefore ship
  both paths**, resolved through `src/collection.js`, never by reading `Settings` directly. See
  `AI_CONTEXT.md` for the full rule.
- **No CDN.** All frontend assets live in `public/vendor/` (read-only — never modify).
- **`sanitizeErr(e)`** before any error reaches the browser. Never send raw `.message` or stack traces.
- **`esc()`** around every user-supplied string injected into HTML in `app.js`.
- **Credentials** are encrypted at rest. Always call `isMasked()` before writing a credential field on save.
- **User passwords** are scrypt-hashed (`src/users.js`). Never store or log plaintext passwords. `verifyPassword()` uses `crypto.timingSafeEqual` — don't replace it with a simple string compare.
- **Session tokens** are 32-byte random hex strings. Never expose them in logs, error messages, or API responses beyond the `Set-Cookie` header. Use `sessionStore.parseCookieHeader()` + `sessionStore.getSession()` to validate incoming requests.

---

## Versioning rule

**Do not bump `package.json` version or edit `CHANGELOG.md` during a working session.** Version bumps happen only when the user says "package it up" or equivalent. One bump covers the entire session.

---

## Testing

- Runner: `node --test` only — no Jest, Mocha, or other frameworks.
- Test the collector's output payload shape and values, not internal implementation details.
- Fake ROS/IO patterns and a coverage checklist for new collectors are in `AI_CONTEXT.md` → "Testing conventions".
- **When editing any collector, update its tests in the same edit.** API changes (new method names, io.to() vs io.emit(), stream vs poll) must be reflected immediately or tests will drift.
- A `.git/hooks/pre-push` hook runs the full suite before every push — fix failures before pushing.

---

## Workflow rules

- The container rebuilds **once per turn**, not once per edit — a Stop hook runs `docker compose build && docker compose up -d`, checksum-gated over `src/`, `public/`, `patch-routeros.js`, `package*.json`, `Dockerfile` and `docker-compose.yml`. Run the rebuild by hand only when you need it mid-turn (running the tests, driving the UI); the gate then makes the turn-end run a no-op.
- Append to `Changes.md` after every file edit (not in a batch at the end).
- Always confirm before `git push` or Docker push.
- A `v*.*.*` git tag is required alongside every version bump so GitHub Actions publishes the Docker image.

---

## Behavioral guidelines

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.
