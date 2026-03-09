# Changelog

All notable changes to MikroDash will be documented in this file.

## Deep Code Review Hardening Pass

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
