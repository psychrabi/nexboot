import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageTerminals } from './terminals.jsx';

// Client management keeps lists usable after partial backend failures and validates Super Admin restore-point names.

const list = (items) => ({ items, total: items.length });
const TERMINALS = [
  { ID: 'terminal-A', Name: 'Client One', MAC: 'AABBCCDDEE01', IP: '192.168.50.101', GroupID: 'g-1', IsSuper: false, State: 'online' },
  { ID: 'terminal-B', Name: 'Client Two', MAC: 'AABBCCDDEE02', IP: '192.168.50.102', GroupID: 'g-1', IsSuper: true, State: 'offline' },
  { ID: 'terminal-C', Name: 'Art Room Client', MAC: 'AABBCCDDEE03', IP: '192.168.60.101', GroupID: 'g-2', IsSuper: false, State: 'offline' },
  { ID: 'terminal-D', Name: 'Running Super Admin', MAC: 'AABBCCDDEE04', IP: '192.168.50.104', GroupID: 'g-1', IsSuper: true, State: 'online' },
];
const GROUPS = [
  { ID: 'g-1', Name: 'Classroom A', SystemImageID: 'win11' },
  { ID: 'g-2', Name: 'Art Classroom', SystemImageID: 'win11' },
];

function stubApi(overrides = {}) {
  const base = {
    listTerminals: async () => list(TERMINALS),
    listGroups: async () => list(GROUPS),
    listImages: async () => list([{ ID: 'win11', Name: 'Win 11', OSType: 'windows', State: 'normal' }]),
    listDriverBundles: async () => list([]),
    listTasks: async () => list([]),
    listAlarms: async () => list([]),
    listPools: async () => list([]),
    listClusterNodes: async () => list([]),
    superDisks: async () => list([]),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageTerminals/></ConfirmProvider></StoreProvider>,
);

// Show StorageServerID column in clusters but omit it on a standalone host.
describe('Client storage location', () => {
  const CLUSTER = [
    { id: 'n1', ip: '10.0.0.3', ha_state: 'active', online: true },
    { id: 'n2', ip: '10.0.0.4', ha_state: 'standby', online: true },
  ];
  afterEach(() => vi.restoreAllMocks());

  it('Shows client storage node in cluster mode', async () => {
    stubApi({
      listClusterNodes: async () => list(CLUSTER),
      listTerminals: async () => list([
        { ...TERMINALS[0], StorageServerID: 'n2' },
        { ...TERMINALS[1], StorageServerID: null },
      ]),
    });
    renderPage();
    const row = (await screen.findByText('Client One')).closest('tr');
    expect(row.textContent).toContain('10.0.0.4');
    expect(screen.getAllByText('Node').length).toBeGreaterThan(0);
  });

  it('Omits node column on standalone hosts', async () => {
    stubApi({ listClusterNodes: async () => list([{ id: 'n1', ip: '10.0.0.3', ha_state: 'active', online: true }]) });
    renderPage();
    await screen.findByText('Client One');
    expect(screen.queryByText('Node')).toBeNull();
  });
});

describe('Client Management', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it('Lists clients and groups', async () => {
    renderPage();
    expect(await screen.findByText('Client One')).toBeTruthy();
    expect(screen.getByText('Art Room Client')).toBeTruthy();
    expect(screen.getAllByText('Classroom A').length).toBeGreaterThan(0);
  });

  // Treat legacy unknown status as Offline; only Online/Offline filters exist.
  it('Treats legacy unknown state as Offline', async () => {
    stubApi({ listTerminals: async () => list([...TERMINALS,
      { ID: 'terminal-E', Name: 'Legacy Client', MAC: 'AABBCCDDEE05', IP: '192.168.50.105', GroupID: 'g-1', IsSuper: false, State: 'unknown' }]) });
    renderPage();
    expect(await screen.findByText('Legacy Client')).toBeTruthy();
    expect(screen.queryByText('Unknown')).toBeNull();
    expect(screen.getByRole('button', { name: /Offline\s*3/ })).toBeTruthy();
  });

  // Fetch lists independently: failure to load groups cannot discard the client list.
  it('Keeps client list when group API fails', async () => {
    stubApi({ listGroups: async () => { throw new Error('500'); } });
    renderPage();
    expect(await screen.findByText('Client One')).toBeTruthy();
    expect(screen.getByText('Client Two')).toBeTruthy();
  });

  // A client API failure must show an error rather than a misleading empty list.
  it('Reports client API errors rather than empty state', async () => {
    stubApi({ listTerminals: async () => { throw new Error('Backend unavailable'); } });
    renderPage();
    expect(await screen.findByText(/Backend unavailable|Load failed/)).toBeTruthy();
  });

  it('Filters clients by name and MAC', async () => {
    renderPage();
    await screen.findByText('Client One');

    const box = screen.getByPlaceholderText(/Search/);
    await userEvent.type(box, 'Art');
    await waitFor(() => expect(screen.queryByText('Client One')).toBeNull());
    expect(screen.getByText('Art Room Client')).toBeTruthy();

    await userEvent.clear(box);
    await userEvent.type(box, 'AABBCCDDEE01');
    await waitFor(() => expect(screen.queryByText('Art Room Client')).toBeNull());
    expect(screen.getByText('Client One')).toBeTruthy();
  });

  // Validate restore-point name before requesting Super Admin shutdown.
  it('Requires restore-point name before Super Admin shutdown', async () => {
    const stopSuper = vi.fn(async () => ({ task_id: 'task-super_stop-1' }));
    stubApi({ stopSuper });
    renderPage();
    await screen.findByText('Client Two');

    await userEvent.click(screen.getByText('Client Two'));
    await userEvent.click(await screen.findByRole('button', { name: /Save Restore Point|Super Admin Shutdown/ }));

    await userEvent.click(await screen.findByRole('button', { name: /Save Restore Point|Save Restore Point/ }));
    expect(stopSuper).not.toHaveBeenCalled();

    await userEvent.type(screen.getByPlaceholderText('2026Q2-update'), '  Office installed  ');
    await userEvent.click(screen.getByRole('button', { name: /Save Restore Point|Save Restore Point/ }));
    // Trim restore-point names to avoid accidental duplicates.
    await waitFor(() => expect(stopSuper).toHaveBeenCalledWith('terminal-B', { reduction_name: 'Office installed' }));
  });

  // Show every Super Admin data disk and submit only checked disks with valid names.
  it('Saves only selected named data disks with Super Admin shutdown', async () => {
    const stopSuper = vi.fn(async () => ({ task_id: 'task-super_stop-1' }));
    stubApi({
      stopSuper,
      listGroupDisks: async () => list([
        { ID: 'disk-d', GroupID: 'g-1', MountTarget: 'D:', ImageID: 'games', ConfigID: 'games_default' },
        { ID: 'disk-e', GroupID: 'g-1', MountTarget: 'E:', ImageID: 'docs', ConfigID: 'docs_default' },
      ]),
    });
    renderPage();
    await screen.findByText('Client Two');
    await userEvent.click(screen.getByText('Client Two'));
    await userEvent.click(await screen.findByRole('button', { name: /Save Restore Point|Super Admin Shutdown/ }));

    await userEvent.type(await screen.findByPlaceholderText('2026Q2-update'), 'sys-v2');
    // List both data disks, selecting only E: with a name.
    expect(await screen.findByLabelText('Save Data Disk D:')).toBeTruthy();
    await userEvent.click(screen.getByLabelText('Save Data Disk E:'));
    await userEvent.type(screen.getByLabelText('Data Disk E: Restore Point Name'), ' Game installed ');
    await userEvent.click(screen.getByRole('button', { name: /Save Restore Point|Save Restore Point/ }));
    await waitFor(() => expect(stopSuper).toHaveBeenCalledWith('terminal-B', {
      reduction_name: 'sys-v2',
      data_disks: [{ disk_id: 'disk-e', reduction_name: 'Game installed' }],
    }));
  });

  // Require a name for any selected data disk before submitting.
  it('Requires names on selected data disks', async () => {
    const stopSuper = vi.fn(async () => ({ task_id: 'task-super_stop-1' }));
    stubApi({
      stopSuper,
      listGroupDisks: async () => list([{ ID: 'disk-d', GroupID: 'g-1', MountTarget: 'D:', ImageID: 'games', ConfigID: 'games_default' }]),
    });
    renderPage();
    await screen.findByText('Client Two');
    await userEvent.click(screen.getByText('Client Two'));
    await userEvent.click(await screen.findByRole('button', { name: /Save Restore Point|Super Admin Shutdown/ }));
    await userEvent.type(await screen.findByPlaceholderText('2026Q2-update'), 'sys-v2');
    await userEvent.click(await screen.findByLabelText('Save Data Disk D:'));
    await userEvent.click(screen.getByRole('button', { name: /Save Restore Point|Save Restore Point/ }));
    expect(stopSuper).not.toHaveBeenCalled();
  });
});

// Saving while online would time out after two minutes; disable Super Admin actions and explain shutdown requirement.
it('Disables Super Admin actions while client is online', async () => {
  const stopSuper = vi.fn(async () => ({ task_id: 'task-super_stop-1' }));
  stubApi({ stopSuper });
  renderPage();
  await screen.findByText('Running Super Admin');
  await userEvent.click(screen.getByText('Running Super Admin'));

  const save = await screen.findByRole('button', { name: /Save Restore Point/ });
  expect(save.disabled).toBe(true);
  expect(screen.getByRole('button', { name: 'Disable Super Admin' }).disabled).toBe(true);
  expect(screen.getByText(/shut down/)).toBeTruthy();
  await userEvent.click(save);
  expect(stopSuper).not.toHaveBeenCalled();
});

it('Disables client type editor while online', async () => {
  stubApi({});
  renderPage();
  const row = (await screen.findByText('Client One')).closest('tr');   // terminal-A: Online、普通
  await userEvent.click(within(row).getByRole('button', { name: 'Edit' }));
  expect((await screen.findByRole('button', { name: 'Super Admin' })).disabled).toBe(true);
  expect(screen.getByRole('button', { name: 'Regular Client' }).disabled).toBe(true);
  expect(screen.getByText(/shut down/)).toBeTruthy();
});

// Disabling Super Admin discards its writable disk; warn about irreversible unsaved changes.
it('Confirms loss of unsaved data before disabling Super Admin', async () => {
  const disableSuper = vi.fn(async () => ({ ID: 'terminal-B', IsSuper: false }));
  stubApi({ disableSuper });
  renderPage();
  await screen.findByText('Client Two');

  await userEvent.click(screen.getByText('Client Two'));
  await userEvent.click(await screen.findByRole('button', { name: 'Disable Super Admin' }));
  let dialog = (await screen.findByText(/Discard.*unsaved|cannot be undone/)).closest('.card');
  await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
  expect(disableSuper).not.toHaveBeenCalled();

  await userEvent.click(screen.getByRole('button', { name: 'Disable Super Admin' }));
  dialog = (await screen.findByText(/Discard.*unsaved|cannot be undone/)).closest('.card');
  await userEvent.click(within(dialog).getByRole('button', { name: /Discard|Confirm/ }));
  await waitFor(() => expect(disableSuper).toHaveBeenCalledWith('terminal-B'));
});

// Changing client type from Super Admin to Regular also deletes its writable clone.
it('Confirms downgrade to Regular Client', async () => {
  const updateTerminal = vi.fn(async () => ({ ID: 'terminal-B', IsSuper: false }));
  stubApi({ updateTerminal });
  renderPage();
  const row = (await screen.findByText('Client Two')).closest('tr');
  await userEvent.click(within(row).getByRole('button', { name: 'Edit' }));
  await userEvent.click(await screen.findByRole('button', { name: 'Regular Client' }));
  await userEvent.click(screen.getByRole('button', { name: 'Save' }));

  const dialog = (await screen.findByText(/cannot be undone/)).closest('.card');
  await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
  expect(updateTerminal).not.toHaveBeenCalled();

  await userEvent.click(screen.getByRole('button', { name: 'Save' }));
  await userEvent.click(within((await screen.findByText(/cannot be undone/)).closest('.card')).getByRole('button', { name: /Discard|Confirm/ }));
  await waitFor(() => expect(updateTerminal).toHaveBeenCalledWith('terminal-B', expect.objectContaining({ is_super: false })));
});

it('Clears incompatible IP when moving clients to another group', async () => {
  const updateTerminal = vi.fn(async () => ({ ID: 'terminal-C' }));
  stubApi({
    updateTerminal,
    listGroups: async () => list([
      { ID: 'g-1', Name: 'Classroom A', SystemImageID: 'win11', StartIP: '192.168.50.100', ClientMax: 20 },
      { ID: 'g-2', Name: 'Art Classroom', SystemImageID: 'win11', StartIP: '192.168.60.100', ClientMax: 20 },
    ]),
  });
  renderPage();
  const row = (await screen.findByText('Art Room Client')).closest('tr');
  await userEvent.click(within(row).getByRole('button', { name: 'Edit' }));
  const ip = await screen.findByDisplayValue('192.168.60.101');
  const dialog = ip.closest('.card');
  await userEvent.selectOptions(within(dialog).getByDisplayValue('Art Classroom'), 'g-1');
  expect(ip.value).toBe('');
  expect(within(dialog).getByText(/Leave blank for automatic allocation in the new group subnet/)).toBeTruthy();

  await userEvent.selectOptions(within(dialog).getByDisplayValue('Classroom A'), 'g-2');
  expect(ip.value).toBe('192.168.60.101');

  await userEvent.selectOptions(within(dialog).getByDisplayValue('Art Classroom'), 'g-1');
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(updateTerminal).toHaveBeenCalledWith('terminal-C', expect.objectContaining({ ip: '', group_id: 'g-1' })));
});

it('Defaults to ten clients per page with adjustable page size', async () => {
  const many = Array.from({ length: 12 }, (_, i) => ({ ID: `t-${i}`, Name: `Client${String(i).padStart(2, '0')}`, MAC: `AABBCCDDEE${String(i).padStart(2, '0')}`, IP: `192.168.50.${110 + i}`, GroupID: 'g-1', IsSuper: false, State: 'offline' }));
  stubApi({ listTerminals: async () => list(many) });
  renderPage();
  expect(await screen.findByText('Total 12 entries')).toBeTruthy();
  expect(screen.getByText('Client09')).toBeTruthy();
  expect(screen.queryByText('Client10')).toBeNull();
  await userEvent.selectOptions(screen.getByLabelText('Rows per page'), '20');
  expect(screen.getByText('Client11')).toBeTruthy();
});

// Renaming a client without changing Super Admin state requires no confirmation.
it('Does not confirm non-type edits to Super Admin clients', async () => {
  const updateTerminal = vi.fn(async () => ({ ID: 'terminal-B', IsSuper: true }));
  stubApi({ updateTerminal });
  renderPage();
  const row = (await screen.findByText('Client Two')).closest('tr');
  await userEvent.click(within(row).getByRole('button', { name: 'Edit' }));
  await userEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(updateTerminal).toHaveBeenCalledWith('terminal-B', expect.objectContaining({ is_super: true })));
  expect(screen.queryByText(/cannot be undone/)).toBeNull();
});


// Super Admin data disks support online publishing; show pending written bytes.
it('Publishes Super Admin data disks online', async () => {
  const publishDataDisk = vi.fn(async () => ({ task_id: 'task-publish-1' }));
  stubApi({
    publishDataDisk,
    superDisks: async () => list([
      { disk_id: 'gd-1', lun: 1, mount_target: 'D:', config_name: 'Games Disk', current_name: '2026-09-14', written: 13100000000, ready: true },
    ]),
  });
  renderPage();
  await screen.findByText('Running Super Admin');
  await userEvent.click(screen.getByText('Running Super Admin'));

  expect(await screen.findByText('2026-09-14')).toBeTruthy();   // Current published point
  expect(screen.getByText(/1[23]\.\d+ GiB/)).toBeTruthy(); // Unpublished changes
  await userEvent.click(screen.getByRole('button', { name: /Save and Publish/ }));

  await userEvent.type(await screen.findByPlaceholderText(/Game update|2026/), 'Game update installed');
  await userEvent.click(screen.getByRole('button', { name: /^Publish$|Publish/ }));
  await waitFor(() => expect(publishDataDisk).toHaveBeenCalledWith('terminal-D', { disk_id: 'gd-1', name: 'Game update installed' }));
});

it('Disables publish without changes or before first boot', async () => {
  stubApi({
    superDisks: async () => list([
      { disk_id: 'gd-1', lun: 1, mount_target: 'D:', config_name: 'Games Disk', current_name: '2026-09-14', written: 0, ready: true },
      { disk_id: 'gd-2', lun: 2, mount_target: 'E:', config_name: 'Documents Disk', current_name: 'Initial', written: 0, ready: false },
    ]),
  });
  renderPage();
  await screen.findByText('Running Super Admin');
  await userEvent.click(screen.getByText('Running Super Admin'));

  const rows = await screen.findAllByRole('row');
  const d = rows.find(r => within(r).queryByText('D:'));
  const e = rows.find(r => within(r).queryByText('E:'));
  expect(within(d).getByRole('button', { name: /Save and Publish/ }).disabled).toBe(true);
  expect(within(d).getByText(/No changes/)).toBeTruthy();
  expect(within(e).getByRole('button', { name: /Save and Publish/ }).disabled).toBe(true);
  expect(within(e).getByText(/after boot/)).toBeTruthy();
});

// The dialog must clarify that saving a restore point does not disable Super Admin.
it('Clarifies that saving restore points retains Super Admin status', async () => {
  stubApi({});
  renderPage();
  await screen.findByText('Client Two');
  await userEvent.click(screen.getByText('Client Two'));
  await userEvent.click(await screen.findByRole('button', { name: /Save Restore Point/ }));
  expect(await screen.findByText(/remains Super Admin/)).toBeTruthy();
});

// Hide explanatory Super Admin help behind ?, but keep core constraints visible.
it('Keeps client type constraints visible while help is collapsed', async () => {
  stubApi({});
  renderPage();
  const row = (await screen.findByText('Art Room Client')).closest('tr');  // terminal-C：OfflineRegular Client
  await userEvent.click(within(row).getByRole('button', { name: 'Edit' }));

  expect(await screen.findByText(/Only one Super Admin can use a configuration at a time/)).toBeTruthy();
  expect(screen.queryByText(/Super Admin edits image contents/)).toBeNull();

  await userEvent.click(screen.getByRole('button', { name: 'Help for Client Type' }));
  expect(await screen.findByText(/Super Admin edits image contents/)).toBeTruthy();
});

// Selection is page-local and resets on filtering/view changes to avoid deleting unseen clients.
it('Clears selection on filters and view changes', async () => {
  stubApi();
  renderPage();
  const row = (await screen.findByText('Client One')).closest('tr');
  await userEvent.click(row.querySelector('input[type="checkbox"]'));
  expect(screen.getByText('Selected 1')).toBeTruthy();

  await userEvent.type(screen.getByPlaceholderText(/Search/), 'Art');
  await waitFor(() => expect(screen.queryByText(/Selected/)).toBeNull());

  await userEvent.clear(screen.getByPlaceholderText(/Search/));
  const row2 = (await screen.findByText('Client One')).closest('tr');
  await userEvent.click(row2.querySelector('input[type="checkbox"]'));
  expect(screen.getByText('Selected 1')).toBeTruthy();
  await userEvent.click(screen.getByRole('button', { name: 'Grid' }));
  await waitFor(() => expect(screen.queryByText(/Selected/)).toBeNull());
});
