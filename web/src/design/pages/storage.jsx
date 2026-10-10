// Storage Pool Management. Do not invent unsupported metrics such as throughput or L2ARC hit rate.
import React from 'react';
import { Icons } from '../icons.jsx';
import { useStore } from '../store.jsx';
import { Modal, useConfirm } from '../overlay.jsx';
import { Field, Select, TableState, StatCard, SegFilter } from '../primitives.jsx';
import { api } from '../../lib/api.js';
import { useResource, useMutation } from '../../lib/hooks.js';
import { fmtCap } from '../../lib/format.js';
const { useState, useEffect } = React;

const ROLE_LABEL = { data: "Storage Disks", read_cache: "Read Cache", write_cache: "Write Cache", special: "Metadata Disks", spare: "Hot Spares" };
// Ongoing ZFS pool operations.
const OP_LABEL = { resilver: "Resilvering", remove: "Migrating" };

// Data pools contain images, configurations and client clones; backup pools store local backups.
// Other pools have not yet been assigned a purpose.
const POOL_ROLE = {
  data: { cls: "cyan", text: "Data Pool", hint: "This pool stores images, configurations and client clones" },
  backup: { cls: "violet", text: "Backup Pool", hint: "Target pool for local backups (configure in Backup Settings)" },
};
const roleRank = (p) => (p.Role === "data" ? 0 : p.Role === "backup" ? 1 : 2);

// Layout cannot be changed without recreating the pool, so explain tradeoffs before selection.
const LAYOUTS = {
  mirror: { label: "Mirror (RAID1)", desc: "Each two (or three) disks hold a complete copy; mirror groups are striped. Reads scale with groups; writes replicate. Each pair tolerates one disk failure. Recommended for data pools." },
  stripe: { label: "Stripe", desc: "Highest capacity and throughput, but no redundancy: one failed disk loses the entire pool. Use only for testing." },
  raidz1: { label: "raidz1", desc: "Single parity, tolerating one disk failure. Random I/O is limited, disk removal unsupported, rebuilds are slow." },
  raidz2: { label: "raidz2", desc: "Double parity tolerates two failures. Capacity-oriented, weak random I/O, no disk removal; suitable for backup pools." },
  raidz3: { label: "raidz3", desc: "Triple parity tolerates three failures, with more redundancy and less capacity than raidz2." },
};
const LAYOUT_ORDER = ["mirror", "stripe", "raidz1", "raidz2", "raidz3"];
const RAIDZ_PARITY = { raidz1: 1, raidz2: 2, raidz3: 3 };

// Validate zpool names: begin with a letter, followed by letters, digits, underscores, hyphens, dots or colons.
// Reject reserved vdev keywords and names starting with c followed by a digit.
function poolNameProblem(name) {
  if (!/^[A-Za-z]/.test(name)) return "Pool name must start with a letter, e.g. tank or data-pool";
  if (!/^[A-Za-z0-9_.:-]+$/.test(name)) return "Pool name may contain only letters, digits and _ - . :";
  const lower = name.toLowerCase();
  if (/^(mirror|raidz|draid|spare)/.test(lower) || lower === "log") return `${name} is a reserved ZFS name (mirror / raidz / draid / spare prefixes and log are forbidden). Choose another.`;
  if (/^c[0-9]/.test(lower)) return `${name} is invalid: ZFS forbids a name beginning with c followed by a digit`;
  return "";
}

// layoutLabel describes RAID1 mirror groups, raidz levels and stripes from the actual pool layout.
// Say RAID1 for disk mirrors to distinguish them from system images.
function layoutLabel(pool) {
  const layout = pool.Layout || "stripe";
  if (layout === "mirror") {
    const groups = (pool.Groups || []).filter(g => g.Role === "data" && g.Kind === "mirror").length;
    return groups ? `Mirror (RAID1)×${groups}` : `Mirror (RAID1)${pool.GroupWidth || 2}-way`;
  }
  return (LAYOUTS[layout] || {}).label || layout;
}

// poolPlan validates disk selection according to backend layout rules and estimates capacity.
// Mirrors use each group's smallest member; raidz uses data count × minimum member × 0.9.
// Estimates only; actual capacity comes from used + available reported by the pool.
function poolPlan(layout, width, disks) {
  const n = disks.length;
  if (!n) return { ok: false, message: "" };
  const sizes = disks.map(d => Number(d.Size || 0));
  const min = (arr) => arr.reduce((m, v) => (m == null || v < m ? v : m), null) || 0;
  if (layout === "stripe") {
    return { ok: true, message: `Stripe ${n} disks ≈ ${fmtCap(sizes.reduce((a, b) => a + b, 0))} available · no redundancy` };
  }
  if (layout === "mirror") {
    if (n < width || n % width !== 0) {
      return { ok: false, message: `Mirrored pool requires ${width} disks per group. Select ${width - (n % width)} more (selected ${n} disks)` };
    }
    let total = 0;
    for (let i = 0; i < n; i += width) total += min(sizes.slice(i, i + width));
    return { ok: true, message: `镜像 ${n / width} groups ≈ ${fmtCap(total)} available · tolerates ${width - 1} disks` };
  }
  const parity = RAIDZ_PARITY[layout] || 0;
  const need = parity + 2;
  if (n < need) return { ok: false, message: `${layout} At least ${need} disks required. Select ${need - n} more (selected ${n} disks)` };
  return { ok: true, message: `${layout} ≈ ${fmtCap((n - parity) * min(sizes) * 0.9)} available · tolerates ${parity} disks` };
}

function healthMeta(h) {
  const up = String(h || "").toUpperCase();
  if (up === "ONLINE") return { cls: "emerald", dot: "live", text: "Online" };
  if (up === "DEGRADED") return { cls: "amber", dot: "warn", text: "Degraded" };
  if (up === "AVAIL") return { cls: "emerald", dot: "live", text: "Available Spare" }; // Hot spare available
  if (up === "INUSE") return { cls: "amber", dot: "warn", text: "Replacing" }; // Hot spare replacing failed disk
  if (up === "" || up === "PENDING") return { cls: "", dot: "cyan", text: "Processing" };
  if (up === "MISSING") return { cls: "rose", dot: "err", text: "Missing", title: "Pool missing on this node" };
  // Other ZFS states indicate failures; retain raw statuses in hover text for diagnostics.
  if (up === "FAULTED") return { cls: "rose", dot: "err", text: "Faulted", title: h };
  if (up === "OFFLINE") return { cls: "rose", dot: "err", text: "Offline", title: h };
  if (up === "UNAVAIL") return { cls: "rose", dot: "err", text: "Unavailable", title: h };
  if (up === "REMOVED") return { cls: "rose", dot: "err", text: "Removed", title: h };
  if (up === "SUSPENDED") return { cls: "rose", dot: "err", text: "Suspended", title: h };
  return { cls: "rose", dot: "err", text: h };
}

// A node may report a missing pool even though its database record remains.
const isMissing = (p) => String(p?.Health || "").toUpperCase() === "MISSING";

// Prefer status-bearing DiskItems when listing pool disks; fall back to path arrays.
function disksByRole(pool, role, fallbackKey) {
  const items = (pool.DiskItems || []).filter(d => d.Role === role);
  if (items.length) return items;
  return (pool[fallbackKey] || []).map(path => ({ Path: path, Role: role, Status: "" }));
}

// Dispatch pool writes to the node holding the disks; use local calls for local pools.
// Keep returned API shape identical so callers need no changes.
function poolWriter(node) {
  if (!node) return api;
  const at = (suffix) => (id, body) => api.clusterPoolAction(node, id, suffix, body);
  return {
    ...api,
    deletePool: (id) => api.clusterDestroyPool(node, id),
    addPoolDisk: at("/disks"),
    removePoolDisk: at("/disks/remove"),
    replacePoolDisk: at("/disks/replace"),
    mirrorUpgradePool: at("/mirror-upgrade"),
    addSpecial: at("/special"),
    removeSpecial: at("/special/remove"),
    addSpare: at("/spares"),
    removeSpare: at("/spares/remove"),
    addReadCache: at("/read-cache"),
    removeReadCache: at("/read-cache/remove"),
    addWriteCache: at("/write-cache"),
    removeWriteCache: at("/write-cache/remove"),
    flushWriteCache: (id) => api.clusterPoolAction(node, id, "/write-cache/flush"),
  };
}

function PageStorage() {
  const store = useStore();
  const conf = useConfirm();
  const [selectedId, setSelectedId] = useState(null);
  const [filterHealth, setFilterHealth] = useState("all");
  const [filterRole, setFilterRole] = useState("all");
  const [modal, setModal] = useState(null);
  const [form, setForm] = useState({ name: "", disks: [], layout: "mirror", group_width: 2 });

  // Settle discovery independently so lsblk failure does not clear the pool list.
  const { data, loading, error, reload: load } = useResource(async () => {
    const [pr, dr, nr, cr] = await Promise.allSettled([
      api.listPools(), api.listDisks(), api.listClusterNodes(), api.listClusterPools(),
    ]);
    if (pr.status === "rejected") throw pr.reason;
    return {
      localPools: (pr.value && pr.value.items) || [],
      disks: dr.status === "fulfilled" ? (dr.value && dr.value.items) || [] : [],
      nodes: nr.status === "fulfilled" ? (nr.value && nr.value.items) || [] : [],
      cluster: cr.status === "fulfilled" ? cr.value : null,
    };
  }, []);
  const localPools = (data && data.localPools) || [];
  const disks = (data && data.disks) || [];
  const nodes = (data && data.nodes) || [];
  const cluster = (data && data.cluster) || null;
  const clustered = nodes.length > 1;
  // Replicated pool records include other nodes, but only local pools have capacity numbers.
  // Obtain remote capacity and health from per-node cluster status.
  const nodeOf = (id) => nodes.find(n => n.id === id) || null;
  const selfNodeID = ((cluster && cluster.nodes) || []).find(n => n.is_self)?.node_id || "";
  const nodeLabel = (id) => (nodeOf(id)?.ip) || id || "—";
  const pools = clustered && cluster
    ? (cluster.nodes || []).flatMap(n => (n.pools || []).map(p => ({ ...p, ServerID: p.ServerID || n.node_id, __node: n })))
    : localPools;
  // List unreachable nodes rather than implying they have no storage.
  const unreachableNodes = clustered && cluster ? (cluster.nodes || []).filter(n => !n.reachable) : [];
  // A null node means local pool calls.
  const remoteNodeOf = (p) => (clustered && p && p.__node && !p.__node.is_self ? p.__node.node_id : null);
  const { busy, run } = useMutation(store.toast);

  const selected = pools.find(p => p.ID === selectedId) || null;

  const filtered = pools.filter(p => {
    if (filterRole !== "all" && p.Role !== filterRole) return false;
    if (filterHealth === "all") return true;
    const up = String(p.Health || "").toUpperCase();
    if (filterHealth === "online") return up === "ONLINE";
    if (filterHealth === "abnormal") return up !== "ONLINE" && up !== "PENDING" && up !== "";
    return true;
  }).sort((a, b) => roleRank(a) - roleRank(b)); // Data pools first; retain node order within a role

  const totalCap = clustered && cluster ? Number(cluster.total_capacity || 0) : pools.reduce((s, p) => s + Number(p.Capacity || 0), 0);
  const totalUsed = clustered && cluster ? Number(cluster.total_used || 0) : pools.reduce((s, p) => s + Number(p.Used || 0), 0);
  const onlineN = pools.filter(p => String(p.Health).toUpperCase() === "ONLINE").length;
  // Maximum two pools per node (data and backup); disable Create only if all listed nodes are full.
  // Backend rejects invalid selection of a full node.
  const poolsByServer = pools.reduce((m, p) => { const k = p.ServerID || ""; m[k] = (m[k] || 0) + 1; return m; }, {});
  const atPoolLimit = pools.length > 0 && Object.values(poolsByServer).every(n => n >= 2);
  const present = pools.filter(p => !isMissing(p));
  const dataDiskN = present.reduce((s, p) => s + ((p.Disks || []).length), 0);
  const rcN = present.reduce((s, p) => s + ((p.ReadCacheDisks || []).length), 0);
  const wcN = present.reduce((s, p) => s + ((p.WriteCacheDisks || []).length), 0);
  const usePct = totalCap ? Math.round(totalUsed / totalCap * 100) : 0;

  const openCreate = () => { setForm({ name: "", disks: [], layout: "mirror", group_width: 2, node: "", role: "" }); setModal("create"); };

  // Each node can have one data and one backup pool; calculate available roles.
  const createNodeID = form.node || selfNodeID;
  const nodePools = clustered ? pools.filter(p => (p.ServerID || "") === createNodeID) : pools;
  const hasData = nodePools.find(p => p.Role === "data");
  const hasBackup = nodePools.find(p => p.Role !== "data");
  const roleChoices = [
    !hasData && { value: "data", label: POOL_ROLE.data.text },
    !hasBackup && { value: "backup", label: POOL_ROLE.backup.text },
  ].filter(Boolean);
  const createRole = roleChoices.length === 1 ? roleChoices[0].value
    : roleChoices.some(o => o.value === form.role) ? form.role : "";
  const roleHint = hasData && hasBackup ? `This node already has data pool ${hasData.Name} and backup pool ${hasBackup.Name}; cannot create another`
    : hasData ? `This node already has data pool ${hasData.Name}; only a backup pool can be created`
    : hasBackup ? `This node already has backup pool ${hasBackup.Name}; only a data pool can be created`
    : "Data pools store images, configurations and client clones; backup pools are local backup targets";

  const pickedDisks = form.disks.map(path => disks.find(d => d.Path === path) || { Path: path, Size: 0 });
  const groupWidth = form.layout === "mirror" ? (form.group_width || 2) : 1;
  const plan = poolPlan(form.layout, groupWidth, pickedDisks);

  const doCreate = async () => {
    const name = form.name.trim();
    if (!createRole) { store.toast("Select a pool type", "err"); return; }
    if (!name) { store.toast("Enter pool name", "err"); return; }
    if (/\s/.test(name)) { store.toast("Pool name cannot contain spaces", "err"); return; }
    const nameProblem = poolNameProblem(name);
    if (nameProblem) { store.toast(nameProblem, "err"); return; }
    if (!form.disks.length) { store.toast("Select at least one disk", "err"); return; }
    if (!plan.ok) { store.toast(plan.message, "err"); return; }
    const body = { name, disks: form.disks, layout: form.layout, group_width: groupWidth, role: createRole };
    const target = clustered && form.node && form.node !== selfNodeID ? form.node : "";
    // Confirm again for nonredundant stripes or remote node creation.
    const stripe = form.layout === "stripe";
    if (stripe || target) {
      const where = target ? `Create pool on ${nodeLabel(target)} with name ${name}; disks: ${form.disks.join(", ")}.` : "";
      const risk = stripe ? `Stripe has no redundancy. Failure of any disk loses all data in ${name} (images, configurations, restore points and client clones). Create anyway?` : "";
      const sure = await conf.ask({
        title: stripe ? "Stripe Has No Redundancy" : "Create on Another Server",
        message: [where, risk].filter(Boolean).join("\n"),
        danger: stripe, confirmText: stripe ? "Create Anyway" : "Create",
      });
      if (!sure) return;
    }
    const ok = await run(
      () => (target ? api.clusterCreatePool(target, body) : api.createPool(body)),
      (target ? `Storage Pool ${name} submitted on ${nodeLabel(target)}; see Task Center for results` : `Storage Pool ${name} creation task submitted (background)`)
        + (createRole === "data" ? "; services restart automatically after creating the data pool" : ""),
      { reload: load, errMsg: "CreateFailed" });
    if (ok) setModal(null);
  };

  const doDestroy = async (pool) => {
    const node = remoteNodeOf(pool);
    const where = node ? nodeLabel(node) : "";
    // Pool names can repeat across nodes. Confirm a remote operation using the node address.
    const ok = isMissing(pool) ? await conf.ask({
      title: "Delete Pool Record",
      message: `${where || "Local node"}上已找不到 ${pool.Name}; delete its record only. No disks are modified. You may recreate a pool with that name afterward.`,
      confirmText: "Delete",
    }) : await conf.ask({
      title: "Destroy Storage Pool",
      message: node
        ? `Destroy ${where} on ${pool.Name} including all images, configurations, restore points and client clones. This cannot be undone. Verify the remote node address.`
        : `Destroy ${pool.Name} deletes all its images, configurations, restore points and client clones permanently.`,
      danger: true, confirmText: "Destroy", typeToConfirm: node ? where : pool.Name,
    });
    if (!ok) return;
    if (await run(() => poolWriter(node).deletePool(pool.ID), `Storage Pool ${pool.Name} destruction task submitted`, { reload: load, errMsg: "Destroy failed" })) {
      setSelectedId(null);
    }
  };

  // Show disks on the selected target node; /dev/sdb may refer to a different disk there.
  const createNode = (form && form.node) || "";
  const { data: nodeDiskData, loading: nodeDisksLoading } = useResource(
    async () => (clustered && createNode ? api.listClusterDisks(createNode) : null),
    [clustered, createNode],
  );
  const createDisks = clustered && createNode
    ? ((nodeDiskData && nodeDiskData.items) || [])
    : disks;
  // Annotate totals when unreachable nodes have unknown capacity.
  const missing = unreachableNodes.length;
  const partial = missing ? ` · Excludes ${missing} nodes` : "";
  // Send remote add/replace/cache operations to that node using its own disk inventory.
  const detailNode = remoteNodeOf(selected);
  const { data: detailDiskData, reload: reloadDetailDisks } = useResource(
    async () => (detailNode ? api.listClusterDisks(detailNode) : null),
    [detailNode],
  );
  const detailDisks = detailNode ? ((detailDiskData && detailDiskData.items) || []) : disks;
  const freeDisks = detailDisks.filter(d => !d.InUse);
  const onDetailChanged = () => { if (detailNode) reloadDetailDisks(); return load(); };
  const createFreeDisks = createDisks.filter(d => !d.InUse);

  return (
    <div className="fade-in" style={{ display: "flex", flexDirection: "column", gap: 16 }}>
      {/* Derive summary metrics from actual pool data. */}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))", gap: 12 }}>
        <StatCard label="Storage Pool" value={pools.length} unit=" pools" accent="cyan"
          sub={clustered
            ? `${onlineN} online · ${nodes.length} nodesServer${missing ? ` · ${missing} nodesunreachable` : ""}`
            : `${onlineN} online`}/>
        <StatCard label="Total Capacity" value={fmtCap(totalCap).split(" ")[0]} unit={fmtCap(totalCap).split(" ")[1] || ""} accent="violet"
          sub={`${dataDiskN}  storage disks${partial}`}/>
        <StatCard label="Used" value={fmtCap(totalUsed).split(" ")[0]} unit={fmtCap(totalUsed).split(" ")[1] || ""} accent="amber"
          sub={`Available ${fmtCap(totalCap - totalUsed)} · ${usePct}% Utilization${partial}`} pct={usePct}/>
        <StatCard label="Cache Disks" value={rcN + wcN} unit=" disks" accent="emerald" sub={`Read ${rcN} · Write ${wcN}`}/>
      </div>

      {/* Filter controls */}
      <div className="card" style={{ padding: "10px 14px", display: "flex", alignItems: "center", gap: 12 }}>
        <span className="lbl">Status</span>
        <Select value={filterHealth} onChange={setFilterHealth} aria-label="Filter by Status" options={[
          { value: "all", label: "All" },
          { value: "online", label: "Online" },
          { value: "abnormal", label: "Abnormal" },
        ]} style={{ width: 120 }}/>
        <span className="lbl">Type</span>
        <Select value={filterRole} onChange={setFilterRole} aria-label="Filter by Type" options={[
          { value: "all", label: "All" },
          { value: "data", label: "Data Pool" },
          { value: "backup", label: "Backup Pool" },
        ]} style={{ width: 120 }}/>
        <div style={{ flex: 1 }}/>
        <button className="btn ghost icon" onClick={() => load()} title="Refresh now" aria-label="Refresh now"><Icons.Refresh size={13}/></button>
        {atPoolLimit && <span className="hint">At most two pools per node (data + backup). Add disks to expand capacity.</span>}
        <button className="btn primary" onClick={openCreate} disabled={atPoolLimit}
          title={atPoolLimit ? "At most two pools per node (one data and one backup)" : undefined}>
          <Icons.Plus size={12}/> Create Storage Pool
        </button>
      </div>

      {/* Pool list */}
      <div className="card" style={{ padding: 0 }}>
        <table className="t">
          <thead><tr>
            <th>Pool Name</th><th>Server</th><th>Layout</th><th>容量</th><th>Storage Disks</th>
            <th>Read / Write Cache</th><th>Status</th><th className="t-actions"></th>
          </tr></thead>
          <tbody>
            {filtered.map((p) => {
              const cap = Number(p.Capacity || 0), used = Number(p.Used || 0);
              const pct = cap ? Math.round(used / cap * 100) : 0;
              const hm = healthMeta(p.Health);
              return (
                <tr key={p.ID} style={{ cursor: "pointer" }} onClick={() => setSelectedId(p.ID)}>
                  <td><div className="row"><Icons.Database size={14} style={{ color: "var(--cyan)" }}/><span className="mono" style={{ fontWeight: 600 }}>{p.Name}</span>{(() => {
                    const role = POOL_ROLE[p.Role];
                    return role
                      ? <span className={`chip ${role.cls}`} title={role.hint}>{role.text}</span>
                      : <span className="chip" title="Unassigned purpose; choose it for backups under Backup Settings">Unassigned</span>;
                  })()}</div></td>
                  {/* Display node IP, not hashed machine ID. */}
                  <td className="muted mono">{clustered ? nodeLabel(p.ServerID) : (p.ServerID || "—")}</td>
                  <td><span className={`chip ${(p.Layout || "stripe") === "stripe" ? "" : "cyan"}`}>{layoutLabel(p)}</span></td>
                  <td style={{ minWidth: 250 }}>
                    {isMissing(p) ? <span className="muted">Pool missing on this node</span> : <div className="row" style={{ gap: 8 }}>
                      {/* Constrain this column's width so the usage bar does not grow excessively. */}
                      <div className={`bar ${pct > 85 ? "rose" : pct > 70 ? "amber" : ""}`} style={{ flex: 1, minWidth: 60, maxWidth: 180 }}><span style={{ width: `${pct}%` }}/></div>
                      {/* Keep capacity text unwrapped; show available space separately because ZFS uses it for out-of-space decisions. */}
                      <span className="mono" style={{ fontSize: 12, whiteSpace: "nowrap", flexShrink: 0 }}>Used {fmtCap(used)} · Available {fmtCap(cap - used)}</span>
                    </div>}
                  </td>
                  <td className="mono">{isMissing(p) ? "—" : (p.Disks || []).length}</td>
                  <td className="mono" style={{ fontSize: 12 }}>
                    <span style={{ color: "var(--emerald)" }}>{(p.ReadCacheDisks || []).length}</span> / <span style={{ color: "var(--violet)" }}>{(p.WriteCacheDisks || []).length}</span>
                  </td>
                  <td>
                    <span className={`chip ${hm.cls}`}>{hm.dot && <span className={`dot ${hm.dot}`}/>}{hm.text}</span>
                    {p.Operation && <span className="mono meta" style={{ marginLeft: 6 }}>{OP_LABEL[p.Operation] || p.Operation} {p.Progress}</span>}
                  </td>
                  <td className="t-actions"><div><button className="btn ghost icon" title="查看池详情" aria-label={`查看 ${p.Name} 详情`}><Icons.Chevron size={12}/></button></div></td>
                </tr>
              );
            })}
            <TableState colSpan={8} loading={loading} error={error} empty={!filtered.length} hint={pools.length ? "No matching storage pools" : "No pools. Select Create Storage Pool."}/>
          </tbody>
        </table>
      </div>

      {selected && <PoolDetail pool={selected} freeDisks={freeDisks} onClose={() => setSelectedId(null)} onChanged={onDetailChanged}
        onDestroy={() => doDestroy(selected)} remoteNode={detailNode} remoteLabel={nodeLabel(detailNode)}
        serverLabel={clustered ? nodeLabel(selected.ServerID) : (selected.ServerID || "—")}/>}

      <Modal open={modal === "create"} onClose={() => !busy && setModal(null)} title="Create Storage Pool" size="md"
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button>
          <button className="btn primary" disabled={busy || !createRole || (form.disks.length > 0 && !plan.ok)} onClick={doCreate}>{busy ? "Creating…" : "Create"}</button></>}>
        {clustered && (
          <Field label="Target Server" required hint="Select the node that owns the disks. Disk names refer to physical devices on that specific server.">
            <Select value={form.node || selfNodeID} aria-label="Target Server"
              onChange={v => setForm({ ...form, node: v, disks: [] })}
              options={nodes.map(n => ({
                value: n.id,
                label: `${n.ip}${n.ha_state === "active" ? " (主机)" : ""}${n.online ? "" : " · 离线"}`,
              }))} style={{ width: "100%" }}/>
          </Field>
        )}
        <Field label="Pool Type" required hint={roleHint}>
          <Select value={createRole} aria-label="Pool Type" disabled={!roleChoices.length}
            onChange={v => setForm({ ...form, role: v })}
            options={!roleChoices.length ? [{ value: "", label: "Cannot create more pools" }]
              : roleChoices.length > 1 ? [{ value: "", label: "Select…" }, ...roleChoices] : roleChoices}/>
        </Field>
        <Field label="Pool Name" required hint="ZFS pool name; letters, digits and underscores, no spaces.">
          <input className="input mono" style={{ width: "100%" }} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="tank2"/>
        </Field>
        <Field label="Layout" required hint={LAYOUTS[form.layout].desc}>
          <div className="row" style={{ gap: 10, flexWrap: "wrap" }}>
            <SegFilter value={form.layout} onChange={v => setForm({ ...form, layout: v })}
              options={LAYOUT_ORDER.map(id => ({ id, label: LAYOUTS[id].label }))}/>
            {form.layout === "mirror" && (
              <div className="row" style={{ gap: 6 }}>
                <span className="lbl">Per Group</span>
                <Select value={String(form.group_width || 2)} onChange={v => setForm({ ...form, group_width: Number(v) })}
                  options={[{ value: "2", label: "2-way" }, { value: "3", label: "3-way" }]} style={{ width: 80 }}/>
              </div>
            )}
          </div>
        </Field>
        <Field label={`Select disks · Selected ${form.disks.length}`} required
          hint={plan.message ? <span className={plan.ok ? "mono" : ""} style={{ color: plan.ok ? "var(--fg-mute)" : "var(--amber)" }}>{plan.message}</span> : "Mirrors pair disks in selection order; raidz uses all selected disks as one group."}>
          <DiskPicker disks={createDisks} selected={form.disks} onToggle={(path, on) => {
            const set = new Set(form.disks);
            on ? set.add(path) : set.delete(path);
            setForm({ ...form, disks: [...set] });
          }}/>
        </Field>
        <div style={{ background: "var(--amber-soft)", border: "1px solid oklch(0.55 0.10 80 / 0.3)", borderRadius: 6, padding: "10px 12px", fontSize: 12, color: "var(--amber)", display: "flex", alignItems: "center", gap: 8 }}>
          <Icons.Shield size={14} style={{ flexShrink: 0 }}/> Selected disks will be formatted by ZFS and all existing data erased.
        </div>
      </Modal>
      {conf.node}
    </div>
  );
}

// DiskPicker lists physical disks and disables in-use devices.
function DiskPicker({ disks, selected, onToggle }) {
  if (!disks.length) {
    return <div style={{ background: "var(--bg-0)", border: "1px solid var(--line)", borderRadius: 6, padding: 14, fontSize: 12, color: "var(--fg-faint)", textAlign: "center" }}>No available disks detected</div>;
  }
  return (
    <div style={{ background: "var(--bg-0)", borderRadius: 6, border: "1px solid var(--line)", padding: 10, display: "flex", flexDirection: "column", gap: 6, maxHeight: 220, overflowY: "auto" }}>
      {disks.map(d => {
        const checked = selected.includes(d.Path);
        const disabled = d.InUse;
        return (
          <label key={d.Path} style={{ display: "flex", alignItems: "center", gap: 10, padding: "5px 6px", borderRadius: 4, cursor: disabled ? "not-allowed" : "pointer", opacity: disabled ? 0.45 : 1 }}>
            <input type="checkbox" disabled={disabled} checked={!!checked} onChange={e => onToggle(d.Path, e.target.checked)}/>
            <span className="mono" style={{ fontSize: 12, fontWeight: 600 }}>{d.Path}</span>
            {d.Type && <span className="chip">{d.Type}</span>}
            <span className="mono muted meta">{fmtCap(d.Size)}</span>
            {d.Model && <span className="muted meta ellip" style={{ maxWidth: 140 }}>{d.Model}</span>}
            <span style={{ marginLeft: "auto", fontSize: 12, color: disabled ? "var(--amber)" : "var(--emerald)" }}>{disabled ? `In Use${d.UsedBy ? " · " + d.UsedBy : ""}` : "Available"}</span>
          </label>
        );
      })}
    </div>
  );
}

// DiskPickModal selects free disks; validate(picked) returns {ok, message}.
// Disable confirmation for invalid layout groups or a mirror attachment with anything but one disk.
function DiskPickModal({ open, title, freeDisks, busy, onClose, onConfirm, validate, hint }) {
  const [picked, setPicked] = useState([]);
  useEffect(() => { if (open) setPicked([]); }, [open]);
  const check = picked.length && validate ? validate(picked) : { ok: picked.length > 0, message: "" };
  return (
    <Modal open={open} onClose={() => !busy && onClose()} title={title} size="md"
      footer={<><button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="btn primary" disabled={busy || !picked.length || !check.ok} onClick={() => onConfirm(picked)}>{busy ? "Submitting…" : "Confirm"}</button></>}>
      {hint && <div className="hint" style={{ marginBottom: 8 }}>{hint}</div>}
      <DiskPicker disks={freeDisks} selected={picked} onToggle={(path, on) => {
        setPicked(prev => on ? [...new Set([...prev, path])] : prev.filter(p => p !== path));
      }}/>
      {check.message && <div className={`hint${check.ok ? " mono" : ""}`} style={{ marginTop: 6, color: check.ok ? "var(--fg-mute)" : "var(--amber)" }}>{check.message}</div>}
    </Modal>
  );
}

// Show details in a centered dialog.
function PoolDetail({ pool, freeDisks, onClose, onChanged, onDestroy, remoteNode = null, remoteLabel = "", serverLabel = "" }) {
  // Dispatch all mutations from this view to the pool's node.
  const api = poolWriter(remoteNode);
  const store = useStore();
  const conf = useConfirm();
  const [picker, setPicker] = useState(null); // "data" | "read" | "write" | "special" | "spare" | { attach: "/dev/sdX" } | { replace: "/dev/sdX" } | { expand: "raidz2-0" }
  const [upgrade, setUpgrade] = useState(null); // Mapping of data disks to free disks during RAID1 upgrade
  const { busy, run: runMut } = useMutation(store.toast);
  const run = (fn, okMsg) => runMut(fn, okMsg, { reload: onChanged });

  const cap = Number(pool.Capacity || 0), used = Number(pool.Used || 0);
  const usePct = cap ? Math.round(used / cap * 100) : 0;
  const hm = healthMeta(pool.Health);
  const missing = isMissing(pool);
  const dataDisks = disksByRole(pool, "data", "Disks");
  const readCache = disksByRole(pool, "read_cache", "ReadCacheDisks");
  const writeCache = disksByRole(pool, "write_cache", "WriteCacheDisks");
  // Render vdev groups from actual pool layout. A stripe member is a single-disk group.
  const groups = pool.Groups || [];
  const dataGroups = groups.filter(g => g.Role === "data");
  const specialGroups = groups.filter(g => g.Role === "special");
  const spareDisks = groups.filter(g => g.Role === "spare").flatMap(g => g.Disks || []);

  const layout = pool.Layout || "stripe";
  const width = pool.GroupWidth || 1;
  const parity = RAIDZ_PARITY[layout] || 0;
  // Validate new data disks using backend layout rules.
  const validateExpand = (picked) => {
    const n = picked.length;
    if (layout === "mirror") {
      if (n < width || n % width !== 0) return { ok: false, message: `Mirrored pool requires ${width} disks一group加盘, Select ${width - (n % width)} more (selected ${n} disks)` };
      return { ok: true, message: `Add ${n / width} mirror groups` };
    }
    if (parity) {
      if (n !== width) return { ok: false, message: n < width ? `${layout} 池按adding complete groups, Per group ${width} disks, Select ${width - n} more (selected ${n} disks)` : `${layout} 池按adding complete groups, Per group ${width} disks, Remove ${n - width} disks` };
      return { ok: true, message: `Add 1 groups of ${layout}` };
    }
    return { ok: true, message: `Add ${n} stripe members with no redundancy` };
  };
  const validateAttach = (picked) => picked.length === 1
    ? { ok: true, message: `${picked[0]} will pair with ${picker?.attach} to form RAID1, copying used blocks online` }
    : { ok: false, message: "Attach only one disk to a single RAID1 member at a time" };
  const validateReplace = (picked) => picked.length === 1
    ? { ok: true, message: `${picked[0]} will replace ${picker?.replace}; data resilvers online` }
    : { ok: false, message: "Select exactly one replacement disk" };
  const attaching = picker && typeof picker === "object" && picker.attach;
  const replacing = picker && typeof picker === "object" && picker.replace;
  const expanding = picker && typeof picker === "object" && picker.expand;
  const validateSpecial = (picked) => picked.length === 2 || picked.length === 3
    ? { ok: true, message: `Add one group of ${picked.length}-way mirrored metadata disks and migrate metadata/small blocks` }
    : { ok: false, message: `Metadata disks must be mirrored. Select 2 or 3 disks (selected ${picked.length} disks); losing it loses the entire pool` };
  const validateRaidzExpand = (picked) => picked.length === 1
    ? { ok: true, message: `${picked[0]} will join ${picker?.expand}; raidz rebalance can take hours or days on HDDs while online` }
    : { ok: false, message: "raidz expansion supports one new disk at a time" };

  const addFor = async (paths) => {
    const kind = picker;
    setPicker(null);
    if (attaching) await run(() => api.addPoolDisk(pool.ID, { disks: paths, mode: "attach", target: kind.attach }), `给 ${kind.attach} RAID1 attachment task submitted`);
    else if (expanding) await run(() => api.addPoolDisk(pool.ID, { disks: paths, mode: "attach", target: kind.expand }), `给 ${kind.expand} expansion task submitted`);
    else if (replacing) await run(() => api.replacePoolDisk(pool.ID, { old_disk: kind.replace, new_disk: paths[0] }), `用 ${paths[0]} replace ${kind.replace} task submitted`);
    else if (kind === "special") await run(() => api.addSpecial(pool.ID, { disks: paths }), "Metadata disk addition submitted");
    else if (kind === "spare") await run(() => api.addSpare(pool.ID, { disks: paths }), "Hot spare addition submitted");
    else if (kind === "data") await run(() => api.addPoolDisk(pool.ID, { disks: paths }), "Storage disk addition submitted");
    else if (kind === "read") await run(() => api.addReadCache(pool.ID, { disks: paths }), "Read cache addition submitted");
    else if (kind === "write") await run(() => api.addWriteCache(pool.ID, { disks: paths }), "Write cache addition submitted");
  };

  // Mirror members use detach without data migration;
  // stripe members and full groups use remove after data migration.
  const removeDataDisk = async (path) => {
    const group = dataGroups.find(g => g.Kind === "mirror" && (g.Disks || []).some(d => d.Path === path));
    if (group) {
      const left = (group.Disks || []).length - 1;
      const ok = await conf.ask({
        title: "Detach RAID1 Disk",
        message: `Detach ${path}？${group.Name} will have only ${left}-way${left === 1 ? " with no redundancy" : ""}. Detach is immediate, without copying data.`,
        danger: true, confirmText: "Detach",
      });
      if (ok) await run(() => api.removePoolDisk(pool.ID, { disk: path }), "RAID1 detach task submitted");
      return;
    }
    const ok = await conf.ask({ title: "Remove磁盘", message: `Remove ${pool.Name} from ${path}? Data will be migrated to surviving disks.`, danger: true });
    if (ok) await run(() => api.removePoolDisk(pool.ID, { disk: path }), "Disk removal task submitted");
  };
  // Require a free disk for every data member before RAID1 upgrade; partial mirroring remains vulnerable.
  const upgradePairs = dataDisks.map(d => ({ target: d.Path, disk: (upgrade || {})[d.Path] || "" }));
  const upgradePlan = (() => {
    if (!upgrade) return { ok: false, message: "" };
    const missing = upgradePairs.filter(p => !p.disk).length;
    if (freeDisks.length < dataDisks.length) return { ok: false, message: `Not enough free disks: need ${dataDisks.length} disks; only ${freeDisks.length} disks` };
    if (missing) return { ok: false, message: `Still ${missing} data disks have no RAID1 partner` };
    return { ok: true, message: `Create ${upgradePairs.length} RAID1 groups; pool becomes mirrored on completion` };
  })();
  const doUpgrade = async () => {
    if (!upgradePlan.ok) return;
    const ok = await run(() => api.mirrorUpgradePool(pool.ID, { pairs: upgradePairs }), `RAID1 upgrade submitted (${upgradePairs.length} pairs)`);
    if (ok) setUpgrade(null);
  };
  const removeSpecialGroup = async (group) => {
    const ok = await conf.ask({
      title: "Remove Metadata Disks",
      message: `Remove metadata group ${group.Name} (${(group.Disks || []).map(d => d.Path).join(", ")})? Metadata will migrate back to data disks while online.`,
      danger: true, confirmText: "Remove",
    });
    if (ok) await run(() => api.removeSpecial(pool.ID, { disk: group.Name }), `Remove ${group.Name} 任务已提交`);
  };
  const removeSpareDisk = async (path) => {
    const ok = await conf.ask({ title: "Remove Hot Spare", message: `Remove ${path} Remove ${pool.Name} hot-spare list? Failed disks will no longer be replaced automatically.`, danger: true, confirmText: "Remove" });
    if (ok) await run(() => api.removeSpare(pool.ID, { disk: path }), "Remove Hot Spare任务已提交");
  };
  const removeGroup = async (group) => {
    const ok = await conf.ask({
      title: "Remove Group",
      message: `Remove vdev group ${group.Name} (${(group.Disks || []).map(d => d.Path).join(", ")})? Data will migrate to other groups while online.`,
      danger: true, confirmText: "Remove Group",
    });
    if (ok) await run(() => api.removePoolDisk(pool.ID, { disk: group.Name }), `Remove ${group.Name} 任务已提交`);
  };
  const removeReadCache = async (path) => run(() => api.removeReadCache(pool.ID, { disk: path }), "Read cache removed");
  const removeWriteCache = async (path) => run(() => api.removeWriteCache(pool.ID, { disk: path }), "Write cache removed");
  const flushWrite = async () => run(() => api.flushWriteCache(pool.ID), "Write-cache flush task submitted");

  return (
    <Modal open onClose={onClose} size="xl"
      title={<span className="row" style={{ gap: 8 }}>
        <Icons.Database size={15} style={{ color: "var(--cyan)" }}/>
        Storage Pool Details · <span className="mono">{pool.Name}</span>
      </span>}
      /* Right-align footer action buttons; close through the title bar, as on other detail dialogs. */
      footer={<button className="btn danger" disabled={busy} onClick={onDestroy}><Icons.Trash size={12}/> {missing ? "Delete Record" : "Destroy Storage Pool"}</button>}>
        <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
          <KVGrid items={[
            ["Pool Name", pool.Name, true],
            ["Server", serverLabel || pool.ServerID || "—", true],
            ["Layout", layoutLabel(pool), false],
            // Show rebuild or migration progress inline with pool status when available.
            ["Status", <span className="row" style={{ gap: 6 }}><span className={`chip ${hm.cls}`}>{hm.dot && <span className={`dot ${hm.dot}`}/>}{hm.text}</span>{pool.Operation && <span className="mono meta">{OP_LABEL[pool.Operation] || pool.Operation} {pool.Progress}</span>}</span>, false],
          ]}/>

          {missing ? <div className="hint">
            Pool no longer exists on this node; only its record remains{(pool.Disks || []).length ? ` (原成员 ${pool.Disks.join(", ")})` : ""}.Deleting the record changes no disks. You may recreate a pool with the same name.
          </div> : <>
          <Section title="容量">
            <div className="row" style={{ gap: 12, marginBottom: 8 }}>
              <span className="mono" style={{ fontSize: 18, fontWeight: 600 }}>{fmtCap(used)}</span>
              <span className="mono" style={{ color: "var(--fg-faint)", fontSize: 13 }}>/ 共 {fmtCap(cap)} · Available {fmtCap(cap - used)} · Used {usePct}%</span>
            </div>
            <div className={`bar ${usePct > 85 ? "rose" : usePct > 70 ? "amber" : ""}`}><span style={{ width: `${usePct}%` }}/></div>
          </Section>

          <DiskSection title="Member Disks" disks={dataDisks} groups={dataGroups} busy={busy}
            onAdd={() => setPicker("data")} onRemove={removeDataDisk}
            removable={!parity && dataDisks.length > 1}
            note={parity
              ? "raidz devices cannot be removed. Use Replace Disk for a failed drive." + (pool.RaidzExpandable ? " This node supports single-disk raidz expansion" : pool.RaidzExpandNote ? "." + pool.RaidzExpandNote : "")
              : null}
            extraAction={layout === "stripe" && dataDisks.length > 1 // Equivalent to attaching RAID1 to a single-disk pool
              ? <button className="btn" disabled={busy} style={{ padding: "3px 10px" }} onClick={() => setUpgrade({})}><Icons.Shield size={11}/> Upgrade to RAID1</button>
              : null}
            groupAction={parity
              ? (pool.RaidzExpandable ? (g) => (
                <button className="btn ghost icon" disabled={busy} onClick={() => setPicker({ expand: g.Name })} title="Expand by One Disk" aria-label={`给 ${g.Name} Expand by One Disk`}><Icons.Plus size={12}/></button>
              ) : null)
              : dataGroups.filter(g => g.Kind === "mirror").length > 1 ? (g) => (
                <button className="btn ghost icon danger" disabled={busy} onClick={() => removeGroup(g)} title="Remove Group" aria-label={`Remove vdev group ${g.Name}`}><Icons.Trash size={12}/></button>
              ) : null}
            rowAction={(d) => (<>
              {/* Stripe members may become mirrors and mirrors can gain a member; raidz cannot attach directly. Replace applies to any layout.
                  按钮带文字, 只有图标时运维找不到「Replace Disk」. */}
              {!parity && <button className="btn ghost" disabled={busy} style={{ padding: "3px 8px" }} onClick={() => setPicker({ attach: d.Path })} title="给这 disks盘配一 disks盘group成 RAID1" aria-label={`给 ${d.Path} Attach RAID1 Disk`}><Icons.Plus size={11}/> Attach RAID1 Disk</button>}
              <button className="btn ghost" disabled={busy} style={{ padding: "3px 8px" }} onClick={() => setPicker({ replace: d.Path })} title="用一 disks空闲盘顶替这 disks盘" aria-label={`Replace ${d.Path}`}><Icons.Refresh size={11}/> Replace Disk</button>
            </>)}/>

          <DiskSection title="Metadata Disks special" groups={specialGroups} disks={[]} busy={busy}
            onAdd={() => setPicker("special")}
            groupAction={(g) => <button className="btn ghost icon danger" disabled={busy} onClick={() => removeSpecialGroup(g)} title="Remove Metadata Disksgroup" aria-label={`Remove metadata group ${g.Name}`}><Icons.Trash size={12}/></button>}
            note="Store metadata and small blocks on mirrored SSD/NVMe. HDD pools benefit most. Losing an unmirrored metadata device loses the pool."/>
          <DiskSection title="Hot Spares" disks={spareDisks} busy={busy}
            onAdd={() => setPicker("spare")} onRemove={removeSpareDisk} removable removeLabel="Remove Hot Spare"
            note="Automatically replaces failed disks (autoreplace enabled upon addition)."/>

          <DiskSection title="Read Cache L2ARC" disks={readCache} busy={busy}
            onAdd={() => setPicker("read")} onRemove={removeReadCache} removable
            note="Add L2ARC if hot data exceeds RAM, mainly on HDD pools with low memory; it is unnecessary on all-SSD pools and failures do not lose data."/>

          <DiskSection title="Write Cache ZIL" disks={writeCache} busy={busy}
            onAdd={() => setPicker("write")} onRemove={removeWriteCache} removable
            note="Accelerates synchronous writes only. Usually unnecessary for client writes; if needed, mirror enterprise SSDs with power-loss protection."
            extraAction={writeCache.length ? <button className="btn" disabled={busy} style={{ padding: "3px 10px" }} onClick={flushWrite}><Icons.Refresh size={11}/> Flush Write Cache</button> : null}/>
          </>}
        </div>

        {/* Upgrade stripes to RAID1 by pairing each data disk with a free disk. Runs online, copying used blocks in the background. */}
        <Modal open={!!upgrade} onClose={() => !busy && setUpgrade(null)} title={`Upgrade to RAID1 · ${pool.Name}`} size="md"
          footer={<><button className="btn" disabled={busy} onClick={() => setUpgrade(null)}>Cancel</button>
            <button className="btn primary" disabled={busy || !upgradePlan.ok} onClick={doUpgrade}>{busy ? "Submitting…" : "Start Upgrade"}</button></>}>
          <div className="hint" style={{ marginBottom: 10 }}>Pair each data disk with an equally large or larger free disk to create RAID1 mirrors. Upgrade runs online; redundancy applies as each pair completes.</div>
          <div style={{ background: "var(--bg-0)", border: "1px solid var(--line-soft)", borderRadius: 6, overflow: "hidden" }}>
            <table className="t" style={{ margin: 0 }}>
              <thead><tr><th>Storage Disks</th><th>RAID1 盘</th></tr></thead>
              <tbody>
                {dataDisks.map(d => {
                  const takenElsewhere = Object.entries(upgrade || {}).filter(([k]) => k !== d.Path).map(([, v]) => v);
                  const options = freeDisks.filter(f => !takenElsewhere.includes(f.Path)).map(f => ({ value: f.Path, label: `${f.Path} · ${fmtCap(f.Size)}` }));
                  return (
                    <tr key={d.Path}>
                      <td className="mono" style={{ fontWeight: 600 }}>{d.Path}</td>
                      <td>
                        <Select value={(upgrade || {})[d.Path] || ""} onChange={v => setUpgrade(prev => ({ ...(prev || {}), [d.Path]: v }))}
                          aria-label={`${d.Path} 的 RAID1 盘`}
                          options={[{ value: "", label: "Select Free Disk…" }, ...options]}/>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {upgradePlan.message && <div className="hint" style={{ marginTop: 6, color: upgradePlan.ok ? "var(--fg-mute)" : "var(--amber)" }}>{upgradePlan.message}</div>}
        </Modal>

        <DiskPickModal open={!!picker} busy={busy} freeDisks={freeDisks}
          title={attaching ? `给 ${picker.attach} Attach RAID1 Disk` : expanding ? `给 ${picker.expand} Expand by One Disk` : replacing ? `Replace ${picker.replace}` : picker === "data" ? "Add Storage Disk" : picker === "special" ? "Add Metadata Disk" : picker === "spare" ? "Add Hot Spare" : picker === "read" ? "Add Read Cache Disk" : "Add Write Cache Disk"}
          hint={picker === "data" ? (layout === "mirror" ? `Mirrored pool requires ${width} disks一group加盘` : parity ? `${layout} 池按adding complete groups, Per group ${width} disks` : null) : replacing ? "Select a free disk at least as large as the original." : picker === "special" ? "Select 2 or 3 SSD/NVMe drives to form a mirror." : null}
          validate={attaching ? validateAttach : expanding ? validateRaidzExpand : replacing ? validateReplace : picker === "data" ? validateExpand : picker === "special" ? validateSpecial : null}
          onClose={() => setPicker(null)} onConfirm={addFor}/>
        {conf.node}
    </Modal>
  );
}

// DiskSection renders member devices; grouped vdev trees show mirror/raidz headers
// to associate degraded groups with failed members. Keep single-disk stripes flat.
// readOnly sections do not offer add/remove actions.
function DiskSection({ title, disks, groups, onAdd, onRemove, removable, busy, extraAction, readOnly, rowAction, groupAction, note, removeLabel = "Remove" }) {
  const grouped = (groups || []).some(g => g.Kind !== "disk");
  const rows = grouped
    ? groups.flatMap(g => g.Kind === "disk"
      ? (g.Disks || []).map(d => ({ type: "disk", d }))
      : [{ type: "group", g }, ...(g.Disks || []).map(d => ({ type: "disk", d, inGroup: true }))])
    : disks.map(d => ({ type: "disk", d }));
  const memberRow = (d, inGroup) => {
    const sm = healthMeta(d.Status);
    return (
      <tr key={d.Path}>
        <td className="mono" style={{ fontWeight: 600, paddingLeft: inGroup ? 28 : undefined }}>{d.Path}</td>
        <td className="muted meta">{ROLE_LABEL[d.Role] || d.Role}</td>
        <td>{d.Status ? <span className={`chip ${sm.cls}`}>{sm.text}</span> : <span className="muted meta">—</span>}</td>
        <td className="t-actions"><div>
          {rowAction && !readOnly && rowAction(d)}
          {removable && !readOnly && <button className="btn ghost icon danger" disabled={busy} onClick={() => onRemove(d.Path)} title={removeLabel} aria-label={`${removeLabel} ${d.Path}`}><Icons.Trash size={12}/></button>}
        </div></td>
      </tr>
    );
  };
  return (
    <Section title={title} actions={readOnly ? null : <div className="row" style={{ gap: 6 }}>
      {extraAction}
      <button className="btn" disabled={busy} style={{ padding: "3px 10px" }} onClick={onAdd}><Icons.Plus size={11}/> Add</button>
    </div>}>
      <div style={{ background: "var(--bg-0)", border: "1px solid var(--line-soft)", borderRadius: 6, overflow: "hidden" }}>
        {rows.length ? (
          <table className="t" style={{ margin: 0 }}>
            <thead><tr><th>Device</th><th>Role</th><th>Status</th><th className="t-actions"></th></tr></thead>
            <tbody>
              {rows.map(r => {
                if (r.type === "disk") return memberRow(r.d, r.inGroup);
                const gm = healthMeta(r.g.Status);
                return (
                  <tr key={r.g.Name} style={{ background: "var(--bg-1)" }}>
                    <td className="mono" style={{ fontWeight: 600 }}><span>{r.g.Name}</span> <span className="muted meta">· {LAYOUTS[r.g.Kind]?.label || r.g.Kind} · {(r.g.Disks || []).length} disks</span></td>
                    <td className="muted meta">{ROLE_LABEL[r.g.Role] || r.g.Role}</td>
                    <td>{r.g.Status ? <span className={`chip ${gm.cls}`}>{gm.text}</span> : <span className="muted meta">—</span>}</td>
                    <td className="t-actions"><div>{groupAction && !readOnly && groupAction(r.g)}</div></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        ) : (
          <div className="hint" style={{ padding: 12, textAlign: "center" }}>None</div>
        )}
      </div>
      {note && <div className="hint" style={{ marginTop: 6 }}>{note}</div>}
    </Section>
  );
}

function Section({ title, children, actions }) {
  return (
    <div>
      <div className="row" style={{ marginBottom: 8 }}>
        <div className="lbl">{title}</div>
        <div style={{ flex: 1 }}/>
        {actions}
      </div>
      {children}
    </div>
  );
}

function KVGrid({ items }) {
  return (
    <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(160px, 1fr))", gap: 1, background: "var(--line-soft)", border: "1px solid var(--line-soft)", borderRadius: 6, overflow: "hidden" }}>
      {items.map(([k, v, mono], i) => (
        <div key={i} style={{ background: "var(--bg-1)", padding: "10px 12px" }}>
          <div className="meta" style={{ marginBottom: 3 }}>{k}</div>
          <div className={mono ? "mono" : ""} style={{ fontSize: 13, fontWeight: 500 }}>{v}</div>
        </div>
      ))}
    </div>
  );
}

window.PageStorage = PageStorage;

export { PageStorage };
