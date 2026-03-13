# MikroDash
### The Ultimate MikroTik RouterOS Dashboard.

> Real-time MikroTik RouterOS v7 dashboard — streaming binary API, Socket.IO, Docker-ready.

MikroDash connects directly to the RouterOS API over a persistent binary TCP connection, streaming live data to the browser via Socket.IO. No page refreshes. No agents. Just plug in your router credentials and go.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

---

## Screenshots

### Dashboard
![Dashboard](screenshots/dashboard.png)

### Connections Map
![Connections Map](screenshots/connections.png)

### Wireless Clients
![Wireless](screenshots/wireless.png)

### Router Interfaces
![Network Diagram](screenshots/networks.png)

### DHCP Leases
![DHCP](screenshots/dhcp.png)

### VPN / WireGuard
![VPN](screenshots/vpn.png)

---

## Features

### Dashboard
- **Live traffic chart** — per-interface RX/TX Mbps with configurable history window
- **System card** — CPU, RAM, Storage gauges with colour-coded thresholds (amber >75%, red >90%), board info, temperature, uptime chip
- **RouterOS update indicator** — shows installed vs available version side by side
- **Network card** — animated SVG topology diagram with live wired/wireless client counts, WAN IP, LAN subnets, and latency chart
- **Connections card** — total connection count sparkline, protocol breakdown bars (TCP/UDP/ICMP), top sources with hostname resolution, top destinations with geo-IP country flags
- **Top Talkers** — top 5 devices by active traffic with RX/TX rates
- **WireGuard card** — active peer list with status and last handshake

### Pages
| Page | Description |
|---|---|
| Wireless | Clients grouped by interface with signal quality, band badge (2.4/5/6 GHz), IP, TX/RX rates, and sortable columns |
| Interfaces | All interfaces as compact tiles with status, IP, live rates, and cumulative RX/TX totals |
| DHCP | Active DHCP leases with hostname, IP, MAC, and expiry |
| VPN | All WireGuard peers (active + idle) as tiles sorted active-first, with allowed IPs, endpoint, handshake, and traffic counters |
| Connections | World map with animated arcs to destination countries, per-country protocol breakdown, sparklines, top ports panel, and click-to-filter |
| Firewall | Top hits, Filter, NAT, and Mangle rule tables with packet counts |
| Bandwidth | Live per-connection bandwidth table with RX, TX, and Total Mbps; sortable columns; WAN traffic chart; ASN/Org colour-coded badges; interface and protocol filters |
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
- Set `DATA_SECRET` in your `.env` to a long random string to protect encrypted credentials in `settings.json`

---

## Quick Start

### Option 1 — Docker Hub / GHCR (recommended)

Pull and run the pre-built image directly — no need to clone the repo:

```bash
docker pull ghcr.io/secops-7/mikrodash:latest
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

- Dashboard: `http://localhost:3081`
- Health check: `http://localhost:3081/healthz` (`200` only after startup completes and RouterOS is connected)

Source builds require the bundled `node-routeros` compatibility patch. If startup reports a missing patch marker, run `node patch-routeros.js` again before launching MikroDash.

For a production-style deployment on an external Docker host such as an R5S that connects to a MikroTik hEX S over the RouterOS API, see `docs/deploy-r5s.md` and the ready-to-copy files in `deploy/r5s/`.

---

## Settings

Most configuration is managed through the **Settings page** in the UI (gear icon at the bottom of the sidebar). Settings are saved to `/data/settings.json` on the Docker volume and persist across container restarts.

| Section | What you can configure |
|---|---|
| Router Connection | Host, API port, username, password, TLS, self-signed cert, default WAN interface, ping target |
| Dashboard Auth | HTTP Basic Auth username and password |
| Poll Intervals | Per-collector polling intervals — changes apply immediately without restart |
| Limits | Top N values for connections, talkers, firewall rules; max connection rows; traffic history window |
| Visible Pages | Toggle individual pages on/off — hidden pages are removed from the sidebar instantly |

Settings values from `.env` are used as the initial defaults if no `settings.json` exists yet, so existing deployments upgrade seamlessly.

### Credential encryption

Router and dashboard passwords are encrypted at rest in `settings.json` using AES-256-GCM. Set `DATA_SECRET` in your `.env` to a long random string to tie the encryption key to your deployment:

```env
DATA_SECRET=your-long-random-secret-here
```

If `DATA_SECRET` is not set, a built-in default is used — not recommended for production.

---

## RouterOS Setup

Create a read-only API user (recommended):

```
/ip service set api port=8728 disabled=no
/user group add name=mikrodash policy=read,api,!local,!telnet,!ssh,!ftp,!reboot,!write,!policy,!test,!winbox,!web,!sniff,!sensitive,!romon,!rest-api
/user add name=mikrodash group=mikrodash password=your-secure-password
```

To use API-SSL (TLS), enable the ssl service and set `ROUTER_TLS=true` in your `.env` or in Settings:

```
/ip service set api-ssl disabled=no port=8729
```

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

# RouterOS — used as initial defaults only (can be changed in Settings UI afterwards)
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

### Polled (concurrent via tagged API multiplexing)
| Collector | Default interval | Data |
|---|---|---|
| System | 3 s | CPU, RAM, storage, temp, ROS version |
| Connections | 3 s | Firewall connection table, geo-IP |
| Bandwidth | 3 s | Per-connection live RX/TX/Total Mbps (shares connection table fetch with Connections) |
| Top Talkers | 3 s | Kid Control traffic stats |
| Wireless | 5 s | Wireless client list |
| Interface Status | 5 s | Interface state, IPs, rx/tx bytes |
| VPN | 10 s | WireGuard peers, rx/tx rates |
| Firewall | 10 s | Rule hit counts |
| Ping | 10 s | RTT + packet loss to ping target |
| ARP | 30 s | MAC to IP mappings |
| DHCP Networks | 5 min | LAN subnets, WAN IP |

All collectors run **concurrently** on a single TCP connection — no serial queuing. All intervals are adjustable in the Settings page and apply immediately without restart.

The WAN traffic monitor (`/interface/monitor-traffic`) pauses its RouterOS API calls automatically when no browser clients are connected, resuming immediately on the next connection.

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
| `7` | Firewall |
| `8` | Logs |
| `9` | Bandwidth |
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
