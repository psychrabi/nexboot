import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider, useStore } from '../store.jsx';
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

function RouteProbe() {
  const store = useStore();
  return <output aria-label="当前路由">{JSON.stringify(store.route)}</output>;
}
const renderPage = () => render(<StoreProvider><ConfirmProvider><PageAlarms/><RouteProbe/></ConfirmProvider></StoreProvider>);

describe('告警中心', () => {
  afterEach(() => vi.restoreAllMocks());

  it('未恢复的机型告警可直达适配，按稳定 ID 传递配置和客户机且不提交任务', async () => {
    const ModelAdapt = { ProfileID: 'h-2', ConfigID: 'c-2', TerminalID: 't-22' };
    stub([{ ...alarm(1), ModelAdapt }, { ...alarm(2, 'acknowledged'), ModelAdapt }, { ...alarm(3, 'recovered'), ModelAdapt }, alarm(4)]);
    const adapt = vi.spyOn(api, 'adaptModel').mockResolvedValue({});
    renderPage();
    await screen.findByText('m1');
    expect(screen.getAllByRole('button', { name: '完整适配' })).toHaveLength(2);
    await userEvent.click(within(screen.getByText('m1').closest('tr')).getByRole('button', { name: '完整适配' }));
    expect(JSON.parse(screen.getByLabelText('当前路由').textContent)).toEqual({ page: 'models', params: { profileId: 'h-2', adapt: true, sourceConfigId: 'c-2', sourceTerminalId: 't-22' } });
    expect(adapt).not.toHaveBeenCalled();
  });

  it('默认每页 10 条，能翻页', async () => {
    const listAlarms = stub(Array.from({ length: 12 }, (_, i) => alarm(i)));
    renderPage();
    expect(await screen.findByText('共 12 条')).toBeTruthy();
    expect(listAlarms).toHaveBeenCalledWith(expect.objectContaining({ page: 1, size: 10 }));
    await userEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(await screen.findByText('m10')).toBeTruthy();
  });

  it('首次发现时间不随巡检更新而变成新故障', async () => {
    stub([{ ...alarm(1), UpdatedAt: '2026-10-08T01:20:00Z' }]);
    renderPage();
    const row = (await screen.findByText('m1')).closest('tr');
    expect(screen.getByRole('columnheader', { name: '首次发现' })).toBeTruthy();
    expect(within(row).getByText('2026-09-30 21:00:00')).toBeTruthy();
    expect(within(row).queryByText('2026-10-08 09:20:00')).toBeNull();
  });

  it('全部确认覆盖所有活动告警，不止当前页', async () => {
    const all = Array.from({ length: 12 }, (_, i) => alarm(i));
    stub(all);
    const ackAlarm = vi.fn(async (id) => { all.find(a => a.ID === id).Status = 'acknowledged'; });
    vi.spyOn(api, 'ackAlarm').mockImplementation(ackAlarm);
    renderPage();
    await screen.findByText('共 12 条');
    await userEvent.click(screen.getByRole('button', { name: /全部确认/ }));
    await waitFor(() => expect(ackAlarm).toHaveBeenCalledTimes(12));
  });

  it('勾选后在上方批量删除，先确认；行内不再有删除按钮', async () => {
    stub([alarm(1), alarm(2), alarm(3)]);
    const deleteAlarm = vi.fn(async () => null);
    vi.spyOn(api, 'deleteAlarm').mockImplementation(deleteAlarm);
    renderPage();
    await screen.findByText('m1');
    expect(screen.queryByRole('button', { name: /删除告警/ })).toBeNull();
    const del = screen.getByRole('button', { name: /删除所选/ });
    expect(del.disabled).toBe(true);
    await userEvent.click(screen.getByLabelText('选择告警 r1'));
    await userEvent.click(screen.getByLabelText('选择告警 r3'));
    await userEvent.click(screen.getByRole('button', { name: /删除所选/ }));
    const dialog = (await screen.findByText(/删除 2 条告警/)).closest('.card');
    await userEvent.click(within(dialog).getByRole('button', { name: '删除' }));
    await waitFor(() => expect(deleteAlarm.mock.calls.map(c => c[0])).toEqual(['a1', 'a3']));
  });

  it('表头勾选只选当前页', async () => {
    stub(Array.from({ length: 12 }, (_, i) => alarm(i)));
    renderPage();
    await screen.findByText('共 12 条');
    await userEvent.click(screen.getByLabelText('选择本页全部告警'));
    expect(screen.getByRole('button', { name: /删除所选/ }).textContent).toContain('10');
  });

  it('已确认和已恢复都带勾', async () => {
    stub([alarm(1, 'acknowledged'), alarm(2, 'recovered')]);
    renderPage();
    const ack = (await screen.findByText('已确认', { selector: 'td .chip' })).closest('.chip');
    const rec = screen.getByText('已恢复', { selector: 'td .chip' }).closest('.chip');
    expect(ack.querySelector('svg')).toBeTruthy();
    expect(rec.querySelector('svg')).toBeTruthy();
  });
});
