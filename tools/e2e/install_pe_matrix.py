#!/usr/bin/env python3
"""ISO 安装、WinPE 取证与机型适配的服务端边界矩阵：不需要客户机、不需要 ISO 的那一半。

这些入口（/api/installations、/api/winpe、/api/models、/api/terminals/{id}/identity|pe-probe|model-adapt，
以及免登录的 /install/{token}、/boot/pe/{token}）是新加的，此前没有任何 e2e 用例碰过。
这里只断言真机上才看得到的边界：导入目录外的文件（含符号链接逃逸）不得被识别、不是安装盘的 ISO
被明确拒绝、伪造令牌拿不到任何文件也写不进回报、不存在的对象一律 404。

真正的安装（空环境制作 → 首次启动 → 保存 → 普通启动验证）和 WinPE 取证需要 PXE 客户机与官方 ISO，
见 README「ISO 安装验证」与「网卡 PNP 半自动真机矩阵」，本脚本不冒充它们，统一记为跳过。

本脚本只在导入目录里建/删 e2e-* 文件，不建分组、终端或镜像。
"""
import os
import shlex
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from ndapi import BASE, IMPORT_DIR, SSH_HOST, call, check, section, skip, ssh, summary  # noqa: E402

import urllib.error  # noqa: E402
import urllib.request  # noqa: E402


def anon(method, path, body=None):
    """免登录入口：不带 Authorization，按客户机的方式请求。"""
    req = urllib.request.Request(BASE + path, method=method, data=body,
                                 headers={"Content-Type": "application/json"} if body else {})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


D = IMPORT_DIR.rstrip("/")
FAKE_ISO = f"{D}/e2e-notinstaller.iso"
ESCAPE = f"{D}/e2e-escape.iso"


def cleanup():
    ssh(f"rm -rf {shlex.quote(FAKE_ISO)} {shlex.quote(ESCAPE)} {D}/e2e-iso-src")


cleanup()

section("A 安装会话与安装源列表")
st, body = call("GET", "/api/installations")
check("A1", "安装会话列表可读，带 items 包装", st == 200 and isinstance(body, dict) and isinstance(body.get("items"), list), f"HTTP {st} {str(body)[:120]}")
# 造一个合法但不是任何系统安装盘的 ISO：识别必须拒绝，而不是进入会格式化磁盘的步骤。
have_mkiso = ssh("command -v genisoimage || command -v xorrisofs || command -v mkisofs")
if have_mkiso:
    ssh(f"mkdir -p {D}/e2e-iso-src && echo hello > {D}/e2e-iso-src/readme.txt && "
        f"{have_mkiso.splitlines()[0]} -quiet -o {shlex.quote(FAKE_ISO)} -V E2E {D}/e2e-iso-src 2>/dev/null; rm -rf {D}/e2e-iso-src")
st, src = call("GET", "/api/installations/sources")
names = [str(i) for i in (src.get("items") or [])] if isinstance(src, dict) else []
check("A2", "安装源列表可读并写明导入目录", st == 200 and isinstance(src, dict) and str(src.get("import_dir", "")).rstrip("/") != "",
      f"HTTP {st} {str(src)[:160]}")
if have_mkiso:
    check("A3", "导入目录里的 .iso 出现在安装源列表", any("e2e-notinstaller.iso" in n for n in names), str(names)[:200])
else:
    skip("A3", "导入目录里的 .iso 出现在安装源列表", "被测节点没有 genisoimage/xorrisofs，造不了测试 ISO")

section("B 识别（inspect）的路径与格式边界")
st, body = call("POST", "/api/installations/inspect", {"path": "/etc/os-release"})
check("B1", "导入目录外的文件被拒（400），提示放进导入目录", st == 400 and "导入目录" in str(body), f"HTTP {st} {str(body)[:120]}")
st, body = call("POST", "/api/installations/inspect", {"path": f"{D}/../../../etc/os-release"})
check("B2", "../ 越界路径被拒（400）", st == 400, f"HTTP {st} {str(body)[:120]}")
ssh(f"ln -sf /etc/os-release {shlex.quote(ESCAPE)}")
st, body = call("POST", "/api/installations/inspect", {"path": ESCAPE})
check("B3", "导入目录内指向目录外的符号链接被拒（400）", st == 400 and "导入目录" in str(body), f"HTTP {st} {str(body)[:120]}")
st, body = call("POST", "/api/installations/inspect", {"path": f"{D}/e2e-does-not-exist.iso"})
check("B4", "不存在的 ISO 被拒（400）并说明", st == 400 and "不存在" in str(body), f"HTTP {st} {str(body)[:120]}")
non_iso = ssh(f"ls {D} | grep -v -i '\\.iso$' | head -1")
if non_iso:
    st, body = call("POST", "/api/installations/inspect", {"path": f"{D}/{non_iso}"})
    check("B5", "导入目录里非 .iso 的文件被拒（400）", st == 400 and "ISO" in str(body), f"{non_iso}: HTTP {st} {str(body)[:120]}")
else:
    skip("B5", "导入目录里非 .iso 的文件被拒", "导入目录里没有非 ISO 文件可用")
if have_mkiso:
    t0 = time.time()
    st, body = call("POST", "/api/installations/inspect", {"path": FAKE_ISO})
    check("B6", "不是安装盘的 ISO 被明确拒绝（4xx，不是 500）", 400 <= st < 500, f"HTTP {st} {time.time() - t0:.1f}s {str(body)[:160]}")
else:
    skip("B6", "不是安装盘的 ISO 被明确拒绝", "造不了测试 ISO")

section("C 发起安装的参数校验（不会真的开始）")
st, body = call("POST", "/api/installations", {"source_path": FAKE_ISO, "name": "e2e-inst", "mac": "not-a-mac", "size_gib": 40})
check("C1", "MAC 不合法被拒（400）", st == 400 and "MAC" in str(body), f"HTTP {st} {str(body)[:120]}")
# 其余参数都给合法值，确保拒绝的理由是安装源本身，而不是别的字段先被挡下。
SEG = ".".join(SSH_HOST.split(".")[:3])  # ND_SSH_HOST 未设时取 ND_BASE 的主机，不能退回一个写死的网段
st, body = call("POST", "/api/installations", {
    "source_path": "/etc/os-release", "name": "e2e-inst", "mac": "AA:BB:CC:77:88:99", "size_gib": 40,
    "ip": f"{SEG}.201", "netmask": "255.255.255.0", "gateway": f"{SEG}.1", "dns1": f"{SEG}.1",
    "username": "e2e", "password": "E2e-pass-1234"})
check("C2", "导入目录外的安装源被拒（400，点名导入目录），不建会话", st == 400 and "导入目录" in str(body), f"HTTP {st} {str(body)[:120]}")
_, after = call("GET", "/api/installations")
left = [i for i in (after.get("items") or []) if "e2e-inst" in str(i)] if isinstance(after, dict) else []
check("C3", "被拒的请求没有留下安装会话", not left, str(left)[:160])
for verb in ("cancel", "retry"):
    st, _ = call("POST", f"/api/installations/ghost/{verb}")
    check(f"C4-{verb}", f"不存在的安装会话 {verb} 404", st == 404, f"HTTP {st}")
st, _ = call("GET", "/api/installations/ghost/logs/install.log")
check("C5", "不存在的安装会话取日志 404", st == 404, f"HTTP {st}")

section("D 免登录入口：伪造令牌什么都拿不到")
for cid, method, path in (("D1", "GET", "/install/e2e-bogus-token/media.iso"),
                          ("D2", "POST", "/install/e2e-bogus-token/report"),
                          ("D3", "POST", "/install/e2e-bogus-token/log/install.log"),
                          ("D4", "GET", "/boot/pe/e2e-bogus-token/boot.wim"),
                          ("D5", "GET", "/boot/pe/e2e-bogus-token/failed"),
                          ("D6", "POST", "/boot/pe/e2e-bogus-token/booted"),
                          ("D7", "POST", "/boot/pe/e2e-bogus-token/result"),
                          ("D8", "POST", "/boot/pe/e2e-bogus-token/log/pe.log")):
    st, raw = anon(method, path, b"{}" if method == "POST" else None)
    check(cid, f"{method} {path.split('e2e-bogus-token')[0]}<伪造令牌>{path.split('e2e-bogus-token')[1]} 被拒", st in (401, 403, 404), f"HTTP {st} {raw[:80]!r}")

section("E WinPE 环境、机型与终端身份：不存在的对象 404")
st, body = call("GET", "/api/winpe")
check("E1", "WinPE 环境列表可读", st == 200 and isinstance(body, (dict, list)), f"HTTP {st} {str(body)[:120]}")
check("E2", "为不存在的镜像提取 WinPE 404", call("POST", "/api/images/e2e-ghost/winpe")[0] == 404)
check("E3", "删除不存在的 WinPE 环境 404", call("DELETE", "/api/winpe/e2e-ghost")[0] == 404)
st, body = call("GET", "/api/models")
check("E4", "机型列表可读", st == 200 and isinstance(body, (dict, list)), f"HTTP {st} {str(body)[:120]}")
check("E5", "适配失败机型（按分组）可读", call("GET", "/api/models/failing")[0] == 200)
check("E6", "不存在的机型 404", call("GET", "/api/models/e2e-ghost")[0] == 404)
check("E7", "不存在的还原点查机型 404", call("GET", "/api/reductions/e2e-ghost/models")[0] == 404)
for cid, method, suffix in (("E8", "GET", "identity"), ("E9", "POST", "identity-capture"),
                            ("E10", "POST", "pe-probe"), ("E11", "POST", "model-adapt"), ("E12", "GET", "model")):
    st, _ = call(method, f"/api/terminals/terminal-E2E000000000/{suffix}", {} if method == "POST" else None)
    check(cid, f"不存在的终端 {suffix} 404", st == 404, f"HTTP {st}")
check("E13", "不存在的任务取日志 404", call("GET", "/api/tasks/e2e-ghost/logs/pe.log")[0] == 404)

section("F 需要客户机与官方 ISO 的部分")
skip("F1", "ISO 空环境制作 → 首次启动 → 保存 → 普通启动验证", "需要 UEFI PXE 客户机与官方 ISO，按 README「ISO 安装验证」在隔离池上做")
skip("F2", "WinPE 取证 → 身份层生成 → 新 MAC 进桌面", "需要真实客户机，按 nic_pnp_matrix.py 半自动执行")

cleanup()
sys.exit(summary())
