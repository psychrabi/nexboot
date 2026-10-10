#!/usr/bin/env bash
# Rebuild internal/boot/tftpassets/files/undionly.kpxe using Ubuntu 22.04 iPXE source and configuration.
# Do not embed a script because that would ignore the boot address supplied through DHCP. Match the snponly.efi version.
set -euo pipefail
out="$(cd "$(dirname "$0")/.." && pwd)/internal/boot/tftpassets/files/undionly.kpxe"
docker run --rm --platform linux/arm64 ubuntu:22.04 bash -c '
set -e
{ sed -n "s/^deb /deb-src /p" /etc/apt/sources.list >> /etc/apt/sources.list
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq dpkg-dev make perl liblzma-dev gcc gcc-x86-64-linux-gnu binutils-x86-64-linux-gnu
  cd /tmp && apt-get source -qq ipxe && cd ipxe-*/
  cp debian/config/* src/config/local/ && echo "#define DOWNLOAD_PROTO_HTTPS" >> src/config/local/general.h
  cd src && make -j6 CROSS=x86_64-linux-gnu- NO_WERROR=1 VERSION="$(cd .. && dpkg-parsechangelog -S Version)" bin-i386-pcbios/undionly.kpxe
} >&2
cat /tmp/ipxe-*/src/bin-i386-pcbios/undionly.kpxe' > "$out.new"
mv "$out.new" "$out"
ls -l "$out"
