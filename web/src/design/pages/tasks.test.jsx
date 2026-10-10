import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageTasks } from './tasks.jsx';

const page = (items) => ({ items, total: items.length });

function stubApi(tasks) {
  const base = {
    listTaskHistory: async () => page(tasks),
    listTerminals: async () => page([{ ID: 'terminal-S', Name: 'Super Admin', MAC: '00505623AAFF' }]),
    listImages: async () => page([]),
  };
  for (const [name, fn] of Object.entries(base)) vi.spyOn(api, name).mockImplementation(fn);
}

const renderPage = () => render(<StoreProvider><ConfirmProvider><PageTasks/></ConfirmProvider></StoreProvider>);

afterEach(() => vi.restoreAllMocks());

// Show failure reasons inline; Failed and 0% alone can be mistaken for a stalled task.
it('Displays failed task reasons without expansion', async () => {
  stubApi([
    { ID: 'task-1', Type: 'publish_data_disk', TargetRef: '00505623AAFF', Status: 'failed', Progress: 0,
      Error: 'D: is still being written; wait for writes to finish before publishing', CreatedAt: new Date().toISOString(), FinishedAt: new Date().toISOString() },
  ]);
  renderPage();
  expect(await screen.findByText(/D: is still being written/)).toBeTruthy();
});

// Task names should match UI actions, e.g. Shut Down and Save Restore Point, not an internal action.
it('Task names match visible console actions', async () => {
  stubApi([
    { ID: 'task-1', Type: 'super_stop', TargetRef: '00505623AAFF', Status: 'success', Progress: 100, CreatedAt: new Date().toISOString() },
    { ID: 'task-2', Type: 'publish_data_disk', TargetRef: '00505623AAFF', Status: 'success', Progress: 100, CreatedAt: new Date().toISOString() },
    { ID: 'task-3', Type: 'migrate_disk', TargetRef: 'pool-tank', Status: 'success', Progress: 100, CreatedAt: new Date().toISOString() },
  ]);
  renderPage();
  await screen.findByText('task-1');
  const rows = screen.getAllByRole('row');
  const typeOf = (id) => {
    const row = rows.find(r => within(r).queryByText(id));
    return row.querySelectorAll('td')[1].textContent;
  };
  await waitFor(() => expect(typeOf('task-1')).toBe('Shut down and save restore point'));
  expect(typeOf('task-2')).toBe('Publish data disk');
  expect(typeOf('task-3')).toBe('Remove disk');
});

// The Type column makes task prefixes redundant; keep the full ID in a tooltip.
it('Removes type prefix from displayed task IDs but preserves full ID in tooltip', async () => {
  stubApi([
    { ID: 'task-publish_data_disk-1789898065236948317', Type: 'publish_data_disk', TargetRef: '0E2EC1000B01',
      Status: 'failed', Progress: 0, Error: 'A newer restore point @e2e-newer exists; saving would delete it, so the operation was aborted',
      CreatedAt: new Date().toISOString(), FinishedAt: new Date().toISOString() },
  ]);
  renderPage();
  const cell = await screen.findByText('1789898065236948317');
  expect(cell.textContent).not.toContain('publish_data_disk');
  expect(cell.getAttribute('title')).toBe('task-publish_data_disk-1789898065236948317');
});

// Show execution host per task; pool jobs run on the pool node, others on the active writer.
// Report unreachable hosts instead of implying they have no tasks.
it('Shows execution server and inaccessible nodes', async () => {
  const now = new Date().toISOString();
  const items = [
    { ID: 'task-create_pool-1', Type: 'create_pool', TargetRef: 'data', Status: 'failed', Progress: 0, Node: '192.168.10.5',
      Error: 'Mount directory /ndiskless/data exists and is not empty; choose another pool name or clear the directory', CreatedAt: now },
    { ID: 'task-destroy_pool-2', Type: 'destroy_pool', TargetRef: 'pool-2f21d1f9cbfd--e2egone', Status: 'success', Progress: 100, Node: '192.168.10.4', CreatedAt: now },
    { ID: 'task-create_config-3', Type: 'create_config', TargetRef: 'vmdk_default', Status: 'success', Progress: 100, CreatedAt: now },
  ];
  vi.spyOn(api, 'listTaskHistory').mockImplementation(async () => ({ items, total: 3, unreachable: ['192.168.10.9'] }));
  vi.spyOn(api, 'listTerminals').mockImplementation(async () => page([]));
  vi.spyOn(api, 'listImages').mockImplementation(async () => page([]));
  renderPage();
  expect(await screen.findByRole('columnheader', { name: 'Server' })).toBeTruthy();
  const row = (id) => screen.getByText(id).closest('tr');
  expect(within(row('1')).getByText('192.168.10.5')).toBeTruthy();
  expect(within(row('2')).getByText('192.168.10.4')).toBeTruthy();
  expect(within(row('2')).getByText('e2egone')).toBeTruthy();   // Show pool name instead of internal ID
  expect(row('3').querySelectorAll('td')[3].textContent).toBe('—'); // Legacy task created before node tracking
  expect(screen.queryByText(/executed on/)).toBeNull();
  expect(screen.getByText(/192\.168\.10\.9  is unreachable; its tasks could not be listed/)).toBeTruthy();
});

it('Shows target display name and keeps dataset ID in tooltip', async () => {
  stubApi([
    { ID: 'task-x', Type: 'reduction_create', TargetRef: 'win11-_default_____179', TargetName: 'Win11 Gaming Edition / default / Graphics drivers installed',
      Status: 'success', Progress: 100, CreatedAt: new Date().toISOString() },
  ]);
  renderPage();
  const cell = await screen.findByText('Win11 Gaming Edition / default / Graphics drivers installed');
  expect(cell.getAttribute('title')).toBe('win11-_default_____179');
});
