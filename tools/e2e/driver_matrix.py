#!/usr/bin/env python3
"""Driver Center: upload driver packs → create bundles → inject into Super Admin → download archive.

Unit tests cover INF parsing and rejection rules, but cannot prove ZIP upload and on-disk storage,
bundle download or injection into a real Super Admin clone. Previously this path had
only manual validation, without automated E2E coverage.

Creates and deletes driver packs, bundles, images, groups and clients. Do not run in production.
"""
import io
import json
import os
import sys
import time
import urllib.request
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import ndapi  # noqa: E402
from ndapi import (BASE, IMPORT_DIR, call, check, configs, get_bytes,  # noqa: E402
                   mount_probe, reductions, section, ssh, summary, task_of)

SOURCE = os.environ.get("ND_WINDOWS_SOURCE", f"{IMPORT_DIR}/isharedisk.gzip")
IMG = "e2e-driver"
CFG = f"{IMG}_default"
MAC = "AA:BB:CC:33:44:55"
TERMINAL = "terminal-AABBCC334455"

# Minimal parseable driver has one INF and one PCI hardware ID.
SAMPLE_INF = """; e2e sample
[Version]
Signature   = "$WINDOWS NT$"
Class       = Net
Provider    = %V_E2E%
DriverVer   = 01/01/2026,1.0.0.1

[Manufacturer]
%V_E2E% = E2E, NTamd64.10.0

[E2E.NTamd64.10.0]
%E2E_DESC% = E2E.ndi, PCI\\VEN_8086&DEV_{dev}

[Strings]
V_E2E = "E2E"
E2E_DESC = "E2E Test NIC"
"""


def pack_zip(dev="15B7"):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        z.writestr("e2enic.inf", SAMPLE_INF.format(dev=dev))
        z.writestr("e2enic.sys", b"\x00" * 64)
    return buf.getvalue()


def upload_pack(name, data, category="boot_critical_nic", os_type="windows", arch="x64"):
    """Multipart upload, hand-built: the endpoint takes a form, not JSON."""
    boundary = "----e2e-driver-boundary"
    parts = []
    for field, value in (("name", name), ("category", category), ("os_type", os_type), ("arch", arch)):
        parts.append(f"--{boundary}\r\nContent-Disposition: form-data; name=\"{field}\"\r\n\r\n{value}\r\n".encode())
    parts.append(
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}.zip\"\r\n"
        f"Content-Type: application/zip\r\n\r\n".encode() + data + b"\r\n")
    parts.append(f"--{boundary}--\r\n".encode())
    req = urllib.request.Request(BASE + "/api/driver-packs", data=b"".join(parts), method="POST")
    req.add_header("Authorization", "Bearer " + (ndapi._token or ndapi.login()))
    req.add_header("Content-Type", f"multipart/form-data; boundary={boundary}")
    try:
        with urllib.request.urlopen(req, timeout=90) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode().strip()


def cleanup():
    call("DELETE", f"/api/terminals/{TERMINAL}")
    for g in call("GET", "/api/groups")[1]["items"]:
        if g["Name"].startswith("e2e-"):
            call("DELETE", f"/api/groups/{g['ID']}")
    for b in call("GET", "/api/driver-bundles")[1]["items"]:
        if b["bundle"]["Name"].startswith("e2e-"):
            call("DELETE", f"/api/driver-bundles/{b['bundle']['ID']}")
    for p in call("GET", "/api/driver-packs")[1]["items"]:
        if p["pack"]["Name"].startswith("e2e-"):
            call("DELETE", f"/api/driver-packs/{p['pack']['ID']}")
    ssh("zfs destroy -r $(zfs list -H -o name | grep SCLIENT-AABBCC334455) 2>/dev/null")
    if call("GET", f"/api/images/{IMG}")[0] == 200:
        for c in configs(IMG):
            call("DELETE", f"/api/configs/{c['ID']}")
            time.sleep(2)
        call("DELETE", f"/api/images/{IMG}")
        time.sleep(3)


ndapi.login()
cleanup()

section("A Upload Driver Packs")
st, body = upload_pack("e2e-nic-a", pack_zip("15B7"))
check("A1", "Upload ZIP containing an INF", st in (200, 201), f"HTTP {st} {str(body)[:140]}")
pack_a = body["pack"]["ID"] if st in (200, 201) else ""
hwids = body.get("pack", {}).get("HWIDs") if st in (200, 201) else None
check("A2", "Parse hardware ID from INF", bool(hwids) and "VEN_8086&DEV_15B7" in str(hwids), str(hwids))
check("A2b", "Parse version and vendor",
      body.get("pack", {}).get("Version") == "1.0.0.1" and body.get("pack", {}).get("Vendor") == "E2E",
      str(body.get("pack", {}))[:120])

st, body = upload_pack("e2e-nic-b", pack_zip("15B8"))
pack_b = body["pack"]["ID"] if st in (200, 201) else ""
check("A3", "Second driver pack", bool(pack_b), f"HTTP {st}")

# Reject non-driver ZIPs during upload, not at client boot-time injection.
empty = io.BytesIO()
with zipfile.ZipFile(empty, "w") as z:
    z.writestr("readme.txt", "no drivers here")
st, body = upload_pack("e2e-not-a-driver", empty.getvalue())
check("A4", "Reject ZIPs without an INF immediately", st == 400 and "inf" in str(body), f"HTTP {st} {str(body)[:100]}")

section("B Create Driver Bundle")
st, body = call("POST", "/api/driver-bundles", {"name": "e2e-bundle", "pack_ids": [pack_a, pack_b]})
bundle = body["bundle"]["ID"] if st == 201 else ""
check("B1", "Create bundle from two driver packs", st == 201, f"HTTP {st} {str(body)[:140]}")
check("B2", "Duplicate driver bundle returns 409", call("POST", "/api/driver-bundles", {"name": "e2e-bundle", "pack_ids": [pack_a]})[0] == 409)
check("B3", "Empty pack list returns 400", call("POST", "/api/driver-bundles", {"name": "e2e-empty", "pack_ids": []})[0] == 400)

# Server must reject disabled driver packs in bundles, not just disable UI selection.
call("POST", f"/api/driver-packs/{pack_b}/status", {"status": "disabled"})
st, body = call("POST", "/api/driver-bundles", {"name": "e2e-with-disabled", "pack_ids": [pack_a, pack_b]})
check("B4", "Reject bundles containing disabled driver packs", st == 400 and "disabled" in str(body), f"HTTP {st} {str(body)[:100]}")
call("POST", f"/api/driver-packs/{pack_b}/status", {"status": "enabled"})

check("B5", "Cannot delete a driver pack referenced by a bundle", call("DELETE", f"/api/driver-packs/{pack_a}")[0] == 409)

section("C Download Archive")
st, raw, headers = get_bytes(f"/api/driver-bundles/{bundle}/archive")
check("C1", "Driver bundle can be downloaded as ZIP", st == 200 and raw[:2] == b"PK", f"HTTP {st} {raw[:40]!r}")
check("C2", "Provide browser download headers",
      headers.get("Content-Type") == "application/zip" and ".zip" in headers.get("Content-Disposition", ""),
      f"{headers.get('Content-Type')} / {headers.get('Content-Disposition')}")
# Super Admin extracts this archive; both driver packs must be present.
with zipfile.ZipFile(io.BytesIO(raw)) as z:
    names = z.namelist()
check("C3", "Archive contains INF files from both packs", sum(1 for n in names if n.lower().endswith(".inf")) >= 2, " ".join(names)[:160])

section("D Inject into Super Admin")
call("POST", "/api/images/import", {"name": IMG, "source_path": SOURCE, "os_type": "windows"})
task = task_of("task-import_image", timeout=900)
check("D1", "Image import completed", task.get("Status") == "success", (task.get("Error") or "")[:120])
time.sleep(10)

# Derive subnet from ND_SSH_HOST; invalid group subnets are rejected.
SEG = ".".join(ndapi.SSH_HOST.split(".")[:3])
st, group = call("POST", "/api/groups", {
    "name": "e2e-driver-group", "start_ip": f"{SEG}.210", "client_max": 10,
    "gateway": "", "netmask": "255.255.255.0",
    "system_image_id": IMG, "system_config_id": CFG,
    "system_reduction_id": reductions(CFG)[0]["ID"]})
gid = group["ID"] if st == 201 else ""
call("POST", "/api/terminals", {"mac": MAC, "name": "Driver Client", "group_id": gid})
call("POST", f"/api/terminals/{TERMINAL}/super", {})
st, body = call("POST", f"/api/terminals/{TERMINAL}/inject-driver", {"bundle_id": bundle})
check("D2", "Inject bundle into Super Admin", st in (200, 201), f"HTTP {st} {str(body)[:140]}")
check("D3", "Response identifies the injected bundle", "e2e-bundle" in str(body), str(body)[:140])

clone = ssh("zfs list -H -o name | grep SCLIENT-AABBCC334455 || true").strip()
probe = mount_probe(clone, 'ls -l ndadapt 2>/dev/null') if clone else ""
check("D4", "Bundle ZIP exists in clone's ndadapt directory", "bundle.zip" in probe, probe.replace("\n", " | ")[:200])

section("E Cleanup")
cleanup()
check("E1", "Driver packs and bundles removed",
      not [p for p in call("GET", "/api/driver-packs")[1]["items"] if p["pack"]["Name"].startswith("e2e-")])

sys.exit(summary())
