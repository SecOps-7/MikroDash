# Home, IoT and Guest on one VLAN-filtering bridge, each with its own addresses
# and DHCP, behind a complete firewall that REPLACES the router's filter rules:
#   Home reaches the internet, the router and IoT;
#   IoT reaches the internet, and Home only on the ports given;
#   Guest reaches the internet only;
#   everything else is dropped, MikroDash's own address excepted.
# Filtering goes on LAST, once the VLANs it must allow are in the table.
/ip firewall filter
remove [ find dynamic=no ]
/ip firewall nat
remove [ find comment="mdcfg:three-vlan-home" ]
/ip dhcp-server network
remove [ find comment="mdcfg:three-vlan-home" ]
/ip dhcp-server
remove [ find comment="mdcfg:three-vlan-home" ]
/ip pool
remove [ find comment="mdcfg:three-vlan-home" ]
/ip address
remove [ find comment="mdcfg:three-vlan-home" ]
/interface list member
remove [ find comment="mdcfg:three-vlan-home" ]
/interface bridge vlan
remove [ find comment="mdcfg:three-vlan-home" ]
/interface vlan
remove [ find comment="mdcfg:three-vlan-home" ]
/interface list
ensure name=WAN
ensure name=LAN
/interface bridge vlan
add bridge={{bridge}} vlan-ids={{home_vlan}} tagged={{tagged_ports}} untagged={{home_ports}} comment="mdcfg:three-vlan-home"
add bridge={{bridge}} vlan-ids={{iot_vlan}} tagged={{tagged_ports}} untagged={{iot_ports}} comment="mdcfg:three-vlan-home"
add bridge={{bridge}} vlan-ids={{guest_vlan}} tagged={{tagged_ports}} untagged={{guest_ports}} comment="mdcfg:three-vlan-home"
/interface bridge port
set [ find interface={{home_ports}} ] pvid={{home_vlan}}
set [ find interface={{iot_ports}} ] pvid={{iot_vlan}}
set [ find interface={{guest_ports}} ] pvid={{guest_vlan}}
/interface vlan
add name=Home vlan-id={{home_vlan}} interface={{bridge}} comment="mdcfg:three-vlan-home"
add name=IoT vlan-id={{iot_vlan}} interface={{bridge}} comment="mdcfg:three-vlan-home"
add name=Guest vlan-id={{guest_vlan}} interface={{bridge}} comment="mdcfg:three-vlan-home"
/interface list member
add list=WAN interface={{wan_iface}} comment="mdcfg:three-vlan-home"
add list=LAN interface=Home comment="mdcfg:three-vlan-home"
/ip address
add address="{{home_gateway}}/{{prefix}}" interface=Home comment="mdcfg:three-vlan-home"
add address="{{iot_gateway}}/{{prefix}}" interface=IoT comment="mdcfg:three-vlan-home"
add address="{{guest_gateway}}/{{prefix}}" interface=Guest comment="mdcfg:three-vlan-home"
/ip pool
add name=mdcfg-home ranges="{{home_pool_start}}-{{home_pool_end}}" comment="mdcfg:three-vlan-home"
add name=mdcfg-iot ranges="{{iot_pool_start}}-{{iot_pool_end}}" comment="mdcfg:three-vlan-home"
add name=mdcfg-guest ranges="{{guest_pool_start}}-{{guest_pool_end}}" comment="mdcfg:three-vlan-home"
/ip dhcp-server
add name=Home interface=Home address-pool=mdcfg-home lease-time=1d comment="mdcfg:three-vlan-home"
add name=IoT interface=IoT address-pool=mdcfg-iot lease-time=1d comment="mdcfg:three-vlan-home"
add name=Guest interface=Guest address-pool=mdcfg-guest lease-time=1h comment="mdcfg:three-vlan-home"
/ip dhcp-server network
add address={{home_network}} gateway={{home_gateway}} dns-server={{home_gateway}} comment="mdcfg:three-vlan-home"
add address={{iot_network}} gateway={{iot_gateway}} dns-server={{iot_gateway}} comment="mdcfg:three-vlan-home"
add address={{guest_network}} gateway={{guest_gateway}} dns-server={{guest_gateway}} comment="mdcfg:three-vlan-home"
/ip dns
set allow-remote-requests=yes
/ip firewall nat
add chain=srcnat action=masquerade out-interface-list=WAN comment="mdcfg:three-vlan-home"
/ip firewall filter
add chain=input action=accept connection-state=established,related,untracked comment="mdcfg:three-vlan-home"
add chain=input action=drop connection-state=invalid comment="mdcfg:three-vlan-home"
add chain=input action=accept src-address={{mgmt_src}} comment="mdcfg:three-vlan-home"
add chain=input action=accept protocol=icmp comment="mdcfg:three-vlan-home"
add chain=input action=accept dst-address=127.0.0.1 comment="mdcfg:three-vlan-home"
add chain=input action=accept in-interface=Home comment="mdcfg:three-vlan-home"
add chain=input action=accept in-interface=IoT protocol=udp dst-port=53,67,5353 comment="mdcfg:three-vlan-home"
add chain=input action=accept in-interface=IoT protocol=tcp dst-port=53 comment="mdcfg:three-vlan-home"
add chain=input action=accept in-interface=Guest protocol=udp dst-port=53,67 comment="mdcfg:three-vlan-home"
add chain=input action=accept in-interface=Guest protocol=tcp dst-port=53 comment="mdcfg:three-vlan-home"
add chain=input action=drop comment="mdcfg:three-vlan-home"
add chain=forward action=fasttrack-connection connection-state=established,related comment="mdcfg:three-vlan-home"
add chain=forward action=accept connection-state=established,related,untracked comment="mdcfg:three-vlan-home"
add chain=forward action=drop connection-state=invalid comment="mdcfg:three-vlan-home"
add chain=forward action=accept in-interface=Home out-interface-list=WAN comment="mdcfg:three-vlan-home"
add chain=forward action=accept in-interface=IoT out-interface-list=WAN comment="mdcfg:three-vlan-home"
add chain=forward action=accept in-interface=Guest out-interface-list=WAN comment="mdcfg:three-vlan-home"
add chain=forward action=accept connection-nat-state=dstnat comment="mdcfg:three-vlan-home"
add chain=forward action=accept in-interface=Home out-interface=IoT comment="mdcfg:three-vlan-home"
add chain=forward action=accept in-interface=IoT out-interface=Home protocol=tcp dst-port={{iot_home_ports}} comment="mdcfg:three-vlan-home"
add chain=forward action=drop comment="mdcfg:three-vlan-home"
/interface bridge
set [ find name={{bridge}} ] vlan-filtering=yes
