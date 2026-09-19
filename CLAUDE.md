# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**MikroDash** is a web dashboard for MikroTik RouterOS routers: a Go server that talks to each router
over the RouterOS binary API, and a TypeScript frontend served by the same binary over HTTP and a
WebSocket.

---

## Where to look

| Question | Where |
|---|---|
| How the collector layer works | `Collector-Architecture.md` — the three layers, gated so it cannot go stale |
| Which RouterOS commands this app uses | `docs/routeros-api-surface.md` — frozen; extend it by hand from the RouterOS docs |
| What a RouterOS menu *can* hold | **rosetta** (MCP, configured in `.mcp.json`), or `help.mikrotik.com` |
| What a collector returns | replay a fixture through its `internal/collect` test, rather than reading the collector and guessing |

Go files here are small and purposeful: read them whole.

---

## Commands

Go runs in a container, so no local Go toolchain is needed. **Go 1.27 is required**: Go
supports only its two newest releases, and `golang.org/x/crypto` follows them.

```bash
# Everything the repo can check: gofmt, vet, `go test ./...`, the generated-code
# checks, the TypeScript type checker and the frontend tests. Nothing is listed in
# the script — `go test ./...` finds new Go tests and `web/test/run.mjs` globs
# `*.test.ts` — so a new check runs without being registered anywhere.
sh tools/verify.sh
sh tools/verify.sh --no-docker   # skip the Go half

# The two halves on their own.
docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine sh -c "go vet ./... && go test ./..."
cd web && npm test

# The production image: frontend, binary, geo tables, Alpine runtime. /data is its only mount.
docker build -t mikrodash:latest .
docker compose up -d
```

**Deploy with `docker compose up -d`, never `docker restart`.** `restart` reuses the image the
container was created from, so a rebuild moves the tag and the running container does not. Before
trusting a live test, check which binary is running:

```bash
docker inspect MikroDash --format '{{.Image}}'      # compare: docker images mikrodash:latest
docker exec MikroDash grep -c '<a symbol only the new code has>' /usr/local/bin/mikrodash
```

**Clean up after a build, by id.** Each build leaves the previous image untagged. Never run
`docker image prune -a` or `docker volume prune`: `tools/verify.sh` mounts the named caches
`mikrodash-gomod` and `mikrodash-gocache`, which look unused between runs, and `mikrodash_data` holds
the app's data.

```bash
docker images -f dangling=true -q |
  while read id; do [ -z "$(docker ps -aq --filter ancestor=$id)" ] && docker rmi "$id"; done
```

**Read-only tools that run against a real router or a real `/data`.** They are not unit tests, and a
green suite does not substitute for them.

```bash
# Protocol conformance. -data decrypts the router's password out of the store, so
# no credential is typed, printed or written down.
docker run --rm --network host -v "$PWD":/src -w /src -v /path/to/data:/data:ro \
  golang:1.27-alpine go run ./cmd/conformance -data /data -router "<label>"

# On-disk compatibility: the store can still read a real /data.
docker run --rm -v mikrodash_data:/data:ro -v "$PWD":/src -w /src \
  golang:1.27-alpine go run ./cmd/compat -data /data

# What streaming every interface would cost a router. Run it twice: the CPU delta
# is noise, and one run reads as a number.
docker run --rm --network host -v "$PWD":/src -w /src -v mikrodash_data:/data:ro \
  golang:1.27-alpine go run ./cmd/streamcost -data /data -router "<label>"
```

---

## Architecture

```
RouterOS binary API (TCP/TLS)
        |
  internal/routeros/   an ADAPTER over github.com/go-routeros/routeros/v3, not a protocol
                       implementation: the vocabulary the app speaks (Cmd, Reply, Trap, Config)
  internal/roscache/   the read cache and per-router scheduler: one read per menu for every asker
  internal/roslimit/   the per-router cap on commands in flight
  internal/collect/    the collectors, one per RouterOS subsystem
  internal/session/    one Session per router, owning the connection its collectors share. It also
                       holds routers nobody is watching, for alerting, history or a known status
                       (internal/session/needs.go)
  internal/hub/        WebSocket rooms, and the declared events every send goes through
  internal/alert/      the alert rules — pure: rows in, verdict out
  internal/guard/      the write guards — also pure (see "Write guards")
  internal/store/      /data: AES-256-GCM settings, scrypt users, routers.json
  internal/db/         SQLite history and audit (modernc.org/sqlite: pure Go, so the binary is static)
  internal/server/     HTTP routes and the WebSocket protocol
        |
  web/src/             the TypeScript frontend
```

The collector layer — acquisition, derivation, views — is described in full in
`Collector-Architecture.md`.

**Three things about `internal/routeros` are load-bearing:**

1. **Async mode is mandatory.** `Dial` calls `Async()`, which gives the client a tag map and
   therefore somewhere to discard a sentence addressed to a cancelled tag. Sync mode keeps no tag
   map, and the failure takes down a connection every collector shares.
2. **The hardware claims are version-qualified.** They were measured on RouterOS 7.24;
   `internal/routeros/client.go` carries them in full. "Not reproduced" is not "never true".
3. **go-routeros is patched.** `go.mod` replaces it with `third_party/go-routeros`, whose
   `RunArgsContext` registers a command's tag before sending it. v3.0.1 did it after, and a reply
   arriving in between was lost for ever, holding a `roslimit` slot; eight of those stopped every
   poll on a router. Drop the copy only for a release that passes
   `TestNoReplyIsLostBeforeItsTagIsRegistered`. Its debug trace also masks credentials, which
   `TestTracingNeverLogsACredential` pins. `PATCHES.md` there has the rest.

**Knowingly accepted:** go-routeros returns on the first `!done`, so block boundaries are invisible
here. `cmd/conformance` tests completeness instead — the bulk registration-table read against the
sum of per-interface reads.

**`internal/store` must read `/data` exactly as existing installs wrote it**, or users are locked
out. Three traps, all documented in the package header: the scrypt salt is a **string**, not decoded
bytes; the envelope is `iv‖tag‖ciphertext` while Go's `Open` wants `ciphertext‖tag`; and
`users.json` must stay a bare JSON array, which is a security property rather than a preference.
`cmd/compat` checks all three against a real `/data`.

**Database identity columns have no blanket rule.** `grants.principal_id`, `audit_events.actor_id`,
`user_layouts.user_id` and `report_schedules.created_by` hold the user ID; `alert_events.acknowledged_by` and
`audit_events.actor_name` hold the username. A writer reaching for the other one is invisible to a
round-trip test, because one implementation agrees with itself whatever it wrote — read the real
table.

---

## Hard constraints

- **Never delete a check to make a change quiet.** A check removed without a reason reads exactly
  like one that never existed.
- **"More efficient" means fewer router channels, not faster payload assembly.** The bottleneck is
  concurrent API channels on the MikroTik, not CPU here.
- **Go stdlib first, but not stdlib-only.** A dependency needs a reason better than convenience.
  Seven are in: `golang.org/x/crypto` (scrypt, which the user store's key derivation demands),
  `modernc.org/sqlite` (pure Go, no cgo, so the binary stays static), `github.com/coder/websocket`,
  `github.com/go-routeros/routeros/v3` (a patched copy, `third_party/go-routeros`: see
  "Three things about `internal/routeros`"), `github.com/go-pdf/fpdf`,
  `github.com/oschwald/maxminddb-golang/v2` (the DB-IP geo reader) and `github.com/evanw/esbuild`.
  - **esbuild runs through its Go API** in `cmd/webbuild`, so the image needs no JavaScript
    runtime. Node is a development dependency only: `tsc --noEmit` and the tests in `web/test/`.
  - **fpdf walks bytes against a cp1252 table**, so `reportpdf.EncodeText` is mandatory on every
    draw and every measurement. It does not kern; `internal/reportpdf/metrics_test.go` pins that,
    so an fpdf that learns to kern fails the suite rather than leaving a note lying.
- **No credential is ever written to a fixture, and nothing identifying either.** This repository is
  public, so anything in a committed file is public. See "Fixtures".

---

## Fixtures

`testdata/fixtures/` holds real captures from live hardware, replayed into the collectors by the
`internal/collect` tests — which is what stops this code re-deriving RouterOS behaviour. Captures
are anonymised at source by `tools/capture-fixtures.js`:

- **Preserved** (structural): interface and profile names, bridge names, VLAN ids, RouterOS ids, and
  the joins between them. `name` is deliberately not anonymised: the WiFi collectors read the band
  out of an interface called "2.4GHz WiFi", so tokenising it breaks a real code path.
- **Scrubbed**: SSIDs, MACs (into `02:` locally-administered), IPs (into `198.51.100.0/24`,
  TEST-NET-2), serials, router identity, country, comments, DHCP hostnames.
- **Dropped entirely**: any key ending in a credential — `passphrase`, `password`, `private-key`,
  `pre-shared-key`.
- **Free text is handled by learning, not by key names.** A log line reads
  `…@5GHz WiFi3(<ssid>) connected`, and no rule about keys catches that. The tool reads the router's
  SSIDs, identity and DHCP hostnames first and replaces them as substrings, so the message keeps its
  shape and only the identifying part moves.

`assertClean()` is a **positive** check — every value under an identifying key must be a token the
tool minted — because a denylist misses the value nobody thought of.

---

## Verifying against MikroTik's documentation

Check every menu path, property name and enumerated value against the official docs before using
it: **rosetta** (`.mcp.json`) first, `help.mikrotik.com` as the fallback. MCP servers connect at
session start, so a session older than that config must be restarted before the tools appear.

**A fixture and the documentation answer different questions, and both are needed.** A fixture
proves what the code does with the rows one router returned; the docs say which rows a router *may*
return. `dnsStatic` once offered six of the nine record types RouterOS supports, and because a
`select` validates against its options, a router holding an MX record opened a form showing "A" —
saving rewrote the record. No fixture could catch that. Enumerated values deserve the closest
reading.

---

## Write guards

**Every guard in `internal/guard/` is pure** — rows in, verdict out, no router I/O — which makes them
the cheapest code here to test. A guard is reached one of two ways:

- **Declared by a resource**, and run by the generic resource write path (`verdictFor` in
  `internal/server/resource.go`). Only names in `portedGuards` in that file can be evaluated; the
  map is the list, not copied here because a copy drifts. **A resource declaring any other guard
  has its writes refused**, not logged and allowed — pinned by `internal/server/guard_test.go`.
  Adding a guard to that path means adding it to the map, deliberately.
- **Called directly by a page handler**: the user and WAN pages call their guards' `Check…`
  functions themselves (`internal/server/rosusers.go`, `wan.go`).

Guard names do not map one-to-one onto file names — `capsmanguard.go` is what provides
`capsmanPush`. Read `internal/guard/` before concluding one is missing, and read the map rather than
any summary of it.

---

## A new page's furniture

Every page, generated or hand-built, has its **title at the far left**, a **blue count pill** beside
it (`card-badge`, plus `active-blue` when it counts something), then **its tabs to the right of the
title** (`stab-bar rttab-bar`), with actions alone in the right-hand `.hdr-actions` corner,
**sortable table headers** (`renderSortHeader`/`sortRows` in `web/src/dom.ts`), **its own nav
icon that suits the page**, and **key columns as coloured pill labels** (a state, an action, a flag)
rather than plain text.

- **Generated pages get all of it for free**: the layout from `cmd/areagen`'s shell template, the pill
  and the sort from `web/src/pages/area.ts`. Held by `TestEveryAreaShellHasItsTitleFirstThenItsTabs`,
  `web/test/area-sort.test.ts` and, for icons, `TestEveryAreaHasItsOwnIcon`.
- **Pills are declared by kind, never by colour**: `Table.Pills` in `internal/areas` maps a column
  to one of `PillKinds`, and `CommonPills` covers the shared flags (`disabled`, `invalid`,
  `inactive`, `dynamic`, `running`). The colours per kind live once in `area.ts`, in the hand-built
  pages' `vpn-hs-badge` styles and the Firewall page's `actionBadge`; `PillKind` is generated, so a
  kind on one side only fails tsc. Held by `TestEveryAreaPillNamesARealColumnAndKind` and
  `web/test/area-pills.test.ts`.
- **A hand-built page must copy the same markup and helpers**; nothing generates them for it.
- **Rx and Tx have fixed colours, and so do protocols.** An Rx value is `var(--accent-rx)` (blue) and
  a Tx value `var(--accent-tx)` (green), everywhere. A protocol column is `protoPill` in
  `web/src/dom.ts` (tcp blue, udp green, icmp amber, anything else grey), not a second scheme.
- **An ordered table** (routing rules, IPsec policies, OSPF templates: the first match decides) does
  **not** sort. It is always in the router's order, with its move arrows — the Queues page's rule.

## Page keys — one word, six meanings

`internal/pages` is the list. The same string is used as six different things, and they do not all
move together:

| as | where | renaming it |
|---|---|---|
| URL path | `internal/server` registers one route per page | changes a public link |
| markup id | `#page-<key>` in `web/src/ui/page-<key>.html` | must move with the file |
| room name | collectors emit to `page-<key>` | a protocol change, both sides at once |
| **permission key** | `rbac.PageKeys`, and `role_pages.page` in the database | **an unknown key is denied before any role is consulted** |
| pagechange detail | `detail === '<key>'` in the page modules | a missed one stops that page loading |
| **visibility guard** | `isVisible('<key>')` / `pageVisible('<key>')` | **a stale one is permanently false: the page renders once and never updates** |

- **A rename needs a `pages.Renamed` entry in the same commit.** `rbac.PageKeys` reads
  `internal/pages`, but `role_pages.page` is data in each install's database, and a stored grant
  naming an old key silently stops conferring that page. `pages.Renamed` is **append-only** — a
  promise to installed databases — and `(*db.DB).RenamePageGrants` applies it at startup from
  `cmd/mikrodash`, not from `db.Open`, because `cmd/compat` opens a real `/data` read-only.
- **Visibility guards are not found by grepping the pagechange spelling.** The socket still
  delivers and the collector still emits; the handler just declines to render.
  `TestVisibilityGuardsNameRealPages` fails on a stale one.
- **`web/src/gen/` is generated — never edit it by hand.** `cmd/tsgen` and `cmd/pagesgen` generate
  from Go, and `tools/*-ts.js` from JSON under `testdata/`. Edit the source and regenerate: a
  hand-edited `.ts` is reverted by the next regeneration. `TestFrozenPageTablesNameRealPages` checks
  the JSON.
- **Not a page key, though it looks like one:** `Layout(user, "dashboard")` and
  `Layout(user, "topology")` in `internal/server/layouts_api.go` are row keys in `user_layouts`,
  holding every layout a user has saved. Renaming them orphans that data.
- **The dashboard is served at `/home`** — the one page whose URL differs from its key, declared as
  `Path` on its entry.

---

## WebSocket events

- **Every event is declared once, with its payload type:** `hub.Declare[T]("name")`, in
  `internal/collect/events.go`, `internal/server/events.go`, or beside its sender in
  `internal/session`. Every send goes through the declared event — `EvX.Send(hub, client, p)`,
  `EvX.Broadcast(...)`, `EvX.Emit(relay, room, p)` — so the compiler checks each payload, and a
  string cannot reach the wire any other way. See `internal/hub/event.go`.
- **The browser's types are generated from those declarations** by `cmd/tsgen` into
  `web/src/gen/payloads.ts`: an interface for every struct a payload reaches, and `Events`, which
  types every `socket.on` handler by its event. A payload that is a Go map is typed by hand in
  `web/src/events-hand.ts`, and tsc fails if that file misses a map event or types one that is not.
- **Go never sends a null array.** Generated slices are `T[]`. `TestNoPayloadSendsANullArray`
  (internal/collect) and `TestNoServerPayloadSendsANullArray` (internal/server) build every
  payload from empty input and fail on any nil slice.
- **No cast on a payload.** A handler that needs `as` is disagreeing with the Go side; fix the Go
  or the page, not the type.

---

## Verification

| | |
|---|---|
| `internal/verify/` | 77 Go tests. Static checks over the current source: credentials, cited paths, the WebSocket vocabulary both ways, endpoints, selectors, module reachability, identity columns, the blur-suspend guard, the fast/slow poll ledger, the shared-menu ledger, fixture schemas, that every page-key literal names a real page, that `Collector-Architecture.md` describes the collector layer the code has, and that the numbers in this file are true. Test-only, so nothing links them into the binary. |
| `web/test/` | 64 test files that bundle the app's TypeScript with esbuild and run it against a DOM shim. See `web/test/README.md` for why they are executed rather than type-checked. |
| package tests | `go test ./...`, standard library `testing` only. |

**Two rules every check follows:**

1. **A ledger fails in BOTH directions.** An unrecorded gap is a failure, and a recorded gap that has
   closed is also a failure — otherwise a ledger becomes a list of excuses nobody re-measures.
2. **A check must not read itself.** Ledgers quote the event names, settings keys and paths they
   look for, so a scan that included them would find everything it names. `isTestSource` exists for
   that.

**The claim these tests make is that the app agrees with itself.** Nothing compares its rendering
against an external reference, which is a weaker claim than "matches what shipped", and it is
stated here rather than left to be discovered.

**A premise that has expired reads exactly like one that is true, and nothing fails.** That is why
the numbers in this file and in `Collector-Architecture.md` are re-measured by tests rather than
trusted.

**Live verification is mandatory.** A green suite has hidden real bugs that only appeared when a
write was executed against a router or a page was opened in a browser.

---

## Versioning rule

**Do not bump a version or write release notes during a working session.** A bump happens only when
asked to "package it up", and one bump covers the entire session. Versions are numbers, not decimals,
and Docker tags sort lexically: 0.8.10 follows 0.8.9, and the release after 0.8.51 is 0.8.52.

---

## Workflow rules

- Append to `Changes.md` after every file edit, not in a batch at the end.
- **Commit freely; never push or release without being asked for that push.** Committing is local
  and revisable. A push publishes, and a `v*.*.*` tag makes GitHub Actions build and publish an
  image people pull.
- **Approval does not carry forward.** One approval covers one push. A follow-up fix, however
  obviously related, is a new release and a new ask.
- **"Package it up"** means bump the version, write the release notes, check the README and commit.
  It does not mean push.
- Build explicitly when you need a binary.

---

## Behavioral guidelines

**Risk appetite: this app is in ACTIVE DEVELOPMENT, not maintenance.** Bias toward making the
change. The safety here is mechanical — `sh tools/verify.sh`, the ledgers that fail in both
directions, mutation testing and live verification — and hesitation is not a net: stopping to
reconsider has not prevented anything the gates did not already catch. A change that is wrong and
caught is cheaper than one that is never attempted.

**The end state is simple, efficient and uniform** — one mechanism per job; fewer router channels,
measured; every part answering the same questions the same way. Efficiency is the only one of the
three a number can settle, and the other two are first-class reasons to change a design, not taste.
**Check a step against that end state, not against the step before it.** When a plan's wording stops
matching the code, the goal moved or the plan was wrong; either way it is raised, not quietly
re-scoped.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them — don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

**Ask about the design, not about whether to proceed.** The shape of a mechanism, a contract or
payload change, anything with more than one reasonable end state, and any time a step's intent no
longer matches the code — re-scoping a step is a design question, not a local decision. Permission to
do work already agreed, or to move to the next step, is not; nor is a change being large, touching
many files, or removing something old — those are the job. **Always ask** before a push, a tag or a
release; before anything that writes to a router; and before deleting operator data.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Replace Rather Than Preserve

**Preserving is what needs the justification.**

- **Simplicity, uniformity and coherence are reasons to change a design, equal with efficiency.**
  Only a preference with no goal behind it — "I would have written it differently" — is taste.
- **Do not keep a mechanism with no instances** because a future caller might want it. Delete it;
  git history holds the reasoning.
- **Do not keep two forms of one thing** because unifying them is work. Migrate the callers and
  delete the old form in the same change.
- **Recorded behaviour is not a specification.** The corpora and recordings under `testdata/` pin
  what the app does, not what it must do. Change them deliberately, and say so.
- **Understanding a quirk before changing it is a step, not a veto.** Find out whether it is
  load-bearing, say which you concluded, and act on the answer.
- **Deliberate changes are fine. Silent ones are not.** A change that moves the rendered page, the
  payload contract or an interaction belongs in the commit message and in `Changes.md`, along with
  which gate you re-aimed, if any.
- Match this repo's Go style, even if you'd do it differently.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Fix the DNS collector" → "the Go payload matches the fixture replay, field for field"
- "Change what the card shows" → "the gate fails, I re-aimed it deliberately, and said why"
- "Speed up the router reads" → "concurrent reads never exceed the cap, measured"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant
clarification.

---

**These guidelines are working if** behaviour is deliberate rather than accidental, and gaps are
visible instead of silent.
