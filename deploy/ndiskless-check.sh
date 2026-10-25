#!/bin/sh
# keepalived health probe: /healthz decides VIP ownership. Distinguish unreachable from unhealthy:
#   200          Healthy; retain VIP.
#   Other HTTP   Service reports unhealthy; release VIP immediately.
#   Unreachable  Role transition may be in progress (a brief systemd restart).
#                Avoid VIP flapping by allowing a 15-second grace period after a role switch.
URL=http://127.0.0.1:@API_PORT@/healthz
SWITCH=/run/ndiskless.switching
WINDOW=15

# keepalived kills timed-out scripts; worst case is 1 + 0.5 + 1 = 2.5s, below its 5s timeout.
code=$(curl -s -o /dev/null -w '%{http_code}' -m 1 "$URL" 2>/dev/null)
case "$code" in
  200) exit 0 ;;
  000) ;;        # Unreachable: check the transition grace period below
  *)   exit 1 ;; # Explicitly unhealthy
esac

if [ -f "$SWITCH" ]; then
  now=$(date +%s)
  stamped=$(stat -c %Y "$SWITCH" 2>/dev/null || echo 0)
  if [ $((now - stamped)) -lt "$WINDOW" ]; then
    # Most transitions resume listening within a second or two; retry once.
    sleep 0.5
    [ "$(curl -s -o /dev/null -w '%{http_code}' -m 1 "$URL" 2>/dev/null)" = "200" ] && exit 0
    exit 0   # In grace period: wait for next probe instead of migrating VIP on a restart
  fi
fi
exit 1
