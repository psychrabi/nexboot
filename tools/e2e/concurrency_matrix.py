#!/usr/bin/env python3
"""Concurrent image mutations: before one finishes, a conflicting request must either be rejected immediately with 409
or wait until it completes; overlapping modifications of one clone tree are forbidden.

Unit tests with a fake pool reproduced corruption from interleaved apply/delete leaving a dangling current point,
and interleaved Super Admin saves losing configuration names. Verify the same contract against real HTTP and storage.

Use a fresh blank data image: locking is per image, so a production image would block real operators
for the duration of this test.

Prerequisite: ND_BASE points to cluster VIP or standalone host. SSH is not required.
"""
import sys
import threading
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from ndapi import call, check, configs, section, skip, summary  # noqa: E402

PREFIX = "e2e-conc"
NAME = f"{PREFIX}-{time.strftime('%H%M%S')}"


def wait(cond, seconds=120, step=2):
    for _ in range(max(1, seconds // step)):
        got = cond()
        if got:
            return got
        time.sleep(step)
    return None


def at_once(*requests):
    """Issue requests concurrently and return (status, response) in the same order as arguments."""
    out = [None] * len(requests)
    gate = threading.Barrier(len(requests))

    def fire(i, req):
        gate.wait()
        out[i] = call(*req)

    threads = [threading.Thread(target=fire, args=(i, r)) for i, r in enumerate(requests)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    return out


def task(task_id):
    st, t = call("GET", f"/api/tasks/{task_id}")
    return t if st == 200 and isinstance(t, dict) else None


def settled(task_ids, seconds=180):
    """Wait for all tasks to complete, returning {task ID: task}."""
    def done():
        got = {i: task(i) for i in task_ids}
        if all(t and t.get("Status") in ("success", "failed") for t in got.values()):
            return got
        return None
    return wait(done, seconds) or {}


def reductions(cfg_id):
    st, res = call("GET", f"/api/configs/{cfg_id}/reductions")
    return (res or {}).get("items", []) if isinstance(res, dict) else []


def config(cfg_id):
    return next((c for c in configs(IMG) if c["ID"] == cfg_id), {})


def image_by_name(name):
    st, imgs = call("GET", "/api/images")
    return next((i for i in (imgs or {}).get("items", []) if i.get("Name") == name), None)


def refused_well(resp):
    """Expected rejection: HTTP 409 identifying the image and conflicting operation."""
    st, body = resp
    text = str(body)
    return st == 409 and NAME in text and "Please wait for the task to complete" in text


def cleanup_leftovers():
    st, imgs = call("GET", "/api/images")
    for img in (imgs or {}).get("items", []):
        if img.get("Name", "").startswith(PREFIX):
            call("DELETE", f"/api/images/{img['ID']}")


section("0 Prerequisite: fresh blank data image")
cleanup_leftovers()
st, body = call("POST", "/api/images/blank", {"name": NAME, "size_bytes": 1024 ** 3, "filesystem": "ntfs"})
made = wait(lambda: image_by_name(NAME), 180)
IMG = (made or {}).get("ID", "")
cfgs = configs(IMG) if IMG else []
CFG = cfgs[0]["ID"] if cfgs else ""
check("0a", "Blank data disk image created", bool(IMG) and bool(CFG), f"HTTP {st} {str(body)[:100]}")
if not CFG:
    sys.exit(summary())

try:
    section("1 Concurrent image operations: accept one and reject conflicts")
    resps = at_once(*[("POST", f"/api/configs/{CFG}/reductions", {"name": f"e2e-c1-{i}"}) for i in range(5)])
    accepted = [r[1].get("task_id") for r in resps if r[0] == 202 and isinstance(r[1], dict)]
    refused = [r for r in resps if r[0] != 202]
    check("1a", "At least one request accepted", len(accepted) >= 1, str([r[0] for r in resps]))
    if not refused:
        skip("1b", "Rejected request identifies image and active operation", "No collision: each previous request finished before the next arrived")
    else:
        check("1b", "All rejected requests return 409 and identify the image/operation",
              all(refused_well(r) and "Create restore point" in str(r[1]) for r in refused), str(refused[0])[:140])
    tasks = settled(accepted)
    check("1c", "All accepted tasks succeeded", len(tasks) == len(accepted) and all(t["Status"] == "success" for t in tasks.values()),
          str([(t.get("Status"), (t.get("Error") or "")[:60]) for t in tasks.values()]))
    names = [r.get("DisplayName") or r.get("Name", "") for r in reductions(CFG)]
    made_now = [n for n in names if "e2e-c1-" in n]
    check("1d", "Created restore-point count matches accepted requests", len(made_now) == len(accepted), f"Accepted {len(accepted)}; created {made_now}")

    section("2 Concurrent apply/delete: current restore point must remain valid")
    for rnd in range(3):
        call("POST", f"/api/configs/{CFG}/reductions", {"name": f"e2e-c2-{rnd}"})
        red = wait(lambda: next((r for r in reductions(CFG)
                                 if (r.get("DisplayName") or r.get("Name", "")).endswith(f"e2e-c2-{rnd}")), None), 120)
        if not red:
            check(f"2{rnd}-", f"Restore point created in round {rnd + 1}", False)
            continue
        delete, apply = at_once(("DELETE", f"/api/reductions/{red['ID']}", None),
                                ("POST", f"/api/reductions/{red['ID']}/apply", None))
        if delete[0] == 202 and isinstance(delete[1], dict):
            settled([delete[1].get("task_id")])
        alive = {r["ID"] for r in reductions(CFG)}
        current = config(CFG).get("DefaultReductionID")
        check(f"2{rnd}a", f"Round {rnd + 1}: current point refers to an existing restore point",
              current in alive, f"Current {current}; delete HTTP {delete[0]}, apply HTTP {apply[0]}")
        ok = lambda r: r[0] in (200, 202, 204, 404) or (r[0] == 409 and bool(str(r[1]).strip()))
        check(f"2{rnd}b", f"Round {rnd + 1}: requests either succeed or reject with reasons",
              ok(delete) and ok(apply), f"Delete {delete[0]} {str(delete[1])[:60]} / Apply {apply[0]} {str(apply[1])[:60]}")
        if apply[0] == 200 and red["ID"] not in alive:
            # Apply succeeded before delete; this is sequential, not interleaved, and current point was verified in 2a.
            print(f"      Round {rnd + 1}: applied before delete; current point moved to {current}")

    section("3 Verify database and pool consistency after operations")
    st, report = call("GET", "/api/system/consistency")
    issues = ((report or {}).get("issues") or []) if isinstance(report, dict) else []
    mine = [i for i in issues if i.get("kind", "").startswith("missing") and (IMG in str(i) or CFG in str(i))]
    check("3a", "No dangling database entries for this image", st == 200 and not mine, str(mine)[:160] if mine else f"HTTP {st}")
finally:
    section("9 Cleanup")
    gone = None
    for _ in range(4):
        call("DELETE", f"/api/images/{IMG}")
        gone = wait(lambda: image_by_name(NAME) is None, 60)
        if gone:
            break
    check("9a", "Temporary image deleted", bool(gone))

sys.exit(summary())
