#!/usr/bin/env python3
"""Wait for cluster convergence: three healthy nodes, exactly one writer and synchronized catalogue. Observe only; do not clean up.

Use this between suites. After service kills, switchovers or power failures, replication may still be recovering,
so comparing catalogues immediately may wait for a state that cannot yet occur.

Observed on hardware: ha_matrix after reset passed 1a in a second and all 20 tests, but when run fifth
in all.sh after heavy failover tests, 1a could not converge within 420 seconds. The baseline differed.

reset.py clears state, while this waits for convergence. Use reset once at the start
and wait between suites; cleanup is destructive and should not repeat for every suite.
"""
import shlex
import sys
import time

sys.path.insert(0, ".")
from ndcluster import NODES, VIP, host, pool_of, ssh_out  # noqa: E402

if not VIP:
    raise SystemExit("Set ND_VIP to the test cluster's virtual IP before running this script.")

DEADLINE = int(sys.argv[1]) if len(sys.argv) > 1 else 600


def snapshot():
    """(health of each node, VIP holder, latest marker per node)"""
    health, vip, marks = {}, [], {}
    born.clear()
    for b in NODES:
        h = host(b)
        health[h] = '"status":"ok"' in ssh_out(
            h, "curl -s -m 5 http://127.0.0.1:8080/healthz", timeout=30)[1]
        if ssh_out(h, f"ip -4 -o addr show | awk -v vip={shlex.quote(VIP)} "
                      "'{split($4, ip, \"/\"); if (ip[1] == vip) found=1} END {print found+0}'",
                   timeout=20)[1] == "1":
            vip.append(h)
        # Inspect two most recent root revisions to allow a standby to lag one revision.
        rows = ssh_out(h, "zfs list -H -p -o name,creation -s creation -t snapshot -d 1 %s/nd 2>/dev/null "
                          "| grep @rep- | tail -2" % pool_of(h), timeout=30)[1].splitlines()
        marks[h] = [r.split()[0].split("@")[-1] for r in rows if r.strip()]
        born[h] = {r.split()[0].split("@")[-1]: int(r.split()[1]) for r in rows if len(r.split()) > 1}
    return health, vip, marks


born = {}  # node -> {marker: creation time}


def in_step(vip, marks):
    """Every node has reached one of the writer's latest two revisions.

    The writer snapshots once per minute and standbys pull independently; their phases depend on startup times.
    With unfavorable timing, a standby may lag by one revision for over 50 seconds per minute; this is normal.
    On 2026-09-29, the writer snapshotted at second 10 and node .3 pulled at second 7, leaving only
    3 seconds with identical markers. Two exact-convergence checks timed out after 600 seconds.
    """
    recent = set(marks.get(vip[0]) or [])
    # A marker created after waiting begins includes the previous suite's final writes. A standby behind this revision
    # is not caught up, or a subsequent takeover would lose those writes.
    fresh = {m for m, t in born.get(vip[0], {}).items() if t >= START}
    if fresh:
        recent = fresh
    return bool(recent) and all(m and m[-1] in recent for m in marks.values())


t0 = time.time()
# Wait for a fresh writer marker after previous changes before checking replication convergence.
START = int(t0)
time.sleep(75)
last = None
while time.time() - t0 < DEADLINE:
    health, vip, marks = snapshot()
    last = (health, vip, marks)
    settled = all(health.values()) and len(vip) == 1 and in_step(vip, marks)
    if settled:
        print("Cluster settled after %.0fs: writer %s; nodes lag by at most one revision %s"
              % (time.time() - t0, vip[0], {h: (m[-1] if m else "") for h, m in marks.items()}), flush=True)
        sys.exit(0)
    time.sleep(10)

health, vip, marks = last or snapshot()
print("Cluster did not settle within %ds: unhealthy=%s VIP holders=%s markers=%s"
      % (DEADLINE, [h for h, ok in health.items() if not ok], vip, marks), flush=True)
print("The next suite will start while the cluster is recovering; failures may be unrelated to its intended checks.", flush=True)
sys.exit(1)
