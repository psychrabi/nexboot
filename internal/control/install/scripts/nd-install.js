// Runs inside the ISO's WinRE. Disk selection is bound to the server's VPD serial and exact size.
var shell=new ActiveXObject('WScript.Shell'), fs=new ActiveXObject('Scripting.FileSystemObject');
var dir=shell.ExpandEnvironmentStrings('%SYSTEMROOT%')+'\\System32';
// 静默命令的输出文件是空的，空文件上 ReadAll 会报「输入超出了文件尾」。
function read(path){var f=fs.OpenTextFile(path,1,false,0),v=f.AtEndOfStream?'':f.ReadAll();f.Close();return v;}
function write(path,text){var f=fs.CreateTextFile(path,true,false);f.Write(text);f.Close();}
function parse(text){if(!/^[\],:{}\s]*$/.test(text.replace(/\\["\\\/bfnrtu]/g,'@').replace(/"[^"\\\n\r]*"|true|false|null|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?/g,']').replace(/(?:^|:|,)(?:\s*\[)+/g,'')))throw Error('Invalid configuration JSON');return eval('('+text+')');}
var cfg=parse(read(dir+'\\nd-install.json')), log=dir+'\\nd-install.log';
function quote(v){if(/[\r\n"]/.test(v))throw Error('Invalid command value');return '"'+v+'"';}
function append(v){v=String(v).split(cfg.base).join('<install-session>/');if(cfg.password)v=v.split(cfg.password).join('[redacted]');var f=fs.OpenTextFile(log,8,true,0);f.WriteLine(v);f.Close();}
function command(cmd,ignoreFailure){
 append(cmd);
 var output=dir+'\\nd-command.log';
 var code=shell.Run('cmd.exe /d /s /c "'+cmd+' > "'+output+'" 2>&1"',0,true);
 var out=read(output);append(out);
 if(code!==0&&!ignoreFailure)throw Error('命令 '+commandName(cmd)+' 执行失败（退出码 '+code+'）'+(lastLines(out)?'：\r\n'+lastLines(out):''));
 return out;
}
function commandName(cmd){var first=cmd.charAt(0)==='"'?cmd.slice(1,cmd.indexOf('"',1)):cmd.split(' ')[0];return first.split('\\').pop();}
// 命令自己的报错在输出末尾（dism 的「错误: 13 / 数据无效」），失败时连同退出码一起显示。
function lastLines(out){var lines=String(out||'').split(/\r?\n/),keep=[];for(var i=lines.length-1;i>=0&&keep.length<4;i--){var l=lines[i].replace(/^\s+|\s+$/g,'');if(l)keep.unshift(l);}return keep.join('\r\n');}
function gib(n){return (Number(n)/1073741824).toFixed(2)+' GiB';}
function http(){var h;try{h=new ActiveXObject('MSXML2.ServerXMLHTTP.6.0');h.setTimeouts(15000,15000,30000,30000);}catch(e){h=new ActiveXObject('WinHttp.WinHttpRequest.5.1');h.SetTimeouts(15000,15000,30000,30000);}return h;}
function json(v){return '"'+String(v).replace(/\\/g,'\\\\').replace(/"/g,'\\"').replace(/\r/g,'\\r').replace(/\n/g,'\\n').replace(/[^\x20-\x7e]/g,function(c){return '\\u'+('0000'+c.charCodeAt(0).toString(16)).slice(-4);})+'"';}
function report(phase,progress,message){var h=http();h.open('POST',cfg.base+'report',false);h.setRequestHeader('Content-Type','application/json');h.send('{"attempt":'+cfg.attempt+',"phase":'+json(phase)+',"progress":'+progress+',"message":'+json(message)+'}');if(h.status<200||h.status>=300)throw Error('Server did not accept installation state ('+h.status+')');}
function download(name,dest){command(quote(dir+'\\curl.exe')+' --fail --silent --show-error --retry 3 --connect-timeout 15 --max-time 3600 -o '+quote(dest)+' '+quote(cfg.base+name));}
function uploadLogs(){
 command(quote(dir+'\\curl.exe')+' --fail --silent --max-time 60 --data-binary @'+quote(log)+' '+quote(cfg.base+'log/console.log'));
 var dism=shell.ExpandEnvironmentStrings('%SYSTEMROOT%')+'\\Logs\\DISM\\dism.log';
 if(fs.FileExists(dism))command(quote(dir+'\\curl.exe')+' --fail --silent --max-time 60 --data-binary @'+quote(dism)+' '+quote(cfg.base+'log/dism.log'));
}
function normalize(v){return String(v||'').toLowerCase().replace(/[^a-z0-9]/g,'');}
function installationDisks(){
 var disks=new Enumerator(GetObject('winmgmts:root\\cimv2').ExecQuery('SELECT * FROM Win32_DiskDrive')),matches=[];
 for(;!disks.atEnd();disks.moveNext()){var d=disks.item();if(normalize(d.SerialNumber)===normalize(cfg.serial)&&Number(d.Size)>0&&Math.abs(Number(d.Size)-Number(cfg.size))<16*1024*1024)matches.push(d);}
 return matches;
}
function one(query){var rows=new Enumerator(GetObject('winmgmts:root\\cimv2').ExecQuery(query)),out=[];for(;!rows.atEnd();rows.moveNext())out.push(rows.item());if(out.length!==1)throw Error('Expected exactly one matching device, got '+out.length);return out[0];}
function bootNIC(){
 var configs=new Enumerator(GetObject('winmgmts:root\\cimv2').ExecQuery('SELECT * FROM Win32_NetworkAdapterConfiguration WHERE IPEnabled=True')),matches=[];
 for(;!configs.atEnd();configs.moveNext()){var c=configs.item(),ips=new VBArray(c.IPAddress).toArray();for(var i=0;i<ips.length;i++)if(ips[i]===cfg.ip)matches.push(c);}
 if(matches.length!==1)throw Error('Cannot uniquely identify the boot network adapter');
 var adapter=one('SELECT * FROM Win32_NetworkAdapter WHERE Index='+matches[0].Index),id=String(adapter.PNPDeviceID||'').toUpperCase();
 var m=id.match(/^(PCI\\VEN_[0-9A-F]{4}&DEV_[0-9A-F]{4})/);if(!m)throw Error('The boot network adapter is not a supported PCI device');return m[1];
}
var VENDORS={'15AD':'VMware','8086':'Intel','10EC':'Realtek','14E4':'Broadcom','1D6A':'Aquantia','1969':'Qualcomm Atheros','1AF4':'VirtIO','1414':'Hyper-V'};
function hasInstallAddress(){
 var configs=new Enumerator(GetObject('winmgmts:root\\cimv2').ExecQuery('SELECT * FROM Win32_NetworkAdapterConfiguration WHERE IPEnabled=True'));
 for(;!configs.atEnd();configs.moveNext()){var c=configs.item();if(c.IPAddress===null||c.IPAddress===undefined)continue;var ips=new VBArray(c.IPAddress).toArray();for(var i=0;i<ips.length;i++)if(ips[i]===cfg.ip)return true;}
 return false;
}
function peBuild(){try{return Number(shell.RegRead('HKLM\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\CurrentBuildNumber'))||0;}catch(e){return 0;}}
function driverText(d){return '  '+String(d.inf).split('\\').pop()+' '+(d.version||'（版本未知）')+'（'+(d.source==='uploaded'?'上传的驱动包':'ISO 自带')+(d.minbuild?'，要求 Windows 内部版本 '+d.minbuild+' 及以上':'')+'）';}
// 没网时 iSCSI 登不上，会被误报成找不到安装盘；这里按设备状态说清是哪块网卡、卡在哪、怎么处理。
function networkFailure(){
 var hwid=String(cfg.hwid||'').toUpperCase(),code=null,rows=new Enumerator(GetObject('winmgmts:root\\cimv2').ExecQuery('SELECT * FROM Win32_PnPEntity'));
 for(;!rows.atEnd();rows.moveNext()){var d=rows.item();if(hwid&&String(d.DeviceID||'').toUpperCase().indexOf(hwid)===0){code=Number(d.ConfigManagerErrorCode);break;}}
 var ven=hwid.match(/VEN_([0-9A-F]{4})/),nic='网卡：'+(hwid||'硬件 ID 未知')+(ven&&VENDORS[ven[1]]?'（'+VENDORS[ven[1]]+'）':'');
 var upload='在控制台「机型 → 驱动包」上传这块网卡的 Windows 10/11 x64 驱动（zip，里面要有 .inf、.sys 和 .cat 签名文件），然后重启这台客户机，安装会自动重新开始';
 var e1000e='  2. 如果是虚拟机，把网卡类型改成 E1000E（安装环境自带它的驱动），再重启这台客户机。';
 // 服务端从 INF 读出了每份驱动要求的最低内部版本；全都高于安装环境时，就是版本不支持而不是没有驱动。
 var drivers=cfg.drivers||[],build=peBuild(),names=[],newer=0,uploaded=false;
 for(var i=0;i<drivers.length;i++){names.push(driverText(drivers[i]));if(build&&drivers[i].minbuild>build)newer++;if(drivers[i].source==='uploaded')uploaded=true;}
 var lines;
 if(code===52)lines=['网卡驱动已带进安装环境，但安装环境验证不了它的签名，拒绝加载，所以无法联网。',nic,'处理：'+upload+'。Windows 自带的这份驱动在安装环境里用不了，需要厂商官方签名的驱动包。'];
 else if(code===0)lines=['网卡驱动正常，但没有拿到安装地址 '+cfg.ip+'，联系不上服务器。',nic,'处理：确认这台客户机和服务器在同一网段、服务器的 DHCP 服务正常，然后重启这台客户机重试。'];
 else if((code===null||code===28)&&drivers.length&&newer===drivers.length)lines=['带进安装环境的网卡驱动不支持这个版本的 Windows，加载不了，所以无法联网。',nic,'驱动：'].concat(names,['这个安装环境是 '+build+' 版，低于上面的要求。','处理（任选其一）：','  1. '+(uploaded?'在控制台「机型 → 驱动包」禁用这个驱动包，再上传支持内部版本 '+build+' 的驱动（打开 .inf，[Manufacturer] 那一行不能带比 '+build+' 大的版本号，例如 ...20124），然后重启这台客户机，安装会自动重新开始':upload)+'；',e1000e]);
 else if((code===null||code===28)&&drivers.length)lines=['网卡驱动已带进安装环境，但没有装上'+(code===28?'（设备错误代码 28）':'')+'，无法联网。',nic,'驱动：'].concat(names,['处理（任选其一）：','  1. '+upload+'；drvload 的报错记在安装日志「PE 控制台」里，可以先看原因；',e1000e]);
 else if(code===null||code===28)lines=['安装环境里没有这块网卡的驱动，无法联网，所以连不上服务器上的安装盘。',nic,'处理（任选其一）：','  1. '+upload+'；',e1000e];
 else lines=['网卡驱动加载失败（设备错误代码 '+code+'），无法联网。',nic,'处理：'+upload+'。'];
 lines.push('本地磁盘没有被改动。');
 return lines.join('\r\n');
}
try{
 write(log,'NexBoot ISO installation\r\n');
 // nd-install.cmd 记下的 drvload 输出：驱动为什么没装上要靠它查。
 var drvload=dir+'\\nd-drvload.log';if(fs.FileExists(drvload))append(read(drvload));
 WScript.Echo('[1/5] 正在检查网络和安装盘，请勿关闭电源。');
 for(var net=0;!hasInstallAddress()&&net<10;net++)WScript.Sleep(2000);
 if(!hasInstallAddress())throw Error(networkFailure());
 // Read-only discovery comes before any diskpart or filesystem operation.
 var matches=installationDisks();
 for(var wait=0;matches.length===0&&wait<3;wait++){WScript.Sleep(3000);matches=installationDisks();}
 if(matches.length===0){
  // Some WinRE environments need a user-mode login even though iPXE supplied iBFT.
  shell.Run('net start msiscsi',0,true);
  command('iscsicli NodeName '+quote(cfg.initiator),true);
  command('iscsicli QAddTargetPortal '+quote(cfg.portal),true);
  command('iscsicli QLoginTarget '+quote(cfg.target)+(cfg.username?' '+quote(cfg.username)+' '+quote(cfg.password):''),true);
  for(var retry=0;retry<10;retry++){WScript.Sleep(1000);matches=installationDisks();if(matches.length!==0)break;}
 }
 if(matches.length!==1)throw Error('Installation disk identity is missing or ambiguous; local disks were not modified');
 if(fs.DriveExists('W:')||fs.DriveExists('S:'))throw Error('Installation drive letters W: or S: are already used; local disks were not modified');
 var hwid=bootNIC();
 report('installing',10,'已唯一确认服务端安装盘，正在安装 Windows');
 write(dir+'\\nd-partition.txt','select disk '+matches[0].Index+'\r\nattributes disk clear readonly\r\nonline disk noerr\r\nclean\r\nconvert gpt\r\ncreate partition efi size=260\r\nformat quick fs=fat32 label=System\r\nassign letter=S\r\ncreate partition msr size=16\r\ncreate partition primary\r\nformat quick fs=ntfs label=Windows\r\nassign letter=W\r\n');
 command('diskpart /s '+quote(dir+'\\nd-partition.txt'));
 WScript.Echo('[2/5] 正在展开 Windows 安装文件，此步骤可能需要较长时间。');
 download('install.wim','W:\\nd-install.wim');
 var got=cfg.wimsize?fs.GetFile('W:\\nd-install.wim').Size:0;
 if(cfg.wimsize&&Number(got)!==Number(cfg.wimsize))throw Error('Windows 安装文件没有下载完整（收到 '+gib(got)+'，应为 '+gib(cfg.wimsize)+'），多半是下载途中服务器重启或网络中断。请确认服务器内存充足、服务稳定后，在镜像页对这次安装点「重试」。');
 command('dism /Apply-Image /ImageFile:W:\\nd-install.wim /Index:'+cfg.index+' /ApplyDir:W:\\');
 fs.DeleteFile('W:\\nd-install.wim',true);
 report('installing',65,'Windows 文件已安装，正在准备启动网卡');
 WScript.Echo('[3/5] 正在配置启动网卡和无盘启动。');
 if(fs.FolderExists('X:\\nddrivers'))command('dism /Image:W:\\ /Add-Driver /Driver:X:\\nddrivers /Recurse');
 command('cscript //nologo '+quote(dir+'\\ndnic.js')+' host '+quote(hwid)+' '+quote(cfg.busloc||'')+' '+quote(dir+'\\nd-host.ini'));
 var host=read(dir+'\\nd-host.ini').match(/GUID=(\{[0-9A-Fa-f-]+\})/);if(!host)throw Error('Cannot read the boot adapter GUID');
 command('dism /Image:W:\\ /Add-NetAdapter /HostAdapter:'+host[1]+' /BootDriver:ms_tcpip /BootDriver:ms_tcpip6');
 command('reg load HKLM\\NDOFF W:\\Windows\\System32\\config\\SYSTEM');
 try {command('cscript //nologo '+quote(dir+'\\ndnic.js')+' adapt '+quote(hwid)+' '+quote(cfg.busloc||'')+' '+quote(dir+'\\nd-adapt-result.json')+' W:');} finally {command('reg unload HKLM\\NDOFF');}
 WScript.Echo('[4/5] 正在设置无人值守初始化和启动文件。');
 if(!fs.FolderExists('W:\\Windows\\Panther'))fs.CreateFolder('W:\\Windows\\Panther');
 fs.CopyFile(dir+'\\unattend.xml','W:\\Windows\\Panther\\unattend.xml',true);
 if(!fs.FolderExists('W:\\ProgramData\\NexBoot'))fs.CreateFolder('W:\\ProgramData\\NexBoot');
 fs.CopyFile(dir+'\\iso-startup.ps1','W:\\ProgramData\\NexBoot\\iso-startup.ps1',true);
 if(!fs.FolderExists('W:\\Windows\\Setup\\Scripts'))fs.CreateFolder('W:\\Windows\\Setup\\Scripts');
 write('W:\\Windows\\Setup\\Scripts\\SetupComplete.cmd','@echo off\r\nschtasks.exe /Run /TN NexBootISO\r\n');
 command('bcdboot W:\\Windows /s S: /f UEFI');
 if(!fs.FolderExists('S:\\EFI\\Boot'))fs.CreateFolder('S:\\EFI\\Boot');
 fs.CopyFile('W:\\Windows\\Boot\\EFI\\bootmgfw.efi','S:\\EFI\\Boot\\bootx64.efi',true);
 report('initializing',90,'Windows 启动配置已完成，即将重启进行首次系统初始化');
 WScript.Echo('[5/5] 安装准备完成，即将自动重启。初始化完成后会自动关机，请在任务中心查看结果。');
 try{uploadLogs();}catch(e){}
 WScript.Quit(0);
}catch(e){
 append(String(e.message));WScript.Echo('安装失败：'+e.message);
 try{report('failed',0,'Windows 安装或启动准备失败，请查看安装日志：'+e.message);}catch(ignored){}
 try{uploadLogs();}catch(ignored){}
 WScript.Quit(1);
}
