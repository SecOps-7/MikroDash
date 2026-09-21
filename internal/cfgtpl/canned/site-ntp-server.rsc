# Site NTP server: the router keeps time from upstream servers and serves it
# to the LAN, so devices without internet access agree on the time.
/system ntp client
set enabled=yes
/system ntp client servers
remove [ find comment="mdcfg:site-ntp-server" ]
add address={{upstream_ntp}} comment="mdcfg:site-ntp-server"
/system ntp server
set enabled=yes broadcast=no
