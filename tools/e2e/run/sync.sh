#!/bin/bash
# Distribute the test harness to every node. Matrices run in-cluster and topology_suite launches others from HOSTS[0].
# Usage: run ./sync.sh [ssh-port] on the development host; default port 22 for ND_NODES from env.
. "$(dirname "$0")/_common.sh"
DEST=${ND_E2E_DIR:-/home/${ND_SSH_USER}/e2e}
hosts() { echo "$ND_NODES" | tr ',' '\n' | sed -E 's#^https?://##; s#:[0-9]+$##'; }
for h in $(hosts); do
  echo -n "  $h: "
  sshpass -p "$ND_SSH_PASSWORD" ssh -o StrictHostKeyChecking=no "$ND_SSH_USER@$h" "mkdir -p $DEST $DEST/run" \
    && sshpass -p "$ND_SSH_PASSWORD" scp -q -o StrictHostKeyChecking=no "$E2E"/*.py "$ND_SSH_USER@$h:$DEST/" \
    && sshpass -p "$ND_SSH_PASSWORD" scp -q -o StrictHostKeyChecking=no "$E2E"/run/*.sh "$E2E"/run/env "$ND_SSH_USER@$h:$DEST/run/" \
    && echo OK || echo Failed
done
