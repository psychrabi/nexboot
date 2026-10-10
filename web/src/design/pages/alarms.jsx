// Server health checks deduplicate alerts by key and automatically recover resolved conditions. Acknowledge only silences an active alert; delete removes history.
import React from "react";
import { Icons } from "../icons.jsx";
import { useStore } from "../store.jsx";
import { useConfirm } from "../overlay.jsx";
import {
  Select,
  TableState,
  SegFilter,
  StatCard,
  Pager,
} from "../primitives.jsx";
import { api } from "../../lib/api.js";
import {
  useResource,
  useMutation,
  usePolling,
  usePageState,
} from "../../lib/hooks.js";
import { fmtDateTimeSec as fmtDateTime } from "../../lib/format.js";
const { useState, useEffect } = React;

const SEV = {
  error: { t: "Error", c: "rose", dot: "err" },
  warn: { t: "Warning", c: "amber", dot: "warn" },
  info: { t: "Information", c: "cyan", dot: "cyan" },
};
const ST = {
  active: { t: "Active", c: "rose" },
  acknowledged: { t: "Acknowledged", c: "cyan" },
  recovered: { t: "Recovered", c: "emerald" },
};

function PageAlarms() {
  const store = useStore();
  const conf = useConfirm();
  const [statusF, setStatusFState] = useState("");
  const [sevF, setSevFState] = useState("");
  const { page, size, setPage, pager } = usePageState();
  const setStatusF = (v) => {
    setStatusFState(v);
    setPage(1);
  };
  const setSevF = (v) => {
    setSevFState(v);
    setPage(1);
  };

  const {
    data,
    loading,
    error,
    reload: load,
  } = useResource(
    () => api.listAlarms({ status: statusF, severity: sevF, page, size }),
    [statusF, sevF, page, size],
  );
  const items = (data && data.items) || [];
  const total = (data && data.total) || 0;
  const summary = (data && data.summary) || {
    active: 0,
    error: 0,
    warn: 0,
    info: 0,
  };
  const { run } = useMutation(store.toast);

  // Poll because server health checks update alert status.
  usePolling(load, { intervalMs: 15000 });

  const ack = (a) =>
    run(() => api.ackAlarm(a.ID), "Alert acknowledged", { reload: load });
  // Selection is page-local; clear it on pagination or filter changes to avoid deleting unseen rows.
  const [selected, setSelected] = useState([]);
  useEffect(() => {
    setSelected([]);
  }, [page, size, statusF, sevF]);
  const toggle = (id) =>
    setSelected((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );
  const allOnPage =
    items.length > 0 && items.every((a) => selected.includes(a.ID));
  const delSelected = async () => {
    const ok = await conf.ask({
      title: "Delete alerts",
      message: `Delete ${selected.length}  alerts? This cannot be undone.`,
      danger: true,
      confirmText: "Delete",
    });
    if (!ok) return;
    let failed = 0;
    for (const id of selected) {
      try {
        await api.deleteAlarm(id);
      } catch {
        failed++;
      }
    }
    store.toast(
      failed
        ? `${failed}  deletions failed`
        : `Deleted ${selected.length} alerts`,
      failed ? "err" : "ok",
    );
    setSelected([]);
    load();
  };
  // Acknowledge all active alerts across pages, repeatedly fetching until none remain.
  const ackAll = async () => {
    let acked = 0;
    await run(
      async () => {
        for (let round = 0; round < 20; round++) {
          const res = await api.listAlarms({
            status: "active",
            page: 1,
            size: 200,
          });
          const active = (res && res.items) || [];
          const before = acked;
          for (const a of active) {
            try {
              await api.ackAlarm(a.ID);
              acked++;
            } catch {
              /* ignore */
            }
          }
          if (!active.length || acked === before) break;
        }
      },
      null,
      { reload: load },
    );
    if (acked) store.toast(`Acknowledged ${acked} alerts`, "ok");
  };

  const statusFilters = [
    { id: "", label: "All" },
    { id: "active", label: "Active", n: summary.active, color: "rose" },
    { id: "acknowledged", label: "Acknowledged" },
    { id: "recovered", label: "Recovered" },
  ];

  return (
    <div
      className="fade-in"
      style={{ display: "flex", flexDirection: "column", gap: 14 }}
    >
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(170px, 1fr))",
          gap: 12,
        }}
      >
        <StatCard label="Active Alerts" value={summary.active} accent="rose" />
        <StatCard label="Error" value={summary.error} accent="rose" />
        <StatCard label="Warning" value={summary.warn} accent="amber" />
        <StatCard label="Information" value={summary.info} accent="cyan" />
      </div>

      <div className="card" style={{ padding: 0 }}>
        <div className="card-h">
          <SegFilter
            value={statusF}
            onChange={setStatusF}
            options={statusFilters}
          />
          <div className="row" style={{ gap: 8 }}>
            <Select
              value={sevF}
              onChange={setSevF}
              options={[
                { value: "", label: "All Severities" },
                { value: "error", label: "Error" },
                { value: "warn", label: "Warning" },
                { value: "info", label: "Information" },
              ]}
              style={{ width: 110 }}
            />
            <button className="btn" disabled={!summary.active} onClick={ackAll}>
              <Icons.Check size={12} /> Acknowledge All
            </button>
            <button
              className="btn danger"
              disabled={!selected.length}
              onClick={delSelected}
            >
              <Icons.Trash size={12} /> Delete Selected
              {selected.length ? ` (${selected.length})` : ""}
            </button>
            <button className="btn ghost icon" onClick={() => load()}>
              <Icons.Refresh size={14} />
            </button>
          </div>
        </div>
        <div style={{ overflowX: "auto" }}>
          <table className="t">
            <thead>
              <tr>
                <th style={{ width: 36 }}>
                  <input
                    type="checkbox"
                    aria-label="Select all alerts on this page"
                    checked={allOnPage}
                    onChange={(e) =>
                      setSelected(
                        e.target.checked ? items.map((a) => a.ID) : [],
                      )
                    }
                  />
                </th>
                <th>Severity</th>
                <th>Type</th>
                <th>Resource</th>
                <th>Message</th>
                <th>Time</th>
                <th>Status</th>
                <th className="t-actions"></th>
              </tr>
            </thead>
            <tbody>
              {items.map((a) => {
                const sev = SEV[a.Severity] || SEV.info;
                const st = ST[a.Status] || ST.active;
                return (
                  <tr key={a.ID}>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`Select alert ${a.Resource || a.Type}`}
                        checked={selected.includes(a.ID)}
                        onChange={() => toggle(a.ID)}
                      />
                    </td>
                    <td>
                      <span className={`chip ${sev.c}`}>
                        <span className={`dot ${sev.dot}`} />
                        {sev.t}
                      </span>
                    </td>
                    <td style={{ whiteSpace: "nowrap" }}>{a.Type}</td>
                    <td
                      className="mono ellip"
                      style={{ fontSize: 12, maxWidth: 130 }}
                      title={a.Resource || ""}
                    >
                      {a.Resource || "—"}
                    </td>
                    {/* Allow messages to wrap to two lines without widening the table card. */}
                    <td
                      className="muted"
                      style={{ fontSize: 12, minWidth: 160 }}
                      title={a.Message || ""}
                    >
                      <div
                        style={{
                          display: "-webkit-box",
                          WebkitLineClamp: 2,
                          WebkitBoxOrient: "vertical",
                          overflow: "hidden",
                        }}
                      >
                        {a.Message || "—"}
                      </div>
                    </td>
                    <td className="mono muted" style={{ fontSize: 12 }}>
                      {fmtDateTime(a.UpdatedAt || a.CreatedAt)}
                    </td>
                    <td>
                      <span className={`chip ${st.c}`}>
                        {a.Status !== "active" && <Icons.Check size={10} />}
                        {st.t}
                      </span>
                    </td>
                    <td className="t-actions">
                      <div>
                        {a.Status === "active" && (
                          <button
                            className="btn"
                            style={{ padding: "3px 8px" }}
                            onClick={() => ack(a)}
                          >
                            Acknowledge
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
              <TableState
                colSpan={8}
                loading={loading}
                error={error}
                empty={!items.length}
                hint="No alerts · System healthy"
              />
            </tbody>
          </table>
        </div>
        <Pager {...pager(total)} />
      </div>
      {conf.node}
    </div>
  );
}

window.PageAlarms = PageAlarms;

export { PageAlarms };
