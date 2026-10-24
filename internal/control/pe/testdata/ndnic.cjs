// Platform doubles for the real WSH entry point, not its private functions.
const assert = require('node:assert/strict');
const vm = require('node:vm');
const source = require('node:fs').readFileSync(0).toString('utf16le').replace(/^\uFEFF/, '');
const scenario = process.argv[2];
const host = 'HKLM\\SYSTEM\\CurrentControlSet', off = 'HKLM\\NDOFF\\ControlSet001';
const netclass = '{4d36e972-e325-11ce-bfc1-08002be10318}';
const instance = 'PCI\\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\\4&bbf9765&0&2088';
const nativeLocation = scenario.startsWith('native-location-property');
const hostGUID = nativeLocation ? '{02A5CEEC-4792-43E6-98AD-40239E11A21C}' : '{9F46AC43-9E7A-458E-91D2-03F9A3331BB5}';
const guid = nativeLocation ? '{B64B653B-CBFD-4AF0-B6A9-8D42D27A3393}' : '{8BE8A630-406A-4D2E-8EB2-BC9D5617C6C2}';
const hostClass = host + '\\Control\\Class\\' + netclass + '\\0001';
const location = nativeLocation ? 'PCIROOT(0)#PCI(1100)#PCI(0400)' : 'PCIROOT(0)#PCI(1100)#PCI(0200)';
const oldLocation = 'PCIROOT(0)#PCI(1100)#PCI(0100)';
const deviceHardwareID = 'PCI#VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01';
const service = off + '\\Services\\E1G60';
const registry = new Map(), files = new Map(), messages = [];
const put = (key, name, type, value) => {
  if (!registry.has(key)) registry.set(key, new Map());
  registry.get(key).set(name, {type, value});
};
put(hostClass, 'MatchingDeviceId', 'REG_SZ', 'pci\\ven_8086&dev_100f');
put(hostClass, 'NetCfgInstanceId', 'REG_SZ', hostGUID);
put(hostClass, 'DeviceInstanceID', 'REG_SZ', instance);
put(hostClass, 'InfPath', 'REG_SZ', 'nete1g3e.inf');
put(host + '\\Enum\\' + instance, 'Service', 'REG_SZ', 'E1G60');
// WinPE 本地化后的位置描述；iPXE 的 busloc 是十六进制（总线 3 设备 0 功能 0 = 0x300）。
// Windows 8 起 LocationInformation 存的是资源引用格式，末尾括号里才是总线、设备、功能。
put(host + '\\Enum\\' + instance, 'LocationInformation', 'REG_SZ', scenario === 'plain-location' ? 'PCI 总线 3、设备 0、功能 0' : '@System32\\drivers\\pci.sys,#65536;PCI 总线 %1、设备 %2、功能 %3;(3,0,0)');
// 同型号第二块网卡在总线 4：只有靠位置才能区分。
if (scenario === 'two-nics-hex-busloc' || scenario === 'two-nics-no-match') {
  const second = host + '\\Control\\Class\\' + netclass + '\\0002', instance2 = 'PCI\\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\\4&bbf9765&0&3088';
  put(second, 'MatchingDeviceId', 'REG_SZ', 'pci\\ven_8086&dev_100f');
  put(second, 'DeviceInstanceID', 'REG_SZ', instance2);
  put(host + '\\Enum\\' + instance2, 'LocationInformation', 'REG_SZ', '@System32\\drivers\\pci.sys,#65536;PCI 总线 %1、设备 %2、功能 %3;(4,0,0)');
}
// Native r11 baseline SYSTEM (SHA256 ba0050d28668e6c8e995b518c6d659f6a23f7dc1ed91d949c7b2186e9e18f656)
// stores DEVPKEY_Device_LocationPaths in the typed property, not an Enum value.
const locationProperty = host + '\\Enum\\' + instance + '\\Properties\\{a45c254e-df1c-4efd-8020-67d146a850e0}\\0025';
const alias = 'ACPI(_SB_)#ACPI(PCI0)#ACPI(P2P0)#ACPI(S5F0)';
put(locationProperty, '', '0xffff2012', location + '\0' + alias + '\0\0');
if (scenario === 'native-location-property-wrong-type') put(locationProperty, '', '0xffff0012', location + '\0\0');
if (scenario === 'native-location-property-missing') registry.delete(locationProperty);
put(host + '\\Services\\E1G60', 'ImagePath', 'REG_EXPAND_SZ', 'System32\\drivers\\E1G6032E.sys');
put(host + '\\Services\\E1G60', 'Type', 'REG_DWORD', '0x1');
put(host + '\\Services\\E1G60', 'Group', 'REG_SZ', 'NDIS');
put('HKLM\\NDOFF\\Select', 'Current', 'REG_DWORD', '0x1');
put(off + '\\Control\\DeviceLocations\\' + location + '\\' + deviceHardwareID + '\\Configuration\\Driver', 'NetCfgInstanceId', 'REG_SZ', guid);
if (scenario === 'native-location-property-alias') put(off + '\\Control\\DeviceLocations\\' + alias + '\\' + deviceHardwareID + '\\Configuration\\Driver', 'NetCfgInstanceId', 'REG_SZ', guid);
put(off + '\\Control\\NetworkSetup2\\Interfaces\\' + guid + '\\Kernel', 'NetLuidIndex', 'REG_DWORD', '0x2');
put(off + '\\Control\\Class\\' + netclass + '\\0000', 'InfPath', 'REG_SZ', 'vmxnet3.inf');
for (const name of ['Tcpip', 'Tcpip6']) put(off + '\\Services\\' + name + '\\Linkage', 'Bind', 'REG_MULTI_SZ', guid);
if (scenario === 'multiple-slots' || scenario === 'ambiguous-location' || scenario === 'native-location-property-other-slot' || scenario === 'native-location-property-ambiguous' || scenario === 'native-location-property-unmatched') {
  const oldGUID = '{11111111-2222-3333-4444-555555555555}';
  put(off + '\\Control\\DeviceLocations\\' + oldLocation + '\\' + deviceHardwareID + '\\Configuration\\Driver', 'NetCfgInstanceId', 'REG_SZ', oldGUID);
  put(off + '\\Control\\NetworkSetup2\\Interfaces\\' + oldGUID + '\\Kernel', 'NetLuidIndex', 'REG_DWORD', '0x9');
  for (const name of ['Tcpip', 'Tcpip6']) put(off + '\\Services\\' + name + '\\Linkage', 'Bind', 'REG_MULTI_SZ', guid + '\\0' + oldGUID);
  if (scenario === 'ambiguous-location' || scenario === 'native-location-property-ambiguous') put(locationProperty, '', '0xffff2012', location + '\0' + oldLocation + '\0\0');
  if (scenario === 'native-location-property-unmatched') registry.delete(off + '\\Control\\DeviceLocations\\' + location + '\\' + deviceHardwareID + '\\Configuration\\Driver');
}
if (scenario === 'existing-service') {
  put(service, 'ImagePath', 'REG_EXPAND_SZ', 'System32\\drivers\\target-e1000.sys');
  put(service, 'Type', 'REG_DWORD', '0x1');
  put(service, 'Group', 'REG_SZ', 'TargetNDIS');
}

const full = key => key.replace(/^HKLM/, 'HKEY_LOCAL_MACHINE');
const shell = {Exec(command) {
  const a = command.match(/"[^"]*"|\S+/g).map(s => s.replace(/^"|"$/g, ''));
  assert.equal(a.shift(), 'reg.exe');
  const action = a.shift(), key = a[0].replace(/^HKEY_LOCAL_MACHINE/, 'HKLM');
  let stdout = '', exit = 0;
  const arg = flag => a[a.indexOf(flag) + 1];
  if (action === 'query') {
    const entries = a.includes('/s') ? [...registry].filter(([k]) => k.startsWith(key)) : [[key, registry.get(key)]];
    if (!a.includes('/v')) {
      stdout = [...registry.keys()].filter(k => k === key || k.startsWith(key + '\\')).map(full).join('\n');
      if (!stdout) exit = 1;
    } else {
      for (const [k, values] of entries) {
        const v = values?.get(arg('/v'));
        if (v) stdout += full(k) + '\n    ' + arg('/v') + '    ' + v.type + '    ' + v.value + '\n';
      }
      if (!stdout) exit = 1;
    }
  } else if (action === 'add') {
    put(key, arg('/v'), arg('/t'), arg('/d'));
  } else if (action === 'copy') {
    for (const [k, values] of [...registry]) if (k === key || k.startsWith(key + '\\')) {
      const dest = a[1] + k.slice(key.length);
      for (const [name, v] of values) put(dest, name, v.type, v.value);
    }
  } else if (action === 'export') {
    let text = 'Windows Registry Editor Version 5.00\r\n\r\n[' + full(key) + ']\r\n';
    for (const [name, value] of registry.get(key) || []) {
      if (!value.type.startsWith('0x')) continue;
      // reg.exe exports the unknown DEVPROP_TYPE_STRING_LIST as typed hex,
      // including Windows .reg continuation lines (also verified by P0 export).
      const hex = [...Buffer.from(value.value, 'utf16le')].map(n => n.toString(16).padStart(2, '0'));
      text += (name ? JSON.stringify(name) : '@') + '=hex(' + value.type.slice(2) + '):';
      text += hex.map((v, i) => v + (i < hex.length - 1 ? ',' + ((i + 1) % 25 === 0 ? '\\\r\n  ' : '') : '')).join('') + '\r\n';
    }
    if (registry.has(key)) files.set(a[1], text); else exit = 1;
  } else assert.equal(action, 'import');
  return {Status: 1, ExitCode: exit, StdOut: {ReadAll: () => stdout}, StdErr: {ReadAll: () => exit ? 'registry value not found' : ''}};
}};
const infPath = 'C:\\Windows\\INF\\nete1g3e.inf';
files.set(infPath, '[Intel.NTamd64]\nE100F=E100F,PCI\\VEN_8086&DEV_100F\n');
const fs = {
  FolderExists: () => false,
  GetFolder: () => ({Files: [{Name: 'nete1g3e.inf', Path: infPath}]}),
  GetSpecialFolder: index => index === 0 ? 'X:\\Windows' : 'X:\\Temp',
  OpenTextFile: path => ({ReadAll: () => { assert.ok(files.has(path), path); return files.get(path); }, Close() {}}),
  CreateTextFile: path => ({Write: text => files.set(path, text), Close() {}}),
  FileExists: path => [...files.keys()].some(key => key.toLowerCase() === path.toLowerCase()),
  CopyFile: (src, dst, overwrite) => {
    assert.equal(overwrite, false);
    assert.ok(!fs.FileExists(dst), 'existing driver must not be overwritten');
    const key = [...files.keys()].find(key => key.toLowerCase() === src.toLowerCase());
    assert.ok(key, 'missing source driver: ' + src);
    files.set(dst, files.get(key));
  },
};
const driverPath = 'C:\\Windows\\System32\\drivers\\E1G6032E.sys';
files.set('X:\\Windows\\System32\\drivers\\E1G6032E.sys', 'loaded-e1000-driver');
if (scenario !== 'missing-binary') files.set(driverPath, 'existing-target-driver');
files.set('C:\\Windows\\System32\\drivers\\target-e1000.sys', 'existing-custom-driver');
function Enumerator(items) { let i = 0; return {atEnd: () => i === items.length, moveNext: () => i++, item: () => items[i]}; }
const failure = {ok: false, stage: 'registry', message: '网卡服务缺少 ImagePath，请检查任务日志'};
if (scenario === 'preserve-failure') files.set('result.json', JSON.stringify(failure));
const failures = {'environment-load': '17', 'environment-unload': '18', 'environment-missing': '19', 'environment-version': '20', 'adapter-dism': '14'};
const args = failures[scenario] ? ['failure', scenario === 'adapter-dism' ? 'adapter' : 'environment', failures[scenario], 'result.json'] : scenario === 'preserve-failure'
  ? ['failure', 'registry', '1', 'result.json']
  : ['adapt', 'PCI\\VEN_8086&DEV_100F', {'hex-busloc': '0x300', 'decimal-busloc': '768', 'busloc-mismatch': '0x400', 'plain-location': '0x300', 'two-nics-hex-busloc': '0x300', 'two-nics-no-match': '0x500'}[scenario] || '', 'result.json', 'C:'];
const quit = {};
let exit = 0;
try {
  vm.runInNewContext(source, {
    ActiveXObject: function(name) { return name === 'WScript.Shell' ? shell : fs; },
    Enumerator,
    WScript: {Arguments: i => args[i], Sleep() {}, Echo: s => messages.push(s), Quit: code => { exit = code; throw quit; }},
  });
} catch (e) { if (e !== quit) throw e; }
const result = JSON.parse(files.get('result.json'));
if (failures[scenario]) {
  assert.equal(exit, 0);
  assert.equal(result.ok, false);
  assert.equal(result.stage, scenario === 'adapter-dism' ? 'adapter' : 'environment');
  assert.doesNotMatch(result.message, /上传|镜像详情|恢复环境版本过旧/);
  const expected = {'environment-load': /加载.*SOFTWARE.*注册表失败/, 'environment-unload': /卸载.*版本信息失败/, 'environment-missing': /CurrentBuildNumber/, 'environment-version': /兼容上限/, 'adapter-dism': /DISM.*日志/};
  assert.match(result.message, expected[scenario]);
  if (scenario !== 'environment-version') assert.doesNotMatch(result.message, /版本不兼容|兼容上限/);
  process.exit(0);
}
if (scenario === 'native-location-property-wrong-type' || scenario === 'native-location-property-missing') {
  assert.equal(exit, 1, 'unavailable/incorrect typed location property must fail self-check');
  assert.equal(result.ok, false);
  assert.match(result.message, /位置属性.*(重新适配|重试)/);
  process.exit(0);
}
if (scenario === 'two-nics-no-match') {
  assert.equal(exit, 1, 'two identical NICs with no location match must not guess');
  assert.match(result.message, /启动网卡匹配数量=0/);
  assert.match(result.message, /3,0,0/);
  assert.match(result.message, /4,0,0/);
  process.exit(0);
}
if (scenario === 'ambiguous-location' || scenario === 'native-location-property-ambiguous' || scenario === 'native-location-property-unmatched') {
  assert.equal(exit, 1, 'ambiguous location must fail self-check');
  assert.equal(result.ok, false);
  assert.match(result.message, scenario === 'native-location-property-unmatched' ? /位置.*匹配.*0/ : /位置.*匹配.*2/);
  process.exit(0);
}
assert.equal(exit, 0, messages.join('\n'));
if (scenario === 'preserve-failure') {
  assert.deepEqual(result, failure);
  process.exit(0);
}
assert.equal(result.ok, true, result.message);
assert.equal(result.data.instance_id, instance);
assert.equal(result.data.netcfg_instance_id, guid);
assert.deepEqual(registry.get(service).get('ImagePath'), {type: 'REG_EXPAND_SZ', value: scenario === 'existing-service' ? 'System32\\drivers\\target-e1000.sys' : 'System32\\drivers\\E1G6032E.sys'});
assert.equal(registry.get(service).get('Group').value, scenario === 'existing-service' ? 'TargetNDIS' : 'NDIS');
assert.equal(registry.get(service).get('Type').value, '0x1');
assert.equal(registry.get(service).get('Start').value, '0');
assert.equal(registry.get(service).get('BootFlags').value, '1');
// iSCSI 启动写不出蓝屏转储；关掉它，开机就不必为转储准备一套 iSCSI 驱动栈。
assert.equal(registry.get(off + '\\Control\\CrashControl')?.get('CrashDumpEnabled')?.value, '0');
assert.equal(files.get(driverPath), scenario === 'missing-binary' ? 'loaded-e1000-driver' : 'existing-target-driver');
assert.equal(files.get('C:\\Windows\\System32\\drivers\\target-e1000.sys'), 'existing-custom-driver');
