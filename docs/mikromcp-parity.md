# MikroMCP tool parity

The wrap-up audit of the MikroMCP parity work, brought up to date on 2026-09-21 when the gaps and exclusions below were built.
Every MikroMCP tool, as its server advertised them on 2026-09-18, mapped to what the MikroDash assistant offers:
a read tool (`list_…`, `ping`, `traceroute`, `read_file`, `export_config`), `change_row` on a resource, a `run_action` entry, `plan_changes`, the raw command tools, or a reason it is not offered.

Statuses: **covered**; **partial** (some of the tool's reach, the rest named); **context** (in every prompt's router context rather than a tool);
**pending** (built, backend only, not advertised); **excluded** (deliberately not offered, with the reason); **gap** (not yet built).

`internal/verify/parity_test.go` holds every MikroDash name in the second column to the generated tool catalogue, the resource registry
and the declared actions, so a renamed or removed tool fails the build rather than leaving this table wrong.

| MikroMCP tool | MikroDash | Status | Note |
|---|---|---|---|
| `apply_plan` | `plan_changes` | covered | The operator's approval of a plan is the apply: one card, the steps in order through the write pipeline, stopping at the first that fails or is flagged. |
| `bandwidth_test` | `run_action:bandwidth_test` | covered | Always proposed; the approver types the far login. |
| `bulk_execute` | `bulk_execute` | covered | Advertised only to a signed-in global administrator with Settings → AI → raw commands on; approved once with the router's name typed back. |
| `check_router_health` | `list_system_status` | covered | CPU, memory, storage, temperature and uptime, re-read when stale; also in every prompt's context. |
| `create_backup` | `run_action:backup_run` | covered |  |
| `delete_file` | `change_row:file` | covered | Create (text, up to 60000 bytes) and delete; a name that would run (*.auto.*) or install (.npk) is refused by the fileName guard. |
| `export_config` | `export_config` | covered | Never show-sensitive; identifying header lines stripped, credential values masked, capped at 48 KiB with one menu exportable in full. |
| `fetch_url` | `run_action:fetch_url` | covered | An http(s) GET into a file named from the URL; a login in the URL, *.auto.* and .npk are refused. Also the Files page's Transfer tab. |
| `get_container_config` | `list_containerConfig` | covered |  |
| `get_dns_settings` | `list_dnsSettings` | covered |  |
| `get_file_content` | `read_file` | covered | Text files up to 64 KiB; credential values masked, a private key refused. Also the Files page's Transfer tab. |
| `get_log` | `list_logs` | covered |  |
| `get_ntp_settings` | `list_ntpClient`, `list_ntpServer` | covered |  |
| `get_ovpn_server` | `list_ovpnServer` | covered |  |
| `get_snmp_settings` | `list_snmp`, `list_snmpCommunity` | covered |  |
| `get_system_clock` | `list_clock` | covered |  |
| `get_system_status` | `list_system_status` | covered | As check_router_health. No serial or licence. |
| `get_upgrade_status` | `list_packages` | covered | Installed and latest version, channel and RouterBOARD firmware. |
| `list_address_list_entries` | `list_addressList` | covered | Read a list at a time: without `list` it returns each list and its counts. |
| `list_arp_entries` | `list_arp` | covered | Also a new ARP page. |
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
| `list_routers` | `list_routers` | covered | The Devices page's rows for this viewer; no host, serial, licence or location. |
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
| `list_wireguard_interfaces` | `list_wgInterface`, `list_wireguard_status` | covered |  |
| `list_wireguard_peers` | `list_wgPeer`, `list_wireguard_status` | covered |  |
| `manage_address_list_entry` | `change_row:addressList` | covered | Guarded by listLockout. |
| `manage_bridge` | `change_row:bridge` | covered |  |
| `manage_bridge_port` | `change_row:bridgePort` | covered |  |
| `manage_certificate` | `change_row:certificate`, `run_action:certificate_sign` | covered | Create, self-sign, edit and remove (certLockout). A removal cannot be undone: a re-created certificate has a new key. |
| `manage_container` | `change_row:container`, `run_action:container_start`, `run_action:container_stop`, `run_action:container_remove` | covered | Image, command and entrypoint behind codeGate. |
| `manage_container_config` | `change_row:containerConfig` | covered |  |
| `manage_container_env` | `change_row:containerEnv` | covered |  |
| `manage_container_mount` | `change_row:containerMount` | covered |  |
| `manage_dhcp_client` | `change_row:dhcpClient` | covered | Guarded by dhcpClientPath and tunnelDefault. |
| `manage_dhcp_lease` | `change_row:dhcpLease` | covered |  |
| `manage_dhcp_server` | `change_row:dhcpServer`, `change_row:dhcpNetwork` | covered |  |
| `manage_dns_entry` | `change_row:dnsStatic` | covered |  |
| `manage_dns_settings` | `change_row:dnsSettings` | covered | Also the DNS page's Edit. |
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
| `manage_upgrade` | `run_action:routeros_upgrade_and_reboot`, `run_action:firmware_upgrade_and_reboot` | covered | Both need the router's name typed back. |
| `manage_user` | `change_row:rosUser` | covered | Guarded by selfAccount. |
| `manage_user_group` | `change_row:rosGroup` | covered | Guarded by selfAccount. |
| `manage_vlan` | `change_row:vlan` | covered |  |
| `manage_vrrp_instance` | `change_row:vrrp` | covered | Scripts behind codeGate. |
| `manage_wifi_interface` | `change_row:wifiNet`, `change_row:wlNet` | covered |  |
| `manage_wireguard_interface` | `change_row:wgInterface` | covered | Guarded by selfPath; private keys are never read back. |
| `manage_wireguard_peer` | `change_row:wgPeer`, `run_action:wireguard_show_config` | covered | The client configuration opens in the approving operator's browser; the model never receives it. |
| `ping` | `ping` | covered | Bounded: up to 10 packets. |
| `plan_changes` | `plan_changes` | covered | Up to 10 create, edit or delete steps; code changes are refused in a plan. |
| `reboot` | `run_action:reboot` | covered | The router's name typed back; also the Packages page's Reboot button. |
| `rollback_change` | `change_row` | covered | Its `undo: true`: the assistant's own newest change to a resource, through the page's undo path, always approved. |
| `run_command` | `run_command` | covered | As bulk_execute: every command typed back. |
| `run_script` | `run_action:script_run` | covered | Behind the raw-command gate and codeGate, with the router's name typed back. |
| `set_system_clock` | `run_action:clock_set` | covered | Date and time in the router's own time zone, validated strictly (RouterOS wraps a time out of range instead of refusing it); always proposed. The Clock page itself still writes only the time zone. |
| `torch` | `run_action:torch` | covered | Always proposed; 5 seconds. |
| `traceroute` | `traceroute` | covered | Bounded: up to 30 hops. |
| `upload_file` | `change_row:file` | covered | As delete_file: a text file created with its contents, which are never read back. |

Totals: covered 118, partial 0, context 0, pending 0, excluded 0, gap 0, of 118 tools.
