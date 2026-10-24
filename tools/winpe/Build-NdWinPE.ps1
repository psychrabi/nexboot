<#
.SYNOPSIS
  用 Windows ADK 生成 NexBoot 的 WinPE 适配环境包。只在镜像没有可用 WinRE 时才需要（见 README）。

.DESCRIPTION
  前提：已安装 Windows ADK 与「Windows PE 附加组件」，版本不低于要适配的 Windows 镜像（C-16）。
  步骤：copype → 挂载 boot.wim → 加入 WMI、Scripting 组件 → 可选把网卡驱动目录做进 wim → 提交卸载
        → 收集 boot.wim / boot.sdi / BCD → 写 manifest.json → 打成 zip。
  产品的 PE 脚本不做进 wim，由 wimboot 在启动时注入，这里只准备可引导的环境。

  用管理员的「部署和映像工具环境」或普通管理员 PowerShell 运行：
    .\Build-NdWinPE.ps1 -WorkDir C:\ndwinpe-build -OutDir C:\ndwinpe
    .\Build-NdWinPE.ps1 -WorkDir C:\ndwinpe-build -OutDir C:\ndwinpe -DriverDir C:\drivers\nic
  产出 OutDir\ndwinpe-<内部版本>-<日期>.zip，上传到控制台「系统参数 → 适配环境」。
#>
[CmdletBinding()]
param(
  [string]$WorkDir = (Join-Path $env:TEMP 'ndwinpe-build'),
  [string]$OutDir = (Join-Path $PWD 'ndwinpe'),
  [string]$DriverDir,
  [string]$Arch = 'amd64'
)
$ErrorActionPreference = 'Stop'

if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw '请用管理员权限运行：DISM 挂载需要。'
}

# 定位 ADK：优先注册表，其次默认安装路径。
$kitsRoot = $null
foreach ($key in 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows Kits\Installed Roots', 'HKLM:\SOFTWARE\Microsoft\Windows Kits\Installed Roots') {
  if (Test-Path $key) { $kitsRoot = (Get-ItemProperty $key).KitsRoot10; if ($kitsRoot) { break } }
}
if (-not $kitsRoot) { $kitsRoot = "${env:ProgramFiles(x86)}\Windows Kits\10\" }
$adk = Join-Path $kitsRoot 'Assessment and Deployment Kit'
$peRoot = Join-Path $adk "Windows Preinstallation Environment\$Arch"
$dismDir = Join-Path $adk "Deployment Tools\$Arch\DISM"
$copype = Join-Path $adk 'Windows Preinstallation Environment\copype.cmd'
foreach ($p in $peRoot, $dismDir, $copype) {
  if (-not (Test-Path $p)) { throw "找不到 ADK 组件：$p。请安装 Windows ADK 与 WinPE 附加组件。" }
}
$dism = Join-Path $dismDir 'dism.exe'
$env:Path = "$dismDir;$env:Path"

if (Test-Path $WorkDir) { Remove-Item -Recurse -Force $WorkDir }
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

Write-Host "copype $Arch → $WorkDir"
& cmd.exe /c "`"$copype`" $Arch `"$WorkDir`"" | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'copype 失败' }

$wim = Join-Path $WorkDir 'media\sources\boot.wim'
$mount = Join-Path $WorkDir 'mount'
New-Item -ItemType Directory -Force -Path $mount | Out-Null
$ocs = Join-Path $peRoot 'WinPE_OCs'
$mounted = $false
try {
  & $dism /Mount-Image /ImageFile:$wim /Index:1 /MountDir:$mount | Out-Null
  if ($LASTEXITCODE -ne 0) { throw 'DISM 挂载 boot.wim 失败' }
  $mounted = $true

  # WMI 供脚本读硬件信息，Scripting 供 cmd 扩展；语言包按默认语言补齐，没有则跳过。
  foreach ($cab in 'WinPE-WMI.cab', 'WinPE-Scripting.cab') {
    & $dism /Image:$mount /Add-Package /PackagePath:(Join-Path $ocs $cab) | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "添加组件失败：$cab" }
    $lang = Get-ChildItem -Directory $ocs | Where-Object { Test-Path (Join-Path $_.FullName $cab) } | Select-Object -First 1
    if ($lang) {
      & $dism /Image:$mount /Add-Package /PackagePath:(Join-Path $lang.FullName $cab) | Out-Null
    }
  }
  if ($DriverDir) {
    if (-not (Test-Path $DriverDir)) { throw "驱动目录不存在：$DriverDir" }
    & $dism /Image:$mount /Add-Driver /Driver:$DriverDir /Recurse | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'DISM 添加驱动失败（未签名驱动需要 /ForceUnsigned，产品不接受未签名网卡驱动）' }
  }
  New-Item -ItemType Directory -Force -Path (Join-Path $mount 'ndpe') | Out-Null
  $buildInfo = & $dism /Image:$mount /Get-CurrentEdition 2>&1 | Out-String
} finally {
  if ($mounted) {
    & $dism /Unmount-Image /MountDir:$mount /Commit | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'DISM 提交卸载失败；请用 dism /Cleanup-Wim 清理后重试' }
  }
}

$info = & $dism /Get-WimInfo /WimFile:$wim /Index:1 | Out-String
if ($info -notmatch 'Version\s*:\s*([0-9.]+)') { throw "读不到 boot.wim 的版本：$info" }
$version = $Matches[1]
$build = ($version -split '\.')[2]

$stage = Join-Path $WorkDir 'stage'
New-Item -ItemType Directory -Force -Path $stage | Out-Null
Copy-Item $wim (Join-Path $stage 'boot.wim')
Copy-Item (Join-Path $WorkDir 'media\Boot\boot.sdi') (Join-Path $stage 'boot.sdi')
Copy-Item (Join-Path $WorkDir 'media\EFI\Microsoft\Boot\BCD') (Join-Path $stage 'BCD')

$files = @{}
foreach ($f in Get-ChildItem $stage -File) {
  $files[$f.Name] = (Get-FileHash -Algorithm SHA256 $f.FullName).Hash.ToLower()
}
$manifest = [ordered]@{
  kind         = 'ndwinpe'
  arch         = $Arch
  version      = $version
  build        = [int]$build
  source       = 'adk'
  drivers      = [bool]$DriverDir
  built_at     = (Get-Date).ToUniversalTime().ToString('o')
  files        = $files
}
$manifest | ConvertTo-Json -Depth 4 | Set-Content -Encoding utf8 (Join-Path $stage 'manifest.json')

$zip = Join-Path $OutDir ("ndwinpe-{0}-{1}.zip" -f $build, (Get-Date -Format 'yyyyMMdd'))
if (Test-Path $zip) { Remove-Item -Force $zip }
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $zip
Write-Host "WinPE 包已生成：$zip"
Write-Host "WinPE 版本：$version"
Write-Host "上传到控制台「系统参数 → 适配环境」即可；要适配的镜像 Windows 版本不能高于 $version。"
