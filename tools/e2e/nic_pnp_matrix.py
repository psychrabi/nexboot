#!/usr/bin/env python3
"""071 半自动真机矩阵；只操作明确指定的 e2e 配置和终端，保留资产供取证。

ND_PNP_CONFIG 必填；ND_PNP_BIOS_CONFIG 可选（没有 MBR 镜像则 BIOS 只验 PE）。
沿用 ndapi 的 ND_BASE/ND_USER/ND_PASSWORD/SSH 环境。每次提示后操作 VMware，
输入终端 ID 或确认实机画面；JSON 报告区分 API 证据和人工桌面/数据盘证据。
"""
import json
import os
import time
from pathlib import Path

from ndapi import call, login, ssh

NICS = {"vmxnet3": ("15ad", "07b0"), "e1000e": ("8086", "10d3"), "e1000": ("8086", "100f")}


def api(method, path, body=None):
    status, result = call(method, path, body)
    if status >= 300:
        raise RuntimeError(f"{method} {path}: HTTP {status}: {result}")
    return result


def wait_task(task_id, seconds=1800):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        task = api("GET", f"/api/tasks/{task_id}")
        if task["Status"] == "failed":
            raise RuntimeError(task.get("Error") or "任务失败")
        if task["Status"] == "success":
            return task
        time.sleep(3)
    raise TimeoutError(f"任务 {task_id} 未完成")


def terminal(nic, firmware, used_macs):
    tid = input(f"关机设置网卡 {nic} / {firmware} / 新 MAC，网络启动识别一次后关机；输入 e2e 终端 ID：").strip()
    t = api("GET", f"/api/terminals/{tid}")
    if not t["Name"].startswith("e2e-") or t["IsSuper"] or t["State"] != "offline":
        raise ValueError("只接受离线、非超管、名称 e2e- 开头的终端")
    g = api("GET", f"/api/groups/{t['GroupID']}")
    if not g["Name"].startswith("e2e-"):
        raise ValueError("终端必须在独立 e2e- 分组")
    status = api("GET", f"/api/terminals/{tid}/model")
    p = api("GET", f"/api/models/{status['profile_id']}")
    if (p["nic_vendor"].lower(), p["nic_device"].lower()) != NICS[nic]:
        raise ValueError(f"实际网卡 {p['nic_label']} 与 {nic} 不符")
    expected = "efi" if firmware == "UEFI" else "pcbios"
    if p["platform"] != expected or t["MAC"] in used_macs:
        raise ValueError("固件不符，或本机型的两个终端 MAC 相同")
    used_macs.add(t["MAC"])
    return t, g, p


def boot(t, number):
    before = api("GET", f"/api/terminals/{t['ID']}").get("LastBootOKAt")
    input(f"启动/重启 {t['Name']}，第 {number} 次。进入桌面后按回车：")
    now = api("GET", f"/api/terminals/{t['ID']}")
    if not now.get("LastBootOKAt") or now["LastBootOKAt"] == before:
        raise RuntimeError("没有本次 Windows /boot/net-config 回调")
    identity = api("GET", f"/api/terminals/{t['ID']}/identity")
    if identity["stale"] or not identity.get("identity", {}).get("LayerReductionRef"):
        raise RuntimeError("身份层未就绪")
    return {"callback": now["LastBootOKAt"], "identity": identity["identity"], "desktop": "人工确认"}


def main():
    login()
    configs = {"UEFI": os.environ["ND_PNP_CONFIG"], "BIOS": os.environ.get("ND_PNP_BIOS_CONFIG")}
    catalog = [cfg for image in api("GET", "/api/images")["items"]
               for cfg in api("GET", f"/api/images/{image['ID']}/configs")["items"]]
    for cid in filter(None, configs.values()):
        cfg = next((c for c in catalog if c["ID"] == cid), None)
        if cfg is None:
            raise ValueError(f"配置不存在：{cid}")
        if not cfg["Name"].startswith("e2e-"):
            raise ValueError("请先从待测还原点派生独立 e2e- 配置；禁止使用现场配置")
    dns_before = ssh("find /etc/dnsmasq.d -maxdepth 1 -type f -exec sha256sum {} + | sort")
    report = []
    output = Path(os.environ.get("ND_PNP_REPORT", "nic-pnp-results.json"))
    try:
        for firmware, cid in configs.items():
            for nic in NICS:
                macs = set()
                for index in range(2):
                    t, g, p = terminal(nic, firmware, macs)
                    row = {"nic": nic, "firmware": firmware, "mac": t["MAC"], "busloc": p["bus_loc"], "boots": []}
                    report.append(row)
                    if not cid:
                        task = api("POST", f"/api/terminals/{t['ID']}/identity-capture", {})
                        input("没有 BIOS/MBR 镜像：本轮只启动 PE 取证，自动关机后回车：")
                        row["pe_task"] = wait_task(task["task_id"])
                        row["scope"] = "仅 PE；未验证 BIOS Windows 开机"
                        continue
                    if g["SystemConfigID"] != cid:
                        raise ValueError("请将 e2e 分组绑定到本轮指定配置后重跑；脚本不改现场绑定")
                    if index == 0:
                        task = api("POST", f"/api/terminals/{t['ID']}/model-adapt", {"config_id": cid, "name": f"e2e-{nic}-{firmware.lower()}-{int(time.time())}"})
                        input("启动客户机进入适配 PE，等待自动关机后回车：")
                        row["adapt_task"] = wait_task(task["task_id"])
                    else:
                        input("第二个 MAC 正常网络启动：先查看客户机详情的「预计下次启动」；可计算或借用的 VMware 实例直接启动，其余进入 PE 取证并关机。完成后回车：")
                        identity = api("GET", f"/api/terminals/{t['ID']}/identity").get("identity")
                        if not identity or identity["ProfileID"] != p["id"]:
                            raise RuntimeError("正常开机没有自动生成终端身份")
                        row["automatic_identity"] = identity
                    for number in range(1, 4):
                        row["boots"].append(boot(t, number))
                    until = time.monotonic() + 300
                    while time.monotonic() < until:
                        if api("GET", f"/api/terminals/{t['ID']}")["State"] != "online":
                            raise RuntimeError("五分钟稳定性检查期间终端离线")
                        time.sleep(10)
                    row["stable_seconds"] = 300
                    row["data_disks_evidence"] = input("检查客户机 nd-datadisks 日志中 /boot/data-disks 回调，输入日志文件/截图位置（不可空）：").strip()
                    if not row["data_disks_evidence"]:
                        raise ValueError("缺少数据盘回调证据")
                    row["nopin_evidence"] = input("检查承载节点内核日志无该会话 NOPIN 超时，输入日志证据位置（不可空）：").strip()
                    if not row["nopin_evidence"]:
                        raise ValueError("缺少会话稳定性证据")
                    row["scope"] = "三次桌面人工确认 + API 回调/身份层 + 五分钟在线"
        if ssh("find /etc/dnsmasq.d -maxdepth 1 -type f -exec sha256sum {} + | sort") != dns_before:
            raise RuntimeError("dnsmasq 配置在矩阵期间发生变化")
        row = {"manual_remaining": "PCI 槽位、切还原点/超管保存、失败注入、关机后 SYSTEM 驱动核对、跨节点 ADAPT/IDENT 残留与一致性检查需独立取证"}
        report.append(row)
    finally:
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        print(f"证据已保存：{output}；保留 e2e 资产供注册表核查，未删除任何现场资产。")


if __name__ == "__main__":
    main()
