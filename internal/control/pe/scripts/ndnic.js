// Native WinPE JScript: reg.exe copies complete subtrees, including custom value types.
var sh = new ActiveXObject("WScript.Shell"), fs = new ActiveXObject("Scripting.FileSystemObject");
var args = WScript.Arguments, mode=args(0), hwid=args(1).toUpperCase(), busloc=args(2), output=args(3);
var netclass="{4d36e972-e325-11ce-bfc1-08002be10318}", host="HKLM\\SYSTEM\\CurrentControlSet";
function quote(v){if(/[\r\n]/.test(v))throw Error("invalid registry input");return '"'+v.replace(/(\\*)"/g,'$1$1\\"').replace(/(\\+)$/g,'$1$1')+'"';}
function run(cmd, optional){var p=sh.Exec("reg.exe "+cmd); var out=p.StdOut.ReadAll(), err=p.StdErr.ReadAll();while(p.Status===0)WScript.Sleep(20); if(p.ExitCode!==0&&!optional)throw Error(cmd+": "+err);return p.ExitCode===0?out:"";}
function get(key,name,optional){var out=run("query "+quote(key)+" /v "+quote(name),optional);var m=out.match(/\s+REG_\w+\s+(.*)/);return m?m[1].replace(/\r/g,""):"";}
function keys(key){var lines=run("query "+quote(key)).split(/\r?\n/),r=[];for(var i=0;i<lines.length;i++)if(/^HKEY_/.test(lines[i]))r.push(lines[i]);return r;}
function set(key,name,type,value){run("add "+quote(key)+" /v "+quote(name)+" /t "+type+" /d "+quote(value)+" /f");}
function multi(key,name,list){set(key,name,"REG_MULTI_SZ",list.join("\\0"));}
function append(key,name,value){var old=get(key,name,true).split("\\0"),r=[];for(var i=0;i<old.length;i++)if(old[i]&&old[i].toLowerCase()!==value.toLowerCase())r.push(old[i]);r.push(value);multi(key,name,r);}
function json(v){return '"'+String(v).replace(/\\/g,"\\\\").replace(/"/g,'\\"').replace(/\r/g,"\\r").replace(/\n/g,"\\n").replace(/[^\x20-\x7e]/g,function(c){return '\\u'+('0000'+c.charCodeAt(0).toString(16)).slice(-4);})+'"';}
function write(text){var f=fs.CreateTextFile(output,true,false);f.Write(text);f.Close();}
// iPXE 的 ${net0/busloc} 是十六进制（0x300 = 总线 3 设备 0 功能 0），也兼容十进制。
function parseBusloc(v){v=String(v);return /^0x/i.test(v)?parseInt(v,16):parseInt(v,10);}
// Windows 8 起 LocationInformation 是 "@System32\drivers\pci.sys,#65536;PCI bus %1, ...;(3,0,0)"，位置在末尾括号里；也认旧的纯文本格式。
function pciLocation(text){text=String(text||"");var m=text.match(/\((\d+),\s*(\d+),\s*(\d+)\)\s*$/)||(text.charAt(0)==="@"?null:text.match(/(\d+)\D+(\d+)\D+(\d+)/));return m?parseInt(m[1],10)*256+parseInt(m[2],10)*8+parseInt(m[3],10):null;}
// 只有一块同型号网卡时就是它；网卡位置只用来在多块同型号网卡之间区分。
function findHost(){var classes=keys(host+"\\Control\\Class\\"+netclass), found=[], matches=[], seen=[];for(var i=0;i<classes.length;i++){var id=get(classes[i],"MatchingDeviceId",true).toUpperCase();if(id.indexOf(hwid)===0){var device=get(classes[i],"DeviceInstanceID",true),location=device?get(host+"\\Enum\\"+device,"LocationInformation",true):"";found.push({key:classes[i],loc:pciLocation(location)});seen.push(location.replace(/^.*;/,"")||"未知");}}if(found.length===1)return found[0].key;var want=busloc?parseBusloc(busloc):null;for(var j=0;j<found.length;j++)if(want!==null&&found[j].loc===want)matches.push(found[j].key);if(matches.length!==1)throw Error("启动网卡匹配数量="+matches.length+"（硬件 ID "+hwid+" 的网卡 "+found.length+" 块"+(seen.length?"，位置 "+seen.join("、"):"")+"；启动网卡位置 "+(busloc||"未知")+"），请确认客户机只使用一块启动网卡");return matches[0];}
function locationPaths(instance){
 // DEVPKEY_Device_LocationPaths is stored as registry type 0xffff2012,
 // not a named Enum value. Export only this property to retain its custom type.
 var key=host+"\\Enum\\"+instance+"\\Properties\\{a45c254e-df1c-4efd-8020-67d146a850e0}\\0025", file=fs.GetSpecialFolder(2)+"\\ndlocation.reg";
 try{run("export "+quote(key)+" "+quote(file)+" /y");}catch(e){throw Error("读取启动网卡位置属性失败，请查看任务日志并重新适配");}
 var stream=fs.OpenTextFile(file,1,false,-1), text=stream.ReadAll();stream.Close();
 text=text.replace(/\\\r?\n\s*/g,"");
 var block=text.match(/\[([^\]\r\n]+)\]([^\[]*)/), value=block&&block[1].toLowerCase()===key.replace(/^HKLM/,"HKEY_LOCAL_MACHINE").toLowerCase()?block[2].match(/^@=hex\(ffff2012\):([0-9a-f, \t]+)\r?$/im):null;
 if(!value)throw Error("启动网卡位置属性格式不正确，请查看任务日志并重新适配");
 var hex=value[1].replace(/[, \t]/g,""), decoded="";
 if(hex.length%4)throw Error("启动网卡位置属性格式不正确，请查看任务日志并重新适配");
 for(var i=0;i<hex.length;i+=4)decoded+=String.fromCharCode(parseInt(hex.substr(i+2,2)+hex.substr(i,2),16));
 if(decoded.slice(-2)!=="\0\0")throw Error("启动网卡位置属性格式不正确，请查看任务日志并重新适配");
 return decoded.split("\0");
}
if(mode==="failure"){
 if(args(2)==="1"&&fs.FileExists(output)){
  var prior=fs.OpenTextFile(output,1,false,0), priorText=prior.ReadAll();prior.Close();
  if(/^\s*\{\s*"ok"\s*:\s*false[,}]/.test(priorText))WScript.Quit(0);
 }
 write('{"ok":false,"stage":'+json(args(1))+',"message":'+json(({"10":"恢复环境无法连接服务器，请检查网络和启动网卡驱动","11":"未找到适配系统盘，请检查客户机与存储节点的连接","12":"安装网卡驱动失败，请上传匹配的已签名驱动包","13":"没有找到启动网卡，请上传匹配的驱动包","14":"DISM未生成网卡接口，请检查任务DISM日志","15":"加载离线SYSTEM注册表失败，请重试适配","16":"卸载离线SYSTEM注册表失败，未保存还原点","17":"加载镜像SOFTWARE注册表失败，请在任务详情查看reg load错误","18":"卸载镜像版本信息失败，未保存还原点","19":"无法读取镜像CurrentBuildNumber，请在任务详情检查系统版本信息","20":"Windows版本超出当前恢复环境的兼容上限，请在任务详情核对Windows/WinPE版本，确认支持后重试"})[args(2)]||"步骤退出码 "+args(2))+'}');WScript.Quit(0);
}
try {
 var hk=findHost(), guid=get(hk,"NetCfgInstanceId"), instance=get(hk,"DeviceInstanceID"), service=get(host+"\\Enum\\"+instance,"Service");
 var hostguid=guid, hostinf=get(hk,"InfPath");
 if(!guid||!instance||instance.toUpperCase().indexOf(hwid)!==0)throw Error("未取得启动网卡完整身份，请上传匹配的网卡驱动");
 if(mode==="host"){write("GUID="+guid+"\r\n");WScript.Quit(0);}
 var classkey=hk.substring(hk.lastIndexOf("\\")+1), luid="";
 if(mode==="adapt"){
  var current=parseInt(get("HKLM\\NDOFF\\Select","Current"),16), cs="ControlSet"+("000"+current).slice(-3), off="HKLM\\NDOFF\\"+cs;
  // 25H2 compatibility setting reported by the iPXE community: https://github.com/ipxe/ipxe/discussions/1544
  var build=0, versions=fs.FolderExists(args(4)+"\\Windows\\servicing\\Version")?new Enumerator(fs.GetFolder(args(4)+"\\Windows\\servicing\\Version").SubFolders):null;
  for(;versions&&!versions.atEnd();versions.moveNext()){var parts=versions.item().Name.split(".");if(parts.length>2)build=Math.max(build,parseInt(parts[2],10));}
  // iSCSI 启动写不出蓝屏转储；关掉它，开机就不必为转储准备一套 iSCSI 驱动栈。
  set(off+"\\Control\\CrashControl","CrashDumpEnabled","REG_DWORD","0");
  if(build>=26200){var feature=off+"\\Control\\FeatureManagement\\Overrides\\8\\3364258958";set(feature,"EnabledState","REG_DWORD","1");set(feature,"EnabledStateOptions","REG_DWORD","0");set(feature,"Variant","REG_DWORD","0");set(feature,"VariantPayload","REG_DWORD","0");set(feature,"VariantPayloadKind","REG_DWORD","0");set(feature,"TelemetryFlags","REG_DWORD","1");}
  // DeviceLocations may retain another adapter with the same VEN/DEV at an old
  // slot. Match the loaded device's complete hardware ID and physical paths.
  var paths=locationPaths(instance), deviceID=instance.substring(0,instance.lastIndexOf("\\")).replace(/\\/g,"#"), targets=[];
  WScript.Echo("boot adapter location paths: "+paths.join(" | "));
  for(var j=0;j<paths.length;j++)if(paths[j]){
   var target=get(off+"\\Control\\DeviceLocations\\"+paths[j]+"\\"+deviceID+"\\Configuration\\Driver","NetCfgInstanceId",true), duplicate=false;
   for(var ti=0;ti<targets.length;ti++)if(targets[ti].toUpperCase()===target.toUpperCase())duplicate=true;
   if(target&&!duplicate)targets.push(target);
  }
  if(targets.length!==1)throw Error("启动网卡位置匹配数量="+targets.length+"，请检查DISM日志中的网卡位置并重新适配");
  guid=targets[0];
  var kernel=off+"\\Control\\NetworkSetup2\\Interfaces\\"+guid+"\\Kernel";
  luid=get(kernel,"NetLuidIndex");if(!luid)throw Error("DISM接口缺少NetLuidIndex");
  var nk="";
  for(var n=0;n<10000;n++){classkey=("0000"+n).slice(-4);nk=off+"\\Control\\Class\\"+netclass+"\\"+classkey;if(!run("query "+quote(nk),true))break;}
  if(n===10000)throw Error("没有空闲网卡类实例");
  run("copy "+quote(hk)+" "+quote(nk)+" /s /f");
  set(nk,"NetCfgInstanceId","REG_SZ",guid);set(nk,"NetLuidIndex","REG_DWORD",luid);set(nk,"DeviceInstanceID","REG_SZ",instance);
  // Locate the published INF by the boot adapter's hardware ID. Preserve inbox names as well as oemN.inf.
  var inf=get(hk,"InfPath"), infdir=fs.GetFolder(args(4)+"\\Windows\\INF"), infs=new Enumerator(infdir.Files), published="";
  for(;!infs.atEnd();infs.moveNext()){var f=infs.item();if(/\.inf$/i.test(f.Name)){var stream, text="";try{stream=fs.OpenTextFile(f.Path,1,false,-1);text=stream.ReadAll().toUpperCase();stream.Close();}catch(ignore){}if(text.indexOf(hwid)<0){stream=fs.OpenTextFile(f.Path,1,false,0);text=stream.ReadAll().toUpperCase();stream.Close();}if(text.indexOf(hwid)>=0){published=f.Name;if(/^oem\d+\.inf$/i.test(f.Name))break;}}}
  if(!published)throw Error("离线Windows中找不到启动网卡的已发布INF");set(nk,"InfPath","REG_SZ",published);
  multi(nk+"\\Linkage","RootDevice",[guid]);multi(nk+"\\Linkage","Export",["\\Device\\"+guid]);
  // DISM creates the protocol-specific bindings. Preserve them and verify the new interface is bound.
  var services=["Tcpip","Tcpip6"];
  for(var si=0;si<services.length;si++){var link=off+"\\Services\\"+services[si]+"\\Linkage", bound=get(link,"Bind",true);if(bound.toUpperCase().indexOf(guid.toUpperCase())<0)throw Error("DISM未绑定协议："+services[si]);}
  set(off+"\\Services\\Tcpip\\Parameters\\Interfaces\\"+guid,"EnableDHCP","REG_DWORD","1");
  set(off+"\\Services\\Tcpip6\\Parameters\\Interfaces\\"+guid,"EnableDHCP","REG_DWORD","1");
  set(off+"\\Services\\NetBT\\Parameters\\Interfaces\\Tcpip_"+guid,"NetbiosOptions","REG_DWORD","0");
  // Map the same verified GUID/INF references as the P0 successful template; reg export retains custom property types.
  var src=host+"\\Enum\\"+instance, dst=off+"\\Enum\\"+instance, exported=fs.GetSpecialFolder(2)+"\\ndenum.reg";
  run("export "+quote(src)+" "+quote(exported)+" /y");
  var stream=fs.OpenTextFile(exported,1,false,-1), text=stream.ReadAll();stream.Close();
  text=text.replace(/\\\r?\n\s*/g,"");
  function replaceAll(value,from,to){return value.split(from).join(to);}
  function hex16(value){var b=[];for(var h=0;h<value.length;h++){var c=value.charCodeAt(h);b.push(("0"+(c&255).toString(16)).slice(-2),("0"+(c>>8).toString(16)).slice(-2));}return b.join(",");}
  text=replaceAll(text,"HKEY_LOCAL_MACHINE\\SYSTEM\\CurrentControlSet\\Enum\\"+instance,"HKEY_LOCAL_MACHINE\\NDOFF\\"+cs+"\\Enum\\"+instance);
  text=replaceAll(text,hostguid,guid);text=replaceAll(text,hostinf,published);
  text=replaceAll(text,hex16(hostguid),hex16(guid));text=replaceAll(text,hex16(hostinf),hex16(published));
  var f=fs.CreateTextFile(exported,true,true);f.Write(text);f.Close();run("import "+quote(exported));
  set(off+"\\Enum\\"+instance,"Driver","REG_SZ",netclass+"\\"+classkey);
  // Staging a new NIC package does not necessarily create its service. Preserve
  // an existing target service; otherwise copy the complete loaded PE service.
  var newService=!get(off+"\\Services\\"+service,"ImagePath",true);
  if(newService)run("copy "+quote(host+"\\Services\\"+service)+" "+quote(off+"\\Services\\"+service)+" /s /f");
  var names=[service,"msiscsi","iScsiPrt"];for(var ni=0;ni<names.length;ni++)set(off+"\\Services\\"+names[ni],"Start","REG_DWORD","0");set(off+"\\Services\\"+service,"BootFlags","REG_DWORD","1");
  var image=get(off+"\\Services\\"+service,"ImagePath").replace(/^\\SystemRoot\\/i,"").replace(/^System32/i,"Windows\\System32");
  // A non-boot-critical package can be staged without reflecting its binary.
  // Copy the loaded PE binary only for a new service whose target file is absent.
  if(newService&&!fs.FileExists(args(4)+"\\"+image))fs.CopyFile(fs.GetSpecialFolder(0)+"\\"+image.replace(/^Windows\\/i,""),args(4)+"\\"+image,false);
  if(!fs.FileExists(args(4)+"\\"+image))throw Error("启动驱动文件不存在："+image);
  if(get(nk,"NetLuidIndex")!==get(kernel,"NetLuidIndex")||get(nk,"DeviceInstanceID")!==instance||get(off+"\\Enum\\"+instance,"Driver")!==netclass+"\\"+classkey)throw Error("设备实例、类实例或LUID自检失败");
  if(get(nk+"\\Linkage","RootDevice").toUpperCase().indexOf(guid.toUpperCase())<0||get(nk+"\\Linkage","Export").toUpperCase().indexOf(guid.toUpperCase())<0)throw Error("网卡Linkage自检失败");
  WScript.Echo("self-check OK: "+instance+" class="+classkey+" guid="+guid+" luid="+luid);
 }
 write('{"ok":true,"stage":"'+mode+'","message":'+json("网卡配置与设备实例自检通过")+',"data":{"instance_id":'+json(instance)+',"hwid":'+json(hwid)+',"class_key":'+json(classkey)+',"netcfg_instance_id":'+json(guid)+',"luid":'+json(luid)+'}}');
} catch(e){WScript.Echo(e.message);write('{"ok":false,"stage":"'+(mode==="adapt"?"registry":mode)+'","message":'+json(e.message)+'}');WScript.Quit(1);}
