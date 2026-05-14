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
docker cp test/ mikrodash:/app/test
docker exec mikrodash node --test /app/test/

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
        │
   Socket.IO emit            ← one named event per collector (e.g. system:data)
        │
   public/app.js             ← ALL frontend logic in one file
```

**`src/index.js`** is the hub:
- `buildSession(routerCfg)` — creates ROS + all 15 collectors wired together
- `teardownSession(session)` — clean shutdown for hot-swap
- `sendInitialState(socket)` — replays `lastPayload` from every collector on new connect
- `connTableCache` — shared between `connections.js` and `bandwidth.js`
- All REST endpoints (settings, routers, dashboard layout)

**Collectors** follow a strict contract: `start()`, `stop()`, `lastPayload`, `pollMs`, `state.last<n>Ts`, `state.last<n>Err`. See `AI_CONTEXT.md` → "Collector delivery model" for the streaming-vs-polling breakdown for each collector.

**Settings** are AES-256-GCM encrypted at `/data/settings.json` — managed by `src/settings.js` (`load`, `save`, `getPublic`, `isMasked`). Router configs live at `/data/routers.json` via `src/routers.js`; `activeRouterId` in settings points to the active entry.

---

## Hard constraints

- **No build step.** CommonJS only — no TypeScript, no bundler, no transpiler.
- **No new runtime deps** without explicit approval.
- **Streaming-first.** Prefer `/listen` (event-driven) over `=interval=N` (timed push) over `setInterval` (polling). See `AI_CONTEXT.md` for the full rule.
- **No CDN.** All frontend assets live in `public/vendor/` (read-only — never modify).
- **`sanitizeErr(e)`** before any error reaches the browser. Never send raw `.message` or stack traces.
- **`esc()`** around every user-supplied string injected into HTML in `app.js`.
- **Credentials** are encrypted at rest. Always call `isMasked()` before writing a credential field on save.

---

## Versioning rule

**Do not bump `package.json` version or edit `CHANGELOG.md` during a working session.** Version bumps happen only when the user says "package it up" or equivalent. One bump covers the entire session.

---

## Testing

- Runner: `node --test` only — no Jest, Mocha, or other frameworks.
- Test the collector's output payload shape and values, not internal implementation details.
- Fake ROS/IO patterns and a coverage checklist for new collectors are in `AI_CONTEXT.md` → "Testing conventions".

---

## Workflow rules

- Rebuild the container after every source edit: `docker compose build && docker compose up -d`.
- Append to `Changes.md` after every file edit (not in a batch at the end).
- Always confirm before `git push` or Docker push.
- A `v*.*.*` git tag is required alongside every version bump so GitHub Actions publishes the Docker image.
