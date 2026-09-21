# Anti-bufferbloat: CAKE on a queue a little below the line's real speed, so
# the queue forms in the router, where CAKE keeps latency low under load.
# The limits are the LAN's upload/download (a queue is seen from its target).
/queue simple
remove [ find name=mdcfg-bufferbloat ]
/queue type
remove [ find name=mdcfg-cake ]
add name=mdcfg-cake kind=cake
/queue simple
add name=mdcfg-bufferbloat target={{lan_network}} max-limit="{{upload}}/{{download}}" queue=mdcfg-cake/mdcfg-cake comment="mdcfg:anti-bufferbloat"
