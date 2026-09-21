# One VLAN on a VLAN-filtering bridge: the bridge's VLAN table entry (tagged on
# the trunks, untagged on the access ports), each access port's VLAN, then
# VLAN filtering on. Deploy it once per VLAN: each is tagged with its own id.
# Filtering goes on LAST, once the VLAN it must allow is in the table.
/interface bridge vlan
remove [ find comment="mdcfg:bridge-vlan:{{vlan_id}}" ]
add bridge={{bridge}} vlan-ids={{vlan_id}} tagged={{tagged_ports}} untagged={{access_ports}} comment="mdcfg:bridge-vlan:{{vlan_id}}"
/interface bridge port
set [ find interface={{access_ports}} ] pvid={{vlan_id}}
/interface bridge
set [ find name={{bridge}} ] vlan-filtering=yes
