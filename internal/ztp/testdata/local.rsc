# MikroDash zero-touch provisioning: Office (local network)
# Instance inst-0123. Valid until 2026-09-29 12:00 UTC.
# THIS FILE HOLDS A SECRET (this device's API password and a one-time token). Keep it like a password.
# Run it on the router: upload the file and import it, or paste it into a terminal.
# It is safe to run twice.

/ip firewall filter
:if ([:len [find comment="MikroDash ZTP"]] = 0) do={ :local first [:pick [find] 0]; :if ([:len $first] > 0) do={ add chain=input action=accept src-address="192.0.2.10" comment="MikroDash ZTP" place-before=$first } else={ add chain=input action=accept src-address="192.0.2.10" comment="MikroDash ZTP" } }
/user
:if ([:len [find name="mikrodash-ztp"]] = 0) do={ add name="mikrodash-ztp" group=full address="192.0.2.10/32" password="pw-FakeFakeFake" comment="MikroDash ZTP" }
set [find name="mikrodash-ztp"] address="192.0.2.10/32"
set [find name="mikrodash-ztp"] password="pw-FakeFakeFake"
/ip service
:foreach i in=[find where (name="api" || name="api-ssl")] do={ :local l [get $i address]; :if ([:len $l] > 0 && [:typeof [:find $l "192.0.2.10/32"]] = "nil") do={ set $i address=($l, "192.0.2.10/32") } }
:if ([:len [find where (name="api" || name="api-ssl") && disabled=no]] = 0) do={ :foreach i in=[find where name="api"] do={ set $i disabled=no address="192.0.2.10/32" } }
/system script
remove [find name="mikrodash-ztp-enrol"]
add name="mikrodash-ztp-enrol" policy=read,write,policy,test,sensitive comment="MikroDash ZTP" source=":local url \"http://192.0.2.10:3081/api/ztp/enrol\"\0A:local id \"\"\0A:do \7B :set id \5B/system routerboard get serial-number\5D \7D on-error=\7B\7D\0A:if (\5B:len \$id\5D = 0) do=\7B :do \7B :set id \5B/system license get system-id\5D \7D on-error=\7B\7D \7D\0A:local model \5B/system resource get board-name\5D\0A:local version \5B/system resource get version\5D\0A:local identity \5B/system identity get name\5D\0A:local body \5B:serialize to=json value=\7B\"instance\"=\"inst-0123\";\"token\"=\"tok-local\";\"serial\"=\$id;\"model\"=\$model;\"version\"=\$version;\"identity\"=\$identity\7D\5D\0A:onerror e in=\7B\0A  :local r \5B/tool fetch url=\$url http-method=post http-header-field=\"Content-Type: application/json\" http-data=\$body output=user as-value\5D\0A  :log info \"MikroDash: enrolled\"\0A  /system scheduler remove \5Bfind name=\"mikrodash-ztp-enrol\"\5D\0A  /system script remove \5Bfind name=\"mikrodash-ztp-enrol\"\5D\0A\7D do=\7B\0A  :if (\5B:typeof \5B:find \$e \"403\"\5D\5D != \"nil\") do=\7B\0A    :log error (\"MikroDash refused this device: \" . \$e)\0A    /system scheduler remove \5Bfind name=\"mikrodash-ztp-enrol\"\5D\0A  \7D else=\7B\0A    :log warning (\"MikroDash: not enrolled yet, trying again in a minute: \" . \$e)\0A  \7D\0A\7D\0A"
/system scheduler
remove [find name="mikrodash-ztp-enrol"]
add name="mikrodash-ztp-enrol" interval=1m on-event="mikrodash-ztp-enrol" policy=read,write,policy,test,sensitive comment="MikroDash ZTP"
/system script run [find name="mikrodash-ztp-enrol"]
