import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageServers } from './servers.jsx';


const STATUS = (over = {}) => ({
  config: { backup_pool: '', enabled: false, schedule: '', ...(over.config || {}) },
  items: over.items || [],
});

function stubApi(status) {
  vi.spyOn(api, 'getServer').mockResolvedValue({ Hostname: 'nd-1', OS: 'Ubuntu', UptimeSeconds: 100 });
  vi.spyOn(api, 'listServices').mockResolvedValue({ items: [] });
  vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue(status);
}

const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageServers/></ConfirmProvider></StoreProvider>,
);

const rowOf = async (source) => (await screen.findByText(source)).closest('tr');

// Healthy backup state requires an actual stored copy, not simply the absence of recorded errors.
describe('Per-node backup status', () => {
  afterEach(() => vi.restoreAllMocks());

  function stubNodes(nodes) {
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: [
      { id: 'b', ip: '192.168.10.3', ha_state: 'active', online: true },
    ], total: 1 });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: [] });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue({
      config: { enabled: true, schedule: 'daily@03:00', schedule_text: 'Daily 03:00' },
      node: {},
    });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({
      nodes, total: nodes.length,
      protected: nodes.filter(n => n.last_backup_at).length,
    });
  }

  const rowOfNode = async (ip) => {
    const panel = await screen.findByTestId('backup-nodes');
    return (await within(panel).findByText(ip)).closest('tr');
  };

  it('Indicates missing backup pool instead of Healthy', async () => {
    const nodes = [{ node_id: 'b', ip: '192.168.10.3', role: 'active', online: true, reachable: true, backup_pool: '' }];
    stubNodes(nodes);
    let resolveBackups;
    api.listClusterBackups.mockReturnValueOnce(new Promise(resolve => { resolveBackups = resolve; }));
    renderPage();
    const rowPending = rowOfNode('192.168.10.3');
    await screen.findByTestId('backup-nodes');
    resolveBackups({ nodes, total: 1, protected: 0 });
    const row = await rowPending;
    expect(within(row).queryByText('Healthy')).toBeNull();
    expect(within(row).getByText('No Backup Pool')).toBeTruthy();
    // Use an em dash for missing pool names so rows remain legible.
    expect(row.cells[2].textContent).toBe('—');
  });

  it('Shows Pending Backup until the first copy exists', async () => {
    stubNodes([{ node_id: 'b', ip: '192.168.10.3', role: 'active', online: true, reachable: true, backup_pool: 'w' }]);
    renderPage();
    const row = await rowOfNode('192.168.10.3');
    expect(within(row).queryByText('Healthy')).toBeNull();
    expect(within(row).getByText('Pending Backup')).toBeTruthy();
    expect(within(row).getByText('w')).toBeTruthy();
  });

  it('Reports Healthy only when a copy exists', async () => {
    stubNodes([{ node_id: 'b', ip: '192.168.10.3', role: 'active', online: true, reachable: true,
                 backup_pool: 'w', last_backup_at: '2026-08-23T03:00:00Z' }]);
    renderPage();
    const row = await rowOfNode('192.168.10.3');
    expect(within(row).getByText('Healthy')).toBeTruthy();
  });

  it('Reports unreachable nodes rather than stale backup state', async () => {
    stubNodes([{ node_id: 'c', ip: '192.168.10.5', role: 'standby', online: false, reachable: false, error: 'Node unreachable' }]);
    renderPage();
    const row = await rowOfNode('192.168.10.5');
    expect(within(row).getByText('Node unreachable')).toBeTruthy();
    expect(within(row).queryByText('Healthy')).toBeNull();
  });
});

describe('Cluster Nodes', () => {
  afterEach(() => vi.restoreAllMocks());

  const NODES = (items) => ({ items, total: items.length });

  function stubCluster(items) {
    vi.spyOn(api, 'getServer').mockResolvedValue({ hostname: 'nimblex1', ip: '192.168.10.4', os: 'Ubuntu 24.04' });
    vi.spyOn(api, 'listServices').mockResolvedValue({ items: [] });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue(STATUS());
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue(NODES(items));
  }

  it('Lists every node and identifies active/offline status', async () => {
    stubCluster([
      { id: 'a', name: 'a', ip: '192.168.10.3', portal_ip: '192.168.10.3', ha_state: 'standby', online: true },
      { id: 'b', name: 'b', ip: '192.168.10.4', portal_ip: '192.168.10.250', ha_state: 'active', online: true },
      { id: 'c', name: 'c', ip: '192.168.10.5', portal_ip: '192.168.10.5', ha_state: 'standby', online: false },
    ]);
    renderPage();
    // Node count comes from the badge, never a hard-coded single-node label.
    const badge = await screen.findAllByText(/3\s*Node/);
    expect(badge.length).toBeGreaterThan(0);
    expect(screen.queryByText(/single node/)).toBeNull();
    // Show one row per node; avoid repeating service headings in every row.
    const list = await screen.findByTestId('cluster-nodes');
    const rowFor = (ip) => Array.from(list.querySelectorAll('tbody tr'))
      .find(tr => tr.textContent.includes(ip));
    const self = rowFor('192.168.10.4');
    expect(within(self).getByText(/Active/)).toBeTruthy();
    expect(within(rowFor('192.168.10.5')).getByText('Offline')).toBeTruthy();
    expect(within(rowFor('192.168.10.3')).getByText(/Standby/)).toBeTruthy();
  });

  // Render each service as a real table column with aligned headers and cells.
  it('Aligns service column headings and cells', async () => {
    stubCluster([
      { id: 'a', name: 'a', ip: '192.168.10.3', ha_state: 'active', online: true },
      { id: 'b', name: 'b', ip: '192.168.10.4', ha_state: 'standby', online: true },
    ]);
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({
      nodes: [
        { node_id: 'a', ip: '192.168.10.3', role: 'active', reachable: true, services: [
          { key: 'dnsmasq', label: 'dnsmasq', capability: 'DHCP', unit: 'dnsmasq.service', ok: true, state: 'running' },
          { key: 'target', label: 'target', capability: 'Disk Service', unit: 'target.service', ok: true, state: 'running' },
        ] },
        { node_id: 'b', ip: '192.168.10.4', role: 'standby', reachable: true, services: [
          { key: 'dnsmasq', label: 'dnsmasq', capability: 'DHCP', unit: 'dnsmasq.service', ok: true, state: 'inactive', provided: 'Provided by Active' },
          { key: 'target', label: 'target', capability: 'Disk Service', unit: 'target.service', ok: true, state: 'running' },
        ] },
      ],
    });
    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    await waitFor(() => expect(within(list).getAllByText('DHCP').length).toBeGreaterThan(0));
    const headers = Array.from(list.querySelectorAll('thead th'));
    expect(headers.map(h => h.textContent)).toContain('DHCP');
    // Headers and rows must have matching column counts.
    for (const tr of list.querySelectorAll('tbody tr')) {
      const cells = Array.from(tr.querySelectorAll('td'));
      const span = cells.reduce((n, td) => n + (Number(td.getAttribute('colspan')) || 1), 0);
      expect(span).toBe(headers.length);
    }
  });

  // Allow remote service restarts from the console when SSH is unavailable.
  it('Restarts named services on remote nodes', async () => {
    stubCluster([
      { id: 'a', name: 'a', ip: '192.168.10.3', ha_state: 'active', online: true },
      { id: 'b', name: 'b', ip: '192.168.10.4', ha_state: 'standby', online: true },
    ]);
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({
      nodes: [
        { node_id: 'a', ip: '192.168.10.3', role: 'active', reachable: true, services: [
          { key: 'dnsmasq', label: 'dnsmasq', capability: 'DHCP', unit: 'dnsmasq.service', ok: true, status: 'running', installed: true },
        ] },
        { node_id: 'b', ip: '192.168.10.4', role: 'standby', reachable: true, services: [
          { key: 'target', label: 'target', capability: 'Disk Service', unit: 'target.service', ok: true, status: 'running', installed: true },
        ] },
      ],
    });
    const act = vi.fn().mockResolvedValue({});
    vi.spyOn(api, 'clusterServiceAction').mockImplementation(act);

    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    const rowFor = (ip) => Array.from(list.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes(ip));
    await userEvent.click(rowFor('192.168.10.4'));

    const panel = await screen.findByTestId('node-services');
    await userEvent.click(within(panel).getByRole('button', { name: 'Restart' }));
    // Confirm the exact service and node before dangerous actions.
    const dialog = (await screen.findByText('Restart Service')).closest('.card');
    expect(dialog.textContent).toContain('192.168.10.4');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));

    await waitFor(() => expect(act).toHaveBeenCalledWith('b', 'target', 'restart'));
  });

  // Removing decommissioned nodes keeps quorum counts correct. Restrict the destructive action to offline nodes with address confirmation.
  it('Removes offline nodes after address confirmation', async () => {
    stubCluster([
      { id: 'a', name: 'a', ip: '192.168.10.3', ha_state: 'active', online: true },
      { id: 'c', name: 'c', ip: '192.168.10.5', ha_state: 'standby', online: false },
    ]);
    const forget = vi.fn().mockResolvedValue({});
    vi.spyOn(api, 'forgetClusterNode').mockImplementation(forget);

    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    const rowFor = (ip) => Array.from(list.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes(ip));

    // Do not offer removal for online nodes.
    expect(within(rowFor('192.168.10.3')).queryByRole('button', { name: /Remove/ })).toBeNull();

    await userEvent.click(within(rowFor('192.168.10.5')).getByRole('button', { name: /Remove/ }));
    const dialog = (await screen.findByText(/Remove from Cluster/)).closest('.card');
    expect(dialog.textContent).toContain('192.168.10.5');
    await userEvent.type(dialog.querySelector('input'), '192.168.10.5');
    await userEvent.click(within(dialog).getByRole('button', { name: /Remove|Confirm/ }));
    await waitFor(() => expect(forget).toHaveBeenCalledWith('c'));
  });

  // Standalone and clustered nodes share rendering and version details.
  it('Shows one full node row for standalone deployments', async () => {
    stubCluster([{ id: 'a', name: 'a', ip: '192.168.10.3', portal_ip: '192.168.10.3', ha_state: 'active', online: true }]);
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: [
      { node_id: 'a', ip: '192.168.10.3', role: 'active', online: true, reachable: true,
        host: { hostname: 'only-one', os: 'Ubuntu 24.04', uptime_sec: 60, version: '9.9.9' }, services: [] },
    ]});
    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    expect(list.querySelectorAll('tbody tr').length).toBe(1);
    expect(screen.queryByText(/single node/)).toBeNull();
    const row = list.querySelector('tbody tr');
    expect(within(row).getByText('9.9.9')).toBeTruthy();
    expect(row.getAttribute('title')).toMatch(/only-one/);
    expect(await screen.findByTestId('node-services')).toBeTruthy();
  });
});

// Assess expected service state: standby dnsmasq must be stopped to avoid conflicting DHCP servers.
// target.service is a oneshot unit, so inactive status alone must not be flagged as unhealthy.
describe('Cluster service matrix', () => {
  afterEach(() => vi.restoreAllMocks());

  const MATRIX = {
    nodes: [
      { node_id: 'b', ip: '192.168.10.4', role: 'active', is_self: true, online: true, reachable: true, services: [
        { key: 'dnsmasq', capability: 'DHCP / PXE Boot', label: 'DNSMASQ', unit: 'dnsmasq.service', status: 'running', expected: 'running', ok: true, provided: true },
        { key: 'iscsi', capability: 'Client Disks', label: 'iSCSI', unit: 'target.service', status: 'stopped', expected: 'ready', ok: true, provided: true },
      ]},
      { node_id: 'a', ip: '192.168.10.3', role: 'standby', is_self: false, online: true, reachable: true, services: [
        { key: 'dnsmasq', capability: 'DHCP / PXE Boot', label: 'DNSMASQ', unit: 'dnsmasq.service', status: 'stopped', expected: 'stopped', ok: true, provided: false },
        { key: 'iscsi', capability: 'Client Disks', label: 'iSCSI', unit: 'target.service', status: 'stopped', expected: 'ready', ok: false, provided: false },
      ]},
    ],
  };

  function stubMatrix(matrix) {
    vi.spyOn(api, 'getServer').mockResolvedValue({ hostname: 'nimblex1', ip: '192.168.10.4', os: 'Ubuntu' });
    vi.spyOn(api, 'listServices').mockResolvedValue({ items: [] });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue(STATUS());
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: [
      { id: 'b', ip: '192.168.10.4', portal_ip: '192.168.10.250', ha_state: 'active', online: true },
      { id: 'a', ip: '192.168.10.3', portal_ip: '192.168.10.3', ha_state: 'standby', online: true },
    ], total: 2 });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue(matrix);
  }

  it('Uses expected service states for standby DHCP and kernel iSCSI', async () => {
    stubMatrix(MATRIX);
    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    const rowFor = (ip) => Array.from(list.querySelectorAll('tbody tr'))
      .find(tr => tr.textContent.includes(ip));

    // Show a compact per-service status row with hover details.
    const standby = rowFor('192.168.10.3');
    expect(within(standby).getByTitle(/DHCP.*Provided by Active/s)).toBeTruthy();
    expect(within(standby).getByTitle(/Client Disks.*Abnormal/s)).toBeTruthy();
    // Active-node disk service is Running.
    expect(within(rowFor('192.168.10.4')).getAllByText('Running').length).toBeGreaterThan(0);
    // Highlight the node with a service issue.
    expect(within(standby).getByText(/1 issues/)).toBeTruthy();
    // Summary judgment is Healthy even though individual capability cells show Running.
    expect(within(rowFor('192.168.10.4')).getByText('Healthy')).toBeTruthy();
    // Show issue count in the header and on the affected row.
    expect(within(list).getAllByText(/1 issues/).length).toBe(2);
  });

  it('Shows All Ready when expected service states hold', async () => {
    const allOK = { nodes: MATRIX.nodes.map(n => ({ ...n, services: n.services.map(s => ({ ...s, ok: true })) })) };
    stubMatrix(allOK);
    renderPage();
    expect(await screen.findByText('All Ready')).toBeTruthy();
  });

  it('Distinguishes unreachable nodes from service failures', async () => {
    const off = { nodes: [MATRIX.nodes[0], { ...MATRIX.nodes[1], reachable: false, services: [], error: 'Node not responding' }] };
    stubMatrix(off);
    renderPage();
    const list2 = await screen.findByTestId('cluster-nodes');
    const row = Array.from(list2.querySelectorAll('tbody tr'))
      .find(tr => tr.textContent.includes('192.168.10.3'));
    // Show the node's diagnostic error rather than a generic unavailable message.
    expect(within(row).getByText('Node not responding')).toBeTruthy();
    // Unreachable service status is not proof of a service failure.
    expect(screen.queryByText(/ issues/)).toBeNull();
  });
});

// Cluster list and service panel must agree on service health.
describe('Service status evaluation', () => {
  afterEach(() => vi.restoreAllMocks());

  function stubOne(services) {
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue(STATUS());
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: [
      { id: 'n1', ip: '192.168.10.4', ha_state: 'active', online: true },
    ], total: 1 });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: [
      { node_id: 'n1', ip: '192.168.10.4', role: 'active', online: true, reachable: true, services },
    ]});
  }

  it('Treats healthy oneshot units as Running rather than Ready', async () => {
    stubOne([{
      key: 'iscsi', capability: 'Client Disks', label: 'iSCSI Target · LIO', unit: 'target.service',
      status: 'stopped', expected: 'ready', ok: true, provided: true, installed: true, critical: true,
    }]);
    renderPage();
    const row = (await screen.findByText('iSCSI Target · LIO')).closest('tr');
    expect(within(row).getByText('Running')).toBeTruthy();
    expect(within(row).queryByText('Stopped')).toBeNull();
    expect(within(row).queryByText('Ready')).toBeNull();
    await userEvent.click(within(row).getByLabelText('Expand Details'));
    expect(await screen.findByText('target.service')).toBeTruthy();
  });

  it('Summarizes exceptions instead of running-service count', async () => {
    stubOne([
      { key: 'a', capability: 'Capability A', label: 'A', unit: 'a.service', status: 'running', expected: 'running', ok: true, provided: true, installed: true },
      { key: 'b', capability: 'Capability B', label: 'B', unit: 'b.service', status: 'stopped', expected: 'ready', ok: true, provided: true, installed: true },
    ]);
    renderPage();
    expect(await screen.findByText('All Running')).toBeTruthy();
    expect(screen.queryByText(/\d\/\d Running/)).toBeNull();
  });
});

describe('Multi-node table density', () => {
  afterEach(() => vi.restoreAllMocks());

  const nodeAt = (i) => ({
    id: `n${i}`, ip: `192.168.10.${10 + i}`, portal_ip: `192.168.10.${10 + i}`,
    ha_state: i === 0 ? 'active' : 'standby', online: true,
  });
  const svcOf = (i) => ({
    node_id: `n${i}`, ip: `192.168.10.${10 + i}`, role: i === 0 ? 'active' : 'standby',
    is_self: i === 0, online: true, reachable: true,
    services: [
      { key: 'dnsmasq', capability: 'DHCP / PXE Boot', label: 'DNSMASQ · DHCP/TFTP/PXE', unit: 'dnsmasq.service', status: i === 0 ? 'running' : 'stopped', expected: i === 0 ? 'running' : 'stopped', ok: true, provided: i === 0 },
      { key: 'zfs', capability: 'Storage', label: 'ZFS · Storage', unit: 'zfs-zed.service', status: 'running', expected: 'running', ok: true, provided: true },
    ],
  });

  it('Shows ten node rows with a single service header', async () => {
    const n = 10;
    const idx = Array.from({ length: n }, (_, i) => i);
    vi.spyOn(api, 'getServer').mockResolvedValue({ hostname: 'n1', ip: '192.168.10.10', os: 'Ubuntu' });
    vi.spyOn(api, 'listServices').mockResolvedValue({ items: [] });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue(STATUS());
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: idx.map(nodeAt), total: n });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: idx.map(svcOf) });
    renderPage();

    const list = await screen.findByTestId('cluster-nodes');
    expect(list.querySelectorAll('tbody tr').length).toBe(n);
    // Display capability names once in the header.
    expect(within(list).getAllByText('DHCP / PXE Boot').length).toBe(1);
  });
});

// Surface capability and status; keep systemd implementation details behind expansion.
describe('Service capability view', () => {
  afterEach(() => vi.restoreAllMocks());

  const items = [
    { key: 'dnsmasq', capability: 'DHCP / PXE Boot', label: 'DNSMASQ · DHCP/TFTP/PXE',
      unit: 'dnsmasq.service', status: 'running', expected: 'running', ok: true, provided: true,
      installed: true, enabled: false, critical: true },
    { key: 'iscsi', capability: 'Client Disks', label: 'iSCSI Target · LIO',
      unit: 'target.service', status: 'stopped', expected: 'ready', ok: true, provided: true,
      installed: true, enabled: false, critical: true },
  ];

  function stub(list, role = 'active') {
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue(STATUS());
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: [
      { id: 'n1', ip: '192.168.10.3', ha_state: role, online: true },
    ], total: 1 });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: [
      { node_id: 'n1', ip: '192.168.10.3', role, online: true, reachable: true,
        host: { hostname: 'n1', os: 'Ubuntu', version: 'dev' }, services: list },
    ]});
  }

  it('Shows capability labels and verdict instead of systemd details', async () => {
    stub(items);
    renderPage();
    // Preserve recognizable service labels such as iSCSI Target · LIO.
    expect(await screen.findByText('iSCSI Target · LIO')).toBeTruthy();
    const cap = screen.getByText('iSCSI Target · LIO').closest('.card');
    // Hide implementation details by default; check Disabled only within the services panel.
    expect(within(cap).queryByText('target.service')).toBeNull();
    expect(within(cap).queryByText('Disabled')).toBeNull();
    expect(within(cap).queryByText('systemd Unit')).toBeNull();
    expect(within(cap).queryByText(/unit exits after boot/)).toBeNull();
  });

  it('Expands technical unit and startup details for troubleshooting', async () => {
    stub(items);
    const user = userEvent.setup();
    renderPage();
    await user.click((await screen.findByText('iSCSI Target · LIO')).closest('tr').querySelector('button[aria-label="Expand Details"]'));
    expect(await screen.findByText('target.service')).toBeTruthy();
    expect(screen.getByText(/Enable at boot/)).toBeTruthy();
  });

  it('Shows Provided by Active for standby services', async () => {
    stub([{ ...items[0], status: 'stopped', expected: 'stopped', ok: true, provided: false }], 'standby');
    renderPage();
    // Both table and services panel must agree that standby capabilities are provided by active.
    const svc = await screen.findByTestId('node-services');
    expect(within(svc).getByText('Provided by Active')).toBeTruthy();
    expect(screen.queryByText('Stopped')).toBeNull();
  });
});

// Select a node in the table to display its services; Local node is not a stable host identity.
describe('Node-centric Server Management', () => {
  afterEach(() => vi.restoreAllMocks());

  const HOSTS = {
    nodes: [
      { node_id: 'b', ip: '192.168.10.4', role: 'active', is_self: true, online: true, reachable: true,
        host: { hostname: 'nimblex2', os: 'Ubuntu 24.04.1 LTS', uptime_sec: 111600, version: 'dev' },
        services: [{ key: 'dnsmasq', capability: 'DHCP / PXE Boot', label: 'DNSMASQ', unit: 'dnsmasq.service', status: 'running', expected: 'running', ok: true, installed: true }] },
      { node_id: 'a', ip: '192.168.10.3', role: 'standby', is_self: false, online: true, reachable: true,
        host: { hostname: 'nimblex1', os: 'Debian 12', uptime_sec: 3600, version: '1.2.3' },
        services: [{ key: 'dnsmasq', capability: 'DHCP / PXE Boot', label: 'DNSMASQ', unit: 'dnsmasq.service', status: 'stopped', expected: 'stopped', ok: true, installed: true }] },
    ],
  };

  function stubHosts() {
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue(STATUS());
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: [
      { id: 'b', ip: '192.168.10.4', ha_state: 'active', online: true },
      { id: 'a', ip: '192.168.10.3', ha_state: 'standby', online: true },
    ], total: 2 });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue(HOSTS);
  }

  it('Selects active node by default without Local node labels', async () => {
    stubHosts();
    renderPage();
    await screen.findByTestId('cluster-nodes');
    // Do not show obsolete Local node information cards or labels.
    expect(screen.queryByText('Local node information')).toBeNull();
    expect(screen.queryByText('Local node')).toBeNull();
    // Label the selected host explicitly in the service panel to avoid wrong-host operations.
    const svc = await screen.findByTestId('node-services');
    expect(within(svc).getAllByText(/192\.168\.10\.4/).length).toBeGreaterThan(0);
    expect(within(svc).getAllByText(/Active/).length).toBeGreaterThan(0);
  });

  it('Updates selected server and its service header', async () => {
    stubHosts();
    renderPage();
    const svc = await screen.findByTestId('node-services');
    expect(within(svc).getAllByText(/192\.168\.10\.4/).length).toBeGreaterThan(0);

    const list = screen.getByTestId('cluster-nodes');
    const other = Array.from(list.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes('192.168.10.3'));
    await userEvent.click(other);
    await waitFor(() => expect(
      within(screen.getByTestId('node-services')).getAllByText(/192\.168\.10\.3/).length).toBeGreaterThan(0));
    // On selection, target service actions and role label must follow the selected node.
    expect(within(screen.getByTestId('node-services')).getAllByText(/Standby/).length).toBeGreaterThan(0);
  });

  it('Shows version and uptime in the node list', async () => {
    stubHosts();
    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    const rowFor = (ip) => Array.from(list.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes(ip));
    expect(within(rowFor('192.168.10.4')).getByText('dev')).toBeTruthy();
    expect(within(rowFor('192.168.10.3')).getByText('1.2.3')).toBeTruthy();
    // Compare uptime across nodes to identify recent restarts.
    expect(within(rowFor('192.168.10.4')).getByText(/1\s*d/)).toBeTruthy();
    expect(within(rowFor('192.168.10.3')).getByText(/1\s*h/)).toBeTruthy();
  });

  // Keep hostname and OS in tooltip details rather than permanent rows.
  it('Omits redundant host information from the service panel', async () => {
    stubHosts();
    renderPage();
    const svc = await screen.findByTestId('node-services');
    expect(within(svc).queryByText('Hostname')).toBeNull();
    expect(within(svc).queryByText('Operating System')).toBeNull();
    expect(within(svc).queryByText('ndiskless Version')).toBeNull();
    // Keep these details in the node-row tooltip.
    const list = screen.getByTestId('cluster-nodes');
    const row = Array.from(list.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes('192.168.10.4'));
    expect(row.getAttribute('title')).toMatch(/nimblex2/);
    expect(row.getAttribute('title')).toMatch(/Ubuntu/);
  });

  it('Omits redundant node Details button and explanation', async () => {
    stubHosts();
    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    expect(within(list).queryByRole('button', { name: 'Details' })).toBeNull();
    expect(screen.queryByText(/fall back to the active node/)).toBeNull();
    expect(screen.queryByText(/DHCP should not run on standby/)).toBeNull();
  });
});

// Show backup copies by node; infer backup pool and expose schedule times rather than raw cron.
describe('Per-node Data Backup', () => {
  afterEach(() => vi.restoreAllMocks());

  const BACKUPS = {
    total: 3, protected: 2,
    nodes: [
      { node_id: 'b', ip: '192.168.10.3', role: 'active', online: true, reachable: true,
        backup_pool: 'w', last_backup_at: '2026-08-23T03:00:00Z', pending: false },
      { node_id: 'a', ip: '192.168.10.4', role: 'standby', online: true, reachable: true,
        backup_pool: 'w2', last_backup_at: '2026-08-22T03:00:00Z', pending: true },
      { node_id: 'c', ip: '192.168.10.5', role: 'standby', online: true, reachable: true,
        backup_pool: '', pending: false },
    ],
  };

  function stubBackups() {
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: [
      { id: 'b', ip: '192.168.10.3', ha_state: 'active', online: true },
    ], total: 1 });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: [] });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue({
      config: { enabled: true, schedule: 'daily@03:00', schedule_text: 'Daily 03:00',
                next_run_at: '2026-08-24T03:00:00Z', last_run_at: '2026-08-23T03:00:00Z' },
      node: { backup_pool: 'w', last_backup_at: '2026-08-23T03:00:00Z', pending: false },
    });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue(BACKUPS);
  }

  it('Shows Backing Up for active replication', async () => {
    stubBackups();
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ ...BACKUPS, nodes: [
      { ...BACKUPS.nodes[0] },
      { ...BACKUPS.nodes[1], copying: true },
      { node_id: 'c', ip: '192.168.10.5', role: 'standby', online: true, reachable: true, backup_pool: 'backup', pending: true, copying: true },
    ] });
    renderPage();
    const first = (await screen.findByText('192.168.10.4')).closest('tr');
    expect(within(first).getByText('Backing Up')).toBeTruthy();
    const never = screen.getByText('192.168.10.5').closest('tr');
    expect(within(never).getByText('Backing Up')).toBeTruthy();
    expect(within(never).queryByText('Pending Backup')).toBeNull();
  });

  it('Infers the backup pool instead of requiring manual entry', async () => {
    stubBackups();
    renderPage();
    await screen.findByText(/Data Backup/);
    expect(screen.queryByLabelText('Backup target pool')).toBeNull();
    expect(screen.queryByPlaceholderText('backup')).toBeNull();
  });

  it('Uses human-friendly daily time rather than raw cron', async () => {
    stubBackups();
    renderPage();
    await screen.findByText(/Data Backup/);
    expect(screen.queryByText(/cron/i)).toBeNull();
    expect(screen.queryByPlaceholderText('0 3 * * *')).toBeNull();
    expect(await screen.findByLabelText('Time')).toBeTruthy();
  });

  it("Shows each node's copy availability and timestamp", async () => {
    stubBackups();
    renderPage();
    const panel = await screen.findByTestId('backup-nodes');
    const rowFor = (ip) => Array.from(panel.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes(ip));

    expect(within(rowFor('192.168.10.3')).getByText('w')).toBeTruthy();
    // Clearly identify lagging nodes instead of marking them healthy.
    expect(within(rowFor('192.168.10.4')).getByText(/Pending Sync|lagging/)).toBeTruthy();
    // No backup pool is not a healthy backup state.
    expect(within(rowFor('192.168.10.5')).getByText(/No Backup Pool/)).toBeTruthy();
    expect(within(rowFor('192.168.10.5')).queryByText('Healthy')).toBeNull();
    // Per-node rows replace a redundant backup summary banner.
    expect(within(panel).queryByText(/nodes have backup copies/)).toBeNull();
  });
});

describe('Layout simplification', () => {
  afterEach(() => vi.restoreAllMocks());

  function stub(nodes, services) {
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: nodes, total: nodes.length });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: services });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue({
      config: { enabled: true, schedule: 'daily@03:00', schedule_text: 'Daily 03:00',
                next_run_at: '2026-08-24T03:00:00Z', next_run_text: '2026-08-24 03:00' },
      node: {},
    });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getRuntime').mockResolvedValue({ portal_ip: '192.168.10.250' });
  }

  it('Shows VIP in cluster header only', async () => {
    stub([{ id: 'a', ip: '192.168.10.3', ha_state: 'active', online: true, portal_ip: '192.168.10.250' }], []);
    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    expect(within(list).getByText(/Virtual IP\s+192\.168\.10\.250/)).toBeTruthy();
    expect(screen.queryByText('HoldingVirtual IP')).toBeNull();
  });

  // Standby dnsmasq should remain stopped so competing authoritative DHCP servers do not reject clients.
  // Do not offer Restart for a service that must remain stopped.
  it('Omits Restart for intentionally stopped services', async () => {
    stub(
      [{ id: 'b', ip: '192.168.10.5', ha_state: 'standby', online: true }],
      [{ node_id: 'b', ip: '192.168.10.5', role: 'standby', online: true, reachable: true, services: [
        { key: 'dnsmasq', capability: 'DHCP', label: 'DNSMASQ · DHCP/TFTP/PXE', unit: 'dnsmasq.service',
          status: 'stopped', expected: 'stopped', ok: true, provided: false, installed: true },
        { key: 'zfs', capability: 'Storage', label: 'ZFS · Storage', unit: 'zfs-zed.service',
          status: 'running', expected: 'running', ok: true, provided: true, installed: true },
      ]}],
    );
    renderPage();
    const svc = await screen.findByTestId('node-services');
    const rowOf = (name) => Array.from(svc.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes(name));
    expect(within(rowOf('DNSMASQ')).queryByRole('button', { name: 'Restart' })).toBeNull();
    // Services expected to run may still be restarted.
    expect(within(rowOf('ZFS')).getByRole('button', { name: 'Restart' })).toBeTruthy();
  });

  // If standby dnsmasq unexpectedly runs, offer Stop.
  it('Offers Stop for incorrectly running standby service', async () => {
    stub(
      [{ id: 'b', ip: '192.168.10.5', ha_state: 'standby', online: true }],
      [{ node_id: 'b', ip: '192.168.10.5', role: 'standby', online: true, reachable: true, services: [
        { key: 'dnsmasq', capability: 'DHCP', label: 'DNSMASQ · DHCP/TFTP/PXE', unit: 'dnsmasq.service',
          status: 'running', expected: 'stopped', ok: false, provided: false, installed: true },
      ]}],
    );
    renderPage();
    const svc = await screen.findByTestId('node-services');
    const row = Array.from(svc.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes('DNSMASQ'));
    expect(within(row).queryByRole('button', { name: 'Restart' })).toBeNull();
    expect(within(row).getByRole('button', { name: 'Stop' })).toBeTruthy();
  });

  // Integrate enablement into frequency selection so unsaved changes do not display as enabled.
  it('Uses one backup frequency selector without an enable toggle', async () => {
    stub([{ id: 'a', ip: '192.168.10.3', ha_state: 'active', online: true }], []);
    renderPage();
    await screen.findByText(/Data Backup/);
    expect(screen.queryByRole('button', { name: 'Enabled' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Disabled' })).toBeNull();
    const sel = screen.getByLabelText('Backup Schedule');
    expect(Array.from(sel.options).map(o => o.textContent)).toEqual(['Disabled', 'Daily', 'Weekly']);
  });

  // Save is required for schedule changes and disabled when nothing changed.
  it('Enables Save only when schedule changes', async () => {
    stub([{ id: 'a', ip: '192.168.10.3', ha_state: 'active', online: true }], []);
    const save = vi.fn().mockResolvedValue({});
    vi.spyOn(api, 'saveBackupConfig').mockImplementation(save);
    renderPage();
    await screen.findByText(/Data Backup/);

    expect(screen.getByRole('button', { name: /Save/ }).disabled).toBe(true);

    await userEvent.selectOptions(screen.getByLabelText('Backup Schedule'), 'off');
    expect(save).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole('button', { name: /Save/ }).disabled).toBe(false));
    // Hide the schedule time when disabled.
    expect(screen.queryByLabelText('Time')).toBeNull();

    // Reverting to stored values disables Save again.
    await userEvent.selectOptions(screen.getByLabelText('Backup Schedule'), 'daily');
    await waitFor(() => expect(screen.getByRole('button', { name: /Save/ }).disabled).toBe(true));
    expect(save).not.toHaveBeenCalled();

    // Submit changes only on Save.
    await userEvent.selectOptions(screen.getByLabelText('Backup Schedule'), 'weekly');
    await userEvent.click(screen.getByRole('button', { name: /Save/ }));
    await waitFor(() => expect(save).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: true, schedule: expect.stringMatching(/^weekly@/) })));
  });

  it('Saves disabled state rather than an empty schedule', async () => {
    stub([{ id: 'a', ip: '192.168.10.3', ha_state: 'active', online: true }], []);
    const save = vi.fn().mockResolvedValue({});
    vi.spyOn(api, 'saveBackupConfig').mockImplementation(save);
    renderPage();
    await screen.findByText(/Data Backup/);
    await userEvent.selectOptions(screen.getByLabelText('Backup Schedule'), 'off');
    await userEvent.click(screen.getByRole('button', { name: /Save/ }));
    // Retain selected time when disabling, for future re-enabling.
    await waitFor(() => expect(save).toHaveBeenCalledWith({ enabled: false, schedule: 'daily@03:00' }));
  });

  // Set width explicitly so select controls fit on one row.
  it('Keeps backup schedule controls on one row', async () => {
    stub([{ id: 'a', ip: '192.168.10.3', ha_state: 'active', online: true }], []);
    renderPage();
    await screen.findByText(/Data Backup/);
    const sel = screen.getByLabelText('Backup Schedule');
    expect(sel.style.width).not.toBe('100%');
    const row = screen.getByTestId('backup-controls');
    expect(row.style.flexWrap).not.toBe('wrap');
  });

  it('Removes redundant backup form labels', async () => {
    stub([{ id: 'a', ip: '192.168.10.3', ha_state: 'active', online: true }], []);
    renderPage();
    await screen.findByText(/Data Backup/);
    expect(screen.queryByText('Scheduled Backup')).toBeNull();
    expect(screen.queryByText('When to run')).toBeNull();
    // Show next-run time using server clock because backups execute there.
    const line = screen.getByTestId('backup-controls');
    expect(line.textContent).toMatch(/Next 2026-08-24 03:00/);
    expect(line.textContent.match(/Daily/g).length).toBe(1);
    expect(within(line).getByRole('button', { name: /Save/ })).toBeTruthy();
  });
});

describe('Contextual help on read-only services', () => {
  afterEach(() => vi.restoreAllMocks());

  function stub() {
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: [
      { id: 'a', ip: '192.168.10.3', ha_state: 'active', online: true },
    ], total: 1 });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: [
      { node_id: 'a', ip: '192.168.10.3', role: 'active', online: true, reachable: true, services: [
        { key: 'zfs', capability: 'Storage', label: 'ZFS · Storage', unit: 'zfs-zed.service',
          status: 'running', expected: 'running', ok: true, installed: true },
        { key: 'ndiskless', capability: 'Control Service', label: 'ndiskless · Control/Boot', unit: 'ndiskless.service',
          status: 'running', expected: 'running', ok: true, installed: true, read_only: true },
      ]},
    ]});
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue({ config: {}, node: {} });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
  }

  // Require clicking help instead of relying on delayed native title hovers.
  it('Opens read-only help on click, not in a table footer', async () => {
    stub();
    renderPage();
    const svc = await screen.findByTestId('node-services');
    const rowOf = (name) => Array.from(svc.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes(name));

    // Help text remains absent until opened.
    expect(screen.queryByText(/The control service is read-only here/)).toBeNull();

    const mark = within(rowOf('ndiskless')).getByRole('button', { name: /Help$/ });
    await userEvent.click(mark);
    const tip = await screen.findByText(/The control service is read-only here/);
    expect(tip.textContent).toMatch(/systemctl restart ndiskless/);

    // Clicking help again collapses it.
    await userEvent.click(mark);
    await waitFor(() => expect(screen.queryByText(/The control service is read-only here/)).toBeNull());

    // Restartable services should not carry this help control.
    expect(within(rowOf('ZFS')).queryByRole('button', { name: /Help$/ })).toBeNull();
  });

  it('Closes help when clicking elsewhere', async () => {
    stub();
    renderPage();
    const svc = await screen.findByTestId('node-services');
    const row = Array.from(svc.querySelectorAll('tbody tr')).find(tr => tr.textContent.includes('ndiskless'));
    await userEvent.click(within(row).getByRole('button', { name: /Help$/ }));
    await screen.findByText(/The control service is read-only here/);
    await userEvent.click(screen.getByTestId('tip-backdrop'));
    await waitFor(() => expect(screen.queryByText(/The control service is read-only here/)).toBeNull());
  });

  // An unloaded matrix has undefined reachability, not an unreachable-node status.
  it('Shows Loading while service matrix is pending', async () => {
    let release;
    const held = new Promise(r => { release = r; });
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({ items: [
      { id: 'a', ip: '192.168.10.3', ha_state: 'active', online: true },
      { id: 'b', ip: '192.168.10.4', ha_state: 'standby', online: true },
    ], total: 2 });
    vi.spyOn(api, 'listClusterServices').mockImplementation(async () => {
      await held;
      return { nodes: [
        { node_id: 'a', ip: '192.168.10.3', role: 'active', online: true, reachable: true, services: [] },
        { node_id: 'b', ip: '192.168.10.4', role: 'standby', online: true, reachable: true, services: [] },
      ]};
    });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue({ config: {}, node: {} });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });

    renderPage();
    const list = await screen.findByTestId('cluster-nodes');
    // While matrix loads, report Loading rather than Unavailable.
    expect(within(list).queryByText(/Unavailable/)).toBeNull();
    expect(within(list).getAllByText(/Loading/).length).toBeGreaterThan(0);

    release();
    await waitFor(() => expect(within(screen.getByTestId('cluster-nodes')).queryByText(/Loading/)).toBeNull());
  });
});

describe('Cluster Nodes · Add Node', () => {
  afterEach(() => vi.restoreAllMocks());

  function stubCluster(candidates, adopt) {
    vi.spyOn(api, 'getServer').mockResolvedValue({ Hostname: 'nd-1', OS: 'Ubuntu', UptimeSeconds: 100 });
    vi.spyOn(api, 'listServices').mockResolvedValue({ items: [] });
    vi.spyOn(api, 'listClusterBackups').mockResolvedValue({ nodes: [], total: 0, protected: 0 });
    vi.spyOn(api, 'getBackupStatus').mockResolvedValue({ config: {}, items: [] });
    vi.spyOn(api, 'listClusterNodes').mockResolvedValue({
      items: [{ id: 'a1', ip: '192.168.10.3', portal_ip: '192.168.10.250', ha_state: 'active', online: true }],
      total: 1,
    });
    vi.spyOn(api, 'listClusterServices').mockResolvedValue({ nodes: [] });
    vi.spyOn(api, 'discoverNodes').mockResolvedValue({ items: candidates, total: candidates.length });
    vi.spyOn(api, 'adoptNode').mockImplementation(adopt);
  }

  it('Adds discovered factory-state nodes without VIP or token input', async () => {
    const adopt = vi.fn(async () => ({ status: 'adopting' }));
    stubCluster([{ node_id: 'fresh-b', version: 'v1', address: '192.168.10.4:8080' }], adopt);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: /Add Node/ }));
    expect(await screen.findByText(/192\.168\.10\.4:8080/)).toBeTruthy();

    const dialog = screen.getByTestId('adopt-nodes');
    expect(dialog.querySelectorAll('input[type="text"]').length).toBe(0); // No input fields are needed
    expect(dialog.textContent).not.toContain('Pool names must match across the cluster'); // Each node chooses its own pool names
    expect(dialog.textContent).toContain('Storage Pool Management');
    await user.click(within(dialog).getByRole('button', { name: /Join Cluster/ }));

    await waitFor(() => expect(adopt).toHaveBeenCalledWith({ address: '192.168.10.4:8080' }));
  });

  it('Explains why no eligible nodes were discovered', async () => {
    stubCluster([], vi.fn());
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole('button', { name: /Add Node/ }));
    expect(await screen.findByText(/No eligible machines found|No eligible machines found/)).toBeTruthy();
  });
});
