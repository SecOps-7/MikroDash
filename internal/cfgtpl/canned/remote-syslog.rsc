# Remote syslog: send the router's log to a syslog server as well as memory.
# The rules go first: an action cannot be removed while rules still use it.
/system logging
remove [ find action=mdcfg-remote ]
/system logging action
remove [ find name=mdcfg-remote ]
add name=mdcfg-remote target=remote remote={{syslog_server}} remote-port={{syslog_port}}
/system logging
add topics=info action=mdcfg-remote
add topics=warning action=mdcfg-remote
add topics=error action=mdcfg-remote
add topics=critical action=mdcfg-remote
