#!/bin/sh
set -eu
BASE='__BASE__'
exec > /tmp/nd-install.log 2>&1
report_failure() {
  rc=$?
  if [ "$rc" -ne 0 ]; then
    curl -fsS --max-time 30 --data-binary @/tmp/nd-install.log "${BASE}log/install.log" || true
    curl -fsS --max-time 30 -H 'Content-Type: application/json' --data '{"attempt":__ATTEMPT__,"phase":"failed","message":"Ubuntu 无盘启动准备失败，请查看安装日志"}' "${BASE}report" || true
  fi
}
trap report_failure EXIT
# open-iscsi、curl 来自 ISO 的系统层，这里不装包，只做无盘配置。
mkdir -p /target/etc/iscsi /target/usr/local/sbin /target/etc/systemd/system/multi-user.target.wants
printf 'ISCSI_AUTO=true\n' > /target/etc/iscsi/iscsi.initramfs
# Never retain curtin's fixed installation target or this machine's MAC/IP in a shared template.
rm -rf /target/etc/iscsi/nodes /target/etc/iscsi/send_targets
rm -rf /target/var/lib/iscsi/nodes /target/var/lib/iscsi/send_targets
rm -f /target/etc/netplan/* /target/etc/cloud/cloud.cfg.d/50-curtin-networking.cfg
printf 'network:\n  version: 2\n  ethernets:\n    ethernet:\n      match: {name: "e*"}\n      dhcp4: true\n' > /target/etc/netplan/01-nexboot.yaml
chmod 600 /target/etc/netplan/01-nexboot.yaml
printf 'network: {config: disabled}\n' > /target/etc/cloud/cloud.cfg.d/99-nexboot-network.cfg
# 安装时的软件源指向安装服务器，换回官方源（纯局域网下只是留着不用）。
rm -f /target/etc/apt/sources.list.d/*
printf 'deb http://archive.ubuntu.com/ubuntu noble main restricted universe multiverse\ndeb http://archive.ubuntu.com/ubuntu noble-updates main restricted universe multiverse\ndeb http://security.ubuntu.com/ubuntu noble-security main restricted universe multiverse\n' > /target/etc/apt/sources.list
rm -rf /target/var/lib/apt/lists/* /target/var/log/installer /target/var/lib/cloud/seed /target/var/lib/cloud/instances
printf 'GRUB_CMDLINE_LINUX="$GRUB_CMDLINE_LINUX iscsi_auto ip=dhcp"\n' > /target/etc/default/grub.d/99-nexboot.cfg
curtin in-target --target=/target -- update-initramfs -u -k all
curtin in-target --target=/target -- update-grub
mkdir -p /target/boot/efi/EFI/BOOT
cp /target/boot/efi/EFI/ubuntu/grubx64.efi /target/boot/efi/EFI/BOOT/grubx64.efi
cp /target/boot/efi/EFI/ubuntu/grub.cfg /target/boot/efi/EFI/BOOT/grub.cfg
if [ -f /target/boot/efi/EFI/ubuntu/shimx64.efi ]; then
  cp /target/boot/efi/EFI/ubuntu/shimx64.efi /target/boot/efi/EFI/BOOT/BOOTX64.EFI
else
  cp /target/boot/efi/EFI/ubuntu/grubx64.efi /target/boot/efi/EFI/BOOT/BOOTX64.EFI
fi
# shim 见到同目录的 fallback 会先建 NVRAM 启动项再重启；无盘每次都从 iPXE 挂的盘启动，留着它就每次开机重启一遍。
rm -f /target/boot/efi/EFI/BOOT/fbx64.efi
curl -fsS --retry 3 "${BASE}iso-startup.py" -o /target/usr/local/sbin/nd-iso-startup
curl -fsS --retry 3 "${BASE}nd-datadisks" -o /target/usr/local/sbin/nd-datadisks
chmod 755 /target/usr/local/sbin/nd-iso-startup /target/usr/local/sbin/nd-datadisks
cat > /target/etc/systemd/system/nd-iso-startup.service <<'UNIT'
[Unit]
Description=NexBoot image installation and ordinary boot verification
Wants=network-online.target
After=network-online.target
Before=ssh.service
[Service]
Type=oneshot
ExecStart=/usr/local/sbin/nd-iso-startup
TimeoutStartSec=300
[Install]
WantedBy=multi-user.target
UNIT
cat > /target/etc/systemd/system/nd-datadisks.service <<'UNIT'
[Unit]
Description=NexBoot data disks
Wants=network-online.target
After=network-online.target
[Service]
Type=oneshot
ExecStart=/usr/local/sbin/nd-datadisks
[Install]
WantedBy=multi-user.target
UNIT
ln -sf ../nd-iso-startup.service /target/etc/systemd/system/multi-user.target.wants/nd-iso-startup.service
ln -sf ../nd-datadisks.service /target/etc/systemd/system/multi-user.target.wants/nd-datadisks.service
rm -rf /target/var/lib/cloud/instances
curl -fsS --max-time 30 --data-binary @/tmp/nd-install.log "${BASE}log/install.log"
curl -fsS --max-time 30 -H 'Content-Type: application/json' --data '{"attempt":__ATTEMPT__,"phase":"initializing","progress":90,"message":"Ubuntu 安装与启动准备完成，正在验证首次启动"}' "${BASE}report"
