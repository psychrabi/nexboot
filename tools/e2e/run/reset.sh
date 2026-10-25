#!/bin/bash
# Reset the cluster to a clean baseline before full regression runs or whenever test state is suspect.
#   ./reset.sh           Reset
#   ./reset.sh --check   Check only; do not change state
. "$(dirname "$0")/_common.sh"
exec python3 -u reset.py "$@"
