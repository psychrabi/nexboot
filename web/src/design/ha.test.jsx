import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../lib/api.js';
import { StoreProvider } from './store.jsx';
import { ConfirmProvider } from './overlay.jsx';
import { HACard, StandbyBanner } from './ha.jsx';

// The HA card shows node role, peer replication and planned switchover; the standby banner guides users
// to the active node before backend writes fail with 503.

const HA_SINGLE = { node_id: 'b7ef', role: 'active', epoch: 0, peer_url: '', peer_reachable: false };
// The active node's peer_url is its own VIP; list other nodes from the node roster.
const HA_ACTIVE = {
  node_id: 'b7ef', role: 'active', epoch: 2,
  peer_url: 'http://192.168.50.250:8080', peer_reachable: true, peer_role: 'active', peer_epoch: 2,
  rate_mbps: 100, rate_source: 'default',
};
const NODES = { items: [
  { id: 'b7ef', ip: '192.168.50.10', ha_state: 'active', online: true },
  { id: 'n2', ip: '192.168.50.11', ha_state: 'standby', online: true },
  { id: 'n3', ip: '192.168.50.12', ha_state: 'standby', online: false },
] };
const HA_STANDBY = {
  node_id: 'node-b', role: 'standby', epoch: 1,
  peer_url: 'http://192.168.50.10:8080', peer_reachable: true, peer_role: 'active', peer_epoch: 2,
};
const REPL = { targets: [{ target: 'http://192.168.50.11:8080', kind: 'primary-serve', last_snapshot: 'rep-9', lag_seconds: 12, last_error: '' }] };

function stubApi(overrides = {}) {
  const base = {
    getHA: async () => HA_SINGLE,
    getReplication: async () => ({ targets: [] }),
    plannedSwitch: async () => ({}),
    setHARate: async () => ({}),
    createCluster: async () => ({}),
    listClusterNodes: async () => NODES,
    listTasks: async () => ({ items: [], total: 0 }),
    listAlarms: async () => ({ items: [], total: 0 }),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

const wrap = (ui) => render(
  <StoreProvider><ConfirmProvider>{ui}</ConfirmProvider></StoreProvider>
);

describe('HACard', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it('Reports node-list failures rather than implying an empty standby list', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, listClusterNodes: async () => { throw new Error('Timeout'); } });
    wrap(<HACard/>);
    const others = await screen.findByTestId('ha-others');
    await waitFor(() => expect(others.textContent).toContain('Cannot retrieve node list'));
    expect(others.textContent).not.toContain('None');
  });

  it('Does not label all-offline peers as healthy standby nodes', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, listClusterNodes: async () => ({ items: [NODES.items[0], { ...NODES.items[2] }] }) });
    wrap(<HACard/>);
    const others = await screen.findByTestId('ha-others');
    await waitFor(() => expect(others.textContent).toContain('Offline'));
    expect(others.parentElement.textContent).not.toContain('Standby');
  });

  it('Standalone: explains role, offers cluster creation and omits switchover', async () => {
    wrap(<HACard/>);
    await waitFor(() => expect(screen.getByText(/is not part of a cluster/)).toBeTruthy());
    expect(screen.getByRole('button', { name: /Create Cluster/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Planned Switchover/ })).toBeNull();
  });

  it('Active: shows role, peers and replication lag; requires confirmation before switchover', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, getReplication: async () => REPL });
    const user = userEvent.setup();
    wrap(<HACard/>);
    await waitFor(() => expect(screen.getByText('Active')).toBeTruthy());
    expect(screen.getByText(/192\.168\.50\.11/)).toBeTruthy();
    expect(screen.getByText(/12 seconds ago/)).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'Planned Switchover' }));
    // Explain consequences in operator-friendly language in the confirmation.
    expect(await screen.findByText(/Running regular clients will restart once/)).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Switch' }));
    await waitFor(() => expect(api.plannedSwitch).toHaveBeenCalled());
  });

  // Replication status follows the section heading; rate controls appear beneath it.
  it('Active: shows replication timestamp and rate limit together', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, getReplication: async () => REPL });
    wrap(<HACard/>);
    const title = await screen.findByText('Catalogue Replication', { exact: false });
    const col = title.closest('div').parentElement;
    await waitFor(() => expect(title.textContent).toMatch(/12 seconds ago/));
    expect(within(col).getByLabelText('Replication Rate Limit')).toBeTruthy();
  });

  it('Active: supports changing replication limit, zero means unlimited', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, getReplication: async () => REPL });
    const user = userEvent.setup();
    wrap(<HACard/>);
    await waitFor(() => expect(screen.getByText('Active')).toBeTruthy());
    const input = screen.getByLabelText('Replication Rate Limit');
    expect(input.value).toBe('100');
    await user.clear(input);
    await user.type(input, '0');
    await user.click(screen.getByRole('button', { name: 'Save Limit' }));
    await waitFor(() => expect(api.setHARate).toHaveBeenCalledWith(0));
  });

  // Display local and peer IPs. Keep term number and node ID in diagnostic details.
  it('Active: shows local IP, hides term and node ID, and collapses help by default', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, getReplication: async () => REPL });
    const user = userEvent.setup();
    wrap(<HACard/>);
    const self = await screen.findByTestId('ha-self');
    await waitFor(() => expect(self.textContent).toContain('192.168.50.10'));
    expect(self.textContent).not.toMatch(/epoch|b7ef/);
    // Show role and planned switchover beside the local IP.
    expect(within(self).getByText('Active')).toBeTruthy();
    expect(within(self).getByRole('button', { name: 'Planned Switchover' })).toBeTruthy();
    expect(screen.queryByText(/Before patching/)).toBeNull();
    await user.click(screen.getByRole('button', { name: 'About Planned Switchover' }));
    expect(screen.getByText(/transfer the active role to a standby/)).toBeTruthy();
  });

  it('Active: lists peer status without treating VIP as a peer', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, getReplication: async () => REPL });
    wrap(<HACard/>);
    await waitFor(() => expect(screen.getByText(/192\.168\.50\.11/)).toBeTruthy());
    const col = screen.getByTestId('ha-others');
    expect(col.textContent).toContain('192.168.50.11');
    expect(col.textContent).toContain('192.168.50.12');
    expect(col.textContent).toContain('Offline');
    expect(col.textContent).not.toContain('192.168.50.10');
    expect(screen.queryByText(/192\.168\.50\.250/)).toBeNull();
  });

  // Cluster tokens secure internal node APIs and must not be displayed; Add Node needs no token.
  it('Active: never displays cluster token', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, getReplication: async () => REPL });
    wrap(<HACard/>);
    await waitFor(() => expect(screen.getByText(/192\.168\.50\.11/)).toBeTruthy());
    expect(screen.queryByText('Cluster Token')).toBeNull();
    expect(screen.queryByText(/tok-abcdef123456/)).toBeNull();
  });

  it('Standby: identifies active node address and hides switchover', async () => {
    stubApi({ getHA: async () => HA_STANDBY });
    wrap(<HACard/>);
    await waitFor(() => expect(screen.getByText('Standby')).toBeTruthy());
    expect(screen.getAllByText(/192\.168\.50\.10/).length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: /Planned Switchover/ })).toBeNull();
  });
});

// Only standalone nodes can create a cluster here.
describe('HACard cluster creation', () => {
  afterEach(() => vi.restoreAllMocks());

  // Manual cluster joining requires a token; the UI adds additional nodes through Add Node.
  it('Standalone: offers Create Cluster but not manual Join', async () => {
    stubApi();
    wrap(<HACard/>);
    await waitFor(() => expect(screen.getByText(/is not part of a cluster/)).toBeTruthy());
    expect(screen.getByRole('button', { name: /Create Cluster/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Join Cluster/ })).toBeNull();
  });

  // Creating a cluster only asks for VIP; tokens are generated automatically.
  it('Cluster creation requests VIP only, not token', async () => {
    const create = vi.fn(async () => ({ status: 'creating' }));
    stubApi({ createCluster: create });
    const user = userEvent.setup();
    wrap(<HACard/>);

    await user.click(await screen.findByRole('button', { name: /Create Cluster/ }));
    // Assert the absence of a token input even when the explanatory copy mentions generation.
    const form = screen.getByTestId('create-cluster-form');
    expect(form.querySelectorAll('input').length).toBe(1);
    expect(screen.queryByPlaceholderText(/cluster-token/)).toBeNull();
    // A cluster can start before a pool exists; explain that takeover requires creating a pool.
    expect(form.textContent).toContain('Storage Pool Management');
    expect(form.textContent).toContain('take over');
    await user.type(screen.getByPlaceholderText(/192\.168/), '192.168.10.250');
    await user.click(screen.getByRole('button', { name: /^Create$/ }));

    await waitFor(() => expect(create).toHaveBeenCalledWith({ vip: '192.168.10.250' }));
  });

  it('Already clustered active node shows no join control', async () => {
    stubApi({ getHA: async () => HA_ACTIVE, getReplication: async () => REPL });
    wrap(<HACard/>);
    await waitFor(() => expect(screen.getByText('Active')).toBeTruthy());
    expect(screen.queryByRole('button', { name: /Join Cluster/ })).toBeNull();
  });
});

describe('StandbyBanner', () => {
  afterEach(() => vi.restoreAllMocks());

  it('Shows standby banner only on standby nodes', async () => {
    stubApi({ getHA: async () => HA_STANDBY });
    const { unmount } = wrap(<StandbyBanner/>);
    await waitFor(() => expect(screen.getByText(/This is a standby node/)).toBeTruthy());
    expect(screen.getByRole('link').getAttribute('href')).toBe('http://192.168.50.10:8080');
    unmount();
    vi.restoreAllMocks();

    stubApi({ getHA: async () => HA_ACTIVE });
    wrap(<StandbyBanner/>);
    await new Promise(r => setTimeout(r, 50));
    expect(screen.queryByText(/This is a standby node/)).toBeNull();
  });

  // A standby holding the VIP may not yet have assumed the active role; avoid directing users elsewhere
  // and explain the takeover state and next steps.
  it('Standby holding VIP explains pending takeover instead of redirecting users', async () => {
    stubApi({ getHA: async () => ({ ...HA_STANDBY, holds_vip: true,
      takeover: { phase: 'refused', reason: 'Node node-a remains healthy and active. This node declines takeover to preserve acknowledged writes; VIP is released back to it' } }) });
    wrap(<StandbyBanner/>);
    await waitFor(() => expect(screen.getByText(/has not become active/)).toBeTruthy());
    expect(screen.getByText(/node-a remains healthy and active/)).toBeTruthy();
    expect(screen.getByText(/within a few minutes/)).toBeTruthy();
    expect(screen.queryByText(/Perform administrative operations on the active node/)).toBeNull();
  });

  it('Catalogue catch-up explains takeover in progress', async () => {
    stubApi({ getHA: async () => ({ ...HA_STANDBY, holds_vip: true,
      takeover: { phase: 'catching-up', reason: 'Catching up catalogue from node-a before takeover (up to 3 minutes)' } }) });
    wrap(<StandbyBanner/>);
    await waitFor(() => expect(screen.getByText(/is becoming active/)).toBeTruthy());
    expect(screen.getByText(/catching up catalogue/)).toBeTruthy();
  });
});
