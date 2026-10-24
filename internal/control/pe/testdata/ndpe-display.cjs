const assert = require('node:assert/strict');
const vm = require('node:vm');
const source = require('node:fs').readFileSync(0).toString('utf16le').replace(/^\uFEFF/, '');
const scenario = process.argv[2];
const messages = [], files = new Map();
let slept = 0;
const args = ['show', 'start', 'adapt', '', '', 'result.json'];
let expectedExit = 0;
if (scenario.startsWith('restart-')) {
  args.splice(0, args.length, 'restart', 'identity', '0', '0');
  if (scenario === 'restart-identity-server-failed') args[3] = '1';
  if (scenario === 'restart-identity-local-failed') args[2] = '1';
  if (scenario === 'restart-adapt') args[1] = 'adapt';
  if (scenario === 'restart-probe') args[1] = 'probe';
  expectedExit = scenario === 'restart-identity-success' ? 0 : 1;
} else if (scenario === 'stage') args[1] = 'registry';
else if (scenario !== 'start') {
  args[1] = 'finish'; args[3] = '0'; args[4] = '0';
  if (scenario === 'failed') {
    args[3] = '1';
    files.set('result.json', '{"ok":false,"stage":"registry","message":"\\u542f\\u52a8\\u7f51\\u5361\\u4f4d\\u7f6e\\u5339\\u914d\\u6570\\u91cf=0"}');
  }
  if (scenario === 'unconfirmed') args[4] = '1';
  if (scenario === 'missing-log') args[4] = 'logs';
  if (scenario === 'identity' || scenario === 'probe') args[2] = scenario;
}
const wshArgs = Object.defineProperty(i => args[i], 'length', {value: args.length});
const quit = {};
try {
  vm.runInNewContext(source, {
    JSON: undefined,
    ActiveXObject: function(name) {
      if (name === 'Scripting.FileSystemObject') return {
        FileExists: path => files.has(path),
        OpenTextFile: (path, mode) => ({
          AtEndOfStream: !files.get(path),
          ReadAll: () => files.get(path) || '',
          WriteLine: line => files.set(path, (files.get(path) || '') + line + '\n'),
          Close() {},
        }),
      };
      assert.equal(name, 'WinHttp.WinHttpRequest.5.1');
      return {SetTimeouts() {}, Open() {}, SetRequestHeader() {}, Send() {}, Status: 204};
    },
    WScript: {Arguments: wshArgs,
      Echo: s => messages.push(s), Sleep: ms => { slept += ms; },
      Quit: code => { assert.equal(code, expectedExit); throw quit; }},
  });
} catch (e) { if (e !== quit) throw e; }
const text = messages.join('\n');
if (scenario.startsWith('restart-')) {
  assert.equal(slept, 0);
  assert.equal(messages.length, 0);
} else if (scenario === 'start') {
  assert.match(text, /NexBoot/);
  assert.match(text, /机型适配/);
  assert.match(text, /自动关机/);
  assert.match(text, /任务中心/);
  assert.equal(slept, 0);
} else if (scenario === 'stage') {
  assert.match(text, /核对.*设备实例/);
  assert.equal(slept, 0);
} else {
  assert.match(text, /任务中心/);
  assert.match(text, scenario === 'identity' ? /自动重启/ : /自动关机/);
  assert.equal(slept, 20000, 'operator can read the result before shutdown, without waiting forever');
  if (scenario === 'failed') {
    assert.match(text, /适配失败/);
    assert.match(text, /启动网卡位置匹配数量=0/);
    assert.match(text, /未切换.*还原点/);
    assert.doesNotMatch(text, /自检通过/);
  } else if (scenario === 'unconfirmed') {
    assert.match(text, /不能确认完成/);
    assert.doesNotMatch(text, /已提交|已保存|适配成功|未保存/);
  } else if (scenario === 'identity' || scenario === 'probe') {
    assert.match(text, scenario === 'identity' ? /身份.*已提交/ : /检查.*已提交/);
    assert.doesNotMatch(text, /保存还原点/);
  } else {
    assert.match(text, /自检通过/);
    assert.match(text, /已提交/);
    assert.match(text, /关机后.*保存还原点/);
    assert.doesNotMatch(text, /适配成功|已保存/);
    if (scenario === 'missing-log') assert.match(text, /日志.*上传未完成/);
  }
}
