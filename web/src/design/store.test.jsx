import React from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../lib/api.js';
import { StoreProvider, useStore } from './store.jsx';
import { onMutation } from '../lib/hooks.js';

// The store supplies sidebar badges, global search and notifications by polling six independent APIs.
// One API failure must not clear unrelated counters, which come from backend data.

const list = (items) => ({ items, total: items.length });

function stubApi(overrides = {}) {
  const base = {
    listImages: async () => list([{ ID: 'win11', Name: 'Win 11', OSType: 'windows', State: 'normal', Size: 1e9, CreatedAt: '2026-07-30T10:00:00Z' }]),
    listGroups: async () => list([{ ID: 'g-1', Name: 'Classroom A', SystemImageID: 'win11', ClientMax: 30, StartIP: '192.168.50.1' }]),
    listTerminals: async () => list([
      { ID: 't-1', MAC: 'AABBCCDDEE01', IP: '192.168.50.1', GroupID: 'g-1', State: 'online', IsSuper: false },
      { ID: 't-2', MAC: 'AABBCCDDEE02', IP: '192.168.50.2', GroupID: 'g-1', State: 'offline', IsSuper: true },
    ]),
    listPools: async () => list([{ ID: 'pool-tank', Name: 'tank' }]),
    listTasks: async () => list([]),
    listAlarms: async () => list([{ ID: 'a-1', Severity: 'critical', Status: 'active', Type: 'pool_degraded', Resource: 'tank', CreatedAt: '2026-07-30T10:00:00Z' }]),
    listClusterNodes: async () => list([{ id: 'n1' }]),
    listClusterPools: async () => ({ nodes: [{ node_id: 'n1', reachable: true, pools: [{ ID: 'pool-tank', Name: 'tank' }] }], pool_count: 1 }),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

function Probe() {
  const s = useStore();
  return (
    <div>
      <span data-testid="counts">{`${s.images.length}/${s.groups.length}/${s.terminals.length}/${s.pools.length}/${s.alarms.length}`}</span>
      <span data-testid="group">{s.groups[0] ? `${s.groups[0].name}·${s.groups[0].image}·${s.groups[0].online}` : ''}</span>
      <span data-testid="route">{s.route.page}</span>
      <button onClick={() => s.goto('images', { imageId: 'win11' })}>Go to Images</button>
      <button onClick={() => s.toast('Saved successfully', 'ok')}>Notify</button>
      <button onClick={() => s.toast('Delete failed: resource is still in use', 'err')}>Show Error</button>
    </div>
  );
}

const renderStore = () => render(<StoreProvider><Probe/></StoreProvider>);

describe('Global store', () => {
  beforeEach(() => stubApi());
  afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });

  it('Populates badge counts from backend responses', async () => {
    renderStore();
    await waitFor(() => expect(screen.getByTestId('counts').textContent).toBe('1/1/2/1/1'));
    // Group cards resolve image names and count only online clients.
    expect(screen.getByTestId('group').textContent).toBe('Classroom A·Win 11·1');
  });

  // After successful mutations, reload immediately rather than waiting for the next 30-second poll.
  it('Refreshes badges immediately after successful mutations', async () => {
    let images = [{ ID: 'win11', Name: 'Win 11', OSType: 'windows', State: 'normal', Size: 1e9, CreatedAt: '2026-07-30T10:00:00Z' },
      { ID: 'ubuntu', Name: 'Ubuntu', OSType: 'linux', State: 'normal', Size: 1e9, CreatedAt: '2026-07-30T10:00:00Z' }];
    stubApi({ listImages: async () => list(images) });
    renderStore();
    await waitFor(() => expect(screen.getByTestId('counts').textContent).toBe('2/1/2/1/1'));

    images = images.slice(0, 1);
    onMutation.emit();
    await waitFor(() => expect(screen.getByTestId('counts').textContent).toBe('1/1/2/1/1'));
  });

  // Independently settle all six API requests; one alert failure must not blank the sidebar.
  it('Isolates individual API failures', async () => {
    stubApi({ listAlarms: async () => { throw new Error('500'); } });
    renderStore();
    await waitFor(() => expect(screen.getByTestId('counts').textContent).toBe('1/1/2/1/0'));
  });

  it('Shows zero for empty backend data instead of placeholders', async () => {
    stubApi({
      listImages: async () => list([]), listGroups: async () => list([]),
      listTerminals: async () => list([]), listPools: async () => list([]),
      listAlarms: async () => list([]),
    });
    renderStore();
    await waitFor(() => expect(screen.getByTestId('counts').textContent).toBe('0/0/0/0/0'));
  });

  it('Preserves route parameters in goto', async () => {
    renderStore();
    await userEvent.click(screen.getByText('Go to Images'));
    expect(screen.getByTestId('route').textContent).toBe('images');
  });

  // Keep error toasts visible longer than success toasts so backend details can be read.
  it('Keeps error toasts longer than success toasts', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderStore();

    await user.click(screen.getByText('Notify'));
    await user.click(screen.getByText('Show Error'));
    expect(screen.getByText('Saved successfully')).toBeTruthy();
    expect(screen.getByText('Delete failed: resource is still in use')).toBeTruthy();

    await act(async () => { await vi.advanceTimersByTimeAsync(4000); });
    expect(screen.queryByText('Saved successfully')).toBeNull();
    expect(screen.getByText('Delete failed: resource is still in use')).toBeTruthy();

    await act(async () => { await vi.advanceTimersByTimeAsync(4000); });
    expect(screen.queryByText('Delete failed: resource is still in use')).toBeNull();
  });

  // Sessions run for hours; polling keeps sidebar counters current.
  it('Keeps refreshing tasks', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderStore();
    await waitFor(() => expect(api.listTasks).toHaveBeenCalled());
    const before = api.listTasks.mock.calls.length;

    await act(async () => { await vi.advanceTimersByTimeAsync(7000); });
    expect(api.listTasks.mock.calls.length).toBeGreaterThan(before);
  });
});

// Count pools reported by each node, as Storage Management does, to avoid replication lag inconsistencies.
describe('Sidebar Storage Pool Badge', () => {
  afterEach(() => vi.restoreAllMocks());

  it('Uses per-node pool counts in clusters', async () => {
    stubApi({
      listPools: async () => list([{ ID: 'pool-a', Name: 'tank' }, { ID: 'pool-b', Name: 'tank' }]),
      listClusterNodes: async () => list([{ id: 'n1' }, { id: 'n2' }, { id: 'n3' }]),
      listClusterPools: async () => ({
        nodes: [
          { node_id: 'n1', reachable: true, pools: [{ ID: 'p1', Name: 'tank' }] },
          { node_id: 'n2', reachable: true, pools: [{ ID: 'p2', Name: 'tank' }] },
          { node_id: 'n3', reachable: true, pools: [{ ID: 'p3', Name: 'tank' }] },
        ],
        pool_count: 3,
      }),
    });
    renderStore();
    await waitFor(() => expect(screen.getByTestId('counts').textContent).toBe('1/1/2/3/1'));
  });

  it('Uses the local pool list on standalone servers', async () => {
    stubApi({
      listPools: async () => list([{ ID: 'pool-a', Name: 'tank' }]),
      listClusterNodes: async () => list([{ id: 'n1' }]),
    });
    renderStore();
    await waitFor(() => expect(screen.getByTestId('counts').textContent).toBe('1/1/2/1/1'));
  });

  it('Falls back to local pool list on cluster API errors', async () => {
    stubApi({
      listPools: async () => list([{ ID: 'pool-a', Name: 'tank' }, { ID: 'pool-b', Name: 'tank' }]),
      listClusterNodes: async () => list([{ id: 'n1' }, { id: 'n2' }]),
      listClusterPools: async () => { throw new Error('500'); },
    });
    renderStore();
    await waitFor(() => expect(screen.getByTestId('counts').textContent).toBe('1/1/2/2/1'));
  });
});

// Polling identical content must preserve references to prevent unnecessary renders.
describe('Polling retains identity for unchanged data', () => {
  beforeEach(() => stubApi());
  afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });

  function Counter({ seen }) {
    const s = useStore();
    seen.push({ tasks: s.tasks, images: s.images });
    return <span data-testid="n">{s.tasks.length}</span>;
  }

  it('Retains stable array references when content is unchanged', async () => {
    const rows = [{ ID: 'task-1', Type: 'import_image', Status: 'running', Progress: 10, CreatedAt: '2026-08-24T00:00:00Z' }];
    stubApi({ listTasks: async () => list(rows) });
    const renders = [];
    render(<StoreProvider><Counter seen={renders}/></StoreProvider>);
    await waitFor(() => expect(screen.getByTestId('n').textContent).toBe('1'));
    const settled = renders[renders.length - 1];

    // Repeat the request with identical backend data.
    await act(async () => { onMutation.emit(); await Promise.resolve(); });
    await act(async () => { await Promise.resolve(); });

    const after = renders[renders.length - 1];
    expect(after.tasks).toBe(settled.tasks);
    expect(after.images).toBe(settled.images);
  });

  it('Replaces references only when content changes', async () => {
    let rows = [];
    stubApi({ listTasks: async () => list(rows) });
    const renders = [];
    render(<StoreProvider><Counter seen={renders}/></StoreProvider>);
    await waitFor(() => expect(screen.getByTestId('n').textContent).toBe('0'));
    const empty = renders[renders.length - 1].tasks;

    rows = [{ ID: 'task-1', Type: 'import_image', Status: 'running', Progress: 10, CreatedAt: '2026-08-24T00:00:00Z' }];
    await act(async () => { onMutation.emit(); await Promise.resolve(); });
    await waitFor(() => expect(screen.getByTestId('n').textContent).toBe('1'));
    expect(renders[renders.length - 1].tasks).not.toBe(empty);
  });
});
