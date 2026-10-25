#!/usr/bin/env python3
"""User and alarm operations against a real database: CRUD, relogin, acknowledgment and deletion.

Unit tests cover validation but not actual password changes, logins or alarms
persisting from a scanner and being acknowledged/deleted. Creates and removes test users; do not run in production.

Storage pool cache operations are excluded because only lsblk TYPE=disk devices can be selected; loop
devices are not accepted. Hosts without a free physical disk cannot exercise that path. See
the manual storage-pool cache test documentation。
"""
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import ndapi  # noqa: E402
from ndapi import (BASE, IMPORT_DIR, call, check, section, skip, ssh,  # noqa: E402
                   summary, task_of)

USER = "e2e-operator"
PW1 = "e2e-Passw0rd!"
PW2 = "e2e-Passw0rd!2"


def users():
    return call("GET", "/api/users")[1]["items"]


def find_user(name):
    return next((u for u in users() if u.get("username") == name), None)


def cleanup():
    for u in users():
        if u.get("username", "").startswith("e2e-"):
            call("DELETE", f"/api/users/{u['id']}")


def login_as(username, password):
    """A fresh login, without disturbing the session this script runs on."""
    import json
    import urllib.error
    import urllib.request
    req = urllib.request.Request(
        BASE + "/api/login", method="POST",
        data=json.dumps({"username": username, "password": password}).encode(),
        headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode().strip()


ndapi.login()
cleanup()

section("A Users")
st, body = call("POST", "/api/users", {"username": USER, "password": PW1})
check("A1", "Create user", st == 201, f"HTTP {st} {str(body)[:120]}")
uid = body.get("id") or body.get("ID") if st == 201 else ""

check("A2", "Duplicate username returns 409", call("POST", "/api/users", {"username": USER, "password": PW1})[0] == 409)
check("A3", "Empty password returns 400", call("POST", "/api/users", {"username": "e2e-empty-password", "password": ""})[0] == 400)

st, _ = login_as(USER, PW1)
check("A4", "New user can sign in", st == 200, f"HTTP {st}")
check("A5", "Incorrect password returns 401", login_as(USER, "wrong")[0] == 401)

st, body = call("POST", f"/api/users/{uid}/password", {"old_password": PW1, "new_password": PW2})
check("A6", "Change password", st in (200, 204), f"HTTP {st} {str(body)[:100]}")
check("A7", "Old password rejected after change", login_as(USER, PW1)[0] == 401)
check("A8", "New password works after change", login_as(USER, PW2)[0] == 200)
check("A9", "Reject password change with incorrect old password", call("POST", f"/api/users/{uid}/password",
                                {"old_password": "wrong", "new_password": "x-Passw0rd!"})[0] in (400, 401, 403))

st, _ = call("PUT", f"/api/users/{uid}", {"username": "e2e-renamed"})
check("A10", "Rename user", st in (200, 204) and find_user("e2e-renamed") is not None, f"HTTP {st}")

st, _ = call("DELETE", f"/api/users/{uid}")
check("A11", "Delete user", st in (200, 204) and find_user("e2e-renamed") is None, f"HTTP {st}")
check("A12", "Deleted user cannot sign in", login_as("e2e-renamed", PW2)[0] == 401)

# Prevent deleting the current account and locking all operators out.
admin = next((u for u in users() if u.get("username") == "admin"), None)
if admin:
    st, body = call("DELETE", f"/api/users/{admin['id']}")
    check("A13", "Cannot delete the last administrator", st >= 400, f"HTTP {st} {str(body)[:100]}")

section("B Alarms")
# Alarm scanner responds to real conditions; create an image that fails health checks.
BROKEN = "e2e-broken-image"
ssh(f"truncate -s 64M {IMPORT_DIR}/e2e-broken.raw")
call("POST", "/api/images/import",
     {"name": BROKEN, "source_path": f"{IMPORT_DIR}/e2e-broken.raw", "os_type": "windows"})
task_of("task-import_image", timeout=300)
# Alarm resource IDs come from normalized image names; resolve the ID rather than assuming it matches display name.
broken_id = next((i["ID"] for i in call("GET", "/api/images")[1]["items"] if i["Name"] == BROKEN), "")

raised = None
for _ in range(9):  # Scanner runs every 30 seconds
    time.sleep(6)
    raised = next((a for a in call("GET", "/api/alarms?size=100")[1]["items"]
                   if a.get("Resource") == broken_id and a.get("Status") == "active"), None)
    if raised:
        break
check("B0", "Health-failing image triggers scanner alarm", bool(raised),
      f"Image {broken_id}: no alarm after 54 seconds" if not raised else raised.get("Message", ""))

st, listing = call("GET", "/api/alarms?size=100")
check("B1", "Alarms can be listed", st == 200 and "items" in listing, f"HTTP {st}")
alarms = listing.get("items", []) if st == 200 else []

active = [a for a in alarms if a.get("Status") == "active"]
if active:
    target = next((a for a in active if a.get("Resource") == broken_id), active[0])
    st, _ = call("POST", f"/api/alarms/{target['ID']}/ack")
    check("B2", "Acknowledge alarm", st == 200, f"HTTP {st}")
    time.sleep(1)
    now = next((a for a in call("GET", "/api/alarms?size=100")[1]["items"] if a["ID"] == target["ID"]), {})
    check("B3", "Alarm status becomes acknowledged", now.get("Status") == "acknowledged", str(now.get("Status")))
else:
    skip("B2", "Acknowledge alarm", "No active alarms (scanner responds to actual conditions)")
    skip("B3", "Alarm status becomes acknowledged", "Same reason")

check("B4", "Filter by status", all(a.get("Status") == "recovered"
                          for a in call("GET", "/api/alarms?status=recovered&size=100")[1]["items"]))
check("B5", "Filter by severity", all(a.get("Severity") == "error"
                          for a in call("GET", "/api/alarms?severity=error&size=100")[1]["items"]))

if alarms:
    victim = alarms[-1]
    st, _ = call("DELETE", f"/api/alarms/{victim['ID']}")
    check("B6", "Delete alarm", st == 200, f"HTTP {st}")
    check("B7", "Deleted alarm is absent",
          not [a for a in call("GET", "/api/alarms?size=100")[1]["items"] if a["ID"] == victim["ID"]])
else:
    skip("B6", "Delete alarm", "No alarms in the database")
    skip("B7", "Deleted alarm is absent", "Same reason")

check("B8", "Acknowledging a nonexistent alarm returns 404", call("POST", "/api/alarms/ghost/ack")[0] == 404)

# Alarms must automatically resolve when the underlying cause disappears.
for c in call("GET", f"/api/images/{broken_id}/configs")[1].get("items", []):
    call("DELETE", f"/api/configs/{c['ID']}")
    time.sleep(2)
# Deletion may return 409 during a background health check; retry after a short delay.
for _ in range(10):
    if call("DELETE", f"/api/images/{broken_id}")[0] != 409:
        break
    time.sleep(6)
recovered = False
for _ in range(9):
    time.sleep(6)
    still = [a for a in call("GET", "/api/alarms?size=100")[1]["items"]
             if a.get("Resource") == broken_id and a.get("Status") == "active"]
    if not still:
        recovered = True
        break
check("B9", "Alarm automatically recovers after cause disappears", recovered, "" if recovered else "Still active after 54 seconds")

section("C Audit")
st, logs = call("GET", "/api/logs?type=operation&size=20")
check("C1", "Recent mutations appear in audit log", st == 200 and logs.get("total", 0) > 0, f"HTTP {st} total={logs.get('total')}")
modules = {row.get("Module") for row in logs.get("items", [])}
check("C2", "Audit entries contain recognized module identifiers", "\u7528\u6237" in modules or "\u544a\u8b66" in modules, str(modules)[:120])

cleanup()
sys.exit(summary())
