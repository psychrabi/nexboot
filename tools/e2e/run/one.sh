#!/bin/bash
# Run one test matrix: ./one.sh backup_matrix.py [arguments], e.g. ./one.sh failover_matrix.py A4.
# Do not run directly with python3: _common.sh resolves the cluster token; bypassing it causes /internal/ 401 errors.
. "$(dirname "$0")/_common.sh"
M=${1:?Usage: one.sh <matrix.py> [matrix arguments]}
shift
[ -f "$M" ] || { echo "Matrix not found: $M" >&2; exit 2; }
case "$M" in
  ha_matrix.py|failover_matrix.py)
    # These suites switch between the active writer and another standby; do not hard-code nodes.
    read -r W O <<< "$(writer_and_other)"
    echo "  A=$W B=$O"
    exec env ND_NODE_A="$W" ND_NODE_B="$O" python3 -u "$M" "$@" ;;
  cluster_matrix.py)
    IFS=',' read -r N1 N2 N3 <<< "$ND_NODES"
    exec env ND_NODE_A="$N1" ND_NODE_B="$N2" ND_NODE_C="$N3" python3 -u "$M" "$@" ;;
  *)
    # ndapi matrices use ND_BASE; without it they connect to default 127.0.0.1:18080.
    # Resolve the current writer for imports, backups and other write operations.
    if grep -q "^from ndapi" "$M"; then
      read -r W _ <<< "$(writer_and_other)"
      echo "  ND_BASE=$W(current writer)"
      exec env ND_BASE="$W" python3 -u "$M" "$@"
    fi
    exec python3 -u "$M" "$@" ;;
esac
