// Three-level model: Image → Configuration → Restore Point. Images are imported or created as blank data disks.
// The backend has no publish, rollback, or set-default endpoint; do not expose those actions.
import React from 'react';
import { Icons } from '../icons.jsx';
import { useStore } from '../store.jsx';
import { Modal, useConfirm } from '../overlay.jsx';
import { Field, Select, TableState, SearchBox } from '../primitives.jsx';
import { ConfigsTab, RestorePointsTab, ExportModal, loadConfigUsers, UsageCell } from './image-configs.jsx';
import { api } from '../../lib/api.js';
import { fmtDateTime, redName } from '../../lib/format.js';
import { useResource, useMutation, usePolling } from '../../lib/hooks.js';
const { useState, useEffect, useRef } = React;

// List referring groups in deletion confirmations when possible; the backend makes the final decision.
async function groupsReferencing(pick) {
  try {
    const r = await api.listGroups();
    return ((r && r.items) || []).filter(pick).map(g => g.Name);
  } catch {
    return [];
  }
}
const namesClause = (names) => names.slice(0, 5).join(", ") + (names.length > 5 ? ` and ${names.length} items` : "");

const GiB = 1024 ** 3;
const fmtGiB = (b) => `${(Number(b || 0) / GiB).toFixed(1)} GiB`;
const normalizeImageName = (v) => {
  v = (v || "").trim().toLowerCase();
  let out = "";
  for (const ch of v) {
    if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9") || ch === "_" || ch === "-") {
      out += ch;
    } else if (/\s/.test(ch)) {
      if (out && !out.endsWith("-")) out += "-";
    } else if (out && !out.endsWith("_")) {
      out += "_";
    }
  }
  return out.replace(/^[-_]+|[-_]+$/g, "");
};
const IMG_STATE = { normal: { t: "Ready", c: "emerald" }, importing: { t: "Importing", c: "cyan" }, error: { t: "Error", c: "rose" } };
// Color indicates disk purpose; OS uses icon shape and neutral text.
const OS_META = { windows: { t: "Windows" }, linux: { t: "Linux" } };
// System images boot clients; data images are formatted volumes mounted alongside the system disk.
const PURPOSE_META = { system: { t: "System Disk Image", c: "cyan" }, data: { t: "Data Disk Image", c: "violet" } };
const purposeOf = (img) => (img.Purpose === "data" ? "data" : "system");
const FS_META = { ntfs: { t: "NTFS（Windows）" }, ext4: { t: "ext4（Linux）" } };

function PageImages() {
  const store = useStore();
  const conf = useConfirm();
  const { route, goto } = store;

  const [tab, setTab] = useState("images"); // images | configs | reductions
  const [purposeF, setPurposeF] = useState("all");
  const [osF, setOsF] = useState("all");
  const [q, setQ] = useState("");
  const [redPick, setRedPick] = useState({ imageId: "", configId: "" });
  const [selImageId, setSelImageId] = useState(null);

  const [modal, setModal] = useState(null); // import | image | export | blank
  // Support browser upload and server directory import. Browser upload is bandwidth-limited; server files stay on the serving node.
  const [importMode, setImportMode] = useState("upload");
  const [sources, setSources] = useState(null);
  const [form, setForm] = useState({});
  const [importDir, setImportDir] = useState("");
  const [importPoolAvail, setImportPoolAvail] = useState(null); // bytes; null means pool capacity unavailable
  const [up, setUp] = useState(null); // { name, size, sent, state, error }
  const uploadIdRef = React.useRef("");
  // AbortController also identifies the current upload; cancellation, closing, or file replacement invalidates the old loop.
  const uploadCtlRef = React.useRef(null);
  const stopUpload = () => { if (uploadCtlRef.current) uploadCtlRef.current.abort(); uploadCtlRef.current = null; };
  useEffect(() => stopUpload, []);
  // State upload destination explicitly: uploads reach the active node, while operators access only the VIP.
  const nodesRes = useResource(() => api.listClusterNodes().catch(() => null), []);
  const nodes = (nodesRes.data && nodesRes.data.items) || [];
  const activeNode = nodes.find(n => n.ha_state === "active");
  const importPlaceholder = `${importDir || "/tank/imports"}/win11.vmdk`;

  const { data: imgData, loading, error, reload: loadImages } = useResource(() => api.listImages(), []);
  // Override backend ID ordering: system images first, then data images, each sorted by creation time.
  const images = [...((imgData && imgData.items) || [])].sort((a, b) =>
    (purposeOf(a) === purposeOf(b) ? 0 : purposeOf(a) === "system" ? -1 : 1)
    || ((Date.parse(a.CreatedAt) || 0) - (Date.parse(b.CreatedAt) || 0))
    || String(a.Name || "").localeCompare(String(b.Name || "")));
  const { busy, run } = useMutation(store.toast);

  const filtered = images.filter(i => (purposeF === "all" || purposeOf(i) === purposeF)
    && (osF === "all" || i.OSType === osF)
    && (!q || String(i.Name || "").toLowerCase().includes(q.toLowerCase())));
  const showReductions = (imageId, configId) => { setRedPick({ imageId, configId }); setModal(null); setTab("reductions"); };
  const selImage = images.find(i => i.ID === selImageId) || null;

  // Poll while images import so cards update automatically; progress comes from store.tasks.
  usePolling(loadImages, { intervalMs: 3000, active: images.some(i => i.State === "importing") });
  const importTaskOf = (imageId) =>
    store.tasks.find(k => k.target === imageId && (k.status === "running" || k.status === "waiting"));

  // Keep selected item valid when filters change
  useEffect(() => {
    if (!filtered.length) { if (selImageId) setSelImageId(null); return; }
    if (!filtered.some(i => i.ID === selImageId)) setSelImageId(filtered[0].ID);
  }, [images, purposeF, osF, q]); // eslint-disable-line

  // Consume each deep-link navigation once, even when navigating to the same image again.
  // Do not depend on the reconstructed images array; otherwise the modal reopens after closing.
  const imagesLoaded = !!imgData;
  const deepLinkSeen = useRef(null);
  useEffect(() => {
    if (!route.params.imageId || !imagesLoaded || deepLinkSeen.current === route.params) return;
    deepLinkSeen.current = route.params;
    if (images.some(i => i.ID === route.params.imageId)) {
      setSelImageId(route.params.imageId);
      setModal("image");
    }
  }, [route.params, imagesLoaded]); // eslint-disable-line

  // Image actions ─────────────────────────────────────────────────────────
  const baseName = (n) => (n || "").split("/").pop().replace(/\.(zfs\.gz|zfs|gzip|vmdk|vhdx|vhd|qcow2|qcow|vdi|raw|img)$/i, "");
  const openImageDetail = (imageId) => {
    setSelImageId(imageId);
    setModal("image");
  };
  const setPurpose = async (purpose) => {
    if (!selImage || purpose === purposeOf(selImage)) return;
    await run(() => api.setImagePurpose(selImage.ID, { purpose }), `${selImage.Name}  changed to ${PURPOSE_META[purpose].t}`, { reload: loadImages, errMsg: "Could not change purpose" });
  };
  const openBlank = () => { setForm({ name: "", size_gib: 100, filesystem: "ntfs", label: "" }); setModal("blank"); };
  const doCreateBlank = async () => {
    const name = (form.name || "").trim();
    if (!name) { store.toast("Enter a data disk name", "err"); return; }
    const gib = Number(form.size_gib);
    if (!(gib >= 1)) { store.toast("Minimum size is 1 GiB", "err"); return; }
    // Submit display name unchanged; backend normalizes dataset IDs to ASCII. Blank volume label defaults to name.
    const body = { name, size_bytes: Math.round(gib * GiB), filesystem: form.filesystem || "ntfs" };
    const label = (form.label || "").trim();
    if (label) body.label = label;
    if (await run(() => api.createBlankImage(body),
      `Data Disk ${name} creation task submitted`, { reload: loadImages, errMsg: "Create failed" })) setModal(null);
  };
  const openImport = async () => {
    setForm({ name: "", source_path: "", os_type: "windows", purpose: "system" });
    setImportMode("upload"); setSources(null);
    setUp(null); uploadIdRef.current = "";
    setImportDir(""); setImportPoolAvail(null);
    setModal("import");
    try {
      const r = await api.listImportSources();
      setImportDir((r && r.import_dir) || "");
      setImportPoolAvail(r && typeof r.pool_available === "number" ? r.pool_available : null);
    } catch {
      // Allow upload when capacity is unavailable; skip only preflight capacity rejection.
    }
  };
  // Use 8 MiB chunks to balance request overhead and retransmission cost for large uploads.
  const CHUNK = 8 * 1024 * 1024;

  // Browser upload is resumable: specify each chunk offset and query server progress after interruption.
  const uploadFile = async (file) => {
    if (!file) return;
    // Reject oversized uploads before transmission if pool capacity is known.
    if (importPoolAvail != null && file.size > importPoolAvail) {
      setUp({ name: file.name, size: file.size, sent: 0, state: "error",
        error: `Insufficient pool space: file ${fmtGiB(file.size)}; available ${fmtGiB(importPoolAvail)}` });
      return;
    }
    stopUpload();
    const ctl = new AbortController();
    uploadCtlRef.current = ctl;
    const live = () => uploadCtlRef.current === ctl;
    setUp({ name: file.name, size: file.size, sent: 0, state: "uploading", error: "" });
    let session;
    try {
      // Send modified timestamp to distinguish the same file from a different file with identical name and size.
      session = await api.beginUpload({ file_name: file.name, size_bytes: file.size, last_modified: file.lastModified });
    } catch (e) {
      if (live()) setUp(u => ({ ...u, state: "error", error: e.message || "Could not start upload" }));
      return;
    }
    if (!live()) return;
    uploadIdRef.current = session.upload_id;
    let offset = session.received || 0;
    // On resume, render progress from the acknowledged offset rather than zero.
    if (offset > 0) setUp(u => ({ ...u, sent: offset }));
    let retriesLeft = 5;
    while (offset < file.size && live()) {
      const blob = file.slice(offset, Math.min(offset + CHUNK, file.size));
      try {
        const r = await api.uploadChunk(session.upload_id, offset, blob, ctl.signal);
        if (!live()) return;
        offset = typeof r.received === "number" ? r.received : offset + blob.size;
        setUp(u => ({ ...u, sent: offset }));
        if (r.complete) {
          // Completed uploads become regular import paths.
          setForm(prev => ({ ...prev, source_path: r.path || "",
            name: prev.name || normalizeImageName(baseName(file.name)) }));
          setUp(u => ({ ...u, sent: file.size, state: "done" }));
          return;
        }
      } catch (e) {
        if (!live()) return;
        if (retriesLeft-- <= 0) {
          setUp(u => ({ ...u, state: "error", error: e.message || "Upload interrupted" }));
          return;
        }
        // Query acknowledged offset before resuming to avoid gaps or corruption.
        try {
          const st = await api.uploadStatus(session.upload_id);
          if (!live()) return;
          offset = typeof st.received === "number" ? st.received : offset;
          setUp(u => ({ ...u, sent: offset }));
        } catch (e2) {
          if (!live()) return;
          setUp(u => ({ ...u, state: "error", error: e2.message || "Upload session expired. Please upload again." }));
          return;
        }
      }
    }
  };

  const cancelUpload = async () => {
    const id = uploadIdRef.current;
    uploadIdRef.current = "";
    stopUpload();
    setUp(null);
    if (id) { try { await api.abortUpload(id); } catch { /* Best-effort cancellation; do not interrupt user */ } }
  };

  // Closing the modal stops local work but preserves upload state; Discard invalidates the server session.
  const closeImport = () => { stopUpload(); setModal(null); };

  const loadImportSources = async () => {
    try { setSources(await api.listImportSources()); }
    catch (e) { setSources({ items: [], import_dir: "", node: "", error: e.message }); }
  };

  const doImport = async () => {
    const name = (form.name || "").trim(), source = (form.source_path || "").trim();
    if (!name) { store.toast("Enter an image name", "err"); return; }
    if (!source) {
      store.toast(importMode === "server" ? "Select a file from the server directory first"
        : up && up.state === "uploading" ? "Upload in progress. Import after completion."
        : up && up.state === "error" ? "Upload failed. Select the file again."
          : "Select an image file to import.", "err");
      return;
    }
    const normalizedName = normalizeImageName(name);
    if (!normalizedName) { store.toast("Image name may contain only letters, digits, hyphens and underscores", "err"); return; }
    // Normalize generated names automatically instead of asking the user to reenter them.
    if (normalizedName !== name) {
      setForm(prev => ({ ...prev, name: normalizedName }));
      store.toast(`Image name normalized to ${normalizedName}`, "info");
    }
    // Clean up browser-uploaded sources after import; preserve files placed by the operator.
    if (await run(() => api.importImage({ name: normalizedName, source_path: source, os_type: form.os_type,
      purpose: form.purpose || "system", delete_source_after: importMode === "upload" }),
      `Image ${normalizedName} import task submitted`, { reload: loadImages, errMsg: "Import failed" })) setModal(null);
  };
  const doDeleteImage = async () => {
    if (!selImage) return;
    const refs = await groupsReferencing(g => g.SystemImageID === selImage.ID);
    const message = refs.length
      ? `${selImage.Name} is used by groups ${namesClause(refs)} as a system disk. Reassign those groups before deleting it.`
      : `Delete ${selImage.Name}? All configurations and restore points will also be deleted. Deletion fails while groups use it as a data disk.`;
    const ok = await conf.ask({ title: "Delete Image", message, danger: true });
    if (!ok) return;
    await run(() => api.deleteImage(selImage.ID), "Image deleted", { reload: loadImages, errMsg: "Delete failed" });
  };

  return (
    <div className="fade-in" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <div className="tabs">
        {[["images", "Image", images.length], ["configs", "Configuration"], ["reductions", "Restore Point"]].map(([v, label, n]) => (
          <div key={v} role="button" className={`tab ${tab === v ? "active" : ""}`} onClick={() => setTab(v)}>
            {label}{n != null && <span className="count">{n}</span>}
          </div>
        ))}
        <div style={{ flex: 1 }}/>
        {tab === "images" && <>
          <button className="btn" style={{ marginBottom: 6 }} onClick={openBlank}><Icons.Disk size={12}/> Create Data Disk</button>
          <button className="btn primary" style={{ marginBottom: 6, marginLeft: 6 }} onClick={openImport}><Icons.Plus size={12}/> Import Image</button>
        </>}
      </div>

      {tab === "configs" && <ConfigsTab images={images} onShowReductions={showReductions}/>}
      {tab === "reductions" && <RestorePointsTab images={images} imageId={redPick.imageId} configId={redPick.configId}
        onPick={(imageId, configId) => setRedPick({ imageId, configId })}/>}

      {tab === "images" && <div className="row" style={{ gap: 8 }}>
        <Select aria-label="Filter by Purpose" value={purposeF} onChange={setPurposeF} style={{ width: 140 }}
          options={[{ value: "all", label: "All Purposes" }, { value: "system", label: "System Disk Image" }, { value: "data", label: "Data Disk Image" }]}/>
        <Select aria-label="Filter by OS" value={osF} onChange={setOsF} style={{ width: 130 }}
          options={[{ value: "all", label: "All Operating Systems" }, { value: "windows", label: "Windows" }, { value: "linux", label: "Linux" }]}/>
        <SearchBox value={q} onChange={setQ} placeholder="Search image names"/>
      </div>}

      {tab === "images" &&       <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(330px, 1fr))", gap: 12, alignContent: "start" }}>
        {filtered.map(img => {
          const active = selImageId === img.ID;
          const os = OS_META[img.OSType] || { t: img.OSType || "—" };
          const accent = PURPOSE_META[purposeOf(img)].c;
          const st = IMG_STATE[img.State] || { t: img.State || "—", c: "" };
          return (
            <div key={img.ID} onClick={() => openImageDetail(img.ID)} className="card" style={{
              padding: 16, cursor: "pointer",
              borderColor: active ? "oklch(0.55 0.10 200 / 0.5)" : undefined,
              boxShadow: active ? "0 0 0 1px var(--cyan)" : undefined,
            }}>
              <div style={{ display: "flex", alignItems: "flex-start", gap: 12 }}>
                <div style={{ width: 44, height: 44, borderRadius: 8, background: `var(--${accent}-soft)`, border: `1px solid oklch(0.55 0.1 ${accent === "violet" ? 290 : 200} / 0.4)`, display: "grid", placeItems: "center", color: `var(--${accent})` }}
                  role="img" aria-label={purposeOf(img) === "data" ? PURPOSE_META.data.t : `${os.t} ${PURPOSE_META.system.t}`}>
                  {purposeOf(img) === "data" ? <Icons.Disk size={22}/> : img.OSType === "linux" ? <Icons.Linux size={22}/> : img.OSType === "windows" ? <Icons.Windows size={20}/> : <Icons.Layers size={22}/>}
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="ellip" style={{ fontSize: 14, fontWeight: 600 }}>{img.Name}</div>
                  {/* Show both logical and allocated sizes to prevent misleading capacity estimates. */}
                  <div className="mono meta" style={{ marginTop: 2 }}>Logical {fmtGiB(img.Size)} · Allocated {img.Used == null ? "—" : fmtGiB(img.Used)}</div>
                </div>
                <span className={`chip ${st.c}`}>{img.State === "importing" && <span className="dot cyan"/>}{st.t}</span>
              </div>
              {img.State === "importing" && (() => {
                const task = importTaskOf(img.ID);
                return (
                  <div style={{ marginTop: 10 }} onClick={e => { e.stopPropagation(); goto("tasks"); }} title="View task details">
                    <div className="bar cyan"><span style={{ width: `${task?.progress || 0}%` }}/></div>
                    <div className="meta" style={{ marginTop: 4 }}>{task ? `Importing ${task.progress}%` : "Import queued"} · View Task</div>
                  </div>
                );
              })()}
              <div style={{ marginTop: 12, display: "flex", justifyContent: "space-between", alignItems: "center" }} className="meta">
                <span className="row" style={{ gap: 6 }}>
                  <span className="chip">{os.t}</span>
                  <span className={`chip ${PURPOSE_META[purposeOf(img)].c}`}>{PURPOSE_META[purposeOf(img)].t}</span>
                </span>
                <span className="mono">{fmtDateTime(img.CreatedAt)}</span>
              </div>
            </div>
          );
        })}
        {!filtered.length && (
          <div className="card" style={{ padding: 40, textAlign: "center", color: "var(--fg-faint)", fontSize: 13, gridColumn: "1 / -1" }}>
            {loading ? "Loading…" : error ? `Load failed: ${error}` : images.length ? "No images match the filters" : "No images. Use Import Image or Create Data Disk."}
          </div>
        )}
      </div>}

      <Modal open={modal === "image"} onClose={() => setModal(null)} title={selImage ? selImage.Name : "Image Details"} size="lg"
        footer={selImage && <>
          <button className="btn" onClick={() => setModal("export")}><Icons.Download size={12}/> Export Image</button>
          <button className="btn danger" onClick={doDeleteImage}><Icons.Trash size={12}/> Delete Image</button>
        </>}>
        {!selImage ? (
          <div style={{ padding: "4px 0", fontSize: 13, color: "var(--fg-faint)" }}>No image selected</div>
        ) : (
          <>
            <div className="row" style={{ gap: 10, marginBottom: 12, fontSize: 13 }}>
              <span className="lbl">Purpose</span>
              <Select value={purposeOf(selImage)} aria-label="Image Purpose" onChange={setPurpose} style={{ width: 160 }}
                options={[{ value: "system", label: "System Disk Image" }, { value: "data", label: "Data Disk Image" }]}/>
              <span className="hint">{selImage.Origin === "blank" ? "New blank data disk" : "Imported image"} · Purpose cannot be changed while groups use this image.</span>
            </div>
            {purposeOf(selImage) === "system" && <ImageHealthCard imageId={selImage.ID}/>}

            <ConfigOverview image={selImage} onManage={(cfg) => showReductions(selImage.ID, cfg.ID)}/>
          </>
        )}
      </Modal>

      {/* Image export and import dialogs */}
      {modal === "export" && selImage && <ExportModal busy={busy} run={run} onClose={() => setModal("image")}
        title={`Export Image · ${selImage.Name}`}
        source="Exports the initially imported image without configuration or restore-point changes. To export a restore point, use its tab."
        ticket={(gzip) => api.exportImageTicket(selImage.ID, gzip)}
        toDir={() => api.exportImage(selImage.ID)}/>}

      <Modal open={modal === "import"} onClose={() => !busy && closeImport()} title="Import Image"
        footer={<><button className="btn" disabled={busy} onClick={closeImport}>Cancel</button>
          {/* Import source is unavailable until upload completes. Disable the button until then. */}
          <button className="btn primary" onClick={doImport}
            disabled={busy || (importMode === "server" ? !form.source_path : (!up || up.state !== "done"))}>
            {busy ? "Submitting…" : importMode === "upload" && up && up.state === "uploading" ? "Uploading…" : "Import"}
          </button></>}>
        <Field label="Image Name" required><input className="input" style={{ width: "100%" }} value={form.name || ""} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="Win11-Pro-2026Q1"/></Field>
        {/* Clarify the active node used for server-directory imports. */}
        <div className="row" style={{ gap: 0, marginBottom: 10 }}>
          <div className="btn-group">
            <button type="button" className={`btn ${importMode === "upload" ? "primary" : ""}`}
              onClick={() => setImportMode("upload")}>Upload from Computer</button>
            <button type="button" className={`btn ${importMode === "server" ? "primary" : ""}`}
              onClick={() => { setImportMode("server"); loadImportSources(); }}>From Server Directory</button>
          </div>
        </div>
        {importMode === "server" ? (
          <Field label="Select Server File" required
            hint={sources
              ? `Directory is on active node ${sources.node || "?"} at ${sources.import_dir} — copy files to this host via SCP to make them available.`
                + "An HA switchover during import will fail the task; check Task Center."
                + (sources.pool_available != null ? ` · Available pool space: ${fmtGiB(sources.pool_available)}` : "")
              : "Loading server directory…"}>
            <div style={{ border: "1px solid var(--line)", borderRadius: 6, maxHeight: 220, overflowY: "auto" }}>
              {(sources?.items || []).length === 0 && (
                <div className="hint" style={{ padding: 12 }}>
                  {sources ? "No importable files found in this directory" : "Loading…"}
                </div>
              )}
              {(sources?.items || []).map(f => (
                <div key={f.path} onClick={() => setForm({ ...form, source_path: f.path })}
                  style={{ padding: "8px 12px", cursor: "pointer", display: "flex", gap: 10, alignItems: "center",
                    background: form.source_path === f.path ? "var(--bg-0)" : undefined,
                    boxShadow: form.source_path === f.path ? "inset 2px 0 0 var(--accent)" : undefined }}>
                  <span className="mono" style={{ flex: 1 }}>{f.name}</span>
                  <span className="chip">{f.format}</span>
                  <span className="meta mono">{fmtGiB(f.size)}</span>
                </div>
              ))}
            </div>
          </Field>
        ) : (
          <Field label="Select Local File" required
            hint={(nodes.length > 1
              ? "After upload, the image can be imported and synchronized to all cluster nodes"
              : "You can import after upload completes")
              + ". Upload is resumable: reselect the same file after interruption."
              + (importPoolAvail != null ? ` · Available pool space: ${fmtGiB(importPoolAvail)}` : "")}>
            <input className="input" type="file" style={{ width: "100%" }} disabled={up && up.state === "uploading"}
              onChange={e => uploadFile(e.target.files && e.target.files[0])}/>
            {up && (
              <div style={{ marginTop: 10 }}>
                <div className="row" style={{ justifyContent: "space-between", fontSize: 13, marginBottom: 4 }}>
                  <span className="mono muted">{up.name}</span>
                  <span className="mono">{up.size ? Math.floor((up.sent / up.size) * 100) : 0}%</span>
                </div>
                <div className={`bar ${up.state === "error" ? "rose" : up.state === "done" ? "emerald" : "cyan"}`}>
                  <span style={{ width: `${up.size ? (up.sent / up.size) * 100 : 0}%` }}/>
                </div>
                <div className="row" style={{ justifyContent: "space-between", marginTop: 6 }}>
                  <span className="meta">
                    {up.state === "done" ? "Upload finished; ready to import"
                      : up.state === "error" ? up.error
                        : `${fmtGiB(up.sent)} / ${fmtGiB(up.size)}`}
                  </span>
                  {up.state === "uploading" && (
                    <button className="btn ghost" style={{ padding: "2px 8px" }} onClick={cancelUpload}>Discard</button>
                  )}
                </div>
              </div>
            )}
          </Field>
        )}

        <Field label="Operating System" required><Select value={form.os_type || "windows"} onChange={v => setForm({ ...form, os_type: v })} options={[{ value: "windows", label: "Windows" }, { value: "linux", label: "Linux" }]}/></Field>
        <Field label="Purpose" required hint={form.purpose === "data" ? "Data disk image: attach to groups as drive D:, etc. No startup script or boot health check." : "System disk image: boot volume for a group"}>
          <Select value={form.purpose || "system"} aria-label="Purpose" onChange={v => setForm({ ...form, purpose: v })} options={[{ value: "system", label: "System Disk Image" }, { value: "data", label: "Data Disk Image" }]}/>
        </Field>
      </Modal>

      {/* Create blank data disk */}
      <Modal open={modal === "blank"} onClose={() => !busy && setModal(null)} title="Create Data Disk"
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={doCreateBlank}>{busy ? "Submitting…" : "Create"}</button></>}>
        <Field label="Data Disk Name" required><input className="input" aria-label="Data Disk Name" style={{ width: "100%" }} value={form.name || ""} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="games-d"/></Field>
        <Field label="Size" required hint="Thin provisioned; consumes only written space and cannot be shrunk.">
          <div className="row" style={{ gap: 8 }}>
            <input className="input mono" type="number" min="1" step="1" aria-label="Size" style={{ width: 120 }} value={form.size_gib ?? 100} onChange={e => setForm({ ...form, size_gib: e.target.value })}/>
            <span className="mono meta">GiB</span>
            <span style={{ flex: 1 }}/>
            {[100, 200, 500, 1000].map(g => <button key={g} type="button" className={`btn ${Number(form.size_gib) === g ? "primary" : ""}`} style={{ padding: "3px 8px" }} onClick={() => setForm({ ...form, size_gib: g })}>{g}</button>)}
          </div>
        </Field>
        <Field label="File System" required hint={form.filesystem === "ext4" ? "For Linux groups" : "For Windows groups"}>
          <Select value={form.filesystem || "ntfs"} aria-label="File System" onChange={v => setForm({ ...form, filesystem: v })} options={Object.entries(FS_META).map(([value, m]) => ({ value, label: m.t }))}/>
        </Field>
        <Field label="Volume Label" hint={`Disk label shown on client, e.g. "${(form.label || form.name || "Games").trim() || "Games"} (F:)". Leave blank to use disk name. Set the drive letter under Mount Target in Group Management.`}>
          <input className="input" aria-label="Volume Label" style={{ width: "100%" }} value={form.label || ""} onChange={e => setForm({ ...form, label: e.target.value })} placeholder="Blank = disk name"/>
        </Field>
      </Modal>

      {conf.node}
    </div>
  );
}

// Image health card (GET /api/images/{id}/health, POST .../health-check).
const HEALTH_META = {
  ok: { label: "Healthy", chip: "emerald" },
  warn: { label: "Warning", chip: "amber" },
  block: { label: "Blocked", chip: "rose" },
  unknown: { label: "Unknown", chip: "" },
};

function ImageHealthCard({ imageId }) {
  const store = useStore();
  const [report, setReport] = useState(null);
  const [missing, setMissing] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const { busy: running, run } = useMutation(store.toast);

  const idRef = useRef(imageId);
  const load = async () => {
    try {
      setReport(await api.getImageHealth(imageId));
      setMissing(false);
    } catch {
      setReport(null);
      setMissing(true);
    }
  };
  useEffect(() => { idRef.current = imageId; setReport(null); setMissing(false); setExpanded(false); load(); }, [imageId]);

  // Health checks run as tasks; wait for completion before showing an updated report.
  const rerun = () => run(
    () => api.runImageHealthCheck(imageId),
    "Health check started",
    {
      errMsg: "Health check could not start",
      reload: async () => {
        if (idRef.current !== imageId) return; // Image changed during the check
        await load();
      },
    },
  );

  const meta = report ? (HEALTH_META[report.Level] || HEALTH_META.unknown) : null;
  return (
    <div style={{ background: "var(--bg-0)", border: "1px solid var(--line-soft)", borderRadius: 8, padding: "10px 12px", marginBottom: 14 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <span className="lbl">Image Health Check</span>
        {report && <span className={`chip ${meta.chip}`}>{meta.label}</span>}
        {report?.PartitionStyle && <span className="chip mono">{report.PartitionStyle.toUpperCase()}</span>}
        {missing && <span className="chip">No report</span>}
        <div style={{ flex: 1 }}/>
        {report && (report.Items || []).length > 0 && (
          <button className="btn ghost" style={{ padding: "2px 8px" }} onClick={() => setExpanded(!expanded)}>
            {expanded ? "Collapse" : `Details: ${(report.Items || []).length}`}
          </button>
        )}
        <button className="btn" style={{ padding: "2px 8px" }} disabled={running} onClick={rerun}>
          <Icons.Refresh size={11}/> {running ? "Checking…" : "Run Health Check"}
        </button>
      </div>
      {expanded && report && (
        <div style={{ marginTop: 10, display: "flex", flexDirection: "column", gap: 6 }}>
          {(report.Items || []).map(item => {
            const m = HEALTH_META[item.level] || HEALTH_META.unknown;
            return (
              <div key={item.name} style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
                <span className={`chip ${m.chip}`} style={{ flexShrink: 0 }}>{m.label}</span>
                <div>
                  <span className="mono" style={{ fontSize: 12, color: "var(--fg-mute)" }}>{item.name}</span>
                  <div className="hint">{item.detail}</div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

// Image details summarize configurations and active restore points; management actions live on the Restore Points tab.
function ConfigOverview({ image, onManage }) {
  const { data } = useResource(async () => {
    const configs = ((await api.listConfigs(image.ID).catch(() => null)) || {}).items || [];
    const reds = await Promise.all(configs.map(c => api.listReductions(c.ID).then(r => r.items || []).catch(() => [])));
    const usersOf = await loadConfigUsers();
    return configs.map((c, i) => {
      return { ...c, current: reds[i].find(r => r.ID === c.DefaultReductionID), count: reds[i].length, users: usersOf(c.ID) };
    });
  }, [image.ID]);
  const rows = data || [];
  return (
    <div style={{ marginTop: 14 }}>
      <div className="lbl" style={{ marginBottom: 8 }}>Configuration Overview</div>
      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        {rows.map(c => (
          <div key={c.ID} className="row" style={{ gap: 10, border: "1px solid var(--line-soft)", borderRadius: 6, padding: "8px 10px", background: "var(--bg-0)", fontSize: 13 }}>
            <Icons.Cog size={12} style={{ color: "var(--fg-mute)" }}/>
            <span style={{ fontWeight: 600 }}>{c.Name}</span>
            <span className="meta">Current restore point {c.current ? redName(c.current) : "—"} · Total {c.count} items</span>
            <span className="meta ellip" style={{ flex: 1 }}><UsageCell users={c.users} empty="No groups use this configuration"/></span>
            <button className="btn ghost" style={{ padding: "2px 8px" }} onClick={() => onManage(c)} aria-label={`Manage Restore Points ${c.Name}`}>Manage Restore Points<Icons.Chevron size={11}/></button>
          </div>
        ))}
        {data && !rows.length && <div className="hint">No configurations. Create one under Configurations.</div>}
      </div>
    </div>
  );
}

window.PageImages = PageImages;

export { PageImages };
