package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"

	"github.com/tianwei/diskless/internal/boot/tftpassets"
	"github.com/tianwei/diskless/internal/control/adapt"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage/iscsi"
)

//go:embed scripts/*
var installScripts embed.FS

type Runtime struct {
	FilesDir   func(context.Context) (string, error)
	ReportBase string
	APIPort    string
	CHAPSecret string
	PE         pe.SessionService
}

func (r Runtime) base(s domain.InstallSession) string {
	return strings.TrimRight(r.ReportBase, "/") + "/install/" + s.Token + "/"
}

func (r Runtime) RedactSecrets(s domain.InstallSession, b []byte) []byte {
	secrets := []string{s.Token, s.Password}
	if iscsi.CHAPEnabled(r.CHAPSecret) {
		c := iscsi.CHAPCredentials(r.CHAPSecret, s.MAC)
		secrets = append(secrets, c.Password)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, encoded := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			b = bytes.ReplaceAll(b, []byte(encoded), []byte("[redacted]"))
		}
	}
	return b
}

// Prepare pins the ISO before use; subsequent file requests only read the prepared manifest.
func (r Runtime) Prepare(ctx context.Context, s domain.InstallSession) (MediaFiles, error) {
	if r.FilesDir == nil {
		return MediaFiles{}, fmt.Errorf("请先创建存储池")
	}
	root, err := r.FilesDir(ctx)
	if err != nil {
		return MediaFiles{}, err
	}
	if !safeMediaID(s.ID) {
		return MediaFiles{}, fmt.Errorf("安装会话标识无效")
	}
	dir := filepath.Join(root, "installations", s.ID)
	manifest := filepath.Join(dir, "media.json")
	m := MediaFiles{Dir: dir, Files: map[string]string{}, Content: map[string][]byte{}}
	if b, err := os.ReadFile(manifest); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return m, fmt.Errorf("安装资源清单损坏，请重新创建安装任务")
		}
	} else {
		if !os.IsNotExist(err) {
			return m, err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return m, err
		}
		source, err := resolveSourcePath(ctx, r.FilesDir, s.SourcePath)
		if err != nil {
			return m, err
		}
		info, err := os.Stat(source)
		if err != nil {
			return m, err
		}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(dir, &stat); err != nil {
			return m, err
		}
		need := info.Size()*3 + 2<<30
		if int64(stat.Bavail)*int64(stat.Bsize) < need {
			return m, fmt.Errorf("安装资源目录空间不足，至少需要 %d GiB；请清理存储池", (need+(1<<30)-1)>>30)
		}
		iso := filepath.Join(dir, "media.iso")
		sum, err := pinISO(ctx, source, iso, s.SourceSHA256)
		if err != nil {
			return m, err
		}
		m.SHA256 = sum
		m.Files["media.iso"] = iso
		entries, err := ArchiveEntries(ctx, iso)
		if err != nil {
			return m, err
		}
		find := func(name string) string {
			for _, e := range entries {
				if strings.EqualFold(e, name) {
					return e
				}
			}
			return ""
		}
		extract := func(entry, name string) error {
			if entry == "" {
				return fmt.Errorf("安装文件缺少 %s，请使用完整的官方 ISO", name)
			}
			dest := filepath.Join(dir, name)
			if err := extractMember(ctx, iso, entry, dest); err != nil {
				return err
			}
			m.Files[name] = dest
			return nil
		}
		if s.OSType == domain.OSTypeWindows {
			entry := find("sources/install.wim")
			if entry == "" {
				entry = find("sources/install.esd")
			}
			if err := extract(entry, "install.wim"); err != nil {
				return m, err
			}
			// WinRE contains WSH/WMI needed by the existing native adapter preparation.
			for _, file := range []struct{ name, path, media string }{
				{"boot.wim", "Windows/System32/Recovery/winre.wim", ""},
				{"BCD", "Windows/Boot/DVD/EFI/BCD", "efi/microsoft/boot/bcd"},
				{"boot.sdi", "Windows/Boot/DVD/PCAT/boot.sdi", "boot/boot.sdi"},
				{"curl.exe", "Windows/System32/curl.exe", ""},
			} {
				p := filepath.Join(dir, file.name)
				err := extractWIMMember(ctx, m.Files["install.wim"], s.EditionIndex, file.path, p)
				// Server Core 版不带 Windows/Boot/DVD：启动文件与版本无关，退回用 ISO 自带的。
				var missing wimMemberMissing
				if errors.As(err, &missing) && file.media != "" && find(file.media) != "" {
					err = extractMember(ctx, iso, find(file.media), p)
				}
				if err != nil {
					return m, err
				}
				m.Files[file.name] = p
			}
		} else {
			kernel, initrd := "casper/vmlinuz", "casper/initrd"
			if isAnaconda(s.Distribution) {
				kernel, initrd = "images/pxeboot/vmlinuz", "images/pxeboot/initrd.img"
			}
			if err := extract(find(kernel), "vmlinuz"); err != nil {
				return m, err
			}
			if err := extract(find(initrd), "initrd"); err != nil {
				return m, err
			}
			cmd := exec.CommandContext(ctx, "openssl", "passwd", "-6", "-stdin")
			cmd.Stdin = strings.NewReader(s.Password + "\n")
			pw, err := cmd.Output()
			if err != nil {
				return m, fmt.Errorf("无法生成 Linux 安装账户，请确认服务器已安装 openssl")
			}
			if err := os.WriteFile(filepath.Join(dir, "password.hash"), bytes.TrimSpace(pw), 0600); err != nil {
				return m, err
			}
		}
		b, _ := json.Marshal(m)
		if err := writePrivate(manifest, b); err != nil {
			return m, err
		}
	}
	if s.Target == "" {
		s.Target = iscsi.TargetForMAC(s.MAC)
	}
	if s.Portal == "" {
		u, _ := url.Parse(r.ReportBase)
		if u != nil {
			s.Portal = u.Hostname()
		}
	}
	if s.OSType == domain.OSTypeWindows {
		drivers := []driverNote{}
		if s.HWID != "" {
			driverDir, err := r.prepareWindowsDrivers(ctx, s, dir, m.Files["install.wim"])
			if err != nil {
				return m, err
			}
			if drivers, err = driverNotes(driverDir, s.HWID); err != nil {
				return m, err
			}
			var paths []string
			err = filepath.WalkDir(driverDir, func(path string, d os.DirEntry, err error) error {
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return err
				}
				if !d.IsDir() {
					paths = append(paths, path)
				}
				return nil
			})
			if err != nil {
				return m, err
			}
			sort.Strings(paths)
			var lines []string
			for i, path := range paths {
				alias := fmt.Sprintf("nd-driver-%04d", i)
				m.Files[alias] = path
				rel, _ := filepath.Rel(driverDir, path)
				lines = append(lines, alias+"|"+strings.ReplaceAll(rel, "/", "\\"))
			}
			m.Content["nd-driver-files.txt"] = crlf([]byte(strings.Join(lines, "\n")))
		}
		wimboot, err := tftpassets.Read("wimboot")
		if err != nil {
			return m, err
		}
		m.Content["wimboot"] = wimboot
		m.Content["winpeshl.ini"] = crlf([]byte("[LaunchApps]\n%SYSTEMROOT%\\System32\\cmd.exe, /c %SYSTEMROOT%\\System32\\nd-install.cmd\n"))
		for _, name := range []string{"nd-install.cmd", "nd-install.js"} {
			b, _ := installScripts.ReadFile("scripts/" + name)
			if strings.HasSuffix(name, ".js") {
				b = wideScript(b)
			} else {
				b = crlf(b)
			}
			m.Content[name] = b
		}
		ndnic, err := pe.ScriptFile("ndnic.js")
		if err != nil {
			return m, err
		}
		m.Content["ndnic.js"] = wideScript(ndnic)
		credentials := iscsi.CHAPCreds{}
		if iscsi.CHAPEnabled(r.CHAPSecret) {
			credentials = iscsi.CHAPCredentials(r.CHAPSecret, s.MAC)
		}
		var wimSize int64
		if info, err := os.Stat(m.Files["install.wim"]); err == nil {
			wimSize = info.Size()
		}
		settings, _ := json.Marshal(map[string]any{"wimsize": wimSize, "base": r.base(s), "attempt": s.Attempt, "serial": iscsi.UnitSerial(s.Target, s.LUN), "size": s.SizeBytes, "index": s.EditionIndex, "hwid": s.HWID, "busloc": s.BusLoc, "ip": s.IP, "portal": s.Portal, "target": s.Target, "initiator": iscsi.InitiatorIQN(s.MAC), "username": credentials.Username, "password": credentials.Password, "drivers": drivers})
		m.Content["nd-install.json"] = settings
		m.Content["unattend.xml"] = WindowsUnattend(s)
		m.Content["iso-startup.ps1"] = wideScript(windowsStartup(s, r.base(s)))
	} else {
		pw, err := os.ReadFile(filepath.Join(dir, "password.hash"))
		if err != nil {
			return m, err
		}
		if isAnaconda(s.Distribution) {
			return r.anacondaContent(s, m, string(pw))
		}
		data, err := UbuntuAutoinstall(s, r.base(s), string(pw))
		if err != nil {
			return m, err
		}
		m.Content["user-data"] = data
		m.Content[casperOverlayName] = casperISCSIOverlay()
		m.Content["meta-data"] = []byte("instance-id: " + s.ID + "\nlocal-hostname: nexboot-image\n")
		for _, name := range []string{"early.sh", "finish.sh", "iso-startup.py"} {
			b, _ := installScripts.ReadFile("scripts/" + name)
			b = bytes.ReplaceAll(b, []byte("__BASE__"), []byte(r.base(s)))
			b = bytes.ReplaceAll(b, []byte("__ATTEMPT__"), []byte(strconv.Itoa(s.Attempt)))
			b = bytes.ReplaceAll(b, []byte("__SERIAL__"), []byte(iscsi.UnitSerial(s.Target, s.LUN)))
			m.Content[name] = b
		}
		m.Content["nd-datadisks"] = adapt.RenderLinuxMountScript(r.APIPort)
	}
	return m, nil
}

func safeMediaID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func crlf(b []byte) []byte {
	return []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", "\r\n"))
}
func wideScript(b []byte) []byte {
	out := []byte{0xff, 0xfe}
	for _, c := range utf16.Encode([]rune(string(crlf(b)))) {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}
func writePrivate(dest string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".install-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}

// pinISO 把 ISO 固定到安装目录并核对校验值。导入目录与安装目录同在文件库里，先硬链接（不占空间、不用复制几 GB）；
// 跨文件系统（旧导入目录里的会话）时退回复制。
func pinISO(ctx context.Context, src, dest, expected string) (string, error) {
	link := filepath.Join(filepath.Dir(dest), ".iso-link")
	_ = os.Remove(link)
	if err := os.Link(src, link); err == nil {
		defer os.Remove(link)
		sum, err := hashFile(ctx, link)
		if err != nil {
			return "", err
		}
		if expected != "" && sum != expected {
			return "", fmt.Errorf("安装文件已变化，请重新选择并识别 ISO")
		}
		return sum, os.Rename(link, dest)
	}
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer f.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".iso-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), &contextReader{ctx: ctx, r: f})
	ce := tmp.Close()
	if err != nil {
		return "", err
	}
	if ce != nil {
		return "", ce
	}
	sum := hex.EncodeToString(h.Sum(nil))
	// 新建的安装没有期望值，只记下算出的；重新开始安装带着原会话的值，换了文件就拦下。
	if expected != "" && sum != expected {
		return "", fmt.Errorf("安装文件已变化，请重新选择并识别 ISO")
	}
	return sum, os.Rename(tmp.Name(), dest)
}

func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &contextReader{ctx: ctx, r: f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func extractMember(ctx context.Context, iso, entry, dest string) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".member-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	cmd := exec.CommandContext(ctx, "7z", "x", "-so", "--", iso, entry)
	cmd.Stdout = f
	err = cmd.Run()
	ce := f.Close()
	if err != nil {
		return fmt.Errorf("提取安装文件 %s 失败，请检查 ISO 完整性", entry)
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), dest)
}

type wimStderr []byte

func (b *wimStderr) Write(p []byte) (int, error) {
	n := len(p)
	if left := 4096 - len(*b); left > 0 {
		*b = append(*b, p[:min(left, n)]...)
	}
	return n, nil
}

func extractWIMMember(ctx context.Context, wim string, index int, entry, dest string) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".wim-member-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	cmd := exec.CommandContext(ctx, "wimlib-imagex", "extract", wim, strconv.Itoa(index), entry, "--to-stdout")
	// Windows paths are case-insensitive; stream to our filename regardless of archive spelling.
	cmd.Env = append(os.Environ(), "WIMLIB_IMAGEX_IGNORE_CASE=1")
	cmd.Stdout = f
	var stderr wimStderr
	cmd.Stderr = &stderr
	err = cmd.Run()
	ce := f.Close()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("请在镜像所在服务器安装 wimtools 后重试：找不到 wimlib-imagex")
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 49 {
			return wimMemberMissing{entry, index}
		}
		return fmt.Errorf("提取 Windows 安装文件 %s 失败，请检查 ISO 完整性和服务器存储后重试：%w；%s", entry, err, strings.TrimSpace(string(stderr)))
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), dest)
}

// wimMemberMissing 是所选版本里没有这个文件（wimlib 退出码 49），区别于读取失败。
type wimMemberMissing struct {
	entry string
	index int
}

func (e wimMemberMissing) Error() string {
	return fmt.Sprintf("请选择包含 %s 的 Windows ISO；所选版本 %d 缺少此安装文件", e.entry, e.index)
}

func (r Runtime) Render(_ context.Context, s domain.InstallSession, m MediaFiles) (string, error) {
	if s.Phase == "installing" {
		return "", fmt.Errorf("安装已开始，请查看任务与日志；中断后请取消本次安装再重新创建，禁止重复格式化")
	}
	base := r.base(s)
	lines := []string{"#!ipxe", "iseq ${platform} efi || goto unsupported", "set keep-san 1", "set initiator-iqn " + iscsi.InitiatorIQN(s.MAC)}
	if iscsi.CHAPEnabled(r.CHAPSecret) {
		c := iscsi.CHAPCredentials(r.CHAPSecret, s.MAC)
		lines = append(lines, "set username "+c.Username, "set password "+c.Password)
	}
	if s.OSType == domain.OSTypeWindows {
		lines = append(lines, "set net0/gateway 0.0.0.0")
	}
	lines = append(lines, fmt.Sprintf("sanhook --drive 0x80 iscsi:%s:::%d:%s || goto failed", s.Portal, s.LUN, s.Target))
	if s.Phase == "initializing" {
		lines = append(lines, "sanboot --drive 0x80 || goto failed")
	} else if s.OSType == domain.OSTypeWindows {
		lines = append(lines, "kernel "+base+"wimboot || goto failed")
		names := []string{"BCD", "boot.sdi", "winpeshl.ini", "nd-install.cmd", "nd-install.js", "nd-install.json", "ndnic.js", "unattend.xml", "iso-startup.ps1", "curl.exe"}
		for name := range m.Files {
			if strings.HasPrefix(name, "nd-driver-") {
				names = append(names, name)
			}
		}
		if _, ok := m.Content["nd-driver-files.txt"]; ok {
			names = append(names, "nd-driver-files.txt")
		}
		for _, name := range names {
			lines = append(lines, "initrd "+base+name+" "+name+" || goto failed")
		}
		lines = append(lines, "initrd "+base+"boot.wim boot.wim || goto failed", "boot || goto failed")
	} else if isAnaconda(s.Distribution) {
		label := string(m.Content["media-label"])
		if label == "" {
			return "", fmt.Errorf("RHEL 系安装资源缺少介质卷标，请重试准备安装资源")
		}
		updates := ""
		if strings.Split(s.Version, ".")[0] == "7" {
			if len(m.Content["nd-el7-updates.img"]) == 0 {
				return "", fmt.Errorf("CentOS/RHEL 7 安装兼容更新尚未准备好，请重试准备安装资源")
			}
			updates = " inst.updates=" + base + "nd-el7-updates.img"
		}
		lines = append(lines, "kernel "+base+"vmlinuz initrd=initrd ip=ibft rd.neednet=1 rd.iscsi.firmware=1 rd.iscsi.ibft=1 inst.text inst.ks="+base+"ks.cfg inst.stage2=hd:LABEL="+label+" inst.repo=cdrom:LABEL="+label+updates+" || goto failed", "initrd "+base+"initrd || goto failed", "boot || goto failed")
	} else {
		// ISO 是同一 target 的只读 LUN 1，附加 initrd 让 casper 先按 iBFT 登录再找介质；
		// 老版本 iPXE 在 UEFI 下只能靠 initrd= 交出 initrd；fsck.mode=skip 免得开机把整张 ISO 校验一遍。
		lines = append(lines, "kernel "+base+"vmlinuz initrd=initrd initrd="+casperOverlayName+" ip=dhcp boot=casper fsck.mode=skip autoinstall ds=nocloud-net;s="+base+" --- || goto failed",
			"initrd "+base+"initrd || goto failed", "initrd "+base+casperOverlayName+" || goto failed", "boot || goto failed")
	}
	lines = append(lines, ":unsupported", "echo This installer requires x64 UEFI network boot.", "sleep 20", "exit", ":failed", "echo Installation boot failed. Check the task center.", "sleep 30", "exit")
	return strings.Join(lines, "\n") + "\n", nil
}

func (r Runtime) MountScript(s domain.InstallSession) []byte {
	if s.OSType != domain.OSTypeWindows {
		return nil
	}
	return append(adapt.RenderMountScript(r.APIPort), []byte("\r\n& \"$env:ProgramData\\NexBoot\\iso-startup.ps1\"\r\n")...)
}
