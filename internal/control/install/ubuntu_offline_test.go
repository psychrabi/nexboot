package install

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/tasks"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage/iscsi"
	"github.com/tianwei/diskless/internal/store"
)

// ubuntuPrepareFixture 用假的 7z/openssl 搭一台没有外网、也没装 apt 工具的服务器。
func ubuntuPrepareFixture(t *testing.T) (Runtime, domain.InstallSession, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "network-calls")
	stubs := map[string]string{
		"7z":      "#!/bin/sh\ncase \"$1\" in\nl) printf '%s\\n' '----------' 'Path = casper/vmlinuz' '' 'Path = casper/initrd';;\nx) printf '%s' \"$5\";;\n*) exit 2;;\nesac\n",
		"openssl": "#!/bin/sh\nprintf '$6$fixture-hash\\n'\n",
		"apt-get": "#!/bin/sh\necho apt-get >> '" + calls + "'\nexit 100\n",
		"curl":    "#!/bin/sh\necho curl >> '" + calls + "'\nexit 6\n",
		"wget":    "#!/bin/sh\necho wget >> '" + calls + "'\nexit 4\n",
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	iso := filepath.Join(root, "ubuntu.iso")
	data := []byte("fixture Ubuntu ISO")
	if err := os.WriteFile(iso, data, 0600); err != nil {
		t.Fatal(err)
	}
	r := Runtime{FilesDir: func(context.Context) (string, error) { return root, nil }, ReportBase: "http://192.168.50.1:8080"}
	s := domain.InstallSession{ID: "install-ubuntu-offline", Token: "test-token", OSType: domain.OSTypeLinux, Distribution: "ubuntu-server",
		SourcePath: iso, SourceSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Username: "tester", Password: "pw-offline-0001", MAC: "001122334455", Attempt: 1}
	return r, s, calls
}

// 纯局域网里服务器上不了外网：准备资源只能用 ISO 自带的东西。
func TestUbuntuPrepareUsesOnlyTheISO(t *testing.T) {
	r, s, calls := ubuntuPrepareFixture(t)
	m, err := r.Prepare(context.Background(), s)
	if err != nil {
		t.Fatalf("Ubuntu preparation must not need the Internet: %v", err)
	}
	if b, err := os.ReadFile(calls); err == nil {
		t.Fatalf("preparation reached for the network: %s", b)
	}
	for _, name := range []string{"vmlinuz", "initrd"} {
		if b, err := os.ReadFile(m.Files[name]); err != nil || string(b) != "casper/"+name {
			t.Errorf("%s = %q %v", name, b, err)
		}
	}
	for name := range m.Files {
		if strings.HasPrefix(name, "repo/") {
			t.Errorf("installer still served a downloaded package: %s", name)
		}
	}
}

// runEarlyScript 在临时目录里跑安装前脚本；tools 是安装环境里有的命令，调用都记进 calls，auto 是跑完后的应答文件。
func runEarlyScript(t *testing.T, tools ...string) (log, calls, auto string, err error) {
	t.Helper()
	r, s, _ := ubuntuPrepareFixture(t)
	m, err := r.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "calls")
	for _, tool := range append(tools, "apt-get", "dpkg") {
		name, _, _ := strings.Cut(tool, ":")
		script := "#!/bin/sh\necho \"" + name + " $*\" >> '" + record + "'\n"
		if name == "apt-get" || name == "dpkg" {
			script += "exit 100\n"
		}
		if tool == "python3" {
			// 安装盘身份核对：唯一匹配序列号的 iSCSI 盘；"python3:nodisk" 则什么都认不出。
			script += "[ \"$2\" = \"" + iscsi.UnitSerial(iscsi.TargetForMAC(s.MAC), 0) + "\" ] && echo /dev/sdb\nexit 0\n"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// 脚本里写死的系统路径换到临时目录，测试不碰本机。
	autoPath := filepath.Join(dir, "autoinstall.yaml")
	if err := os.WriteFile(autoPath, []byte("storage:\n  config:\n  - id: nd-system\n    path: "+installDiskPlaceholder+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer("/tmp/", dir+"/", "/usr/sbin/", dir+"/", "/autoinstall.yaml", autoPath).Replace(string(m.Content["early.sh"]))
	path := filepath.Join(dir, "early.sh")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", path)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	err = cmd.Run()
	b, _ := os.ReadFile(filepath.Join(dir, "nd-early.log"))
	c, _ := os.ReadFile(record)
	a, _ := os.ReadFile(autoPath)
	return string(b), string(c), string(a), err
}

// Server 版安装环境自带 iscsiadm 和 curl：直接连盘，不装任何包。
func TestUbuntuEarlyScriptInstallsNothing(t *testing.T) {
	log, calls, auto, err := runEarlyScript(t, "python3", "modprobe", "iscsiadm", "udevadm", "curl")
	if err != nil {
		t.Fatalf("early script failed: %v\n%s\ncalls:\n%s", err, log, calls)
	}
	if strings.Contains(calls, "apt-get") || strings.Contains(calls, "dpkg") {
		t.Fatalf("early script installed packages:\n%s", calls)
	}
	if !strings.Contains(calls, `"phase":"installing"`) {
		t.Fatalf("early script never reported the confirmed disk:\n%s", calls)
	}
	if !strings.Contains(auto, "path: /dev/sdb\n") {
		t.Fatalf("installer config still has no real system disk:\n%q\nlog:%s\ncalls:%s", auto, log, calls)
	}
}

// 认盘没给出设备：不能拿空路径替换占位符接着装。
func TestUbuntuEarlyScriptStopsWhenNoDiskIsIdentified(t *testing.T) {
	_, calls, auto, err := runEarlyScript(t, "python3:nodisk", "modprobe", "iscsiadm", "udevadm", "curl")
	if err == nil || strings.Contains(calls, `"phase":"installing"`) || !strings.Contains(auto, installDiskPlaceholder) {
		t.Fatalf("early script went on without a disk: err=%v\nconfig:\n%s\ncalls:\n%s", err, auto, calls)
	}
}

// 安装环境没有 iSCSI 工具就连不上安装盘：动盘之前停下，并说清楚缺什么。
func TestUbuntuEarlyScriptStopsWithoutISCSITools(t *testing.T) {
	log, calls, _, err := runEarlyScript(t, "python3", "modprobe", "udevadm", "curl")
	if err == nil {
		t.Fatalf("early script continued without iscsiadm:\n%s", calls)
	}
	if strings.Contains(calls, "apt-get") || strings.Contains(calls, "dpkg") || strings.Contains(calls, `"phase":"installing"`) {
		t.Fatalf("early script went on without iscsiadm:\n%s", calls)
	}
	if !strings.Contains(log, "open-iscsi") {
		t.Fatalf("install log does not say what is missing:\n%s", log)
	}
}

// 新系统的 open-iscsi、curl 来自 ISO 的系统层：收尾时只做无盘配置，不往系统里装包。
func TestUbuntuFinishScriptInstallsNothingIntoTarget(t *testing.T) {
	r, s, _ := ubuntuPrepareFixture(t)
	m, err := r.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	for _, d := range []string{"usr/sbin", "etc/netplan", "etc/cloud/cloud.cfg.d", "etc/apt/sources.list.d", "etc/default/grub.d", "boot/efi/EFI/ubuntu"} {
		if err := os.MkdirAll(filepath.Join(target, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(target, "boot/efi/EFI/BOOT"), 0700); err != nil {
		t.Fatal(err)
	}
	// 安装程序走可移动介质路径时会放 shim 的 fallback；它会建 NVRAM 启动项然后重启。
	for _, f := range []string{"boot/efi/EFI/ubuntu/grubx64.efi", "boot/efi/EFI/ubuntu/grub.cfg", "boot/efi/EFI/BOOT/fbx64.efi"} {
		if err := os.WriteFile(filepath.Join(target, f), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "calls")
	stubs := map[string]string{
		"curtin": "#!/bin/sh\necho \"curtin $*\" >> '" + record + "'\n",
		"curl":   "#!/bin/sh\necho \"curl $*\" >> '" + record + "'\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && : > \"$2\"; shift; done\n",
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer("/target", target, "/tmp/", dir+"/").Replace(string(m.Content["finish.sh"]))
	path := filepath.Join(dir, "finish.sh")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", path)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	runErr := cmd.Run()
	calls, _ := os.ReadFile(record)
	log, _ := os.ReadFile(filepath.Join(dir, "nd-install.log"))
	if runErr != nil {
		t.Fatalf("finish script failed: %v\n%s\ncalls:\n%s", runErr, log, calls)
	}
	if strings.Contains(string(calls), "apt-get") || strings.Contains(string(calls), "dpkg") {
		t.Fatalf("finish script installed packages into the image:\n%s", calls)
	}
	if !strings.Contains(string(calls), "update-initramfs") || !strings.Contains(string(calls), `"phase":"initializing"`) {
		t.Fatalf("finish script skipped diskless preparation:\n%s", calls)
	}
	// 无盘每次都从 iPXE 挂的盘启动：留着 fallback，每次开机都会被它重启一次。
	if _, err := os.Stat(filepath.Join(target, "boot/efi/EFI/BOOT/fbx64.efi")); !os.IsNotExist(err) {
		t.Fatalf("shim fallback left in the image: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "boot/efi/EFI/BOOT/BOOTX64.EFI")); err != nil {
		t.Fatalf("removable boot path missing: %v", err)
	}
}

// inspectUbuntuFixture 用假 7z 把 files 当成 ISO 内容，返回识别结果。
func inspectUbuntuFixture(t *testing.T, files map[string]string) (Metadata, error) {
	t.Helper()
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	for name, data := range files {
		p := filepath.Join(tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\nl) echo ----------; cd '" + tree + "' && find . -type f | sed 's#^\\./#Path = #';;\nx) exec cat '" + tree + "'/\"$5\";;\n*) exit 2;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "7z"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	imports := filepath.Join(dir, "imports")
	if err := os.Mkdir(imports, 0700); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(imports, "ubuntu.iso")
	if err := os.WriteFile(iso, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return Inspector{ImportDir: func(context.Context) (string, error) { return imports, nil }}.Inspect(context.Background(), iso)
}

// 摘自 ubuntu-24.04.1-live-server-amd64.iso：open-iscsi、curl 在最底下的 minimal 层。
var ubuntuServerISO = map[string]string{
	".disk/info":     `Ubuntu-Server 24.04.1 LTS "Noble Numbat" - Release amd64 (20240827)`,
	"casper/vmlinuz": "kernel", "casper/initrd": "initrd",
	"casper/install-sources.yaml":                         "- description:\n    en: This version has been customized.\n  id: ubuntu-server-minimal\n  locale_support: none\n  path: ubuntu-server-minimal.squashfs\n  type: fsimage\n  variant: server\n- default: true\n  description:\n    en: The default install.\n  id: ubuntu-server\n  path: ubuntu-server-minimal.ubuntu-server.squashfs\n  type: fsimage-layered\n  variant: server\n",
	"casper/ubuntu-server-minimal.manifest":               "--- /build/livecd.ubuntu-server..manifest.full\t1970-01-01\n+++ /build/livecd.ubuntu-server.ubuntu-server-minimal.manifest.full\t2024-08-27\n+adduser\t3.137ubuntu1\n+curl\t8.5.0-2ubuntu10.3\n+open-iscsi\t2.1.9-3ubuntu5.1\n+python3\t3.12.3-0ubuntu2\n",
	"casper/ubuntu-server-minimal.ubuntu-server.manifest": "--- a\n+++ b\n+byobu\t6.11-0ubuntu1\n",
}

func TestUbuntuServerISOCarriesWhatDisklessBootNeeds(t *testing.T) {
	meta, err := inspectUbuntuFixture(t, ubuntuServerISO)
	if err != nil || meta.Distribution != "ubuntu-server" {
		t.Fatalf("server ISO = %+v %v", meta, err)
	}
}

// 桌面版默认系统层里没有 open-iscsi：装完也起不来，识别时就拦下并说清楚。
func TestUbuntuISOWithoutISCSIIsRejectedAtInspection(t *testing.T) {
	desktop := map[string]string{
		".disk/info":     `Ubuntu 24.04.1 LTS "Noble Numbat" - Release amd64 (20240827)`,
		"casper/vmlinuz": "kernel", "casper/initrd": "initrd",
		"casper/install-sources.yaml":      "- default: false\n  id: ubuntu-desktop-minimal\n  path: minimal.squashfs\n  type: fsimage\n- default: true\n  id: ubuntu-desktop\n  path: minimal.standard.squashfs\n  type: fsimage-layered\n",
		"casper/minimal.manifest":          "adduser\t3.137ubuntu1\ncurl\t8.5.0-2ubuntu10.3\n",
		"casper/minimal.standard.manifest": "firefox\t1:1snap1\n",
		// 安装环境层带了 open-iscsi 也不算：要看装出来的系统。
		"casper/minimal.standard.live.manifest": "open-iscsi\t2.1.9-3ubuntu5.1\n",
	}
	_, err := inspectUbuntuFixture(t, desktop)
	if err == nil || !strings.Contains(err.Error(), "open-iscsi") || !strings.Contains(err.Error(), "Ubuntu Server") || strings.Contains(err.Error(), "curl") {
		t.Fatalf("desktop ISO without open-iscsi = %v", err)
	}
}

// 老版本 iPXE 在 UEFI 下不把 initrd 交给内核，内核只能按命令行里的 initrd= 去找，缺了就内核恐慌找不到根设备。
func TestUbuntuInstallerKernelIsToldWhereItsInitrdIs(t *testing.T) {
	r := Runtime{ReportBase: "http://192.168.50.1:8080"}
	s := domain.InstallSession{Token: "tok", MAC: "001122334455", OSType: domain.OSTypeLinux, Portal: "192.168.50.1", Target: "iqn.test:install"}
	script, err := r.Render(context.Background(), s, MediaFiles{})
	if err != nil {
		t.Fatal(err)
	}
	var kernel, initrd string
	for _, line := range strings.Split(script, "\n") {
		switch {
		case strings.HasPrefix(line, "kernel "):
			kernel = line
		case strings.HasPrefix(line, "initrd ") && strings.Contains(line, "/initrd "):
			initrd = line
		}
	}
	if !strings.Contains(kernel, "/install/tok/vmlinuz initrd=initrd ") || !strings.HasPrefix(initrd, "initrd http://192.168.50.1:8080/install/tok/initrd ") {
		t.Fatalf("kernel cannot find its initrd:\n%s", script)
	}
}

// Ubuntu 安装期间把 ISO 作为只读光驱挂给客户机；装完从安装盘首次启动时拿掉，Windows 不挂。
func TestInstallerMediaIsAttachedOnlyWhileUbuntuInstalls(t *testing.T) {
	for _, os := range []domain.OSType{domain.OSTypeLinux, domain.OSTypeWindows} {
		t.Run(string(os), func(t *testing.T) {
			tasks.SetNode("192.168.50.1")
			t.Cleanup(func() { tasks.SetNode("") })
			ctx := context.Background()
			st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
				t.Fatal(err)
			}
			if err := st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 1 << 40}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			iso := filepath.Join(dir, "media.iso")
			disk := &diskFake{}
			svc := Service{SaveGrace: -1, Store: st, Storage: disk, Host: disk, ReportBase: "http://192.168.50.1:8080", InspectSource: func(context.Context, string) (Metadata, error) {
				return Metadata{Path: "/iso/x.iso", Fingerprint: "fp", OSType: os, Distribution: "ubuntu-server", Version: "24.04", Architecture: "amd64", Editions: []Edition{{Index: 1, Name: "Pro"}}}, nil
			}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
				return MediaFiles{Dir: dir, Files: map[string]string{"media.iso": iso}}, nil
			}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
			res, err := svc.Start(ctx, StartRequest{SourcePath: "/iso/x.iso", SourceFingerprint: "fp", EditionIndex: 1, Name: "X", PoolID: "pool", SizeGiB: 64, MAC: "00:11:22:33:44:55", IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "Password123!"})
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if os == domain.OSTypeLinux {
				want = iso
			}
			if len(disk.prepared) != 1 || disk.prepared[0].MediaPath != want {
				t.Fatalf("install disk requests = %+v, want media %q", disk.prepared, want)
			}
			s, _ := st.InstallSessions().Get(ctx, res.SessionID)
			if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "installing"}); err != nil {
				t.Fatal(err)
			}
			if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "initializing"}); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := svc.BootScript(ctx, s.MAC); err != nil || !ok {
				t.Fatalf("first boot = %v %v", ok, err)
			}
			if last := disk.prepared[len(disk.prepared)-1]; len(disk.prepared) != 2 || last.MediaPath != "" {
				t.Fatalf("first boot from the installed disk still carries the installer: %+v", disk.prepared)
			}
			// 是这台客户机自己在开机：上一轮 iPXE 的会话还没超时也不能拒绝它。
			if !disk.prepared[1].Booting || disk.prepared[0].Booting {
				t.Fatalf("boot-time re-export must replace the client's own stale session: %+v", disk.prepared)
			}
		})
	}
}

// ISO 是同一 target 的只读 LUN 1：仍只有一条 sanhook；内核另带附加 initrd 去登录它，不再从网址整张下载。
func TestUbuntuInstallerBootsFromISOOverISCSI(t *testing.T) {
	r := Runtime{ReportBase: "http://192.168.50.1:8080"}
	s := domain.InstallSession{Token: "tok", MAC: "001122334455", OSType: domain.OSTypeLinux, Portal: "192.168.50.1", Target: "iqn.test:install"}
	script, err := r.Render(context.Background(), s, MediaFiles{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(script, "sanhook ") != 1 || strings.Contains(script, "netboot=url") || strings.Contains(script, "media.iso") {
		t.Fatalf("installer must read the ISO from the iSCSI disk:\n%s", script)
	}
	for _, want := range []string{" initrd=initrd initrd=nd-iscsi.cpio ", " boot=casper ", " fsck.mode=skip ", "\ninitrd http://192.168.50.1:8080/install/tok/nd-iscsi.cpio "} {
		if !strings.Contains(script, want) {
			t.Errorf("boot script lacks %q:\n%s", want, script)
		}
	}
}

// readNewc 解开 newc 格式的 cpio，返回 名字→(权限, 内容)。
func readNewc(t *testing.T, b []byte) map[string]struct {
	mode uint64
	data string
} {
	t.Helper()
	out := map[string]struct {
		mode uint64
		data string
	}{}
	pad := func(n int) int { return (n + 3) &^ 3 }
	for off := 0; ; {
		if len(b) < off+110 || string(b[off:off+6]) != "070701" {
			t.Fatalf("bad newc header at %d", off)
		}
		field := func(i int) uint64 {
			v, err := strconv.ParseUint(string(b[off+6+8*i:off+14+8*i]), 16, 32)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		mode, size, nameSize := field(1), int(field(6)), int(field(11))
		name := string(b[off+110 : off+110+nameSize-1])
		data := pad(off + 110 + nameSize)
		if name == "TRAILER!!!" {
			return out
		}
		out[name] = struct {
			mode uint64
			data string
		}{mode, string(b[data : data+size])}
		off = pad(data + size)
	}
}

// 附加 initrd 替换 casper-premount 的 ORDER：先按 iBFT 登录，再照编号顺序跑 ISO 原有的脚本。
func TestCasperOverlayLogsInBeforeTheISOsOwnPremountScripts(t *testing.T) {
	r, s, _ := ubuntuPrepareFixture(t)
	m, err := r.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	files := readNewc(t, m.Content["nd-iscsi.cpio"])
	order, script := files["scripts/casper-premount/ORDER"], files["scripts/casper-premount/05nexboot-iscsi"]
	if order.data == "" || script.mode&0o777 != 0o755 {
		t.Fatalf("overlay = %v", files)
	}
	dir := t.TempDir()
	premount := filepath.Join(dir, "scripts", "casper-premount")
	bin := filepath.Join(dir, "bin")
	record := filepath.Join(dir, "calls")
	for _, d := range []string{premount, bin} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fix := strings.NewReplacer("/scripts/", dir+"/scripts/", "/conf/", dir+"/conf/", "/dev/console", record)
	write := func(path, data string) {
		if err := os.WriteFile(path, []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(premount, "ORDER"), fix.Replace(order.data))
	write(filepath.Join(premount, "05nexboot-iscsi"), fix.Replace(script.data))
	// ISO 原有的两个 premount 脚本（Ubuntu 24.04 Server）。
	for _, name := range []string{"10driver_updates", "20iso_scan"} {
		write(filepath.Join(premount, name), "#!/bin/sh\necho \""+name+" $*\" >> '"+record+"'\n")
	}
	for _, name := range []string{"modprobe", "iscsistart", "udevadm"} {
		write(filepath.Join(bin, name), "#!/bin/sh\necho \""+name+" $*\" >> '"+record+"'\n")
	}
	cmd := exec.Command("/bin/sh", "-c", ". '"+filepath.Join(premount, "ORDER")+"'", "sh", "arg")
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ORDER failed: %v\n%s", err, out)
	}
	calls, _ := os.ReadFile(record)
	want := "modprobe iscsi_tcp\nmodprobe iscsi_ibft\niscsistart -b\nudevadm settle --timeout=30\n10driver_updates arg\n20iso_scan arg\n"
	if string(calls) != want {
		t.Fatalf("premount order:\n%s\nwant:\n%s", calls, want)
	}
}

// 同一个文件（大小、修改时间不变）再识别不重新读；文件一变就重新识别，指纹跟着变。
// 识别不再整张算校验值（大 ISO 要几分钟），由创建安装任务时复制 ISO 顺带算。
func TestInspectReusesResultForTheSameFile(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "7z-calls")
	tree := filepath.Join(dir, "tree")
	for name, data := range ubuntuServerISO {
		p := filepath.Join(tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$1\" >> '" + calls + "'\ncase \"$1\" in\nl) echo ----------; cd '" + tree + "' && find . -type f | sed 's#^\\./#Path = #';;\nx) exec cat '" + tree + "'/\"$5\";;\n*) exit 2;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "7z"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	imports := filepath.Join(dir, "imports")
	if err := os.Mkdir(imports, 0700); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(imports, "server.iso")
	if err := os.WriteFile(iso, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	in := Inspector{ImportDir: func(context.Context) (string, error) { return imports, nil }}
	first, err := in.Inspect(context.Background(), iso)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == "" {
		t.Fatalf("inspection must return a file fingerprint: %+v", first)
	}
	before, _ := os.ReadFile(calls)
	again, err := in.Inspect(context.Background(), iso)
	if err != nil || again.Fingerprint != first.Fingerprint {
		t.Fatalf("second inspection = %+v %v", again, err)
	}
	if after, _ := os.ReadFile(calls); string(after) != string(before) {
		t.Fatalf("unchanged file was read again: %q -> %q", before, after)
	}
	// 换了内容（修改时间随之变化）：必须重新识别，校验值跟着变。
	if err := os.WriteFile(iso, []byte("second file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(iso, time.Now().Add(time.Minute), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	changed, err := in.Inspect(context.Background(), iso)
	if err != nil || changed.Fingerprint == first.Fingerprint {
		t.Fatalf("changed file kept the old result: %+v %v", changed, err)
	}
}
