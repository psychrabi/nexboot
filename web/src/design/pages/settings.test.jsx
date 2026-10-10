import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageSystemParams } from './settings.jsx';

// Cross-subnet network settings require confirmation; disabling an in-use feature must show the backend rejection.

const list = (items) => ({ items, total: items.length });
const NETWORK = {
  interfaces: [
    { name: 'ndbr0', addrs: ['192.168.50.1/24'], up: true, client: true },
    { name: 'wlo1', addrs: ['192.168.124.56/24'], up: true, client: false },
  ],
  client_iface: 'ndbr0', client_iface_setting: '', client_networks: ['192.168.50.1/24'], server_addrs: ['192.168.50.1', '192.168.124.56'],
  boot_host: '192.168.50.1', allow_cross_subnet: false, known: true,
  suggest: { start_ip: '192.168.50.10', client_max: 30, netmask: '255.255.255.0', gateway: '' },
  groups: [
    { id: 'g-1', name: 'Class A', start_ip: '192.168.50.50', client_max: 30, status: 'same' },
    { id: 'g-2', name: 'vmdk', start_ip: '192.168.10.50', client_max: 30, status: 'blocked' },
  ],
};

function stubApi(overrides = {}) {
  const base = {
    getSettings: async () => ({ import_dir: '/tank/imports', default_import_dir: '/tank/imports' }),
    getNetwork: async () => NETWORK,
    saveNetwork: async (body) => ({ ...NETWORK, ...body }),
    listTasks: async () => list([]),
    listAlarms: async () => list([]),
    listPools: async () => list([]),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageSystemParams/></ConfirmProvider></StoreProvider>,
);

describe('System Settings · Client Network', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it("Shows client NIC, subnet and each group's network classification", async () => {
    renderPage();
    expect(await screen.findByText('Client Network')).toBeTruthy();
    const select = screen.getByLabelText('Client Interface');
    // Without a selection, auto-detect the network interface and show it.
    expect(select.value).toBe('');
    expect(select.selectedOptions[0].textContent).toContain('ndbr0');
    expect(screen.getAllByText(/192\.168\.50\.1\/24/).length).toBeGreaterThan(0);
    expect(screen.getByText('vmdk')).toBeTruthy();
    expect(screen.getByText('Unreachable')).toBeTruthy();
  });

  // Contextual help follows labels, collapsed until the operator clicks ?.
  it('Keeps network help collapsed until opened', async () => {
    renderPage();
    expect(await screen.findByText('Client Network')).toBeTruthy();
    expect(screen.queryByText(/The interface on which dnsmasq assigns client IPs/)).toBeNull();
    const q = screen.getByRole('button', { name: 'Help for Client Interface' });
    await userEvent.click(q);
    // Help appears beside the label and ? button, not below the input.
    expect(q.parentElement.textContent).toMatch(/The interface on which dnsmasq assigns client IPs/);
    expect(screen.getByRole('button', { name: 'Help for Client Subnet' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Help for Cross-subnet Groups' })).toBeTruthy();
  });

  it('Requires confirmation before enabling cross-subnet groups', async () => {
    const saveNetwork = vi.fn(async (body) => ({ ...NETWORK, ...body }));
    stubApi({ saveNetwork });
    renderPage();
    await screen.findByText('Client Network');
    await userEvent.click(screen.getByLabelText('Allow cross-subnet groups (DHCP relay)'));
    // Before saving, the confirmation explains the switch configuration requirements.
    expect(saveNetwork).not.toHaveBeenCalled();
    expect(await screen.findByText(/groups may use a different subnet than the server/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Enable' }));
    await waitFor(() => expect(saveNetwork).toHaveBeenCalledWith({ client_iface: '', allow_cross_subnet: true }));
  });

  it("Shows the backend's original rejection when disabling fails", async () => {
    const saveNetwork = vi.fn(async () => { throw new Error('One cross-subnet group remains: vmdk. Move it to the server subnet before disabling cross-subnet groups.'); });
    stubApi({ getNetwork: async () => ({ ...NETWORK, allow_cross_subnet: true }), saveNetwork });
    renderPage();
    await screen.findByText('Client Network');
    await userEvent.click(screen.getByLabelText('Allow cross-subnet groups (DHCP relay)'));
    await waitFor(() => expect(saveNetwork).toHaveBeenCalledWith({ client_iface: '', allow_cross_subnet: false }));
    expect((await screen.findAllByText(/One cross-subnet group remains: vmdk/)).length).toBeGreaterThan(0);
  });
});
