#!/usr/bin/env python3
"""注册表/超管保存矩阵：断电留下的未提交 SYSTEM（序号不等、改动在 .LOG1/.LOG2 里）。

    0  夹具：从分组 test 当前点（真实断电保存出来的脏点）分出 e2e-hive 配置；核对它的 SYSTEM
       与节点 /tmp 里的样本同一份（sha256），且确实是脏的
    1  开机兜底：测试终端（借终端 22 的机型上报）从脏点开机，身份层建成、/boot 200；
       回放只发生在本终端的克隆上，还原点本身不变
    2  断电后的超管保存：回放成功、保存出的还原点干净（两个序号相等），残留日志原样保留；
       测试终端从新点开机 200
    3  日志不可用（截断 LOG1/LOG2）：拒绝，配置、还原点、超管盘都不变
    4  快速启动（hiberfil.sys 头为 HIBR）：拒绝，提示关闭快速启动，同样什么都不变

样本（SYSTEM、SYSTEM.LOG1、SYSTEM.LOG2）只放在节点的 ND_DIRTY_SAMPLE（默认 /tmp/nd9-sample），不进仓库。
只创建 e2e-hive-* 配置/分组和 0E2EC1000C0x 终端，结束时全部删掉并核对各节点池里没有残留。
"""
import os
import shlex
import sys
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from ndapi import call, check, configs, section, skip, summary  # noqa: E402
from ndcluster import NODES, host, ssh_out  # noqa: E402

PREFIX = "e2e-hive"
STAMP = time.strftime("%H%M%S")
NAME = f"{PREFIX}-{STAMP}"
SUPER_MAC, BOOT_MAC = "0E2EC1000C01", "0E2EC1000C02"
SUPER_TID, BOOT_TID = f"terminal-{SUPER_MAC}", f"terminal-{BOOT_MAC}"
SAMPLE = os.environ.get("ND_DIRTY_SAMPLE", "/tmp/nd9-sample")
SOURCE_GROUP = os.environ.get("ND_HIVE_GROUP", "test")
PROFILE_TERMINAL = os.environ.get("ND_HIVE_PROFILE_TERMINAL", "22")
HIVE = "Windows/System32/config"
# 读基块两个序号；在挂载根目录下执行。
SEQ = ("python3 -c \"import struct;b=open('" + HIVE + "/SYSTEM','rb').read(512);"
       "print('SEQ', *struct.unpack_from('<II', b, 4))\"")


def wait(cond, seconds=120, step=3):
    for _ in range(max(1, seconds // step)):
        got = cond()
        if got:
            return got
        time.sleep(step)
    return None


def writer_and_pool():
    st, view = call("GET", "/api/cluster/pools")
    for node in (view or {}).get("nodes", []) if isinstance(view, dict) else []:
        if node.get("is_self"):
            pools = node.get("pools") or []
            data = next((p for p in pools if p.get("Role") == "data"), pools[0] if pools else None)
            return node.get("ip"), (data or {}).get("Name")
    return "", ""


HOST, POOL = writer_and_pool()
if not HOST or not POOL:
    print("找不到写入者或数据池，停")
    sys.exit(2)
SYS_CLONE = f"{POOL}/run/SCLIENT-{SUPER_MAC}"
PROBE = f"{POOL}/{PREFIX}-probe"


def sh(cmd, timeout=120):
    got, out = ssh_out(HOST, cmd, timeout=timeout)
    assert got, f"读不到 {HOST}：{out}"
    return out.strip()


def sh_try(cmd, timeout=120):
    return ssh_out(HOST, cmd, timeout=timeout)[1].strip()


def exists(dataset):
    return sh(f"zfs list -H -o name {dataset} 2>/dev/null || true") == dataset


def with_windows(dataset, script, rw=False):
    """挂载数据集上带 Windows 的 NTFS 分区，在挂载根执行 script；分区节点有延迟，重试。"""
    opts = "rw,force" if rw else "ro,force"
    for _ in range(6):
        out = sh_try(
            "udevadm settle --timeout=10; D=$(mktemp -d); for p in 1 2 3 4; do "
            f"mount -t ntfs3 -o {opts} /dev/zvol/{dataset}-part$p $D 2>/dev/null || continue; "
            f"if [ -d $D/{HIVE} ]; then ( cd $D && {script} ); fi; umount $D; done; rmdir $D")
        if out:
            return out
        time.sleep(3)
    return ""


def snapshot_probe(snapshot, script):
    """快照只读：临时克隆出来挂载查看，查完即删。"""
    sh_try(f"zfs destroy -r {PROBE}")
    sh(f"zfs clone -o readonly=on {snapshot} {PROBE}")
    try:
        return with_windows(PROBE, script)
    finally:
        time.sleep(1)
        sh_try(f"zfs destroy -r {PROBE}")


def seqs(out):
    line = next((x for x in out.splitlines() if x.startswith("SEQ ")), "")
    return tuple(int(x) for x in line.split()[1:3]) if line else ()


def sha_lines(out):
    return {ln.split()[1].rsplit("/", 1)[-1]: ln.split()[0] for ln in out.splitlines() if len(ln.split()) == 2 and len(ln.split()[0]) == 64}


def pool_state(cfg_id):
    """配置及其快照（含 guid）和超管盘的来源：拒绝前后必须完全一样。"""
    return sh(f"zfs list -H -t all -o name,guid,origin -r {POOL}/nd/{cfg_id} {SYS_CLONE} 2>/dev/null | grep -v '@rep-' | sort")


def reductions(cfg_id):
    st, r = call("GET", f"/api/configs/{cfg_id}/reductions")
    return sorted((x["ID"], x["Name"]) for x in (r or {}).get("items", [])) if isinstance(r, dict) else []


def put_super(state, gid, ip):
    return call("PUT", f"/api/terminals/{SUPER_TID}", {"mac": SUPER_MAC, "ip": ip, "group_id": gid, "name": f"{NAME}-super", "is_super": True, "state": state})


def fresh_super_clone(cfg_id):
    """超管机开过机的盘：从配置最新的还原点克隆（和开机时建的是同一种东西）。"""
    snap = sh(f"zfs list -H -t snapshot -s creation -o name -r {POOL}/nd/{cfg_id} | grep -v '@rep-' | grep '^{POOL}/nd/{cfg_id}@' | tail -1")
    sh_try(f"zfs destroy -r {SYS_CLONE}")
    sh(f"zfs clone {snap} {SYS_CLONE}")
    sh_try("udevadm settle --timeout=10")
    time.sleep(2)
    return snap


def stop_super(name):
    st, r = call("POST", f"/api/terminals/{SUPER_TID}/super/stop", {"reduction_name": name})
    task_id = r.get("task_id") if isinstance(r, dict) else ""
    if not task_id:
        return st, r, {}

    def done():
        s, t = call("GET", f"/api/tasks/{task_id}")
        return t if s == 200 and isinstance(t, dict) and t.get("Status") in ("success", "failed") else None
    return st, r, wait(done, 300, 2) or {}


def boot(mac, hints):
    q = urllib.parse.urlencode({"mac": mac, **hints})
    for _ in range(3):
        st, script = call("GET", f"/boot?{q}")
        if st == 200 and "sanhook" in str(script):
            return st, script
        time.sleep(5)
    return st, script


def find_on_nodes(*patterns):
    """各节点池里名字含任一 pattern 的数据集/快照（身份层建在哪台不确定）。"""
    hits = []
    grep = " ".join(f"-e {shlex.quote(p)}" for p in patterns)
    for b in NODES:
        got, out = ssh_out(host(b), f"zfs list -H -t all -o name 2>/dev/null | grep -F {grep} || true", timeout=60)
        hits += [f"{host(b)}:{x}" for x in out.split() if got and x]
    return hits


def journal(since, pattern):
    lines = []
    for b in NODES:
        got, out = ssh_out(host(b), f"journalctl -u ndiskless --since '{since}' --no-pager | grep -F -- {pattern!r} || true", timeout=60)
        lines += [f"{host(b)}: {x}" for x in out.splitlines() if got and x]
    return lines


def cleanup():
    call("DELETE", f"/api/terminals/{SUPER_TID}/super")
    for tid in (SUPER_TID, BOOT_TID):
        call("DELETE", f"/api/terminals/{tid}")
    st, gs = call("GET", "/api/groups")
    for g in (gs or {}).get("items", []) if isinstance(gs, dict) else []:
        if g.get("Name", "").startswith(PREFIX):
            call("DELETE", f"/api/groups/{g['ID']}")
    st, imgs = call("GET", "/api/images")
    for img in (imgs or {}).get("items", []) if isinstance(imgs, dict) else []:
        for c in configs(img["ID"]):
            if PREFIX in c["Name"]:
                call("DELETE", f"/api/configs/{c['ID']}")
    sh_try(f"zfs destroy -r {PROBE}")
    time.sleep(3)


section("0 夹具：从真实断电保存的脏点分出测试配置")
cleanup()
since = sh("date -u '+%Y-%m-%d %H:%M:%S UTC'")
st, gs = call("GET", "/api/groups")
src = next((g for g in (gs or {}).get("items", []) if g.get("Name") == SOURCE_GROUP), None)
if not src:
    print(f"没有分组 {SOURCE_GROUP}，停")
    sys.exit(2)
IMG, SRC_CFG = src["SystemImageID"], src["SystemConfigID"]
st, cfg_view = call("GET", f"/api/configs/{SRC_CFG}")
point = (cfg_view or {}).get("DefaultReductionID") if isinstance(cfg_view, dict) else None
point = point or src.get("SystemReductionID")
src_reds = dict(reductions(SRC_CFG))
dirty_snap = f"{POOL}/nd/{SRC_CFG}@{src_reds.get(point, '').lstrip('@')}"
sample = sh(f"cd {SAMPLE} && sha256sum SYSTEM SYSTEM.LOG1 SYSTEM.LOG2 2>/dev/null || true")
sample_sha = sha_lines(sample)
check("0a", "节点上有脏 hive 样本（SYSTEM/LOG1/LOG2）", len(sample_sha) == 3, f"{SAMPLE}: {sample[:80]}")
src_before = sh(f"zfs list -H -o name,guid {dirty_snap} 2>/dev/null || true")
st, _ = call("POST", f"/api/configs/{SRC_CFG}/fork", {"name": NAME, "reduction_id": point})
cfg = wait(lambda: next((c for c in configs(IMG) if c["Name"] == NAME), None))
check("0b", f"从 {SOURCE_GROUP} 当前点 {src_reds.get(point)} 分出测试配置", bool(cfg), f"HTTP {st}")
if not cfg:
    cleanup()
    sys.exit(summary())
CFG = cfg["ID"]
fork_snap = sh(f"zfs list -H -t snapshot -s creation -o name -r {POOL}/nd/{CFG} | grep -v '@rep-' | grep '^{POOL}/nd/{CFG}@' | head -1")
probe = snapshot_probe(fork_snap, f"sha256sum {HIVE}/SYSTEM; {SEQ}")
fixture_sha = sha_lines(probe).get("SYSTEM", "")
fixture_seq = seqs(probe)
check("0c", "测试配置的 SYSTEM 就是样本那一份，且是脏的（序号不等）",
      fixture_sha == sample_sha.get("SYSTEM") and len(fixture_seq) == 2 and fixture_seq[0] != fixture_seq[1],
      f"sha {fixture_sha[:12]} vs {sample_sha.get('SYSTEM', '')[:12]} seq {fixture_seq}")

head = ".".join(src["StartIP"].split(".")[:3])
st, group = call("POST", "/api/groups", {
    "name": NAME, "start_ip": f"{head}.200", "client_max": 4,
    "gateway": src.get("Gateway", ""), "netmask": src.get("Netmask", "255.255.255.0"),
    "dns1": src.get("DNS1", ""), "dns2": src.get("DNS2", ""),
    "system_image_id": IMG, "system_config_id": CFG})
GID = (group or {}).get("ID", "") if isinstance(group, dict) else ""
check("0d", "测试分组已建", bool(GID), f"HTTP {st} {str(group)[:90]}")
st, bt = call("POST", "/api/terminals", {"mac": BOOT_MAC, "ip": "", "group_id": GID, "name": f"{NAME}-boot"})
st2, stt = call("POST", "/api/terminals", {"mac": SUPER_MAC, "ip": "", "group_id": GID, "name": f"{NAME}-super"})
SUPER_IP = stt.get("IP", "") if isinstance(stt, dict) else ""
check("0e", "测试终端已建（开机用、超管用）", st in (200, 201) and st2 in (200, 201), f"{st} {st2}")

# 借真实终端的机型上报：开机时按它的网卡写身份层，和 22/33 走同一条路。
st, ts = call("GET", "/api/terminals")
pt = next((t for t in (ts or {}).get("items", []) if t.get("Name") == PROFILE_TERMINAL), None) if isinstance(ts, dict) else None
hints = {}
if pt and pt.get("HardwareProfileID"):
    st, m = call("GET", f"/api/models/{pt['HardwareProfileID']}")
    if st == 200 and isinstance(m, dict):
        hints = {"platform": m.get("platform", ""), "busid": "01" + m.get("nic_vendor", "") + m.get("nic_device", ""),
                 "busloc": m.get("bus_loc", ""), "manufacturer": m.get("manufacturer", ""), "product": m.get("product", "")}
check("0f", f"取到终端 {PROFILE_TERMINAL} 的机型上报参数", bool(hints), str(hints)[:120])

try:
    section("1 开机兜底：从脏点开机，回放只在本终端的克隆上")
    st, script = boot(BOOT_MAC, hints)
    check("1a", "测试终端从脏点开机 /boot 200", st == 200 and "sanhook" in str(script), f"HTTP {st} {str(script)[:100]}")
    ident = [x for x in find_on_nodes(f"IDENT-{BOOT_MAC}@ready")]
    if ident:
        check("1b", "身份层已建成（IDENT@ready）", True, ident[0])
        node_host, ds = ident[0].split(":", 1)
        tmp = f"{ds.split('/')[0]}/{PREFIX}-identprobe"  # 各节点池名不同
        got, out = ssh_out(node_host, f"zfs clone -o readonly=on {ds} {tmp} && udevadm settle --timeout=10; sleep 2; "
                           f"D=$(mktemp -d); for p in 1 2 3 4; do mount -t ntfs3 -o ro,force /dev/zvol/{tmp}-part$p $D 2>/dev/null || continue; "
                           f"if [ -d $D/{HIVE} ]; then ( cd $D && {SEQ} ); fi; umount $D; done; rmdir $D; zfs destroy -r {tmp}")
        s = seqs(out)
        check("1c", "身份层里的 SYSTEM 已回放为干净（序号相等）", len(s) == 2 and s[0] == s[1], f"{node_host} seq {s}")
    else:
        rebuild = journal(since, f"terminal-{BOOT_MAC}")
        skip("1b", "身份层已建成", f"这次开机没有走身份层（机型/还原点组合被判为直接启动）：{' | '.join(rebuild)[-160:]}")
    errs = [x for x in journal(since, "Windows注册表尚未提交") if BOOT_MAC in x or f"terminal-{BOOT_MAC}" in x]
    check("1d", "没有「注册表尚未提交」的身份层错误", not errs, " | ".join(errs)[:160])
    probe = snapshot_probe(fork_snap, f"sha256sum {HIVE}/SYSTEM; {SEQ}")
    check("1e", "还原点本身未被改动（仍是原样的脏 SYSTEM）", sha_lines(probe).get("SYSTEM") == fixture_sha, str(seqs(probe)))
    check("1f", f"{SOURCE_GROUP} 的当前点快照未变", sh(f"zfs list -H -o name,guid {dirty_snap} 2>/dev/null || true") == src_before)

    section("2 断电后的超管保存：回放、校验、存成干净的还原点")
    st, r = call("POST", f"/api/terminals/{SUPER_TID}/super")
    check("2a", "测试终端设为超管", st == 200, f"HTTP {st} {str(r)[:80]}")
    fresh_super_clone(CFG)
    out = with_windows(SYS_CLONE, f"sha256sum {HIVE}/SYSTEM; {SEQ}")
    check("2b", "超管盘带着断电时的脏 SYSTEM", sha_lines(out).get("SYSTEM") == fixture_sha, str(seqs(out)))
    put_super("offline", GID, SUPER_IP)
    st, r, task = stop_super("hive-ok")
    check("2c", "关机存还原点成功", task.get("Status") == "success", f"HTTP {st} {task.get('Status')} {str(task.get('Error') or r)[:120]}")
    reds = (call("GET", f"/api/configs/{CFG}/reductions")[1] or {}).get("items", [])
    ok_red = next((x for x in reds if (x.get("DisplayName") or "").endswith("hive-ok") or x.get("Name", "").endswith("hive-ok")), None)
    check("2d", "新还原点已入库", bool(ok_red), str([(x.get("Name"), x.get("DisplayName")) for x in reds]))
    logs = journal(since, "replayed SYSTEM transaction logs")
    check("2e", "日志记下了这次回放（超管盘、HvLE、条数、序号）", any(f"SCLIENT-{SUPER_MAC}" in x for x in logs), (logs[-1] if logs else "")[-200:])
    if ok_red:
        snap = f"{POOL}/nd/{CFG}@{ok_red['Name'].lstrip('@')}"
        out = snapshot_probe(snap, f"sha256sum {HIVE}/SYSTEM {HIVE}/SYSTEM.LOG1 {HIVE}/SYSTEM.LOG2; {SEQ}")
        s, shas = seqs(out), sha_lines(out)
        check("2f", "保存出的还原点 SYSTEM 干净（序号相等，且晚于原序号）",
              len(s) == 2 and s[0] == s[1] and fixture_seq and s[0] > fixture_seq[0], f"seq {s} (原 {fixture_seq})")
        check("2g", "残留的 LOG1/LOG2 原样保留（不截断）",
              shas.get("SYSTEM.LOG1") == sample_sha.get("SYSTEM.LOG1") and shas.get("SYSTEM.LOG2") == sample_sha.get("SYSTEM.LOG2"))
        st, _ = call("POST", f"/api/reductions/{ok_red['ID']}/apply")
        st, script = boot(BOOT_MAC, hints)
        check("2h", "测试终端从新还原点开机 /boot 200", st == 200 and "sanhook" in str(script), f"HTTP {st} {str(script)[:100]}")
    else:
        for cid in ("2f", "2g", "2h"):
            skip(cid, "新还原点检查", "没有新还原点")

    def refused(cid_prefix, label, prepare, expect):
        st, r = call("POST", f"/api/terminals/{SUPER_TID}/super")
        fresh_super_clone(CFG)
        out = with_windows(SYS_CLONE, prepare, rw=True)
        check(f"{cid_prefix}a", f"造出{label}的超管盘", "PREPARED" in out, out[-120:])
        put_super("offline", GID, SUPER_IP)
        before_pool, before_reds = pool_state(CFG), reductions(CFG)
        st, r, task = stop_super(f"hive-{cid_prefix}")
        err = str(task.get("Error") or r)
        check(f"{cid_prefix}b", f"{label}：拒绝保存，给出中文原因", task.get("Status") == "failed" and expect in err and "未改动" in err, err[:160])
        check(f"{cid_prefix}c", "配置、还原点、超管盘（guid/来源）原样", pool_state(CFG) == before_pool and reductions(CFG) == before_reds,
              "" if pool_state(CFG) == before_pool else pool_state(CFG)[:200])
        st, t = call("GET", f"/api/terminals/{SUPER_TID}")
        check(f"{cid_prefix}d", "这台仍是超管机，可在正常关机后重存", isinstance(t, dict) and t.get("IsSuper") is True)

    section("3 事务日志不可用：拒绝且不改任何东西")
    refused("3", "日志被截断的脏 SYSTEM",
            f"cp {SAMPLE}/SYSTEM {HIVE}/SYSTEM && : > {HIVE}/SYSTEM.LOG1 && : > {HIVE}/SYSTEM.LOG2 && sync && echo PREPARED",
            "注册表")

    section("4 快速启动/休眠关机：拒绝且不改任何东西")
    refused("4", "hiberfil.sys 头为 HIBR",
            "python3 -c \"open('hiberfil.sys','wb').write(b'HIBR'+bytes(4092))\" && sync && echo PREPARED",
            "快速启动")
finally:
    section("9 清理（只删本矩阵建的对象）")
    cleanup()
    left = find_on_nodes(SUPER_MAC, BOOT_MAC, PREFIX, *([CFG] if cfg else []))
    check("9a", "各节点池里没有测试残留", not left, " ".join(left)[:200])
    check("9b", f"{SOURCE_GROUP} 的当前点快照仍在且未变", sh(f"zfs list -H -o name,guid {dirty_snap} 2>/dev/null || true") == src_before)

sys.exit(summary())
