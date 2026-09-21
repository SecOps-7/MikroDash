# IPv6 firewall: MikroTik's default IPv6 firewall, as the lab router's export
# writes it.
/interface list
ensure name=LAN
/ipv6 firewall filter
remove [ find comment="mdcfg:ipv6-firewall" ]
add chain=input action=accept connection-state=established,related,untracked comment="mdcfg:ipv6-firewall"
add chain=input action=drop connection-state=invalid comment="mdcfg:ipv6-firewall"
add chain=input action=accept protocol=icmpv6 comment="mdcfg:ipv6-firewall"
add chain=input action=accept protocol=udp dst-port=33434-33534 comment="mdcfg:ipv6-firewall"
add chain=input action=accept protocol=udp dst-port=546 src-address=fe80::/10 comment="mdcfg:ipv6-firewall"
add chain=input action=drop in-interface-list=!LAN comment="mdcfg:ipv6-firewall"
add chain=forward action=accept connection-state=established,related,untracked comment="mdcfg:ipv6-firewall"
add chain=forward action=drop connection-state=invalid comment="mdcfg:ipv6-firewall"
add chain=forward action=drop protocol=icmpv6 hop-limit=equal:1 comment="mdcfg:ipv6-firewall"
add chain=forward action=accept protocol=icmpv6 comment="mdcfg:ipv6-firewall"
add chain=forward action=drop in-interface-list=!LAN comment="mdcfg:ipv6-firewall"
