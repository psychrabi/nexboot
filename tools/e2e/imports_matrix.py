#!/usr/bin/env python3
"""导入目录矩阵：导入目录在各节点存储池上（<池挂载点>/nd/files/imports），随目录库复制到备机。

    I  每台节点各自解析出自己池上的导入目录；旧目录只读列出
    U  上传的文件一轮复制后在三台上 sha256 一致
    P  备机池放不下时上传在建会话时就被拒（临时 refreservation 模拟，跑完删除）
    L  旧目录里的文件由操作者移入（用本矩阵自己放的小文件，不动现场已有文件）
    F  上传中途主备切换：新主机接手复制过来的分片续传到完成，哈希一致；
       切换前传完的 ISO 在新主机上能识别
    D  删除随复制同步到备机

环境同 ndcluster（ND_NODES / ND_VIP / ND_PASSWORD / ND_CLUSTER_TOKEN / ND_SSH_USER）。
只创建 e2e8-* 文件与 <池>/e2e8-reserve 数据集，结束时全部清理；会做一次计划切换。
"""
import hashlib
import json
import os
import shlex
import sys
import time
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ndcluster import (NODES, VIP_BASE, call, check, host, login, node_call, section,  # noqa: E402
                       skip, ssh_out, summary, wait)

STAMP = time.strftime("%H%M%S")
CHUNK = 8 << 20
GiB = 1 << 30
REPL_WAIT = int(os.environ.get("ND_REPL_WAIT", "300"))  # 一轮复制默认 60s，留足余量


def active_base():
    for b in NODES:
        st, body = node_call(b, "GET", "/internal/ha/status", timeout=8)
        if st == 200 and isinstance(body, dict) and body.get("role") == "active":
            return b
    return ""


def runtime_dir(base):
    st, body = call(base, "GET", "/api/runtime")
    return (body.get("import_dir") or "") if st == 200 and isinstance(body, dict) else ""


def sha_on(base, path):
    got, out = ssh_out(host(base), f"sha256sum {shlex.quote(path)} 2>/dev/null | cut -d' ' -f1")
    return out.strip() if got else ""


def exists_on(base, path):
    got, out = ssh_out(host(base), f"test -e {shlex.quote(path)} && echo yes || echo no")
    return got and out.strip() == "yes"


def patch_chunk(base, uid, data, offset, total):
    req = urllib.request.Request(f"{base}/api/images/uploads/{uid}", data=data, method="PATCH")
    req.add_header("Authorization", "Bearer " + login(base))
    req.add_header("Content-Type", "application/octet-stream")
    req.add_header("Content-Range", f"bytes {offset}-{offset + len(data) - 1}/{total}")
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()
    except Exception as e:  # noqa: BLE001
        return 0, str(e)


def upload(base, name, data, last_modified, upto=None):
    """传到 upto 字节（默认传完）。返回 (upload_id, 最后应答)。"""
    st, s = call(base, "POST", "/api/images/uploads",
                 {"file_name": name, "size_bytes": len(data), "last_modified": last_modified})
    if st not in (200, 201) or not isinstance(s, dict):
        return "", (st, s)
    uid, off = s.get("upload_id", ""), int(s.get("received") or 0)
    end = len(data) if upto is None else upto
    last = (st, s)
    while off < end:
        piece = data[off:min(off + CHUNK, end)]
        last = patch_chunk(base, uid, piece, off, len(data))
        if last[0] != 200:
            break
        off += len(piece)
    return uid, last


def make_payload(base, name, size_mib):
    """在节点上造一张能识别的假 Ubuntu Server 24.04 ISO（有 xorrisofs/genisoimage 时），否则造随机内容。"""
    tool_ok, tool = ssh_out(host(base), "command -v xorrisofs || command -v genisoimage || command -v mkisofs")
    work = f"/tmp/nd-e2e8-{STAMP}"
    if tool_ok and tool.strip():
        script = f"""set -e
rm -rf {work}; mkdir -p {work}/src/.disk {work}/src/casper
echo 'Ubuntu-Server 24.04.1 LTS "Noble Numbat" - Release amd64 (20240827)' > {work}/src/.disk/info
echo kernel > {work}/src/casper/vmlinuz; echo initrd > {work}/src/casper/initrd
printf -- '- default: true\\n  id: ubuntu-server\\n  path: ubuntu-server-minimal.squashfs\\n  type: fsimage\\n' > {work}/src/casper/install-sources.yaml
printf 'open-iscsi\\t2.1.9\\ncurl\\t8.5.0\\n' > {work}/src/casper/ubuntu-server-minimal.manifest
head -c {size_mib}M /dev/urandom > {work}/src/casper/pad.bin
{tool.splitlines()[0]} -quiet -R -J -V E2E8 -o {work}/out.iso {work}/src
chmod 644 {work}/out.iso"""
        got, out = ssh_out(host(base), script, timeout=180)
        real = True
    else:
        got, out = ssh_out(host(base), f"rm -rf {work}; mkdir -p {work}; head -c {size_mib}M /dev/urandom > {work}/out.iso; chmod 644 {work}/out.iso")
        real = False
    got2, b64 = ssh_out(host(base), f"base64 -w0 {work}/out.iso; rm -rf {work}", timeout=180)
    if not got2:
        raise SystemExit(f"造测试文件失败：{out} {b64}")
    import base64
    return base64.b64decode(b64), real


created = []          # (base, path) 结束时删除
reserve = []          # (host, dataset)


def cleanup():
    section("清理")
    for h, ds in reserve:
        ssh_out(h, f"zfs destroy {shlex.quote(ds)} 2>/dev/null; true")
    for b in NODES:
        d = runtime_dir(b)
        if d:
            ssh_out(host(b), f"rm -f {shlex.quote(d)}/e2e8-*.iso {shlex.quote(d)}/.uploads/*e2e8* 2>/dev/null; true")
    for b, p in created:
        ssh_out(host(b), f"rm -f {shlex.quote(p)}")
    left = []
    for b in NODES:
        got, out = ssh_out(host(b), "zfs list -H -o name 2>/dev/null | grep e2e8-reserve; true")
        if got and out.strip():
            left.append(out.strip())
    check("Z1", "reservation 数据集已删除", not left, str(left))


W = active_base()
if not W:
    raise SystemExit("找不到写入者")
standbys = [b for b in NODES if b != W]
print("writer", W, "standbys", standbys)

try:
    section("I 导入目录")
    dirs = {b: runtime_dir(b) for b in NODES}
    for b, d in dirs.items():
        check(f"I1-{host(b)}", f"{host(b)} 的导入目录在它自己的池上（…/nd/files/imports）",
              d.startswith("/ndiskless/") and d.endswith("/nd/files/imports"), d)
    got, fstype = ssh_out(host(W), f"stat -f -c %T {shlex.quote(dirs[W])}")
    check("I2", "写入者导入目录在 ZFS 上（不是系统盘）", got and fstype.strip() == "zfs", fstype)
    for b in NODES:
        st, s = call(b, "GET", "/api/settings")
        st2, lg = call(b, "GET", "/api/imports/legacy")
        legacy = s.get("legacy_import_dirs") if isinstance(s, dict) else None
        names = [i.get("path") for i in (lg.get("items") or [])] if isinstance(lg, dict) else []
        check(f"I3-{host(b)}", f"{host(b)} 设置页只读给出导入目录与旧目录", st == 200 and s.get("import_dir") == dirs[b] and isinstance(legacy, list),
              f"dir={s.get('import_dir') if isinstance(s, dict) else s} legacy={legacy} files={names}")
    # 现场：.4 的 /var/lib/ndiskless/imports 里有用户的 Win10 ISO，应作为旧目录文件只读列出（本矩阵不移动它）。
    for b in NODES:
        if host(b).endswith(".4"):
            _, lg = call(b, "GET", "/api/imports/legacy")
            items = lg.get("items") or [] if isinstance(lg, dict) else []
            check("I4", ".4 旧目录 /var/lib/ndiskless/imports 里的现有 ISO 作为旧文件列出",
                  any(i.get("dir") == "/var/lib/ndiskless/imports" and i.get("name", "").lower().endswith(".iso") for i in items),
                  str([(i.get("name"), i.get("size")) for i in items])[:200])
    st, body = call(W, "PUT", "/api/settings", {"import_dir": "/tmp"})
    check("I5", "PUT import_dir 被忽略并给出提示，导入目录不变",
          st == 200 and isinstance(body, dict) and body.get("warning") and body.get("import_dir") == dirs[W], f"{st} {str(body)[:160]}")

    section("U 上传 → 三台一致")
    # ND_FIXTURE_ISO：跑矩阵那台上的一张能识别的 ISO（节点上没有造 ISO 的工具时由外面带进来）
    if os.environ.get("ND_FIXTURE_ISO"):
        iso_a, real_iso = open(os.environ["ND_FIXTURE_ISO"], "rb").read(), True
    else:
        iso_a, real_iso = make_payload(W, "a", 20)
    name_a = f"e2e8-a-{STAMP}.iso"
    uid, last = upload(VIP_BASE, name_a, iso_a, 1700000000001)
    sha_a = hashlib.sha256(iso_a).hexdigest()
    check("U1", "上传完成（经 VIP）", last[0] == 200 and isinstance(last[1], dict) and last[1].get("complete"), str(last)[:200])
    path_w = f"{dirs[W]}/{name_a}"
    check("U2", "写入者导入目录里哈希一致", sha_on(W, path_w) == sha_a, path_w)
    for b in standbys:
        p = f"{dirs[b]}/{name_a}"
        ok = wait(lambda: sha_on(b, p) == sha_a, REPL_WAIT, 10)
        check(f"U3-{host(b)}", f"一轮复制后 {host(b)} 上哈希一致", ok, p)

    section("P 备机池放不下时拒绝")
    st, cp = call(W, "GET", "/api/cluster/pools")
    free = {}
    for n in (cp.get("nodes") or []) if isinstance(cp, dict) else []:
        for p in n.get("pools") or []:
            if p.get("Role") == "data" and n.get("reachable"):
                free[n.get("ip")] = (p.get("Capacity", 0) - p.get("Used", 0), p.get("Name"), n.get("is_self"))
    wfree = next((v[0] for v in free.values() if v[2]), 0)
    sb = sorted([(v[0], ip, v[1]) for ip, v in free.items() if not v[2]])
    if not sb or wfree < 30 * GiB:
        skip("P1", "备机池放不下时拒绝", f"池空间读不全：{free}")
    else:
        sfree, sip, spool = sb[0]
        begin = lambda size: call(VIP_BASE, "POST", "/api/images/uploads",  # noqa: E731
                                  {"file_name": f"e2e8-big-{STAMP}.iso", "size_bytes": size, "last_modified": 1})
        rejected = lambda size: wait(lambda: (lambda r: r if r[0] == 409 else None)(begin(size)), 90, 10)  # noqa: E731
        names_it = lambda res: bool(res) and sip in str(res[1]) and "备用节点" in str(res[1])  # noqa: E731
        if sfree + 2 * GiB < wfree:
            # 这台备机本来就比写入者小：取一个写入者放得下、它放不下的大小，不用预留
            res = rejected(sfree - GiB // 2)
            check("P1", f"建会话时被拒（409），点名放不下的备机（{sip} 剩 {sfree / GiB:.1f}G，写入者剩 {wfree / GiB:.1f}G）",
                  names_it(res), str(res)[:240])
            ok = begin(sfree - 2 * GiB)
            check("P2", "备机加余量放得下的大小放行", ok[0] in (200, 201), str(ok)[:160])
            if ok[0] in (200, 201) and isinstance(ok[1], dict):
                call(VIP_BASE, "DELETE", f"/api/images/uploads/{ok[1].get('upload_id')}")
        else:
            size = wfree - 5 * GiB              # 写入者（含 1 GiB 余量）放得下
            target = size - 10 * GiB            # 让这台备机放不下
            need_res = sfree - target
            if target < 40 * GiB or need_res <= 0:
                skip("P1", "备机池放不下时拒绝", f"为保证备机至少留 40G，不做（备机 {sip} 剩 {sfree / GiB:.1f}G，需要压到 {target / GiB:.1f}G）")
            else:
                ds = f"{spool}/e2e8-reserve"
                reserve.append((sip, ds))
                got, out = ssh_out(sip, f"zfs create -o refreservation={need_res} -o canmount=off {shlex.quote(ds)} && echo ok")
                check("P0", f"在 {sip} 临时预留 {need_res / GiB:.1f}G（剩约 {target / GiB:.1f}G）", got and "ok" in out, out)
                res = rejected(size)
                check("P1", "建会话时被拒（409），点名放不下的备机", names_it(res), str(res)[:240])
                ssh_out(sip, f"zfs destroy {shlex.quote(ds)}")
                reserve.clear()
                ok = wait(lambda: (lambda r: r if r[0] in (200, 201) else None)(begin(size)), 90, 10)
                check("P2", "预留删除后同样的上传放行", bool(ok), str(ok)[:160])
                if ok and isinstance(ok[1], dict):
                    call(VIP_BASE, "DELETE", f"/api/images/uploads/{ok[1].get('upload_id')}")

    section("L 旧目录文件移入")
    _, s = call(W, "GET", "/api/settings")
    legacy_dirs = s.get("legacy_import_dirs") or [] if isinstance(s, dict) else []
    if "/var/lib/ndiskless/imports" not in legacy_dirs:
        skip("L1", "旧目录文件移入", f"写入者没有旧目录 /var/lib/ndiskless/imports：{legacy_dirs}")
    else:
        src = f"/var/lib/ndiskless/imports/e2e8-legacy-{STAMP}.iso"
        created.append((W, src))
        ssh_out(host(W), f"head -c 1M /dev/urandom > {src}")
        lsha = sha_on(W, src)
        st, body = call(W, "POST", "/api/imports/legacy/move", {"path": src})
        check("L1", "移入请求受理（202）", st == 202, f"{st} {str(body)[:160]}")
        dest = f"{dirs[W]}/e2e8-legacy-{STAMP}.iso"
        check("L2", "移入后导入目录里哈希一致、旧目录里没了",
              bool(wait(lambda: sha_on(W, dest) == lsha and not exists_on(W, src), 60, 3)), dest)
        st, body = call(W, "POST", "/api/imports/legacy/move", {"path": "/etc/passwd"})
        check("L3", "旧目录以外的路径被拒（400）", st == 400, f"{st} {str(body)[:120]}")

    section("F 上传中途主备切换")
    data_b, _ = make_payload(W, "b", 24)
    name_b = f"e2e8-b-{STAMP}.iso"
    sha_b = hashlib.sha256(data_b).hexdigest()
    half = 2 * CHUNK
    uid_b, last = upload(VIP_BASE, name_b, data_b, 1700000000002, upto=half)
    check("F1", "先传一部分", bool(uid_b) and last[0] == 200 and last[1].get("received") == half, str(last)[:160])
    for b in standbys:
        part = f"{dirs[b]}/.uploads/{uid_b}.part"
        ok = wait(lambda: ssh_out(host(b), f"stat -c %s {shlex.quote(part)} 2>/dev/null")[1].strip() == str(half)
                  and exists_on(b, f"{dirs[b]}/.uploads/{uid_b}.json"), REPL_WAIT, 10)
        check(f"F2-{host(b)}", f"分片已复制到 {host(b)}", ok, part)
    st, r = call(VIP_BASE, "POST", "/api/ha/planned-switch", timeout=240)
    check("F3", "计划切换受理", st in (200, 202), f"{st} {str(r)[:160]}")
    new_w = wait(lambda: (lambda a: a if a and a != W else None)(active_base()), 300, 5)
    check("F4", "写入者已换到另一台", bool(new_w), f"{W} → {new_w}")
    if new_w:
        wait(lambda: call(VIP_BASE, "GET", "/api/runtime")[1].get("import_dir") == dirs[new_w]
             if isinstance(call(VIP_BASE, "GET", "/api/runtime")[1], dict) else False, 120, 5)
        st, s = call(VIP_BASE, "GET", f"/api/images/uploads/{uid_b}")
        check("F5", "新主机接手复制过来的会话，进度不丢", st == 200 and isinstance(s, dict) and s.get("received") == half,
              f"{st} {str(s)[:200]}")
        uid2, last = upload(VIP_BASE, name_b, data_b, 1700000000002)
        check("F6", "重新选同一个文件：认领同一会话并续传完成",
              uid2 == uid_b and last[0] == 200 and isinstance(last[1], dict) and last[1].get("complete"), f"{uid2} {str(last)[:200]}")
        check("F7", "新写入者上的成品哈希一致", sha_on(new_w, f"{dirs[new_w]}/{name_b}") == sha_b)
        for b in [n for n in NODES if n != new_w]:
            ok = wait(lambda: sha_on(b, f"{dirs[b]}/{name_b}") == sha_b, REPL_WAIT, 10)
            check(f"F8-{host(b)}", f"续传的成品复制到 {host(b)}，哈希一致", ok)
        # 切换前传完的 ISO：新主机用自己池上的路径识别
        st, src = call(VIP_BASE, "GET", "/api/installations/sources")
        paths = [i.get("path") for i in (src.get("items") or [])] if isinstance(src, dict) else []
        want = f"{dirs[new_w]}/{name_a}"
        check("F9", "新写入者的 ISO 列表里有切换前上传的文件（本机路径）", want in paths, str(paths)[:200])
        st, meta = call(VIP_BASE, "POST", "/api/installations/inspect", {"path": want}, timeout=120)
        if real_iso:
            check("F10", "新写入者能识别它（Ubuntu Server 24.04）",
                  st == 200 and isinstance(meta, dict) and meta.get("distribution") == "ubuntu-server", f"{st} {str(meta)[:200]}")
        else:
            check("F10", "新写入者找得到它（不是「不存在」）", 400 <= st < 500 and "不存在" not in str(meta), f"{st} {str(meta)[:160]}")

    section("D 删除同步到备机")
    cur = active_base() or W
    victims = [f"e2e8-a-{STAMP}.iso", f"e2e8-b-{STAMP}.iso", f"e2e8-legacy-{STAMP}.iso"]
    ssh_out(host(cur), "rm -f " + " ".join(shlex.quote(f"{dirs[cur]}/{v}") for v in victims))
    for b in [n for n in NODES if n != cur]:
        ok = wait(lambda: not any(exists_on(b, f"{dirs[b]}/{v}") for v in victims), REPL_WAIT, 10)
        check(f"D1-{host(b)}", f"删除已同步到 {host(b)}", ok)
finally:
    cleanup()
    summary()
