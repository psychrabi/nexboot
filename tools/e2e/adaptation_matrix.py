#!/usr/bin/env python3
"""Super Admin offline adaptation: write the driver bundle and adaptation script into its persistent clone.

The server orchestration and on-disk layout need real-host validation: unit tests can inspect which files were requested,
but not whether the clone exists, files reached NTFS, and terminal records were updated.
Actual Windows driver installation by adapt.ps1 still requires manual verification; see the adaptation manual-test documentation.

Requires a real Windows image that passes health checks (ND_WINDOWS_SOURCE). This suite creates and removes
its own image/group/client. Do not run against a production pool.
"""
import os
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from ndapi import (IMPORT_DIR, POOL, SSH_HOST, call, check, configs, mount_probe,  # noqa: E402
                   reductions, section, ssh, summary, task_of)

SOURCE = os.environ.get("ND_WINDOWS_SOURCE", f"{IMPORT_DIR}/isharedisk.gzip")
IMG = "e2e-adapt"
CFG = f"{IMG}_default"
MAC = "AA:BB:CC:22:33:44"
TERMINAL = "terminal-AABBCC223344"
CLONE = f"{POOL}/run/SCLIENT-AABBCC223344"


def cleanup():
    call("DELETE", f"/api/terminals/{TERMINAL}")
    for g in call("GET", "/api/groups")[1]["items"]:
        if g["Name"].startswith("e2e-"):
            call("DELETE", f"/api/groups/{g['ID']}")
    ssh(f"zfs destroy -r {CLONE} 2>/dev/null")
    if call("GET", f"/api/images/{IMG}")[0] == 200:
        for c in configs(IMG):
            call("DELETE", f"/api/configs/{c['ID']}")
            time.sleep(2)
        call("DELETE", f"/api/images/{IMG}")
        time.sleep(3)


cleanup()

section("A Prepare Super Admin client")
call("POST", "/api/images/import", {"name": IMG, "source_path": SOURCE, "os_type": "windows"})
task = task_of("task-import_image", timeout=900)
check("A1", "Image import completed", task.get("Status") == "success", (task.get("Error") or "")[:160])
time.sleep(10)  # Wait for the health check's temporary clone to be released.

# Derive client subnet from ND_SSH_HOST; the server rejects groups outside the client NIC subnet.
SEG = ".".join(SSH_HOST.split(".")[:3])
st, group = call("POST", "/api/groups", {
    "name": "e2e-adaptation-group", "start_ip": f"{SEG}.200", "client_max": 10,
    "gateway": "", "netmask": "255.255.255.0",
    "system_image_id": IMG, "system_config_id": CFG,
    "system_reduction_id": reductions(CFG)[0]["ID"]})
check("A2", "Create group", st == 201, str(group)[:120])
gid = group["ID"] if st == 201 else ""
st, _ = call("POST", "/api/terminals", {"mac": MAC, "name": "Adaptation Client", "group_id": gid})
check("A3", "Create client", st in (200, 201))

# Regular clients have no persistent writable clone.
st, body = call("POST", f"/api/terminals/{TERMINAL}/inject-driver", {"bundle_id": ""})
check("A4", "Regular clients cannot receive injection (enable Super Admin first)", st in (400, 409), f"HTTP {st} {str(body)[:100]}")

st, _ = call("POST", f"/api/terminals/{TERMINAL}/super", {})
check("A5", "Enable Super Admin", st in (200, 201, 204))

section("B Injection: clone and on-disk layout")
st, result = call("POST", f"/api/terminals/{TERMINAL}/inject-driver", {"bundle_id": ""})
check("B1", "Injection without a driver bundle installs only the drive-letter script", st in (200, 201), f"HTTP {st} {str(result)[:160]}")
check("B2", "Super Admin persistent clone created", CLONE in ssh(f"zfs list -H -o name | grep SCLIENT || true"),
      ssh("zfs list -H -o name | grep SCLIENT || true"))

# Mount the clone's Windows partition like the product and inspect files on disk; no unit test covers this.
probe = mount_probe(CLONE,
                    'find Windows/System32/GroupPolicy -maxdepth 4 2>/dev/null; '
                    'echo "--scripts.ini--"; cat Windows/System32/GroupPolicy/Machine/Scripts/scripts.ini 2>/dev/null; '
                    'ls ndadapt 2>/dev/null | sed "s/^/--ndadapt--/"')
check("B3", "Adaptation script installed in local GPO startup directory", "Startup/adapt.ps1" in probe, probe.replace("\n", " | ")[:220])
check("B4", "Startup script registered by ndadapt.cmd", "0CmdLine=ndadapt.cmd" in probe, probe.replace("\n", " | ")[:160])
check("B5", "Local policy has gpt.ini for gpsvc", "GroupPolicy/gpt.ini" in probe)

st, terminal = call("GET", f"/api/terminals/{TERMINAL}")
pending = (terminal or {}).get("PendingBundleAt")
check("B6", "Client records pending adaptation", bool(pending), str(terminal)[:160])

st, view = call("GET", f"/api/terminals/{TERMINAL}/inject-result")
check("B7", "Status remains incomplete until the client runs",
      st == 200 and view.get("done") is False and view.get("injected_at"),
      f"HTTP {st} {str(view)[:120]}")

section("C Repeated injection")
st, body = call("POST", f"/api/terminals/{TERMINAL}/inject-driver", {"bundle_id": ""})
# Rejections must give actionable instructions. This API currently returns 400 (other conflicts use 409).
check("C1", "Reject overwriting pending adaptation and explain recovery",
      st in (400, 409) and "overwrite" in str(body), f"HTTP {st} {str(body)[:120]}")
st, body = call("POST", f"/api/terminals/{TERMINAL}/inject-driver", {"bundle_id": "", "overwrite": True})
check("C2", "Explicit overwrite resets the clone for retry", st in (200, 201), f"HTTP {st} {str(body)[:120]}")

section("D Cleanup")
st, _ = call("DELETE", f"/api/terminals/{TERMINAL}/super")
check("D1", "Disable Super Admin", st in (200, 204))
cleanup()
check("D2", "Super Admin clone is cleaned up", "SCLIENT-AABBCC223344" not in ssh("zfs list -H -o name || true"),
      ssh("zfs list -H -o name | grep SCLIENT || true"))

sys.exit(summary())
