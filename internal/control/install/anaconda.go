package install

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/tianwei/diskless/internal/control/adapt"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage/iscsi"
)

func isAnaconda(distribution string) bool { return strings.HasPrefix(distribution, "anaconda-") }

func anacondaPythonPackage(version string) string {
	if strings.HasPrefix(version, "8") {
		return "python36"
	}
	return "python3"
}

func treeInfo(data []byte) map[string]string {
	values := map[string]string{}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			values[section+"."+strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return values
}

func inspectAnaconda(ctx context.Context, source string, lookup map[string]string, meta Metadata) (Metadata, error) {
	b, err := ReadArchiveEntry(ctx, source, lookup[".treeinfo"], 1<<20)
	if err != nil {
		return meta, err
	}
	info := treeInfo(b)
	family := info["general.family"]
	version := info["general.version"]
	arch := info["general.arch"]
	if family == "" {
		family = info["release.name"]
	}
	if version == "" {
		version = info["release.version"]
	}
	if arch == "" {
		arch = info["tree.arch"]
	}
	distribution := ""
	switch strings.ToLower(family) {
	case "centos", "centos linux", "centos stream":
		distribution = "centos"
	case "red hat enterprise linux", "red hat enterprise linux server":
		distribution = "rhel"
	case "rocky linux", "rocky":
		distribution = "rocky"
	case "almalinux", "almalinux os":
		distribution = "alma"
	}
	if distribution == "" || arch != "x86_64" || !regexp.MustCompile(`^[789](\.[0-9]+)*$`).MatchString(version) {
		return meta, errs.Invalid("请使用 CentOS/RHEL/Rocky/AlmaLinux 7、8 或 9 的 x86_64 完整 DVD；这张 ISO 的发行版、版本或架构尚不支持")
	}
	for _, entry := range []string{info["stage2.mainimage"], info["images-x86_64.kernel"], info["images-x86_64.initrd"], "efi/boot/bootx64.efi"} {
		if entry == "" || lookup[strings.ToLower(entry)] == "" {
			return meta, errs.Invalid("请使用完整 DVD：Anaconda ISO 缺少安装内核、启动环境或 x64 UEFI 文件")
		}
	}
	if info["images-x86_64.kernel"] != "images/pxeboot/vmlinuz" || info["images-x86_64.initrd"] != "images/pxeboot/initrd.img" {
		return meta, errs.Invalid("请使用标准 Anaconda DVD：这张 ISO 的安装内核路径尚不支持")
	}
	// 完整 DVD 才有离线软件源；7 为根目录，8/9 为 BaseOS 与 AppStream。
	repos := []string{"repodata/repomd.xml"}
	if version[0] != '7' {
		repos = []string{"baseos/repodata/repomd.xml", "appstream/repodata/repomd.xml"}
	}
	for _, repo := range repos {
		if lookup[repo] == "" {
			return meta, errs.Invalid("请使用完整 DVD：ISO 缺少离线软件源 " + repo + "，不能使用 boot/netinstall ISO")
		}
	}
	for _, pkg := range []string{"iscsi-initiator-utils", "dracut-network", "curl", anacondaPythonPackage(version), "grub2-efi-x64", "shim-x64", "openssh-server", "NetworkManager", "grubby"} {
		found := false
		for entry := range lookup {
			name := path.Base(entry)
			if strings.HasPrefix(name, strings.ToLower(pkg)+"-") && len(name) > len(pkg)+1 && name[len(pkg)+1] >= '0' && name[len(pkg)+1] <= '9' && (strings.HasSuffix(name, ".x86_64.rpm") || strings.HasSuffix(name, ".noarch.rpm")) {
				found = true
				break
			}
		}
		if !found {
			return meta, errs.Invalid(fmt.Sprintf("请使用包含 %s 的完整 DVD：无盘安装不会从外网补装软件包", pkg))
		}
	}
	meta.OSType = domain.OSTypeLinux
	meta.Distribution = "anaconda-" + distribution
	meta.Version = version
	meta.RecommendedSizeGiB = 32
	return meta, nil
}

// AnacondaKickstart keeps destructive storage directives in a file generated only
// after the installer has identified the server's unique writable iSCSI LUN.
func AnacondaKickstart(s domain.InstallSession, base, passwordHash string) ([]byte, error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`).MatchString(s.Username) || !regexp.MustCompile(`^\$6\$[A-Za-z0-9./$-]+$`).MatchString(passwordHash) {
		return nil, fmt.Errorf("RHEL 系安装账户或密码摘要无效")
	}
	mac := strings.ToLower(s.MAC)
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(mac) {
		return nil, fmt.Errorf("安装客户机 MAC 无效")
	}
	mac = mac[0:2] + ":" + mac[2:4] + ":" + mac[4:6] + ":" + mac[6:8] + ":" + mac[8:10] + ":" + mac[10:12]
	repo := ""
	if !strings.HasPrefix(s.Version, "7") {
		repo = "repo --name=AppStream --baseurl=file:///run/install/repo/AppStream\n"
	}
	return []byte(fmt.Sprintf(`text
lang en_US.UTF-8
keyboard us
timezone Asia/Shanghai --utc
rootpw --lock
user --name=%s --groups=wheel --password=%s --iscrypted
network --bootproto=dhcp --device=%s --activate --hostname=nexboot-image
firewall --enabled --service=ssh
services --enabled=sshd,NetworkManager
selinux --enforcing
firstboot --disable
skipx
%sreboot
%%pre --erroronfail --log=/tmp/nd-pre.log
set -eu
curl -fsS --retry 3 '%sanaconda-early.py' -o /tmp/nd-early.py
if command -v python3 >/dev/null 2>&1; then python3 /tmp/nd-early.py
elif [ -x /usr/libexec/platform-python ]; then /usr/libexec/platform-python /tmp/nd-early.py
else python /tmp/nd-early.py; fi
%%end
%%include /tmp/nd-partitions.ks
%%packages
@core
iscsi-initiator-utils
dracut-network
curl
%s
grub2-efi-x64
shim-x64
openssh-server
NetworkManager
grubby
%%end
%%post --nochroot --erroronfail --log=/tmp/nd-post.log
set -eu
curl -fsS --retry 3 '%sanaconda-finish.sh' -o /tmp/nd-finish.sh
sh /tmp/nd-finish.sh
%%end
%%onerror
# Installer images need not ship coreutils timeout. Bound only our own child.
nd_readonly() {
  "$@" &
  nd_command=$!
  (
    trap 'kill "$nd_sleep" 2>/dev/null; wait "$nd_sleep" 2>/dev/null; exit 0' TERM INT
    sleep 5 &
    nd_sleep=$!
    wait "$nd_sleep"
    kill -KILL "$nd_command" 2>/dev/null
  ) &
  nd_watcher=$!
  wait "$nd_command"
  nd_status=$?
  kill "$nd_watcher" 2>/dev/null
  wait "$nd_watcher" 2>/dev/null
  return "$nd_status"
}
# Read-only snapshot: never login/logout or update initiator records.
{
  for mode in session node iface; do
    echo "=== iscsiadm $mode ==="
    if [ "$mode" = session ]; then nd_readonly iscsiadm -m session -P 1; else nd_readonly iscsiadm -m "$mode"; fi
  done
  echo "=== iscsid journal ==="
  nd_readonly journalctl -b -t iscsid --no-pager -n 100
  for f in /sys/class/iscsi_session/session*/targetname /sys/class/iscsi_session/session*/ifacename /sys/class/iscsi_connection/connection*/address /sys/class/iscsi_connection/connection*/port; do
    [ -r "$f" ] || continue
    echo "=== $f ==="
    cat "$f"
  done
} > /tmp/nd-iscsi-raw.log 2>&1
for name in anaconda storage program iscsi; do
  source=/tmp/$name.log
  [ "$name" != iscsi ] || source=/tmp/nd-iscsi-raw.log
  [ -r "$source" ] || continue
  # Bound each upload below SaveLog's 16 MiB limit without changing source logs.
  { echo '[Last up to 16770000 bytes; older content may be truncated]'; tail -c 16770000 "$source"; } |
    awk '{ line=tolower($0); if (line ~ /(password|passwd|secret|token)[^ =:]*[ \t]*[=:]/) { sub(/[=:].*/, "= [REDACTED]"); } gsub(/https?:\/\/[^ \t"<>]+/, "[URL REDACTED]"); print }' | tail -c 16770000 > /tmp/nd-upload.log
  curl -fsS --max-time 30 --data-binary @/tmp/nd-upload.log '%slog/'"$name.log" || true
 done
rm -f /tmp/nd-iscsi-raw.log /tmp/nd-upload.log
curl -fsS --max-time 30 -H 'Content-Type: application/json' --data '{"attempt":%d,"phase":"failed","message":"RHEL 系安装失败，请查看 Anaconda 安装日志"}' '%sreport' || true
%%end
`, s.Username, passwordHash, mac, repo, base, anacondaPythonPackage(s.Version), base, base, s.Attempt, base)), nil
}

func (r Runtime) anacondaContent(s domain.InstallSession, m MediaFiles, passwordHash string) (MediaFiles, error) {
	ks, err := AnacondaKickstart(s, r.base(s), passwordHash)
	if err != nil {
		return m, err
	}
	m.Content["ks.cfg"] = ks
	// The ISO9660 volume label is stable across iSCSI disk enumeration. Escape
	// spaces for dracut rather than assuming this machine's /dev/sdX ordering.
	f, err := os.Open(m.Files["media.iso"])
	if err != nil {
		return m, err
	}
	defer f.Close()
	pvd := make([]byte, 2048)
	if _, err = f.ReadAt(pvd, 16*2048); err != nil {
		return m, err
	}
	if string(pvd[1:6]) != "CD001" {
		return m, fmt.Errorf("RHEL 系 DVD 缺少 ISO9660 卷标，请使用完整官方 DVD")
	}
	label := strings.TrimSpace(string(pvd[40:72]))
	if !regexp.MustCompile(`^[A-Za-z0-9_. -]+$`).MatchString(label) {
		return m, fmt.Errorf("RHEL 系 DVD 卷标不支持，请使用官方 DVD")
	}
	m.Content["media-label"] = []byte(strings.ReplaceAll(label, " ", `\x20`))
	for _, name := range []string{"anaconda-early.py", "anaconda-finish.sh", "iso-startup.py"} {
		b, _ := installScripts.ReadFile("scripts/" + name)
		b = bytes.ReplaceAll(b, []byte("__BASE__"), []byte(r.base(s)))
		b = bytes.ReplaceAll(b, []byte("__ATTEMPT__"), []byte(strconv.Itoa(s.Attempt)))
		b = bytes.ReplaceAll(b, []byte("__SERIAL__"), []byte(iscsi.UnitSerial(s.Target, s.LUN)))
		b = bytes.ReplaceAll(b, []byte("__SIZE__"), []byte(strconv.FormatInt(s.SizeBytes, 10)))
		if name == "iso-startup.py" {
			b = bytes.ReplaceAll(b, []byte("Ubuntu"), []byte("Linux"))
		}
		m.Content[name] = b
	}
	if strings.Split(s.Version, ".")[0] == "7" {
		script, err := installScripts.ReadFile("scripts/anaconda-el7-iscsi.py")
		if err != nil {
			return m, err
		}
		archive := newcArchive([]cpioEntry{{"run", 0o040755, ""}, {"run/install", 0o040755, ""}, {"run/install/updates", 0o040755, ""}, {"run/install/updates/blivet", 0o040755, ""}, {"run/install/updates/blivet/iscsi.py", 0o100644, string(script)}, {"TRAILER!!!", 0, ""}})
		var packed bytes.Buffer
		gz := gzip.NewWriter(&packed)
		if _, err := gz.Write(archive); err != nil {
			return m, err
		}
		if err := gz.Close(); err != nil {
			return m, err
		}
		m.Content["nd-el7-updates.img"] = packed.Bytes()
	}
	m.Content["nd-datadisks"] = adapt.RenderLinuxMountScript(r.APIPort)
	return m, nil
}
