# AI_CONTEXT.md

Context for an AI agent working on MikroDash. Written for the Go + TypeScript
implementation, which replaced the Node one on 2026-08-30.

**This file does not repeat `CLAUDE.md`.** That one carries the hard constraints,
the commands, the write guards, the fixture rules and the behavioural guidance,
and it is the one that outranks this. Two documents making the same claim is how
one of them goes stale — this project has paid for that repeatedly. Read
`CLAUDE.md` first; come here for the shapes it does not describe.

---

## What MikroDash is

A real-time dashboard for MikroTik RouterOS v7. It holds a persistent binary-API
connection to each watched router and streams what it sees to the browser over a
WebSocket. No agents on the router, no polling from the browser, no page refreshes.

It is read-mostly by design. Writes exist — firewall rules, queues, DNS entries,
DHCP leases, wireless config, RouterOS users, packages, backups — and every one of
them goes through a guard that can refuse it.

---

## Stack

| | |
|---|---|
| Backend | Go 1.25, standard library plus five dependencies |
| Frontend | TypeScript, bundled with esbuild, no framework |
| Database | SQLite via `modernc.org/sqlite` — pure Go, no cgo, which is what keeps the binary static |
| Transport | `github.com/coder/websocket` |
| RouterOS | `github.com/go-routeros/routeros/v3`, wrapped by `internal/routeros` |
| PDF | `github.com/go-pdf/fpdf` |
| GeoIP | `github.com/oschwald/maxminddb-golang`, reading DB-IP City Lite |

Node is a **build-time** dependency only, for the frontend bundle and for the
corpus generators in `tools/`. Nothing Node-related runs in production.

---

## Repository layout

```
cmd/mikrodash/      the server binary
cmd/conformance/    B1 — protocol conformance against live hardware, read-only
cmd/compat/         B2 — on-disk compatibility against a real /data, read-only
cmd/dnsseed/        a small utility

internal/
  routeros/         adapter over go-routeros. Cmd, Reply, Trap, Config
  collect/          29 collectors, one per RouterOS subsystem
  session/          one Session per watched router; owns the shared connection
  routers/          background pool for routers nobody is watching
  alertpool/        background pool for routers with alerting enabled
  alert/            alert rules — pure: rows in, verdict out
  alertwire/        adapts collector payloads into the evaluator, files the rows
  alertdispatch/    message assembly, cooldown, delivery fan-out
  guard/            write guards — pure; a refusal is one testable function
  resource/         the declarative resource engine (see below)
  store/            /data as it is on disk: settings, users, routers
  db/               SQLite history and audit
  server/           HTTP routes and the WebSocket protocol
  rbac/             roles, grants, per-router permission checks
  safe/             error sanitisation — everything browser-bound goes through it
  reportpdf/        PDF report rendering
  hub/              WebSocket rooms and broadcast

web/src/            TypeScript frontend; web/public/ the vendored assets
tools/              corpus generators (*-cases.js), gates (*-check.js), audits
nodecheck/          fixtures replayed into the REFERENCE collectors
testdata/           fixtures and generated corpora
```

---

## Collector pattern

Every collector in `internal/collect` follows the same shape. `dns.go` is a fair
example:

```go
type DNS struct {
    pollMs  *pollInterval
    poll    *pollLoop
    mu      sync.Mutex
    lastFp  string        // fingerprint of the last payload
    last    *DNSPayload
    lastErr string
}

func NewDNS(ros Reader, emit Emit, pollMs int) *DNS
func (d *DNS) Tick()          // one collection cycle
func (d *DNS) Start()         // begin polling
func (d *DNS) Suspend()       // stop; page blurred or dormant
func (d *DNS) Reconnected()   // the shared connection came back
func (d *DNS) RefreshNow()    // immediate re-read, e.g. after a write
func (d *DNS) Last() *DNSPayload
```

Four things are load-bearing and are easy to leave out:

1. **Parsing is separated from collecting.** `ParseDNSSettings(row)` and
   `ParseStaticEntries(rows)` are pure functions over `routeros.Reply`. That is
   what lets a fixture be replayed without a router, and it is why the
   differential gates can exist at all.
2. **Fingerprint before emitting.** `lastFp` holds a hash of the last payload; an
   unchanged payload is not broadcast. A collector that emits every tick makes the
   browser redraw for nothing.
3. **`pollLoop` is a self-rescheduling timer, not a goroutine parked on a
   channel.** It arms the next tick at the end of the current one and never
   consults the session — so a collector that is not explicitly stopped keeps
   running forever. That was a real defect: nine of fourteen collectors kept
   ticking after their session was released, reading through a closed client.
4. **Emit goes through one interception point.** `Session`'s `emit` closure is
   where the alert evaluator and the history recorder see every payload, before
   any room is chosen. Adding a side-channel emit bypasses both.

---

## Collector delivery model

Three gates decide whether a collector runs at all:

- **Nobody watching the router** — nothing polls and no channel is held.
- **Nobody on the page** — a page-scoped collector runs only while somebody is on
  its page. Leave the VLANs page and its poll stops and its `/listen` closes.
- **Dormancy** — a collector whose data comes back empty, or whose menu the router
  does not have, suspends itself and its card says so instead of going stale. It
  re-probes on a backoff growing to ten minutes, and wakes on page focus or
  reconnect.

**Concurrent API channels, not data volume, are the documented bottleneck.** A
page nobody is looking at should cost the router nothing. This is why "more
efficient" in this codebase means fewer channels, never faster parsing.

---

## Writing to RouterOS — the resource engine

`internal/resource` is declarative. A resource declares its fields, their types,
validation and which guards apply; the generic path in `internal/server/resource.go`
handles the rest — form generation, validation, the guard verdict, the write, the
audit row and the undo entry.

```go
type Resource struct { Key, Page, Label string; Fields []Field; ... }
func (r *Resource) Validate(values map[string]string, editing bool) (Validated, []Error)
func (r *Resource) GuardTargets(action string, values, before map[string]string) []string
```

Socket events: `res:new`, `res:row`, `res:schema`, `res:preview`, `res:save`,
`res:remove`, `res:move`, `res:action`, `res:undo`, `res:redo`.

**A resource declaring a guard that is not in `portedGuards` has its writes
REFUSED**, not logged-and-allowed. The whole point of a guard is that its absence
is not survivable. `portedGuards` in `internal/server/resource.go` is the
authority — check the map, never prose about it.

`res:preview` renders the exact RouterOS command a write will issue. It is worth
using when verifying a write path by hand: it shows the command before anything
touches the router.

---

## The WebSocket protocol

One socket per browser. Frames are `{"event": "...", "data": ...}`.

Inbound events are page subscriptions (`dashboard`, `dns`, `firewall`, `queues`,
`wireless`, …), lifecycle (`router:select`, `page:focus`, `page:blur`,
`dashcard:focus`, `dashcard:blur`), and the write verbs above plus per-page ones
(`wan:renew`, `wan:release`, `queue:save`, `rosuser:save`, `packages:schedule`,
`backups:run`, `wifiscan:start`, …).

**Rooms carry the fan-out.** `router-<id>` is router-wide; `router-<id>-<page>` is
page-scoped. An empty sub means router-wide, which is what the top-bar chrome
needs — the gauges and the uptime chip belong to no page. A sub naming several
rooms comma-separated delivers ONE copy to the union, which matters because a
viewer can be in two of them.

**Per-connection state does not survive a reconnect.** The server holds the
selected router on the connection, so the client re-asserts it on every `connect`.
Anything the operator chose — an interface, a filter, a page — needs somewhere with
a longer life than the socket.

---

## REST endpoints

34 routes, for the things a socket is the wrong shape for: auth
(`/api/auth/status`, `/api/auth/login`, `/api/auth/logout`, `/api/auth/permissions`),
configuration (`/api/routers`, `/api/sites`, `/api/settings`, `/api/users`,
`/api/groups`, `/api/roles`, `/api/grants`), history and export
(`/api/reports/*`, `/api/audit`), layout (`/api/dashboard-layout`,
`/api/nav-prefs`), and `/healthz`.

`/healthz` is unauthenticated by design and returns 200 only after startup
completes, RouterOS is connected and the critical collectors are delivering fresh
data. Everything else requires a session.

---

## Settings

`/data/settings.json`, read through `store.Settings()` and merged over defaults.
Two rules that have each caused a real defect:

- **A setting that is rendered, validated and persisted but never READ is the most
  common defect class in this codebase.** Four have been found:
  `topN`, `topTalkersN`, the retention settings and `rosDebug`. Each looked
  complete from the UI. `tools/settings-consumer-audit.js` now checks the class —
  but read its own note: a mutation survived it, so it is a net beneath a
  call-site test, not a substitute for one.
- **Coercion is deliberate and asymmetric.** A boolean arriving from a form may be
  the string `"false"`, which is truthy. `jsIsTrue` / `jsIsFalse` in
  `internal/store` reproduce the reference semantics exactly; do not "simplify"
  them to `!!`.

---

## Security model

- Credentials at rest are AES-256-GCM with a key from `DATA_SECRET` or
  `/data/.secret`. The envelope is `iv‖tag‖ciphertext`.
- Dashboard passwords are scrypt-hashed. **The salt is a string, not decoded
  bytes** — get this wrong and every existing user is locked out.
- `users.json` must stay a bare JSON array. That is a security property, not a
  formatting preference.
- **Every error reaching a browser goes through `safe.Message()`**, which redacts
  addresses, hostnames and credentials. Sanitising at the point of STORAGE rather
  than the point of sending is deliberate: `lastErr` is read by more than one
  sender, and each one would otherwise have to remember.
- RBAC is per-router and checked **at the moment of use**, not at subscribe time.
  Alert delivery asks `rbac.Can(userID, "router:read", routerID)` at send time, so
  revoking a grant stops delivery on the very next alert.
- Passphrases are never read back. Collector proplists do not request them, and an
  empty field in a form means "leave the current one alone".

---

## Known RouterOS API quirks

These are properties of RouterOS, not of any implementation, and they cost real
debugging time to find.

### `/ip/route/print` — `.flags` omitted for default-state routes

RouterOS v7 on some firmware builds omits `.flags` for routes in their default
(active) state, treating active+static as unremarkable. Disabled routes always
receive `.flags` because disabled is non-default. Always include a fallback
type-inference path: if no type flag is set and the gateway is a real IP address
(not an interface name like `bridge`), infer `static=true`. `/ip/route/listen`
events always carry the full row, so this only affects the initial `/print`.

### `=.proplist=` on registration-table calls — can filter rows

On the v7 wifi package, including unknown or absent field names in `=.proplist=`
for `/interface/wifi/registration-table/print` can make RouterOS **filter rows**
rather than omit those fields per row — requesting `signal` (which is
`signal-strength` in the new API) may return only clients where it is non-empty.
**Do not use `=.proplist=` on wireless registration-table calls.** The table is
small enough that the optimisation is not worth the risk.

### `/queue/*` — units, unlimited, and where statistics come from

- **Statistics need no flag.** `rate`, `packet-rate`, `bytes`, `packets`,
  `dropped` and the `queued-*` fields all come back on a plain print.
- **The API answers in raw bps.** `max-limit=15M/20M` reads back as
  `"15000000/20000000"`. Suffixes are accepted in, never returned out.
- **Unlimited is `0`, not absent.** `"0/0"` means explicitly unlimited; a missing
  field means the router said nothing. Collapsing the two reports a deliberate
  choice as an unknown.
- **`max-limit` must be >= `limit-at`**, so the pair has to move together and a
  form editing one half fails.
- **The two menus are different shapes.** Simple uses pairs and `packet-marks`
  (plural) and has a `dynamic` flag; tree uses single values and `packet-mark`
  (singular) and has no `dynamic` field at all.
- **FastTrack does not disable a queue, it diverts connections.** It bypasses
  simple queues and trees with `parent=global`; an interface-parented tree is
  unaffected. This is the usual reason a queue looks configured and does nothing.

### `/user/group/set` — a positive policy list is ADDITIVE

On `add`, RouterOS fills in the negations itself. On **`set` it does not** — a
positive-only list only adds, and a policy is removed only when explicitly named
with `!`:

```
group holds read,test,api
/user/group/set =policy=read                      -> read,test,api   (silently unchanged)
/user/group/set =policy=!local,...,read,...,!api  -> read
```

A quiet failure, not an error: a permissions editor built on `add` behaviour
appears to work while never removing anything. `collect.BuildPolicy` therefore
always emits the full 17-policy vocabulary with explicit negations, correct for
both verbs. Verified on RouterOS 7.24.

### `!empty` and cancelled tags — handled by the adapter, not by you

RouterOS 7.18+ sends `!empty` when a command returns zero results. The Node
implementation needed a library patch for this; `go-routeros` handles it, and
`cmd/conformance` asserts the behaviour on real hardware (16 of 16 empty replies
across three routers sent `!done` 10–30 microseconds later).

A packet arriving for a tag that has already been cleaned up — a trailing sentence
after `!done`, or a delayed response after a stream is stopped — is discarded by
the **tag map that async mode maintains**. This is why `Dial` calls `Async()` and
why synchronous mode is not an option: sync keeps no tag map, so nothing catches
the sentence, and the failure takes down a connection every collector shares.

---

## Testing conventions

- **Go**: `go test ./...`, standard library `testing` only, no frameworks.
- **Corpora are generated, never transcribed.** `tools/*-cases.js` RUN or LIFT the
  reference implementation; `--check` fails when one is stale.
- **Mutate what you write.** A gate that no mutation kills is decoration. Record
  equivalent mutants as equivalent rather than counting them as kills.
- **Assert the check found something.** Anything scanning a set must fail when the
  set is empty, or it becomes indistinguishable from a broken check.
- **Test the call site when the defect is "somebody forgot to call it."** Asking
  the callee what it returns will not find it.
- `sh tools/verify.sh` runs everything and discovers what to run.

---

## Run instructions

```bash
# Frontend, then binary
go run ./cmd/webbuild -dir web
go build ./cmd/mikrodash
./mikrodash -data ./devdata -web web/dist -static web/public

# Or the production image
docker build -t mikrodash-go:latest .
docker run -d --name MikroDash --restart unless-stopped \
  -p 3081:3081 -v mikrodash_data:/data mikrodash-go:latest
```

The feature switches — `-history`, `-backup-scheduler`, `-retention`,
`-alert-dispatch` — default OFF because each is unsafe to run twice against the
same routers. The image's `CMD` turns them on, because a normal install is the
only MikroDash watching its fleet.
