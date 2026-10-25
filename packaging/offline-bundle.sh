#!/bin/bash
# Create an offline installation bundle with ndiskless .deb and all dependency packages for isolated PXE/iSCSI networks.
# Build on the same Ubuntu release as the target (24.04); only the packaging host needs Internet access.
#
# Usage: DEB=<path-to-ndiskless.deb> packaging/offline-bundle.sh [outdir]
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
deb=${DEB:?set DEB to the ndiskless .deb (from make deb)}
outdir=${1:-$repo/dist}
[ -f "$deb" ] || { echo "deb not found: $deb" >&2; exit 1; }
command -v apt-get >/dev/null || { echo "apt-get missing (build this on Ubuntu)" >&2; exit 1; }

. /etc/os-release
rel=${VERSION_ID:-unknown}
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/packages/deps"
cp "$deb" "$stage/packages/"

# Download every Depends package and its transitive dependencies.
deps=$(dpkg-deb -f "$deb" Depends | tr ',' '\n' | sed -E 's/\|.*//; s/\(.*\)//; s/^[[:space:]]*//; s/[[:space:]]*$//' | awk 'NF')
echo "resolving dependencies for: $(echo "$deps" | tr '\n' ' ')"
( cd "$stage/packages/deps"
  # --reinstall also downloads installed packages, ensuring the target needs no package mirror.
  apt-get install --yes --download-only --reinstall \
    -o Dir::Cache::archives="$(pwd)" $deps >/dev/null 2>&1 || true
  apt-get download $(apt-cache depends --recurse --no-recommends --no-suggests \
    --no-conflicts --no-breaks --no-replaces --no-enhances $deps \
    | grep '^\w' | sort -u) 2>/dev/null || true
  rm -f lock partial/* 2>/dev/null || true; rmdir partial 2>/dev/null || true )

count=$(find "$stage/packages/deps" -name '*.deb' | wc -l | tr -d ' ')
echo "bundled $count dependency package(s)"

cat > "$stage/install.sh" <<'INSTALL'
#!/bin/bash
# Offline install entry point. Run on the target (isolated segment, no internet).
#   1. installs every bundled .deb (deps first, then ndiskless)
#   2. hands you the configure command to run next
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
echo "installing bundled packages (offline)…"
dpkg -i "$here"/packages/deps/*.deb 2>/dev/null || true   # ordering resolved by the retry
dpkg -i "$here"/packages/deps/*.deb                        # second pass settles deps
dpkg -i "$here"/packages/*.deb
echo
echo "installed. now configure this node:"
echo "  sudo ndiskless-configure --bootstrap-password '<password>' \\"
echo "         --create-pool tank --pool-disks /dev/sdX"
echo "  (HA pair: add --peer <ip> --vip <ip> --cluster-token <s> --chap-secret <s>)"
INSTALL
chmod 0755 "$stage/install.sh"

cat > "$stage/INSTALLATION.txt" <<CARD
NexBoot Offline Installation (Ubuntu $rel / amd64)

Prerequisite: this machine is on a dedicated management/PXE network; Internet access is not required.

1) Extract the bundle, enter its directory, and install:
     sudo ./install.sh
2) Configure this host (select disks and set the administrator password):
     sudo ndiskless-configure --bootstrap-password 'your-password' \\
            --create-pool tank --pool-disks /dev/sdX
   For HA, also add: --peer PEER_IP --vip VIRTUAL_IP \\
            --cluster-token SHARED_TOKEN --chap-secret SHARED_SECRET
3) Open http://HOST_IP:8080 in your browser

Upgrade: install the new package on standby first, reboot and verify takeover before upgrading the active host.
CARD

install -d -m 0755 "$outdir"
out="$outdir/nexboot-offline-ubuntu${rel}-amd64.tar.gz"
tar -C "$stage" -czf "$out" .
echo "$out"
