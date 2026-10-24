package adapt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/storage"
)

func TestMountScriptVersionTracksScriptIdentity(t *testing.T) {
	// 版本代表「镜像是否已带脚本」，必须恰好在烘焙内容变化时变化。
	v8080 := MountScriptVersion("8080")
	if v8080 == "" {
		t.Fatal("version is empty; an image could never be marked as baked")
	}
	if v8080 != MountScriptVersion("8080") {
		t.Fatal("version is not stable across calls; every boot would re-inject")
	}
	if v8080 != MountScriptVersion(" 8080 ") || v8080 != MountScriptVersion("") {
		t.Fatal("version ignores the port normalization RenderMountScript applies")
	}
	if v8080 == MountScriptVersion("18080") {
		t.Fatal("changing the API port left the version unchanged; clients would keep calling the old port")
	}
}

// 版本必须包含注入布局：只哈希脚本内容时，布局变化后已烘焙镜像仍自称最新，开机路径会跳过重新注入。
func TestMountScriptVersionCoversTheInjectionLayout(t *testing.T) {
	bodyOnly := sha256.Sum256(RenderMountScript("8080"))
	if MountScriptVersion("8080") == hex.EncodeToString(bodyOnly[:])[:16] {
		t.Fatal("version hashes the script body alone; a layout change would not invalidate baked images")
	}
}

func TestLinuxMountScriptCarriesThePortAndItsOwnVersion(t *testing.T) {
	script := string(RenderLinuxMountScript("18080"))
	if !strings.Contains(script, "port='18080'") || strings.Contains(script, "__ND_API_PORT__") {
		t.Fatalf("port not rendered:\n%s", script)
	}
	if LinuxMountScriptVersion("8080") == MountScriptVersion("8080") || LinuxMountScriptVersion("8080") != LinuxMountScriptVersion("") {
		t.Fatal("linux version must differ from the Windows one and normalize the port")
	}
	if LinuxMountScriptVersion("8080") == LinuxMountScriptVersion("18080") {
		t.Fatal("port change left the version unchanged")
	}
}

// 开机脚本按服务端开关关闭自动更新、自动重启、睡眠和休眠；先读后写，已关好的不再调 powercfg。
func TestMountScriptEnforcesWindowsPolicyWhenAsked(t *testing.T) {
	script := string(RenderMountScript("8080"))
	for _, want := range []string{"$net.windows_policy", "NoAutoUpdate", "NoAutoRebootWithLoggedOnUsers", "HibernateEnabled", "STANDBYIDLE", "powercfg /hibernate off"} {
		if !strings.Contains(script, want) {
			t.Fatalf("开机脚本缺少 %q", want)
		}
	}
}

// 运行中读快照判断是否装完，脚本收尾要把 C: 缓存刷回盘，排在写 done.txt 和摘注册之后。

// 静态下发契约；真实 Storage cmdlet 行为仍需 Windows 验证。
func TestMountScriptWarnsBeforeChangingConflictingLetters(t *testing.T) {
	script := string(RenderMountScript("8080"))
	for _, want := range []string{
		"Get-Volume -ErrorAction Stop",
		"$managedLetters -notcontains [string]$volume.DriveLetter",
		"WARNING: lun ${lun}: target ${target}: occupied by",
		"$($volume.DriveType)", "$($volume.UniqueId)",
		"WARNING: all data disk drive letters unchanged; resolve the occupied target and retry",
		"if (-not $letterConflict)",
		"Set-Partition -NewDriveLetter $target -ErrorAction Stop",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("mount script missing %q", want)
		}
	}
	if strings.Index(script, "Get-Volume -ErrorAction Stop") > strings.Index(script, "Remove-PartitionAccessPath") {
		t.Fatal("conflict detection must precede releasing existing drive letters")
	}
}

// 镜像里只烘焙启动器，挂盘脚本开机时取最新的：版本只由布局和端口决定，脚本正文改了不让已烘焙镜像失效。
func TestMountScriptVersionIgnoresTheScriptBody(t *testing.T) {
	sum := sha256.Sum256([]byte(storage.MountScriptLayoutID() + "\x00" + "8080"))
	if MountScriptVersion("8080") != hex.EncodeToString(sum[:])[:16] {
		t.Fatal("版本仍随挂盘脚本正文变化；脚本一改，所有已烘焙镜像都会退回逐台注入")
	}
	// 启动器认这一行：下载到的不是挂盘脚本（比如出错页）就运行烘焙副本。
	if first, _, _ := strings.Cut(string(RenderMountScript("8080")), "\n"); strings.TrimSpace(first) != storage.MountScriptMarker {
		t.Fatalf("挂盘脚本首行 = %q，启动器认不出它", first)
	}
	// 启动器从烘焙副本里读端口。
	if !strings.Contains(string(RenderMountScript("18080")), "$port = '18080'") {
		t.Fatal("挂盘脚本里读不到端口")
	}
}

// Run the actual PowerShell body with deterministic WMI/network/time seams; no Windows disks or registry are touched.
func TestMountScriptRetriesDiscoveryAndCallbackWithinOneDeadline(t *testing.T) {
	pwsh := os.Getenv("ND_TEST_PWSH")
	if pwsh == "" {
		var err error
		pwsh, err = exec.LookPath("pwsh")
		if err != nil {
			t.Skip("PowerShell unavailable; set ND_TEST_PWSH to run the script harness")
		}
	}
	for _, mode := range []string{"late-wmi", "late-http", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := strings.Replace(string(RenderMountScript("8080")), `$work = 'C:\ndadapt'`, "$work = '"+strings.ReplaceAll(dir, "'", "''")+"'", 1)

			source := filepath.Join(dir, "mount.ps1")
			if err := os.WriteFile(source, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			harness := `param($Source, $Mode, $Result)
$global:ND_seconds=0; $global:ND_wmi=0; $global:ND_net=0; $global:ND_data=0
function Get-Date { param($Format) $d=[datetime]'2026-10-19T00:00:00Z'; $d=$d.AddSeconds($global:ND_seconds); if($Format){return $d.ToString($Format)};return $d }
function Start-Sleep {param($Seconds) $global:ND_seconds += $Seconds}
function whoami { 'mock-system' }
function Get-CimInstance {param($Namespace,$ClassName,$OperationTimeoutSec,$ErrorAction)
  if(!$ClassName){$ClassName=$Namespace}
  if($ClassName -eq 'MSiSCSIInitiator_SessionClass') {
    $global:ND_wmi++;if($Mode -eq 'late-wmi' -and $global:ND_wmi -lt 3){throw 'WMI not ready'}
    $ips=@('10.0.0.1');if($Mode -eq 'deadline'){$ips=1..20|ForEach-Object{"10.0.0.$_"}}
    return [pscustomobject]@{ConnectionInformation=@($ips|ForEach-Object{[pscustomobject]@{TargetAddress=$_;InitiatorAddress='10.0.0.2'}})}
  }
  return [pscustomobject]@{PhysicalAdapter=$true;MACAddress='00:11:22:33:44:55'}
}
function Invoke-RestMethod {param($Uri,$TimeoutSec)
  if($Uri -like '*net-config*') {
    $global:ND_net++
    if($Mode -eq 'deadline'){$global:ND_seconds+=$TimeoutSec;throw 'network not ready'}
    if($Mode -eq 'late-http' -and $global:ND_net -lt 3){$global:ND_seconds+=$TimeoutSec;throw 'network not ready'}
    return [pscustomobject]@{gateway='';windows_policy=$false}
  }
  $global:ND_data++; return [pscustomobject]@{items=@()}
}
& $Source
@{seconds=$global:ND_seconds;wmi=$global:ND_wmi;net=$global:ND_net;data=$global:ND_data}|ConvertTo-Json -Compress|Set-Content $Result
`
			hp, rp := filepath.Join(dir, "harness.ps1"), filepath.Join(dir, "result.json")
			if err := os.WriteFile(hp, []byte(harness), 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-File", hp, source, mode, rp).CombinedOutput(); err != nil {
				t.Fatalf("harness %v: %s", err, out)
			}
			b, err := os.ReadFile(rp)
			if err != nil {
				t.Fatal(err)
			}
			var got struct{ Seconds, Wmi, Net, Data int }
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if mode == "deadline" {
				if got.Seconds > 90 || got.Net < 2 || got.Data != 0 {
					t.Fatalf("deadline not shared across candidates: %+v", got)
				}
			} else if got.Net == 0 || got.Data != 1 || (mode == "late-wmi" && got.Wmi < 3) || (mode == "late-http" && got.Net < 3) {
				t.Fatalf("startup readiness was not retried: %+v", got)
			}
			log, err := os.ReadFile(filepath.Join(dir, "mount.log"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(log), "attempt") {
				t.Fatalf("retry diagnosis missing: %s", log)
			}
		})
	}
}

func TestBootstrapUsesShortBudgetThenBakedBodyOnce(t *testing.T) {
	pwsh := os.Getenv("ND_TEST_PWSH")
	if pwsh == "" {
		var err error
		pwsh, err = exec.LookPath("pwsh")
		if err != nil {
			t.Skip("PowerShell unavailable")
		}
	}
	dir := t.TempDir()
	boot := filepath.Join(dir, "bootstrap.ps1")
	fallback := filepath.Join(dir, storage.MountScriptName)
	result := filepath.Join(dir, "result.json")
	if err := os.WriteFile(boot, []byte(storage.MountBootstrapBody), 0600); err != nil {
		t.Fatal(err)
	}
	body := `$port = '8080'
$global:ND_baked++
`
	if err := os.WriteFile(fallback, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	harness := `param($Source,$Result)
$global:ND_seconds=0;$global:ND_baked=0
function Get-Date {param($Format) $d=([datetime]'2026-10-19T00:00:00Z').AddSeconds($global:ND_seconds);if($Format){return $d.ToString($Format)};return $d}
function Get-CimInstance {param($ClassName,$Namespace,$OperationTimeoutSec,$ErrorAction)
  if($Namespace){return [pscustomobject]@{ConnectionInformation=@(1..20|ForEach-Object{[pscustomobject]@{TargetAddress="10.0.0.$_"}})}}
  return [pscustomobject]@{PhysicalAdapter=$true;MACAddress='001122334455'}
}
function Invoke-WebRequest {param($Uri,$TimeoutSec,[switch]$UseBasicParsing,$ErrorAction) $global:ND_seconds+=$TimeoutSec;throw 'not ready'}
& $Source
@{seconds=$global:ND_seconds;baked=$global:ND_baked}|ConvertTo-Json -Compress|Set-Content $Result
`
	hp := filepath.Join(dir, "harness.ps1")
	if err := os.WriteFile(hp, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-File", hp, boot, result).CombinedOutput(); err != nil {
		t.Fatalf("bootstrap %v %s", err, out)
	}
	b, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Seconds, Baked int }
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Seconds > 15 || got.Baked != 1 {
		t.Fatalf("bootstrap exceeded budget or lost fallback: %+v", got)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "bootstrap.log")); err != nil {
		t.Fatalf("no persistent launcher diagnosis: %v", err)
	}
}
