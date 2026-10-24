#!/bin/sh
set -eu
BASE='__BASE__'
exec > /tmp/nd-early.log 2>&1
fail() {
  python3 - "$BASE" <<'PY' || true
import json,pathlib,sys,urllib.request
base=sys.argv[1]
for endpoint,data,content_type in [('log/install.log',pathlib.Path('/tmp/nd-early.log').read_bytes(),'text/plain'),('report',json.dumps({'attempt':__ATTEMPT__,'phase':'failed','message':'Ubuntu 安装准备失败，请查看安装日志；本地磁盘未被格式化'}).encode(),'application/json')]:
    try:
        urllib.request.urlopen(urllib.request.Request(base+endpoint,data=data,headers={'Content-Type':content_type},method='POST'),timeout=20).close()
    except OSError: pass
PY
  exit 1
}
# 只用 ISO 自带的组件：Server 版安装环境自带 open-iscsi，没有就连不上安装盘，也不去外网装。
if ! command -v iscsiadm >/dev/null 2>&1; then
  echo '安装环境里没有 iscsiadm（open-iscsi），连不上安装盘；请改用 Ubuntu Server 24.04 官方 ISO。'
  fail
fi
modprobe iscsi_ibft || fail
modprobe iscsi_tcp || fail
iscsiadm -m fw -l >/tmp/nd-iscsi.log 2>&1 || true
udevadm settle --timeout=30 || fail
# 动盘之前按序列号、数量和传输方式唯一认出安装盘（同一 target 上还有只读的 ISO 盘）。
disk=$(python3 - '__SERIAL__' <<'PY'
import json,subprocess,sys
def norm(v):return ''.join(c for c in (v or '').lower() if c.isalnum())
data=json.loads(subprocess.check_output(['lsblk','--json','--nodeps','--output','PATH,SERIAL,TRAN']))
matches=[d for d in data['blockdevices'] if norm(d.get('serial'))==norm(sys.argv[1])]
if len(matches)!=1 or matches[0].get('tran')!='iscsi': raise SystemExit('Installation disk identity is missing or ambiguous')
print(matches[0]['path'])
PY
) || fail
case "$disk" in /dev/?*) ;; *) echo "没有认出安装盘（序列号 __SERIAL__）"; fail;; esac
# 安装程序只认它探测到的盘：把认出的设备填进应答文件，本脚本跑完后它会重读 /autoinstall.yaml。
sed "s#__NEXBOOT_INSTALL_DISK__#$disk#g" /autoinstall.yaml > /tmp/nd-autoinstall.yaml || fail
cat /tmp/nd-autoinstall.yaml > /autoinstall.yaml || fail
if grep -q __NEXBOOT_INSTALL_DISK__ /autoinstall.yaml || ! grep -q "$disk" /autoinstall.yaml; then echo "安装盘 $disk 没能写进 /autoinstall.yaml"; fail; fi
curl -fsS --retry 3 -H 'Content-Type: application/json' --data '{"attempt":__ATTEMPT__,"phase":"installing","progress":10,"message":"已确认服务端安装盘，正在安装 Ubuntu"}' "${BASE}report" || fail
