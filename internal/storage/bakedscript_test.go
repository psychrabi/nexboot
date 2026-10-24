package storage

import (
	"strings"
	"testing"
)

// 镜像里只烘焙一个固定的启动器：开机先向启动服务器取最新的挂盘脚本，取不到再跑烘焙时的副本。
// 挂盘脚本以后怎么改，都不会让已烘焙的镜像失效。
func TestMountBootstrapFetchesLatestScriptAndFallsBack(t *testing.T) {
	if !strings.Contains(MountLauncherBody, MountBootstrapName) || strings.Contains(MountLauncherBody, MountScriptName) {
		t.Fatalf("组策略启动项要先跑启动器，而不是直接跑挂盘脚本：%q", MountLauncherBody)
	}
	for _, want := range []string{"/boot/mount-script?mac=", "format=text", MountScriptName, "$port", "MSiSCSIInitiator_SessionClass", "-TimeoutSec"} {
		if !strings.Contains(MountBootstrapBody, want) {
			t.Errorf("启动器缺少 %q", want)
		}
	}
	// Windows PowerShell 5.1 按系统代码页读脚本，非 ASCII 字面量会被解错。
	for i, r := range MountBootstrapBody {
		if r > 0x7e || (r < 0x20 && r != '\r' && r != '\n') {
			t.Fatalf("启动器第 %d 个字符 %q 不是 ASCII", i, r)
		}
	}
}
