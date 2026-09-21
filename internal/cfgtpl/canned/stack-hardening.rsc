# IP stack hardening: SYN cookies on, source routing and redirects refused,
# and loose reverse-path filtering (strict breaks multi-WAN).
/ip settings
set tcp-syncookies=yes accept-source-route=no accept-redirects=no send-redirects=no rp-filter=loose
