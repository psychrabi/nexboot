// Operator-facing high availability: local role, peer replication, and the planned switchover action.
// The backend blocks unsafe operations (503 on standby writes); this UI guides operators.
import React from 'react';
import { Icons } from './icons.jsx';
import { useStore } from './store.jsx';
import { Modal } from './overlay.jsx';
import { Tip } from './primitives.jsx';
import { api } from '../lib/api.js';
import { useResource, useMutation } from '../lib/hooks.js';
const { useState, useEffect, useRef } = React;

const ROLE = {
  active:  { chip: 'emerald', text: 'Active' },
  standby: { chip: 'amber',   text: 'Standby' },
};

// HACard is shown in system settings; explain standalone nodes without treating lack of peers as an error.
export function HACard() {
  const store = useStore();
  const { data, loading, error, reload } = useResource(() => api.getHA(), []);
  const repl = useResource(() => api.getReplication(), []);
  // The active node's peer_url is its own VIP; list other nodes from the node roster.
  const roster = useResource(() => api.listClusterNodes(), []);
  const { busy, run } = useMutation(store.toast);
  const [confirmOn, setConfirmOn] = useState(false);
  const [rate, setRate] = useState(null); // null means unchanged
  const settle = useRef(null);
  useEffect(() => () => clearTimeout(settle.current), []);

  const ha = data || {};
  const paired = !!ha.peer_url;
  const role = ROLE[ha.role] || { chip: '', text: ha.role || '…' };
  const targets = (repl.data && repl.data.targets) || [];
  const nodes = (roster.data && roster.data.items) || [];
  const others = nodes.filter(n => n.id !== ha.node_id);
  const selfIP = (nodes.find(n => n.id === ha.node_id) || {}).ip;

  const doSwitch = async () => {
    if (await run(() => api.plannedSwitch(),
      'Planned switchover started: this node will restart as standby and the virtual IP will move to another standby node', { errMsg: 'Switchover not executed' })) {
      setConfirmOn(false);
      // The service restarts underneath; refresh resources once the new role stabilizes.
      settle.current = setTimeout(() => { reload(); repl.reload(); roster.reload(); }, 4000);
    }
  };

  return (
    <div className="card" style={{ padding: 0 }}>
      <div className="card-h">
        <div className="card-title"><Icons.Server size={14}/> High Availability</div>
      </div>
      <div style={{ padding: 16 }}>
        {loading && <div className="hint">Loading…</div>}
        {error && <div style={{ fontSize: 13, color: 'var(--rose)' }}>Could not load: {error}</div>}
        {!loading && !error && !paired && <StandaloneCluster />}
        {!loading && !error && paired && (
          <>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: 16, fontSize: 14 }}>
              <div data-testid="ha-self">
                <div className="row meta" style={{ gap: 6, minHeight: 22 }}>
                  Local node <span className={`chip ${role.chip}`}>{role.text}</span>
                </div>
                <div className="row" style={{ gap: 6, marginTop: 6, minHeight: 32 }}>
                  <span className="mono" title={`Term ${ha.epoch} · Node ${ha.node_id}`}>{selfIP || ha.node_id}</span>
                  {ha.role === 'active' && (
                    <>
                      <button className="btn" style={{ padding: '2px 10px', marginLeft: 4 }} disabled={busy} onClick={() => setConfirmOn(true)}>Planned Switchover</button>
                      <Tip label="About Planned Switchover" text="Before patching, restarting, or replacing local hardware, transfer the active role to a standby node. The system waits for catalogue replication and aborts if it cannot catch up. Running clients will restart once."/>
                    </>
                  )}
                </div>
              </div>
              {ha.role === 'active' ? (
                <div>
                  <div className="row meta" style={{ gap: 6, minHeight: 22 }}>
                    Other nodes {!roster.error && others.some(n => n.online && (!n.ha_state || n.ha_state === 'standby')) && <span className="chip amber">Standby</span>}
                  </div>
                  <div data-testid="ha-others" className="row" style={{ flexWrap: 'wrap', gap: 12, marginTop: 6, minHeight: 32 }}>
                    {roster.loading && <span className="meta">Loading…</span>}
                    {!roster.loading && roster.error && <span style={{ color: 'var(--rose)' }}>Cannot retrieve node list: {roster.error}</span>}
                    {!roster.loading && !roster.error && others.length === 0 && <span className="meta">None</span>}
                    {!roster.error && others.map(n => (
                      <span key={n.id} className="row" style={{ gap: 4 }}>
                        <span className="mono">{n.ip || n.id}</span>
                        {!n.online
                          ? <span className="chip rose">Offline</span>
                          : n.ha_state && n.ha_state !== 'standby' && <span className={`chip ${(ROLE[n.ha_state] || {}).chip || ''}`}>{(ROLE[n.ha_state] || {}).text || n.ha_state}</span>}
                      </span>
                    ))}
                  </div>
                </div>
              ) : (
                <div>
                  <div className="row meta" style={{ gap: 6, minHeight: 22 }}>
                    Active {ha.peer_reachable
                      ? <span className={`chip ${(ROLE[ha.peer_role] || {}).chip || ''}`}>{(ROLE[ha.peer_role] || {}).text || ha.peer_role}</span>
                      : <span className="chip rose">Unreachable</span>}
                  </div>
                  <div className="mono" style={{ marginTop: 6, minHeight: 32, display: 'flex', alignItems: 'center' }}>{ha.peer_url}</div>
                </div>
              )}
              <div>
                <div className="row meta" style={{ gap: 6, minHeight: 22 }}>
                  Catalogue Replication <span style={{ color: 'var(--fg)' }}>{replSummary(targets)}</span>
                </div>
                {ha.role === 'active' && (
                  <div className="row" style={{ flexWrap: 'wrap', gap: 8, marginTop: 6, minHeight: 32 }}>
                    <label className="meta" htmlFor="ha-rate">Replication Rate Limit</label>
                    <input id="ha-rate" aria-label="Replication Rate Limit" className="input" style={{ width: 90 }} type="number" min="0"
                      value={rate === null ? String(ha.rate_mbps ?? '') : rate}
                      onChange={e => setRate(e.target.value)}/>
                    <span className="meta">MB/s{ha.rate_source === 'default' ? ' (default)' : ''}</span>
                    {rate !== null && rate !== String(ha.rate_mbps) && (
                      <button className="btn" disabled={busy} onClick={async () => {
                        const n = parseInt(rate, 10);
                        if (Number.isNaN(n) || n < 0) { store.toast('Limit must be a nonnegative integer (0 means unlimited)', 'err'); return; }
                        if (await run(() => api.setHARate(n),
                          n === 0 ? 'Rate limit removed — ensure no clients are in use; replication may saturate the link' : `Replication limit set to ${n} MB/s`,
                          { errMsg: 'Save failed' })) { setRate(null); reload(); }
                      }}>Save Limit</button>
                    )}
                    <Tip label="About Replication Limits" text="Catalogue replication shares network bandwidth with clients. Keep a limit during business hours, or set zero for unlimited bandwidth while idle. Changes apply to the next replication cycle."/>
                  </div>
                )}
              </div>
            </div>
            {ha.role === 'standby' && (
              <div className="hint" style={{ marginTop: 12 }}>
                This node is a standby. Perform administrative operations on the active node at <a className="mono" href={ha.peer_url} style={{ color: 'var(--cyan)' }}>{ha.peer_url}</a>.
              </div>
            )}
          </>
        )}
      </div>

      <Modal open={confirmOn} onClose={() => !busy && setConfirmOn(false)} title="Planned Switchover" size="md"
        footer={<>
          <button className="btn" disabled={busy} onClick={() => setConfirmOn(false)}>Cancel</button>
          <button className="btn primary" disabled={busy} onClick={doSwitch}>{busy ? 'Switching…' : 'Switch'}</button>
        </>}>
        <div className="hint">
          First catch up catalogue replication (up to 3 minutes; cancel if synchronization fails). Then this node restarts as standby and 
          the virtual IP moves to a standby node. <b>Running regular clients will restart once</b> (approximately 2–4 minutes to return to the desktop). Unsaved Super Admin sessions will be lost; schedule this during downtime.
        </div>
      </Modal>
    </div>
  );
}

// StandaloneCluster creates the first cluster node; subsequent nodes join through Add Node.
function StandaloneCluster() {
  const store = useStore();
  const { busy, run } = useMutation(store.toast);
  const [creating, setCreating] = useState(false);
  const [vip, setVip] = useState('');

  const submitCreate = async () => {
    if (!vip.trim()) { store.toast('Enter the virtual IP', 'err'); return; }
    if (await run(() => api.createCluster({ vip: vip.trim() }),
      'Creating cluster: this node will restart as active and assume the virtual IP. Use the virtual IP to access the console afterward')) {
      setCreating(false);
    }
  };

  return (
    <>
      <div className="hint">This node is standalone and is not part of a cluster.</div>
      <div style={{ marginTop: 12, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <button className="btn primary" onClick={() => setCreating(true)}>Create Cluster…</button>
        <span className="hint">Create the cluster on the first node; add other nodes through Server Management.</span>
      </div>

      <Modal open={creating} onClose={() => !busy && setCreating(false)} title="Create Cluster" size="md"
        footer={<>
          <button className="btn" disabled={busy} onClick={() => setCreating(false)}>Cancel</button>
          <button className="btn primary" disabled={busy} onClick={submitCreate}>{busy ? 'Creating…' : 'Create'}</button>
        </>}>
        <div style={{ display: 'grid', gap: 12 }} data-testid="create-cluster-form">
          <div className="hint">
            This node becomes <b>Active</b>; the console and clients will use the virtual IP. Add other machines in Server Management → Add Node.
            The cluster can be created before a data pool; create one later under Storage Pool Management. A standby cannot take over until then.
          </div>
          <label style={{ display: 'grid', gap: 4 }}>
            <span className="meta">Virtual IP</span>
            <input className="input" placeholder="e.g. 192.168.10.250" value={vip}
              autoFocus onChange={e => setVip(e.target.value)} />
            <span className="hint">An unused address on the client subnet, outside the IP ranges assigned to groups.</span>
          </label>
        </div>
      </Modal>

    </>
  );
}

function replSummary(targets) {
  if (!targets.length) return <span className="meta">No replication records</span>;
  const t = targets[0];
  if (t.last_error) return <span style={{ color: 'var(--rose)' }}>Error: {t.last_error}</span>;
  if (!t.last_snapshot) return <span className="meta">Not yet replicated</span>;
  return <span>{t.lag_seconds} seconds ago <span className="mono meta">({t.last_snapshot})</span></span>;
}

// StandbyBanner links to the active node on every standby page, before the user attempts a blocked write.
// If a standby holds the VIP, explain why writes are still blocked and what happens next.
export function StandbyBanner() {
  const { data, reload } = useResource(() => api.getHA(), []);
  const ha = data || {};
  const takingOver = ha.role === 'standby' && !!ha.holds_vip;
  React.useEffect(() => {
    if (!takingOver) return undefined;
    const timer = setInterval(reload, 10000); // Poll during transition to avoid stale status
    return () => clearInterval(timer);
  }, [takingOver, reload]);
  if (ha.role !== 'standby') return null;
  const bar = {
    padding: '6px 14px', fontSize: 14, background: 'rgba(245,158,11,.12)',
    borderBottom: '1px solid var(--line)', display: 'flex', gap: 8, alignItems: 'center',
  };
  if (takingOver) {
    const t = ha.takeover || {};
    return (
      <div role="status" style={{ ...bar, flexWrap: 'wrap' }}>
        <Icons.Shield size={14}/>
        {t.phase === 'refused' ? (
          <>
            <span>This node holds the virtual IP but has not become active: {t.reason}。</span>
            <span className="hint">No action is needed. Replication and takeover should complete automatically within a few minutes. If still pending after ten minutes, inspect ndiskless and keepalived on the former active node.</span>
          </>
        ) : (
          <span>This node holds the virtual IP and is becoming active{t.reason ? `：${t.reason}` : '; the page will refresh shortly'}. Administrative operations are temporarily unavailable.</span>
        )}
      </div>
    );
  }
  return (
    <div role="status" style={bar}>
      <Icons.Shield size={14}/>
      <span>This is a standby node (read-only replica). Perform administrative operations on the active node</span>
      <a className="mono" href={ha.peer_url} style={{ color: 'var(--cyan)' }}>{ha.peer_url}</a>
      <span> .</span>
    </div>
  );
}
