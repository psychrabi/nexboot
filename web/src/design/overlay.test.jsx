import React from 'react';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ConfirmProvider, Modal, useConfirm } from './overlay.jsx';

// useConfirm exposes ask; the provider owns rendering so callers cannot omit the dialog accidentally.
function DeleteButton({ onDone }) {
  const { ask } = useConfirm();
  return (
    <button onClick={async () => onDone(await ask({ message: 'Delete this alert?' }))}>
      Delete
    </button>
  );
}

describe('Confirmation Dialog', () => {
  it('Confirm resolves the caller's pending promise', async () => {
    const done = vi.fn();
    render(<ConfirmProvider><DeleteButton onDone={done}/></ConfirmProvider>);

    await userEvent.click(screen.getByText('Delete'));
    await screen.findByText('Delete this alert?');
    await userEvent.click(screen.getByText('Confirm'));

    await waitFor(() => expect(done).toHaveBeenCalledWith(true));
  });

  it('Cancel resolves false without leaving a pending promise', async () => {
    const done = vi.fn();
    render(<ConfirmProvider><DeleteButton onDone={done}/></ConfirmProvider>);

    await userEvent.click(screen.getByText('Delete'));
    await userEvent.click(await screen.findByText('Cancel'));

    await waitFor(() => expect(done).toHaveBeenCalledWith(false));
  });

  // Reject hooks used without their provider during render, rather than failing silently on click.
  it('Throws without a Provider before the button is clicked', () => {
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => {});
    expect(() => render(<DeleteButton onDone={() => {}}/>)).toThrow(/ConfirmProvider/);
    quiet.mockRestore();
  });

  it('Disables confirmation until typeToConfirm matches', async () => {
    const done = vi.fn();
    function Danger() {
      const { ask } = useConfirm();
      return <button onClick={async () => done(await ask({ message: 'Destroy storage pool', typeToConfirm: 'tank' }))}>Destroy</button>;
    }
    render(<ConfirmProvider><Danger/></ConfirmProvider>);

    await userEvent.click(screen.getByText('Destroy'));
    const confirm = await screen.findByText('Confirm');
    expect(confirm.disabled).toBe(true);

    await userEvent.type(screen.getByPlaceholderText('tank'), 'tank');
    expect(confirm.disabled).toBe(false);
    await userEvent.click(confirm);
    await waitFor(() => expect(done).toHaveBeenCalledWith(true));
  });
});

// Inline onClose handlers must not reorder nested overlays or lose the original scroll-lock state during rerenders.
describe('Stacked Overlays', () => {
  function Inner() {
    const [open, setOpen] = React.useState(true);
    return (
      <Modal open={open} onClose={() => setOpen(false)} title="Inner">
        <button onClick={() => setOpen(false)}>Close inner</button>
      </Modal>
    );
  }
  const MemoInner = React.memo(Inner);
  function Outer() {
    const [n, setN] = React.useState(0);
    const [open, setOpen] = React.useState(true);
    return (
      <>
        <Modal open={open} onClose={() => setOpen(false)} title="Outer">
          <button onClick={() => setN(n + 1)}>Refresh outer {n}</button>
          <button onClick={() => setOpen(false)}>Close outer</button>
        </Modal>
        <MemoInner/>
      </>
    );
  }

  it('Escape closes only the topmost overlay after rerender', async () => {
    render(<Outer/>);
    fireEvent.click(screen.getByText(/Refresh outer/));
    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByText('Inner')).toBeNull());
    expect(screen.getByText('Outer')).toBeTruthy();
  });

  it('Restores page scrolling after both overlays close', async () => {
    document.body.style.overflow = '';
    render(<Outer/>);
    expect(document.body.style.overflow).toBe('hidden');
    fireEvent.click(screen.getByText(/Refresh outer/));
    fireEvent.click(screen.getByText('Close inner'));
    fireEvent.click(screen.getByText('Close outer'));
    await waitFor(() => expect(screen.queryByText('Outer')).toBeNull());
    expect(document.body.style.overflow).toBe('');
  });
});
