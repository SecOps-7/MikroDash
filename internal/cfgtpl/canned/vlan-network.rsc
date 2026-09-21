# Add a VLAN network: a tagged VLAN on a trunk (an interface or a bridge), with
# the router's address on it, a DHCP pool and server, and the VLAN in the LAN
# list so the firewall templates treat it as LAN. Each VLAN is tagged with its
# own id, so re-applying one VLAN replaces that VLAN and leaves the others.
# Removed in the order that frees each row's references first.
/ip dhcp-server network
remove [ find comment="mdcfg:vlan-network:{{vlan_id}}" ]
/ip dhcp-server
remove [ find comment="mdcfg:vlan-network:{{vlan_id}}" ]
/ip pool
remove [ find comment="mdcfg:vlan-network:{{vlan_id}}" ]
/ip address
remove [ find comment="mdcfg:vlan-network:{{vlan_id}}" ]
/interface list member
remove [ find comment="mdcfg:vlan-network:{{vlan_id}}" ]
/interface vlan
remove [ find comment="mdcfg:vlan-network:{{vlan_id}}" ]
/interface list
ensure name=LAN
/interface vlan
add name={{vlan_name}} vlan-id={{vlan_id}} interface={{parent_iface}} comment="mdcfg:vlan-network:{{vlan_id}}"
/interface list member
add list=LAN interface={{vlan_name}} comment="mdcfg:vlan-network:{{vlan_id}}"
/ip address
add address="{{gateway}}/{{prefix}}" interface={{vlan_name}} comment="mdcfg:vlan-network:{{vlan_id}}"
/ip pool
add name="mdcfg-vlan{{vlan_id}}" ranges="{{pool_start}}-{{pool_end}}" comment="mdcfg:vlan-network:{{vlan_id}}"
/ip dhcp-server
add name={{vlan_name}} interface={{vlan_name}} address-pool="mdcfg-vlan{{vlan_id}}" comment="mdcfg:vlan-network:{{vlan_id}}"
/ip dhcp-server network
add address={{network}} gateway={{gateway}} dns-server={{gateway}} comment="mdcfg:vlan-network:{{vlan_id}}"
