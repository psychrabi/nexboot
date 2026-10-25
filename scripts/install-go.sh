#!/usr/bin/env bash
set -euo pipefail

APP=ndiskless
PREFIX=/opt/ndiskless
DATA_DIR=/var/lib/ndiskless
ENV_DIR=/etc/ndiskless
ENV_FILE=$ENV_DIR/ndiskless.env
SERVICE_FILE=/etc/systemd/system/ndiskless.service
DNSMASQ_CONF=/etc/dnsmasq.d/ndiskless.conf
DNSMASQ_BASE_CONF=/etc/dnsmasq.d/00-ndiskless-base.conf
IMPORT_DIR=$DATA_DIR/imports

INSTALL_DEPS=0
# --provisioned: the .deb already installed the binary and unit; ndiskless-configure only handles site configuration (env, pool, HA, startup).
# Default 0 installs everything from source/tarball.
PROVISIONED=0
BINARY=
POOL=tank
ADDR=:8080
BOOT_URL=
BOOTSTRAP_USER=admin
BOOTSTRAP_PASSWORD=
CREATE_POOL=
POOL_DISKS=
# HA options --peer/--vip/--cluster-token configure membership in an active/standby cluster; omit for standalone.
PEER_IP=
VIP=
CLUSTER_TOKEN=
NODE_ADDR=
JWT_SECRET=
CHAP_SECRET=
HA_ROLE=active
DEFER_KEEPALIVED=0

usage() {
  cat <<'EOF'
Usage:
  install-go.sh --binary ./ndiskless --bootstrap-password <password> [options]

Options:
  --install-deps                 Install Ubuntu/Debian runtime dependencies.
  --binary PATH                  ndiskless binary to install.
  --pool NAME                    ZFS pool used by ndiskless. Default: tank.
  --addr ADDR                    HTTP listen address. Default: :8080.
  --boot-url URL                 iPXE HTTP boot URL. Default: auto-detect from host IPv4 and --addr.
  --import-dir DIR               Server-side ZFS send file import directory. Default: /var/lib/ndiskless/imports.
  --bootstrap-user USER          Initial admin user. Default: admin.
  --bootstrap-password PASSWORD  Initial admin password. Required.
  --create-pool NAME             Create this ZFS pool if missing.
  --pool-disks CSV               Comma-separated disks for --create-pool.
  --chap-secret SECRET           iSCSI CHAP secret. Locks each client's disk
                                 behind a per-MAC login so no one else on the
                                 segment can attach it. Omit on a single node
                                 to auto-generate one; on a cluster it MUST be
                                 the same on every node (like --cluster-token),
                                 and omitting it there stays in demo mode.
  --defer-keepalived             Write keepalived's config but leave the daemon
                                 stopped, marking the join pending. The service
                                 starts it once the writer lists this node as a
                                 VRRP peer — starting earlier, before the master
                                 sends adverts here, elects a second master.
                                 Used by the UI join; not for hand installs.
  -h, --help                     Show this help.

Cluster options (a node is single-node unless --peer is given):
  --peer IP[,IP...]              Every OTHER node's real address (comma-separated),
                                 never the VIP. Enables HA and seeds keepalived's
                                 unicast peer list; the service keeps that list
                                 equal to the cluster roster afterwards, so nodes
                                 added later need no re-run here.
  --vip IP                       Virtual IP the clients and the API use. Giving
                                 it is what makes this node a cluster; --peer may
                                 be empty on the first node (no peers yet).
  --cluster-token TOKEN          Shared secret for the cluster channel; identical
                                 on every node. Omit it and one is generated (the
                                 first node) or the existing one kept (a re-run);
                                 pass it only to copy another node's value.
  --ha-role active|standby       Role this node starts in. Default: active.
                                 The FIRST node keeps active; every node that
                                 joins later must start as standby.
  --node-addr IP                 This node's OWN address (never the VIP) — what
                                 other nodes and clients placed on it dial.
                                 Required on every node once the cluster has
                                 more than one node.
  --jwt-secret HEX               Session signing key. Must be IDENTICAL on every
                                 node, otherwise a failover signs every operator
                                 out (the token one node minted does not verify
                                 on the next). Generated once on a fresh install
                                 and preserved across re-runs; pass it only to
                                 copy the first node's value onto the others.

Examples:
  # single node
  bash install-go.sh --install-deps --binary ./ndiskless --create-pool tank --pool-disks '/dev/sdb' --bootstrap-password 'secret'
  # second node of an HA pair (run on the new node first, then re-run on the old one)
  bash install-go.sh --install-deps --binary ./ndiskless --create-pool tank --pool-disks '/dev/sdb' \
      --bootstrap-password 'secret' --peer 192.168.10.3 --vip 192.168.10.250 \
      --cluster-token 'cluster-secret' --ha-role standby --node-addr 192.168.10.4
EOF
}

log() { printf '[INFO] %s\n' "$*"; }
warn() { printf '[WARN] %s\n' "$*" >&2; }
die() { printf '[ERROR] %s\n' "$*" >&2; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing command: $1"
}

random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
  fi
}

trim() {
  local value=$1
  value=${value#"${value%%[![:space:]]*}"}
  value=${value%"${value##*[![:space:]]}"}
  printf '%s' "$value"
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --install-deps) INSTALL_DEPS=1 ;;
      # Passed by the .deb wrapper ndiskless-configure: binary and service unit already exist; only configure this host.
      --provisioned) PROVISIONED=1 ;;
      --binary) shift; BINARY=${1:-} ;;
      --pool) shift; POOL=${1:-} ;;
      --addr) shift; ADDR=${1:-} ;;
      --boot-url) shift; BOOT_URL=${1:-} ;;
      --import-dir) shift; IMPORT_DIR=${1:-} ;;
      --bootstrap-user) shift; BOOTSTRAP_USER=${1:-} ;;
      --bootstrap-password) shift; BOOTSTRAP_PASSWORD=${1:-} ;;
      --create-pool) shift; CREATE_POOL=${1:-} ;;
      --pool-disks) shift; POOL_DISKS=${1:-} ;;
      # Multiple peers may be comma-separated; all nodes participate in VRRP. One peer creates a two-node configuration.
      --peer) shift; PEER_IP=${1:-} ;;
      --vip) shift; VIP=${1:-} ;;
      --cluster-token) shift; CLUSTER_TOKEN=${1:-} ;;
      --ha-role) shift; HA_ROLE=${1:-} ;;
      --node-addr) shift; NODE_ADDR=${1:-} ;;
      --jwt-secret) shift; JWT_SECRET=${1:-} ;;
      --chap-secret) shift; CHAP_SECRET=${1:-} ;;
      --defer-keepalived) DEFER_KEEPALIVED=1 ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown option: $1" ;;
    esac
    shift
  done
}

# is_ipv4 accepts four dotted decimal octets, each in 0..255.
is_ipv4() {
  local ip=$1 part parts
  [[ "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]] || return 1
  IFS=. read -r -a parts <<< "$ip"
  for part in "${parts[@]}"; do
    [ "$((10#$part))" -le 255 ] || return 1
  done
}

has_control_chars() {
  [[ "$1" =~ [[:cntrl:]] ]]
}

# validate_args must run before writing files. Invalid values, such as a VIP containing /24, must be rejected before keepalived setup
# or the host can restart as standby without keepalived and become impossible to adopt from the console.
validate_args() {
  local peer peers name_ok='^[A-Za-z][A-Za-z0-9_.:-]*$' token_ok='^[A-Za-z0-9._:+=@-]+$'
  if [ -n "$VIP" ]; then
    is_ipv4 "$VIP" || die "--vip must be a valid IPv4 address without mask or port, e.g. 192.168.10.250"
  fi
  if [ -n "$PEER_IP" ]; then
    has_control_chars "$PEER_IP" && die "--peer contains control characters. Separate multiple nodes by commas; specify bare addresses only"
    IFS=, read -r -a peers <<< "$PEER_IP"
    for peer in "${peers[@]}"; do
      peer=$(trim "$peer")
      [ -n "$peer" ] || continue
      is_ipv4 "$peer" || die "Invalid IPv4 address in --peer: ${peer}. Separate nodes by commas; specify bare addresses only"
    done
  fi
  if [ -n "$NODE_ADDR" ]; then
    is_ipv4 "$NODE_ADDR" || die "--node-addr must be the local IPv4 address on the client subnet"
  fi
  if [ -n "$POOL" ]; then
    [[ "$POOL" =~ $name_ok ]] || die "--pool name must start with a letter and contain only letters, digits, underscores, hyphens, dots and colons"
  fi
  if [ -n "$CREATE_POOL" ]; then
    [[ "$CREATE_POOL" =~ $name_ok ]] || die "--create-pool name must start with a letter and contain only letters, digits, underscores, hyphens, dots and colons"
  fi
  if [ -n "$CLUSTER_TOKEN" ]; then
    [[ "$CLUSTER_TOKEN" =~ $token_ok ]] || die "--cluster-token may contain only letters, digits and . _ - : + = @"
  fi
  has_control_chars "$JWT_SECRET" && die "--jwt-secret contains control characters; set another value"
  has_control_chars "$CHAP_SECRET" && die "--chap-secret contains control characters; set another value"
  has_control_chars "$BOOTSTRAP_PASSWORD" && die "--bootstrap-password contains control characters; set another value"
  case "$HA_ROLE" in
    active|standby) ;;
    *) die "--ha-role must be active or standby" ;;
  esac
  return 0
}

check_root_systemd() {
  [ "$(id -u)" -eq 0 ] || die "run as root"
  need_cmd systemctl
  [ -d /run/systemd/system ] || die "systemd is required"
}

check_debian_family() {
  [ -f /etc/os-release ] || die "/etc/os-release not found"
  . /etc/os-release
  case "${ID:-}" in
    ubuntu|debian) ;;
    *) die "unsupported OS: ${PRETTY_NAME:-unknown}; only Ubuntu/Debian is supported" ;;
  esac
}

install_deps() {
  check_debian_family
  need_cmd apt-get
  export DEBIAN_FRONTEND=noninteractive

  log "installing runtime packages"
  apt-get -o DPkg::Lock::Timeout=600 update
  # dnsmasq-utils provides dhcp_release to drop obsolete leases after client deletion, IP changes or group transfers.
  # Without it, dnsmasq may reject reserved addresses still leased elsewhere ("not using configured address X because it is leased to Y"),
  # even if the operation itself reports success.
  apt-get -o DPkg::Lock::Timeout=600 install -y curl dnsmasq dnsmasq-utils qemu-utils targetcli-fb zfsutils-linux

  if ! modprobe zfs >/dev/null 2>&1; then
    log "zfs module missing; installing dkms headers"
    apt-get -o DPkg::Lock::Timeout=600 install -y "linux-headers-$(uname -r)" zfs-dkms
    modprobe zfs
  fi

  modprobe target_core_mod
  modprobe iscsi_target_mod
  # ndiskless writes LIO configfs directly (without targetcli on boot); ensure kernel modules persist across reboot.
  printf 'target_core_mod\niscsi_target_mod\n' > /etc/modules-load.d/ndiskless.conf

  systemctl enable zfs-import.target >/dev/null 2>&1 || true
  systemctl start zfs-import.target >/dev/null 2>&1 || true
  install_dnsmasq_base_config
  systemctl enable --now dnsmasq
}

install_dnsmasq_base_config() {
  install -d -m 0755 "$(dirname "$DNSMASQ_BASE_CONF")"
  cat > "$DNSMASQ_BASE_CONF" <<'EOF'
# managed by ndiskless install-go.sh
# Keep Ubuntu's systemd-resolved on port 53; ndiskless uses dnsmasq for DHCP/TFTP.
port=0
EOF
}

check_runtime_commands() {
  need_cmd zfs
  need_cmd zpool
  need_cmd qemu-img
  need_cmd targetcli
  need_cmd dnsmasq
  zfs version >/dev/null 2>&1 || die "zfs is not usable"
  qemu-img --version >/dev/null 2>&1 || die "qemu-img is not usable"
  targetcli ls >/dev/null 2>&1 || die "targetcli is not usable"
  dnsmasq --version >/dev/null 2>&1 || die "dnsmasq is not usable"
}

ensure_pool() {
  if [ -n "$CREATE_POOL" ]; then
    POOL=$CREATE_POOL
    if zpool list "$POOL" >/dev/null 2>&1; then
      log "pool exists: $POOL"
      return
    fi
    [ -n "$POOL_DISKS" ] || die "--pool-disks is required with --create-pool"

    local disks=()
    local disk
    IFS=',' read -r -a disks <<< "$POOL_DISKS"
    [ "${#disks[@]}" -gt 0 ] || die "--pool-disks is empty"
    for i in "${!disks[@]}"; do
      disk=$(trim "${disks[$i]}")
      [ -n "$disk" ] || die "empty disk in --pool-disks"
      [ -b "$disk" ] || die "not a block device: $disk"
      disks[$i]=$disk
    done

    log "creating pool $POOL"
    zpool create "$POOL" "${disks[@]}"
    return
  fi

  if ! zpool list "$POOL" >/dev/null 2>&1; then
    warn "ZFS pool not found: $POOL; create it in the UI or rerun with --create-pool and --pool-disks"
  fi
}

check_port() {
  local port=${ADDR##*:}
  [[ "$port" =~ ^[0-9]+$ ]] || return 0
  if systemctl is-active --quiet "$APP" 2>/dev/null; then
    return 0
  fi
  if command -v ss >/dev/null 2>&1 && ss -ltn | awk '{print $4}' | grep -Eq "[:.]$port$"; then
    die "TCP port already in use: $port"
  fi
}

detect_boot_url() {
  [ -z "$BOOT_URL" ] || return 0
  local port host
  port=${ADDR##*:}
  [[ "$port" =~ ^[0-9]+$ ]] || die "--boot-url is required when --addr has no TCP port"
  host=${ADDR%:*}
  host=${host#\[}
  host=${host%\]}
  if [ -z "$host" ] || [ "$host" = "0.0.0.0" ] || [ "$host" = "::" ]; then
    if command -v ip >/dev/null 2>&1; then
      host=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1; i<=NF; i++) if ($i=="src") {print $(i+1); exit}}')
    fi
    if [ -z "$host" ]; then
      host=$(hostname -I 2>/dev/null | awk '{print $1}')
    fi
  fi
  [ -n "$host" ] || die "could not auto-detect boot URL host; pass --boot-url"
  BOOT_URL="http://$host:$port/boot?mac=\${net0/mac}"
}

# ensure_dirs creates directories required by write_env and services. Provisioned installs skip install_binary,
# so ensure /etc/ndiskless exists. This step is idempotent.
ensure_dirs() {
  install -d -m 0755 "$DATA_DIR" "$ENV_DIR" "$IMPORT_DIR"
}

# prepare_runtime performs host setup independent of apt: load/persist LIO modules, enable pool auto-import,
# and write base dnsmasq configuration (keeping :53 for systemd-resolved). Run on every install, including .deb without --install-deps.
# Idempotent; module load errors warn only because controller-only nodes need not host iSCSI.
prepare_runtime() {
  modprobe target_core_mod 2>/dev/null || warn "could not load target_core_mod; load it before serving iSCSI clients"
  modprobe iscsi_target_mod 2>/dev/null || true
  printf 'target_core_mod\niscsi_target_mod\n' > /etc/modules-load.d/ndiskless.conf
  systemctl enable zfs-import.target >/dev/null 2>&1 || true
  systemctl start zfs-import.target >/dev/null 2>&1 || true
  install_dnsmasq_base_config
}

install_binary() {
  [ -n "$BINARY" ] || die "--binary is required"
  [ -f "$BINARY" ] || die "binary not found: $BINARY"
  [ -n "$IMPORT_DIR" ] || die "--import-dir is required"
  [[ "$IMPORT_DIR" = /* ]] || die "--import-dir must be absolute"
  install -d -m 0755 "$PREFIX" "$DATA_DIR" "$ENV_DIR" "$IMPORT_DIR"
  if [ -f "$PREFIX/$APP" ]; then
    cp -a "$PREFIX/$APP" "$PREFIX/$APP.$(date +%Y%m%d%H%M%S).bak"
  fi
  install -m 0755 "$BINARY" "$PREFIX/$APP"
}

# Environment keys managed by this script. Preserve other operator-managed keys on reinstall.
MANAGED_ENV_KEYS='^NDISKLESS_(ADDR|DB_DSN|POOL|IMPORT_DIR|DNSMASQ_CONF|BOOT_URL|BOOTSTRAP_USERNAME|BOOTSTRAP_PASSWORD|JWT_SECRET|CHAP_SECRET|ROLE|PEER_URL|CLUSTER_TOKEN|PORTAL_ADDR|NODE_ADDR)='

# resolve_chap_secret determines this host's iSCSI CHAP secret (empty means demo mode). Different node secrets prevent
# clients from authenticating after failover, so follow this precedence:
#   --chap-secret provided     Use it; operator ensures cluster-wide consistency.
#   Existing env secret        Reuse it across upgrades/reconfiguration.
#   Standalone without secret  Generate one.
#   Cluster peer without it   Leave blank; randomly generated per-node secrets would diverge.
resolve_chap_secret() {
  if [ -n "$CHAP_SECRET" ]; then
    printf '%s' "$CHAP_SECRET"
    return
  fi
  if [ -f "$ENV_FILE" ] && grep -q '^NDISKLESS_CHAP_SECRET=' "$ENV_FILE"; then
    sed -n 's/^NDISKLESS_CHAP_SECRET=//p' "$ENV_FILE" | head -1
    return
  fi
  if [ -z "$VIP" ]; then
    random_hex
    return
  fi
  printf ''
}

# Cluster token: use an explicit value, otherwise reuse env across upgrades; generate only if neither exists
# (on the first cluster node). Adopted nodes receive the active node's token through the UI.
# Resolve it before write_env to prevent an empty stored token that breaks node authentication.
resolve_cluster_token() {
  [ -n "$VIP" ] || return 0
  if [ -z "$CLUSTER_TOKEN" ] && [ -f "$ENV_FILE" ] && grep -q "^NDISKLESS_CLUSTER_TOKEN=" "$ENV_FILE"; then
    CLUSTER_TOKEN=$(sed -n "s/^NDISKLESS_CLUSTER_TOKEN=//p" "$ENV_FILE" | head -1)
    [ -n "$CLUSTER_TOKEN" ] && log "keeping the existing cluster token"
  fi
  if [ -z "$CLUSTER_TOKEN" ]; then
    CLUSTER_TOKEN=$(random_hex)
    log "generated a cluster token for this new cluster"
  fi
}

write_env() {
  # Reconfiguration and upgrades preserve the administrator password unless explicitly overridden.
  local bootstrap_password
  bootstrap_password=$BOOTSTRAP_PASSWORD
  if [ -z "$bootstrap_password" ] && [ -f "$ENV_FILE" ] && grep -q '^NDISKLESS_BOOTSTRAP_PASSWORD=' "$ENV_FILE"; then
    bootstrap_password=$(sed -n 's/^NDISKLESS_BOOTSTRAP_PASSWORD=//p' "$ENV_FILE" | head -1)
    log "keeping the existing admin password"
  fi
  [ -n "$bootstrap_password" ] || die "--bootstrap-password is required"
  local jwt_secret preserved peer_url
  # Preserve the signing key across installs to keep sessions valid and permit cross-node token verification.
  # Priority: --jwt-secret, then existing env value, then random generation.
  if [ -n "$JWT_SECRET" ]; then
    jwt_secret=$JWT_SECRET
  elif [ -f "$ENV_FILE" ] && grep -q '^NDISKLESS_JWT_SECRET=' "$ENV_FILE"; then
    jwt_secret=$(sed -n 's/^NDISKLESS_JWT_SECRET=//p' "$ENV_FILE" | head -1)
    log "keeping the existing session signing key"
  else
    jwt_secret=$(random_hex)
  fi
  local chap_secret
  chap_secret=$(resolve_chap_secret)
  # Retain operator-edited keys such as replication rate and driver directory.
  preserved=
  if [ -f "$ENV_FILE" ]; then
    preserved=$(grep -vE "$MANAGED_ENV_KEYS" "$ENV_FILE" | grep -vE '^\s*(#|$)' || true)
  fi
  umask 077
  cat > "$ENV_FILE" <<EOF
NDISKLESS_ADDR=$ADDR
NDISKLESS_DB_DSN=file:$DATA_DIR/ndiskless.db
NDISKLESS_POOL=$POOL
NDISKLESS_IMPORT_DIR=$IMPORT_DIR
NDISKLESS_DNSMASQ_CONF=$DNSMASQ_CONF
NDISKLESS_BOOT_URL=$BOOT_URL
NDISKLESS_BOOTSTRAP_USERNAME=$BOOTSTRAP_USER
NDISKLESS_BOOTSTRAP_PASSWORD=$bootstrap_password
NDISKLESS_JWT_SECRET=$jwt_secret
NDISKLESS_CHAP_SECRET=$chap_secret
EOF
  if [ -n "$VIP" ]; then
    # With no peer, VIP is the rendezvous address and this node is the writer; this also prevents duplicate cluster creation.
    peer_url="http://${VIP}:${ADDR##*:}"
    [ -z "$PEER_IP" ] || peer_url="http://${PEER_IP%%,*}:${ADDR##*:}"
    cat >> "$ENV_FILE" <<EOF
NDISKLESS_ROLE=$HA_ROLE
NDISKLESS_PEER_URL=$peer_url
NDISKLESS_CLUSTER_TOKEN=$CLUSTER_TOKEN
NDISKLESS_PORTAL_ADDR=$VIP
EOF
    # NODE_ADDR is the host's own reachable IP for peers and clients, not VIP; required in multi-node clusters.
    if [ -n "${NODE_ADDR:-}" ]; then
      echo "NDISKLESS_NODE_ADDR=$NODE_ADDR" >> "$ENV_FILE"
    fi
    printf "%s" "$CLUSTER_TOKEN" > /etc/ndiskless/cluster-token
    chmod 600 /etc/ndiskless/cluster-token
  fi
  if [ -n "$preserved" ]; then
    printf '%s\n' "$preserved" >> "$ENV_FILE"
  fi
  # Report CHAP status explicitly because it controls who can mount client disks on the subnet.
  if [ -z "$chap_secret" ]; then
    warn "iSCSI CHAP off (demo mode): any host on the client network that guesses a target IQN can attach a client's disk. Pass --chap-secret (the SAME value on every node) to turn on per-client auth."
  elif [ -n "$CHAP_SECRET" ]; then
    log "iSCSI CHAP on (secret from --chap-secret)."
  elif [ -z "$VIP" ]; then
    log "iSCSI CHAP on (generated a secret for this single node). Nodes added from the console (Server Management → Add Node) receive it automatically; only a command-line join needs --chap-secret with this node's NDISKLESS_CHAP_SECRET from $ENV_FILE."
  else
    log "iSCSI CHAP on (kept the existing secret)."
  fi
}

# At startup, the persisted role marker overrides env defaults; a demoted host may otherwise start as standby even while holding VIP.
# An explicit --ha-role updates the marker while preserving epoch, the fence against stale active nodes.
seed_role_marker() {
  [ -n "$VIP" ] || return 0
  local file="$DATA_DIR/ha-role" epoch=0
  if [ -f "$file" ]; then
    # Use [0-9][0-9]* instead of [0-9]\+: BSD sed may not support \+, which can accidentally reset epoch to 0.
    epoch=$(sed -n 's/.*"epoch"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$file" | head -1)
    [ -n "$epoch" ] || epoch=0
  fi
  printf '{"role":"%s","epoch":%s}\n' "$HA_ROLE" "$epoch" > "$file"
  log "role marker: $HA_ROLE (epoch $epoch)"
}

# nd_deploy_dir locates deploy assets beside scripts for source builds or under /opt/ndiskless/deploy for .deb installs.
nd_deploy_dir() {
  local d
  d=$(cd "$(dirname "$0")/../deploy" 2>/dev/null && pwd) && { printf '%s' "$d"; return; }
  [ -d /opt/ndiskless/deploy ] && { printf '/opt/ndiskless/deploy'; return; }
  die "deploy/ assets not found (looked next to scripts/ and in /opt/ndiskless/deploy)"
}

# write_service installs the single authoritative unit from deploy instead of maintaining an inline duplicate
# that might miss ordering dependencies such as zfs-import.target/zfs.target.
write_service() {
  install -m 0644 "$(nd_deploy_dir)/ndiskless.service" "$SERVICE_FILE"
}

start_service() {
  systemctl daemon-reload
  systemctl enable "$APP"
  if systemctl is-active --quiet "$APP"; then
    systemctl restart "$APP"
  else
    systemctl start "$APP"
  fi
}

health_check() {
  local port=${ADDR##*:}
  [[ "$port" =~ ^[0-9]+$ ]] || { warn "skip health check for non-port addr: $ADDR"; return 0; }
  if ! command -v curl >/dev/null 2>&1; then
    warn "curl missing; skipped HTTP health check"
    return 0
  fi

  # /healthz reports VIP eligibility; a newly installed standby can correctly return 503.
  # Installation only requires a listening service; print details for non-200 responses.
  local code body
  for _ in $(seq 1 20); do
    # Keep || true because curl exit 7 under set -e would silently stop installation before the service starts.
    code=$(curl -s -o /dev/null -w '%{http_code}' -m 3 "http://127.0.0.1:$port/healthz" 2>/dev/null || true)
    if [ "$code" = "200" ]; then
      log "health check ok: http://127.0.0.1:$port/healthz"
      return 0
    fi
    if [ -n "$code" ] && [ "$code" != "000" ]; then
      body=$(curl -s -m 3 "http://127.0.0.1:$port/healthz" 2>/dev/null || true)
      log "service is up (healthz $code): $body"
      return 0
    fi
    sleep 1
  done
  systemctl status "$APP" --no-pager -l || true
  die "health check failed"
}

# Before creating a cluster (--vip without --peer), ensure VIP is unclaimed; two clusters with the same VRRP group and VIP
# may reject each other's advertisements and both become MASTER with the same address.
# In that case, leave this host unconfigured and adopt it through the existing cluster's console.
refuse_if_vip_taken() {
  [ -n "$VIP" ] || return 0
  [ -z "$PEER_IP" ] || return 0   # An explicit peer means joining an existing cluster, not creating one.
  command -v curl >/dev/null 2>&1 || return 0
  local port=${ADDR##*:} body
  body=$(curl -s --max-time 3 "http://$VIP:$port/internal/cluster/identity" 2>/dev/null) || return 0
  [ -n "$body" ] || return 0
  die "Virtual IP $VIP is already owned by a cluster (responded at $VIP:$port). Do not create a new cluster here —
     Leave this host in its factory state (omit --vip), then use Add Node in the existing cluster console.
     If that cluster is decommissioned, stop it first or choose another virtual IP."
}

setup_keepalived() {
  # Cluster mode is selected by --vip, not --peer; the first node has no peers and initially becomes MASTER.
  # The service populates additional peers from the cluster roster as they join.
  [ -n "$VIP" ] || return 0
# standby_rank assigns an ordered index to each node IP to give standbys distinct VRRP priorities:
# lowest address gets 100 and others increment, remaining below the active node's 150.
standby_rank() {
  local self=$1 peers=${2:-} rank
  rank=$(printf '%s\n%s\n' "$self" "$(printf '%s' "$peers" | tr ',' '\n')" \
         | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' \
         | awk 'NF' \
         | sort -u -t. -k1,1n -k2,2n -k3,3n -k4,4n \
         | grep -n -x -F "$self" | head -1 | cut -d: -f1)
  echo $(( ${rank:-1} - 1 ))
}

  apt-get -o DPkg::Lock::Timeout=600 install -y keepalived >/dev/null
  local iface self_ip port deploy_dir
  port=${ADDR##*:}
  deploy_dir=$(cd "$(dirname "$0")/../deploy" 2>/dev/null && pwd) || die "deploy/ directory not found next to scripts/"
  # The client-facing network interface is the one in the VIP's /24 subnet.
  iface=$(ip -o -4 addr show | awk -v vip="$VIP" '{ split($4, a, "/"); split(a[1], ip, "."); split(vip, v, "."); if (ip[1]==v[1] && ip[2]==v[2] && ip[3]==v[3]) { print $2; exit } }')
  [ -n "$iface" ] || die "no interface on the VIP's /24 ($VIP); bring the client NIC up first"
  self_ip=$(ip -o -4 addr show dev "$iface" | awk '{split($4,a,"/"); print a[1]; exit}')

  # The node holding the real catalogue must win election so a new node's empty catalogue cannot overwrite images.
  # Rank standby priorities by address instead of setting them all to 100. Equal priorities in a three-node cluster
  # can cause two standbys to become MASTER at the same epoch.
  local priority=100
  if [ "$HA_ROLE" = "active" ]; then
    priority=150
  else
    priority=$((100 + $(standby_rank "$self_ip" "$PEER_IP")))
  fi
  # Add one line per peer so all nodes participate in VRRP and any standby can take over.
  local peer_block
  peer_block=$(printf '%s' "$PEER_IP" | tr ',' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' \
               | awk 'NF { lines[++n] = "        " $0 } END { for (i = 1; i <= n; i++) printf "%s%s", lines[i], (i < n ? "\\n" : "") }')
  sed -e "s/@IFACE@/$iface/" -e "s/@VIP@/$VIP/" -e "s/@SELF_IP@/$self_ip/" \
      -e "s/@AUTH_PASS@/$(printf %.8s "$CLUSTER_TOKEN")/" \
      -e "s/@PRIORITY@/$priority/" \
      "$deploy_dir/keepalived.conf.tmpl" \
    | sed -e "s|^ *@PEER_IP@ *$|$peer_block|" > /etc/keepalived/keepalived.conf
  sed "s/@API_PORT@/$port/" "$deploy_dir/ndiskless-check.sh" > /etc/keepalived/ndiskless-check.sh
  sed "s/@API_PORT@/$port/" "$deploy_dir/ndiskless-notify.sh" > /etc/keepalived/ndiskless-notify.sh
  chmod 755 /etc/keepalived/ndiskless-check.sh /etc/keepalived/ndiskless-notify.sh

  # Competing authoritative DHCP services can NAK each other's clients; only the active node should start dnsmasq.
  systemctl disable dnsmasq >/dev/null 2>&1 || true
  if [ "$DEFER_KEEPALIVED" -eq 1 ]; then
    # During UI adoption, this node may not yet be in the writer's peer list. Avoid self-election by deferring VRRP
    # until the active node recognizes it, then let ndiskless start keepalived.
    systemctl disable --now keepalived >/dev/null 2>&1 || true
    touch "$ENV_DIR/keepalived.pending"
    log "keepalived configured but not started (join pending): the service starts it once the writer lists this node"
  else
    systemctl enable --now keepalived
  fi
  log "keepalived configured: vip=$VIP iface=$iface peers=$PEER_IP role=$HA_ROLE priority=$priority"
}

main() {
  parse_args "$@"
  validate_args
  check_root_systemd
  [ "$INSTALL_DEPS" -eq 0 ] || install_deps
  check_runtime_commands
  ensure_pool
  check_port
  detect_boot_url
  # TFTP boot assets are embedded in the binary and written to /srv/tftp at startup.
  # --provisioned skips binary and unit installation.
  if [ "$PROVISIONED" -eq 0 ]; then
    install_binary
  fi
  # Check VIP ownership before writing anything; otherwise partial env configuration can leave this host outside all clusters
  # and no longer adoptable as a factory-state node.
  refuse_if_vip_taken
  ensure_dirs
  prepare_runtime
  resolve_cluster_token
  write_env
  seed_role_marker
  if [ "$PROVISIONED" -eq 0 ]; then
    write_service
  fi
  setup_keepalived
  start_service
  health_check

  log "installed $APP"
  local port=${ADDR##*:}
  if [[ "$port" =~ ^[0-9]+$ ]]; then
    log "open http://<server-ip>:$port"
  fi
  # Offline bundles are produced separately when required.
}

main "$@"
