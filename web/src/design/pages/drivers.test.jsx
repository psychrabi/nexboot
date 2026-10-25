import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageDrivers } from './drivers.jsx';

// The server validates driver bundles; the UI must only offer eligible driver packs.
// Showing disabled or non-boot-critical packs would lead to avoidable rejections.

const list = (items) => ({ items, total: items.length });
const packItem = (over) => ({
  pack: {
    ID: 'p-1', Name: 'Intel I219', Category: 'boot_critical_nic', OSType: 'windows',
    Arch: 'x64', Version: '12.19.2.60', Vendor: 'Intel', Status: 'enabled',
    Recommended: false, HWIDs: ['PCI\\VEN_8086&DEV_15B7'], CreatedAt: '2026-07-30T10:00:00Z', ...over,
  },
  risks: [],
});
const PACKS = [
  packItem({}),
  packItem({ ID: 'p-2', Name: 'Realtek RTL8168', Vendor: 'Realtek' }),
  packItem({ ID: 'p-3', Name: 'Disabled NIC', Status: 'disabled' }),
  packItem({ ID: 'p-4', Name: 'Graphics Driver', Category: 'gpu' }),
];

function stubApi(overrides = {}) {
  const base = {
    listDriverPacks: async () => list(PACKS),
    listDriverBundles: async () => list([]),
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
  <StoreProvider><ConfirmProvider><PageDrivers/></ConfirmProvider></StoreProvider>,
);

describe('Driver Center', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it('Lists driver packs and searches by name/vendor', async () => {
    renderPage();
    await screen.findByText('Intel I219');

    await userEvent.type(screen.getByPlaceholderText(/Search name/), 'realtek');
    await waitFor(() => expect(screen.queryByText('Intel I219')).toBeNull());
    expect(screen.getByText('Realtek RTL8168')).toBeTruthy();
  });

  it('Does not upload without a file', async () => {
    const uploadDriverPack = vi.fn();
    stubApi({ uploadDriverPack });
    renderPage();
    await screen.findByText('Intel I219');

    await userEvent.click(screen.getByRole('button', { name: /Upload Driver Pack/ }));
    await userEvent.click(await screen.findByRole('button', { name: 'Upload and Parse' }));
    expect(uploadDriverPack).not.toHaveBeenCalled();
  });

  it('Includes form fields in uploads', async () => {
    const uploadDriverPack = vi.fn(async () => ({}));
    stubApi({ uploadDriverPack });
    renderPage();
    await screen.findByText('Intel I219');

    await userEvent.click(screen.getByRole('button', { name: /Upload Driver Pack/ }));
    await userEvent.type(await screen.findByPlaceholderText('Intel I219 v12.19'), 'Realtek NIC');
    const file = new File([new Uint8Array([0x50, 0x4b, 3, 4])], 'rtl.zip', { type: 'application/zip' });
    await userEvent.upload(document.querySelector('input[type=file]'), file);
    await userEvent.click(screen.getByRole('button', { name: 'Upload and Parse' }));

    await waitFor(() => expect(uploadDriverPack).toHaveBeenCalled());
    const form = uploadDriverPack.mock.calls[0][0];
    expect(form.get('name')).toBe('Realtek NIC');
    expect(form.get('category')).toBe('boot_critical_nic');
    expect(form.get('file').name).toBe('rtl.zip');
  });

  // Bundles only accept enabled boot-critical NIC drivers; filter out all other packs.
  it('Only offers enabled boot NIC packs when creating a bundle', async () => {
    renderPage();
    await screen.findByText('Intel I219');
    // Driver bundles are managed in a separate tab.
    await userEvent.click(screen.getByText(/Boot Driver Bundles/));
    await userEvent.click(await screen.findByRole('button', { name: /Create Bundle/ }));

    await screen.findByPlaceholderText('2026 Common NIC Boot Drivers');
    // All driver packs offered in the dialog.
    const options = document.body.textContent;
    expect(options).toContain('Intel I219');
    expect(options).toContain('Realtek RTL8168');
    expect(options).not.toContain('Disabled NIC');
    expect(options).not.toContain('Graphics Driver');
  });

  it('Requires a name and at least one pack for a bundle', async () => {
    const createDriverBundle = vi.fn(async () => ({}));
    stubApi({ createDriverBundle });
    renderPage();
    await screen.findByText('Intel I219');
    // Driver bundles are managed in a separate tab.
    await userEvent.click(screen.getByText(/Boot Driver Bundles/));
    await userEvent.click(await screen.findByRole('button', { name: /Create Bundle/ }));

    const submit = await screen.findByRole('button', { name: 'Create' });
    await userEvent.click(submit);
    expect(createDriverBundle).not.toHaveBeenCalled();

    await userEvent.type(screen.getByPlaceholderText('2026 Common NIC Boot Drivers'), 'Classroom NIC Bundle');
    await userEvent.click(submit);
    expect(createDriverBundle).not.toHaveBeenCalled(); // Name entered but no driver pack selected

    await userEvent.click(screen.getByText('Intel I219'));
    await userEvent.click(submit);
    await waitFor(() => expect(createDriverBundle).toHaveBeenCalledWith({ name: 'Classroom NIC Bundle', pack_ids: ['p-1'] }));
  });
});
