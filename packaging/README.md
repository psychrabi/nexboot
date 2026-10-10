# Packaging

Package the static binary into a `.deb`, then package it with its dependencies for offline installation.

## Commands

```sh
make deb        # dist/ndiskless_<version>_amd64.deb
make offline    # dist/nexboot-offline-ubuntu24.04-amd64.tar.gz (deb + 47 dependencies + install.sh)
```

The version comes from `git describe` (as with `--version`). For Debian versions, the `-N-g<hash>` suffix after the tag is converted to `~N-g<hash>` so development builds sort below the corresponding release tag.

## Two Requirements

1. **Build on Linux.** `dpkg-deb` is available on Debian/Ubuntu, not on the local macOS host. Build on Ubuntu or in CI.
2. **Run `make offline` on an internet-connected Ubuntu system matching the target release.** Dependency `.deb` packages are release-specific: Ubuntu 24.04 packages cannot simply be installed on 22.04. The build machine can be on an office network rather than the deployment network. If an APT mirror lacks a required package (for example, because security updates are not synchronized), `offline-bundle.sh` explicitly reports the missing package and marks the bundle incomplete instead of silently creating a broken installer.

## Package Contents and Responsibilities

The Debian package installs **files only**:

```text
/opt/ndiskless/ndiskless               Executable
/opt/ndiskless/scripts/install-go.sh   Configuration logic
/opt/ndiskless/deploy/*                Unit source and keepalived templates/scripts
/etc/systemd/system/ndiskless.service  Canonical unit from deploy/
/usr/sbin/ndiskless-configure          Wrapper for install-go.sh --provisioned
```

The package does **not** select a storage pool, write environment configuration, or start the service. Those decisions belong to `ndiskless-configure` at deployment. `postinst` only runs `daemon-reload`; `prerm` stops and disables the service during removal.

`Depends:` lists runtime dependencies (dnsmasq, qemu-utils, targetcli-fb, zfsutils-linux, keepalived, curl). On Ubuntu, the ZFS kernel module is provided with the distribution's ZFS packages without a separate DKMS build.

## Verification Status

On an Ubuntu 24.04 test machine (`.56`), the following were verified: Debian package build, package structure (eight expected file paths), embedded version, `Depends`, and acquisition of the full 47-package dependency closure by the offline bundler, including explicit failure messages for unavailable packages.

**Not yet verified end to end on physical hardware:** `dpkg -i` → `ndiskless-configure` → service startup. This lifecycle modifies machine state (`prerm` stops the service on removal); use a clean test VM or `topology_suite` instead of a manually configured verification host.
