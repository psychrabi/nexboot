// Aggregate across the cluster rather than the local node; otherwise operators may misjudge image capacity.
// Show the cluster panel only for multi-node clusters.
import React from "react";
import { Icons } from "../icons.jsx";
import { useStore } from "../store.jsx";
import { api } from "../../lib/api.js";
import { useResource } from "../../lib/hooks.js";
import { TableState, StatCard } from "../primitives.jsx";
import { fmtCap, TASK_LABELS } from "../../lib/format.js";

const TASK_STATUS = {
  pending: { t: "Pending", c: "" },
  running: { t: "Running", c: "cyan" },
  success: { t: "Succeeded", c: "emerald" },
  failed: { t: "Failed", c: "rose" },
};

// Show only a few recent jobs so restore-point activity does not displace capacity and image information.
const RECENT_TASKS = 5;

function poolRow(p, node, key) {
  const cap = Number(p.Capacity || 0),
    used = Number(p.Used || 0);
  const pct = cap ? (used / cap) * 100 : 0;
  return {
    key,
    node,
    name: p.Name,
    reachable: true,
    cap,
    used,
    pct,
    disks: (p.Disks || []).length,
    color: pct > 85 ? "amber" : pct > 70 ? "cyan" : "emerald",
  };
}

function PageOverview() {
  const store = useStore();

  const { data, loading } = useResource(async () => {
    const [tr, gr, ir, pr, kr, nr, cr] = await Promise.allSettled([
      api.listTerminals(),
      api.listGroups(),
      api.listImages(),
      api.listPools(),
      api.listTaskHistory({ page: 1, size: RECENT_TASKS }),
      api.listClusterNodes(),
      api.listClusterPools(),
    ]);
    const items = (r) => (r.status === "fulfilled" ? r.value?.items || [] : []);
    return {
      terminals: items(tr),
      groups: items(gr),
      images: items(ir),
      pools: items(pr),
      tasks: items(kr),
      nodes: items(nr),
      cluster: cr.status === "fulfilled" ? cr.value : null,
    };
  }, []);
  const {
    terminals = [],
    groups = [],
    images = [],
    pools = [],
    tasks = [],
    nodes = [],
    cluster = null,
  } = data || {};
  const online = terminals.filter((t) => t.State === "online").length;
  // A cluster is shown only for more than one node even though the API also responds for a standalone node.
  const clustered = nodes.length > 1;
  // Prefer cluster totals when available; exclude unreachable nodes rather than treating their capacities as zero.
  const poolCount = clustered && cluster ? cluster.pool_count : pools.length;
  const totalCap =
    clustered && cluster
      ? Number(cluster.total_capacity || 0)
      : pools.reduce((s, p) => s + Number(p.Capacity || 0), 0);
  const usedCap =
    clustered && cluster
      ? Number(cluster.total_used || 0)
      : pools.reduce((s, p) => s + Number(p.Used || 0), 0);
  const offlineNodes = nodes.filter((n) => !n.online).length;
  // Clients booted before placement tracking have no node field; attribute them to the local node.
  const selfNodeID =
    (cluster?.nodes || []).find((n) => n.is_self)?.node_id || "";
  const activeNode = nodes.find((n) => n.ha_state === "active");
  // Show each pool on its own row with its node. List unreachable nodes explicitly to avoid implying they have no storage.
  const poolRows =
    clustered && cluster
      ? (cluster.nodes || []).flatMap((n) =>
          n.reachable
            ? (n.pools || []).map((p) =>
                poolRow(p, n.ip, `${n.node_id}/${p.Name}`),
              )
            : [
                {
                  key: n.node_id,
                  node: n.ip,
                  name: "—",
                  reachable: false,
                  error: n.error || "Cannot read pools on this node",
                },
              ],
        )
      : pools.map((p) => poolRow(p, "", p.ID));
  // Compare active-client load across nodes to assess distribution; offline clients do not contribute.
  const loadByNode = terminals.reduce((m, t) => {
    if (t.State !== "online") return m;
    const id = t.StorageServerID || selfNodeID;
    if (id) m[id] = (m[id] || 0) + 1;
    return m;
  }, {});
  const peakLoad = Math.max(1, ...nodes.map((n) => loadByNode[n.id] || 0));
  const capacity = groups.reduce((s, g) => s + (g.ClientMax || 0), 0);
  const onlineOf = (gid) =>
    terminals.filter((t) => t.GroupID === gid && t.State === "online").length;

  return (
    <div
      className="fade-in"
      style={{ display: "flex", flexDirection: "column", gap: 18 }}
    >
      {/* Summary statistics */}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))",
          gap: 14,
        }}
      >
        {clustered && (
          <StatCard
            label="Cluster"
            value={nodes.length}
            unit="nodes"
            accent={offlineNodes ? "amber" : "emerald"}
            sub={`${offlineNodes ? `${offlineNodes} offline` : "All online"}${activeNode ? ` · Active node ${activeNode.ip}` : ""}`}
          />
        )}
        <StatCard
          label="Online Clients"
          value={online}
          unit={`/ ${terminals.length}`}
          accent="emerald"
          sub={`${terminals.length} registered clients`}
        />
        <StatCard
          label="Client Groups"
          value={groups.length}
          unit="groups"
          accent="cyan"
          sub={`Capacity ${capacity} clients`}
        />
        <StatCard
          label="Storage Pools"
          value={poolCount}
          unit="pools"
          accent="violet"
          sub={`Used ${fmtCap(usedCap)} · Available ${fmtCap(totalCap - usedCap)}`}
        />
        <StatCard
          label="Images"
          value={images.length}
          unit="images"
          accent="amber"
          sub={`${images.filter((i) => i.OSType === "windows").length} Win · ${images.filter((i) => i.OSType === "linux").length} Linux`}
        />
      </div>

      {!loading && terminals.length === 0 && (
        <SetupGuide
          pools={pools}
          images={images}
          groups={groups}
          goto={store.goto}
        />
      )}

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(360px, 1fr))",
          gap: 14,
        }}
      >
        {/* Group utilization */}
        <div className="card" style={{ padding: 0 }}>
          <div className="card-h">
            <div className="card-title">
              <Icons.Group size={14} /> Group Online Rate
            </div>
            <span className="chip">{groups.length} groups</span>
          </div>
          <div style={{ padding: "16px 18px" }}>
            {groups.map((g) => {
              const on = onlineOf(g.ID),
                cap = g.ClientMax || 0;
              const pct = cap ? (on / cap) * 100 : 0;
              const color =
                pct >= 90 ? "emerald" : pct >= 50 ? "cyan" : "violet";
              return (
                <div
                  key={g.ID}
                  style={{ marginBottom: 10, cursor: "pointer" }}
                  onClick={() => store.goto("terminals", { groupId: g.ID })}
                >
                  <div
                    style={{
                      display: "flex",
                      justifyContent: "space-between",
                      fontSize: 13,
                      marginBottom: 4,
                    }}
                  >
                    <span>{g.Name}</span>
                    <span className="mono" style={{ color: "var(--fg-mute)" }}>
                      <span style={{ color: `var(--${color})` }}>{on}</span> /{" "}
                      {cap}
                    </span>
                  </div>
                  <div className={`bar ${color}`}>
                    <span style={{ width: `${pct}%` }} />
                  </div>
                </div>
              );
            })}
            {!groups.length && (
              <div
                style={{
                  fontSize: 13,
                  color: "var(--fg-faint)",
                  textAlign: "center",
                  padding: 16,
                }}
              >
                {loading ? "Loading…" : "No groups"}
              </div>
            )}
          </div>
        </div>

        {/* Recent tasks */}
        <div className="card" style={{ padding: 0 }}>
          <div className="card-h">
            <div className="card-title">
              <Icons.Tasks size={14} /> Recent Tasks
            </div>
            <button
              className="btn ghost"
              style={{ padding: "2px 8px" }}
              onClick={() => store.goto("tasks")}
            >
              View All
              <Icons.Chevron size={11} />
            </button>
          </div>
          <div style={{ padding: 14 }}>
            {tasks.slice(0, RECENT_TASKS).map((t) => {
              const st = TASK_STATUS[t.Status] || { t: t.Status, c: "" };
              return (
                <div key={t.ID} style={{ marginBottom: 12 }}>
                  <div
                    style={{
                      display: "flex",
                      justifyContent: "space-between",
                      fontSize: 13,
                      marginBottom: 5,
                      gap: 8,
                    }}
                  >
                    <span className="ellip">
                      {TASK_LABELS[t.Type] || t.Type} ·{" "}
                      <span className="muted" title={t.TargetRef || ""}>
                        {t.TargetName || t.TargetRef || "—"}
                      </span>
                    </span>
                    <span className={`chip ${st.c}`} style={{ flexShrink: 0 }}>
                      {st.t}
                    </span>
                  </div>
                  <div
                    className={`bar ${t.Status === "success" ? "emerald" : t.Status === "failed" ? "rose" : ""}`}
                  >
                    <span
                      style={{
                        width: `${t.Status === "success" ? 100 : t.Progress || 0}%`,
                      }}
                    />
                  </div>
                </div>
              );
            })}
            {!tasks.length && (
              <div
                style={{
                  fontSize: 13,
                  color: "var(--fg-faint)",
                  textAlign: "center",
                  padding: 16,
                }}
              >
                {loading ? "Loading…" : "No tasks"}
              </div>
            )}
          </div>
        </div>
      </div>

      <div className={`overview-trio${clustered ? "" : " duo"}`}>
        {/* Node load */}
        {clustered && (
          <div className="card" style={{ padding: 0 }}>
            <div className="card-h">
              <div className="card-title">
                <Icons.Server size={14} /> Node Load
              </div>
              <button
                className="btn ghost"
                style={{ padding: "2px 8px" }}
                onClick={() => store.goto("servers")}
              >
                Details
                <Icons.Chevron size={11} />
              </button>
            </div>
            <div
              style={{
                padding: 14,
                display: "flex",
                flexDirection: "column",
                gap: 12,
              }}
            >
              {nodes.map((n) => {
                const load = loadByNode[n.id] || 0;
                const pct = (load / peakLoad) * 100;
                return (
                  <div key={n.id}>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "baseline",
                        fontSize: 13,
                        marginBottom: 5,
                      }}
                    >
                      <span
                        style={{
                          display: "flex",
                          alignItems: "baseline",
                          gap: 8,
                        }}
                      >
                        <span
                          className="mono"
                          style={{ color: "var(--fg-mute)" }}
                        >
                          {n.ip}
                        </span>
                        <span className="meta">
                          {n.ha_state === "active" ? "Active" : "Standby"}
                        </span>
                        {!n.online && (
                          <span className="tag amber">Offline</span>
                        )}
                      </span>
                      <span
                        className="mono"
                        style={{ color: "var(--fg-mute)" }}
                      >
                        {load} clients
                      </span>
                    </div>
                    <div className={`bar ${n.online ? "cyan" : ""}`}>
                      <span style={{ width: `${n.online ? pct : 0}%` }} />
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}
        {/* Storage capacity */}
        <div className="card" style={{ padding: 0 }}>
          <div className="card-h">
            <div className="card-title">
              <Icons.Database size={14} /> Storage Pool Capacity
            </div>
            <button
              className="btn ghost"
              style={{ padding: "2px 8px" }}
              onClick={() => store.goto("storage")}
            >
              Details
              <Icons.Chevron size={11} />
            </button>
          </div>
          <div
            style={{
              padding: 14,
              display: "flex",
              flexDirection: "column",
              gap: 12,
            }}
          >
            {poolRows.map((row) => (
              <div key={row.key}>
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "baseline",
                    gap: 10,
                    fontSize: 13,
                    marginBottom: 5,
                  }}
                >
                  <span
                    className="mono"
                    style={{
                      color: "var(--fg-mute)",
                      display: "flex",
                      flexWrap: "wrap",
                      alignItems: "baseline",
                      columnGap: 10,
                      minWidth: 0,
                    }}
                  >
                    <span>
                      {row.node && (
                        <span style={{ color: "var(--fg-faint)" }}>
                          {row.node}{" "}
                        </span>
                      )}
                      {row.name}
                    </span>
                    {row.reachable && (
                      <span className="meta">
                        Used {fmtCap(row.used)} · Available{" "}
                        {fmtCap(row.cap - row.used)} · {row.disks} disks
                      </span>
                    )}
                  </span>
                  {row.reachable ? (
                    <span
                      className="mono"
                      style={{ color: `var(--${row.color})` }}
                    >
                      {row.pct.toFixed(0)}%
                    </span>
                  ) : (
                    <span className="mono" style={{ color: "var(--fg-faint)" }}>
                      Unavailable
                    </span>
                  )}
                </div>
                {row.reachable ? (
                  <>
                    <div className={`bar ${row.color}`}>
                      <span style={{ width: `${row.pct}%` }} />
                    </div>
                  </>
                ) : (
                  <div className="mono meta" style={{ marginTop: 4 }}>
                    {row.error}
                  </div>
                )}
              </div>
            ))}
            {!poolRows.length && (
              <div
                style={{
                  fontSize: 13,
                  color: "var(--fg-faint)",
                  textAlign: "center",
                  padding: 16,
                }}
              >
                {loading ? "Loading…" : "No storage pools"}
              </div>
            )}
          </div>
        </div>

        {/* Image overview */}
        <div className="card" style={{ padding: 0 }}>
          <div className="card-h">
            <div className="card-title">
              <Icons.Layers size={14} /> Images
            </div>
            <button
              className="btn ghost"
              style={{ padding: "2px 8px" }}
              onClick={() => store.goto("images")}
            >
              Details
              <Icons.Chevron size={11} />
            </button>
          </div>
          {/* Allow horizontal scrolling within the narrow card without expanding the entire page. */}
          <div style={{ padding: 0, overflowX: "auto" }}>
            <table className="t" style={{ margin: 0 }}>
              <tbody>
                {images.map((im) => (
                  <tr
                    key={im.ID}
                    style={{ cursor: "pointer" }}
                    onClick={() => store.goto("images", { imageId: im.ID })}
                  >
                    <td>
                      <span className="mono" style={{ fontWeight: 600 }}>
                        {im.Name}
                      </span>
                    </td>
                    <td>
                      <span className="chip">
                        {im.OSType === "linux"
                          ? "Linux"
                          : im.OSType === "windows"
                            ? "Windows"
                            : im.OSType}
                      </span>
                    </td>
                    <td className="mono muted" style={{ fontSize: 12 }}>
                      Logical {fmtCap(im.Size)} · Allocated{" "}
                      {im.Used == null ? "—" : fmtCap(im.Used)}
                    </td>
                    <td>
                      <span
                        className={`chip ${im.State === "normal" ? "emerald" : im.State === "error" ? "rose" : "cyan"}`}
                      >
                        {im.State === "normal"
                          ? "Ready"
                          : im.State === "importing"
                            ? "Importing"
                            : "Error"}
                      </span>
                    </td>
                  </tr>
                ))}
                {/* colSpan must match the number of columns so empty states span the table. */}
                <TableState
                  colSpan={4}
                  loading={loading}
                  empty={!images.length}
                  hint="No images"
                />
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  );
}

// Show the first-run checklist until the first client is registered. Derive progress from real data.
function SetupGuide({ pools, images, groups, goto }) {
  const steps = [
    {
      label: "Create Storage Pool",
      desc: "Combine server disks into a ZFS pool for images and client clones",
      done: pools.length > 0,
      page: "storage",
      action: "Create",
    },
    {
      label: "Import System Image",
      desc: "Import Windows/Linux images (VMDK, VHD, ZFS streams, etc.) as bootable images",
      done: images.length > 0,
      page: "images",
      action: "Import",
    },
    {
      label: "Create Client Group",
      desc: "Configure subnet, gateway, and system image shared by group clients",
      done: groups.length > 0,
      page: "groups",
      action: "Create",
    },
    {
      label: "Add Client",
      desc: "Register client MAC addresses for subsequent PXE boot",
      done: false,
      page: "terminals",
      action: "Add",
    },
  ];
  const next = steps.findIndex((s) => !s.done);
  return (
    <div className="card" style={{ padding: 0 }}>
      <div className="card-h">
        <div className="card-title">
          <Icons.Tasks size={14} /> Setup Guide · Four steps to deploy diskless
          clients
        </div>
        <span className="chip cyan">
          {steps.filter((s) => s.done).length} / {steps.length}
        </span>
      </div>
      <div
        style={{
          padding: 16,
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))",
          gap: 12,
        }}
      >
        {steps.map((s, i) => (
          <div
            key={s.page}
            style={{
              border: `1px solid ${i === next ? "var(--cyan)" : "var(--line-soft)"}`,
              borderRadius: 8,
              padding: 12,
              background: s.done
                ? "var(--emerald-soft)"
                : i === next
                  ? "var(--cyan-soft)"
                  : "var(--bg-0)",
              opacity: !s.done && i !== next ? 0.6 : 1,
            }}
          >
            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: 8,
                marginBottom: 6,
              }}
            >
              {/* Use Icons.Check rather than a glyph to keep weight and baseline consistent across fonts. */}
              <span
                className={`chip ${s.done ? "emerald" : i === next ? "cyan" : ""}`}
                style={{ fontSize: 11 }}
              >
                {s.done ? <Icons.Check size={10} /> : i + 1}
              </span>
              <span style={{ fontSize: 13, fontWeight: 600 }}>{s.label}</span>
            </div>
            <div className="hint" style={{ minHeight: 48 }}>
              {s.desc}
            </div>
            {!s.done && (
              <button
                className={`btn ${i === next ? "primary" : ""}`}
                style={{ marginTop: 8, padding: "3px 10px" }}
                onClick={() => goto(s.page)}
              >
                {s.action}
                <Icons.Chevron size={11} />
              </button>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

window.PageOverview = PageOverview;

export { PageOverview };
