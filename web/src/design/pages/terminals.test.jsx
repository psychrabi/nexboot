import React from 'react';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider, useStore } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageTerminals } from './terminals.jsx';
import { PageModels } from './models.jsx';

// 客户机页：后端部分接口失败时列表仍可用，筛选结果正确，保存超管机必须填还原点名。

const list = (items) => ({ items, total: items.length });
const TERMINALS = [
  { ID: 'terminal-A', Name: '一号机', MAC: 'AABBCCDDEE01', IP: '192.168.50.101', GroupID: 'g-1', IsSuper: false, State: 'online' },
  { ID: 'terminal-B', Name: '二号机', MAC: 'AABBCCDDEE02', IP: '192.168.50.102', GroupID: 'g-1', IsSuper: true, State: 'offline' },
  { ID: 'terminal-C', Name: '美术教室机', MAC: 'AABBCCDDEE03', IP: '192.168.60.101', GroupID: 'g-2', IsSuper: false, State: 'offline' },
  { ID: 'terminal-D', Name: '还开着的超管机', MAC: 'AABBCCDDEE04', IP: '192.168.50.104', GroupID: 'g-1', IsSuper: true, State: 'online' },
];
const GROUPS = [
  { ID: 'g-1', Name: '教学一班', SystemImageID: 'win11' },
  { ID: 'g-2', Name: '美术教室', SystemImageID: 'win11' },
];

function stubApi(overrides = {}) {
  const base = {
    listTerminals: async () => list(TERMINALS),
    listGroups: async () => list(GROUPS),
    listImages: async () => list([{ ID: 'win11', Name: 'Win 11', OSType: 'windows', State: 'normal' }]),
    listTasks: async () => list([]),
    listAlarms: async () => list([]),
    listPools: async () => list([]),
    listClusterNodes: async () => list([]),
    superDisks: async () => list([]),
    terminalModel: async () => ({}),
    terminalIdentity: async () => ({ next_boot: { kind: 'unknown', reason: 'unconfigured' } }),
    listInstallations: async () => list([]),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

function RouteProbe() {
  const { route } = useStore();
  return <output aria-label="当前路由">{JSON.stringify(route)}</output>;
}
const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageTerminals/><RouteProbe/></ConfirmProvider></StoreProvider>,
);
function WorkflowPages() {
  const { route } = useStore();
  return route.page === 'models' ? <PageModels/> : <PageTerminals/>;
}
const renderWorkflow = () => render(<StoreProvider><ConfirmProvider><WorkflowPages/><RouteProbe/></ConfirmProvider></StoreProvider>);

// 多节点时显示客户机落在哪台服务器（StorageServerID），单机时不显示这一列。
describe('终端所在节点', () => {
  const CLUSTER = [
    { id: 'n1', ip: '10.0.0.3', ha_state: 'active', online: true },
    { id: 'n2', ip: '10.0.0.4', ha_state: 'standby', online: true },
  ];
  afterEach(() => vi.restoreAllMocks());

  it('多节点时列出客户机落在哪台', async () => {
    stubApi({
      listClusterNodes: async () => list(CLUSTER),
      listTerminals: async () => list([
        { ...TERMINALS[0], StorageServerID: 'n2' },
        { ...TERMINALS[1], StorageServerID: null },
      ]),
    });
    renderPage();
    const row = (await screen.findByText('一号机')).closest('tr');
    expect(row.textContent).toContain('10.0.0.4');
    expect(screen.getAllByText('节点').length).toBeGreaterThan(0);
  });

  it('单机不加这一列', async () => {
    stubApi({ listClusterNodes: async () => list([{ id: 'n1', ip: '10.0.0.3', ha_state: 'active', online: true }]) });
    renderPage();
    await screen.findByText('一号机');
    expect(screen.queryByText('节点')).toBeNull();
  });
});

it('客户机系统盘保留简短机型和启动说明，不暴露身份诊断或普遍取证操作', async () => {
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'VMware E1000', status: 'waiting' }),
    terminalIdentity: async () => ({ identity: { InstanceID: 'PCI\\VEN_8086&DEV_100F\\PRIVATE_INSTANCE', Source: 'captured', LayerReductionRef: 'IDENT-internal-ready' }, stale: false,
      next_boot: { kind: 'winpe', reason: 'pending_adapt', mode: 'adapt', state: 'pending', task_id: 'adapt-task' } }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  const basic = await screen.findByText('基础信息');
  const modal = basic.closest('.card');
  const system = within(modal).getByText('系统盘');
  const profile = await within(modal).findByText(/VMware E1000/);
  expect(basic.compareDocumentPosition(profile) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(system.compareDocumentPosition(profile) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(await within(modal).findByText(/开机进入维护环境做完整适配/)).toBeTruthy();
  expect(within(modal).queryByRole('button', { name: '重新取证' })).toBeNull();
  expect(within(modal).queryByText('PCI\\VEN_8086&DEV_100F\\PRIVATE_INSTANCE')).toBeNull();
  expect(within(modal).queryByText('启动诊断')).toBeNull();
  expect(within(modal).queryByText('身份来源')).toBeNull();
  expect(within(modal).queryByText('身份层状态')).toBeNull();
  expect(within(modal).queryByText('IDENT-internal-ready')).toBeNull();
});

it.each([
  [{ status: 'waiting' }, '本机在当前还原点上还没有开机成功的回报'],
  [{ status: 'ok', boot_verified_at: '2026-10-08T01:00:00Z' }, '本机已开机成功'],
  [{ status: 'failed', failure: { stage: 'windows', at: '2026-10-08T01:00:00Z' } }, '上次未收到 Windows 成功回报'],
  [{ status: 'failed', failure: { stage: 'template', at: '2026-10-08T01:00:00Z' } }, '网卡模板两次没进系统'],
  [{ status: 'waiting', failure: { stage: 'sanhook', code: '1058021379', at: '2026-10-08T01:00:00Z' } }, '上次连接系统盘失败'],
])('当前点依据 %j 只按本机开机结果说话，不借记录宣称能开机', async (evidence, label) => {
  stubApi({
    listTerminals: async () => list([{ ...TERMINALS[2], State: 'online' }]),
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', config_id: 'cfg-1', reduction_id: 'red-1', ...evidence }),
    terminalIdentity: async () => ({ next_boot: { kind: 'system', reason: 'direct' } }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  expect(await screen.findByText(label)).toBeTruthy();
  expect(screen.queryByText(/蓝屏|已适配|待确认|兼容记录/)).toBeNull();
  if (!evidence.boot_verified_at) expect(screen.queryByText('本机已开机成功')).toBeNull();
  expect(!!screen.queryByRole('button', { name: '完整适配' })).toBe(evidence.status === 'failed');
});

it('客户机开机失败后完整适配进入精确筛选列表，不自动弹窗，编辑搜索恢复普通查询', async () => {
  const getModel = vi.fn(async () => ({}));
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', config_id: 'cfg-used', reduction_id: 'red-1', status: 'failed', failure: { stage: 'windows', at: '2026-10-08T01:00:00Z' } }),
    terminalIdentity: async () => ({ next_boot: { kind: 'system', reason: 'unverified' } }),
    listModels: async () => [
      { id: 'h-1', name: 'E1000', nic_label: '8086:100F', status: 'failed' },
      { id: 'h-same', name: 'E1000', nic_label: '8086:9999', status: 'failed' },
      { id: 'h-part', name: 'E1000 增强', nic_label: '8086:8888', status: 'failed' },
      { id: 'h-vmx', name: 'VMXNET3', nic_label: '15AD:07B0', status: 'failed' },
    ], getModel,
  });
  renderWorkflow();
  await userEvent.click(await screen.findByText('美术教室机'));
  await screen.findByText('上次未收到 Windows 成功回报');
  expect(screen.queryByRole('button', { name: '查看机型' })).toBeNull();
  await userEvent.click(screen.getByRole('button', { name: '完整适配' }));
  const search = await screen.findByRole('textbox', { name: '搜索机型 / 网卡' });
  expect(search.value).toBe('E1000');
  await screen.findByText('8086:100F');
  expect(screen.queryByText('8086:9999')).toBeNull();
  expect(screen.queryByText('8086:8888')).toBeNull();
  expect(screen.queryByText('15AD:07B0')).toBeNull();
  expect(screen.queryByText('基础信息')).toBeNull();
  expect(screen.queryByRole('heading')).toBeNull();
  expect(screen.queryByRole('button', { name: '开始适配' })).toBeNull();
  expect(getModel).not.toHaveBeenCalled();
  expect(JSON.parse(screen.getByLabelText('当前路由').textContent).params).toEqual({ filterProfileId: 'h-1', filterProfileName: 'E1000', sourceConfigId: 'cfg-used', sourceTerminalId: 'terminal-C' });
  await userEvent.clear(search);
  expect(screen.getAllByRole('button', { name: /^完整适配 / })).toHaveLength(4);
  await userEvent.type(search, 'E1000');
  expect(screen.getAllByRole('button', { name: /^完整适配 / })).toHaveLength(3);
});

it.each([
  ['当前客户机', '美术教室机', 'AA:BB:CC:DD:EE:03'],
  ['另一台客户机', '二号机', 'AA:BB:CC:DD:EE:02'],
])('机型列表详情选择%s时打开对应客户机详情', async (_, name, mac) => {
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', config_id: 'cfg-used', reduction_id: 'red-1', status: 'failed' }),
    terminalIdentity: async () => ({ next_boot: { kind: 'system', reason: 'unverified' } }),
    listModels: async () => [{ id: 'h-1', name: 'E1000', nic_label: '8086:100F', status: 'failed' }],
    getModel: async () => ({ id: 'h-1', name: 'E1000', status: 'failed', terminal_list: [
      { id: 'terminal-C', name: '美术教室机', state: 'offline' },
      { id: 'terminal-B', name: '二号机', mac: 'AABBCCDDEE02', state: 'offline' },
    ] }),
  });
  renderWorkflow();
  await userEvent.click(await screen.findByText('美术教室机'));
  await userEvent.click(await screen.findByRole('button', { name: '完整适配' }));
  await userEvent.click(await screen.findByRole('button', { name: '详情' }));
  await userEvent.click(await screen.findByRole('button', { name: 'Windows 客户机 (2)' }));
  await userEvent.click(within(screen.getByRole('region', { name: '机型客户机' })).getByRole('button', { name }));
  const detail = (await screen.findByText('基础信息')).closest('.card');
  expect(within(detail).getAllByText(name).length).toBeGreaterThan(0);
  expect(within(detail).getByText(mac)).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'E1000' })).toBeNull();
  expect(JSON.parse(screen.getByLabelText('当前路由').textContent).page).toBe('terminals');
});

it('客户机导航机型页后缺驱动可上传并返回原适配表单', async () => {
  const adaptModel = vi.fn(async () => { throw new Error('离线Windows中找不到启动网卡的已发布INF'); });
  const uploadDriverPack = vi.fn(async () => ({}));
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', config_id: 'cfg-used', reduction_id: 'red-1', status: 'failed' }),
    terminalIdentity: async () => ({ next_boot: { kind: 'system', reason: 'unverified' } }),
    listModels: async () => [{ id: 'h-1', name: 'E1000', nic_label: '8086:100F', status: 'failed' }],
    listDriverPacks: async () => list([]),
    getModel: async () => ({ id: 'h-1', name: 'E1000', terminal_list: [{ id: 'terminal-C', name: '美术教室机', state: 'offline' }] }),
    listConfigs: async () => list([{ ID: 'cfg-used', Name: '当前教学配置' }]),
    adaptModel, uploadDriverPack,
  });
  renderWorkflow();
  await userEvent.click(await screen.findByText('美术教室机'));
  await userEvent.click(await screen.findByRole('button', { name: '完整适配' }));
  await userEvent.click(await screen.findByRole('button', { name: '完整适配 E1000' }));
  await screen.findByRole('option', { name: /当前教学配置/ });
  await userEvent.type(screen.getByLabelText('还原点名称'), '当前机型新点');
  await userEvent.click(screen.getByRole('button', { name: '开始适配' }));
  await userEvent.click(await screen.findByRole('button', { name: '上传匹配驱动包' }));
  await userEvent.click(await screen.findByRole('button', { name: '上传驱动包' }));
  expect(await screen.findByRole('button', { name: '上传并解析' })).toBeTruthy();
  expect(screen.queryByLabelText('还原点名称')).toBeNull();
  expect(screen.queryByText('基础信息')).toBeNull();
  await userEvent.type(screen.getByPlaceholderText('Intel I219 v12.19'), '本机网卡');
  const uploadModal = screen.getByPlaceholderText('Intel I219 v12.19').closest('.card');
  await userEvent.upload(uploadModal.querySelector('input[type=file]'), new File(['zip'], 'nic.zip', { type: 'application/zip' }));
  await userEvent.click(screen.getByRole('button', { name: '上传并解析' }));
  await waitFor(() => expect(uploadDriverPack).toHaveBeenCalled());
  const sent = uploadDriverPack.mock.calls[0][0];
  expect(sent.get('file').name).toBe('nic.zip');
  expect(sent.get('category')).toBe('boot_critical_nic');
  await userEvent.click(screen.getByRole('button', { name: '返回机型' }));
  expect(await screen.findByLabelText('还原点名称')).toBeTruthy();
  expect(screen.getByLabelText('还原点名称').value).toBe('当前机型新点');
  expect(screen.getByLabelText('目标系统配置').value).toBe('cfg-used');
  expect(screen.getByLabelText('关机客户机').value).toBe('terminal-C');
  expect(JSON.parse(screen.getByLabelText('当前路由').textContent).page).toBe('models');
  await userEvent.keyboard('{Escape}');
  expect(screen.queryByText('基础信息')).toBeNull();
  expect(screen.getByRole('textbox', { name: '搜索机型 / 网卡' }).value).toBe('E1000');
  expect(adaptModel).toHaveBeenCalledTimes(1);
});

it.each([
  { kind: 'winpe', mode: 'probe', task_id: 'probe-task' },
  { kind: 'wait', mode: 'adapt', reason: 'finalizing', task_id: 'adapt-task' },
])('客户机已有维护 %j 时完整适配导航禁用，不绕过维护占用', async next => {
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', config_id: 'cfg-1', reduction_id: 'red-1', status: 'failed' }),
    terminalIdentity: async () => ({ next_boot: next }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  const adapt = await screen.findByRole('button', { name: '完整适配' });
  expect(adapt.disabled).toBe(true);
  await userEvent.click(adapt);
  expect(screen.getByText('基础信息')).toBeTruthy();
  expect(screen.queryByRole('button', { name: '查看机型' })).toBeNull();
  expect(JSON.parse(screen.getByLabelText('当前路由').textContent).page).not.toBe('models');
});

it.each([
  [{ kind: 'system', reason: 'linux' }, '直接启动 Linux。'],
  [{ kind: 'system', reason: 'super' }, '启动超管环境，使用最新可用还原点。'],
  [{ kind: 'unknown', reason: 'unconfigured' }, '尚未配置启动系统，请检查分组的系统镜像与配置。'],
])('启动目标 %j 不套用 Windows 验证与适配入口', async (next, label) => {
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', config_id: 'cfg-1', reduction_id: 'red-1', boot_verified_at: '2026-10-08T01:00:00Z' }),
    terminalIdentity: async () => ({ next_boot: next }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  expect(await screen.findByText(label)).toBeTruthy();
  expect(screen.queryByText('本机已开机成功')).toBeNull();
  expect(screen.queryByRole('button', { name: '完整适配' })).toBeNull();
});

it('尚未解析到当前还原点时不能从客户机详情跳过系统配置去适配其他点', async () => {
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000' }),
    terminalIdentity: async () => ({ next_boot: { kind: 'unknown', reason: 'unconfigured' } }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  expect(await screen.findByText('尚未配置启动系统，请检查分组的系统镜像与配置。')).toBeTruthy();
  expect(screen.queryByRole('button', { name: '完整适配' })).toBeNull();
  expect(screen.queryByRole('button', { name: '查看机型' })).toBeNull();
});

it.each([
  ['普通客户机', false, 'red-applied', '已应用点'],
  ['超管机', true, 'red-latest', '最新可用点'],
])('系统盘展示%s实际下一次还原点，而非分组旧缓存', async (_, isSuper, reductionID, expectedName) => {
  stubApi({
    listTerminals: async () => list([{ ...TERMINALS[2], IsSuper: isSuper }]),
    listGroups: async () => list([{ ...GROUPS[1], SystemConfigID: 'cfg-1', SystemReductionID: 'red-group' }]),
    listConfigs: async () => list([{ ID: 'cfg-1', Name: '教学配置', DefaultReductionID: 'red-applied' }]),
    listReductions: async () => list([
      { ID: 'red-group', DisplayName: '旧分组点', Status: 'ready', CreatedAt: '2026-10-01T01:00:00Z' },
      { ID: 'red-applied', DisplayName: '已应用点', Status: 'ready', CreatedAt: '2026-10-02T01:00:00Z' },
      { ID: 'red-latest', DisplayName: '最新可用点', Status: 'ready', CreatedAt: '2026-10-03T01:00:00Z' },
    ]),
    terminalIdentity: async () => ({ next_boot: { kind: 'system', reason: isSuper ? 'super' : 'direct', image_id: 'win11', config_id: 'cfg-1', reduction_id: reductionID } }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  const modal = (await screen.findByText('基础信息')).closest('.card');
  expect(await within(modal).findByText(expectedName)).toBeTruthy();
  expect(within(modal).queryByText('旧分组点')).toBeNull();
  expect(within(modal).getByText('教学配置')).toBeTruthy();
});

it.each([
  [{ kind: 'system', reason: 'direct' }, '直接启动 Windows'],
  [{ kind: 'system', reason: 'linux' }, '直接启动 Linux'],
  [{ kind: 'system', reason: 'super' }, '启动超管环境'],
  [{ kind: 'system', reason: 'automatic_config' }, '直接启动 Windows'],
  [{ kind: 'system', reason: 'identity_rebuild' }, '直接启动 Windows'],
  [{ kind: 'system', reason: 'automatic_template' }, '自动生成网卡配置后启动 Windows'],
  [{ kind: 'winpe', reason: 'automatic_identity' }, '首次开机会进入维护环境'],
  [{ kind: 'winpe', reason: 'pending_identity', mode: 'identity', state: 'pending' }, '完成客户机启动准备'],
  [{ kind: 'winpe', reason: 'pending_probe', mode: 'probe', state: 'pending' }, '进行启动网卡检测'],
  [{ kind: 'winpe', reason: 'pending_adapt', mode: 'adapt', state: 'booted' }, '已进入维护环境，请等待'],
  [{ kind: 'wait', reason: 'preparing', state: 'preparing' }, '服务器正在准备维护任务'],
  [{ kind: 'wait', reason: 'finalizing', state: 'finalizing' }, '等待服务器保存适配结果'],
  [{ kind: 'system', reason: 'unverified' }, '尝试启动 Windows'],
  [{ kind: 'unknown', reason: 'unconfigured' }, '尚未配置启动系统'],
])('下一次启动按真实路由 %j 给行动说明', async (next, text) => {
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', status: 'ok' }),
    terminalIdentity: async () => ({ next_boot: next }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  const modal = (await screen.findByText('基础信息')).closest('.card');
  expect(await within(modal).findByText(new RegExp(text))).toBeTruthy();
  if (['automatic_config', 'identity_rebuild'].includes(next.reason)) {
    expect(within(modal).getByText('直接启动 Windows。')).toBeTruthy();
    expect(within(modal).queryByText(/服务器会准备客户机配置/)).toBeNull();
  }
});

it('维护适配目标跨镜像时显示实际目标业务名，不沿用分组系统', async () => {
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', config_id: 'old-config', reduction_id: 'old-red', boot_verified_at: '2026-10-08T01:00:00Z' }),
    listImages: async () => list([{ ID: 'win11', Name: '分组 Windows', OSType: 'windows' }, { ID: 'target-image', Name: '目标 Windows', OSType: 'windows' }]),
    listConfigs: async id => list(id === 'target-image' ? [{ ID: 'target-config', Name: '目标教学配置', DefaultReductionID: 'target-red' }] : [{ ID: 'old-config', Name: '旧分组配置' }]),
    listReductions: async id => list(id === 'target-config' ? [{ ID: 'target-red', DisplayName: '适配来源点', Status: 'ready' }] : []),
    terminalIdentity: async () => ({ next_boot: { kind: 'winpe', reason: 'pending_adapt', mode: 'adapt', state: 'pending', image_id: 'target-image', config_id: 'target-config', reduction_id: 'target-red' } }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  const modal = (await screen.findByText('基础信息')).closest('.card');
  expect(await within(modal).findByText('目标教学配置')).toBeTruthy();
  expect(within(modal).getByText('目标 Windows')).toBeTruthy();
  expect(within(modal).getByText('适配来源点')).toBeTruthy();
  expect(within(modal).queryByText('分组 Windows')).toBeNull();
  expect(within(modal).queryByText('本机已开机成功')).toBeNull();
});

it.each([
  [{ kind: 'winpe', reason: 'pending_adapt', mode: 'adapt', state: 'pending' }, '开机进入维护环境做完整适配'],
  [{ kind: 'wait', reason: 'preparing', mode: 'adapt', state: 'preparing' }, '服务器正在准备维护任务'],
  [{ kind: 'wait', reason: 'finalizing', mode: 'adapt', state: 'finalizing' }, '等待服务器保存适配结果'],
])('维护适配目标 %j 缺失时不把分组镜像和旧还原点冒充目标', async (next, text) => {
  stubApi({
    terminalModel: async () => ({ profile_id: 'h-1', profile_name: 'E1000', config_id: 'old-config', reduction_id: 'old-red', boot_verified_at: '2026-10-08T01:00:00Z' }),
    listGroups: async () => list([{ ...GROUPS[1], SystemConfigID: 'old-config', SystemReductionID: 'old-red' }]),
    listImages: async () => list([{ ID: 'win11', Name: '旧分组镜像', OSType: 'windows' }]),
    listConfigs: async () => list([{ ID: 'old-config', Name: '旧分组配置', DefaultReductionID: 'old-red' }]),
    listReductions: async () => list([{ ID: 'old-red', DisplayName: '旧分组点' }]),
    terminalIdentity: async () => ({ next_boot: next }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  const modal = (await screen.findByText('基础信息')).closest('.card');
  expect(await within(modal).findByText(new RegExp(text))).toBeTruthy();
  expect(within(modal).getByText('适配目标镜像')).toBeTruthy();
  expect(within(modal).queryByText('旧分组镜像')).toBeNull();
  expect(within(modal).queryByText('旧分组配置')).toBeNull();
  expect(within(modal).queryByText('旧分组点')).toBeNull();
  expect(within(modal).queryByText('本机已开机成功')).toBeNull();
});

it.each([
  [{ kind: 'winpe', reason: 'pending_adapt', mode: 'adapt', state: 'pending' }, '开机进入维护环境做完整适配'],
  [{ kind: 'wait', reason: 'finalizing', mode: 'adapt', state: 'finalizing' }, '等待服务器保存适配结果'],
])('详情保持打开时维护状态 %j 完成后自动更新启动说明和同配置新还原点名称', async (pending, text) => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    let completed = false;
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    stubApi({
      listGroups: async () => list([{ ...GROUPS[1], SystemConfigID: 'same-config', SystemReductionID: 'source-red' }]),
      listConfigs: async () => list([{ ID: 'same-config', Name: '教学配置', DefaultReductionID: completed ? 'new-red' : 'source-red' }]),
      listReductions: async () => list(completed ? [{ ID: 'new-red', DisplayName: '新可开机点', Status: 'ready' }] : [{ ID: 'source-red', DisplayName: '适配来源点', Status: 'ready' }]),
      terminalIdentity: async () => ({ next_boot: { image_id: 'win11', config_id: 'same-config',
        ...(completed ? { kind: 'system', reason: 'direct', reduction_id: 'new-red' } : { ...pending, reduction_id: 'source-red', task_id: 'adapt-task' }) } }),
    });
    renderPage();
    await user.click(await screen.findByText('美术教室机'));
    const modal = (await screen.findByText('基础信息')).closest('.card');
    expect(await within(modal).findByText(new RegExp(text))).toBeTruthy();
    expect(await within(modal).findByText('适配来源点')).toBeTruthy();
    completed = true;
    await act(async () => { vi.advanceTimersByTime(5000); });
    expect(await within(modal).findByText('直接启动 Windows。')).toBeTruthy();
    expect(await within(modal).findByText('新可开机点')).toBeTruthy();
    expect(within(modal).queryByText('适配来源点')).toBeNull();
    expect(within(modal).queryByText('适配目标镜像')).toBeNull();
    expect(within(modal).getByText('系统镜像')).toBeTruthy();
  } finally { vi.useRealTimers(); }
});

describe('终端页', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it('列出终端及其分组', async () => {
    renderPage();
    expect(await screen.findByText('一号机')).toBeTruthy();
    expect(screen.getByText('美术教室机')).toBeTruthy();
    expect(screen.getAllByText('教学一班').length).toBeGreaterThan(0);
  });

  // 只有在线、离线两种：旧数据里的 unknown 按离线算，也没有「未知」筛选。
  it('没有「未知」状态，旧数据按离线显示', async () => {
    stubApi({ listTerminals: async () => list([...TERMINALS,
      { ID: 'terminal-E', Name: '旧数据机', MAC: 'AABBCCDDEE05', IP: '192.168.50.105', GroupID: 'g-1', IsSuper: false, State: 'unknown' }]) });
    renderPage();
    expect(await screen.findByText('旧数据机')).toBeTruthy();
    expect(screen.queryByText('未知')).toBeNull();
    expect(screen.getByRole('button', { name: /离线\s*3/ })).toBeTruthy();
  });

  // 四个列表一起取、分开 settle：分组列表失败只丢分组名，不能丢客户机。
  it('分组接口挂了也照样显示终端', async () => {
    stubApi({ listGroups: async () => { throw new Error('500'); } });
    renderPage();
    expect(await screen.findByText('一号机')).toBeTruthy();
    expect(screen.getByText('二号机')).toBeTruthy();
  });

  // 客户机列表本身失败时要明说，不能显示成空列表。
  it('终端接口挂了要报错，而不是显示成没有终端', async () => {
    stubApi({ listTerminals: async () => { throw new Error('后端不可用'); } });
    renderPage();
    expect(await screen.findByText(/后端不可用|加载失败/)).toBeTruthy();
  });

  it('搜索按名称与 MAC 收窄', async () => {
    renderPage();
    await screen.findByText('一号机');

    const box = screen.getByPlaceholderText(/搜索/);
    await userEvent.type(box, '美术');
    await waitFor(() => expect(screen.queryByText('一号机')).toBeNull());
    expect(screen.getByText('美术教室机')).toBeTruthy();

    await userEvent.clear(box);
    await userEvent.type(box, 'AABBCCDDEE01');
    await waitFor(() => expect(screen.queryByText('美术教室机')).toBeNull());
    expect(screen.getByText('一号机')).toBeTruthy();
  });

  // 保存超管机即生成还原点，必须先填名称，不能等机器已被要求关机后再被后端拒绝。
  it('超管停机必须先填还原点名称', async () => {
    const stopSuper = vi.fn(async () => ({ task_id: 'task-super_stop-1' }));
    stubApi({ stopSuper });
    renderPage();
    await screen.findByText('二号机');

    await userEvent.click(screen.getByText('二号机'));
    await userEvent.click(await screen.findByRole('button', { name: /存还原点|超管停机/ }));

    await userEvent.click(await screen.findByRole('button', { name: /保存还原点|存盘停机/ }));
    expect(stopSuper).not.toHaveBeenCalled();

    await userEvent.type(screen.getByPlaceholderText('2026Q2-update'), '  装完office  ');
    await userEvent.click(screen.getByRole('button', { name: /保存还原点|存盘停机/ }));
    // 名称要去掉首尾空格，否则会存成另一个还原点名。
    await waitFor(() => expect(stopSuper).toHaveBeenCalledWith('terminal-B', { reduction_name: '装完office' }));
  });

  // 有数据盘的超管机：逐盘列出，只提交勾选且填了名称的盘。
  it('超管停机可逐块勾选数据盘存还原点', async () => {
    const stopSuper = vi.fn(async () => ({ task_id: 'task-super_stop-1' }));
    stubApi({
      stopSuper,
      listGroupDisks: async () => list([
        { ID: 'disk-d', GroupID: 'g-1', MountTarget: 'D:', ImageID: 'games', ConfigID: 'games_default' },
        { ID: 'disk-e', GroupID: 'g-1', MountTarget: 'E:', ImageID: 'docs', ConfigID: 'docs_default' },
      ]),
    });
    renderPage();
    await screen.findByText('二号机');
    await userEvent.click(screen.getByText('二号机'));
    await userEvent.click(await screen.findByRole('button', { name: /存还原点|超管停机/ }));

    await userEvent.type(await screen.findByPlaceholderText('2026Q2-update'), 'sys-v2');
    // 两块盘都列出；勾选 E: 并命名，D: 不勾。
    expect(await screen.findByLabelText('保存数据盘 D:')).toBeTruthy();
    await userEvent.click(screen.getByLabelText('保存数据盘 E:'));
    await userEvent.type(screen.getByLabelText('数据盘 E: 还原点名称'), ' 装完游戏 ');
    await userEvent.click(screen.getByRole('button', { name: /保存还原点|存盘停机/ }));
    await waitFor(() => expect(stopSuper).toHaveBeenCalledWith('terminal-B', {
      reduction_name: 'sys-v2',
      data_disks: [{ disk_id: 'disk-e', reduction_name: '装完游戏' }],
    }));
  });

  // 勾选数据盘却没填还原点名，提交前就拦下，与系统盘一致。
  it('勾选的数据盘必须填名称', async () => {
    const stopSuper = vi.fn(async () => ({ task_id: 'task-super_stop-1' }));
    stubApi({
      stopSuper,
      listGroupDisks: async () => list([{ ID: 'disk-d', GroupID: 'g-1', MountTarget: 'D:', ImageID: 'games', ConfigID: 'games_default' }]),
    });
    renderPage();
    await screen.findByText('二号机');
    await userEvent.click(screen.getByText('二号机'));
    await userEvent.click(await screen.findByRole('button', { name: /存还原点|超管停机/ }));
    await userEvent.type(await screen.findByPlaceholderText('2026Q2-update'), 'sys-v2');
    await userEvent.click(await screen.findByLabelText('保存数据盘 D:'));
    await userEvent.click(screen.getByRole('button', { name: /保存还原点|存盘停机/ }));
    expect(stopSuper).not.toHaveBeenCalled();
  });
});

// 机器在线时保存，后端要等满两分钟才失败。所以在线时超管相关操作直接禁用，并说明何时可做。
it('在线的超管机，超管操作直接禁用', async () => {
  const stopSuper = vi.fn(async () => ({ task_id: 'task-super_stop-1' }));
  stubApi({ stopSuper });
  renderPage();
  await screen.findByText('还开着的超管机');
  await userEvent.click(screen.getByText('还开着的超管机'));

  const save = await screen.findByRole('button', { name: /存还原点/ });
  expect(save.disabled).toBe(true);
  expect(screen.getByRole('button', { name: '取消超管' }).disabled).toBe(true);
  expect(screen.getByText(/关机后可/)).toBeTruthy();
  await userEvent.click(save);
  expect(stopSuper).not.toHaveBeenCalled();
});

it('在线的客户机，编辑表单里的终端类型开关也禁用', async () => {
  stubApi({});
  renderPage();
  const row = (await screen.findByText('一号机')).closest('tr');   // terminal-A: 在线、普通
  await userEvent.click(within(row).getByRole('button', { name: '编辑' }));
  expect((await screen.findByRole('button', { name: '超管机' })).disabled).toBe(true);
  expect(screen.getByRole('button', { name: '普通终端' }).disabled).toBe(true);
  expect(screen.getByText(/关机后/)).toBeTruthy();
});

// 「取消超管」会当场删掉超管盘，未保存的改动无法找回，必须先确认后果。
it('取消超管要先确认，说清楚会丢弃没保存的改动', async () => {
  const disableSuper = vi.fn(async () => ({ ID: 'terminal-B', IsSuper: false }));
  stubApi({ disableSuper });
  renderPage();
  await screen.findByText('二号机');

  await userEvent.click(screen.getByText('二号机'));
  await userEvent.click(await screen.findByRole('button', { name: '取消超管' }));
  let dialog = (await screen.findByText(/丢弃.*没保存|无法恢复/)).closest('.card');
  await userEvent.click(within(dialog).getByRole('button', { name: '取消' }));
  expect(disableSuper).not.toHaveBeenCalled();

  await userEvent.click(screen.getByRole('button', { name: '取消超管' }));
  dialog = (await screen.findByText(/丢弃.*没保存|无法恢复/)).closest('.card');
  await userEvent.click(within(dialog).getByRole('button', { name: /丢弃|确认/ }));
  await waitFor(() => expect(disableSuper).toHaveBeenCalledWith('terminal-B'));
});

// 编辑表单里把「终端类型」从超管改回普通，和「取消超管」是同一件事，同样会删盘。
it('编辑时把超管改回普通终端也要先确认', async () => {
  const updateTerminal = vi.fn(async () => ({ ID: 'terminal-B', IsSuper: false }));
  stubApi({ updateTerminal });
  renderPage();
  const row = (await screen.findByText('二号机')).closest('tr');
  await userEvent.click(within(row).getByRole('button', { name: '编辑' }));
  await userEvent.click(await screen.findByRole('button', { name: '普通终端' }));
  await userEvent.click(screen.getByRole('button', { name: '保存' }));

  const dialog = (await screen.findByText(/无法恢复/)).closest('.card');
  await userEvent.click(within(dialog).getByRole('button', { name: '取消' }));
  expect(updateTerminal).not.toHaveBeenCalled();

  await userEvent.click(screen.getByRole('button', { name: '保存' }));
  await userEvent.click(within((await screen.findByText(/无法恢复/)).closest('.card')).getByRole('button', { name: /丢弃|确认/ }));
  await waitFor(() => expect(updateTerminal).toHaveBeenCalledWith('terminal-B', expect.objectContaining({ is_super: false })));
});

it('换到网段不含原 IP 的分组时清空 IP，交给后端在新分组里分配', async () => {
  const updateTerminal = vi.fn(async () => ({ ID: 'terminal-C' }));
  stubApi({
    updateTerminal,
    listGroups: async () => list([
      { ID: 'g-1', Name: '教学一班', SystemImageID: 'win11', StartIP: '192.168.50.100', ClientMax: 20 },
      { ID: 'g-2', Name: '美术教室', SystemImageID: 'win11', StartIP: '192.168.60.100', ClientMax: 20 },
    ]),
  });
  renderPage();
  const row = (await screen.findByText('美术教室机')).closest('tr');
  await userEvent.click(within(row).getByRole('button', { name: '编辑' }));
  const ip = await screen.findByDisplayValue('192.168.60.101');
  const dialog = ip.closest('.card');
  await userEvent.selectOptions(within(dialog).getByDisplayValue('美术教室'), 'g-1');
  expect(ip.value).toBe('');
  expect(within(dialog).getByText(/留空将在新分组网段内自动分配/)).toBeTruthy();

  await userEvent.selectOptions(within(dialog).getByDisplayValue('教学一班'), 'g-2');
  expect(ip.value).toBe('192.168.60.101');

  await userEvent.selectOptions(within(dialog).getByDisplayValue('美术教室'), 'g-1');
  await userEvent.click(within(dialog).getByRole('button', { name: '保存' }));
  await waitFor(() => expect(updateTerminal).toHaveBeenCalledWith('terminal-C', expect.objectContaining({ ip: '', group_id: 'g-1' })));
});

it('表格默认每页 10 台，能切换条数', async () => {
  const many = Array.from({ length: 12 }, (_, i) => ({ ID: `t-${i}`, Name: `机器${String(i).padStart(2, '0')}`, MAC: `AABBCCDDEE${String(i).padStart(2, '0')}`, IP: `192.168.50.${110 + i}`, GroupID: 'g-1', IsSuper: false, State: 'offline' }));
  stubApi({ listTerminals: async () => list(many) });
  renderPage();
  expect(await screen.findByText('共 12 条')).toBeTruthy();
  expect(screen.getByText('机器09')).toBeTruthy();
  expect(screen.queryByText('机器10')).toBeNull();
  await userEvent.selectOptions(screen.getByLabelText('每页条数'), '20');
  expect(screen.getByText('机器11')).toBeTruthy();
});

// 不碰超管开关的编辑（改个名）不弹框。
it('编辑超管机但不改终端类型时不打扰', async () => {
  const updateTerminal = vi.fn(async () => ({ ID: 'terminal-B', IsSuper: true }));
  stubApi({ updateTerminal });
  renderPage();
  const row = (await screen.findByText('二号机')).closest('tr');
  await userEvent.click(within(row).getByRole('button', { name: '编辑' }));
  await userEvent.click(screen.getByRole('button', { name: '保存' }));
  await waitFor(() => expect(updateTerminal).toHaveBeenCalledWith('terminal-B', expect.objectContaining({ is_super: true })));
  expect(screen.queryByText(/无法恢复/)).toBeNull();
});


// 超管机数据盘可在线「保存并发布」，要列出未发布的改动量，一眼看出有没有东西要发布。
it('超管机的数据盘能在线发布', async () => {
  const publishDataDisk = vi.fn(async () => ({ task_id: 'task-publish-1' }));
  stubApi({
    publishDataDisk,
    superDisks: async () => list([
      { disk_id: 'gd-1', lun: 1, mount_target: 'D:', config_name: '游戏盘', current_name: '2026-09-14', written: 13100000000, ready: true },
    ]),
  });
  renderPage();
  await screen.findByText('还开着的超管机');
  await userEvent.click(screen.getByText('还开着的超管机'));

  expect(await screen.findByText('2026-09-14')).toBeTruthy();   // 当前发布点
  expect(screen.getByText(/1[23]\.\d+ GiB/)).toBeTruthy(); // 未发布改动
  await userEvent.click(screen.getByRole('button', { name: /保存并发布/ }));

  await userEvent.type(await screen.findByPlaceholderText(/永劫无间|2026/), '装了永劫无间');
  await userEvent.click(screen.getByRole('button', { name: /^发布$|确认发布/ }));
  await waitFor(() => expect(publishDataDisk).toHaveBeenCalledWith('terminal-D', { disk_id: 'gd-1', name: '装了永劫无间' }));
});

it('没有改动、或者还没开过机，就不让发布', async () => {
  stubApi({
    superDisks: async () => list([
      { disk_id: 'gd-1', lun: 1, mount_target: 'D:', config_name: '游戏盘', current_name: '2026-09-14', written: 0, ready: true },
      { disk_id: 'gd-2', lun: 2, mount_target: 'E:', config_name: '资料盘', current_name: '初始', written: 0, ready: false },
    ]),
  });
  renderPage();
  await screen.findByText('还开着的超管机');
  await userEvent.click(screen.getByText('还开着的超管机'));

  const rows = await screen.findAllByRole('row');
  const d = rows.find(r => within(r).queryByText('D:'));
  const e = rows.find(r => within(r).queryByText('E:'));
  expect(within(d).getByRole('button', { name: /保存并发布/ }).disabled).toBe(true);
  expect(within(d).getByText(/无改动/)).toBeTruthy();
  expect(within(e).getByRole('button', { name: /保存并发布/ }).disabled).toBe(true);
  expect(within(e).getByText(/开机后/)).toBeTruthy();
});

// 弹窗里要写明：存还原点不会结束超管。
it('存还原点的弹窗要说清楚保存后还是超管机', async () => {
  stubApi({});
  renderPage();
  await screen.findByText('二号机');
  await userEvent.click(screen.getByText('二号机'));
  await userEvent.click(await screen.findByRole('button', { name: /存还原点/ }));
  expect(await screen.findByText(/仍为超管机/)).toBeTruthy();
});

// 超管机的用途说明收在问号后面；常用约束（同一配置只能一台、关机后才能切换）一直显示。
it('终端类型：约束一直显示，超管机的说明收在问号后面', async () => {
  stubApi({});
  renderPage();
  const row = (await screen.findByText('美术教室机')).closest('tr');  // terminal-C：离线普通终端
  await userEvent.click(within(row).getByRole('button', { name: '编辑' }));

  expect(await screen.findByText(/同一配置同时只能有一台超管机/)).toBeTruthy();
  expect(screen.queryByText(/超管机用于修改镜像内容/)).toBeNull();

  await userEvent.click(screen.getByRole('button', { name: '终端类型说明' }));
  expect(await screen.findByText(/超管机用于修改镜像内容/)).toBeTruthy();
});

// 勾选只对眼前这一页有效：筛选、换视图后清空，批量删除不会删到看不见的行。
it('筛选或切换视图后清空勾选', async () => {
  stubApi();
  renderPage();
  const row = (await screen.findByText('一号机')).closest('tr');
  await userEvent.click(row.querySelector('input[type="checkbox"]'));
  expect(screen.getByText('已选 1')).toBeTruthy();

  await userEvent.type(screen.getByPlaceholderText(/搜索/), '美术');
  await waitFor(() => expect(screen.queryByText(/已选/)).toBeNull());

  await userEvent.clear(screen.getByPlaceholderText(/搜索/));
  const row2 = (await screen.findByText('一号机')).closest('tr');
  await userEvent.click(row2.querySelector('input[type="checkbox"]'));
  expect(screen.getByText('已选 1')).toBeTruthy();
  await userEvent.click(screen.getByRole('button', { name: '矩阵' }));
  await waitFor(() => expect(screen.queryByText(/已选/)).toBeNull());
});

// ISO 安装会临时借用客户机：开机先走安装，客户机页要看得出被谁占着、哪些操作要等安装结束。
const INSTALLING = { id: 'install-1', task_id: 'task-install-1', name: 'win11-新装', mac: 'AABBCCDDEE03', ip: '192.168.60.101', phase: 'waiting', progress: 0, message: '等待客户机开机', os_type: 'windows' };

it('被 ISO 安装占用的客户机，详情说明占用、下次开机进安装程序，冲突操作禁用', async () => {
  const getInstallation = vi.fn(async () => ({ ...INSTALLING, source_path: '/srv/iso/win11.iso' }));
  stubApi({
    listInstallations: async () => list([INSTALLING, { ...INSTALLING, id: 'install-0', name: '早先装好的', phase: 'ready' }]),
    terminalIdentity: async () => ({ next_boot: { kind: 'install', reason: 'waiting', task_id: 'task-install-1', install_id: 'install-1' } }),
    getInstallation,
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  const modal = (await screen.findByText('基础信息')).closest('.card');
  expect(await within(modal).findByText(/正在用于安装镜像「win11-新装」/)).toBeTruthy();
  expect(within(modal).queryByText(/早先装好的/)).toBeNull();
  expect(await within(modal).findByText(/进入 ISO 安装程序/)).toBeTruthy();
  expect(within(modal).getByText(/安装期间开机不使用分组的系统盘/)).toBeTruthy();
  expect(within(modal).getByRole('button', { name: '设为超管' }).disabled).toBe(true);
  expect(within(modal).getByRole('button', { name: '删除' }).disabled).toBe(true);
  expect(within(modal).getByText(/安装结束或放弃后/)).toBeTruthy();

  await userEvent.click(within(modal).getByRole('button', { name: '查看安装' }));
  expect(getInstallation).toHaveBeenCalledWith('install-1');
  expect(await screen.findByText('ISO 安装')).toBeTruthy();
});

it.each([
  ['preparing', /服务器正在准备安装资源/],
  ['installing', /安装进行中，请勿重启/],
  ['initializing', /从新装好的系统盘启动/],
  ['saving', /等待安装保存完成/],
  ['verifying', /启动新镜像做开机验证/],
  ['failed', /安装失败，开机不会启动任何系统/],
])('安装阶段 %s 的预计下次启动照实说', async (phase, text) => {
  stubApi({
    listInstallations: async () => list([{ ...INSTALLING, phase }]),
    terminalIdentity: async () => ({ next_boot: { kind: 'install', reason: phase, task_id: 'task-install-1', install_id: 'install-1' } }),
  });
  renderPage();
  await userEvent.click(await screen.findByText('美术教室机'));
  expect(await screen.findByText(text)).toBeTruthy();
});

it('列表和矩阵把被安装占用的客户机标成安装中，编辑和删除要等安装结束', async () => {
  stubApi({ listInstallations: async () => list([INSTALLING, { ...INSTALLING, id: 'install-2', mac: 'AABBCCDDEE01', phase: 'cancelled' }]) });
  renderPage();
  const row = (await screen.findByText('美术教室机')).closest('tr');
  expect(await within(row).findByText('安装中')).toBeTruthy();
  expect(within(row).queryByText('离线')).toBeNull();
  expect(within(row).getByRole('button', { name: '编辑' }).disabled).toBe(true);
  expect(within(row).getByRole('button', { name: /删除终端/ }).disabled).toBe(true);
  expect(within(row).getByRole('button', { name: '编辑' }).title).toMatch(/win11-新装/);

  const other = screen.getByText('一号机').closest('tr');
  expect(within(other).getByText('在线')).toBeTruthy();
  expect(within(other).getByRole('button', { name: '编辑' }).disabled).toBe(false);

  await userEvent.click(screen.getByRole('button', { name: '矩阵' }));
  const cell = screen.getByText('美术教室机').closest('.tmcell');
  expect(within(cell).getByText('安装中')).toBeTruthy();
});

it('安装列表读不到时客户机页照常可用', async () => {
  stubApi({ listInstallations: async () => { throw new Error('boom'); } });
  renderPage();
  const row = (await screen.findByText('美术教室机')).closest('tr');
  expect(within(row).getByText('离线')).toBeTruthy();
  expect(within(row).getByRole('button', { name: '编辑' }).disabled).toBe(false);
});
