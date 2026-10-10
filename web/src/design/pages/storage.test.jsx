import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageStorage } from './storage.jsx';

// Destroying a pool permanently removes images, configurations, restore points and clones; test both creation and destruction safeguards.

const list = (items) => ({ items, total: items.length });
const POOLS = [
  { ID: 'pool-tank', Name: 'tank', Health: 'ONLINE', Capacity: 500e9, Used: 200e9, Disks: ['sda'], ReadCacheDisks: [], WriteCacheDisks: [], Layout: 'stripe', GroupWidth: 1 },
  { ID: 'pool-cold', Name: 'cold', Health: 'DEGRADED', Capacity: 100e9, Used: 10e9, Disks: ['/dev/sdb', '/dev/sdd', '/dev/sde', '/dev/sdf'], ReadCacheDisks: [], WriteCacheDisks: [],
    Layout: 'mirror', GroupWidth: 2,
    Groups: [
      { Name: 'mirror-0', Kind: 'mirror', Role: 'data', Status: 'DEGRADED', Disks: [
        { Path: '/dev/sdb', Role: 'data', Status: 'ONLINE', Vdev: 'mirror-0' },
        { Path: '/dev/sdd', Role: 'data', Status: 'OFFLINE', Vdev: 'mirror-0' } ] },
      { Name: 'mirror-1', Kind: 'mirror', Role: 'data', Status: 'ONLINE', Disks: [
        { Path: '/dev/sde', Role: 'data', Status: 'ONLINE', Vdev: 'mirror-1' },
        { Path: '/dev/sdf', Role: 'data', Status: 'ONLINE', Vdev: 'mirror-1' } ] },
      { Name: '/dev/sdz', Kind: 'disk', Role: 'spare', Status: 'AVAIL', Disks: [ { Path: '/dev/sdz', Role: 'spare', Status: 'AVAIL', Vdev: '' } ] },
    ],
    DiskItems: [
      { Path: '/dev/sdb', Role: 'data', Status: 'ONLINE', Vdev: 'mirror-0' }, { Path: '/dev/sdd', Role: 'data', Status: 'OFFLINE', Vdev: 'mirror-0' },
      { Path: '/dev/sde', Role: 'data', Status: 'ONLINE', Vdev: 'mirror-1' }, { Path: '/dev/sdf', Role: 'data', Status: 'ONLINE', Vdev: 'mirror-1' },
      { Path: '/dev/sdz', Role: 'spare', Status: 'AVAIL', Vdev: '' } ] },
];
const RAIDZ_POOL = { ID: 'pool-backup', Name: 'backup', Health: 'ONLINE', Capacity: 400e9, Used: 40e9, Disks: ['/dev/sdj', '/dev/sdk', '/dev/sdl', '/dev/sdm'], ReadCacheDisks: [], WriteCacheDisks: [],
  Layout: 'raidz2', GroupWidth: 4,
  Groups: [ { Name: 'raidz2-0', Kind: 'raidz2', Role: 'data', Status: 'ONLINE', Disks: ['/dev/sdj', '/dev/sdk', '/dev/sdl', '/dev/sdm'].map(p => ({ Path: p, Role: 'data', Status: 'ONLINE', Vdev: 'raidz2-0' })) } ],
  DiskItems: ['/dev/sdj', '/dev/sdk', '/dev/sdl', '/dev/sdm'].map(p => ({ Path: p, Role: 'data', Status: 'ONLINE', Vdev: 'raidz2-0' })) };
const DISKS = [
  { Path: '/dev/sdc', Name: 'sdc', Size: 500e9, InUse: false },
  { Path: '/dev/sdg', Name: 'sdg', Size: 500e9, InUse: false },
  { Path: '/dev/sdh', Name: 'sdh', Size: 500e9, InUse: false },
  { Path: '/dev/sdi', Name: 'sdi', Size: 400e9, InUse: false },
  { Path: '/dev/sda', Name: 'sda', Size: 500e9, InUse: true },
];

function stubApi(overrides = {}) {
  const base = {
    listPools: async () => list(POOLS),
    listDisks: async () => list(DISKS),
    listImages: async () => list([]),
    listGroups: async () => list([]),
    listTerminals: async () => list([]),
    listTasks: async () => list([]),
    listAlarms: async () => list([]),
    listClusterNodes: async () => list([]),
    listClusterPools: async () => ({ nodes: [], total_capacity: 0, total_used: 0, pool_count: 0 }),
    listClusterDisks: async () => list(DISKS),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageStorage/></ConfirmProvider></StoreProvider>,
);

// List pools across all nodes, with real node IPs rather than hashed IDs. Only owning nodes know capacity and health.
describe('Storage management cluster aggregation', () => {
  const CLUSTER_NODES = [
    { id: 'n1', ip: '192.168.10.3', ha_state: 'active', online: true },
    { id: 'n2', ip: '192.168.10.4', ha_state: 'standby', online: true },
  ];
  const CLUSTER_POOLS = {
    nodes: [
      { node_id: 'n1', ip: '192.168.10.3', role: 'active', is_self: true, online: true, reachable: true,
        pools: [{ ...POOLS[0], ServerID: 'n1' }] },
      { node_id: 'n2', ip: '192.168.10.4', role: 'standby', online: true, reachable: true,
        pools: [{ ID: 'pool-peer', Name: 'tank', Health: 'ONLINE', Capacity: 800e9, Used: 100e9,
          Disks: ['/dev/sdb'], ReadCacheDisks: [], WriteCacheDisks: [], Layout: 'stripe', GroupWidth: 1, ServerID: 'n2' }] },
    ],
    total_capacity: 1300e9, total_used: 300e9, pool_count: 2, unreachable: 0,
  };
  const stubCluster = (over = {}) => stubApi({
    listClusterNodes: async () => list(CLUSTER_NODES),
    listClusterPools: async () => CLUSTER_POOLS,
    ...over,
  });
  afterEach(() => vi.restoreAllMocks());

  it('Lists all node pools by IP rather than hashed ID', async () => {
    stubCluster();
    renderPage();
    // Wait for the remote node's row, not just two rows that might include the loading placeholder.
    const table = await screen.findByRole('table');
    await waitFor(() => expect(within(table).getByText('192.168.10.4')).toBeTruthy());
    expect(table.textContent).not.toContain('n2');
  });

  // Capacity from unreachable nodes is unavailable; require an annotation by totals rather than a specific layout.
  it('Annotates capacity when nodes are unreachable', async () => {
    stubCluster({
      listClusterPools: async () => ({
        nodes: [
          CLUSTER_POOLS.nodes[0],
          { node_id: 'n2', ip: '192.168.10.4', role: 'standby', online: false, reachable: false, pools: [], error: 'Node unreachable' },
        ],
        total_capacity: 500e9, total_used: 200e9, pool_count: 1, unreachable: 1,
      }),
    });
    renderPage();
    // Annotate both totals with the count of omitted nodes.
    const capCard = (await screen.findByText('Total Capacity')).closest('.stat-card');
    expect(capCard.textContent).toMatch(/Excludes.*1  nodes/);
    const usedCard = (await screen.findByText('Used')).closest('.stat-card');
    expect(usedCard.textContent).toMatch(/Excludes.*1  nodes/);
    // State how many nodes are unreachable.
    const poolCard = (await screen.findByText('Storage Pool')).closest('.stat-card');
    expect(poolCard.textContent).toMatch(/1  unreachable nodes/);
    // Do not add a separate warning banner.
    expect(screen.queryByText(/Totals and utilization above/)).toBeNull();
  });

  // Remote pool creation must use the remote node's disks; identical device paths can refer to other hardware.
  it('Loads target-node disk choices for remote pool creation', async () => {
    const create = vi.fn().mockResolvedValue({});
    stubCluster({ listClusterDisks: async (node) => list(
      node === 'n2' ? [{ Path: '/dev/sdx', Name: 'sdx', Size: 900e9, InUse: false }] : DISKS) });
    vi.spyOn(api, 'clusterCreatePool').mockImplementation(create);
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Create Storage Pool|Create Storage Pool/ }));
    const dlg = (await screen.findByText('Pool Name')).closest('.card');
    const nodeSel = within(dlg).getByLabelText(/Target Server/);
    await userEvent.selectOptions(nodeSel, 'n2');
    await waitFor(() => expect(within(dlg).getByText('/dev/sdx')).toBeTruthy());
    expect(within(dlg).queryByText('/dev/sdc')).toBeNull();
  });

  // A node can have one data and one backup pool. Exclude roles it already owns and reject a third pool.
  const withPeerPools = (peerPools) => stubCluster({
    listClusterPools: async () => ({ ...CLUSTER_POOLS, nodes: [
      { ...CLUSTER_POOLS.nodes[0], pools: [{ ...POOLS[0], ServerID: 'n1', Role: 'data' }] },
      { ...CLUSTER_POOLS.nodes[1], pools: peerPools },
    ] }),
    listClusterDisks: async () => list([{ Path: '/dev/sdx', Name: 'sdx', Size: 900e9, InUse: false }]),
  });
  const openCreateDialog = async () => {
    await userEvent.click(await screen.findByRole('button', { name: /Create Storage Pool/ }));
    return (await screen.findByText('Pool Name')).closest('.card');
  };
  const optionsOf = (sel) => [...sel.querySelectorAll('option')].map(o => o.textContent);

  it('Restricts a data-pool node to adding a backup pool', async () => {
    const create = vi.fn(async () => ({ task_id: 't' }));
    withPeerPools([]);
    vi.spyOn(api, 'createPool').mockImplementation(create);
    renderPage();
    const dlg = await openCreateDialog();
    const typeSel = within(dlg).getByLabelText('Pool Type');
    expect(optionsOf(typeSel)).toEqual(['Backup Pool']);
    expect(within(dlg).getByText(/This node already has data pool tank; only a backup pool can be created/)).toBeTruthy();
    await userEvent.type(within(dlg).getByPlaceholderText('tank2'), 'bak');
    await userEvent.click(within(dlg).getByRole('button', { name: /^Stripe/ }));
    await userEvent.click(await within(dlg).findByText('/dev/sdc'));
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Create Anyway' }));
    await waitFor(() => expect(create).toHaveBeenCalledWith(expect.objectContaining({ name: 'bak', role: 'backup' })));
    expect(screen.queryByText(/services restart automatically/)).toBeNull();
  });

  // Combine remote-node and nonredundant-stripe warnings into one confirmation.
  it('Confirms a remote stripe pool only once', async () => {
    const create = vi.fn(async () => ({ task_id: 't' }));
    withPeerPools([]);
    vi.spyOn(api, 'clusterCreatePool').mockImplementation(create);
    renderPage();
    const dlg = await openCreateDialog();
    await userEvent.selectOptions(within(dlg).getByLabelText(/Target Server/), 'n2');
    await userEvent.selectOptions(within(dlg).getByLabelText('Pool Type'), 'data');
    await userEvent.type(within(dlg).getByPlaceholderText('tank2'), 'data');
    await userEvent.click(within(dlg).getByRole('button', { name: /^Stripe/ }));
    await userEvent.click(await within(dlg).findByText('/dev/sdx'));
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    const msg = await screen.findByText(/Create anyway/);
    expect(msg.textContent).toBe('Create pool on 192.168.10.4 with name data; disks: /dev/sdx.\n'
      + 'Stripe has no redundancy. Failure of any disk loses all data in data (images, configurations, restore points and client clones). Create anyway?');
    await userEvent.click(screen.getByRole('button', { name: 'Create Anyway' }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(screen.queryByText(/Create on Another Server/)).toBeNull();
  });

  it('Requires selecting data or backup role when both available', async () => {
    withPeerPools([]);
    renderPage();
    const dlg = await openCreateDialog();
    await userEvent.selectOptions(within(dlg).getByLabelText(/Target Server/), 'n2');
    const typeSel = within(dlg).getByLabelText('Pool Type');
    expect(optionsOf(typeSel)).toEqual(['Select…', 'Data Pool', 'Backup Pool']);
    const submit = screen.getByRole('button', { name: 'Create' });
    expect(submit.disabled).toBe(true);
    await userEvent.selectOptions(typeSel, 'data');
    expect(submit.disabled).toBe(false);
  });

  it('Disables pool creation when both roles exist', async () => {
    withPeerPools([
      { ID: 'pool-d', Name: 'tank', Role: 'data', Health: 'ONLINE', Capacity: 1, Used: 0, Disks: ['/dev/sdb'], ReadCacheDisks: [], WriteCacheDisks: [], ServerID: 'n2' },
      { ID: 'pool-b', Name: 'bak', Role: 'backup', Health: 'ONLINE', Capacity: 1, Used: 0, Disks: ['/dev/sdc'], ReadCacheDisks: [], WriteCacheDisks: [], ServerID: 'n2' },
    ]);
    renderPage();
    const dlg = await openCreateDialog();
    await userEvent.selectOptions(within(dlg).getByLabelText(/Target Server/), 'n2');
    expect(within(dlg).getByLabelText('Pool Type').disabled).toBe(true);
    expect(within(dlg).getByText(/This node already has data pool tank and backup pool bak/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Create' }).disabled).toBe(true);
  });

  // Details show node IP, not the hashed machine ID.
  it('Displays server IP in pool details', async () => {
    stubCluster();
    renderPage();
    const rows = await screen.findAllByText('tank');
    await userEvent.click(rows[rows.length - 1]);
    const dlg = (await screen.findByText(/Storage Pool Details/)).closest('.card');
    expect(within(dlg).getByText('192.168.10.4')).toBeTruthy();
    expect(within(dlg).queryByText('n2')).toBeNull();
  });

  // Mutating remote pools requires identifying the affected node in confirmation.
  it('Requires verifying target node before remote pool mutation', async () => {
    const destroy = vi.fn().mockResolvedValue({});
    stubCluster();
    vi.spyOn(api, 'clusterDestroyPool').mockImplementation(destroy);
    renderPage();
    const peerRow = await waitFor(async () => {
      const rs = Array.from((await screen.findByRole('table')).querySelectorAll('tbody tr'));
      const r = rs.find(x => x.textContent.includes('192.168.10.4'));
      if (!r) throw new Error('Remote pool has not rendered');
      return r;
    });
    await userEvent.click(peerRow);
    await userEvent.click(await screen.findByRole('button', { name: /Destroy Storage Pool/ }));
    const dialog = (await screen.findByText(/deletes all its images, configurations, restore points and client clones permanently/)).closest('.card');
    // Require remote node address for confirmation because multiple nodes may have pool tank.
    expect(dialog.textContent).toContain('192.168.10.4');
    const input = dialog.querySelector('input');
    await userEvent.type(input, '192.168.10.4');
    await userEvent.click(within(dialog).getByRole('button', { name: /Destroy|Confirm/ }));
    await waitFor(() => expect(destroy).toHaveBeenCalledWith('n2', 'pool-peer'));
  });
});

describe('Storage Pool Management', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  // Show pool roles so operators can distinguish data, backup and unassigned pools.
  it('Displays data/backup/unassigned pool roles', async () => {
    stubApi({ listPools: async () => list([
      { ...POOLS[0], Role: 'data' },
      { ...POOLS[1], Role: 'backup' },
      { ...RAIDZ_POOL, Role: '' },
    ]) });
    renderPage();
    const table = await screen.findByRole('table');
    expect(await within(table).findByText('Data Pool')).toBeTruthy();
    expect(within(table).getByText('Backup Pool')).toBeTruthy();
    expect(within(table).getByText('Unassigned')).toBeTruthy();
  });

  it('Sorts data pools before backups while preserving node order', async () => {
    stubApi({ listPools: async () => list([
      { ...POOLS[0], ID: 'b3', Name: 'backup3', Role: 'backup' },
      { ...POOLS[0], ID: 'd3', Name: 'tank3', Role: 'data' },
      { ...POOLS[0], ID: 'b4', Name: 'backup4', Role: 'backup' },
      { ...POOLS[0], ID: 'd4', Name: 'tank4', Role: 'data' },
    ]) });
    renderPage();
    await screen.findByText('tank3');
    const names = screen.getAllByRole('row').slice(1).map(r => r.textContent.match(/(tank\d|backup\d)/)?.[0]).filter(Boolean);
    expect(names).toEqual(['tank3', 'tank4', 'backup3', 'backup4']);
  });

  it('Filters by pool type', async () => {
    stubApi({ listPools: async () => list([
      { ...POOLS[0], ID: 'd3', Name: 'tank3', Role: 'data' },
      { ...POOLS[0], ID: 'b3', Name: 'backup3', Role: 'backup' },
    ]) });
    renderPage();
    await screen.findByText('tank3');
    await userEvent.selectOptions(screen.getByLabelText('Filter by Type'), 'backup');
    await waitFor(() => expect(screen.queryByText('tank3')).toBeNull());
    expect(screen.getByText('backup3')).toBeTruthy();
  });

  // Explain and disable pool creation when both roles are already allocated.
  it('Disables creation after reaching two-pool limit', async () => {
    renderPage();
    await screen.findByText('tank');
    const create = screen.getByRole('button', { name: /Create Storage Pool/ });
    expect(create.disabled).toBe(true);
    expect(screen.getByText(/At most two pools per node/)).toBeTruthy();
  });

  it('Lists storage pools', async () => {
    renderPage();
    expect(await screen.findByText('tank')).toBeTruthy();
    expect(screen.getByText('cold')).toBeTruthy();
  });

  // Show available capacity separately because ZFS uses it for out-of-space checks.
  it('Shows used and available capacity', async () => {
    renderPage();
    expect(await screen.findByText('Used 186.3 GiB · Available 279.4 GiB')).toBeTruthy();
    expect(screen.getByText('Used 9.3 GiB · Available 83.8 GiB')).toBeTruthy();
  });

  // lsblk disk discovery failure must not clear the pool list.
  it('Keeps pool list when disk discovery fails', async () => {
    stubApi({ listDisks: async () => { throw new Error('lsblk unavailable'); } });
    renderPage();
    expect(await screen.findByText('tank')).toBeTruthy();
  });

  it('Reports pool API errors', async () => {
    stubApi({ listPools: async () => { throw new Error('Backend unavailable'); } });
    renderPage();
    expect(await screen.findByText(/Backend unavailable|Load failed/)).toBeTruthy();
  });

  it('Filters by health', async () => {
    renderPage();
    await screen.findByText('tank');
    // The filter is a select control, not a row of buttons.
    await userEvent.selectOptions(screen.getByLabelText('Filter by Status'), 'abnormal');
    await waitFor(() => expect(screen.queryByText('tank')).toBeNull());
    expect(screen.getByText('cold')).toBeTruthy();
  });

  // Require typing the pool name and explain exactly which data destruction affects.
  it('Requires typing pool name before destruction', async () => {
    const deletePool = vi.fn(async () => ({ task_id: 'task-destroy_pool-1' }));
    stubApi({ deletePool });
    renderPage();
    await userEvent.click(await screen.findByText('tank'));

    await userEvent.click(await screen.findByRole('button', { name: /Destroy/ }));
    const dialog = await screen.findByText(/cannot be undone/);
    expect(dialog.textContent).toContain('images, configurations, restore points and client clones');

    const confirm = screen.getByRole('button', { name: 'Destroy' });
    expect(confirm.disabled).toBe(true);
    expect(deletePool).not.toHaveBeenCalled();

    await userEvent.type(screen.getByPlaceholderText('tank'), 'tank');
    expect(confirm.disabled).toBe(false);
    await userEvent.click(confirm);
    await waitFor(() => expect(deletePool).toHaveBeenCalledWith('pool-tank'));
  });

  // Missing pools with stale database records must not contribute old capacity figures.
  const GONE = { ID: 'pool-data1', Name: 'data1', Health: 'MISSING', Capacity: 0, Used: 0, Disks: ['/dev/sdb', '/dev/sdc'], ReadCacheDisks: [], WriteCacheDisks: [], Layout: 'stripe', GroupWidth: 1 };

  it('Marks missing pools without showing stale capacity', async () => {
    stubApi({ listPools: async () => list([POOLS[0], GONE]) });
    renderPage();
    await screen.findByText('data1');
    const row = screen.getByText('data1').closest('tr');
    expect(within(row).getByText('Pool missing on this node')).toBeTruthy();
    expect(within(row).getByText('Missing')).toBeTruthy();
    expect(within(row).queryByText(/Used/)).toBeNull();
    expect(screen.getByText(/^1  storage disks/)).toBeTruthy();
  });

  // Deleting a stale record changes no disks and does not require destructive-action typing.
  it('Deletes stale pool records without modifying disks', async () => {
    const deletePool = vi.fn(async () => ({ task_id: 'task-destroy_pool-1' }));
    stubApi({ listPools: async () => list([POOLS[0], GONE]), deletePool });
    renderPage();
    await userEvent.click(await screen.findByText('data1'));
    expect(await screen.findByText(/Deleting the record changes no disks/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: /Delete Record/ }));
    const confirm = await screen.findByRole('button', { name: 'Delete' });
    expect(confirm.disabled).toBe(false);
    await userEvent.click(confirm);
    await waitFor(() => expect(deletePool).toHaveBeenCalledWith('pool-data1'));
  });

  // Validate pool names before submitting ZFS tasks, including rejecting whitespace.
  it('Validates missing names, whitespace and unselected disks', async () => {
    const createPool = vi.fn(async () => ({ task_id: 'task-create_pool-1' }));
    stubApi({ createPool, listPools: async () => list([POOLS[0]]) });
    renderPage();
    await screen.findByText('tank');
    await userEvent.click(screen.getByRole('button', { name: /Create Storage Pool/ }));

    const submit = await screen.findByRole('button', { name: 'Create' });
    const nameBox = screen.getByPlaceholderText('tank2');

    // Verify each guard with all other conditions satisfied.
    await userEvent.type(nameBox, 'tank2');
    await userEvent.click(submit);
    expect(createPool).not.toHaveBeenCalled(); // No selected disks

    // Only free disks that are not in another pool can be selected.
    await userEvent.click(screen.getByText(/sdc/));
    await userEvent.clear(nameBox);
    await userEvent.click(submit);
    expect(createPool).not.toHaveBeenCalled(); // Pool Name is empty

    await userEvent.type(nameBox, 'my pool');
    await userEvent.click(submit);
    expect(createPool).not.toHaveBeenCalled(); // ZFS rejects whitespace


    await userEvent.clear(nameBox);
    await userEvent.type(nameBox, 'tank2');
    // A single disk can only form a stripe; warn about the lack of redundancy.
    await userEvent.click(screen.getByRole('button', { name: /^Stripe/ }));

    // mirrors is a reserved zpool name; reject it before task submission.
    await userEvent.clear(nameBox);
    await userEvent.type(nameBox, 'mirrors');
    await userEvent.click(submit);
    expect(createPool).not.toHaveBeenCalled();
    expect(await screen.findByText(/reserved ZFS name/)).toBeTruthy();
    await userEvent.clear(nameBox);
    await userEvent.type(nameBox, 'tank2');

    await userEvent.click(submit);
    expect(createPool).not.toHaveBeenCalled(); // Awaiting confirmation
    await userEvent.click(await screen.findByRole('button', { name: 'Create Anyway' }));
    await waitFor(() => expect(createPool).toHaveBeenCalledWith({ name: 'tank2', disks: ['/dev/sdc'], layout: 'stripe', group_width: 1, role: 'data' }));
    // Data pool creation restarts services; warn before confirming.
    expect(await screen.findByText(/services restart automatically after creating the data pool/)).toBeTruthy();
  });

  // Layout cannot be changed afterward; validate disk counts and estimate capacity while selecting disks.
  it('Validates layout disk counts and estimates capacity', async () => {
    const createPool = vi.fn(async () => ({ task_id: 'task-create_pool-1' }));
    stubApi({ createPool, listPools: async () => list([POOLS[0]]) });
    renderPage();
    await screen.findByText('tank');
    await userEvent.click(screen.getByRole('button', { name: /Create Storage Pool/ }));
    const submit = await screen.findByRole('button', { name: 'Create' });
    await userEvent.type(screen.getByPlaceholderText('tank2'), 'tank2');

    // Default mirror layout requires complete disk groups; three disks cannot form two pairs.
    await userEvent.click(screen.getByText(/sdc/));
    await userEvent.click(screen.getByText(/sdg/));
    await userEvent.click(screen.getByText(/sdh/));
    expect(await screen.findByText(/Mirrored pool requires 2 disks per group. Select 1 more/)).toBeTruthy();
    expect(submit.disabled).toBe(true);

    // Four disks form two pairs: min(500,500) + min(500,400) = 900e9 = 838.2 GiB.
    await userEvent.click(screen.getByText(/sdi/));
    expect(await screen.findByText(/Mirror 2 groups ≈ 838\.2 GiB available · tolerates 1 disk failures per group/)).toBeTruthy();
    expect(submit.disabled).toBe(false);

    // Four-disk raidz2: (4−2) × 400e9 × 0.9 = 720e9 = 670.6 GiB.
    await userEvent.click(screen.getByRole('button', { name: /^raidz2/ }));
    expect(await screen.findByText(/raidz2 ≈ 670\.6 GiB available · tolerates 2 disks/)).toBeTruthy();
    // Removing a disk leaves too few for raidz2.
    await userEvent.click(screen.getByText(/sdi/));
    expect(await screen.findByText(/raidz2 requires at least 4 disks. Select 1 more/)).toBeTruthy();
    expect(submit.disabled).toBe(true);

    await userEvent.click(screen.getByRole('button', { name: /^Mirror/ }));
    await userEvent.click(screen.getByText(/sdi/));
    await userEvent.click(submit);
    await waitFor(() => expect(createPool).toHaveBeenCalledWith({ name: 'tank2', disks: ['/dev/sdc', '/dev/sdg', '/dev/sdh', '/dev/sdi'], layout: 'mirror', group_width: 2, role: 'data' }));
  });

  // Mirror pool expansion requires full groups; attaching one mirror partner is a separate supported action.
  it('Expands mirrors by complete groups and attaches partners', async () => {
    const addPoolDisk = vi.fn(async () => ({ task_id: 'task-add_disk-1' }));
    stubApi({ addPoolDisk });
    renderPage();
    await userEvent.click(await screen.findByText('cold'));
    await screen.findByText('mirror-0');

    // Adding a single disk cannot complete a new mirror group.
    const addButtons = screen.getAllByRole('button', { name: /Add/ });
    await userEvent.click(addButtons[0]);
    const confirm = await screen.findByRole('button', { name: 'Confirm' });
    await userEvent.click(screen.getByText(/sdc/));
    expect(await screen.findByText(/Add full mirror groups of 2 disks. Select 1 more/)).toBeTruthy();
    expect(confirm.disabled).toBe(true);
    await userEvent.click(screen.getByText(/sdg/));
    expect(await screen.findByText(/Add 1 mirror groups/)).toBeTruthy();
    await userEvent.click(confirm);
    await waitFor(() => expect(addPoolDisk).toHaveBeenCalledWith('pool-cold', { disks: ['/dev/sdc', '/dev/sdg'] }));

    // Attach a mirror partner through that disk's row.
    await userEvent.click(screen.getByRole('button', { name: 'Attach RAID1 disk to /dev/sdb' }));
    const confirm2 = await screen.findByRole('button', { name: 'Confirm' });
    await userEvent.click(screen.getByText(/sdh/));
    await userEvent.click(confirm2);
    await waitFor(() => expect(addPoolDisk).toHaveBeenCalledWith('pool-cold', { disks: ['/dev/sdh'], mode: 'attach', target: '/dev/sdb' }));
  });

  // raidz accepts whole-group additions and does not allow mirror attachment.
  it('Requires raidz full groups and blocks mirror attachments', async () => {
    stubApi({ listPools: async () => list([...POOLS, RAIDZ_POOL]) });
    renderPage();
    await userEvent.click(await screen.findByText('backup'));
    await screen.findByText('raidz2-0');
    expect(screen.queryByRole('button', { name: /Attach RAID1 disk/ })).toBeNull();

    await userEvent.click(screen.getAllByRole('button', { name: /Add/ })[0]);
    const confirm = await screen.findByRole('button', { name: 'Confirm' });
    await userEvent.click(screen.getByText(/sdc/));
    expect(await screen.findByText(/raidz2 requires full groups of 4 disks. Select 3 more/)).toBeTruthy();
    expect(confirm.disabled).toBe(true);
  });

  // raidz members cannot be removed; replace failed drives instead.
  it('Blocks raidz removal but permits replacement', async () => {
    const replacePoolDisk = vi.fn(async () => ({ task_id: 'task-replace_disk-1' }));
    stubApi({ listPools: async () => list([...POOLS, RAIDZ_POOL]), replacePoolDisk });
    renderPage();
    await userEvent.click(await screen.findByText('backup'));
    await screen.findByText('raidz2-0');
    expect(screen.queryByRole('button', { name: /^Remove \/dev/ })).toBeNull();
    expect(screen.getByText(/raidz devices cannot be removed/)).toBeTruthy();

    await userEvent.click(screen.getByRole('button', { name: 'Replace /dev/sdj' }));
    const confirm = await screen.findByRole('button', { name: 'Confirm' });
    await userEvent.click(screen.getByText(/sdc/));
    await userEvent.click(confirm);
    await waitFor(() => expect(replacePoolDisk).toHaveBeenCalledWith('pool-backup', { old_disk: '/dev/sdj', new_disk: '/dev/sdc' }));
  });

  // Detaching one mirror member reduces redundancy; removing a group migrates its data.
  it('Confirms member detach and supports group removal', async () => {
    const removePoolDisk = vi.fn(async () => ({ task_id: 'task-detach_disk-1' }));
    stubApi({ removePoolDisk });
    renderPage();
    await userEvent.click(await screen.findByText('cold'));
    await screen.findByText('mirror-0');

    await userEvent.click(screen.getByRole('button', { name: 'Remove /dev/sdb' }));
    expect((await screen.findByText(/mirror-0 will have only 1-way/)).textContent).toContain('with no redundancy');
    await userEvent.click(screen.getByRole('button', { name: 'Detach' }));
    await waitFor(() => expect(removePoolDisk).toHaveBeenCalledWith('pool-cold', { disk: '/dev/sdb' }));

    await userEvent.click(screen.getByRole('button', { name: 'Remove vdev group mirror-1' }));
    await screen.findByText(/migrate to other groups/);
    await userEvent.click(screen.getByRole('button', { name: 'Remove Group' }));
    await waitFor(() => expect(removePoolDisk).toHaveBeenCalledWith('pool-cold', { disk: 'mirror-1' }));
  });

  // Upgrade a striped pool to RAID1 by pairing each member with a free disk in one online task.
  // Use RAID1 to distinguish disk mirrors from system images.
  it('Upgrades multi-disk stripes to RAID1', async () => {
    const mirrorUpgradePool = vi.fn(async () => ({ task_id: 'task-mirror_upgrade-1' }));
    stubApi({ mirrorUpgradePool, listPools: async () => list([{ ...POOLS[0], Disks: ['sda', 'sdb'] }]) });
    renderPage();
    await userEvent.click(await screen.findByText('tank'));
    await userEvent.click(await screen.findByRole('button', { name: /Upgrade to RAID1/ }));

    const start = await screen.findByRole('button', { name: 'Start Upgrade' });
    expect(start.disabled).toBe(true); // Not paired yet
    await userEvent.selectOptions(screen.getByLabelText('RAID1 partner for sda'), '/dev/sdc');
    expect(start.disabled).toBe(true); // Partial pairing is still unprotected
    await userEvent.selectOptions(screen.getByLabelText('RAID1 partner for sdb'), '/dev/sdg');
    expect(start.disabled).toBe(false);
    await userEvent.click(start);
    await waitFor(() => expect(mirrorUpgradePool).toHaveBeenCalledWith('pool-tank',
      { pairs: [{ target: 'sda', disk: '/dev/sdc' }, { target: 'sdb', disk: '/dev/sdg' }] }));
  });

  // On one-disk stripes, adding RAID1 partner replaces the redundant upgrade flow.
  it('Offers attaching RAID1 instead of upgrade on one-disk stripes', async () => {
    renderPage();
    await userEvent.click(await screen.findByText('tank'));
    expect(await screen.findByRole('button', { name: 'Attach RAID1 disk to sda' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Upgrade to RAID1/ })).toBeNull();
  });

  // Special metadata vdevs require mirrors; hot spares are managed one disk at a time.
  it('Requires mirrored metadata vdevs and supports hot spares', async () => {
    const addSpecial = vi.fn(async () => ({ task_id: 'task-add_special-1' }));
    const addSpare = vi.fn(async () => ({ task_id: 'task-add_spare-1' }));
    const removeSpare = vi.fn(async () => ({ task_id: 'task-remove_spare-1' }));
    stubApi({ addSpecial, addSpare, removeSpare });
    renderPage();
    await userEvent.click(await screen.findByText('cold'));
    await screen.findByText('mirror-0');

    const addButtons = screen.getAllByRole('button', { name: /Add/ });
    await userEvent.click(addButtons[1]); // Metadata disks
    const confirm = await screen.findByRole('button', { name: 'Confirm' });
    await userEvent.click(screen.getByText(/sdc/));
    expect(await screen.findByText(/Metadata disks must be mirrored. Select 2 or 3 disks/)).toBeTruthy();
    expect(confirm.disabled).toBe(true);
    await userEvent.click(screen.getByText(/sdg/));
    await userEvent.click(confirm);
    await waitFor(() => expect(addSpecial).toHaveBeenCalledWith('pool-cold', { disks: ['/dev/sdc', '/dev/sdg'] }));

    await userEvent.click(screen.getAllByRole('button', { name: /Add/ })[2]); // Hot spares
    const confirm2 = await screen.findByRole('button', { name: 'Confirm' });
    await userEvent.click(screen.getByText(/sdi/));
    await userEvent.click(confirm2);
    await waitFor(() => expect(addSpare).toHaveBeenCalledWith('pool-cold', { disks: ['/dev/sdi'] }));

    await userEvent.click(screen.getByRole('button', { name: 'Remove Hot Spare /dev/sdz' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Remove' }));
    await waitFor(() => expect(removeSpare).toHaveBeenCalledWith('pool-cold', { disk: '/dev/sdz' }));
  });

  // Show single-disk raidz expansion only on supported nodes, otherwise explain why unavailable.
  it('Gates raidz expansion by node capability', async () => {
    const addPoolDisk = vi.fn(async () => ({ task_id: 'task-add_disk-1' }));
    stubApi({ addPoolDisk, listPools: async () => list([...POOLS, { ...RAIDZ_POOL, RaidzExpandable: false, RaidzExpandNote: 'ZFS userspace 2.2.2 does not support single-disk raidz expansion (requires 2.3+)' }]) });
    const { unmount } = renderPage();
    await userEvent.click(await screen.findByText('backup'));
    await screen.findByText('raidz2-0');
    expect(screen.queryByRole('button', { name: /Expand by One Disk/ })).toBeNull();
    expect(screen.getByText(/does not support single-disk raidz expansion/)).toBeTruthy();
    unmount();

    stubApi({ addPoolDisk, listPools: async () => list([...POOLS, { ...RAIDZ_POOL, RaidzExpandable: true }]) });
    renderPage();
    await userEvent.click(await screen.findByText('backup'));
    await userEvent.click(await screen.findByRole('button', { name: 'Expand raidz2-0 by one disk' }));
    const confirm = await screen.findByRole('button', { name: 'Confirm' });
    await userEvent.click(screen.getByText(/sdc/));
    await userEvent.click(confirm);
    await waitFor(() => expect(addPoolDisk).toHaveBeenCalledWith('pool-backup', { disks: ['/dev/sdc'], mode: 'attach', target: 'raidz2-0' }));
  });

  // Show pool layout in the list and group-level degraded member details in the dialog.
  it('Displays layout and grouped details', async () => {
    renderPage();
    expect(await screen.findByText('Mirror (RAID1)×2')).toBeTruthy();
    expect(screen.getByText('Stripe')).toBeTruthy();

    await userEvent.click(screen.getByText('cold'));
    expect(await screen.findByText('mirror-0')).toBeTruthy();
    expect(screen.getByText('mirror-1')).toBeTruthy();
    const row = screen.getByText('/dev/sdd').closest('tr');
    // Translate ZFS states for display but keep raw values in tooltips.
    expect(row.textContent).toContain('Offline');
    // Separate hot spares from data disks.
    expect(screen.getAllByText('Hot Spares').length).toBeGreaterThan(0);
    expect(screen.getByText('/dev/sdz').closest('tr').textContent).toContain('Hot Spares');
  });
});

// Remote pool mutations target the owning node, including its free disk inventory.
describe('Remote pool details', () => {
  afterEach(() => vi.restoreAllMocks());

  it("Uses the owning node's free disks for pool expansion", async () => {
    const CLUSTER_NODES = [
      { id: 'n1', ip: '192.168.10.3', ha_state: 'active', online: true },
      { id: 'n2', ip: '192.168.10.4', ha_state: 'standby', online: true },
    ];
    const listClusterDisks = vi.fn(async (node) => list(node === 'n2'
      ? [{ Path: '/dev/sdx', Name: 'sdx', Size: 900e9, InUse: false }, { Path: '/dev/sdb', Name: 'sdb', Size: 900e9, InUse: true }]
      : DISKS));
    stubApi({
      listClusterNodes: async () => list(CLUSTER_NODES),
      listClusterPools: async () => ({
        nodes: [
          { node_id: 'n1', ip: '192.168.10.3', role: 'active', is_self: true, online: true, reachable: true, pools: [{ ...POOLS[0], ServerID: 'n1' }] },
          { node_id: 'n2', ip: '192.168.10.4', role: 'standby', online: true, reachable: true,
            pools: [{ ...POOLS[1], ID: 'pool-peer', ServerID: 'n2' }] },
        ],
        total_capacity: 600e9, total_used: 210e9, pool_count: 2, unreachable: 0,
      }),
      listClusterDisks,
    });
    renderPage();
    await userEvent.click(await screen.findByText('cold'));
    await screen.findByText('mirror-0');
    await waitFor(() => expect(listClusterDisks).toHaveBeenCalledWith('n2'));
    await userEvent.click(screen.getAllByRole('button', { name: /Add/ })[0]);
    await screen.findByRole('button', { name: 'Confirm' });
    expect(await screen.findByText(/sdx/)).toBeTruthy();
    expect(screen.queryByText(/sdc/)).toBeNull();
  });
});
