# What is left to change architecturally, re-measured

> **STATUS: one item delivered, one half-delivered, one still open.**
>
> Written 2026-08-27 against the JavaScript app at v0.7.38, when it was deferred until after
> cutover. **Re-measured 2026-08-31** against the Go + TypeScript tree at v0.8.1, which is what the
> original asked for: "Re-measure before acting -- if the port has already collapsed the wire
> surface or the collector fan-out, parts of this are answered."
>
> Parts of it were. The numbers below are the current ones; where a claim died, it is struck and the
> reason kept rather than deleted, because a closed item reads exactly like one that never existed.

## Context

This answers a question rather than proposing work: *if the app could depart from its inherited
architecture to be more efficient, easier to manage and smaller, what should change and why?*

The honest test of an architecture is which bug classes it makes impossible, not which it makes
unlikely. Every item below is anchored to a defect that actually shipped.

**Measured 2026-08-31, on the Go + TypeScript tree:**

| | v0.7.38 (JS) | v0.8.1 (Go + TS) |
|---|---|---|
| frontend | `public/app.js`, 16,602 lines, one file | 114 modules, 32,088 lines, largest 1,318 |
| server | `src/index.js`, 7,421 lines | `internal/`, package per concern |
| collectors | 30 files, 11,775 lines | 29 files, 14,666 lines |
| wire surface | 93 emit + 47 handlers + 78 REST = **218** | 85 emits + 85 handlers + 30 REST = **200** |
| RouterOS I/O sites | 28 `stream()`, 108 `write()` | 52 `Do()`, 29 poll loops |
| collectors started on connect | 14 (pool: 3) | 16 (pool: 5) |
| never-re-read caches | 3 | **0** |
| typed payload definitions | 0 | 3,194 json-tagged Go fields |

Counts from the repo's own audits (`event-audit`, `emit-audit`, `endpoint-audit`), not from
hand-written greps -- an earlier pass of this revision used regexes and got the event count wrong by
70%.

## 1. One schema for the wire, generated both ways — STILL OPEN, and the case is now sharper

**Still the highest-leverage change, and it is not about performance.**

The original argument was that 218 contracts existed with none typed. Half of that is now fixed:
**3,194 json-tagged struct fields** define every payload once, in Go, with a compiler enforcing them.

But the *other* end is still hand-written. `web/src/pages/routing-types.ts` opens:

> "The routing:update payload, as `internal/collect/routing.go` emits it."

That comment is the contract. Every payload is still defined **twice** -- once in a Go struct, once
in a TypeScript interface that a human keeps in step by reading the Go. The failure mode is
unchanged: nothing breaks when they drift, and the page renders.

**What makes this sharper than in August:** the generation machinery already exists. `web/src/gen/`
holds four generated modules -- and they are generated from the *old JavaScript* corpus
(`grid-tables`, `appearance-tables`, `alert-filters`, `modals`), not from the Go structs. The
pipeline points backwards at an implementation that no longer exists.

Pointing it at `internal/` instead would turn a hand-maintained mirror into a build artefact. No IDL
is needed; the structs and their tags are already the schema.

## 2. One connection per router, one reader, collectors as consumers — HALF DELIVERED

The documented bottleneck was **concurrent open channels, not data volume**. The port halved the
I/O sites: **52 `Do()` calls against the old 108 `write()`**, on one shared connection per session
rather than a channel per collector. The #119 class -- a batch published before its delimiter --
became structurally impossible, exactly as predicted.

**What did not change is ownership.** Each collector still owns its own loop: **29 `newPollLoop`
instances**, each with its own start/stop/suspend/resume and its own "am I connected" test. That is
the half still worth doing, and on 2026-08-31 it cost a real bug:

> Every collector's `Resume()` opens with `if ros.Connected()`. A page focus arriving before the
> dial completed was silently dropped by all thirteen that have one, and nothing asked again -- so
> the Connections card sat empty until the connection dropped and returned. Fixed by latching the
> resume in `Session` (`80acdcb`), which is a patch over the ownership problem rather than a
> resolution of it.

With one reader owning the connection and collectors as pure consumers, that race has nowhere to
live: there is one lifecycle to get right instead of twenty-nine.

Two smaller counts moved the wrong way and are worth watching rather than acting on: a session now
starts **16** collectors on connect (was 14) and the background pool **5** (was 3).

## ~~3. Modules instead of one 16,602-line script~~ — DELIVERED

Shipped in v0.8.0. **114 TypeScript modules, largest 1,318 lines**, against one 16,602-line file of
IIFEs sharing a global scope by convention.

The predicted benefit landed and is measurable: the cross-IIFE scope trap -- a function invisible to
its siblings, throwing at *call* time so the page renders and the failure waits for a click -- is
gone. It killed both device-modal buttons in #117 and passed the entire suite. `tsc` now catches
that class before anything runs, and the espree scope-checker written for it was retired with the
file it checked.

## What I would deliberately keep

Unchanged from August, and all still true:

- **The collector contract.** Survives change 2; only who owns the socket changes.
- **Read-time normalisation as migration.** A half-migrated file is always valid. The port leaned on
  this again for the boolean coercions: values stored wrongly correct themselves on read.
- **The downgrade-mirror doctrine.** `grants.role` and the scalar `siteId` are written and never
  read, purely so a rolled-back binary keeps working. That paid out at cutover: `v0.7.40` can still
  read everything `v0.8.1` writes.
- **`users.json` as a bare JSON array.** A security property: any other shape makes a downgrade read
  zero users and re-open the unauthenticated setup route.
- **RBAC as a union with one decision point.**

## ~~On the never-re-read caches~~ — CLOSED

The three JS caches (`settings`, `routers`, `users`) each began `if (_cache) return _cache` and
never re-read. `internal/store` has none: `Settings()` and `Routers()` call `os.ReadFile` on every
invocation. The item existed because two processes could not see each other's writes, and it was one
of the things stopping the two apps running side by side -- so cutover closed it twice over.

## On footprint

The static binary against Node plus `node_modules` needed no argument and was measured at cutover:
**180 MB against 775 MB**, no runtime Node, ARMv7 restored.

What is left is the geo database, now **three quarters of the image**. That is the obvious target if
footprint ever matters more than answer-parity, and it is a data decision rather than an
architectural one.

## The thing I would resist

**Do not rewrite the payload contracts to make change 1 easier.** The rule that nothing user-visible
may change was correct and hard-won, and it is still the thing 136 gates enforce. Schema generation
is compatible with it: generate types *for the payloads that exist*. Redesigning them at the same
time would move two variables at once, with no way to tell which one broke the page.

The new version of that warning: **the recordings are frozen.** They cannot be regenerated, because
the implementation that produced them is deleted. A deliberate payload change now means re-aiming or
retiring a gate, not re-recording it -- so the cost of changing a contract went up at cutover, not
down.
