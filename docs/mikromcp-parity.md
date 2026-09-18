# MikroMCP tool parity

The wrap-up audit of the MikroMCP parity work.
Every MikroMCP tool, as its server advertised them on 2026-09-18, mapped to what the MikroDash assistant offers:
a read tool (`list_…`, `ping`, `traceroute`), `change_row` on a resource, a `run_action` entry, or a reason it is not offered.

Statuses: **covered**; **partial** (some of the tool's reach, the rest named); **context** (in every prompt's router context rather than a tool);
**pending** (built, backend only, not advertised: slice 4); **excluded** (deliberately not offered, with the reason); **gap** (not yet built).

`internal/verify/parity_test.go` holds every MikroDash name in the second column to the generated tool catalogue, the resource registry
and the declared actions, so a renamed or removed tool fails the build rather than leaving this table wrong.

| MikroMCP tool | MikroDash | Status | Note |
|---|---|---|---|
| `apply_plan` | — | excluded | MikroDash has no multi-step plan object: each change is one proposal through the write pipeline, with its own guard, read-back and undo. |
| `bandwidth_test` | `run_action:bandwidth_test` | covered | Always proposed; the approver types the far login. |
| `bulk_execute` | — | pending | Built and tested, backend only (slice 4); not advertised until the frontend is wired. |
| `check_router_health` | — | context | The system collector's reading (CPU, memory, temperature, uptime) is in every prompt's context; no separate tool. |
| `create_backup` | `run_action:backup_run` | covered |  |
| `delete_file` | `change_row:file` | covered | Delete only; a file cannot be created or edited here. |
| `export_config` | — | excluded | Exports are taken and kept by the Backups page; the model is not handed a configuration export. |
| `fetch_url` | — | excluded | Making the router download an arbitrary URL is a request-forgery path with no page behind it. |
| `get_container_config` | `list_containerConfig` | covered |  |
| `get_dns_settings` | — | gap | The DNS page shows the settings, but there is no dnsSettings resource or tool yet. |
| `get_file_content` | — | excluded | File contents are never read (the Files page's rule). |
| `get_log` | `list_logs` | covered |  |
| `get_ntp_settings` | `list_ntpClient`, `list_ntpServer` | covered |  |
| `get_ovpn_server` | `list_ovpnServer` | covered |  |
| `get_snmp_settings` | `list_snmp`, `list_snmpCommunity` | covered |  |
| `get_system_clock` | `list_clock` | covered |  |
| `get_system_status` | — | context | As check_router_health. |
| `get_upgrade_status` | `list_packages` | covered | Installed and latest version, channel and RouterBOARD firmware. |
| `list_address_list_entries` | `list_addressList` | covered |  |
| `list_arp_entries` | — | gap | ARP is read for topology and bandwidth, but there is no ARP page or tool. |
| `list_bgp_peers` | `list_bgp_sessions` | covered |  |
| `list_bridges` | `list_bridge`, `list_bridgePort` | covered |  |
| `list_certificates` | `list_certificate` | covered |  |
| `list_connections` | `list_connections` | covered | A summary, not the raw table. |
| `list_container_envs` | `list_containerEnv` | covered | Values are never returned. |
| `list_container_mounts` | `list_containerMount` | covered |  |
| `list_containers` | `list_container` | covered |  |
| `list_dhcp_clients` | `list_dhcpClient` | covered |  |
| `list_dhcp_leases` | `list_dhcpLease` | covered |  |
| `list_dhcp_servers` | `list_dhcpServer`, `list_dhcpNetwork`, `list_dhcp_networks` | covered |  |
| `list_dns_entries` | `list_dnsStatic` | covered |  |
| `list_files` | `list_file` | covered |  |
| `list_firewall_rules` | `list_fwFilter`, `list_fwNat`, `list_fwRaw`, `list_fwFilter6`, `list_fwNat6`, `list_fwRaw6` | covered |  |
| `list_interface_lists` | `list_ifList`, `list_ifListMember` | covered |  |
| `list_interfaces` | `list_iface`, `list_interface_traffic` | covered |  |
| `list_ip_pools` | `list_ipPool` | covered |  |
| `list_ip_services` | `list_ipService` | covered |  |
| `list_ipsec_peers` | `list_ipsecPeer`, `list_ipsecIdentity` | covered | Secrets are never returned. |
| `list_ipsec_policies` | `list_ipsecPolicy` | covered |  |
| `list_log_actions` | `list_logAction` | covered |  |
| `list_log_rules` | `list_logRule` | covered |  |
| `list_mangle_rules` | `list_fwMangle`, `list_fwMangle6` | covered |  |
| `list_neighbors` | `list_topology` | covered |  |
| `list_netwatch_entries` | `list_netwatch` | covered |  |
| `list_ospf_neighbors` | `list_ospfNeighbor` | covered |  |
| `list_ovpn_clients` | `list_ovpnClient` | covered |  |
| `list_packages` | `list_packages` | covered |  |
| `list_ppp_profiles` | `list_pppProfile` | covered |  |
| `list_pppoe_clients` | `list_pppoeClient` | covered |  |
| `list_queues` | `list_queues`, `list_simpleQueue`, `list_queueTree` | covered |  |
| `list_routers` | — | excluded | The assistant is scoped to the router the operator has selected; fleet-wide tools are out of scope. |
| `list_routes` | `list_route`, `list_route6` | covered |  |
| `list_routing_rules` | `list_routingRule` | covered |  |
| `list_routing_tables` | `list_routingTable` | covered |  |
| `list_scheduled_jobs` | `list_scheduler` | covered |  |
| `list_scripts` | `list_script` | covered |  |
| `list_user_groups` | `list_rosGroup` | covered |  |
| `list_users` | `list_rosUser`, `list_router_users` | covered | Passwords are never returned. |
| `list_vrrp_instances` | `list_vrrp` | covered |  |
| `list_wifi_clients` | `list_wifi_clients` | covered |  |
| `list_wifi_interfaces` | `list_wifiNet`, `list_wlNet` | covered |  |
| `list_wireguard_interfaces` | `list_wireguard_status` | partial | Peer state per interface; there is no WireGuard interface resource yet. |
| `list_wireguard_peers` | `list_wgPeer`, `list_wireguard_status` | covered |  |
| `manage_address_list_entry` | `change_row:addressList` | covered | Guarded by listLockout. |
| `manage_bridge` | `change_row:bridge` | covered |  |
| `manage_bridge_port` | `change_row:bridgePort` | covered |  |
| `manage_certificate` | `change_row:certificate` | partial | Edit and remove (certLockout); creating and signing certificates is not offered. |
| `manage_container` | `change_row:container`, `run_action:container_start`, `run_action:container_stop`, `run_action:container_remove` | covered | Image, command and entrypoint behind codeGate. |
| `manage_container_config` | `change_row:containerConfig` | covered |  |
| `manage_container_env` | `change_row:containerEnv` | covered |  |
| `manage_container_mount` | `change_row:containerMount` | covered |  |
| `manage_dhcp_client` | `change_row:dhcpClient` | covered | Guarded by dhcpClientPath and tunnelDefault. |
| `manage_dhcp_lease` | `change_row:dhcpLease` | covered |  |
| `manage_dhcp_server` | `change_row:dhcpServer`, `change_row:dhcpNetwork` | covered |  |
| `manage_dns_entry` | `change_row:dnsStatic` | covered |  |
| `manage_dns_settings` | — | gap | As get_dns_settings. |
| `manage_firewall_rule` | `change_row:fwFilter`, `change_row:fwNat`, `change_row:fwRaw`, `change_row:fwFilter6`, `change_row:fwNat6`, `change_row:fwRaw6` | covered | Guarded by fwGuard. |
| `manage_interface_list` | `change_row:ifList` | covered | Guarded by listLockout. |
| `manage_interface_list_member` | `change_row:ifListMember` | covered | Guarded by listLockout. |
| `manage_ip_address` | `change_row:ipAddress`, `change_row:ipv6Address` | covered | Guarded by addressPath. |
| `manage_ip_pool` | `change_row:ipPool` | covered |  |
| `manage_ip_service` | `change_row:ipService` | covered | Guarded by serviceLockout. |
| `manage_ipsec_peer` | `change_row:ipsecPeer`, `change_row:ipsecIdentity` | covered | Guarded by ipsecPath. |
| `manage_ipsec_policy` | `change_row:ipsecPolicy` | covered | Guarded by ipsecPath. |
| `manage_log_action` | `change_row:logAction` | covered |  |
| `manage_log_rule` | `change_row:logRule` | covered |  |
| `manage_mangle_rule` | `change_row:fwMangle`, `change_row:fwMangle6` | covered |  |
| `manage_netwatch_entry` | `change_row:netwatch` | covered |  |
| `manage_ntp_client` | `change_row:ntpClient`, `change_row:ntpServer` | covered |  |
| `manage_ovpn_client` | `change_row:ovpnClient` | covered | Guarded by selfPath and tunnelDefault. |
| `manage_ovpn_server` | `change_row:ovpnServer` | covered |  |
| `manage_package` | `run_action:packages_schedule`, `run_action:packages_apply_and_reboot` | covered | Apply needs the router's name typed back. |
| `manage_ppp_profile` | `change_row:pppProfile` | covered |  |
| `manage_pppoe_client` | `change_row:pppoeClient` | covered | Guarded by selfPath and tunnelDefault. |
| `manage_queue` | `change_row:simpleQueue`, `change_row:queueTree` | covered | Guarded by queueThrottle. |
| `manage_route` | `change_row:route`, `change_row:route6` | covered | Guarded by routePath. |
| `manage_routing_rule` | `change_row:routingRule` | covered | Guarded by rulePath. |
| `manage_routing_table` | `change_row:routingTable` | covered | Guarded by tableInUse. |
| `manage_scheduled_job` | `change_row:scheduler` | covered | On-event behind codeGate. |
| `manage_script` | `change_row:script` | covered | Source behind codeGate. |
| `manage_upgrade` | `run_action:firmware_upgrade_and_reboot` | partial | RouterBOOT firmware only; a RouterOS version upgrade is not offered as an action. |
| `manage_user` | `change_row:rosUser` | covered | Guarded by selfAccount. |
| `manage_user_group` | `change_row:rosGroup` | covered | Guarded by selfAccount. |
| `manage_vlan` | `change_row:vlan` | covered |  |
| `manage_vrrp_instance` | `change_row:vrrp` | covered | Scripts behind codeGate. |
| `manage_wifi_interface` | `change_row:wifiNet`, `change_row:wlNet` | covered |  |
| `manage_wireguard_interface` | — | gap | No WireGuard interface resource yet (peers only). |
| `manage_wireguard_peer` | `change_row:wgPeer` | covered |  |
| `ping` | `ping` | covered | Bounded: up to 10 packets. |
| `plan_changes` | — | excluded | As apply_plan. |
| `reboot` | — | excluded | A reboot is offered only as the last step of an apply or firmware upgrade, with the router's name typed back; a bare reboot has no page action. |
| `rollback_change` | — | excluded | Undo is per operator and per page, in the page's history; the assistant is not given it. |
| `run_command` | — | pending | As bulk_execute. |
| `run_script` | — | partial | A script's Run is a row action on the Scripts page, behind codeGate; the assistant does not run scripts. |
| `set_system_clock` | — | excluded | The Clock page never sends the time (NTP sets it); only the time zone is written. |
| `torch` | `run_action:torch` | covered | Always proposed; 5 seconds. |
| `traceroute` | `traceroute` | covered | Bounded: up to 30 hops. |
| `upload_file` | — | excluded | Files are seen and removed, never uploaded (the Files page's rule). |

Totals: covered 96, partial 4, context 2, pending 2, excluded 10, gap 4, of 118 tools.
