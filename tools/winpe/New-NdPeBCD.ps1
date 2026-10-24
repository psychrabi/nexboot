<#
.SYNOPSIS
  生成 wimboot 引导 WinPE 所需的 BCD（启动配置数据）。只需要 Windows 自带的 bcdedit，不需要 ADK 或安装介质。

.DESCRIPTION
  产出一个只含「ramdisk 启动 boot.wim」条目的 BCD。wimboot 启动时会按固件自动改写 winload.exe/winload.efi，
  所以 BIOS 与 UEFI 共用这一份。生成一次即可，不依赖任何镜像版本。

  用管理员 PowerShell 运行：
    .\New-NdPeBCD.ps1 -OutDir C:\ndwinpe
  产出 C:\ndwinpe\BCD 与 BCD.sha256，把 BCD 上传到控制台「系统参数 → 适配环境」，或放进 Build-NdWinPE.ps1 的产物目录。
#>
[CmdletBinding()]
param(
  [string]$OutDir = (Join-Path $PWD 'ndwinpe'),
  [string]$Description = 'NexBoot WinPE'
)
$ErrorActionPreference = 'Stop'

if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw '请用管理员权限运行：bcdedit 需要。'
}
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$store = Join-Path $OutDir 'BCD'
if (Test-Path $store) { Remove-Item -Force $store }

function Invoke-Bcd {
  param([string[]]$Args)
  $out = & bcdedit.exe /store $store @Args 2>&1
  if ($LASTEXITCODE -ne 0) { throw "bcdedit $($Args -join ' ') 失败：$out" }
  return ($out | Out-String)
}

& bcdedit.exe /createstore $store | Out-Null
if ($LASTEXITCODE -ne 0) { throw '创建 BCD 存储失败' }

# ramdisk 选项：boot.sdi 由 wimboot 从同一引导包提供，路径固定。
Invoke-Bcd @('/create', '{ramdiskoptions}', '/d', 'Ramdisk Options') | Out-Null
Invoke-Bcd @('/set', '{ramdiskoptions}', 'ramdisksdidevice', 'boot') | Out-Null
Invoke-Bcd @('/set', '{ramdiskoptions}', 'ramdisksdipath', '\boot\boot.sdi') | Out-Null

# 启动条目：wimboot 会把 [boot]\boot.wim 对应到它加载的 wim，并按固件改写 winload 路径。
$created = Invoke-Bcd @('/create', '/d', $Description, '/application', 'osloader')
if ($created -notmatch '\{[0-9a-fA-F-]{36}\}') { throw "没有拿到新条目的 GUID：$created" }
$guid = $Matches[0]
foreach ($pair in @(
    @('device', 'ramdisk=[boot]\boot.wim,{ramdiskoptions}'),
    @('osdevice', 'ramdisk=[boot]\boot.wim,{ramdiskoptions}'),
    @('path', '\windows\system32\boot\winload.efi'),
    @('systemroot', '\windows'),
    @('detecthal', 'yes'),
    @('winpe', 'yes'),
    @('bootmenupolicy', 'standard'))) {
  Invoke-Bcd @('/set', $guid, $pair[0], $pair[1]) | Out-Null
}

Invoke-Bcd @('/create', '{bootmgr}', '/d', 'Boot Manager') | Out-Null
Invoke-Bcd @('/set', '{bootmgr}', 'default', $guid) | Out-Null
Invoke-Bcd @('/set', '{bootmgr}', 'displayorder', $guid) | Out-Null
Invoke-Bcd @('/set', '{bootmgr}', 'timeout', '0') | Out-Null

$enum = Invoke-Bcd @('/enum', 'all')
foreach ($must in @('ramdisksdipath', 'boot.wim', 'winpe', 'detecthal')) {
  if ($enum -notmatch [regex]::Escape($must)) { throw "生成的 BCD 缺少 $must" }
}
$hash = (Get-FileHash -Algorithm SHA256 $store).Hash.ToLower()
"$hash  BCD" | Set-Content -Encoding ascii (Join-Path $OutDir 'BCD.sha256')
Write-Host "BCD 已生成：$store"
Write-Host "SHA-256：$hash"
Write-Host "条目 GUID：$guid"
