# Resolvers and time: which DNS servers the router asks, whether it answers
# for the LAN, and the NTP servers it keeps its clock by.
/ip dns
set servers={{dns_servers}} allow-remote-requests={{answer_lan}}
/system ntp client
set enabled=yes
/system ntp client servers
remove [ find comment="mdcfg:dns-and-time" ]
add address={{ntp_primary}} comment="mdcfg:dns-and-time"
add address={{ntp_secondary}} comment="mdcfg:dns-and-time"
