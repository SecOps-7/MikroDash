# Isolate a network (IoT, guests): its devices reach the internet but not the
# rest of the LAN, and not the router's management services. MikroDash's own
# address is accepted first, in case it sits on that network.
/interface list
ensure name=LAN
/ip firewall filter
remove [ find comment="mdcfg:iot-isolation" ]
add chain=forward action=accept connection-state=established,related in-interface={{isolated_iface}} comment="mdcfg:iot-isolation"
add chain=forward action=drop in-interface={{isolated_iface}} out-interface-list=LAN comment="mdcfg:iot-isolation"
add chain=input action=accept src-address={{mgmt_src}} comment="mdcfg:iot-isolation"
add chain=input action=drop in-interface={{isolated_iface}} protocol=tcp dst-port=21,22,23,80,443,8291,8728,8729 comment="mdcfg:iot-isolation"
