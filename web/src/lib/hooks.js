// Data hooks centralize load/error state, mutation → toast → reload flows and non-overlapping polling.
// Pages only declare fetch functions and success messages.
import React from 'react';
import { api } from './api.js';

// Unwrap backend `{items:[...]}` lists or accept arrays directly.

// useResource(fetcher, deps) → { data, loading, error, reload, setData }
// Fetch on mount/dependency changes; reload preserves old data after failure and sets error.
export function useResource(fetcher, deps = []) {
  const [data, setData] = React.useState(null);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  // Accept only the most recent request so slow responses cannot overwrite new page/filter data.
  const seq = React.useRef(0);
  const reload = React.useCallback(async () => {
    const mine = ++seq.current;
    try {
      const d = await fetcher();
      if (mine !== seq.current) return;
      setData(d);
      setError("");
    } catch (e) {
      if (mine !== seq.current) return;
      setError(e.message || "Load failed");
    } finally {
      if (mine === seq.current) setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  // Only dependency changes enter loading state; polls and reloads refresh without flicker.
  React.useEffect(() => { setLoading(true); reload(); }, [reload]);
  return { data, loading, error, reload, setData };
}

// followTask polls to a terminal task state. A mutation returning task_id only confirms acceptance;
// the task result must be followed and errors displayed with their operator-facing reason.
const TASK_POLL_MS = 1500;
const TASK_GIVE_UP_MS = 30 * 60 * 1000; // Image imports may run for several minutes

async function followTask(taskId, { onSettled, alive }) {
  const deadline = Date.now() + TASK_GIVE_UP_MS;
  while (Date.now() < deadline) {
    await new Promise(r => setTimeout(r, TASK_POLL_MS));
    if (alive && !alive()) return;
    let task;
    try {
      task = await api.getTask(taskId);
    } catch {
      continue; // One polling failure does not mean task failure
    }
    if (task?.Status === "success" || task?.Status === "failed") {
      onSettled(task);
      return;
    }
  }
}

// onMutation fires after successful writes and completed tasks to update the global store,
// refreshing sidebar badges and global search without waiting for the next 30-second poll.
export const onMutation = (() => {
  const target = new EventTarget();
  return {
    emit: () => target.dispatchEvent(new Event('mutation')),
    subscribe: (fn) => {
      target.addEventListener('mutation', fn);
      return () => target.removeEventListener('mutation', fn);
    },
  };
})();

// useMutation(toast) → { busy, run }
// run wraps mutations with busy state, success/error toasts and reload, returning true on success.
// For task_id responses, return after acceptance and follow the task asynchronously, refreshing or showing errors.
export function useMutation(toast) {
  const [busy, setBusy] = React.useState(false);
  const alive = React.useRef(true);
  React.useEffect(() => () => { alive.current = false; }, []);
  const run = React.useCallback(async (fn, okMsg, { reload, errMsg = "Operation failed" } = {}) => {
    setBusy(true);
    try {
      const res = await fn();
      if (okMsg) toast(okMsg, "ok");
      onMutation.emit();
      const taskId = res && (res.task_id || res.taskId);
      if (taskId) {
        followTask(taskId, {
          alive: () => alive.current,
          onSettled: (task) => {
            if (task.Status === "failed") toast(task.Error || errMsg, "err");
            if (reload) reload();
            onMutation.emit();
          },
        });
        return true;
      }
      if (reload) await reload();
      return true;
    } catch (e) {
      toast(e.message || errMsg, "err");
      return false;
    } finally {
      setBusy(false);
    }
  }, [toast]);
  return { busy, run };
}

// usePolling invokes periodically without overlapping slow requests.
// Poll failures retry silently while useResource retains previous successful data.
export function usePolling(fn, { intervalMs, active = true } = {}) {
  React.useEffect(() => {
    if (!active || !intervalMs) return;
    let inflight = false;
    const id = setInterval(async () => {
      if (inflight) return;
      inflight = true;
      try { await fn(); } catch { /* Retry on next poll. */ }
      inflight = false;
    }, intervalMs);
    return () => clearInterval(id);
  }, [fn, intervalMs, active]);
}

export const PAGE_SIZES = [10, 20, 50];

// Pagination state for server-side lists; changing page size resets to page one.
export function usePageState(initialSize = PAGE_SIZES[0]) {
  const [page, setPage] = React.useState(1);
  const [size, setSizeState] = React.useState(initialSize);
  const setSize = React.useCallback((n) => { setSizeState(n); setPage(1); }, []);
  return { page, size, setPage, setSize, pager: (total) => ({ page, size, total, onPage: setPage, onSize: setSize }) };
}

// Paginate an in-memory list on the frontend.
export function usePaged(items, initialSize = PAGE_SIZES[0]) {
  const { page, size, setPage, setSize } = usePageState(initialSize);
  const pages = Math.max(1, Math.ceil(items.length / size));
  const cur = Math.min(page, pages);
  return {
    rows: items.slice((cur - 1) * size, cur * size),
    page: cur, size, setPage,
    pager: { page: cur, size, total: items.length, onPage: setPage, onSize: setSize },
  };
}
