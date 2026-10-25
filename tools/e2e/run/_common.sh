# Sourced by all runner scripts: load environment variables and validate required settings.
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
E2E=$(cd "$HERE/.." && pwd)
ENV_FILE=${ND_ENV:-$HERE/env}
if [ ! -f "$ENV_FILE" ]; then
  echo "Missing $ENV_FILE — copy $HERE/env.example to $HERE/env and configure it" >&2
  exit 2
fi
set -a; . "$ENV_FILE"; set +a
: "${ND_NODES:?ND_NODES is missing from env}"
: "${ND_VIP:?ND_VIP is missing from env}"
: "${ND_PASSWORD:?ND_PASSWORD is missing from env}"
cd "$E2E"

# Cluster tokens are regenerated during Create Cluster. The env token becomes stale after rebuilding a cluster.
# An outdated token makes /internal/ tests fail with "cluster token required". Prefer the local product configuration;
# only use env when local configuration is unavailable outside the cluster.
if [ -r /etc/ndiskless/ndiskless.env ] || sudo -n true 2>/dev/null; then
  real_token=$(sudo -n sed -n 's/^NDISKLESS_CLUSTER_TOKEN=//p' /etc/ndiskless/ndiskless.env 2>/dev/null | tr -d '"' | head -1 || true)
  if [ -z "${real_token:-}" ] && [ -n "${ND_SSH_PASSWORD:-${ND_PASSWORD:-}}" ]; then
    real_token=$(echo "${ND_SSH_PASSWORD:-$ND_PASSWORD}" | sudo -S -p '' \
      sed -n 's/^NDISKLESS_CLUSTER_TOKEN=//p' /etc/ndiskless/ndiskless.env 2>/dev/null | tr -d '"' | head -1 || true)
  fi
  if [ -n "${real_token:-}" ] && [ "${real_token}" != "${ND_CLUSTER_TOKEN:-}" ]; then
    echo "Using cluster token from local product configuration (the env token is stale)" >&2
    export ND_CLUSTER_TOKEN="$real_token"
  fi
fi

# writer_and_other prints the current writer and a different standby. Any of three nodes can hold the VIP,
# and ha_matrix requires node A to be the active writer, so do not hard-code its address.
writer_and_other() {
  python3 -c "
import os, json, urllib.request
nodes = os.environ['ND_NODES'].split(',')
tok = os.environ.get('ND_CLUSTER_TOKEN', '')
active, standby = '', ''
for b in nodes:
    try:
        rq = urllib.request.Request(b + '/internal/ha/status', headers={'Authorization': 'Bearer ' + tok})
        st = json.load(urllib.request.urlopen(rq, timeout=4))
        if st.get('role') == 'active': active = b
        elif not standby: standby = b
    except Exception: pass
active = active or nodes[0]
standby = standby or next((n for n in nodes if n != active), nodes[-1])
print(active, standby)
"
}

wait_roster() {
  python3 -c "
import sys; sys.path.insert(0, '.')
from ndcluster import wait_roster_online, entry_base
n = len('${ND_NODES}'.split(','))
print('Roster converged:', bool(wait_roster_online(entry_base(), n, 300)))
"
}
