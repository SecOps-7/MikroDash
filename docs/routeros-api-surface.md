# RouterOS API surface

**Generated from the Node source, which no longer exists — the generator was deleted
with the port-parity harness on 2026-09-01. This file is now maintained BY HAND from
the RouterOS documentation; see the mikrotik-docs skill.**

Rows added by hand cite the Go file that declares the command, not the deleted
Node one. The nine `/caps-man` reads below are the legacy CAPsMAN tree, added on
2026-09-13 — its property names are the half MikroTik's documentation does not
enumerate, so the three status menus are read whole and the profile menus carry
proplists checked against a live manager's own export.

Every RouterOS command MikroDash issues, derived from the source. This is the input list
for the fixture capture (plan A1), the specification for the Go client, and the checklist
for what a ported collector has to cover.

| Kind | Count |
|---|---|
| read | 75 |
| stream | 16 |
| write | 12 |
| action | 16 |
| menu | 34 |
| distinct proplists | 70 |

## Reads

| Command | Used by |
|---|---|
| `/caps-man/channel/print` | internal/collect/capsman.go, wifi.go, wireless.go |
| `/caps-man/configuration/print` | internal/collect/capsman.go, wifi.go, wireless.go |
| `/caps-man/datapath/print` | internal/collect/capsman.go, wifi.go |
| `/caps-man/interface/print` | internal/collect/capsman.go, wifi.go, wireless.go |
| `/caps-man/manager/print` | internal/collect/capsman.go |
| `/caps-man/provisioning/print` | internal/collect/capsman.go |
| `/caps-man/radio/print` | internal/collect/capsman.go |
| `/caps-man/registration-table/print` | internal/collect/capsman.go, wifi.go, wireless.go |
| `/caps-man/remote-cap/print` | internal/collect/capsman.go |
| `/caps-man/security/print` | internal/collect/capsman.go, wifi.go |
| `/file/print` | src/backups/runner.js |
| `/interface/bridge/host/print` | src/collectors/bridges.js, src/collectors/topology.js |
| `/interface/bridge/port/print` | src/collectors/bridges.js, src/collectors/vlans.js |
| `/interface/bridge/print` | src/collectors/bridges.js |
| `/interface/bridge/vlan/print` | src/collectors/vlans.js |
| `/interface/detect-internet/state/print` | src/collectors/dhcpNetworks.js, src/collectors/wan.js |
| `/interface/ethernet/print` | src/collectors/interfaceStatus.js |
| `/interface/pppoe-server/server/print` | src/collectors/ppp.js |
| `/interface/print` | src/collectors/interfaceStatus.js, src/collectors/interfaces.js, src/collectors/wan.js |
| `/interface/vlan/print` | src/collectors/dhcpLeases.js, src/collectors/topology.js, src/collectors/vlans.js |
| `/interface/wifi/cap/print` | src/collectors/capsman.js |
| `/interface/wifi/capsman/print` | src/collectors/capsman.js |
| `/interface/wifi/capsman/remote-cap/print` | src/collectors/capsman.js, src/collectors/topology.js, src/index.js |
| `/interface/wifi/channel/print` | src/routeros/wifiMenus.js |
| `/interface/wifi/configuration/print` | src/routeros/wifiMenus.js |
| `/interface/wifi/datapath/print` | src/routeros/wifiMenus.js |
| `/interface/wifi/monitor` | src/index.js |
| `/interface/wifi/print` | src/collectors/capsman.js, src/collectors/topology.js, src/collectors/wifi.js, src/collectors/wireless.js |
| `/interface/wifi/provisioning/print` | src/collectors/capsman.js, src/routeros/wifiMenus.js |
| `/interface/wifi/radio/print` | src/collectors/capsman.js, src/collectors/wifi.js |
| `/interface/wifi/registration-table/print` | src/collectors/capsman.js, src/collectors/topology.js, src/collectors/wifi.js, src/collectors/wireless.js |
| `/interface/wifi/security/print` | src/routeros/wifiMenus.js |
| `/interface/wireguard/peers/print` | src/collectors/vpn.js |
| `/interface/wireless/print` | src/collectors/topology.js, src/collectors/wifi.js, src/collectors/wireless.js |
| `/interface/wireless/registration-table/print` | src/collectors/topology.js, src/collectors/wifi.js, src/collectors/wireless.js |
| `/interface/wireless/security-profiles/print` | src/collectors/wifi.js |
| `/ip/address/print` | src/collectors/dhcpNetworks.js, src/collectors/interfaceStatus.js, src/collectors/wan.js, src/index.js |
| `/ip/arp/print` | src/collectors/arp.js |
| `/ip/dhcp-client/print` | src/collectors/wan.js, src/index.js |
| `/ip/dhcp-server/lease/print` | src/collectors/dhcpLeases.js |
| `/ip/dhcp-server/network/print` | src/collectors/dhcpNetworks.js |
| `/ip/dhcp-server/print` | src/collectors/dhcpLeases.js |
| `/ip/dns/print` | src/collectors/dns.js |
| `/ip/dns/static/print` | src/collectors/dns.js |
| `/ip/firewall/connection/print` | src/collectors/connections.js |
| `/ip/firewall/filter/print` | src/collectors/firewall.js |
| `/ip/firewall/mangle/print` | src/collectors/firewall.js |
| `/ip/firewall/nat/print` | src/collectors/firewall.js |
| `/ip/firewall/raw/print` | src/collectors/firewall.js |
| `/ip/ipsec/active-peers/print` | src/collectors/vpn.js |
| `/ip/ipsec/installed-sa/print` | src/collectors/vpn.js |
| `/ip/kid-control/device/print` | src/collectors/talkers.js |
| `/ip/neighbor/discovery-settings/print` | src/collectors/topology.js |
| `/ip/neighbor/print` | src/collectors/topology.js |
| `/ip/pool/print` | src/collectors/dhcpNetworks.js |
| `/ip/route/print` | src/collectors/routing.js, src/collectors/wan.js, src/index.js |
| `/ipv6/route/print` | src/collectors/routing.js |
| `/log/print` | src/collectors/logs.js |
| `/ppp/active/print` | src/collectors/ppp.js, src/collectors/vpn.js |
| `/ppp/profile/print` | src/collectors/ppp.js |
| `/queue/simple/print` | src/collectors/queues.js |
| `/queue/tree/print` | src/collectors/queues.js |
| `/routing/bgp/session/print` | src/collectors/routing.js |
| `/system/health/print` | src/collectors/system.js |
| `/system/identity/print` | internal/server/routers_identity.go |
| `/system/license/print` | src/collectors/system.js |
| `/system/package/print` | src/collectors/packages.js |
| `/system/package/update/print` | src/collectors/packages.js, src/collectors/system.js, src/index.js |
| `/system/resource/print` | src/backups/runner.js, src/collectors/system.js, src/index.js |
| `/system/routerboard/print` | src/backups/runner.js, src/collectors/packages.js, src/collectors/system.js, src/index.js |
| `/system/routerboard/settings/print` | internal/collect/packages.go, internal/server/packages.go |
| `/tool/netwatch/print` | src/collectors/netwatch.js |
| `/user/active/print` | src/collectors/rosusers.js, src/index.js |
| `/user/group/print` | src/collectors/rosusers.js, src/index.js |
| `/user/print` | src/collectors/rosusers.js, src/index.js |
| `/user/settings/print` | src/collectors/rosusers.js |

## Streams (`/listen`)

| Command | Used by |
|---|---|
| `/interface/bridge/port/listen` | src/collectors/bridges.js, src/collectors/util.js |
| `/interface/vlan/listen` | src/collectors/vlans.js |
| `/interface/wifi/capsman/remote-cap/listen` | src/collectors/capsman.js |
| `/interface/wifi/listen` | src/collectors/wifi.js |
| `/interface/wireguard/peers/listen` | src/collectors/vpn.js |
| `/interface/wireless/listen` | src/collectors/wifi.js |
| `/ip/arp/listen` | src/collectors/arp.js |
| `/ip/dhcp-server/lease/listen` | src/collectors/dhcpLeases.js |
| `/ip/route/listen` | src/collectors/routing.js, src/collectors/wan.js |
| `/ipv6/route/listen` | src/collectors/routing.js |
| `/log/listen` | src/collectors/logs.js |
| `/ppp/active/listen` | src/collectors/ppp.js |
| `/queue/simple/listen` | src/collectors/queues.js |
| `/queue/tree/listen` | src/collectors/queues.js |
| `/routing/bgp/session/listen` | src/collectors/routing.js |
| `/tool/netwatch/listen` | src/collectors/netwatch.js |

## Actions

| Command | Used by |
|---|---|
| `/file/read` | src/backups/runner.js |
| `/interface/wifi/frequency-scan` | src/wifiScan.js |
| `/ip/dhcp-client/release` | src/index.js |
| `/ip/dhcp-client/renew` | src/index.js |
| `/system/backup/load` | src/index.js |
| `/system/backup/save` | src/backups/runner.js |
| `/system/package/apply-changes` | src/index.js |
| `/system/package/update/check-for-updates` | src/collectors/system.js, src/index.js |
| `/system/package/update/install` | src/index.js |
| `/system/reboot` | internal/server/packages.go |
| `/system/routerboard/upgrade` | internal/server/packages.go |
| `/tool/bandwidth-test` | internal/diag/btest.go (Tools page) |
| `/tool/fetch` | src/index.js |
| `/tool/ping` | src/collectors/ping.js, src/collectors/topology.js, internal/diag/ping.go (Tools page) |
| `/tool/torch` | internal/diag/torch.go (Tools page) |
| `/tool/traceroute` | internal/diag/traceroute.go (Tools page) |

## Writes issued from a literal path

| Command | Used by |
|---|---|
| `/file/remove` | src/backups/runner.js |
| `/queue/simple/move` | src/index.js |
| `/system/identity/set` | internal/server/routers_identity.go |
| `/system/package/disable` | src/index.js |
| `/system/package/enable` | src/index.js |
| `/system/routerboard/settings/set` | internal/server/packages.go |
| `/user/active/remove` | src/index.js |
| `/user/add` | src/index.js |
| `/user/group/add` | src/index.js |
| `/user/group/remove` | src/index.js |
| `/user/group/set` | src/index.js |
| `/user/remove` | src/index.js |
| `/user/set` | src/index.js |

## Bare menus (a verb is appended at runtime)

| Command | Used by |
|---|---|
| `/interface` | src/routeros/resources.js |
| `/interface/bridge` | src/routeros/resources.js |
| `/interface/bridge/port` | src/routeros/resources.js |
| `/interface/monitor-traffic` | src/collectors/interfaceStatus.js, src/collectors/traffic.js |
| `/interface/veth` | src/routeros/resources.js |
| `/interface/vlan` | src/routeros/resources.js |
| `/interface/wifi` | src/routeros/resources.js |
| `/interface/wifi/channel` | src/routeros/resources.js |
| `/interface/wifi/configuration` | src/routeros/resources.js |
| `/interface/wifi/datapath` | src/routeros/resources.js |
| `/interface/wifi/provisioning` | src/routeros/resources.js |
| `/interface/wifi/security` | src/routeros/resources.js |
| `/interface/wireguard` | src/routeros/resources.js |
| `/interface/wireguard/peers` | src/routeros/resources.js |
| `/interface/wireless` | src/routeros/resources.js |
| `/interface/wireless/security-profiles` | src/routeros/resources.js |
| `/ip/dhcp-server` | src/routeros/resources.js |
| `/ip/dhcp-server/lease` | src/routeros/resources.js |
| `/ip/dns/static` | src/routeros/resources.js |
| `/ip/firewall/filter` | src/routeros/fwGuard.js, src/routeros/resources.js |
| `/ip/firewall/mangle` | src/routeros/resources.js |
| `/ip/firewall/nat` | src/routeros/resources.js |
| `/ip/firewall/raw` | src/routeros/fwGuard.js, src/routeros/resources.js |
| `/ip/route` | src/routeros/resources.js |
| `/ipv6/route` | src/routeros/resources.js |
| `/login` | src/index.js |
| `/login.html` | src/index.js |
| `/login.js` | src/index.js |
| `/logo.png` | src/index.js |
| `/queue/simple` | src/index.js |
| `/queue/tree` | src/index.js |
| `/routing/table` | src/routeros/resources.js |
| `/system/package/uninstall` | src/index.js |
| `/system/package/unschedule` | src/index.js |

## Composed at runtime — the resource engine

These menus never appear as a complete command in the source: `res:save` and friends
build `<menu>/<verb>` from the registry. Listed from `src/routeros/resources.js` so the
surface stays complete.

Every write reads the one row it is about, by id, before and after it (`<menu>/print
?.id=<id>`, `readRow` in `internal/server/resource.go`); a create learns its row's id
from the add's `!done =ret=`. Only a settings menu (no id), a reorder and the Wi-Fi
inherit guard read the whole menu. Before 2026-09-18 every write read the whole menu
twice, 37,111 rows each way for one address-list entry on a synced blocklist.

| Menu | Resource | Page | Verbs |
|---|---|---|---|
| `/certificate` | certificate | certificates | set, remove |
| `/snmp` | snmp | snmp | set |
| `/snmp/community` | snmpCommunity | snmp | add, set, remove |
| `/system/clock` | clock | clock | set |
| `/system/logging` | logRule | logging | add, set, remove |
| `/system/logging/action` | logAction | logging | add, set, remove |
| `/system/ntp/client` | ntpClient | ntp-client | set |
| `/system/ntp/client/servers` | ntpServer | ntp-client | add, set, remove |
| `/system/scheduler` | scheduler | scheduler | add, set, remove |
| `/system/script` | script | scripts | add, set, remove, run |
| `/file` | file | files | remove |
| `/interface` | iface | interfaces | set (comment, disabled) |
| `/interface/bridge` | bridge | bridges | add, set, remove |
| `/interface/bridge/port` | bridgePort | bridges | add, set, remove |
| `/interface/list` | ifList | interface-lists | add, set, remove |
| `/interface/list/member` | ifListMember | interface-lists | add, set, remove |
| `/interface/veth` | veth | interfaces | add, set, remove |
| `/interface/vlan` | vlan | vlans | add, set, remove |
| `/interface/wifi` | wifiNet | wifi | add, set, remove, enable, disable |
| `/interface/wifi/channel` | capsChannel | capsman | add, set, remove |
| `/interface/wifi/configuration` | capsConfig | capsman | add, set, remove |
| `/interface/wifi/datapath` | capsDatapath | capsman | add, set, remove |
| `/interface/wifi/provisioning` | capsProvisioning | capsman | add, set, remove, move, enable, disable |
| `/interface/wifi/security` | capsSecurity | capsman | add, set, remove |
| `/interface/ovpn-server/server` | ovpnServer | openvpn | add, set, remove |
| `/interface/ovpn-client` | ovpnClient | openvpn | add, set, remove (selfPath, tunnelDefault; password never read) |
| `/interface/pppoe-client` | pppoeClient | pppoe-clients | add, set, remove (selfPath, tunnelDefault; password never read) |
| `/container` | container | containers | add, set, remove, start, stop (codeGate on image, cmd, entrypoint) |
| `/container/envs` | containerEnv | containers | add, set, remove (value never read) |
| `/container/mounts` | containerMount | containers | add, set, remove |
| `/container/config` | containerConfig | containers | set (singleton; password never read) |
| `/interface/veth` | veth | containers | add, set, remove (selfPath) |
| `/interface/vrrp` | vrrp | vrrp | add, set, remove (selfPath, codeGate on the scripts; password never read) |
| `/interface/wireguard/peers/show-client-config` | — | wireguard | read (the client config and its QR; `.id` not `numbers`; `show-sensitive` draws the QR) |
| `/interface/wireguard` | wgInterface | wireguard | add, set, remove (selfPath; private-key never read) |
| `/interface/wireguard/peers` | wgPeer | wireguard | add, set, remove (private-key and preshared-key never read) |
| `/interface/wireless` | wlNet | wifi | add, set, remove, enable, disable |
| `/interface/wireless/security-profiles` | wlSecProfile | wifi | add, set, remove |
| `/ip/dhcp-client` | dhcpClient | dhcp-clients | add, set, remove (dhcpClientPath, tunnelDefault, codeGate on the script) |
| `/ip/dhcp-server` | dhcpServer | dhcp-servers | add, set, remove (codeGate on the lease script) |
| `/ip/dhcp-server/network` | dhcpNetwork | dhcp-servers | add, set, remove |
| `/ip/dhcp-server/lease` | dhcpLease | dhcp | add, set, remove, make-static |
| `/ip/address` | ipAddress | ip-addresses | add, set, remove |
| `/ip/ipsec/peer` | ipsecPeer | ipsec | add, set, remove (guarded by ipsecPath; ppk-secret never read) |
| `/ip/ipsec/identity` | ipsecIdentity | ipsec | add, set, remove (guarded by ipsecPath; secret and password never read) |
| `/ip/ipsec/policy` | ipsecPolicy | ipsec | add, set, remove, move (guarded by ipsecPath) |
| `/ip/pool` | ipPool | ip-pools | add, set, remove |
| `/ip/service` | ipService | ip-services | set |
| `/ip/dns/static` | dnsStatic | dns | add, set, remove |
| `/ip/firewall/address-list` | addressList | address-lists | add, set, remove; the page polls `print =.proplist=list,dynamic,disabled` for its per-list summary and reads one list with `print ?list=<name>` when it is opened; the assistant's `list_addressList` reads the same two ways |
| `/ip/firewall/filter` | fwFilter | firewall | add, set, remove, move, enable, disable |
| `/ip/firewall/mangle` | fwMangle | firewall | add, set, remove, move, enable, disable |
| `/ip/firewall/nat` | fwNat | firewall | add, set, remove, move, enable, disable |
| `/ip/firewall/raw` | fwRaw | firewall | add, set, remove, move, enable, disable |
| `/ip/route` | route | routing | add, set, remove |
| `/routing/ospf/instance` | ospfInstance | ospf | add, set, remove |
| `/routing/ospf/area` | ospfArea | ospf | add, set, remove (no-summaries is a presence flag) |
| `/routing/ospf/interface-template` | ospfTemplate | ospf | add, set, remove, move (passive is a presence flag; auth-key never read) |
| `/routing/ospf/neighbor` | ospfNeighbor | ospf | read only |
| `/routing/rule` | routingRule | routing-rules | add, set, remove, move (guarded by rulePath) |
| `/routing/table` | routingTable | routing-tables | add, set, remove (fib is a presence flag, cleared with `!fib`; tableInUse reads `/routing/rule` before a remove, disable or FIB unset) |
| `/tool/netwatch` | netwatch | netwatch | add, set, remove (no scripts, no DNS record type) |
| `/ipv6/address` | ipv6Address | ip-addresses | add, set, remove |
| `/ipv6/route` | route6 | routing | add, set, remove |


## The Security Scan's reads (internal/secscan)

Added by hand on 2026-09-19, and kept out of the counts above, which describe the
Node-era surface. The Security Scan page reads these once per scan, on demand, one
at a time (`internal/server/secscan.go`), each with the proplist below, which names
no credential. Every path and property was read off CHR Test and hAP AC2 (RouterOS
7.24.3) before it was written here. A menu the router does not have (no wireless
package, no RouterBOARD on a CHR) answers "no such command" and its checks report
unknown. Only the firewall tables are read whole: a rule's matchers are the
question, and any property can be one.

The Dashboard's Security Score card and the assistant's `security_scan` tool run
the same scan through the same path (`runClaimedScan`), so they read exactly these
menus, with these proplists. The card scans only when its router has no report;
the tool scans on every call, or waits for a scan already running.

| Command | Proplist |
|---|---|
| `/ip/service/print` | `=.proplist=name,port,disabled,dynamic,available-from,address,certificate` |
| `/tool/mac-server/print` | `=.proplist=allowed-interface-list` |
| `/tool/mac-server/mac-winbox/print` | `=.proplist=allowed-interface-list` |
| `/tool/mac-server/ping/print` | `=.proplist=enabled` |
| `/ip/neighbor/discovery-settings/print` | `=.proplist=discover-interface-list` |
| `/tool/romon/print` | `=.proplist=enabled` |
| `/tool/bandwidth-server/print` | `=.proplist=enabled,authenticate` |
| `/ip/ssh/print` | `=.proplist=strong-crypto,forwarding-enabled` |
| `/ip/firewall/filter/print` | (whole row) |
| `/ipv6/firewall/filter/print` | (whole row) |
| `/ipv6/settings/print` | `=.proplist=disable-ipv6` |
| `/ip/settings/print` | `=.proplist=rp-filter,tcp-syncookies` |
| `/ip/dns/print` | `=.proplist=allow-remote-requests` |
| `/interface/list/member/print` | `=.proplist=interface,list,disabled` |
| `/ip/upnp/print` | `=.proplist=enabled` |
| `/ip/socks/print` | `=.proplist=enabled,auth-method` |
| `/ip/proxy/print` | `=.proplist=enabled` |
| `/ip/smb/print` | `=.proplist=enabled,status` |
| `/interface/pptp-server/server/print` | `=.proplist=enabled` |
| `/interface/l2tp-server/server/print` | `=.proplist=enabled,use-ipsec` |
| `/snmp/print` | `=.proplist=enabled` |
| `/snmp/community/print` | `=.proplist=name,addresses,security,write-access,disabled` |
| `/ip/cloud/print` | `=.proplist=ddns-enabled` |
| `/user/print` | `=.proplist=name,group,address,disabled` |
| `/user/group/print` | `=.proplist=name,policy` |
| `/user/settings/print` | `=.proplist=minimum-password-length` |
| `/user/active/print` | `=.proplist=name,via` |
| `/system/package/update/print` | `=.proplist=installed-version,latest-version,status` |
| `/system/routerboard/print` | `=.proplist=routerboard,current-firmware,upgrade-firmware` |
| `/system/ntp/client/print` | `=.proplist=enabled` |
| `/system/logging/print` | `=.proplist=action,disabled` |
| `/system/logging/action/print` | `=.proplist=name,target,remote` |
| `/system/script/print` | `=.proplist=name,dont-require-permissions` |
| `/system/device-mode/print` | `=.proplist=mode` |
| `/interface/wifi/security/print` | `=.proplist=name,authentication-types,encryption,wps` |
| `/interface/wireless/security-profiles/print` | `=.proplist=name,mode,authentication-types,unicast-ciphers` |
| `/certificate/print` | `=.proplist=name,invalid-after` |

## The Containers page's app store (internal/server/apps.go)

Added by hand on 2026-09-19, and kept out of the counts above, like the Security
Scan's. RouterOS 7.21 and later; read and written on CHR Test (7.24.3) before it
was written here. The store is read when the Apps tab is shown, never polled; an
change then reads that one app's row every 2 s until it reaches its goal
(running, stopped, or removed for a cleanup), fails or 10 minutes pass. Stopped and
not installed are told apart by `interface` (`none` when not installed): 7.24.3
keeps a cleaned-up app's `app-size`, although the docs say cleanup empties it. The `/app` rows also carry `secrets` and the compose
`yaml`, and no proplist here names either (`TestTheAppStoreReadsNoSecret`).
`default-credentials` is the catalog's published first login, the same on every
router. A router with no `/app` answers "no such command" and the tab says the
store is not available.

| Command | Proplist or arguments |
|---|---|
| `/app/print` | `=.proplist=.id,name,category,description,project-page,default-credentials,default-network,firewall-redirects,disabled,running,status,ui-url,interface,app-size,data-size,memory-current,cpu-usage,custom` |
| `/app/print` (the target, before a change) | `=.proplist=.id,name ?name=<app>` |
| `/app/print` (following a change) | `=.proplist=name,disabled,running,status,ui-url,interface ?name=<app>` |
| `/app/settings/print` | `=.proplist=disk,lan-bridge,assumed-lan-bridge,router-ip,assumed-router-ip` |
| `/disk/print` | `=.proplist=slot,fs,mounted,free,size` |
| `/interface/bridge/print` | `=.proplist=name` |
| `/ip/cloud/print` | `=.proplist=dns-name` |
| `/app/set` (before an install) | `=numbers=<id> =use-https=yes\|no`: yes only when IP Cloud has a DNS name, since without one the app waits for a reverse proxy that never comes |
| `/app/enable` | `=numbers=<id>`: install (download, network, firewall, start) or start |
| `/app/disable` | `=numbers=<id>`: stop |
| `/app/restart` | `=numbers=<id>` |
| `/app/cleanup` | `=numbers=<id>`: remove, deleting the app's data; needs the name typed |
| `/app/settings/set` | `=disk=<slot>` and `=lan-bridge=<bridge>`, each one the router offered |

## Config Management's import path (measured by cmd/importprobe)

Added by hand on 2026-09-21, and kept out of the counts above. Nothing in the app
issues these yet: they were MEASURED on CHR Test (7.24.4) before any deploy code
was written, because the documentation contradicts itself on two of them. The raw
sentences are in `testdata/fixtures/import/probe-7.24.4.json` and
`oversize-write-7.24.4.json`.

| Command | Arguments, and what was measured |
|---|---|
| `/file/add` | `=name=<n> =type=file`. `!done` carries the new file's id in `ret` |
| `/file/set` | `=.id=<id> =contents=<text>`. **60416 bytes accepted; 61440 refused** with the clean trap `failure: contents too long`. **102400 bytes gets NO trap: the router closes the API connection** (EOF) and every later command on it fails. The cap must be enforced before sending, and a template larger than it is split |
| `/file/print` | `?name=<n>`, for the id and `size` |
| `/file/remove` | `=.id=<id>`, or `=numbers=<name>`: removing by NAME works over the API (no trap, file gone; `m10`, 7.24.4). `backups.Sweep` removes that way, and Config Management's sweep shares it |
| `/import` | `=file-name=<n>`. **The real run is `verbose=no`.** It returns when finished, not before (600 lines, 266 ms, all applied at return). A **runtime** error stops the file at that line with the lines before it applied, and the trap names the line and the command: `input does not match any value of list (/interface/list/member/add (list); line 5)`. `verbose=yes` stops at the same line but its trap drops the line number. A **syntax** error applies nothing: the whole file is parsed first, and the trap is `expected name value (line 7 column 1)` |
| `/import` (dry-run) | `=file-name=<n> =verbose=yes =dry-run=`, or `=dry-run=yes` — both accepted over the API. **Returns no text over the API**: success is `!done` `ret=true`, a syntax error is the trap `found 1 error(s) in import file` with no line. It checks **syntax only** — a reference to a list that does not exist passes — so it cannot see whether an earlier line creates what a later one uses |
| `/execute` | `=script=/import file-name=<n> verbose=yes dry-run =file=<out>`: the ONLY way to get the dry-run's own words back. Writes the console report to `<out>.txt` (CRLF line endings) with `#line N` markers and the error with its line and column. **Console syntax, not API syntax**: in this string `dry-run` is a bare flag, and `dry-run=yes` is "expected end of command" |
| (tag cancel) | Cancelling `/import`'s API tag **stops it mid-file and leaves it half-applied**: 180 of 950 lines after an 80 ms cancel, stable thereafter. A real import is never cancelled on a short timer, and a timeout is recorded as a partial outcome |
| (firewall settle) | Measured by `m14` (7.24.4, `probe-7.24.4-m14.json`): an `/import` that **removes a filter rule and adds its replacement returns before the new rule is in force**. New logins from the address it drops got in at 0, 300 and 700 ms and were dropped from 1 s on, three runs alike; a plain add (the control) is in force at once. A deploy's fresh login therefore waits `firewallSettle` (5 s) after the import: every canned lock-class template opens with exactly that remove, and a login inside the hole disarmed a dead-man over a real lockout |
| `/system/scheduler/add` | `=name=<n> =interval=15s =on-event=/import file-name=<undo>`: the dead-man revert. **First fires after one interval, not at creation** (15 s), and its `/import` of the undo file ran with the API user's rights |
| `/system/scheduler/add` (backup load) | `=interval=20s =on-event=/system backup load name=<n>.backup password=<one-time>`: a dead-man that reverts by loading a backup taken just before the change (`m11`, 7.24.4, destructive). **It runs from a scheduler with no prompt to answer**: fired at 20 s, the router was back at 35 s holding the pre-change state (a marker made before the backup present, one made after gone), and **the scheduler was gone**, because it was added after the backup. The backup file itself remains and must be swept |
| (run-after-reset bootstrap) | Measured by `m12` (7.24.4, destructive): each line wrapped in `:do { <cmd> } on-error={ :log warning "<prefix>bootstrap: line N failed" }` **survives a failing line and logs which one** (the DHCP client a CHR keeps through a reset failed on purpose, and every later line ran); `/certificate add`, `/certificate sign` and `/ip service set api-ssl certificate=` **work inside the script**, so the router came back over api-ssl in 25 s; a closing `:log info "<prefix>bootstrap: done"` marks that the script reached its end. Reset with `no-defaults=yes skip-backup=yes keep-users=yes` |
| `/user/active/print` | Rows carry `address`, `via`, `group`, `name`, `radius`, `when`: `address` is where MikroDash arrives from, as the router sees it |
| `/system/reset-configuration` | `=no-defaults=yes =skip-backup=yes =keep-users=yes =run-after-reset=<file>` (`export-reset-7.24.4.json`). The connection drops; the CHR was back in 20 s. **`keep-users=yes` keeps MikroDash's login, password and all**, so a credential never has to be written into the script. The script **may remove its own file on its first line** — it is loaded before it runs. **A reset destroys certificates**: api-ssl came back enabled with `certificate=none`, and TLS clients got `ssl: no common ciphers`. **The first runtime error aborts the rest of the script**, and nothing reports it at the time: it is only in `/log` afterwards, `system,error,critical: error while running run-after-reset script: <msg> (<cmd>; line N)`. On a CHR a DHCP client on ether1 survives a `no-defaults` reset, so a script adding one fails at that line |
| `/system/backup/save` | `=name=<n> =dont-encrypt=yes` (lab only) — the way back before a reset |
| `/system/backup/load` | `=name=<n>.backup =password=`: restored the lab in 10 s, original certificate included |

## Proplists

A proplist is the only thing keeping a credential out of a payload — see
`src/routeros/wifiMenus.js`. Every one of these is part of the port contract.

| Proplist | Used by |
|---|---|
| `=.proplist=.id` | src/index.js |
| `=.proplist=.id,.dead,address,active-address,mac-address,active-mac-address,status,comment,host-name,server,dynamic` | src/collectors/dhcpLeases.js |
| `=.proplist=.id,address,identity,board-name,serial,version,base-mac,common-name,state,connected-time,uptime` | src/collectors/capsman.js |
| `=.proplist=.id,bridge,interface,pvid,frame-types,disabled` | src/collectors/vlans.js |
| `=.proplist=.id,bridge,interface,pvid,role,edge,learn,horizon,path-cost,frame-types,disabled,inactive,dynamic` | src/collectors/bridges.js |
| `=.proplist=.id,bridge,vlan-ids,tagged,untagged,current-tagged,dynamic,disabled` | src/collectors/vlans.js |
| `=.proplist=.id,disabled,dynamic,chain,action,comment,src-address,dst-address,protocol,dst-port,in-interface,packets,bytes` | src/collectors/firewall.js |
| `=.proplist=.id,dst-address,gateway,distance,active,dynamic` | src/collectors/wan.js |
| `=.proplist=.id,dst-address,gateway,distance,comment,.flags,active,static,dynamic,connect,bgp,ospf,disabled` | src/collectors/routing.js |
| `=.proplist=.id,interface,status,address,gateway,primary-dns,secondary-dns,expires-after,dhcp-server,disabled,invalid` | src/collectors/wan.js |
| `=.proplist=.id,name,address,type,ttl,disabled,comment,regexp,cname,forward-to,text,mx-exchange,ns,srv-target` | src/collectors/dns.js |
| `=.proplist=.id,name,authentication-types,wps,ft,ft-over-ds,connect-priority,disabled,comment` | src/routeros/wifiMenus.js |
| `=.proplist=.id,name,band,frequency,width,secondary-frequency,skip-dfs-channels,disabled,comment` | src/routeros/wifiMenus.js |
| `=.proplist=.id,name,bridge,vlan-id,client-isolation,local-forwarding,traffic-processing,disabled,comment` | src/routeros/wifiMenus.js |
| `=.proplist=.id,name,default-name,disabled,running,master-interface,radio-mac,mac-address,configuration,configuration.ssid,configuration.mode,configuration.hide-ssid,configuration.country,configuration.manager,security,security.authentication-types,channel,channel.band,channel.frequency,channel.width,datapath,datapath.bridge,datapath.vlan-id,comment,dynamic` | src/collectors/wifi.js |
| `=.proplist=.id,name,default-name,disabled,running,ssid,mode,band,frequency,channel-width,security-profile,master-interface,hide-ssid,vlan-id,vlan-mode,mac-address,comment,dynamic` | src/collectors/wifi.js |
| `=.proplist=.id,name,group,address,comment,disabled,expired,last-logged-in,inactivity-timeout,inactivity-policy` | src/collectors/rosusers.js |
| `=.proplist=.id,name,local-address,remote-address,rate-limit,only-one,use-encryption` | src/collectors/ppp.js |
| `=.proplist=.id,name,mode,authentication-types,default` | src/collectors/wifi.js |
| `=.proplist=.id,name,policy,skin,comment` | src/collectors/rosusers.js |
| `=.proplist=.id,name,protocol-mode,vlan-filtering,igmp-snooping,dhcp-snooping,fast-forward,priority,ageing-time,mac-address,actual-mtu,mtu,running,disabled,comment` | src/collectors/bridges.js |
| `=.proplist=.id,name,service,caller-id,address,uptime,encoding,session-id,limit-bytes-in,limit-bytes-out,bytes-in,bytes-out` | src/collectors/ppp.js |
| `=.proplist=.id,name,ssid,mode,country,hide-ssid,security,channel,datapath,manager,disabled,comment` | src/routeros/wifiMenus.js |
| `=.proplist=.id,name,state,state-change-time` | src/collectors/wan.js |
| `=.proplist=.id,name,type,running,disabled` | src/collectors/interfaces.js |
| `=.proplist=.id,name,version,build-time,scheduled,size,available,disabled` | src/collectors/packages.js |
| `=.proplist=.id,name,vlan-id,interface,mtu,running,disabled,comment` | src/collectors/vlans.js |
| `=.proplist=.id,packets,bytes` | src/collectors/firewall.js |
| `=.proplist=.id,service-name,interface,disabled,max-sessions,authentication` | src/collectors/ppp.js |
| `=.proplist=.id,src-address,dst-address,protocol,dst-port,orig-bytes,repl-bytes` | src/collectors/connections.js |
| `=.proplist=.id,supported-bands,action,master-configuration,slave-configurations,name-format,radio-mac,identity-regexp,comment,disabled` | src/collectors/capsman.js, src/routeros/wifiMenus.js |
| `=.proplist=.id,when,name,address,via,group,radius` | src/collectors/rosusers.js |
| `=.proplist=address,gateway,dns-server` | src/collectors/dhcpNetworks.js |
| `=.proplist=address,interface,disabled` | src/collectors/dhcpNetworks.js, src/collectors/wan.js, src/index.js |
| `=.proplist=address,mac-address,interface` | src/collectors/arp.js |
| `=.proplist=board-name,version` | src/index.js |
| `=.proplist=board-name,version,free-hdd-space,total-hdd-space` | src/backups/runner.js |
| `=.proplist=channel,networks,load,nf,max-signal,min-signal` | src/wifiScan.js |
| `=.proplist=cpu-load,total-memory,free-memory,total-hdd-space,free-hdd-space,version,board-name,platform,cpu-count,cpu-frequency,uptime,architecture-name` | src/collectors/system.js |
| `=.proplist=dst-address,gateway,distance,active` | src/index.js |
| `=.proplist=identity,address,board-name,state` | src/collectors/topology.js |
| `=.proplist=interface,address` | src/collectors/interfaceStatus.js |
| `=.proplist=interface,mac-address,uptime,signal,ssid` | src/collectors/capsman.js |
| `=.proplist=interface,ssid` | src/collectors/wifi.js |
| `=.proplist=mac-address,interface,signal-strength,uptime` | src/collectors/topology.js |
| `=.proplist=mac-address,interface,ssid,signal,uptime` | src/collectors/topology.js |
| `=.proplist=mac-address,on-interface,bridge,vid` | src/collectors/topology.js |
| `=.proplist=mac-address,on-interface,bridge,vid,dynamic,local,external,age` | src/collectors/bridges.js |
| `=.proplist=name` | src/backups/runner.js |
| `=.proplist=name,interface` | src/collectors/dhcpLeases.js |
| `=.proplist=name,interface,state` | src/collectors/dhcpNetworks.js |
| `=.proplist=name,mac-address,master-interface,disabled` | src/collectors/topology.js |
| `=.proplist=name,mac-address,rate-up,rate-down` | src/collectors/talkers.js |
| `=.proplist=name,radio-mac,master-interface,cap,disabled,inactive` | src/collectors/capsman.js |
| `=.proplist=name,radio-mac,master-interface,disabled` | src/collectors/topology.js |
| `=.proplist=name,ranges` | src/collectors/dhcpNetworks.js |
| `=.proplist=name,remote-address,remote-as,state,uptime,prefix-count,updates-sent,updates-received,last-error` | src/collectors/routing.js |
| `=.proplist=name,remote.address,remote-address,remote.as,remote-as,comment` | src/collectors/routing.js |
| `=.proplist=name,remote.address,remote.as,local.role,established,uptime,prefix-count,updates-sent,updates-received,state,last-notification,inactive-reason,hold-time,keepalive-time` | src/collectors/routing.js |
| `=.proplist=name,rx-bits-per-second,tx-bits-per-second` | src/collectors/interfaceStatus.js |
| `=.proplist=name,rx-bits-per-second,tx-bits-per-second,running,disabled` | src/collectors/traffic.js |
| `=.proplist=name,size` | src/backups/runner.js |
| `=.proplist=name,type,running` | src/collectors/wan.js |
| `=.proplist=name,vlan-id` | src/collectors/dhcpLeases.js, src/collectors/topology.js |
| `=.proplist=radio-mac,interface,cap,disabled` | src/collectors/capsman.js, src/collectors/wifi.js |
| `=.proplist=routerboard,board-name,model,serial-number,firmware-type,current-firmware,upgrade-firmware,minimum-firmware` | src/collectors/packages.js |
| `=.proplist=serial-number` | src/backups/runner.js, src/index.js |
| `=.proplist=time,response-time,status,min-rtt,max-rtt` | src/collectors/ping.js |
| `=.proplist=time,topics,message` | src/collectors/logs.js |
| `=.proplist=version` | src/index.js |
