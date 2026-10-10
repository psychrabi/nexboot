// The backend has no cancel/retry/delete task endpoints, so those actions are not offered.
import React from "react";
import { Icons } from "../icons.jsx";
import { api } from "../../lib/api.js";
import { useResource, usePolling, usePageState } from "../../lib/hooks.js";
import { TableState, SegFilter, Pager } from "../primitives.jsx";
import {
  TASK_LABELS,
  fmtDateTimeSec as fmtDateTime,
  pad2,
} from "../../lib/format.js";
const { useState } = React;

const STATUS = {
  pending: { t: "Pending", c: "" },
  running: { t: "Running", c: "cyan" },
  success: { t: "Succeeded", c: "emerald" },
  failed: { t: "Failed", c: "rose" },
};
const duration = (t) => {
  if (!t.FinishedAt || !t.CreatedAt) return "—";
  const ms = new Date(t.FinishedAt) - new Date(t.CreatedAt);
  if (isNaN(ms) || ms < 0) return "—";
  if (ms < 1000) return `${ms}ms`;
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${pad2(s % 60)}s`;
};

// shortTaskID strips the task type prefix and retains the nanosecond timestamp.
const shortTaskID = (id) => String(id || "").replace(/^task-[a-z_]+-/, "");

function PageTasks() {
  const { page, size, setPage, setSize, pager } = usePageState();
  const [status, setStatus] = useState("");
  const [expanded, setExpanded] = useState(null);

  const { data, loading, error, reload } = useResource(
    () => api.listTaskHistory({ page, size, status }),
    [page, size, status],
  );
  const tasks = (data && data.items) || [];
  const total = (data && data.total) || 0;
  const unreachable = (data && data.unreachable) || [];

  // Resolve client and image names for the target column; fall back to MAC or ID.
  const { data: refData } = useResource(async () => {
    const [tr, ir] = await Promise.allSettled([
      api.listTerminals(),
      api.listImages(),
    ]);
    const items = (r) =>
      (r.status === "fulfilled" && r.value && r.value.items) || [];
    const byMac = {};
    for (const t of items(tr)) if (t.MAC && t.Name) byMac[t.MAC] = t.Name;
    const byImage = {};
    for (const im of items(ir))
      if (im.Name && im.Name !== im.ID) byImage[im.ID] = im.Name;
    return { byMac, byImage };
  }, []);
  const refLabel = (ref) => {
    if (!ref) return "";
    // Pool IDs contain a node and pool name; since server has its own column, display only the pool name.
    const pool = /^pool-[0-9a-f]+--(.+)$/.exec(ref);
    if (pool) return pool[1];
    const norm = String(ref)
      .replace(/[:\-.\s]/g, "")
      .toUpperCase();
    if (refData?.byMac?.[norm]) return `${refData.byMac[norm]} · ${ref}`;
    if (refData?.byImage?.[ref]) return `${refData.byImage[ref]} · ${ref}`;
    return ref;
  };

  // Automatically poll while this page contains unfinished tasks.
  const hasInflight = tasks.some(
    (t) => t.Status === "running" || t.Status === "pending",
  );
  usePolling(reload, { intervalMs: 3000, active: hasInflight });

  const setFilter = (s) => {
    setStatus(s);
    setPage(1);
    setExpanded(null);
  };
  const goPage = (p) => {
    setPage(p);
    setExpanded(null);
  };

  const filters = [
    { id: "", label: "All" },
    { id: "running", label: "Running", color: "cyan" },
    { id: "pending", label: "Pending" },
    { id: "success", label: "Succeeded", color: "emerald" },
    { id: "failed", label: "Failed", color: "rose" },
  ];

  return (
    <div className="fade-in card" style={{ padding: 0 }}>
      <div className="card-h">
        <SegFilter value={status} onChange={setFilter} options={filters} />
        <div className="row" style={{ gap: 8 }}>
          {unreachable.length > 0 && (
            <span className="meta" style={{ color: "var(--amber)" }}>
              {unreachable.join("、")} is unreachable; its tasks could not be
              listed
            </span>
          )}
          <span className="meta">
            Total {total} tasks
            {hasInflight ? " · Refreshing every 3 seconds" : ""}
          </span>
          <button
            className="btn ghost icon"
            onClick={() => reload()}
            title="Refresh now"
            aria-label="Refresh now"
          >
            <Icons.Refresh size={13} />
          </button>
        </div>
      </div>
      <table className="t">
        <thead>
          <tr>
            <th>Task ID</th>
            <th>Type</th>
            <th>Target</th>
            <th>Server</th>
            <th>Progress</th>
            <th>Status</th>
            <th>Created At</th>
            <th>Duration</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((t) => {
            const st = STATUS[t.Status] || { t: t.Status, c: "" };
            const isOpen = expanded === t.ID;
            return (
              <React.Fragment key={t.ID}>
                <tr
                  style={{ cursor: "pointer" }}
                  onClick={() => setExpanded(isOpen ? null : t.ID)}
                >
                  {/* The Type column makes the ID prefix redundant; the full ID is in the title. */}
                  <td className="mono" style={{ fontSize: 12 }} title={t.ID}>
                    {shortTaskID(t.ID)}
                  </td>
                  <td style={{ whiteSpace: "nowrap" }}>
                    {TASK_LABELS[t.Type] || t.Type}
                  </td>
                  <td
                    className="muted ellip"
                    style={{ maxWidth: 220 }}
                    title={t.TargetRef || ""}
                  >
                    {t.TargetName || refLabel(t.TargetRef) || t.Message || "—"}
                  </td>
                  <td className="mono muted" style={{ whiteSpace: "nowrap" }}>
                    {t.Node || "—"}
                  </td>
                  <td style={{ width: 340 }}>
                    <div
                      style={{ display: "flex", alignItems: "center", gap: 8 }}
                    >
                      <div
                        className={`bar ${t.Status === "success" ? "emerald" : t.Status === "failed" ? "rose" : ""}`}
                        style={{ flex: 1 }}
                      >
                        <span
                          style={{
                            width: `${t.Status === "success" ? 100 : t.Progress || 0}%`,
                          }}
                        />
                      </div>
                      <span
                        className="mono"
                        style={{ fontSize: 12, width: 36, textAlign: "right" }}
                      >
                        {t.Status === "success" ? 100 : t.Progress || 0}%
                      </span>
                    </div>
                    {t.Status === "running" && t.Message && (
                      <div
                        className="meta ellip"
                        style={{ marginTop: 3, maxWidth: 220 }}
                      >
                        {t.Message}
                      </div>
                    )}
                    {/* Show failure reasons immediately, without requiring expansion. */}
                    {t.Status === "failed" && t.Error && (
                      <div
                        className="meta ellip"
                        style={{
                          marginTop: 3,
                          maxWidth: 340,
                          color: "var(--rose)",
                        }}
                        title={t.Error}
                      >
                        {t.Error}
                      </div>
                    )}
                  </td>
                  <td>
                    <span className={`chip ${st.c}`}>
                      {t.Status === "running" && <span className="dot cyan" />}
                      {st.t}
                    </span>
                  </td>
                  <td className="mono muted" style={{ fontSize: 12 }}>
                    {fmtDateTime(t.CreatedAt)}
                  </td>
                  <td className="mono muted" style={{ fontSize: 12 }}>
                    {duration(t)}
                  </td>
                </tr>
                {isOpen && (
                  <tr>
                    <td
                      colSpan={8}
                      style={{
                        background: "var(--bg-0)",
                        padding: "10px 16px",
                      }}
                    >
                      <div
                        style={{
                          display: "grid",
                          gridTemplateColumns: "auto 1fr",
                          gap: "4px 16px",
                          fontSize: 12,
                        }}
                      >
                        <span style={{ color: "var(--fg-faint)" }}>
                          Message
                        </span>
                        <span>{t.Message || "—"}</span>
                        <span style={{ color: "var(--fg-faint)" }}>Result</span>
                        <span className="mono">{t.Result || "—"}</span>
                        <span style={{ color: "var(--fg-faint)" }}>
                          Finished At
                        </span>
                        <span className="mono">
                          {fmtDateTime(t.FinishedAt)}
                        </span>
                        {t.Error && (
                          <>
                            <span style={{ color: "var(--rose)" }}>Error</span>
                            <span style={{ color: "var(--rose)" }}>
                              {t.Error}
                            </span>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                )}
              </React.Fragment>
            );
          })}
          <TableState
            colSpan={8}
            loading={loading}
            error={error}
            empty={!tasks.length}
            hint="No tasks"
          />
        </tbody>
      </table>
      <Pager
        {...pager(total)}
        onPage={goPage}
        onSize={(n) => {
          setSize(n);
          setExpanded(null);
        }}
      />
    </div>
  );
}

window.PageTasks = PageTasks;

export { PageTasks };
