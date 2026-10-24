#!/usr/bin/env python3
"""机型页驱动包上传、INF解析、禁用和删除；仅清理本轮创建的包。"""
import io
import json
import sys
import urllib.request
import urllib.error
import zipfile
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import ndapi
from ndapi import BASE, call, check, section, summary

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


if __name__ == "__main__":
    ndapi.login()
    created = []
    try:
        section("驱动包管理")
        st, body = upload_pack("e2e-nic-v2", pack_zip())
        check("D1", "上传并解析硬件ID", st == 201 and "VEN_8086&DEV_15B7" in str(body), f"HTTP {st}")
        if st == 201:
            pack = body["pack"]; created.append(pack["ID"])
            check("D2", "解析厂商和版本", pack["Vendor"] == "E2E" and pack["Version"] == "1.0.0.1")
            st, body = call("POST", f"/api/driver-packs/{pack['ID']}/status", {"status": "disabled"})
            check("D3", "禁用包", st == 200 and body["Status"] == "disabled")
        buf = io.BytesIO()
        with zipfile.ZipFile(buf, "w") as z: z.writestr("readme.txt", "no INF")
        check("D4", "缺INF当场拒绝", upload_pack("e2e-invalid", buf.getvalue())[0] == 400)
        check("D5", "旧管理入口已撤下", call("GET", "/api/driver-bundles")[0] == 404)
    finally:
        for pack_id in created:
            check("D6", "只删除本轮驱动包", call("DELETE", f"/api/driver-packs/{pack_id}")[0] == 204)
    sys.exit(summary())
