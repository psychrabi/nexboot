<#
  ndiskless startup driver installation script, injected offline by the server as a local machine GPO startup script.
  Runs once as SYSTEM when the Super Admin client boots:
    1) Extract C:\ndadapt\bundle.zip to C:\ndadapt\drivers.
    2) Preinstall all INF packages using PnPUtil.
    3) Register NIC driver services and the iSCSI stack (msiscsi/iScsiPrt) for boot-start (Start=0),
       and write hardware-ID-to-service mappings into CriticalDeviceDatabase (CDDB) to avoid 0x7B when switching NICs.
    4) Write installation status to C:\ndadapt\done.txt and adapt.log, then unregister its own GPO startup entry.
  **Do not shut down automatically.** The operator verifies driver installation inside Windows, shuts down the client,
  and uses Shut Down and Save Restore Point in the console to commit changes.
#>
$ErrorActionPreference = 'Continue'
$work = 'C:\ndadapt'
$done = Join-Path $work 'done.txt'
$exit = 0
function Log($m) { $m | Out-File (Join-Path $work 'adapt.log') -Append -Encoding utf8 }

New-Item -ItemType Directory -Force -Path $work | Out-Null
Log ("start " + (Get-Date -Format o) + " as " + (whoami))

try {
  # 1) Extract driver bundle
  $drivers = Join-Path $work 'drivers'
  Remove-Item -Recurse -Force $drivers -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force -Path $drivers | Out-Null
  $zip = Join-Path $work 'bundle.zip'
  if (Test-Path $zip) {
    Expand-Archive -Path $zip -DestinationPath $drivers -Force
  }
  $infs = Get-ChildItem -Path $drivers -Recurse -Filter *.inf -ErrorAction SilentlyContinue
  Log ("inf count = " + $infs.Count)

  # 2) Preinstall each INF with PnPUtil (BundleArchive contains one directory per package).
  foreach ($inf in $infs) {
    & pnputil.exe /add-driver $inf.FullName /install 2>&1 | Out-File (Join-Path $work 'pnputil.log') -Append -Encoding utf8
    if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 259) { $exit = 1; Log "pnputil $($inf.Name) returned $LASTEXITCODE" }
  }

  # 3) Set iSCSI stack services to boot-start
  foreach ($svc in @('msiscsi', 'iScsiPrt')) {
    $key = "HKLM:\SYSTEM\CurrentControlSet\Services\$svc"
    if (Test-Path $key) { Set-ItemProperty -Path $key -Name Start -Value 0 -Type DWord }
    elseif ($svc -eq 'msiscsi') { $exit = 2; Log "msiscsi service missing" }
  }
  # NIC service boot-start and CDDB registration
  foreach ($inf in $infs) {
    $svcNames = Select-String -Path $inf.FullName -Pattern 'AddService\s*=\s*([^,\s]+)' -AllMatches |
      ForEach-Object { $_.Matches } | ForEach-Object { $_.Groups[1].Value } | Sort-Object -Unique
    $hwids = Select-String -Path $inf.FullName -Pattern '(PCI\\VEN_[0-9A-Fa-f]{4}&DEV_[0-9A-Fa-f]{4}[^,\s"]*)' -AllMatches |
      ForEach-Object { $_.Matches } | ForEach-Object { $_.Groups[1].Value } | Sort-Object -Unique
    foreach ($svc in $svcNames) {
      $key = "HKLM:\SYSTEM\CurrentControlSet\Services\$svc"
      if (Test-Path $key) {
        Set-ItemProperty -Path $key -Name Start -Value 0 -Type DWord
        foreach ($hwid in $hwids) {
          $cddbId = ($hwid -replace '\\', '#').ToUpper()
          if ($cddbId -match '^(PCI#VEN_[0-9A-F]{4}&DEV_[0-9A-F]{4})') {
            $cddbKey = "HKLM:\SYSTEM\CurrentControlSet\Control\CriticalDeviceDatabase\$($Matches[1])"
            if (-not (Test-Path $cddbKey)) { New-Item -Path $cddbKey -Force | Out-Null }
            Set-ItemProperty -Path $cddbKey -Name Service -Value $svc -Type String
            Set-ItemProperty -Path $cddbKey -Name ClassGUID -Value '{4d36e972-e325-11ce-bfc1-08002be10318}' -Type String
          }
        }
      } elseif ($exit -eq 0) { $exit = 1; Log "service $svc not registered" }
    }
  }
} catch {
  $exit = 3; Log ("exception: " + $_.Exception.Message)
}

# 4) Installation diagnostics (not an automatic success signal).
"OK=$($exit -eq 0) EXIT=$exit AT=$(Get-Date -Format o)" | Out-File $done -Encoding utf8

# 5) Remove only our own startup script registration so it runs once, not on every subsequent boot after committing a restore point.
#    Preserve existing image GPO entries and ndmount; delete ndadapt.cmd only, then increment gpt.ini version for gpsvc.
function Remove-AdaptRegistration([string]$gp) {
  $ini = Join-Path $gp 'Machine\Scripts\scripts.ini'
  if (Test-Path $ini) {
    $bytes = [IO.File]::ReadAllBytes($ini)
    $utf16 = $bytes.Length -ge 2 -and $bytes[0] -eq 0xFF -and $bytes[1] -eq 0xFE
    $enc = if ($utf16) { New-Object Text.UnicodeEncoding($false, $true) } else { [Text.Encoding]::Default }
    $text = $enc.GetString($bytes).TrimStart([char]0xFEFF)
    $out = New-Object System.Collections.Generic.List[string]
    $items = New-Object System.Collections.Generic.List[object]
    $inStartup = $false
    foreach ($line in ($text -split "`r?`n")) {
      if ($line -match '^\s*\[(.+)\]\s*$') {
        if ($inStartup) { Add-StartupItems $out $items }
        $inStartup = ($Matches[1] -ieq 'Startup')
        $out.Add($line); continue
      }
      if ($inStartup -and $line -match '^\s*(\d+)(CmdLine|Parameters)=(.*)$') {
        $n = [int]$Matches[1]
        $item = $items | Where-Object { $_.N -eq $n } | Select-Object -First 1
        if (-not $item) { $item = [pscustomobject]@{ N = $n; CmdLine = ''; Parameters = '' }; $items.Add($item) }
        $item.($Matches[2]) = $Matches[3]
        continue
      }
      if ($inStartup -and $line.Trim() -eq '') { continue } # Blank lines inside the section would shift entries down.
      $out.Add($line)
    }
    if ($inStartup) { Add-StartupItems $out $items }
    [IO.File]::WriteAllText($ini, (($out -join "`r`n").TrimEnd() + "`r`n"), $enc)
  }
  $startup = Join-Path $gp 'Machine\Scripts\Startup'
  Remove-Item -Force (Join-Path $startup 'adapt.ps1'), (Join-Path $startup 'ndadapt.cmd') -ErrorAction SilentlyContinue
  $gpt = Join-Path $gp 'gpt.ini'
  if (Test-Path $gpt) {
    $g = [IO.File]::ReadAllText($gpt)
    $g = [regex]::Replace($g, '(?m)^Version=(\d+)', { param($m) 'Version=' + ([int64]$m.Groups[1].Value + 1) })
    [IO.File]::WriteAllText($gpt, $g, [Text.Encoding]::Default)
  }
}

# Rewrite [Startup] entries excluding ndadapt.cmd, renumbering sequentially from zero.
function Add-StartupItems($out, $items) {
  $k = 0
  foreach ($it in ($items | Sort-Object N)) {
    if ($it.CmdLine -ieq 'ndadapt.cmd') { continue }
    $out.Add("$($k)CmdLine=$($it.CmdLine)"); $out.Add("$($k)Parameters=$($it.Parameters)"); $k++
  }
  $items.Clear()
}

$gp = 'C:\Windows\System32\GroupPolicy'
try {
  Remove-AdaptRegistration $gp
} catch {
  # If merging fails, use the legacy fallback: preventing repeated adaptation is more important than preserving existing image startup policy.
  Log "remove adapt registration failed, falling back: $_"
  $startup = Join-Path $gp 'Machine\Scripts\Startup'
  if (Test-Path (Join-Path $startup 'ndmount.cmd')) {
    "[Startup]`r`n0CmdLine=ndmount.cmd`r`n0Parameters=" |
      Out-File (Join-Path $gp 'Machine\Scripts\scripts.ini') -Encoding ascii
    Remove-Item -Force (Join-Path $startup 'adapt.ps1'), (Join-Path $startup 'ndadapt.cmd') -ErrorAction SilentlyContinue
  } else {
    Remove-Item -Recurse -Force $gp -ErrorAction SilentlyContinue
  }
}

# Do not shut down automatically; operators verify installed drivers, shut down, and save a restore point.
Log "install done exit=$exit; drivers installed, awaiting user shutdown + save reduction"
