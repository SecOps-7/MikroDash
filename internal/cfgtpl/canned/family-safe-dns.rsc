# Family-safe DNS: filtering resolvers, and every LAN DNS query redirected to
# the router, so a device set to another resolver is filtered too.
/interface list
ensure name=LAN
/ip dns
set servers={{family_dns}} allow-remote-requests=yes
/ip firewall nat
remove [ find comment="mdcfg:family-safe-dns" ]
add chain=dstnat action=redirect protocol=udp dst-port=53 in-interface-list=LAN to-ports=53 comment="mdcfg:family-safe-dns"
add chain=dstnat action=redirect protocol=tcp dst-port=53 in-interface-list=LAN to-ports=53 comment="mdcfg:family-safe-dns"
