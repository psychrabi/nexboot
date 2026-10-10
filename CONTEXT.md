# NexBoot Diskless Boot Terminology

Diskless clients boot through iPXE, mounting their system and data disks from an image server. The current architecture uses a Go monolith for boot provisioning, configuration management, and storage operations, with an embedded React web console. Legacy names such as `Nd*` and PNP below are retained for interpreting older implementations.

## Terminology

**System Disk**: The client's boot disk, attached using `sanhook --drive 0x80` and ultimately booted by `sanboot`. Every client has exactly one system disk.

_Avoid_: calling it simply the boot volume where this could be confused with other boot assets.

**Data Disk**: An additional disk configured for a group (`NdGroupDisk`). Each data disk has a **drive letter** (`mountPoint`, such as `D:` or `E:`) and an image. A group may have zero or more data disks.

_Avoid_: ambiguous terms such as extra disk.

**Driver Disk** (legacy): The destination for installing PnP drivers. This is **not a separate physical disk**: its drive letter (`driverDiskLetter`) must match an existing data disk. It identifies the data disk onto which drivers are installed. Historically a group could configure at most one driver disk, specified by a drive letter and `driverDownloadUrl`.

_Avoid_: standalone driver disk. This legacy feature has been removed from the newer architecture; see Known Ambiguities.

**Drive Letter**: A Windows drive letter such as `D` or `E`. `normalizeDiskLetter` removes colons and spaces and converts it to uppercase. Data disk letters are stored in `NdGroupDisk.mountPoint`; the legacy driver disk letter is in `NdGroup.driverDiskLetter`.

**Super Admin Machine**: A client with `terminalSuper == 1` (identified by `Configurations.isClientSuper(mac)`). It acts as a **golden-image/template client**: its disk changes are persisted back to the parent image through `superPrepareForClone`. Ordinary clients use **temporary clones** that are discarded after use. In the legacy PnP workflow, the template client committed installed drivers and ordinary clients consumed them; their iPXE configurations were mutually exclusive.

_Avoid_: management machine.

## Storage Model

**Image**: The imported, read-only base system disk, root of the copy-on-write (CoW) chain and owner of the initial restore point. **Clients do not boot directly from it.**

_Avoid_: master disk in formal documentation; prefer image.

**Configuration (Config)**: A clone or branch of an image snapshot. An image can contain multiple configurations. Importing an image automatically creates a configuration named `default`. **Every client boots from a configuration.**

_Avoid_: branch as a user-facing name.

**Restore Point (Reduction)**: A bootable saved state represented by a snapshot of a configuration. Restore points can be created, deleted, or merged. Legacy entity: `NdReduction`.

_Avoid_: snapshot (the ZFS implementation detail), or backup.

**Client Clone**: A writable volume dedicated to a client, cloned from a restore point of a configuration. Ordinary clients receive temporary `CLIENT-<mac>` clones, which are reclaimed after they have been offline for a grace period. Super Admin clients use persistent `SCLIENT-<mac>` clones that are never automatically reclaimed.

**Startup Script Baking**: Windows clients require a Group Policy startup script to restore default gateway/DNS settings (because C-1 prevents the gateway from entering iBFT) and assign data disk drive letters. The script is **byte-for-byte identical on every client**; per-client configuration is fetched from the API at runtime. Therefore, it is baked into an image **once during import, before the initial snapshot**. All downstream configurations, restore points, and client clones inherit it through CoW. This avoids mounting and modifying a different clone on every boot just to insert the same script.

The image records `MountScriptVersion`. The `/boot` path compares this string without mounting a disk and skips reinjection when the version matches. An empty or different version (older images, updated server script, or changed API port) falls back to per-client injection, preserving legacy behavior.

_Known limitation_: If a user removes the GPO script from a Super Admin client and commits that restore point, the recorded version may still claim that the script exists. A later boot may incorrectly skip reinjection and fail to restore the gateway. This is the tradeoff for trusting version metadata rather than mounting the disk to verify its contents.

_Avoid_: mounting a client clone merely to check for the presence of the script.

**Offline Clone Reclaim**: Reclaims temporary `CLIENT-<mac>` clones once a client has stopped using them. This decision is based on **server-observed iSCSI sessions**, not assumptions based on heartbeats. **A disconnected session does not always mean shutdown**: link interruptions, switch restarts, and suspend/resume can all drop sessions briefly. A grace period is required before reclamation.

Two sources identify potential clones: the terminal record's persistent `OfflineAt` timestamp, and reconciliation with clones found on storage nodes (to handle stale records, deleted terminals, and historical orphans). `internal/control/assets/terminal_gc.go` is the sole owner of this operation. Reclamation and boot provisioning share a per-client MAC lock in `internal/storage/local`; without that lock, reclamation could destroy a dataset just created by boot provisioning.

_Avoid_: assuming a heartbeat or client-reported state means shutdown, or destroying a clone immediately when a session disconnects.

**Offline Volume Session**: A local server-side mount of partitions from a client clone or image clone for offline reads and writes. GPO script injection, adaptation result inspection, and image health checks use this abstraction. Owned by `internal/storage/local/winvol.go`. The invariant is **no leaked mounts**: each session defers cleanup, and teardown reclaims any remaining mounts.

_Avoid_: ad hoc mount/umount calls in callers.

**Carrier**: A storage-pool dataset or snapshot that holds a domain entity, together with its name. Naming and recognizing entities must follow the same rules. An image, configuration, or restore point must receive a ZFS-compatible ASCII name (the display name is kept in the database), while readers of the pool must be able to identify clients, Super Admin clones, temporary health-check clones, displaced configurations, and domain entities from those names. `internal/storage/layout.go` owns both directions; `Classify` handles recognition.

_Avoid_: callers manually concatenating `"CLIENT-" + mac` or using `strings.HasPrefix` to guess which object owns a dataset.

**Pool Topology**: The observed pool state at the start of a multi-step mutation, used to advance each step idempotently toward a target state. ZFS has no transactions, so recovery after an interruption requires rerunning operations. Blindly rerunning a fixed sequence could destroy a dataset already renamed into place. Instead, inspect the topology, determine the current stage, and resume. Helpers including `ensureDestroyed`, `ensureRenamed`, and `ensurePromoted` treat an already-satisfied state as complete. This underlies configuration merges and Super Admin commits, and is owned by `internal/storage/local/topology.go`.

_Avoid_: swallowing ZFS already-exists/not-found/not-a-clone errors throughout callers or blindly replaying each operation.

**Zvol Device Node**: The actual kernel block device for a zvol, `/dev/zdN`, created directly through devtmpfs. Correctness **must not depend on** `/dev/zvol` symlinks managed by udev. Under boot storms, udev can lag by seconds, leaving links absent or pointing to a reused minor number belonging to another client's disk. Dataset/device mapping, appearance and removal waiting, and mount enumeration are owned by `internal/storage/zfs/zvoldev.go`; the core primitive is the `BLKZNAME` ioctl.

_Avoid_: relying on `readlink /dev/zvol` to identify devices or using `udevadm settle` as proof of device readiness.

## Relationships

- One **image** contains one or more **configurations** (including `default`).
- One **configuration** contains zero or more **restore points**.
- At boot, a client receives a **client clone** of the **configuration and restore point** selected by its group; that clone is the client's system disk.
- A **Super Admin client** can commit disk changes as a new restore point of its configuration.

## Known Ambiguities and Deprecated Features

- **Driver disks and GPU configuration distribution (removed in the redesign on 2026-06-29)**: The legacy system supported a driver disk (installing PnP drivers to a data disk) and GPU configuration distribution. The new architecture removes both. References to `driverDiskLetter`, `driverDownloadUrl`, `purepnp`, `puregpu`, `NdTerminalGpu`, and the driver-letter/data-disk constraint are historical only.
- **Clearing the gateway**: Windows iSCSI routing can erroneously send same-subnet traffic through the gateway. Therefore, only Windows images execute `set netX/gateway 0.0.0.0` before `sanhook`; Linux images preserve the gateway. After Windows boots, the startup script restores the gateway and DNS.
