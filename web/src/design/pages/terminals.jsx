// Backend exposes online/offline only; treat legacy unknown as offline. Do not invent power or load data.
import React from 'react';
import { Icons } from '../icons.jsx';
import { useStore } from '../store.jsx';
import { Modal, useConfirm } from '../overlay.jsx';
import { Field, Select, TableState, SearchBox, SegFilter, Pager } from '../primitives.jsx';
import { api } from '../../lib/api.js';
import { redName, fmtCap } from '../../lib/format.js';
import { useResource, useMutation, usePolling, usePaged } from '../../lib/hooks.js';
const { useState, useEffect, useRef } = React;

const STATE = {
  online:  { t: "Online", c: "emerald", dot: "live" },
  offline: { t: "Offline", c: "",        dot: "idle" },
};
// Only online and offline states; never-connected clients are offline.
const stateOf = (t) => (t?.State === "online" ? "online" : "offline");
const terminalName = (t) => String(t?.Name || t?.name || "").trim();
const terminalTitle = (t) => terminalName(t) || t?.ID || "—";
const terminalForm = (t) => ({
  id: t.ID,
  name: terminalName(t),
  // Display colon-separated MACs; backend normalizes on save.
  mac: fmtMac(t.MAC),
  ip: t.IP,
  group_id: t.GroupID,
  is_super: t.IsSuper,
  state: t.State || "offline",
});
// Compute group IP ranges as [StartIP, StartIP + ClientMax - 1] using uint32 arithmetic, matching backend logic.
// Carry across octets rather than adding to the last one only.
const ipToInt = (value) => {
  const parts = String(value || "").trim().split(".");
  if (parts.length !== 4) return null;
  let n = 0;
  for (const part of parts) {
    if (!/^\d{1,3}$/.test(part)) return null;
    const v = Number(part);
    if (v > 255) return null;
    n = n * 256 + v;
  }
  return n;
};
const intToIp = (n) => [n >>> 24, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join(".");
const ipRangeEnd = (startIP, max) => {
  const start = ipToInt(startIP);
  if (start === null || !max) return startIP || "—";
  return intToIp(start + Math.max(0, max - 1));
};
// Do not warn on partially typed IPs; server-side validation is authoritative.
const ipInGroupPool = (ip, group) => {
  const n = ipToInt(ip);
  const start = ipToInt(group && group.StartIP);
  if (n === null || start === null || !(group && group.ClientMax)) return true;
  return n >= start && n <= start + group.ClientMax - 1;
};
// Backend stores uppercase MACs without separators; format for display, normalize for search.
const normMac = (s) => String(s || "").replace(/[:\-.\s]/g, "").toUpperCase();
const fmtMac = (s) => normMac(s).match(/.{1,2}/g)?.join(":") || (s || "");
const pad2 = (n) => String(n).padStart(2, "0");
const fmtTime = (iso) => {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d)) return "—";
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
};

async function downloadRaw(res, filename) {
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url; a.download = filename;
  document.body.appendChild(a); a.click(); a.remove();
  URL.revokeObjectURL(url);
}

function PageTerminals() {
  const store = useStore();
  const conf = useConfirm();
  const { route, goto } = store;

  const [view, setView] = useState("table");
  const [groupFilter, setGroupFilter] = useState(route.params.groupId || "all");
  const [stateFilter, setStateFilter] = useState("all");
  const [search, setSearch] = useState("");
  const [selectedIds, setSelectedIds] = useState([]);
  const [detailId, setDetailId] = useState(null);
  const [editId, setEditId] = useState(null);
  const [modal, setModal] = useState(null); // add | bulk-group | stopSuper | injectDriver
  const [form, setForm] = useState({});
  const fileRef = useRef(null);

  // Settle queries separately; failure to fetch groups or images must not clear the client list.
  const { data, loading, error, reload: load } = useResource(async () => {
    const [tr, gr, ir, br, nr] = await Promise.allSettled([api.listTerminals(), api.listGroups(), api.listImages(), api.listDriverBundles(), api.listClusterNodes()]);
    if (tr.status === "rejected") throw tr.reason;
    return {
      terminals: (tr.value && tr.value.items) || [],
      groups: gr.status === "fulfilled" ? (gr.value && gr.value.items) || [] : [],
      images: ir.status === "fulfilled" ? (ir.value && ir.value.items) || [] : [],
      bundles: br.status === "fulfilled" ? (br.value && br.value.items) || [] : [],
      nodes: nr.status === "fulfilled" ? (nr.value && nr.value.items) || [] : [],
    };
  }, []);
  const terminals = (data && data.terminals) || [];
  const groups = (data && data.groups) || [];
  const images = (data && data.images) || [];
  const bundles = (data && data.bundles) || [];
  const nodes = (data && data.nodes) || [];
  // Show placement node in multi-node clusters so operators can locate client disks.
  const clustered = nodes.length > 1;
  const nodeLabel = (t) => {
    const id = t.StorageServerID;
    // Older clients may lack placement metadata; never imply the VIP host is their storage node.
    if (!id) return "Not Recorded";
    return (nodes.find(n => n.id === id) || {}).ip || id;
  };
  const { busy, run } = useMutation(store.toast);

  // Server derives online state from iSCSI sessions, requiring polling; key detail/editor by client ID and mode.
  // Background refresh must not discard unsaved edits.
  usePolling(load, { intervalMs: 15000 });

  useEffect(() => { if (route.params.groupId) setGroupFilter(route.params.groupId); }, [route.params.groupId]);

  const groupById = Object.fromEntries(groups.map(g => [g.ID, g]));
  const imageById = Object.fromEntries(images.map(i => [i.ID, i]));
  const bundleNameById = Object.fromEntries(bundles.map(b => [b.bundle.ID, b.bundle.Name]));
  const imageNameOf = (groupId) => {
    const g = groupById[groupId];
    return (g && imageById[g.SystemImageID] && imageById[g.SystemImageID].Name) || "—";
  };

  const filtered = terminals.filter(t =>
    (groupFilter === "all" || t.GroupID === groupFilter) &&
    (stateFilter === "all" || stateOf(t) === stateFilter) &&
    (!search ||
      terminalTitle(t).toLowerCase().includes(search.toLowerCase()) ||
      t.ID.toLowerCase().includes(search.toLowerCase()) ||
      normMac(t.MAC).includes(normMac(search)) ||
      (t.IP || "").includes(search))
  );
  const stateCount = terminals.reduce((m, t) => ({ ...m, [stateOf(t)]: (m[stateOf(t)] || 0) + 1 }), {});
  const detail = terminals.find(t => t.ID === detailId) || null;
  const editing = terminals.find(t => t.ID === editId) || null;

  // Select All affects the current page only.
  const paged = usePaged(filtered);
  const visible = view === "table" ? paged.rows : filtered;

  const toggleSel = (id) => setSelectedIds(prev => prev.includes(id) ? prev.filter(x => x !== id) : [...prev, id]);
  const selectAll = () => setSelectedIds(visible.map(t => t.ID));
  const clearSel = () => setSelectedIds([]);
  // Clear selection after filter, pagination, page size, or view changes to avoid acting on hidden rows.
  useEffect(() => { setSelectedIds([]); }, [groupFilter, stateFilter, search, paged.page, paged.size, view]);

  // Mutation actions ───────────────────────────────────────────────────────
  const openDetail = (t) => setDetailId(t.ID);
  const openEdit = (t) => setEditId(t.ID);
  const openAdd = () => { setForm({ name: "", mac: "", ip: "", group_id: groups[0]?.ID || "" }); setModal("add"); };
  const saveAdd = async () => {
    if (!form.mac?.trim()) { store.toast("Enter a MAC address", "err"); return; }
    if (!form.group_id) { store.toast("Select a group", "err"); return; }
    const ok = await run(() => api.createTerminal({
      name: form.name?.trim() || "",
      mac: form.mac.trim(),
      ip: form.ip?.trim() || "",
      group_id: form.group_id,
      is_super: false,
      state: "offline",
    }), "Client added", { reload: load, errMsg: "Save failed" });
    if (ok) setModal(null);
  };
  // Disabling Super Admin immediately discards its writable clone; button and editor share this action.
  const confirmDropSuper = (t) => conf.ask({ title: "Disable Super Admin", danger: true, confirmText: "Discard Changes",
    message: `Disabling Super Admin discards all unsaved changes on ${terminalTitle(t)} permanently. To preserve them, use Shut Down and Save Restore Point.` });
  const saveEdit = async (next) => {
    if (!next.mac?.trim()) { store.toast("Enter a MAC address", "err"); return; }
    if (!next.group_id) { store.toast("Select a group", "err"); return; }
    const before = terminals.find(x => x.ID === next.id);
    if (before?.IsSuper && !next.is_super && !(await confirmDropSuper(before))) return;
    const ok = await run(() => api.updateTerminal(next.id, {
      name: next.name?.trim() || "",
      mac: next.mac.trim(),
      ip: next.ip?.trim() || "",
      group_id: next.group_id,
      is_super: !!next.is_super,
      state: next.state || "offline",
    }), "Client updated", { reload: load, errMsg: "Save failed" });
    if (ok) setEditId(null);
  };

  const deleteOne = async (t) => {
    const ok = await conf.ask({ title: "Delete Client", message: `Delete ${terminalTitle(t)} (${fmtMac(t.MAC)}) and its client clone?`, danger: true });
    if (!ok) return;
    if (await run(() => api.deleteTerminal(t.ID), "Client deleted", { reload: load, errMsg: "Delete failed" })) {
      if (detailId === t.ID) setDetailId(null);
      if (editId === t.ID) setEditId(null);
    }
  };
  const bulkDelete = async () => {
    const ok = await conf.ask({ title: "Delete Selected Clients", message: `Delete ${selectedIds.length} clients? This cannot be undone.`, danger: true, confirmText: "Delete" });
    if (!ok) return;
    const errs = [];
    for (const id of selectedIds) { try { await api.deleteTerminal(id); } catch (e) { errs.push(id); } }
    store.toast(errs.length ? `Finished; ${errs.length} client deletions failed` : `Deleted ${selectedIds.length} clients`, errs.length ? "err" : "ok");
    clearSel(); await load();
  };
  const applyBulkGroup = async () => {
    if (!form.group_id) { store.toast("Select a target group", "err"); return; }
    if (await run(() => api.moveTerminals({ terminal_ids: selectedIds, group_id: form.group_id }),
      `Moved ${selectedIds.length} clients`, { reload: load, errMsg: "Move failed" })) {
      setModal(null); clearSel();
    }
  };

  const setSuper = async (t, enable) => {
    if (!enable && !(await confirmDropSuper(t))) return;
    return run(() => (enable ? api.enableSuper(t.ID) : api.disableSuper(t.ID)),
      enable ? "Super Admin enabled" : "Super Admin disabled", { reload: load });
  };
  // Publishing a data disk needs only a restore-point name; explain side effects in its dialog.
  const openPublish = (t, disk) => {
    setForm({ id: t.ID, name: t.Name || t.MAC, disk_id: disk.disk_id, mount: disk.mount_target, written: disk.written, publish_name: "" });
    setModal("publish"); setDetailId(null);
  };
  const doPublish = async () => {
    if (!form.publish_name?.trim()) { store.toast("Enter a restore-point name", "err"); return; }
    if (await run(() => api.publishDataDisk(form.id, { disk_id: form.disk_id, name: form.publish_name.trim() }),
      "Publish task submitted; takes effect on the next client boot.", { reload: load, errMsg: "Publish failed" })) setModal(null);
  };
  const openStopSuper = (t) => {
    // Save the system disk by default; explicitly select named group data disks.
    setForm({ id: t.ID, name: t.Name || t.MAC, save_system: true, reduction_name: "", disks: [], saves: {} });
    setModal("stopSuper"); setDetailId(null);
    api.listGroupDisks(t.GroupID)
      .then(r => setForm(f => (f.id === t.ID ? { ...f, disks: (r && r.items) || [] } : f)))
      .catch(() => {});
  };
  const doStopSuper = async () => {
    // Check current online status before submission to avoid a two-minute server timeout.
    // Client state may change while operators enter a name.
    const live = terminals.find(t => t.ID === form.id);
    if (live && live.State === "online") {
      store.toast(`${form.name || "This Super Admin client"} is running. Shut it down before saving.`, "err");
      return;
    }
    const saveSystem = form.save_system !== false;
    const systemName = (form.reduction_name || "").trim();
    if (saveSystem && !systemName) { store.toast("Enter a system-disk restore-point name", "err"); return; }
    const dataDisks = [];
    for (const d of form.disks || []) {
      const save = form.saves?.[d.ID];
      if (!save?.on) continue;
      const name = (save.name || "").trim();
      if (!name) { store.toast(`Enter a name for data disk ${d.MountTarget} restore point`, "err"); return; }
      dataDisks.push({ disk_id: d.ID, reduction_name: name });
    }
    if (!saveSystem && !dataDisks.length) { store.toast("Select at least one disk to save, or disable Super Admin to discard changes.", "err"); return; }
    const body = { reduction_name: saveSystem ? systemName : "" };
    if (dataDisks.length) body.data_disks = dataDisks;
    if (await run(() => api.stopSuper(form.id, body), "Save restore-point task submitted", { reload: load })) setModal(null);
  };
  const setDiskSave = (id, patch) => setForm(f => ({ ...f, saves: { ...(f.saves || {}), [id]: { ...((f.saves || {})[id] || {}), ...patch } } }));

  const doExport = async () => {
    try { await downloadRaw(await api.exportTerminals(groupFilter === "all" ? "" : groupFilter), "terminals.xlsx"); }
    catch (e) { store.toast(e.message || "Export failed", "err"); }
  };
  const doTemplate = async () => {
    try { await downloadRaw(await api.terminalTemplate(), "terminals-template.xlsx"); }
    catch (e) { store.toast(e.message || "Download failed", "err"); }
  };
  const doImport = async (file) => {
    const fd = new FormData(); fd.append("file", file);
    try {
      const r = await api.importTerminals(fd);
      const errN = (r.errors || []).length;
      store.toast(`Import complete · Added ${r.created || 0}${errN ? ` · ${errN} failed rows` : ""}`, errN ? "err" : "ok");
      await load();
    } catch (e) { store.toast(e.message || "Import failed", "err"); }
  };

  // Keep counts in filters rather than duplicate interactive-looking badges.
  const stateOptions = [
    { id: "all", label: "All", n: terminals.length },
    { id: "online", label: "Online", n: stateCount.online || 0 },
    { id: "offline", label: "Offline", n: stateCount.offline || 0 },
  ];
  const filterActive = filtered.length !== terminals.length;

  return (
    <div className="fade-in">
      {selectedIds.length > 0 && (
        <div className="card" style={{ padding: 10, marginBottom: 14, display: "flex", alignItems: "center", gap: 10, background: "var(--cyan-soft)", borderColor: "oklch(0.55 0.1 200 / 0.4)" }}>
          <span className="chip cyan">Selected {selectedIds.length}</span>
          <button className="btn" onClick={() => { setForm({ group_id: groups[0]?.ID || "" }); setModal("bulk-group"); }}>Change Group</button>
          <button className="btn danger" onClick={bulkDelete}><Icons.Trash size={12}/> Delete</button>
          <button className="btn ghost" onClick={clearSel} style={{ marginLeft: "auto" }}>Clear Selection</button>
        </div>
      )}

      <div className="card" style={{ padding: 12, marginBottom: 14, display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
          <SegFilter value={stateFilter} onChange={setStateFilter} options={stateOptions}/>
          <select className="input" value={groupFilter} onChange={e => setGroupFilter(e.target.value)} style={{ width: 180 }}>
            <option value="all">All Groups</option>
            {groups.map(g => <option key={g.ID} value={g.ID}>{g.Name}</option>)}
          </select>
          <SearchBox value={search} onChange={setSearch} placeholder="Search name / MAC / IP" width={260}/>
          {/* Show filtered count only when filters are active. */}
          {filterActive && <span className="chip cyan">Filtered {filtered.length} / {terminals.length}</span>}
          <div style={{ marginLeft: "auto" }} className="seg">
            <button className={view === "grid" ? "on" : ""} onClick={() => setView("grid")}>Grid</button>
            <button className={view === "table" ? "on" : ""} onClick={() => setView("table")}>List</button>
          </div>
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap", borderTop: "1px solid var(--line-soft)", paddingTop: 10 }}>
          {/* Automatic polling occurs every 15 seconds; manual refresh uses an icon only. */}
          <button className="btn ghost icon" onClick={() => load()} title="Refresh Now" aria-label="Refresh Now"><Icons.Refresh size={13}/></button>
          <span style={{ fontSize: 12, color: "var(--fg-faint)" }}>Online state comes from iSCSI sessions · Refreshes every 15 seconds</span>
          <div style={{ flex: 1 }}/>
          <input ref={fileRef} type="file" accept=".xlsx" style={{ display: "none" }} onChange={e => { const f = e.target.files?.[0]; if (f) doImport(f); e.target.value = ""; }}/>
          <div className="btn-group">
            <button className="btn" onClick={() => fileRef.current?.click()}><Icons.ArrowDn size={12}/> Import</button>
            <button className="btn" onClick={doExport}><Icons.ArrowUp size={12}/> Export</button>
            <button className="btn" onClick={doTemplate} title="Download XLSX import template">Template</button>
          </div>
          <button className="btn primary" onClick={openAdd}><Icons.Plus size={12}/> Add Client</button>
        </div>
      </div>

      {view === "grid" ? (
        <div className="card" style={{ padding: 16 }}>
            {groups.filter(g => groupFilter === "all" || g.ID === groupFilter).map(g => {
              const cells = filtered.filter(t => t.GroupID === g.ID);
              if (cells.length === 0) return null;
              return (
                <div key={g.ID} style={{ marginBottom: 22 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 10 }}>
                    {/* Preserve group names exactly as entered. */}
                    <span className="row" style={{ fontSize: 13, color: "var(--fg-mute)", fontWeight: 600, cursor: "pointer", gap: 2 }} onClick={() => goto("groups", { groupId: g.ID })} title="View Group">{g.Name}<Icons.Chevron size={11}/></span>
                    <span className="chip">{cells.filter(c => c.State === "online").length} / {cells.length} Online</span>
                    <div style={{ flex: 1, height: 1, background: "var(--line-soft)" }}/>
                  </div>
                  <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(150px, 1fr))", gap: 8 }}>
                    {cells.map(t => {
                      const st = STATE[stateOf(t)];
                      return (
                        <div key={t.ID} className={`tmcell s-${t.State}`}
                             onClick={(e) => { if (e.shiftKey) toggleSel(t.ID); else openDetail(t); }}
                             style={{ borderColor: detailId === t.ID ? "var(--cyan)" : selectedIds.includes(t.ID) ? "var(--violet)" : undefined,
                                      boxShadow: (detailId === t.ID || selectedIds.includes(t.ID)) ? `0 0 0 1px ${selectedIds.includes(t.ID) ? "var(--violet)" : "var(--cyan)"}` : undefined }}>
                          <div className="tmh">
                            <span className={`dot ${st.dot}`}/>
                            <span className="tmname" title={terminalTitle(t)}>{terminalTitle(t)}</span>
                            {t.IsSuper && <span className="chip violet" style={{ padding: "0 6px" }} title="Super Admin">SU</span>}
                            {t.IsSuper && t.PendingBundleID && <span className="chip amber" style={{ padding: "0 5px" }} title={`Injected ${bundleNameById[t.PendingBundleID] || t.PendingBundleID}; not yet committed`}><Icons.Clock size={10}/></span>}
                          </div>
                          <div className="tmip">{t.IP || "—"}</div>
                          <div className="tmfoot">
                            <span>{fmtMac(t.MAC).split(":").slice(-3).join(":")}</span>
                            <span className="tmstate">{st.t}</span>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                </div>
              );
            })}
            {!filtered.length && <div style={{ fontSize: 13, color: "var(--fg-faint)", textAlign: "center", padding: 24 }}>{loading ? "Loading…" : error ? `Load failed: ${error}` : "No clients"}</div>}
            <div className="hint" style={{ marginTop: 8 }}>Tip: Shift-click to multi-select · Click for details</div>
        </div>
      ) : (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          <table className="t">
            <thead><tr>
              <th style={{ width: 36 }}><input type="checkbox" checked={visible.length > 0 && visible.every(t => selectedIds.includes(t.ID))} onChange={e => e.target.checked ? selectAll() : clearSel()}/></th>
              <th>Client</th><th>MAC</th><th>IP</th><th>Group</th><th>System Image</th>{clustered && <th>Node</th>}<th>Status</th><th>Last Heartbeat</th><th className="t-actions"></th>
            </tr></thead>
            <tbody>
              {visible.map(t => {
                const st = STATE[stateOf(t)];
                return (
                  <tr key={t.ID}>
                    <td><input type="checkbox" checked={selectedIds.includes(t.ID)} onChange={() => toggleSel(t.ID)} onClick={e => e.stopPropagation()}/></td>
                    {/* Do not show t.ID; it is just terminal- plus the adjacent MAC. */}
                    <td onClick={() => openDetail(t)} style={{ cursor: "pointer" }} title="View Details"><div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                      <span style={{ fontWeight: 600 }}>{terminalTitle(t)}</span>
                      {t.IsSuper && <span className="chip violet" style={{ padding: "0 6px" }}>Super Admin</span>}
                      {t.IsSuper && t.PendingBundleID && <span className="chip amber" style={{ padding: "0 6px" }} title={`Injected ${bundleNameById[t.PendingBundleID] || t.PendingBundleID}; not yet committed`}>Drivers Awaiting Commit</span>}
                    </div></td>
                    <td className="mono muted" style={{ fontSize: 12 }}>{fmtMac(t.MAC)}</td>
                    <td className="mono">{t.IP || "—"}</td>
                    <td className="muted" style={{ cursor: "pointer" }} onClick={() => goto("groups", { groupId: t.GroupID })} title="Go to Group">{groupById[t.GroupID]?.Name || "—"}</td>
                    <td className="muted">{imageNameOf(t.GroupID)}</td>
                    {clustered && <td className="mono muted" style={{ fontSize: 12 }}>{nodeLabel(t)}</td>}
                    {/* Status dot appears only inside the status badge. */}
                    <td><span className={`chip ${st.c}`}><span className={`dot ${st.dot}`}/>{st.t}</span></td>
                    <td className="mono muted" style={{ fontSize: 12 }}>{fmtTime(t.LastHeartbeatAt)}</td>
                    <td className="t-actions"><div>
                      <button className="btn" style={{ padding: "3px 8px" }} onClick={() => openDetail(t)}>Details</button>
                      <button className="btn" style={{ padding: "3px 8px" }} onClick={() => openEdit(t)}>Edit</button>
                      <button className="btn ghost icon danger" onClick={() => deleteOne(t)} title="Delete Client" aria-label={`Delete Client ${terminalTitle(t)}`}><Icons.Trash size={12}/></button>
                    </div></td>
                  </tr>
                );
              })}
              <TableState colSpan={clustered ? 10 : 9} loading={loading && !terminals.length} error={error}
                empty={!visible.length}
                hint={terminals.length ? "No matching clients. Adjust filters or search." : "No clients. Select Add Client to register a MAC address."}/>
            </tbody>
          </table>
          <Pager {...paged.pager}/>
        </div>
      )}

      {detail && <TerminalDetail t={detail} group={groupById[detail.GroupID]} images={images} bundleNameById={bundleNameById}
        imageName={imageNameOf(detail.GroupID)} onClose={() => setDetailId(null)} onDelete={() => deleteOne(detail)}
        onSuper={(en) => setSuper(detail, en)} onStopSuper={() => openStopSuper(detail)} onPublish={(d) => openPublish(detail, d)}
        gotoDrivers={() => goto("drivers", { tab: "cure" })}/>}

      {editing && <TerminalEdit t={editing} groups={groups} busy={busy} onClose={() => setEditId(null)} onSave={saveEdit}/>}

      {/* Add / Edit */}
      <Modal open={modal === "add"} onClose={() => !busy && setModal(null)}
        title="Add Client"
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={saveAdd}>{busy ? "Saving…" : "Save"}</button></>}>
        <Field label="Client Name"><input className="input" style={{ width: "100%" }} value={form.name || ""} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="e.g. FrontDesk-01"/></Field>
        <Field label="MAC Address" required><input className="input mono" style={{ width: "100%" }} value={form.mac || ""} onChange={e => setForm({ ...form, mac: e.target.value })} placeholder="52:54:00:..."/></Field>
        <Field label="IP Address" hint="Leave blank to assign automatically by DHCP within the group subnet"><input className="input mono" style={{ width: "100%" }} value={form.ip || ""} onChange={e => setForm({ ...form, ip: e.target.value })} placeholder="192.168.10.x"/></Field>
        <Field label="Group" required>
          <Select value={form.group_id || ""} onChange={v => setForm({ ...form, group_id: v })} options={groups.length ? groups.map(g => ({ value: g.ID, label: g.Name })) : [{ value: "", label: "No groups available" }]}/>
        </Field>
      </Modal>

      {/* Bulk group reassignment */}
      <Modal open={modal === "bulk-group"} onClose={() => !busy && setModal(null)} title={`Move ${selectedIds.length} clients to Group`}
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={applyBulkGroup}>{busy ? "Moving…" : "Apply"}</button></>}>
        <Field label="Target Group" required hint="Network and image changes for the new group apply at next boot">
          <Select value={form.group_id || ""} onChange={v => setForm({ ...form, group_id: v })} options={groups.map(g => ({ value: g.ID, label: g.Name }))}/>
        </Field>
      </Modal>

      {/* Publish data disk online */}
      <Modal open={modal === "publish"} onClose={() => !busy && setModal(null)} title={`Publish Data Disk ${form.mount || ""}`}
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={doPublish}>{busy ? "Publishing…" : "Publish"}</button></>}>
        <div className="hint" style={{ marginBottom: 10 }}>
          The disk will briefly go offline while publishing; shutdown is not required. Running clients use changes at next boot.
        </div>
        <Field label="Restore Point Name" required hint={form.written ? `Unpublished changes ${fmtCap(form.written)}` : ""}>
          <input className="input" style={{ width: "100%" }} autoFocus placeholder="e.g. 2026Q3-Game-Update"
            value={form.publish_name || ""} onChange={e => setForm({ ...form, publish_name: e.target.value })}/>
        </Field>
      </Modal>

      {/* Stop Super Admin and save a restore point */}
      <Modal open={modal === "stopSuper"} onClose={() => !busy && setModal(null)} title="Super Admin Shutdown · Save Restore Point"
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={doStopSuper}>{busy ? "Submitting…" : "Save Restore Point"}</button></>}>
        {(terminals.find(t => t.ID === form.id) || {}).State === "online" && (
          <div className="hint" style={{ marginBottom: 10, color: "var(--amber)" }}>
            This client is running. Shut it down before saving.
          </div>
        )}
        {/* State what is saved and what will be discarded. */}
        <div className="hint" style={{ marginBottom: 4 }}>Selected disks are saved as restore points and made current for clients' next boot. This client remains Super Admin.</div>
        <div className="hint" style={{ marginBottom: 10, color: "var(--amber)" }}>Changes on unchecked disks will be discarded.</div>
        <div className="row" style={{ gap: 8, alignItems: "center", marginBottom: 8 }}>
          <label className="row" style={{ gap: 6, width: 150, fontSize: 13, cursor: "pointer" }}>
            <input type="checkbox" checked={form.save_system !== false} onChange={e => setForm({ ...form, save_system: e.target.checked })} aria-label="Save System Disk"/>
            System Disk
          </label>
          <input className="input" style={{ flex: 1 }} disabled={form.save_system === false} value={form.reduction_name || ""} onChange={e => setForm({ ...form, reduction_name: e.target.value })} placeholder="2026Q2-update" aria-label="System Disk Restore Point Name"/>
        </div>
        {(form.disks || []).map(d => {
          const save = form.saves?.[d.ID] || {};
          return (
            <div key={d.ID} className="row" style={{ gap: 8, alignItems: "center", marginBottom: 8 }}>
              <label className="row" style={{ gap: 6, width: 150, fontSize: 13, cursor: "pointer" }}>
                <input type="checkbox" checked={!!save.on} onChange={e => setDiskSave(d.ID, { on: e.target.checked })} aria-label={`Save Data Disk ${d.MountTarget}`}/>
                Data Disk {d.MountTarget}
                <span className="meta" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{images.find(i => i.ID === d.ImageID)?.Name || d.ImageID}</span>
              </label>
              <input className="input" style={{ flex: 1 }} disabled={!save.on} value={save.name || ""} onChange={e => setDiskSave(d.ID, { name: e.target.value })} placeholder="Restore Point Name" aria-label={`Data Disk ${d.MountTarget} Restore Point Name`}/>
            </div>
          );
        })}
      </Modal>

      {conf.node}
    </div>
  );
}

// Details and Edit use separate dialogs to avoid unexpected content replacement.
function TerminalModalTitle({ t }) {
  const st = STATE[stateOf(t)];
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
      <span className={`dot ${st.dot}`}/>
      <span>{terminalTitle(t)}</span>
      {/* Hide ID since it duplicates terminal- plus MAC. */}
      {t.IsSuper && <span className="chip violet">Super Admin</span>}
    </div>
  );
}

function TerminalEdit({ t, groups, busy, onClose, onSave }) {
  const [form, setForm] = useState(terminalForm(t));
  // Depend on client ID only so polling cannot wipe in-progress edits.
  useEffect(() => { setForm(terminalForm(t)); }, [t.ID]);

  // Preserve IP within same group; let backend allocate IP in a new group. Reset incompatible old IP.
  const changeGroup = (groupID) => {
    let ip = form.ip;
    if (!ip?.trim() || ip === t.IP) {
      const g = groups.find(x => x.ID === groupID);
      ip = groupID === t.GroupID || (t.IP && ipInGroupPool(t.IP, g)) ? t.IP : "";
    }
    setForm({ ...form, group_id: groupID, ip });
  };
  const formGroup = groups.find(g => g.ID === form.group_id) || null;
  const ipOutOfPool = !!form.ip?.trim() && formGroup && !ipInGroupPool(form.ip, formGroup);
  const ipHint = ipOutOfPool ? (
    <span style={{ color: "var(--amber)" }}>
      Outside group {formGroup.Name} subnet {formGroup.StartIP} → {ipRangeEnd(formGroup.StartIP, formGroup.ClientMax)}; save will be rejected
    </span>
  ) : form.group_id !== t.GroupID
    ? "Leave blank for automatic allocation in the new group subnet; manually entered IP must belong to that subnet."
    : "Leave blank to preserve the current IP; manually entered IP must belong to the group's subnet.";

  return (
    <Modal open onClose={() => !busy && onClose()} size="lg" title={<TerminalModalTitle t={t}/>}
      footer={<>
        <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="btn primary" disabled={busy} onClick={() => onSave(form)}>{busy ? "Saving…" : "Save"}</button>
      </>}>
      <Field label="Client Name"><input className="input" style={{ width: "100%" }} value={form.name || ""} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="e.g. FrontDesk-01"/></Field>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))", gap: 12 }}>
        <Field label="MAC Address" required><input className="input mono" style={{ width: "100%" }} value={form.mac || ""} onChange={e => setForm({ ...form, mac: e.target.value })}/></Field>
        <Field label="IP Address" hint={ipHint}><input className="input mono" style={{ width: "100%" }} value={form.ip || ""} onChange={e => setForm({ ...form, ip: e.target.value })} placeholder={t.IP || "192.168.10.x"}/></Field>
      </div>
      <Field label="Group" required>
        <Select value={form.group_id || ""} onChange={changeGroup} options={groups.map(g => ({ value: g.ID, label: g.Name }))}/>
      </Field>
      <Field label="Client Type" helpInline
        help="Super Admin edits image contents. Saved restore points apply to group clients at next boot."
        hint={t.State === "online" ? "Client running; shut down before changing type." : "Only one Super Admin can use a configuration at a time."}>
        <div className="seg" style={{ width: "fit-content" }}>
          <button type="button" disabled={t.State === "online"} className={!form.is_super ? "on" : ""} onClick={() => setForm({ ...form, is_super: false })}>Regular Client</button>
          <button type="button" disabled={t.State === "online"} className={form.is_super ? "on" : ""} onClick={() => setForm({ ...form, is_super: true })}>Super Admin</button>
        </div>
      </Field>
    </Modal>
  );
}

function TerminalDetail({ t, group, images, bundleNameById, imageName, onClose, onDelete, onSuper, onStopSuper, onPublish, gotoDrivers }) {
  const st = STATE[stateOf(t)];
  const { data: diskData, loading: disksLoading } = useResource(
    () => (group?.ID ? api.listGroupDisks(group.ID) : Promise.resolve({ items: [] })),
    [group?.ID],
  );
  const disks = (diskData && diskData.items) || [];
  // Super Admin data disks show the current published point and pending writes.
  const { data: superDiskData } = useResource(
    () => (t.IsSuper ? api.superDisks(t.ID) : Promise.resolve({ items: [] })),
    [t.IsSuper, t.ID],
  );
  const superDisks = (superDiskData && superDiskData.items) || [];
  const imageNameById = (id) => images.find(i => i.ID === id)?.Name || id || "—";
  // Resolve configuration/restore-point IDs with nanosecond suffixes to names where possible.
  const { data: nameData } = useResource(async () => {
    if (!group?.SystemImageID) return null;
    const [cfgs, reds] = await Promise.allSettled([
      api.listConfigs(group.SystemImageID),
      group.SystemConfigID ? api.listReductions(group.SystemConfigID) : Promise.resolve(null),
    ]);
    const items = (r) => (r.status === "fulfilled" && r.value && r.value.items) || [];
    return {
      configs: Object.fromEntries(items(cfgs).map(c => [c.ID, c.Name])),
      reductions: Object.fromEntries(items(reds).map(rp => [rp.ID, redName(rp)])),
    };
  }, [group?.SystemImageID, group?.SystemConfigID]);
  const running = t.State === "online";
  const configLabel = (nameData?.configs || {})[group?.SystemConfigID] || group?.SystemConfigID || "—";
  const reductionLabel = (nameData?.reductions || {})[group?.SystemReductionID] || group?.SystemReductionID || "—";

  return (
    <Modal open onClose={onClose} size="xl" title={<TerminalModalTitle t={t}/>}
      footer={<>
        {/* Disable operations while a client runs and explain the condition. */}
        {running && <span className="hint" style={{ marginRight: "auto" }}>{t.IsSuper ? "Client running; shut down to disable Super Admin or save a restore point." : "Client running; shut down before enabling Super Admin."}</span>}
        {t.IsSuper
          ? <>
              <button className="btn" disabled={running} onClick={() => onSuper(false)}>Disable Super Admin</button>
              <button className="btn primary" disabled={running} onClick={onStopSuper}><Icons.Snapshot size={12}/> Shut Down and Save Restore Point</button>
            </>
          : <button className="btn" disabled={running} onClick={() => onSuper(true)}>Enable Super Admin</button>}
        <button className="btn danger" onClick={onDelete}>Delete</button>
      </>}>
        <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          {t.IsSuper && t.PendingBundleID && (
            <div style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 13, color: "var(--fg-mute)" }}>
              <span className="chip amber"><Icons.Clock size={11}/> Driver finalization session in progress</span>
              <span>Injected {bundleNameById[t.PendingBundleID] || t.PendingBundleID}; not yet committed</span>
              <span className="row" style={{ color: "var(--cyan)", cursor: "pointer", gap: 2 }} onClick={gotoDrivers}>Manage in Driver Center<Icons.Chevron size={11}/></span>
            </div>
          )}
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(240px, 1fr))", gap: 14 }}>
            <DetailSection title="Basic Information" icon={<Icons.Monitor size={12}/>}>
              <DetailKV k="Client Name" v={terminalTitle(t)}/>
              <DetailKV k="MAC Address" v={fmtMac(t.MAC)}/>
              <DetailKV k="IP Address" v={t.IP || "—"}/>
              <DetailKV k="Status" v={st.t}/>
              <DetailKV k="Last Heartbeat" v={fmtTime(t.LastHeartbeatAt)}/>
            </DetailSection>
            <DetailSection title="Group Network" icon={<Icons.Network size={12}/>}>
              <DetailKV k="Group" v={group?.Name || "—"}/>
              <DetailKV k="IP Range" v={group ? `${group.StartIP} → ${ipRangeEnd(group.StartIP, group.ClientMax)}` : "—"}/>
              <DetailKV k="Gateway" v={group?.Gateway || "—"}/>
              <DetailKV k="Subnet Mask" v={group?.Netmask || "—"}/>
              <DetailKV k="DNS" v={group ? [group.DNS1, group.DNS2].filter(Boolean).join(" / ") || "—" : "—"}/>
            </DetailSection>
          </div>
          <DetailSection title="System Disk" icon={<Icons.Layers size={12}/>}>
            <DetailKV k="System Image" v={imageName || "—"}/>
            <DetailKV k="Boot Configuration" v={configLabel}/>
            <DetailKV k="Default Restore Point" v={reductionLabel}/>
          </DetailSection>
          <DetailSection title="Data Disk" icon={<Icons.Disk size={12}/>}>
            {t.IsSuper && <div className="hint" style={{ marginBottom: 8 }}>Data disks can be published without shutdown; the disk briefly goes offline and takes effect at the next boot.</div>}
            <div style={{ background: "var(--bg-0)", border: "1px solid var(--line-soft)", borderRadius: 6, overflow: "hidden" }}>
              <table className="t" style={{ margin: 0 }}>
                <thead><tr><th>Mount Target</th>{t.IsSuper ? <><th>Purpose</th><th>Current Published Point</th><th>Unpublished Changes</th><th/></> : <><th>Image</th><th>Configuration</th></>}</tr></thead>
                <tbody>
                  {t.IsSuper ? superDisks.map(d => {
                    // Explain why publishing is unavailable.
                    const why = !d.ready ? "Available after boot" : (d.written > 0 ? null : "No changes");
                    return (
                      <tr key={d.disk_id}>
                        <td className="mono" style={{ fontWeight: 600 }}>{d.mount_target}</td>
                        <td>{d.config_name || "—"}</td>
                        <td className="muted">{d.current_name || "—"}</td>
                        <td className={why ? "muted" : "mono"}>{why || fmtCap(d.written)}</td>
                        <td style={{ textAlign: "right" }}>
                          <button className="btn" style={{ padding: "3px 8px" }} disabled={!!why} onClick={() => onPublish(d)}>Save and Publish</button>
                        </td>
                      </tr>
                    );
                  }) : disks.map(d => (
                    <tr key={d.ID}>
                      <td className="mono" style={{ fontWeight: 600 }}>{d.MountTarget}</td>
                      <td className="muted">{imageNameById(d.ImageID)}</td>
                      <td className="mono muted" style={{ fontSize: 12 }}>{d.ConfigID}</td>
                    </tr>
                  ))}
                  {!(t.IsSuper ? superDisks : disks).length && <tr><td colSpan={5} style={{ textAlign: "center", padding: 14, color: "var(--fg-faint)", fontSize: 12 }}>{disksLoading ? "Loading…" : "No data disks"}</td></tr>}
                </tbody>
              </table>
            </div>
          </DetailSection>
        </div>
    </Modal>
  );
}

function DetailSection({ title, icon, children }) {
  return (
    <div style={{ background: "var(--bg-0)", border: "1px solid var(--line-soft)", borderRadius: 8, padding: 12 }}>
      <div className="card-title-zh" style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>{icon}{title}</div>
      {children}
    </div>
  );
}

function DetailKV({ k, v }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", padding: "7px 0", borderBottom: "1px solid var(--line-soft)", fontSize: 13 }}>
      <span style={{ color: "var(--fg-faint)" }}>{k}</span>
      <span className="mono" style={{ fontWeight: 500 }}>{v}</span>
    </div>
  );
}


window.PageTerminals = PageTerminals;

export { PageTerminals };
