#!/bin/sh
set -eu
BASE='__BASE__'
exec > /tmp/nd-install.log 2>&1
report_failure() {
  rc=$?
  curl -fsS --max-time 30 --data-binary @/tmp/nd-install.log "${BASE}log/install.log" || true
  [ ! -f /tmp/anaconda.log ] || curl -fsS --max-time 30 --data-binary @/tmp/anaconda.log "${BASE}log/anaconda.log" || true
  if [ "$rc" -ne 0 ]; then
    curl -fsS --max-time 30 -H 'Content-Type: application/json' --data '{"attempt":__ATTEMPT__,"phase":"failed","message":"RHEL 系无盘启动准备失败，请查看安装日志"}' "${BASE}report" || true
  fi
}
trap report_failure EXIT
# Anaconda's --nochroot root is shared across EL7/8/9. Never install from external repositories.
ROOT=/mnt/sysimage
[ -d "$ROOT/etc" ]
mkdir -p "$ROOT/etc/dracut.conf.d" "$ROOT/usr/local/sbin" "$ROOT/etc/systemd/system/multi-user.target.wants"
cat > "$ROOT/etc/dracut.conf.d/99-nexboot.conf" <<'DRACUT'
hostonly="no"
hostonly_cmdline="no"
add_dracutmodules+=" iscsi network rootfs-block "
DRACUT
# No target IQN, initiator, CHAP, installation NIC or IP may survive in a shared image.
rm -rf "$ROOT/var/lib/iscsi/nodes" "$ROOT/var/lib/iscsi/send_targets" "$ROOT/etc/iscsi/nodes" "$ROOT/etc/iscsi/send_targets"
rm -f "$ROOT/etc/iscsi/initiatorname.iscsi" "$ROOT/etc/cmdline.d/95root-dev.conf"
rm -f "$ROOT/etc/sysconfig/network-scripts/ifcfg-"* "$ROOT/etc/NetworkManager/system-connections/"* "$ROOT/var/lib/NetworkManager/no-auto-default.state"
# NM creates a DHCP connection for the current NIC, rather than binding to the installation MAC.
mkdir -p "$ROOT/etc/NetworkManager/conf.d"
printf '[main]\nno-auto-default=\n' > "$ROOT/etc/NetworkManager/conf.d/99-nexboot.conf"
chroot "$ROOT" /usr/bin/python3 - <<'PY'
import re
p='/etc/default/grub'
with open(p) as f: data=f.read()
args='rd.iscsi.firmware=1 rd.iscsi.ibft=1 rd.neednet=1 ip=ibft'
def clean(match):
    tokens=match.group(1).split()
    tokens=[t for t in tokens if not t.startswith(('rd.iscsi.', 'netroot=', 'ip=', 'ifname=', 'BOOTIF=', 'rd.neednet='))]
    return 'GRUB_CMDLINE_LINUX="'+' '.join(tokens+[args])+'"'
data,n=re.subn(r'^GRUB_CMDLINE_LINUX="([^"]*)"',clean,data,flags=re.M)
if not n: data+='\nGRUB_CMDLINE_LINUX="'+args+'"\n'
with open(p,'w') as f: f.write(data)
PY
chroot "$ROOT" grubby --update-kernel=ALL --remove-args='netroot ip ifname BOOTIF rd.iscsi.initiator rd.iscsi.target.name rd.iscsi.target.ip rd.iscsi.target.port rd.iscsi.target.group rd.iscsi.username rd.iscsi.password' --args='rd.iscsi.firmware=1 rd.iscsi.ibft=1 rd.neednet=1 ip=ibft'
chroot "$ROOT" dracut --force --regenerate-all
# Preserve the vendor boot directory and provide the UEFI removable boot path.
VENDOR=
for cfg in "$ROOT"/boot/efi/EFI/*/grub.cfg; do
  [ -f "$cfg" ] || continue
  case "$cfg" in */BOOT/*) continue;; esac
  [ -z "$VENDOR" ] || { echo 'Multiple EFI vendors; refusing ambiguous boot files'; exit 1; }
  VENDOR=${cfg%/grub.cfg}
done
[ -n "$VENDOR" ] && [ -f "$VENDOR/grubx64.efi" ]
# Older EFI installs keep the full config in the vendor directory; newer
# releases use a small configfile stub. Never overwrite that stub.
if grep -Eq '^[[:space:]]*configfile[[:space:]]' "$VENDOR/grub.cfg"; then
  chroot "$ROOT" grub2-mkconfig -o /boot/grub2/grub.cfg
else
  chroot "$ROOT" grub2-mkconfig -o "${VENDOR#"$ROOT"}/grub.cfg"
fi
mkdir -p "$ROOT/boot/efi/EFI/BOOT"
cp "$VENDOR/grubx64.efi" "$ROOT/boot/efi/EFI/BOOT/grubx64.efi"
cp "$VENDOR/grub.cfg" "$ROOT/boot/efi/EFI/BOOT/grub.cfg"
if [ -f "$VENDOR/shimx64.efi" ]; then
  cp "$VENDOR/shimx64.efi" "$ROOT/boot/efi/EFI/BOOT/BOOTX64.EFI"
elif [ -f "$VENDOR/shim.efi" ]; then
  cp "$VENDOR/shim.efi" "$ROOT/boot/efi/EFI/BOOT/BOOTX64.EFI"
else
  cp "$VENDOR/grubx64.efi" "$ROOT/boot/efi/EFI/BOOT/BOOTX64.EFI"
fi
rm -f "$ROOT/boot/efi/EFI/BOOT/fbx64.efi"
curl -fsS --retry 3 "${BASE}iso-startup.py" -o "$ROOT/usr/local/sbin/nd-iso-startup"
curl -fsS --retry 3 "${BASE}nd-datadisks" -o "$ROOT/usr/local/sbin/nd-datadisks"
chmod 755 "$ROOT/usr/local/sbin/nd-iso-startup" "$ROOT/usr/local/sbin/nd-datadisks"
for unit in nd-iso-startup nd-datadisks; do
  cat > "$ROOT/etc/systemd/system/$unit.service" <<UNIT
[Unit]
Description=NexBoot $unit
Wants=network-online.target
After=network-online.target
[Service]
Type=oneshot
ExecStart=/usr/local/sbin/$unit
TimeoutStartSec=300
[Install]
WantedBy=multi-user.target
UNIT
  ln -sf "../$unit.service" "$ROOT/etc/systemd/system/multi-user.target.wants/$unit.service"
done
chroot "$ROOT" restorecon -RF /usr/local/sbin /etc/systemd/system /etc/NetworkManager /etc/dracut.conf.d
# Remove installation credentials from Anaconda's generated copies.
rm -f "$ROOT/root/anaconda-ks.cfg" "$ROOT/root/original-ks.cfg"
rm -rf "$ROOT/var/log/anaconda"
curl -fsS --max-time 30 -H 'Content-Type: application/json' --data '{"attempt":__ATTEMPT__,"phase":"initializing","progress":90,"message":"RHEL 系安装与启动准备完成，正在验证首次启动"}' "${BASE}report"
