# MikroDash zero-touch provisioning: any device (mass deployment)
# Instance inst-0123. Valid until 2026-09-29 12:00 UTC.
# THIS FILE HOLDS A SECRET (the shared enrolment key and a batch token). Keep it like a password.
# Run it on the router: upload the file and import it, or paste it into a terminal.
# It is safe to run twice.

/interface wireguard
:if ([:len [find name="mikrodash-ztp"]] = 0) do={ add name="mikrodash-ztp" comment="MikroDash ZTP" }
set [find name="mikrodash-ztp"] disabled=no
/interface wireguard peers
:if ([:len [find interface="mikrodash-ztp"]] = 0) do={ add interface="mikrodash-ztp" public-key="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE=" endpoint-address="vpn.example.net" endpoint-port=13231 allowed-address="10.249.0.1/32" persistent-keepalive=25s comment="MikroDash ZTP" }
/interface wireguard
:if ([:len [find name="mikrodash-ztp-enrol"]] = 0) do={ add name="mikrodash-ztp-enrol" private-key="BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBE=" comment="MikroDash ZTP" }
set [find name="mikrodash-ztp-enrol"] disabled=no
/interface wireguard peers
:if ([:len [find interface="mikrodash-ztp-enrol"]] = 0) do={ add interface="mikrodash-ztp-enrol" public-key="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE=" endpoint-address="vpn.example.net" endpoint-port=13231 allowed-address="10.249.0.1/32" persistent-keepalive=25s comment="MikroDash ZTP" }
/ip address
:if ([:len [find interface="mikrodash-ztp-enrol"]] = 0) do={ add address=("10.249.255." . [:rndnum from=2 to=254] . "/32") network="10.249.0.1" interface="mikrodash-ztp-enrol" comment="MikroDash ZTP" }
/ip firewall filter
:if ([:len [find comment="MikroDash ZTP"]] = 0) do={ :local first [:pick [find] 0]; :if ([:len $first] > 0) do={ add chain=input action=accept in-interface="mikrodash-ztp" comment="MikroDash ZTP" place-before=$first } else={ add chain=input action=accept in-interface="mikrodash-ztp" comment="MikroDash ZTP" } }
/user
:if ([:len [find name="mikrodash-ztp"]] = 0) do={ add name="mikrodash-ztp" group=full address="10.249.0.1/32" password=[:rndstr length=32 from="abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"] comment="MikroDash ZTP" }
set [find name="mikrodash-ztp"] address="10.249.0.1/32"
/ip service
:foreach i in=[find where (name="api" || name="api-ssl")] do={ :local l [get $i address]; :if ([:len $l] > 0 && [:typeof [:find $l "10.249.0.1/32"]] = "nil") do={ set $i address=($l, "10.249.0.1/32") } }
:if ([:len [find where (name="api" || name="api-ssl") && disabled=no]] = 0) do={ :foreach i in=[find where name="api"] do={ set $i disabled=no address="10.249.0.1/32" } }
/system script
remove [find name="mikrodash-ztp-enrol"]
add name="mikrodash-ztp-enrol" policy=read,write,policy,test,sensitive comment="MikroDash ZTP" source=":local url \"http://10.249.0.1/enrol\"\0A:local pw \5B:rndstr length=32 from=\"abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789\"\5D\0A:local id \"\"\0A:do \7B :set id \5B/system routerboard get serial-number\5D \7D on-error=\7B\7D\0A:if (\5B:len \$id\5D = 0) do=\7B :do \7B :set id \5B/system license get system-id\5D \7D on-error=\7B\7D \7D\0A:local model \5B/system resource get board-name\5D\0A:local version \5B/system resource get version\5D\0A:local identity \5B/system identity get name\5D\0A:local key \5B/interface wireguard get \5Bfind name=\"mikrodash-ztp\"\5D public-key\5D\0A:local body \5B:serialize to=json value=\7B\"instance\"=\"inst-0123\";\"token\"=\"tok-batch\";\"serial\"=\$id;\"model\"=\$model;\"version\"=\$version;\"identity\"=\$identity;\"password\"=\$pw;\"publicKey\"=\$key\7D\5D\0A:onerror e in=\7B\0A  :local r \5B/tool fetch url=\$url http-method=post http-header-field=\"Content-Type: application/json\" http-data=\$body output=user as-value\5D\0A  /user set \5Bfind name=\"mikrodash-ztp\"\5D password=\$pw\0A  :local j \5B:deserialize from=json value=(\$r->\"data\")\5D\0A  /ip address add address=((\$j->\"address\") . \"/32\") network=\"10.249.0.1\" interface=\"mikrodash-ztp\" comment=\"MikroDash ZTP\"\0A  /ip address remove \5Bfind interface=\"mikrodash-ztp-enrol\"\5D\0A  /interface wireguard peers remove \5Bfind interface=\"mikrodash-ztp-enrol\"\5D\0A  /interface wireguard remove \5Bfind name=\"mikrodash-ztp-enrol\"\5D\0A  :log info \"MikroDash: enrolled\"\0A  /system scheduler remove \5Bfind name=\"mikrodash-ztp-enrol\"\5D\0A  /system script remove \5Bfind name=\"mikrodash-ztp-enrol\"\5D\0A\7D do=\7B\0A  :if (\5B:typeof \5B:find \$e \"403\"\5D\5D != \"nil\") do=\7B\0A    :log error (\"MikroDash refused this device: \" . \$e)\0A    /system scheduler remove \5Bfind name=\"mikrodash-ztp-enrol\"\5D\0A  \7D else=\7B\0A    :log warning (\"MikroDash: not enrolled yet, trying again in a minute: \" . \$e)\0A  \7D\0A\7D\0A"
/system scheduler
remove [find name="mikrodash-ztp-enrol"]
add name="mikrodash-ztp-enrol" interval=1m on-event="mikrodash-ztp-enrol" policy=read,write,policy,test,sensitive comment="MikroDash ZTP"
/system script run [find name="mikrodash-ztp-enrol"]
