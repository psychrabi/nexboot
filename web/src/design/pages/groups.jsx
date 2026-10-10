// A group binds a system image, configuration, restore point and DHCP settings; configure data disks in the same dialog.
import React from "react";
import { Icons } from "../icons.jsx";
import { useStore } from "../store.jsx";
import { Modal, useConfirm } from "../overlay.jsx";
import {
  Field,
  Select,
  SearchBox,
  TableState,
  Toggle,
} from "../primitives.jsx";
import { api } from "../../lib/api.js";
import { useResource, useMutation } from "../../lib/hooks.js";
import {
  classifyGroupNetwork,
  groupWindow,
  intToIp4,
  relaySnippet,
} from "../../lib/network.js";

// Prepopulate public DNS for new groups; operators may replace it with internal DNS.
// Prioritize local DNS because Windows queries DNS1 first and may wait for timeouts before DNS2.
// DNS must not be empty: dnsmasq port=0 disables resolution and clients need a resolver.
const DEFAULT_DNS1 = "114.114.114.114";
const DEFAULT_DNS2 = "223.5.5.5";
const { useState, useEffect } = React;

// Network status labels shared between group cards and forms.
const NET_STATUS = {
  same: { chip: "emerald", text: "Same Subnet" },
  relay: { chip: "amber", text: "Cross-subnet · Relay" },
  blocked: { chip: "rose", text: "Blocked" },
  unknown: { chip: "", text: "" },
};
const rangeText = (startIP, max) => {
  const w = groupWindow(startIP, max);
  return w ? `${intToIp4(w.start)}–${intToIp4(w.end)}` : startIP || "—";
};

const ipRangeEnd = (startIP, max) => {
  const m = /^(.*\.)(\d+)$/.exec(String(startIP || ""));
  if (!m || !max) return startIP || "—";
  return m[1] + (parseInt(m[2], 10) + Math.max(0, max - 1));
};
const asItems = (r) =>
  Array.isArray(r) ? r : r && Array.isArray(r.items) ? r.items : [];

function PageGroups() {
  const store = useStore();
  const conf = useConfirm();
  const { route, goto } = store;

  const [search, setSearch] = useState("");
  const [modal, setModal] = useState(null); // add | edit | fixnet
  const [form, setForm] = useState({});
  const [fix, setFix] = useState(null); // { group, target, preview }
  // Load the server client subnet independently for suggested defaults and range validation.
  const { data: network, reload: reloadNetwork } = useResource(
    () => api.getNetwork(30).catch(() => null),
    [],
  );
  // Cluster roster for storage-node selection; hide the selector on standalone nodes or failed loads.
  const { data: nodesData } = useResource(
    () => api.listClusterNodes().catch(() => null),
    [],
  );
  const clusterNodes = (nodesData && nodesData.items) || [];

  // Fetch independently so failed image/client requests do not clear groups.
  const {
    data,
    loading,
    error,
    reload: load,
  } = useResource(async () => {
    const [gr, ir, tr] = await Promise.allSettled([
      api.listGroups(),
      api.listImages(),
      api.listTerminals(),
    ]);
    if (gr.status === "rejected") throw gr.reason;
    const groups = (gr.value && gr.value.items) || [];
    const diskResults = await Promise.allSettled(
      groups.map((g) => api.listGroupDisks(g.ID)),
    );
    const disks = Object.fromEntries(
      groups.map((g, i) => [
        g.ID,
        diskResults[i].status === "fulfilled"
          ? asItems(diskResults[i].value)
          : [],
      ]),
    );
    // Resolve configuration and restore-point IDs to names, falling back to IDs.
    const imageIds = [
      ...new Set(
        [
          ...groups.map((g) => g.SystemImageID),
          ...Object.values(disks)
            .flat()
            .map((d) => d.ImageID),
        ].filter(Boolean),
      ),
    ];
    const configIds = [
      ...new Set(groups.map((g) => g.SystemConfigID).filter(Boolean)),
    ];
    const [cfgResults, redResults] = await Promise.all([
      Promise.allSettled(imageIds.map((id) => api.listConfigs(id))),
      Promise.allSettled(configIds.map((id) => api.listReductions(id))),
    ]);
    const names = {};
    for (const r of [...cfgResults, ...redResults]) {
      if (r.status !== "fulfilled") continue;
      for (const item of (r.value && r.value.items) || [])
        names[item.ID] = item.DisplayName || item.Name;
    }
    return {
      groups,
      disks,
      images:
        ir.status === "fulfilled" ? (ir.value && ir.value.items) || [] : [],
      terminals:
        tr.status === "fulfilled" ? (tr.value && tr.value.items) || [] : [],
      names,
    };
  }, []);
  const groups = (data && data.groups) || [];
  const images = (data && data.images) || [];
  const terminals = (data && data.terminals) || [];
  const disksOf = (gid) => (data && data.disks && data.disks[gid]) || [];
  const nameOf = (id) => (data && data.names && data.names[id]) || id || "—";
  const { busy, run } = useMutation(store.toast);

  const imageById = Object.fromEntries(images.map((i) => [i.ID, i]));
  const filtered = groups.filter(
    (g) =>
      !search ||
      g.Name.toLowerCase().includes(search.toLowerCase()) ||
      (g.StartIP || "").includes(search),
  );

  const termsOf = (gid) => terminals.filter((t) => t.GroupID === gid);
  const onlineOf = (gid) =>
    termsOf(gid).filter((t) => t.State === "online").length;

  const openAdd = () => {
    // Derive defaults from the server client subnet rather than hard-coded addresses.
    const sug = network?.suggest;
    setForm({
      name: "",
      start_ip: sug?.start_ip || "",
      client_max: sug?.client_max || 30,
      gateway: sug?.gateway || "",
      netmask: sug?.netmask || "255.255.255.0",
      dns1: DEFAULT_DNS1,
      dns2: DEFAULT_DNS2,
      is_default: false,
      system_image_id: "",
      system_config_id: "",
      storage_server_id: "",
      disks: [],
      newDisks: [],
      removedDisks: [],
    });
    setModal("add");
  };
  // fillSuggested finds a free range on the client subnet for the requested number of clients.
  const fillSuggested = async () => {
    try {
      const view = await api.getNetwork(Number(form.client_max) || 30);
      if (!view?.suggest) {
        store.toast(
          "Not enough addresses in the server subnet. Reduce the client count and retry.",
          "err",
        );
        return;
      }
      setForm((f) => ({
        ...f,
        start_ip: view.suggest.start_ip,
        netmask: view.suggest.netmask,
        gateway: view.suggest.gateway || "",
      }));
    } catch (e) {
      store.toast(e.message || "Failed to load server network", "err");
    }
  };
  const netStatus = classifyGroupNetwork(form, network);
  const openEdit = (g) => {
    setForm({
      id: g.ID,
      name: g.Name,
      start_ip: g.StartIP,
      client_max: g.ClientMax,
      gateway: g.Gateway,
      netmask: g.Netmask,
      dns1: g.DNS1,
      dns2: g.DNS2,
      is_default: g.IsDefault,
      system_image_id: g.SystemImageID,
      system_config_id: g.SystemConfigID,
      storage_server_id: g.StorageServerID || "",
      disks: disksOf(g.ID),
      newDisks: [],
      removedDisks: [],
    });
    setModal("edit");
  };
  const save = async () => {
    if (!form.name?.trim()) {
      store.toast("Enter a group name", "err");
      return;
    }
    if (netStatus === "blocked") {
      store.toast(
        "The group subnet is outside the server client subnet. Move it or enable cross-subnet groups.",
        "err",
      );
      return;
    }
    if (!form.system_image_id || !form.system_config_id) {
      store.toast("Select an image and configuration", "err");
      return;
    }
    const diskProblem = checkNewDisks(form);
    if (diskProblem) {
      store.toast(diskProblem, "err");
      return;
    }
    const body = {
      name: form.name.trim(),
      is_default: !!form.is_default,
      start_ip: form.start_ip?.trim(),
      client_max: +form.client_max || 0,
      gateway: form.gateway?.trim(),
      netmask: form.netmask?.trim(),
      dns1: form.dns1?.trim() || "",
      dns2: form.dns2?.trim() || "",
      system_image_id: form.system_image_id,
      system_config_id: form.system_config_id,
      storage_server_id: form.storage_server_id || "",
    };
    if (modal === "add") {
      const ok = await run(
        async () => {
          const g = await api.createGroup(body);
          // If group creation succeeds but disk setup fails, switch to edit mode so retrying only adds remaining disks.
          setForm((f) => ({ ...f, id: g.ID }));
          setModal("edit");
          await applyDisks(g.ID, form, "Group created");
        },
        "Group created",
        { reload: load, errMsg: "Save failed" },
      );
      if (ok) setModal(null);
      return;
    }
    // Preview network changes: moving a range changes member IP addresses on DHCP renewal.
    // Changed Windows IPs break iSCSI sessions; warn operators before saving.
    const before = groups.find((g) => g.ID === form.id);
    const moved =
      before &&
      (before.StartIP !== body.start_ip ||
        before.ClientMax !== body.client_max ||
        (before.Netmask || "") !== (body.netmask || "") ||
        (before.Gateway || "") !== (body.gateway || ""));
    if (moved) {
      try {
        const preview = await api.previewGroupNetwork(form.id, {
          start_ip: body.start_ip,
          client_max: body.client_max,
          netmask: body.netmask,
          gateway: body.gateway,
        });
        if ((preview.terminals || []).length) {
          setFix({ group: before, target: body, preview, body });
          setModal("fixnet");
          return;
        }
      } catch (e) {
        store.toast(e.message || "Cannot preview address changes", "err");
        return;
      }
    }
    if (
      await run(() => saveEdit(form.id, body, form), "Group updated", {
        reload: load,
        errMsg: "Save failed",
      })
    )
      setModal(null);
  };
  const saveEdit = async (id, body, f) => {
    await api.updateGroup(id, body);
    await applyDisks(id, f, "Group saved");
  };
  // applyDisks removes before adding and records each success so retries apply only remaining changes.
  const applyDisks = async (groupId, f, saved) => {
    for (const d of f.removedDisks || []) {
      try {
        await api.deleteGroupDisk(d.ID);
      } catch (e) {
        throw new Error(
          `${saved}; data disk ${d.MountTarget} could not be removed: ${e.message}`,
        );
      }
      setForm((cur) => ({
        ...cur,
        removedDisks: cur.removedDisks.filter((x) => x.ID !== d.ID),
      }));
    }
    for (const row of f.newDisks || []) {
      const mount = row.mount_target.trim();
      let created;
      try {
        created = await api.createGroupDisk(groupId, {
          mount_target: mount,
          image_id: row.image_id,
          config_id: row.config_id,
        });
      } catch (e) {
        throw new Error(
          `${saved}; data disk ${mount} could not be added: ${e.message}`,
        );
      }
      setForm((cur) => ({
        ...cur,
        newDisks: cur.newDisks.filter((x) => x.key !== row.key),
        disks: [
          ...cur.disks,
          created && created.ID
            ? created
            : {
                ID: row.key,
                MountTarget: mount,
                ImageID: row.image_id,
                ConfigID: row.config_id,
              },
        ],
      }));
    }
  };
  const onDelete = async (g) => {
    const n = termsOf(g.ID).length;
    const message =
      n > 0
        ? `Group ${g.Name} still has ${n} clients. Move or delete them before deleting the group.`
        : `Delete group ${g.Name}?`;
    const ok = await conf.ask({ title: "Delete Group", message, danger: true });
    if (!ok) return;
    await run(() => api.deleteGroup(g.ID), "Group deleted", {
      reload: load,
      errMsg: "Delete failed",
    });
  };
  const onSetDefault = (g) =>
    run(() => api.setDefaultGroup(g.ID), `${g.Name} set as default group`, {
      reload: load,
    });
  const netStatusOf = (g) =>
    (network?.groups || []).find((x) => x.id === g.ID)?.status ||
    classifyGroupNetwork(
      { start_ip: g.StartIP, client_max: g.ClientMax },
      network,
    );
  // openFix previews a safe range and client IP changes before writing.
  const openFix = async (g) => {
    try {
      const view = await api.getNetwork(g.ClientMax || 30);
      if (!view?.suggest) {
        store.toast(
          "Not enough addresses in the server subnet. Reduce the group client count.",
          "err",
        );
        return;
      }
      const target = {
        start_ip: view.suggest.start_ip,
        client_max: g.ClientMax,
        netmask: view.suggest.netmask,
        gateway: view.suggest.gateway || "",
      };
      const preview = await api.previewGroupNetwork(g.ID, target);
      setFix({ group: g, target, preview });
      setModal("fixnet");
    } catch (e) {
      store.toast(e.message || "Cannot preview changes", "err");
    }
  };
  const applyFix = async () => {
    const g = fix.group,
      t = fix.target;
    // Keep other form fields intact; Move to Server Subnet changes only network settings.
    const body = fix.body || {
      name: g.Name,
      is_default: !!g.IsDefault,
      start_ip: t.start_ip,
      client_max: t.client_max,
      gateway: t.gateway,
      netmask: t.netmask,
      dns1: g.DNS1 || "",
      dns2: g.DNS2 || "",
      system_image_id: g.SystemImageID,
      system_config_id: g.SystemConfigID,
    };
    const commit = fix.body
      ? () => saveEdit(g.ID, body, form)
      : () => api.updateGroup(g.ID, body);
    if (
      await run(
        commit,
        `${g.Name} moved to ${rangeText(t.start_ip, t.client_max)}`,
        {
          reload: () => {
            load();
            reloadNetwork();
          },
          errMsg: "Update failed",
        },
      )
    ) {
      setModal(null);
      setFix(null);
    }
  };

  return (
    <div
      className="fade-in"
      style={{ display: "flex", flexDirection: "column", gap: 14 }}
    >
      <div
        className="card"
        style={{ padding: 14, display: "flex", alignItems: "center", gap: 12 }}
      >
        <SearchBox
          value={search}
          onChange={setSearch}
          placeholder="Search group name / IP range"
          width={280}
        />
        <span className="chip">{groups.length} groups</span>
        <span className="chip emerald">
          {groups.reduce((s, g) => s + onlineOf(g.ID), 0)} Online
        </span>
        <span className="chip">
          {groups.reduce((s, g) => s + (g.ClientMax || 0), 0)} Client capacity
        </span>
        <div style={{ marginLeft: "auto", display: "flex", gap: 8 }}>
          <button
            className="btn ghost icon"
            onClick={() => load()}
            title="Refresh now"
            aria-label="Refresh now"
          >
            <Icons.Refresh size={13} />
          </button>
          <button className="btn primary" onClick={openAdd}>
            <Icons.Plus size={12} /> New Group
          </button>
        </div>
      </div>

      {!filtered.length && (
        <div
          className="card"
          style={{
            padding: 40,
            textAlign: "center",
            color: "var(--fg-faint)",
            fontSize: 13,
          }}
        >
          {loading ? "Loading…" : error ? `Load failed: ${error}` : "No groups"}
        </div>
      )}

      {/* Use auto-fill instead of auto-fit so a single group does not fill the entire row. */}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fill, minmax(430px, 1fr))",
          gap: 14,
        }}
      >
        {filtered.map((g) => {
          const online = onlineOf(g.ID),
            registered = termsOf(g.ID).length,
            cap = g.ClientMax || 0;
          const full = cap > 0 && registered >= cap;
          const focused = route.params.groupId === g.ID;
          const img = imageById[g.SystemImageID];
          return (
            <div
              key={g.ID}
              className="card"
              style={{
                padding: 0,
                overflow: "hidden",
                borderColor: focused ? "var(--cyan)" : undefined,
              }}
            >
              <div
                style={{
                  padding: 16,
                  borderBottom: "1px solid var(--line-soft)",
                  display: "flex",
                  alignItems: "center",
                  gap: 12,
                }}
              >
                <div
                  style={{
                    width: 40,
                    height: 40,
                    borderRadius: 8,
                    background: "var(--cyan-soft)",
                    border: "1px solid oklch(0.55 0.1 200 / 0.4)",
                    display: "grid",
                    placeItems: "center",
                    color: "var(--cyan)",
                    flexShrink: 0,
                  }}
                >
                  <Icons.Group size={20} />
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div
                    style={{ display: "flex", alignItems: "center", gap: 8 }}
                  >
                    <span style={{ fontSize: 14, fontWeight: 600 }}>
                      {g.Name}
                    </span>
                    {g.IsDefault && <span className="chip cyan">Default</span>}
                    {/* Hide generated group IDs; names provide sufficient identification. */}
                  </div>
                  {/* Keep labels on one line and truncate overly long values. */}
                  <div
                    className="mono meta"
                    style={{
                      marginTop: 3,
                      whiteSpace: "nowrap",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      display: "flex",
                      alignItems: "center",
                      gap: 6,
                    }}
                  >
                    <span>
                      {g.StartIP} → {ipRangeEnd(g.StartIP, g.ClientMax)}
                    </span>
                    {(() => {
                      const st = NET_STATUS[netStatusOf(g)];
                      return st?.text ? (
                        <span
                          className={`chip ${st.chip}`}
                          title={
                            st.chip === "rose"
                              ? "Outside the server client interface subnet; these clients cannot obtain an address"
                              : ""
                          }
                        >
                          {st.text}
                        </span>
                      ) : null;
                    })()}
                  </div>
                </div>
                {/* Label each count separately for clarity. */}
                <div
                  style={{
                    display: "flex",
                    alignItems: "flex-start",
                    flexShrink: 0,
                  }}
                >
                  <GroupStat
                    value={online}
                    label="Online"
                    color={online ? "var(--emerald)" : "var(--cyan)"}
                  />
                  <StatSep />
                  <GroupStat
                    value={registered}
                    label="Assigned"
                    color="var(--fg)"
                  />
                  <StatSep />
                  <GroupStat
                    value={cap}
                    label="Capacity"
                    color={full ? "var(--amber)" : "var(--fg-mute)"}
                    title={
                      full
                        ? "This group is full. Increase Maximum Clients under Edit to add more."
                        : "Maximum number of clients assignable to this group"
                    }
                  />
                </div>
              </div>

              {/* Show system and data disks on cards; gateway, mask and DNS belong in the edit dialog. */}
              <div
                style={{
                  padding: "12px 16px",
                  display: "grid",
                  gridTemplateColumns: "max-content 1fr",
                  gap: "8px 14px",
                  alignItems: "center",
                  fontSize: 13,
                }}
              >
                <span className="meta">System Disk</span>
                <span
                  style={{
                    minWidth: 0,
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  {img?.Name || "—"}
                  <span className="meta"> / </span>
                  {nameOf(g.SystemConfigID)}
                  {g.SystemReductionID && (
                    <span className="meta">
                      {" "}
                      · {nameOf(g.SystemReductionID)}
                    </span>
                  )}
                </span>
                <span className="meta">Data Disks</span>
                <span
                  style={{
                    display: "flex",
                    gap: 6,
                    flexWrap: "wrap",
                    minWidth: 0,
                  }}
                >
                  {disksOf(g.ID).length ? (
                    disksOf(g.ID).map((d) => (
                      <span
                        key={d.ID}
                        className="chip"
                        title={`${imageById[d.ImageID]?.Name || d.ImageID} / ${nameOf(d.ConfigID)}`}
                      >
                        <span className="mono">{d.MountTarget}</span>{" "}
                        {imageById[d.ImageID]?.Name || d.ImageID}
                      </span>
                    ))
                  ) : (
                    <span className="meta">—</span>
                  )}
                </span>
              </div>

              <div
                style={{
                  padding: "10px 16px",
                  borderTop: "1px solid var(--line-soft)",
                  display: "flex",
                  gap: 8,
                  justifyContent: "flex-end",
                  flexWrap: "wrap",
                }}
              >
                <button
                  className="btn"
                  onClick={() => goto("terminals", { groupId: g.ID })}
                >
                  <Icons.Eye size={12} /> View Clients
                </button>
                {netStatusOf(g) === "blocked" && (
                  <button className="btn primary" onClick={() => openFix(g)}>
                    Move to Server Subnet
                  </button>
                )}
                {!g.IsDefault && (
                  <button className="btn" onClick={() => onSetDefault(g)}>
                    Set Default
                  </button>
                )}
                <button className="btn" onClick={() => openEdit(g)}>
                  <Icons.Cog size={12} /> Edit
                </button>
                <button
                  className="btn ghost icon danger"
                  onClick={() => onDelete(g)}
                  title="Delete Group"
                  aria-label={`Delete group ${g.Name}`}
                >
                  <Icons.Trash size={12} />
                </button>
              </div>
            </div>
          );
        })}
      </div>

      {/* Create / Edit */}
      <Modal
        open={modal === "add" || modal === "edit"}
        onClose={() => !busy && setModal(null)}
        size="md"
        title={modal === "add" ? "Create Client Group" : "Edit Group"}
        footer={
          <>
            <button
              className="btn"
              disabled={busy}
              onClick={() => setModal(null)}
            >
              Cancel
            </button>
            <button
              className="btn primary"
              disabled={busy || netStatus === "blocked"}
              title={
                netStatus === "blocked"
                  ? "The group subnet is outside the server client subnet. Move it or enable cross-subnet groups."
                  : ""
              }
              onClick={save}
            >
              {busy ? "Saving…" : "Save"}
            </button>
          </>
        }
      >
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
            gap: 12,
          }}
        >
          <Field label="Group Name" required>
            <input
              className="input"
              aria-label="Group Name"
              style={{ width: "100%" }}
              value={form.name || ""}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </Field>
          <Field label="Set as Default Group">
            <Toggle
              checked={!!form.is_default}
              onChange={(v) => setForm({ ...form, is_default: v })}
              onLabel="Yes"
              offLabel="No"
            />
          </Field>
        </div>
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
            gap: 12,
          }}
        >
          <Field label="Starting IP" required>
            <input
              className="input mono"
              aria-label="Starting IP"
              style={{ width: "100%" }}
              value={form.start_ip || ""}
              onChange={(e) => setForm({ ...form, start_ip: e.target.value })}
            />
          </Field>
          <Field label="Maximum Clients" required>
            <input
              className="input mono"
              type="number"
              aria-label="Maximum Clients"
              style={{ width: "100%" }}
              value={form.client_max || 0}
              onChange={(e) =>
                setForm({ ...form, client_max: +e.target.value })
              }
            />
          </Field>
        </div>
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
            gap: 12,
          }}
        >
          <Field
            label={
              netStatus === "relay" ? "Gateway (Relay Address)" : "Gateway"
            }
            required={netStatus === "relay"}
            help={
              netStatus === "relay"
                ? "Layer 3 interface address on the switch performing DHCP relay."
                : "Router address for client Internet access; use server if it routes traffic or leave blank if there is no router."
            }
          >
            <input
              className="input mono"
              aria-label={
                netStatus === "relay" ? "Gateway (Relay Address)" : "Gateway"
              }
              style={{ width: "100%" }}
              value={form.gateway || ""}
              onChange={(e) => setForm({ ...form, gateway: e.target.value })}
            />
          </Field>
          <Field label="Subnet Mask" required>
            <input
              className="input mono"
              aria-label="Subnet Mask"
              style={{ width: "100%" }}
              value={form.netmask || ""}
              onChange={(e) => setForm({ ...form, netmask: e.target.value })}
            />
          </Field>
        </div>
        <NetworkStatusLine
          status={netStatus}
          form={form}
          network={network}
          onFill={fillSuggested}
          toast={store.toast}
        />
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
            gap: 12,
          }}
        >
          <Field label="DNS1">
            <input
              className="input mono"
              aria-label="DNS1"
              style={{ width: "100%" }}
              value={form.dns1 || ""}
              onChange={(e) => setForm({ ...form, dns1: e.target.value })}
            />
          </Field>
          <Field label="DNS2">
            <input
              className="input mono"
              aria-label="DNS2"
              style={{ width: "100%" }}
              value={form.dns2 || ""}
              onChange={(e) => setForm({ ...form, dns2: e.target.value })}
            />
          </Field>
        </div>
        <ImageBindingPicker
          images={images.filter((i) => i.Purpose !== "data")}
          imageLabel="System Image"
          configHelp="Regular clients boot the applied restore point; Super Admin clients use the latest restore point. Image, configuration and network changes apply at next boot. Moving subnets updates client addresses."
          imageId={form.system_image_id}
          configId={form.system_config_id}
          onChange={({ image_id, config_id }) =>
            setForm({
              ...form,
              system_image_id: image_id,
              system_config_id: config_id,
            })
          }
        />
        <DataDisksEditor
          images={images}
          systemImageId={form.system_image_id}
          form={form}
          setForm={setForm}
          imageName={(id) => imageById[id]?.Name || id}
          configName={nameOf}
          onCreateImage={() => goto("images")}
        />
        {clusterNodes.length > 1 && (
          <Field
            label="Storage Node"
            help="Select the storage node serving this group's disks. Automatic balancing chooses a healthy, lightly loaded node and prefers the previous host for cache locality. Pinning a node dedicates its resources. Changes apply at next boot; failures fall back to the active node."
          >
            <select
              className="input"
              aria-label="Storage Node"
              style={{ width: "100%" }}
              value={form.storage_server_id || ""}
              onChange={(e) =>
                setForm({ ...form, storage_server_id: e.target.value })
              }
            >
              <option value="">Automatic balancing (recommended)</option>
              {clusterNodes.map((n) => (
                <option key={n.id} value={n.id} disabled={!n.online}>
                  Pin to {n.ip || n.name || n.id}
                  {n.ha_state === "active" ? " (active)" : ""}
                  {n.online ? "" : " (offline)"}
                </option>
              ))}
            </select>
          </Field>
        )}
      </Modal>

      {/* Move a group outside the subnet back into the client subnet. */}
      <Modal
        open={modal === "fixnet" && !!fix}
        onClose={() => !busy && setModal(null)}
        size="md"
        title={fix ? `${fix.group.Name} · Move to Server Subnet` : ""}
        footer={
          <>
            <button
              className="btn"
              disabled={busy}
              onClick={() => setModal(null)}
            >
              Cancel
            </button>
            <button className="btn primary" disabled={busy} onClick={applyFix}>
              {busy ? "Updating…" : "Confirm Change"}
            </button>
          </>
        }
      >
        {fix && (
          <>
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "max-content 1fr",
                gap: "4px 12px",
                fontSize: 14,
                marginBottom: 10,
              }}
            >
              <span className="meta">Current</span>
              <span className="mono">
                {rangeText(fix.group.StartIP, fix.group.ClientMax)} · Gateway{" "}
                {fix.group.Gateway || "None"}
              </span>
              <span className="meta">Change to</span>
              <span className="mono">
                {rangeText(fix.target.start_ip, fix.target.client_max)} · Mask{" "}
                {fix.target.netmask} · Gateway {fix.target.gateway || "None"}
              </span>
            </div>
            {fix.preview.terminals.length ? (
              <>
                <div className="hint" style={{ marginBottom: 6 }}>
                  Group's {fix.preview.terminals.length} client addresses will
                  change while retaining their relative positions; old leases
                  are released.
                </div>
                {/* Running clients change IPs upon lease renewal, potentially disconnecting Windows iSCSI. */}
                {fix.preview.online_count > 0 && (
                  <div
                    className="hint"
                    style={{ marginBottom: 6, color: "var(--amber)" }}
                  >
                    Among them, {fix.preview.online_count} running clients will
                    change IPs at lease renewal, not immediately, potentially
                    disconnecting and rebooting. Wait until these clients are
                    shut down before making this change.
                  </div>
                )}
                <div style={{ overflowX: "auto" }}>
                  <table className="t">
                    <thead>
                      <tr>
                        <th>Client</th>
                        <th>MAC</th>
                        <th>Current</th>
                        <th>Change to</th>
                        <th>Status</th>
                      </tr>
                    </thead>
                    <tbody>
                      {fix.preview.terminals.map((t) => (
                        <tr key={t.id}>
                          <td>{t.name || t.id}</td>
                          <td className="mono">{t.mac}</td>
                          <td className="mono">{t.old_ip}</td>
                          <td className="mono">{t.new_ip}</td>
                          <td>
                            {t.online ? (
                              <span className="chip amber">Running</span>
                            ) : (
                              <span className="meta">Off</span>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </>
            ) : (
              <div className="hint">
                No clients in this group need an IP address change.
              </div>
            )}
          </>
        )}
      </Modal>

      {conf.node}
    </div>
  );
}

// GroupStat displays a vertically stacked count and label.
function GroupStat({ value, label, color, title }) {
  return (
    <div
      title={title}
      style={{
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        minWidth: 36,
      }}
    >
      <span
        className="mono"
        style={{ fontSize: 18, fontWeight: 600, lineHeight: 1.1, color }}
      >
        {value}
      </span>
      <span
        style={{
          fontSize: 11,
          color: "var(--fg-faint)",
          marginTop: 4,
          whiteSpace: "nowrap",
        }}
      >
        {label}
      </span>
    </div>
  );
}
const StatSep = () => (
  <span
    className="mono"
    style={{
      fontSize: 16,
      lineHeight: 1.25,
      color: "var(--line)",
      padding: "0 6px",
    }}
  >
    /
  </span>
);

// useImageConfigs keeps image/configuration selectors in sync with available options.
// A select visually defaults to the first option; synchronize state to prevent an invalid downstream selection.
function useImageConfigs(images, imageId, configId, onChange) {
  const [configs, setConfigs] = useState([]);
  useEffect(() => {
    if (!images.length || images.some((i) => i.ID === imageId)) return;
    onChange({ image_id: images[0].ID, config_id: "" });
  }, [imageId, images, onChange]);
  useEffect(() => {
    if (!imageId) {
      setConfigs([]);
      return;
    }
    let c = false;
    api
      .listConfigs(imageId)
      .then((r) => {
        if (!c) setConfigs(asItems(r));
      })
      .catch(() => {
        if (!c) setConfigs([]);
      });
    return () => {
      c = true;
    };
  }, [imageId]);
  useEffect(() => {
    if (!imageId || !configs.length) return;
    if (configs.some((c) => c.ID === configId)) return;
    onChange({ image_id: imageId, config_id: configs[0].ID });
  }, [imageId, configId, configs, onChange]);
  return configs;
}

const imageOptions = (images) =>
  images.length
    ? images.map((i) => ({ value: i.ID, label: `${i.Name} · ${i.OSType}` }))
    : [{ value: "", label: "No images available" }];
const configOptions = (configs) =>
  configs.length
    ? configs.map((c) => ({ value: c.ID, label: c.Name }))
    : [{ value: "", label: "No configurations available" }];

function ImageBindingPicker({
  images,
  imageId,
  configId,
  onChange,
  imageLabel = "System Image",
  configLabel = "Boot Configuration",
  configHelp,
}) {
  const configs = useImageConfigs(images, imageId, configId, onChange);
  return (
    <>
      <Field label={imageLabel} required>
        <Select
          value={imageId || ""}
          aria-label={imageLabel}
          onChange={(v) => onChange({ image_id: v, config_id: "" })}
          options={imageOptions(images)}
        />
      </Field>
      <Field label={configLabel} required help={configHelp}>
        <Select
          value={configId || ""}
          onChange={(v) => onChange({ image_id: imageId, config_id: v })}
          options={configOptions(configs)}
        />
      </Field>
    </>
  );
}

// New data disks require complete fields and distinct mount targets.
function checkNewDisks(form) {
  const taken = new Set(
    (form.disks || []).map((d) => d.MountTarget.toUpperCase()),
  );
  for (const row of form.newDisks || []) {
    const mount = row.mount_target.trim();
    if (!mount) return "Enter the data disk mount target";
    if (!row.image_id || !row.config_id)
      return `Choose an image and configuration for data disk ${mount}`;
    if (taken.has(mount.toUpperCase()))
      return `Mount target ${mount} is duplicated`;
    taken.add(mount.toUpperCase());
  }
  return "";
}

// nextDriveLetter chooses the first unused drive letter starting at D:.
function nextDriveLetter(form) {
  const used = new Set(
    [
      ...(form.disks || []).map((d) => d.MountTarget),
      ...(form.newDisks || []).map((r) => r.mount_target),
    ].map((m) => m.trim().toUpperCase()),
  );
  for (let c = 68; c <= 90; c++) {
    const letter = String.fromCharCode(c) + ":";
    if (!used.has(letter)) return letter;
  }
  return "";
}

let diskKeySeq = 0;

// Group data disks are mounted with the system disk and saved in the group creation/edit dialog.
// Existing disks cannot be edited by the API; remove and readd to change them.
function DataDisksEditor({
  images,
  systemImageId,
  form,
  setForm,
  imageName,
  configName,
  onCreateImage,
}) {
  const [showHelp, setShowHelp] = useState(false);
  const sysOS = images.find((i) => i.ID === systemImageId)?.OSType;
  const dataImages = images.filter(
    (i) => i.Purpose === "data" && (!sysOS || i.OSType === sysOS),
  );
  const existing = form.disks || [];
  const added = form.newDisks || [];
  const add = () =>
    setForm((f) => ({
      ...f,
      newDisks: [
        ...(f.newDisks || []),
        {
          key: `new-${++diskKeySeq}`,
          mount_target: sysOS === "linux" ? "" : nextDriveLetter(f),
          image_id: "",
          config_id: "",
        },
      ],
    }));
  const removeExisting = (d) =>
    setForm((f) => ({
      ...f,
      disks: f.disks.filter((x) => x.ID !== d.ID),
      removedDisks: [...(f.removedDisks || []), d],
    }));
  const updateRow = (key, patch) =>
    setForm((f) => ({
      ...f,
      newDisks: f.newDisks.map((r) => (r.key === key ? { ...r, ...patch } : r)),
    }));
  const removeRow = (key) =>
    setForm((f) => ({
      ...f,
      newDisks: f.newDisks.filter((r) => r.key !== key),
    }));
  return (
    <div style={{ marginTop: 4 }}>
      <div
        className="row"
        style={{ justifyContent: "space-between", marginBottom: 8 }}
      >
        <div className="card-title">
          <Icons.Disk size={12} /> Data Disks
          <button
            type="button"
            className="field-help"
            aria-label="Data Disk Help"
            aria-expanded={showHelp}
            onClick={() => setShowHelp((v) => !v)}
          >
            ?
          </button>
        </div>
        <button
          className="btn ghost"
          style={{ padding: "3px 8px" }}
          disabled={!dataImages.length}
          onClick={add}
        >
          <Icons.Plus size={11} /> Add Data Disk
        </button>
      </div>
      {showHelp && (
        <div className="hint" style={{ marginBottom: 8 }}>
          Additional disks mounted alongside the system disk (such as game
          disks). Select compatible data-disk images for the same OS. Existing
          disks must be removed and readded to change them. Changes apply at
          next boot.
        </div>
      )}
      <div
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
              <th style={{ width: 96 }}>Mount Target</th>
              <th>Image</th>
              <th>Configuration</th>
              <th className="t-actions"></th>
            </tr>
          </thead>
          <tbody>
            {existing.map((d) => (
              <tr key={d.ID}>
                <td className="mono" style={{ fontWeight: 600 }}>
                  {d.MountTarget}
                </td>
                <td className="muted">{imageName(d.ImageID)}</td>
                <td className="muted">{configName(d.ConfigID)}</td>
                <td className="t-actions">
                  <div>
                    <button
                      className="btn ghost icon danger"
                      onClick={() => removeExisting(d)}
                      title="Remove Data Disk"
                      aria-label={`Remove Data Disk ${d.MountTarget}`}
                    >
                      <Icons.Trash size={12} />
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {added.map((row, i) => (
              <NewDiskRow
                key={row.key}
                index={i + 1}
                row={row}
                images={dataImages}
                linux={sysOS === "linux"}
                onChange={(patch) => updateRow(row.key, patch)}
                onRemove={() => removeRow(row.key)}
              />
            ))}
            <TableState
              colSpan={4}
              empty={!existing.length && !added.length}
              hint="No data disks configured"
            />
          </tbody>
        </table>
      </div>
      {!dataImages.length && (
        <div className="hint" style={{ marginTop: 6 }}>
          No {sysOS === "linux" ? "Linux" : "Windows"} data-disk images.{" "}
          <span
            style={{ color: "var(--cyan)", cursor: "pointer" }}
            onClick={onCreateImage}
          >
            Create one in Image Management
          </span>
          .
        </div>
      )}
    </div>
  );
}

function NewDiskRow({ index, row, images, linux, onChange, onRemove }) {
  const pick = React.useCallback(
    ({ image_id, config_id }) => onChange({ image_id, config_id }),
    [onChange],
  );
  const configs = useImageConfigs(images, row.image_id, row.config_id, pick);
  const label = `New Data Disk ${index}`;
  return (
    <tr>
      <td>
        <input
          className="input mono"
          style={{ width: "100%" }}
          aria-label={`${label} Mount Target`}
          placeholder={linux ? "/data" : "D:"}
          value={row.mount_target}
          onChange={(e) => onChange({ mount_target: e.target.value })}
        />
      </td>
      <td>
        <Select
          aria-label={`${label} Image`}
          value={row.image_id || ""}
          onChange={(v) => onChange({ image_id: v, config_id: "" })}
          options={imageOptions(images)}
        />
      </td>
      <td>
        <Select
          aria-label={`${label} Configuration`}
          value={row.config_id || ""}
          onChange={(v) => onChange({ config_id: v })}
          options={configOptions(configs)}
        />
      </td>
      <td className="t-actions">
        <div>
          <button
            className="btn ghost icon danger"
            onClick={onRemove}
            title="Remove"
            aria-label={`Remove ${label}`}
          >
            <Icons.Trash size={12} />
          </button>
        </div>
      </td>
    </tr>
  );
}

// NetworkStatusLine previews the same network classification used during server-side save.
function NetworkStatusLine({ status, form, network, onFill, toast }) {
  const [probe, setProbe] = useState(null); // { ip, reachable, rtt_ms } | { ip, error }
  const [showSnippet, setShowSnippet] = useState(false);
  useEffect(() => {
    setProbe(null);
  }, [form.gateway]);
  if (status === "unknown" || !network) return null;
  const range = rangeText(form.start_ip, form.client_max);
  const nets = (network.client_networks || []).join("、");
  const iface = network.client_iface || "?";
  const box = (color, bg, children) => (
    <div
      style={{
        display: "flex",
        gap: 8,
        alignItems: "flex-start",
        padding: "8px 10px",
        borderRadius: 6,
        background: bg,
        color,
        fontSize: 13,
        lineHeight: 1.6,
        marginBottom: 12,
      }}
    >
      <span
        className="dot"
        style={{ background: color, marginTop: 6, flexShrink: 0 }}
      />
      <span style={{ minWidth: 0 }}>{children}</span>
    </div>
  );
  if (status === "same") {
    // Keep normal status concise; show detailed guidance for problems.
    return box(
      "var(--emerald)",
      "var(--emerald-soft)",
      <>
        <b>Same Subnet</b> · Clients can obtain addresses on boot.
      </>,
    );
  }
  if (status === "blocked") {
    const sug = network.suggest;
    return box(
      "var(--rose)",
      "var(--rose-soft)",
      <>
        <b>Outside the server client subnet</b> · Server client interface:{" "}
        {iface} ({nets}), {range} clients cannot get addresses or boot.
        <br />
        Move to server subnet
        {sug ? (
          <>
            {" "}
            (
            <button
              type="button"
              className="btn"
              style={{ padding: "1px 8px", marginLeft: 2 }}
              onClick={onFill}
            >
              Use{" "}
              {rangeText(
                sug.start_ip,
                Number(form.client_max) || sug.client_max,
              )}
            </button>
            )
          </>
        ) : (
          ""
        )}
        , Alternatively, for another VLAN, enable Cross-subnet Groups in System
        Settings.
      </>,
    );
  }
  const doProbe = async () => {
    const ip = (form.gateway || "").trim();
    if (!ip) {
      toast("Enter the gateway (relay address) first", "err");
      return;
    }
    try {
      setProbe(await api.probeNetwork(ip));
    } catch (e) {
      setProbe({ ip, error: e.message || "Test failed" });
    }
  };
  return box(
    "var(--amber)",
    "var(--amber-soft)",
    <>
      <b>Cross-subnet · DHCP Relay</b> · Clients in {range} subnet have requests
      relayed to server {network.boot_host || nets}. Set gateway to the relay
      switch's Layer 3 interface for this subnet.
      <span style={{ display: "inline-flex", gap: 8, marginLeft: 6 }}>
        <button
          type="button"
          className="btn"
          style={{ padding: "1px 8px" }}
          onClick={doProbe}
        >
          Test
        </button>
        <button
          type="button"
          className="btn"
          style={{ padding: "1px 8px" }}
          onClick={() => setShowSnippet((v) => !v)}
        >
          {showSnippet ? "Collapse" : "View Switch Configuration"}
        </button>
      </span>
      {probe &&
        (probe.error ? (
          <div>Probe failed: {probe.error}</div>
        ) : probe.reachable ? (
          <div>
            Ping from server to {probe.ip}: Reachable
            {probe.rtt_ms ? ` (${probe.rtt_ms} ms)` : ""} ✓
          </div>
        ) : (
          <div>
            Cannot ping {probe.ip}.Clients may not get addresses. Check DHCP
            relay (helper-address {network.boot_host}) and routing to the
            server. Ignore ping failures if the switch blocks ICMP.
          </div>
        ))}
      {showSnippet && (
        <pre
          className="mono"
          style={{
            marginTop: 6,
            whiteSpace: "pre-wrap",
            fontSize: 12,
            color: "var(--fg)",
          }}
        >
          {relaySnippet(network.boot_host || nets)}
        </pre>
      )}
    </>,
  );
}

window.PageGroups = PageGroups;

export { PageGroups };
