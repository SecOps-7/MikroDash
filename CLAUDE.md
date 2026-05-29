# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **Full context lives in `AI_CONTEXT.md`** — it covers the collector pattern, RouterOS quirks, security invariants, and testing conventions in detail. Read it before making architectural decisions.

---

## Commands

```bash
# Rebuild and restart the container (do this after every source change)
docker compose build && docker compose up -d

# View live logs
docker logs -f mikrodash

# Run all tests (test/ is excluded from the image — copy first)
docker cp test/. mikrodash:/app/test
docker exec mikrodash node --test --test-force-exit /app/test/

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
   src/collectors/          ← 15 domain collectors, orchestrated by index.js
        │                                        │
   Socket.IO emit            ← one named event   src/db-writer.js → src/db.js (SQLite)
        │                      per collector        time-series: traffic, ping, bandwidth
   public/app.js             ← ALL frontend logic in one file
```

**`src/index.js`** is the hub:
- `buildSession(routerCfg)` — creates ROS + all 15 collectors wired together
- `teardownSession(session)` — clean shutdown for hot-swap
- `sendInitialState(socket)` — replays `lastPayload` from every collector on new connect
- `connTableCache` — shared between `connections.js` and `bandwidth.js`
- All REST endpoints (settings, routers, dashboard layout, auth)

**Collectors** follow a strict contract: `start()`, `stop()`, `lastPayload`, `pollMs`, `state.last<n>Ts`, `state.last<n>Err`. See `AI_CONTEXT.md` → "Collector delivery model" for the streaming-vs-polling breakdown for each collector.

**Settings** are AES-256-GCM encrypted at `/data/settings.json` — managed by `src/settings.js` (`load`, `save`, `getPublic`, `isMasked`). Router configs live at `/data/routers.json` via `src/routers.js`; `activeRouterId` in settings points to the active entry.

**Database** (`src/db.js`) — SQLite via `better-sqlite3`, opened at `/data/mikrodash.db`. Schema is managed by numbered migrations in `MIGRATIONS[]`. Stores time-series data: `ping_samples`, `traffic_samples`, `bandwidth_usage`, `alert_events`, `connectivity_events`. `src/db-writer.js` is the write facade: it accumulates raw per-second traffic/bandwidth samples into 1-minute bucketed averages before flushing, so the DB never sees raw per-second rows. Call `db.open()` once at startup; `db.close()` on shutdown.

**Auth** — two layers that co-exist:
- Legacy HTTP Basic Auth (`src/auth/basicAuth.js`): enabled via `BASIC_AUTH_USER`/`BASIC_AUTH_PASS` env vars; covers all HTTP routes + Socket.IO engine.
- Session auth (`src/auth/sessionStore.js` + `src/users.js`): cookie-based (`mikrodash_sid`), users stored in `/data/users.json` with scrypt-hashed passwords, roles `admin`/`viewer`, optional `allowedRouterIds` per user. Login UI at `public/login.html` + `public/login.js`; `public/preflight.js` is the client-side auth gate loaded before `app.js`.

---

## Hard constraints

- **No build step.** CommonJS only — no TypeScript, no bundler, no transpiler.
- **No new runtime deps** without explicit approval. (`better-sqlite3` is approved and in use.)
- **Streaming-first.** Prefer `/listen` (event-driven) over `=interval=N` (timed push) over `setInterval` (polling). See `AI_CONTEXT.md` for the full rule.
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

- Rebuild the container after every source edit: `docker compose build && docker compose up -d`.
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
