import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageModels } from './models.jsx';

// 驱动组能装什么由服务端定，但页面只能列出可选的包：列出停用或非启动关键的包，
// 操作者在表单里拿到的拒绝无从处理。

const list = (items) => ({ items, total: items.length });
const packItem = (over) => ({
  pack: {
    ID: 'p-1', Name: 'Intel I219', Category: 'boot_critical_nic', OSType: 'windows',
    Arch: 'x64', Version: '12.19.2.60', Vendor: 'Intel', Status: 'enabled',
    HWIDs: ['PCI\\VEN_8086&DEV_15B7'], CreatedAt: '2026-07-30T10:00:00Z', ...over,
  },
  risks: [],
});
const PACKS = [
  packItem({}),
  packItem({ ID: 'p-2', Name: 'Realtek RTL8168', Vendor: 'Realtek' }),
  packItem({ ID: 'p-3', Name: '已禁用网卡', Status: 'disabled' }),
  packItem({ ID: 'p-4', Name: '显卡驱动', Category: 'gpu' }),
];

function stubApi(overrides = {}) {
  const base = {
    listDriverPacks: async () => list(PACKS),
    listModels: async () => [],
    listTerminals: async () => list([]),
    listImages: async () => list([]),
    listGroups: async () => list([]),
    listPools: async () => list([]),
    listTasks: async () => list([]),
    listAlarms: async () => list([]),
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageModels/></ConfirmProvider></StoreProvider>,
);

describe('机型页驱动包', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it('列出驱动包并按名称/厂商搜索', async () => {
    renderPage();
    await userEvent.click(screen.getByRole('button', { name: /^驱动包/ }));
    await screen.findByText('Intel I219');

    await userEvent.type(screen.getByPlaceholderText(/搜索名称/), 'realtek');
    await waitFor(() => expect(screen.queryByText('Intel I219')).toBeNull());
    expect(screen.getByText('Realtek RTL8168')).toBeTruthy();
  });

  it('没选文件时不提交上传', async () => {
    const uploadDriverPack = vi.fn();
    stubApi({ uploadDriverPack });
    renderPage();
    await userEvent.click(screen.getByRole('button', { name: /^驱动包/ }));
    await screen.findByText('Intel I219');

    await userEvent.click(screen.getByRole('button', { name: /上传驱动包/ }));
    await userEvent.click(await screen.findByRole('button', { name: '上传并解析' }));
    expect(uploadDriverPack).not.toHaveBeenCalled();
  });

  it('上传把表单字段一并带上', async () => {
    const uploadDriverPack = vi.fn(async () => ({}));
    stubApi({ uploadDriverPack });
    renderPage();
    await userEvent.click(screen.getByRole('button', { name: /^驱动包/ }));
    await screen.findByText('Intel I219');

    await userEvent.click(screen.getByRole('button', { name: /上传驱动包/ }));
    await userEvent.type(await screen.findByPlaceholderText('Intel I219 v12.19'), '螃蟹网卡');
    const file = new File([new Uint8Array([0x50, 0x4b, 3, 4])], 'rtl.zip', { type: 'application/zip' });
    await userEvent.upload(document.querySelector('input[type=file]'), file);
    await userEvent.click(screen.getByRole('button', { name: '上传并解析' }));

    await waitFor(() => expect(uploadDriverPack).toHaveBeenCalled());
    const form = uploadDriverPack.mock.calls[0][0];
    expect(form.get('name')).toBe('螃蟹网卡');
    expect(form.get('category')).toBe('boot_critical_nic');
    expect(form.get('file').name).toBe('rtl.zip');
  });

});
