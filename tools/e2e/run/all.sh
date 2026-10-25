#!/bin/bash
# Nine regression suites: cluster administration, load balancing, three-node cluster, HA recovery, failover, Super Admin/data-disk publish, concurrency, backups and catalogue preservation.
# Run on a cluster member. Usage: ND_ENV=... ./all.sh
. "$(dirname "$0")/_common.sh"

# Reset to a clean baseline so leftovers from earlier runs cannot cause false failures.
# ND_SKIP_RESET=1 skips reset. Investigate leftover state before attributing failures to the product.
if [ "${ND_SKIP_RESET:-0}" != "1" ]; then
  echo "##### 0 Reset to clean baseline #####"
  if ! python3 -u reset.py; then
    echo "!! Reset failed; stopping because results from a dirty environment are unreliable"
    exit 2
  fi
fi

wait_roster
read -r WRITER OTHER <<< "$(writer_and_other)"
echo "  ha_matrix parameters: A=$WRITER B=$OTHER"

# Use PIPESTATUS[0] for exit code; $? would report tail's exit status.
# Disable errexit while running: pipefail propagates suite errors and set -e would silently abort later suites.
# Report failures without stopping the full regression run.
run() { # Number, title, script, optional environment
  local no=$1 title=$2 script=$3; shift 3
  echo "##### $no $title #####"
  # Wait for replication to catch up before each suite, avoiding false timeouts after previous failover tests.
  # Continue with a warning if synchronization does not complete.
  python3 -u wait_settled.py 600 || echo "!! Baseline is not clean; interpret this suite's results cautiously"
  set +e
  env "$@" timeout 2400 python3 -u "$script" 2>&1 | tee "/tmp/e2e-$no.log" | tail -40
  local rc=${PIPESTATUS[0]}
  set -e
  echo "RC$no=$rc"
}

run 1 "Cluster management (three nodes)" cluster_ops_matrix.py
run 2 "Load balancing (three nodes)" balance_matrix.py
IFS=',' read -r N1 N2 N3 <<< "$ND_NODES"
run 3 "Three-node cluster"       cluster_matrix.py "ND_NODE_A=$N1" "ND_NODE_B=$N2" "ND_NODE_C=$N3"
run 4 "HA recovery"          ha_recovery_matrix.py
run 5 "Two-node failover"         ha_matrix.py "ND_NODE_A=$WRITER" "ND_NODE_B=$OTHER"
# Run Super Admin tests after failover suites; they manage their temporary state and require only one writer.
run 6 "Super Admin and data disk publishing" super_matrix.py "ND_BASE=http://$ND_VIP:8080"
# Only touch a newly created blank data disk image; the test leaves no persistent state.
run 7 "Same-image concurrency"       concurrency_matrix.py "ND_BASE=http://$ND_VIP:8080"
# Temporarily change backup settings and perform planned switchover, then restore settings and clean up copies.
run 8 "Cluster backups"         backup_cluster_matrix.py
# Run last because this suite creates a catalogue fork on a standby that requires a full rebuild.
run 9 "Catalogue preservation and visibility" catalogue_matrix.py "ND_BASE=http://$ND_VIP:8080"
echo ALL-DONE
