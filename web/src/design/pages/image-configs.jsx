// Configuration and Restore Point tabs and dialogs used exclusively by images.jsx.
import React from 'react';
import { Icons } from '../icons.jsx';
import { useStore } from '../store.jsx';
import { Modal, useConfirm } from '../overlay.jsx';
import { Field, Select, TableState, SearchBox, Pager } from '../primitives.jsx';
import { api } from '../../lib/api.js';
import { redName, fmtDateTime } from '../../lib/format.js';
import { useResource, useMutation, usePaged } from '../../lib/hooks.js';
const { useState, useEffect } = React;

const RED_STATE = { ready: { t: "Ready", c: "emerald" }, creating: { t: "Creating", c: "cyan" }, error: { t: "Error", c: "rose" } };
const namesClause = (names) => names.slice(0, 5).join(", ") + (names.length > 5 ? `, and ${names.length - 5} more` : "");
const byTime = (a, b) => (Date.parse(a.CreatedAt) || 0) - (Date.parse(b.CreatedAt) || 0);
// Match backend rules: the active or sole restore point cannot be deleted; backend handles damaged snapshots.
const deleteLock = (rp) => {
  if (rp.Status !== "ready") return "";
  if (rp.cfg.reductions.length <= 1) return `This is configuration ${rp.cfg.Name}'s only restore point. Create another restore point before deleting it.`;
  if (rp.cfg.DefaultReductionID === rp.ID) return `This is configuration ${rp.cfg.Name}'s currently applied restore point. Apply another one before deletion.`;
  return "";
};
const latestOf = (reds) => reds.filter(r => r.Status === "ready").sort(byTime).pop() || null;
const items = (r) => (r && r.items) || [];

async function listGroups() {
  try { return items(await api.listGroups()); } catch { return []; }
}

// loadConfigUsers lists groups using a configuration for boot ({ group }) or as a data disk ({ group, mount }).
export async function loadConfigUsers() {
  const groups = await listGroups();
  const disks = await Promise.all(groups.map(g => api.listGroupDisks(g.ID).then(items).catch(() => [])));
  return (configId) => [
    ...groups.filter(g => g.SystemConfigID === configId).map(g => ({ group: g.Name })),
    ...disks.flatMap((ds, i) => ds.filter(d => d.ConfigID === configId).map(d => ({ group: groups[i].Name, mount: d.MountTarget }))),
  ];
}

export const usageLabel = (u) => (u.mount ? `${u.group} (data disk ${u.mount})` : u.group);

// Show the first two consumers and collapse the remainder to +N; hover for the full list.
export function UsageCell({ users, empty = "—" }) {
  if (!users.length) return <span className="muted">{empty}</span>;
  const shown = users.slice(0, 2);
  return (
    <span className="row" style={{ gap: 6, whiteSpace: "nowrap" }} title={users.map(usageLabel).join("\n")}>
      <span>{shown.map((u, i) => (
        <React.Fragment key={i}>{i > 0 && ", "}{u.group}{u.mount && <span className="meta"> {u.mount} (data disk)</span>}</React.Fragment>
      ))}</span>
      {users.length > 2 && <span className="chip">+{users.length - 2}</span>}
    </span>
  );
}

// Fetch configurations, restore points, and consuming groups for both tabs in one request batch.
function useCatalogue(images) {
  const key = images.map(i => i.ID).join(",");
  return useResource(async () => {
    const configs = (await Promise.all(images.map(img => api.listConfigs(img.ID).then(items).catch(() => []))))
      .flat();
    const reds = await Promise.all(configs.map(c => api.listReductions(c.ID).then(items).catch(() => [])));
    const usersOf = await loadConfigUsers();
    return configs.map((c, i) => {
      const users = usersOf(c.ID);
      return { ...c, reductions: reds[i].sort(byTime), users, groups: users.map(usageLabel) };
    });
  }, [key]);
}

// Configuration tab ─────────────────────────────────────────────────────────
export function ConfigsTab({ images, onShowReductions }) {
  const store = useStore();
  const conf = useConfirm();
  const { busy, run } = useMutation(store.toast);
  const [imageF, setImageF] = useState("");
  const [q, setQ] = useState("");
  const [modal, setModal] = useState(null);
  const { data, loading, error, reload } = useCatalogue(images);
  const all = data || [];
  const imageName = (id) => images.find(i => i.ID === id)?.Name || id;
  const rows = all.filter(c => (!imageF || c.ImageID === imageF) && (!q || c.Name.toLowerCase().includes(q.toLowerCase())));
  const paged = usePaged(rows);

  const doDelete = async (cfg) => {
    const message = cfg.groups.length
      ? `Configuration ${cfg.Name} is used by groups ${namesClause(cfg.groups)} as a boot configuration. Reassign these groups before deleting it.`
      : `Delete configuration ${cfg.Name}? Its ${cfg.reductions.length} restore points will also be deleted.`;
    if (!(await conf.ask({ title: "Delete Configuration", message, danger: true }))) return;
    await run(() => api.deleteConfig(cfg.ID), "Configuration deletion task submitted", { reload, errMsg: "Deletion failed" });
  };

  return (
    <>
      <div className="row" style={{ gap: 8 }}>
        <Select aria-label="Filter by Image" value={imageF} onChange={setImageF} style={{ width: 180 }}
          options={[{ value: "", label: "All Images" }, ...images.map(i => ({ value: i.ID, label: i.Name }))]}/>
        <SearchBox value={q} onChange={setQ} placeholder="Search configuration names"/>
        <div style={{ flex: 1 }}/>
        <button className="btn primary" onClick={() => setModal({ kind: "config" })}><Icons.Plus size={12}/> Create Configuration</button>
      </div>
      <div className="card" style={{ padding: 0 }}>
        <div style={{ overflowX: "auto" }}><table className="t">
          <thead><tr><th>Configuration</th><th>Image</th><th>Current Restore Point</th><th>Restore Point</th><th>Groups Using It</th><th>Created At</th><th className="t-actions"></th></tr></thead>
          <tbody>
            {paged.rows.map(cfg => {
              const current = cfg.reductions.find(r => r.ID === cfg.DefaultReductionID);
              const latest = latestOf(cfg.reductions);
              const onlyConfig = !all.some(c => c.ImageID === cfg.ImageID && c.ID !== cfg.ID);
              return (
                <tr key={cfg.ID}>
                  <td style={{ fontWeight: 600 }}>{cfg.Name}</td>
                  <td>{imageName(cfg.ImageID)}</td>
                  <td>
                    <span className="row" style={{ gap: 6 }}>
                      {current ? redName(current) : "—"}
                      {current && latest && current.ID !== latest.ID && <span className="chip amber" title={`Latest: ${redName(latest)}`}>Not Latest</span>}
                    </span>
                  </td>
                  <td><button className="btn ghost" style={{ padding: "2px 6px" }} onClick={() => onShowReductions(cfg.ImageID, cfg.ID)}>{cfg.reductions.length} items</button></td>
                  <td style={{ maxWidth: 260, overflow: "hidden", textOverflow: "ellipsis" }}><UsageCell users={cfg.users}/></td>
                  <td className="mono muted" style={{ fontSize: 12 }}>{fmtDateTime(cfg.CreatedAt)}</td>
                  <td className="t-actions"><div>
                    <button className="btn ghost icon danger" disabled={onlyConfig} onClick={() => doDelete(cfg)} aria-label={`Delete configuration ${cfg.Name}`}
                      title={onlyConfig ? `This is image ${imageName(cfg.ImageID)}'s last configuration and cannot be deleted. Delete the image instead if it is no longer needed.` : "Delete Configuration"}><Icons.Trash size={12}/></button>
                  </div></td>
                </tr>
              );
            })}
            <TableState colSpan={7} loading={loading && !data} error={error} empty={!rows.length} hint="No configurations. Click Create Configuration above."/>
          </tbody>
        </table></div>
        <Pager {...paged.pager}/>
      </div>
      {modal?.kind === "config" && <NewConfigModal images={images} catalogue={all} defaultImage={imageF} busy={busy} run={run} onClose={() => setModal(null)} onDone={reload}/>}
      {conf.node}
    </>
  );
}

// New configurations can start from the original image or an existing restore point, which then becomes protected.
function NewConfigModal({ images, catalogue, defaultImage, busy, run, onClose, onDone }) {
  const store = useStore();
  const [form, setForm] = useState({ name: "", imageId: defaultImage || images[0]?.ID || "", from: "image", configId: "", reductionId: "" });
  const configsOf = catalogue.filter(c => c.ImageID === form.imageId);
  const src = configsOf.find(c => c.ID === form.configId) || configsOf[0];
  const reds = src ? src.reductions : [];
  useEffect(() => {
    if (form.from !== "config" || !src) return;
    if (form.configId !== src.ID || !reds.some(r => r.ID === form.reductionId)) {
      setForm(f => ({ ...f, configId: src.ID, reductionId: src.DefaultReductionID || reds[reds.length - 1]?.ID || "" }));
    }
  }, [form.from, form.imageId, form.configId]); // eslint-disable-line

  const submit = async () => {
    const name = form.name.trim();
    if (!name) { store.toast("Enter a configuration name", "err"); return; }
    if (!form.imageId) { store.toast("Select a source image", "err"); return; }
    const ok = form.from === "image"
      ? await run(() => api.createConfigFromImage(form.imageId, { name }), "Configuration creation task submitted", { reload: onDone, errMsg: "Creation failed" })
      : form.reductionId
        ? await run(() => api.forkConfig(form.configId, { name, reduction_id: form.reductionId }), "Configuration creation task submitted", { reload: onDone, errMsg: "Creation failed" })
        : (store.toast("Select a source restore point", "err"), false);
    if (ok) onClose();
  };

  return (
    <Modal open onClose={() => !busy && onClose()} title="Create Configuration"
      footer={<><button className="btn" disabled={busy} onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy} onClick={submit}>{busy ? "Submitting…" : "Create"}</button></>}>
      <Field label="Configuration Name" required>
        <input className="input" aria-label="Configuration Name" style={{ width: "100%" }} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="e.g. Classroom-Office"/>
      </Field>
      <Field label="Image" required>
        <Select aria-label="Image" value={form.imageId} onChange={v => setForm({ ...form, imageId: v, configId: "", reductionId: "" })}
          options={images.map(i => ({ value: i.ID, label: i.Name }))}/>
      </Field>
      <Field label="Starting Point" required>
        <div style={{ display: "flex", flexDirection: "column", gap: 8, fontSize: 13 }}>
          <label className="row" style={{ gap: 8, cursor: "pointer" }}>
            <input type="radio" name="from" checked={form.from === "image"} onChange={() => setForm({ ...form, from: "image" })}/>
            Original image contents (as imported)
          </label>
          <label className="row" style={{ gap: 8, cursor: "pointer" }}>
            <input type="radio" name="from" checked={form.from === "config"} disabled={!configsOf.length} onChange={() => setForm({ ...form, from: "config" })}/>
            Restore point from an existing configuration
          </label>
          {form.from === "config" && src && (
            <div className="row" style={{ gap: 8, paddingLeft: 22 }}>
              <Select aria-label="Source Configuration" value={src.ID} onChange={v => setForm({ ...form, configId: v, reductionId: "" })} style={{ width: 160 }}
                options={configsOf.map(c => ({ value: c.ID, label: c.Name }))}/>
              <Select aria-label="Source Restore Point" value={form.reductionId} onChange={v => setForm({ ...form, reductionId: v })} style={{ width: 200 }}
                options={reds.map(r => ({ value: r.ID, label: redName(r) + (r.ID === src.DefaultReductionID ? " (current)" : "") }))}/>
            </div>
          )}
        </div>
      </Field>
      <div className="hint">The new configuration and its source are independent and can be modified separately.{form.from === "config" && "The source restore point cannot be deleted while this new configuration exists."}</div>
    </Modal>
  );
}

// Restore Point tab ────────────────────────────────────────────────────────
// Default to all restore points, preserving image/configuration filters when navigating from another view.
export function RestorePointsTab({ images, imageId, configId, onPick }) {
  const store = useStore();
  const conf = useConfirm();
  const { busy, run } = useMutation(store.toast);
  const [modal, setModal] = useState(null);
  const [selected, setSelected] = useState([]);
  const [q, setQ] = useState("");
  const { data, loading, error, reload } = useCatalogue(images);
  const catalogue = data || [];
  const imageOrder = (id) => images.findIndex(i => i.ID === id);
  const imageOf = (id) => images.find(i => i.ID === id) || null;
  const cfgOf = (id) => catalogue.find(c => c.ID === id) || null;
  const configChoices = catalogue.filter(c => !imageId || c.ImageID === imageId);
  const rows = catalogue
    .filter(c => (!imageId || c.ImageID === imageId) && (!configId || c.ID === configId))
    .sort((a, b) => imageOrder(a.ImageID) - imageOrder(b.ImageID) || byTime(a, b))
    .flatMap(c => c.reductions.map(r => ({ ...r, cfg: c })))
    .filter(r => !q || redName(r).toLowerCase().includes(q.toLowerCase()));
  const paged = usePaged(rows);
  useEffect(() => { setSelected([]); }, [imageId, configId, q, paged.page, paged.size]);
  // Remove selected restore points from the selection after applying or deleting them.
  const chosen = rows.filter(r => selected.includes(r.ID) && !deleteLock(r));
  useEffect(() => {
    if (chosen.length !== selected.length) setSelected(chosen.map(r => r.ID));
  }, [data]); // eslint-disable-line react-hooks/exhaustive-deps

  const apply = (rp) => run(() => api.applyReduction(rp.ID), `Applied ${redName(rp)}; takes effect at the client's next boot`, { reload, errMsg: "Apply failed" });

  const confirmDelete = async (rp) => {
    const refs = (await listGroups()).filter(g => g.SystemReductionID === rp.ID).map(g => g.Name);
    const warnings = [];
    if (refs.length) warnings.push(`Used by groups ${namesClause(refs)} as a restore point; deletion will fail`);
    const message = warnings.length ? `Restore point ${redName(rp)}: ${warnings.join("; ")}. Delete it?` : `Delete restore point ${redName(rp)}?`;
    if (!(await conf.ask({ title: "Delete Restore Point", message, danger: true, confirmText: "Delete" }))) return;
    await run(() => api.deleteReduction(rp.ID), "Restore-point deletion task submitted", { reload, errMsg: "Deletion failed" });
  };

  const deleteSelected = async () => {
    const picked = chosen;
    if (!(await conf.ask({ title: "Delete Restore Point", message: `Delete ${picked.length} restore points: ${namesClause(picked.map(r => `${r.cfg.Name}/${redName(r)}`))}? Deletion cannot be undone.`, danger: true, confirmText: "Delete" }))) return;
    const failed = [];
    for (const rp of picked) {
      try { await api.deleteReduction(rp.ID); } catch (e) { failed.push(`${redName(rp)}: ${e.message || "Deletion failed"}`); }
    }
    if (failed.length) store.toast(failed.join("\n"), "err");
    else store.toast(`Submitted deletion of ${picked.length} restore points`, "ok");
    setSelected([]);
    reload();
  };

  const overwrite = async (rp) => {
    const image = imageOf(rp.cfg.ImageID);
    const siblings = catalogue.filter(c => c.ImageID === rp.cfg.ImageID && c.ID !== rp.cfg.ID).map(c => c.Name);
    const later = rp.cfg.reductions.filter(r => byTime(r, rp) > 0).map(redName);
    const message = [
      `Replace the original contents of image ${image?.Name || rp.cfg.ImageID} using restore point ${redName(rp)}.`,
      siblings.length ? `Other configurations of this image (${namesClause(siblings)}) will be deleted.` : null,
      `Configuration ${rp.cfg.Name}'s restore points will be removed, leaving only a new starting point${later.length ? `; newer points ${namesClause(later)} are included` : ""}.`,
      "Groups using this configuration will switch to the new starting point. This cannot be undone.",
    ].filter(Boolean).join("\n");
    if (!(await conf.ask({ title: "Overwrite Original Image", message, danger: true, confirmText: "Overwrite" }))) return;
    await run(() => api.overwriteImage(rp.ID), "Overwrite-original-image task submitted", { reload, errMsg: "Overwrite failed" });
  };

  const pageIds = paged.rows.filter(r => !deleteLock(r)).map(r => r.ID);
  const allOnPage = pageIds.length > 0 && pageIds.every(id => chosen.some(r => r.ID === id));
  return (
    <>
      <div className="row" style={{ gap: 8 }}>
        <Select aria-label="Filter by Image" value={imageId || ""} onChange={v => onPick(v, "")} style={{ width: 170 }}
          options={[{ value: "", label: "All Images" }, ...images.map(i => ({ value: i.ID, label: i.Name }))]}/>
        <Select aria-label="Filter by Configuration" value={configId || ""} onChange={v => onPick(imageId, v)} style={{ width: 170 }}
          options={[{ value: "", label: "All Configurations" }, ...configChoices.map(c => ({ value: c.ID, label: imageId ? c.Name : `${imageOf(c.ImageID)?.Name || c.ImageID} / ${c.Name}` }))]}/>
        <SearchBox value={q} onChange={setQ} placeholder="Search restore point names" width={200}/>
        <div style={{ flex: 1 }}/>
        <button className="btn danger" disabled={!chosen.length} onClick={deleteSelected}><Icons.Trash size={12}/> Delete Selected{chosen.length ? ` (${chosen.length})` : ""}</button>
      </div>
      <div className="card" style={{ padding: 0 }}>
        <div style={{ overflowX: "auto" }}><table className="t">
          <thead><tr>
            <th style={{ width: 36 }}><input type="checkbox" aria-label="Select all restore points on this page" checked={allOnPage} disabled={!pageIds.length} onChange={e => setSelected(e.target.checked ? pageIds : [])}/></th>
            <th>Restore Point</th><th>Image</th><th>Configuration</th><th>Status</th><th>Created At</th><th className="t-actions"></th>
          </tr></thead>
          <tbody>
            {paged.rows.map(rp => {
              const current = rp.cfg.DefaultReductionID === rp.ID;
              const latest = latestOf(rp.cfg.reductions);
              const rs = RED_STATE[rp.Status] || { t: rp.Status || "—", c: "" };
              const ready = rp.Status === "ready";
              const name = redName(rp);
              const lock = deleteLock(rp);
              return (
                <tr key={rp.ID}>
                  <td><input type="checkbox" aria-label={`Select restore point ${name}`} checked={!lock && selected.includes(rp.ID)} disabled={!!lock} title={lock || undefined}
                    onChange={() => setSelected(s => s.includes(rp.ID) ? s.filter(x => x !== rp.ID) : [...s, rp.ID])}/></td>
                  <td>
                    <span className="row" style={{ gap: 6, whiteSpace: "nowrap" }}>
                      <span className="ellip" style={{ fontWeight: 600, maxWidth: 140 }} title={name}>{name}</span>
                      {current && <span className="chip cyan" title="Restore point used to boot clients">Applied</span>}
                      {latest?.ID === rp.ID && <span className="meta">Latest</span>}
                    </span>
                    {rp.Remark && <div className="hint">{rp.Remark}</div>}
                  </td>
                  <td>{imageOf(rp.cfg.ImageID)?.Name || rp.cfg.ImageID}</td>
                  <td>{rp.cfg.Name}</td>
                  <td><span className={`chip ${rs.c}`}>{rs.t}</span></td>
                  <td className="mono muted" style={{ fontSize: 12 }}>{fmtDateTime(rp.CreatedAt)}</td>
                  <td className="t-actions"><div>
                    {!current && ready && <button className="btn" style={{ padding: "3px 8px" }} disabled={busy} onClick={() => apply(rp)} aria-label={`Apply ${name}`}>Apply</button>}
                    <button className="btn" style={{ padding: "3px 8px" }} disabled={!ready} onClick={() => setModal({ kind: "saveAs", rp })} aria-label={`Save as New Image ${name}`}>Save as New Image</button>
                    <button className="btn" style={{ padding: "3px 8px" }} disabled={!ready} onClick={() => setModal({ kind: "export", rp })} aria-label={`Export as Image File ${name}`}>Export as Image File</button>
                    <MoreMenu label={`More Actions ${name}`} items={[
                      { label: "Overwrite Original Image", danger: true, disabled: !ready, onClick: () => overwrite(rp) },
                      { label: "Delete", danger: true, disabled: !!lock, title: lock, onClick: () => confirmDelete(rp) },
                    ]}/>
                  </div></td>
                </tr>
              );
            })}
            <TableState colSpan={7} loading={loading && !data} error={error} empty={!rows.length} hint={catalogue.length ? "No matching restore points" : "No restore points"}/>
          </tbody>
        </table></div>
        <Pager {...paged.pager}/>
      </div>
      {modal?.kind === "saveAs" && <SaveAsImageModal rp={modal.rp} image={imageOf(modal.rp.cfg.ImageID)} busy={busy} run={run} onClose={() => setModal(null)}/>}
      {modal?.kind === "export" && <ExportModal busy={busy} run={run} onClose={() => setModal(null)}
        title={`Export Restore Point · ${imageOf(modal.rp.cfg.ImageID)?.Name || modal.rp.cfg.ImageID} / ${redName(modal.rp)}`}
        source="Exports the complete disk data of this restore point."
        ticket={(gzip) => api.exportReductionTicket(modal.rp.ID, gzip)}
        toDir={() => api.exportReduction(modal.rp.ID)}/>}
      {conf.node}
    </>
  );
}

// More-actions dropdown uses the same overlay pattern as the Tip in servers.jsx.
function MoreMenu({ label, items: entries }) {
  const [open, setOpen] = useState(false);
  return (
    <span style={{ position: "relative", display: "inline-flex" }}>
      <button className="btn ghost" style={{ padding: "3px 8px" }} aria-label={label} onClick={() => setOpen(v => !v)}>More ▾</button>
      {open && (
        <>
          <span onClick={() => setOpen(false)} style={{ position: "fixed", inset: 0, zIndex: "var(--z-popover)" }}/>
          <span className="card" style={{ position: "absolute", top: 28, right: 0, zIndex: "var(--z-popover)", padding: 4, minWidth: 120,
            display: "flex", flexDirection: "column", boxShadow: "0 8px 24px oklch(0 0 0 / 0.45)" }}>
            {entries.map(e => (
              <button key={e.label} className={`btn ghost${e.danger ? " danger" : ""}`} disabled={e.disabled} title={e.title || undefined} style={{ justifyContent: "flex-start" }}
                onClick={() => { setOpen(false); e.onClick(); }}>{e.label}</button>
            ))}
          </span>
        </>
      )}
    </span>
  );
}

function SaveAsImageModal({ rp, image, busy, run, onClose }) {
  const [name, setName] = useState(`${image?.Name || "Image"}-${redName(rp)}`);
  const submit = async () => {
    if (!name.trim()) return;
    if (await run(() => api.saveReductionAsImage(rp.ID, { name: name.trim() }), "Save-as-new-image task submitted. Track progress in Task Center.", { errMsg: "Save As failed" })) onClose();
  };
  return (
    <Modal open onClose={() => !busy && onClose()} title={`Save as New Image · ${redName(rp)}`} size="sm"
      footer={<><button className="btn" disabled={busy} onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy || !name.trim()} onClick={submit}>{busy ? "Submitting…" : "Save As"}</button></>}>
      <Field label="New Image Name" required>
        <input className="input" aria-label="New Image Name" style={{ width: "100%" }} value={name} onChange={e => setName(e.target.value)}/>
      </Field>
      <div className="hint">Create a full copy of this restore point as a new image with a default configuration. The new and original images are independent and can be deleted separately. Large images may take several minutes to copy.</div>
    </Modal>
  );
}

// ExportModal handles both image and restore-point exports as reimportable ZFS streams.
// ticket(gzip) obtains a one-time download URL; optional toDir supports server-side export.
export function ExportModal({ title, source, ticket, toDir, busy, run, onClose }) {
  const store = useStore();
  const [gzip, setGzip] = useState(false);
  // Convert a ticket into a URL for native browser download; fetch+blob would buffer the whole image.
  const download = async () => {
    const ok = await run(async () => {
      const t = await ticket(gzip);
      const a = document.createElement("a");
      a.href = t.url;
      document.body.appendChild(a);
      a.click();
      a.remove();
      return t;
    }, "Download started; check the browser downloads list for progress", { errMsg: "Export failed" });
    if (ok) onClose();
  };
  const exportToDir = async () => {
    let r;
    const ok = await run(async () => (r = await toDir()), null, { errMsg: "Export failed" });
    if (ok) {
      store.toast(`Export task submitted; destination: ${r.node || "Current host"} on ${r.path}`, "ok");
      onClose();
    }
  };
  return (
    <Modal open onClose={() => !busy && onClose()} title={title} size="sm">
      <div className="hint" style={{ marginBottom: 12 }}>
        {source}The exported format is a ZFS stream (.zfs or .zfs.gz with compression), importable on any server.
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <label style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 13, cursor: "pointer" }}>
          <input type="checkbox" checked={gzip} disabled={busy} onChange={e => setGzip(e.target.checked)} aria-label="gzip Compression"/>
          gzip Compression
        </label>
        <span className="hint" style={{ marginTop: -4 }}>
          ZFS data streams include pool compression. gzip may reduce size by another 30%–50% but increases server CPU usage. Imports automatically detect compression.
        </span>
        <div className="row" style={{ gap: 10, alignItems: "flex-start" }}>
          <button className="btn primary" style={{ flexShrink: 0, minWidth: 128 }} disabled={busy} onClick={download}>
            <Icons.Download size={12}/> Download Locally
          </button>
          <span className="hint" style={{ paddingTop: 4 }}>Download through your browser and monitor progress in its downloads list. Interrupted transfers must be restarted.</span>
        </div>
        {toDir && (
          <div className="row" style={{ gap: 10, alignItems: "flex-start" }}>
            <button className="btn" style={{ flexShrink: 0, minWidth: 128 }} disabled={busy} onClick={exportToDir}>
              <Icons.Server size={12}/> Export to Server Directory
            </button>
            <span className="hint" style={{ paddingTop: 4 }}>
              Write directly to the active host's import directory without network transfer; useful for large images. gzip is unavailable, and an HA switchover during export will fail the task. Check Task Center for details.
            </span>
          </div>
        )}
      </div>
    </Modal>
  );
}
