const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('scripts/nd-install.js', 'utf8');
function run(disks, refuseReport = false, needsLogin = false) {
  const commands = [], reports = [], files = new Map();
  const cfg = {base:'http://server/install/secret-token/',attempt:1,serial:'disk-uuid',size:68719476736,index:1,ip:'192.168.50.10',portal:'192.168.50.1',target:'iqn.test:install',initiator:'iqn.test:client',username:'chap-user',password:'chap-secret'};
  let exit;
  const filesystem = {
    OpenTextFile(path, mode) {
      return {ReadAll:()=>path.endsWith('nd-install.json') ? JSON.stringify(cfg) : path.endsWith('nd-host.ini') ? 'GUID={11223344-1122-3344-5566-112233445566}' : files.get(path) || '',
        WriteLine:v=>files.set(path,(files.get(path)||'')+v+'\n'),Close(){}};
    },
    CreateTextFile(path) {return {Write:v=>files.set(path,v),Close(){}};},
    FolderExists:()=>true, DriveExists:()=>false, FileExists:()=>false,
    CopyFile(){}, DeleteFile(){}, CreateFolder(){},
  };
  function ActiveXObject(name) {
    if(name === 'WScript.Shell') return {ExpandEnvironmentStrings:()=> 'X:\\Windows', Run(cmd){commands.push(cmd);return needsLogin&&cmd.includes('iscsicli')?1:0;}};
    if(name === 'Scripting.FileSystemObject')return filesystem;
    return {setTimeouts(){},open(){},setRequestHeader(){},send(body){reports.push(JSON.parse(body));this.status=refuseReport?409:200;}};
  }
  function Enumerator(items){let n=0;this.atEnd=()=>n>=items.length;this.moveNext=()=>n++;this.item=()=>items[n];}
  const context = {ActiveXObject,Enumerator,VBArray:function(a){this.toArray=()=>a;},
    GetObject:()=>({ExecQuery(query){
      if(query.includes('Win32_DiskDrive'))return needsLogin&&!commands.some(c=>c.includes('QLoginTarget'))?[]:disks;
      if(query.includes('Configuration'))return [{IPAddress:[cfg.ip],Index:3}];
      return [{PNPDeviceID:'PCI\\VEN_8086&DEV_100F'}];
    }}),WScript:{Echo(){},Sleep(){},Quit(code){exit=code;}}};
  vm.runInNewContext(source, context);
  return {commands,reports,files,exit};
}
const disk={SerialNumber:'disk-uuid',Size:68719476736,Index:7};
for(const disks of [[],[disk,{...disk,Index:0}]]){
  const r=run(disks);
  assert.equal(r.exit,1);
  assert(!r.commands.some(c=>/diskpart|Apply-Image|bcdboot/.test(c)),'ambiguous disks must never be formatted');
  if(disks.length>1)assert(!r.commands.some(c=>c.includes('QLoginTarget')),'ambiguity is not a reason to connect another target');
}
const refused=run([disk],true);
assert(!refused.commands.some(c=>c.includes('diskpart /s')),'server must authorize the installing transition before clean');
const r=run([{SerialNumber:'local',Size:68719476736,Index:0},{...disk,Size:disk.Size-8225280}]);
assert.equal(r.exit,0);
assert([...r.files.values()].some(v=>v.startsWith('select disk 7\r\n')));
assert.equal(r.reports.at(-1).phase,'initializing');
assert(![...r.files.values()].filter(v=>v.includes('NexBoot ISO installation')).some(v=>v.includes('secret-token')));
assert(r.commands.some(c=>c.includes('/Add-NetAdapter')));
assert(!r.commands.some(c=>/msiscsi|NodeName|QLoginTarget/.test(c)),'an existing iBFT connection must be reused');
const fallback=run([{SerialNumber:'local',Size:68719476736,Index:0},disk],false,true);
assert.equal(fallback.exit,0,'WinRE must connect the dedicated target if iBFT did not do so');
assert(fallback.commands.some(c=>c.includes('QLoginTarget')));
assert(fallback.commands.some(c=>c.includes('NodeName "iqn.test:client"')));
assert([...fallback.files.values()].some(v=>v.startsWith('select disk 7\r\n')));
assert(![...fallback.files.values()].filter(v=>v.includes('NexBoot ISO installation')).some(v=>v.includes('chap-secret')));
