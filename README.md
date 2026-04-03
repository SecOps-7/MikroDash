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
- **Live traffic chart** — per-interface RX/TX Mbps with configurable history window
- **System card** — CPU, RAM, Storage gauges with colour-coded thresholds (amber >75%, red >90%), board info, temperature, uptime chip
- **RouterOS update indicator** — shows installed vs available version side by side
- **Network card** — animated SVG topology diagram with live wired/wireless client counts, WAN IP, LAN subnets, and latency chart
- **Connections card** — total connection count sparkline, protocol breakdown bars (TCP/UDP/ICMP), top sources with hostname resolution, top destinations with geo-IP country flags and click-to-filter
- **Top Talkers** — top 5 devices by active traffic with RX/TX rates
- **WireGuard card** — active peers sorted by most recent handshake, limited to a configurable Top N (default 5)
- **Multi-router switcher** — monitor multiple MikroTik routers from one dashboard instance; switch between them via the dropdown in the page header with no restart or page refresh required

### Pages
| Page | Description |
|---|---|
| Wireless | Signal Health and Band Split summary cards; clients grouped by interface with signal quality, band pill (2.4 / 5 / 6 GHz), IP, TX/RX rates, and sortable columns |
| Interfaces | Physical Ports card (RJ-45 port visualiser, colour-coded by state) and Interface Types card (count by type); all interfaces as compact tiles with status, IP, live rates, cumulative RX/TX totals, and per-card traffic trend sparkline |
| DHCP | Subnet utilisation card with per-network lease counts, pool sizes, and colour-coded progress bars; IP Utilisation gauge driven live from the lease stream; active lease table with hostname, IP, MAC, and status; sortable columns |
| VPN | Summary stats bar (Total / Connected / Idle / Throughput); all WireGuard peers as tiles sorted active-first, with colour-coded handshake age badge, live RX/TX rates, allowed IPs, and endpoint |
| Connections | World map with animated arcs to destination countries, per-country protocol breakdown, sparklines, top ports panel, and click-to-filter by country |
| Firewall | Rule Counts, Action Breakdown, and Total Hits summary cards; search bar; Top Hits, Filter, NAT, Mangle, and Raw rule tables with packet counts, byte totals, and live delta-pulse indicators |
| Bandwidth | Live per-connection bandwidth table with RX, TX, and Total Mbps; sortable columns; WAN traffic chart; ASN/Org colour-coded badges; interface and protocol filters |
| Routing | Route count summary by protocol with doughnut chart (total displayed in chart centre); static and dynamic route table (event-driven via `/ip/route/listen`); BGP peer table with state badges, prefix trend sparklines, and session flap detection (event-driven via `/routing/bgp/session/listen`) |
| Logs | Live router log stream with severity filter and text search |
| Settings | Persistent UI configuration — see below |

### Notifications
- Bell icon in topbar opens an alert history panel showing the last 50 alerts with timestamps
- Browser push notifications (when permitted) for:
  - Interface down / back up
  - WireGuard peer disconnected / reconnected
  - CPU exceeds 90% (1-minute cooldown)
  - 100% ping loss to ping target

---

## ⚠️ Security Notice

MikroDash is designed to run **on your local network only**. It has no built-in HTTPS or role-based access control.

**Do not expose MikroDash directly to the internet.** Doing so would allow anyone to:
- View live data from your router (traffic, clients, connections, firewall rules, logs)
- Read your WAN IP, LAN topology, and connected device information
- Monitor your network activity in real time

If you need remote access, place MikroDash **behind an authenticating reverse proxy** (such as Nginx with Basic Auth, Authelia, or Cloudflare Access) or access it exclusively over a VPN.

**Recommended local hardening:**
- Set a dashboard username and password in the Settings page (HTTP Basic Auth)
- Run on a non-default port and bind to your LAN interface only
- Set `chmod 600 .env` to protect your router credentials
- Use a dedicated read-only API user on the router (see RouterOS Setup below)
- Set `DATA_SECRET` in your `.env` to a long random string to protect encrypted credentials in `settings.json` and `routers.json`

---

## Quick Start

### Option 1 — Docker Hub / GHCR (recommended)

Pull and run the pre-built image directly — no need to clone the repo:

```bash
docker pull ghcr.io/secops-7/mikrodash:latest
```

The image is built automatically by GitHub Actions on every push to `main` and on version tags. It is published as a multi-arch manifest covering `linux/amd64` and `linux/arm64`. Docker will automatically pull the correct layer for your platform — this includes Raspberry Pi 4/5, MikroTik's own R5S/RB5009 companion boards, and Apple M-series machines running Linux containers.

To pin to a specific release:

```bash
docker pull ghcr.io/secops-7/mikrodash:0.5.22
```

Create your `.env` file:

```bash
curl -o .env https://raw.githubusercontent.com/SecOps-7/MikroDash/main/.env.example
# Edit .env — set ROUTER_HOST, ROUTER_USER, ROUTER_PASS at minimum
```

Run with Docker Compose — create a `docker-compose.yml`:

```yaml
services:
  mikrodash:
    image: ghcr.io/secops-7/mikrodash:latest
    restart: unless-stopped
    env_file: .env
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

### Option 2 — Build from source

```bash
git clone https://github.com/SecOps-7/MikroDash.git
cd MikroDash
node patch-routeros.js
cp .env.example .env
# Edit .env — set ROUTER_HOST, ROUTER_USER, ROUTER_PASS at minimum
docker compose up -d
```

To build a multi-arch image locally (requires Docker Buildx):

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t mikrodash:local --load .
```

- Dashboard: `http://localhost:3081`
- Health check: `http://localhost:3081/healthz` (`200` only after startup completes and RouterOS is connected)

Source builds require the bundled `node-routeros` compatibility patch. If startup reports a missing patch marker, run `node patch-routeros.js` again before launching MikroDash.

For a production-style deployment on an external Docker host such as an R5S that connects to a MikroTik hEX S over the RouterOS API, see `docs/deploy-r5s.md` and the ready-to-copy files in `deploy/r5s/`.

---

## Settings

Most configuration is managed through the **Settings page** in the UI (gear icon at the bottom of the sidebar). Settings are saved to `/data/settings.json` on the Docker volume and persist across container restarts.

| Section | What you can configure |
|---|---|
| Routers | Add, edit, and delete router connections. Each entry stores host, port, username, password (encrypted), TLS options, WAN interface, and ping target. A Test Connection button validates credentials before saving. The active router is selected from the dropdown in the page header |
| Dashboard Auth | HTTP Basic Auth username and password for the dashboard itself |
| Poll Intervals | Per-collector polling intervals — changes apply immediately without restart. Includes sliders for Firewall (counter poll) and VPN (counter poll). Streamed collectors (Interfaces, ARP, Routing) show an Event-driven badge instead of a slider |
| Limits | Top N values for connections, talkers, firewall rules, and VPN dashboard peers; max connection rows; traffic history window |
| Alert Thresholds | CPU alert threshold (%) and ping loss alert (%) for browser notifications |
| Visible Pages | Toggle individual pages on/off — hidden pages are removed from the sidebar instantly |

Settings values from `.env` are used as the initial defaults if no `settings.json` exists yet, so existing deployments upgrade seamlessly.

### Credential encryption

Router and dashboard passwords are encrypted at rest using AES-256-GCM. Router credentials are stored in `/data/routers.json` and the dashboard password in `/data/settings.json` — both use the same `DATA_SECRET`-derived key. Set `DATA_SECRET` in your `.env` to a long random string to tie the encryption key to your deployment:

```env
DATA_SECRET=your-long-random-secret-here
```

If `DATA_SECRET` is not set, a built-in default is used — not recommended for production.

---

## RouterOS Setup

Create a read-only API user (recommended):

```
/ip service set api port=8728 disabled=no
/user group add name=mikrodash policy=read,api,test,!local,!telnet,!ssh,!ftp,!reboot,!write,!policy,!winbox,!web,!sniff,!sensitive,!romon,!rest-api
/user add name=mikrodash group=mikrodash password=your-secure-password
```

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

The `.env` file seeds the initial defaults for the Settings page. Once `settings.json` exists on the data volume, the UI values take precedence. Only the variables below are relevant at the container level — everything else is managed in the Settings page.

```env
# Server
PORT=3081                    # HTTP port MikroDash listens on
MAX_SOCKETS=50               # Maximum concurrent Socket.IO clients
TRUSTED_PROXY=               # Proxy IP to trust X-Forwarded-For from (e.g. 127.0.0.1)

# Data volume & credential encryption
DATA_SECRET=                 # Secret used to encrypt credentials in settings.json — set this!

# RouterOS — used to seed the first router entry in routers.json on first start only.
# After that, all router connection details are managed in the Settings → Routers card.
ROUTER_HOST=192.168.88.1
ROUTER_PORT=8729
ROUTER_TLS=true
ROUTER_TLS_INSECURE=false
ROUTER_USER=mikrodash
ROUTER_PASS=change-me
DEFAULT_IF=ether1

# Advanced / rarely changed
ROS_WRITE_TIMEOUT_MS=30000   # Force reconnect if a RouterOS command exceeds this time
ROS_DEBUG=false              # Log raw RouterOS API frames (very verbose)
```

All other settings (poll intervals, top-N limits, page visibility, ping target, dashboard auth) are configured in the Settings page and do not need to be set in `.env`.

---

## Architecture

### Streamed (router pushes on change — zero poll overhead)
| Data | RouterOS endpoint |
|---|---|
| WAN Traffic RX/TX | `/interface/monitor-traffic` |
| Router Logs | `/log/listen` |
| DHCP Lease changes | `/ip/dhcp-server/lease/listen` |
| Interface up/down state | `/interface/listen` |
| Firewall structural changes (rule add/remove/edit) | `/ip/firewall/filter\|nat\|mangle/listen` |
| WireGuard peer handshakes & stats | `/interface/wireguard/peers/listen` |
| ARP table (device join/leave) | `/ip/arp/listen` |
| Route table (add/remove/change) | `/ip/route/listen` |
| BGP session state changes | `/routing/bgp/session/listen` |

### Polled (concurrent via tagged API multiplexing)
| Collector | Default interval | Data |
|---|---|---|
| System | 1 s | CPU, RAM, storage, temp, ROS version |
| Connections | 3 s | Firewall connection table, geo-IP |
| Bandwidth | 3 s | Per-connection live RX/TX/Total Mbps (shares connection table fetch with Connections) |
| Top Talkers | 3 s | Kid Control traffic stats |
| VPN counters | 10 s | WireGuard per-peer byte counter refresh for live rates |
| Firewall counters | 5 s | Packet/byte counter refresh for all firewall rules (RouterOS 7.x does not push counter updates via the listen stream) |
| Ping | 10 s | RTT + packet loss to ping target |
| Interface Status | 15 s | Byte counter refresh for live rate bars |
| Wireless | 60 s | Wireless client list |
| DHCP Networks | 5 min | LAN subnets, pool sizes, WAN IP |

All collectors run **concurrently** on a single TCP connection — no serial queuing. All intervals are adjustable in the Settings page and apply immediately without restart.

**Idle gating** — all polled collectors skip their RouterOS API calls entirely when no browser clients are connected. On an unattended dashboard, RouterOS API traffic drops to near zero across all data paths.

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
