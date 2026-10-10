// Server page: cluster nodes, selected node's systemd services, and data backup.
import React from "react";
import { Icons } from "../icons.jsx";
import { useStore } from "../store.jsx";
import { Modal, useConfirm } from "../overlay.jsx";
import { Field, Select, TableState, Toggle, Tip } from "../primitives.jsx";
import { api } from "../../lib/api.js";
import { useResource, useMutation, usePolling } from "../../lib/hooks.js";
import { fmtDateTime } from "../../lib/format.js";
const { useState } = React;

const SVC_STATUS = {
  running: { t: "Running", c: "emerald", dot: "live" },
  stopped: { t: "Stopped", c: "", dot: "idle" },
  failed: { t: "Failed", c: "rose", dot: "err" },
  "not-installed": { t: "Not Installed", c: "amber", dot: "warn" },
  activating: { t: "Starting", c: "cyan", dot: "cyan" },
  deactivating: { t: "Stopping", c: "cyan", dot: "cyan" },
  reloading: { t: "Reloading", c: "cyan", dot: "cyan" },
  unknown: { t: "Unknown", c: "", dot: "idle" },
};
const fmtUptime = (sec) => {
  sec = Number(sec || 0);
  if (sec <= 0) return "—";
  const d = Math.floor(sec / 86400),
    h = Math.floor((sec % 86400) / 3600),
    m = Math.floor((sec % 3600) / 60);
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : `${m}m`;
};
const sinceNow = (iso) => {
  if (!iso) return "—";
  const t = new Date(iso);
  if (isNaN(t)) return "—";
  return fmtUptime((Date.now() - t.getTime()) / 1000);
};

// Use the same role descriptions as the HA card.
const NODE_ROLE = {
  active: { chip: "emerald", text: "Active" },
  standby: { chip: "amber", text: "Standby" },
};

function fmtSeen(iso) {
  if (!iso) return "—";
  const sec = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (!Number.isFinite(sec) || sec < 0) return "—";
  if (sec < 60) return `${sec} seconds ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)} minutes ago`;
  return `${Math.floor(sec / 3600)} hours ago`;
}

function PageServers() {
  const store = useStore();
  const conf = useConfirm();
  const [busyKey, setBusyKey] = useState(null);
  const [logs, setLogs] = useState(null); // { key, label, text }
  const [selectedId, setSelectedId] = useState(null);

  const nodesRes = useResource(
    () => api.listClusterNodes().catch(() => null),
    [],
  );
  const matrixRes = useResource(
    () => api.listClusterServices().catch(() => null),
    [],
  );
  const matrix = (matrixRes.data && matrixRes.data.nodes) || [];
  // Until the matrix loads, avoid marking nodes unreachable; that would resemble a real outage.
  const matrixReady = matrixRes.data != null;
  // Combine roster heartbeats with matrix roles and services; display matrix nodes even if roster fails.
  const roster = (nodesRes.data && nodesRes.data.items) || [];
  const nodes = mergeNodes(roster, matrix);
  const clustered = nodes.length > 1;
  const load = () => {
    nodesRes.reload();
    matrixRes.reload();
  };

  // Default to the active node only on first load or if the selection disappears; do not override operator selection.
  const known = nodes.some((n) => n.id === selectedId);
  const fallbackId =
    (
      nodes.find((n) => n.ha_state === "active" && n.online) ||
      nodes.find((n) => n.online) ||
      nodes[0] ||
      {}
    ).id || null;
  const activeId = known ? selectedId : fallbackId;
  const selectedNode = nodes.find((n) => n.id === activeId) || null;
  const selectedView = matrix.find((m) => m.node_id === activeId) || null;

  // Poll while services are starting or stopping so the state updates automatically.
  const transitioning = (selectedView?.services || []).some((s) =>
    ["activating", "deactivating", "reloading"].includes(s.status),
  );
  usePolling(matrixRes.reload, { intervalMs: 2500, active: transitioning });

  // Include the target node in confirmations; changing the wrong node may affect different clients.
  const actOnNode = async (node, svc, action) => {
    const where = node.ip || node.id;
    const ok = await conf.ask({
      title: `${action === "restart" ? "Restart" : action === "stop" ? "Stop" : "Start"}Service`,
      message:
        `This will ${action === "restart" ? "restart" : action === "stop" ? "stop" : "start"} ${svc.capability || svc.label} (${svc.unit}) on ${where}.` +
        (svc.critical || action === "stop"
          ? "This is a critical diskless-boot service; connected clients on this node may be affected."
          : ""),
      danger: action === "stop",
      confirmText: action === "restart" ? "Restart" : "Confirm",
    });
    if (!ok) return;
    setBusyKey(`${node.id}/${svc.key}`);
    try {
      await api.clusterServiceAction(node.id, svc.key, action);
      store.toast(
        `${where} · ${svc.capability || svc.label} command sent`,
        "ok",
      );
      setTimeout(() => matrixRes.reload(), 800);
    } catch (e) {
      store.toast(e.message || "Operation failed", "err");
    } finally {
      setBusyKey(null);
    }
  };
  // Node removal requires typing its address and is offered only for offline nodes.
  const forgetNode = async (node) => {
    const where = node.ip || node.id;
    const ok = await conf.ask({
      title: "Remove Node from Cluster",
      message:
        `${where} will be removed from the roster and will no longer participate in active-node elections.\n` +
        `This does not delete local data. If this host reconnects to this cluster, it will rejoin as a new node.`,
      danger: true,
      confirmText: "Remove",
      typeToConfirm: where,
    });
    if (!ok) return;
    try {
      await api.forgetClusterNode(node.id);
      store.toast(`${where}  removed from cluster`, "ok");
      nodesRes.reload();
      matrixRes.reload();
    } catch (e) {
      store.toast(e.message || "Removal failed", "err");
    }
  };
  const openNodeLogs = async (node, svc) => {
    const label = `${node.ip || node.id} · ${svc.capability || svc.label}`;
    setLogs({ key: `${node.id}/${svc.key}`, label, text: "Loading…" });
    try {
      const r = await api.clusterServiceLogs(node.id, svc.key, 200);
      setLogs({
        key: `${node.id}/${svc.key}`,
        label,
        text: r.logs || "(No logs)",
      });
    } catch (e) {
      setLogs({
        key: `${node.id}/${svc.key}`,
        label,
        text: e.message || "Log retrieval failed",
      });
    }
  };
  return (
    <div
      className="fade-in"
      style={{ display: "flex", flexDirection: "column", gap: 16 }}
    >
      {/* The local node simply means the current VIP holder; select a node in the table to view its details below. */}
      <ClusterNodes
        nodes={nodes}
        matrix={matrix}
        matrixReady={matrixReady}
        selected={selectedId}
        onSelect={setSelectedId}
        onRefresh={load}
        onForget={clustered ? forgetNode : null}
      />

      <NodeServices
        node={selectedNode}
        view={selectedView}
        matrixReady={matrixReady}
        onAct={actOnNode}
        onLogs={openNodeLogs}
        busyKey={busyKey}
      />

      <BackupSection />

      {logs && (
        <Modal
          open
          onClose={() => setLogs(null)}
          title={`Logs · ${logs.label}`}
          size="xl"
          footer={
            <button className="btn" onClick={() => setLogs(null)}>
              Close
            </button>
          }
        >
          <pre
            style={{
              margin: 0,
              maxHeight: "60vh",
              overflow: "auto",
              background: "var(--bg-0)",
              border: "1px solid var(--line)",
              borderRadius: 6,
              padding: 12,
              fontSize: 12,
              fontFamily: "var(--font-mono)",
              whiteSpace: "pre-wrap",
              wordBreak: "break-all",
            }}
          >
            {logs.text}
          </pre>
        </Modal>
      )}
      {conf.node}
    </div>
  );
}

// An absent error does not imply a valid backup; require a snapshot before showing healthy status.
function backupChip(item, cfg) {
  if (item.error) return <span className="chip rose">{item.error}</span>;
  if (item.last_snapshot) return <span className="chip emerald">Healthy</span>;
  if (!cfg?.backup_pool) return <span className="chip">Not Configured</span>;
  return <span className="chip amber">Pending Backup</span>;
}

function BackupSection() {
  const store = useStore();
  const [form, setForm] = useState(null);

  const { data: status, reload: load } = useResource(async () => {
    try {
      const s = await api.getBackupStatus();
      setForm(formOf(s.config));
      return s;
    } catch {
      return null;
    }
  }, []);
  const nodesRes = useResource(
    () => api.listClusterBackups().catch(() => null),
    [],
  );
  const { busy, run: runMut } = useMutation(store.toast);

  // Configuration changes take effect only on Save. Disable Save when unchanged.
  const save = () =>
    runMut(
      () =>
        api.saveBackupConfig({
          enabled: form.enabled,
          schedule: scheduleOf(form),
        }),
      "Backup schedule saved",
      {
        reload: () => {
          load();
          nodesRes.reload();
        },
        errMsg: "Save failed",
      },
    );
  const run = () =>
    runMut(
      () => api.runBackup(),
      "Backup started; each node will update its own copy",
      {
        reload: () => {
          load();
          nodesRes.reload();
        },
        errMsg: "Start failed",
      },
    );

  const cfg = status?.config;
  // Derive dirty state by comparing form and stored config so reverting edits disables Save.
  const dirty =
    !!form &&
    !!cfg &&
    (form.enabled !== !!cfg.enabled ||
      (form.enabled && scheduleOf(form) !== (cfg.schedule || "")));
  const running =
    !!status?.task &&
    (status.task.Status === "running" || status.task.Status === "pending");
  const rows = (nodesRes.data && nodesRes.data.nodes) || [];

  return (
    <div className="card" style={{ padding: 0 }}>
      <div className="card-h">
        <div className="card-title">
          <Icons.Database size={14} /> Data Backup
        </div>
        <div className="row" style={{ gap: 6 }}>
          {running && (
            <span className="chip cyan">
              <span className="dot cyan" /> Backing Up
            </span>
          )}
          <button className="btn" disabled={busy || running} onClick={run}>
            <Icons.Backup size={12} /> Back Up Now
          </button>
        </div>
      </div>
      <div
        style={{
          padding: 16,
          display: "flex",
          flexDirection: "column",
          gap: 14,
        }}
      >
        {/* Choose the backup pool automatically from non-data pools. Offer schedule times without exposing cron. */}
        {/* Show the saved schedule beside Save as the outcome of these controls. */}
        {form && (
          <div
            data-testid="backup-controls"
            className="row"
            style={{ gap: 10, alignItems: "center" }}
          >
            {/* Integrate enablement into frequency selection; indicating Enabled before saving would be misleading.
                Set an explicit select width so it does not fill the row. */}
            <Select
              value={form.enabled ? form.every : "off"}
              aria-label="Backup Schedule"
              style={{ width: 104 }}
              onChange={(v) =>
                setForm(
                  v === "off"
                    ? { ...form, enabled: false }
                    : { ...form, enabled: true, every: v },
                )
              }
              options={[
                { value: "off", label: "Disabled" },
                { value: "daily", label: "Daily" },
                { value: "weekly", label: "Weekly" },
              ]}
            />
            {form.enabled && form.every === "weekly" && (
              <Select
                value={String(form.weekday)}
                aria-label="Weekday"
                style={{ width: 92 }}
                onChange={(v) => setForm({ ...form, weekday: Number(v) })}
                options={WEEKDAYS}
              />
            )}
            {/* Hide the time when disabled but retain it for re-enabling. */}
            {form.enabled && (
              <input
                className="input mono"
                type="time"
                style={{ width: 110 }}
                aria-label="Time"
                value={form.time}
                onChange={(e) => setForm({ ...form, time: e.target.value })}
              />
            )}
            <button
              className="btn primary"
              disabled={busy || !dirty}
              onClick={save}
            >
              <Icons.Check size={12} /> {busy ? "Saving…" : "Save"}
            </button>
            {cfg?.enabled && cfg?.next_run_at && (
              <span className="meta">
                Next{" "}
                <span className="mono">
                  {cfg.next_run_text || fmtDateTime(cfg.next_run_at)}
                </span>
              </span>
            )}
          </div>
        )}

        {/* Each node writes copies to its own backup pool; show backup status per node. */}
        <div
          data-testid="backup-nodes"
          style={{
            background: "var(--bg-0)",
            border: "1px solid var(--line-soft)",
            borderRadius: 6,
            overflow: "hidden",
          }}
        >
          <table className="t" style={{ margin: 0 }}>
            <thead>
              <tr>
                <th>Node</th>
                <th>Role</th>
                <th>Backup Pool</th>
                <th>Copy Time</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {rows.length === 0 && (
                <tr>
                  <td colSpan={5} className="meta" style={{ padding: 12 }}>
                    Cannot read backup status for each node yet
                  </td>
                </tr>
              )}
              {rows.map((n) => (
                <tr key={n.node_id}>
                  <td className="mono">{n.ip || n.node_id}</td>
                  <td>
                    <span className={`chip ${NODE_ROLE[n.role]?.chip || ""}`}>
                      {NODE_ROLE[n.role]?.text || n.role || "—"}
                    </span>
                  </td>
                  <td className="mono muted" style={{ fontSize: 12 }}>
                    {n.backup_pool || "—"}
                  </td>
                  <td className="mono muted" style={{ fontSize: 12 }}>
                    {n.last_backup_at ? fmtDateTime(n.last_backup_at) : "—"}
                  </td>
                  <td>{nodeBackupChip(n)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!status && (
          <div style={{ fontSize: 13, color: "var(--fg-faint)" }}>
            Backup status unavailable
          </div>
        )}
      </div>
    </div>
  );
}

const WEEKDAYS = [
  { value: "1", label: "Monday" },
  { value: "2", label: "Tuesday" },
  { value: "3", label: "Wednesday" },
  { value: "4", label: "Thursday" },
  { value: "5", label: "Friday" },
  { value: "6", label: "Saturday" },
  { value: "0", label: "Sunday" },
];

// formOf and scheduleOf convert daily@03:00 storage syntax to/from frequency and time controls.
function formOf(cfg) {
  const raw = (cfg?.schedule || "").trim();
  const base = {
    enabled: !!cfg?.enabled,
    every: "daily",
    weekday: 0,
    time: "03:00",
  };
  const parts = raw.split("@");
  if (parts[0] === "daily" && parts[1])
    return { ...base, every: "daily", time: parts[1] };
  if (parts[0] === "weekly" && parts[2])
    return {
      ...base,
      every: "weekly",
      weekday: Number(parts[1]) || 0,
      time: parts[2],
    };
  return base;
}

function scheduleOf(form) {
  const time = (form.time || "03:00").slice(0, 5);
  return form.every === "weekly"
    ? `weekly@${form.weekday}@${time}`
    : `daily@${time}`;
}

// Status indicates whether a recoverable copy exists; no configured backup pool is a separate state.
function nodeBackupChip(n) {
  if (!n.reachable)
    return <span className="chip rose">{n.error || "Unavailable"}</span>;
  if (!n.backup_pool) return <span className="chip">No Backup Pool</span>;
  if (n.copying)
    return (
      <span className="chip cyan">
        <span className="dot cyan" />
        Backing Up
      </span>
    );
  if (!n.last_backup_at)
    return <span className="chip amber">Pending Backup</span>;
  if (n.pending) return <span className="chip amber">Pending Sync</span>;
  return (
    <span className="chip emerald">
      <span className="dot live" />
      Healthy
    </span>
  );
}

function mergeNodes(roster, matrix) {
  const out = roster.map((n) => ({ ...n }));
  const byId = {};
  for (const n of out) byId[n.id] = n;
  for (const m of matrix) {
    const hit = byId[m.node_id];
    if (hit) {
      // Prefer the node's self-reported role because the replicated roster may lag.
      if (m.role) hit.ha_state = m.role;
      continue;
    }
    out.push({ id: m.node_id, ip: m.ip, ha_state: m.role, online: m.online });
  }
  return out.sort(
    (a, b) =>
      (b.ha_state === "active") - (a.ha_state === "active") ||
      String(a.ip || "").localeCompare(String(b.ip || "")),
  );
}

// Show hostname and OS as hover details; do not use them for decisions.
function nodeHint(node, view) {
  const h = (view && view.host) || {};
  const parts = [node.ip || node.id];
  if (h.hostname) parts.push(`Hostname ${h.hostname}`);
  if (h.os) parts.push(h.os);
  return parts.join(" · ");
}

function ClusterNodes({
  nodes,
  matrix,
  matrixReady,
  selected,
  onSelect,
  onRefresh,
  onForget,
}) {
  const [adopting, setAdopting] = useState(false);
  // The active node is authoritative for the VIP currently serving clients.
  const vip =
    (nodes.find((n) => n.ha_state === "active") || {}).portal_ip || "";
  const byId = {};
  for (const m of matrix) byId[m.node_id] = m;
  const ordered = [...nodes].sort(
    (a, b) => (b.ha_state === "active") - (a.ha_state === "active"),
  );
  const faults = matrix.reduce(
    (n, m) =>
      n + (m.reachable ? (m.services || []).filter((s) => !s.ok).length : 0),
    0,
  );
  const offline = nodes.filter((n) => !n.online).length;

  // Use any reachable node to establish fixed service columns and labels.
  const sample =
    matrix.find((m) => m.reachable && (m.services || []).length) || {};
  const columns = (sample.services || []).map((s) => ({
    key: s.key,
    label: s.capability || shortLabel(s.label),
  }));

  return (
    <div className="card" style={{ padding: 0 }} data-testid="cluster-nodes">
      <div className="card-h">
        <div className="card-title">
          <Icons.Server size={14} /> Cluster Nodes
          {/* Show VIP in the cluster heading, not again in an individual node row. */}
          {vip && (
            <span
              className="mono meta"
              style={{ marginLeft: 10, fontWeight: 400 }}
            >
              Virtual IP {vip}
            </span>
          )}
        </div>
        <div className="row" style={{ gap: 6 }}>
          <span className="chip">{nodes.length} Node</span>
          {offline > 0 && <span className="chip rose">{offline} offline</span>}
          <span className={`chip ${faults ? "amber" : "emerald"}`}>
            {faults ? `${faults}  issues` : "All Ready"}
          </span>
          <button className="btn" onClick={() => setAdopting(true)}>
            Add Node…
          </button>
          <button
            className="btn ghost icon"
            onClick={onRefresh}
            title="Refresh Now"
            aria-label="Refresh Now"
          >
            <Icons.Refresh size={14} />
          </button>
        </div>
      </div>
      {adopting && (
        <AdoptNodes
          onClose={() => {
            setAdopting(false);
            onRefresh && onRefresh();
          }}
        />
      )}
      <div style={{ overflowX: "auto" }}>
        <table className="t">
          <thead>
            <tr>
              <th>Node</th>
              <th>Role</th>
              {/* Give each service its own column so varying label lengths cannot cause misalignment. */}
              {columns.map((c) => (
                <th key={c.key}>{c.label}</th>
              ))}
              <th>Status</th>
              <th>Heartbeat</th>
              {/* Show per-node versions for rolling upgrades, when replication and protocol versions may differ. */}
              <th>Version</th>
              {/* Compare uptime across nodes to identify a node that recently restarted. */}
              <th>Uptime</th>
              <th className="t-actions"></th>
            </tr>
          </thead>
          <tbody>
            {ordered.map((n) => {
              const m = byId[n.id] || {};
              const services = m.services || [];
              const bad = services.filter((s) => !s.ok).length;
              const primary = n.ha_state === "active";
              const picked = n.id === selected;
              return (
                <React.Fragment key={n.id}>
                  <tr
                    onClick={() => onSelect(n.id)}
                    aria-selected={picked}
                    title={nodeHint(n, m)}
                    style={{
                      cursor: "pointer",
                      background: picked ? "var(--bg-0)" : undefined,
                      boxShadow: picked
                        ? "inset 2px 0 0 var(--accent)"
                        : undefined,
                    }}
                  >
                    <td className="mono">{n.ip || n.id}</td>
                    <td>
                      <span
                        className={`chip ${NODE_ROLE[n.ha_state]?.chip || ""}`}
                      >
                        {NODE_ROLE[n.ha_state]?.text || n.ha_state || "—"}
                      </span>
                    </td>
                    {!n.online || !m.reachable ? (
                      <td colSpan={Math.max(1, columns.length)}>
                        <span className="meta">
                          {!n.online
                            ? "Unreachable"
                            : !matrixReady
                              ? "Loading…"
                              : m.error || "Service status unavailable"}
                        </span>
                      </td>
                    ) : (
                      columns.map((c) => {
                        const s = services.find((x) => x.key === c.key);
                        if (!s)
                          return (
                            <td key={c.key} className="meta">
                              —
                            </td>
                          );
                        const verdict = capabilityState(s);
                        return (
                          <td key={c.key} title={cellHint(s, n)}>
                            <span
                              className={`chip ${verdict.chip}`}
                              style={{ padding: "1px 7px" }}
                            >
                              {verdict.chip === "emerald" && (
                                <span className="dot live" />
                              )}
                              {verdict.text}
                            </span>
                          </td>
                        );
                      })
                    )}
                    <td>
                      {!n.online ? (
                        <span className="chip rose">Offline</span>
                      ) : bad ? (
                        <span className="chip amber">{bad} issues</span>
                      ) : (
                        <span className="chip emerald">
                          <span className="dot live" />
                          Healthy
                        </span>
                      )}
                    </td>
                    <td className="meta">{fmtSeen(n.last_seen_at)}</td>
                    <td className="mono meta">{m.host?.version || "—"}</td>
                    <td className="meta">
                      {m.host?.uptime_sec ? fmtUptime(m.host.uptime_sec) : "—"}
                    </td>
                    <td className="t-actions">
                      <div>
                        {onForget && !n.online && (
                          <button
                            className="btn ghost icon danger"
                            title="Remove this node from cluster"
                            aria-label={`Remove node ${n.ip || n.id}`}
                            onClick={(e) => {
                              e.stopPropagation();
                              onForget(n);
                            }}
                          >
                            <Icons.Trash size={12} />
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                </React.Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// Explain expected stopped DHCP on a standby, since two authoritative dnsmasq services
// would conflict; the iSCSI unit exits after loading while the kernel provides the service.
function cellHint(svc, node) {
  const name = svc.capability || shortLabel(svc.label);
  const base = `${name} · ${capabilityState(svc).text} (${svc.unit})`;
  if (svc.expected === "stopped" && node.ha_state !== "active") {
    return `${base}\nDHCP should not run on standby; competing DHCP servers can reject clients.`;
  }
  if (svc.expected === "ready") {
    return `${base}\nThe unit exits after loading at boot. Its functionality is provided by the kernel.`;
  }
  return base;
}

// AdoptNodes adds unconfigured hosts on the client subnet; no fields needed because the VIP and cluster token
// are known to the cluster. Discovered nodes have no data pool.
function AdoptNodes({ onClose }) {
  const store = useStore();
  const { busy, run } = useMutation(store.toast);
  const { data, loading, error, reload } = useResource(
    () => api.discoverNodes(),
    [],
  );
  const items = (data && data.items) || [];

  const adopt = async (c) => {
    if (
      await run(
        () => api.adoptNode({ address: c.address }),
        `Accepted: ${c.address} will restart as standby and join the cluster. Create a data pool for it under Storage Pool Management.`,
      )
    ) {
      reload();
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title="Add Node"
      size="lg"
      footer={
        <>
          <button className="btn" onClick={reload} disabled={busy || loading}>
            Rescan
          </button>
          <button className="btn primary" onClick={onClose}>
            Done
          </button>
        </>
      }
    >
      <div data-testid="adopt-nodes" style={{ display: "grid", gap: 12 }}>
        <div className="hint">
          Machines on the client subnet that are{" "}
          <b>installed and not yet configured</b> can join with one click; no
          manual configuration is required. After joining, create a data pool
          under Storage Pool Management. Machines with existing pools are not
          listed and must be joined using the CLI.
        </div>
        {loading && <div className="hint">Scanning client subnet…</div>}
        {error && (
          <div style={{ fontSize: 13, color: "var(--rose)" }}>
            Scan failed: {error}
          </div>
        )}
        {!loading && !error && items.length === 0 && (
          <div className="hint">
            No eligible machines found. Verify the server is installed and
            running on the client subnet with no existing data pool. Remove
            previously registered nodes before rejoining.
          </div>
        )}
        {items.length > 0 && (
          <table className="t">
            <thead>
              <tr>
                <th>Address</th>
                <th>Node ID</th>
                <th>Version</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {items.map((c) => (
                <tr key={c.address}>
                  <td className="mono">{c.address}</td>
                  <td className="mono meta">{c.node_id}</td>
                  <td className="meta">{c.version || "—"}</td>
                  <td style={{ textAlign: "right" }}>
                    <button
                      className="btn"
                      disabled={busy}
                      onClick={() => adopt(c)}
                    >
                      Join Cluster
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </Modal>
  );
}

// NodeServices shows the selected host in its title to prevent operating on the wrong node.
function NodeServices({ node, view, matrixReady, onAct, onLogs, busyKey }) {
  const [expanded, setExpanded] = useState(null);
  if (!node) {
    return (
      <div className="card" style={{ padding: 16 }}>
        <span className="meta">No manageable nodes yet</span>
      </div>
    );
  }
  const where = node.ip || node.id;
  const role = NODE_ROLE[node.ha_state]?.text || node.ha_state || "";
  const services = (view && view.services) || [];
  const bad = services.filter((s) => s.ok === false).length;
  const unreachable = !node.online || !view || !view.reachable;
  const pending = node.online && !matrixReady;
  return (
    <div className="card" style={{ padding: 0 }} data-testid="node-services">
      <div className="card-h">
        <div className="card-title">
          <Icons.Cog size={14} /> Diskless Server ·{" "}
          <span className="mono">{where}</span>
          {role && (
            <span
              className={`chip ${NODE_ROLE[node.ha_state]?.chip || ""}`}
              style={{ marginLeft: 8 }}
            >
              {role}
            </span>
          )}
        </div>
        <span
          className={`chip ${pending ? "" : unreachable ? "rose" : bad ? "amber" : "emerald"}`}
        >
          {pending
            ? "Loading…"
            : unreachable
              ? node.online
                ? "Service status unavailable"
                : "Unreachable"
              : bad
                ? `${bad}  issues`
                : "All Running"}
        </span>
      </div>
      {unreachable ? (
        <div style={{ padding: 16 }}>
          <span className="meta">
            {pending
              ? "Loading service status…"
              : node.online
                ? "Cannot read service status for this node; actions are unavailable"
                : "Node unreachable; actions are unavailable"}
          </span>
        </div>
      ) : (
        <>
          <table className="t" style={{ background: "transparent" }}>
            {/* Surface service capability status; keep unit, enablement and PID details collapsed. */}
            <thead>
              <tr>
                <th>Service</th>
                <th>Status</th>
                <th className="t-actions">Actions</th>
              </tr>
            </thead>
            <tbody>
              {services.map((svc) => {
                const verdict = capabilityState(svc);
                const busy = busyKey === `${node.id}/${svc.key}`;
                const open = expanded === svc.key;
                return (
                  <React.Fragment key={svc.key}>
                    <tr>
                      <td>
                        <div className="row" style={{ gap: 8 }}>
                          <span style={{ fontWeight: 600 }}>
                            {svc.label || svc.capability}
                          </span>
                          {svc.critical && (
                            <span className="chip amber">Critical</span>
                          )}
                          {/* Attach help to the corresponding row instead of the table footer. */}
                          {svc.read_only && (
                            <Tip
                              label={`Help for ${svc.label || svc.capability}`}
                              text="The control service is read-only here. Restarting it would disconnect this session; use systemctl restart ndiskless on that node."
                            />
                          )}
                        </div>
                      </td>
                      <td>
                        <span className={`chip ${verdict.chip}`}>
                          {verdict.chip === "emerald" && (
                            <span className="dot live" />
                          )}
                          {verdict.text}
                        </span>
                      </td>
                      <td className="t-actions">
                        <div>
                          {/* Do not offer Restart for services expected to be stopped: restarting standby dnsmasq
                      could conflict with active DHCP and reject clients. Only offer Stop if it is running. */}
                          {!svc.read_only &&
                            svc.installed !== false &&
                            (svc.expected === "stopped" ? (
                              svc.status === "running" && (
                                <button
                                  className="btn"
                                  disabled={busy}
                                  style={{ padding: "3px 8px" }}
                                  onClick={() => onAct(node, svc, "stop")}
                                >
                                  Stop
                                </button>
                              )
                            ) : (
                              <button
                                className="btn"
                                disabled={busy}
                                style={{ padding: "3px 8px" }}
                                onClick={() => onAct(node, svc, "restart")}
                              >
                                Restart
                              </button>
                            ))}
                          <button
                            className="btn ghost"
                            style={{ padding: "3px 8px" }}
                            onClick={() => onLogs(node, svc)}
                          >
                            <Icons.Log size={11} /> Logs
                          </button>
                          {/* Keep unit status, uptime and PID in expandable details. */}
                          <button
                            className="btn ghost"
                            aria-label="Expand Details"
                            style={{ padding: "3px 8px" }}
                            onClick={() => setExpanded(open ? null : svc.key)}
                          >
                            {open ? "Collapse" : "Details"}
                          </button>
                        </div>
                      </td>
                    </tr>
                    {open && (
                      <tr>
                        <td colSpan={3} style={{ background: "var(--bg-0)" }}>
                          <div
                            style={{
                              display: "grid",
                              gridTemplateColumns:
                                "repeat(auto-fit, minmax(160px, 1fr))",
                              gap: 10,
                              padding: "4px 2px",
                            }}
                          >
                            <Info label="systemd Unit" value={svc.unit} />
                            <Info
                              label="Enable at boot"
                              value={bootText(svc)}
                            />
                            <Info
                              label="systemd Unit Status"
                              value={
                                svc.expected === "ready"
                                  ? `${(SVC_STATUS[svc.status] || SVC_STATUS.unknown).t} (unit exits after boot; service is provided by the kernel)`
                                  : (
                                      SVC_STATUS[svc.status] ||
                                      SVC_STATUS.unknown
                                    ).t
                              }
                            />
                            <Info
                              label="Running Time"
                              value={sinceNow(svc.active_since)}
                            />
                            <Info
                              label="PID"
                              value={
                                svc.main_pid > 0 ? String(svc.main_pid) : "—"
                              }
                            />
                          </div>
                          {!svc.read_only &&
                            svc.installed !== false &&
                            svc.expected !== "stopped" && (
                              <div
                                className="row"
                                style={{ gap: 6, marginTop: 8 }}
                              >
                                {svc.status !== "running" && (
                                  <button
                                    className="btn"
                                    disabled={busy}
                                    style={{ padding: "3px 8px" }}
                                    onClick={() => onAct(node, svc, "start")}
                                  >
                                    Start Unit
                                  </button>
                                )}
                                {svc.status === "running" && (
                                  <button
                                    className="btn ghost danger"
                                    disabled={busy}
                                    style={{ padding: "3px 8px" }}
                                    onClick={() => onAct(node, svc, "stop")}
                                  >
                                    Stop Unit
                                  </button>
                                )}
                              </div>
                            )}
                        </td>
                      </tr>
                    )}
                  </React.Fragment>
                );
              })}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}

// Service name appears in the header; show secondary capability labels on hover.
function shortLabel(label) {
  return String(label || "").split(" · ")[0];
}

// capabilityState reports locally provided, provided by active node, or abnormal.
function capabilityState(svc) {
  if (svc.ok === false) return { chip: "rose", text: "Abnormal" };
  if (svc.provided === false) return { chip: "", text: "Provided by Active" };
  // Report kernel-ready iSCSI and running dnsmasq consistently as Running.
  return { chip: "emerald", text: "Running" };
}

// Enable-on-boot is irrelevant for oneshot units; modules-load.d loads modules and exports are created on demand.
// Display Not required rather than Disabled, which suggests a misconfiguration.
function bootText(svc) {
  if (!svc.installed) return "—";
  if (svc.expected === "ready")
    return "Not required (kernel module loads at boot)";
  return svc.enabled ? "Enabled" : "Disabled";
}

// localVerdict separates high-level service status from detailed systemd status on hover.
// Fall back to raw status for older backend responses without ok.
function localVerdict(svc, st) {
  if (svc.ok === undefined)
    return { c: st.c, dot: st.dot, t: st.t, title: svc.unit };
  return {
    c: svc.ok ? "emerald" : "rose",
    dot: svc.ok ? "live" : "",
    t: stateText(svc),
    title: svc.detail
      ? `${svc.detail} (systemd unit: ${st.t})`
      : `systemd unit: ${st.t}`,
  };
}

// stateText reports service capability rather than systemd-specific state.
function stateText(s) {
  if (s.expected === "ready") return s.ok ? "Ready" : "Missing";
  if (s.expected === "stopped") return s.ok ? "Stopped" : "Should Not Run";
  if (s.ok) return "Running";
  return s.status === "not-installed" ? "Not Installed" : "Stopped";
}

function Info({ label, value }) {
  return (
    <div>
      <div className="lbl" style={{ marginBottom: 4 }}>
        {label}
      </div>
      <div className="mono ellip" style={{ fontSize: 14, fontWeight: 500 }}>
        {value}
      </div>
    </div>
  );
}

window.PageServers = PageServers;

export { PageServers };
