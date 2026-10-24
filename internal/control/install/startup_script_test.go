package install

import (
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
)

// 安装时没人登录过账户，镜像里留着「下次登录跑首次设置」，无盘客户机每次登录都会停在联网查更新那页（OOBEZDP）。
// 回报初始化完成之前就要清掉，回报后服务端随时可能开始保存。
func TestWindowsStartupClearsFirstLogonSetupBeforeReportingInstalled(t *testing.T) {
	script := string(windowsStartup(domain.InstallSession{}, "http://192.168.10.250:8080/install/x/"))
	start := strings.Index(script, "$setup.OOBEInProgress")
	report := strings.Index(script, "phase='installed'")
	if start < 0 || report < start {
		t.Fatalf("startup script lost the installed branch:\n%s", script)
	}
	beforeReport := script[start:report]
	for _, want := range []string{
		`'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\OOBE' -Name LaunchUserOOBE`,
		`-eq 'defaultuser0'`,
		`-Name AutoAdminLogon -Value '0'`,
	} {
		if !strings.Contains(beforeReport, want) {
			t.Errorf("before reporting installed, script lacks %s", want)
		}
	}
}
