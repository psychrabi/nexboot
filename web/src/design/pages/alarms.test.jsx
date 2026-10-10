import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageAlarms } from './alarms.jsx';

const alarm = (i, status = 'active') => ({ ID: `a${i}`, Severity: 'warn', Type: '复制滞后', Resource: `r${i}`, Value: 'v', Message: `m${i}`, Status: status, CreatedAt: '2026-09-30T13:00:00Z' });

function stub(all) {
  const listAlarms = vi.fn(async ({ status = '', page = 1, size = 50 } = {}) => {
    const rows = all.filter(a => !status || a.Status === status);
    return { items: rows.slice((page - 1) * size, page * size), total: rows.length, summary: { active: all.filter(a => a.Status === 'active').length, error: 0, warn: 0, info: 0 } };
  });
  vi.spyOn(api, 'listAlarms').mockImplementation(listAlarms);
  return listAlarms;
}

const renderPage = () => render(<StoreProvider><ConfirmProvider><PageAlarms/></ConfirmProvider></StoreProvider>);

describe('Alert Center', () => {
  afterEach(() => vi.restoreAllMocks());

  it('Shows ten alerts per page and supports pagination', async () => {
    const listAlarms = stub(Array.from({ length: 12 }, (_, i) => alarm(i)));
    renderPage();
    expect(await screen.findByText('Total 12 entries')).toBeTruthy();
    expect(listAlarms).toHaveBeenCalledWith(expect.objectContaining({ page: 1, size: 10 }));
    await userEvent.click(screen.getByRole('button', { name: 'Next' }));
    expect(await screen.findByText('m10')).toBeTruthy();
  });

  it('Acknowledges all active alerts, not just the current page', async () => {
    const all = Array.from({ length: 12 }, (_, i) => alarm(i));
    stub(all);
    const ackAlarm = vi.fn(async (id) => { all.find(a => a.ID === id).Status = 'acknowledged'; });
    vi.spyOn(api, 'ackAlarm').mockImplementation(ackAlarm);
    renderPage();
    await screen.findByText('Total 12 entries');
    await userEvent.click(screen.getByRole('button', { name: /Acknowledge All/ }));
    await waitFor(() => expect(ackAlarm).toHaveBeenCalledTimes(12));
  });

  it('Deletes selected alerts with confirmation, without per-row delete controls', async () => {
    stub([alarm(1), alarm(2), alarm(3)]);
    const deleteAlarm = vi.fn(async () => null);
    vi.spyOn(api, 'deleteAlarm').mockImplementation(deleteAlarm);
    renderPage();
    await screen.findByText('m1');
    expect(screen.queryByRole('button', { name: /Delete alerts/ })).toBeNull();
    const del = screen.getByRole('button', { name: /Delete Selected/ });
    expect(del.disabled).toBe(true);
    await userEvent.click(screen.getByLabelText('Select alert r1'));
    await userEvent.click(screen.getByLabelText('Select alert r3'));
    await userEvent.click(screen.getByRole('button', { name: /Delete Selected/ }));
    const dialog = (await screen.findByText(/Delete 2 alerts/)).closest('.card');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(deleteAlarm.mock.calls.map(c => c[0])).toEqual(['a1', 'a3']));
  });

  it('Header checkbox selects only the current page', async () => {
    stub(Array.from({ length: 12 }, (_, i) => alarm(i)));
    renderPage();
    await screen.findByText('Total 12 entries');
    await userEvent.click(screen.getByLabelText('Select all alerts on this page'));
    expect(screen.getByRole('button', { name: /Delete Selected/ }).textContent).toContain('10');
  });

  it('Acknowledged and recovered alerts are marked', async () => {
    stub([alarm(1, 'acknowledged'), alarm(2, 'recovered')]);
    renderPage();
    const ack = (await screen.findByText('Acknowledged', { selector: 'td .chip' })).closest('.chip');
    const rec = screen.getByText('Recovered', { selector: 'td .chip' }).closest('.chip');
    expect(ack.querySelector('svg')).toBeTruthy();
    expect(rec.querySelector('svg')).toBeTruthy();
  });
});
