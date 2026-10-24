import React from 'react';
import { Icons } from '../icons.jsx';
import { useStore } from '../store.jsx';
import { Modal, useConfirm } from '../overlay.jsx';
import { Field, Select, TableState, Pager, SearchBox } from '../primitives.jsx';
import { api } from '../../lib/api.js';
import { fmtDateTime, MODEL_RECORD_SOURCES } from '../../lib/format.js';
import { useResource, useMutation, usePolling, usePaged } from '../../lib/hooks.js';
const { useState, useEffect } = React;
const STATUS = { ok: ['正常', 'emerald'], waiting: ['等待首次开机', ''], failed: ['开机失败', 'rose'], adapting: ['适配中', 'amber'], idle: ['暂无客户机', ''] };
const BOOT_LABELS = { ok: '已开机', failed: '开机失败', waiting: '未确认' };

function PageModels() {
  const store = useStore();
  const [tab, setTab] = useState(store.route.params.tab || 'models');
  const [pick, setPick] = useState(null);
  const [adapt, setAdapt] = useState(null);
  const [q, setQ] = useState('');
  const [filterProfileID, setFilterProfileID] = useState('');
  const { data, loading, error, reload } = useResource(() => api.listModels(), []);
  const models = data || [];
  const pager = usePaged(models.filter(m => filterProfileID ? m.id === filterProfileID : !q || `${m.name} ${m.nic_name || ''} ${m.nic_label}`.toLowerCase().includes(q.toLowerCase())));
  usePolling(reload, { intervalMs: 5000, active: tab === 'models' });
  useEffect(() => {
    if (store.route.params.tab) setTab(store.route.params.tab);
    if (store.route.params.filterProfileId) {
      setTab('models');
      setQ(store.route.params.filterProfileName || '');
      setFilterProfileID(store.route.params.filterProfileId);
      setPick(null);
      setAdapt(null);
      pager.setPage(1);
    } else {
      setFilterProfileID('');
      if (store.route.params.profileId) {
        setPick({ id: store.route.params.profileId });
        if (store.route.params.adapt) setAdapt({ id: store.route.params.profileId, configId: store.route.params.sourceConfigId, terminalId: store.route.params.sourceTerminalId });
      }
    }
  }, [store.route.params]);
  const openAdapt = (id, configId) => {
    const source = store.route.params.filterProfileId === id ? store.route.params : null;
    setAdapt({ id, configId: configId || source?.sourceConfigId, terminalId: source?.sourceTerminalId });
  };
  return <div className="fade-in" style={{ minWidth: 0 }}>
      <div className="tabs">
        <button style={{ background: 'transparent', borderTop: 0, borderLeft: 0, borderRight: 0 }} className={`tab ${tab === 'models' ? 'active' : ''}`} onClick={() => setTab('models')}>机型 <span className="count">{models.length}</span></button>
        <button style={{ background: 'transparent', borderTop: 0, borderLeft: 0, borderRight: 0 }} className={`tab ${tab === 'packs' ? 'active' : ''}`} onClick={() => setTab('packs')}>驱动包</button>
      </div>
      {tab === 'models' && <>
        <p className="hint">Windows 客户机按启动网卡归成机型，新机型首次开机时自动生成网卡配置，一般不用处理；这里看每种机型在当前系统上的开机结果。Ubuntu/Linux 不归入机型。</p>
        <details className="hint" style={{ marginBottom: 14 }}>
          <summary style={{ cursor: 'pointer' }}>机型与开机说明</summary>
          <ol>
            <li>先开机自动准备；若进入维护环境读取网卡信息，读取成功后会自动重启并继续启动，失败时按任务提示处理。启动路径见客户机详情的「预计下次启动」。</li>
            <li>收到 Windows 开机成功回报才算正常，完整适配后也要再次开机验证。</li>
            <li>仍无法开机：选择目标系统配置和一台关机客户机，填写新还原点名，提交完整适配。按提示开机，等自动关机和任务成功后再开机。新还原点会设为配置当前点，使用该配置的客户机下次开机生效。</li>
          </ol>
        </details>
      </>}
      {tab === 'packs' ? <>{adapt && <p><button className="btn" onClick={() => setTab('models')}>返回机型</button></p>}<DriverPacks/></> : <div className="card" style={{ padding: 0 }}>
        <div className="card-h"><SearchBox placeholder="搜索机型 / 网卡" value={q} onChange={q => { setQ(q); setFilterProfileID(''); pager.setPage(1); }}/></div>
        <div style={{ overflowX: 'auto' }}><table className="t"><thead><tr><th>机型</th><th>启动网卡</th><th>固件</th><th>Windows 客户机</th><th>状态</th><th className="t-actions"/></tr></thead>
          <tbody><TableState colSpan={6} loading={loading} error={error} empty={!pager.rows.length} hint="Windows 客户机首次开机后自动识别机型；历史记录会保留"/>
            {pager.rows.map(m => <tr key={m.id}><td>{m.name}</td><td><div>{m.nic_name || m.nic_label}</div>{m.nic_name && <div className="mono meta">{m.nic_label}</div>}</td><td>{m.platform === 'efi' ? 'UEFI' : m.platform === 'pcbios' ? 'BIOS' : m.platform || '—'}</td><td>{m.terminals}</td><td><ModelStatus status={m.status}/>{bootSummary(m) && <div className="meta">{bootSummary(m)}</div>}</td>
              <td className="t-actions"><div>{m.status === 'failed' && <button className="btn" aria-label={`完整适配 ${m.name}`} onClick={() => openAdapt(m.id)}>完整适配</button>}<button className="btn" onClick={() => setPick({ id: m.id })}>详情</button></div></td></tr>)}
          </tbody></table></div><Pager {...pager.pager}/>
      </div>}
    {pick && <ModelDetail key={`detail-${pick.id}`} id={pick.id} open={tab === 'models' && !adapt}
      onClose={() => { setPick(null); if (!store.route.params.filterProfileId) store.goto('models'); }} onDone={reload}
      onAdapt={configId => openAdapt(pick.id, configId)}/>}
    {adapt && <ModelAdapt key={adapt.id} id={adapt.id} initialConfigID={adapt.configId} initialTerminalID={adapt.terminalId} open={tab === 'models'} onClose={() => setAdapt(null)} onDone={reload} onUpload={() => setTab('packs')}/ >}
  </div>;
}

function ModelStatus({ status }) {
  const [label, color] = STATUS[status] || STATUS.waiting;
  return <span className={`chip ${color}`}>{label}</span>;
}

function bootCounts({ ok, failed, waiting }) {
  return [ok && `${ok} 台已开机`, failed && `${failed} 台开机失败`, waiting && `${waiting} 台未确认`].filter(Boolean).join(' · ');
}

// 列表里状态下面那一行：失败只报失败台数，其余报已开机、未确认。
function bootSummary(m) {
  if (m.status === 'idle') return '';
  if (m.boot_failed) return `${m.boot_failed} 台开机失败`;
  return bootCounts({ ok: m.boot_ok, waiting: m.boot_waiting });
}

function adaptNextStep(task) {
  if (task?.Status === 'success') return '已保存并设为配置当前点；确认客户机已关机后，再次开机验证 Windows。';
  if (task?.Status === 'failed') return '适配未完成，请查看失败原因和客户机维护状态。';
  if (task?.Progress >= 90 || task?.Message?.includes('保存还原点')) return '等待服务器保存成功；此时无需开机。';
  if (task?.Progress >= 50 || task?.Message?.includes('已进入WinPE') || task?.Message?.includes('已进入 WinPE')) return '客户机已进入维护环境；等待处理完成并自动关机，再等待任务成功。';
  if (task?.Status === 'running' && /引导客户机|等待客户机开机|请开启/.test(task.Message || '')) return '现在启动所选客户机，进入维护环境；处理完成后会自动关机。';
  if (task?.Status === 'pending' || /准备.*环境/.test(task?.Message || '')) return '等待环境准备完成，按任务提示再开机。';
  return '正在读取任务状态，请按任务提示操作。';
}

function missingDriver(message) {
  return /找不到启动网卡.*(?:INF|驱动)|没有找到启动网卡|(?:没有匹配|未找到|缺少).*网卡驱动|请上传匹配的(?:已签名)?(?:网卡)?驱动(?:包)?/.test(message || '');
}

function ModelAdapt({ id, initialConfigID, initialTerminalID, open = true, onClose, onDone, onUpload }) {
  const store = useStore();
  const [form, setForm] = useState({ config_id: '', terminal_id: '', name: '' });
  const [result, setResult] = useState(null);
  const [submitError, setSubmitError] = useState('');
  const [returnToForm, setReturnToForm] = useState(false);
  const { busy, run } = useMutation(store.toast);
  const { data, loading, error } = useResource(async () => {
    const [model, images] = await Promise.all([api.getModel(id), api.listImages()]);
    const configs = (await Promise.all((images.items || []).filter(i => i.OSType === 'windows' && i.Purpose !== 'data').map(async i =>
      (await api.listConfigs(i.ID)).items.map(c => ({ ...c, label: `${i.Name} / ${c.Name}` }))))).flat();
    return { model, configs, terminals: (model.terminal_list || []).filter(t => t.state === 'offline') };
  }, [id]);
  useEffect(() => {
    if (data) {
      const preferred = initialConfigID || data.model.points?.find(p => p.failed && data.configs.some(c => c.ID === p.config_id))?.config_id;
      const terminal = initialTerminalID ? data.terminals.find(t => t.id === initialTerminalID)?.id : data.terminals[0]?.id;
      setForm(f => ({ ...f, config_id: f.config_id || (data.configs.some(c => c.ID === preferred) ? preferred : ''), terminal_id: f.terminal_id || terminal || '' }));
    }
  }, [data, initialConfigID, initialTerminalID]);
  const taskRes = useResource(() => result ? api.getTask(result.task_id) : Promise.resolve(null), [result?.task_id]);
  const sourceTerminal = data?.model.terminal_list?.find(t => t.id === initialTerminalID);
  const terminalHint = initialTerminalID && sourceTerminal?.state !== 'offline' && !form.terminal_id
    ? sourceTerminal?.state === 'online' ? `请先关闭来源客户机「${sourceTerminal.name || sourceTerminal.mac || initialTerminalID}」，再打开适配。` : '来源客户机不在当前可选列表，请确认机型和关机状态。'
    : !data?.model.terminal_list?.length ? '当前没有该机型的 Windows 客户机，请先在客户机管理中为一台该机型的客户机分配 Windows 镜像。'
      : !data.terminals.length ? '请先关闭一台该机型的 Windows 客户机，再打开适配。' : '选择同机型的一台关机客户机执行维护。';
  const task = taskRes.data;
  useEffect(() => { if (task?.Status === 'success') onDone?.(); }, [result?.task_id, task?.Status]);
  const failed = !!result && task?.Status === 'failed';
  const failureRes = useResource(async () => failed
    ? { ...(await api.terminalIdentity(form.terminal_id)), taskID: result.task_id }
    : null, [result?.task_id, task?.Status]);
  const next = failureRes.data?.next_boot;
  const checked = failureRes.data?.taskID === result?.task_id && !!next;
  const blocked = checked && (!!next.task_id || next.kind === 'wait');
  const canRetry = failed && checked && !failureRes.loading && !failureRes.error && !blocked;
  const retry = () => { setResult(null); taskRes.setData(null); failureRes.setData(null); };
  // 上传返程重新查占用；保存阶段失败的适配盘必须保留原任务。
  useEffect(() => {
    if (open) {
      if (returnToForm) failureRes.reload();
      setSubmitError('');
    } else if (failed) {
      failureRes.setData(null);
      setReturnToForm(true);
    }
  }, [open]);
  useEffect(() => {
    if (open && returnToForm && canRetry) { retry(); setReturnToForm(false); }
  }, [open, returnToForm, canRetry]);
  usePolling(taskRes.reload, { intervalMs: 3000, active: !!result && !['success', 'failed'].includes(task?.Status) });
  const submit = async () => {
    if (!form.config_id || !form.terminal_id || !form.name.trim()) { setSubmitError('请选择配置、关机客户机并填写还原点名称'); return; }
    setSubmitError('');
    await run(async () => {
      try { setResult(await api.adaptModel(form.terminal_id, { config_id: form.config_id, name: form.name.trim() })); }
      catch (e) { setSubmitError(e.message); throw e; }
    }, '适配任务已提交，请启动所选客户机', { reload: onDone });
  };
  return <Modal open={open} onClose={() => !busy && onClose()} title={`完整适配 · ${data?.model.name || ''}`} footer={<>
    <button className="btn" disabled={busy} onClick={onClose}>{result ? '关闭' : '取消'}</button>
    {canRetry && <button className="btn primary" onClick={retry}>修改并重试</button>}
    {!result && <button className="btn primary" disabled={busy || loading || !!error || !data?.terminals.length || !data?.configs.length} onClick={submit}>{busy ? '提交中…' : '开始适配'}</button>}
  </>}>
    {loading ? <div className="hint">加载中…</div> : error ? <div className="hint" role="alert">{error}</div> : result ? <>
      <p style={{ fontSize: 13 }}>{adaptNextStep(task)}</p>
      {task?.Status === 'failed' && <div role="alert" className="hint">{task.Error || '任务失败，请查看日志。'}</div>}
      <div className="col" style={{ marginTop: 14, gap: 8 }}>
        <div className="meta" style={{ color: 'var(--fg-mute)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{task?.Status === 'success' ? '适配完成' : task?.Status === 'failed' ? '适配失败' : task?.Message || '准备环境'}</div>
        <div className="row">
          <div className={`bar ${task?.Status === 'success' ? 'emerald' : task?.Status === 'failed' ? 'rose' : ''}`} style={{ flex: 1, minWidth: 0 }} role="progressbar" aria-label="完整适配进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={task ? task.Progress || 0 : undefined}>
            <span style={{ width: `${task?.Progress || 0}%` }}/>
          </div>
          <span className="mono" style={{ fontSize: 12, flexShrink: 0 }}>{task ? `${task.Progress || 0}%` : taskRes.error ? '—' : '读取中…'}</span>
        </div>
        <div><button className="btn" onClick={() => store.goto('tasks', { taskId: checked && next.task_id || result.task_id })}>查看任务与日志</button></div>
      </div>
      {failed && !failureRes.error && (!checked || failureRes.loading) && <p className="hint">正在确认客户机维护状态…</p>}
      {failed && failureRes.error && <div role="alert" className="hint">维护状态读取失败：{failureRes.error} <button className="btn" onClick={failureRes.reload}>重试维护状态</button></div>}
      {failed && blocked && <p className="hint">{next.reason === 'finalizing' || next.state === 'finalizing' ? '保存还原点尚未完成，请查看任务，等待保存完成后再操作。' : '客户机还有未完成的维护任务，请查看任务，结束后再重试。'}</p>}
      {canRetry && missingDriver(task.Error) && <p className="hint">准备匹配该启动网卡、已签名且启用的驱动包，再返回这里重试。 <button className="btn" onClick={onUpload}>上传匹配驱动包</button></p>}
      {taskRes.error && <div role="alert" className="hint">{taskRes.error} <button className="btn" onClick={taskRes.reload}>重试任务状态</button></div>}
    </> : <>
      <Field label="目标系统配置" required hint="为这个配置保存完整适配还原点。"><Select aria-label="目标系统配置" value={form.config_id} onChange={config_id => setForm(f => ({ ...f, config_id }))} options={[{ value: '', label: '请选择系统配置' }, ...data.configs.map(c => ({ value: c.ID, label: c.label }))]}/></Field>
      <Field label="关机客户机" required hint={terminalHint}><Select aria-label="关机客户机" value={form.terminal_id} onChange={terminal_id => setForm(f => ({ ...f, terminal_id }))} options={[{ value: '', label: '请选择关机客户机' }, ...data.terminals.map(t => ({ value: t.id, label: `${t.name || t.mac} · ${t.mac || 'MAC 未上报'}` }))]}/></Field>
      <Field label="还原点名称" required><input className="input" aria-label="还原点名称" style={{ width: '100%' }} value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))}/></Field>
      <p className="hint" style={{ color: 'var(--fg-mute)', fontSize: 13 }}>{form.config_id && <span>目标：{data.configs.find(c => c.ID === form.config_id)?.label}。 </span>}新还原点会设为配置当前点；使用这个配置的所有客户机下次开机生效。</p>
      <ol className="hint" style={{ paddingLeft: 20, color: 'var(--fg-mute)', fontSize: 13 }}><li>提交后，启动所选客户机，开机进入维护环境。</li><li>处理完成后自动关机；等待任务成功。</li><li>再次开机验证 Windows。</li></ol>
      {submitError && <div role="alert" className="hint">{submitError}{missingDriver(submitError) && <button className="btn" onClick={onUpload}>上传匹配驱动包</button>}</div>}
    </>}
  </Modal>;
}

function ModelDetail({ id, open, onClose, onDone, onAdapt }) {
  const store = useStore();
  const { data, error, reload } = useResource(() => api.getModel(id), [id]);
  usePolling(reload, { intervalMs: 5000 });
  const [name, setName] = useState('');
  const [rename, setRename] = useState(false);
  const [tab, setTab] = useState('points');
  const [clientQ, setClientQ] = useState('');
  const [pointQ, setPointQ] = useState('');
  const clients = usePaged((data?.terminal_list || []).filter(t => `${t.name || ''} ${t.mac || ''} ${t.group_name || ''}`.toLowerCase().includes(clientQ.toLowerCase())));
  const records = usePaged((data?.reduction_list || []).filter(a => `${a.image_name || ''} ${a.config_name || ''} ${a.reduction_name || ''}`.toLowerCase().includes(pointQ.toLowerCase())));
  const { busy, run } = useMutation(store.toast);
  useEffect(() => { if (data) setName(data.name); }, [data?.name]);
  const linkStyle = { background: 'none', border: 0, padding: 0, color: 'var(--cyan)', font: 'inherit', cursor: 'pointer', textAlign: 'left' };
  const saveName = async () => {
    if (await run(() => api.renameModel(id, { name: name.trim() }), '机型名称已更新', { reload: () => { reload(); onDone(); } })) setRename(false);
  };
  return <><Modal open={open && !rename} onClose={onClose} size="xl" title="机型详情" footer={<button className="btn" onClick={onClose}>关闭</button>}>
    <div className="row" style={{ flexWrap: 'wrap', marginBottom: 14 }}>
      <h2 className="card-title-zh" style={{ margin: 0 }}>{data?.name || '机型详情'}</h2>
      {data && <ModelStatus status={data.status}/>}
      <div className="row" style={{ marginLeft: 'auto' }}>
        {data && <button className="btn" onClick={() => setRename(true)}>改名</button>}
      </div>
    </div>
    {error ? <div className="hint" role="alert">加载失败：{error} <button className="btn" onClick={reload}>重试</button></div> : !data ? <div className="hint" role="status" aria-label="机型详情加载状态">加载中…</div> : <>
      {data.platform && <p className="meta" style={{ color: 'var(--fg-mute)' }}>启动方式：{data.platform === 'efi' ? 'UEFI' : data.platform === 'pcbios' ? 'BIOS' : data.platform}</p>}
      {data.status === 'adapting' && <p className="hint" style={{ color: 'var(--fg-mute)' }}>已有完整适配任务，请在任务页查看进度。</p>}
      {data.status === 'failed' && <p className="hint" style={{ color: 'var(--fg-mute)' }}>没进系统的机器可以做一次完整适配：在一台该机型的机器上进维护环境重新生成网卡配置，存成新还原点并设为配置当前点。做过仍进不了系统，就给这些机器换一种网卡型号。</p>}
      {(data.points || []).map(p => <div key={p.reduction_id} className="row" style={{ flexWrap: 'wrap', marginBottom: 10 }}>
        <span className="hint" style={{ color: 'var(--fg-mute)' }}>配置「{p.config_name || '名称不可用'}」当前还原点「{p.reduction_name || '名称不可用'}」：{bootCounts(p)}</span>
        {!!p.failed && <button className="btn" aria-label={`完整适配 ${p.config_name || '此配置'}`} disabled={data.status === 'adapting'} onClick={() => onAdapt(p.config_id)}>完整适配</button>}
      </div>)}
      <div className="card" style={{ padding: 0, minWidth: 0 }}>
        <div className="tabs" style={{ padding: '0 14px', marginBottom: 0 }}>
          <button className={`tab ${tab === 'points' ? 'active' : ''}`} style={{ background: 'transparent', borderTop: 0, borderLeft: 0, borderRight: 0 }} aria-pressed={tab === 'points'} onClick={() => setTab('points')}>记录 ({(data.reduction_list || []).length})</button>
          <button className={`tab ${tab === 'clients' ? 'active' : ''}`} style={{ background: 'transparent', borderTop: 0, borderLeft: 0, borderRight: 0 }} aria-pressed={tab === 'clients'} onClick={() => setTab('clients')}>Windows 客户机 ({(data.terminal_list || []).length})</button>
        </div>
        {tab === 'clients' ? <section aria-label="机型客户机" style={{ minWidth: 0 }}>
          <div className="card-h"><SearchBox placeholder="搜索客户机 / MAC / 分组" value={clientQ} onChange={q => { setClientQ(q); clients.setPage(1); }} width="100%"/></div>
          <div style={{ overflowX: 'auto' }}><table className="t"><thead><tr><th>客户机</th><th>MAC</th><th>分组</th><th>开机情况</th><th>状态</th></tr></thead><tbody>
            <TableState colSpan={5} empty={!clients.rows.length} hint={clientQ ? '没有匹配的客户机' : '暂无该机型的 Windows 客户机'}/>
            {clients.rows.map(t => <tr key={t.id}><td><button style={linkStyle} onClick={() => store.goto('terminals', { terminalId: t.id })}>{t.name || t.mac}</button></td><td className="mono">{t.mac}</td><td>{t.group_name || '—'}</td><td>{BOOT_LABELS[t.boot] || BOOT_LABELS.waiting}</td><td>{t.state === 'online' ? '在线' : t.state === 'offline' ? '离线' : '状态未知'}</td></tr>)}
          </tbody></table></div><Pager {...clients.pager}/>
        </section> : <section aria-label="机型记录" style={{ minWidth: 0 }}>
          <div className="card-h"><SearchBox placeholder="搜索镜像 / 配置 / 还原点" value={pointQ} onChange={q => { setPointQ(q); records.setPage(1); }} width="100%"/></div>
          <div style={{ overflowX: 'auto' }}><table className="t" style={{ display: 'table', tableLayout: 'fixed', minWidth: 760 }}>
            <colgroup><col/><col style={{ width: 110 }}/><col style={{ width: 150 }}/><col style={{ width: 150 }}/><col style={{ width: 190 }}/></colgroup>
            <thead><tr><th>还原点</th><th>镜像</th><th>配置</th><th>记录时间</th><th className="t-actions">操作</th></tr></thead><tbody>
            <TableState colSpan={5} empty={!records.rows.length} hint={pointQ ? '没有匹配的还原点' : '暂无记录'}/>
            {records.rows.map(a => <tr key={a.id}>
              <td><div className="ellip" style={{ fontWeight: 500 }} title={a.reduction_name || '还原点不可用'}>{a.reduction_name || '还原点不可用'}</div>{MODEL_RECORD_SOURCES[a.source] && <div className="meta">{MODEL_RECORD_SOURCES[a.source]}</div>}{a.unavailable && <div className="hint">{a.unavailable}</div>}</td>
              <td className="ellip" title={a.image_name || '镜像不可用'}>{a.unavailable ? a.image_name || '镜像不可用' : <button className="ellip" style={{ ...linkStyle, maxWidth: '100%' }} onClick={() => store.goto('images', { imageId: a.image_id })}>{a.image_name || '镜像不可用'}</button>}</td>
              <td className="ellip" title={a.config_name || '配置不可用'}>{a.unavailable ? a.config_name || '配置不可用' : <button className="ellip" style={{ ...linkStyle, maxWidth: '100%' }} onClick={() => store.goto('images', { tab: 'reductions', imageId: a.image_id, configId: a.config_id })}>{a.config_name || '配置不可用'}</button>}</td>
              <td className="meta" style={{ whiteSpace: 'nowrap' }}>{fmtDateTime(a.created_at)}</td>
              <td className="t-actions"><div><button className="btn" disabled={!!a.unavailable} onClick={() => store.goto('images', { tab: 'reductions', imageId: a.image_id, configId: a.config_id })}>查看还原点</button>{a.task_id && <button className="btn ghost" onClick={() => store.goto('tasks', { taskId: a.task_id })}>查看任务</button>}</div></td>
            </tr>)}
          </tbody></table></div><Pager {...records.pager}/>
        </section>}
      </div>
      <div className="row" style={{ flexWrap: 'wrap', marginTop: 14 }}>
        <span className="hint" style={{ color: 'var(--fg-mute)' }}>仍然进不了系统时，可以做一次完整适配，结果存成新还原点并设为配置当前点。</span>
        <button className="btn ghost" disabled={data.status === 'adapting'} onClick={() => onAdapt()}>完整适配</button>
      </div>
    </>}
    </Modal>
    <Modal open={open && rename} onClose={() => !busy && setRename(false)} size="sm" title="修改机型名称" footer={<><button className="btn" disabled={busy} onClick={() => setRename(false)}>取消</button><button className="btn primary" disabled={busy || !name.trim()} onClick={saveName}>保存名称</button></>}>
      <Field label="机型名称"><input className="input" aria-label="机型名称" value={name} onChange={e => setName(e.target.value)}/></Field>
    </Modal>
  </>;
}

const CATEGORY_LABELS = {
  boot_critical_nic: '启动网卡',
  nic: '网卡',
  gpu: '显卡',
  audio: '声卡',
  chipset: '芯片组',
  other: '其它',
};
const RISK_LABELS = { unsigned: '未签名', no_hwids: '无 HWID', outdated: '版本过旧' };


function DriverPacks() {
  const store = useStore();
  const conf = useConfirm();
  const [uploadOpen, setUploadOpen] = useState(false);
  const [q, setQ] = useState('');
  const [catFilter, setCatFilter] = useState('all');

  const { data, loading, error, reload } = useResource(() => api.listDriverPacks(), []);
  const packs = data?.items || [];
  const { busy, run } = useMutation(store.toast);
  const filteredPacks = packs.filter(({ pack }) => (catFilter === 'all' || pack.Category === catFilter) &&
    (!q || pack.Name.toLowerCase().includes(q.toLowerCase()) || (pack.Vendor || '').toLowerCase().includes(q.toLowerCase())));
  const packPager = usePaged(filteredPacks);
  const toggleStatus = (pack) => {
    const next = pack.Status === 'enabled' ? 'disabled' : 'enabled';
    return run(() => api.setDriverPackStatus(pack.ID, next), next === 'enabled' ? '驱动包已启用' : '驱动包已禁用', { reload });
  };


  const removePack = async (pack) => {
    const ok = await conf.ask({ title: '删除驱动包', message: `确定删除 ${pack.Name}？`, danger: true });
    if (ok) await run(() => api.deleteDriverPack(pack.ID), '驱动包已删除', { reload });
  };


  return <>
      <p className="hint">开机或完整适配提示缺少启动网卡驱动时，再上传匹配的驱动包。</p>
      <details className="hint" style={{ marginBottom: 14 }}>
        <summary style={{ cursor: 'pointer' }}>驱动包何时使用</summary>
        <ol>
          <li>提示缺少网卡驱动时，再准备适用于 Windows x64 的已签名驱动包。完整适配优先用镜像自带驱动，维护环境也需有匹配驱动才能联网。</li>
          <li>将 INF、SYS、CAT 一起打成 ZIP，类别选「启动网卡」，上传后启用。</li>
          <li>返回机型页，确认配置、客户机和还原点名称，重新提交完整适配。</li>
        </ol>
      </details>
      <div className="card" style={{ padding: 0 }}>
        <div className="card-h">
          <div className="row" style={{ gap: 10 }}>
            <input className="input" placeholder="搜索名称 / 厂商" style={{ width: 200 }} value={q}
              onChange={e => { setQ(e.target.value); packPager.setPage(1); }}/>
            <Select value={catFilter} onChange={v => { setCatFilter(v); packPager.setPage(1); }} style={{ width: 130 }}
              options={[{ value: 'all', label: '全部类别' }, ...Object.entries(CATEGORY_LABELS).map(([value, label]) => ({ value, label }))]}/>
            {(q || catFilter !== 'all') && <span className="chip">{filteredPacks.length} / {packs.length}</span>}
          </div>
          <button className="btn primary" onClick={() => setUploadOpen(true)}><Icons.Upload size={12}/> 上传驱动包</button>
        </div>
        <table className="t">
          <thead><tr><th>名称</th><th>类别</th><th>厂商</th><th>版本</th><th>日期</th><th>HWID</th><th>签名</th><th>风险</th><th>状态</th><th className="t-actions"></th></tr></thead>
          <tbody>
            <TableState colSpan={10} loading={loading} error={error} empty={!filteredPacks.length}
              hint={packs.length ? '没有匹配的驱动包 · 调整筛选或搜索条件' : '暂无驱动包 · 先上传一个含 INF 的 zip'}/>
            {packPager.rows.map(({ pack, risks }) => (
              <tr key={pack.ID}>
                <td>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ fontWeight: 500 }}>{pack.Name}</span>
                  </div>
                </td>
                <td><span className={`chip ${pack.Category === 'boot_critical_nic' ? 'amber' : ''}`}>{CATEGORY_LABELS[pack.Category] || pack.Category}</span></td>
                <td className="muted">{pack.Vendor || '—'}</td>
                <td className="mono" style={{ fontSize: 12 }}>{pack.Version || '—'}</td>
                <td className="mono muted" style={{ fontSize: 12 }}>{pack.ReleaseDate || '—'}</td>
                <td title={(pack.HWIDs || []).join('\n')} className="mono" style={{ fontSize: 12 }}>{(pack.HWIDs || []).length}</td>
                <td>{pack.Signed ? <span className="chip emerald">已签名</span> : <span className="chip rose">未验证</span>}</td>
                <td>
                  {(risks || []).length === 0 ? <span className="muted">—</span> :
                    (risks || []).map(r => <span key={r} className="chip rose" style={{ marginRight: 4 }}>{RISK_LABELS[r] || r}</span>)}
                </td>
                <td>{pack.Status === 'enabled' ? <span className="chip emerald">启用</span> : <span className="chip">禁用</span>}</td>
                <td className="t-actions"><div>
                  <button className="btn" style={{ padding: '3px 8px' }} onClick={() => toggleStatus(pack)}>{pack.Status === 'enabled' ? '禁用' : '启用'}</button>
                  <button className="btn ghost icon danger" onClick={() => removePack(pack)} title="删除驱动包" aria-label={`删除驱动包 ${pack.Name}`}><Icons.Trash size={12}/></button>
                </div></td>
              </tr>
            ))}
          </tbody>
        </table>
        <Pager {...packPager.pager}/>
      </div>
      <DriverPackUpload open={uploadOpen} onClose={() => setUploadOpen(false)} onDone={reload}/>
  </>;
}

function DriverPackUpload({ open, onClose, onDone }) {
  const store = useStore();
  const [uploadForm, setUploadForm] = useState({ name: '', category: 'boot_critical_nic', os_type: 'windows', arch: 'x64', file: null });
  const { busy, run } = useMutation(store.toast);
  const doUpload = async () => {
    if (!uploadForm.file) { store.toast('请选择驱动 zip 文件', 'err'); return; }
    const fd = new FormData();
    fd.append('file', uploadForm.file);
    fd.append('name', uploadForm.name);
    fd.append('category', uploadForm.category);
    fd.append('os_type', uploadForm.os_type);
    fd.append('arch', uploadForm.arch);
    if (await run(() => api.uploadDriverPack(fd), '驱动包已上传并解析', { reload: onDone })) {
      onClose();
      setUploadForm({ name: '', category: 'boot_critical_nic', os_type: 'windows', arch: 'x64', file: null });
    }
  };
  return <Modal open={open} onClose={() => !busy && onClose()} title="上传驱动包"
        footer={<>
          <button className="btn" disabled={busy} onClick={onClose}>取消</button>
          <button className="btn primary" disabled={busy} onClick={doUpload}>{busy ? '解析中…' : '上传并解析'}</button>
        </>}>
        <Field label="驱动 zip 文件" required hint="zip 内须包含至少一个 .inf；服务端自动解析厂商 / 版本 / HWID / 签名状态">
          <input className="input" type="file" accept=".zip" style={{ width: '100%' }}
            onChange={e => setUploadForm({ ...uploadForm, file: e.target.files[0] || null })}/>
        </Field>
        <Field label="名称" hint="留空则取文件名">
          <input className="input" style={{ width: '100%' }} value={uploadForm.name}
            onChange={e => setUploadForm({ ...uploadForm, name: e.target.value })} placeholder="Intel I219 v12.19"/>
        </Field>
        <Field label="类别" required>
          <Select value={uploadForm.category} onChange={v => setUploadForm({ ...uploadForm, category: v })}
            options={Object.entries(CATEGORY_LABELS).map(([value, label]) => ({ value, label }))}/>
        </Field>
        <Field label="客户机系统">
          <Select value={uploadForm.os_type} onChange={v => setUploadForm({ ...uploadForm, os_type: v })}
            options={[{ value: 'windows', label: 'Windows' }, { value: 'linux', label: 'Linux' }]}/>
        </Field>
      </Modal>;
}

export { PageModels, ModelAdapt };
