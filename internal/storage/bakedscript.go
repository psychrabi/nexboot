package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Windows 镜像在导入时烘焙开机脚本，下游克隆都继承。镜像是否过期靠版本串比较，
// 所以版本必须覆盖整个注入布局（写哪些文件、叫什么、如何向 gpsvc 注册），而不只是脚本正文。
// 布局以数据形式放在这里并参与哈希，改动后旧镜像会自动失效。
const (
	// MountScriptName 是开机脚本本身，由 control/adapt 生成。
	MountScriptName = "mount-disks.ps1"
	// MountLauncherName 是 gpsvc 调用的 cmd 包装：scripts.ini 只能注册命令，
	// `start` 让 PowerShell 脱离，组策略开机阶段不会因等盘符而卡住登录界面。
	MountLauncherName = "ndmount.cmd"
	MountLauncherBody = "@echo off\r\nstart \"\" powershell -NoProfile -ExecutionPolicy Bypass -File \"%~dp0" + MountBootstrapName + "\"\r\n"
	// MountBootstrapName 是烘焙进镜像的启动器：开机先向启动服务器取最新的挂盘脚本，
	// 取不到再跑同目录里烘焙时的副本。挂盘脚本以后怎么改，都不让已烘焙的镜像失效。
	MountBootstrapName = "mount-boot.ps1"
	// GroupPolicyINI 列出 Scripts 客户端扩展 GUID，gpsvc 才会处理本地策略；Version 取大值以强制重新处理。
	GroupPolicyINI = "[General]\r\ngPCMachineExtensionNames=[{" + ScriptsCSEGUID + "}{40B6664F-4972-11D1-A7CA-0000F87571E3}]\r\nVersion=65539\r\n"
	// ScriptsCSEGUID 用于判断 gpt.ini 是否已注册 Scripts 扩展。
	ScriptsCSEGUID = "42B5FAAE-6536-11D2-AE5A-0000F87571E3"

	// 挂载后 Windows 卷内的路径，用斜杠是因为指的是镜像内位置而非本机。
	// 本地组策略纯靠文件，离线镜像才能注册而不必改注册表 hive。
	groupPolicyPath   = "Windows/System32/GroupPolicy"
	machineScriptPath = groupPolicyPath + "/Machine/Scripts"
	startupPath       = machineScriptPath + "/Startup"
)

// MountBootstrapBody 只用 ASCII：Windows PowerShell 5.1 按系统代码页读脚本。端口取自烘焙的挂盘脚本，
// 服务器取自当前 iSCSI 会话，与挂盘脚本找服务器的办法相同；只接受带标记行、不超过 256 KB 的脚本。
const MountBootstrapBody = `# NexBoot startup launcher, baked into the image once.
# Fetch the latest body; its bounded readiness retries also run when discovery falls back to the baked copy.
$ErrorActionPreference = 'Continue'
$local = Join-Path $PSScriptRoot '` + MountScriptName + `'
$port = ''
$line = Select-String -Path $local -Pattern '^\$port = ''(\d+)''' -List -ErrorAction SilentlyContinue
if ($line) { $port = $line.Matches[0].Groups[1].Value }
$body = $null
$start = Get-Date
$env:ND_MOUNT_DEADLINE = $start.AddSeconds(90).ToString('o')
$fetchDeadline = $start.AddSeconds(15)
$bootstrapLog = Join-Path $PSScriptRoot 'bootstrap.log'
"start $($start.ToString('o'))" | Out-File $bootstrapLog -Encoding utf8
function Bootstrap-Log($m) { $m | Out-File $bootstrapLog -Append -Encoding utf8 }
function Bootstrap-Timeout { [int][Math]::Min(5, [Math]::Floor(($fetchDeadline - (Get-Date)).TotalSeconds)) }
if ($port) {
  try {
    $timeout = Bootstrap-Timeout
    if ($timeout -gt 0) {
      $addrs = @(Get-CimInstance -Namespace root/wmi -ClassName MSiSCSIInitiator_SessionClass -OperationTimeoutSec $timeout -ErrorAction Stop |
        ForEach-Object { $_.ConnectionInformation } | ForEach-Object { $_.TargetAddress } | Where-Object { $_ } | Sort-Object -Unique)
      $timeout = Bootstrap-Timeout
      $macs = @()
      if ($timeout -gt 0) {
        $macs = @(Get-CimInstance -ClassName Win32_NetworkAdapter -OperationTimeoutSec $timeout -ErrorAction Stop | Where-Object { $_.PhysicalAdapter -and $_.MACAddress } |
          ForEach-Object { $_.MACAddress } | Sort-Object -Unique)
      }
      :fetch foreach ($addr in $addrs) {
        foreach ($mac in $macs) {
          $timeout = Bootstrap-Timeout
          if ($timeout -le 0) { break fetch }
          try {
            $r = Invoke-WebRequest -UseBasicParsing -Uri "http://${addr}:${port}/boot/mount-script?mac=${mac}&format=text" -TimeoutSec $timeout -ErrorAction Stop
            if ($r.StatusCode -eq 200 -and $r.Content.Length -lt 262144 -and $r.Content.Contains('` + MountScriptMarker + `')) { $body = $r.Content; break fetch }
          } catch { Bootstrap-Log "fetch ${addr} mac=${mac}: $($_.Exception.Message)" }
        }
      }
    }
  } catch { Bootstrap-Log "discovery not ready: $($_.Exception.Message)" }
}
Bootstrap-Log "source: $(if ($body) { 'server' } else { 'baked' })"

if ($body) {
  $env:ND_MOUNT_SOURCE = 'server'
  $fresh = Join-Path $env:windir 'Temp\nd-mount-disks.ps1'
  [IO.File]::WriteAllText($fresh, $body, (New-Object Text.UTF8Encoding $true))
  & $fresh
} else {
  $env:ND_MOUNT_SOURCE = 'baked'
  & $local
}
`

// MountScriptMarker 是挂盘脚本的首行，启动器据此确认下载到的是挂盘脚本而不是别的响应。
const MountScriptMarker = "# ndiskless:mount-disks"

// GroupPolicyDir 是挂载卷内存放 gpt.ini 的目录；MachineScriptsDir 放 scripts.ini，StartupScriptDir 放脚本本身。
func GroupPolicyDir(mnt string) string {
	return filepath.Join(mnt, filepath.FromSlash(groupPolicyPath))
}
func MachineScriptsDir(mnt string) string {
	return filepath.Join(mnt, filepath.FromSlash(machineScriptPath))
}
func StartupScriptDir(mnt string) string { return filepath.Join(mnt, filepath.FromSlash(startupPath)) }

// MountScriptLayoutID 标识注入布局（写哪些文件、启动器内容、如何注册），是烘焙版本的依据；挂盘脚本正文不在其中，
// 由启动器开机时取最新的。布局变化会让旧镜像失效，开机时退回逐台注入。
func MountScriptLayoutID() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		startupPath,
		MountScriptName,
		MountLauncherName,
		MountLauncherBody,
		MountBootstrapName,
		MountBootstrapBody,
		GroupPolicyINI,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}
