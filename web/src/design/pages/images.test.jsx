import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider, useStore } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageImages } from './images.jsx';

// Image tests cover Image → Configuration → Restore Point cascades and named dependency warnings before deletion.

const list = (items) => ({ items, total: items.length });
const IMAGES = [
  { ID: 'win11', Name: 'Win 11', OSType: 'windows', State: 'normal', Size: 32 * 1024 ** 3, Used: 8.5 * 1024 ** 3, Purpose: 'system', Origin: 'imported', CreatedAt: '2026-07-30T10:00:00Z' },
  { ID: 'ubuntu', Name: 'Ubuntu', OSType: 'linux', State: 'normal', Size: 8 * 1024 ** 3, Used: null, Purpose: 'system', Origin: 'imported', CreatedAt: '2026-07-30T10:00:00Z' },
  { ID: 'games', Name: 'Games D', OSType: 'windows', State: 'normal', Size: 200 * 1024 ** 3, Used: 0, Purpose: 'data', Origin: 'blank', CreatedAt: '2026-08-18T10:00:00Z' },
];
const CONFIGS = {
  win11: [{ ID: 'win11_default', ImageID: 'win11', Name: 'default', DefaultReductionID: 'win11_default_0', CreatedAt: '2026-07-30T10:00:00Z' }],
  ubuntu: [{ ID: 'ubuntu_default', ImageID: 'ubuntu', Name: 'default', DefaultReductionID: null, CreatedAt: '2026-07-30T10:00:00Z' }],
};
const REDUCTIONS = {
  win11_default: [
    { ID: 'win11_default_0', ConfigID: 'win11_default', Name: '@0', DisplayName: '0', Status: 'ready', CreatedAt: '2026-07-30T10:00:00Z' },
    { ID: 'win11_default_office', ConfigID: 'win11_default', Name: '@office', DisplayName: 'Office installed', Status: 'ready', CreatedAt: '2026-07-30T11:00:00Z' },
  ],
  ubuntu_default: [],
};

function stubApi(overrides = {}) {
  const base = {
    listImages: async () => list(IMAGES),
    listConfigs: async (imageId) => list(CONFIGS[imageId] || []),
    listReductions: async (configId) => list(REDUCTIONS[configId] || []),
    listGroups: async () => list([]),
    listImportSources: async () => ({ items: [], dir: '/tank/imports' }),
    listTasks: async () => list([]),
    listAlarms: async () => list([]),
    listPools: async () => list([]),
    listTerminals: async () => list([]),
    getImageHealth: async () => ({ Level: 'ok', Items: [] }),
    listClusterNodes: async () => list([]),
    beginUpload: async ({ file_name, size_bytes }) =>
      ({ upload_id: 'nodeA-1', file_name, size_bytes, received: 0, node_id: 'nodeA' }),
    uploadChunk: async () => ({ upload_id: 'nodeA-1', received: 0, complete: false }),
    uploadStatus: async () => ({ upload_id: 'nodeA-1', received: 0 }),
    abortUpload: async () => null,
  };
  for (const [name, fn] of Object.entries({ ...base, ...overrides })) {
    vi.spyOn(api, name).mockImplementation(fn);
  }
}

const renderPage = () => render(
  <StoreProvider><ConfirmProvider><PageImages/></ConfirmProvider></StoreProvider>,
);

// Open image details from a card; configuration management is accessed from there.
async function openDetail(name = 'Win 11') {
  await userEvent.click(await screen.findByText(name));
  return screen.findByText('Configuration Overview');
}

describe('Images page', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  // Browser downloads use one-time tickets; server-directory exports remain available.
  it('Exports through one-time browser download tickets', async () => {
    const exportImageTicket = vi.fn(async () => ({ token: 'tok1', url: '/api/images/export-download?token=tok1', expires_in: 60, file_name: 'Win 11.zfs' }));
    stubApi({ exportImageTicket });
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    renderPage();
    await openDetail();

    await userEvent.click(screen.getByRole('button', { name: /Export Image/ }));
    expect(screen.getByText(/Exports the initially imported image/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: /Download Locally/ }));
    await waitFor(() => expect(exportImageTicket).toHaveBeenCalledWith('win11', false));
    expect(click).toHaveBeenCalled();

    // Both download and server export paths remain supported.
    await userEvent.click(screen.getByRole('button', { name: /Export Image/ }));
    expect(screen.getByRole('button', { name: /Export to Server Directory/ })).toBeTruthy();
  });

  // gzip selection must reach the export-ticket API; extension and stream compression must match.
  it('Includes gzip flag in export ticket requests', async () => {
    const exportImageTicket = vi.fn(async () => ({ token: 'tok1', url: '/api/images/export-download?token=tok1', expires_in: 60, file_name: 'Win 11.zfs.gz' }));
    stubApi({ exportImageTicket });
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    renderPage();
    await openDetail();

    await userEvent.click(screen.getByRole('button', { name: /Export Image/ }));
    await userEvent.click(screen.getByLabelText('gzip Compression'));
    await userEvent.click(screen.getByRole('button', { name: /Download Locally/ }));
    await waitFor(() => expect(exportImageTicket).toHaveBeenCalledWith('win11', true));
  });

  // Image cards show logical and actual allocated size; missing values show unknown, not zero.
  it('Shows logical and allocated image sizes', async () => {
    renderPage();
    expect(await screen.findByText('Logical 32.0 GiB · Allocated 8.5 GiB')).toBeTruthy();
    expect(screen.getByText('Logical 8.0 GiB · Allocated —')).toBeTruthy();
  });

  // Import dialog checks free pool space before upload starts.
  it('Rejects image files that exceed available pool capacity', async () => {
    const beginUpload = vi.fn();
    stubApi({
      beginUpload,
      listImportSources: async () => ({ import_dir: '/var/lib/ndiskless/imports', pool_available: 20 * 1024 ** 3, items: [] }),
    });
    renderPage();
    await screen.findByText('Win 11');
    await userEvent.click(screen.getByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');
    expect(dlg.textContent).toMatch(/Available pool space: 20\.0 GiB/);

    const huge = new File(['x'], 'huge.raw');
    Object.defineProperty(huge, 'size', { value: 300 * 1024 ** 3 });
    await userEvent.upload(dlg.querySelector('input[type="file"]'), huge);
    await waitFor(() => expect(dlg.textContent).toMatch(/Insufficient pool space/));
    expect(beginUpload).not.toHaveBeenCalled();
  });

  // Filter images by purpose and OS rather than separate system/data tabs.
  it('Filters images by purpose and operating system', async () => {
    renderPage();
    await screen.findByText('Games D');
    await userEvent.selectOptions(screen.getByLabelText('Filter by Purpose'), 'data');
    await waitFor(() => expect(screen.queryByText('Win 11')).toBeNull());
    expect(screen.getByText('Games D')).toBeTruthy();
    await userEvent.selectOptions(screen.getByLabelText('Filter by Purpose'), 'all');
    await userEvent.selectOptions(screen.getByLabelText('Filter by OS'), 'linux');
    await waitFor(() => expect(screen.queryByText('Win 11')).toBeNull());
    expect(screen.getByText('Ubuntu')).toBeTruthy();
  });

  // Image details summarize configuration state; edits happen in configuration and restore-point tabs.
  it('Summarizes configurations in a read-only detail dialog', async () => {
    renderPage();
    await userEvent.click(await screen.findByText('Win 11'));
    const dialog = (await screen.findByText('Configuration Overview')).closest('.card');
    expect(await within(dialog).findByText('default')).toBeTruthy();
    expect(within(dialog).getByText(/Current restore point 0/)).toBeTruthy();
    expect(within(dialog).queryByRole('button', { name: /Create Configuration|Fork|Merge/ })).toBeNull();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Manage Restore Points default' }));
    expect((await screen.findByLabelText('Filter by Configuration')).value).toBe('win11_default');
    expect(screen.getByLabelText('Filter by Image').value).toBe('win11');
  });

  // Create blank data disk images with name, size and filesystem.
  it('Creates blank data disk images', async () => {
    const createBlankImage = vi.fn(async () => ({ task_id: 'task-create_blank_image-1' }));
    stubApi({ createBlankImage });
    renderPage();
    await screen.findByText('Win 11');
    await userEvent.click(screen.getByRole('button', { name: /Create Data Disk/ }));
    await userEvent.type(await screen.findByLabelText('Data Disk Name'), 'Games Disk');
    await userEvent.clear(screen.getByLabelText('Size'));
    await userEvent.type(screen.getByLabelText('Size'), '200');
    await userEvent.selectOptions(screen.getByLabelText('File System'), 'ntfs');
    // Volume label is optional; omit when blank and submit the entered value otherwise.
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(createBlankImage).toHaveBeenCalledWith({ name: 'Games Disk', size_bytes: 200 * 1024 ** 3, filesystem: 'ntfs' }));

    await userEvent.click(screen.getByRole('button', { name: /Create Data Disk/ }));
    await userEvent.type(await screen.findByLabelText('Data Disk Name'), 'games');
    await userEvent.type(screen.getByLabelText('Volume Label'), ' GAMES ');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(createBlankImage).toHaveBeenLastCalledWith({ name: 'games', size_bytes: 100 * 1024 ** 3, filesystem: 'ntfs', label: 'GAMES' }));
  });

  // Imported images may be designated for data disks.
  it('Supports purpose selection on image import', async () => {
    const importImage = vi.fn(async () => ({ task_id: 'task-import_image-1' }));
    stubApi({
      importImage,
      uploadChunk: async (id, offset, blob) => ({
        upload_id: id, received: offset + blob.size, complete: true,
        path: '/var/lib/ndiskless/imports/d.vmdk',
      }),
    });
    renderPage();
    await screen.findByText('Win 11');
    await userEvent.click(screen.getByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');
    await userEvent.upload(dlg.querySelector('input[type="file"]'), new File(['x'], 'd.vmdk'));
    await waitFor(() => expect(dlg.textContent).toMatch(/Upload finished/));
    await userEvent.selectOptions(screen.getByLabelText('Purpose'), 'data');
    await userEvent.click(screen.getByRole('button', { name: 'Import' }));
    await waitFor(() => expect(importImage).toHaveBeenCalledWith(expect.objectContaining({
      source_path: '/var/lib/ndiskless/imports/d.vmdk', purpose: 'data', delete_source_after: true,
    })));
  });

});

const openTab = async (name) => {
  await screen.findByText('Win 11');
  await userEvent.click(screen.getByRole('button', { name: new RegExp('^' + name) }));
};

describe('Configurations tab', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it('Lists image configurations and navigates to restore points', async () => {
    renderPage();
    await openTab('Configuration');
    const table = await screen.findByRole('table');
    await waitFor(() => expect(within(table).getAllByRole('row').some(r => r.textContent.includes('Win 11'))).toBe(true));
    const row = within(table).getAllByRole('row').find(r => r.textContent.includes('Win 11'));
    expect(within(row).getByText('Win 11')).toBeTruthy();
    expect(within(row).getByText('0')).toBeTruthy();
    expect(within(row).getByText('Not Latest')).toBeTruthy();
    await userEvent.click(within(row).getByRole('button', { name: /2/ }));
    expect(await screen.findByText('Office installed')).toBeTruthy();
  });

  it("Blocks deletion of an image's only configuration", async () => {
    stubApi({ listConfigs: async (imageId) => list(imageId === 'win11'
      ? [...CONFIGS.win11, { ID: 'win11_work', ImageID: 'win11', Name: 'work', DefaultReductionID: null, CreatedAt: '2026-07-31T10:00:00Z' }]
      : CONFIGS[imageId] || []) });
    renderPage();
    await openTab('Configuration');
    const table = await screen.findByRole('table');
    await waitFor(() => expect(within(table).getAllByRole('row').some(r => r.textContent.includes('Ubuntu'))).toBe(true));
    const buttons = within(table).getAllByRole('button', { name: /Delete Configuration/ });
    const ubuntu = buttons.find(b => b.closest('tr').textContent.includes('Ubuntu'));
    expect(ubuntu.disabled).toBe(true);
    expect(ubuntu.title).toContain('Delete Image');
    for (const b of buttons.filter(b => b.closest('tr').textContent.includes('Win 11'))) expect(b.disabled).toBe(false);
  });

  it('Lists data-disk consumers and mount targets', async () => {
    stubApi({
      listGroups: async () => list([{ ID: 'g-1', Name: 'Classroom A', SystemConfigID: 'win11_default' }]),
      listGroupDisks: async (gid) => list(gid === 'g-1' ? [{ ID: 'gd-1', GroupID: 'g-1', MountTarget: '/data', ConfigID: 'ubuntu_default' }] : []),
    });
    renderPage();
    await openTab('Configuration');
    const table = await screen.findByRole('table');
    const ubuRow = await waitFor(() => {
      const r = within(table).getAllByRole('row').find(x => x.textContent.includes('Ubuntu') && x.textContent.includes('/data'));
      expect(r).toBeTruthy();
      return r;
    });
    expect(ubuRow.textContent).toContain('Classroom A');
    const winRow = within(table).getAllByRole('row').find(r => r.textContent.includes('Win 11'));
    expect(within(winRow).getByText('Classroom A')).toBeTruthy();
  });

  it('Collapses group consumers after two entries and exposes full list on hover', async () => {
    const groups = ['Class A', 'Class B', 'Class C', 'Class D'].map((n, i) => ({ ID: `g-${i}`, Name: n, SystemConfigID: 'win11_default' }));
    stubApi({ listGroups: async () => list(groups), listGroupDisks: async () => list([]) });
    renderPage();
    await openTab('Configuration');
    const table = await screen.findByRole('table');
    const row = await waitFor(() => {
      const r = within(table).getAllByRole('row').find(x => x.textContent.includes('Win 11') && x.textContent.includes('Class A'));
      expect(r).toBeTruthy();
      return r;
    });
    expect(row.textContent).toContain('Class B');
    expect(row.textContent).not.toContain('Class C');
    const more = within(row).getByText('+2');
    expect(more.closest('[title]').getAttribute('title')).toContain('Class D');
  });

  it('Creates configurations from image source or existing restore points', async () => {
    const createConfigFromImage = vi.fn(async () => ({ task_id: 't1' }));
    const forkConfig = vi.fn(async () => ({ task_id: 't2' }));
    stubApi({ createConfigFromImage, forkConfig });
    renderPage();
    await openTab('Configuration');
    await userEvent.click(await screen.findByRole('button', { name: 'Create Configuration' }));
    await userEvent.type(screen.getByLabelText('Configuration Name'), 'Office Edition');
    await userEvent.selectOptions(screen.getByLabelText('Image'), 'win11');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(createConfigFromImage).toHaveBeenCalledWith('win11', { name: 'Office Edition' }));

    await userEvent.click(await screen.findByRole('button', { name: 'Create Configuration' }));
    await userEvent.type(screen.getByLabelText('Configuration Name'), 'Gaming Edition');
    await userEvent.selectOptions(screen.getByLabelText('Image'), 'win11');
    await userEvent.click(screen.getByLabelText('Restore point from an existing configuration'));
    await userEvent.selectOptions(await screen.findByLabelText('Source Restore Point'), 'win11_default_office');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(forkConfig).toHaveBeenCalledWith('win11_default', { name: 'Gaming Edition', reduction_id: 'win11_default_office' }));
  });
});

describe('Restore Points tab', () => {
  beforeEach(() => stubApi());
  afterEach(() => vi.restoreAllMocks());

  it('Lists restore points by image and configuration, with image filtering', async () => {
    renderPage();
    await openTab('Restore Point');
    const table = await screen.findByRole('table');
    await waitFor(() => expect(within(table).getByText('Office installed')).toBeTruthy());
    expect(within(table).getByRole('columnheader', { name: 'Image' })).toBeTruthy();
    expect(within(table).getByRole('columnheader', { name: 'Configuration' })).toBeTruthy();
    const row = within(table).getByText('Office installed').closest('tr');
    expect(within(row).getByText('Win 11')).toBeTruthy();
    expect(within(row).getByText('default')).toBeTruthy();
    await userEvent.selectOptions(screen.getByLabelText('Filter by Image'), 'ubuntu');
    await waitFor(() => expect(within(table).queryByText('Office installed')).toBeNull());
  });

  // Super Admin saves and data disk publishes create restore points automatically; manual creation would only duplicate
  // the latest point and can be confused with saving applied state. Keep its API for tests but not the UI.
  it('Omits manual Create Restore Point action from both tabs', async () => {
    renderPage();
    await openTab('Restore Point');
    await screen.findByText('Office installed');
    expect(screen.queryByRole('button', { name: /Create Restore Point/ })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: /^Configuration/ }));
    await screen.findByRole('button', { name: /Create Configuration/ });
    expect(screen.queryByRole('button', { name: /Create Restore Point/ })).toBeNull();
  });

  it('Applies a restore point', async () => {
    const applyReduction = vi.fn(async () => ({}));
    stubApi({ applyReduction });
    renderPage();
    await openTab('Restore Point');
    await screen.findByText('Office installed');
    expect(screen.getByText('Applied')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Apply Office installed' }));
    await waitFor(() => expect(applyReduction).toHaveBeenCalledWith('win11_default_office'));
  });

  it('Saves a restore point as a new image', async () => {
    const saveReductionAsImage = vi.fn(async () => ({ task_id: 'task-copy' }));
    stubApi({ saveReductionAsImage });
    renderPage();
    await openTab('Restore Point');
    await userEvent.click(await screen.findByRole('button', { name: 'Save as New Image Office installed' }));
    const name = await screen.findByLabelText('New Image Name');
    await userEvent.clear(name);
    await userEvent.type(name, 'Office Edition');
    await userEvent.click(screen.getByRole('button', { name: 'Save As' }));
    await waitFor(() => expect(saveReductionAsImage).toHaveBeenCalledWith('win11_default_office', { name: 'Office Edition' }));
  });

  // Reuse the same export dialog for images and restore points.
  it('Exports restore points via one-time tickets with optional gzip', async () => {
    const exportReductionTicket = vi.fn(async () => ({ url: '/api/images/export-download?token=t', file_name: 'x.zfs.gz' }));
    stubApi({ exportReductionTicket });
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    renderPage();
    await openTab('Restore Point');
    await userEvent.click(await screen.findByRole('button', { name: 'Export as Image File Office installed' }));
    expect(await screen.findByText(/Exports the complete disk data of this restore point/)).toBeTruthy();
    await userEvent.click(screen.getByLabelText('gzip Compression'));
    await userEvent.click(screen.getByRole('button', { name: /Download Locally/ }));
    await waitFor(() => expect(exportReductionTicket).toHaveBeenCalledWith('win11_default_office', true));
  });

  it('Exports restore points to the server directory', async () => {
    const exportReduction = vi.fn(async () => ({ task_id: 't3', path: '/var/lib/ndiskless/imports/Win 11-Office installed.zfs', node: '192.168.10.3' }));
    stubApi({ exportReduction });
    renderPage();
    await openTab('Restore Point');
    await userEvent.click(await screen.findByRole('button', { name: 'Export as Image File Office installed' }));
    await userEvent.click(await screen.findByRole('button', { name: /Export to Server Directory/ }));
    await waitFor(() => expect(exportReduction).toHaveBeenCalledWith('win11_default_office'));
    expect(await screen.findByText(/destination: 192\.168\.10\.3 on /)).toBeTruthy();
  });

  it('Omits Assign to Group from image details', async () => {
    renderPage();
    await openDetail();
    expect(screen.queryByRole('button', { name: 'Assign to Group' })).toBeNull();
  });

  it('Explains consequences before overwriting an image', async () => {
    const overwriteImage = vi.fn(async () => ({ task_id: 'task-merge' }));
    stubApi({ overwriteImage });
    renderPage();
    await openTab('Restore Point');
    await userEvent.click(await screen.findByRole('button', { name: 'More Actions 0' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Overwrite Original Image' }));
    const dialog = (await screen.findByText(/Replace the original contents of image Win 11/)).closest('.card');
    expect(dialog.textContent).toContain('Office installed');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Overwrite' }));
    await waitFor(() => expect(overwriteImage).toHaveBeenCalledWith('win11_default_0'));
  });

  it('Shows rejection reasons for selected restore-point deletions', async () => {
    const extra = { ID: 'win11_default_wps', ConfigID: 'win11_default', Name: '@wps', DisplayName: 'WPS installed', Status: 'ready', CreatedAt: '2026-07-30T12:00:00Z' };
    const deleteReduction = vi.fn(async (id) => {
      if (id === 'win11_default_office') throw new Error('Restore point is used by derived configuration work; delete work first');
      return {};
    });
    stubApi({ deleteReduction, listReductions: async (cid) => list(cid === 'win11_default' ? [...REDUCTIONS.win11_default, extra] : REDUCTIONS[cid] || []) });
    renderPage();
    await openTab('Restore Point');
    await screen.findByText('Office installed');
    await userEvent.click(screen.getByLabelText('Select restore point Office installed'));
    await userEvent.click(screen.getByLabelText('Select restore point WPS installed'));
    await userEvent.click(screen.getByRole('button', { name: /Delete Selected/ }));
    const dialog = (await screen.findByText(/Delete 2 restore points/)).closest('.card');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(deleteReduction).toHaveBeenCalledTimes(2));
    expect(await screen.findByText(/derived configuration work/)).toBeTruthy();
  });

  it('Blocks selecting or deleting the currently applied restore point', async () => {
    stubApi({ deleteReduction: vi.fn(async () => ({})) });
    renderPage();
    await openTab('Restore Point');
    await screen.findByText('Office installed');
    const box = screen.getByLabelText('Select restore point 0');
    expect(box.disabled).toBe(true);
    await userEvent.click(screen.getByLabelText('Select all restore points on this page'));
    expect(screen.getByRole('button', { name: /Delete Selected \(1\)/ })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'More Actions 0' }));
    const del = await screen.findByRole('button', { name: 'Delete' });
    expect(del.disabled).toBe(true);
    expect(del.title).toContain('Apply another one');
  });

  it('Clears selection after applying a restore point', async () => {
    let current = 'win11_default_0';
    stubApi({
      listConfigs: async (imageId) => list(imageId === 'win11' ? [{ ...CONFIGS.win11[0], DefaultReductionID: current }] : CONFIGS[imageId] || []),
      applyReduction: vi.fn(async (id) => { current = id; return {}; }),
      deleteReduction: vi.fn(async () => ({})),
    });
    renderPage();
    await openTab('Restore Point');
    await screen.findByText('Office installed');
    await userEvent.click(screen.getByLabelText('Select restore point Office installed'));
    expect(screen.getByRole('button', { name: /Delete Selected \(1\)/ })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Apply Office installed' }));
    await waitFor(() => expect(screen.getByLabelText('Select restore point Office installed').disabled).toBe(true));
    expect(screen.getByLabelText('Select restore point Office installed').checked).toBe(false);
    expect(screen.getByRole('button', { name: /Delete Selected/ }).disabled).toBe(true);
  });

  it('Does not restore stale selection after an applied point unlocks', async () => {
    let current = 'win11_default_0';
    stubApi({
      listConfigs: async (imageId) => list(imageId === 'win11' ? [{ ...CONFIGS.win11[0], DefaultReductionID: current }] : CONFIGS[imageId] || []),
      applyReduction: vi.fn(async (id) => { current = id; return {}; }),
    });
    renderPage();
    await openTab('Restore Point');
    await screen.findByText('Office installed');
    await userEvent.click(screen.getByLabelText('Select restore point Office installed'));
    await userEvent.click(screen.getByRole('button', { name: 'Apply Office installed' }));
    await waitFor(() => expect(screen.getByLabelText('Select restore point Office installed').disabled).toBe(true));
    await userEvent.click(screen.getByRole('button', { name: 'Apply 0' }));
    await waitFor(() => expect(screen.getByLabelText('Select restore point Office installed').disabled).toBe(false));
    expect(screen.getByLabelText('Select restore point Office installed').checked).toBe(false);
  });

  it('Identifies dependents before deleting a restore point', async () => {
    const groups = [{ ID: 'g-1', Name: 'Classroom A', SystemReductionID: 'win11_default_office' }];
    stubApi({ listGroups: async () => list(groups), deleteReduction: vi.fn(async () => ({})) });
    renderPage();
    await openTab('Restore Point');
    await userEvent.click(await screen.findByRole('button', { name: 'More Actions Office installed' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByText(/Restore Point Office installed/);
    expect(dialog.textContent).toContain('Used by groups Classroom A');
  });
});

describe('Image cards', () => {
  afterEach(() => vi.restoreAllMocks());

  it('Sorts system images before data images, then by creation time', async () => {
    const at = (d) => `2026-09-${d}T10:00:00Z`;
    stubApi({ listImages: async () => list([
      { ID: 'd-old', Name: 'Old Data Disk', OSType: 'windows', State: 'normal', Purpose: 'data', CreatedAt: at('01') },
      { ID: 's-old', Name: 'Old System Disk', OSType: 'windows', State: 'normal', Purpose: 'system', CreatedAt: at('02') },
      { ID: 'd-new', Name: 'New Data Disk', OSType: 'linux', State: 'normal', Purpose: 'data', CreatedAt: at('30') },
      { ID: 's-new', Name: 'New System Disk', OSType: 'linux', State: 'normal', Purpose: 'system', CreatedAt: at('20') },
    ]) });
    renderPage();
    await screen.findByText('New System Disk');
    const names = [...document.querySelectorAll('.card .ellip')].map(e => e.textContent).filter(n => /(?:System|Data) Disk$/.test(n));
    expect(names).toEqual(['Old System Disk', 'New System Disk', 'Old Data Disk', 'New Data Disk']);
  });

  it('Uses disk icon for data images and OS-specific icons for system images', async () => {
    stubApi();
    renderPage();
    await screen.findByText('Games D');
    const icon = (name) => within(screen.getByText(name).closest('.card')).getByRole('img');
    expect(icon('Games D').getAttribute('aria-label')).toBe('Data Disk Image');
    expect(icon('Win 11').getAttribute('aria-label')).toBe('Windows System Disk Image');
    expect(icon('Ubuntu').getAttribute('aria-label')).toBe('Linux System Disk Image');
    const shapes = new Set(['Games D', 'Win 11', 'Ubuntu'].map(n => icon(n).innerHTML));
    expect(shapes.size).toBe(3);
  });
});

describe('Browser image upload', () => {
  afterEach(() => vi.restoreAllMocks());

  it('Uploads large files in chunks with offsets', async () => {
    const chunks = [];
    stubApi({
      uploadChunk: async (id, offset, blob) => {
        chunks.push({ offset, size: blob.size });
        const received = offset + blob.size;
        return { upload_id: id, received, complete: received >= 12 };
      },
    });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');
    // Switch to Upload from Computer.
    const picker = dlg.querySelector('input[type="file"]');
    expect(picker).toBeTruthy();
    const file = new File(['0123456789ab'], 'win11.zfs', { type: 'application/octet-stream' });
    await userEvent.upload(picker, file);
    await waitFor(() => expect(chunks.length).toBeGreaterThan(0), { timeout: 4000 });
    // Chunks must be contiguous; gaps can yield corrupt files of the expected total length.
    let expected = 0;
    for (const c of chunks) {
      expect(c.offset).toBe(expected);
      expected += c.size;
    }
    expect(expected).toBe(12);
  });

  it('Resumes an interrupted upload from acknowledged offset', async () => {
    let failNext = true;
    const offsets = [];
    stubApi({
      // Query acknowledged upload offset on retry.
      uploadStatus: async () => ({ upload_id: 'nodeA-1', received: 6 }),
      uploadChunk: async (id, offset, blob) => {
        offsets.push(offset);
        if (failNext) { failNext = false; throw new Error('network'); }
        const received = offset + blob.size;
        return { upload_id: id, received, complete: received >= 12 };
      },
    });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');
    await userEvent.upload(dlg.querySelector('input[type="file"]'),
      new File(['0123456789ab'], 'win11.zfs'));
    await waitFor(() => expect(offsets.length).toBeGreaterThanOrEqual(2), { timeout: 5000 });
    // Resume after the first failed chunk, not from zero.
    expect(offsets[1]).toBe(6);
  });

  // Both browser upload and server-directory import are supported; upload is the default.
  it('Supports both import paths with browser upload as default', async () => {
    stubApi();
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');
    expect(dlg.querySelector('input[type="file"]')).toBeTruthy();
    expect(within(dlg).queryByRole('button', { name: /Server File/ })).toBeNull();
    expect(dlg.textContent).not.toMatch(/Scan Directory|Manual Input/);
  });

  // Remove browser-uploaded source files after import to recover pool capacity.
  it('Marks uploaded sources for post-import cleanup', async () => {
    const importImage = vi.fn(async () => ({ task_id: 't1' }));
    stubApi({
      importImage,
      uploadChunk: async (id, offset, blob) => ({
        upload_id: id, received: offset + blob.size, complete: true,
        path: '/var/lib/ndiskless/imports/win11.zfs',
      }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');
    await userEvent.upload(dlg.querySelector('input[type="file"]'),
      new File(['abc'], 'win11.zfs'));
    await waitFor(() => expect(dlg.textContent).toMatch(/Upload finished/));
    await userEvent.click(within(dlg).getByRole('button', { name: /^Import$/ }));
    await waitFor(() => expect(importImage).toHaveBeenCalled());
    expect(importImage.mock.calls[0][0].delete_source_after).toBe(true);
  });


  // source_path exists only after upload. Disable Import and explain the pending state without exposing node addresses.
  it('Disables Import until upload finishes and explains progress', async () => {
    let release;
    const held = new Promise(r => { release = r; });
    stubApi({
      uploadChunk: async (id, offset, blob) => {
        await held;
        return { upload_id: id, received: offset + blob.size, complete: true,
                 path: '/var/lib/ndiskless/imports/d.zfs.gz' };
      },
    });
    renderPage();
    await screen.findByText('Win 11');
    await userEvent.click(screen.getByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');

    // Cannot import without choosing a file.
    expect(screen.getByRole('button', { name: /^Import$/ }).disabled).toBe(true);

    await userEvent.upload(dlg.querySelector('input[type="file"]'), new File(['xy'], 'd.zfs.gz'));
    // While uploading, explain the disabled button rather than silently graying it out.
    await waitFor(() => expect(screen.getByRole('button', { name: /Uploading/ }).disabled).toBe(true));

    release();
    await waitFor(() => expect(dlg.textContent).toMatch(/Upload finished/));
    await waitFor(() => expect(screen.getByRole('button', { name: /^Import$/ }).disabled).toBe(false));
  });

  it('Upload status omits active-node identity', async () => {
    stubApi({
      listClusterNodes: async () => list([
        { id: 'n1', ip: '192.168.10.3', ha_state: 'active', online: true },
        { id: 'n2', ip: '192.168.10.4', ha_state: 'standby', online: true },
      ]),
    });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');
    const hint = within(dlg).getByText(/resumable/);
    expect(hint.textContent).toMatch(/synchronized to all cluster nodes/);
    expect(hint.textContent).not.toMatch(/Active node|192\.168/);
  });
});

// Server-directory imports/exports are local to the active node; identify that node.
describe('Server-directory import/export', () => {
  afterEach(() => vi.restoreAllMocks());

  const SOURCES = {
    items: [{ name: 'win11.zfs', path: '/var/lib/ndiskless/imports/win11.zfs', size: 12884901888, format: 'zfs-send' }],
    import_dir: '/var/lib/ndiskless/imports', node: '192.168.10.3', total: 1, pool_available: 500 * 1024 ** 3,
  };

  it('Selects server-directory files and identifies their node', async () => {
    const importImage = vi.fn(async () => ({ task_id: 't1' }));
    stubApi({ importImage, listImportSources: async () => SOURCES });
    renderPage();
    await screen.findByText('Win 11');
    await userEvent.click(screen.getByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');

    await userEvent.type(within(dlg).getByPlaceholderText('Win11-Pro-2026Q1'), 'from-server');
    await userEvent.click(within(dlg).getByRole('button', { name: /Server Directory/ }));
    // Show both directory and owning node.
    expect(await within(dlg).findByText(/192\.168\.10\.3/)).toBeTruthy();
    expect(within(dlg).getByText(/\/var\/lib\/ndiskless\/imports/)).toBeTruthy();

    await userEvent.click(await within(dlg).findByText('win11.zfs'));
    await userEvent.click(screen.getByRole('button', { name: 'Import' }));
    await waitFor(() => expect(importImage).toHaveBeenCalledWith(expect.objectContaining({
      source_path: '/var/lib/ndiskless/imports/win11.zfs',
    })));
    // Preserve files placed by operators in the server directory.
    expect(importImage.mock.calls[0][0].delete_source_after).toBeFalsy();
  });

  it('Returns server-directory export destination and node', async () => {
    const exportImage = vi.fn(async () => ({ task_id: 't2', path: '/var/lib/ndiskless/imports/Win 11.zfs', node: '192.168.10.3' }));
    stubApi({ exportImage, listImportSources: async () => SOURCES });
    renderPage();
    await openDetail();
    await userEvent.click(screen.getByRole('button', { name: /Export Image/ }));
    await userEvent.click(screen.getByRole('button', { name: /Export to Server Directory/ }));
    await waitFor(() => expect(exportImage).toHaveBeenCalledWith('win11'));
    expect(await screen.findByText(/Export task submitted; destination: 192\.168\.10\.3 on \/var\/lib\/ndiskless\/imports\/Win 11\.zfs/)).toBeTruthy();
  });
});

// Resuming reselected files requires modified time; progress starts at acknowledged offset.
describe('Reselected file upload resumes', () => {
  afterEach(() => vi.restoreAllMocks());

  it('Sends modified time and resumes from server offset', async () => {
    // A 10-byte file with 4 bytes acknowledged must resume at byte 4.
    const beginUpload = vi.fn(async () => ({
      upload_id: 'nodeA-abc', received: 4, node_id: 'nodeA',
    }));
    const uploadChunk = vi.fn(async (id, offset, blob) => ({
      upload_id: id, received: offset + blob.size, complete: true,
      path: '/var/lib/ndiskless/imports/win11.zfs.gz',
    }));
    stubApi({ beginUpload, uploadChunk });
    renderPage();
    await screen.findByText('Win 11');
    await userEvent.click(screen.getByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');

    const file = new File(['0123456789'], 'win11.zfs.gz');
    Object.defineProperty(file, 'lastModified', { value: 1724400000000 });
    await userEvent.upload(dlg.querySelector('input[type="file"]'), file);

    await waitFor(() => expect(beginUpload).toHaveBeenCalledWith(expect.objectContaining({
      file_name: 'win11.zfs.gz', last_modified: 1724400000000,
    })));
    // On resume, send the first chunk from server offset, not zero.
    await waitFor(() => expect(uploadChunk).toHaveBeenCalled());
    expect(uploadChunk.mock.calls[0][1]).toBe(4);
  });
});

// Deep-linked details from global search or overview open once; close via X or Escape.
describe('Deep-linked image details', () => {
  afterEach(() => vi.restoreAllMocks());

  function DeepLink() {
    const { goto } = useStore();
    React.useEffect(() => { goto('images', { imageId: 'win11' }); }, []); // eslint-disable-line
    return null;
  }

  it('Closes deep-linked detail dialog by X and Escape', async () => {
    stubApi();
    render(<StoreProvider><ConfirmProvider><DeepLink/><PageImages/></ConfirmProvider></StoreProvider>);
    const title = await screen.findByText('Configuration Overview');
    const dlg = title.closest('.card');
    await userEvent.click(within(dlg.querySelector('.card-h')).getByRole('button'));
    await waitFor(() => expect(screen.queryByText('Configuration Overview')).toBeNull());
    // Closing stays closed across rerenders.
    await new Promise(r => setTimeout(r, 50));
    expect(screen.queryByText('Configuration Overview')).toBeNull();

    await openDetail();
    await userEvent.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByText('Configuration Overview')).toBeNull());
  });
});

// Aborting or closing import must stop upload loops and suppress obsolete in-flight errors.
describe('Upload cancellation', () => {
  afterEach(() => vi.restoreAllMocks());

  const startUpload = async () => {
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Import Image/ }));
    const dlg = (await screen.findByText('Image Name')).closest('.card');
    await userEvent.upload(dlg.querySelector('input[type="file"]'), new File(['x'.repeat(1000)], 'win11.zfs'));
    return dlg;
  };
  // A one-byte server chunk window makes cancellation observable.
  const slowChunk = (calls) => async (id, offset) => {
    calls.push(offset);
    await new Promise(r => setTimeout(r, 5));
    return { upload_id: id, received: offset + 1, complete: false };
  };
  const settle = () => new Promise(r => setTimeout(r, 80));

  it('Stops sending chunks and aborts server upload on Discard', async () => {
    const calls = [];
    const abortUpload = vi.fn(async () => null);
    stubApi({ uploadChunk: slowChunk(calls), abortUpload });
    await startUpload();
    await waitFor(() => expect(calls.length).toBeGreaterThan(2));
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }));
    await settle();
    const n = calls.length;
    await settle();
    expect(calls.length).toBe(n);
    expect(abortUpload).toHaveBeenCalledWith('nodeA-1');
  });

  it('Stops sending chunks after closing import dialog', async () => {
    const calls = [];
    stubApi({ uploadChunk: slowChunk(calls) });
    const dlg = await startUpload();
    await waitFor(() => expect(calls.length).toBeGreaterThan(2));
    await userEvent.click(within(dlg).getByRole('button', { name: 'Cancel' }));
    await settle();
    const n = calls.length;
    await settle();
    expect(calls.length).toBe(n);
  });

  it('Aborts in-flight chunks without showing stale errors', async () => {
    const inflight = [];
    stubApi({
      uploadChunk: (id, offset, blob, signal) => new Promise((resolve, reject) => {
        inflight.push({ signal, reject });
      }),
      uploadStatus: async () => { throw new Error('Upload session expired'); },
    });
    await startUpload();
    await waitFor(() => expect(inflight.length).toBe(1));
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(inflight[0].signal && inflight[0].signal.aborted).toBe(true);
    inflight[0].reject(new Error('network'));
    await settle();
    expect(inflight.length).toBe(1);
    expect(screen.queryByText(/Upload session expired|network/)).toBeNull();
  });
});
