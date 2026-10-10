// Application shell: sidebar, top bar and page routing with real backend sessions and authentication.
import React from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

import { Icons } from './design/icons.jsx';
import { Logo, Wordmark } from './design/brand.jsx';
import { StoreProvider, useStore } from './design/store.jsx';
import { Modal, ConfirmProvider } from './design/overlay.jsx';
import { StandbyBanner } from './design/ha.jsx';
import { Field } from './design/primitives.jsx';
import { GlobalSearch, NotifPopover } from './design/global-search.jsx';
import { LoginPage } from './design/pages/login.jsx';
import { api } from './lib/api.js';
import { PageOverview } from './design/pages/overview.jsx';
import { PageServers } from './design/pages/servers.jsx';
import { PageTerminals } from './design/pages/terminals.jsx';
import { PageImages } from './design/pages/images.jsx';
import { PageGroups } from './design/pages/groups.jsx';
import { PageAlarms } from './design/pages/alarms.jsx';
import { PageStorage } from './design/pages/storage.jsx';
import { PageTasks } from './design/pages/tasks.jsx';
import { PageDrivers } from './design/pages/drivers.jsx';
import { PageLogs } from './design/pages/logs.jsx';
import { PageSettings, PageSystemParams } from './design/pages/settings.jsx';
import { clearSession, getStoredUser, getToken, onUnauthorized } from './lib/api.js';

const { useState: useS, useEffect: useE } = React;

// PAGES is the single source of truth for sidebar groups/order, routes, breadcrumbs, headers, sidebar badges,
// and Alt-N shortcuts. Add a page here; dynamic count/header properties can be store functions.
const PAGES = [
  { section: "Operations Monitoring" },
  { id: "overview",  label: "Overview",         icon: Icons.Dashboard, component: PageOverview,
    header: { h: "Overview", s: "Real-time diskless system overview · Clients, groups, storage and images" } },
  { id: "alarms",    label: "Alert Center",     icon: Icons.Bell, accent: "amber", component: PageAlarms,
    count: (store) => store.alarms.filter(a => a.status === "active").length,
    header: { h: "Alert Center", s: "Server, storage, heartbeat and service alerts · Manage and archive" } },
  { id: "tasks",     label: "Task Center",     icon: Icons.Tasks, component: PageTasks,
    count: (store) => store.tasks.filter(t => t.status === "running" || t.status === "waiting").length,
    header: { h: "Task Center", s: "Background jobs for image distribution, snapshots, backups and more" } },

  { section: "Resource Management" },
  { id: "servers",   label: "Server Management",   icon: Icons.Server, component: PageServers, shortcut: "1",
    header: { h: "Server Management", s: "Node status · Diskless services (dnsmasq / iSCSI / ZFS) and data backup" } },
  { id: "storage",   label: "Storage Pool Management",   icon: Icons.Database, component: PageStorage, shortcut: "4",
    count: (store) => store.pools.length,
    header: { h: "Storage Pool Management", s: "ZFS pool capacity, read/write caches, and disk membership" } },

  { id: "images",    label: "Image Management",     icon: Icons.Layers, component: PageImages, shortcut: "3",
    count: (store) => store.images.length,
    header: { h: "Image Management", s: "Import images · Configuration versions and restore points" } },
  { id: "groups",    label: "Group Management",     icon: Icons.Group, component: PageGroups,
    count: (store) => store.groups.length,
    header: { h: "Group Management", s: "Organize clients by location and configuration · Network settings and image assignment" } },
  { id: "terminals", label: "Client Management",   icon: Icons.Monitor, component: PageTerminals, shortcut: "2",
    count: (store) => store.terminals.length,
    // The count appears in the sidebar badge and page filters; avoid repeating it in the subtitle.
    header: { h: "Client Management", s: "Online status, groups, and system images" } },
  { id: "drivers",   label: "Driver Management",     icon: Icons.Cpu, component: PageDrivers,
    header: { h: "Driver Management", s: "Driver packages · INF parsing, boot driver bundles, and risk indicators" } },

  { section: "System Administration" },
  { id: "logs",      label: "Operation Logs",     icon: Icons.Log, component: PageLogs,
    header: { h: "Audit Logs", s: "按用户、时间、模块查询Operation Logs与Login Logs" } },
  { id: "settings",  label: "User Management",     icon: Icons.User, component: PageSettings,
    header: { h: "User Management", s: "Manage accounts, passwords, and administrators" } },
  { id: "system-params", label: "System Settings", icon: Icons.Cog, component: PageSystemParams,
    header: { h: "System Settings", s: "Global runtime settings and service directories" } },
];
const PAGE_BY_ID = Object.fromEntries(PAGES.filter(p => p.id).map(p => [p.id, p]));

function Sidebar({ onLogout, account }) {
  const store = useStore();
  const [userMenuOpen, setUserMenuOpen] = useS(false);
  const [pwOpen, setPwOpen] = useS(false);
  const [version, setVersion] = useS("");
  const userMenuRef = React.useRef(null);
  const name = account?.username || account?.Username || "admin";
  const avatar = name.charAt(0).toUpperCase();
  // Fetch the actual backend version when the menu first opens.
  useE(() => {
    if (!userMenuOpen || version) return;
    api.getServer().then(s => setVersion(s?.version || "")).catch(() => {});
  }, [userMenuOpen, version]);
  useE(() => {
    if (!userMenuOpen) return;
    const onDoc = (e) => { if (userMenuRef.current && !userMenuRef.current.contains(e.target)) setUserMenuOpen(false); };
    const onKey = (e) => { if (e.key === "Escape") setUserMenuOpen(false); };
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("mousedown", onDoc); document.removeEventListener("keydown", onKey); };
  }, [userMenuOpen]);
  return (
    <aside className="sidebar">
      <div className="brand">
        <Logo size={34}/>
        <div>
          <div className="brand-name"><Wordmark size={15}/></div>
          <div className="brand-sub">Diskless Endpoint</div>
        </div>
      </div>

      <div className="nav">
        {PAGES.map((it, i) => {
          if (it.section) return <div key={i} className="nav-section">{it.section}</div>;
          const count = it.count ? it.count(store) : null;
          return (
            <div key={it.id} className={`nav-item ${store.route.page === it.id ? "active" : ""}`} onClick={() => store.goto(it.id)}>
              <it.icon size={16}/>
              <span>{it.label}</span>
              {count != null && <span className="count" style={it.accent && count > 0 ? { color: `var(--${it.accent})` } : null}>{count}</span>}
            </div>
          );
        })}
      </div>

      <div className="sidebar-foot" ref={userMenuRef} style={{ position: "relative" }}>
        <div
          className={`user-trigger ${userMenuOpen ? "open" : ""}`}
          onClick={() => setUserMenuOpen(o => !o)}
          role="button"
          tabIndex={0}
        >
          <div className="avatar">{avatar}</div>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontSize: 13, fontWeight: 500 }}>{name}</div>
            <div className="meta">Administrator</div>
          </div>
          <Icons.Chevron size={12} className={`user-trigger-chev ${userMenuOpen ? "rot" : ""}`}/>
        </div>
        {userMenuOpen && (
          <div className="user-menu">
            <div className="user-menu-head">
              <div className="avatar" style={{ width: 36, height: 36, fontSize: 14 }}>{avatar}</div>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 14, fontWeight: 600 }}>{name}</div>
                <div className="meta" style={{ marginTop: 2 }}>Administrator</div>
              </div>
              <span className="chip emerald"><span className="dot live"/> Online</span>
            </div>
            <div className="user-menu-body">
              <button className="user-mi" onClick={() => { setUserMenuOpen(false); store.goto("settings"); }}>
                <Icons.User size={14}/><span>User Management</span>
              </button>
              <button className="user-mi" onClick={() => { setUserMenuOpen(false); setPwOpen(true); }}>
                <Icons.Lock size={14}/><span>Change Password</span>
              </button>
              <button className="user-mi" onClick={() => { setUserMenuOpen(false); store.goto("logs"); }}>
                <Icons.Log size={14}/><span>Login Logs</span>
              </button>
              <div className="user-menu-sep"/>
              <button className="user-mi danger" onClick={() => { setUserMenuOpen(false); onLogout && onLogout(); }}>
                <Icons.Power size={14}/><span>Sign Out</span>
                <span style={{ flex: 1 }}/>
                <Icons.Chevron size={11}/>
              </button>
            </div>
            <div className="user-menu-foot mono">
              ndiskless {version || "—"}
            </div>
          </div>
        )}
      </div>
      <ChangePasswordModal open={pwOpen} account={account} onClose={() => setPwOpen(false)} onChanged={onLogout}/>
    </aside>
  );
}

// Change the current user's password. On success, sign out and use the new password.
function ChangePasswordModal({ open, account, onClose, onChanged }) {
  const store = useStore();
  const [form, setForm] = useS({ old_password: "", new_password: "" });
  const [busy, setBusy] = useS(false);
  useE(() => { if (open) setForm({ old_password: "", new_password: "" }); }, [open]);

  const submit = async () => {
    if (!form.old_password || !form.new_password) { store.toast("Enter the current and new passwords", "err"); return; }
    if (form.new_password.length < 6) { store.toast("The new password must be at least 6 characters", "err"); return; }
    const id = account?.id || account?.ID;
    if (!id) { store.toast("未识别当前用户，请在「User Management」中修改", "err"); return; }
    setBusy(true);
    try {
      await api.changeUserPassword(id, { old_password: form.old_password, new_password: form.new_password });
      store.toast("Password updated. Sign in again with the new password", "ok");
      onClose();
      onChanged && onChanged();
    } catch (e) {
      store.toast(e.message || "Update failed", "err");
    } finally { setBusy(false); }
  };

  return (
    <Modal open={open} onClose={() => !busy && onClose()} title="Change Password" size="sm"
      footer={<><button className="btn" disabled={busy} onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy} onClick={submit}>{busy ? "Submitting…" : "Confirm"}</button></>}>
      <Field label="Current Password" required><input className="input mono" type="password" style={{ width: "100%" }} value={form.old_password} onChange={e => setForm({ ...form, old_password: e.target.value })}/></Field>
      <Field label="New Password" required hint="At least 6 characters. Sign in again after changing it"><input className="input mono" type="password" style={{ width: "100%" }} value={form.new_password} onChange={e => setForm({ ...form, new_password: e.target.value })}/></Field>
    </Modal>
  );
}

function Topbar({ onSearch, onNotif, notifBtnRef }) {
  const store = useStore();
  const [now, setNow] = useS(new Date());
  useE(() => { const id = setInterval(() => setNow(new Date()), 1000); return () => clearInterval(id); }, []);
  const fmt = (n) => String(n).padStart(2,"0");
  const time = `${fmt(now.getHours())}:${fmt(now.getMinutes())}:${fmt(now.getSeconds())}`;

  return (
    <header className="topbar">
      <div className="crumbs">
        <Icons.Dashboard size={13}/>
        <span style={{ cursor: "pointer" }} onClick={() => store.goto("overview")}>Console</span>
        <Icons.Chevron size={11} className="sep"/>
        <span className="cur">{PAGE_BY_ID[store.route.page]?.label || store.route.page}</span>
      </div>
      <div className="grow"/>
      <div className="search" onClick={onSearch} style={{ cursor: "pointer" }}>
        <Icons.Search size={13}/>
        <span className="ellip" style={{ flex: 1, color: "var(--fg-faint)" }}>Search servers / clients / images / alerts...</span>
        <kbd>⌘K</kbd>
      </div>
      <div className="row" style={{ gap: 10 }}>
        <SystemHealthChip/>
        <span className="mono" style={{ fontSize: 12, color: "var(--fg-mute)" }}>{time}</span>
        <button ref={notifBtnRef} className="btn ghost icon" onClick={onNotif} style={{ position: "relative" }}>
          <Icons.Bell size={15}/>
          {store.alarms.filter(a => a.status === "active").length > 0 &&
            <span style={{ position: "absolute", top: 4, right: 4, width: 6, height: 6, borderRadius: "50%", background: "var(--amber)", boxShadow: "0 0 0 2px var(--bg-1)" }}/>}
        </button>
      </div>
    </header>
  );
}

// Derive system health from active alerts loaded by the store; click to open Alert Center.
function SystemHealthChip() {
  const store = useStore();
  const active = store.alarms.filter(a => a.status === "active");
  const errors = active.filter(a => a.severity === "error").length;
  const cls = errors > 0 ? "rose" : active.length > 0 ? "amber" : "emerald";
  const dot = errors > 0 ? "err" : active.length > 0 ? "warn" : "live";
  const text = active.length > 0 ? `${active.length}  alerts` : "System healthy";
  return (
    <span className={`chip ${cls}`} style={{ cursor: "pointer" }} title="点击查看Alert Center"
      onClick={() => store.goto("alarms")}>
      <span className={`dot ${dot}`}/> {text}
    </span>
  );
}

function AppInner({ onLogout, account }) {
  const store = useStore();
  const page = store.route.page;
  const [searchOpen, setSearchOpen] = useS(false);
  const [notifOpen, setNotifOpen] = useS(false);
  const notifBtnRef = React.useRef(null);

  useE(() => {
    const onKey = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") { e.preventDefault(); setSearchOpen(true); }
      if (e.key === "Escape") { setSearchOpen(false); setNotifOpen(false); }
      if (e.altKey) {
        const target = PAGES.find(p => p.shortcut === e.key);
        if (target) store.goto(target.id);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const pg = PAGE_BY_ID[page] || PAGE_BY_ID.overview;
  const Content = pg.component;
  const head = typeof pg.header === "function" ? pg.header(store) : pg.header;

  return (
    <div className="app" data-screen-label={page}>
      <Sidebar onLogout={onLogout} account={account}/>
      <Topbar onSearch={() => setSearchOpen(true)} onNotif={() => setNotifOpen(o => !o)} notifBtnRef={notifBtnRef}/>
      <StandbyBanner/>
      <GlobalSearch open={searchOpen} onClose={() => setSearchOpen(false)}/>
      <NotifPopover open={notifOpen} onClose={() => setNotifOpen(false)}/>
      <main className="main">
        {head && (
          <div className="page-head">
            <div className="page-title">
              <h1>{head.h}</h1>
              <div className="sub">{head.s}</div>
            </div>
          </div>
        )}
        <Content/>
      </main>
    </div>
  );
}

function App() {
  const [user, setUser] = useS(() => (getToken() ? getStoredUser() : null));
  const [leaving, setLeaving] = useS(false);

  useE(() => onUnauthorized.subscribe(() => { setUser(null); setLeaving(false); }), []);

  const handleLogout = () => {
    clearSession();
    setUser(null);
    setLeaving(false);
  };

  if (!user) {
    return (
      <div className={leaving ? "login-root leaving" : ""} style={leaving ? { position: "fixed", inset: 0, zIndex: 100 } : null}>
        <LoginPage onLogin={(u) => {
          setLeaving(true);
          setTimeout(() => setUser(u || getStoredUser() || { username: "admin" }), 520);
        }}/>
      </div>
    );
  }

  return <StoreProvider><ConfirmProvider><AppInner onLogout={handleLogout} account={user}/></ConfirmProvider></StoreProvider>;
}

createRoot(document.getElementById('root')).render(<App/>);
