# Changelog

All notable changes to MikroDash will be documented in this file.

## [0.5.3] — UI & Accuracy Improvements

### Features

- **Per-band wireless client counts** — the Wireless Clients card header now
  shows live counts per band (`2.4GHz: N`, `5GHz: N`, and `6GHz: N` when
  present), separated from the total count badge by a thin vertical divider
  (`public/index.html`, `public/app.js`)
- **ASN / org lookup on Connections page** — destination IPs are resolved to
  organisation names via a curated CIDR→org table with a 5000-entry LRU cache,
  displayed as a label beneath each IP:port entry; no new runtime dependencies
  (`src/util/asnLookup.js`, `src/collectors/connections.js`,
  `public/index.html`, `public/app.js`)
- **Service badge colour coding** — destinations are grouped into seven
  categories (cdn, cloud, social, streaming, messaging, video, dns) with
  distinct coloured inline badges in Top Destinations, org sub-rows in Top
  Countries, and IP tooltips on hover (`public/index.html`, `public/app.js`,
  `src/util/asnLookup.js`, `src/collectors/connections.js`)
- **Connection Flow Sankey diagram** — a pure-SVG source→destination flow
  diagram rendered at the bottom of the Connections page, driven by
  `conn:update` data, with proportional ribbon widths, category colours, and
  resize-awareness; no external library (`public/index.html`, `public/app.js`)
- **Log count indicators** — four clickable severity pill badges (`N errors`,
  `N warnings`, `N info`, `N debug`) in the Logs card header tally the buffer
  by severity, toggle the severity filter on click, and remain visible at zero
  count (`public/index.html`, `public/app.js`)

### Bug Fixes

- **Wireless band detection uses RouterOS registration table directly** —
  the previous heuristic based on interface name patterns and tx-rate strings
  (`MHT-xxx`, `HE-MCS`) incorrectly reported 5GHz for some 2.4GHz clients.
  The collector now reads the `band` field directly from each registration
  table entry — the same authoritative source Winbox displays in its Band
  column (`src/collectors/wireless.js`)
- **Ping target label updates dynamically** — `<span id="pingTargetLabel">`
  is now updated from `data.target` in both `ping:history` and `ping:update`
  handlers (`public/app.js`)
- **Wired client count uses interface type allowlist** — count now derives
  from `type === 'ether'` entries in `ifstatus:update` rather than the talkers
  list, avoiding false positives (`public/app.js`)

### UI

- **Connections page layout reorganised** — Top Countries now spans the full
  page width; Connection Flow and Top Ports share the row below it at a
  `2fr 1fr` split (`public/index.html`)
- **Sankey diagram taller** — minimum height raised from 180px to 260px and
  per-source row height increased from 24px to 36px (`public/app.js`)
- **Service badge colours fully distinct** — `svc-video` changed from blue
  (conflicting with `svc-cdn`) to amber; `svc-dns` changed from green
  (conflicting with `svc-messaging`) to teal; Sankey ribbon colours updated
  to match (`public/index.html`, `public/app.js`)
- **Log count badges more visible** — background and text opacities raised
  across all four severity levels; debug badge no longer uses the near-invisible
  `--text-muted` colour (`public/index.html`)
- **Country list sparklines moved to top-right** — the per-country sparkline
  is repositioned to the top-right of the country name row using a flex
  space-between wrapper (`public/app.js`)
- **Nav logo no longer jumps on expand/collapse** — logo previously switched
  between `justify-content:center` and `flex-start` mid-transition; it now
  sits permanently left-aligned with `padding:0 14px`, matching the nav icons,
  with no animated properties (`public/index.html`)
- **Traffic card width on mobile fixed** — removed a redundant inner wrapper
  div that caused the Traffic card to render slightly narrower than sibling
  cards on mobile viewports (`public/index.html`)
- **Mobile topbar decluttered** — clock and router tag spans hidden at ≤767px
  via `.topbar-mobile-hide` (`public/index.html`)
- **Mobile dashboard scaling** — `.page-view` padding reduced on small screens;
  grid gaps tightened; connections card set to `height:auto` on narrow
  viewports; `connMapList` grid uses `minmax(min(220px,100%),1fr)` to prevent
  horizontal overflow (`public/index.html`)

## [0.5.2] — UI Improvements & Bug Fixes

### Features

- **Live interface traffic rates on Interfaces page** — each interface tile now
  displays real-time RX and TX rates with colour-coded bar indicators (blue for
  RX, green for TX) that scale relative to the session peak. Rates are derived
  from cumulative byte counter deltas between polls, since
  `rx-bits-per-second` is not available from `/interface/print`
  (`src/collectors/interfaceStatus.js`, `public/index.html`, `public/app.js`)
- **Log persistence across page refreshes** — the server now maintains a
  ring buffer of the last 500 log entries (configurable via `LOG_HISTORY_SIZE`)
  and replays them to each new socket connection, so the Logs page is no longer
  blank after a refresh (`src/collectors/logs.js`, `src/index.js`,
  `public/app.js`)
- **Self-hosted fonts** — JetBrains Mono and Syne are now bundled as woff2
  files under `public/vendor/fonts/`, eliminating the last remaining external
  requests to Google Fonts and completing the fully air-gapped deployment story
  (`public/vendor/fonts/`, `public/index.html`)

### UI

- **Item count badges on Interfaces and VPN pages** — the Interfaces card and
  the WireGuard Peers card now show a count badge matching the style used on
  Wireless Clients and DHCP (`public/index.html`, `public/app.js`)
- **Consistent card badge styling across all pages** — all five card badges
  (Wireless Clients, DHCP, WireGuard dashboard, WireGuard Peers, Interfaces)
  now use a shared `.card-badge` CSS class with CSS variable-based colours that
  are legible in both dark and light mode, replacing the Tabler `bg-*` classes
  that were invisible in light mode (`public/index.html`, `public/app.js`)

### Bug Fixes

- **Notification bell invisible in light mode** — the bell SVG had an inline
  `stroke:var(--text-muted)` overriding `currentColor`, a blanket `opacity:.85`
  on the button, and no explicit `width`/`height` on dynamically injected SVGs,
  causing it to be nearly invisible or zero-sized. All three issues resolved
  (`public/index.html`, `public/app.js`)
- **ROS and reconnect banners stacking** — when the router disconnected,
  both the amber RouterOS banner and the red Socket.IO reconnect banner could
  appear simultaneously. The reconnect banner now suppresses the ROS banner
  while active, and restores it on reconnect only if the router is still
  offline (`public/app.js`)
- **VPN peer dot hidden on long peer names** — the status dot in WireGuard
  peer tiles was clipped when the peer name was long due to `overflow:hidden`
  applied to the flex container. The dot is now `flex-shrink:0` and truncation
  applies only to the name text span (`public/index.html`, `public/app.js`)

## [0.5.1] — Production Resilience Hardening

### Security

- **Self-hosted frontend assets and tightened CSP** — the dashboard now serves
  vendored Chart.js, TopoJSON, world-atlas, and Tabler assets locally instead
  of loading them from third-party CDNs. Helmet configuration was extracted
  into a dedicated module and tightened to a self-hosted Content Security
  Policy (`src/security/helmetOptions.js`, `public/index.html`, `public/app.js`,
  `public/vendor/`)
- **Startup patch verification for `node-routeros`** — application startup now
  hard-fails if the required MikroDash compatibility markers are missing from
  the patched `node-routeros` files, preventing silent boot with a broken
  runtime (`src/index.js`)
- **Socket.IO connection cap** — the server now applies a configurable
  `MAX_SOCKETS` limit and caps Socket.IO message size, reducing abuse surface
  on LAN deployments (`src/index.js`)

### Reliability

- **Per-command RouterOS write timeout with forced reconnect** — one-shot
  RouterOS API calls now use a configurable timeout budget
  (`ROS_WRITE_TIMEOUT_MS`) and close the active shared connection on timeout so
  the existing reconnect loop can recover cleanly (`src/routeros/client.js`)
- **Inflight guards across polling collectors** — all interval-based
  collectors now skip overlapping runs instead of stacking concurrent RouterOS
  calls when a slow tick exceeds its poll interval (`src/collectors/*.js`)
- **Graceful shutdown with unref’d fallback timer** — shutdown now stops
  RouterOS, Socket.IO, and HTTP resources in order and uses an unref’d 5-second
  forced-exit timer so the fallback does not keep the process alive on its own
  (`src/index.js`, `src/shutdown.js`)
- **RouterOS patch verification and write-timeout helpers extracted for testable
  runtime behavior** — health/CSP/shutdown support code was split into small
  modules to make the hardening logic independently testable
  (`src/health.js`, `src/security/helmetOptions.js`, `src/shutdown.js`)

### Operations

- **`/healthz` now behaves like readiness** — the endpoint returns `503` until
  startup completes or when RouterOS is disconnected, and now includes a
  `startupReady` flag in the JSON body (`src/index.js`, `src/health.js`)
- **Connection-table processing cap metadata** — the connections collector now
  reports the raw total separately from the number of rows processed, exposing
  `processed` and `processingCapped` to make truncation explicit (`src/collectors/connections.js`)
- **Auth failure tracking cap** — the in-memory auth failure map now evicts the
  oldest tracked IPs once it exceeds `maxTrackedIPs`, bounding memory growth
  under probe traffic (`src/auth/basicAuth.js`)
- **Wireless API probe debug logging** — failed wireless capability probes now
  log at debug level instead of failing silently (`src/collectors/wireless.js`)

### Bug Fixes

- **Info-page logo path normalized** — the about/info page now uses `/logo.png`
  like the rest of the app, avoiding broken image resolution on non-root paths
  (`public/index.html`)
- **Removed stale vendored CSS sourcemap reference** — the checked-in Tabler CSS
  no longer advertises a missing `.map` file, eliminating pointless 404s in
  browser devtools (`public/vendor/tabler.min.css`)
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

### Tests

- **Added production resilience regression coverage** — new tests cover the
  self-hosted asset/CSP contract, readiness health semantics, forced shutdown
  timer unref behavior, RouterOS write timeout recovery, connection collector
  truncation metadata, and auth failure eviction (`test/production-resilience-regressions.test.js`,
  `test/smoke-fixes.test.js`)

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
