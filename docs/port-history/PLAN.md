# Porting MikroDash to Go + TypeScript

## Context

MikroDash works — 0.7.31, 1,451 tests, 30 collectors, live household infrastructure. The motives for
moving are **maintainability** (a 7,200-line `src/index.js` and a 16,086-line `public/app.js` that
`CLAUDE.md` forbids agents from reading in full), **type safety** (RouterOS rows are untyped bags of
strings — `'false'`, comma lists, dotted keys), and **footprint** (a ~150 MB Node image against a
~20 MB static Go binary).

Decision: **a Go server and a TypeScript frontend**, built as a separate project in
`/DATA/Backup/Projects/MikroDash_New` using a **strangler-fig** migration, and preceded by a
**structural phase in the current repo** that stages it. The structural phase is not a decision point
— it is the precondition. You cannot strangler-fig a 7,200-line file; you can strangler-fig ten
modules.

### What an evidence pass changed

Before committing, an inventory of the codebase moved two beliefs:

- **The client/server duplication is not a driver.** 13 mirrors pinned by 13 tests — **0.9% of the
  suite**, roughly 350 lines. I had claimed a rewrite would delete a whole class of tests; it would
  not. Worse for that argument, the codebase has already killed two whole classes of mirror *without
  a build step*, by serving registries over the wire (`GET /api/collectors`, `src/index.js:2247`;
  `res:schema`, `src/routeros/resources.js:36`). That pattern, not a compiler, is the fix for the
  remaining 13.
- **The real asset is the RouterOS knowledge, and it is much bigger than the code.** ~50 documented
  behaviour workarounds across `src/`, **28 of them explicitly verified against specific live
  hardware**, plus 1,167 LOC of write-safety guards. Of the six patches in `patch-routeros.js`,
  **four are protocol realities** any fresh client in any language must independently solve — and at
  least one (wifi-qcom devices sending `/interface/wifi/registration-table/print` as a separate
  `!done`-terminated block *per interface*) is close to undiscoverable without the hardware.
  **Retested on 7.24 and NOT reproduced — see B1.** The wider point survives and is worth keeping:
  the knowledge was real, it was only discoverable on hardware, and the way to settle it was to go
  back to the hardware rather than to reason about it. Doing so cost one afternoon and removed
  649 lines of hand-written protocol.

Two consequences shape this whole plan:

1. **Extract the knowledge before porting, as executable fixtures.** Not documentation — tests the Go
   implementation must pass. This is the single highest-value activity in the project.
2. **Performance is not a reason to do this.** The bottleneck is documented as the router, not the
   process: *"the evidence in #104 points at concurrent open channels rather than data volume"*
   (`src/collection.js:6`). Every perf mechanism in the codebase exists to reduce load on the
   MikroTik. Go changes none of that. Footprint is a real win; throughput is not.

---

### The hard constraint: nothing user-visible changes

**The app must look and behave exactly as it does today.** Backend mechanics are free to change where
that buys efficiency; the interface is not. This is a port, not a redesign, and it has three concrete
consequences that shape the work more than the language choice does:

1. **The TypeScript frontend keeps the existing stylesheet and DOM contract.** `public/index.html`
   carries ~6,688 lines including the whole embedded stylesheet — every class (`.stab`, `.wl-band`,
   `.fa-stat-val`, `.card-badge`, the theme and palette token sets). That CSS is **reused verbatim**,
   along with the class names, the element ids and the DOM shape. Only the logic that produces the
   DOM is rewritten. This turns the frontend port from "redesign" into "retype the renderers", and it
   is the difference between a plausible port and an open-ended one.
2. **A visual and behavioural baseline is captured in Phase A, before anything moves.** Reference
   screenshots of every page across both themes and every palette, plus DOM snapshots and the
   interaction paths (tab switches, sorts, modals, row expansion). The current app is the
   specification; that specification has to be written down while it is still the only
   implementation.
3. **"More efficient" should target the router, not the process.** The documented bottleneck is
   concurrent API channels on the MikroTik, not CPU in Node. Efficiency work that reduces channel
   count, batches reads or improves the dormancy arbiter is real; work that makes payload assembly
   faster is measuring the wrong thing.

If the frontend port stalls, there is a fallback that preserves the constraint completely: keep
`public/app.js` and put a thin plain-WebSocket shim behind its `socket.on` / `socket.emit` surface,
so Go still never has to speak Socket.IO. Worth knowing it exists; not the plan of record.

### A note on the shape of this plan

**Phase A is specified in detail; Phase B deliberately is not.** Phase A changes the very interfaces
Phase B would be designed against — the module boundaries, the frozen wire contract, the fixture
corpus. Designing the Go implementation now would be designing against a codebase that is about to
move, and the detail would be stale before it was used. Phase B gets its own planning session, with
A1's fixtures, A2's visual baseline and A3's wire contract in hand as inputs. What is fixed here is
its **direction, sequencing, gates and kill criteria** — the decisions that are expensive to change
later and cheap to make now.

---

## Phase A — structural work in the current repo

Every item here is worth doing on its own merits and pays off whether or not Phase B completes.

**A1. Turn the hardware knowledge into executable fixtures.** For each collector, capture real
`print` responses from the live hAP AX3, hAP ac2 and cAP AX into JSON golden fixtures, and add
differential tests of the form *fixture in → payload out*. The ~50 documented quirks each become a
named case. Index them in `docs/routeros-behaviours.md`, one entry per quirk, each linking to the
test that pins it and the hardware it was found on. **Do this first** — it is the artefact that makes
the port survivable, and it is most reliably done while the code embodying the knowledge is live.

**A2. Capture the user-visible baseline.** Reference screenshots of every page in every theme and
palette, DOM snapshots, and scripted interaction paths — driven through the existing Playwright setup
against the live dev instance on `:3081`. This is the acceptance criterion for the whole port, and it
can only be captured while today's app is the only implementation. Pair it with the wire contract
below: together they are the definition of "exactly the same".

**A3. Freeze the wire contract.** Capture mechanically from the current code: every Socket.IO event
name and payload shape (30 collectors), the ~40 REST routes, the room naming scheme
(`router-<rid>-page-*`, `router-<rid>-dash-card-*`), and the `mikrodash_sid` cookie. This becomes the
contract both implementations are asserted against.

**A4. Split the two monster files** along the seams that already exist. The inventory found ~4,500 of
`index.js`'s 7,200 lines and ~10,500 of `app.js`'s 16,086 already sit in cleanly separable units —
`app.js` alone has ~45 IIFEs, most one per page, each closing `}());`. The irreducible core is
`buildSession` / `teardownSession` / `sendInitialState`, about 900 lines of genuine wiring. Split in
dependency order, verifying the suite after each move.

**A5. Convert the remaining 13 mirrors to registry-over-the-wire**, following the two precedents
above. Each conversion deletes a mirror and its drift test.

---

## Phase B — MikroDash_New

**B1. The RouterOS client — SETTLED, and NOT the way this plan first said.** It was written from
scratch on the argument that four protocol realities made a general-purpose library unsafe, and that
`go-routeros/routeros` got two of them wrong. **That argument was tested against all three routers on
RouterOS 7.24 and it did not survive** — the evidence is in `test-results.md`, and the client now
wraps `github.com/go-routeros/routeros/v3`.

What the testing found:

| Reality | Verdict on 7.24 |
|---|---|
| `!empty` not followed by `!done`, hanging a client | **Not reproduced** — 16/16 empty replies sent `!done` 10–30 µs later |
| Multi-block `!done`, one per interface, on wifi-qcom | **Not reproduced on the exact hardware named** — the AX3 answered 30 clients across 8 interfaces in ONE block, 6/6 |
| Raw bytes for `/file/read` | **Real, and inapplicable to Go** — `string(b)` does not transcode; the hazard was Node's UTF-8 decode |
| Trailing packets for torn-down tags | **Real, and the library handles it** — in ASYNC MODE only, which is therefore mandatory |

Two things this plan got right and should keep: the realities were worth writing down, and they were
worth testing against hardware rather than accepted. What it got wrong was treating a code reading of
the library as equivalent to running it. The quoted `reply.go:29-44` was accurate; the CONSEQUENCE it
was said to have did not occur on any router this project targets.

The from-scratch client cost a 20 ms settle window on every call — about 7× on small reads — to
defend against behaviour observed on none of them. **Zero dependencies was the other argument, and
the operator has ruled that a dependency is acceptable where it simplifies the project or is
faster.** It is both.

Version-qualified deliberately: the original findings describe **7.23** and the tests ran on **7.24**.
"Not reproduced" is not "never true", and if a 7.23 box ever reappears this is worth re-running.

`cmd/conformance` remains the gate and still runs against live hardware. Its multi-block case was
rewritten: the old one counted rows and interfaces, never `!done` sentences, so it could not fail for
the reason it existed. It now cross-checks the bulk registration-table read against the sum of
per-interface reads, which is the property block structure was only ever a proxy for — and which
would have caught the original 3-of-26 failure whatever the framing looked like.

**B2. On-disk compatibility — verified, not assumed.** Go must read the existing `/data` or the user
is locked out. The parameters are pinned:

| Artefact | Format |
|---|---|
| `settings.json` | AES-256-GCM, key = `scrypt(DATA_SECRET, "mikrodash-settings-v1", 32)` at Node's defaults (N=16384, r=8, p=1), 12-byte random IV |
| `users.json` | scrypt, `{N:16384, r:8, p:1}`, `HASH_LEN = 64`, salt = 32 random bytes hex. **Must stay a bare JSON array** — `CLAUDE.md` documents this as a security property: a version wrapper makes a rolled-back binary read zero users and re-open the unauthenticated setup route |
| `routers.json` | plain JSON, credentials encrypted as above |
| `mikrodash.db` | SQLite, WAL, numbered migrations — portable as-is |

A round-trip test (Node writes → Go reads → Go writes → Node reads) gates everything else.

**B3. The strangler boundary.** Go sits in front and proxies anything it has not implemented to the
Node app. Because the frontend is being rewritten in TypeScript, **Go never needs to implement the
Socket.IO protocol** — the new client speaks plain WebSocket, while unported pages continue to be
served by Node and its existing Socket.IO. That choice is what removes the single riskiest dependency
from the port. Rooms and their authorization role are reimplemented explicitly in Go, since room
membership is the enforcement point, not an optimisation.

**B4. First vertical slice: the DNS page.** One collector, one page, one write resource
(`dnsStatic`), minimal cross-collector dependencies. It exercises the entire stack in miniature —
RouterOS read, collector, WS push, TS render, auth gating, and one write through the resource engine.
Nothing else moves until that slice is green against live hardware.

**B5. Migration order thereafter:** read-only single-collector pages first (DNS, Packages, Bridges),
then pages with dependencies (VLANs, WAN), then the write-heavy engine pages (Firewall, Wifi
Networks, CAPsMAN), with the dashboard and its cross-collector caches last — `connTableCache` and the
geo/ASN caches shared between `connections.js` and `bandwidth.js` are the most tangled part.

---

## Verification

- Phase A: the existing suite stays green after every split; each mirror conversion deletes its drift
  test; the fixture corpus reproduces every documented quirk; the visual baseline is captured and
  re-verifies clean against the unchanged app.
- Phase B: the conformance harness passes identically against Node and Go on all three routers; the
  on-disk round-trip test passes; each migrated page is diffed payload-for-payload against the Node
  implementation **and screenshot-diffed against the A2 baseline** before its route is cut over. A
  page that renders differently has not been ported, however correct its data.
- Live hardware throughout. Four real bugs surfaced this week that a green suite did not catch, two of
  them only when a write was actually executed.

**Drift from the live repo is not covered by any of the above, and that gap is now proven.** The live
repo moved from `1f3f792` to `v0.7.33` mid-port, across 11 commits. Every gate stayed green, and two
of the changes were genuinely invisible to all of them:

- `make-golden --check` compares payload **shape**. A change to a collector's **emit frequency** — a
  fingerprint that gained a field — produces an identical payload and cannot fail a golden.
- A proplist change is invisible unless a **fixture happens to hold the row type it affects**. The
  captured routers hold no MX, SRV or TXT record, which is precisely why the DNS defect survived on
  the live side long enough to be found by reading the documentation instead.

`extract-ui --check` watches markup and `make-golden --check` watches payloads. **Nothing watches the
live repo's logic**, so the port learns about a change only when somebody writes it down.

The cheap version of a real gate: **record the live repo's HEAD in this tree, and fail when it
moves.** That converts a silent divergence into a visible one at the cost of a few lines, and it does
not require understanding the change — only noticing it. The expensive version, diffing the modules a
ported page depends on, is worth it only if the cheap one proves too noisy. Until either exists,
`../MikroDash/port_drift_notes.md` is the sole signal and the live repo's version is the thing to
watch.

**Verify the notes rather than scheduling from them.** Of the nine items in the first drift batch,
**four needed no work at all** — they were defects this port found, reported upstream, and the live
side then fixed, so they arrived described as changes the port must follow. A fifth was already
satisfied more strongly here than in the fix it mirrored. Reading a drift note as a work item without
checking this tree first would have generated five pieces of unnecessary work and two unnecessary
regressions.

## Kill criteria — decide these now, while it is cheap

Abandon Phase B and keep Phase A if any of these hold:

- The Go RouterOS client cannot pass the conformance harness against all three routers within a
  bounded effort. If the protocol layer is not solid, nothing above it can be trusted.
- The on-disk round-trip is not byte-compatible and would require migrating user data.
- Six months in, fewer than half the pages have migrated and the Node side is still gaining features
  — the strangler has become two codebases instead of one.

Phase A is valuable standalone. Phase B is not, until it reaches parity. That asymmetry is the whole
reason for the ordering.

## Risks

- **Losing the hardware knowledge is the main way this fails**, and A1 is the mitigation. If A1 is
  skipped or rushed, stop.
- **Two codebases moving at once. This has now happened**, and the shape of it is worth keeping: 11
  commits and two merged PRs landed on the live side mid-port, and the port's entire gate suite
  stayed green through all of them. The risk is not that drift breaks the build — it is that drift is
  **silent**, and the port keeps passing its own tests while diverging. See the Verification section
  for what the gates can and cannot see. A feature freeze on the Node side remains the clean answer;
  short of that, the live repo's HEAD needs watching.
- **The frontend rewrite is the larger half** and delays a working Go slice. Sequencing B4 before any
  broad frontend work keeps a real end-to-end proof early.
- **`geoip-lite` and the city index.** `src/cityIndex.js` reimplements the MaxMind binary record
  reader; a Go port needs an equivalent, and the gazetteer logic is yours to carry over.
- **Version and CHANGELOG in the current repo stay untouched** until "package it up"; `Changes.md`
  gets an append after each file edit, per the existing rules.
