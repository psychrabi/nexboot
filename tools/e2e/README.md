# End-to-End Test Matrices

These matrices run against a deployed server and cover behavior that unit tests cannot verify: ZFS rejecting operations, LIO exporting LUNs, partition block devices appearing, and Windows image health checks reading real partitions. Failures in these tests can expose defects specific to physical deployments.

| Script | Coverage |
| --- | --- |
| `config_reduction_matrix.py` | Configuration and restore-point creation, branching, merging, and deletion, including success and failure paths (38 cases) |
| `boot_matrix.py` | Boot path, baked startup scripts, clone naming, data-disk LUNs and drive letters, clone reclamation after client deletion, and interrupted merge recovery |
| `adaptation_matrix.py` | Offline Super Admin adaptation, clone creation, script and GPO registration injection, and explicit confirmation of repeat injection |
| `driver_matrix.py` | Driver-pack upload and INF parsing, bundle creation and downloads, and Super Admin injection |
| `platform_matrix.py` | User creation/deletion/password changes and login, alert lifecycle through acknowledgement and recovery, and audit persistence |
| `pool_matrix.py` | Pool creation, disk additions and replacement, read/write cache changes, destruction, and disk reclamation |
| `backup_matrix.py` | Local backups, full-database streaming with a DB copy, incremental recovery after merges/Super Admin commits, and fallback to full backup when the baseline is lost (requires `ND_BACKUP_POOL` and `ND_WINDOWS_SOURCE`) |
| `ha_matrix.py` | Two-node HA, VIP transition timing, failover/recovery/planned switchover, fencing under isolation, and survival of data in both directions (requires `ND_NODE_A/B`, `ND_VIP`, `ND_CLUSTER_TOKEN`) |
| `cluster_matrix.py` | Three-node clusters, node registration, placing groups on storage nodes, failback/recovery, quorum fencing, and routing cleanup (requires `ND_NODE_A/B/C`, `ND_VIP`, `ND_CLUSTER_TOKEN`) |
| `failover_matrix.py` | Failover with clients under power loss, link failures, rejoining a former active node with unreplicated data, split-brain prevention, repeated failovers, and replication catch-up |
| `cluster_ops_matrix.py` | Cluster management, internal endpoint authentication, pool identity per node, capacity aggregation, node-local disks, remote service restarts/log inspection, and rejecting operations sent to the wrong node (adapts to the count in `ND_NODES`) |
| `balance_matrix.py` | Automatic client placement balance, sticky placement, explicit affinity overrides, reverting to automatic mode, and failover to a healthy node |
| `ha_recovery_matrix.py` | HA automatic recovery and manual rescue: standby holding VIP becomes active; partitioned nodes holding VIP must not compete; an active node without VIP steps down; forced activation still respects the double-write fence |
| `topology_suite.py` | Destructive staged topology testing: convert the same machines from standalone to two-node to three-node, running appropriate matrices at each stage |

## Offline Configuration Checks

These checks need no server. They verify that `reset.py` and `wait_settled.py` reject a missing `ND_VIP` and recognize custom VIPs using the full address:

```sh
python3 tools/e2e/test_environment.py
```

## Prerequisites

- A deployed, reachable server with valid login credentials and SSH access. The scripts use `sshpass`; install with `brew install sshpass` or `apt install sshpass`.
- An import directory in the pool. `boot_matrix.py` and `adaptation_matrix.py` also require a **real Windows image source that passes health checks**. A ZFS stream can import quickly. A dummy 64 MiB raw file fails image health checks and prevents group creation.

## Running Cluster Matrices

Use the orchestration scripts under `run/`; do not manually assemble the environment variables.

Cluster test matrices must execute **on a node within the cluster**, where nodes can communicate directly. Port-forwarded requests from a development workstation are insufficient. Node A and B cannot be fixed in advance because the VIP may move to any of three VRRP participants. In `ha_matrix`, node A must be the **current active writer** during planned switchover. The run scripts handle these requirements.

```sh
cd tools/e2e/run
cp env.example env && vi env     # Set node addresses, VIP, credentials, pool, and image source
./sync.sh                        # Deploy harness to every node
# On any cluster node, in ND_E2E_DIR/run:
./all.sh                         # All regression suites
./one.sh backup_matrix.py        # One suite for targeted debugging
./topology.sh                    # Destructive standalone → two-node → three-node test
```

The `env` file contains secrets and is ignored by Git. Commit `env.example` only.

### Without a Real Windows Image

`boot_matrix` and `backup_matrix` need an image that passes import health checks. If no real Windows image is available, create a small synthetic fixture (1 GiB apparent size, only a few MiB allocated):

```sh
sudo tools/e2e/fixtures/mkwin.sh /var/lib/ndiskless/imports/winmin.raw 1G
```

The blocking health checks require `Windows/System32` and `drivers/msiscsi.sys`. The fixture includes these paths, NIC INF files, and BCD. **This fixture is not a bootable Windows system.** It is suitable for tests of streams, incremental changes, clone names, and Super Admin commits, where bootability is not asserted. Use a real image to test Windows startup.

Without an image source, the relevant suites **skip with an explanation** rather than recording misleading failures.

### Running Standalone Matrices Directly

```sh
export ND_BASE=http://192.168.124.56:18080
export ND_PASSWORD='...'                        # Also used for SSH unless ND_SSH_PASSWORD is specified
export ND_WINDOWS_SOURCE=/tank/imports/xxx.gzip # Needed by boot and adaptation suites

python3 tools/e2e/config_reduction_matrix.py
python3 tools/e2e/boot_matrix.py
python3 tools/e2e/adaptation_matrix.py
python3 tools/e2e/driver_matrix.py
python3 tools/e2e/platform_matrix.py
python3 tools/e2e/pool_matrix.py                # Requires four unused block devices
```

## Test Disks for Storage-Pool Matrices

The product accepts devices with `lsblk TYPE=disk` as candidate disks; loopback files do not qualify. If no spare physical drives are available, use the kernel's `scsi_debug` module to create four **independent** simulated disks:

```sh
modprobe scsi_debug dev_size_mb=1024 add_host=4 num_tgts=1 per_host_store=1
```

All parameters matter. Setting `num_tgts=4` causes targets to share backing storage, which makes ZFS report that multiple vdevs refer to the same device. `per_host_store=1` gives each host separate storage. After testing, use `rmmod scsi_debug` to remove the devices; reloading the module creates fresh ones.

A successful suite exits with code 0 and lists failed cases again at the end. See comments at the top of `ndapi.py` for other settings: `ND_USER`, `ND_SSH_HOST`, `ND_SSH_USER`, `ND_POOL`, and `ND_IMPORT_DIR`.

## Running Multi-Node Matrices

Nodes must communicate directly, so copy `tools/e2e/` to a node inside the cluster rather than running tests through workstation port forwarding.

The `run/` scripts define the many required variables. Missing one may cause **silent** failures.

```sh
cd tools/e2e/run && cp env.example env && vi env
./sync.sh                       # Copy harness to all nodes
# On a node, enter ND_E2E_DIR/run and execute ./all.sh
```

**Distinguish `ND_PASSWORD` from `ND_SSH_PASSWORD`.** Using the wrong SSH password may cause commands to fail silently and return empty data, producing plausible but incorrect assertions. `env.example` declares the variables separately for this reason.

## Destructive Topology Suite

`topology_suite.py` follows the supported upgrade path: standalone → two nodes → three nodes, running the relevant suites at each stage. This tests the actual expansion path, not simulations of topologies on an existing three-node cluster. The system supports adding nodes, and the upgrade step itself is a source of subtle bugs: the original node was once the only entry in the roster and was treated as the local identity.

```sh
./topology.sh       # Run on a node under ND_E2E_DIR/run; ND_STAGES can restrict stages
```

Standalone tests cover cluster management, balancing, **boot chain**, and **local backup**. Two-node tests add HA and failover with clients. Three-node tests rerun the boot chain (including the critical check that Super Admin commits go to the active writer) and run the cluster matrix.

Earlier, boot-path tests were only executed manually; consequently, a defect in which a Super Admin commit landed on standby and was reverted by snapshot replication escaped multiple regression cycles. Keeping the boot matrix in staged testing helps prevent recurrence.

The suite **clears the catalogue database, configuration, and keepalived setup on each machine, then reinstalls**. Data pools are left intact: images, configurations, and restore points persist in their datasets and are rediscovered on startup. Groups, clients, and users are not recovered; the matrices create and clean up these records themselves.

## Important Safety Rules

**Never run against production pools.** The matrices delete and recreate their own test images, groups, clients, driver packs, and users. `pool_matrix.py` creates and destroys only the test pool it created; it does not touch preexisting pools.

When interpreting results, avoid these false failures (existing tests handle them; preserve these safeguards when modifying tests):

- Task history must be searched beyond the most recent entry. Importing an image immediately starts an image health check, so `limit=1` may hide the successful import.
- Delete terminals by ID (`/api/terminals/terminal-AABBCC112233`), not MAC; using MAC returns 404 and can look like a failed clone cleanup.
- List endpoints and `/boot/data-disks` return `{items:[...]}`, not plain arrays.
- Archive/export endpoints return binary data; use `get_bytes()`. Text-decoding ZIP bytes through `call()` fails immediately.
- An alert's `Resource` contains the image **ID**, not its display name (non-ASCII names may be normalized during import).
- Use `skip()` when prerequisites are missing; reporting success instead would overstate test coverage.

After passing the matrices, manually confirm with a real client that the OS reaches the desktop, data disk drive letters appear, and `adapt.ps1` installs Windows drivers successfully. API assertions cannot replace these end-to-end observations.
