# Privacy essentials: switch off what announces or exposes the router, and
# keep discovery and MAC access to the LAN.
/interface list
ensure name=LAN
/ip upnp
set enabled=no
/ip proxy
set enabled=no
/ip socks
set enabled=no
/ip neighbor discovery-settings
set discover-interface-list=LAN
/tool mac-server
set allowed-interface-list=LAN
/tool mac-server mac-winbox
set allowed-interface-list=LAN
/tool bandwidth-server
set enabled=no
