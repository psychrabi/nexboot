import React from "react";
import { Icons } from "../icons.jsx";
import { useStore } from "../store.jsx";
import { Modal, useConfirm } from "../overlay.jsx";
import { Field, Select, TableState, Pager } from "../primitives.jsx";
import { api } from "../../lib/api.js";
import {
  useResource,
  useMutation,
  usePolling,
  usePaged,
} from "../../lib/hooks.js";

const { useState, useEffect } = React;

const CATEGORY_LABELS = {
  boot_critical_nic: "Boot-critical NIC",
  nic: "Network Adapter",
  gpu: "Graphics Adapter",
  audio: "Audio Adapter",
  chipset: "Chipset",
  other: "Other",
};
const RISK_LABELS = {
  unsigned: "Unsigned",
  no_hwids: "No HWIDs",
  outdated: "Outdated Version",
};

function PageDrivers() {
  const store = useStore();
  const conf = useConfirm();
  const { route } = store;
  // The packs, bundles, and cure tabs support deep links from client details into driver finalization.
  const [tab, setTab] = useState(route.params.tab || "packs");
  useEffect(() => {
    if (route.params.tab) setTab(route.params.tab);
  }, [route.params.tab]);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [bundleOpen, setBundleOpen] = useState(false);
  const [uploadForm, setUploadForm] = useState({
    name: "",
    category: "boot_critical_nic",
    os_type: "windows",
    arch: "x64",
    file: null,
  });
  const [bundleForm, setBundleForm] = useState({ name: "", packIds: [] });
  const [q, setQ] = useState("");
  const [catFilter, setCatFilter] = useState("all");

  // Settle requests separately so a failed bundle query does not clear the driver pack table.
  const { data, loading, error, reload } = useResource(async () => {
    const [packRes, bundleRes, termRes] = await Promise.allSettled([
      api.listDriverPacks(),
      api.listDriverBundles(),
      api.listTerminals(),
    ]);
    if (packRes.status === "rejected") throw packRes.reason;
    return {
      packs: (packRes.value && packRes.value.items) || [],
      bundles:
        bundleRes.status === "fulfilled"
          ? (bundleRes.value && bundleRes.value.items) || []
          : [],
      terminals:
        termRes.status === "fulfilled"
          ? (termRes.value && termRes.value.items) || []
          : [],
    };
  }, []);
  const packs = (data && data.packs) || [];
  const bundles = (data && data.bundles) || [];
  const terminals = (data && data.terminals) || [];
  const superTerminal = terminals.find((t) => t.IsSuper) || null;
  const curePending = !!(superTerminal && superTerminal.PendingBundleID);
  const { busy, run } = useMutation(store.toast);

  const filteredPacks = packs.filter(
    ({ pack }) =>
      (catFilter === "all" || pack.Category === catFilter) &&
      (!q ||
        pack.Name.toLowerCase().includes(q.toLowerCase()) ||
        (pack.Vendor || "").toLowerCase().includes(q.toLowerCase())),
  );
  const packPager = usePaged(filteredPacks);
  const bundlePager = usePaged(bundles);

  const doUpload = async () => {
    if (!uploadForm.file) {
      store.toast("Select a driver ZIP file", "err");
      return;
    }
    const fd = new FormData();
    fd.append("file", uploadForm.file);
    fd.append("name", uploadForm.name);
    fd.append("category", uploadForm.category);
    fd.append("os_type", uploadForm.os_type);
    fd.append("arch", uploadForm.arch);
    if (
      await run(
        () => api.uploadDriverPack(fd),
        "Driver pack uploaded and parsed",
        { reload },
      )
    ) {
      setUploadOpen(false);
      setUploadForm({
        name: "",
        category: "boot_critical_nic",
        os_type: "windows",
        arch: "x64",
        file: null,
      });
    }
  };

  const toggleStatus = (pack) => {
    const next = pack.Status === "enabled" ? "disabled" : "enabled";
    return run(
      () => api.setDriverPackStatus(pack.ID, next),
      next === "enabled" ? "Driver pack enabled" : "Driver pack disabled",
      { reload },
    );
  };

  const recommend = (pack) =>
    run(() => api.recommendDriverPack(pack.ID), "Set as recommended version", {
      reload,
    });

  const removePack = async (pack) => {
    const ok = await conf.ask({
      title: "Delete Driver Pack",
      message: `Delete ${pack.Name}? Packages referenced by a bundle cannot be deleted.`,
      danger: true,
    });
    if (ok)
      await run(() => api.deleteDriverPack(pack.ID), "Driver pack deleted", {
        reload,
      });
  };

  const bootPacks = packs.filter(
    (p) =>
      p.pack.Category === "boot_critical_nic" && p.pack.Status === "enabled",
  );

  const doCreateBundle = async () => {
    if (!bundleForm.name.trim() || bundleForm.packIds.length === 0) {
      store.toast(
        "Enter a name and select at least one boot-critical NIC driver pack",
        "err",
      );
      return;
    }
    if (
      await run(
        () =>
          api.createDriverBundle({
            name: bundleForm.name.trim(),
            pack_ids: bundleForm.packIds,
          }),
        "Boot driver bundle created",
        { reload },
      )
    ) {
      setBundleOpen(false);
      setBundleForm({ name: "", packIds: [] });
    }
  };

  const removeBundle = async (bundle) => {
    const ok = await conf.ask({
      title: "Delete Driver Bundle",
      message: `Delete ${bundle.Name}？`,
      danger: true,
    });
    if (ok)
      await run(
        () => api.deleteDriverBundle(bundle.ID),
        "Driver bundle deleted",
        { reload },
      );
  };

  const downloadBundle = async (bundle) => {
    try {
      const res = await api.downloadDriverBundle(bundle.ID);
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${bundle.Name}.zip`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      store.toast(e.message, "err");
    }
  };

  return (
    <div
      className="fade-in"
      style={{ display: "flex", flexDirection: "column", gap: 14 }}
    >
      <div className="tabs">
        <div
          className={`tab ${tab === "packs" ? "active" : ""}`}
          onClick={() => setTab("packs")}
        >
          Driver Pack <span className="count">{packs.length}</span>
        </div>
        <div
          className={`tab ${tab === "bundles" ? "active" : ""}`}
          onClick={() => setTab("bundles")}
        >
          Boot Driver Bundles <span className="count">{bundles.length}</span>
        </div>
        <div
          className={`tab ${tab === "cure" ? "active" : ""}`}
          onClick={() => setTab("cure")}
          style={{ display: "flex", alignItems: "center", gap: 6 }}
        >
          Driver Finalization{" "}
          {curePending && (
            <span className="dot warn" title="Finalization in progress" />
          )}
        </div>
      </div>

      {tab === "packs" && (
        <div className="card" style={{ padding: 0 }}>
          <div className="card-h">
            <div className="row" style={{ gap: 10 }}>
              <input
                className="input"
                placeholder="Search name / vendor"
                style={{ width: 200 }}
                value={q}
                onChange={(e) => {
                  setQ(e.target.value);
                  packPager.setPage(1);
                }}
              />
              <Select
                value={catFilter}
                onChange={(v) => {
                  setCatFilter(v);
                  packPager.setPage(1);
                }}
                style={{ width: 130 }}
                options={[
                  { value: "all", label: "All Categories" },
                  ...Object.entries(CATEGORY_LABELS).map(([value, label]) => ({
                    value,
                    label,
                  })),
                ]}
              />
              {(q || catFilter !== "all") && (
                <span className="chip">
                  {filteredPacks.length} / {packs.length}
                </span>
              )}
            </div>
            <button className="btn primary" onClick={() => setUploadOpen(true)}>
              <Icons.Upload size={12} /> Upload Driver Pack
            </button>
          </div>
          <table className="t">
            <thead>
              <tr>
                <th>Name</th>
                <th>Category</th>
                <th>Vendor</th>
                <th>Version</th>
                <th>Date</th>
                <th>HWID</th>
                <th>Signature</th>
                <th>Risk</th>
                <th>Status</th>
                <th className="t-actions"></th>
              </tr>
            </thead>
            <tbody>
              <TableState
                colSpan={10}
                loading={loading}
                error={error}
                empty={!filteredPacks.length}
                hint={
                  packs.length
                    ? "No matching driver packs. Adjust the filter or query."
                    : "No driver packs. Upload a ZIP containing an INF file."
                }
              />
              {packPager.rows.map(({ pack, risks }) => (
                <tr key={pack.ID}>
                  <td>
                    <div
                      style={{ display: "flex", alignItems: "center", gap: 6 }}
                    >
                      <span style={{ fontWeight: 500 }}>{pack.Name}</span>
                      {pack.Recommended && (
                        <span
                          className="chip cyan"
                          style={{ padding: "0 6px" }}
                        >
                          Recommended
                        </span>
                      )}
                    </div>
                  </td>
                  <td>
                    <span
                      className={`chip ${pack.Category === "boot_critical_nic" ? "amber" : ""}`}
                    >
                      {CATEGORY_LABELS[pack.Category] || pack.Category}
                    </span>
                  </td>
                  <td className="muted">{pack.Vendor || "—"}</td>
                  <td className="mono" style={{ fontSize: 12 }}>
                    {pack.Version || "—"}
                  </td>
                  <td className="mono muted" style={{ fontSize: 12 }}>
                    {pack.ReleaseDate || "—"}
                  </td>
                  <td
                    title={(pack.HWIDs || []).join("\n")}
                    className="mono"
                    style={{ fontSize: 12 }}
                  >
                    {(pack.HWIDs || []).length}
                  </td>
                  <td>
                    {pack.Signed ? (
                      <span className="chip emerald">Signed</span>
                    ) : (
                      <span className="chip rose">Unverified</span>
                    )}
                  </td>
                  <td>
                    {(risks || []).length === 0 ? (
                      <span className="muted">—</span>
                    ) : (
                      (risks || []).map((r) => (
                        <span
                          key={r}
                          className="chip rose"
                          style={{ marginRight: 4 }}
                        >
                          {RISK_LABELS[r] || r}
                        </span>
                      ))
                    )}
                  </td>
                  <td>
                    {pack.Status === "enabled" ? (
                      <span className="chip emerald">Enable</span>
                    ) : (
                      <span className="chip">Disable</span>
                    )}
                  </td>
                  <td className="t-actions">
                    <div>
                      <button
                        className="btn"
                        style={{ padding: "3px 8px" }}
                        onClick={() => toggleStatus(pack)}
                      >
                        {pack.Status === "enabled" ? "Disable" : "Enable"}
                      </button>
                      {!pack.Recommended && (
                        <button
                          className="btn"
                          style={{ padding: "3px 8px" }}
                          onClick={() => recommend(pack)}
                        >
                          Recommend
                        </button>
                      )}
                      <button
                        className="btn ghost icon danger"
                        onClick={() => removePack(pack)}
                        title="Delete Driver Pack"
                        aria-label={`Delete Driver Pack ${pack.Name}`}
                      >
                        <Icons.Trash size={12} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <Pager {...packPager.pager} />
        </div>
      )}

      {tab === "bundles" && (
        <div className="card" style={{ padding: 0 }}>
          <div className="card-h">
            <div className="card-title">
              <Icons.Layers size={14} /> Boot Driver Bundles{" "}
              <span className="count">{bundles.length}</span>
            </div>
            <button className="btn" onClick={() => setBundleOpen(true)}>
              <Icons.Plus size={12} /> Create Bundle
            </button>
          </div>
          <table className="t">
            <thead>
              <tr>
                <th>Name</th>
                <th>OS</th>
                <th>Member Packages</th>
                <th>Created At</th>
                <th className="t-actions"></th>
              </tr>
            </thead>
            <tbody>
              <TableState
                colSpan={5}
                loading={loading}
                error={error}
                empty={!bundles.length}
                hint="No bundles. Create one from boot-critical NIC drivers for Super Admin adaptation."
              />
              {bundlePager.rows.map(({ bundle, packs: memberPacks }) => (
                <tr key={bundle.ID}>
                  <td style={{ fontWeight: 500 }}>{bundle.Name}</td>
                  <td className="muted">
                    {bundle.OSType === "windows" ? "Windows" : "Linux"}
                  </td>
                  <td className="muted" style={{ fontSize: 12 }}>
                    {(memberPacks || []).map((p) => p.Name).join("、") || "—"}
                  </td>
                  <td className="mono muted" style={{ fontSize: 12 }}>
                    {(bundle.CreatedAt || "").slice(0, 10)}
                  </td>
                  <td className="t-actions">
                    <div>
                      <button
                        className="btn"
                        style={{ padding: "3px 8px" }}
                        onClick={() => downloadBundle(bundle)}
                      >
                        <Icons.Download size={11} /> Download
                      </button>
                      <button
                        className="btn ghost icon danger"
                        onClick={() => removeBundle(bundle)}
                        title="Delete Driver Bundle"
                        aria-label={`Delete Driver Bundle ${bundle.Name}`}
                      >
                        <Icons.Trash size={12} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <Pager {...bundlePager.pager} />
        </div>
      )}

      {tab === "cure" && (
        <DriverCureCard
          bundles={bundles}
          superTerminal={superTerminal}
          conf={conf}
          reload={reload}
        />
      )}

      <Modal
        open={uploadOpen}
        onClose={() => setUploadOpen(false)}
        title="Upload Driver Pack"
        footer={
          <>
            <button className="btn" onClick={() => setUploadOpen(false)}>
              Cancel
            </button>
            <button className="btn primary" disabled={busy} onClick={doUpload}>
              {busy ? "Parsing…" : "Upload and Parse"}
            </button>
          </>
        }
      >
        <Field
          label="Driver ZIP File"
          required
          hint="ZIP must contain at least one .inf. The server parses vendor, version, HWIDs, and signature status."
        >
          <input
            className="input"
            type="file"
            accept=".zip"
            style={{ width: "100%" }}
            onChange={(e) =>
              setUploadForm({ ...uploadForm, file: e.target.files[0] || null })
            }
          />
        </Field>
        <Field label="Name" hint="Leave blank to use the filename">
          <input
            className="input"
            style={{ width: "100%" }}
            value={uploadForm.name}
            onChange={(e) =>
              setUploadForm({ ...uploadForm, name: e.target.value })
            }
            placeholder="Intel I219 v12.19"
          />
        </Field>
        <Field label="Category" required>
          <Select
            value={uploadForm.category}
            onChange={(v) => setUploadForm({ ...uploadForm, category: v })}
            options={Object.entries(CATEGORY_LABELS).map(([value, label]) => ({
              value,
              label,
            }))}
          />
        </Field>
        <Field label="Client OS">
          <Select
            value={uploadForm.os_type}
            onChange={(v) => setUploadForm({ ...uploadForm, os_type: v })}
            options={[
              { value: "windows", label: "Windows" },
              { value: "linux", label: "Linux" },
            ]}
          />
        </Field>
      </Modal>

      <Modal
        open={bundleOpen}
        onClose={() => setBundleOpen(false)}
        title="Create Boot Driver Bundle"
        footer={
          <>
            <button className="btn" onClick={() => setBundleOpen(false)}>
              Cancel
            </button>
            <button
              className="btn primary"
              disabled={busy}
              onClick={doCreateBundle}
            >
              Create
            </button>
          </>
        }
      >
        <Field label="Name" required>
          <input
            className="input"
            style={{ width: "100%" }}
            value={bundleForm.name}
            onChange={(e) =>
              setBundleForm({ ...bundleForm, name: e.target.value })
            }
            placeholder="2026 Common NIC Boot Drivers"
          />
        </Field>
        <Field
          label="Boot-critical NIC Driver Packs"
          required
          hint="Only enabled boot_critical_nic packages are listed"
        >
          {bootPacks.length === 0 ? (
            <div className="muted" style={{ fontSize: 13 }}>
              No enabled boot-critical NIC driver packs. Upload and classify one
              first.
            </div>
          ) : (
            bootPacks.map(({ pack }) => (
              <label
                key={pack.ID}
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                  padding: "6px 0",
                  cursor: "pointer",
                  fontSize: 13,
                }}
              >
                <input
                  type="checkbox"
                  checked={bundleForm.packIds.includes(pack.ID)}
                  onChange={(e) =>
                    setBundleForm((f) => ({
                      ...f,
                      packIds: e.target.checked
                        ? [...f.packIds, pack.ID]
                        : f.packIds.filter((id) => id !== pack.ID),
                    }))
                  }
                />
                <span>{pack.Name}</span>
                <span className="mono muted meta">
                  {(pack.HWIDs || []).length} HWID
                </span>
                {!pack.Signed && <span className="chip rose">Unsigned</span>}
              </label>
            ))
          )}
        </Field>
      </Modal>
      {conf.node}
    </div>
  );
}

// Driver finalization on the sole Super Admin client follows Inject → Verify → Commit. Both the source bundles and
// new restore points belong to this workflow; the client details page only links here.
const pad2 = (n) => String(n).padStart(2, "0");
const fmtTime = (iso) => {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d)) return "—";
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
};

function DriverCureCard({ bundles, superTerminal, conf, reload }) {
  const store = useStore();
  const { busy, run } = useMutation(store.toast);
  const [bundleId, setBundleId] = useState("");
  const [reductionName, setReductionName] = useState("");
  const [result, setResult] = useState(null); // Content returned by injectResult
  const [checkError, setCheckError] = useState("");
  const [curing, setCuring] = useState(false); // stop-super task submitted

  const pendingId = superTerminal?.PendingBundleID || null;
  const pendingAt = superTerminal?.PendingBundleAt || null;
  const pending = !!(superTerminal && pendingId);
  const bundleName = (id) =>
    bundles.find((b) => b.bundle.ID === id)?.bundle.Name || id;
  const superLabel = superTerminal
    ? `${superTerminal.Name || superTerminal.ID}（${superTerminal.MAC}）`
    : "";

  const checkInstallResult = async () => {
    if (!superTerminal) return;
    try {
      setResult(await api.injectResult(superTerminal.ID));
      setCheckError("");
    } catch (e) {
      setCheckError(e.message || "Verification failed");
    }
  };

  useEffect(() => {
    setResult(null);
    setCheckError("");
    setCuring(false);
    if (pending) checkInstallResult();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [superTerminal?.ID, pendingId, pendingAt]);

  // Step 2: poll for driver installation results; the client may still be booting, so manual checks are insufficient.
  usePolling(checkInstallResult, {
    intervalMs: 5000,
    active: pending && !(result && result.done),
  });

  // Track finalization while waiting for shutdown and restore-point creation; return to step 1 once Super Admin mode ends.
  usePolling(reload, { intervalMs: 5000, active: curing });

  const doInject = async () => {
    if (!bundleId) {
      store.toast("Select a boot driver bundle", "err");
      return;
    }
    if (pendingId) {
      const sameBundle = pendingId === bundleId;
      const ok = await conf.ask({
        title: "Overwrite Uncommitted Driver Changes",
        message: `Previously injected "${bundleName(pendingId)}" (${fmtTime(superTerminal.PendingBundleAt)}), but changes have not been saved as a restore point. Continuing to ${sameBundle ? "re-" : ""}inject will recreate the Super Admin system disk with unsaved changes. Only the most recently injected bundle will apply on the next boot. Continue?`,
        danger: true,
        confirmText: "Overwrite Injection Anyway",
      });
      if (!ok) return;
    }
    if (
      await run(
        () =>
          api.injectDriver(superTerminal.ID, {
            bundle_id: bundleId,
            overwrite: pending,
          }),
        "Driver bundle injected into the Super Admin clone",
        { reload },
      )
    ) {
      setBundleId("");
      setResult(null);
      setCheckError("");
    }
  };

  const doCure = async () => {
    if (!reductionName.trim()) {
      store.toast("Enter a restore point name", "err");
      return;
    }
    if (!result?.done || !result.ok) {
      store.toast(
        "Complete boot verification successfully before finalization",
        "err",
      );
      return;
    }
    if (
      await run(
        () =>
          api.stopSuper(superTerminal.ID, {
            reduction_name: reductionName.trim(),
          }),
        "Save-restore-point task submitted; it will wait for client shutdown before committing",
        { reload },
      )
    ) {
      setReductionName("");
      setCuring(true);
    }
  };

  const active = !pending ? 1 : result?.done && result.ok ? 3 : 2;
  return (
    <div
      className="card"
      style={{
        padding: 0,
        borderColor: pending ? "oklch(0.55 0.1 80 / 0.5)" : undefined,
      }}
    >
      <div className="card-h">
        <div className="card-title">
          <Icons.Snapshot size={14} /> Driver Finalization · Inject into Super
          Admin
          {pending && (
            <span className="chip amber" style={{ marginLeft: 8 }}>
              Session in progress
            </span>
          )}
        </div>
        {superTerminal && (
          <span className="chip violet">Super Admin: {superLabel}</span>
        )}
      </div>
      {!superTerminal ? (
        <div
          style={{
            padding: 18,
            fontSize: 13,
            color: "var(--fg-mute)",
            lineHeight: 1.7,
          }}
        >
          To commit a boot driver bundle into a system image, use a{" "}
          <b>Super Admin client</b>: inject drivers, boot for automatic
          installation, then shut down and save a restore point. Assign that
          restore point to the group to deploy the drivers.
          <div style={{ marginTop: 8 }}>
            <span
              className="row"
              style={{
                color: "var(--cyan)",
                cursor: "pointer",
                display: "inline-flex",
              }}
              onClick={() => store.goto("terminals")}
            >
              Go to Client Management to set a client as Super Admin
              <Icons.Chevron size={11} />
            </span>
          </div>
        </div>
      ) : (
        <div
          style={{
            padding: 16,
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(240px, 1fr))",
            gap: 12,
          }}
        >
          <CureStep
            n={1}
            title="Inject Driver Bundle"
            state={pending ? "done" : active === 1 ? "active" : "idle"}
          >
            {pending && (
              <div style={{ marginBottom: 8 }}>
                Injected <b>{bundleName(pendingId)}</b>
                <div className="mono meta" style={{ marginTop: 2 }}>
                  {fmtTime(superTerminal.PendingBundleAt)}
                </div>
              </div>
            )}
            <Select
              value={bundleId}
              onChange={setBundleId}
              options={[
                {
                  value: "",
                  label: bundles.length
                    ? "Select bundle…"
                    : "No bundles — create one above",
                },
                ...bundles.map((b) => ({
                  value: b.bundle.ID,
                  label: `${b.bundle.Name}（${(b.packs || []).length} Driver Pack）`,
                })),
              ]}
            />
            <button
              className="btn primary"
              disabled={busy || !bundles.length}
              style={{ marginTop: 8 }}
              onClick={doInject}
            >
              {busy
                ? "Injecting…"
                : pending
                  ? "Overwrite Injection"
                  : "Offline Injection"}
            </button>
            {pending && (
              <div className="hint" style={{ marginTop: 6 }}>
                Choose another bundle to replace the pending uncommitted
                injection.
              </div>
            )}
          </CureStep>
          <CureStep
            n={2}
            title="Boot Verification"
            state={
              !pending
                ? "idle"
                : result?.done
                  ? result.ok
                    ? "done"
                    : "error"
                  : checkError
                    ? "error"
                    : "active"
            }
            done={
              (result?.done &&
                (result.ok ? (
                  <span className="row" style={{ color: "var(--emerald)" }}>
                    <Icons.Check size={12} /> Driver installation succeeded
                  </span>
                ) : (
                  <span
                    className="row"
                    style={{
                      color: "var(--rose)",
                      whiteSpace: "pre-wrap",
                      alignItems: "flex-start",
                    }}
                  >
                    <Icons.X
                      size={12}
                      style={{ flexShrink: 0, marginTop: 2 }}
                    />{" "}
                    Installation failed · {result.log}
                  </span>
                ))) ||
              (checkError && (
                <span style={{ color: "var(--rose)" }}>
                  Verification failed · {checkError}
                </span>
              ))
            }
          >
            {!pending ? (
              <span style={{ color: "var(--fg-faint)" }}>
                After injection, boot the Super Admin client once to install
                drivers automatically. It will not shut down automatically;
                verify installation on the client before shutting down.
              </span>
            ) : (
              <span>
                Waiting for the Super Admin client to boot and install drivers…
                <span className="dot cyan" style={{ marginLeft: 6 }} />
                <div className="hint" style={{ marginTop: 6 }}>
                  Checking installation results every 5 seconds
                </div>
              </span>
            )}
            {result?.done && result.ok && result.log && (
              <div
                className="mono hint"
                style={{ marginTop: 6, whiteSpace: "pre-wrap" }}
              >
                {result.log}
              </div>
            )}
          </CureStep>
          <CureStep
            n={3}
            title="Shut Down and Save Restore Point"
            state={!pending ? "idle" : active === 3 ? "active" : "idle"}
          >
            {!pending ? (
              <span style={{ color: "var(--fg-faint)" }}>
                After verification, make sure the Super Admin client has shut
                down, then save the changes as a new restore point.
              </span>
            ) : (
              <>
                <input
                  className="input"
                  style={{ width: "100%" }}
                  value={reductionName}
                  onChange={(e) => setReductionName(e.target.value)}
                  placeholder="Restore point name, e.g. 2026Q3-Driver Update"
                />
                <button
                  className="btn primary"
                  disabled={busy || curing || !result?.done || !result.ok}
                  style={{ marginTop: 8 }}
                  onClick={doCure}
                >
                  {curing
                    ? "Finalization task running…"
                    : busy
                      ? "Submitting…"
                      : "Shut Down and Save Restore Point"}
                </button>
                <div className="hint" style={{ marginTop: 6 }}>
                  The Super Admin client must be off. The task waits up to 2
                  minutes.
                </div>
              </>
            )}
          </CureStep>
        </div>
      )}
    </div>
  );
}

function CureStep({ n, title, state, done, children }) {
  const border =
    state === "error"
      ? "var(--rose)"
      : state === "active"
        ? "var(--cyan)"
        : state === "done"
          ? "oklch(0.55 0.1 155 / 0.5)"
          : "var(--line-soft)";
  const chipClass =
    state === "error"
      ? "rose"
      : state === "done"
        ? "emerald"
        : state === "active"
          ? "cyan"
          : "";
  return (
    <div
      style={{
        border: `1px solid ${border}`,
        borderRadius: 8,
        padding: 12,
        background:
          state === "done"
            ? "var(--emerald-soft)"
            : state === "error"
              ? "var(--rose-soft)"
              : "var(--bg-0)",
        opacity: state === "idle" ? 0.65 : 1,
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          marginBottom: 8,
        }}
      >
        <span className={`chip ${chipClass}`}>
          {state === "done" ? (
            <Icons.Check size={10} />
          ) : state === "error" ? (
            <Icons.X size={10} />
          ) : (
            n
          )}
        </span>
        <span style={{ fontSize: 13, fontWeight: 600 }}>{title}</span>
      </div>
      <div style={{ fontSize: 12, lineHeight: 1.6 }}>{done || children}</div>
    </div>
  );
}

window.PageDrivers = PageDrivers;

export { PageDrivers };
