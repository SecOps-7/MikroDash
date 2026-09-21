# Fair share: every LAN device gets an equal part of the line when it is busy
# (PCQ, one sub-queue per address), and all of it when it is not.
/queue simple
remove [ find name=mdcfg-fair-share ]
/queue type
remove [ find name=mdcfg-pcq-up ]
remove [ find name=mdcfg-pcq-down ]
add name=mdcfg-pcq-up kind=pcq pcq-classifier=src-address
add name=mdcfg-pcq-down kind=pcq pcq-classifier=dst-address
/queue simple
add name=mdcfg-fair-share target={{lan_network}} max-limit="{{upload}}/{{download}}" queue=mdcfg-pcq-up/mdcfg-pcq-down comment="mdcfg:fair-share"
