# AI_CONTEXT.md

This file gives AI coding assistants (Claude, Copilot, Cursor, etc.) immediate grounding in the MikroDash codebase. Read this before suggesting any changes.

---

## What MikroDash is

MikroDash is a **real-time MikroTik RouterOS v7 dashboard**. It connects directly to the RouterOS binary API over a persistent TCP connection, streams live network data to a browser via Socket.IO, and serves a static single-page UI over Express. There are no page refreshes, no polling from the browser, no external agents, and no build step.

**Target user:** Network operator/admin on a trusted LAN.  
**Not for:** Public internet exposure — there is no HTTPS termination. (Role-based access control *does*
exist: cookie sessions with `admin`/`viewer` roles — see the Security model section.)

---

## Hard constraints — do not violate these

| Constraint | Detail |
|---|---|
| No build step | Plain CommonJS (`require`/`module.exports`) throughout. No TypeScript, Babel, Webpack, Vite, or any transpiler. |
| No new test frameworks | Tests use `node:test` + `node:assert/strict` only. No Jest, Mocha, Vitest, or other deps. |
| No CDN dependencies | All frontend assets are vendored under `public/vendor/`. Never add a `<script src="https://...">` tag. |
| No new runtime deps without approval | The dependency list in `package.json` is intentional and minimal. |
| Collector pattern must be followed | Every new data collector must implement the contract described below. |
| Streaming-first architecture | **Prefer streaming over polling wherever RouterOS supports it.** Two streaming mechanisms exist: (1) `/listen` streams — event-driven, fires only when data changes (e.g. `/ip/arp/listen`); (2) `=interval=N` on print commands — RouterOS pushes a full snapshot every N seconds over a persistent channel (e.g. `/system/resource/print =interval=2`). Use `=interval=N` for any command that lacks a `/listen` variant but produces regular data (system resources, traffic counters, ping RTT, connection table). Polling via `setInterval` is a last resort only when neither mechanism is viable. When converting a collector to streaming, set `pollMs: 0` in the payload and show "Event-driven" in the Settings UI instead of a slider. Streaming is the default, not a mandate: since #105 each router can be switched to Poll mode via its `collection` block, so a `pollable` collector must implement both paths. |
| Credentials never in plaintext | Router and dashboard passwords are AES-256-GCM encrypted in `settings.json` and masked in all API responses. |
| Vendored assets are read-only | Never modify `public/vendor/` unless explicitly instructed. |

---

## Stack

| Layer | Technology |
|---|---|
| Runtime | Node.js (CommonJS, no transpilation) |
| HTTP server | Express 4 |
| Real-time transport | Socket.IO 4 |
| Router API | node-routeros (binary RouterOS API over TCP) |
| Security | helmet, express-rate-limit |
| Geo/ASN | geoip-lite, custom asnLookup util |
| IP utilities | ipaddr.js |
| Config | dotenv + `/data/settings.json` (Docker volume) |
| Frontend | Vanilla JS, Tabler CSS, Chart.js (all vendored) |
| Fonts | JetBrains Mono, Syne (vendored) |
| Tests | node:test + node:assert/strict |
| Container | Docker + docker-compose |

---

## Repository layout

```
src/
├── index.js                   # Entry point: Express + Socket.IO wiring, collector orchestration,
│                              #   settings REST API, sendInitialState(), graceful shutdown
├── routers.js                 # Load/save/add/edit/delete routers.json — per-router connection config,
│                              #   AES-256-GCM encrypted passwords, name uniqueness, backwards-compat seed.
├── settings.js                # Load/save settings.json with AES-256-GCM credential encryption.
│                              #   Exports: load(), save(), getPublic(), isMasked(), DEFAULTS
├── health.js                  # computeHealthStatus() — logic for /healthz endpoint
├── shutdown.js                # scheduleForcedShutdownTimer() — fallback exit after 5 s
├── db.js                      # SQLite (better-sqlite3) at /data/mikrodash.db. Numbered MIGRATIONS[].
│                              #   Tables: ping_samples, traffic_samples, bandwidth_usage,
│                              #   alert_events, connectivity_events. open() once; close() on shutdown
├── db-writer.js               # Write facade. Accumulates raw per-second traffic/bandwidth/ping samples
│                              #   into 1-minute bucketed averages before flushing — the DB never sees
│                              #   raw per-second rows. Collectors must not write to db.js directly
├── geo.js                     # The single load point for geoip-lite: available(), lookup(),
│                              #   unavailableReason(). Never require('geoip-lite') elsewhere — a load
│                              #   failure must stay visible instead of silently returning no geo data
├── alerter.js                 # Alert evaluator: wraps io.emit to detect threshold crossings, applies
│                              #   per-subject cooldown, drives push notifications
├── notifier.js                # Push delivery — Telegram Bot API, Pushbullet, SMTP (nodemailer).
│                              #   testChannel() backs POST /api/settings/test-notification
├── alertSessions.js           # Background per-router sessions (System/Ping/Netwatch/Vpn/
│                              #   InterfaceStatus collectors) so alerts fire for NON-active routers
│                              #   too. syncSessions(), getStatusMap()
├── overviewSessions.js        # Same idea for the Routers-page overview cards: getSummaries(), plus
│                              #   suspend()/resume() when that page is not visible
├── auth/
│   └── sessionStore.js        # Cookie session store: createSession(), getSession(), parseCookieHeader(),
│                              #   buildCookieHeader(), prune interval. Tokens are 32-byte random hex.
├── users.js                   # User accounts in /data/users.json — scrypt-hashed passwords, admin/viewer
│                              #   roles, per-user allowedRouterIds, timingSafeEqual verification
├── collectors/                # One file per RouterOS data domain (see Collector Pattern below)
│   ├── traffic.js             # RX/TX Mbps per interface, 1 s polling, ring-buffer history
│   ├── system.js              # CPU/RAM/HDD/temp/uptime/version/update-check
│   ├── connections.js         # Firewall connection table: protocol counts, top sources/destinations,
│   │                          #   geo enrichment, port aggregates, IPv6, truncation metadata
│   ├── bandwidth.js           # Per-connection bandwidth (Mbps), ASN/org badges, interface+proto filters
│   ├── talkers.js             # Top-N devices by MAC with TX/RX rate calculation
│   ├── dhcpLeases.js          # DHCP lease stream + initial load; name resolution (comment > hostname); server→interface→VLAN join
│   ├── dhcpNetworks.js        # LAN CIDRs, WAN IP from interface addresses, lease counts per network
│   ├── arp.js                 # ARP table snapshot; bidirectional IP↔MAC lookup
│   ├── wireless.js            # Wireless clients: band detection, signal, SSID, DHCP/ARP enrichment.
│   │                          #   ⚠ No =.proplist= on registration-table calls — see RouterOS quirks below
│   ├── vpn.js                 # WireGuard peers: connected/idle state, TX/RX rates, stale pruning
│   ├── firewall.js            # Filter/NAT/mangle rules with delta packet counts between polls
│   ├── interfaceStatus.js     # All interfaces: running, disabled, IPs, RX/TX Mbps, cumulative bytes
│   ├── ping.js                # ICMP ping RTT + loss%, ring-buffer history, fallback averaging
│   ├── routing.js             # Route table (/ip/route/listen stream) + BGP sessions (/routing/bgp/session/listen stream)
│   ├── netwatch.js            # NetWatch host up/down state (/tool/netwatch/listen stream), 60 s heartbeat
│   └── logs.js                # RouterOS log stream, severity classification, bounded history buffer
├── routeros/
│   ├── client.js              # ROS class (extends EventEmitter): connectLoop() with exponential backoff,
│   │                          #   write(), stream(), waitUntilConnected(). Emits: connected, close, error
│   ├── patchVerification.js   # verifyRouterOSPatchMarkers() — exits process if patch is missing
│   └── classifyError.js       # classifyRosError() — maps raw Node/ROS error codes to human reasons
├── security/
│   └── helmetOptions.js       # buildHelmetOptions() — CSP with self-hosted asset allowlist, HSTS
└── util/
    ├── ringbuffer.js           # RingBuffer(size): push(item), toArray(), get(i)
    ├── ip.js                   # isPrivateIP(), cidrContains(), normalizeIP() — wraps ipaddr.js
    ├── logger.js               # timestamped console wrapper — see the CodeQL note on _patchConsole
    └── asnLookup.js            # lookupASN(ip) → { asn, org } using geoip-lite data

public/
├── index.html                 # Single-page app shell: nav, page containers, modal templates
├── app.js                     # ALL frontend logic: Socket.IO client, Chart.js charts, DOM updates,
│                              #   page routing, stale-data timers, alert panel, push notifications
└── vendor/                    # Read-only vendored assets
    ├── tabler.min.css
    ├── chart.umd.min.js
    ├── topojson-client.min.js
    ├── world-atlas/countries-110m.json
    └── fonts/                 # JetBrains Mono, Syne (woff2 + fonts.css)

test/                          # 11 files. NOT copied into the image — `docker cp test/. mikrodash:/app/test`
├── collector-data-transforms.test.js          # tick() → emitted payload shape and value correctness
├── collector-lifecycle.test.js                # start(), timer setup/teardown, stream, reconnect
├── production-resilience-regressions.test.js  # Regression tests for confirmed production bugs
├── smoke-fixes.test.js                        # Smoke-level sanity checks
├── auth-rbac.test.js                          # Session auth + admin/viewer RBAC + allowedRouterIds scoping
├── security-and-validation.test.js            # Alerter evaluator, input validation, credential masking
├── data-cleanup.test.js                       # #77 purge: scoping by router/type/age, preview == delete, VACUUM
├── reports-bandwidth-summary.test.js          # #62 report summary queries (moved out of the browser)
├── connectivity-timestamp.test.js             # #99: a debounced offline event records when the disconnect
│                                              #   was OBSERVED, not when the debounce expired
├── review-fixes.test.js                       # 2026-05-29 review: ping bucketing, alerter DB decoupling +
│                                              #   cooldown ordering, per-router evaluator isolation
└── code-review-remediation.test.js            # 2026-07-26 review: connectLoop listener containment,
                                               #   connections suspend/watchdog, traffic bindSocket
                                               #   idempotency, empty-table stream packets, ping restart timer

docs/superpowers/specs/
└── 2026-03-10-test-coverage-design.md         # Authoritative test design philosophy for this project

deploy/r5s/                    # Alternate docker-compose for NanoPi R5S deployment
patch-routeros.js              # One-time patch script — must be run after every npm install
.env.example                   # All supported environment variables with comments
Dockerfile
docker-compose.yml
CHANGELOG.md
```

---

## Versioning & changelog rules

### When to bump the version

**Do not bump the version or update CHANGELOG.md or README.md during a working session.**

Version bumps, changelog entries, and README updates happen **only at the explicit end of a session**, when the user says something like "package it up", "we're done", "final zip", or otherwise signals they are satisfied with all changes made during the session. Until that instruction is given:

- Keep `package.json` version unchanged.
- Do not add entries to `CHANGELOG.md`.
- Do not modify `README.md`.

When the user does request final packaging, **one version bump covers the entire session** — all changes made since the previous release go into a single changelog entry. Never create one entry per fix or per sub-session.

### Semantic versioning

`major.minor.patch` in `package.json`. Bump patch for bug fixes; minor for new features or behaviour changes; major for breaking changes.

### How to write a CHANGELOG.md entry

1. Add the new version block at the **very top** of `CHANGELOG.md`, immediately after the file header line (`All notable changes…`).
2. Use this exact format:
   ```
   ## [x.y.z] — Short title describing the release

   ### Added
   - High-level user-facing feature descriptions only.

   ### Changed
   - Behaviour changes, architecture shifts, removed UI elements.

   ### Fixed
   - User-observable bugs, not internal refactors.
   ```
3. **Do not edit any previous version block.** The entry for the version being released is the only thing that changes.
4. **One entry per meaningful change** — no sub-bullets for implementation details, test names, or trial-and-error intermediate steps. If a bug was fixed through multiple iterations, write one bullet describing the final fix and its user-visible impact.
5. **Omit:** test additions, internal refactors with no user-visible effect, intermediate debugging steps, lint fixes, comment updates.
6. **Do not duplicate** a fix across multiple bullets. If a bug had multiple contributing causes, describe the root cause and fix once.

### How to update `package.json`

Change only the `"version"` field. Nothing else.


---

## Collector delivery model

| Collector | Delivery | RouterOS endpoint(s) | Notes |
|---|---|---|---|
| `traffic.js` | **Stream** (interval=1 s) | `/interface/monitor-traffic` | One persistent channel per subscribed interface. NOT idle-gated: the stream always runs to feed the SQLite history; only browser emits are suppressed when no clients |
| `system.js` | **Stream** (interval=N s) | `/system/resource/print` | Resource stream pushes every pollMs; `/system/health/print` polled separately at 2× interval (no interval= support); update-check every 12 h |
| `connections.js` | **Stream** (interval=N s) | `/ip/firewall/connection/print` | Initial `/print` on connect; interval stream replaces polling; watchdog restarts stale streams; idle-gated; skips geo computation when `page-connections` room is empty |
| `bandwidth.js` | Poll | `/ip/firewall/connection/print` | Shares `connTableCache` with connections; idle-gated |
| `talkers.js` | **Stream** (interval=N s) | `/ip/kid-control/device/print` | Backs off when Kid Control unavailable; idle-gated |
| `dhcpLeases.js` | **Stream** | `/ip/dhcp-server/lease/listen` | Initial `/print` on connect; also reads `/ip/dhcp-server` + `/interface/vlan` once per connect to resolve each lease's interface and VLAN |
| `dhcpNetworks.js` | Poll | `/ip/dhcp-server/network/print` | Slow poll (default 10 min) |
| `arp.js` | **Stream** | `/ip/arp/listen` | Initial `/print` on connect |
| `wireless.js` | Poll | `/interface/wifi/registration-table/print` | Probes both wifi and legacy wireless APIs |
| `vpn.js` | **Stream** + Poll | `/interface/wireguard/peers/listen` | Stream for peer state; poll for counter snapshots; heartbeat every 60 s |
| `firewall.js` | **Stream** + Poll | `/ip/firewall/{filter,nat,mangle}/listen` | Three concurrent streams for rule state; poll for delta packet/byte counts; heartbeat every 60 s |
| `interfaceStatus.js` | **Stream** (interval=N s, ×3) | `/interface/print`, `/ip/address/print`, `/interface/monitor-traffic` | Three concurrent interval streams: interface state, IP addresses, byte counters; idle-gated |
| `ping.js` | **Stream** (interval=N s) | `/tool/ping` | Persistent interval stream replaces per-tick write(); ring-buffer history |
| `routing.js` | **Stream** | `/ip/route/listen` + `/routing/bgp/session/listen` | BGP keepalives fingerprint-suppressed |
| `netwatch.js` | **Stream** | `/tool/netwatch/listen` | Initial `/print` on connect; 60 s heartbeat re-emit; drives NetWatch host-down alerts |
| `logs.js` | **Stream** | `/log/listen` | Bounded history buffer (500 entries) |
| `queues.js` | **Stream** (`/listen` ×2) + Poll | `/queue/simple/print`, `/queue/tree/print` | Two listen channels (one per menu) carrying no data — they mark the tables stale and the ordinary tick reads them. The tick runs on its own rather than waiting for stream data: a router with no queues would never fire a listen, and the page would sit on "waiting for data" forever. Rates are derived from the byte counter over our own poll window (ppp.js idiom), seeded on the first tick only from the router's `rate`. Borrows the FastTrack summary from `firewall.js` by reference, `requires: []` |
| `wan.js` | **Stream** (`/listen`) + Poll | `/interface/detect-internet/state`, `/ip/dhcp-client`, `/ip/route`, `/ip/address`, `/interface` | The uplink set is RouterOS's (`state=internet`), matching the Dashboard Network card; it does NOT infer uplinks from default routes. `/ip/route/listen` because a default route going inactive IS a failover. Rates borrowed from `ifStatus` by reference, projected by name; `requires: []`. `detectionEnabled` distinguishes "detection is off" — the common case, since `detect-interface-list` defaults to `none` — from "no uplinks" |
| `rosusers.js` | Poll | `/user/print`, `/user/group/print`, `/user/active/print`, `/user/settings/print` | Poll-only by design (`streamKey: null`), like `packages.js`: a router's user list changes when an operator edits it, so a channel held open for weeks buys nothing. Slow default (60 s) with `refreshNow()` after every action. **Reads only** — every write lives in the socket handlers, enforced by a source guard |

**Page gating — a new page must opt in.** `streamRooms` in `src/pages.js` means "suspend this
collector when nobody occupies these rooms". It once held only the five collectors with an
`=interval=N` counter stream, so every page added afterwards declared `[]` and kept polling the
router from the Dashboard — four collectors at 5 s and six idle `/listen` channels, for pages nobody
was viewing. A page whose collector reads the router on a timer names its own `page-<key>` room. Two
consequences to keep in step: the collector must live at `session.<page>` for the room sweep to find
it, and `_idleResume()` must NOT resume it by name, which is what defeated the gate the first time.

**Rule:** always prefer streaming. Use `/listen` for event-driven data; use `=interval=N` on print commands that lack a `/listen` variant. Fall back to `setInterval` polling only when the RouterOS command genuinely cannot push (rare — check both mechanisms first).

---

## Known RouterOS API quirks

### `/ip/route/print` — `.flags` omitted for default-state routes

RouterOS v7 on some firmware builds omits the `.flags` field for routes in their default (active) state, treating active+static as unremarkable. Disabled routes always receive `.flags` because disabled is non-default. When writing route-related code, always include a fallback type-inference path: if no type flag is set and the gateway is a real IP address (matches an IPv4/IPv6 pattern, not an interface name like `'bridge'`), infer `static=true`. `/ip/route/listen` stream events always carry the full row so this only affects the initial `/print` load.

### `=.proplist=` on registration-table calls — can filter rows

On RouterOS v7 new wifi package, including unknown or absent field names in `=.proplist=` for `/interface/wifi/registration-table/print` can cause RouterOS to **filter rows** rather than simply omitting those fields per row. For example, requesting `'signal'` (which is `'signal-strength'` in the new API) may return only clients where that field is non-empty — resulting in only 1 of N clients being returned. **Do not use `=.proplist=` on wireless registration-table calls.** The table is small enough that the optimisation is not worth the risk.

### `/queue/*` — units, unlimited, and where the statistics come from

Settled against a live router while building the Queues page:

- **Statistics need no flag.** `rate`, `packet-rate`, `bytes`, `packets`, `dropped` and the
  `queued-*` fields all come back on a plain `/queue/simple/print`. The CLI's `print stats` has no
  API equivalent to pass.
- **The API answers in raw bps.** `max-limit=15M/20M` on input reads back as `"15000000/20000000"`.
  Suffixes are accepted on the way in and never returned on the way out.
- **Unlimited is `0`, not absent.** An unlimited queue reads back as `"0/0"`, so `0` means
  "explicitly unlimited" and a missing field means "the router said nothing". Collapsing the two
  reports a deliberate choice as an unknown.
- **`max-limit` must be ≥ `limit-at`**, refused as `failure: download-max-limit less than
  download-limit`. The pair has to move together, so a form that edits only one half fails.
- **The two menus are different shapes.** Simple uses pairs and `packet-marks` (plural) and has a
  `dynamic` flag; tree uses single values and `packet-mark` (singular) and has **no `dynamic` field
  at all**.
- **FastTrack does not disable a queue, it diverts connections.** Measured: a fresh queue on the LAN
  still counted several megabits within seconds while the default `fasttrack-connection` rule was
  active. It bypasses simple queues and queue trees with `parent=global` — an interface-parented tree
  is unaffected.

### `/user/group/set` — a positive policy list is ADDITIVE

On `add`, RouterOS fills in the negations itself: send `=policy=read,api` and it stores all 17
policies with every unnamed one negated. On **`set` it does not**. A positive-only list only adds,
and a policy is removed only when it is explicitly named with a `!`:

```
group holds read,test,api
/user/group/set =policy=read                      -> read,test,api   (silently unchanged)
/user/group/set =policy=!local,...,read,...,!api  -> read
```

This is a quiet failure, not an error: a permissions editor built on the `add` behaviour appears to
work while never removing anything. `RosUsersCollector.buildPolicy()` therefore always emits the
full vocabulary with explicit negations, which is correct for both verbs. Verified on RouterOS 7.24.

### `!empty` reply — RouterOS 7.18+

RouterOS 7.18+ sends `!empty` when a command returns zero results. The `node-routeros` library throws `UNKNOWNREPLY` on this. `patch-routeros.js` patches `Channel.js` to treat `!empty` as an empty done (resolves to `[]`). This patch must be applied once after every `npm install` — the `Dockerfile` runs it automatically.

### UNREGISTEREDTAG crash — node-routeros

When RouterOS sends a packet for a tag that `node-routeros` has already cleaned up (trailing packet after `!done`, or delayed response after a stream is stopped), the library throws `UNREGISTEREDTAG` synchronously inside a socket data event — uncatchable by user code. `patch-routeros.js` patches `Receiver.js` to log and discard these packets instead.

---

## Writing to RouterOS — the resource engine

Issue #97. Four write surfaces were built by hand (Queues, Router Users, WAN lease actions,
Packages) and each carries its own copy of the same seven steps: check both gates, read fresh, match
the row, validate, build the sentence, write, audit, refresh. **Anything after those four is a
registry entry, not a handler.**

- `src/routeros/resources.js` — the registry. A resource is a *description*: `page`, `collector`,
  `menu`, `identity`, `readOnlyWhen`, optional `actions` and `guard`, and a list of fields.
- `src/index.js` — six generic handlers (`res:schema`, `res:row`, `res:save`, `res:remove`,
  `res:action`, `res:preview`) that execute every resource.
- `public/app.js` — one dialog, built from the schema the server sends.

**A field's `type` does three jobs from one declaration:** it picks the server-side validator, it
picks the browser's input widget, and it is the allow-list — `buildArgs()` can only ever emit
`=<field.ros>=`, so a key the registry does not name cannot reach a RouterOS sentence. Be precise
about what that does and does not buy: the binary API length-prefixes every word, so a `=` inside a
*value* cannot forge a second argument the way it could on a CLI. What the allow-list stops is an
unnamed *key* being set.

**Rules for a new resource:**

- **The browser gets `describe()`, never a copy of the fields.** app.js already carries five
  hand-maintained mirrors of server-side lists; a sixth would be a sixth thing to drift. Nothing in
  app.js knows a field name.
- **`identity` names the field that is round-tripped.** A `.id` survives a rename, which makes it the
  right key to *address* a row with and the wrong one to *identify* it by. If the freshly-read row no
  longer carries the identity the operator was looking at, the write is refused as `stale-row`.
- **`readOnlyWhen` is evaluated against the fresh read, never the browser's claim** — same for an
  action's `when()`. The browser offering a button is a hint, never a permission.
- **The collector a resource names must feed the page it names.** A test enforces it: otherwise a
  save refreshes a view nobody is looking at, and the page in front of the operator keeps the old
  row. That collector needs a `refreshNow()`.
- **A secret field must declare `type: 'secret'`.** It is never read back into the form, never sent
  to the browser, skipped rather than cleared when blank, and masked in the audit trail by
  `_resAuditValues()` — which keys on the declared **type**, not on the field name, because
  `audit.js`'s `CRED_PATTERN` does not match `presharedKey`.
- **A card opts in with two attributes in `index.html` and nothing else:** `data-res-add="<key>"`
  for the + Add button, `data-res-rows="<key>"` for the rows that open the edit form. Rows carry
  `data-id` and `data-identity` via `resRow()`. A row with nothing to edit carries no `data-id` and
  is simply not clickable.

### Guards — five of them now, and they do not agree on purpose

|  | verdict | on failure | question |
|---|---|---|---|
| `selfGuard` | **refuses** | fails **closed** | may this `/user` row be touched |
| `queueGuard` | warns | fails open | would this queue throttle us |
| `wanGuard` | warns | fails open | is the management path local or remote |
| `selfPath` | warns | fails open | which **interface** are we reachable on |
| `fwGuard` | warns | fails open | could this **rule** block our session |

Only `selfGuard` refuses, because breaking the login is unrecoverable from inside the app — the fix
is WinBox. The other three warn: their mistakes are recoverable from the very row that caused them.
All three fail **open** because `/user/active` is denied to the read-only API user the README
recommends, so an unreadable answer is the common case, not an edge one.

A guard's verdict shape is `{ level, code, detail, fingerprint }` everywhere, so the acknowledgement
dance is shared: the server describes the consequence and writes nothing, the browser confirms, the
retry carries the fingerprint, a mismatch is `stale-warning`. Recomputing the fingerprint from a
fresh read is what stops an ack being carried from one row to another or replayed against a later
write.

**Do not make a warning fire on the innocent case.** `selfPath` skips an update that only changes a
comment or an MTU, and a VLAN names only itself rather than its parent — an address on `bridge` would
otherwise make every VLAN riding that bridge warn. Every false alarm trains the operator to click
through the one that mattered.

### Declined from #97, deliberately

- **A per-router "management enabled" toggle.** RBAC is the gate, consistent with the four write
  surfaces already shipped.
- **A backup before each write.** `/system/backup/save` writes to router flash, and a hAP ac2 has
  ~16 MB.
- **Safe mode.** Not reachable over the API at all — it is a console/WinBox session feature, there is
  no `/system/safe-mode` node anywhere in the tracked command tree (7.9 to 7.24rc2), and
  `/system/history` exposes only find/get/print, so there is no undo verb either.
### Ordering, and the rest of the registry vocabulary

The firewall is the one place where **position is meaning** — a rule below the final drop never runs,
and the same rule above an accept blocks everything. Four declarations exist for it, and each is
general rather than firewall-specific:

- **`ordered: true`** puts ↑/↓ on the rows and lets `res:move` address the menu. The browser sends a
  **direction, never a position**: the server resolves the neighbour from a read taken in the same
  tick, so two quick clicks cannot land a rule at an index computed against a table that has already
  changed. RouterOS `move` accepts `.id` for both `=numbers=` and `=destination=` — verified live —
  and inserts *before* the destination, which is why moving down targets the row two below.
- **`identity` may be a list of fields.** A firewall rule has no name and nothing unique. The row is
  *addressed* by `.id` and the composite only has to answer "is this still the rule I clicked" —
  which matters because **RouterOS reuses `*N` ids after a delete**. `public/app.js` carries the one
  mirror of this (`fwIdentity`), and a test asserts the two agree field for field.
- **`optionsFrom: { values: [...] }`** for a fixed vocabulary. The field type stays `text` on purpose:
  RouterOS has more actions than any list will name, and a `select` validates against its options, so
  a rule with an exotic action could not be edited at all.
- **`check(values)`** for a rule spanning two fields. Firewall's is RouterOS's own — *"ports can be
  specified if proto is tcp,udp,udp-lite,dccp,sctp"* — met during live verification, where the router
  refuses without naming a field. It runs only after every field passed its own type, so a rejected
  value does not produce a second complaint about the first.
- **`requiresMenu`** hides a resource whose menu ships with an optional package (VETH and containers).

**What fwGuard does not model, and must keep saying so:** ORDER. Whether a rule takes effect depends
on every rule above it, and evaluating that means a firewall simulator whose bugs would be invisible.
So it asks two narrower questions it can actually answer — *could this rule match our management
traffic*, and *does the accept being removed currently match it* — and the browser prints "rule order
is not taken into account" on every prompt. Also unmodelled: address lists, `jump` targets, layer7,
time windows and negated matches.

Two consequences worth knowing: the firewall collector now emits **disabled rules** (a rule you
cannot see is a rule you cannot re-enable), and the Action Breakdown and Chain Count cards filter them
out in `app.js` so they still mean "what is in force" — while Rule Counts keeps them, which is what
its long-dead "N off" badge was always for.

---

## Collector pattern

**Streaming-first, with a per-router opt-out:** always prefer a `/listen` stream over a poll interval
when the RouterOS endpoint supports it. Streaming stays the default, but since #105 a router may be
switched to Poll mode, may have individual collectors disabled, and may override any interval — all
resolved by `resolveCollection()` in `src/collection.js` and applied in `buildSession()`. A collector
marked `pollable` in the registry **must** implement both paths, and both must produce the identical
`lastPayload` for the same rows. A disabled collector is replaced by `makeNullCollector(key)` rather
than merely left unstarted, because most of them open their streams from a `ros.on('connected')`
handler in the constructor. See the constraint table above. Use the polling pattern only when no stream is available.

### Streaming collector pattern (preferred)

```js
class XyzCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros         = ros;
    this.io          = io;
    this.pollMs      = pollMs;   // retained for Settings UI / stale-threshold display only
    this.state       = state;
    this.timer       = null;     // null for fully-streamed collectors
    this.lastPayload = null;

    this._stream       = null;
    this._restarting   = false;
    this._restartTimer = null;
    this._heartbeat    = null;   // 60s re-emit so client stale timer never fires on stable networks
  }

  async start() {
    await this._loadInitial();   // one-shot /print to populate in-memory state
    this._startStream();
    this._startHeartbeat();

    // Register reconnect handlers EXACTLY ONCE — never call start() inside 'connected'.
    // Calling start() recursively doubles the listener count on every reconnect.
    this.ros.on('close', () => { this._stopStream(); this._stopHeartbeat(); });
    this.ros.on('connected', async () => {
      this._stopStream(); this._stopHeartbeat();
      await this._loadInitial();
      this._startStream(); this._startHeartbeat();
    });
  }

  _startStream() {
    if (this._stream || !this.ros.connected) return;
    this._stream = this.ros.stream(['/xyz/listen'], (err, data) => {
      if (err) {
        this.state.lastXyzErr = String(err && err.message ? err.message : err);
        this._stopStream();
        if (this.ros.connected && !this._restarting) {
          this._restarting = true;
          this._restartTimer = setTimeout(async () => {
            this._restarting = false; this._restartTimer = null;
            if (!this.ros.connected) return;
            await this._loadInitial(); this._startStream();
          }, 3000);
        }
        return;
      }
      if (data) { this._applyDelta(data); this._emit(); }
    });
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this._stream) { try { this._stream.stop(); } catch (_) {} this._stream = null; }
  }

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (this.lastPayload) this.io.emit('xyz:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }
  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  stop() {
    // Kept for settings live-update loop compatibility. Streaming collectors have
    // no poll timer — this is a safe no-op but must not throw.
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
  }
}
```

**Streaming payload convention:** set `pollMs: 0` so the client knows data is event-driven. The Settings UI shows "Event-driven" instead of a slider.

### `=interval=N` streaming pattern (for commands without a `/listen` variant)

Many RouterOS print commands accept `=interval=N` to turn a one-shot response into a continuous push stream. RouterOS sends a fresh snapshot every N seconds over the same open channel. Use `ros.stream()` with a `null` callback and subscribe to the `'data'` event on the returned `RStream` — this bypasses the built-in `onStream()` handler which debounces section frames:

```js
_startStream() {
  const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
  const stream = this.ros.stream(
    ['/some/print', `=interval=${intervalSec}`, '=.proplist=field1,field2'],
    null  // null callback — use 'data' event instead
  );
  stream.on('data', (packet) => {
    // RouterOS interval responses include a .section field on the first packet
    // of each push cycle. Filter it: require at least one real data field.
    if (!packet || !packet['field1']) return;
    this._processRow(packet);
    this._emit();
  });
  stream.on('error', (err) => {
    this._stopStream();
    // restart after 3 s if still connected
    if (this.ros.connected && !this._restarting) {
      this._restarting = true;
      this._restartTimer = setTimeout(() => {
        this._restarting = false; this._restartTimer = null;
        if (this.ros.connected) this._startStream();
      }, 3000);
    }
  });
  this._stream = stream;
}
```

Key differences from `/listen` streams:
- RouterOS pushes data at a fixed interval regardless of whether values changed — fingerprint-check before emitting to avoid redundant Socket.IO frames.
- The interval is derived from `pollMs` (e.g. `pollMs: 5000` → `=interval=5`). Minimum 1 s.
- `pollMs` is still passed through to the client payload for the Settings UI slider — it controls the stream interval, not a JS timer.
- For commands that report byte/bit counters (traffic, interfaceStatus bandwidth), values are cumulative or rate-computed by RouterOS — no manual delta calculation needed.

### Polling collector pattern (only when no stream mechanism works)

```js
class XyzCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros = ros; this.io = io; this.pollMs = pollMs;
    this.state = state; this.timer = null; this._inflight = false;
    this.lastPayload = null;
  }

  async start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) {
        this.state.lastXyzErr = String(e && e.message ? e.message : e);
      } finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    // Register handlers ONCE — never call start() inside 'connected'
    this.ros.on('close',     () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }

  async tick() {
    if (!this.ros.connected) return;
    const rows = await this.ros.write('/some/command');
    const payload = /* transform */;
    this.io.emit('xyz:update', payload);
    this.lastPayload = payload;
    this.state.lastXyzTs = Date.now(); this.state.lastXyzErr = null;
  }

  stop() { if (this.timer) { clearInterval(this.timer); this.timer = null; } }
}
module.exports = XyzCollector;
```

**Invariants (both patterns):**
- `lastPayload` is never null after first successful emit. `sendInitialState()` replays it to new browser clients.
- `state.last<n>Ts` and `state.last<n>Err` updated on every emit — feed `/healthz`.
- Stream-based collectors must restart after callback errors — transient failures must not leave the dashboard silently stale.
- All collector timers are cleared in `shutdown()` in `index.js`. New collectors must be added to `allCollectors` there.
- **Never call `start()` inside a `ros.on('connected')` handler.** Register `connected` and `close` listeners exactly once in `start()`. Calling `start()` recursively doubles the listener count on every reconnect, causing exponential listener growth and multiple concurrent collector chains.
---

## Socket.IO events

| Direction | Pattern | Examples |
|---|---|---|
| Server → all clients (broadcast) | `<domain>:update` | `traffic:update`, `system:update`, `vpn:update` |
| Server → new client (initial state) | `<domain>:list` or `<domain>:history` | `leases:list`, `ping:history`, `logs:history` |
| Server → client (status / error) | `<domain>:status` or `<domain>:error` | `ros:status`, `interfaces:error`, `wan:status` |
| Client → server | `<domain>:<verb>` | `traffic:select` |
| Settings change broadcast | `settings:pages` | emitted to all clients on every settings save |
| Router list update | `routers:update` | emitted when routers.json changes (add/edit/delete/label update) |
| Active router changed | `router:active` | `{ activeId }` — emitted on hot-swap completion and to new sockets |
| Hot-swap in progress | `router:switching` | `{ routerId, label }` — emitted at start of hot-swap so UI can show overlay |

---

## REST endpoints

Auth is applied by a single global middleware (`_authGate`) registered near the top of `src/index.js`,
ahead of every route. Only `_MODERN_PUBLIC` paths and `/vendor/*` are exempt. The **Auth** column below
therefore means:

- **none** — in the `_MODERN_PUBLIC` allowlist; reachable unauthenticated.
- **session** — any valid session, either role (`admin` or `viewer`).
- **admin** — additionally wrapped in `_requireAdmin`. In `authMode: 'none'` there is no identity, so
  every request is implicitly admin; RBAC is enforced only when a role is present.
- **admin + scope** — `_requireAdmin` plus `_scopeRouterId`, which confines the query to routers the
  caller is allowed to see.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `GET` | `/healthz` | none | Readiness probe. Returns `{ ok, version, routerConnected, startupReady, uptime, checks }` — or only `{ ok, starting }` to an unauthenticated caller |
| `GET` | `/login` | none | Serves `public/login.html` |
| `GET` | `/api/auth/status` | none | Whether auth is required, whether any user exists yet, and the current identity if there is one |
| `POST` | `/api/auth/login` | none | Login. Rate-limited by `loginLimiter`; sets the `mikrodash_sid` cookie |
| `POST` | `/api/users/setup` | none | First-run admin creation. Rate-limited by `setupLimiter`; refuses once a user exists |
| `GET` | `/api/auth/logout` | session | Destroys the session and clears the cookie |
| `PUT` | `/api/auth/me/active-router` | session | Sets the caller's **own** active router; honours their `allowedRouterIds` |
| `GET` | `/api/dashboard-layout` | session | Per-user dashboard card layout |
| `POST` | `/api/dashboard-layout` | session | Saves the layout |
| `GET` | `/api/settings` | session | Current settings, credentials masked as `••••••••` |
| `GET` | `/api/routers` | session | `{ routers, activeId }` with passwords masked |
| `GET` | `/api/localcc` | session | `{ cc, wanIp }` — country code for the WAN IP via geoip-lite |
| `POST` | `/api/settings` | admin | Updates settings (poll intervals, page visibility, alert toggles). Applies poll changes live, broadcasts `settings:pages`. Router connection fields are **not** settable here — use `/api/routers` |
| `POST` | `/api/settings/test-notification` | admin | Sends a test push through one channel. Rate-limited |
| `POST` | `/api/routers` | admin | Add a router. Body: `{ host, port, tls, tlsInsecure, username, password, defaultIf, pingTarget, label }`. Returns the saved entry, password masked |
| `PUT` | `/api/routers/:id` | admin | Edit a router. Same body; the password field is ignored when it is the `••••••••` sentinel |
| `DELETE` | `/api/routers/:id` | admin | Delete a router. `409 Conflict` if it is the active one |
| `POST` | `/api/routers/:id/activate` | admin | Hot-swap to a router. Responds immediately; the swap runs async |
| `POST` | `/api/routers/test` | admin | Test a connection without saving. Returns `{ ok, boardName?, error? }`. Rate-limited |
| `GET` | `/api/users` | admin | List user accounts (never password hashes) |
| `POST` | `/api/users` | admin | Create a user |
| `PUT` | `/api/users/:id` | admin | Edit a user (role, `allowedRouterIds`, password) |
| `DELETE` | `/api/users/:id` | admin | Delete a user |
| `GET` | `/api/reports/{ping,traffic,bandwidth,alerts,connectivity}` | admin + scope | Time-series report data from SQLite |
| `GET` | `/api/reports/{…}/export` | admin + scope | Same data as a file export (`pdfkit` for PDF) |
| `GET` | `/api/db/stats` | admin | Row counts and oldest-sample timestamps per table |
| `POST` | `/api/db/purge` | admin | Deletes time-series rows |

---

## Settings system

- Router list stored at `${DATA_DIR}/routers.json` (default: `/data/routers.json`) — managed by `src/routers.js`
- `settings.json` stored at `${DATA_DIR}/settings.json` (default: `/data/settings.json`)
- `routers.json` stored at `${DATA_DIR}/routers.json` — router list. `activeRouterId` in `settings.json` points to the active entry
- Credentials (`routerPass`, `dashPass`) are AES-256-GCM encrypted using a key derived from `DATA_SECRET`
- `settings.load()` merges stored values over `DEFAULTS`, decrypting credentials
- `settings.getPublic()` returns settings safe for the browser — credentials replaced with `••••••••`
- `settings.isMasked(v)` returns true if the value is the mask sentinel — used to ignore unchanged password fields in POST body
- `settings.save(updates)` merges updates, re-encrypts, writes to disk, updates in-memory cache
- Most settings changes take effect immediately without restart. Router connection changes (`routerHost`, `routerPort`, `routerTls`, `routerUser`, `routerPass`) require restart — the API returns `{ requiresRestart: true }`.

### Deferred: renaming the wireless keys (noted 2026-08-20, not done)

The two wireless pages were renamed to **Wifi Clients** and **Wifi Networks**, but only their
DISPLAY strings changed. The keys still read `wireless`:

| Key | Where it is persisted | Renaming it breaks |
|---|---|---|
| `wireless` (page key) | `role_pages.page` rows in SQLite | every role granting the page loses that grant — fails closed, silently, on upgrade |
| `pageWireless` | `settings.json` | the install-wide visibility toggle resets to default |
| `pollWireless` / `streamWireless` | `settings.json`, and each router's `collection.overrides` | per-router interval and stream overrides are lost |
| `wireless` (collector key) | each router's `collection.off` array in `routers.json` | a collector deliberately switched off comes back on |
| `page-wireless` (room) | nothing persisted — derived | nothing, but it must move with the page key |

A migration is perfectly possible and would need to, in one startup step: rewrite `role_pages.page`
`'wireless'` → `'wificlients'`; rename the three `settings.json` keys; and rewrite `collection.off`
and `collection.overrides` in every `routers.json` entry. It must be idempotent and must tolerate a
half-migrated file, because a rollback to an earlier binary would read the NEW keys and find none of
them — which for `pageWireless` means the page silently disappears rather than failing loudly.

Whether it is worth doing is a judgement call: the keys are invisible to users, and the only cost of
leaving them is that a reader has to know `wireless` means Wifi Clients. That is what this note is
for. `src/pages.js` and `src/collection.js` carry the same warning at the point of use.

Note the asymmetry: **Wifi Networks needs no migration at all.** Its key has been `wifi` since it
was written, so only its title changed.

---

## Shared infrastructure in index.js

**`buildSession(routerCfg)`** — creates a fresh ROS instance + all 26 collectors + connTableCache wired to the given router config. Called on startup and on every hot-swap.

**`teardownSession(session)`** — stops all collectors (timers + streams), stops the ROS connection, waits 150 ms for in-flight callbacks to settle.

**`switchRouter(newRouterId)`** — hot-swap: broadcasts offline status, saves `activeRouterId`, calls teardownSession + buildSession, re-wires ROS events. Called by `POST /api/routers/:id/activate`.

**`connTableCache`** — shared cache for `/ip/firewall/connection/print` used by both `ConnectionsCollector` and `BandwidthCollector`. TTL = 40% of the faster collector's poll interval. Invalidated on ROS `close` event.

**`sendInitialState(socket)`** — called on every new Socket.IO connection. Replays `lastPayload` from every collector, sends traffic history, fetches interface list, sends current settings and page visibility.

**`broadcastRosStatus(connected, reason)`** — tracks last known ROS connection state and broadcasts `ros:status` to all clients. Converts raw Node.js error codes (`ECONNREFUSED`, `ETIMEDOUT`, etc.) into human-readable messages.

**`startCollectors()`** — called once on the first `connected` event from `ROS`. Starts all collectors in dependency order (leases before networks, before connections). Sets `startupReady = true` on success.

---

## Security model

### What is built (invariants — never weaken these)

- **LAN-only assumption.** No HTTPS termination. Designed for trusted networks only.
- **Session auth** (`authMode: 'modern'`, the default): cookie sessions via `src/auth/sessionStore.js`, users with scrypt-hashed passwords and `admin`/`viewer` roles in `/data/users.json`. Applied to all HTTP routes and the Socket.IO engine; only `_MODERN_PUBLIC` paths and `/vendor/*` are exempt. Rate-limited to 100 req/min (skipped for `/healthz`). `authMode: 'none'` disables all auth (implicit admin) — legacy Basic Auth was removed in 0.5.45.
- **CSP:** `helmetOptions.js` enforces a strict Content Security Policy allowing only self-hosted assets. No inline scripts beyond what already exists.
- **Error sanitization:** `sanitizeErr(e)` in `index.js` strips stack traces and truncates to 200 chars. Never send raw error objects to the browser.
- **Credential masking:** `settings.getPublic()` and `routers.getPublic()` ensure passwords are never returned in API responses. `isMasked()` prevents the mask sentinel `••••••••` from being written back as a real password value.
- **AES-256-GCM encryption at rest:** All router passwords and the dashboard password are encrypted in `settings.json` and `routers.json` using a key derived from `DATA_SECRET`. The plaintext value never touches disk.
- **Socket cap:** `MAX_SOCKETS` (default 50) — excess connections are disconnected immediately.
- **`DATA_SECRET`:** Must be set to a strong random value in production. The insecure default is for local development only. Never allow this value to be changed via the Settings UI — it is the encryption key for all stored credentials.

### Security requirements for new development

Every change — new endpoint, new setting, new UI feature — must be evaluated against the following checklist before implementation. These are not optional.

#### New REST endpoints
- All new endpoints are covered by the global auth middleware; admin-mutating routes must additionally use `_requireAdmin`, and report/export routes `_scopeRouterId`. The only fully exempt endpoint is `/healthz` (health probe — and it returns only `{ok, starting}` when unauthenticated). Never add a new `_MODERN_PUBLIC` entry without explicit justification.
- Validate and sanitize all input before using it. For string fields: trim, enforce a maximum length (256 chars for general strings, 512 for passwords). For integer fields: parse with `parseInt`, validate against a `[min, max]` range, reject `NaN`. For boolean fields: compare strictly (`=== true || === 'true'`).
- Never return raw Node.js error objects, stack traces, or `e.message` directly in API responses. Use `String(e.message || e).slice(0, 200)`.
- For operations that modify state (POST/PUT/DELETE), emit the relevant Socket.IO event so all connected clients see the update — don't rely on clients polling.
- The `DELETE /api/routers/:id` endpoint demonstrates the correct pattern for a dangerous operation: check a precondition (not the active router), return a meaningful HTTP status code (409 Conflict) on violation, and broadcast the change.

#### Credentials and secrets
- Passwords from the client must always be checked with `Settings.isMasked()` / the `••••••••` sentinel before storing. If the value is the mask, leave the stored value unchanged.
- Never log credentials, even in debug mode. The ROS `debug: true` option logs raw API frames — this is controlled by `ROS_DEBUG=true` in `.env` which is opt-in and documented as verbose.
- Never add credential fields to `DEFAULTS` in `settings.js` with a non-empty default value. Empty string is the only safe default for a credential.
- Router credentials (`host`, `port`, `username`, `password`, `tls`, `defaultIf`, `pingTarget`) are managed exclusively through `routers.js` and `/api/routers`. They must never be added back to the Settings API or stored directly in `settings.json` beyond `activeRouterId`.

#### What belongs in `.env` vs Settings UI
This distinction is a security boundary, not just a UX choice:

| `.env` only | Settings UI (runtime-configurable) |
|---|---|
| `DATA_SECRET` — the encryption key | Poll intervals |
| `TRUSTED_PROXY` — Express proxy trust | Page visibility |
| `PORT` — TCP bind port | Top-N limits |
| `MAX_SOCKETS` — DoS protection | Dashboard auth credentials (username/password) |
| `ROS_DEBUG` — raw API logging | Traffic history window |
| `ROS_WRITE_TIMEOUT_MS` — API write timeout | Ping target |
| `DATA_DIR` — volume mount path | Router connection details (via Routers card) |

`TRUSTED_PROXY` must remain `.env`-only. Allowing it to be set via the UI would let a misconfigured or malicious value cause Express to trust spoofed `X-Forwarded-For` headers, bypassing the rate limiter and potentially spoofing client IPs in auth decisions. Similarly, `DATA_SECRET` must never be exposed in the UI — changing it at runtime would invalidate all encrypted credentials on disk.

#### Frontend / client-side
- All user-supplied strings rendered into HTML must be passed through `esc()` (the XSS-escaping helper defined at the top of `app.js`). No exceptions, even for data that looks numeric.
- Never construct HTML by concatenating unescaped user data. The correct pattern: `'<div>' + esc(userValue) + '</div>'`.
- Never write credentials, encryption keys, or `DATA_SECRET` into the DOM, `localStorage`, `sessionStorage`, or any JavaScript global.
- Passwords sent to the server should be in the request body (POST/PUT), never in query parameters or URL paths.
- The mask sentinel `••••••••` must be rendered as a placeholder in password fields, not as an actual value the user would need to clear before typing.

#### Dependency additions
- No new runtime dependencies without explicit approval (existing hard constraint). This applies doubly to any dependency that processes untrusted input (parsers, templating engines, serialization libraries) — these are high-risk attack surface.
- Never add client-side JavaScript from a CDN. All frontend assets must be vendored under `public/vendor/`. A compromised CDN delivering a malicious script would have full access to the dashboard and all Socket.IO data.

#### Router API connections
- All RouterOS connections must go through the `ROS` class in `src/routeros/client.js`. Never open a raw TCP connection to a router from elsewhere in the codebase.
- The `test` connection endpoint (`POST /api/routers/test`) creates a temporary ROS instance. It must always call `testRos.stop()` in all code paths — including errors and timeouts — to prevent connection leaks.
- `tlsInsecure: true` disables certificate verification. This is a user-acknowledged risk for self-signed certs on private networks. It must never be set to `true` programmatically without the user's explicit opt-in.

### Static analysis (CodeQL) — standing dismissals

Inline `// codeql[rule-id]` comments are **not** honoured by GitHub code scanning (they are
LGTM-era syntax). Alerts previously "closed" that way silently reopened on every scan. Suppress
via the API/UI instead, or fix the code. Three classes are dismissed on this repo, with reasons:

**`js/tainted-format-string` — dismissed "won't fix" (sanitised at source, and inert anyway).**
Two independent reasons:

1. *Sanitised at source.* `ROS.routerLabel` is a setter (`src/routeros/client.js`) that strips
   control characters and `%` from every label before storing it. That is the single origin of
   the `this._lbl` prefix used by all collectors, so no format specifier — and no newline that
   could forge a log line — can reach a logging call, whatever shape the call site has. The other
   tainted value, `router.host`, is validated by `VALID_HOST = /^[a-zA-Z0-9.\-]{1,253}$/`, which
   cannot contain `%` either.
2. *Inert by construction.* `_patchConsole()` in `src/index.js` wraps
   `console.{log,info,warn,error}` so **every** call receives the timestamp as argument 0. The
   caller's string therefore lands in an *argument* position, never the format-string position,
   and `util.format` performs no specifier substitution on it — verify with
   `console.log('[ts]', 'a %s b', 'X')`, which prints `a %s b X`.

The sink is a log line, not a security boundary. Sanitising the one source was preferred over
rewriting 85+ sinks; see the invariant below for when that calculus changes.

> ⚠ **Invariant:** 85+ call sites pass `this._lbl + '…'` as argument 0. If `_patchConsole` is ever
> changed to merge the caller's string into the format position (e.g. `` orig(`[${ts()}] ${args[0]}`, …) ``),
> those specifiers become live and every one of those call sites must first be converted to a
> literal format string (`console.error('%s …: %s', this._lbl, msg)`). Do not change the logger
> in isolation — that trades 11 flagged sites for 85 real ones.

**`js/resource-exhaustion` — dismissed "false positive".** The `setTimeout` delays in
`system.js` (`_scheduleResourceNext`) and `interfaceStatus.js` (`_scheduleRatesNext`) are clamped
inline at the call site to `Math.max(500, Math.min(60000, …))`, and `Settings.load()` clamps every
poll interval to its documented range beforehand. The delay cannot be unbounded or near-zero.

**`js/missing-rate-limiting` — dismissed "false positive".** `authLimiter` (100 req/min) is
applied globally by the `app.use()` block near the top of `src/index.js`, ahead of every route
except `/healthz`. CodeQL cannot trace the limiter through that wrapper closure. Route-level
duplicates were removed deliberately — they consumed two of the 100/min budget per request.

---

## Testing conventions

**Runner:** `node --test` · **Command:** `npm test` · **No extra test deps**

### Fake object shapes (copy-paste ready)

```js
// Fake ROS — polling collector
const ros = { connected: true, on() {}, write: async () => [/* rows */] };

// Fake ROS — streaming collector
let streamHandler;
const ros = {
  connected: true, on() {},
  stream(words, cb) { streamHandler = cb; return { stop() {} }; },
};

// Fake IO
const emitted = [];
const io = { emit(ev, data) { emitted.push({ ev, data }); } };

// Deterministic timing
const orig = Date.now;
Date.now = () => fixedNow;
try { await collector.tick(); } finally { Date.now = orig; }
```

### Coverage checklist for new collectors/features

- [ ] Happy path → correct payload shape and values
- [ ] Empty/null RouterOS response → no crash, sensible defaults (0, null, [])
- [ ] Malformed field values → clamped to 0 or fallback, not NaN/undefined
- [ ] `state.last<n>Ts` updated on success; `state.last<n>Err` set on failure
- [ ] Rate-based: counter reset → 0 rate (never negative); stale `prev` entries pruned
- [ ] Stream-based: callback error → stream restarts, existing state preserved
- [ ] Inflight guard: second tick skipped while first is in progress
- [ ] `stop()`: timer cleared correctly

---

## Environment variables

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `3081` | HTTP/WS server port |
| `MAX_SOCKETS` | `50` | Max concurrent WebSocket clients |
| `TRUSTED_PROXY` | _(unset)_ | Express trust proxy value |
| `DATA_DIR` | `/data` | Settings persistence directory |
| `DATA_SECRET` | _(insecure default)_ | **Set this in production** |
| `ROUTER_HOST` | `192.168.88.1` | RouterOS hostname or IP |
| `ROUTER_PORT` | `8729` | 8729 = TLS, 8728 = plain |
| `ROUTER_TLS` | `true` | Enable TLS on API connection |
| `ROUTER_TLS_INSECURE` | `false` | Skip certificate verification |
| `ROUTER_USER` | `admin` | RouterOS API username |
| `ROUTER_PASS` | _(empty)_ | RouterOS API password |
| `DEFAULT_IF` | `ether1` | Default WAN interface name |
| `PING_TARGET` | `1.1.1.1` | ICMP ping destination |
| `ROS_WRITE_TIMEOUT_MS` | `30000` | RouterOS API write timeout (ms) |
| `ROS_DEBUG` | `false` | RouterOS API debug logging |
| `CONNS_POLL_MS` | `5000` | Connections stream interval (ms) — controls `=interval=N` on the connection-table stream |
| `TALKERS_POLL_MS` | `3000` | Top-talkers stream interval (ms) |
| `BANDWIDTH_POLL_MS` | `5000` | Bandwidth poll interval (ms) — still polled, shares connTableCache |
| `SYSTEM_POLL_MS` | `2000` | System resource stream interval (ms) |
| `WIRELESS_POLL_MS` | `30000` | Wireless poll interval (ms) — still polled |
| `VPN_POLL_MS` | `10000` | VPN counter poll interval (ms) — stream handles state changes; poll fetches byte counters |
| `FIREWALL_POLL_MS` | `5000` | Firewall counter poll interval (ms) — streams handle rule changes; poll fetches packet deltas |
| `IFSTATUS_POLL_MS` | `5000` | Interface status stream interval (ms) — controls all three `=interval=N` streams |
| `IFACES_POLL_MS` | `60000` | Interface list refresh interval (ms) — utility list used by traffic subscriber |
| `PING_POLL_MS` | `5000` | Ping stream interval (ms) — controls `=interval=N` on the ping stream |
| `ARP_POLL_MS` | `30000` | Retained for Settings UI display only — ARP collector is stream-based (`/ip/arp/listen`), not polled |
| `DHCP_POLL_MS` | `600000` | DHCP networks collector interval (ms) — slow poll, default 10 min |
| `ROUTING_POLL_MS` | `10000` | Retained for Settings UI display only — routing collector is event-driven (two concurrent `/listen` streams), not polled |
| `TOP_N` | `5` | Top-N limit for connections page (sources, destinations, ports, countries) |
| `TOP_TALKERS_N` | `5` | Top-N limit for talkers card |
| `FIREWALL_TOP_N` | `15` | Max firewall rules shown in the firewall card |
| `VPN_DASH_TOP_N` | `5` | Max WireGuard peers shown on dashboard card |
| `MAX_CONNS` | `20000` | Maximum connection-table rows processed per tick |
| `HISTORY_MINUTES` | `30` | Traffic and ping ring-buffer history window (minutes) |
| `ALERT_CPU_THRESHOLD` | `90` | CPU % above which a spike notification fires |
| `ALERT_PING_LOSS` | `100` | Ping loss % at which a loss notification fires (100 = only fire on 100% loss) |

---

## Run instructions

```bash
# First time (or after npm install)
node patch-routeros.js

# Development
npm install
npm test
node src/index.js

# Production
docker compose up -d --build
```

The app starts and serves the UI immediately. Collectors start only after the first successful RouterOS connection. The browser shows a connection banner until RouterOS is reachable — this is expected behaviour, not a bug.
