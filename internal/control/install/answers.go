package install

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
)

func xmlText(v string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}

// WindowsUnattend configures an applied image; disk selection is never delegated to Windows Setup.
func WindowsUnattend(s domain.InstallSession) []byte {
	task := `cmd.exe /c schtasks.exe /Create /F /TN NexBootISO /SC ONSTART /RU SYSTEM /TR "powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\ProgramData\NexBoot\iso-startup.ps1 -Installed" & schtasks.exe /Run /TN NexBootISO`
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
 <settings pass="specialize"><component name="Microsoft-Windows-Deployment" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS"><RunSynchronous><RunSynchronousCommand wcm:action="add"><Order>1</Order><Path>` + xmlText(task) + `</Path></RunSynchronousCommand></RunSynchronous></component>
 <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS"><ComputerName>NEXBOOT-IMAGE</ComputerName><TimeZone>China Standard Time</TimeZone></component></settings>
 <settings pass="oobeSystem">` + windowsLocale(s.Language) + `<component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
  <OOBE><HideEULAPage>true</HideEULAPage><HideOEMRegistrationScreen>true</HideOEMRegistrationScreen><HideLocalAccountScreen>true</HideLocalAccountScreen><HideOnlineAccountScreens>true</HideOnlineAccountScreens><HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE><ProtectYourPC>3</ProtectYourPC></OOBE>
  <UserAccounts><AdministratorPassword><Value>` + xmlText(s.Password) + `</Value><PlainText>true</PlainText></AdministratorPassword><LocalAccounts><LocalAccount wcm:action="add"><Name>` + xmlText(s.Username) + `</Name><DisplayName>` + xmlText(s.Username) + `</DisplayName><Group>Administrators</Group><Password><Value>` + xmlText(s.Password) + `</Value><PlainText>true</PlainText></Password></LocalAccount></LocalAccounts></UserAccounts>
 </component></settings>
</unattend>`)
}

var languageTag = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$`)

// windowsLocale 让首次初始化跳过地区、键盘页；语言读不到或格式不对时不写，免得填错让初始化出错。
func windowsLocale(lang string) string {
	if !languageTag.MatchString(lang) {
		return ""
	}
	return `<component name="Microsoft-Windows-International-Core" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">` +
		`<InputLocale>` + lang + `</InputLocale><SystemLocale>` + lang + `</SystemLocale><UILanguage>` + lang + `</UILanguage><UserLocale>` + lang + `</UserLocale></component>`
}

func windowsStartup(s domain.InstallSession, base string) []byte {
	baseJSON, _ := json.Marshal(base)
	code := `param([switch]$Installed)
$ErrorActionPreference = 'Stop'
$base = __BASE__
for ($retry = 0; $retry -lt 90; $retry++) {
try {
  $boot = Invoke-RestMethod -Uri ($base + 'boot.json') -TimeoutSec 15
  if ($Installed -and $boot.phase -eq 'initializing') {
    $setup = Get-ItemProperty 'HKLM:\SYSTEM\Setup' -ErrorAction SilentlyContinue
    if ($setup.SystemSetupInProgress -or $setup.OOBEInProgress) { Start-Sleep 10; continue }
    # 账户还没登录过；不清掉，客户机每次登录都会重跑首次设置并卡在联网查更新。
    Remove-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\OOBE' -Name LaunchUserOOBE -ErrorAction SilentlyContinue
    $winlogon = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon'
    if ((Get-ItemProperty $winlogon -ErrorAction SilentlyContinue).DefaultUserName -eq 'defaultuser0') {
      Set-ItemProperty $winlogon -Name AutoAdminLogon -Value '0'
      Remove-ItemProperty $winlogon -Name DefaultUserName -ErrorAction SilentlyContinue
    }
    $body = @{attempt=$boot.attempt; phase='installed'; message='Windows 初始化完成，等待关机保存镜像'} | ConvertTo-Json -Compress
    Invoke-RestMethod -Method Post -Uri ($base + 'report') -Body $body -ContentType 'application/json; charset=utf-8' -TimeoutSec 30 | Out-Null
    Remove-Item "$env:windir\Panther\unattend.xml" -Force -ErrorAction SilentlyContinue
    shutdown.exe /s /t 15 /c "NexBoot: installation complete; saving image"
    break
  } elseif ($boot.phase -eq 'verifying' -and $boot.boot_id) {
    $body = @{attempt=$boot.attempt; phase='verified'; boot_id=$boot.boot_id; message='Windows 普通无盘启动回报成功'} | ConvertTo-Json -Compress
    Invoke-RestMethod -Method Post -Uri ($base + 'report') -Body $body -ContentType 'application/json; charset=utf-8' -TimeoutSec 30 | Out-Null
    schtasks.exe /Delete /TN NexBootISO /F | Out-Null
    break
  } else {
    if ($boot.phase -eq 'ready') { schtasks.exe /Delete /TN NexBootISO /F | Out-Null }
    break
  }
} catch {
  # The expired installation token has no role in subsequent ordinary boots.
  if ($_.Exception.Response.StatusCode.value__ -in @(401,403,404,410)) { break }
  'Waiting for installation server; retrying.' | Out-File "$env:ProgramData\NexBoot\iso-startup-error.log" -Encoding utf8
  Start-Sleep 10
}
}
`
	return []byte(strings.ReplaceAll(code, "__BASE__", string(baseJSON)))
}

// UbuntuAutoinstall emits YAML-compatible JSON so account values cannot inject YAML or shell syntax.
// installDiskPlaceholder 是应答文件里系统盘的占位：安装程序只认它探测到的盘，
// 安装前脚本按序列号唯一认出安装盘后填入真实设备（见 scripts/early.sh）。
const installDiskPlaceholder = "__NEXBOOT_INSTALL_DISK__"

func UbuntuAutoinstall(s domain.InstallSession, base, passwordHash string) ([]byte, error) {
	if !strings.HasPrefix(passwordHash, "$6$") {
		return nil, fmt.Errorf("Ubuntu 安装账户密码摘要无效")
	}
	config := []any{
		map[string]any{"id": "nd-system", "type": "disk", "path": installDiskPlaceholder, "ptable": "gpt", "wipe": "superblock", "grub_device": true},
		map[string]any{"id": "nd-efi", "type": "partition", "device": "nd-system", "number": 1, "size": "512M", "flag": "boot", "grub_device": true},
		map[string]any{"id": "nd-root", "type": "partition", "device": "nd-system", "number": 2, "size": -1},
		map[string]any{"id": "nd-efi-fs", "type": "format", "volume": "nd-efi", "fstype": "fat32"},
		map[string]any{"id": "nd-root-fs", "type": "format", "volume": "nd-root", "fstype": "ext4"},
		map[string]any{"id": "nd-root-mount", "type": "mount", "device": "nd-root-fs", "path": "/"},
		map[string]any{"id": "nd-efi-mount", "type": "mount", "device": "nd-efi-fs", "path": "/boot/efi"},
	}
	mac := strings.ToLower(s.MAC)
	if len(mac) == 12 {
		mac = mac[0:2] + ":" + mac[2:4] + ":" + mac[4:6] + ":" + mac[6:8] + ":" + mac[8:10] + ":" + mac[10:12]
	}
	bootnic := map[string]any{"match": map[string]string{"macaddress": mac}, "dhcp4": true}
	gateway := s.Gateway
	if gateway == "" {
		gateway = s.Portal
	}
	bootnic["routes"] = []any{map[string]any{"to": "default", "via": gateway, "on-link": true}}
	auto := map[string]any{
		"version": 1, "locale": "en_US.UTF-8", "keyboard": map[string]string{"layout": "us"}, "timezone": "Asia/Shanghai",
		"refresh-installer": map[string]bool{"update": false}, "updates": "security",
		"kernel":   map[string]string{"package": "linux-generic"},
		"identity": map[string]string{"hostname": "nexboot-image", "username": s.Username, "password": passwordHash},
		"network":  map[string]any{"version": 2, "ethernets": map[string]any{"bootnic": bootnic}},
		// 安装服务器不提供软件源：安装程序测不通就只用 ISO 自带的包离线装，不依赖任何外网。
		"apt":            map[string]any{"fallback": "offline-install", "geoip": false, "preserve_sources_list": false, "primary": []any{map[string]any{"arches": []string{"amd64"}, "uri": base + "offline"}}},
		"storage":        map[string]any{"config": config, "grub": map[string]bool{"update_nvram": false, "reorder_uefi": false}},
		"early-commands": []any{[]string{"sh", "-c", "python3 -c \"import urllib.request; urllib.request.urlretrieve('" + base + "early.sh','/tmp/nd-early.sh')\" && sh /tmp/nd-early.sh"}},
		"late-commands":  []any{[]string{"sh", "-c", "curl -fsS --retry 3 '" + base + "finish.sh' -o /tmp/nd-finish.sh && sh /tmp/nd-finish.sh"}},
		"error-commands": []any{[]string{"sh", "-c", fmt.Sprintf("curl -fsS --max-time 30 -X POST -H 'Content-Type: application/json' --data '{\"attempt\":%d,\"phase\":\"failed\",\"message\":\"Ubuntu 安装失败，请查看安装日志\"}' '%sreport'; curl -fsS --max-time 60 --data-binary @/var/log/installer/subiquity-server-debug.log '%slog/subiquity.log'", s.Attempt, base, base)}},
		"shutdown":       "reboot",
	}
	auto["source"] = map[string]any{"search_drivers": false}
	if s.Distribution == "ubuntu-desktop" {
		auto["source"].(map[string]any)["id"] = "ubuntu-desktop"
	}
	b, err := json.MarshalIndent(map[string]any{"autoinstall": auto}, "", "  ")
	return append([]byte("#cloud-config\n"), b...), err
}
