# SNMPv3 read-only: a community with authentication and encryption, readable
# only from the monitoring server.
/snmp community
remove [ find name=mdcfg-monitor ]
add name=mdcfg-monitor addresses={{monitor_address}} security=private read-access=yes write-access=no authentication-protocol=SHA1 authentication-password={{auth_password}} encryption-protocol=AES encryption-password={{priv_password}}
/snmp
set enabled=yes contact={{contact}} location={{location}}
