#!/bin/bash
# Tiered test suite: reconfigure the same hosts as standalone, two-node and three-node clusters.
# Destructive: phase 1 wipes and reinstalls three hosts, retaining data pools but clearing DB, config, keepalived and pool DB copies. Use test hosts only.
. "$(dirname "$0")/_common.sh"
: "${ND_HOSTS:?ND_HOSTS is missing from env}"
: "${ND_POOL_DISK:?ND_POOL_DISK is missing from env}"
echo "  About to wipe and reinstall: $ND_HOSTS"
exec python3 -u topology_suite.py
