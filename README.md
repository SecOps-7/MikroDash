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
- **Live traffic chart** — per-interface RX/TX Mbps with configurable history window (1m–30m)
- **System card** — CPU, RAM, Storage gauges with colour-coded thresholds (amber >75%, red >90%), board info, temperature, uptime chip
- **RouterOS update indicator** — shows installed vs available version side by side
- **Network card** — animated SVG topology diagram with live wired/wireless client counts, WAN IP, LAN subnets, and latency chart (ping to 1.1.1.1)
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
| Logs | Live router log stream with severity filter and text search |

### Notifications
- Bell icon in topbar opens an alert history panel showing the last 50 alerts with timestamps
- Browser push notifications (when permitted) for:
  - Interface down / back up
  - WireGuard peer disconnected / reconnected
  - CPU exceeds 90% (1-minute cooldown)
  - 100% ping loss to 1.1.1.1

---

## ⚠️ Security Notice

MikroDash is designed to run **on your local network only**. It has no built-in HTTPS or role-based access control.

**Do not expose MikroDash directly to the internet.** Doing so would allow anyone to:
- View live data from your router (traffic, clients, connections, firewall rules, logs)
- Read your WAN IP, LAN topology, and connected device information
- Monitor your network activity in real time

If you need remote access, place MikroDash **behind an authenticating reverse proxy** (such as Nginx with Basic Auth, Authelia, or Cloudflare Access) or access it exclusively over a VPN.

**Recommended local hardening:**
- Set `BASIC_AUTH_USER` and `BASIC_AUTH_PASS` to require HTTP Basic Auth for the dashboard and Socket.IO endpoint
- Run on a non-default port and bind to your LAN interface only
- Set `chmod 600 .env` to protect your router credentials
- Use a dedicated read-only API user on the router (see RouterOS Setup below)

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
# Edit .env — set ROUTER_HOST, ROUTER_USER, ROUTER_PASS, DEFAULT_IF
```

Run the container:

```bash
docker run -d   --name mikrodash   --restart unless-stopped   --env-file .env   -p 3081:3081   ghcr.io/secops-7/mikrodash:latest
```

Or with Docker Compose — create a `docker-compose.yml`:

```yaml
services:
  mikrodash:
    image: ghcr.io/secops-7/mikrodash:latest
    restart: unless-stopped
    env_file: .env
    ports:
      - "3081:3081"
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
# Edit .env — set ROUTER_HOST, ROUTER_USER, ROUTER_PASS, DEFAULT_IF
docker compose up -d
```

- Dashboard: `http://localhost:3081`
- Health check: `http://localhost:3081/healthz` (`200` only after startup completes and RouterOS is connected)

Source builds require the bundled `node-routeros` compatibility patch. If startup reports a missing patch marker, run `node patch-routeros.js` again before launching MikroDash.

---

## RouterOS Setup

Create a read-only API user (recommended):

```
/ip service set api port=8728 disabled=no
/user group add name=mikrodash policy=read,api,!local,!telnet,!ssh,!ftp,!reboot,!write,!policy,!test,!winbox,!web,!sniff,!sensitive,!romon,!rest-api
/user add name=mikrodash group=mikrodash password=your-secure-password
```

To use API-SSL (TLS) instead, enable the ssl service and set `ROUTER_TLS=true` in your `.env`:

```
/ip service set api-ssl disabled=no port=8729
```

---

## Environment Variables

```env
PORT=3081                    # HTTP port MikroDash listens on
ROUTER_HOST=192.168.88.1     # RouterOS IP or hostname
ROUTER_PORT=8728             # API port (8728 plain, 8729 TLS)
ROUTER_TLS=false             # Set true to use API-SSL
ROUTER_TLS_INSECURE=false    # Skip TLS cert verification (self-signed certs)
ROUTER_USER=mikrodash        # API username
ROUTER_PASS=change-me        # API password
BASIC_AUTH_USER=             # Optional dashboard HTTP Basic Auth username
BASIC_AUTH_PASS=             # Optional dashboard HTTP Basic Auth password
TRUSTED_PROXY=               # Proxy IP to trust X-Forwarded-For from (e.g. 127.0.0.1)
DEFAULT_IF=ether1            # Default interface shown in traffic chart
HISTORY_MINUTES=30           # Traffic chart history window
ROS_WRITE_TIMEOUT_MS=30000   # Force reconnect if a RouterOS command exceeds this time
MAX_SOCKETS=50               # Maximum concurrent Socket.IO clients

# Polling intervals (ms)
CONNS_POLL_MS=3000
KIDS_POLL_MS=3000
DHCP_POLL_MS=15000
LEASES_POLL_MS=15000
ARP_POLL_MS=30000
SYSTEM_POLL_MS=3000
WIRELESS_POLL_MS=5000
VPN_POLL_MS=10000
FIREWALL_POLL_MS=10000
IFSTATUS_POLL_MS=5000
PING_POLL_MS=10000

# Ping target for latency monitor
PING_TARGET=1.1.1.1

# Top-N limits
TOP_N=10
TOP_TALKERS_N=5
FIREWALL_TOP_N=15
MAX_CONNS=20000             # Maximum connection-table rows processed per tick
```

---

## Architecture

### Streamed (router pushes on change — zero poll overhead)
| Data | RouterOS endpoint |
|---|---|
| WAN Traffic RX/TX | `/interface/monitor-traffic` |
| Router Logs | `/log/listen` |
| DHCP Lease changes | `/ip/dhcp-server/lease/listen` |

### Polled (concurrent via tagged API multiplexing)
| Collector | Interval | Data |
|---|---|---|
| System | 3s | CPU, RAM, storage, temp, ROS version |
| Connections | 3s | Firewall connection table, geo-IP |
| Top Talkers | 3s | Kid Control traffic stats |
| Wireless | 5s | Wireless client list |
| Interface Status | 5s | Interface state, IPs, rx/tx bytes |
| VPN | 10s | WireGuard peers, rx/tx rates |
| Firewall | 10s | Rule hit counts |
| Ping | 10s | RTT + packet loss to PING_TARGET |
| DHCP Networks | 15s | LAN subnets, WAN IP |
| DHCP Leases | 15s | Active lease table |
| ARP | 30s | MAC to IP mappings |

All collectors run **concurrently** on a single TCP connection — no serial queuing.

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
