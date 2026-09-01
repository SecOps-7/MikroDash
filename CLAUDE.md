# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **This IS MikroDash.** It was a Node.js application until 2026-08-30, when this Go + TypeScript
> implementation replaced it, and the Node source was removed from the tree at the v0.8.0 cutover on
> 2026-08-31. (`CUTOVER.md` recorded that changeover and was deleted on 2026-08-31 with the other
> finished port documents; it is in git history.)
>
> **There is no reference repository any more.** Every gate compares against a RECORDING of the old
> implementation, committed under `testdata/`, `internal/*/testdata/` and the differential tests (now `web/test/`).
> `MIKRODASH_SRC` still exists and still defaults to `../MikroDash`, but nothing is there: the 105
> generator `--check` runs detect that and report a skip. The Node source remains in this repo's own
> history and at the `v0.7.40` tag if it is ever needed.

> **The rewrite's working documents were deleted on 2026-08-31** — the plan, the queue, the work
> list, the handover and the hardware results. They described a job that is finished. What the code
> still depends on was moved into the code and into this file before they went; everything else is
> in git history at `v0.8.2` and before.

---

## Code navigation and editing

Go files here are small and purposeful — read them whole. The frontend TypeScript under `web/src/`
is where symbolic navigation earns its keep.

| Task | Use | Not |
|---|---|---|
| Find a definition | `find_symbol` / `find_declaration` | `Grep` then `Read` |
| Find callers before changing a signature | `find_referencing_symbols` | `Grep` |
| Know what RouterOS commands exist | `docs/routeros-api-surface.md` (generated) | grepping by hand |
| Know what a RouterOS menu *can* hold | **rosetta** (MCP, `.mcp.json`), or `WebFetch` on `help.mikrotik.com` | inferring the property set from a fixture |
| Know what a collector returns | replay a fixture (`internal/collect` tests) | reading the collector and guessing |

- `Glob` and `Grep` are fine for **discovery**; follow up with a symbolic read rather than a whole-file one.

---

## Commands

No Go toolchain on the host — everything runs in a container.

```bash
# Build and vet
docker run --rm -v "$PWD":/src -w /src golang:1.25-alpine sh -c "go vet ./... && go build ./..."

# Go 1.25 is required: golang.org/x/crypto will not build on 1.23.

# THE PRODUCTION IMAGE. Multi-stage: frontend, binary, geoip tables, Alpine runtime.
# ~180 MB, /data its only mount. NEEDS NO OTHER IMAGE: the geo database is
# fetched fresh from DB-IP by the `geodata` stage. It used to be copied out of
# the Node `mikrodash` image, which was the last build-time tie to it, and the
# geoip tables are copied from it — see the Dockerfile's own note.
docker build -t mikrodash:latest .

# EVERYTHING THIS REPO CAN CHECK, discovered rather than listed.
#
# It runs the Go side (gofmt, vet, `go test ./...`, and `cmd/tsgen -check`), the
# TypeScript type checker, and the frontend's own tests. Nothing is named in the
# script: `go test ./...` finds anything new under `internal/verify/`, and
# `web/test/run.mjs` globs `*.test.ts`.
#
# THAT DISCOVERY IS THE POINT, and it is inherited. The old sweep globbed
# `tools/*-check.js` because `endpoint-audit` had been red for an unknown number
# of sessions while sweeps ran a list of names typed from memory.
sh tools/verify.sh
sh tools/verify.sh --no-docker   # skip the Go half, which needs a container

# Go unit tests + the differential gate (Go payload vs the Node golden) + the
# PROPLIST DRIFT gate.
#
# NO MOUNT IS NEEDED ANY MORE. The drift gate used to SKIP without the reference
# — "a gate that never runs", as this file said — and after cutover that skip
# would have been permanent. The live proplists are now recorded in
# `internal/collect/testdata/live-proplists.json` and the resources declarations
# in `internal/resource/testdata/live-resources.js`, so both gates run on any
# clone. MOUNT IT ANYWAY WHEN YOU HAVE IT: with the reference present each
# recording is re-derived and compared, which is the only thing that stops it
# going stale.
docker run --rm -v "$PWD":/src -w /src golang:1.25-alpine go test ./...

# Re-record deliberately, if a recording is ever regenerated from history:
#   -e MIKRODASH_PROPLIST_FREEZE=1   (internal/collect)
#   -e MIKRODASH_RESOURCES_FREEZE=1  (internal/resource)

# The frontend's own tests: 18 of them, bundling the app's TypeScript with
# esbuild and running it against a DOM shim. See web/test/README.md for why they
# are executed rather than type-checked.
cd web && npm test

# THE GO/NO-GO GATES. Both passed on 2026-08-30 and both are re-runnable.
#
# B1 — protocol conformance against live hardware, READ-ONLY. It needs no
# credential from you: `-data` decrypts the router's password out of the store
# exactly as the app does, so nothing is typed, printed or written down.
docker run --rm --network host -v "$PWD":/src -w /src -v /path/to/data:/data:ro \
  golang:1.25-alpine go run ./cmd/conformance -data /data -router "<label>"

# B2 — on-disk compatibility against a real /data, read-only.
docker run --rm -v mikrodash_data:/data:ro -v "$PWD":/src -w /src \
  golang:1.25-alpine go run ./cmd/compat -data /data

# Corpora. Each `tools/*-cases.js` RAN or LIFTED the Node implementation to build
# its corpus. That source is gone, so all 105 `--check` runs now SKIP and say so —
# the corpora are frozen artefacts. NEVER retype what one of these generated: a
# hand-copied table is a fork with no update path. To re-derive one, check out
# `v0.7.40` into a scratch tree and point `MIKRODASH_SRC` at it.
node tools/make-golden.js            # --check fails if stale
node tools/extract-ui.js             # lifts markup verbatim
node tools/api-surface.js            # regenerates the API surface

# Audits worth knowing by name.
node tools/page-gate-audit.js   # 85 of 85, 0 ungated (2026-08-30)
node tools/doc-claim-audit.js     # re-measures the numbers THIS FILE claims
node tools/identity-audit.js      # which identity goes in which column

# STEP ONE OF TWO, and the distinction matters. This LIFTS the reference
# renderer and writes `web/dist/_compare/live-<page>.js`. It COMPARES NOTHING,
# and exits 0 whether or not the port agrees with what it lifted.
node tools/live-renderer.js dns
# STEP TWO is a gate that CONSUMES the bundle and drives both from ONE payload,
# comparing innerHTML — the routers-grid check is the worked example.
```

---

## Architecture

```
RouterOS binary API (TCP/TLS)
        |
  internal/routeros/     an ADAPTER over github.com/go-routeros/routeros/v3, not a
                         protocol implementation. It owns the vocabulary the rest
                         of the app speaks — Cmd, Reply, Trap, Config.
  internal/collect/      the collectors, one per RouterOS subsystem
  internal/session/      one Session per watched router, owning the connection every
                         collector shares
  internal/routers/      the background pool for routers nobody is watching
  internal/alertpool/    the same, for routers with alerting enabled
  internal/alert/        the alert rules — pure: rows in, verdict out
  internal/guard/        the write guards — also pure. See "Write guards" below
  internal/store/        /data as it is written: AES-256-GCM settings, scrypt users,
                         routers.json
  internal/db/           the SQLite history and audit database (modernc.org/sqlite,
                         pure Go, which is what makes the binary static)
  internal/server/       HTTP routes and the WebSocket protocol
        |
  web/src/               the TypeScript frontend
```

**Two things about `internal/routeros` are load-bearing and must not be undone casually:**

1. **ASYNC MODE IS MANDATORY.** `Dial` calls `Async()`. That is what gives the client a tag map, and
   therefore somewhere to discard a sentence addressed to a cancelled tag. Sync mode keeps no tag map
   and nothing catches it, and the failure takes down a connection every collector shares.
2. **The hardware claims are version-qualified.** They were measured on RouterOS **7.24**, against
   the three routers this project targets; `internal/routeros/client.go` carries them in full.
   "Not reproduced" is not "never true".

**What is knowingly accepted:** go-routeros returns on the first `!done` and this side cannot see
block boundaries. `cmd/conformance` therefore tests COMPLETENESS instead — the bulk registration-table
read against the sum of per-interface reads — which is the property block structure was only ever a
proxy for.

**`internal/store`** must read what the Node app wrote, or the user is locked out. Three traps, all
documented in the package header: the scrypt salt is a **string** not decoded bytes; the envelope is
`iv‖tag‖ciphertext` while Go's `Open` wants `ciphertext‖tag`; and `users.json` must stay a bare JSON
array, which is a security property rather than a preference.

**`testdata/fixtures/`** is what stops this code re-deriving RouterOS behaviour: real captures from
live hardware, replayed into the Go collectors by `internal/collect`.

---

## Hard constraints

- **The app may now change, and the gates tell you when it does.** "Nothing user-visible may
  change" was the PORT's acceptance criterion, and it was retired at cutover: this is the product
  now, not a reproduction of one, and it has to evolve.

  The gates that enforced that rule are gone (2026-09-01) along with the recordings they compared
  against. What replaced them checks this app against ITSELF — see "Verification" below — so a
  failing test now means "you broke an invariant", not "you changed the rendering".

  **Never delete a check to make a change quiet.** A check removed without a reason reads exactly
  like one that never existed, and that is how this repository has lost coverage before.

- **"More efficient" means fewer router channels, not faster payload assembly.** The documented
  bottleneck is concurrent API channels on the MikroTik, not CPU.

- **Go stdlib first, but not stdlib-only.** A dependency needs a reason better than convenience.
  Seven are in: `golang.org/x/crypto` (scrypt, because the user store's key derivation demands
  it), `modernc.org/sqlite` (pure Go, no cgo, so the binary stays static),
  `github.com/coder/websocket`, `github.com/go-routeros/routeros/v3`, `github.com/go-pdf/fpdf`,
  `github.com/oschwald/maxminddb-golang` (the DB-IP reader that replaced geoip-lite's `.dat`
  files) and `github.com/evanw/esbuild`.

  **esbuild's reason is that it REMOVES a runtime rather than adding one.** The frontend build
  was 142 lines of Node driving the esbuild binary — and esbuild is a Go program, so Node was
  orchestrating Go. `cmd/webbuild` does the same work through esbuild's Go API and its output
  is BYTE-IDENTICAL, verified file by file before the Node stage was deleted. The image no
  longer pulls `node:22-alpine`. What Node is still needed for is `tsc --noEmit`, which emits
  nothing, and the tests in `web/test/` that bundle frontend TypeScript — testing TypeScript
  needs a JavaScript runtime, and that is not a dependency this repo can decide away.

  fpdf earned its place on a property **measured before it was chosen**: its Helvetica metrics ARE
  pdfkit's, and they agree to 2e-13 pt across 792 measurements. Two things it does NOT do, both found by
  running it and both pinned: it does not kern where pdfkit does (worst real-report error 0.65 pt,
  because `_render` centres rather than right-aligns), and it walks BYTES against a cp1252 table, so
  `reportpdf.EncodeText` is mandatory on every draw and every measurement.
  `internal/reportpdf/metrics_test.go` asserts the kerning gap **still exists** — an fpdf that learned
  to kern fails the suite and forces the note to be deleted rather than left lying.

- **No credential is ever written to a fixture, and nothing identifying either.** The exposure vector
  is FILE CONTENT: this repo is public, so anything in a committed file is public. `assertClean()` and
  the anonymisation in the capture-fixtures tool enforce exactly that. See "Fixtures" below.

- **`MIKRODASH_SRC`** was how every tool found the Node source. It is vestigial: the source is gone,
  the variable still defaults to `../MikroDash`, and the tools detect its absence and skip. Do not
  hard-code a path to it, and do not delete the variable — the skip logic keys on it.

---

## Fixtures

Captures are anonymised at source by the capture-fixtures tool. Every rule below was learned by
watching a real capture leak:

- **Preserved** (structural): interface and profile names, bridge names, VLAN ids, RouterOS ids, and
  the joins between them. `name` is deliberately NOT anonymised — `wifi.js` reads the band out of an
  interface called "2.4GHz WiFi", so tokenising it produced a fixture on which a real code path could
  not run.
- **Scrubbed**: SSIDs, MACs (into `02:` locally-administered), IPs (into `198.51.100.0/24`,
  TEST-NET-2), serials, router identity, country, comments, DHCP hostnames.
- **Dropped entirely**: any key ending in a credential — `passphrase`, `password`, `private-key`,
  `pre-shared-key`. A fixture has no use for a WireGuard private key.
- **Free text is handled by learning, not by key names.** A log line reads
  `…@5GHz WiFi3(Cyberdyne Systems) connected`; no rule about keys catches that. The tool reads this
  router's SSIDs, identity and DHCP hostnames FIRST and replaces them as substrings, so the message
  keeps its shape and only the identifying part moves.

`assertClean()` is a **positive** structural check — every value under an identifying key must be a
token the tool minted — because a denylist would not have caught the first leak, and did not.

---

## Versioning rule

**Do not bump a version or write release notes during a working session.** This project has no
released four: 0.8.0 (tagged, never published — its build failed on 32-bit ARM), 0.8.1 (the first
published Go image), 0.8.2 and 0.8.10. **0.8.10 follows 0.8.2**: these are numbers, not decimals. A bump happens only when the user says "package it up", and one bump
covers the entire session.

---

## Write guards

`src/routeros/*Guard*.js` is ~960 lines across **six** modules (measured 2026-08-25; this said
"~1,085 lines across eight", which also contradicted "ALL SIX ARE NOW PORTED" below it), and
**every one of them is pure** —
rows in, verdict out, no router I/O. The live repo did that deliberately: "the decision lives in one
pure module rather than inline in six handlers. Every refusal is one function, testable without a
router." That makes them the cheapest and lowest-risk code in the port, and fully unit-testable.

**They were ported just-in-time, with the page that needed them** — but "ported" and "in
`portedGuards`" are two different questions, and conflating them is how a session re-ports code that
already exists.

- **In `portedGuards`**, i.e. runnable from the generic RESOURCE write path: `selfPath`
  (+ `selfAddress`), `fwGuard`, `wifiInherit`, `capsmanPush`. Those are also exactly the four that
  any resource declares, so no declared guard is currently refused.
- **Ported as code but NOT in that map**, because they are called directly by a page handler rather
  than through a resource: `queueGuard` (`internal/guard/queueguard.go`, 308 lines, 5 tests, used by
  `internal/server/queues.go`), `selfGuard` and `wifiGuard`. Do not re-port these.
- **ALL SIX ARE NOW PORTED.** `wanGuard` was the last one and landed on 2026-08-24
  (`internal/guard/wanguard.go`, pinned by the wanguard corpus → `wanguard_test.go`, 91
  verdicts, eight mutations killed). It is NOT in `portedGuards` and does not belong there: no
  resource declares it, and like `queueGuard` it is called directly by a page handler. ~~**Wiring it
  into the WAN page's renew/release is a separate step and has not been done**~~ **IT IS DONE —
  measured 2026-08-30.** `internal/server/wan.go:114` calls `guard.ResolveWanPath`, and
  `web/src/pages/wan.ts` draws Renew and Release behind a confirmation with a status line. The
  "read-only" string that made this claim look true is the RBAC notice shown to a principal WITHOUT
  write access, not a statement about the page. Read the call site, not the string.

  Note the filename trap that made an earlier version of this list wrong:
  `internal/guard/capsmanguard.go` is the port of `capsmanGuard.js` AND is what provides
  `capsmanPush` — so a missing `capsmanpush.go` does not mean capsmanPush is missing.

  One name had to change: `wanGuard.resolveManagementPath` returns something the live side also
  calls a management path, but it is a different question from `selfPath`'s — "local or remote"
  against "which interfaces are we behind" — and one Go package cannot hold both. It is `WanPath`
  here, `ResolveWanPath` to build it.

The six guards do not map one-to-one onto file names — `capsmanguard.go` is what provides
`capsmanPush`, so a missing `capsmanpush.go` does not mean it is absent. Read `internal/guard/`
before concluding one is missing.

`portedGuards` in `internal/server/resource.go` is the authority; this line is a summary of it and
has gone stale before. Check the map, not the prose.

**A resource declaring an unported guard has its writes REFUSED**, not logged-and-allowed
(`portedGuards` in `internal/server/resource.go`, pinned by `internal/server/guard_test.go`). That
default matters precisely because just-in-time means "declared but not ported" is a routine state:
proceeding would silently skip a check the live app makes, and the whole point of a guard is that
its absence is not supposed to be survivable. Porting one means adding it to `portedGuards`, which
fails the test until done deliberately.

Note which resources need **no** guard, so their write paths are unblocked already: `route`,
`route6`, `dnsStatic`, `dhcpLease`, `veth`, `wgPeer`, `wlSecProfile`, `capsProvisioning`.

**THE CUTOVER BLOCKERS ARE ALL CLOSED.** This section used to carry a numbered list of them, and
several stayed on it after they were closed — long enough that a session summarised one back to the
operator as remaining work. They are gone rather than struck through here, because the list is no
longer about anything, and the port record keeps the reasoning. **62 comments in shipped code cite that queue by name**, which is why it
survived the 2026-08-31 deletion of the other port documents — and why a comment saying "blocker 5 is
the reason" describes a blocker that is CLOSED. Read the queue for why, never for what is true now.

The one durable lesson from that list is worth keeping, because it is this file's most expensive
recurring defect: **a blocker that has been closed reads exactly like one that never was, and nothing
fails when a premise expires.** That is why the doc-claim audit exists — it re-measures the
numbers this file claims, and it caught three that were wrong on the day it was written.

Both go/no-go gates passed on 2026-08-30 and both are re-runnable — see Commands. `cmd/conformance`
needs no credential from you: `-data` decrypts the router's password out of the store exactly as the
app does. The claim that it "still needs the operator" survived in this file long after the tool's own
header documented otherwise.

## Verifying against MikroTik's documentation

Every menu path, property name and enumerated value gets checked against the official docs before it
is ported. **rosetta** is configured for this repo in `.mcp.json` (`bunx @tikoci/rosetta`); MCP
servers connect at session start, so a session older than that config must be restarted before the
tools appear. The fallback is `WebFetch` against `help.mikrotik.com`, which is what
`.claude/skills/mikrotik-docs.md` describes.

**A fixture and the documentation answer different questions, and the port needs both.** A fixture
proves what the code does with the rows one router actually returned; the docs say which rows a
router *may* return. The gap between them is where the defects live: `dnsStatic` offered six of the
nine record types RouterOS supports, and because a `select` validates against its options, a router
holding an MX record opened a form showing "A" — saving rewrote the record. No fixture could catch
that, because the captured router had no MX record.

Enumerated values deserve the closest reading for exactly that reason.

## Page keys — one word, five meanings

`internal/pages` is the list. A key is not just a label; the same string is used
as **five** different things, and they do not all move together:

| as | where | renaming it |
|---|---|---|
| URL path | `internal/server` registers one route per page | changes a public link |
| markup id | `#page-<key>` in `web/src/ui/page-<key>.html` | must move with the file |
| room name | collectors emit to `page-<key>` | a PROTOCOL change, both sides at once |
| **permission key** | `rbac.PageKeys`, and `role_pages.page` in the operator's database | **an unknown key is DENIED before any role is consulted** |
| pagechange detail | `detail === '<key>'` in ~20 page modules | a missed one stops that page loading |
| **visibility guard** | `isVisible('<key>')` / `pageVisible('<key>')` | **a stale one is permanently false: the page renders once and never updates** |

The fourth is the dangerous one. On 2026-09-01 six keys were renamed and
`rbac.PageKeys` was missed: four pages showed nothing at all, for everyone
including administrators, with a green build and no error anywhere. `PageKeys`
reads `internal/pages` now, so it cannot drift again.

**The fourth has a SECOND failure the first fix did not touch, and it is quieter.**
`rbac.PageKeys` is source; `role_pages.page` is DATA, in the operator's database.
A renamed key leaves every stored grant naming the old one pointing at nothing,
and an orphaned grant is not an error anywhere — the role just stops conferring
that page. Administrators are structural and never read the table, so whoever is
most likely to be testing cannot see the loss. Later that same day this install
was found still holding `topology` and `wireless`, so the readonly and operator
roles had silently lost two pages, plus `routers`, dead since the Node cutover
and unnoticed for a day.

**`pages.Renamed` is the fix and it is APPEND-ONLY** — a promise to installed
databases, not a note about this source. `(*db.DB).RenamePageGrants` applies it
once at startup from `cmd/mikrodash`, deliberately NOT from `db.Open`, because
`cmd/compat` opens a real `/data` through a read-only mount. Rename a key and add
the entry in the same commit; `internal/pages`' own test fails a value that is not
a current key, a key that still is one, and a chain left uncollapsed.

**The sixth is the quietest, and it is not a fifth-and-a-half.** A visibility
guard asks "is this page the one on screen?" by comparing a literal against
`currentPage`. Rename the key anywhere else and the comparison simply stops
matching: the socket still delivers, the collector still emits, the room is still
joined, and the handler declines to render. On 2026-09-01 that left the Users
page sitting on "Waiting for user data…" and stopped Network Topology scheduling
live frames. Grepping the pagechange spelling does not find these, which is how
they survived a rename that was otherwise thorough.
`TestVisibilityGuardsNameRealPages` now fails on one.

**And the frozen tables cannot be reached by a rename at all.** `web/src/gen/`
is generated from JSON in `testdata/` whose own generators read the Node app and
were deleted, so `PAGE_KEYS` (the digit shortcuts) and `VIEW_PRESETS` went on
naming pages that no longer existed. **Edit the JSON, not the `.ts`** — a
hand-edited `.ts` is ahead of its source and the next regeneration reverts it,
which is the state this repository was actually in.
`TestFrozenPageTablesNameRealPages` checks the JSON for that reason.

**What is NOT a page key, though it looks identical:** `Layout(user,
"dashboard")` and `Layout(user, "topology")` in `internal/server/layouts_api.go`
are ROW KEYS in `user_layouts`, holding every layout an operator has saved.
Renaming those orphans their data silently. The permission check beside them is a
page key and does move. Read which question is being asked before touching either.

**The dashboard is served at `/home`** — the one page whose URL differs from its
key, declared as `Path` on that entry. Everything else is served at its key.

## Verification — where the checks live now

The port-parity harness was retired on 2026-09-01. It was 136 gates, 35 audits and
96 corpus generators under `tools/`, and nearly all of it asked one question: does
this app still reproduce a frozen recording of the Node implementation it
replaced? That question died with the port, and 25 MB of recordings went with it.

**The checks that asked a different question moved rather than died:**

| | |
|---|---|
| `internal/verify/` | 26 Go tests. Static checks over the CURRENT source: credentials, cited paths, the WebSocket vocabulary both ways, endpoints, selectors, module reachability, identity columns, the blur-suspend guard, fixture schemas, and that every page-key literal still names a real page. |
| `web/test/` | 15 test files that bundle the app's TypeScript and run it against a DOM shim (25 cases). |

**Two rules carried across, and both are load-bearing:**

1. **A ledger fails in BOTH directions.** An unrecorded gap is a failure, and a
   recorded gap that has CLOSED is also a failure. Without the second half a
   ledger becomes folklore — a list of excuses nobody re-measures.
2. **A check must not read itself.** `internal/verify/` ledgers quote the very
   event names, settings keys and paths they look for, so a scan that included
   them would prove anything it names is present. `isTestSource` exists for that,
   after the trap was hit three times in one migration — once by a gate reading a
   ledger written minutes earlier.

**What was knowingly given up:** nothing now compares this app's rendering against
anything external. The surviving tests assert its behaviour against itself, which
is real but a weaker claim than "matches what shipped". That is the deliberate
trade — the app has to be free to change — and it is written here rather than left
to be discovered as a silent gap.

`docs/unwired-elements.md` preserves the one piece of knowledge that had no
mechanism to move to: the annotated record of which element ids are deliberately
inert.

## Testing

- **Go**: `go test ./...` — standard library `testing` only, no frameworks.
- **The two gates are not unit tests.** `cmd/conformance` and `cmd/compat` run against live hardware
  and the live `/data`. They are the go/no-go checks, and a green unit suite does not substitute for
  them.
- **`internal/verify/`** holds the repository's static self-checks as Go tests — 26 of them. They
  read the CURRENT source and assert properties still worth holding: no committed credential, every
  cited path present, every emitted event consumed, every multi-room collector behind an occupancy
  guard. They are test-only, so nothing can link them into the binary.
- **`web/test/`** holds 18 frontend tests that bundle the app's TypeScript and run it against a DOM
  shim. JavaScript-hosted because testing TypeScript needs a JavaScript runtime.
- **A gap is documented, never hidden.** Every ledger in those tests fails in BOTH directions: an
  unrecorded gap is a failure, and a recorded gap that has CLOSED is also a failure, so a note cannot
  outlive the situation it describes.
- **Live verification is mandatory.** A green suite hid four real bugs in the last feature shipped on
  the Node side, two of which only appeared when a write was actually executed against a router.

---

## Agent tooling

Set up to mirror the live repo's, adapted for this tree. Committed rather than ignored, because the
hooks and the memories are project knowledge — only the personal half (`settings.local.json`,
`project.local.yml`, the Serena cache) is ignored.

| Piece | What it does |
|---|---|
| `.serena/memories/` | The project graph, rooted at `core`. Read `core` first; it links the rest. |
| `.claude/hooks/check-after-edit.sh` | Per-edit check: `gofmt -e` for Go, `tsc --noEmit` for TS, `node --check` for JS, `jq` for JSON. Reports, never blocks. |
| `.claude/hooks/gateguard-serena.js` | Bridges Serena's MCP editors into ECC's fact-forcing gate, which only matched `Edit|Write`. |
| `.claude/hooks/changes-reminder.sh` | Stop hook: nudges when a source file is newer than `Changes.md`. Build output is excluded so it stays signal. |
| `.claude/skills/mikrotik-docs/SKILL.md` | Look up RouterOS documentation before guessing a command or path. **THE PATH IS THE POINT: it was a loose `mikrotik-docs.md` for its first six days and therefore never loaded** — skills are discovered as `<name>/SKILL.md`, and a loose file is skipped silently. No error, no warning; the only tell is absence from the available-skills list. Fixed 2026-08-27. Its content was stale too, predating rosetta and telling an agent to `WebFetch` an index. |
| the identity audit | **WHICH IDENTITY** this port writes into each column of the shared database. There is no blanket rule — `grants.principal_id`, `audit_events.actor_id` and `user_layouts.user_id` hold the user ID, while `alert_events.acknowledged_by` and `audit_events.actor_name` hold the USERNAME. Two bugs on 2026-08-27 were a writer reaching for the other one, and neither was visible to any test: a round trip through one implementation agrees with itself whatever it wrote. Both were found by reading the real table. |

**Serena's language server here is `typescript`, not `go`, and that is not a mistake.** Its servers
run on the host, this host has no Go toolchain, and declaring `go` fails the whole language-server
manager — Serena's symbolic tools then stop working for *every* file, including the TypeScript ones.
The typescript server indexes `.ts` and `.js`, which covers `web/` and `tools/`. Go
files are read whole, as the navigation table above already says. Changing `.serena/project.yml`
takes effect at the next session start.

**There is deliberately no rebuild-on-stop hook.** There used to be one, inherited from the Node
era, which rebuilt the Node container after every turn. It was retired at the v0.8.0 cutover: a Go
image build fetches a fresh geo database and takes minutes, so rebuilding once per turn is the wrong
trade. Build explicitly when you need a binary.

---

## Workflow rules

- Append to `Changes.md` after every file edit (not in a batch at the end).
- Always confirm before `git push` or Docker push.
  Build here explicitly when you need a binary.
- `tools/api-surface.js --check` regenerated `docs/routeros-api-surface.md` from the Node source and
  now skips with the other 104 generators. The committed surface is frozen; extend it by hand from
  the RouterOS documentation (see the mikrotik-docs skill) rather than expecting the tool to.

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

- **A quirk is not automatically worth keeping, but it is worth understanding first.** Plenty of
  this app's behaviour was inherited verbatim because reproducing it was the job. Some of that is
  deliberate and load-bearing; some is an accident nobody has questioned since. Find out which
  before changing it, and say which one you concluded.
- **Deliberate changes are fine. Silent ones are not.** If a change moves the rendered page, the
  payload contract or an interaction, that belongs in the commit message and in `Changes.md` --
  along with which gate you re-aimed, if any.
- **Efficiency remains a reason to depart from the old design; taste alone still is not.** A more
  direct data path, fewer router channels, a simpler internal shape: all welcome. "I would have
  written it differently" is not.
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

**These guidelines are working if:** behaviour is reproduced rather than reinvented, and gaps are
visible instead of silent.
