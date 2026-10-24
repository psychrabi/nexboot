package local

import (
	"context"
	"fmt"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/winreg"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const scriptsCSEPair = "[{" + storage.ScriptsCSEGUID + "}{40B6664F-4972-11D1-A7CA-0000F87571E3}]"

// editINI 按原编码读改写 ini；gpedit 写的是带 BOM 的 UTF-16LE，按字节处理会找不到段名而整份覆盖。
func editINI(path string, edit func(string) string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text, wide := decodeINI(b)
	return os.WriteFile(path, encodeINI(edit(text), wide), 0o644)
}

func decodeINI(b []byte) (string, bool) {
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE {
		return string(b), false
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 2; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(units)), true
}

func encodeINI(s string, wide bool) []byte {
	if !wide {
		return []byte(s)
	}
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

var (
	gptExtensionsRe = regexp.MustCompile(`(?mi)^(\s*gPCMachineExtensionNames=)([^\r\n]*)`)
	gptGroupRe      = regexp.MustCompile(`\[[^\]]*\]`)
	gptGeneralRe    = regexp.MustCompile(`(?mi)^\s*\[General\][^\r\n]*(\r?\n)?`)
)

// registerScriptsCSE 确保 gpt.ini 注册了 Scripts 扩展（保留镜像已有的其它扩展）并递增版本。
// 扩展组按 GUID 排序：顺序不对时 gpsvc 可能跳过其中的扩展。
func registerScriptsCSE(content string) string {
	if strings.TrimSpace(content) == "" {
		return storage.GroupPolicyINI
	}
	if !strings.Contains(strings.ToUpper(content), storage.ScriptsCSEGUID) {
		if loc := gptExtensionsRe.FindStringSubmatchIndex(content); loc != nil {
			groups := append(gptGroupRe.FindAllString(content[loc[4]:loc[5]], -1), scriptsCSEPair)
			slices.SortFunc(groups, func(a, b string) int { return strings.Compare(strings.ToUpper(a), strings.ToUpper(b)) })
			content = content[:loc[4]] + strings.Join(groups, "") + content[loc[5]:]
		} else {
			content = insertUnderGeneral(content, "gPCMachineExtensionNames="+scriptsCSEPair)
		}
	}
	if gptVersionRe.MatchString(content) {
		return bumpGptVersion(content)
	}
	return insertUnderGeneral(content, "Version=65539")
}

// insertUnderGeneral 把一行加到 [General] 段首；没有该段就新建在文件开头。
func insertUnderGeneral(content, line string) string {
	if loc := gptGeneralRe.FindStringIndex(content); loc != nil {
		head := content[:loc[1]]
		if !strings.HasSuffix(head, "\n") {
			head += "\r\n"
		}
		return head + line + "\r\n" + content[loc[1]:]
	}
	return "[General]\r\n" + line + "\r\n" + content
}

// toCRLF 统一为 Windows 换行，否则注入的脚本运行会出错。
func toCRLF(b []byte) []byte {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

// injectMountScript 把盘符挂载启动脚本合并进系统克隆的本地组策略启动项，每次开机执行。
// 只合并不重置组策略目录：已放入的 adapt.ps1 和镜像自带项都要保留。
func (a *Agent) injectMountScript(ctx context.Context, dev string, script []byte) error {
	return a.mergeMountScriptInto(ctx, dev, script, clonePartitionWait)
}

// bakeMountScript 在导入时对新镜像做同样的合并，只是等待更久：没人在等，
// 而这里放弃意味着之后每次开机都要再注入。
func (a *Agent) bakeMountScript(ctx context.Context, dev string, script []byte) error {
	return a.mergeMountScriptInto(ctx, dev, script, importPartitionWait)
}

func (a *Agent) mergeMountScriptInto(ctx context.Context, dev string, script []byte, wait time.Duration) error {
	if a.injectMountScriptFn != nil {
		return a.injectMountScriptFn(ctx, dev, script)
	}
	r := a.runner
	if r == nil {
		r = execRunner{}
	}
	return withWindowsPartition(ctx, r, dev, volRW, wait, func(mnt string) error {
		return prepareWindowsRoot(ctx, mnt, script)
	})
}

// disableCrashDump 可替换，单元测试不必依赖 hivexregedit 和真实注册表。
var disableCrashDump = winreg.DisableCrashDump

// prepareWindowsRoot 是导入烘焙、开机注入、ISO 安装保存时对 Windows 系统分区做的全部修改：
// 写入挂盘启动脚本，并关掉蓝屏转储（iSCSI 启动写不出转储，Windows 10 开机准备转储时会蓝屏）。
// 关不掉蓝屏转储只记警告：判成烘焙失败的话，镜像会永远退回逐台注入，而转储照样关不掉。
func prepareWindowsRoot(ctx context.Context, mnt string, script []byte) error {
	if err := mergeMountScript(mnt, script); err != nil {
		return err
	}
	hive, ok := lookupPath(mnt, "Windows/System32/config/SYSTEM")
	if !ok {
		slog.Warn("系统分区里找不到 Windows\\System32\\config\\SYSTEM，没能关闭蓝屏转储", "mount", mnt)
		return nil
	}
	if err := disableCrashDump(ctx, hive); err != nil {
		slog.Warn("没能关闭蓝屏转储；Windows 10 无盘启动会蓝屏 PAGE_FAULT_IN_NONPAGED_AREA。原机没正常关机时注册表有未提交的改动，请正常关机后重新导入", "error", err)
	}
	return nil
}

func mergeMountScript(mnt string, script []byte) error {
	// 三个目录都按已有写法复用（见 ensureDirFold），否则 WINDOWS\system32 的镜像会多出一棵 Windows 看不见的目录。
	startup, err := ensureDirFold(mnt, storage.StartupScriptDir(""))
	if err != nil {
		return err
	}
	scripts, err := ensureDirFold(mnt, storage.MachineScriptsDir(""))
	if err != nil {
		return err
	}
	policy, err := ensureDirFold(mnt, storage.GroupPolicyDir(""))
	if err != nil {
		return err
	}
	// 脚本内容总是覆盖（服务端升级后重启即生效）；ini 注册幂等合并。
	if err := os.WriteFile(fileFold(startup, storage.MountScriptName), toCRLF(script), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(fileFold(startup, storage.MountLauncherName), []byte(storage.MountLauncherBody), 0o644); err != nil {
		return err
	}
	// 启动器带 BOM：PowerShell 5.1 才按 UTF-8 读；内容是 ASCII，带不带都一样能跑。
	if err := os.WriteFile(fileFold(startup, storage.MountBootstrapName), append([]byte("\ufeff"), toCRLF([]byte(storage.MountBootstrapBody))...), 0o644); err != nil {
		return err
	}
	if err := mergeScriptsINI(fileFold(scripts, "scripts.ini")); err != nil {
		return err
	}
	// gpt.ini 必须含 Scripts CSE，gpsvc 才处理本地脚本；改了 scripts.ini 要递增 Version 强制重新处理。
	return editINI(fileFold(policy, "gpt.ini"), registerScriptsCSE)
}

var (
	scriptsCmdLineRe = regexp.MustCompile(`(?m)^\s*(\d+)CmdLine=`)
	gptVersionRe     = regexp.MustCompile(`(?mi)^(\s*Version=)(\d+)`)
)

// mergeScriptsINI 在保留已有项的前提下注册 ndmount.cmd 启动项。新项紧跟 [Startup] 标题插入
// （执行顺序看序号不看位置），避免被后面的 [Shutdown] 段吞掉。
func mergeScriptsINI(path string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content, wide := decodeINI(b)
	if strings.Contains(content, storage.MountLauncherName) {
		return nil
	}
	if !strings.Contains(content, "[Startup]") {
		return os.WriteFile(path, encodeINI("[Startup]\r\n0CmdLine="+storage.MountLauncherName+"\r\n0Parameters=\r\n"+content, wide), 0o644)
	}
	next := 0
	for _, m := range scriptsCmdLineRe.FindAllStringSubmatch(content, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= next {
			next = n + 1
		}
	}
	entry := fmt.Sprintf("[Startup]\r\n%dCmdLine=%s\r\n%dParameters=", next, storage.MountLauncherName, next)
	content = strings.Replace(content, "[Startup]", entry, 1)
	return os.WriteFile(path, encodeINI(content, wide), 0o644)
}

// bumpGptVersion 递增 gpt.ini 的 Version（低位字是计算机策略版本），让 gpsvc 重新处理。
// 按行首匹配：gPCFunctionalityVersion 不能跟着变。
func bumpGptVersion(content string) string {
	return gptVersionRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := gptVersionRe.FindStringSubmatch(m)
		n, err := strconv.Atoi(sub[2])
		if err != nil {
			return m
		}
		return sub[1] + strconv.Itoa(n+1)
	})
}
