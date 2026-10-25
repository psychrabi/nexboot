#!/usr/bin/env python3
"""Client load balancing matrix: automatic balancing, explicit pinning, sticky placement and failover re-placement.

Placement priority, from strongest to weakest:
    Super Admin stickiness > group pinning > previous node > least-loaded healthy node (automatic balancing)
Test each layer independently: pinning can appear to disable balancing, while sticky placement can
appear to override a pin. Without separate cases, operators cannot tell policy behavior from defects.

    ND_NODES / ND_VIP / ND_PASSWORD / ND_CLUSTER_TOKEN / ND_POOL

Creates bal-e2e-* groups and clients, then removes them.
"""
import re
import sys
import time
import urllib.parse

from ndcluster import (NODES, POOL, call, check, entry_base, host, node_id_of, pool_of,
                       roster, section, skip, ssh, ssh_out, summary, wait)

GROUP = "bal-e2e"
PREFIX = "bal-e2e-"


def main():
    if not NODES:
        raise SystemExit("ND_NODES is required")
    n = len(NODES)
    A = NODES[0]
    ids = [node_id_of(b) for b in NODES]
    by_id = {node_id_of(b): b for b in NODES}
    base = entry_base()

    tmpl = _template_group(base)
    image, config = tmpl.get("SystemImageID"), tmpl.get("SystemConfigID")
    if not (image and config):
        skip("0", "Load balancing", "No usable image/configuration; skip the matrix")
        return summary()

    _sweep_leftovers(base)
    group_id, made = None, []
    try:
        section(f"0 Prerequisites ({n} nodes)")
        check("0a", "Found reusable image and configuration", True, f"{image}/{config}")
        online = [x for x in roster(base).values() if x.get("online")]
        check("0b", f"All {n} nodes are online", len(online) == n, str([x["ip"] for x in online]))

        # Placement counts track decisions rather than sessions, so stale counts from earlier suites can create false negatives.
        # ReconcileLoad corrects counts from live sessions; wait for clone cleanup and warn if it cannot finish.
        settled = wait(lambda: all(
            int(ssh(h, f"zfs list -H -o name -r {pool_of(h)}/run 2>/dev/null | grep -c CLIENT- || true").stdout.strip() or 0) == 0
            for h in [host(b) for b in NODES]), 120, 5)
        if settled is None:
            print("    Warning: stale client clones remain; placement counts may be skewed")

        section("1 Automatic balancing without pinning")
        group_id = _make_group(base, GROUP, tmpl, node=None)
        check("1a", "Default group is not pinned to a storage node", group_id is not None, str(group_id))
        if group_id is None:
            # Without a group, further checks would produce misleading failures.
            skip("1-5", "All placement checks", "Group creation failed; see previous error")
            return summary()
        # Use twice as many clients as nodes to distinguish genuine balancing from chance.
        count = n * 2
        portal = _portal_to_node(base)
        norm = lambda t: portal.get(t, t)
        placed = {}
        for i in range(count):
            mac = f"0e:2e:c1:0b:00:{i:02x}"
            tid = _register(base, group_id, mac, f"{PREFIX}{i}")
            if tid:
                made.append(tid)
            placed[mac] = norm(_boot_target(base, mac))
            time.sleep(0.4)
        seen = {}
        for mac, tgt in placed.items():
            seen.setdefault(tgt, []).append(mac)
        check("1b", f"All {count} clients received a placement", all(placed.values()),
              str({m: t for m, t in placed.items() if not t}))
        if n > 1:
            check("1c", f"Placements span {n} different node addresses", len(seen) == n,
                  {k: len(v) for k, v in seen.items()})
            spread = sorted(len(v) for v in seen.values())
            # Check balancing by comparing minimum and maximum client counts per node.
            check("1d", "Node client loads are approximately equal", spread[-1] - spread[0] <= 1, str(spread))
        else:
            check("1c", "Standalone mode places all clients locally", len(seen) == 1, str(list(seen)))

        section("2 Sticky placement: repeat boots reuse the same node")
        mac0 = list(placed)[0]
        again = [norm(_boot_target(base, mac0)) for _ in range(3)]
        check("2a", "Three consecutive boots reuse the same node", len(set(again)) == 1 and again[0] == placed[mac0],
              f"{placed[mac0]} -> {again}")

        if n < 2:
            skip("3", "Explicit pinning", "Only one node; no alternative for pinning")
            skip("4", "Re-placement after node failure", "Only one node")
            return summary()

        section("3 Explicit pinning overrides automatic balancing")
        target = ids[-1]                     # Pin to the last node.
        st, body = _set_group_node(base, group_id, target)
        check("3a", "Group pinned to selected node", st in (200, 204), f"HTTP {st} {str(body)[:120]}")
        time.sleep(1)
        # Newly created clients must use the pinned node regardless of current load.
        pinned = []
        for i in range(count, count + 2):
            mac = f"0e:2e:c1:0b:00:{i:02x}"
            tid = _register(base, group_id, mac, f"{PREFIX}{i}")
            if tid:
                made.append(tid)
            pinned.append(norm(_boot_target(base, mac)))
        want_host = host(by_id[target])
        check("3b", "All new clients placed on the pinned node",
              all(t == want_host for t in pinned), f"want {want_host}, got {pinned}")

        section("4 Return to automatic balancing after unpinning")
        st, body = _set_group_node(base, group_id, "")
        check("4a", "Remove group pin", st in (200, 204), f"HTTP {st} {str(body)[:120]}")
        time.sleep(1)
        fresh = []
        for i in range(count + 2, count + 2 + n):
            mac = f"0e:2e:c1:0b:00:{i:02x}"
            tid = _register(base, group_id, mac, f"{PREFIX}{i}")
            if tid:
                made.append(tid)
            fresh.append(norm(_boot_target(base, mac)))
        check("4b", "New placements spread after unpinning", len({t for t in fresh if t}) > 1 or n == 1,
              str(fresh))

        section("5 Failover: clients move from failed node to healthy peers")
        # Stop a non-active node to test placement recovery without triggering VIP failover.
        r_now = roster(base)
        cands = [(b, i) for b, i in zip(NODES, ids) if (r_now.get(i) or {}).get("ha_state") != "active"]
        if not cands:
            skip("5", "Failure re-placement", "No standby node available to stop")
            return summary()
        victim, vid = cands[-1]
        vhost = host(victim)
        # Find a client initially placed on the victim node.
        victim_macs = [m for m, t in placed.items() if t == vhost]
        if not victim_macs:
            skip("5a", "Failure re-placement", f"No client was originally placed on {vhost}")
        else:
            ssh(vhost, "systemctl stop ndiskless")
            try:
                gone = wait(lambda: not (roster(base).get(vid) or {}).get("online", True), 180, 10)
                check("5a", "Roster marks the node offline", bool(gone),
                      "" if gone else f"{vhost} still appears online after 180s")
                mac = victim_macs[0]
                moved = wait(lambda: (lambda t: t if t and t != vhost else None)(norm(_boot_target(base, mac))), 90, 5)
                check("5b", "Client re-placed on a healthy node", bool(moved),
                      moved or f"Still points to {vhost}")
            finally:
                ssh(vhost, "systemctl start ndiskless")
            back = wait(lambda: (roster(base).get(vid) or {}).get("online"), 180, 10)
            check("5c", "Node returns online after recovery", bool(back),
                  "" if back else f"{vhost} did not recover online within 180s")

        return summary()
    finally:
        for tid in made:
            call(base, "DELETE", f"/api/terminals/{tid}")
        if group_id:
            call(base, "DELETE", f"/api/groups/{group_id}")


def _sweep_leftovers(base):
    """Remove matching test groups and clients left by previous runs.

    A previous run may have terminated before finally cleanup (timeout, kill or restart). Remaining resources
    cause group creation to fail, leaving group_id empty and cascading failures.
    Later symptoms such as unbalanced placement distract from the original failure.
    """
    st, body = call(base, "GET", "/api/terminals")
    for t in (body.get("items") or []) if st == 200 else []:
        if str(t.get("Name", "")).startswith(PREFIX):
            call(base, "DELETE", f"/api/terminals/{t['ID']}")
    st, body = call(base, "GET", "/api/groups")
    for g in (body.get("items") or []) if st == 200 else []:
        if g.get("Name") == GROUP:
            for t in (call(base, "GET", f"/api/terminals?group_id={g['ID']}")[1].get("items") or []):
                call(base, "DELETE", f"/api/terminals/{t['ID']}")
            call(base, "DELETE", f"/api/groups/{g['ID']}")


def _template_group(base):
    """Network and image parameters used for group creation.

    Prefer network settings from existing groups; hard-coded subnets may be rightly rejected in a new environment
    as outside the client NIC subnet, causing cascading test errors.
    Groups might not exist after a database reset even though images and configurations are reconstructed from storage;
    then derive the subnet from the local IP.
    """
    st, body = call(base, "GET", "/api/groups")
    items = (body.get("items") or []) if st == 200 else []
    if items:
        return items[0]
    img, cfg = _any_image(base)
    if not img:
        return {}
    head = host(base).rsplit(".", 1)[0]
    return {"StartIP": f"{head}.1", "Netmask": "255.255.255.0", "Gateway": "",
            "SystemImageID": img, "SystemConfigID": cfg}


def _any_image(base):
    """Any available image and one of its configurations; restored from the pool catalogue at startup."""
    st, body = call(base, "GET", "/api/images")
    for img in (body.get("items") or []) if st == 200 else []:
        st2, cfgs = call(base, "GET", f"/api/images/{img['ID']}/configs")
        for c in (cfgs.get("items") or []) if st2 == 200 else []:
            return img["ID"], c["ID"]
    return None, None


def _make_group(base, name, tmpl, node):
    head = (tmpl.get("StartIP") or "192.168.10.100").rsplit(".", 1)[0]
    body = {"name": name, "start_ip": f"{head}.150", "client_max": 40,
            "gateway": tmpl.get("Gateway", ""), "netmask": tmpl.get("Netmask", "255.255.255.0"),
            "dns1": "114.114.114.114", "dns2": "223.5.5.5",
            "system_image_id": tmpl.get("SystemImageID"),
            "system_config_id": tmpl.get("SystemConfigID")}
    if node:
        body["storage_server_id"] = node
    st, res = call(base, "POST", "/api/groups", body)
    if st in (200, 201):
        return res.get("ID") or res.get("id")
    print("    Group creation failed:", st, str(res)[:220])
    return None


def _set_group_node(base, group_id, node):
    """Reassigning a group must submit the complete record. Sending only the pin field fails full-request validation
    because subnet and image fields are required, just as the UI submits the entire form."""
    st, g = call(base, "GET", f"/api/groups/{group_id}")
    if st != 200:
        return st, g
    # Include is_default: the sole group after reset is the default, and removing that designation is rejected.
    body = {"name": g.get("Name"), "start_ip": g.get("StartIP"),
            "client_max": g.get("ClientMax"), "gateway": g.get("Gateway", ""),
            "netmask": g.get("Netmask", ""), "dns1": g.get("DNS1", ""), "dns2": g.get("DNS2", ""),
            "is_default": bool(g.get("IsDefault")),
            "system_image_id": g.get("SystemImageID"), "system_config_id": g.get("SystemConfigID"),
            "storage_server_id": node}
    return call(base, "PUT", f"/api/groups/{group_id}", body)


def _register(base, group_id, mac, name):
    st, res = call(base, "POST", "/api/terminals",
                   {"name": name, "mac": mac, "group_id": group_id})
    if st in (200, 201):
        return res.get("ID") or res.get("id")
    return None


def _boot_target(base, mac):
    """Identify the server serving this client's disk from the sanhook portal address.

    Extract only the IP: a portal is iscsi:<ip>:::0:<iqn>, where IQN includes client MAC.
    Including IQN would make each client appear to have a distinct placement, preventing balance checks.

    Use an authenticated request even though the client normally boots anonymously. /boot verifies the source IP:
    only the client itself or a signed-in administrator may request its boot configuration. The matrix runs
    from another node and queries six clients, which anonymous requests must not be allowed to do.
    Use the operator's authenticated session, matching how the console retrieves placements.
    The actual anonymous client boot path is covered separately by boot_matrix."""
    st, body = call(base, "GET", f"/boot?mac={urllib.parse.quote(mac)}", timeout=60)
    if st != 200:
        return ""
    if not isinstance(body, str):
        return ""
    m = re.search(r"iscsi:([0-9a-fA-F.:]+?):::", body)
    return m.group(1) if m else ""


def _portal_to_node(base):
    """Map portal addresses to node IPs. Active nodes report VIP and standbys report their own IP; without normalization,
    the active node and VIP would count as different placements and invalidate balancing checks."""
    m = {}
    for nid, n in roster(base).items():
        ip = n.get("ip")
        if ip:
            m[ip] = ip
        p = n.get("portal_ip")
        if p:
            m[p] = ip
    return m


if __name__ == "__main__":
    sys.exit(main())
