#!/usr/bin/env python3
"""界面入盟矩阵：/api/cluster/join、create、discover、adopt 与 /internal/cluster/join-config。

其余多节点矩阵都用 install-go.sh 的命令行参数把节点装进集群；运维从界面「加入集群」
走的是另一条路：加入者拿 VIP + 令牌去拉 join-config（共享密钥、池名、成员地址），
再分离启动 ndiskless-configure 把自己重配成备机。这条路此前没有任何端到端覆盖。

    A 只读与拒绝路径（总是跑，不改集群）
      join-config 的认证与内容、各成员的 identity、对成员的纳管/加入/建集群一律被拒、discover
    B 真实的界面重新入盟（破坏性，需 ND_JOIN_DESTRUCTIVE=1）
      挑一台既不是写入者、也不是跑测试的那台的备机，清成单机（保留数据池），
      先用错令牌加入（必须被拒、状态不变），再用正确令牌经界面加入，等它回到花名册、
      成为备机、拿到库副本，并确认集群仍可写。失败时用 join-config 下发的密钥走命令行兜底恢复。
    C 复制残留回收（破坏性程度低：只在备机上造/删空数据集）
      被打断的接收留下的 recv-<pid>-<seq> 临时数据集、-rebuilding- 重建残留要被下一轮回收，
      -diverged- 副本必须原样保留（它装着别处没有的内容）。
    D 超管机落在写入者，与分组绑定无关
      分组绑到一台备机：普通开机落在那台；设为超管后开机落到写入者，超管盘只在写入者上建，
      关机存还原点成功，盘随后被收回。

    ND_NODES / ND_VIP / ND_PASSWORD / ND_CLUSTER_TOKEN / ND_SSH_USER / ND_RUNNER_IP
    ND_JOIN_DESTRUCTIVE=1   打开 B
"""
import os
import shlex
import sys
import time
import urllib.parse

from ndcluster import (NODES, PORT, TOKEN, VIP, VIP_BASE, PASSWORD, anon_get, anon_post, call,
                       check, forget_tokens, host, node_call, node_id_of, pool_of, roster, section,
                       skip, ssh, ssh_out, summary, wait, wait_roster_online)

RUNNER = os.environ.get("ND_RUNNER_IP", "")
DESTRUCTIVE = os.environ.get("ND_JOIN_DESTRUCTIVE") == "1"
STAMP = time.strftime("%H%M%S")


def ha_status(base):
    st, body = node_call(base, "GET", "/internal/ha/status", timeout=8)
    return body if st == 200 and isinstance(body, dict) else {}


def writer_base():
    for b in NODES:
        if ha_status(b).get("role") == "active":
            return b
    return ""


def _group_reduction(group_id):
    st, g = call(VIP_BASE, "GET", f"/api/groups/{group_id}")
    return (g or {}).get("SystemReductionID") if st == 200 and isinstance(g, dict) else None


def chap_enabled(node_host):
    """(读到了吗, 开没开)。只数行，不把密钥读出来。"""
    ok, out = ssh_out(node_host, "grep -cE '^NDISKLESS_CHAP_SECRET=\"?[^[:space:]\"]' /etc/ndiskless/ndiskless.env || true")
    return ok, ok and out.strip() not in ("", "0")


def chap_fingerprint(node_host):
    """NDISKLESS_CHAP_SECRET 的 sha256（"off" = 演示模式，"" = 没读到）。只回哈希。"""
    ok, out = ssh_out(node_host, "v=$(sed -n 's/^NDISKLESS_CHAP_SECRET=//p' /etc/ndiskless/ndiskless.env | head -1 "
                                 "| tr -d '\"[:space:]'); if [ -n \"$v\" ]; then printf %s \"$v\" | sha256sum | cut -c1-64; "
                                 "else echo off; fi")
    return out if ok and (out == "off" or len(out) == 64) else ""


def text(r):
    return r.get("error", "") if isinstance(r, dict) else str(r)


def section_a(W):
    section("A1 join-config：只给持令牌的人")
    st, _ = anon_get(VIP_BASE, "/internal/cluster/join-config")
    check("A1a", "不带令牌 401", st == 401, f"HTTP {st}")
    import urllib.request
    import urllib.error
    rq = urllib.request.Request(VIP_BASE + "/internal/cluster/join-config",
                                headers={"Authorization": "Bearer not-the-token"})
    try:
        with urllib.request.urlopen(rq, timeout=10) as r:
            st = r.status
    except urllib.error.HTTPError as e:
        st = e.code
    except Exception:  # noqa: BLE001
        st = 0
    check("A1b", "错令牌 401", st == 401, f"HTTP {st}")

    st, jc = node_call(VIP_BASE, "GET", "/internal/cluster/join-config")
    ok = st == 200 and isinstance(jc, dict)
    check("A1c", "经 VIP 持令牌取得 join-config", ok, f"HTTP {st}")
    if not ok:
        return {}
    keys = set(jc)
    check("A1d", "只含 jwt_secret/chap_secret/pool/peers 四项", keys == {"jwt_secret", "chap_secret", "pool", "peers"},
          str(sorted(keys)))
    # CHAP 关着（NDISKLESS_CHAP_SECRET 为空，演示模式）时 chap_secret 本该是空的：加入者照抄，全集群一致地关。
    read, chap_on = chap_enabled(host(W))
    if not read:
        check("A1e", "共享密钥非空（读不到写入者的 CHAP 设置）", False)
    elif chap_on:
        check("A1e", "共享密钥非空（CHAP 开着）", bool(jc.get("jwt_secret")) and bool(jc.get("chap_secret")))
    else:
        check("A1e", "JWT 密钥非空、CHAP 关着时 chap_secret 为空", bool(jc.get("jwt_secret")) and not jc.get("chap_secret"))
    wpool = pool_of(host(W))
    check("A1f", "池名是写入者当前的数据池", jc.get("pool") == wpool, f"{jc.get('pool')} vs {wpool}")
    peers = [str(p) for p in (jc.get("peers") or [])]
    ips = {host(b) for b in NODES}
    check("A1g", "成员地址覆盖全部真实节点", ips <= set(peers), f"{peers}")
    check("A1h", "成员地址里没有 VIP（VIP 不是一台机器）", VIP not in peers, f"{peers}")
    leaked = [k for k in ("password", "bootstrap", "db", "dsn", "cluster_token") if k in str(sorted(keys)).lower()]
    check("A1i", "不带密码/库/令牌字段", not leaked, str(leaked))

    section("A2 成员不可被纳管")
    for i, b in enumerate(NODES):
        st, body = anon_get(b, "/internal/cluster/identity")
        import json
        try:
            ident = json.loads(body)
        except Exception:  # noqa: BLE001
            ident = {}
        check(f"A2a{i}", f"{host(b)} identity 应答且 adoptable=false",
              st == 200 and ident.get("adoptable") is False and bool(ident.get("node_id")), f"HTTP {st} {body[:90]}")
    b = NODES[-1]
    st, body = anon_post(b, "/internal/cluster/adopt", {"vip": VIP, "cluster_token": TOKEN})
    check("A2b", "对成员匿名纳管被拒（409，或关闭纳管时 403）", st in (409, 403), f"HTTP {st} {body[:90]}")

    section("A3 对已在集群里的节点：加入/建集群/纳管一律被拒")
    for i, b in enumerate(NODES):
        st, r = call(b, "POST", "/api/cluster/join", {"vip": VIP, "cluster_token": TOKEN})
        # 写入者答 409「已在集群中」；备机的写操作统一被网关挡下（503 并指向 VIP），同样不得受理。
        ok = st == 409 if b == W else (st == 409 or (st == 503 and VIP in text(r)))
        check(f"A3a{i}", f"{host(b)} 再次加入被拒（写入者 409，备机 409/503 指向 VIP）", ok, f"HTTP {st} {text(r)[:80]}")
    st, r = call(W, "POST", "/api/cluster/join", {"vip": "", "cluster_token": ""})
    check("A3b", "空字段被拒（不得是 202）", st in (400, 409), f"HTTP {st} {text(r)[:80]}")
    st, r = call(W, "POST", "/api/cluster/join", {"vip": "192.168.10.250/24", "cluster_token": TOKEN})
    check("A3c", "带掩码的 VIP 被拒（不得是 202）", st in (400, 409), f"HTTP {st} {text(r)[:80]}")
    st, r = call(W, "POST", "/api/cluster/create", {"vip": VIP})
    check("A3d", "成员上建集群被拒 409", st == 409, f"HTTP {st} {text(r)[:80]}")
    # 地址按 discover 给出的 host:port 格式（AdoptNode 的约定）。
    st, r = call(W, "POST", "/api/cluster/adopt", {"address": f"{host(NODES[-1])}:{PORT}"})
    check("A3e", "纳管一台成员被拒 409", st == 409, f"HTTP {st} {text(r)[:80]}")
    st, r = call(W, "POST", "/api/cluster/adopt", {"address": ""})
    check("A3f", "纳管不给地址 400", st == 400, f"HTTP {st} {text(r)[:80]}")

    section("A4 discover：只列可纳管的生面孔")
    st, r = call(W, "GET", "/api/cluster/discover", timeout=90)
    items = (r.get("items") or []) if st == 200 and isinstance(r, dict) else []
    check("A4a", "发现接口应答", st == 200, f"HTTP {st} {text(r)[:80]}")
    ids = set(roster(W))
    check("A4b", "不列出花名册里的节点", not any(x.get("node_id") in ids for x in items),
          str([x.get("address") for x in items]))
    return jc


def wipe_to_standalone(h, pool):
    mp = ssh_out(h, f"zfs get -H -o value mountpoint {pool}/nd/db 2>/dev/null")[1].strip() or f"/{pool}/nd/db"
    r = ssh(h, "systemctl stop ndiskless keepalived 2>/dev/null; systemctl disable keepalived 2>/dev/null; "
               "rm -rf /var/lib/ndiskless /etc/ndiskless; "
               f"rm -f {mp}/*.db {mp}/*.db-wal {mp}/*.db-shm; "
               f"ndiskless-configure --pool {shlex.quote(pool)} --bootstrap-password {shlex.quote(PASSWORD)} 2>&1 | tail -3",
            timeout=600)
    return r.returncode == 0, (r.stdout + r.stderr).strip()[-300:]


def env_has_token(h):
    return ssh(h, "grep -q '^NDISKLESS_CLUSTER_TOKEN=.' /etc/ndiskless/ndiskless.env && echo yes || echo no").stdout.strip() == "yes"


def cli_rejoin(h, pool, jc, others):
    """兜底：界面加入失败时，按 join-config 下发的密钥用命令行把节点装回备机。"""
    args = ["ndiskless-configure", "--peer", ",".join(others), "--vip", VIP, "--cluster-token", TOKEN,
            "--ha-role", "standby", "--node-addr", h, "--pool", pool,
            "--jwt-secret", jc.get("jwt_secret", ""), "--chap-secret", jc.get("chap_secret", "")]
    r = ssh(h, " ".join(shlex.quote(a) for a in args) + " 2>&1 | tail -3", timeout=600)
    return r.returncode == 0


def section_b(W, jc):
    section("B 界面重新入盟（破坏性）")
    if not DESTRUCTIVE:
        skip("B", "界面重新入盟", "未设 ND_JOIN_DESTRUCTIVE=1（要把一台备机清成单机）")
        return
    if len(NODES) < 3 or not jc:
        skip("B", "界面重新入盟", "需要至少 3 台且 join-config 可用（清掉一台后集群仍需主备）")
        return
    cands = [b for b in NODES if b != W and host(b) != RUNNER and ha_status(b).get("role") == "standby"]
    if not cands:
        skip("B", "界面重新入盟", "找不到既非写入者也非测试机的备机")
        return
    T = cands[0]
    h = host(T)
    pool = pool_of(h)
    nid = node_id_of(T)
    others = [host(b) for b in NODES if b != T]
    rejoined = False
    try:
        ok, out = wipe_to_standalone(h, pool)
        forget_tokens()
        up = wait(lambda: anon_get(T, "/healthz", timeout=4)[0] not in (0,), 180, 5)
        check("B1", f"{h} 已清成单机并重新起来（池 {pool} 保留）", ok and bool(up) and not env_has_token(h), out[-120:])
        st, body = anon_get(T, "/internal/cluster/identity")
        check("B2", "有池的单机不可被纳管（adoptable=false）", st == 200 and '"adoptable":false' in body.replace(" ", ""),
              f"HTTP {st} {body[:80]}")

        st, r = call(T, "POST", "/api/cluster/join", {"vip": VIP, "cluster_token": ""})
        check("B3", "不给令牌加入 400", st == 400, f"HTTP {st} {text(r)[:80]}")
        st, r = call(T, "POST", "/api/cluster/join", {"vip": VIP, "cluster_token": "wrong-" + TOKEN[:6]})
        check("B4", "错令牌加入被拒 502（连不上集群），不是 202", st == 502, f"HTTP {st} {text(r)[:80]}")
        time.sleep(5)
        check("B5", "被拒之后本机仍是单机（env 里没有集群令牌）", not env_has_token(h))

        st, r = call(T, "POST", "/api/cluster/join", {"vip": VIP, "cluster_token": TOKEN})
        check("B6", "正确令牌加入受理 202", st == 202 and isinstance(r, dict) and r.get("role") == "standby",
              f"HTTP {st} {str(r)[:100]}")
        forget_tokens()
        got = wait(lambda: ha_status(T).get("role") == "standby" or None, 300, 10)
        check("B7", "重启后以备机身份应答 /internal/ha/status", bool(got), str(ha_status(T))[:120])
        env = ssh(h, "grep -E '^NDISKLESS_(POOL|NODE_ADDR|ROLE)=' /etc/ndiskless/ndiskless.env").stdout
        check("B8", "保留本机池名、写对本机地址", f"NDISKLESS_POOL={pool}" in env and f"NDISKLESS_NODE_ADDR={h}" in env,
              env.replace("\n", " "))
        wjwt = ssh(host(W), "grep '^NDISKLESS_JWT_SECRET=' /etc/ndiskless/ndiskless.env").stdout.strip()
        tjwt = ssh(h, "grep '^NDISKLESS_JWT_SECRET=' /etc/ndiskless/ndiskless.env").stdout.strip()
        check("B9", "JWT 密钥与写入者一致（会话可跨节点）", bool(wjwt) and wjwt == tjwt)
        # 入盟者照抄写入者的 CHAP：不一致时客户机放到它上面，脚本凭据（写入者算）与 target（它自己算）对不上，Windows 登录被拒。
        wfp, tfp = chap_fingerprint(host(W)), chap_fingerprint(h)
        check("B9a", "CHAP 开关与密钥哈希和写入者一致", bool(wfp) and wfp == tfp,
              f"写入者={'off' if wfp == 'off' else 'on' if wfp else '?'} 入盟者={'off' if tfp == 'off' else 'on' if tfp else '?'}")
        r = wait_roster_online(W, len(NODES), 300, 10)
        check("B10", f"花名册回到 {len(NODES)} 台全在线", bool(r) and nid in (r or {}),
              str({k: v.get('online') for k, v in (r or roster(W)).items()}))
        ka = wait(lambda: ssh(h, "systemctl is-active keepalived").stdout.strip() == "active" or None, 300, 10)
        check("B11", "写入者把它列进 peer 后 keepalived 起来了", bool(ka))
        ready = wait(lambda: ssh(h, f"curl -s -m 3 -o /dev/null -w '%{{http_code}}' http://127.0.0.1:{PORT}/healthz").stdout.strip() == "200" or None, 420, 10)
        check("B12", "拿到库副本、够格参选（/healthz 200）", bool(ready))
        check("B13", "VIP 没被加入者抢走", ha_status(W).get("role") == "active" and
              ssh(h, f"ip -4 addr | grep -c ' {VIP}/' || true").stdout.strip() == "0")
        name = f"e2e-join-{STAMP}"
        st, g = call(VIP_BASE, "POST", "/api/users", {"username": name, "password": "E2e-join-Pass1"})
        check("B14", "入盟后集群仍可写", st in (200, 201), f"HTTP {st} {str(g)[:80]}")
        if st in (200, 201):
            uid = (g or {}).get("ID") or (g or {}).get("id") or name
            call(VIP_BASE, "DELETE", f"/api/users/{uid}")
        rejoined = bool(got) and bool(r)
    finally:
        if not rejoined:
            print(f"    兜底：用命令行把 {h} 装回备机")
            cli_rejoin(h, pool, jc, others)
            wait_roster_online(W, len(NODES), 300, 10)
            # 命令行兜底同样要带上 --chap-secret（cli_rejoin 照 join-config 传），否则它会留着单机时生成的密钥。
            wfp, tfp = chap_fingerprint(host(W)), chap_fingerprint(h)
            check("B15", "命令行兜底入盟后 CHAP 密钥哈希与写入者一致", bool(wfp) and wfp == tfp)


def section_c(W):
    section("C 复制残留回收")
    sb = [b for b in NODES if b != W and ha_status(b).get("role") == "standby"]
    if not sb:
        skip("C", "复制残留回收", "没有备机")
        return
    S = sb[-1]
    h = host(S)
    p = pool_of(h)
    tmp = f"{p}/nd/recv-4242{STAMP}-1"
    reb = f"{p}/nd-rebuilding-1000000000"
    div = f"{p}/nd-diverged-{int(time.time())}000000000"
    made = ssh(h, f"zfs create -o mountpoint=none {tmp} && zfs create -o mountpoint=none {reb} && "
                  f"zfs create -o mountpoint=none {div} && echo ok").stdout.strip()
    try:
        check("C0", f"在备机 {h} 上造出三种残留", made == "ok", made)
        exists = lambda ds: ssh(h, f"zfs list -H -o name {ds} 2>/dev/null").stdout.strip() == ds
        gone_tmp = wait(lambda: (not exists(tmp)) or None, 300, 10)
        check("C1", "被打断的接收留下的 recv-* 临时数据集被回收", bool(gone_tmp))
        logs = ssh(h, "journalctl -u ndiskless --since '-10min' --no-pager | grep -c 'interrupted receive' || true").stdout.strip()
        check("C2", "回收写了日志", logs not in ("", "0"), f"命中 {logs} 行")
        gone_reb = wait(lambda: (not exists(reb)) or None, 300, 10)
        check("C3", "已同步时 -rebuilding- 重建残留全部回收（不再保留一份）", bool(gone_reb))
        check("C4", "-diverged- 副本原样保留", exists(div))
        st = ha_status(S)
        check("C5", "回收之后它仍是备机", st.get("role") == "standby", str(st)[:120])
    finally:
        ssh(h, f"zfs destroy -r {tmp} 2>/dev/null; zfs destroy -r {reb} 2>/dev/null; zfs destroy -r {div} 2>/dev/null; true")


def _boot_portal(base, mac):
    import re
    st, body = call(base, "GET", f"/boot?mac={urllib.parse.quote(mac)}", timeout=90)
    if st != 200 or not isinstance(body, str):
        return ""
    m = re.search(r"iscsi:([0-9a-fA-F.:]+?):::", body)
    return m.group(1) if m else ""


def section_d(W):
    section("D 超管机落在写入者，与分组绑定无关")
    wid = node_id_of(W)
    others = [b for b in NODES if b != W]
    if not others:
        skip("D", "超管落点", "单机")
        return
    X = others[0]
    xid = node_id_of(X)
    st, gs = call(VIP_BASE, "GET", "/api/groups")
    tmpl = next((g for g in (gs.get("items") or []) if g.get("SystemImageID")), None) if st == 200 else None
    if not tmpl:
        skip("D", "超管落点", "没有可参照的分组（镜像/配置）")
        return
    # 超管保存在自己 fork 的配置上做：直接用参照分组的配置会往现场配置里存还原点、挪走它的当前应用点。
    img0 = tmpl["SystemImageID"]
    fork_name = f"e2e-sj-{STAMP}"
    reds = (call(VIP_BASE, "GET", f"/api/configs/{tmpl['SystemConfigID']}/reductions")[1] or {}).get("items") or []
    fork = None
    if reds:
        call(VIP_BASE, "POST", f"/api/configs/{tmpl['SystemConfigID']}/fork", {"name": fork_name, "reduction_id": reds[-1]["ID"]})
        fork = wait(lambda: next((c for c in (call(VIP_BASE, "GET", f"/api/images/{img0}/configs")[1] or {}).get("items", [])
                                  if c.get("Name") == fork_name), None), 180, 5)
    if not fork:
        skip("D", "超管落点", "临时配置 fork 不出来")
        return
    cfg0 = fork["ID"]
    xp0 = pool_of(host(X))
    # X 还没收到配置快照时开机会正确地退回写入者，D2 就测不到绑定；等 X 收到再往下走。
    wait(lambda: ssh(host(X), f"zfs list -H -t snapshot -o name -r {xp0}/nd/{cfg0} 2>/dev/null | grep -c @ || true")
         .stdout.strip() not in ("", "0") or None, 300, 5)
    head = tmpl["StartIP"].rsplit(".", 1)[0]
    mac = "0e:2e:5a:0b:00:01"
    MACH = mac.replace(":", "").upper()
    name = f"e2e-sj-{STAMP}"
    body = {"name": name, "start_ip": f"{head}.200", "client_max": 4, "gateway": tmpl.get("Gateway", ""),
            "netmask": tmpl.get("Netmask", "255.255.255.0"), "dns1": tmpl.get("DNS1") or "114.114.114.114",
            "system_image_id": img0, "system_config_id": cfg0, "storage_server_id": xid}
    st, g = call(VIP_BASE, "POST", "/api/groups", body)
    gid = (g or {}).get("ID") if isinstance(g, dict) else None
    tid = None
    portal = {}
    for n in roster(VIP_BASE).values():
        if n.get("ip"):
            portal[n["ip"]] = n["ip"]
        if n.get("portal_ip"):
            portal[n["portal_ip"]] = n.get("ip")
    if VIP:
        portal[VIP] = host(W)
    norm = lambda a: portal.get(a, a)
    wp, xp = pool_of(host(W)), pool_of(host(X))
    try:
        check("D0", f"建了绑定到 {host(X)} 的分组", bool(gid), f"HTTP {st} {str(g)[:100]}")
        if not gid:
            return
        st, t = call(VIP_BASE, "POST", "/api/terminals", {"mac": mac, "group_id": gid, "name": name})
        tid = (t or {}).get("ID") if isinstance(t, dict) else None
        check("D1", "建了终端", bool(tid), f"HTTP {st} {str(t)[:100]}")
        if not tid:
            return
        p1 = norm(_boot_portal(VIP_BASE, mac))
        check("D2", "普通开机落在绑定的节点", p1 == host(X), f"{p1} want {host(X)}")
        # 还原点行上没有「当前」标记：配置当前应用的还原点记在用它的分组上（SystemReductionID）。
        # 以前在还原点行里找 "current" 字段，永远找不到，D8 前的「应用回原来那个」从没执行过。
        prev = _group_reduction(gid)
        if not prev:
            before = call(VIP_BASE, "GET", f"/api/configs/{cfg0}/reductions")[1]
            items = (before or {}).get("items") or [] if isinstance(before, dict) else []
            prev = next((x["ID"] for x in items if x.get("Name") == "@0"), items[0]["ID"] if items else None)
        st, r = call(VIP_BASE, "POST", f"/api/terminals/{tid}/super")
        check("D3", "设为超管", st == 200, f"HTTP {st} {text(r)[:80]}")
        p2 = norm(_boot_portal(VIP_BASE, mac))
        check("D4", "超管开机改落写入者（绑定不压过超管粘性）", p2 == host(W), f"{p2} want {host(W)}")
        on_w = wait(lambda: ssh(host(W), f"zfs list -H -o name -r {wp}/run | grep -c SCLIENT-{MACH} || true").stdout.strip() not in ("", "0") or None, 120, 5)
        check("D5", "超管盘建在写入者上", bool(on_w))
        on_x = ssh(host(X), f"zfs list -H -o name -r {xp}/run 2>/dev/null | grep -c SCLIENT-{MACH} || true").stdout.strip()
        check("D6", "绑定节点上没有另起一份超管盘", on_x in ("", "0"), f"{on_x} 份")
        st, r = call(VIP_BASE, "POST", f"/api/terminals/{tid}/super/stop", {"reduction_name": f"e2e-sj-{STAMP}"})
        saved = wait(lambda: next((x for x in (call(VIP_BASE, "GET", f"/api/configs/{cfg0}/reductions")[1].get("items") or [])
                                   if (x.get("DisplayName") or x.get("Name", "")).endswith(f"e2e-sj-{STAMP}")), None), 180, 5)
        check("D7", "在写入者上关机存还原点成功", st in (200, 202) and bool(saved), f"HTTP {st} {text(r)[:80]}")
        if saved:
            # 还原点被超管机占着时删不掉（产品正确地拒绝），先取消超管。
            call(VIP_BASE, "DELETE", f"/api/terminals/{tid}/super")
            # 超管保存会把新还原点设为配置的当前应用点（参照分组也随之用它开机）；先应用回原来那个再删。
            if prev:
                ast, ar = call(VIP_BASE, "POST", f"/api/reductions/{prev}/apply")
                if ast != 200:
                    print(f"  (应用回 {prev} 失败：HTTP {ast} {text(ar)[:80]})")
                # 等用这个配置的分组都不再指向新还原点，再删。
                wait(lambda: _group_reduction(gid) != saved["ID"] or None, 60, 2)
            st, r = call(VIP_BASE, "DELETE", f"/api/reductions/{saved['ID']}")
            if st == 409:  # 指针刚移动，偶尔还有在途的引用检查；稍等重试一次
                time.sleep(10)
                st, r = call(VIP_BASE, "DELETE", f"/api/reductions/{saved['ID']}")
            check("D8", "删掉测试还原点", st in (200, 202, 204), f"HTTP {st} {text(r)[:80]}")
    finally:
        if tid:
            call(VIP_BASE, "DELETE", f"/api/terminals/{tid}/super")
            call(VIP_BASE, "DELETE", f"/api/terminals/{tid}")
        if gid:
            call(VIP_BASE, "DELETE", f"/api/groups/{gid}")
        # 删配置连带它的还原点；分组刚删、写闸门或同步中会 409/503，稍等重试。
        for _ in range(12):
            if call(VIP_BASE, "DELETE", f"/api/configs/{cfg0}")[0] in (200, 202, 204, 404):
                break
            time.sleep(10)


def main():
    if len(NODES) < 2 or not VIP or not TOKEN:
        skip("0", "界面入盟矩阵", "需要 ND_NODES（≥2）、ND_VIP、ND_CLUSTER_TOKEN")
        return summary()
    W = writer_base()
    if not W:
        skip("0", "界面入盟矩阵", "找不到写入者")
        return summary()
    print(f"  writer={W} runner={RUNNER} destructive={DESTRUCTIVE}")
    jc = section_a(W)
    section_c(W)
    section_d(W)
    section_b(W, jc)
    return summary()


if __name__ == "__main__":
    sys.exit(main())
