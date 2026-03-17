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
│   ├── wireless.js            # Wireless clients: band detection, signal, SSID, DHCP/ARP enrichment
│   ├── vpn.js                 # WireGuard peers: connected/idle state, TX/RX rates, stale pruning
│   ├── firewall.js            # Filter/NAT/mangle rules with delta packet counts between polls
│   ├── interfaceStatus.js     # All interfaces: running, disabled, IPs, RX/TX Mbps, cumulative bytes
│   ├── ping.js                # ICMP ping RTT + loss%, ring-buffer history, fallback averaging
│   ├── routing.js             # Static/dynamic routes, BGP peers with state + prefix trend
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

## Collector pattern

Every data collector must follow this contract exactly. Deviations will cause bugs in `sendInitialState()`, `/healthz`, and graceful shutdown.

```js
class XyzCollector {
  constructor({ ros, io, pollMs, state, /* ...domain deps */ }) {
    this.ros         = ros;         // ROS client instance
    this.io          = io;          // Socket.IO server instance
    this.pollMs      = pollMs;      // poll interval in ms
    this.state       = state;       // shared state object from index.js
    this.timer       = null;        // setInterval handle — checked by shutdown()
    this._inflight   = false;       // prevents overlapping tick() calls
    this.lastPayload = null;        // replayed to new sockets in sendInitialState()
  }

  async start() {
    await this.tick();              // run immediately on start
    this.timer = setInterval(async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (_) {} finally { this._inflight = false; }
    }, this.pollMs);

    // Stream-based collectors call this._startStream() here instead of / alongside setInterval

    this.ros.on('connected', () => { this.stop(); this.start(); }); // restart on reconnect
    this.ros.on('close',     () => this.stop());
  }

  async tick() {
    try {
      const rows = await this.ros.write('/some/command', ['=param=value']);
      const payload = /* transform rows */;
      this.io.emit('xyz:update', payload);
      this.lastPayload = payload;
      this.state.lastXyzTs  = Date.now();
      this.state.lastXyzErr = null;
    } catch (e) {
      this.state.lastXyzErr = e;
    }
  }

  stop() {
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
  }
}
module.exports = XyzCollector;
```

**Invariants:**
- `lastPayload` is never null after a successful tick. `sendInitialState()` in `index.js` uses it to replay state to newly connected browser clients.
- `state.last<n>Ts` (timestamp) and `state.last<n>Err` (error or null) must be updated on every tick — these feed `/healthz`.
- `tick()` never throws. Errors are caught internally and stored in state.
- Stream-based collectors (`logs.js`, `dhcpLeases.js`) must restart their stream after callback errors — transient failures must not leave the dashboard silently stale.
- All collector timers are cleared in `shutdown()` in `index.js`. New collectors must be added to `allCollectors` there.

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
