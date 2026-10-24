import React from 'react';
import {render,screen,waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {afterEach,expect,it,vi} from 'vitest';
import {api} from '../lib/api.js';
import {StoreProvider} from './store.jsx';
import {ConfirmProvider} from './overlay.jsx';
import {InstallationDetails} from './installation.jsx';

const session = {id:'install-1',task_id:'task-install-1',name:'Win11 Lab',mac:'001122334455',ip:'192.168.50.10',phase:'failed',error:'准备失败'};
function setup(overrides={}, props={}) {
  const get = vi.spyOn(api,'getInstallation').mockResolvedValue({...session,...overrides});
  const onClose = vi.fn();
  render(<StoreProvider><ConfirmProvider><InstallationDetails id={session.id} onClose={onClose} {...props}/></ConfirmProvider></StoreProvider>);
  return {get,onClose};
}
afterEach(()=>vi.restoreAllMocks());

it('放弃时磁盘仍占用：停在清理中并说明要关客户机，清理完后可在原地重试', async()=>{
  const cancel = vi.spyOn(api,'cancelInstallation').mockResolvedValue({...session,phase:'cancelling'});
  const {get,onClose} = setup();
  await userEvent.click(await screen.findByRole('button',{name:'放弃安装'}));
  expect(screen.getByText(/原 ISO 文件保留/)).toBeTruthy();
  expect(screen.getAllByRole('dialog')).toHaveLength(1);
  await userEvent.click(screen.getByRole('button',{name:'确认放弃'}));
  expect(cancel).toHaveBeenCalledWith('install-1');
  expect(await screen.findByText(/仍开机的话请先关闭/)).toBeTruthy();
  expect(onClose).not.toHaveBeenCalled();
  get.mockResolvedValue({...session,phase:'cancelled'});
  expect(await screen.findByRole('button',{name:'重试'}, {timeout:4000})).toBeTruthy();
});

it('清理失败保留具体错误和确认入口，不创建新安装', async()=>{
  vi.spyOn(api,'cancelInstallation').mockRejectedValue(Error('请先关闭客户机 001122334455'));
  const create = vi.spyOn(api,'createInstallation');
  const {onClose} = setup();
  await userEvent.click(await screen.findByRole('button',{name:'放弃安装'}));
  await userEvent.click(screen.getByRole('button',{name:'确认放弃'}));
  expect(await screen.findByText('请先关闭客户机 001122334455')).toBeTruthy();
  expect(screen.getByRole('button',{name:'确认放弃'}).disabled).toBe(false);
  expect(create).not.toHaveBeenCalled();
  expect(onClose).not.toHaveBeenCalled();
});

it('修改设置交给调用方打开向导，带上这次安装', async()=>{
  const onEditSettings = vi.fn();
  const {onClose} = setup({}, {intent:'retry', onEditSettings});
  await userEvent.click(await screen.findByRole('button',{name:'修改设置'}));
  expect(onClose).toHaveBeenCalled();
  expect(onEditSettings).toHaveBeenCalledWith(expect.objectContaining({id:'install-1',phase:'failed'}));
});

it('保存失败只重试保存，不重新格式化安装盘', async()=>{
  const retry = vi.spyOn(api,'retryInstallation').mockResolvedValue({...session,phase:'verifying',error:''});
  const cancel = vi.spyOn(api,'cancelInstallation');
  setup({phase:'saving'});
  await userEvent.click(await screen.findByRole('button',{name:'重试保存'}));
  await waitFor(()=>expect(retry).toHaveBeenCalledWith('install-1'));
  expect(cancel).not.toHaveBeenCalled();
  expect(screen.queryByRole('button',{name:'重试'})).toBeNull();
});

it('系统名已含在版本里时不重复', async()=>{
  setup({os_type:'windows', version:'Windows 11'});
  expect(await screen.findByText(/^Windows 11 · 客户机/)).toBeTruthy();
  expect(screen.queryByText(/Windows Windows/)).toBeNull();
});

it('重新安装时密码不够 8 位：输入框下直接说差几位，开始安装不可点', async()=>{
  setup({}, {intent:'retry'});
  const input = await screen.findByLabelText('初始密码');
  expect(screen.getByText(/8 至 127 位/)).toBeTruthy();
  await userEvent.type(input, '123456');
  expect(screen.getByText('至少 8 位，现在 6 位')).toBeTruthy();
  expect(screen.getByRole('button',{name:'开始安装'}).disabled).toBe(true);
  await userEvent.type(input, '78');
  expect(screen.queryByText(/至少 8 位/)).toBeNull();
  expect(screen.getByRole('button',{name:'开始安装'}).disabled).toBe(false);
});

it('Ubuntu 安装只列 Ubuntu 的日志，默认打开启动准备', async()=>{
  const log = vi.spyOn(api,'taskLog').mockResolvedValue(new Response('prepared'));
  setup({os_type:'linux'});
  await userEvent.click(await screen.findByText('安装日志'));
  expect(await screen.findByText('prepared')).toBeTruthy();
  expect(log).toHaveBeenCalledWith('task-install-1','install.log');
  expect(screen.queryByText('PE 控制台')).toBeNull();
  expect(screen.queryByText('DISM 日志')).toBeNull();
  expect(screen.getByText('Ubuntu 安装日志')).toBeTruthy();
});

it('Windows 安装只列 PE 与 DISM 日志', async()=>{
  vi.spyOn(api,'taskLog').mockResolvedValue(new Response('pe'));
  setup({os_type:'windows'});
  await userEvent.click(await screen.findByText('安装日志'));
  expect(await screen.findByText('pe')).toBeTruthy();
  expect(screen.getByText('DISM 日志')).toBeTruthy();
  expect(screen.queryByText(/Ubuntu/)).toBeNull();
});

it('CentOS 安装详情显示真实系统名和 Anaconda 日志', async()=>{
  const log = vi.spyOn(api,'taskLog').mockImplementation(async (_id, name)=>new Response(`log ${name}`));
  setup({os_type:'linux',distribution:'anaconda-centos',version:'7'});
  expect(await screen.findByText(/^CentOS 7 · 客户机/)).toBeTruthy();
  await userEvent.click(screen.getByText('安装日志'));
  await screen.findByText('log install.log');
  await userEvent.click(screen.getByText('Anaconda 安装日志'));
  await waitFor(()=>expect(log).toHaveBeenLastCalledWith('task-install-1','anaconda.log'));
  for (const [label, name] of [['磁盘识别日志','storage.log'], ['命令日志','program.log'], ['iSCSI 诊断','iscsi.log']]) {
    await userEvent.click(screen.getByRole('button',{name:label}));
    await waitFor(()=>expect(log).toHaveBeenLastCalledWith('task-install-1',name));
    await screen.findByText(`log ${name}`);
  }
  expect(screen.queryByText(/Ubuntu/)).toBeNull();
});
