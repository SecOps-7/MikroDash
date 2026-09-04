# Installing MikroDash as a RouterOS container

How to run MikroDash directly on a MikroTik router, using RouterOS's own container
support rather than the **Apps** menu.

Every step here came from a real install ([#124](https://github.com/SecOps-7/MikroDash/issues/124)),
including the two that are easy to get wrong and give you no useful error when you do.

> **Why not the Apps menu?** MikroTik maintain that catalogue themselves, so the entry
> there can lag well behind the current release. Check the version it gives you at
> `http://<container-ip>:3081/healthz`. If it is not the version you expect, install as a
> container using this guide, which always pulls the current image.

---

## Before you start

| | |
|---|---|
| **RouterOS** | 7.4 or later. The syntax below has two forms, split at **7.21**; both are given. |
| **Architecture** | arm, arm64 or x86. MikroDash publishes `linux/amd64`, `linux/arm64` and `linux/arm/v7`. |
| **Package** | The `container` package must be installed (it is a separate download from the RouterOS bundle). |
| **Storage** | An external disk (USB or NVMe). The image is about 180 MB and the router's internal flash is usually too small. |
| **Physical access** | Required once, to enable container mode. See the next step. |

### 1. Enable container mode

This is deliberately awkward: MikroTik require you to prove physical possession of the
router before containers can run at all.

```routeros
/system/device-mode/update container=yes
```

The command will tell you to confirm within about five minutes by **pressing the reset or
mode button, or power-cycling the router**. Do that, and the router reboots with container
support enabled.

### 2. Check DNS is configured

```routeros
/ip/dns/print
```

If no DNS server is set here, the container will not start, and the reason is not obvious
from the logs. Set one if the list is empty.

### 3. Create a dedicated router user for MikroDash

Do not use `admin`. MikroDash only reads, so give it only what it needs:

```routeros
/user/group/add name=mikrodash policy=read,test,api,winbox
/user/add name=mikrodash group=mikrodash password="<choose-a-strong-password>"
```

`read`, `test` and `api` are the minimum for a read-only dashboard. Add `write` only if you
want to use MikroDash's write features (enabling and disabling interfaces, firewall rules,
DHCP leases and so on), and `policy` only if you want it to manage router users.

---

## Networking

The container gets a virtual ethernet interface on its own small subnet, bridged and
routed by the router.

```routeros
# A virtual interface for the container
/interface/veth/add name=veth_mikrodash address=172.16.10.2/24 gateway=172.16.10.1 \
  comment="MikroDash virtual interface"

# A bridge for it to live on, and the router's address on that subnet
/interface/bridge/add name=dock-bridge comment="MikroDash virtual bridge"
/interface/bridge/port/add bridge=dock-bridge interface=veth_mikrodash
/ip/address/add address=172.16.10.1/24 interface=dock-bridge comment="MikroDash gateway"

# Let the container reach the internet (for GeoIP lookups and notifications)
/ip/firewall/nat/add chain=srcnat src-address=172.16.10.0/24 action=masquerade \
  comment="MikroDash internet access"
```

### Enable the API service

```routeros
/ip/service/set api disabled=no port=8728
```

Use `api-ssl` on 8729 instead if you want TLS. With a self-signed certificate you must tick
**Allow self-signed certificate** when adding the router in MikroDash.

### The firewall rule that everyone misses

**This is the step that most often goes wrong, and it produces a bare "timed out".**

MikroDash talking to the router it runs on is traffic arriving at the router itself, so it
lands on the `input` chain. The default RouterOS firewall accepts input only from the LAN
interface list, and `dock-bridge` is not in it. Your API request is dropped in silence.

```routeros
/ip/firewall/filter/add chain=input src-address=172.16.10.0/24 \
  protocol=tcp dst-port=8728 action=accept \
  comment="MikroDash API" \
  place-before=[find chain=input action=drop]
```

The `place-before` matters: added at the end of the chain, the rule sits below the default
drop and never runs.

> **Reading the error tells you which problem you have.** *Timed out* means the packet
> reached nothing, so it is this firewall rule. *Connection refused* means the API service
> is off or on a different port. *Invalid username or password* means the network is fine
> and MikroDash reached RouterOS, so check the credentials.

---

## Persistent storage

**Do not skip this.** Without a `/data` mount, MikroDash's database lives inside the
container's own root directory, and `/container/repull` replaces that directory. Every
update would silently destroy your account, your routers, your history and your backups,
and the app would greet you with the first-run wizard again as if it were new.

**RouterOS 7.21 and later** (`list=` and `mountlists=`):

```routeros
/container/mounts/add list=mikrodash_data src=usb1/mikrodash-data dst=/data
```

**RouterOS 7.20 and earlier** (`name=` and `mounts=`):

```routeros
/container/mounts/add name=mikrodash_data src=usb1/mikrodash-data dst=/data
```

7.21 renamed this: mount `name` became list `name`, and one list can now map several
mounts. Replace `usb1` with your own disk name from `/disk/print`.

---

## Pull and create the container

```routeros
# Where to extract the image. Point this at the external disk, not internal flash.
/container/config/set tmpdir=usb1/tmp
```

**RouterOS 7.21 and later:**

```routeros
/container/add remote-image=ghcr.io/secops-7/mikrodash:latest \
  interface=veth_mikrodash root-dir=usb1/mikrodash \
  mountlists=mikrodash_data \
  logging=yes start-on-boot=yes comment="MikroDash"
```

**RouterOS 7.20 and earlier:** the same, with `mounts=mikrodash_data`.

Then start it and watch it come up:

```routeros
/container/print
/container/start [find comment="MikroDash"]
/log/print follow where topics~"container"
```

The first pull takes a few minutes. `/container/print` shows `status=running` when it is
ready.

### No environment variables are needed

MikroDash is configured entirely through its own web interface. `ROUTEROS_HOST`,
`ROUTEROS_USER`, `ROUTEROS_PASSWORD` and `ROUTER_*` are **ignored**: older guides set them,
and a blank `ROUTER_PASS` used to produce a confusing "username or password is invalid"
banner. Leave them out.

---

## Reaching the web interface

The container listens on port **3081**. The simplest route is to browse straight to it:

```
http://172.16.10.2:3081
```

If your LAN cannot route to that subnet, forward the port from the router's own address
instead:

```routeros
/ip/firewall/nat/add chain=dstnat in-interface-list=LAN protocol=tcp dst-port=3081 \
  action=dst-nat to-addresses=172.16.10.2 to-ports=3081 \
  comment="MikroDash web access"
```

> **Keep `in-interface-list=LAN` on that rule.** Without it, the rule also matches traffic
> arriving on your WAN, and because the default forward chain permits anything that has
> been destination-NATed, **MikroDash would be reachable from the internet on port 3081**.
> The `LAN` restriction confines it to your own network.

Then open `http://<router-lan-ip>:3081`.

---

## First run

1. Open the address in a browser. The setup wizard appears.
2. Create your administrator account.
3. The add-router form opens immediately after. Enter:
   - **Host**: `172.16.10.1` (the router's address on the container bridge, not your LAN gateway)
   - **Port**: `8728`, TLS off. Use `8729` with TLS on for `api-ssl`.
   - **Username / password**: the `mikrodash` user you created
4. Use **Test Connection** before saving.

---

## Updating

```routeros
/container/stop [find comment="MikroDash"]
/container/repull [find comment="MikroDash"]
/container/start [find comment="MikroDash"]
```

With the `/data` mount in place, your account, routers, history and settings survive this.
If it asks you to create an account again, the mount is missing: check
`/container/mounts/print` and confirm the container references the list.

Confirm the running version:

```
http://172.16.10.2:3081/healthz
```

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| **"Timed out" when adding the router** | The `input` chain firewall rule is missing or sits below the default drop. |
| **"Connection refused"** | The API service is disabled, or on a different port. Check `/ip/service/print`. |
| **"Invalid username or password"** | The network is fine. Check the credentials, and that the user's group has `api` and `read`. |
| **Container will not start** | No DNS in `/ip/dns`, or `tmpdir` pointing at internal flash with too little space. Check `/log/print where topics~"container"`. |
| **Asked to create an account after every update** | No `/data` mount. See Persistent storage above. |
| **Image will not pull** | RouterOS defaults `registry-url` to `https://lscr.io/`. It normally detects `ghcr.io` from the image name; if it does not, set it explicitly with `/container/config/set registry-url=https://ghcr.io`. |
| **Wrong version after Update in the Apps menu** | That is MikroTik's catalogue copy, not this image. Install as a container using this guide. |

To start over completely:

```routeros
/container/stop [find comment="MikroDash"]
/container/remove [find comment="MikroDash"]
```

Delete the `usb1/mikrodash-data` directory as well if you want to discard the database too.

---

## Security notes

Worth being deliberate about, since this runs on your router:

- **Give the router user only `read`, `test` and `api`** unless you actually want MikroDash's write features.
- **Keep the web interface off the internet.** Use `in-interface-list=LAN` on any dstnat rule, as above.
- **Container mode weakens the router's security model.** MikroTik say so plainly in their own documentation: a container runs code on your router, and your router is then only as secure as what runs inside it. That applies to MikroDash exactly as it does to anything else.
