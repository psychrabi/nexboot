<#
  ndiskless client startup script. The server adds it to the system clone's local machine GPO
  startup scripts on every boot (also included during driver adaptation). Runs as SYSTEM on every client boot:
    1) Derive server address from the current iSCSI session (necessarily reachable for diskless boot).
    2) GET /boot/net-config?mac= to restore default gateway and DNS. C-1 clears the gateway
       before sanhook to avoid bad Windows routes to same-subnet iSCSI targets. Windows configures the
       boot NIC statically from iBFT without DHCP. Restore routes without changing IP
       configuration so the iSCSI session remains intact. Same-subnet targets use direct routing; a /32
       host route provides an extra safeguard.
    3) GET /boot/data-disks?mac= for the data-disk LUN-to-drive-letter mapping. Identify
       iSCSI data disks by SCSI LUN and reconcile drive letters idempotently.
  Console configuration changes take effect on the next boot. Errors are logged without blocking startup.
  Log: C:\ndadapt\mount.log (overwritten on each boot; contains the most recent run only).
#>
$ErrorActionPreference = 'Continue'
$port = '__ND_API_PORT__'
$work = 'C:\ndadapt'
New-Item -ItemType Directory -Force -Path $work | Out-Null
$logFile = Join-Path $work 'mount.log'
"start $(Get-Date -Format o) as $(whoami)" | Out-File $logFile -Encoding utf8
function Log($m) { $m | Out-File $logFile -Append -Encoding utf8 }

# A data disk is any disk on the system iSCSI target with LUN >= 1; return Win32_DiskDrive including Index and SCSILogicalUnit.
function Find-DataDisk($lun) {
  $iscsiNums = @(Get-Disk | Where-Object { $_.BusType -eq 'iSCSI' } | ForEach-Object Number)
  Get-CimInstance Win32_DiskDrive | Where-Object {
    [int]$_.SCSILogicalUnit -eq [int]$lun -and $iscsiNums -contains [int]$_.Index
  } | Select-Object -First 1
}

# Select the largest normal data partition (skip recovery/reserved partitions).
function Get-DataPartition($diskIndex) {
  @(Get-Partition -DiskNumber $diskIndex -ErrorAction SilentlyContinue |
    Where-Object { $_.Type -in @('Basic', 'IFS', 'Logical', 'Huge') }) |
    Sort-Object Size -Descending | Select-Object -First 1
}

try {
  # 1) Obtain server and local addresses from the current iSCSI session.
  $sessions = @(Get-CimInstance -Namespace root/wmi -ClassName MSiSCSIInitiator_SessionClass -ErrorAction SilentlyContinue |
    ForEach-Object { $_.ConnectionInformation })
  $addrs = @($sessions | ForEach-Object { $_.TargetAddress } | Where-Object { $_ } | Sort-Object -Unique)
  $initAddrs = @($sessions | ForEach-Object { $_.InitiatorAddress } | Where-Object { $_ } | Sort-Object -Unique)
  if (-not $addrs.Count) { Log 'no iscsi session address; skip'; exit 0 }

  # Probe physical NIC MACs until the server finds a registered client.
  $macs = @(Get-CimInstance Win32_NetworkAdapter |
    Where-Object { $_.PhysicalAdapter -and $_.MACAddress } |
    ForEach-Object { $_.MACAddress } | Sort-Object -Unique)
  $net = $null
  $items = $null
  foreach ($addr in $addrs) {
    foreach ($mac in $macs) {
      try {
        $net = Invoke-RestMethod -Uri "http://${addr}:${port}/boot/net-config?mac=${mac}" -TimeoutSec 5
        try {
          $resp = Invoke-RestMethod -Uri "http://${addr}:${port}/boot/data-disks?mac=${mac}" -TimeoutSec 5
          $items = @($resp.items)
        } catch { Log "data-disks fetch failed: $($_.Exception.Message)" }
        Log "config from ${addr} mac=${mac}: net=$($net | ConvertTo-Json -Compress) disks=$($items | ConvertTo-Json -Compress)"
        break
      } catch { }
    }
    if ($null -ne $net) { break }
  }
  if ($null -eq $net) { Log 'net-config api unreachable; skip all' ; exit 0 }

  # 2) Restore gateway and DNS before data disks so networking becomes available promptly.
  $gw = "$($net.gateway)".Trim()
  if ($gw) {
    # The boot NIC owns the local endpoint address of the iSCSI session.
    $ifIndex = $null
    foreach ($ia in $initAddrs) {
      $ip = Get-NetIPAddress -AddressFamily IPv4 -IPAddress $ia -ErrorAction SilentlyContinue | Select-Object -First 1
      if ($ip) { $ifIndex = $ip.InterfaceIndex; break }
    }
    if ($null -eq $ifIndex) {
      Log "gateway: boot NIC not found (initiator addrs: $($initAddrs -join ','))"
    } else {
      # Pin the iSCSI server to a /32 direct route as an extra safeguard.
      foreach ($sa in $addrs) {
        if (-not (Get-NetRoute -DestinationPrefix "$sa/32" -InterfaceIndex $ifIndex -ErrorAction SilentlyContinue)) {
          try {
            New-NetRoute -DestinationPrefix "$sa/32" -InterfaceIndex $ifIndex -ErrorAction Stop | Out-Null
            Log "route: pinned $sa/32 on-link"
          } catch { Log "route: pin ${sa}: $($_.Exception.Message)" }
        }
      }
      $def = Get-NetRoute -DestinationPrefix '0.0.0.0/0' -InterfaceIndex $ifIndex -ErrorAction SilentlyContinue | Select-Object -First 1
      if ($def -and $def.NextHop -eq $gw) {
        Log "gateway: already $gw"
      } else {
        if ($def) {
          try { $def | Remove-NetRoute -Confirm:$false -ErrorAction Stop }
          catch { Log "gateway: remove stale default: $($_.Exception.Message)" }
        }
        try {
          New-NetRoute -DestinationPrefix '0.0.0.0/0' -InterfaceIndex $ifIndex -NextHop $gw -ErrorAction Stop | Out-Null
          Log "gateway: set $gw"
        } catch { Log "gateway: set ${gw}: $($_.Exception.Message)" }
      }
      $dns = @(@($net.dns) | ForEach-Object { "$_".Trim() } | Where-Object { $_ })
      if ($dns.Count) {
        try {
          Set-DnsClientServerAddress -InterfaceIndex $ifIndex -ServerAddresses $dns -ErrorAction Stop
          Log "dns: set $($dns -join ',')"
        } catch { Log "dns: $($_.Exception.Message)" }
      }
    }
  } else { Log 'no gateway configured; skip net restore' }

  # 3) Reconcile data disk drive letters.
  if ($null -eq $items) { Log 'data-disks api unreachable; skip disks'; exit 0 }

  # Desired LUN-to-drive-letter mapping (validated on server; validate here too).
  $targets = @{}
  foreach ($it in $items) {
    $letter = ("$($it.letter)").Trim().TrimEnd(':').ToUpper()
    if ($letter -match '^[D-Z]$') { $targets[[int]$it.lun] = $letter }
    else { Log "skip lun $($it.lun): bad letter '$($it.letter)'" }
  }
  if (-not $targets.Count) { Log 'no data disks configured; done'; exit 0 }

  # Wait for data-disk LUNs, which may become ready later than the system disk.
  $found = @{}
  for ($i = 0; $i -lt 30 -and $found.Count -lt $targets.Count; $i++) {
    foreach ($lun in @($targets.Keys)) {
      if (-not $found.ContainsKey($lun)) {
        $d = Find-DataDisk $lun
        if ($d) { $found[$lun] = [int]$d.Index }
      }
    }
    if ($found.Count -lt $targets.Count) { Start-Sleep -Seconds 1 }
  }
  foreach ($lun in @($targets.Keys)) {
    if (-not $found.ContainsKey($lun)) { Log "lun ${lun}: disk not found" }
  }

  # SAN policy may mark data disks offline/read-only; bring them online first.
  foreach ($lun in @($found.Keys)) {
    $gd = Get-Disk -Number $found[$lun] -ErrorAction SilentlyContinue
    if ($gd -and $gd.IsOffline) { Set-Disk -Number $gd.Number -IsOffline $false }
    if ($gd -and $gd.IsReadOnly) { Set-Disk -Number $gd.Number -IsReadOnly $false }
  }

  # Two passes: release incorrect letters on managed partitions, then assign desired letters (handling swaps like D↔E).
  foreach ($lun in @($found.Keys)) {
    $part = Get-DataPartition $found[$lun]
    if ($part -and $part.DriveLetter -and $part.DriveLetter -ne $targets[$lun]) {
      try { $part | Remove-PartitionAccessPath -AccessPath "$($part.DriveLetter):\" }
      catch { Log "lun ${lun}: free $($part.DriveLetter): failed: $($_.Exception.Message)" }
    }
  }
  foreach ($lun in @($found.Keys | Sort-Object)) {
    $target = $targets[$lun]
    $part = Get-DataPartition $found[$lun]
    if (-not $part) { Log "lun ${lun}: no data partition"; continue }
    if ($part.DriveLetter -eq $target) { Log "lun ${lun}: already ${target}:"; continue }
    try {
      $part | Set-Partition -NewDriveLetter $target
      Log "lun ${lun}: set ${target}:"
    } catch { Log "lun ${lun}: set ${target}: failed: $($_.Exception.Message)" }
  }
} catch {
  Log "exception: $($_.Exception.Message)"
}
Log "done $(Get-Date -Format o)"
