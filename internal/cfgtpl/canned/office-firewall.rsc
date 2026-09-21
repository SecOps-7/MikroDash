# Office firewall: the home baseline, plus a RAW drop of addresses that can
# never arrive from the internet legitimately. Private ranges and carrier NAT
# (100.64.0.0/10) are NOT in the list: many WAN links use them.
/interface list
ensure name=WAN
ensure name=LAN
/interface list member
ensure list=WAN interface={{wan_iface}}
ensure list=LAN interface={{lan_iface}}
/ip firewall address-list
remove [ find comment="mdcfg:office-firewall" ]
add list=mdcfg-martians address=0.0.0.0/8 comment="mdcfg:office-firewall"
add list=mdcfg-martians address=127.0.0.0/8 comment="mdcfg:office-firewall"
add list=mdcfg-martians address=169.254.0.0/16 comment="mdcfg:office-firewall"
add list=mdcfg-martians address=192.0.2.0/24 comment="mdcfg:office-firewall"
add list=mdcfg-martians address=198.18.0.0/15 comment="mdcfg:office-firewall"
add list=mdcfg-martians address=198.51.100.0/24 comment="mdcfg:office-firewall"
add list=mdcfg-martians address=203.0.113.0/24 comment="mdcfg:office-firewall"
add list=mdcfg-martians address=224.0.0.0/3 comment="mdcfg:office-firewall"
/ip firewall raw
remove [ find comment="mdcfg:office-firewall" ]
add chain=prerouting action=drop in-interface-list=WAN src-address-list=mdcfg-martians comment="mdcfg:office-firewall"
/ip firewall filter
remove [ find comment="mdcfg:office-firewall" ]
add chain=input action=accept connection-state=established,related,untracked comment="mdcfg:office-firewall"
add chain=input action=drop connection-state=invalid comment="mdcfg:office-firewall"
add chain=input action=accept protocol=icmp comment="mdcfg:office-firewall"
add chain=input action=accept src-address={{mgmt_src}} comment="mdcfg:office-firewall"
add chain=input action=accept dst-address=127.0.0.1 comment="mdcfg:office-firewall"
add chain=input action=jump jump-target=mdcfg-allow comment="mdcfg:office-firewall"
add chain=input action=drop in-interface-list=!LAN comment="mdcfg:office-firewall"
add chain=forward action=fasttrack-connection connection-state=established,related comment="mdcfg:office-firewall"
add chain=forward action=accept connection-state=established,related,untracked comment="mdcfg:office-firewall"
add chain=forward action=drop connection-state=invalid comment="mdcfg:office-firewall"
add chain=forward action=drop connection-nat-state=!dstnat in-interface-list=WAN comment="mdcfg:office-firewall"
/ip firewall nat
remove [ find comment="mdcfg:office-firewall" ]
add chain=srcnat action=masquerade out-interface-list=WAN comment="mdcfg:office-firewall"
