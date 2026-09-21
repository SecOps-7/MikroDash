# Management lockdown: plain-text services off, and every way in restricted to
# the admin network, MikroDash's own address kept in.
/ip service
set [ find name=telnet ] disabled=yes
set [ find name=ftp ] disabled=yes
set [ find name=www ] disabled=yes
set [ find name=winbox ] address={{admin_network}}
set [ find name=ssh ] address={{admin_network}}
set [ find name={{api_service}} ] address="{{admin_network}},{{mgmt_src}}"
/ip ssh
set strong-crypto=yes
