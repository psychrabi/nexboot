import React from 'react';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider, useStore } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageModels, ModelAdapt } from './models.jsx';

const list = items => ({ items });
const model = { id: 'h-1', name: 'VMware E1000', nic_label: '8086:100F', platform: 'efi', terminals: 2, status: 'failed', boot_failed: 1, boot_ok: 0, boot_waiting: 1, points: [] };
function stub(overrides = {}) {
  const base = {
    listModels: async () => [model],
    getModel: async () => ({ ...model, terminal_list: [
      { id: 't-1', name: '测试客户机', mac: '0050562EF650', state: 'offline' },
      { id: 't-2', name: '正在运行', state: 'online' },
    ], adaptations: [] }),
    listDriverPacks: async () => list([{ pack: { ID: 'p-1', Name: 'Intel I219', Status: 'enabled', HWIDs: [] }, risks: [] }]),
    listImages: async () => list([{ ID: 'i-1', Name: 'Windows 11', OSType: 'windows' }]),
    listConfigs: async () => list([{ ID: 'c-1', Name: 'default' }]),
    listTerminals: async () => list([]), listGroups: async () => list([]),
    listPools: async () => list([]), listTasks: async () => list([]), listAlarms: async () => list([]),
    terminalIdentity: async () => ({ next_boot: { kind: 'system', reason: 'unverified' } }),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) vi.spyOn(api, name).mockImplementation(fn);
}
function RouteProbe() {
  const store = useStore();
  return <><output aria-label="当前路由">{JSON.stringify(store.route)}</output><button onClick={() => store.goto('models')}>侧栏进入机型</button></>;
}
function RouteEntry({ params }) {
  const store = useStore();
  React.useEffect(() => { store.goto('models', params); }, [params]);
  return null;
}
const renderPage = params => render(<StoreProvider><ConfirmProvider>{params && <RouteEntry params={params}/>}<PageModels/><RouteProbe/></ConfirmProvider></StoreProvider>);

describe('机型', () => {
  beforeEach(() => stub());

  it.each([['offline', 'source-terminal'], ['online', '']])('告警直达适配预选来源配置和客户机，来源 %s 时不替换机器', async (state, terminalID) => {
    const submit = vi.spyOn(api, 'adaptModel').mockResolvedValue({});
    stub({
      listConfigs: async () => list([{ ID: 'other-config', Name: '其他配置' }, { ID: 'source-config', Name: '来源配置' }]),
      getModel: async () => ({ ...model, terminal_list: [{ id: 'other-terminal', name: '其他客户机', state: 'offline' }, { id: 'source-terminal', name: '来源客户机', state }], adaptations: [] }),
    });
    renderPage({ profileId: 'h-1', adapt: true, sourceConfigId: 'source-config', sourceTerminalId: 'source-terminal' });
    await screen.findByRole('option', { name: /来源配置/ });
    expect(screen.getByLabelText('目标系统配置').value).toBe('source-config');
    expect(screen.getByLabelText('关机客户机').value).toBe(terminalID);
    if (state === 'online') expect(screen.getByText('请先关闭来源客户机「来源客户机」，再打开适配。')).toBeTruthy();
    expect(submit).not.toHaveBeenCalled();
  });

  it('启动网卡列显示网卡型号，PCI 编号作为小字留着核对；按型号名也能搜到', async () => {
    stub({ listModels: async () => [{ ...model, nic_name: 'Intel E1000' }, { ...model, id: 'h-2', name: 'VMware 虚拟机 · VMware vmxnet3', nic_name: 'VMware vmxnet3', nic_label: '15AD:07B0' }] });
    renderPage();
    const row = (await screen.findByText('VMware 虚拟机 · VMware vmxnet3')).closest('tr');
    const cell = within(row).getByText('VMware vmxnet3', { selector: 'div' });
    expect(cell).toBeTruthy();
    expect(within(row).getByText('15AD:07B0')).toBeTruthy();
    await userEvent.type(screen.getByPlaceholderText('搜索机型 / 网卡'), 'vmxnet3');
    expect(screen.queryByText('VMware E1000')).toBeNull();
    expect(screen.getByText('VMware 虚拟机 · VMware vmxnet3')).toBeTruthy();
  });
  afterEach(() => vi.restoreAllMocks());
  it('说明仅用于 Windows，列表和详情明确 Windows 客户机统计', async () => {
    renderPage();
    await screen.findByText(model.name);
    expect(screen.getByText(/新机型首次开机时自动生成网卡配置/)).toBeTruthy();
    expect(screen.getByText(/Ubuntu\/Linux 不归入机型/)).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'Windows 客户机' })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '详情' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Windows 客户机 (2)' }));
    expect(screen.getByText('测试客户机')).toBeTruthy();
    expect(screen.getByText('正在运行')).toBeTruthy();
  });
  it.each([
    ['efi', '', '启动方式：UEFI'],
    ['efi', '0000:00:03.0', '启动方式：UEFI'],
    ['pcbios', '', '启动方式：BIOS'],
    ['pcbios', '0000:00:03.0', '启动方式：BIOS'],
    ['', '', null],
    ['', '0000:00:03.0', null],
  ])('机型详情仅在已有平台时显示启动方式，不重复网卡或 PCI（%s / %s）', async (platform, busLoc, bootMode) => {
    stub({ getModel: async () => ({ ...model, platform, bus_loc: busLoc, terminal_list: [], reduction_list: [] }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    const detail = within((await screen.findByRole('heading', { name: model.name })).closest('.card'));
    expect(detail.queryByText(/启动网卡/)).toBeNull();
    expect(detail.queryByText(model.nic_label)).toBeNull();
    expect(detail.queryByText(/PCI 位置未上报|固件未上报|0000:00:03\.0/)).toBeNull();
    if (bootMode) expect(detail.getByText(bootMode)).toBeTruthy();
    else expect(detail.queryByText(/启动方式/)).toBeNull();
  });
  it('显示机型状态和两个标签，驱动包仍可搜索', async () => {
    renderPage();
    await screen.findByText('VMware E1000');
    expect(screen.getByText('开机失败')).toBeTruthy();
    expect(screen.queryByText('启动驱动集')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: /^驱动包/ }));
    await screen.findByText('Intel I219');
    await userEvent.type(screen.getByPlaceholderText(/搜索名称/), 'realtek');
    await waitFor(() => expect(screen.queryByText('Intel I219')).toBeNull());
  });
  it.each([
    ['h-1', 'VMware E1000', 'source-config', 'source-terminal'],
    ['h-other', '另一机型', 'other-config', 'other-terminal'],
  ])('来源导航后适配 %s 只继承同机型上下文，详情关闭后仍保留来源', async (id, name, configID, terminalID) => {
    stub({
      listModels: async () => [model, { ...model, id: 'h-other', name: '另一机型' }],
      listConfigs: async () => list([{ ID: 'source-config', Name: '来源配置' }, { ID: 'other-config', Name: '其他配置' }]),
      getModel: async profileID => ({ ...model, id: profileID, name: profileID === 'h-1' ? model.name : '另一机型',
        points: [{ config_id: 'other-config', failed: 1 }], terminal_list: profileID === 'h-1' ? [
          { id: 'first-terminal', name: '其他关机机器', state: 'offline' },
          { id: 'source-terminal', name: '来源客户机', state: 'offline' },
        ] : [{ id: 'other-terminal', name: '另一机型客户机', state: 'offline' }], reduction_list: [] }),
    });
    renderPage({ filterProfileId: 'h-1', filterProfileName: model.name, sourceConfigId: 'source-config', sourceTerminalId: 'source-terminal' });
    const search = await screen.findByRole('textbox', { name: '搜索机型 / 网卡' });
    await screen.findByText(model.name);
    if (id !== 'h-1') await userEvent.clear(search);
    const row = screen.getByRole('row', { name: new RegExp(name) });
    await userEvent.click(within(row).getByRole('button', { name: `完整适配 ${name}` }));
    await screen.findByRole('option', { name: /来源配置/ });
    expect(screen.getByLabelText('目标系统配置').value).toBe(configID);
    expect(screen.getByLabelText('关机客户机').value).toBe(terminalID);
    await userEvent.click(screen.getByRole('button', { name: '取消' }));
    await userEvent.click(within(screen.getByRole('row', { name: new RegExp(name) })).getByRole('button', { name: '详情' }));
    await screen.findByRole('heading', { name });
    await userEvent.click(screen.getByRole('button', { name: '关闭' }));
    await userEvent.click(within(screen.getByRole('row', { name: new RegExp(name) })).getByRole('button', { name: '详情' }));
    await screen.findByRole('heading', { name });
    await userEvent.click(screen.getByRole('button', { name: '完整适配', exact: true }));
    await screen.findByRole('option', { name: /来源配置/ });
    expect(screen.getByLabelText('目标系统配置').value).toBe(configID);
    expect(screen.getByLabelText('关机客户机').value).toBe(terminalID);
  });
  it('分组和镜像既有 profileId 导航仍打开机型详情', async () => {
    renderPage({ profileId: 'h-1' });
    expect(await screen.findByRole('heading', { name: model.name })).toBeTruthy();
    expect(screen.getByRole('textbox', { name: '搜索机型 / 网卡' }).value).toBe('');
    expect(screen.queryByRole('button', { name: '开始适配' })).toBeNull();
  });
  it('同页重新进入机型撤掉精确筛选和来源适配上下文', async () => {
    stub({
      listModels: async () => [model, { ...model, id: 'same-name', nic_label: '8086:9999' }],
      listConfigs: async () => list([{ ID: 'source-config', Name: '来源配置' }, { ID: 'other-config', Name: '其他配置' }]),
      getModel: async () => ({ ...model, points: [{ config_id: 'other-config', failed: 1 }], terminal_list: [
        { id: 'first-terminal', name: '其他关机机器', state: 'offline' },
        { id: 'source-terminal', name: '来源客户机', state: 'offline' },
      ] }),
    });
    renderPage({ filterProfileId: 'h-1', filterProfileName: model.name, sourceConfigId: 'source-config', sourceTerminalId: 'source-terminal' });
    await screen.findByText('8086:100F');
    expect(screen.queryByText('8086:9999')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '侧栏进入机型' }));
    expect(await screen.findByText('8086:9999')).toBeTruthy();
    const row = screen.getByText('8086:100F').closest('tr');
    await userEvent.click(within(row).getByRole('button', { name: `完整适配 ${model.name}` }));
    await screen.findByRole('option', { name: /来源配置/ });
    expect(screen.getByLabelText('目标系统配置').value).toBe('other-config');
    expect(screen.getByLabelText('关机客户机').value).toBe('first-terminal');
  });
  it.each([
    ['t-2', '请先关闭来源客户机「正在运行」，再打开适配。'],
    ['missing-terminal', '来源客户机不在当前可选列表，请确认机型和关机状态。'],
  ])('来源客户机 %s 不可选时留空并明确说明，不替换成另一台关机机器', async (terminalID, hint) => {
    const adaptModel = vi.fn();
    stub({ adaptModel });
    renderPage({ filterProfileId: 'h-1', filterProfileName: model.name, sourceConfigId: 'c-1', sourceTerminalId: terminalID });
    await userEvent.click(await screen.findByRole('button', { name: `完整适配 ${model.name}` }));
    await screen.findByRole('option', { name: /测试客户机/ });
    expect(screen.getByLabelText('目标系统配置').value).toBe('c-1');
    expect(screen.getByLabelText('关机客户机').value).toBe('');
    expect(screen.queryByRole('option', { name: /正在运行/ })).toBeNull();
    expect(screen.getByText(hint)).toBeTruthy();
    await userEvent.type(screen.getByLabelText('还原点名称'), '来源适配');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    expect(screen.getByRole('alert').textContent).toContain('请选择配置、关机客户机');
    expect(adaptModel).not.toHaveBeenCalled();
  });
  it('机型详情在弹窗中打开，关闭后保留列表搜索和所在页', async () => {
    const models = Array.from({ length: 15 }, (_, i) => ({ ...model, id: `h-${i}`, name: `VMware 机型${i + 1}` }));
    stub({ listModels: async () => models, getModel: async id => ({ ...models.find(m => m.id === id), terminal_list: [], reduction_list: [] }) });
    renderPage();
    const search = await screen.findByRole('textbox', { name: '搜索机型 / 网卡' });
    await screen.findByText('VMware 机型1');
    await userEvent.type(search, 'VMware');
    await userEvent.click(screen.getByRole('button', { name: '下一页' }));
    const row = screen.getByRole('row', { name: /VMware 机型11/ });
    await userEvent.click(within(row).getByRole('button', { name: '详情' }));
    await screen.findByRole('heading', { name: 'VMware 机型11' });
    expect(screen.getByRole('textbox', { name: '搜索机型 / 网卡' }).value).toBe('VMware');
    expect(screen.getByRole('row', { name: /VMware 机型11/ })).toBeTruthy();
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('heading', { name: 'VMware 机型11' })).toBeNull();
    expect(screen.getByRole('textbox', { name: '搜索机型 / 网卡' }).value).toBe('VMware');
    expect(screen.getByRole('row', { name: /VMware 机型11/ })).toBeTruthy();
    expect(screen.queryByRole('row', { name: /VMware 机型1\s/ })).toBeNull();
  });
  it('适配只选目标配置、关机客户机、还原点名并跟踪任务', async () => {
    const adaptModel = vi.fn(async () => ({ task_id: 'task-1', session_id: 'pe-1' }));
    stub({ adaptModel, getTask: async () => ({ ID: 'task-1', Status: 'running', Progress: 20, Message: '引导客户机' }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    await screen.findByRole('option', { name: /测试客户机/ });
    expect(screen.queryByRole('option', { name: /正在运行/ })).toBeNull();
    expect(screen.getAllByRole('combobox')).toHaveLength(2);
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), 'e1000 可开机');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    await waitFor(() => expect(adaptModel).toHaveBeenCalledWith('t-1', { config_id: 'c-1', name: 'e1000 可开机' }));
    expect(await screen.findByText('引导客户机')).toBeTruthy();
  });

  it('适配保存成功再次刷新调用方，提示重新开机验证而非已经验证', async () => {
    const onDone = vi.fn();
    stub({ adaptModel: async () => ({ task_id: 'saved-task' }), getTask: async () => ({ Status: 'success', Progress: 100 }) });
    render(<StoreProvider><ConfirmProvider><ModelAdapt id="h-1" initialConfigID="c-1" onClose={() => {}} onDone={onDone}/></ConfirmProvider></StoreProvider>);
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.type(screen.getByLabelText('还原点名称'), '新配置点');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    expect(await screen.findByText(/再次开机验证 Windows/)).toBeTruthy();
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(/本机已验证/)).toBeNull();
  });
  it.each([
    ['pending', 0], ['running', 50], ['success', 0], ['failed', 50],
  ])('已提交适配 %s 以可访问进度显示实际百分比 %i 并保留日志跳转', async (status, progress) => {
    stub({ adaptModel: async () => ({ task_id: 'progress-task' }), getTask: async () => ({ Status: status, Progress: progress }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: `完整适配 ${model.name}` }));
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '进度回归');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    const bar = await screen.findByRole('progressbar', { name: '完整适配进度' });
    await waitFor(() => expect(bar.getAttribute('aria-valuenow')).toBe(String(progress)));
    expect(bar.getAttribute('aria-valuemin')).toBe('0');
    expect(bar.getAttribute('aria-valuemax')).toBe('100');
    expect(screen.getByText(`${progress}%`)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '查看任务与日志' }));
    expect(JSON.parse(screen.getByLabelText('当前路由').textContent)).toEqual({ page: 'tasks', params: { taskId: 'progress-task' } });
  });
  it('任务进度尚未读取时不声明零进度，返回真实零值后准确显示', async () => {
    let resolveTask;
    const pendingTask = new Promise(resolve => { resolveTask = resolve; });
    stub({ adaptModel: async () => ({ task_id: 'loading-task' }), getTask: async () => pendingTask });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: `完整适配 ${model.name}` }));
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '进度待读取');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    const bar = await screen.findByRole('progressbar', { name: '完整适配进度' });
    expect(bar.getAttribute('aria-valuenow')).toBeNull();
    expect(screen.queryByText('0%')).toBeNull();
    expect(screen.getByText('读取中…')).toBeTruthy();
    await act(async () => { resolveTask({ Status: 'pending', Progress: 0 }); });
    await waitFor(() => expect(bar.getAttribute('aria-valuenow')).toBe('0'));
    expect(screen.getByText('0%')).toBeTruthy();
  });

  it('目标系统配置排除数据盘和 Linux，优先本机型缺适配配置并说明影响及开机顺序', async () => {
    stub({
      listImages: async () => list([{ ID: 'data', Name: '数据盘', Purpose: 'data', OSType: 'windows' }, { ID: 'linux', Name: 'Ubuntu', OSType: 'linux' }, { ID: 'i-1', Name: 'Windows 11', Purpose: 'system', OSType: 'windows' }]),
      listConfigs: async id => list(id === 'i-1' ? [{ ID: 'unrelated', Name: '未使用配置' }, { ID: 'used', Name: '教学配置' }] : [{ ID: `${id}-cfg`, Name: 'default' }]),
      getModel: async () => ({ ...model, points: [{ config_id: 'used', config_name: '教学配置', failed: 1 }], terminal_list: [{ id: 't-1', name: '测试客户机', state: 'offline' }] }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    const configs = await screen.findByLabelText('目标系统配置');
    await waitFor(() => expect(configs.value).toBe('used'));
    expect(within(configs).queryByRole('option', { name: /数据盘/ })).toBeNull();
    expect(within(configs).queryByRole('option', { name: /Ubuntu/ })).toBeNull();
    const modal = screen.getByText('完整适配 · VMware E1000').closest('.card');
    expect(within(modal).getByText(/新还原点会设为配置当前点/).textContent).toContain('所有客户机');
    expect(within(modal).getByText(/开机进入维护环境/)).toBeTruthy();
    expect(within(modal).getByText(/等待任务成功/)).toBeTruthy();
    expect(within(modal).getByText(/再次开机验证 Windows/)).toBeTruthy();
  });
  it('没有匹配的使用配置时必须明确选择，不默认列表第一条', async () => {
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    const configs = await screen.findByLabelText('目标系统配置');
    await screen.findByRole('option', { name: /Windows 11/ });
    expect(configs.value).toBe('');
    expect(within(configs).getByRole('option', { name: '请选择系统配置' })).toBeTruthy();
  });
  it.each([
    [{ Status: 'pending', Progress: 0, Message: '准备适配环境' }, '等待环境准备完成，按任务提示再开机'],
    [{ Status: 'running', Progress: 0, Message: '引导客户机：请开启测试客户机' }, '现在启动所选客户机，进入维护环境'],
    [{ Status: 'running', Progress: 50, Message: '生成配置：客户机已进入WinPE，正在自检' }, '等待处理完成并自动关机'],
    [{ Status: 'running', Progress: 90, Message: '保存还原点：客户机已关机，正在保存' }, '等待服务器保存成功；此时无需开机'],
    [{ Status: 'success', Progress: 100, Message: '机型适配成功' }, '再次开机验证 Windows'],
  ])('适配任务 %j 的下一步由实际阶段决定', async (task, text) => {
    stub({ adaptModel: async () => ({ task_id: 'task-1' }), getTask: async () => task });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '保留适配点');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    const modal = screen.getByText('完整适配 · VMware E1000').closest('.card');
    expect(await within(modal).findByText(new RegExp(text))).toBeTruthy();
    if (task.Progress >= 50 || task.Status === 'pending') expect(screen.queryByText(/现在启动所选客户机/)).toBeNull();
    expect(api.terminalIdentity).not.toHaveBeenCalled();
  });
  it.each([
    ['registry: 离线Windows中找不到启动网卡的已发布INF', true],
    ['driver: 安装网卡驱动失败，请上传匹配的已签名驱动包', true],
    ['驱动日志上传失败：服务器网络回报超时', false],
    ['恢复环境无法连接服务器，请检查网络和启动网卡驱动', false],
  ])('适配失败 %s 只在确切缺包时引导上传', async (error, offer) => {
    stub({ adaptModel: async () => ({ task_id: 'task-1' }), getTask: async () => ({ Status: 'failed', Progress: 50, Error: error }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '保留适配点');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    expect(await screen.findByText(error)).toBeTruthy();
    expect(!!screen.queryByRole('button', { name: /上传.*驱动包/ })).toBe(offer);
  });
  it('缺驱动跳转上传后可返回原适配配置、客户机和名称，表单上下文不丢失', async () => {
    const adaptModel = vi.fn(async () => ({ task_id: 'task-1' }));
    stub({ adaptModel, getTask: async () => ({ Status: 'failed', Error: '离线Windows中找不到启动网卡的已发布INF' }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Windows 客户机 (2)' }));
    await userEvent.type(screen.getByRole('textbox', { name: '搜索客户机 / MAC / 分组' }), '测试');
    await userEvent.click(screen.getByRole('button', { name: '完整适配', exact: true }));
    await screen.findByRole('option', { name: /测试客户机/ });
    expect(screen.queryByRole('heading', { name: 'VMware E1000' })).toBeNull();
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '保留适配点');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    await userEvent.click(await screen.findByRole('button', { name: '上传匹配驱动包' }));
    await screen.findByText('Intel I219');
    expect(screen.queryByLabelText('还原点名称')).toBeNull();
    expect(screen.queryByRole('heading', { name: 'VMware E1000' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '返回机型' }));
    expect(await screen.findByLabelText('还原点名称')).toBeTruthy();
    expect(screen.getByLabelText('还原点名称').value).toBe('保留适配点');
    expect(screen.getByLabelText('目标系统配置').value).toBe('c-1');
    expect(screen.getByLabelText('关机客户机').value).toBe('t-1');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    await waitFor(() => expect(adaptModel).toHaveBeenCalledTimes(2));
    expect(adaptModel).toHaveBeenLastCalledWith('t-1', { config_id: 'c-1', name: '保留适配点' });
    await userEvent.keyboard('{Escape}');
    expect(screen.getByRole('heading', { name: 'VMware E1000' })).toBeTruthy();
    expect(screen.getByRole('textbox', { name: '搜索客户机 / MAC / 分组' }).value).toBe('测试');
  });
  it('适配失败仍在保存阶段时保留任务，只查看任务而不发起新的适配', async () => {
    const terminalIdentity = vi.fn(async () => ({ next_boot: { kind: 'wait', reason: 'finalizing', mode: 'adapt', state: 'finalizing', task_id: 'task-1' } }));
    const adaptModel = vi.fn(async () => ({ task_id: 'task-1' }));
    stub({ adaptModel, terminalIdentity, getTask: async () => ({ Status: 'failed', Progress: 50, Error: '保存还原点未完成，请重启服务重试保存（适配盘已保留）' }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '保留适配点');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    await screen.findByText(/保存还原点未完成/);
    await waitFor(() => expect(terminalIdentity).toHaveBeenCalledWith('t-1'));
    expect(await screen.findByText(/请查看任务，等待保存完成/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: '修改并重试' })).toBeNull();
    expect(screen.queryByRole('button', { name: '开始适配' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '查看任务与日志' }));
    expect(screen.getByLabelText('当前路由').textContent).toContain('"taskId":"task-1"');
    expect(adaptModel).toHaveBeenCalledTimes(1);
  });
  it('失败后维护状态读取失败需重查，解除占用后能保留表单再次提交', async () => {
    const terminalIdentity = vi.fn().mockRejectedValueOnce(new Error('连接超时')).mockResolvedValueOnce({ next_boot: { kind: 'system', reason: 'unverified' } });
    const adaptModel = vi.fn(async () => ({ task_id: 'task-1' }));
    stub({ adaptModel, terminalIdentity, getTask: async () => ({ Status: 'failed', Error: '适配环境准备失败' }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '保留适配点');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    expect(await screen.findByText(/维护状态读取失败：连接超时/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: '修改并重试' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '重试维护状态' }));
    await userEvent.click(await screen.findByRole('button', { name: '修改并重试' }));
    expect(screen.getByLabelText('还原点名称').value).toBe('保留适配点');
    expect(screen.getByLabelText('目标系统配置').value).toBe('c-1');
    expect(screen.getByLabelText('关机客户机').value).toBe('t-1');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    await waitFor(() => expect(adaptModel).toHaveBeenCalledTimes(2));
  });
  it('失败后另一维护任务仍占用客户机时不可重试，并定位阻塞任务', async () => {
    stub({ adaptModel: async () => ({ task_id: 'task-1' }), getTask: async () => ({ Status: 'failed', Error: '适配环境准备失败' }),
      terminalIdentity: async () => ({ next_boot: { kind: 'winpe', reason: 'pending_probe', state: 'pending', task_id: 'blocking-task' } }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '保留适配点');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    expect(await screen.findByText(/还有未完成的维护任务/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: '修改并重试' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '查看任务与日志' }));
    expect(screen.getByLabelText('当前路由').textContent).toContain('"taskId":"blocking-task"');
  });
  it('上传返回时重新确认维护占用，保存中的任务不能因返回表单而绕过', async () => {
    const terminalIdentity = vi.fn().mockResolvedValueOnce({ next_boot: { kind: 'system', reason: 'unverified' } })
      .mockResolvedValue({ next_boot: { kind: 'wait', reason: 'finalizing', state: 'finalizing', task_id: 'task-1' } });
    stub({ terminalIdentity, adaptModel: async () => ({ task_id: 'task-1' }), getTask: async () => ({ Status: 'failed', Error: '离线Windows中找不到启动网卡的已发布INF' }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    await screen.findByRole('option', { name: /测试客户机/ });
    await userEvent.selectOptions(screen.getByLabelText('目标系统配置'), 'c-1');
    await userEvent.type(screen.getByLabelText('还原点名称'), '保留适配点');
    await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
    await userEvent.click(await screen.findByRole('button', { name: '上传匹配驱动包' }));
    await screen.findByText('Intel I219');
    await userEvent.click(screen.getByRole('button', { name: '返回机型' }));
    expect(await screen.findByText(/请查看任务，等待保存完成/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: '开始适配' })).toBeNull();
    expect(screen.queryByRole('button', { name: '修改并重试' })).toBeNull();
    expect(terminalIdentity).toHaveBeenCalledTimes(2);
  });
  it('没有关机客户机时不能提交，加载失败可见', async () => {
    stub({ getModel: async () => ({ ...model, terminal_list: [{ id: 't-2', state: 'online' }] }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    expect(await screen.findByText(/请先关闭一台/)).toBeTruthy();
    expect(screen.getByRole('button', { name: '开始适配' }).disabled).toBe(true);
  });
  it.each([
    [[], '当前没有该机型的 Windows 客户机，请先在客户机管理中为一台该机型的客户机分配 Windows 镜像。'],
    [[{ id: 't-2', state: 'online' }], '请先关闭一台该机型的 Windows 客户机，再打开适配。'],
  ])('只有历史记录或 Windows 客户机全在线时提示正确的准备步骤', async (terminalList, hint) => {
    stub({ getModel: async () => ({ ...model, terminals: terminalList.length, terminal_list: terminalList }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '完整适配 VMware E1000' }));
    expect(await screen.findByText(hint)).toBeTruthy();
    expect(screen.getByRole('button', { name: '开始适配' }).disabled).toBe(true);
  });
  it('详情客户机默认十条分页，可搜索分组并切换每页数量', async () => {
    const terminals = Array.from({ length: 25 }, (_, i) => ({
      id: `t-${i + 1}`, name: `客户机${i + 1}`, mac: `0050560000${String(i + 1).padStart(2, '0')}`,
      group_name: i === 24 ? '实验室' : '教学区', state: 'offline',
    }));
    stub({ getModel: async () => ({ ...model, terminal_list: terminals, adaptations: [] }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Windows 客户机 (25)' }));
    await screen.findByText('客户机1');
    expect(screen.queryByText('客户机11')).toBeNull();
    const clients = screen.getByRole('region', { name: '机型客户机' });
    await userEvent.click(within(clients).getByRole('button', { name: '下一页' }));
    expect(within(clients).getByText('客户机11')).toBeTruthy();
    expect(within(clients).queryByText('客户机1')).toBeNull();
    await userEvent.selectOptions(within(clients).getByLabelText('每页条数'), '20');
    expect(within(clients).getByText('客户机20')).toBeTruthy();
    expect(within(clients).queryByText('客户机21')).toBeNull();
    await userEvent.type(within(clients).getByRole('textbox', { name: '搜索客户机 / MAC / 分组' }), '实验室');
    expect(within(clients).getByText('客户机25')).toBeTruthy();
    expect(within(clients).queryByText('客户机1')).toBeNull();
  });
  it('机型记录展示业务名称，两个列表独立搜索分页并能定位任务', async () => {
    const terminals = Array.from({ length: 25 }, (_, i) => ({ id: `t-${i}`, name: `客户机${i + 1}`, mac: `MAC${i}`, state: 'offline' }));
    const reductions = Array.from({ length: 25 }, (_, i) => ({
      id: `a-${i}`, reduction_id: `internal-reduction-${i}`, reduction_name: `可开机点${i + 1}`,
      image_id: 'i-1', image_name: 'Windows 11', config_id: 'qa-config', config_name: '教学配置',
      source: i === 0 ? 'inherited' : 'observed', task_id: i === 0 ? 'origin-task' : '',
      current: i === 0, created_at: '2026-10-07T04:00:00Z',
    }));
    stub({ getModel: async () => ({ ...model, terminal_list: terminals, reduction_list: reductions, adaptations: [{ ID: 'a-0', ReductionID: 'internal-reduction-0', Source: 'inherited' }] }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    await screen.findByRole('region', { name: '机型记录' });
    expect(screen.queryByLabelText('机型名称')).toBeNull();
    expect(screen.getByRole('textbox', { name: '搜索机型 / 网卡' })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Windows 客户机 (25)' }));
    const clients = screen.getByRole('region', { name: '机型客户机' });
    await userEvent.click(within(clients).getByRole('button', { name: '下一页' }));
    await userEvent.click(screen.getByRole('button', { name: /^记录/ }));
    const points = screen.getByRole('region', { name: '机型记录' });
    expect(within(points).getAllByText('Windows 11')).toHaveLength(10);
    expect(within(points).getAllByText('教学配置')).toHaveLength(10);
    expect(within(points).queryByText('internal-reduction-0')).toBeNull();
    expect(within(points).queryByText('可开机点11')).toBeNull();
    await userEvent.click(within(points).getByRole('button', { name: '下一页' }));
    expect(within(points).getByText('可开机点11')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: /^Windows 客户机 \(/ }));
    expect(screen.getByText('客户机11')).toBeTruthy();
    expect(screen.queryByText('客户机1')).toBeNull();
    await userEvent.type(screen.getByRole('textbox', { name: '搜索客户机 / MAC / 分组' }), '客户机25');
    await userEvent.click(screen.getByRole('button', { name: /^记录/ }));
    expect(screen.getByText('可开机点11')).toBeTruthy();
    await userEvent.type(screen.getByRole('textbox', { name: '搜索镜像 / 配置 / 还原点' }), '可开机点1');
    await userEvent.click(screen.getByRole('button', { name: '查看任务' }));
    expect(screen.getByLabelText('当前路由').textContent).toContain('"taskId":"origin-task"');
    await userEvent.click(screen.getAllByRole('button', { name: 'Windows 11' })[0]);
    expect(screen.getByLabelText('当前路由').textContent).toBe('{"page":"images","params":{"imageId":"i-1"}}');
    await userEvent.click(screen.getAllByRole('button', { name: '教学配置' })[0]);
    expect(screen.getByLabelText('当前路由').textContent).toBe('{"page":"images","params":{"tab":"reductions","imageId":"i-1","configId":"qa-config"}}');
  });

  it('机型记录在还原点下写明来源，保留五列和统一操作及原任务链接', async () => {
    const longName = 'Windows 11 教学系统添加 VMware VMXNET3 网卡并保留 E1000 配置的完整适配还原点名称';
    const reductions = [
      { id: 'a-adapted', source: 'adapted', reduction_name: '已适配点', task_id: 'adapt-task' },
      { id: 'a-observed', source: 'observed', reduction_name: '开机记录点', task_id: '' },
      { id: 'a-inherited', source: 'inherited', reduction_name: longName, task_id: 'origin-task', current: true },
    ].map(a => ({ ...a, image_id: 'i-1', image_name: 'Windows 11', config_id: 'c-1', config_name: '教学配置', created_at: '2026-10-07T04:00:00Z' }));
    stub({ getModel: async () => ({ ...model, terminal_list: [], reduction_list: reductions }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    const points = await screen.findByRole('region', { name: '机型记录' });
    expect(within(points).getAllByRole('columnheader').map(h => h.textContent)).toEqual(['还原点', '镜像', '配置', '记录时间', '操作']);
    expect(screen.getByRole('button', { name: '记录 (3)' })).toBeTruthy();
    // 来源说清楚：只有开机成功是开过机，完整适配和沿用都只是写过配置。
    expect(within(within(points).getByRole('row', { name: /已适配点/ })).getByText('完整适配')).toBeTruthy();
    expect(within(within(points).getByRole('row', { name: /开机记录点/ })).getByText('开机成功')).toBeTruthy();
    expect(within(within(points).getByRole('row', { name: new RegExp(longName) })).getByText('沿用上一个还原点')).toBeTruthy();
    expect(within(points).getAllByRole('row')).toHaveLength(4);
    expect(within(points).queryByText('配置当前点')).toBeNull();
    expect(within(points).queryByRole('button', { name: /依据说明|查看来源任务/ })).toBeNull();
    expect(within(points).getByTitle(longName).textContent).toBe(longName);
    const observed = within(points).getByRole('row', { name: /开机记录点/ });
    expect(within(observed).getByRole('button', { name: '查看还原点' })).toBeTruthy();
    expect(within(observed).queryByRole('button', { name: '查看任务' })).toBeNull();
    expect(within(points).getAllByRole('button', { name: '查看任务' })).toHaveLength(2);
    await userEvent.click(within(within(points).getByRole('row', { name: /已适配点/ })).getByRole('button', { name: '查看任务' }));
    expect(JSON.parse(screen.getByLabelText('当前路由').textContent)).toEqual({ page: 'tasks', params: { taskId: 'adapt-task' } });
    await userEvent.click(within(within(points).getByRole('row', { name: new RegExp(longName) })).getByRole('button', { name: '查看任务' }));
    expect(JSON.parse(screen.getByLabelText('当前路由').textContent)).toEqual({ page: 'tasks', params: { taskId: 'origin-task' } });
  });

  it('开机失败的当前点按业务名称给出完整适配，改名使用独立小弹窗', async () => {
    stub({ getModel: async () => ({ ...model, points: [{ config_id: 'c-1', config_name: '教学配置', reduction_name: '期中', terminals: 2, ok: 1, failed: 1, waiting: 0 }], reduction_list: [], terminal_list: [] }), renameModel: async () => ({}) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    expect(await screen.findByText('配置「教学配置」当前还原点「期中」：1 台已开机 · 1 台开机失败')).toBeTruthy();
    expect(screen.getByText(/^没进系统的机器可以做一次完整适配/)).toBeTruthy();
    expect(screen.queryByLabelText('机型名称')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '改名' }));
    expect(screen.queryByRole('heading', { name: 'VMware E1000' })).toBeNull();
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByLabelText('机型名称')).toBeNull();
    expect(screen.getByRole('heading', { name: 'VMware E1000' })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '改名' }));
    await userEvent.clear(screen.getByLabelText('机型名称'));
    await userEvent.type(screen.getByLabelText('机型名称'), '教室 E1000');
    await userEvent.click(screen.getByRole('button', { name: '保存名称' }));
    await waitFor(() => expect(api.renameModel).toHaveBeenCalledWith('h-1', { name: '教室 E1000' }));
    await userEvent.click(screen.getByRole('button', { name: '完整适配 教学配置' }));
    expect(await screen.findByRole('button', { name: '开始适配' })).toBeTruthy();
    expect(screen.getByLabelText('目标系统配置').value).toBe('c-1');
  });
  it('还没开过机的机型只说明下次开机自动准备，当前点不给完整适配，底部留兜底入口', async () => {
    stub({ getModel: async () => ({ ...model, status: 'waiting', boot_failed: 0, points: [{ config_id: 'c-1', config_name: '教学配置', reduction_name: '0', terminals: 1, waiting: 1 }], terminal_list: [], reduction_list: [] }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    expect(await screen.findByText('配置「教学配置」当前还原点「0」：1 台未确认')).toBeTruthy();
    expect(screen.getAllByText('等待首次开机').length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: '完整适配 教学配置' })).toBeNull();
    expect(screen.getByText(/仍然进不了系统时/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '完整适配', exact: true }));
    expect(await screen.findByRole('button', { name: '开始适配' })).toBeTruthy();
  });
  it('机型详情打开时自动刷新已完成任务的状态与可开机还原点', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let completed = false;
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      stub({ getModel: async () => ({ ...model, status: completed ? 'ok' : 'adapting', terminal_list: [],
        reduction_list: completed ? [{ id: 'a-1', reduction_name: '刚完成适配点', config_name: '教学配置', image_name: 'Windows 11', current: true, source: 'adapted' }] : [] }) });
      renderPage();
      await user.click(await screen.findByRole('button', { name: '详情' }));
      await screen.findByText('适配中');
      completed = true;
      await act(async () => { vi.advanceTimersByTime(5000); });
      expect(await screen.findByText('刚完成适配点')).toBeTruthy();
      expect(screen.getAllByText('正常').length).toBeGreaterThan(0);
      expect(screen.queryByText('适配中')).toBeNull();
    } finally { vi.useRealTimers(); }
  });
  it('说明收在原生详情中，驱动包何时用写清楚', async () => {
    renderPage();
    await screen.findByText('VMware E1000');
    const modelGuide = screen.getByText('机型与开机说明').closest('details');
    expect(modelGuide.open).toBe(false);
    await userEvent.click(screen.getByText('机型与开机说明'));
    expect(screen.getByText(/收到 Windows 开机成功回报才算正常/)).toBeTruthy();
    expect(screen.getByText(/选择目标系统配置和一台关机客户机/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: /^驱动包/ }));
    const driverGuide = screen.getByText('驱动包何时使用').closest('details');
    expect(driverGuide.open).toBe(false);
    await userEvent.click(screen.getByText('驱动包何时使用'));
    expect(screen.getByText(/完整适配优先用镜像自带驱动/)).toBeTruthy();
    expect(screen.getByText(/INF、SYS、CAT/)).toBeTruthy();
    expect(screen.getByText(/适用于 Windows x64 的已签名驱动包/)).toBeTruthy();
    expect(screen.getByText(/维护环境也需有匹配驱动才能联网/)).toBeTruthy();
    expect(screen.getByText(/返回机型页.*重新提交/)).toBeTruthy();
  });
  it.each([
    ['ok', { boot_ok: 2, boot_failed: 0, boot_waiting: 0 }, '正常', '2 台已开机', false],
    ['waiting', { boot_ok: 1, boot_failed: 0, boot_waiting: 1 }, '等待首次开机', '1 台已开机 · 1 台未确认', false],
    ['failed', { boot_ok: 1, boot_failed: 1, boot_waiting: 0 }, '开机失败', '1 台开机失败', true],
    ['adapting', { boot_ok: 0, boot_failed: 1, boot_waiting: 0 }, '适配中', '1 台开机失败', false],
    ['idle', { boot_ok: 0, boot_failed: 0, boot_waiting: 0 }, '暂无客户机', '', false],
  ])('列表状态 %s 按开机结果显示，只有开机失败才给完整适配', async (status, counts, label, summary, adapt) => {
    stub({ listModels: async () => [{ ...model, status, ...counts }] });
    renderPage();
    const row = (await screen.findByText(model.name)).closest('tr');
    expect(within(row).getByText(label)).toBeTruthy();
    if (summary) expect(within(row).getByText(summary)).toBeTruthy();
    expect(!!within(row).queryByRole('button', { name: `完整适配 ${model.name}` })).toBe(adapt);
    expect(within(row).getByRole('button', { name: '详情' })).toBeTruthy();
    expect(within(row).queryByText(/已有兼容记录|待确认|需适配|模板可启动/)).toBeNull();
  });
  it('详情客户机列表显示每台的开机情况', async () => {
    stub({ getModel: async () => ({ ...model, reduction_list: [], terminal_list: [
      { id: 't-1', name: '已开机的', state: 'offline', boot: 'ok' },
      { id: 't-2', name: '没进系统的', state: 'offline', boot: 'failed' },
      { id: 't-3', name: '还没开的', state: 'offline', boot: 'waiting' },
    ] }) });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Windows 客户机 (3)' }));
    const clients = screen.getByRole('region', { name: '机型客户机' });
    expect(within(clients).getAllByRole('columnheader').map(h => h.textContent)).toEqual(['客户机', 'MAC', '分组', '开机情况', '状态']);
    expect(within(within(clients).getByRole('row', { name: /已开机的/ })).getByText('已开机')).toBeTruthy();
    expect(within(within(clients).getByRole('row', { name: /没进系统的/ })).getByText('开机失败')).toBeTruthy();
    expect(within(within(clients).getByRole('row', { name: /还没开的/ })).getByText('未确认')).toBeTruthy();
  });
  it('详情加载失败可重试，空列表清楚可见并可用键盘切换和返回', async () => {
    let reject;
    const getModel = vi.fn().mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; }))
      .mockResolvedValueOnce({ ...model, terminal_list: [], reduction_list: [], adaptations: [] });
    stub({ getModel });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: '详情' }));
    expect(screen.getByRole('status', { name: '机型详情加载状态' }).textContent).toContain('加载中');
    await act(async () => { reject(new Error('机型详情读取失败')); });
    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('机型详情读取失败');
    await userEvent.click(screen.getByRole('button', { name: '重试' }));
    expect(await screen.findByText(/暂无记录/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '记录 (0)' }));
    await userEvent.tab();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Windows 客户机 (0)' }));
    await userEvent.keyboard('{Enter}');
    expect(screen.getByText('暂无该机型的 Windows 客户机')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '关闭' }));
    expect(await screen.findByRole('button', { name: '详情' })).toBeTruthy();
    expect(screen.getByLabelText('当前路由').textContent).toBe('{"page":"models","params":{}}');
  });
});
