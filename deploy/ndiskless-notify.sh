#!/bin/sh
# Notify ndiskless of keepalived role changes. Fire and return; blocking notify delays VRRP.
# master: acquired VIP; restart as active (startup synchronizes and restarts dnsmasq).
# backup/fault/stop: lost VIP; restart as standby (startup stops dnsmasq and clears exports).
STATE="$1"
TOKEN="$(cat /etc/ndiskless/cluster-token 2>/dev/null)"
BASE="http://127.0.0.1:@API_PORT@"
LOG="logger -t ndiskless-ha"

case "$STATE" in
  master)
    $LOG "keepalived MASTER: activating"
    curl -sf -m 5 -X POST -H "Authorization: Bearer $TOKEN" "$BASE/internal/ha/activate" >/dev/null 2>&1 &
    ;;
  backup|fault|stop)
    $LOG "keepalived $STATE: stepping down"
    curl -sf -m 5 -X POST -H "Authorization: Bearer $TOKEN" "$BASE/internal/ha/standby" >/dev/null 2>&1 &
    ;;
esac
exit 0
