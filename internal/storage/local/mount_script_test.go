package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/storage"
)

func TestMergeMountScriptFreshImage(t *testing.T) {
	mnt := t.TempDir()
	if err := mergeMountScript(mnt, []byte("mount\n")); err != nil {
		t.Fatal(err)
	}
	gp := filepath.Join(mnt, "Windows", "System32", "GroupPolicy")
	ini, err := os.ReadFile(filepath.Join(gp, "Machine", "Scripts", "scripts.ini"))
	if err != nil || !strings.Contains(string(ini), "0CmdLine=ndmount.cmd") {
		t.Fatalf("scripts.ini = %q err=%v", ini, err)
	}
	if gpt, err := os.ReadFile(filepath.Join(gp, "gpt.ini")); err != nil || !strings.Contains(string(gpt), "42B5FAAE") {
		t.Fatalf("gpt.ini = %q err=%v", gpt, err)
	}
	if ps1, err := os.ReadFile(filepath.Join(gp, "Machine", "Scripts", "Startup", "mount-disks.ps1")); err != nil || string(ps1) != "mount\r\n" {
		t.Fatalf("mount-disks.ps1 = %q err=%v", ps1, err)
	}
}

func TestMergeMountScriptPreservesExistingStartupEntry(t *testing.T) {
	// 合并只追加 ndmount，镜像原有启动脚本必须保留。
	mnt := t.TempDir()
	gp := filepath.Join(mnt, "Windows", "System32", "GroupPolicy")
	scripts := filepath.Join(gp, "Machine", "Scripts")
	if err := os.MkdirAll(filepath.Join(scripts, "Startup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "scripts.ini"), []byte("[Startup]\r\n0CmdLine=existing.cmd\r\n0Parameters=\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gp, "gpt.ini"), []byte(storage.GroupPolicyINI), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeMountScript(mnt, []byte("mount\n")); err != nil {
		t.Fatal(err)
	}
	ini, err := os.ReadFile(filepath.Join(scripts, "scripts.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ini), "0CmdLine=existing.cmd") || !strings.Contains(string(ini), "1CmdLine=ndmount.cmd") {
		t.Fatalf("scripts.ini = %q", ini)
	}
	// Version 应递增，让 gpsvc 重新处理 scripts.ini。
	if gpt, err := os.ReadFile(filepath.Join(gp, "gpt.ini")); err != nil || !strings.Contains(string(gpt), "Version=65540") {
		t.Fatalf("gpt.ini = %q err=%v", gpt, err)
	}

	// 再次合并时 ini 不变（ndmount 已注册）。
	if err := mergeMountScript(mnt, []byte("mount v2\n")); err != nil {
		t.Fatal(err)
	}
	ini2, err := os.ReadFile(filepath.Join(scripts, "scripts.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ini2) != string(ini) {
		t.Fatalf("scripts.ini changed on re-merge: %q", ini2)
	}
	// 脚本内容本身要刷新。
	if ps1, err := os.ReadFile(filepath.Join(scripts, "Startup", "mount-disks.ps1")); err != nil || string(ps1) != "mount v2\r\n" {
		t.Fatalf("mount-disks.ps1 = %q err=%v", ps1, err)
	}
}

// 上一次注入留下的适配注册被替换而不是叠加：ndadapt 只注册一次，脚本内容是这次的。
// 镜像自带的组策略不再整目录清掉（见 TestInjectAdaptationMergesIntoTheImagesOwnPolicy）。

// gpt.ini 只递增行首的 Version；gPCFunctionalityVersion 不是策略版本，改了它 gpsvc 可能拒读。
func TestBumpGptVersionLeavesFunctionalityVersionAlone(t *testing.T) {
	got := bumpGptVersion("[General]\r\ngPCFunctionalityVersion=2\r\nVersion=65538\r\n")
	if got != "[General]\r\ngPCFunctionalityVersion=2\r\nVersion=65539\r\n" {
		t.Fatalf("got %q", got)
	}
}

// 镜像版本哈希的是 storage 中对注入布局的描述；本用例确保实际写入与该描述一致，防止两者悄悄脱节。
func TestMergeMountScriptWritesTheLayoutStorageDescribes(t *testing.T) {
	mnt := t.TempDir()
	if err := mergeMountScript(mnt, []byte("mount\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(storage.StartupScriptDir(mnt))
	if err != nil {
		t.Fatalf("startup dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	want := []string{storage.MountBootstrapName, storage.MountLauncherName, storage.MountScriptName}
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("startup scripts = %v, want %v — the layout hashed into the version no longer matches what is written", names, want)
	}
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(b)
	}
	if got := read(filepath.Join(storage.StartupScriptDir(mnt), storage.MountLauncherName)); got != storage.MountLauncherBody {
		t.Fatalf("launcher = %q, want %q", got, storage.MountLauncherBody)
	}
	if got := read(filepath.Join(storage.StartupScriptDir(mnt), storage.MountBootstrapName)); got != "\ufeff"+string(toCRLF([]byte(storage.MountBootstrapBody))) {
		t.Fatalf("bootstrap = %q, want BOM + MountBootstrapBody", got)
	}
	if got := read(filepath.Join(storage.GroupPolicyDir(mnt), "gpt.ini")); got != storage.GroupPolicyINI {
		t.Fatalf("gpt.ini = %q, want %q", got, storage.GroupPolicyINI)
	}
	if got := read(filepath.Join(storage.MachineScriptsDir(mnt), "scripts.ini")); !strings.Contains(got, storage.MountLauncherName) {
		t.Fatalf("scripts.ini = %q, want it to register %q", got, storage.MountLauncherName)
	}
}

// 适配写入和结果回读都要排在同一台机器的开机供给、超管保存、数据盘发布之后，
// 否则会往正被改名或销毁的 SCLIENT 克隆上打快照、挂载。

// 回读用的临时克隆是客户机克隆一类，必须落在 run/ 下：放在 nd/ 会被复制和备份带走。

// writeImagePolicy 在挂载点里放一份镜像自带的本地组策略：自己的启动/关机脚本、注册表策略和别的 CSE。
func writeImagePolicy(t *testing.T, mnt string, scriptsINI []byte) {
	t.Helper()
	gp := storage.GroupPolicyDir(mnt)
	if err := os.MkdirAll(storage.StartupScriptDir(mnt), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(gp, "gpt.ini"):                                 []byte("[General]\r\ngPCMachineExtensionNames=[{827D319E-6EAC-11D2-A4EA-00C04F79F83A}{803E14A0-B4FB-11D0-A0D0-00A0C90F574B}]\r\nVersion=7\r\n"),
		filepath.Join(storage.MachineScriptsDir(mnt), "scripts.ini"): scriptsINI,
		filepath.Join(gp, "Machine", "Registry.pol"):                 []byte("PReg"),
		filepath.Join(storage.StartupScriptDir(mnt), "corp.cmd"):     []byte("corp"),
	}
	for path, content := range files {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readPolicy(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// 适配是往镜像里追加一次性启动项，不能删掉镜像自带的本地组策略；gpt.ini 版本要在原值上递增，
// 否则 gpsvc 认为策略没变、不重新处理。

// gpedit 写的 scripts.ini 是 UTF-16LE 带 BOM；按字节找 "[Startup]" 会落空，进而整份覆盖掉。

func utf16LE(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

func TestMergeMountScriptPreservesUTF16StartupAndShutdown(t *testing.T) {
	mnt := t.TempDir()
	writeImagePolicy(t, mnt, utf16LE("[Startup]\r\n0CmdLine=corp.cmd\r\n0Parameters=\r\n[Shutdown]\r\n0CmdLine=corp-stop.cmd\r\n0Parameters=\r\n"))
	path := filepath.Join(storage.MachineScriptsDir(mnt), "scripts.ini")
	if err := mergeMountScript(mnt, []byte("mount\n")); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content, wide := decodeINI(first)
	if !wide {
		t.Fatalf("customer scripts.ini must retain UTF-16LE encoding: % x", first[:min(len(first), 8)])
	}
	for _, want := range []string{"0CmdLine=corp.cmd", "[Shutdown]\r\n0CmdLine=corp-stop.cmd", "1CmdLine=" + storage.MountLauncherName} {
		if !strings.Contains(content, want) {
			t.Fatalf("customer startup/shutdown policy was replaced: missing %q in %q", want, content)
		}
	}
	if err := mergeMountScript(mnt, []byte("mount v2\n")); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated injection must preserve the original policy and avoid duplicate entries: %q err=%v", second, err)
	}
}

// 复用已有超管盘注入前要先摘掉系统盘 LUN，否则 LIO 占着 zvol 挂不上。

func TestMergeMountScriptPreservesShutdownOnlyPolicy(t *testing.T) {
	for _, wide := range []bool{false, true} {
		t.Run(fmt.Sprintf("utf16=%t", wide), func(t *testing.T) {
			mnt := t.TempDir()
			original := "[Shutdown]\r\n0CmdLine=corp-stop.cmd\r\n0Parameters=\r\n"
			writeImagePolicy(t, mnt, encodeINI(original, wide))
			if err := mergeMountScript(mnt, []byte("mount\n")); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(storage.MachineScriptsDir(mnt), "scripts.ini"))
			if err != nil {
				t.Fatal(err)
			}
			content, gotWide := decodeINI(raw)
			if gotWide != wide || !strings.Contains(content, original) || !strings.Contains(content, "[Startup]\r\n0CmdLine="+storage.MountLauncherName) {
				t.Fatalf("adding startup must preserve the existing shutdown policy and encoding: wide=%v content=%q", gotWide, content)
			}
		})
	}
}

// 改 Windows 系统分区时一并关掉蓝屏转储：iSCSI 启动写不出转储，Windows 10 开机准备转储时还会蓝屏。
// 导入烘焙、开机注入、ISO 安装保存都走这一步。
func TestPrepareWindowsRootDisablesCrashDump(t *testing.T) {
	mnt := t.TempDir()
	hive := filepath.Join(mnt, "WINDOWS", "system32", "config", "SYSTEM")
	if err := os.MkdirAll(filepath.Dir(hive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hive, []byte("regf"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got string
	old := disableCrashDump
	disableCrashDump = func(_ context.Context, path string) error { got = path; return nil }
	defer func() { disableCrashDump = old }()

	if err := prepareWindowsRoot(context.Background(), mnt, []byte("mount\n")); err != nil {
		t.Fatal(err)
	}
	if got != hive {
		t.Fatalf("改的注册表 = %q，应是系统分区里的 %q（大小写不定）", got, hive)
	}
	if _, ok := lookupPath(mnt, "Windows/System32/GroupPolicy/Machine/Scripts/scripts.ini"); !ok {
		t.Fatal("挂盘启动脚本也要照常写入")
	}

	// 找不到 SYSTEM 也只记警告，挂盘脚本照常写入（见下一个测试）。
	got = ""
	if err := prepareWindowsRoot(context.Background(), t.TempDir(), []byte("mount\n")); err != nil || got != "" {
		t.Fatalf("err = %v, 改了 %q", err, got)
	}
}

// 关不掉崩溃转储（比如原机没正常关机、注册表有未提交的改动）不能连累挂盘脚本：
// 判成烘焙失败的话，镜像会永远退回逐台注入。
func TestPrepareWindowsRootKeepsTheMountScriptWhenCrashDumpCannotBeDisabled(t *testing.T) {
	mnt := t.TempDir()
	hive := filepath.Join(mnt, "Windows", "System32", "config", "SYSTEM")
	if err := os.MkdirAll(filepath.Dir(hive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hive, []byte("regf"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := disableCrashDump
	disableCrashDump = func(context.Context, string) error {
		return fmt.Errorf("Windows注册表尚未提交，请先关闭客户机")
	}
	defer func() { disableCrashDump = old }()
	if err := prepareWindowsRoot(context.Background(), mnt, []byte("mount\n")); err != nil {
		t.Fatalf("关转储失败不该让烘焙失败：%v", err)
	}
	if _, ok := lookupPath(mnt, "Windows/System32/GroupPolicy/Machine/Scripts/Startup/"+storage.MountBootstrapName); !ok {
		t.Fatal("启动器没写进去")
	}
}

// 镜像里是 WINDOWS\system32 这种写法时，挂盘脚本必须写进已有的那棵目录，不能在旁边再建一棵
// Windows\System32——Windows 下两者同名，后建的那棵根本不会被读到。
func TestMergeMountScriptReusesExistingDirectoryCase(t *testing.T) {
	mnt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mnt, "WINDOWS", "system32", "grouppolicy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "WINDOWS", "system32", "grouppolicy", "GPT.INI"), []byte("[General]\r\nVersion=3\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeMountScript(mnt, []byte("mount\n")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(mnt)
	if len(entries) != 1 || entries[0].Name() != "WINDOWS" {
		t.Fatalf("根目录 = %v，不该另建 Windows", entries)
	}
	sys, _ := os.ReadDir(filepath.Join(mnt, "WINDOWS"))
	if len(sys) != 1 || sys[0].Name() != "system32" {
		t.Fatalf("WINDOWS 下 = %v，不该另建 System32", sys)
	}
	gp, _ := os.ReadDir(filepath.Join(mnt, "WINDOWS", "system32", "grouppolicy"))
	names := []string{}
	for _, e := range gp {
		names = append(names, e.Name())
	}
	if strings.Contains(strings.Join(names, ","), "gpt.ini") {
		t.Fatalf("已有 GPT.INI 时不该再写一份 gpt.ini：%v", names)
	}
	b, err := os.ReadFile(filepath.Join(mnt, "WINDOWS", "system32", "grouppolicy", "GPT.INI"))
	if err != nil || !strings.Contains(string(b), "Version=4") {
		t.Fatalf("GPT.INI 应被就地递增版本：%q %v", b, err)
	}
	if _, ok := lookupPath(mnt, "Windows/System32/GroupPolicy/Machine/Scripts/Startup/"+storage.MountLauncherName); !ok {
		t.Fatal("启动器没写进已有目录")
	}
}
