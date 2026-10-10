# Third-Party Notices

NexBoot is licensed under [GNU AGPLv3](./LICENSE). Third-party components distributed with NexBoot retain their respective licenses, as listed below.

## Standalone Programs Distributed with the Binary

The following files are packaged as standalone programs in `internal/boot/tftpassets/files/`. They are delivered to clients through TFTP and executed independently at runtime. They are not linked with NexBoot code and remain subject to their own licenses.

| File | Project | Version | License | Source |
|---|---|---|---|---|
| `undionly.kpxe` | [iPXE](https://ipxe.org) | Ubuntu 22.04 source package `1.21.1+git-20220113.fbbdc3926-0ubuntu1` | GPL-2.0-or-later (some files additionally offer UBDL) | Ubuntu 22.04 `apt-get source ipxe`; unmodified source. See [`scripts/build-undionly.sh`](scripts/build-undionly.sh) for build instructions (the script is not embedded). |
| `snponly.efi` | [iPXE](https://ipxe.org) | Same as above | GPL-2.0-or-later (some files additionally offer UBDL) | Same source, built as `bin-x86_64-efi/snponly.efi` |
| `wimboot` | [wimboot](https://ipxe.org/wimboot) | v2.8.0 | GPL-2.0-or-later | <https://github.com/ipxe/wimboot/tree/v2.8.0> |

Full license text for these components is available at <https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt>. To obtain the corresponding complete source for a distributed version, follow the sources above or contact the project maintainers.

## Go Dependencies Compiled into the Binary

| License | Modules |
|---|---|
| MIT | github.com/dustin/go-humanize, github.com/go-chi/chi/v5, github.com/golang-jwt/jwt/v5, github.com/jackc/pgpassfile, github.com/jackc/pgservicefile, github.com/jackc/pgx/v5, github.com/jackc/puddle/v2, github.com/mattn/go-isatty, github.com/mfridman/interpolate, github.com/ncruces/go-strftime, github.com/pressly/goose/v3, go.uber.org/multierr |
| BSD | github.com/google/uuid, github.com/remyoudompheng/bigfft, golang.org/x/sync, golang.org/x/sys, golang.org/x/text, modernc.org/libc, modernc.org/mathutil, modernc.org/memory, modernc.org/sqlite |
| Apache-2.0 | github.com/sethvargo/go-retry |

## Dependencies Bundled with the Frontend

| License | Packages |
|---|---|
| MIT | react, react-dom |

## System Programs Invoked at Runtime

dnsmasq, targetcli-fb, ZFS (zfsutils-linux), keepalived, and qemu-utils are installed through the operating system package manager. NexBoot invokes them at runtime but does not distribute them as part of this project.
