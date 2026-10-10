#!/bin/bash
# Package the previously built static binary as an ndiskless .deb.
# The package installs files only. Selecting a pool, writing env, and starting services require deployment choices
# handled by ndiskless-configure (install-go.sh --provisioned). postinst must not start a unit without env.
#
# Use the same directory layout as install-go.sh, source, and tarball installations:
#   /opt/ndiskless/ndiskless              the binary
#   /opt/ndiskless/scripts/install-go.sh  the configure logic (also as a wrapper)
#   /opt/ndiskless/deploy/*               unit source + keepalived template/scripts
#   /etc/systemd/system/ndiskless.service the canonical unit (from deploy/)
#   /usr/sbin/ndiskless-configure         wrapper → install-go.sh --provisioned
#
# Usage: VERSION=<v> BINARY=<path> packaging/build-deb.sh [outdir]
# Requires dpkg-deb (dpkg-dev).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
version=${VERSION:?set VERSION (e.g. from git describe)}
binary=${BINARY:-$repo/bin/ndiskless-linux-amd64}
outdir=${1:-$repo/dist}
arch=amd64

[ -f "$binary" ] || { echo "binary not found: $binary (run: make build-static)" >&2; exit 1; }
command -v dpkg-deb >/dev/null || { echo "dpkg-deb missing (apt-get install dpkg-dev)" >&2; exit 1; }

# Convert to Debian version ordering: strip leading v and replace the post-tag - with ~
# so 0.1.0-6-gabc sorts below the official 0.1.0 release.
debver=${version#v}
debver=$(printf '%s' "$debver" | sed -E 's/-([0-9]+-g[0-9a-f]+(-dirty)?)$/~\1/')

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

install -d -m 0755 "$stage/opt/ndiskless" "$stage/opt/ndiskless/scripts" \
  "$stage/opt/ndiskless/deploy" "$stage/etc/systemd/system" \
  "$stage/usr/sbin" "$stage/DEBIAN"

install -m 0755 "$binary" "$stage/opt/ndiskless/ndiskless"
install -m 0755 "$repo/scripts/install-go.sh" "$stage/opt/ndiskless/scripts/install-go.sh"
install -m 0644 "$repo/deploy/ndiskless.service" "$stage/opt/ndiskless/deploy/ndiskless.service"
install -m 0644 "$repo/deploy/keepalived.conf.tmpl" "$stage/opt/ndiskless/deploy/keepalived.conf.tmpl"
install -m 0755 "$repo/deploy/ndiskless-check.sh" "$stage/opt/ndiskless/deploy/ndiskless-check.sh"
install -m 0755 "$repo/deploy/ndiskless-notify.sh" "$stage/opt/ndiskless/deploy/ndiskless-notify.sh"
# Install the unit file directly; postinst performs daemon-reload only.
install -m 0644 "$repo/deploy/ndiskless.service" "$stage/etc/systemd/system/ndiskless.service"

# ndiskless-configure is a thin wrapper so operators invoke a command rather than a path.
cat > "$stage/usr/sbin/ndiskless-configure" <<'WRAP'
#!/bin/sh
# Configure this node after the package is installed: pick the pool, write env,
# set up HA, start the service. The binary and unit are already in place, so run
# the installer in --provisioned mode. All install-go.sh flags pass through.
exec /opt/ndiskless/scripts/install-go.sh --provisioned "$@"
WRAP
chmod 0755 "$stage/usr/sbin/ndiskless-configure"

# zfsutils-linux supplies the Ubuntu prebuilt kernel module; DKMS and kernel headers are unnecessary.
cat > "$stage/DEBIAN/control" <<EOF
Package: ndiskless
Version: $debver
Architecture: $arch
Maintainer: NexBoot <noreply@nexboot.local>
Depends: dnsmasq, dnsmasq-utils, qemu-utils, targetcli-fb, zfsutils-linux, keepalived, curl
Section: admin
Priority: optional
Description: NexBoot diskless boot control plane
 Single-binary control plane for diskless (PXE/iSCSI) client boot: image
 management, per-client clones, DHCP/TFTP, and an optional active/standby HA
 pair. After install, run ndiskless-configure to pick a pool and start.
EOF

install -m 0755 "$here/postinst" "$stage/DEBIAN/postinst"
install -m 0755 "$here/prerm" "$stage/DEBIAN/prerm"

install -d -m 0755 "$outdir"
out="$outdir/ndiskless_${debver}_${arch}.deb"
dpkg-deb --build --root-owner-group "$stage" "$out"
echo "$out"
