import React from 'react';
import { api } from '../lib/api.js';
import { onMutation, usePolling, useResource } from '../lib/hooks.js';
import { Modal } from './overlay.jsx';
import { useStore } from './store.jsx';
import { Field } from './primitives.jsx';
import { TaskLogs } from './task-logs.jsx';
import { fmtMac } from '../lib/format.js';

const PHASES = {preparing:'准备资源', waiting:'等待客户机开机', installing:'正在安装', initializing:'首次系统启动', installed:'等待关机保存', saving:'保存镜像', verifying:'等待启动验证', cancelling:'清理安装资源', failed:'安装失败', cancelled:'已取消', ready:'镜像已就绪'};
// 「等待关机保存」并入「保存」一格，阶段条只给运维看大步骤。
const STEPS = [['准备', ['preparing']], ['等待开机', ['waiting']], ['安装', ['installing']], ['首次启动', ['initializing']], ['保存', ['installed', 'saving']], ['启动验证', ['verifying']]];
const RUNNING = STEPS.flatMap(([, phases]) => phases);

export const installationPhaseLabel = (phase) => PHASES[phase] || phase;
export const installationRunning = (phase) => RUNNING.includes(phase);
export const installationShowsProgress = (s) => ['installing', 'initializing', 'saving'].includes(s.phase) && s.progress > 0;

export function InstallationPhase({session}) {
  return <span className={`chip ${session.phase === 'failed' ? 'rose' : session.phase === 'ready' ? 'emerald' : ['cancelled','cancelling'].includes(session.phase) ? '' : 'cyan'}`}>{installationPhaseLabel(session.phase)}</span>;
}

function Steps({phase}) {
  const at = STEPS.findIndex(([, phases]) => phases.includes(phase));
  if (at < 0) return null;
  return <ol aria-label="安装阶段" className="row" style={{listStyle:'none', margin:0, padding:0, gap:6, flexWrap:'wrap'}}>
    {STEPS.map(([label], i) => <li key={label} aria-current={i === at ? 'step' : undefined}
      className={`chip ${i === at ? 'cyan' : i < at ? 'emerald' : ''}`}>{label}</li>)}
  </ol>;
}

const fileName = (path) => (path || '').split('/').pop() || '未记录';
export const installationOSLabel = (s) => {
  const distributions = {'anaconda-centos':'CentOS', 'anaconda-rhel':'RHEL', 'anaconda-rocky':'Rocky Linux', 'anaconda-alma':'AlmaLinux'};
  const os = s.os_type !== 'linux' ? 'Windows' : distributions[s.distribution] || (!s.distribution || s.distribution.startsWith('ubuntu') ? 'Ubuntu' : 'Linux');
  const v = String(s.version || '').trim();
  return v.toLowerCase().startsWith(os.toLowerCase()) ? v : `${os}${v ? ` ${v}` : ''}`;
};

// 一次 ISO 安装的唯一详情面：进度、日志、重试和放弃都在这里完成，不弹第二层。
// intent 为 retry / abandon 时打开即展开对应的确认区（镜像卡片上的按钮走这里）。
export function InstallationDetails({id, intent, onClose, onChanged, onEditSettings}) {
  const store = useStore();
  const {data:s, loading, error, reload, setData} = useResource(() => api.getInstallation(id), [id]);
  const [mode, setMode] = React.useState(intent || null);
  const [password, setPassword] = React.useState('');
  const [logsOpen, setLogsOpen] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [actionError, setActionError] = React.useState('');
  usePolling(reload, {intervalMs:3000, active:!!s && !busy && !['failed','ready','cancelled'].includes(s.phase)});

  const changed = (next) => { setData(next); onMutation.emit(); onChanged?.(); };
  const back = () => { setMode(null); setActionError(''); };
  const editSettings = () => {
    onClose();
    if (onEditSettings) onEditSettings(s);
    else store.goto('images', {recreateInstallationId:s.id});
  };
  const abandon = async () => {
    setBusy(true); setActionError('');
    try {
      const next = await api.cancelInstallation(id);
      changed(next);
      if (next.phase === 'cancelled') { store.toast(`已放弃「${next.name}」的安装`, 'ok'); onClose(); }
      else setMode(null);
    } catch (e) { setActionError(e.message || '清理失败，请重试'); }
    finally { setBusy(false); }
  };
  // 先确认原 ISO 还在再清理：文件没了就留着这次安装，让运维换文件而不是两头落空。
  const restart = async () => {
    setBusy(true); setActionError('');
    try {
      const src = await api.listInstallationSources();
      if (!(src?.items || []).some(f => f.path === s.source_path)) {
        throw new Error(`原安装文件 ${s.source_path || ''} 已不在导入目录${src?.import_dir ? ` ${src.import_dir}` : ''}里：请把它放回去（旧导入目录里的文件可在「系统参数 → 镜像导入目录」移入），或点「修改设置」换一个安装文件。`);
      }
      // 重新识别只读目录和几个小文件；原会话的校验值一并带上，复制 ISO 时核对文件没被换掉。
      const meta = await api.inspectInstallationISO(s.source_path);
      if (s.phase !== 'cancelled') {
        const next = await api.cancelInstallation(id);
        changed(next);
        if (next.phase !== 'cancelled') throw new Error(`请先关闭客户机 ${fmtMac(s.mac)}，等原来的安装清理完再点「开始安装」。`);
      }
      await api.createInstallation({source_path:s.source_path, source_fingerprint:meta.fingerprint, source_sha256:s.source_sha256, edition_index:s.edition_index,
        name:s.name, size_gib:s.size_gib, mac:s.mac, ip:s.ip, netmask:s.netmask, gateway:s.gateway, dns1:s.dns1,
        username:s.username, password});
      onMutation.emit(); onChanged?.();
      // 进度在镜像卡片上看，不再换成新安装的详情。
      store.toast(`已重新开始安装：等卡片显示「等待客户机开机」后，启动客户机 ${fmtMac(s.mac)}`, 'ok');
      onClose();
    } catch (e) { setActionError(e.message || '无法重新开始安装'); }
    finally { setBusy(false); }
  };
  const retrySave = async () => {
    setBusy(true); setActionError('');
    try { changed(await api.retryInstallation(id)); }
    catch (e) { setActionError(e.message || '保存失败，请重试'); }
    finally { setBusy(false); }
  };

  const locked = busy || loading || !!error;
  const running = !!s && installationRunning(s.phase);
  const canRetry = !!s && ['failed','cancelled'].includes(s.phase);
  const canAbandon = !!s && (running || s.phase === 'failed');
  const footer = !s ? null
    : mode === 'abandon' && canAbandon ? <>
      <button className="btn" disabled={busy} onClick={back}>返回</button>
      <button className="btn danger" disabled={busy} onClick={abandon}>{busy ? '清理中…' : '确认放弃'}</button>
    </> : mode === 'retry' && canRetry ? <>
      <button className="btn" disabled={busy} onClick={back}>返回</button>
      <button className="btn primary" disabled={busy || password.length < 8} onClick={restart}>{busy ? '处理中…' : '开始安装'}</button>
    </> : <>
      {canAbandon && <button className="btn ghost" disabled={locked} onClick={() => setMode('abandon')}>放弃安装</button>}
      {s.phase === 'saving' && s.error && <button className="btn primary" disabled={locked} onClick={retrySave}>{busy ? '处理中…' : '重试保存'}</button>}
      {canRetry && <button className="btn primary" disabled={locked} onClick={() => setMode('retry')}>重试</button>}
    </>;

  return <Modal open title="ISO 安装" size="lg" onClose={() => !busy && onClose()} footer={footer}>
    {loading && !s && <p className="hint">正在读取安装状态…</p>}
    {error && <p role="alert" className="hint">{error} <button className="btn" onClick={reload}>重试</button></p>}
    {s && <div className="col" style={{gap:14}}>
      <div className="row" style={{justifyContent:'space-between', flexWrap:'wrap'}}><strong className="ellip" title={s.name}>{s.name}</strong><InstallationPhase session={s}/></div>
      <div className="meta mono">{installationOSLabel(s)} · 客户机 {fmtMac(s.mac)} · {s.ip}</div>
      <Steps phase={s.phase}/>
      {s.phase === 'failed'
        ? <div style={{color:'var(--rose)', overflowWrap:'anywhere'}}>{s.error || '安装未完成，请展开下面的安装日志查看原因。'}</div>
        : <p className="hint" style={{margin:0}}>{s.phase === 'cancelling' ? `正在清理安装资源；客户机 ${fmtMac(s.mac)} 仍开机的话请先关闭。` : s.phase === 'cancelled' ? '安装资源已清理。' : s.message}</p>}
      {s.phase === 'saving' && s.error && <p style={{color:'var(--rose)', overflowWrap:'anywhere', margin:0}}>{s.error}</p>}
      {installationShowsProgress(s) && <div className="bar cyan" role="progressbar" aria-label="安装进度" aria-valuenow={s.progress} aria-valuemin={0} aria-valuemax={100}><span style={{width:`${s.progress}%`}}/></div>}
      {mode === 'abandon' && canAbandon && <p style={{margin:0, padding:12, borderRadius:6, background:'var(--rose-soft)', overflowWrap:'anywhere'}}>
        放弃后会清理「{s.name}」的安装盘和未发布的镜像，原 ISO 文件保留。{running && `请先关闭客户机 ${fmtMac(s.mac)}。`}
      </p>}
      {mode === 'retry' && canRetry && <div style={{padding:12, borderRadius:6, background:'var(--bg-0)'}}>
        <p className="hint" style={{marginTop:0}}>用原来的设置重新安装：先清理这次的安装盘和未发布的镜像，原 ISO 文件保留。</p>
        <p className="meta mono" style={{overflowWrap:'anywhere'}}>安装文件 {fileName(s.source_path)} · 系统盘 {s.size_gib || '—'} GiB · 初始账户 {s.username || '—'}</p>
        <Field label="初始密码" required hint={password && password.length < 8
          ? <span style={{color:'var(--rose)'}}>至少 8 位，现在 {password.length} 位</span>
          : '8 至 127 位；密码不保存，重新安装要再填一次。'}>
          <input className="input" type="password" autoComplete="new-password" aria-label="初始密码" style={{width:'100%'}}
            value={password} onChange={e => setPassword(e.target.value)}/>
        </Field>
        <button className="btn ghost" disabled={busy} onClick={editSettings}>修改设置</button>
      </div>}
      {actionError && <div role="alert" style={{color:'var(--rose)', overflowWrap:'anywhere'}}>{actionError}</div>}
      {s.task_id && <details onToggle={e => setLogsOpen(e.currentTarget.open)}>
        <summary className="hint" style={{cursor:'pointer'}}>安装日志</summary>
        {logsOpen && <TaskLogs taskId={s.task_id} installation os={s.os_type} distribution={s.distribution} tail={40}/>}
      </details>}
    </div>}
  </Modal>;
}
