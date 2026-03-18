# AI_CONTEXT.md

This file gives AI coding assistants (Claude, Copilot, Cursor, etc.) immediate grounding in the MikroDash codebase. Read this before suggesting any changes.

---

## What MikroDash is

MikroDash is a **real-time MikroTik RouterOS v7 dashboard**. It connects directly to the RouterOS binary API over a persistent TCP connection, streams live network data to a browser via Socket.IO, and serves a static single-page UI over Express. There are no page refreshes, no polling from the browser, no external agents, and no build step.

**Target user:** Network operator/admin on a trusted LAN.  
**Not for:** Public internet exposure — there is no HTTPS termination or role-based access control built in.

---

## Hard constraints — do not violate these

| Constraint | Detail |
|---|---|
| No build step | Plain CommonJS (`require`/`module.exports`) throughout. No TypeScript, Babel, Webpack, Vite, or any transpiler. |
| No new test frameworks | Tests use `node:test` + `node:assert/strict` only. No Jest, Mocha, Vitest, or other deps. |
| No CDN dependencies | All frontend assets are vendored under `public/vendor/`. Never add a `<script src="https://...">` tag. |
| No new runtime deps without approval | The dependency list in `package.json` is intentional and minimal. |
| Collector pattern must be followed | Every new data collector must implement the contract described below. |
| Streaming-first architecture | **Prefer `/listen` streams over polling wherever RouterOS supports them.** Polling is only acceptable when no stream endpoint exists (e.g. `/tool/ping`) or the stream is demonstrably too noisy without benefit (rejected case: `/routing/bgp/session/listen` was initially rejected as "noisy" but was successfully streamed with keepalive-fingerprint suppression). When converting a collector to streaming, set `pollMs: 0` in the payload and show "Event-driven" in the Settings UI instead of a slider. |
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
├── settings.js                # Load/save settings.json with AES-256-GCM credential encryption.
│                              #   Exports: load(), save(), getPublic(), isMasked(), DEFAULTS
├── health.js                  # computeHealthStatus() — logic for /healthz endpoint
├── shutdown.js                # scheduleForcedShutdownTimer() — fallback exit after 5 s
├── auth/
│   └── basicAuth.js           # createBasicAuthMiddleware() — HTTP Basic Auth, also applied to Socket.IO engine
├── collectors/                # One file per RouterOS data domain (see Collector Pattern below)
│   ├── traffic.js             # RX/TX Mbps per interface, 1 s polling, ring-buffer history
│   ├── system.js              # CPU/RAM/HDD/temp/uptime/version/update-check
│   ├── connections.js         # Firewall connection table: protocol counts, top sources/destinations,
│   │                          #   geo enrichment, port aggregates, IPv6, truncation metadata
│   ├── bandwidth.js           # Per-connection bandwidth (Mbps), ASN/org badges, interface+proto filters
│   ├── talkers.js             # Top-N devices by MAC with TX/RX rate calculation
│   ├── dhcpLeases.js          # DHCP lease stream + initial load; name resolution (comment > hostname)
│   ├── dhcpNetworks.js        # LAN CIDRs, WAN IP from interface addresses, lease counts per network
│   ├── arp.js                 # ARP table snapshot; bidirectional IP↔MAC lookup
│   ├── wireless.js            # Wireless clients: band detection, signal, SSID, DHCP/ARP enrichment.
│   │                          #   ⚠ No =.proplist= on registration-table calls — see RouterOS quirks below
│   ├── vpn.js                 # WireGuard peers: connected/idle state, TX/RX rates, stale pruning
│   ├── firewall.js            # Filter/NAT/mangle rules with delta packet counts between polls
│   ├── interfaceStatus.js     # All interfaces: running, disabled, IPs, RX/TX Mbps, cumulative bytes
│   ├── ping.js                # ICMP ping RTT + loss%, ring-buffer history, fallback averaging
│   ├── routing.js             # Route table (/ip/route/listen stream) + BGP sessions (/routing/bgp/session/listen stream)
│   └── logs.js                # RouterOS log stream, severity classification, bounded history buffer
├── routeros/
│   ├── client.js              # ROS class (extends EventEmitter): connectLoop() with exponential backoff,
│   │                          #   write(), stream(), waitUntilConnected(). Emits: connected, close, error
│   └── patchVerification.js   # verifyRouterOSPatchMarkers() — exits process if patch is missing
├── security/
│   └── helmetOptions.js       # buildHelmetOptions() — CSP with self-hosted asset allowlist, HSTS
└── util/
    ├── ringbuffer.js          # RingBuffer(size): push(item), toArray(), get(i)
    ├── ip.js                  # isPrivateIP(), cidrContains(), normalizeIP() — wraps ipaddr.js
    └── asnLookup.js           # lookupASN(ip) → { asn, org } using geoip-lite data

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

test/
├── collector-data-transforms.test.js          # tick() → emitted payload shape and value correctness
├── collector-lifecycle.test.js                # start(), timer setup/teardown, stream, reconnect
├── production-resilience-regressions.test.js  # Regression tests for confirmed production bugs
└── smoke-fixes.test.js                        # Smoke-level sanity checks

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

**Semantic version:** `major.minor.patch` in `package.json`. Bump patch for bug fixes; minor for new features or behaviour changes; major for breaking changes.

**How to write a CHANGELOG.md entry:**

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

**How to update `package.json`:** change only the `"version"` field. Nothing else.


---

## Collector delivery model

| Collector | Delivery | RouterOS endpoint(s) | Notes |
|---|---|---|---|
| `traffic.js` | Poll 1 s | `/interface/monitor-traffic` | No stream endpoint; idle-gated when no browser clients |
| `system.js` | Poll | `/system/resource/print` | Update-check sub-interval (5 min) |
| `connections.js` | Poll | `/ip/firewall/connection/print` | Shared `connTableCache` with bandwidth |
| `bandwidth.js` | Poll | `/ip/firewall/connection/print` | Shared `connTableCache` with connections |
| `talkers.js` | Poll | `/ip/kid-control/device/print` | Backs off when Kid Control unavailable |
| `dhcpLeases.js` | **Stream** | `/ip/dhcp-server/lease/listen` | Initial `/print` on connect |
| `dhcpNetworks.js` | Poll | `/ip/dhcp-server/network/print` | Slow poll (default 5 min) |
| `arp.js` | **Stream** | `/ip/arp/listen` | Initial `/print` on connect |
| `wireless.js` | Poll | `/interface/wifi/registration-table/print` | Probes both wifi and legacy wireless APIs |
| `vpn.js` | **Stream** | `/interface/wireguard/peers/listen` | Heartbeat every 60 s |
| `firewall.js` | **Stream** | `/ip/firewall/{filter,nat,mangle}/listen` | Three concurrent streams |
| `interfaceStatus.js` | **Stream** + Poll | `/interface/listen` + `/interface/print` | Stream for state; poll for byte counters |
| `ping.js` | Poll | `/tool/ping` | No stream endpoint exists |
| `routing.js` | **Stream** | `/ip/route/listen` + `/routing/bgp/session/listen` | BGP keepalives fingerprint-suppressed |
| `logs.js` | **Stream** | `/log/listen` | Bounded history buffer (500 entries) |

**Rule:** always prefer streaming. Add a new collector as streaming unless the RouterOS endpoint genuinely does not support `/listen` (e.g. `/tool/ping`).

---

## Known RouterOS API quirks

### `/ip/route/print` — `.flags` omitted for default-state routes

RouterOS v7 on some firmware builds omits the `.flags` field for routes in their default (active) state, treating active+static as unremarkable. Disabled routes always receive `.flags` because disabled is non-default. When writing route-related code, always include a fallback type-inference path: if no type flag is set and the gateway is a real IP address (matches an IPv4/IPv6 pattern, not an interface name like `'bridge'`), infer `static=true`. `/ip/route/listen` stream events always carry the full row so this only affects the initial `/print` load.

### `=.proplist=` on registration-table calls — can filter rows

On RouterOS v7 new wifi package, including unknown or absent field names in `=.proplist=` for `/interface/wifi/registration-table/print` can cause RouterOS to **filter rows** rather than simply omitting those fields per row. For example, requesting `'signal'` (which is `'signal-strength'` in the new API) may return only clients where that field is non-empty — resulting in only 1 of N clients being returned. **Do not use `=.proplist=` on wireless registration-table calls.** The table is small enough that the optimisation is not worth the risk.

### `!empty` reply — RouterOS 7.18+

RouterOS 7.18+ sends `!empty` when a command returns zero results. The `node-routeros` library throws `UNKNOWNREPLY` on this. `patch-routeros.js` patches `Channel.js` to treat `!empty` as an empty done (resolves to `[]`). This patch must be applied once after every `npm install` — the `Dockerfile` runs it automatically.

### UNREGISTEREDTAG crash — node-routeros

When RouterOS sends a packet for a tag that `node-routeros` has already cleaned up (trailing packet after `!done`, or delayed response after a stream is stopped), the library throws `UNREGISTEREDTAG` synchronously inside a socket data event — uncatchable by user code. `patch-routeros.js` patches `Receiver.js` to log and discard these packets instead.

---

## Collector pattern

**Streaming-first:** always prefer a `/listen` stream over a poll interval when the RouterOS endpoint supports it. See the constraint table above. Use the polling pattern only when no stream is available.

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

### Polling collector pattern (only when no stream endpoint exists)

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

---

## REST endpoints

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `GET` | `/healthz` | none | Readiness probe. Returns `{ ok, version, routerConnected, startupReady, uptime, checks }` |
| `GET` | `/api/settings` | Basic Auth | Returns current settings with credentials masked as `••••••••` |
| `POST` | `/api/settings` | Basic Auth | Updates settings. Applies poll changes live. Broadcasts `settings:pages`. Returns `{ ok, requiresRestart }` |
| `GET` | `/api/localcc` | Basic Auth | Returns `{ cc, wanIp }` — country code for WAN IP via geoip-lite |

---

## Settings system

- Stored at `${DATA_DIR}/settings.json` (default: `/data/settings.json`)
- Credentials (`routerPass`, `dashPass`) are AES-256-GCM encrypted using a key derived from `DATA_SECRET`
- `settings.load()` merges stored values over `DEFAULTS`, decrypting credentials
- `settings.getPublic()` returns settings safe for the browser — credentials replaced with `••••••••`
- `settings.isMasked(v)` returns true if the value is the mask sentinel — used to ignore unchanged password fields in POST body
- `settings.save(updates)` merges updates, re-encrypts, writes to disk, updates in-memory cache
- Most settings changes take effect immediately without restart. Router connection changes (`routerHost`, `routerPort`, `routerTls`, `routerUser`, `routerPass`) require restart — the API returns `{ requiresRestart: true }`.

---

## Shared infrastructure in index.js

**`connTableCache`** — shared cache for `/ip/firewall/connection/print` used by both `ConnectionsCollector` and `BandwidthCollector`. TTL = 40% of the faster collector's poll interval. Invalidated on ROS `close` event.

**`sendInitialState(socket)`** — called on every new Socket.IO connection. Replays `lastPayload` from every collector, sends traffic history, fetches interface list, sends current settings and page visibility.

**`broadcastRosStatus(connected, reason)`** — tracks last known ROS connection state and broadcasts `ros:status` to all clients. Converts raw Node.js error codes (`ECONNREFUSED`, `ETIMEDOUT`, etc.) into human-readable messages.

**`startCollectors()`** — called once on the first `connected` event from `ROS`. Starts all collectors in dependency order (leases before networks, before connections). Sets `startupReady = true` on success.

---

## Security model

- **LAN-only assumption.** No HTTPS termination. No role separation. Designed for trusted networks only.
- **Basic Auth** (optional): enabled when `BASIC_AUTH_USER` + `BASIC_AUTH_PASS` are set. Applied to all HTTP routes and the Socket.IO engine. Rate-limited to 100 req/min (skipped for `/healthz`).
- **CSP:** `helmetOptions.js` enforces a strict Content Security Policy allowing only self-hosted assets. No inline scripts beyond what already exists.
- **Error sanitization:** `sanitizeErr(e)` in `index.js` strips stack traces and truncates to 200 chars. Never send raw error objects to the browser.
- **Credential masking:** `settings.getPublic()` and `isMasked()` ensure passwords are never returned in API responses or accidentally saved back unchanged.
- **Socket cap:** `MAX_SOCKETS` (default 50) — excess connections are disconnected immediately.
- **`DATA_SECRET`:** Must be set to a strong random value in production. The insecure default is only for local development.

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
| `BASIC_AUTH_USER` | _(empty)_ | Dashboard Basic Auth username |
| `BASIC_AUTH_PASS` | _(empty)_ | Dashboard Basic Auth password |
| `PING_TARGET` | `1.1.1.1` | ICMP ping destination |
| `ROS_WRITE_TIMEOUT_MS` | `30000` | RouterOS API write timeout (ms) |
| `ROS_DEBUG` | `false` | RouterOS API debug logging |
| `CONNS_POLL_MS` | `3000` | Connections collector interval |
| `TALKERS_POLL_MS` | `3000` | Top talkers collector interval |
| `BANDWIDTH_POLL_MS` | `3000` | Bandwidth collector interval |
| `SYSTEM_POLL_MS` | `3000` | System collector interval |
| `WIRELESS_POLL_MS` | `5000` | Wireless collector interval |
| `VPN_POLL_MS` | `10000` | VPN collector interval |
| `FIREWALL_POLL_MS` | `10000` | Firewall collector interval |
| `IFSTATUS_POLL_MS` | `5000` | Interface status collector interval |
| `PING_POLL_MS` | `10000` | Ping collector interval |
| `ARP_POLL_MS` | `30000` | ARP collector interval |
| `DHCP_POLL_MS` | `300000` | DHCP networks collector interval |
| `ROUTING_POLL_MS` | `10000` | Routing collector interval |

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
