import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageGroups } from './groups.jsx';

// Groups bind an image and configuration (using that configuration's applied restore point).
// Select state must match the visible option so downstream configurations can load.

const list = (items) => ({ items, total: items.length });
const IMAGES = [
  { ID: 'isharedisk', Name: 'isharedisk', OSType: 'windows', State: 'normal', Purpose: 'system', CreatedAt: '2026-07-30T10:00:00Z' },
  { ID: 'games', Name: 'games', OSType: 'windows', State: 'normal', Purpose: 'data', CreatedAt: '2026-08-18T10:00:00Z' },
  { ID: 'linuxdata', Name: 'linuxdata', OSType: 'linux', State: 'normal', Purpose: 'data', CreatedAt: '2026-08-18T10:00:00Z' },
];
const CONFIGS = [{ ID: 'isharedisk_default', ImageID: 'isharedisk', Name: 'default', DefaultReductionID: 'isharedisk_default_0' }];
const REDUCTIONS = [{ ID: 'isharedisk_default_0', ConfigID: 'isharedisk_default', Name: '@0', DisplayName: '0', Status: 'ready' }];
// The server's client subnet determines defaults and validates network fields.
const NETWORK = {
  interfaces: [{ name: 'ndbr0', addrs: ['192.168.50.1/24'], up: true, client: true }],
  client_iface: 'ndbr0', client_networks: ['192.168.50.1/24'], server_addrs: ['192.168.50.1'],
  boot_host: '192.168.50.1', allow_cross_subnet: false, known: true,
  suggest: { start_ip: '192.168.50.10', client_max: 30, netmask: '255.255.255.0', gateway: '' },
  groups: [],
};

function stubApi(overrides = {}) {
  const base = {
    listGroups: async () => list([]),
    listImages: async () => list(IMAGES),
    listTerminals: async () => list([]),
    listConfigs: async () => list(CONFIGS),
    listReductions: async () => list(REDUCTIONS),
    listGroupDisks: async () => list([]),
    listPools: async () => list([]),
    listTasks: async () => list([]),
    listAlarms: async () => list([]),
    getNetwork: async () => NETWORK,
    listClusterNodes: async () => list([]),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageGroups/></ConfirmProvider></StoreProvider>,
);

describe('Groups page', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it('Populates all cascading selectors when creating a group', async () => {
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /New Group/ }));

    // Ensure the selected image matches form state so configurations can resolve.
    await waitFor(() => expect(api.listConfigs).toHaveBeenCalledWith('isharedisk'));

    expect(screen.queryByText('No configurations available')).toBeNull();
    expect(await screen.findByText('default')).toBeTruthy();
  });

  it('Saves the selected image and configuration', async () => {
    const createGroup = vi.fn(async () => ({ ID: 'g-1' }));
    stubApi({ createGroup });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /New Group/ }));
    await waitFor(() => expect(api.listConfigs).toHaveBeenCalled());

    // Group Name input has no placeholder; locate it through the Field container.
    const nameField = screen.getByText(/Group Name/).parentElement.querySelector('input');
    await userEvent.type(nameField, 'Classroom A');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(createGroup).toHaveBeenCalled(), { timeout: 3000 });
    const body = createGroup.mock.calls[0][0];
    expect(body.system_image_id).toBe('isharedisk');
    expect(body.system_config_id).toBe('isharedisk_default');
    // Applied restore points belong to configurations, not individual groups.
    expect(body.system_reduction_id).toBeUndefined();
  });

  // Show boot images for system disks and OS-compatible images for data disks.
  it('Filters system and data disk image choices by purpose', async () => {
    stubApi({ listGroups: async () => list([
      { ID: 'g-1', Name: 'Classroom A', StartIP: '192.168.10.10', ClientMax: 30, Gateway: '192.168.10.1', Netmask: '255.255.255.0', SystemImageID: 'isharedisk', SystemConfigID: 'isharedisk_default', SystemReductionID: 'isharedisk_default_0' },
    ]) });
    renderPage();
    await screen.findByText('Classroom A');
    await userEvent.click(screen.getByRole('button', { name: /New Group/ }));
    const sys = await screen.findByLabelText('System Image');
    const sysOptions = [...sys.querySelectorAll('option')].map(o => o.value);
    expect(sysOptions).toContain('isharedisk');
    expect(sysOptions).not.toContain('games');

    await userEvent.click(screen.getByRole('button', { name: /Add Data Disk/ }));
    const data = await screen.findByLabelText('New Data Disk 1 Image');
    const dataOptions = [...data.querySelectorAll('option')].map(o => o.value);
    expect(dataOptions).toEqual(['games']); // Only Windows data disks
  });

  // Clearly indicate when no image can be selected before an import.
  it('Displays an empty-state message only when there are no images', async () => {
    stubApi({ listImages: async () => list([]), listConfigs: async () => list([]), listReductions: async () => list([]) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /New Group/ }));
    expect(await screen.findByText('No images available')).toBeTruthy();
  });

  // Form network defaults come from the server subnet, not hard-coded values.
  it('Uses the server client NIC for initial group network defaults', async () => {
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /New Group/ }));
    expect((await screen.findByLabelText('Starting IP')).value).toBe('192.168.50.10');
    expect(screen.getByLabelText('Subnet Mask').value).toBe('255.255.255.0');
    expect(screen.getByLabelText('Gateway').value).toBe('');
    expect(await screen.findByText(/Same Subnet/)).toBeTruthy();
  });

  // For out-of-subnet ranges, identify the NIC, disable Save and offer a suggested range.
  it('Blocks invalid subnets and offers a corrective action', async () => {
    const getNetwork = vi.fn(async () => NETWORK);
    const createGroup = vi.fn(async () => ({ ID: 'g-1' }));
    stubApi({ getNetwork, createGroup });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /New Group/ }));
    const start = await screen.findByLabelText('Starting IP');
    await userEvent.clear(start);
    await userEvent.type(start, '192.168.10.50');
    const status = await screen.findByText(/Outside the server client subnet/);
    expect(status.parentElement.textContent).toContain('ndbr0');
    expect(screen.getByRole('button', { name: 'Save' }).disabled).toBe(true);

    await userEvent.click(screen.getByRole('button', { name: /Use 192\.168\.50\.10/ }));
    await waitFor(() => expect(screen.getByLabelText('Starting IP').value).toBe('192.168.50.10'));
    expect(await screen.findByText(/Same Subnet/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Save' }).disabled).toBe(false);
  });

  // With cross-subnet support enabled, classify the range as relayed and allow gateway tests.
  it('Shows relay help and tests gateway when cross-subnet is enabled', async () => {
    const probeNetwork = vi.fn(async () => ({ ip: '192.168.10.1', reachable: true, rtt_ms: 0.4 }));
    stubApi({ getNetwork: async () => ({ ...NETWORK, allow_cross_subnet: true }), probeNetwork });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /New Group/ }));
    const start = await screen.findByLabelText('Starting IP');
    await userEvent.clear(start);
    await userEvent.type(start, '192.168.10.50');
    expect(await screen.findByText(/Cross-subnet · DHCP Relay/)).toBeTruthy();
    const gw = screen.getByLabelText('Gateway (Relay Address)');
    await userEvent.type(gw, '192.168.10.1');
    await userEvent.click(screen.getByRole('button', { name: 'Test' }));
    await waitFor(() => expect(probeNetwork).toHaveBeenCalledWith('192.168.10.1'));
    expect(await screen.findByText(/Reachable/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Save' }).disabled).toBe(false);
  });

  // Flag misplaced groups and preview each client's new IP before migration.
  it('Moves existing out-of-subnet groups to the server subnet', async () => {
    const group = { ID: 'g-1', Name: 'vmdk', StartIP: '192.168.10.50', ClientMax: 30, Gateway: '192.168.10.1', Netmask: '255.255.255.0', DNS1: '223.5.5.5', SystemImageID: 'isharedisk', SystemConfigID: 'isharedisk_default' };
    const previewGroupNetwork = vi.fn(async () => ({ status: 'same', terminals: [{ id: 't1', name: '001', mac: '00505625442C', old_ip: '192.168.10.50', new_ip: '192.168.50.50' }] }));
    const updateGroup = vi.fn(async () => group);
    stubApi({
      listGroups: async () => list([group]),
      getNetwork: async (clientMax) => ({ ...NETWORK, suggest: { start_ip: '192.168.50.50', client_max: clientMax || 30, netmask: '255.255.255.0', gateway: '' }, groups: [{ id: 'g-1', name: 'vmdk', start_ip: '192.168.10.50', client_max: 30, status: 'blocked' }] }),
      previewGroupNetwork, updateGroup,
    });
    renderPage();
    expect(await screen.findByText('Blocked')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: /Move to Server Subnet/ }));
    await waitFor(() => expect(previewGroupNetwork).toHaveBeenCalledWith('g-1', expect.objectContaining({ start_ip: '192.168.50.50', client_max: 30 })));
    expect(await screen.findByText('192.168.50.50')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Confirm Change' }));
    await waitFor(() => expect(updateGroup).toHaveBeenCalledWith('g-1', expect.objectContaining({ start_ip: '192.168.50.50', gateway: '', system_image_id: 'isharedisk' })));
  });

  it('Provides storage node selector for multi-node groups and submits storage_server_id', async () => {
    let sent = null;
    stubApi({
      listClusterNodes: async () => list([
        { id: 'node-a', name: 'Server A', online: true, ha_state: 'active' },
        { id: 'node-b', name: 'Server B', online: true, ha_state: 'standby' },
      ]),
      createGroup: async (body) => { sent = body; return { ID: 'g1', ...body }; },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New Group' }));
    const picker = await screen.findByLabelText('Storage Node');
    // Automatic balancing is the default; placement needs no advance planning.
    expect(picker.value).toBe('');
    expect(Array.from(picker.options).some(o => /Automatic balancing/.test(o.text))).toBe(true);
    await user.selectOptions(picker, 'node-b');
    await user.type(screen.getByLabelText('Group Name'), 'Placement Group');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(sent).not.toBeNull());
    expect(sent.storage_server_id).toBe('node-b');
  });

  it('Omits storage node selector on a standalone host', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New Group' }));
    await screen.findByLabelText('Group Name');
    expect(screen.queryByLabelText('Storage Node')).toBeNull();
  });

  // Prepopulate editable DNS servers so clients can resolve names.
  it('Prepopulates two DNS addresses in preferred order', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New Group' }));
    await screen.findByLabelText('Group Name');

    const dns1 = screen.getByLabelText('DNS1');
    const dns2 = screen.getByLabelText('DNS2');
    expect(dns1.value).toBe('114.114.114.114');
    expect(dns2.value).toBe('223.5.5.5');
  });

  // Warn that renewed DHCP leases on active Windows clients can break iSCSI connectivity.
  it('Previews running clients and network-change consequences', async () => {
    const user = userEvent.setup();
    // Ensure test group initially passes network validation.
    stubApi({
      listGroups: async () => list([
        { ID: 'g-1', Name: 'Classroom A', StartIP: '192.168.50.100', ClientMax: 10, Gateway: '192.168.50.1', Netmask: '255.255.255.0', SystemImageID: 'isharedisk', SystemConfigID: 'isharedisk_default' },
      ]),
    });
    vi.spyOn(api, 'previewGroupNetwork').mockResolvedValue({
      status: 'ok', online_count: 1,
      terminals: [
        { id: 't1', name: 'Client 01', mac: 'AABBCCDDEE01', old_ip: '192.168.50.100', new_ip: '192.168.50.150', online: true },
        { id: 't2', name: 'Client 02', mac: 'AABBCCDDEE02', old_ip: '192.168.50.101', new_ip: '192.168.50.151', online: false },
      ],
    });
    renderPage();
    await user.click((await screen.findByText('Classroom A')).closest('tr'));
    await user.click(await screen.findByRole('button', { name: /Change subnet|Edit/ }));
    await screen.findByLabelText('Group Name');
    const start = screen.getByLabelText('Starting IP');
    await user.clear(start);
    await user.type(start, '192.168.50.150');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    const dlg = await screen.findByText(/ client addresses will change/);
    const card = dlg.closest('.card');
    // Highlight the running client.
    const row = Array.from(card.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes('AABBCCDDEE01'));
    expect(within(row).getByText(/Running|Online/)).toBeTruthy();
    // Describe lease renewal consequences, not merely next-boot effects.
    expect(card.textContent).toMatch(/lease renewal|disconnect|disconnect/);
  });

  // Configure data disks within group Create/Edit rather than as a separate step.
  describe('Configure data disks with groups', () => {
    const DATA_CONFIGS = { games: [{ ID: 'games_default', ImageID: 'games', Name: 'default' }] };
    const configsOf = async (img) => list(DATA_CONFIGS[img] || CONFIGS);
    const GROUP = { ID: 'g-1', Name: 'Classroom A', StartIP: '192.168.50.100', ClientMax: 10, Gateway: '192.168.50.1', Netmask: '255.255.255.0', SystemImageID: 'isharedisk', SystemConfigID: 'isharedisk_default' };

    it('Adds a data disk when creating a group and chooses the next free drive letter', async () => {
      const createGroup = vi.fn(async () => ({ ID: 'g-new' }));
      const createGroupDisk = vi.fn(async () => ({}));
      stubApi({ createGroup, createGroupDisk, listConfigs: configsOf });
      renderPage();
      await userEvent.click(await screen.findByRole('button', { name: /New Group/ }));
      await userEvent.type(await screen.findByLabelText('Group Name'), 'Gaming Zone');
      await userEvent.click(screen.getByRole('button', { name: /Add Data Disk/ }));
      expect(screen.getByLabelText('New Data Disk 1 Mount Target').value).toBe('D:');
      await waitFor(() => expect(screen.getByLabelText('New Data Disk 1 Configuration').value).toBe('games_default'));
      await userEvent.click(screen.getByRole('button', { name: 'Save' }));
      await waitFor(() => expect(createGroupDisk).toHaveBeenCalledWith('g-new', { mount_target: 'D:', image_id: 'games', config_id: 'games_default' }));
      expect(createGroup).toHaveBeenCalledTimes(1);
    });

    it('Edits group data disks by removing or adding disks', async () => {
      const updateGroup = vi.fn(async () => ({}));
      const deleteGroupDisk = vi.fn(async () => null);
      const createGroupDisk = vi.fn(async () => ({}));
      stubApi({
        listGroups: async () => list([GROUP]), listConfigs: configsOf, updateGroup, deleteGroupDisk, createGroupDisk,
        listGroupDisks: async () => list([{ ID: 'gd-1', GroupID: 'g-1', MountTarget: 'D:', ImageID: 'games', ConfigID: 'games_default' }]),
      });
      renderPage();
      await screen.findByText('Classroom A');
      // Show data disks on the group card, without a separate Data Disks button.
      await waitFor(() => expect(screen.getByText('D:')).toBeTruthy());
      expect(screen.queryByRole('button', { name: /^Data Disks$/ })).toBeNull();

      await userEvent.click(screen.getByRole('button', { name: /Edit/ }));
      await screen.findByLabelText('Group Name');
      await userEvent.click(screen.getByRole('button', { name: /Add Data Disk/ }));
      expect(screen.getByLabelText('New Data Disk 1 Mount Target').value).toBe('E:');
      await userEvent.click(screen.getByRole('button', { name: 'Remove Data Disk D:' }));
      await waitFor(() => expect(screen.getByLabelText('New Data Disk 1 Configuration').value).toBe('games_default'));
      await userEvent.click(screen.getByRole('button', { name: 'Save' }));
      await waitFor(() => expect(createGroupDisk).toHaveBeenCalledWith('g-1', { mount_target: 'E:', image_id: 'games', config_id: 'games_default' }));
      expect(updateGroup).toHaveBeenCalled();
      expect(deleteGroupDisk).toHaveBeenCalledWith('gd-1');
    });

    it('Keeps network details in dialogs and disk help behind a tooltip', async () => {
      stubApi({
        listGroups: async () => list([GROUP]), listConfigs: configsOf,
        listGroupDisks: async () => list([{ ID: 'gd-1', GroupID: 'g-1', MountTarget: 'D:', ImageID: 'games', ConfigID: 'games_default' }]),
      });
      renderPage();
      await screen.findByText('Classroom A');
      await waitFor(() => expect(screen.getByText('D:')).toBeTruthy());
      expect(screen.queryByText('Subnet Mask')).toBeNull();
      await userEvent.click(screen.getByRole('button', { name: /Edit/ }));
      await screen.findByLabelText('Group Name');
      expect(screen.queryByText(/Additional disks mounted alongside the system disk/)).toBeNull();
      await userEvent.click(screen.getByRole('button', { name: 'Data Disk Help' }));
      expect(screen.getByText(/Additional disks mounted alongside the system disk/)).toBeTruthy();
    });

    // Show labeled Online/Assigned/Capacity statistics without duplicating numbers in buttons.
    it('Shows Online, Assigned and Capacity, highlighting full groups', async () => {
      stubApi({
        listGroups: async () => list([{ ...GROUP, ClientMax: 2 }]), listConfigs: configsOf,
        listTerminals: async () => list([
          { ID: 't1', Name: '01', MAC: 'AA', GroupID: 'g-1', State: 'online' },
          { ID: 't2', Name: '02', MAC: 'BB', GroupID: 'g-1', State: 'offline' },
        ]),
      });
      renderPage();
      const card = (await screen.findByText('Classroom A')).closest('.card');
      const stat = (label) => within(card).getByText(label).parentElement.textContent;
      await waitFor(() => expect(stat('Online')).toBe('1Online'));
      expect(stat('Assigned')).toBe('2Assigned');
      expect(stat('Capacity')).toBe('2Capacity');
      expect(within(card).getByText('Capacity').parentElement.getAttribute('title')).toMatch(/full/);
      expect(within(card).getByRole('button', { name: /View Clients/ })).toBeTruthy();
    });

    it('Retries failed disk creation without recreating the group', async () => {
      const createGroup = vi.fn(async () => ({ ID: 'g-new' }));
      const updateGroup = vi.fn(async () => ({}));
      let fail = true;
      const createGroupDisk = vi.fn(async () => { if (fail) throw new Error('Mount target already exists'); return {}; });
      stubApi({ createGroup, updateGroup, createGroupDisk, listConfigs: configsOf });
      renderPage();
      await userEvent.click(await screen.findByRole('button', { name: /New Group/ }));
      await userEvent.type(await screen.findByLabelText('Group Name'), 'Gaming Zone');
      await userEvent.click(screen.getByRole('button', { name: /Add Data Disk/ }));
      await waitFor(() => expect(screen.getByLabelText('New Data Disk 1 Configuration').value).toBe('games_default'));
      await userEvent.click(screen.getByRole('button', { name: 'Save' }));
      expect(await screen.findByText(/Group created; data disk D: could not be added/)).toBeTruthy();
      expect(screen.getByText('Edit Group')).toBeTruthy();

      fail = false;
      await userEvent.click(screen.getByRole('button', { name: 'Save' }));
      await waitFor(() => expect(createGroupDisk).toHaveBeenCalledTimes(2));
      expect(createGroup).toHaveBeenCalledTimes(1);
      expect(updateGroup).toHaveBeenCalled();
    });
  });
});
