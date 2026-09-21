# A guest network: its own VLAN with an address, DHCP and public DNS, that
# reaches the internet but not the LAN and not the router. It is NOT added to
# the LAN list, and its devices are given public resolvers, because a
# firewall template drops what does not come from the LAN, DNS to the router
# included. MikroDash's own address is accepted before the router is closed
# to the guests, in case it sits on that network.
/ip firewall filter
remove [ find comment="mdcfg:guest-network" ]
/ip dhcp-server network
remove [ find comment="mdcfg:guest-network" ]
/ip dhcp-server
remove [ find comment="mdcfg:guest-network" ]
/ip pool
remove [ find comment="mdcfg:guest-network" ]
/ip address
remove [ find comment="mdcfg:guest-network" ]
/interface vlan
remove [ find comment="mdcfg:guest-network" ]
/interface list
ensure name=LAN
/interface vlan
add name={{vlan_name}} vlan-id={{vlan_id}} interface={{parent_iface}} comment="mdcfg:guest-network"
/ip address
add address="{{gateway}}/{{prefix}}" interface={{vlan_name}} comment="mdcfg:guest-network"
/ip pool
add name=mdcfg-guest ranges="{{pool_start}}-{{pool_end}}" comment="mdcfg:guest-network"
/ip dhcp-server
add name={{vlan_name}} interface={{vlan_name}} address-pool=mdcfg-guest lease-time=1h comment="mdcfg:guest-network"
/ip dhcp-server network
add address={{network}} gateway={{gateway}} dns-server={{guest_dns}} comment="mdcfg:guest-network"
/ip firewall filter
add chain=input action=accept src-address={{mgmt_src}} comment="mdcfg:guest-network"
add chain=input action=accept in-interface={{vlan_name}} protocol=udp dst-port=67 comment="mdcfg:guest-network"
add chain=input action=drop in-interface={{vlan_name}} comment="mdcfg:guest-network"
add chain=forward action=accept connection-state=established,related in-interface={{vlan_name}} comment="mdcfg:guest-network"
add chain=forward action=drop in-interface={{vlan_name}} out-interface-list=LAN comment="mdcfg:guest-network"
