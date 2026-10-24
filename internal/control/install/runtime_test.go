package install

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os/exec"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
)

func TestRuntimeBootUsesPrivateMediaAndWindowsGatewayRule(t *testing.T) {
	r := Runtime{ReportBase: "http://192.168.50.1:8080"}
	s := domain.InstallSession{Token: "private-token", MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", Portal: "192.168.50.1", Target: "iqn.test:install", OSType: domain.OSTypeWindows}
	files := MediaFiles{Files: map[string]string{"boot.wim": "/private/boot.wim"}}
	win, err := r.Render(context.Background(), s, files)
	if err != nil {
		t.Fatal(err)
	}
	clear, hook := strings.Index(win, "set net0/gateway 0.0.0.0"), strings.Index(win, "sanhook")
	if clear < 0 || hook < clear || !strings.Contains(win, "/install/private-token/boot.wim") {
		t.Fatalf("unsafe Windows boot: %s", win)
	}
	s.OSType = domain.OSTypeLinux
	linux, err := r.Render(context.Background(), s, files)
	if err != nil || strings.Contains(linux, "gateway 0.0.0.0") || !strings.Contains(linux, "autoinstall") || !strings.Contains(linux, "ds=nocloud-net;s=http://192.168.50.1:8080/install/private-token/") {
		t.Fatalf("Ubuntu boot: %s %v", linux, err)
	}
}

func TestInstallerScriptsProtectLocalDisksAndReportCorrectBoot(t *testing.T) {
	for _, spec := range []struct{ tool, file string }{{"node", "testdata/windows.cjs"}, {"python3", "testdata/ubuntu.py"}} {
		t.Run(spec.tool, func(t *testing.T) {
			bin, err := exec.LookPath(spec.tool)
			if err != nil {
				t.Skip(spec.tool + " unavailable")
			}
			if out, err := exec.Command(bin, spec.file).CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s", spec.file, err, out)
			}
		})
	}
}

func TestRuntimeRestartNeverRunsInstallerAgain(t *testing.T) {
	r := Runtime{}
	s := domain.InstallSession{Phase: "installing", OSType: domain.OSTypeWindows}
	if _, err := r.Render(context.Background(), s, MediaFiles{}); err == nil {
		t.Fatal("incomplete installation must block before disk formatting")
	}
	s.Phase = "initializing"
	text, err := r.Render(context.Background(), s, MediaFiles{})
	if err != nil || !strings.Contains(text, "sanboot") || strings.Contains(text, "wimboot") {
		t.Fatalf("initializing must boot existing disk: %s %v", text, err)
	}
}

func TestWindowsUnattendEscapesAccountAndDoesNotSelectLocalDisk(t *testing.T) {
	data := WindowsUnattend(domain.InstallSession{Username: "tester", Password: `A<&\"Password!`})
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		_, err := decoder.Token()
		if err != nil {
			if err.Error() != "EOF" {
				t.Fatal(err)
			}
			break
		}
	}
	if strings.Contains(string(data), "DiskID") || !strings.Contains(string(data), "A&lt;&amp;") {
		t.Fatalf("unsafe answer file: %s", data)
	}
}

func TestUbuntuAutoinstallTargetsOnlyNamedISCSIDisk(t *testing.T) {
	s := domain.InstallSession{MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", Portal: "192.168.50.1", Target: "iqn.test:install", Username: "tester", Attempt: 1, Token: "tok"}
	data, err := UbuntuAutoinstall(s, "http://192.168.50.1:8080/install/tok/", "$6$hash")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	// 安装程序只认它探测到的盘：系统盘先占位，安装前脚本按序列号唯一认出后再填真实设备；配置里不带 iSCSI 地址和 CHAP 口令。
	if !strings.Contains(text, `"path": "`+installDiskPlaceholder+`"`) || strings.Contains(text, "iscsi:") || strings.Contains(text, "/dev/sda") || !strings.Contains(text, "reorder_uefi") || !strings.Contains(text, "false") {
		t.Fatalf("unsafe install target: %s", data)
	}
	if !strings.Contains(text, "/early.sh") || !strings.Contains(text, "/finish.sh") {
		t.Fatalf("missing disk check or diskless preparation: %s", data)
	}
	var parsed map[string]map[string]any
	if err := json.Unmarshal(data[len("#cloud-config\n"):], &parsed); err != nil {
		t.Fatal(err)
	}
	// 纯局域网：软件包只来自 ISO，镜像源测不通就离线装，绝不去外网找。
	apt := parsed["autoinstall"]["apt"].(map[string]any)
	if apt["fallback"] != "offline-install" || apt["geoip"] != false || apt["sources_list"] != nil {
		t.Fatalf("installer must install offline from the ISO: %v", apt)
	}
	for _, p := range apt["primary"].([]any) {
		if uri := p.(map[string]any)["uri"].(string); !strings.HasPrefix(uri, "http://192.168.50.1:8080/install/tok/") {
			t.Fatalf("installer mirror points outside the installation server: %s", uri)
		}
	}
	if !strings.Contains(text, "on-link") {
		t.Fatal("isolated direct link must be recognized by Subiquity as connected")
	}
}
