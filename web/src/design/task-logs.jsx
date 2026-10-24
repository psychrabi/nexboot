import React from 'react';
import { Icons } from './icons.jsx';
import { api, downloadRaw } from '../lib/api.js';
import { useStore } from './store.jsx';
import { useResource } from '../lib/hooks.js';
import { SegFilter } from './primitives.jsx';
const { useState } = React;

const PE_LOGS = [{ id: 'console.log', label: 'PE 控制台' }, { id: 'dism.log', label: 'DISM 日志' }];
const UBUNTU_LOGS = [{ id: 'install.log', label: 'Ubuntu 启动准备' }, { id: 'subiquity.log', label: 'Ubuntu 安装日志' }];

const ANACONDA_LOGS = [{ id: 'install.log', label: '安装准备与收尾' }, { id: 'anaconda.log', label: 'Anaconda 安装日志' }, { id: 'storage.log', label: '磁盘识别日志' }, { id: 'program.log', label: '命令日志' }, { id: 'iscsi.log', label: 'iSCSI 诊断' }];

// TaskLogs 读取任务日志并按编码解码；tail 只显示最后若干行，下载仍是完整原始字节。
// 安装详情知道系统时只列对应的日志；任务中心不知道系统，全部列出。
export function TaskLogs({ taskId, installation, os, distribution, tail }) {
  const store = useStore();
  const options = !installation ? PE_LOGS : os === 'linux' ? (distribution?.startsWith('anaconda-') ? ANACONDA_LOGS : UBUNTU_LOGS) : os === 'windows' ? PE_LOGS : [...PE_LOGS, ...UBUNTU_LOGS, ...ANACONDA_LOGS.slice(1)];
  const [name, setName] = useState(options[0].id);
  const { data, loading, error, reload } = useResource(async () => {
    const bytes = new Uint8Array(await (await api.taskLog(taskId, name)).arrayBuffer());
    // DISM 等原生日志可能带 UTF-16 BOM；须先按声明的编码整份解码，避免拆坏双字节换行。
    if ((bytes[0] === 0xff && bytes[1] === 0xfe) || (bytes[0] === 0xfe && bytes[1] === 0xff)) {
      return new TextDecoder(bytes[0] === 0xff ? 'utf-16le' : 'utf-16be').decode(bytes);
    }
    // Windows 原生命令输出 GBK，脚本输出 UTF-8；逐行解码，下载仍保留原始字节。
    const utf8 = new TextDecoder('utf-8', { fatal: true });
    const gbk = new TextDecoder('gb18030');
    const lines = []; let start = 0;
    for (let i = 0; i <= bytes.length; i++) {
      if (i !== bytes.length && bytes[i] !== 10) continue;
      const line = bytes.subarray(start, i);
      try { lines.push(utf8.decode(line)); } catch { lines.push(gbk.decode(line)); }
      start = i + 1;
    }
    return lines.join('\n');
  }, [taskId, name]);
  const download = async () => {
    try { await downloadRaw(await api.taskLog(taskId, name), `${taskId}-${name}`); }
    catch (e) { store.toast(e.message, 'err'); }
  };
  return <div>
    <div className="row" style={{ marginTop: 12, flexWrap: 'wrap' }}>
      <div style={{ maxWidth: '100%', overflowX: 'auto' }}><SegFilter value={name} onChange={setName} options={options}/></div>
      <button className="btn" disabled={loading || !!error} onClick={download}>下载原始日志</button>
      <button className="btn ghost icon" onClick={reload} aria-label="刷新日志" title="刷新日志"><Icons.Refresh size={13}/></button>
    </div>
    {error ? <div role="alert" className="hint">{error}</div> : <pre className="hint mono" style={{ maxHeight: tail ? '30vh' : '50vh', overflow: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{loading ? '加载中…' : (tail && data ? data.trimEnd().split('\n').slice(-tail).join('\n') : data) || '日志为空'}</pre>}
  </div>;
}
