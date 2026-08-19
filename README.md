# MikroDash
### The Ultimate MikroTik RouterOS Dashboard.

> Real-time MikroTik RouterOS v7 dashboard — streaming binary API, Socket.IO, Docker-ready.

MikroDash connects directly to the RouterOS API over a persistent binary TCP connection, streaming live data to the browser via Socket.IO. No page refreshes. No agents. Just plug in your router credentials and go.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

---

## Screenshots

### Dashboard
![Dashboard](screenshots/dashboard.png)

### Connections
![Connections](screenshots/connections.png)

### Connections Map
![Connections Map](screenshots/connections_map.png)

### Wireless Clients
![Wireless](screenshots/wireless.png)

### Router Interfaces
![Interfaces](screenshots/Interfaces.png)

### DHCP Leases
![DHCP](screenshots/dhcp.png)

### VPN / WireGuard
![VPN](screenshots/vpn.png)

### Firewall
![Firewall](screenshots/firewall.png)

### Routing
![Routing](screenshots/routing.png)

### Bandwidth
![Bandwidth](screenshots/bandwidth.png)

### Logs
![Logs](screenshots/logs.png)

---

## Features

### Dashboard
- **Configurable drag-and-drop grid** — 24×22 layout; drag cards to reposition, resize with 8 handles, or swap positions by hovering one card over another for 1.5 s; add/remove cards via the Add Card panel; layout synced server-side so all browsers and devices share the same arrangement
- **Live traffic chart** — per-interface RX/TX Mbps with configurable history window
- **System card** — CPU, RAM, Storage gauges with colour-coded thresholds (amber >75%, red >90%), board info, temperature, uptime chip
- **RouterOS update indicator** — shows installed vs available version side by side
- **Network card** — animated SVG topology diagram with live wired/wireless client counts, WAN IP, LAN subnets, and latency chart
- **Connections card** — total connection count sparkline, protocol breakdown bars (TCP/UDP/ICMP), top sources with hostname resolution, top destinations with geo-IP country flags and click-to-filter
- **Top Talkers** — top 5 devices by active traffic with RX/TX rates
- **WireGuard card** — active peers sorted by most recent handshake, limited to a configurable Top N (default 5)
- **Multi-router switcher** — monitor multiple MikroTik routers from one dashboard instance; switch between them via the dropdown in the page header with no restart or page refresh required
- **First-run setup wizard** — on a fresh install with no router configured, a guided setup overlay appears automatically; enter router details, test the connection, and connect — no `.env` file or container restart needed

#### Optional dashboard cards (15, hidden by default)
| Card | Description |
|---|---|
| Signal Health | Per-client RSSI bars for all wireless interfaces |
| Band Split | 2.4 / 5 / 6 GHz client count breakdown |
| Physical Ports | RJ-45 port visualiser colour-coded by link state |
| IP Utilisation | DHCP pool gauge with live lease percentage |
| Connections Map | World map with animated arcs — identical to the Connections page map |
| Top Countries | Country list with connection counts and protocol breakdown |
| Connection Flow | Source → destination Sankey diagram |
| Top Ports | Top 10 destination ports with connection counts |
| Routes | Routes-by-protocol doughnut with total in centre |
| BGP Peers | BGP session state and prefix counts |
| Bandwidth | Download / Upload utilisation bars (% of configured capacity, 30 s average) |
| Firewall Actions | Action breakdown bars (accept / drop / reject / other) |
| Chain Count | Rule count per chain type (forward / input / output / srcnat / dstnat / prerouting etc.) across all tables, shown as a colour-coded vertical bar chart |
| Logs | Live scrolling router log feed |
| NetWatch | Live status table for RouterOS NetWatch monitored hosts (up/down state, last change) |

### Pages
| Page | Description |
|---|---|
| WAN | The uplinks RouterOS reports as internet-connected — the same set the Dashboard Network card shows, in detail. Summary cards for uplink count, which one is carrying traffic, the public address and aggregate throughput, then a table giving each uplink its type (physical or tunnel), how long it has held that state, its address marked public or private, gateway, default-route distance with the active one marked, live rates and DHCP lease status with the countdown to renewal. With write access, Renew and Release on any uplink that has a DHCP client. RouterOS decides what counts as a WAN, so a router with internet detection switched off is told how to enable it rather than shown an empty table |
| Wireless | WiFi SSIDs, Signal Health and Band Split summary cards; clients grouped by interface with signal quality, band pill (2.4 / 5 / 6 GHz), IP, TX/RX rates, and sortable columns. The SSIDs card lists every network the router broadcasts, read from the interface table rather than from connected clients, so a network with nobody on it is still listed, with its bands and live client count. **Client names** are resolved from the DHCP lease table by MAC, falling back to a reverse DNS lookup of the client's IP. That IP comes only from the router's ARP table, so a router that bridges wireless clients at layer 2 — a CAP or access point whose gateway and DHCP live on another device — shows MAC addresses rather than names, however well reverse DNS is configured. The router needs an IP interface on the client subnet, or to be their DHCP server. It says so in the container log when this applies |
| CAPsMAN | Manager status, the remote CAP table (identity, board, serial, RouterOS version, state, connected time) and the provisioning rules that configure them. Each CAP lists its own radios and client count, and expands to the clients on it with signal, SSID and uptime. Attribution comes from the `cap` field the router reports on each radio and interface, with virtual APs resolved up to their master, so a guest-SSID client is counted against the access point serving it rather than against the manager. On a router that is itself a CAP, a panel names its manager and discovery interfaces instead. New-stack (`wifi`) CAPsMAN only |
| Interfaces | Physical Ports card (RJ-45 port visualiser, colour-coded by state) and Interface Types card (count by type); all interfaces as tiles with status, IP, live rates and a traffic trend sparkline, in three card sizes, plus a List view adding cumulative RX/TX totals, error and drop counters, link flap count and time since last link-up |
| DHCP | Subnet utilisation card with per-network lease counts, pool sizes, and colour-coded progress bars; IP Utilisation gauge driven live from the lease stream; active lease table with hostname, IP, MAC, and status; sortable columns; filter by DHCP server with its interface and VLAN shown as context (e.g. `IoT DHCP · IoT · VLAN 10`), composable with the text search |
| DNS | Resolver panel showing DNS over HTTPS with its certificate-verification state, servers, mDNS repeat interfaces, cache limits and query timeouts; a cache-usage gauge; and the static entry table, including regexp entries. The cache **contents** are deliberately not read — the used/size figures come from the resolver's own settings row, while the cache itself is a record of everywhere the network has been |
| VLANs | Summary cards (VLAN count, tagged ports, untagged ports, total throughput) and a VLAN table joining three router tables into one view: the L3 VLAN interfaces, the bridge VLAN table's tagged and untagged ports, and each bridge port's `pvid`. Shows id, interface, parent, MTU, tagged and untagged ports, DHCP client count and live RX/TX per VLAN. A VLAN that exists only at layer 2, with no `/interface/vlan` entry, still appears. Below it the bridge VLAN table, with the rows RouterOS adds automatically folded behind a count so what an operator actually configured leads. Rates and client counts are reused from the interface and DHCP streams, so the page costs no extra router queries |
| Bridges | Bridge list with STP mode, VLAN filtering, IGMP snooping, MAC, MTU and live throughput, then one tabbed card holding the port table (STP role, PVID, edge, horizon, state) and the learned MAC/host table with search, capped with its true total reported. A port on a bridge running no spanning tree says "no STP" rather than showing an invented role. VLAN membership lives on the VLANs page rather than being duplicated here |
| VPN | Summary stats bar (Total / Active / Stale / Never Connected / Throughput); all WireGuard peers as tiles sorted active-first, with colour-coded handshake age badge, live RX/TX rates, allowed IPs, and endpoint; plus PPP sessions (session uptime, caller address) and IPsec peers (negotiated ciphers), each shown only when the router has them |
| PPP | Summary cards (active sessions, count by service, aggregate throughput) and a session table of everyone connected — user, service (PPPoE / L2TP / PPTP / SSTP / OVPN), assigned address, caller id, uptime, live RX/TX rate and cumulative totals. Per-user rates are derived from byte counters between polls, since RouterOS reports only totals; a session's first reading shows no rate rather than a made-up one. PPPoE servers and PPP profiles in a second card. Account credentials are never read — the page shows sessions, not secrets. A router with no PPP says so |
| Connections | World map with animated arcs to destination countries; per-country protocol breakdown and org breakdown; sparklines; top ports panel; click-to-filter by country or by individual LAN client |
| Firewall | Rule Counts, Action Breakdown, and Chain Count summary cards; search bar; Filter, NAT, Mangle, and Raw rule tables (tab-gated — only the active tab streams); packet counts, byte totals, and live delta-pulse indicators |
| Bandwidth | Live per-connection bandwidth table with RX, TX, and Total Mbps; sortable columns; WAN traffic chart; ASN/Org colour-coded badges; interface and protocol filters |
| Routing | Route count summary by protocol with doughnut chart (total displayed in chart centre) and a BGP session summary, both above the tabs since they describe the page rather than one protocol. Tabbed tables, opening on **Routes**: static and dynamic routes (event-driven via `/ip/route/listen`), and **BGP** — peer table with state badges, prefix trend sparklines, and session flap detection (event-driven via `/routing/bgp/session/listen`) |
| Logs | Live router log stream with historical log import on connect, severity filter and text search |
| Queues | Simple queues and queue trees on one tabbed card, in the router's own order — simple queues are first-match-wins, so position changes behaviour and the table never sorts that away. Limits, priority, live rates with sparklines, bytes shaped and drop counts; queues created by Kid Control or a DHCP lease are marked and left alone. With write access: create, edit, enable, disable, reorder, reset counters and remove. A queue that covers MikroDash's own address at a throttling limit prompts before it is written. A FastTrack banner appears when one is active and a queue is affected, because FastTracked connections bypass simple queues entirely — the usual reason a queue looks configured and does nothing |
| Router Users | RouterOS's own accounts, not MikroDash's: users, groups with the full 17-permission matrix, and the sessions logged in right now. With write access, create, edit, enable, disable and remove users and groups, and end a session. The account MikroDash connects with, and its group, are structurally protected — they cannot be edited, moved, renamed or disconnected from this page, because that is the one change that could lock the dashboard out of the router with no way back |
| Audit | Every write action, in one searchable trail: who did it, from where, what changed and whether it was allowed. Covers MikroDash's own configuration (routers, users, roles, grants, sites, settings, layouts) and every write reaching a router. Refusals are recorded as well as successes, so an attempt that was denied leaves a trace. Filterable by actor, action, router, outcome and date, with CSV export. Credential values are never stored — the field name and the fact it changed are, which is the useful part |
| Packages | The package inventory — installed, disabled, and the extras MikroTik offers but that are not on the router — with versions, sizes and build dates, plus a firmware panel (current, upgrade, minimum) and the RouterOS update channel and status. With write access on the page it can also **schedule** changes: install, enable, disable or uninstall. RouterOS does not act on these immediately, it records them and applies them on the next reboot, so the page leads with a pending-changes banner, offers Undo on every scheduled row, and keeps "Apply changes & reboot" as a separate action that requires the router's name typed back. Account credentials and configuration are never touched |
| Reports | Historical data viewer with configurable date range and aggregation. Five tabs: **Ping** (RTT chart + sortable table), **Traffic** (per-interface RX/TX chart + table), **Bandwidth** (usage chart + table), **Alerts** (alert event history), **Connectivity** (router up/down event history). CSV and PDF export on every tab. Requires read on the Reports page |
| Routers | Fleet summary cards — Total Devices, Online, Offline, Alerting (routers with an unresolved alert). Four views, remembered between visits: **Comfortable** and **Compact** card grids, **List** — a sortable table of status, name, host, model, RouterOS, alerts, CPU/RAM/Disk, clients, WAN Rx/Tx and uptime — and **Map**, plotting each router on a world map by its location, with co-located routers clustered into one dot and routers with no location kept in a tray rather than dropped. One search box narrows any view by name, host, model or version, and understands `online`, `offline` and `alerting`. Cards show connection status (WiFi icon), CPU / RAM / Disk usage bars, Uptime, DHCP client count, and live WAN RX/TX rates; board name, RouterOS version, architecture, serial number, and license level pills. Background sessions pre-load data at startup so cards are populated instantly on first visit. Hidden for single-router setups |
| Settings | Persistent UI configuration — see below |

### Notifications
- Bell icon in topbar opens an alert history panel showing the last 50 alerts with timestamps
- Browser push notifications (when permitted) for interface, VPN, CPU, ping, NetWatch, router online/offline, and RouterOS update events
- **Push notification channels** — Telegram Bot, Pushbullet, SMTP email, and ntfy; all four can be active simultaneously; credentials stored AES-256-GCM encrypted
- **Per-router alert monitoring** — lightweight background connection to non-active routers so alerts fire for any configured router, not just the one currently displayed; opt-in per router. A router with alerts enabled keeps its alert collectors running even when no browser is watching it
- **Alert types** — Interface up/down (per interface type: ether/wlan/bridge/vlan), WireGuard peer state, CPU ≥ threshold, ping loss ≥ threshold, NetWatch host reachability, router online/offline, RouterOS update available
- **Independent Up/Down templates** — separate `notifBody` (⚠️ alert) and `notifBodyUp` (✅ recovery) templates with `{{alertType}}`, `{{routerName}}`, `{{detail}}`, and more variables
- Configurable cooldown (10 s – 60 min) prevents duplicate notifications per alert subject
- **Per-user channels** (optional, off by default) — each user can add their own Telegram, Pushbullet, ntfy or email destination under **My Account**, delivered *in addition to* the install-wide channels. A user is only ever notified about routers their role lets them read, checked at the moment the alert is sent, so revoking access stops delivery immediately. Which alert types fire stays an administrator's decision; a user chooses only where their own alerts go. Email is an opt-in plus an address — the mail server stays admin-only. Enable with **Allow personal channels** in Settings → Notifications

---

## ⚠️ Security Notice

MikroDash is designed to run **on your local network only**. It has no built-in HTTPS (terminate TLS at a reverse proxy if you need it).

MikroDash supports two authentication modes (**Settings → Authentication**): `none` (open access) and `modern` (cookie sessions with per-user accounts and role-based access control). **`none` mode serves the dashboard with no authentication — the server logs a startup warning in that state.**

In `modern` mode, access is granted as **(role, scope)**: a role says *which pages* someone sees and whether they may act on them, and the scope says *which routers* — everything, one site, or a single router. Roles are editable rather than fixed: **Administrator** is built in and always sees everything, while **Read Only** and **Operator** are ordinary roles you can change, alongside any you create. A grant can go to a user or to a group, and a router can belong to a site so a whole location is granted at once. Managing users, groups, roles and sites is always Administrator-only, and only at global scope — an administrator of one site cannot grant themselves more.

**Do not expose MikroDash directly to the internet.** Doing so would allow anyone (in an unauthenticated mode) to:
- View live data from your router (traffic, clients, connections, firewall rules, logs)
- Read your WAN IP, LAN topology, and connected device information
- Monitor your network activity in real time

If you need remote access, enable `modern` auth **and** place MikroDash behind an authenticating reverse proxy (such as Nginx, Authelia, or Cloudflare Access) or access it exclusively over a VPN.

**Recommended local hardening:**
- Enable authentication: switch to `modern` mode and create user accounts in **Settings → Authentication → Access Management**, granting each the narrowest role and scope that suits them
- Run on a non-default port and bind to your LAN interface only
- Use a dedicated read-only API user on the router (see RouterOS Setup below)
- User passwords are scrypt-hashed in `/data/users.json` (mode 0600); the encryption key for stored router credentials is auto-generated and saved to `/data/.secret` (mode 0600) — keep your Docker volume secure

---

## Quick Start

### Option 1 — GHCR (recommended)

Pull and run the pre-built image directly — no need to clone the repo or create a `.env` file:

```bash
docker pull ghcr.io/secops-7/mikrodash:latest
```

Images are published by GitHub Actions on version tags only, so `latest` always tracks the most recent release rather than unreleased work on `main`. (Pushes to `main` still build both architectures as a check, they just are not published.) Each release is a multi-arch manifest covering `linux/amd64` and `linux/arm64`. Docker will automatically pull the correct layer for your platform — this includes Raspberry Pi 4/5, MikroTik's own R5S/RB5009 companion boards, and Apple M-series machines running Linux containers.

> **ARMv7 (32-bit ARM) is no longer built, as of 0.6.0.** MikroDash moved to a Node 24 base image, and Node 24 dropped 32-bit ARM upstream, so `node:24-alpine` publishes no `linux/arm/v7` variant.
>
> If you run MikroDash on ARMv7 hardware — a RouterOS container on a 32-bit device such as the hEX S (2025), or an older Raspberry Pi — pin to `ghcr.io/secops-7/mikrodash:0.5.54`, the last release built for it. **If you are already pinned to `:0.5` you are fine and need do nothing**, since 0.6.0 does not match that tag. Do not stay on `:latest`: it now resolves to a manifest with no arm/v7 entry, so your pull will fail rather than degrade gracefully.
>
> 0.5.54 will not receive further updates, including security fixes. Whether a separate ARMv7 build is worth maintaining depends on how many people are actually on one, so please comment on [#44](https://github.com/SecOps-7/MikroDash/issues/44) if this affects you.

To pin to a specific release:

```bash
docker pull ghcr.io/secops-7/mikrodash:0.5.43
```

Run with Docker Compose — create a `docker-compose.yml`:

```yaml
services:
  mikrodash:
    image: ghcr.io/secops-7/mikrodash:latest
    restart: unless-stopped
    ports:
      - "3081:3081"
    volumes:
      - mikrodash-data:/data

volumes:
  mikrodash-data:
```

```bash
docker compose up -d
```

Open `http://localhost:3081` — the first-run setup wizard will guide you through adding your router. No `.env` file is required.

### Option 2 — Build from source

```bash
git clone https://github.com/SecOps-7/MikroDash.git
cd MikroDash
docker compose up -d
```

To build a multi-arch image locally (requires Docker Buildx):

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t mikrodash:local --load .
```

- Dashboard: `http://localhost:3081`
- Health check: `http://localhost:3081/healthz` (`200` only after startup completes, RouterOS is connected, and the critical collectors are delivering fresh data; a stalled traffic stream or an unreachable `defaultIf` now returns `503`)

Source builds require the bundled `node-routeros` compatibility patch. If startup reports a missing patch marker, run `node patch-routeros.js` again before launching MikroDash.

For a production-style deployment on an external Docker host such as an R5S that connects to a MikroTik hEX S over the RouterOS API, see `docs/deploy-r5s.md` and the ready-to-copy files in `deploy/r5s/`.

---

## Settings

Most configuration is managed through the **Settings page** in the UI (gear icon at the bottom of the sidebar). Settings are saved to `/data/settings.json` on the Docker volume and persist across container restarts.

| Section | What you can configure |
|---|---|
| Routers | Add, edit, and delete router connections. Each entry stores host, port, username, password (encrypted), TLS options, WAN interface, and ping target. The table also shows each router's model, serial number, and RouterOS version, learned from the device and stored against the entry so they stay visible while a router is offline or disabled. Test Connection validates credentials before saving. The active router is selected from the picker in the page header, which lists each router with its host and a live online/offline dot, and gains a search box once you have five or more Each router also carries its own collection settings — Stream or Poll, which collectors run, and interval overrides. **Location** is a city or town picker used by the Routers Map view: leave it empty and the location is derived automatically from the router's WAN IP, falling back to its site's location; set it and your choice wins. Nothing is sent anywhere to resolve it — the lookup uses the geo-IP data already bundled in the image. |
| Authentication | Auth mode (`none` / `modern` cookie sessions) and session timeout. In `modern` mode, **Access Management** holds four tabs: **Users**, **Groups**, **Sites** and **Roles** — a role is a per-page read/write matrix, granted to a user or group over all routers, a site, or one router. Passwords are scrypt-hashed |
| Poll Intervals | Per-collector update intervals with **Polling Profile** preset buttons (Fast / Faster / Standard / Slow / Slower / Custom). Drag any slider to enter Custom mode; **Save Custom Profile** persists your values as a reusable template. Changes apply immediately without restart. Pure event-driven collectors (ARP, Routing, DHCP Leases, Firewall rule changes) show an Event-driven badge instead of a slider. Sliders sit on exactly two scales — **1s–30s** for live data (rates, sessions, uplinks) and **10s–10m** for things that change when somebody edits the router (packages, DHCP networks, topology) — laid out in two columns under Advanced and grouped under a heading for each scale. |
| Collection Method | **Moved to each router** (Settings → Routers → edit). A per-router Stream/Poll master switch, per-collector enable/disable, and interval overrides. Poll replaces persistent API streams with periodic requests, which suits lower-end hardware where concurrent open channels — not data volume — are the constraint. Logs and the traffic graph always stream. |
| Limits | Top N values for connections, talkers, firewall rules, and VPN dashboard peers; max connection rows; traffic history window |
| Alert Thresholds | CPU alert threshold (%) and ping loss alert (%) for browser notifications |
| Notifications | Push notification channels — Telegram Bot, Pushbullet, SMTP email, and ntfy (all four can be active simultaneously); per-type toggles (interface up/down, WireGuard, CPU, ping, NetWatch, router status, RouterOS update); separate ⚠️ alert and ✅ recovery message templates with `{{variable}}` substitution; configurable cooldown (10 s – 60 min) per alert subject; test-send button per channel. **Allow personal channels** lets each user add their own destination under My Account — off by default, since a personal ntfy topic or SMTP recipient is an address the user chooses |
| My Account | Not on this page — every signed-in user reaches it from their name in the sidebar. Change your own password (signs out your other sessions), see the roles and scopes you hold, review and revoke your active sessions, and manage your own notification channels |
| Data Retention | Traffic/ping/bandwidth sample retention (1–3650 days, default 90) and alert/connectivity event retention (1–3650 days, default 365); pruning runs automatically |
| Data Cleanup | Delete stored history on demand rather than waiting for retention. Scope by router (one or all), data type (traffic, ping, bandwidth, alerts & connectivity) and age (1 / 7 / 30 / 90 / 365 days, or everything). Shows database size, total rows and a per-router breakdown; **Preview** reports exactly how many rows the selection would remove before you confirm. The database is compacted afterwards so the space is returned to disk. Admin only |
| Diagnostics | Enable/disable verbose RouterOS API debug logging at runtime — no container restart required |
| Appearance | 26 named palette swatches (dark and light variants) — applies instantly and persists via `localStorage`. Contrast, Text Brightness, and Background Brightness sliders (15 steps each) for fine-grained adjustment independent of palette. **Font Family** picker with 26 self-hosted options (Inter, IBM Plex Sans, Source Sans 3, Geist, JetBrains Mono, Oxanium, Orbitron, and 19 more — all served as local WOFF2 files, no CDN; all SIL Open Font License, see [public/fonts/OFL.txt](public/fonts/OFL.txt)). **Font Size** with six presets (Extra Small to Extra Large). Includes a **Visible Pages** subsection with three canned view presets — **Home** (Wireless, Interfaces, DHCP, Connections, Bandwidth), **Standard** (adds Topology, DNS, VLANs, VPN, Firewall, Logs) and **Advanced** (everything, and the only tier showing Routers) — plus the individual page toggles they set. Picking a preset ticks a whole tier at once; editing any toggle afterwards shows Custom. Presets narrow what an install shows and can never widen it: each user still sees only the pages their role permits. The same three tiers appear in Access Management as a bulk-editor for a role's page matrix. A **Group the sidebar into categories** toggle collapses the nav's twenty-four pages into seven expandable groups — Network, Wireless, IP Services, Tunnels, Traffic, Security and System — with Dashboard, Routers, Reports, Audit and Settings always at the top level. Which groups are open is remembered against your account rather than the browser, and the same toggle appears in the account dialog so it is reachable without Settings access. |

### Credential encryption

Router and dashboard passwords are encrypted at rest using AES-256-GCM. On first start, MikroDash automatically generates a random 64-character key and saves it to `/data/.secret` on the Docker volume (mode 0600). This key is tied to your volume — as long as you keep the volume, your encrypted credentials are safe.

If you need to move credentials across volumes or manage the key yourself, set `DATA_SECRET` in a `.env` file and mount it:

```env
DATA_SECRET=your-long-random-secret-here
```

The `DATA_SECRET` env var always takes priority over the auto-generated `/data/.secret` file when set.

---

## RouterOS Setup

Create a read-only API user (recommended):

```
/ip service set api port=8728 disabled=no
/user group add name=mikrodash policy=read,api,test,!local,!telnet,!ssh,!ftp,!reboot,!write,!policy,!winbox,!web,!sniff,!sensitive,!romon,!rest-api
/user add name=mikrodash group=mikrodash password=your-secure-password
```

That group is read-only, which is the right default: every dashboard, chart and alert works with it,
and a compromised MikroDash cannot change your router.

#### Optional: the write features

Two pages change router configuration, and each needs more than `read`:

| Page | Needs | What it can do |
|---|---|---|
| **Packages** | `write` | Schedule a package enable/disable/uninstall, and reboot to apply |
| **Queues** | `write` | Create, edit and remove simple queues and queue trees |
| **Router Users** | `write` **and** `policy` | Create, edit and remove RouterOS users, groups and sessions |

> **If a queue seems to do nothing, check FastTrack first.** RouterOS's default configuration includes
> a `fasttrack-connection` firewall rule, and FastTracked connections bypass simple queues and any
> queue tree parented to `global`. A queue only shapes the traffic FastTrack did not take. The Queues
> page detects this and says so. To shape that traffic too, disable or narrow the FastTrack rule in
> `/ip/firewall/filter`.

Grant them only if you want those pages, and understand the trade: **`policy` is the permission that
governs user management, so an account holding it can create router users.** That is a real increase
in what a compromised MikroDash could do. It is your call to make deliberately, not a default.

```
/user group set [find name=mikrodash] policy=read,write,policy,api,test,!local,!telnet,!ssh,!ftp,!reboot,!winbox,!web,!sniff,!sensitive,!romon,!rest-api
```

Without them nothing breaks: both pages detect the refusal, drop to read-only and show the command
above rather than failing silently. Each page also has an install-wide toggle under
**Settings → Visible Pages**, and per-user access is controlled by roles.

MikroDash will not let you edit the account it signs in with, or that account's group, from the
Router Users page — that is the one change that could lock the dashboard out of the router with no
way back. Use WinBox for those.

### Enabling TLS (API-SSL)

MikroDash supports encrypted connections to the RouterOS API over `api-ssl` (default port 8729). You can use a self-signed certificate — no external CA or purchased certificate is required.

**Step 1 — Enable the API-SSL service**

```
/ip/service set api-ssl disabled=no port=8729
```

**Step 2 — Create and self-sign a local CA**

```
/certificate add name=local-ca common-name=local-ca days-valid=3650 key-size=2048 key-usage=key-cert-sign,crl-sign
/certificate sign local-ca
```

**Step 3 — Create and sign the API-SSL certificate using that CA**

```
/certificate add name=api-ssl-cert common-name=mikrodash days-valid=3650 key-size=2048 key-usage=digital-signature,key-encipherment,tls-server
/certificate sign api-ssl-cert ca=local-ca
```

**Step 4 — Apply the certificate to the service**

```
/ip/service set api-ssl certificate=api-ssl-cert disabled=no port=8729
```

Once the certificate is applied, go to **Settings → Routers**, edit your router entry, enable **TLS**, enable **Allow self-signed cert**, set the port to `8729`, and save. MikroDash will reconnect over an encrypted channel immediately.

---

## Environment Variables

A `.env` file is **not required**. All router configuration, dashboard auth, and encryption keys are managed through the web UI and the Docker volume. The only reason to create a `.env` is to override infrastructure-level defaults:

```env
# Port MikroDash listens on inside the container (default: 3081)
# PORT=3081

# Maximum simultaneous browser connections (default: 50)
# MAX_SOCKETS=50

# Trusted proxy IP for X-Forwarded-For (only needed behind a reverse proxy)
# TRUSTED_PROXY=127.0.0.1

# RouterOS API write timeout in milliseconds (default: 30000)
# ROS_WRITE_TIMEOUT_MS=30000

# Encryption key for credentials at rest — auto-generated if not set
# DATA_SECRET=your-long-random-string-here

# Verbose RouterOS debug logging — can also be toggled in Settings → Diagnostics
# ROS_DEBUG=false
```

Copy `.env.example` to `.env`, uncomment lines you need, and add `env_file: .env` to your `docker-compose.yml`.

---

## Architecture

### Streamed (router pushes continuously — no poll overhead)
| Data | RouterOS endpoint |
|---|---|
| System metrics (CPU, RAM, temp, uptime) | `/system/resource/print =interval=N` |
| WAN Traffic RX/TX per interface | `/interface/monitor-traffic =interface=X =interval=1` |
| Ping RTT + loss | `/tool/ping =address=X =interval=N` |
| Top Talkers (Kid Control) | `/ip/kid-control/device/print =interval=N` |
| Interface metadata (name, IP, state) | `/interface/print =interval=N` + `/ip/address/print =interval=N` |
| Interface byte counters (all interfaces) | `/interface/monitor-traffic =interface=all =interval=N` |
| Firewall connection table, geo-IP | `/ip/firewall/connection/print =interval=N` |
| Router Logs | `/log/listen` |
| DHCP Lease changes | `/ip/dhcp-server/lease/listen` |
| Firewall structural changes (rule add/remove/edit) | `/ip/firewall/filter\|nat\|mangle/listen` |
| WireGuard peer handshakes & stats | `/interface/wireguard/peers/listen` |
| ARP table (device join/leave) | `/ip/arp/listen` |
| Route table (add/remove/change) | `/ip/route/listen` |
| BGP session state changes | `/routing/bgp/session/listen` |

### Polled (concurrent via tagged API multiplexing)

Bridges, VLANs, CAPsMAN, PPP, WAN and Queues additionally hold a `/listen` channel each in Stream
mode, so a change appears the moment the router makes it; the interval below governs how often the
volatile tables are re-read. Setting a router to Poll closes those channels. DNS, Packages and Router
Users are poll-only by design — see `src/collection.js` for why.
| Collector | Default interval | Data |
|---|---|---|
| Bandwidth | 3 s | Per-connection live RX/TX/Total Mbps (reads from the shared connection-table cache populated by the Connections stream) |
| VPN counters | 5 s | WireGuard per-peer byte counter refresh for live rates |
| Firewall counters | 5 s | Packet/byte counter refresh for all firewall rules (RouterOS 7.x does not push counter updates via the listen stream) |
| Wireless | 30 s | Wireless client list |
| VLANs | 5 s | VLAN interfaces, bridge VLAN table and bridge port PVIDs (the configuration tables are re-read every 12th tick; rates and client counts are reused from the interface and DHCP streams at no router cost) |
| PPP | 5 s | Active PPP sessions, PPPoE servers and PPP profiles; per-session rates derived from the byte counters |
| Bridges | 5 s | Bridges, ports with STP roles and the learned host table (capped); rates reused from the interface stream |
| CAPsMAN | 10 s | Manager and CAP state, remote CAPs, provisioning rules, radios and per-CAP client counts |
| DNS | 10 s | Resolver settings and cache usage; static entries on a slower cadence. The cache contents are never enumerated |
| Packages | 60 s | Package inventory, firmware versions and update status. Slow by design — an inventory changes on a reboot, not on a tick |
| WAN | 10 s | Internet-connected uplinks, their addresses, default routes and DHCP leases; rates reused from the interface stream |
| Queues | 5 s | Simple queues and queue trees with limits and counters; rates derived from the byte counters over the poll window |
| Router Users | 60 s | RouterOS users, groups and active sessions. Slow by design — a user list changes when somebody edits it, not on a tick |
| DHCP Networks | ~10 min | LAN subnets, pool sizes, WAN IP, internet-facing interfaces |

All collectors run **concurrently** on a single TCP connection — no serial queuing. All intervals are adjustable in the Settings page and apply immediately without restart.

**Idle gating** — two gates. Nothing polls or holds a channel when no browser has that router open. And a page-scoped collector runs only while somebody is actually on its page: leave the VLANs page and its poll stops and its `/listen` closes, return and it refreshes at once. Concurrent open channels, not data volume, are what strain small hardware, so a page nobody is looking at costs the router nothing.

All collectors that support RouterOS `/listen` streams use event-driven delivery — RouterOS pushes only delta rows when data changes, producing zero API traffic when the network is idle. A 60-second heartbeat emit keeps the browser's stale-detection timers alive.

---

## Keyboard Shortcuts

| Key | Page |
|---|---|
| `1` | Dashboard |
| `2` | Wireless |
| `3` | Interfaces |
| `4` | DHCP |
| `5` | VPN |
| `6` | Connections |
| `7` | Routing |
| `8` | Bandwidth |
| `9` | Firewall |
| `0` | Logs |
| `/` | Focus log search |

---

## License

MIT — see [LICENSE](LICENSE)

Third-party attributions — see [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)

---

## Disclaimer

MikroDash is an independent, community-built project and is **not affiliated with, endorsed by, or associated with MikroTik SIA** in any way. MikroTik and RouterOS are trademarks of MikroTik SIA. All product names and trademarks are the property of their respective owners.

---

## Built With AI

The code for MikroDash was written with the assistance of [Claude](https://claude.ai) by [Anthropic](https://anthropic.com).
