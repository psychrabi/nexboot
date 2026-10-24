// NexBoot WinPE 回报兜底：cscript //nologo ndpe-report.js <url> [file]
// WinRE 自带 cscript 但没有 curl.exe；用 WinHttp 发 POST，文件按文本读入。
var args = WScript.Arguments;
if (args.length < 1) { WScript.Quit(2); }
function restartReady(mode, code, reported) {
  return mode === "identity" && code === "0" && (reported === "0" || reported === "logs");
}
if (args(0) === "restart") { WScript.Quit(restartReady(args(1), args(2), args(3)) ? 0 : 1); }
if (args(0) === "show") {
  var phase = args(1), mode = args(2);
  function say(text) { WScript.Echo(text); }
  if (phase === "start") {
    say("NexBoot · " + (mode === "adapt" ? "机型适配" : mode === "identity" ? "终端身份采集" : "恢复环境检查"));
    say(mode === "adapt" ? "正在为这类网卡生成 Windows 启动配置，并检查能否用于开机。" : mode === "identity" ? "正在采集这台客户机的启动网卡身份，供下次 Windows 开机使用。" : "正在检查恢复环境、网络和结果回报。" );
    say("本次进入的是维护环境，不会进入 Windows 桌面。请勿断电或重启。" );
    say(mode === "identity" ? "身份保存成功后将自动重启，继续启动 Windows；失败时自动关机。" : "完成后将自动关机；请在 NexBoot 控制台的任务中心查看最终结果。" );
  } else if (phase === "finish") {
    var failed = args(3) !== "0", submitted = args(4) === "0" || args(4) === "logs";
    var restart = restartReady(mode, args(3), args(4));
    if (!submitted) {
      say("未能确认服务器收到结果，不能确认完成。请检查网络并查看任务中心。" );
    } else if (failed) {
      say(mode === "adapt" ? "本次适配失败，未切换当前还原点。" : "本次检查失败，失败结果已提交给服务器。" );
    } else if (mode === "adapt") {
      say("本机适配与自检通过，结果已提交给服务器。" );
      say("关机后服务器才会保存还原点；请到任务中心确认最终成功，再开机测试 Windows。" );
    } else {
      say((mode === "identity" ? "终端身份采集" : "恢复环境检查") + "结果已提交，请到任务中心确认最终结果。" );
    }
    if (failed) {
      var detail = "步骤退出码 " + args(3);
      try {
        var fs = new ActiveXObject("Scripting.FileSystemObject");
        var file = fs.OpenTextFile(args(5), 1, false, 0), text = file.ReadAll(); file.Close();
        var message = text.match(/"message"\s*:\s*"((?:\\.|[^"\\])*)"/);
        if (message) detail = message[1].replace(/\\(u[0-9a-f]{4}|["\\\/bfnrt])/gi, function(all, escaped) {
          return escaped.charAt(0) === "u" ? String.fromCharCode(parseInt(escaped.substring(1), 16)) : ({'"':'"', '\\':'\\', '/':'/', b:'\b', f:'\f', n:'\n', r:'\r', t:'\t'})[escaped];
        }).replace(/[\x00-\x1f]/g, " ");
      } catch (ignore) {}
      say("失败原因：" + detail);
    }
    if (args(4) === "logs") say("日志上传未完成，请在任务中心检查日志是否齐全。" );
    say("请在 NexBoot 控制台的任务中心查看详情和日志。" );
    say(restart ? "身份已保存。客户机将在 20 秒后自动重启，继续启动 Windows。" : "请记住以上结果。客户机将在 20 秒后自动关机。" );
    for (var seconds = 20; seconds > 0; seconds--) {
      if (seconds % 5 === 0) say((restart ? "自动重启倒计时：" : "自动关机倒计时：") + seconds + " 秒");
      WScript.Sleep(1000);
    }
  } else {
    var stages = {network:"正在准备网络与恢复环境，可能需要几分钟，请勿重启。", disk:"[1/5] 正在连接服务器上的适配系统盘。", environment:"[2/5] 正在检查 Windows 与恢复环境版本。", driver:"[3/5] 正在安装启动网卡驱动。", adapter:"[4/5] 正在生成网卡网络配置。", registry:"[5/5] 正在核对启动网卡的设备实例与配置。", identity:"正在读取这台客户机的启动网卡身份。", probe:"正在检查系统版本、网络和启动网卡。", report:"正在上传日志与执行结果，请等待回报完成。"};
    say(stages[phase] || "正在执行维护任务，请稍候。" );
  }
  WScript.Quit(0);
}
var url = args(0), body = "";
if (args.length > 1) {
  var fso = new ActiveXObject("Scripting.FileSystemObject");
  if (fso.FileExists(args(1))) {
    var ts = fso.OpenTextFile(args(1), 1, false, -2);
    body = ts.AtEndOfStream ? "" : ts.ReadAll();
    ts.Close();
  }
}
var http = new ActiveXObject("WinHttp.WinHttpRequest.5.1");
http.SetTimeouts(10000, 10000, 30000, 120000);
http.Open("POST", url, false);
http.SetRequestHeader("Content-Type", "application/octet-stream");
http.Send(body);
if (http.Status >= 200 && http.Status < 300) { WScript.Quit(0); }
WScript.Echo("report failed: " + http.Status + " " + http.StatusText);
WScript.Quit(1);
