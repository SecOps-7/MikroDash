# Changelog

All notable changes to MikroDash will be documented in this file.


## [0.5.1] — RouterOS Offline Resilience & UI Polish

### Bug Fixes

- **`package.json` version now matches app version** — `package.json` was
  still reporting `0.4.8` while `app.js`, the changelog, and `/healthz` all
  reported `0.5.0`; version bumped to `0.5.1` to resolve the mismatch
  (`package.json`)
- **`.log-line` CSS rule added** — `buildLogHtml()` wraps each entry in
  `<div class="log-line">` but no matching rule existed; added `.log-line`
  with `display:block`, `padding`, and a subtle hover highlight
  (`public/index.html`)
- **Log colours now visible in light mode** — `.log-error`, `.log-warning`,
  `.log-debug`, `.log-info` and all topic classes (`.log-dhcp`,
  `.log-wireless`, `.log-firewall`, `.log-system`) had no
  `html[data-theme="light"]` overrides, making several severity levels
  nearly invisible on a light background; 12 light-mode rules added
  (`public/index.html`)

### Features

- **RouterOS offline banner** — a yellow warning banner now appears at the
  top of the dashboard whenever RouterOS is not reachable, with a plain-
  English reason (e.g. "Connection refused — is RouterOS reachable at
  192.168.88.1?"). The banner dismisses automatically when the connection
  is restored. Distinct from the red Socket.IO reconnect banner which fires
  only when the browser loses its connection to the MikroDash server itself
  (`public/index.html`, `public/app.js`, `src/index.js`)
- **Container no longer blocks on RouterOS availability** — the startup
  sequence previously called `waitUntilConnected(60000)` in an async IIFE,
  meaning the HTTP server started but collectors never ran if RouterOS was
  unreachable at boot. The startup is now event-driven: collectors start the
  moment the `connected` event fires (whether that is immediately or minutes
  later), and the container stays healthy the entire time. The `ros:status`
  event is broadcast to all connected browser clients on every connection
  state change so the UI always reflects reality (`src/index.js`)
- **Human-readable RouterOS error messages** — raw Node.js network errors
  (`ECONNREFUSED`, `ETIMEDOUT`, `ENOTFOUND`, `ECONNRESET`) and RouterOS
  errors (TLS certificate, authentication) are translated to clear
  actionable messages before being sent to the client (`src/index.js`)


## [0.5.0] — UI Fixes & Security Hardening

### Security

- **Closed `traffic:select` whitelist race** — `_normalizeIfName()` in
  `TrafficCollector` previously allowed `traffic:select` events through when
  `availableIfs` was empty (i.e. before `sendInitialState()` had completed),
  bypassing the interface whitelist entirely. The guard is now inverted: an
  empty whitelist is treated as "not ready" and the event is rejected with a
  console warning rather than passed to the RouterOS API
  (`src/collectors/traffic.js`)

### Bug Fixes

- **Log viewer entries now render on separate lines** — `buildLogHtml()`
  was returning bare `<span>` elements joined with `\n`. Inside a `<div>`
  container, `\n` is collapsed whitespace and produces no visual line break.
  Each entry is now wrapped in a `<div class="log-line">` block element so
  every router log entry occupies its own line. The `flushLogs()` join
  separator is also cleaned up from `'\n'` to `''`
  (`public/app.js`)
- **Notification bell icon now shown on page load** — `updateNotifBtn()` was
  only ever called after an async `Notification.requestPermission()` callback,
  leaving the hardcoded crossed-bell SVG from `index.html` in place for the
  entire session on browsers where permission had already been granted. A
  startup IIFE now reads `Notification.permission` synchronously and calls
  `updateNotifBtn()` immediately so the correct icon is rendered before the
  user sees the topbar (`public/app.js`)
- **SVG network diagram boxes now respect light mode** — `.nd-node`,
  `.nd-count`, `.nd-label`, `.nd-wan-ip`, `.nd-line`, and `.nd-router-bg`
  had hardcoded dark RGBA fill/stroke values with no light-mode override,
  causing the Wired, Wireless, and WAN boxes to remain dark when switching
  themes. Seven `html[data-theme="light"]` CSS rules now override all
  affected SVG classes with light-appropriate colours (`public/index.html`)

### Features

- **`interfaces:error` Socket.IO event** — when `fetchInterfaces()` fails
  during `sendInitialState()`, the server now emits `interfaces:error` with
  the reason string instead of silently resolving to an empty list via
  `Promise.allSettled()`. The client handles this event by showing an
  explicit "Interface list unavailable" placeholder in the interface dropdown
  and logging the reason to the browser console, replacing a silent empty
  dropdown with actionable feedback (`src/index.js`, `public/app.js`)

## [0.4.9] — Deep Code Review Hardening Pass

### Security

- **HMAC-based timing-safe credential comparison** — authentication now
  compares HMAC-SHA256 digests of fixed length via `crypto.timingSafeEqual`,
  eliminating the timing side-channel that leaked credential length through
  the old length-check fast path (`446f2d2`)
- **Dropped unconditional X-Forwarded-For trust** — `getClientIp()` no longer
  reads `X-Forwarded-For` by default, preventing attackers from spoofing their
  IP to bypass rate limiting (`446f2d2`)
- **Sanitized /healthz error strings** — error messages are now truncated to
  200 characters with stack traces stripped before being exposed in the health
  endpoint, preventing internal implementation details from leaking (`faba151`)

### Features

- **Opt-in `TRUSTED_PROXY` env var** — when set to a proxy IP (e.g.
  `127.0.0.1`), Express `trust proxy` is enabled and `req.ip` correctly
  resolves the real client address from `X-Forwarded-For`. Disabled by default
  for safe out-of-the-box behaviour (`8965a31`)
- **Incremental ping updates** — server now emits lightweight `ping:update`
  events with only the latest data point; full history is sent once via
  `ping:history` on client connect, reducing per-tick payload size (`acb8001`)

### Bug Fixes

- **Unified version strings** — `APP_VERSION` is now sourced from
  `package.json` in one place, fixing inconsistencies between the healthz
  endpoint and startup log messages (`157986e`)
- **Removed redundant dynamic require** — `geoip-lite` was being required
  twice (module-level and inside a function); consolidated to module-level
  only (`157986e`)
- **Fixed /api/localcc polling storm** — client-side code moved the
  `fetch('/api/localcc')` call from inside the `conn:update` handler (fired
  every 3 s) to a once-per-connect pattern (`4b9e862`)
- **Decoupled wanIface from process.env** — `DhcpNetworksCollector` now
  receives `wanIface` as a constructor parameter instead of reading
  `process.env.WAN_IFACE` directly, improving testability (`4b9e862`)
- **Pruned stale keys in firewall, VPN, and talkers prev-maps** — all three
  Maps grew unboundedly as rules/peers/devices were added and removed; each
  collector now tracks seen keys per tick and deletes stale entries
  (`010bb46`)
- **Error state consistency** — all 7 collectors now set `lastXxxErr = null`
  on success instead of `delete`, keeping the state object shape stable and
  matching the initial values in `index.js` (`6df3e92`)
- **Per-interface traffic error flag** — replaced the single boolean
  `_hadTrafficErr` with a per-interface `Set`, so an error on one interface
  no longer suppresses first-error logging on others (`6df3e92`)
- **Extracted PING_COUNT constant** — the magic number `3` used in both the
  RouterOS ping command and the loss-calculation fallback is now a named
  constant (`6df3e92`)
- **DOM-based log truncation** — replaced `innerHTML.split('\n')` with
  `childNodes` counting and `removeChild`, avoiding O(n) re-serialization
  of the log panel on every new log line (`faba151`)

### Performance

- **Single-pass connections loop** — merged three separate iterations over
  the connections array (src/dst counts, protocol counts, country/port counts)
  into one loop (`acb8001`)
- **ARP reverse index** — `arp.js` now maintains a `byMAC` Map updated
  atomically in `tick()`, making `getByMAC()` O(1) instead of O(n)
  (`acb8001`)

### Earlier Hardening (prior commits)

- Hardened dashboard runtime paths and general polish (`200c1d9`, `8ac0703`,
  `5009ac9`)

## [0.4.8] — 2026-03-06

Initial public release of MikroDash.

- Real-time RouterOS v7 dashboard with Socket.IO live updates
- Traffic, connections, DHCP leases, ARP table, firewall, VPN, wireless,
  system resource, and ping collectors
- Top talkers (Kid Control) monitoring
- GeoIP connection mapping with world map visualisation
- Log viewer with severity filtering and search
- Per-interface traffic charts with configurable history window
- Optional HTTP Basic Auth with rate-limiting
- Docker and docker-compose deployment support
- `.env`-based configuration for all settings
- Removed accidentally committed `.env` file (`6a85d96`)
- Updated README with setup instructions and screenshots (`2ee0134`,
  `1460b3c`, `e5ec193`)
