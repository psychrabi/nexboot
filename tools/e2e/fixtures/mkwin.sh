#!/bin/bash
# Build a minimal Windows-shaped image that passes import health checks for boot_matrix and backup_matrix.
# Blocking health checks require Windows/System32 and drivers/msiscsi.sys (see inspectWindows in
# internal/storage/local/inspect.go). NIC INF and Boot/BCD are warnings, included for a clean report.
# This image is not bootable. The test matrices do not require its contents. A sparse 1 GiB file consumes only a few MiB.
# Usage: sudo ./mkwin.sh /var/lib/ndiskless/imports/winmin.raw 1G
# Dependencies: parted, ntfs-3g (mkfs.ntfs), losetup
set -euo pipefail
OUT=${1:-/var/lib/ndiskless/imports/winmin.raw}
SIZE=${2:-1G}
[ "$(id -u)" -eq 0 ] || { echo "Root privileges are required for mount and losetup" >&2; exit 1; }
for c in parted mkfs.ntfs losetup; do
  command -v "$c" >/dev/null || { echo "Missing $c (install with apt install parted ntfs-3g)" >&2; exit 1; }
done

mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"
truncate -s "$SIZE" "$OUT"
parted -s "$OUT" mklabel msdos mkpart primary ntfs 1MiB 100%
LOOP=$(losetup --find --show --partscan "$OUT")
MNT=$(mktemp -d)
trap 'umount "$MNT" 2>/dev/null || true; losetup -d "$LOOP" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true' EXIT
sleep 1
mkfs.ntfs -Q -F -L WINMIN "${LOOP}p1" >/dev/null
mount -t ntfs-3g "${LOOP}p1" "$MNT"

mkdir -p "$MNT/Windows/System32/drivers"
mkdir -p "$MNT/Windows/System32/DriverStore/FileRepository/e1000.inf_amd64"
mkdir -p "$MNT/Boot"
printf 'test fixture, not a real driver\n' > "$MNT/Windows/System32/drivers/msiscsi.sys"
printf 'fixture\n' > "$MNT/Boot/BCD"
cat > "$MNT/Windows/System32/DriverStore/FileRepository/e1000.inf_amd64/e1000.inf" <<'INF'
[Version]
Signature="$Windows NT$"
Class=Net
ClassGUID={4d36e972-e325-11ce-bfc1-08002be10318}
Provider=%Fixture%
[Manufacturer]
%Fixture%=Fixture,NTamd64
[Fixture.NTamd64]
%E1000.DeviceDesc%=E1000.ndi,PCI\VEN_8086&DEV_100E
%E1000.DeviceDesc%=E1000.ndi,PCI\VEN_8086&DEV_10D3
[Strings]
Fixture="e2e fixture"
E1000.DeviceDesc="Intel(R) PRO/1000 (fixture)"
INF
sync
umount "$MNT"
losetup -d "$LOOP"
rmdir "$MNT"
trap - EXIT
echo "  Created $OUT  Apparent $(du -h --apparent-size "$OUT" | cut -f1)  Actual disk usage $(du -h "$OUT" | cut -f1)"
