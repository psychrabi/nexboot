import React from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api.js';
import { useMutation, useResource, onMutation } from './hooks.js';

// Async mutations return task_id; failure reasons live on tasks, so test surfacing those errors.
function Screen({ toast, call, reload }) {
  const { busy, run } = useMutation(toast);
  return (
    <button disabled={busy} onClick={() => run(call, 'Task submitted', { reload, errMsg: 'Merge failed' })}>
      Merge
    </button>
  );
}

describe('Async task receipts', () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

  it("Shows the task's recorded failure reason", async () => {
    const toast = vi.fn();
    const reload = vi.fn();
    vi.spyOn(api, 'getTask')
      .mockResolvedValueOnce({ Status: 'running' })
      .mockResolvedValue({ Status: 'failed', Error: 'Configuration has derived children; delete derived configuration forked first' });

    render(<Screen toast={toast} reload={reload} call={async () => ({ task_id: 'task-merge-1' })}/>);
    await userEvent.click(screen.getByText('Merge'));

    expect(toast).toHaveBeenCalledWith('Task submitted', 'ok');
    await vi.advanceTimersByTimeAsync(5000);
    await waitFor(() => expect(toast).toHaveBeenCalledWith(
      'Configuration has derived children; delete derived configuration forked first', 'err'));
    expect(reload).toHaveBeenCalled();
  });

  it('Refreshes the list after success', async () => {
    const toast = vi.fn();
    const reload = vi.fn();
    vi.spyOn(api, 'getTask').mockResolvedValue({ Status: 'success' });

    render(<Screen toast={toast} reload={reload} call={async () => ({ task_id: 'task-merge-2' })}/>);
    await userEvent.click(screen.getByText('Merge'));

    await vi.advanceTimersByTimeAsync(3000);
    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(toast).not.toHaveBeenCalledWith(expect.anything(), 'err');
  });

  // A transient polling error is not task failure; keep polling rather than report a false failure.
  it('Keeps polling after a transient polling failure', async () => {
    const toast = vi.fn();
    const reload = vi.fn();
    vi.spyOn(api, 'getTask')
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValue({ Status: 'success' });

    render(<Screen toast={toast} reload={reload} call={async () => ({ task_id: 'task-import-1' })}/>);
    await userEvent.click(screen.getByText('Merge'));

    await vi.advanceTimersByTimeAsync(5000);
    // Continue polling past transient network errors until the job completes.
    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(toast).not.toHaveBeenCalledWith(expect.anything(), 'err');
  });

  // Synchronous mutations lack task IDs but still need to refresh data before returning.
  it('Refreshes synchronous operations without polling', async () => {
    const toast = vi.fn();
    const reload = vi.fn();
    const getTask = vi.spyOn(api, 'getTask');

    render(<Screen toast={toast} reload={reload} call={async () => ({ ID: 'group-1' })}/>);
    await userEvent.click(screen.getByText('Merge'));

    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(getTask).not.toHaveBeenCalled();
  });

  // Broadcast successful mutations so the global store refreshes immediately; never broadcast failed calls.
  it('Broadcasts only after success', async () => {
    const heard = vi.fn();
    const unsubscribe = onMutation.subscribe(heard);
    const toast = vi.fn();
    render(<Screen toast={toast} reload={vi.fn()} call={async () => ({ ID: 'group-1' })}/>);
    await userEvent.click(screen.getByText('Merge'));
    await waitFor(() => expect(heard).toHaveBeenCalledTimes(1));

    render(<Screen toast={toast} reload={vi.fn()} call={async () => { throw new Error('nope'); }}/>);
    await userEvent.click(screen.getAllByText('Merge')[1]);
    await waitFor(() => expect(toast).toHaveBeenCalled());
    expect(heard).toHaveBeenCalledTimes(1);
    unsubscribe();
  });

  it('Reports mutation errors without pretending submission succeeded', async () => {
    const toast = vi.fn();
    render(<Screen toast={toast} reload={vi.fn()} call={async () => { throw new Error('Restore point name is required'); }}/>);
    await userEvent.click(screen.getByText('Merge'));

    await waitFor(() => expect(toast).toHaveBeenCalledWith('Restore point name is required', 'err'));
    expect(toast).not.toHaveBeenCalledWith('Task submitted', 'ok');
  });
});

// Discard stale slow responses after filter/page changes and show loading while switching.
describe('useResource parameter changes', () => {
  function deferred() {
    let resolve;
    const promise = new Promise(r => { resolve = r; });
    return { promise, resolve };
  }
  function List({ k, pending }) {
    const { data, loading } = useResource(() => pending[k].promise, [k]);
    return <div>{`${loading ? 'Loading' : 'Ready'}:${data || 'Empty'}`}</div>;
  }

  it('Discards outdated responses rather than overwriting new data', async () => {
    const pending = { a: deferred(), b: deferred() };
    const { rerender } = render(<List k="a" pending={pending}/>);
    rerender(<List k="b" pending={pending}/>);
    await act(async () => { pending.b.resolve('Second page'); });
    await screen.findByText('Ready:Second page');
    await act(async () => { pending.a.resolve('First page'); });
    expect(screen.getByText('Ready:Second page')).toBeTruthy();
  });

  it('Returns to loading state when parameters change', async () => {
    const pending = { a: deferred(), b: deferred() };
    const { rerender } = render(<List k="a" pending={pending}/>);
    await act(async () => { pending.a.resolve('First page'); });
    await screen.findByText('Ready:First page');
    rerender(<List k="b" pending={pending}/>);
    expect(await screen.findByText('Loading:First page')).toBeTruthy();
  });
});
