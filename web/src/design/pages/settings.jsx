// The backend User has only ID, Username, and CreatedAt; omit unsupported columns and tabs.
import React from 'react';
import { Icons } from '../icons.jsx';
import { useStore } from '../store.jsx';
import { Modal, useConfirm } from '../overlay.jsx';
import { Field, SearchBox, Select, TableState, Pager } from '../primitives.jsx';
import { api } from '../../lib/api.js';
import { useResource, useMutation, usePaged } from '../../lib/hooks.js';
import { HACard } from '../ha.jsx';
const { useState, useEffect } = React;

const NET_STATUS = {
  same:    { chip: "emerald", text: "Same Subnet" },
  relay:   { chip: "amber",   text: "Cross-subnet · Relay" },
  blocked: { chip: "rose",    text: "Unreachable" },
  unknown: { chip: "",        text: "Unknown" },
};

const getImportDir = (settings) => (settings && (settings.import_dir || settings.ImportDir)) || "";
const getDefaultImportDir = (settings) => (settings && (settings.default_import_dir || settings.DefaultImportDir)) || "/var/lib/ndiskless/imports";
const isAbsolutePath = (v) => /^\//.test((v || "").trim());
const isVarPath = (v) => /^\/var(\/|$)/.test((v || "").trim());

function PageSettings() {
  const store = useStore();
  const conf = useConfirm();
  const [q, setQ] = useState("");
  const [modal, setModal] = useState(null); // add | rename | password
  const [form, setForm] = useState({});

  let currentUsername = null;
  try { currentUsername = JSON.parse(localStorage.getItem("nd_user") || "null")?.username; } catch { /* ignore */ }

  const { data, loading, error, reload: load } = useResource(() => api.listUsers(), []);
  const users = (data && data.items) || (Array.isArray(data) ? data : []);
  const { busy, run } = useMutation(store.toast);

  const filtered = users.filter(u => !q || u.username.toLowerCase().includes(q.toLowerCase()));
  const paged = usePaged(filtered);

  const openAdd = () => { setForm({ username: "", password: "" }); setModal("add"); };
  const openRename = (u) => { setForm({ id: u.id, username: u.username }); setModal("rename"); };
  const openPassword = (u) => { setForm({ id: u.id, username: u.username, old_password: "", new_password: "" }); setModal("password"); };

  const doAdd = async () => {
    if (!form.username?.trim()) { store.toast("Enter a username", "err"); return; }
    if (!form.password || form.password.length < 6) { store.toast("The initial password must have at least 6 characters", "err"); return; }
    if (await run(() => api.createUser({ username: form.username.trim(), password: form.password }),
      `User ${form.username.trim()} created`, { reload: load, errMsg: "Creation failed" })) setModal(null);
  };
  const doRename = async () => {
    if (!form.username?.trim()) { store.toast("Enter a username", "err"); return; }
    if (await run(() => api.updateUser(form.id, { username: form.username.trim() }),
      "Username updated", { reload: load, errMsg: "Update failed" })) setModal(null);
  };
  const doPassword = async () => {
    if (!form.old_password || !form.new_password) { store.toast("Enter both the current and new passwords", "err"); return; }
    if (form.new_password.length < 6) { store.toast("The new password must have at least 6 characters", "err"); return; }
    if (await run(() => api.changeUserPassword(form.id, { old_password: form.old_password, new_password: form.new_password }),
      `Changed ${form.username}'s password`, { errMsg: "Change failed" })) setModal(null);
  };
  const del = async (u) => {
    const ok = await conf.ask({ title: "Delete User", message: `Delete user ${u.username}？`, danger: true });
    if (!ok) return;
    await run(() => api.deleteUser(u.id), "User deleted", { reload: load, errMsg: "Deletion failed" });
  };

  return (
    <div className="fade-in" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <div className="card" style={{ padding: "10px 14px", display: "flex", alignItems: "center", gap: 12 }}>
        <SearchBox value={q} onChange={setQ} placeholder="Search usernames"/>
        <span className="chip">{users.length} users</span>
        <div style={{ flex: 1 }}/>
        <button className="btn ghost icon" onClick={() => load()} title="Refresh now" aria-label="Refresh now"><Icons.Refresh size={13}/></button>
        <button className="btn primary" onClick={openAdd}><Icons.Plus size={12}/> Create User</button>
      </div>

      <div className="card" style={{ padding: 0 }}>
        <table className="t">
          <thead><tr><th>User ID</th><th>Username</th><th className="t-actions">Actions</th></tr></thead>
          <tbody>
            {paged.rows.map(u => (
              <tr key={u.id}>
                <td className="mono muted" style={{ fontSize: 12 }}>{u.id}</td>
                <td>
                  <div className="row" style={{ gap: 8 }}>
                    <div style={{ width: 24, height: 24, borderRadius: "50%", background: u.username === "admin" ? "linear-gradient(135deg,var(--cyan),var(--violet))" : "var(--bg-3)", display: "grid", placeItems: "center", fontSize: 11, fontWeight: 600, color: u.username === "admin" ? "oklch(0.18 0.014 250)" : "var(--fg)" }}>{u.username.charAt(0).toUpperCase()}</div>
                    <span className="mono" style={{ fontWeight: 500 }}>{u.username}</span>
                    {u.username === currentUsername && <span className="chip cyan">Current</span>}
                  </div>
                </td>
                <td className="t-actions"><div>
                  <button className="btn" style={{ padding: "3px 8px" }} onClick={() => openRename(u)}>Rename</button>
                  <button className="btn" style={{ padding: "3px 8px" }} onClick={() => openPassword(u)}><Icons.Key size={11}/> Change Password</button>
                  {u.username !== "admin" && <button className="btn ghost icon danger" onClick={() => del(u)} title="Delete User" aria-label={`Delete User ${u.username}`}><Icons.Trash size={12}/></button>}
                </div></td>
              </tr>
            ))}
            <TableState colSpan={3} loading={loading} error={error} empty={!filtered.length} hint={users.length ? "No matching users" : "No users"}/>
          </tbody>
        </table>
        <Pager {...paged.pager}/>
      </div>

      <div className="hint" style={{ padding: "0 4px" }}>
        Role permissions and menu management are not yet supported by the backend.
      </div>

      {/* Create user */}
      <Modal open={modal === "add"} onClose={() => !busy && setModal(null)} title="Create User" size="sm"
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={doAdd}>{busy ? "Creating…" : "Create"}</button></>}>
        <Field label="Username" required><input className="input mono" style={{ width: "100%" }} value={form.username || ""} onChange={e => setForm({ ...form, username: e.target.value })} placeholder="operator"/></Field>
        <Field label="Initial Password" required hint="At least 6 characters"><input className="input mono" type="password" style={{ width: "100%" }} value={form.password || ""} onChange={e => setForm({ ...form, password: e.target.value })}/></Field>
      </Modal>

      {/* Rename */}
      <Modal open={modal === "rename"} onClose={() => !busy && setModal(null)} title="Rename User" size="sm"
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={doRename}>{busy ? "Saving…" : "Save"}</button></>}>
        <Field label="Username" required><input className="input mono" style={{ width: "100%" }} value={form.username || ""} onChange={e => setForm({ ...form, username: e.target.value })}/></Field>
      </Modal>

      {/* Change Password */}
      <Modal open={modal === "password"} onClose={() => !busy && setModal(null)} title={`Change Password · ${form.username || ""}`} size="sm"
        footer={<><button className="btn" disabled={busy} onClick={() => setModal(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={doPassword}>{busy ? "Submitting…" : "Confirm"}</button></>}>
        <Field label="Current Password" required><input className="input mono" type="password" style={{ width: "100%" }} value={form.old_password || ""} onChange={e => setForm({ ...form, old_password: e.target.value })}/></Field>
        <Field label="New Password" required hint="At least 6 characters"><input className="input mono" type="password" style={{ width: "100%" }} value={form.new_password || ""} onChange={e => setForm({ ...form, new_password: e.target.value })}/></Field>
      </Modal>

      {conf.node}
    </div>
  );
}

function PageSystemParams() {
  const store = useStore();
  const [form, setForm] = useState({ import_dir: "" });
  const { data, loading, error, reload } = useResource(() => api.getSettings(), []);
  const { busy, run } = useMutation(store.toast);

  const savedImportDir = getImportDir(data);
  const defaultImportDir = getDefaultImportDir(data);
  const value = form.import_dir || "";
  const dirty = value.trim() !== (savedImportDir || "").trim();
  const valid = isAbsolutePath(value);

  useEffect(() => {
    if (data) setForm({ import_dir: getImportDir(data) });
  }, [data]);

  const save = async () => {
    const dir = value.trim();
    if (!dir) { store.toast("Enter the image import directory", "err"); return; }
    if (!isAbsolutePath(dir)) { store.toast("The image import directory must be an absolute path", "err"); return; }
    await run(() => api.saveSettings({ import_dir: dir }), "Image import directory updated", { reload, errMsg: "Save failed" });
  };

  return (
    <div className="fade-in" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <HACard/>
      <ClientNetworkSettings store={store}/>
      <ImageDirectorySettings
        loading={loading}
        error={error}
        value={value}
        savedValue={savedImportDir}
        defaultValue={defaultImportDir}
        dirty={dirty}
        valid={valid}
        busy={busy}
        onChange={(v) => setForm({ import_dir: v })}
        onReset={() => setForm({ import_dir: savedImportDir })}
        onSave={save}
      />
    </div>
  );
}

// ClientNetworkSettings configures the dnsmasq interface and support for groups on routed subnets via DHCP relay.
// Explain the cross-subnet impact to network administrators. Display backend rejection details when groups still depend on it.
function ClientNetworkSettings({ store }) {
  const { data, loading, error, reload } = useResource(() => api.getNetwork(30), []);
  const { busy, run } = useMutation(store.toast);
  const [confirmOn, setConfirmOn] = useState(false);
  const [saveError, setSaveError] = useState("");
  const [iface, setIface] = useState(null); // null means unchanged
  const goto = store.goto;
  const view = data || {};
  const chosenIface = iface === null ? (view.client_iface_setting || "") : iface;
  const ifaceOptions = [{ value: "", label: `Auto-detect${view.client_iface ? `（Current ${view.client_iface}）` : ""}` }]
    .concat((view.interfaces || []).map(i => ({ value: i.name, label: `${i.name} · ${(i.addrs || []).join(", ") || "No Address"}` })));
  const save = async (allow) => {
    setSaveError("");
    const ok = await run(() => api.saveNetwork({ client_iface: chosenIface, allow_cross_subnet: allow }), allow ? "Cross-subnet groups enabled. dnsmasq will receive requests on all interfaces" : "Saved", {
      reload: () => { setIface(null); reload(); },
      errMsg: "Save failed",
    });
    if (!ok) {
      // Keep dependency-related rejection details visible in the card, not just in a transient toast.
      try { await api.saveNetwork({ client_iface: chosenIface, allow_cross_subnet: allow }); } catch (e) { setSaveError(e.message || "Save failed"); }
    }
    return ok;
  };
  const onToggle = async () => {
    if (view.allow_cross_subnet) { await save(false); return; }
    setConfirmOn(true);
  };
  const stateChip = loading ? { cls: "", text: "Loading" } : !view.known ? { cls: "rose", text: "Interface Has No Address" } : view.allow_cross_subnet ? { cls: "amber", text: "Cross-subnet Mode" } : { cls: "emerald", text: "Same Subnet Mode" };
  return (
    <div className="card" style={{ padding: 0, overflow: "hidden" }}>
      <div className="card-h">
        <div className="card-title"><Icons.Network size={14}/> Client Network</div>
        <span className={`chip ${stateChip.cls}`}>{stateChip.text}</span>
      </div>
      <div style={{ padding: 16 }}>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))", gap: 16, alignItems: "start" }}>
          <Field label="Client Interface" helpAfterLabel help="The interface on which dnsmasq assigns client IPs; rescan after changing server interfaces">
            <div style={{ display: "flex", gap: 8 }}>
              <Select value={chosenIface} aria-label="Client Interface" onChange={v => setIface(v)} options={ifaceOptions} style={{ flex: 1, minWidth: 0 }}/>
              {iface !== null && iface !== (view.client_iface_setting || "") && <button className="btn primary" disabled={busy} onClick={() => save(!!view.allow_cross_subnet)}>Save</button>}
              <button className="btn" disabled={busy || loading} onClick={() => { setIface(null); reload(); }}>Rescan</button>
            </div>
          </Field>
          <Field label="Client Subnet" helpAfterLabel help={view.allow_cross_subnet ? "Receive requests on all interfaces, including relayed requests" : `Only on ${view.client_iface || "Client Interface"} accept requests`}>
            <div className="mono" style={{ fontSize: 14, minHeight: 32, display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
              {(view.client_networks || []).length
                ? view.client_networks.map(n => <span key={n} className="chip">{n}</span>)
                : (loading ? "Loading…" : <span style={{ color: "var(--rose)" }}>Client Interface {view.client_iface || ""}  has no IPv4 address</span>)}
            </div>
          </Field>
          <Field label="Cross-subnet Groups" helpAfterLabel help={`Enable for remote client subnets; point switch DHCP relay to ${view.boot_host || "Server"}`}>
            <label style={{ display: "flex", alignItems: "center", gap: 8, minHeight: 32, fontSize: 14, cursor: "pointer" }}>
              <input type="checkbox" checked={!!view.allow_cross_subnet} disabled={busy || loading} onChange={onToggle} aria-label="Allow cross-subnet groups (DHCP relay)"/>
              Allow (DHCP relay)
            </label>
          </Field>
        </div>
        {saveError && <div style={{ fontSize: 13, color: "var(--rose)", marginBottom: 10 }}>{saveError}</div>}
        {error && <div style={{ fontSize: 13, color: "var(--rose)", marginBottom: 10 }}>Load failed: {error}</div>}

        {(view.groups || []).length > 0 && (
          <div>
            <div style={{ border: "1px solid var(--line)", borderRadius: "var(--r-md)", overflow: "hidden" }}>
              <table className="t">
                <thead><tr><th>Group</th><th>Start Address</th><th>Clients</th><th>Status</th></tr></thead>
                <tbody>
                  {view.groups.map(g => {
                    const st = NET_STATUS[g.status] || NET_STATUS.unknown;
                    return (
                      <tr key={g.id}>
                        <td><button type="button" style={{ background: "none", border: 0, padding: 0, color: "var(--cyan)", cursor: "pointer", font: "inherit" }} onClick={() => goto("groups", { groupId: g.id })}>{g.name}</button></td>
                        <td className="mono">{g.start_ip}</td>
                        <td className="mono">{g.client_max}</td>
                        <td>
                          {st.text && <span className={`chip ${st.chip}`}>{st.text}</span>}
                          {g.status === "blocked" && <span className="meta" style={{ marginLeft: 8 }}>Cannot obtain an address. Move the group onto the server subnet in Groups</span>}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>

      <Modal open={confirmOn} onClose={() => !busy && setConfirmOn(false)} title="Enable Cross-subnet Groups" size="md"
        footer={<><button className="btn" disabled={busy} onClick={() => setConfirmOn(false)}>Cancel</button><button className="btn primary" disabled={busy} onClick={async () => { if (await save(true)) setConfirmOn(false); }}>{busy ? "Saving…" : "Enable"}</button></>}>
        <div className="hint" style={{ marginBottom: 8 }}>When enabled, groups may use a different subnet than the server. Configure DHCP relay on the switch and use its Layer 3 interface address as the group gateway. Review the switch settings when editing remote groups.</div>
        <div className="hint">dnsmasq will accept DHCP requests on all interfaces but respond only to registered clients; other devices on the office network will not receive addresses.</div>
      </Modal>
    </div>
  );
}

function ImageDirectorySettings({ loading, error, value, savedValue, defaultValue, dirty, valid, busy, onChange, onReset, onSave }) {
  const risk = isVarPath(value);
  const empty = !(value || "").trim();
  const stateChip = loading
    ? { cls: "", text: "Loading" }
    : dirty
      ? { cls: "amber", text: "Unsaved" }
      : risk
        ? { cls: "amber", text: "System Disk Risk" }
        : { cls: "emerald", text: "Applied" };
  return (
    <div className="card" style={{ padding: 0, overflow: "hidden" }}>
      <div className="card-h">
        <div className="card-title"><Icons.Folder size={14}/> Image Import Directory</div>
        <span className={`chip ${stateChip.cls}`}>{stateChip.text}</span>
      </div>
      <div style={{ padding: 16 }}>
        <Field label="Directory Path" required hint={risk ? "This path is under /var; large images could fill the system disk." : "An absolute server path. Saving performs a write test."}>
          <div style={{ display: "grid", gridTemplateColumns: "minmax(260px, 1fr) auto auto", gap: 8, alignItems: "center" }}>
            <input
              className="input mono"
              style={{ width: "100%", minWidth: 0 }}
              value={value}
              onChange={e => onChange(e.target.value)}
              placeholder="/tank/imports"
            />
            {dirty && <button className="btn primary" style={{ minWidth: 58, justifyContent: "center", whiteSpace: "nowrap" }} disabled={busy || loading || empty || !valid} onClick={onSave}>
              {busy ? "Saving…" : "Save"}
            </button>}
            {dirty && <button className="btn" style={{ whiteSpace: "nowrap" }} disabled={busy} onClick={onReset}>Revert</button>}
          </div>
        </Field>
        <div style={{ display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center", minHeight: 22 }}>
          {savedValue && <span className="mono" style={{ fontSize: 12, color: "var(--fg-faint)", wordBreak: "break-all" }}>Current: {savedValue}</span>}
          <span className="chip mono" title={defaultValue}>Default Directory</span>
          {!empty && !valid && <span className="chip rose">Absolute path required</span>}
          {error && <span className="chip rose">{error}</span>}
        </div>
      </div>
    </div>
  );
}

window.PageSettings = PageSettings;
window.PageSystemParams = PageSystemParams;

export { PageSettings, PageSystemParams };
