# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **This IS MikroDash.** It was a Node.js application until 2026-08-30, when this Go + TypeScript
> implementation replaced it, and the Node source was removed from the tree at the v0.8.0 cutover on
> 2026-08-31. (`CUTOVER.md` recorded that changeover and was deleted on 2026-08-31 with the other
> finished port documents; it is in git history.)
>
> **There is no reference repository any more.** Every gate compares against a RECORDING of the old
> implementation, committed under `testdata/`, `internal/*/testdata/` and `nodecheck/testdata/`.
> `MIKRODASH_SRC` still exists and still defaults to `../MikroDash`, but nothing is there: the 105
> generator `--check` runs detect that and report a skip. The Node source remains in this repo's own
> history and at the `v0.7.40` tag if it is ever needed.

> **Two documents from the rewrite survive in `docs/port-history/`** — `gate-conversion.md` (the
> rules for re-aiming a gate, which `tools/gate-conversion/*.py` implement) and `test-results.md`
> (the RouterOS 7.24 hardware results). Both carry knowledge the code still depends on. The plan,
> the work list and the handover were deleted on 2026-08-31: they described a job that is finished,
> and they are in git history if ever needed.

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
| Know what a collector returns | replay a fixture (`nodecheck/`) | reading the collector and guessing |

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
docker build -t mikrodash-go:latest .

# EVERYTHING THIS REPO CAN CHECK, discovered rather than listed. Run this before
# claiming a session is green: it globs the gates, the audits, the `--check`
# generators and nodecheck, so a category cannot be forgotten the way
# `endpoint-audit` was (red for an unknown number of sessions, because every
# sweep ran a list of audit names typed from memory).
#
# It reports a SKIP as a skip. **12** generators need `better-sqlite3` or
# `pdfkit` and run in a throwaway container from the `mikrodash` IMAGE — it used
# to need that container RUNNING, which turned twelve gates into a permanent skip
# the moment cutover stopped it. ASK THE SWEEP: it prints
# "container generators: N checked".
#
# It also runs the Go side (gofmt, vet, test) and tsc. That was once excluded on
# the grounds that "Go runs in a container and CLAUDE.md documents it
# separately", which is precisely the leave-it-to-be-remembered arrangement that
# let `endpoint-audit` go red. A whole session ran this sweep, called it green,
# and never compiled the Go.
#
# It ALSO ratchets a per-gate census (`testdata/gate-census.txt`) and fails when
# a gate shrinks. "136 run, 0 failed" cannot otherwise tell a gate that compared
# forty cases from one that compared none.
sh tools/verify.sh
sh tools/verify.sh --no-docker   # skip docker-dependent generators

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

# Node-side differential tests. THREE FILES REMAIN and none needs the reference:
# `topojson-decode`, `reports-presets` and `conn-tables` all record their live
# half. Two others were RETIRED on 2026-08-31 — they replayed fixtures into the
# live collectors and had no port side, so freezing them would have produced a
# recording compared with itself. That cost 122 assertions; their result is the
# committed fixtures, which `internal/collect` compares against on every run.
# The GLOB, not the directory: `node --test nodecheck/` tries to require the
# directory itself and dies with MODULE_NOT_FOUND before running anything.
node --test nodecheck/*.test.js

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
# comparing innerHTML — `tools/routers-grid-check.js` is the worked example.
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
2. **The hardware claims are version-qualified.** `docs/port-history/test-results.md` recorded them on
   RouterOS 7.24. "Not reproduced" is not "never true".

**What is knowingly accepted:** go-routeros returns on the first `!done` and this side cannot see
block boundaries. `cmd/conformance` therefore tests COMPLETENESS instead — the bulk registration-table
read against the sum of per-interface reads — which is the property block structure was only ever a
proxy for.

**`internal/store`** must read what the Node app wrote, or the user is locked out. Three traps, all
documented in the package header: the scrypt salt is a **string** not decoded bytes; the envelope is
`iv‖tag‖ciphertext` while Go's `Open` wants `ciphertext‖tag`; and `users.json` must stay a bare JSON
array, which is a security property rather than a preference.

**`testdata/fixtures/`** is what stops this code re-deriving RouterOS behaviour: real captures from
live hardware, replayed into the Go collectors by `internal/collect`. `nodecheck/` replayed them
into the Node collectors too until cutover; its three surviving tests record their own live half.

---

## Hard constraints

- **The app may now change, and the gates tell you when it does.** "Nothing user-visible may
  change" was the PORT's acceptance criterion, and it was retired at cutover: this is the product
  now, not a reproduction of one, and it has to evolve.

  What the 136 gates mean changed with it. They no longer prove parity with an implementation that
  exists; they compare against a frozen RECORDING of one that does not. So a failing gate is not
  "you broke it" any more, it is **"you changed the rendering — did you mean to?"** That is still
  worth having, because the expensive defects in this repo have always been the silent ones.

  The cost moved rather than vanished. **The recordings cannot be regenerated**, so a deliberate
  change means re-aiming or retiring the gate that guarded the old behaviour, and saying so in the
  commit. Never delete a gate to make a change quiet: a gate removed without a reason reads exactly
  like one that never existed.

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
  nothing, and the 132 gates that bundle frontend TypeScript — testing TypeScript needs a
  JavaScript runtime, and that is not a dependency this repo can decide away.

  fpdf earned its place on a property **measured before it was chosen**: its Helvetica metrics ARE
  pdfkit's, and they agree to 2e-13 pt across 792 measurements. Two things it does NOT do, both found by
  running it and both pinned: it does not kern where pdfkit does (worst real-report error 0.65 pt,
  because `_render` centres rather than right-aligns), and it walks BYTES against a cp1252 table, so
  `reportpdf.EncodeText` is mandatory on every draw and every measurement.
  `internal/reportpdf/metrics_test.go` asserts the kerning gap **still exists** — an fpdf that learned
  to kern fails the suite and forces the note to be deleted rather than left lying.

- **No credential is ever written to a fixture, and nothing identifying either.** The exposure vector
  is FILE CONTENT: this repo is public, so anything in a committed file is public. `assertClean()` and
  the anonymisation in `tools/capture-fixtures.js` enforce exactly that. See "Fixtures" below.

- **`MIKRODASH_SRC`** was how every tool found the Node source. It is vestigial: the source is gone,
  the variable still defaults to `../MikroDash`, and the tools detect its absence and skip. Do not
  hard-code a path to it, and do not delete the variable — the skip logic keys on it.

---

## Fixtures

Captures are anonymised at source by `tools/capture-fixtures.js`. Every rule below was learned by
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
released three: 0.8.0 (tagged, never published — its build failed on 32-bit ARM), 0.8.1 (the first
published Go image) and 0.8.2. A bump happens only when the user says "package it up", and one bump
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
  (`internal/guard/wanguard.go`, pinned by `tools/wanguard-cases.js` → `wanguard_test.go`, 91
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
longer about anything, and `docs/port-history/PORT-QUEUE.md` keeps the reasoning. **62 comments in shipped code cite that queue by name**, which is why it
survived the 2026-08-31 deletion of the other port documents — and why a comment saying "blocker 5 is
the reason" describes a blocker that is CLOSED. Read the queue for why, never for what is true now.

The one durable lesson from that list is worth keeping, because it is this file's most expensive
recurring defect: **a blocker that has been closed reads exactly like one that never was, and nothing
fails when a premise expires.** That is why `tools/doc-claim-audit.js` exists — it re-measures the
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

## Gate conversion — the rules, and the three tools

Every gate and audit compares against a RECORDING of the Node implementation
rather than reading its source. The recordings live in `testdata/golden-gates/`
(JS), `internal/*/testdata/` (Go) and `nodecheck/testdata/`.

While the source existed, each recording was re-derived and compared on every
run, which is what kept it honest. That ended at the v0.8.0 cutover: the last
such comparison ran immediately before deletion and was green. The recordings are
now frozen artefacts, and the census in `tools/verify.sh` — which fails when a
gate checks LESS than it used to — is what guards them from here.

**When a gate touches the reference, what it touches is one of four things, and
the wrong treatment is silent in every direction:**

| What it is | Treatment | If treated wrongly |
|---|---|---|
| A question about the SOURCE TEXT — "is the handler still at this anchor" | **GUARD** with `LIFT.hasReference(ROOT)` | The gate dies at module scope |
| A VALUE the comparison consumes — `MAX_ALERTS`, a case table, a lifted `esc()` | **FREEZE** with `G.value`, plus a sanity assertion on the recording | Guarding leaves it undefined and every comparison passes vacuously |
| A BEHAVIOUR assertion — "a tooltip is built", "the centre draws the total" | **RE-AIM at the port** | Guarding silently deletes a real check |
| A DECLARED DIFFERENCE — "the live app sends, the port returns early" | **FREEZE the live half**, keep every assertion | Guarding deletes the only proof the difference still holds |

Where a gate EXECUTES lifted text (`vm.runInContext`, `new Function`), freeze the
SOURCE and not the outputs: the recording is a fraction of the size and the live
half still runs, so a case added later still gets a live answer. Freeze the
JOINED program rather than each lift — that covers every lift inside it whatever
shape each takes.

**The three tools, and what each sees that the others cannot:**

| Tool | Asks |
|---|---|
| `tools/canfail-audit.js` | would this gate tell us if it failed **at all**? |
| the gate census (`tools/verify.sh`) | did this gate check **less than it used to**? |
| `tools/vacuity-audit.js` | does it check the same **with and without** the reference? |
| `tools/gate-conversion/accept.py` | does it still catch a **port defect** without one? |

Run `vacuity-audit` after converting anything. This session found three gates
going quietly vacuous — all three were PASSING, and each was caught by a
different one of those four.

**A mutation is evidence only if it applies, compiles, lands in the module the
gate BUNDLES, and does the forbidden thing.** Six ways one can prove nothing were
seen here; `accept.py` catches four mechanically. **Exit 0 has been wrong three
separate ways** — a crash exiting non-zero read as a detection, a broken gate
making every mutation look caught, and a swallowed rejection reading as a pass.
Judge a gate by what it SAYS it checked.

`docs/port-history/gate-conversion.md` is the full record, including the failures
these rules came from.

## Testing

- **Go**: `go test ./...` — standard library `testing` only, no frameworks.
- **The two gates are not unit tests.** `cmd/conformance` and `cmd/compat` run against live hardware
  and the live `/data`. They are the go/no-go checks, and a green unit suite does not substitute for
  them.
- **`nodecheck/` runs under `node --test`** and replays fixtures into the OLD collectors. It is how a
  fixture is proved faithful before the Go side is asked to reproduce it.
- **A gap is documented, never hidden.** `nodecheck` carries a `KNOWN_INCOMPLETE` list; the test
  asserts the gap *still exists*, so closing it fails the suite and forces the note to be removed
  rather than left lying.
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
| `tools/identity-audit.js` | **WHICH IDENTITY** this port writes into each column of the shared database. There is no blanket rule — `grants.principal_id`, `audit_events.actor_id` and `user_layouts.user_id` hold the user ID, while `alert_events.acknowledged_by` and `audit_events.actor_name` hold the USERNAME. Two bugs on 2026-08-27 were a writer reaching for the other one, and neither was visible to any test: a round trip through one implementation agrees with itself whatever it wrote. Both were found by reading the real table. |

**Serena's language server here is `typescript`, not `go`, and that is not a mistake.** Its servers
run on the host, this host has no Go toolchain, and declaring `go` fails the whole language-server
manager — Serena's symbolic tools then stop working for *every* file, including the TypeScript ones.
The typescript server indexes `.ts` and `.js`, which covers `web/`, `tools/` and `nodecheck/`. Go
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
