import React from 'react';
import { render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageOverview } from './overview.jsx';

// Cluster overview must aggregate pool counts and capacity across nodes, not just locally.
const NODES = [
  { id: 'n1', ip: '10.0.0.3', ha_state: 'active', online: true },
  { id: 'n2', ip: '10.0.0.4', ha_state: 'standby', online: true },
  { id: 'n3', ip: '10.0.0.5', ha_state: 'standby', online: false },
];

const CLUSTER_POOLS = {
  nodes: [
    { node_id: 'n1', ip: '10.0.0.3', role: 'active', is_self: true, online: true, reachable: true,
      pools: [{ ID: 'p1', Name: 'tank', Capacity: 80 * 2 ** 30, Used: 20 * 2 ** 30, Health: 'ONLINE' }] },
    { node_id: 'n2', ip: '10.0.0.4', role: 'standby', online: true, reachable: true,
      pools: [{ ID: 'p2', Name: 'tank', Capacity: 80 * 2 ** 30, Used: 60 * 2 ** 30, Health: 'ONLINE' }] },
    { node_id: 'n3', ip: '10.0.0.5', role: 'standby', online: false, reachable: false, pools: [], error: 'Node unreachable' },
  ],
  total_capacity: 160 * 2 ** 30, total_used: 80 * 2 ** 30, pool_count: 2, unreachable: 1,
};

function stubApi({ nodes = NODES, clusterPools = CLUSTER_POOLS, terminals = [] } = {}) {
  vi.spyOn(api, 'listTerminals').mockResolvedValue({ items: terminals });
  vi.spyOn(api, 'listGroups').mockResolvedValue({ items: [] });
  vi.spyOn(api, 'listImages').mockResolvedValue({ items: [] });
  vi.spyOn(api, 'listPools').mockResolvedValue({ items: [] });
  vi.spyOn(api, 'listTaskHistory').mockResolvedValue({ items: [] });
  vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: nodes });
  vi.spyOn(api, 'listClusterPools').mockResolvedValue(clusterPools);
}

const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageOverview/></ConfirmProvider></StoreProvider>,
);

afterEach(() => vi.restoreAllMocks());

describe('Overview cluster aggregation', () => {
  it('Uses cluster-wide pool statistics, not local-only values', async () => {
    stubApi();
    renderPage();
    const card = (await screen.findAllByText('Storage Pools')).map(el => el.closest('.stat-card')).find(Boolean);
    expect(card).toBeTruthy();
    // Two pools totaling 160G with 80G used, rather than the single local pool.
    expect(within(card).getByText('2')).toBeTruthy();
    expect(within(card).getByText(/80\.0 ?GiB/)).toBeTruthy();
  });

  it('Shows cluster status for multi-node deployments', async () => {
    stubApi();
    renderPage();
    expect(await screen.findByText('Cluster')).toBeTruthy();
    // Show one unreachable node explicitly instead of implying all three are healthy.
    expect(await screen.findByText(/1 offline/)).toBeTruthy();
    expect(await screen.findByText(/Active node 10\.0\.0\.3/)).toBeTruthy();
  });

  it('Omits cluster panel in standalone mode', async () => {
    stubApi({ nodes: [NODES[0]], clusterPools: { ...CLUSTER_POOLS, nodes: [CLUSTER_POOLS.nodes[0]] } });
    renderPage();
    await screen.findAllByText('Images');
    expect(screen.queryByText('Cluster')).toBeNull();
  });

  it("Shows each pool's node and explains unreachable nodes", async () => {
    stubApi();
    renderPage();
    const card = (await screen.findByText('Storage Pool Capacity')).closest('.card');
    expect(within(card).getByText('10.0.0.4')).toBeTruthy();
    expect(within(card).getByText(/Node unreachable/)).toBeTruthy();
  });

  // Counts need units so users know that zero means zero clients.
  it('Labels load as client count', async () => {
    stubApi({ terminals: [
      { ID: 't1', State: 'online', StorageServerID: 'n1' },
      { ID: 't2', State: 'online', StorageServerID: 'n2' },
    ] });
    renderPage();
    const card = (await screen.findByText('Node Load')).closest('.card');
    expect(within(card).getAllByText(/ clients/).length).toBe(3);
  });

  // Limit recent tasks so capacity and image panels remain visible.
  it('Limits recent tasks to five', async () => {
    const many = Array.from({ length: 12 }, (_, i) => ({
      ID: `k${i}`, Type: 'reduction_create', TargetRef: `testsys_${i}`, Status: 'success', Progress: 100,
    }));
    stubApi();
    vi.spyOn(api, 'listTaskHistory').mockResolvedValue({ items: many });
    renderPage();
    const card = (await screen.findByText('Recent Tasks')).closest('.card');
    await within(card).findByText(/testsys_0/);
    expect(within(card).queryByText(/testsys_5/)).toBeNull();
    expect(within(card).getAllByText(/testsys_/).length).toBe(5);
  });

  // Prefer display names over normalized ASCII dataset IDs derived from non-ASCII names.
  it('Shows task target names instead of dataset IDs', async () => {
    stubApi();
    vi.spyOn(api, 'listTaskHistory').mockResolvedValue({ items: [
      { ID: 'k1', Type: 'reduction_create', TargetRef: 'win11-_default', TargetName: 'Win11 Gaming Edition / default', Status: 'success', Progress: 100 },
    ] });
    renderPage();
    const card = (await screen.findByText('Recent Tasks')).closest('.card');
    expect(await within(card).findByText(/Win11 Gaming Edition \/ default/)).toBeTruthy();
    expect(within(card).queryByText(/win11-_default/)).toBeNull();
  });

  // Keep three small panels on one row to avoid excessive scrolling.
  it('Keeps node load, capacity and images in one row', async () => {
    stubApi();
    renderPage();
    const load = (await screen.findByText('Node Load')).closest('.card');
    const cap = (await screen.findByText('Storage Pool Capacity')).closest('.card');
    const img = (await screen.findAllByText('Images')).map(el => el.closest('.card-title')).find(Boolean).closest('.card');
    expect(load.parentElement).toBe(cap.parentElement);
    expect(img.parentElement).toBe(cap.parentElement);
  });

  it('Shows how automatic placement distributes client load', async () => {
    stubApi({ terminals: [
      { ID: 't1', State: 'online', StorageServerID: 'n1' },
      { ID: 't2', State: 'online', StorageServerID: 'n2' },
      { ID: 't3', State: 'online', StorageServerID: 'n2' },
      { ID: 't4', State: 'offline', StorageServerID: 'n2' },
    ] });
    renderPage();
    const card = (await screen.findByText('Node Load')).closest('.card');
    // Only online clients contribute to load; offline t4 does not count on n2.
    expect(within(card).getByText(/10\.0\.0\.4/)).toBeTruthy();
    const row = within(card).getByText(/10\.0\.0\.4/).closest('div');
    expect(row.parentElement.textContent).toMatch(/2 clients/);
  });
});
