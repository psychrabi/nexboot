// Global React context state for navigation, toasts and loaded collections. UI primitives live in overlay.jsx / primitives.jsx.
import React from "react";
import { api as backendApi } from "../lib/api.js";
import { onMutation } from "../lib/hooks.js";
import { TASK_LABELS, fmtDateTime, fmtTime } from "../lib/format.js";

const StoreCtx = React.createContext(null);

// Live data loading helpers
// Map raw backend records to compact data for global search and notifications; pages use raw records directly.
// No sample data: empty backend results produce empty collections.
const unwrapList = (r) =>
  Array.isArray(r) ? r : r && Array.isArray(r.items) ? r.items : null;
const mapImage = (im) => ({
  id: im.ID,
  type: "system", // Backend distinguishes Windows/Linux but not system/game
  name: im.Name,
  size: Math.max(0.1, +(Number(im.Size || 0) / 1024 ** 3).toFixed(1)),
  status:
    im.State === "normal"
      ? "published"
      : im.State === "importing"
        ? "publishing"
        : im.State === "error"
          ? "building"
          : "idle",
  unit: 0,
  default: "—",
  updated: fmtDateTime(im.CreatedAt),
  configs: 0,
  snapshots: 0,
});
const mapTask = (t) => ({
  id: t.ID,
  type: TASK_LABELS[t.Type] || t.Type,
  target: t.TargetRef || t.Message || "—",
  progress: t.Progress || 0,
  status:
    t.Status === "success"
      ? "done"
      : t.Status === "failed"
        ? "failed"
        : t.Status === "running"
          ? "running"
          : "waiting",
  started: fmtTime(t.CreatedAt),
});

function StoreProvider({ children }) {
  // Collections support sidebar/header badges, global search and notifications; pages fetch through useResource.
  // Initialize empty and replace with backend results, including empty collections to show true zero.
  const [pools, setPools] = React.useState([]);
  const [groups, setGroups] = React.useState([]);
  const [images, setImages] = React.useState([]);
  const [alarms, setAlarms] = React.useState([]);
  const [terminals, setTerminals] = React.useState([]);
  const [tasks, setTasks] = React.useState([]);

  const [toasts, setToasts] = React.useState([]);
  // Keep error toasts visible longer than success messages for actionable backend details; clicking dismisses them.
  const toast = (msg, kind = "info") => {
    const id = Math.random().toString(36).slice(2);
    setToasts((t) => [...t, { id, msg, kind }]);
    setTimeout(
      () => setToasts((t) => t.filter((x) => x.id !== id)),
      kind === "err" ? 6500 : 3200,
    );
  };
  const dismissToast = (id) => setToasts((t) => t.filter((x) => x.id !== id));

  // Cross-page navigation
  const [route, setRoute] = React.useState({ page: "overview", params: {} });
  const goto = (page, params = {}) => setRoute({ page, params });

  const refreshActiveTasks = React.useCallback(async () => {
    const rawTasks = unwrapList(await backendApi.listTasks());
    keepIfSame(setTasks, (rawTasks || []).map(mapTask));
  }, []);

  React.useEffect(() => {
    const load = async () => {
      try {
        await refreshActiveTasks();
      } catch {
        return;
      }
    };
    load();
    const id = setInterval(load, 3000);
    return () => {
      clearInterval(id);
    };
  }, [refreshActiveTasks]);

  // Periodically refresh badge/search/notification data because sessions may last for hours.
  React.useEffect(() => {
    let cancelled = false;
    const hydrate = async () => {
      const [imgR, grpR, termR, poolR, taskR, alarmR, nodeR, cpoolR] =
        await Promise.allSettled([
          backendApi.listImages(),
          backendApi.listGroups(),
          backendApi.listTerminals(),
          backendApi.listPools(),
          backendApi.listTasks(),
          backendApi.listAlarms({ size: 100 }),
          backendApi.listClusterNodes(),
          backendApi.listClusterPools(),
        ]);
      if (cancelled) return;
      const raw = (r) =>
        r.status === "fulfilled" ? unwrapList(r.value) : null;
      const rawImgs = raw(imgR),
        rawGrps = raw(grpR),
        rawTerms = raw(termR),
        rawPools = raw(poolR),
        rawTasks = raw(taskR),
        rawAlarms = raw(alarmR);
      const rawNodes = raw(nodeR);
      const clusterPools = cpoolR.status === "fulfilled" ? cpoolR.value : null;

      const imgById = {};
      (rawImgs || []).forEach((im) => {
        imgById[im.ID] = im;
      });

      // Resolve group image names and count online clients for global search.
      const designGroups = (rawGrps || []).map((g) => {
        const inGroup = (rawTerms || []).filter((t) => t.GroupID === g.ID);
        return {
          id: g.ID,
          name: g.Name,
          startIp: g.StartIP || "—",
          max: g.ClientMax || inGroup.length || 0,
          gateway: g.Gateway || "—",
          mask: g.Netmask || "—",
          dns: g.DNS1 || "—",
          image:
            (imgById[g.SystemImageID] && imgById[g.SystemImageID].Name) || "—",
          total: g.ClientMax || inGroup.length || 0,
          online: inGroup.filter((t) => t.State === "online").length,
        };
      });
      const grpById = {};
      designGroups.forEach((g) => {
        grpById[g.id] = g;
      });

      keepIfSame(setImages, (rawImgs || []).map(mapImage));
      keepIfSame(setGroups, designGroups);
      // Replication delays may make local pool counts inaccurate; badges should agree with Storage Management and Overview
      // by using pools reported by each node (.length).
      const clustered = (rawNodes || []).length > 1;
      keepIfSame(
        setPools,
        clustered && clusterPools
          ? (clusterPools.nodes || []).flatMap((n) => n.pools || [])
          : rawPools || [],
      );
      keepIfSame(
        setTerminals,
        (rawTerms || []).map((t, i) => ({
          idx: i + 1,
          id: t.ID,
          mac: t.MAC,
          ip: t.IP,
          group: t.GroupID,
          state:
            t.State === "online"
              ? "on"
              : t.State === "offline"
                ? "off"
                : "warn",
          image: (grpById[t.GroupID] && grpById[t.GroupID].image) || "—",
          superAdmin: !!t.IsSuper,
        })),
      );

      if (taskR.status === "fulfilled") {
        keepIfSame(setTasks, (rawTasks || []).map(mapTask));
      }
      if (alarmR.status === "fulfilled") {
        keepIfSame(
          setAlarms,
          (rawAlarms || []).map((a) => ({
            id: a.ID,
            severity: a.Severity,
            status:
              a.Status === "recovered"
                ? "recovered"
                : a.Status === "acknowledged"
                  ? "acknowledged"
                  : "active",
            type: a.Type,
            resource: a.Resource,
            value: a.Value,
            threshold: a.Threshold,
            time: fmtDateTime(a.UpdatedAt || a.CreatedAt),
          })),
        );
      }
    };
    hydrate().catch(() => {
      /* Keep collections empty if all fetches fail. */
    });
    const id = setInterval(
      () =>
        hydrate().catch(() => {
          /* Retry later. */
        }),
      30000,
    );
    // Refresh badges immediately after mutations such as deleting images or creating pools.
    const unsubscribe = onMutation.subscribe(() =>
      hydrate().catch(() => {
        /* Retry later. */
      }),
    );
    return () => {
      cancelled = true;
      clearInterval(id);
      unsubscribe();
    };
  }, []);

  return (
    <StoreCtx.Provider
      value={{
        pools,
        groups,
        images,
        alarms,
        terminals,
        tasks,
        route,
        goto,
        toast,
      }}
    >
      {children}
      <ToastStack toasts={toasts} onDismiss={dismissToast} />
    </StoreCtx.Provider>
  );
}

function useStore() {
  return React.useContext(StoreCtx);
}

// keepIfSame preserves reference identity when polled content is unchanged so React does not rerender unnecessarily.
// Avoid rerendering the provider and its subtree.
// Compare JSON content because field-by-field checks can miss future API additions.
// Lists are small enough that serialization costs less than full-tree rendering.
function keepIfSame(setter, next) {
  setter((prev) => (sameContent(prev, next) ? prev : next));
}

function sameContent(a, b) {
  if (a === b) return true;
  try {
    return JSON.stringify(a) === JSON.stringify(b);
  } catch {
    return false; // Treat circular references as changed rather than miss an update.
  }
}

function ToastStack({ toasts, onDismiss }) {
  return (
    <div
      style={{
        position: "fixed",
        top: 24,
        left: "50%",
        transform: "translateX(-50%)",
        zIndex: "var(--z-toast)",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        gap: 8,
        pointerEvents: "none",
      }}
    >
      {toasts.map((t) => (
        <div
          key={t.id}
          className="card"
          onClick={() => onDismiss && onDismiss(t.id)}
          title="Click to dismiss"
          style={{
            padding: "10px 14px",
            display: "flex",
            alignItems: "center",
            gap: 10,
            minWidth: 260,
            maxWidth: "min(560px, 90vw)",
            pointerEvents: "auto",
            cursor: "pointer",
            borderColor:
              t.kind === "ok"
                ? "oklch(0.55 0.1 155 / 0.5)"
                : t.kind === "err"
                  ? "oklch(0.55 0.14 18 / 0.5)"
                  : "var(--line)",
            background:
              t.kind === "ok"
                ? "var(--emerald-soft)"
                : t.kind === "err"
                  ? "var(--rose-soft)"
                  : "var(--bg-2)",
            animation: "fadeIn 0.2s",
          }}
        >
          <span
            className={`dot ${t.kind === "ok" ? "live" : t.kind === "err" ? "err" : "cyan"}`}
          />
          <span
            style={{
              fontSize: 13,
              color:
                t.kind === "ok"
                  ? "var(--emerald)"
                  : t.kind === "err"
                    ? "var(--rose)"
                    : "var(--fg)",
            }}
          >
            {t.msg}
          </span>
        </div>
      ))}
    </div>
  );
}

window.StoreProvider = StoreProvider;
window.useStore = useStore;

export { StoreProvider, useStore };
