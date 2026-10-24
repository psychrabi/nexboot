package install

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 用 node 模拟 WSH（WMI、文件、shell）把 WinPE 里的安装脚本跑到第一处失败，看屏幕上打出什么。
const wshHarness = `
const __fs = require('fs');
const fx = JSON.parse(process.env.FIXTURES);
const echoes = [], files = {}, reports = [];
// 真实的 WScript.Quit 立即结束脚本，不会被脚本自己的 catch 接住。
const LOG = 'X:\\Windows\\System32\\nd-install.log';
global.WScript = { Echo: s => echoes.push(String(s)), Sleep: () => {}, Quit: c => { console.log(JSON.stringify({ screen: echoes.join('\n'), quit: c, reports, log: files[LOG] || '' })); process.exit(0); } };
global.Enumerator = class { constructor(a) { this.a = a; this.i = 0; } atEnd() { return this.i >= this.a.length; } moveNext() { this.i++; } item() { return this.a[this.i]; } };
global.VBArray = function (a) { this.toArray = () => a; };
global.GetObject = () => ({ ExecQuery: q => q.includes('Win32_NetworkAdapterConfiguration') ? (fx.configs || []) : q.includes('Win32_PnPEntity') ? (fx.pnp || []) : q.includes('Win32_DiskDrive') ? (fx.disks || []) : q.includes('Win32_NetworkAdapter') ? (fx.adapters || []) : [] });
global.ActiveXObject = function (name) {
  // 按命令名模拟失败：把输出写进命令重定向到的文件，返回指定的退出码。
  // 安装环境的内部版本号从注册表读；fx.build 不给就当读不到。
  if (name === 'WScript.Shell') return { RegRead: k => { if (fx.build === undefined || !/CurrentBuildNumber$/.test(k)) throw new Error('no value'); return fx.build; }, ExpandEnvironmentStrings: s => s.replace('%SYSTEMROOT%', 'X:\\Windows'), Run: c => {
    const f = (fx.fail || []).find(x => c.includes(x.match)), out = c.match(/> "([^"]+)" 2>&1/);
    if (f && out) files[out[1]] = f.output || '';
    return f ? f.code : 0;
  } };
  if (name === 'Scripting.FileSystemObject') return {
    // 与真实 WSH 一致：命令输出文件存在但为空时，ReadAll 报错，AtEndOfStream 为真。
    OpenTextFile: p => ({ get AtEndOfStream() { return !files[p]; }, ReadAll: () => { if (!files[p]) throw new Error('输入超出了文件尾'); return files[p]; }, WriteLine: v => { files[p] = (files[p] || '') + v + '\n'; }, Close: () => {} }),
    CreateTextFile: p => ({ Write: v => { files[p] = v; }, Close: () => {} }),
    FileExists: p => p in files, DriveExists: () => false, FolderExists: () => false, CreateFolder: () => {}, CopyFile: () => {}, DeleteFile: () => {},
    GetFile: () => ({ Size: fx.wimGot === undefined ? 1 : fx.wimGot }),
  };
  if (fx.online && name === 'MSXML2.ServerXMLHTTP.6.0') return { setTimeouts: () => {}, open: () => {}, setRequestHeader: () => {}, send: b => reports.push(b), status: 204 };
  throw new Error('no network');
};
if (fx.hostIni) files['X:\\Windows\\System32\\nd-host.ini'] = fx.hostIni;
if (fx.drvload) files['X:\\Windows\\System32\\nd-drvload.log'] = fx.drvload;
files['X:\\Windows\\System32\\nd-install.json'] = JSON.stringify(Object.assign({ base: 'http://srv/install/t/', attempt: 1, ip: '192.168.10.10', hwid: 'PCI\\VEN_15ad&DEV_07b0', serial: 'x', size: 1, portal: 'p', target: 't', initiator: 'i', wimsize: 1 }, fx.cfg || {}));
eval(__fs.readFileSync(process.env.SRC, 'utf8'));
console.log(JSON.stringify({ screen: echoes.join('\n'), quit: null, reports, log: files[LOG] || '' }));
`

func runInstallScript(t *testing.T, fixtures map[string]any) (string, int) {
	t.Helper()
	screen, quit, _ := runInstallScriptLog(t, fixtures)
	return screen, quit
}

// runInstallScriptLog 另外返回安装日志 nd-install.log 的内容。
func runInstallScriptLog(t *testing.T, fixtures map[string]any) (string, int, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("模拟 WSH 需要 node")
	}
	b, _ := json.Marshal(fixtures)
	cmd := exec.Command(node, "-e", wshHarness)
	cmd.Env = append(os.Environ(), "FIXTURES="+string(b), "SRC="+filepath.Join("scripts", "nd-install.js"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness: %v %s", err, out)
	}
	var res struct {
		Screen string `json:"screen"`
		Quit   int    `json:"quit"`
		Log    string `json:"log"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("harness output %s: %v", out, err)
	}
	return res.Screen, res.Quit, res.Log
}

func TestInstallScriptExplainsMissingNetworkBeforeTouchingDisks(t *testing.T) {
	nic := func(code int) []map[string]any {
		return []map[string]any{{"DeviceID": `PCI\VEN_8086&DEV_100E\3&1`, "ConfigManagerErrorCode": 0}, {"DeviceID": `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\FF`, "ConfigManagerErrorCode": code}}
	}
	for _, tc := range []struct {
		name    string
		configs []map[string]any
		pnp     []map[string]any
		want    []string
	}{
		{"没有驱动", nil, nic(28), []string{"安装失败：安装环境里没有这块网卡的驱动", `PCI\VEN_15AD&DEV_07B0（VMware）`, "「机型 → 驱动包」", ".cat", "E1000E", "重启这台客户机", "本地磁盘没有被改动"}},
		{"签名不过", nil, nic(52), []string{"验证不了它的签名", `PCI\VEN_15AD&DEV_07B0（VMware）`, "厂商官方签名的驱动包", "本地磁盘没有被改动"}},
		{"没拿到地址", []map[string]any{{"IPAddress": []string{"169.254.3.4"}}}, nic(0), []string{"没有拿到安装地址 192.168.10.10", "DHCP", "本地磁盘没有被改动"}},
		{"找不到这块网卡", nil, nil, []string{"安装环境里没有这块网卡的驱动", `PCI\VEN_15AD&DEV_07B0（VMware）`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen, quit := runInstallScript(t, map[string]any{"configs": tc.configs, "pnp": tc.pnp})
			if quit != 1 {
				t.Fatalf("quit = %d, screen:\n%s", quit, screen)
			}
			for _, part := range tc.want {
				if !strings.Contains(screen, part) {
					t.Errorf("screen lacks %q:\n%s", part, screen)
				}
			}
			if strings.Contains(screen, "Installation disk identity") {
				t.Errorf("network failure misreported as missing disk:\n%s", screen)
			}
		})
	}
	// 网络正常时不拦，照常进入找盘（这里没有盘，报原来的错）。
	screen, _ := runInstallScript(t, map[string]any{"configs": []map[string]any{{"IPAddress": []string{"192.168.10.10"}}}, "pnp": nic(0)})
	if !strings.Contains(screen, "Installation disk identity") || strings.Contains(screen, "网卡") {
		t.Fatalf("healthy network should reach disk discovery:\n%s", screen)
	}
}

// 静默命令（curl 下载等）没有输出，输出文件为空；脚本不能因为读空文件而失败。
func TestInstallScriptSurvivesCommandsWithoutOutput(t *testing.T) {
	screen, quit := runInstallScript(t, map[string]any{
		"online":   true,
		"configs":  []map[string]any{{"IPAddress": []string{"192.168.10.10"}, "Index": 7}},
		"adapters": []map[string]any{{"PNPDeviceID": `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\FF`}},
		"disks":    []map[string]any{{"SerialNumber": "x", "Size": 1, "Index": 1}},
		"hostIni":  "GUID={11111111-2222-3333-4444-555555555555}\r\n",
	})
	if quit != 0 || !strings.Contains(screen, "[5/5]") {
		t.Fatalf("quit = %d, screen:\n%s", quit, screen)
	}
}

func healthyInstall() map[string]any {
	return map[string]any{
		"online":   true,
		"configs":  []map[string]any{{"IPAddress": []string{"192.168.10.10"}, "Index": 7}},
		"adapters": []map[string]any{{"PNPDeviceID": `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\FF`}},
		"disks":    []map[string]any{{"SerialNumber": "x", "Size": 1, "Index": 1}},
		"hostIni":  "GUID={11111111-2222-3333-4444-555555555555}\r\n",
	}
}

// 命令失败时屏幕上要有它自己的报错，只有退出码看不出原因。
func TestInstallScriptShowsFailingCommandOutput(t *testing.T) {
	fx := healthyInstall()
	fx["fail"] = []map[string]any{{"match": "dism /Apply-Image", "code": 13, "output": "部署映像服务和管理工具\r\n版本: 10.0.26100.1\r\n\r\n错误: 13\r\n\r\n数据无效。\r\n\r\n可以在 X:\\windows\\Logs\\DISM\\dism.log 上找到 DISM 日志文件\r\n"}}
	screen, quit := runInstallScript(t, fx)
	if quit != 1 {
		t.Fatalf("quit = %d, screen:\n%s", quit, screen)
	}
	for _, part := range []string{"dism", "退出码 13", "数据无效"} {
		if !strings.Contains(screen, part) {
			t.Errorf("screen lacks %q:\n%s", part, screen)
		}
	}
}

// 下载不完整（服务器中途重启、网络中断）要在展开前拦下并说清楚，而不是让 dism 报「数据无效」。
func TestInstallScriptStopsOnTruncatedDownload(t *testing.T) {
	fx := healthyInstall()
	fx["wimGot"] = 0.5
	screen, quit := runInstallScript(t, fx)
	if quit != 1 {
		t.Fatalf("quit = %d, screen:\n%s", quit, screen)
	}
	for _, part := range []string{"没有下载完整", "服务器", "重试"} {
		if !strings.Contains(screen, part) {
			t.Errorf("screen lacks %q:\n%s", part, screen)
		}
	}
	if strings.Contains(screen, "[3/5]") {
		t.Errorf("must stop before applying the image:\n%s", screen)
	}
}

// 安装环境核对下载是否完整，靠的是服务端告诉它 install.wim 应有多大。
func TestWindowsPrepareTellsWinPEExpectedWIMSize(t *testing.T) {
	r, s := windowsPrepareFixture(t, "Windows", "", true)
	m, err := r.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		WIMSize int64 `json:"wimsize"`
	}
	if err := json.Unmarshal(m.Content["nd-install.json"], &cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(m.Files["install.wim"])
	if err != nil || cfg.WIMSize != info.Size() || cfg.WIMSize == 0 {
		t.Fatalf("wimsize = %d, file = %v %v", cfg.WIMSize, info, err)
	}
}

// 驱动带进去了却没装上，多半是驱动只支持更新的 Windows；要说清版本对不上，不能说成「没有驱动」。
func TestInstallScriptExplainsDriverThatDoesNotFitThisWindows(t *testing.T) {
	nic := []map[string]any{{"DeviceID": `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\FF`, "ConfigManagerErrorCode": 28}}
	vmxnet3 := func(minbuild int) map[string]any {
		return map[string]any{"drivers": []map[string]any{{"inf": `uploaded\Win10\vmxnet3.inf`, "version": "1.9.12.0", "source": "uploaded", "minbuild": minbuild}}}
	}
	for _, tc := range []struct {
		name   string
		fx     map[string]any
		want   []string
		reject []string
	}{
		{"版本不支持", map[string]any{"pnp": nic, "cfg": vmxnet3(20124), "build": "19041"},
			[]string{"不支持这个版本的 Windows", `PCI\VEN_15AD&DEV_07B0（VMware）`, "vmxnet3.inf", "1.9.12.0", "上传的驱动包", "内部版本 20124 及以上", "安装环境是 19041", "禁用", "E1000E", "本地磁盘没有被改动"},
			[]string{"没有这块网卡的驱动"}},
		{"版本对得上但没装上", map[string]any{"pnp": nic, "cfg": vmxnet3(0), "build": "19041"},
			[]string{"已带进安装环境，但没有装上", "错误代码 28", "vmxnet3.inf", "1.9.12.0", "安装日志", "E1000E", "本地磁盘没有被改动"},
			[]string{"没有这块网卡的驱动", "不支持这个版本"}},
		{"读不到安装环境版本", map[string]any{"pnp": nic, "cfg": vmxnet3(20124)},
			[]string{"已带进安装环境，但没有装上", "内部版本 20124 及以上"},
			[]string{"没有这块网卡的驱动", "不支持这个版本"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen, quit := runInstallScript(t, tc.fx)
			if quit != 1 {
				t.Fatalf("quit = %d, screen:\n%s", quit, screen)
			}
			for _, part := range tc.want {
				if !strings.Contains(screen, part) {
					t.Errorf("screen lacks %q:\n%s", part, screen)
				}
			}
			for _, part := range tc.reject {
				if strings.Contains(screen, part) {
					t.Errorf("screen must not say %q:\n%s", part, screen)
				}
			}
		})
	}
}

// drvload 的输出原来被丢掉，驱动为什么没装上无从查起；要进安装日志一起传回服务器。
func TestInstallLogKeepsDrvloadOutput(t *testing.T) {
	cmd, err := os.ReadFile(filepath.Join("scripts", "nd-install.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cmd), `nd-drvload.log`) || strings.Contains(string(cmd), `drvload "%%F" >nul`) {
		t.Fatalf("nd-install.cmd must keep drvload output:\n%s", cmd)
	}
	out := "X:\\nddrivers\\uploaded\\Win10\\vmxnet3.inf\r\nDrvLoad: Unable to load X:\\nddrivers\\uploaded\\Win10\\vmxnet3.inf (Error 0x800F0242).\r\n"
	_, _, log := runInstallScriptLog(t, map[string]any{"pnp": []map[string]any{{"DeviceID": `PCI\VEN_15AD&DEV_07B0\FF`, "ConfigManagerErrorCode": 28}}, "drvload": out})
	if !strings.Contains(log, "Error 0x800F0242") {
		t.Fatalf("install log lacks drvload output:\n%s", log)
	}
}
