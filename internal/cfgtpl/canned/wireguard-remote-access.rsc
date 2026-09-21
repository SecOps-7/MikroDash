# WireGuard remote access: an interface for road-warrior peers, its address,
# and its port opened through the firewall templates' mdcfg-allow hook. Peers
# are added on the WireGuard page. Re-applying keeps the interface's key.
/interface wireguard
ensure name=mdcfg-wg
set [ find name=mdcfg-wg ] listen-port={{listen_port}} comment="mdcfg:wireguard-remote-access"
/ip address
remove [ find comment="mdcfg:wireguard-remote-access" ]
add address={{tunnel_address}} interface=mdcfg-wg comment="mdcfg:wireguard-remote-access"
/ip firewall filter
remove [ find comment="mdcfg:wireguard-remote-access" ]
add chain=mdcfg-allow action=accept protocol=udp dst-port={{listen_port}} comment="mdcfg:wireguard-remote-access"
