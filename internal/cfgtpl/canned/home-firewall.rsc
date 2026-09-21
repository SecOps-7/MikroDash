# Home firewall: MikroTik's own default firewall, as the lab router's export
# writes it, with MikroDash's address accepted before the drop and a hook
# (chain mdcfg-allow) where other templates open ports.
/interface list
ensure name=WAN
ensure name=LAN
/interface list member
ensure list=WAN interface={{wan_iface}}
ensure list=LAN interface={{lan_iface}}
/ip firewall filter
remove [ find comment="mdcfg:home-firewall" ]
add chain=input action=accept connection-state=established,related,untracked comment="mdcfg:home-firewall"
add chain=input action=drop connection-state=invalid comment="mdcfg:home-firewall"
add chain=input action=accept protocol=icmp comment="mdcfg:home-firewall"
add chain=input action=accept src-address={{mgmt_src}} comment="mdcfg:home-firewall"
add chain=input action=accept dst-address=127.0.0.1 comment="mdcfg:home-firewall"
add chain=input action=jump jump-target=mdcfg-allow comment="mdcfg:home-firewall"
add chain=input action=drop in-interface-list=!LAN comment="mdcfg:home-firewall"
add chain=forward action=fasttrack-connection connection-state=established,related comment="mdcfg:home-firewall"
add chain=forward action=accept connection-state=established,related,untracked comment="mdcfg:home-firewall"
add chain=forward action=drop connection-state=invalid comment="mdcfg:home-firewall"
add chain=forward action=drop connection-nat-state=!dstnat in-interface-list=WAN comment="mdcfg:home-firewall"
/ip firewall nat
remove [ find comment="mdcfg:home-firewall" ]
add chain=srcnat action=masquerade out-interface-list=WAN comment="mdcfg:home-firewall"
