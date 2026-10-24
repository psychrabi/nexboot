package pe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/boot/tftpassets"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func newSessionFixture(t *testing.T) (SessionService, *store.SQLStore, string) {
	t.Helper()
	st, err := store.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	img := domain.Image{ID: "win11", Name: "Win11", OSType: domain.OSTypeWindows, Purpose: domain.ImagePurposeSystem, State: domain.ImageStateNormal, CreatedAt: now}
	red := "win11_0"
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Images().Create(ctx, img))
	must(st.Configs().Create(ctx, domain.Config{ID: "win11_default", ImageID: "win11", Name: "default", DefaultReductionID: &red, CreatedAt: now}))
	must(st.Reductions().Create(ctx, domain.Reduction{ID: red, ConfigID: "win11_default", Name: "@0", Status: domain.ReductionStatusReady, CreatedAt: now}))
	must(st.Groups().Create(ctx, domain.Group{ID: "g1", Name: "windows", StartIP: "192.168.10.40", ClientMax: 10, Netmask: "255.255.255.0", SystemImageID: "win11", SystemConfigID: "win11_default", SystemReductionID: red}))
	must(st.Terminals().Create(ctx, domain.Terminal{ID: "t1", Name: "前台02", MAC: "0050562EF64E", IP: "192.168.10.40", GroupID: "g1", State: domain.TerminalStateOffline}))
	must(st.Terminals().Create(ctx, domain.Terminal{ID: "t2", Name: "在线机", MAC: "0050562EF64F", IP: "192.168.10.41", GroupID: "g1", State: domain.TerminalStateOnline}))
	filesDir := t.TempDir()
	envDir := filepath.Join(filesDir, "winpe", "win11")
	must(os.MkdirAll(envDir, 0o755))
	must(os.WriteFile(filepath.Join(envDir, "boot.wim"), []byte("wim"), 0o644))
	must(os.WriteFile(filepath.Join(envDir, "boot.sdi"), []byte("sdi"), 0o644))
	must(os.WriteFile(filepath.Join(envDir, "curl.exe"), []byte("curl"), 0o644))
	must(os.WriteFile(filepath.Join(envDir, "BCD"), []byte("regf-efi"), 0o644))
	must(os.WriteFile(filepath.Join(envDir, "BCD.pcat"), []byte("regf-pcat"), 0o644))
	must(st.WinPEEnvs().Create(ctx, domain.WinPEEnv{ID: "winpe-win11", Kind: domain.WinPEEnvImageWinRE, ImageID: "win11", Version: "10.0.22621.4037", Build: 22621, Dir: envDir,
		Files: map[string]string{"boot.wim": "a", "boot.sdi": "b", "curl.exe": "c", "BCD": "e", "BCD.pcat": "p"}, CreatedAt: now}))
	bcdDir := filepath.Join(filesDir, "winpe", "bcd")
	must(os.MkdirAll(bcdDir, 0o755))
	must(os.WriteFile(filepath.Join(bcdDir, "BCD"), []byte("regfbcd"), 0o644))
	must(st.WinPEEnvs().Create(ctx, domain.WinPEEnv{ID: "winpe-bcd", Kind: domain.WinPEEnvBCD, Dir: bcdDir, Files: map[string]string{"BCD": "d"}, CreatedAt: now}))
	svc := SessionService{Store: st, FilesDir: func(context.Context) (string, error) { return filesDir, nil },
		Now: func() time.Time { return now }, ReportBase: "http://192.168.10.250:8080"}
	return svc, st, filesDir
}

func TestStartProbeCreatesSessionAndScript(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.PESessions().Get(ctx, res.SessionID)
	if err != nil || sess.State != domain.PEStatePending || sess.Mode != domain.PEModeProbe || sess.EnvID != "winpe-win11" || sess.BCDID != "winpe-win11" || len(sess.Token) < 32 {
		t.Fatalf("session = %+v err = %v", sess, err)
	}
	if !sess.ExpiresAt.Equal(svc.Now().Add(DefaultTTL)) {
		t.Fatalf("expires = %v", sess.ExpiresAt)
	}
	task, err := st.Tasks().Get(ctx, res.TaskID)
	if err != nil || task.Type != domain.TaskTypePEProbe || task.Status == domain.TaskStatusSuccess {
		t.Fatalf("task = %+v err = %v", task, err)
	}

	active, ok, err := svc.Active(ctx, "t1")
	if err != nil || !ok || active.ID != sess.ID {
		t.Fatalf("active = %+v ok = %v err = %v", active, ok, err)
	}
	terminal, _ := st.Terminals().Get(ctx, "t1")
	script, err := svc.Script(ctx, active, terminal)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#!ipxe",
		"set initiator-iqn iqn.2026-06.local.ndiskless:initiator-0050562ef64e",
		"kernel /boot/pe/" + sess.Token + "/wimboot",
		"initrd /boot/pe/" + sess.Token + "/BCD?platform=${platform} BCD",
		"initrd /boot/pe/" + sess.Token + "/boot.sdi boot.sdi",
		"initrd /boot/pe/" + sess.Token + "/winpeshl.ini winpeshl.ini",
		"initrd /boot/pe/" + sess.Token + "/ndpe.ini ndpe.ini",
		"initrd /boot/pe/" + sess.Token + "/ndpe.cmd ndpe.cmd",
		"initrd /boot/pe/" + sess.Token + "/curl.exe curl.exe",
		"initrd /boot/pe/" + sess.Token + "/boot.wim boot.wim",
		"boot || goto nd-pe-failed",
		"set nd-pe-errno ${errno}",
		"imgfetch /boot/pe/" + sess.Token + "/failed?err=${nd-pe-errno}&platform=${platform}",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "sanhook") {
		t.Fatal("探测模式不挂盘")
	}
	// boot.wim 必须最后加载：wimboot 按顺序把前面的文件注入进去。
	if strings.LastIndex(script, "boot.wim") < strings.LastIndex(script, "ndpe.cmd") {
		t.Fatal("boot.wim 要排在注入文件之后")
	}
}

func TestStartProbeRejections(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	if _, err := svc.StartProbe(ctx, "t2", ProbeRequest{}); err == nil || !strings.Contains(err.Error(), "在线机") {
		t.Fatalf("在线终端要点名拒绝：%v", err)
	}
	if _, err := svc.StartProbe(ctx, "t1", ProbeRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartProbe(ctx, "t1", ProbeRequest{}); err == nil || !strings.Contains(err.Error(), "前台02") {
		t.Fatalf("已有进行中会话要拒绝：%v", err)
	}
	if err := st.WinPEEnvs().Delete(ctx, "winpe-bcd"); err != nil {
		t.Fatal(err)
	}
	st.Terminals().Create(ctx, domain.Terminal{ID: "t3", Name: "三号", MAC: "0050562EF650", IP: "192.168.10.42", GroupID: "g1", State: domain.TerminalStateOffline})
	noBCD := domain.WinPEEnv{ID: "winpe-adk-nobcd", Kind: domain.WinPEEnvADK, Version: "10.0.26100.1", Build: 26100, Dir: t.TempDir(), Files: map[string]string{"boot.wim": "x", "boot.sdi": "y"}, CreatedAt: svc.Now()}
	st.WinPEEnvs().Create(ctx, noBCD)
	if _, err := svc.StartProbe(ctx, "t3", ProbeRequest{EnvID: noBCD.ID}); err == nil || !strings.Contains(err.Error(), "BCD") {
		t.Fatalf("环境没有 BCD 又没上传时要说清：%v", err)
	}
}

func TestStartProbeFallsBackToADKAndRequiresSomeEnvironment(t *testing.T) {
	svc, st, filesDir := newSessionFixture(t)
	ctx := context.Background()
	if err := st.WinPEEnvs().Delete(ctx, "winpe-win11"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartProbe(ctx, "t1", ProbeRequest{}); err == nil || !strings.Contains(err.Error(), "恢复环境") {
		t.Fatalf("没有任何 WinPE 来源要指引：%v", err)
	}
	adkDir := filepath.Join(filesDir, "winpe", "adk-1")
	os.MkdirAll(adkDir, 0o755)
	os.WriteFile(filepath.Join(adkDir, "boot.wim"), []byte("w"), 0o644)
	os.WriteFile(filepath.Join(adkDir, "boot.sdi"), []byte("s"), 0o644)
	os.WriteFile(filepath.Join(adkDir, "BCD"), []byte("regf"), 0o644)
	st.WinPEEnvs().Create(ctx, domain.WinPEEnv{ID: "winpe-adk-1", Kind: domain.WinPEEnvADK, Version: "10.0.26100.1", Build: 26100, Dir: adkDir, Files: map[string]string{"boot.wim": "x", "boot.sdi": "y", "BCD": "z"}, CreatedAt: svc.Now()})
	st.WinPEEnvs().Delete(ctx, "winpe-bcd")
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	if sess.EnvID != "winpe-adk-1" || sess.BCDID != "winpe-adk-1" {
		t.Fatalf("env = %s bcd = %s", sess.EnvID, sess.BCDID)
	}
	terminal, _ := st.Terminals().Get(ctx, "t1")
	script, _ := svc.Script(ctx, sess, terminal)
	if strings.Contains(script, "curl.exe") {
		t.Fatal("ADK 包没有 curl.exe 时脚本不能引用它")
	}
}

func TestSessionFilesAreServedByTokenAndRenderedConfig(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	res, _ := svc.StartProbe(ctx, "t1", ProbeRequest{})
	sess, _ := st.PESessions().Get(ctx, res.SessionID)

	f, err := svc.File(ctx, sess.Token, "wimboot", "efi")
	wimboot, _ := tftpassets.Read("wimboot")
	if err != nil || !bytes.Equal(f.Content, wimboot) {
		t.Fatalf("wimboot: %v", err)
	}
	if f, err = svc.File(ctx, sess.Token, "BCD", "efi"); err != nil || !strings.HasSuffix(f.Path, filepath.Join("win11", "BCD")) {
		t.Fatalf("BCD = %+v err = %v", f, err)
	}
	// BIOS 固件拿 PCAT 版；环境没有 PCAT 版时退回 EFI 版。
	if f, err = svc.File(ctx, sess.Token, "BCD", "pcbios"); err != nil || !strings.HasSuffix(f.Path, filepath.Join("win11", "BCD.pcat")) {
		t.Fatalf("BCD pcbios = %+v err = %v", f, err)
	}
	if f, err = svc.File(ctx, sess.Token, "boot.wim", "efi"); err != nil || !strings.HasSuffix(f.Path, filepath.Join("win11", "boot.wim")) {
		t.Fatalf("boot.wim = %+v err = %v", f, err)
	}
	f, err = svc.File(ctx, sess.Token, "ndpe.ini", "efi")
	if err != nil {
		t.Fatal(err)
	}
	ini := string(f.Content)
	for _, want := range []string{"mode=probe\r\n", "token=" + sess.Token + "\r\n", "report=http://192.168.10.250:8080/boot/pe/" + sess.Token + "\r\n", "mac=00:50:56:2E:F6:4E\r\n"} {
		if !strings.Contains(ini, want) {
			t.Errorf("ndpe.ini missing %q:\n%s", want, ini)
		}
	}
	if f, err = svc.File(ctx, sess.Token, "winpeshl.ini", "efi"); err != nil || !strings.Contains(string(f.Content), "ndpe.cmd") || !strings.Contains(string(f.Content), "\r\n") {
		t.Fatalf("winpeshl.ini = %q err = %v", f.Content, err)
	}
	for _, name := range []string{"ndpe.cmd", "ndpe-common.cmd", "ndpe-report.js"} {
		if f, err = svc.File(ctx, sess.Token, name, "efi"); err != nil || len(f.Content) == 0 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err = svc.File(ctx, sess.Token, "../etc/passwd", "efi"); err == nil {
		t.Fatal("未知文件名要拒绝")
	}
	if _, err = svc.File(ctx, "nope", "BCD", "efi"); err == nil {
		t.Fatal("错 token 要拒绝")
	}
}

// CMD reads batch files using the current console code page. Unicode messages
// belong in the UTF-16 JScript payload, so the batch payload is ASCII on any PE.
func TestSessionBatchPayloadsAreCodePageIndependent(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	for _, name := range []string{"ndpe.cmd", "ndpe-common.cmd", "ndadapt-pe.cmd", "ndidentity.cmd"} {
		t.Run(name, func(t *testing.T) {
			file, err := svc.File(ctx, sess.Token, name, "efi")
			if err != nil {
				t.Fatal(err)
			}
			for i, b := range file.Content {
				if b > 127 {
					t.Fatalf("batch payload depends on console code page at byte %d", i)
				}
			}
		})
	}
}

func TestSessionLifecycleAndTask(t *testing.T) {
	svc, st, filesDir := newSessionFixture(t)
	ctx := context.Background()
	res, _ := svc.StartProbe(ctx, "t1", ProbeRequest{})
	sess, _ := st.PESessions().Get(ctx, res.SessionID)

	if err := svc.MarkBooted(ctx, sess.Token); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.PESessions().Get(ctx, sess.ID); s.State != domain.PEStateBooted {
		t.Fatalf("state = %s", s.State)
	}
	if err := svc.SaveLog(ctx, sess.Token, "console.log", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(filesDir, "pe-sessions", sess.ID, "logs", "console.log")); err != nil || string(b) != "hello" {
		t.Fatalf("log = %q err = %v", b, err)
	}
	if err := svc.SaveLog(ctx, sess.Token, "../../x", strings.NewReader("x")); err == nil {
		t.Fatal("日志文件名要走白名单")
	}
	var done []domain.PESession
	svc.OnDone = func(_ context.Context, s domain.PESession, r PEResult) error { done = append(done, s); return nil }
	if err := svc.Complete(ctx, sess.Token, PEResult{OK: true, Stage: "probe", Message: "WinPE 引导与回报正常"}); err != nil {
		t.Fatal(err)
	}
	s, _ := st.PESessions().Get(ctx, sess.ID)
	task, _ := st.Tasks().Get(ctx, res.TaskID)
	if s.State != domain.PEStateDone || task.Status != domain.TaskStatusSuccess || len(done) != 1 {
		t.Fatalf("session = %+v task = %+v done = %d", s, task, len(done))
	}
	if _, err := svc.File(ctx, sess.Token, "BCD", "efi"); err == nil {
		t.Fatal("会话结束后 token 失效")
	}
	if _, ok, _ := svc.Active(ctx, "t1"); ok {
		t.Fatal("完成后不再有进行中会话")
	}

	// 失败回报进任务错误，文案给操作者。
	res2, _ := svc.StartProbe(ctx, "t1", ProbeRequest{})
	sess2, _ := st.PESessions().Get(ctx, res2.SessionID)
	if err := svc.Complete(ctx, sess2.Token, PEResult{OK: false, Stage: "probe", Message: "探测步骤返回 1"}); err != nil {
		t.Fatal(err)
	}
	task2, _ := st.Tasks().Get(ctx, res2.TaskID)
	if task2.Status != domain.TaskStatusFailed || !strings.Contains(task2.Error, "探测步骤返回 1") {
		t.Fatalf("task = %+v", task2)
	}
}

func TestExpireStaleFailsSessionsAndTasks(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	res, _ := svc.StartProbe(ctx, "t1", ProbeRequest{})
	svc.Now = func() time.Time { return time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC).Add(DefaultTTL + time.Minute) }
	n, err := svc.ExpireStale(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n = %d err = %v", n, err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	task, _ := st.Tasks().Get(ctx, res.TaskID)
	if sess.State != domain.PEStateFailed || task.Status != domain.TaskStatusFailed || !strings.Contains(task.Error, "超时") {
		t.Fatalf("session = %+v task = %+v", sess, task)
	}
	// 过期后 Active 也不再返回它，客户机下次开机走正常路径。
	if _, ok, _ := svc.Active(ctx, "t1"); ok {
		t.Fatal("过期会话不该仍算进行中")
	}
}

func TestPEFilesFollowLocalLibraryAfterPoolChanges(t *testing.T) {
	svc, st, filesDir := newSessionFixture(t)
	ctx := context.Background()
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	env, _ := st.WinPEEnvs().Get(ctx, sess.EnvID)
	env.Dir = "/ndiskless/old-pool/nd/files/winpe/win11"
	if err := st.WinPEEnvs().Update(ctx, env); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"boot.wim", "boot.sdi", "curl.exe", "BCD"} {
		file, err := svc.File(ctx, sess.Token, name, "efi")
		want := filepath.Join(filesDir, "winpe", "win11", name)
		if err != nil || file.Path != want {
			t.Fatalf("%s path=%s want=%s err=%v", name, file.Path, want, err)
		}
	}
	if err := (&EnvService{Store: st, Storage: &fakeStorage{filesDir: filesDir}}).Delete(ctx, env.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "winpe", "win11")); !os.IsNotExist(err) {
		t.Fatalf("local environment not removed: %v", err)
	}
}

func TestPEInjectsDriverFilesWithUEFIAndBIOSNamesAndPreservesDirectories(t *testing.T) {
	svc, st, filesDir := newSessionFixture(t)
	ctx := context.Background()
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	dir := filepath.Join(filesDir, "pe-sessions", sess.ID, "drivers", "Win10")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vmxnet3.inf", "vmxnet3.sys"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	terminal, _ := st.Terminals().Get(ctx, "t1")
	script, err := svc.Script(ctx, sess, terminal)
	if err != nil {
		t.Fatal(err)
	}
	// UEFI uses the image name (--name); BIOS uses the first argument.
	// Both must agree with the manifest, even though the download URL is indexed.
	for _, line := range []string{
		"initrd --name nd-file-0000 /boot/pe/" + sess.Token + "/drivers/0000 nd-file-0000 || goto nd-pe-failed",
		"initrd --name nd-file-0001 /boot/pe/" + sess.Token + "/drivers/0001 nd-file-0001 || goto nd-pe-failed",
	} {
		if !strings.Contains(script, line) {
			t.Fatalf("script missing compatible driver injection: %s", line)
		}
	}
	manifest, err := svc.File(ctx, sess.Token, "nd-driver-files.txt", "")
	if err != nil || string(manifest.Content) != "nd-file-0000|Win10\\vmxnet3.inf\r\nnd-file-0001|Win10\\vmxnet3.sys\r\n" {
		t.Fatalf("manifest %s %v", manifest.Content, err)
	}
	file, err := svc.File(ctx, sess.Token, "drivers/0001", "")
	if err != nil || file.Name != "nd-file-0001" || !strings.HasSuffix(file.Path, "vmxnet3.sys") {
		t.Fatalf("file %+v %v", file, err)
	}
}

// 镜像还没提取过恢复环境时，开始会话会当场提取（EnsureEnv），不用运维先去别处点一下。
func TestStartProbeExtractsWinREOnDemand(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	env, _ := st.WinPEEnvs().Get(ctx, "winpe-win11")
	if err := st.WinPEEnvs().Delete(ctx, "winpe-win11"); err != nil {
		t.Fatal(err)
	}
	asked := ""
	svc.EnsureEnv = func(ctx context.Context, imageID string) (domain.WinPEEnv, error) {
		asked = imageID
		if err := st.WinPEEnvs().Create(ctx, env); err != nil {
			return domain.WinPEEnv{}, err
		}
		return env, nil
	}
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil || asked != "win11" {
		t.Fatalf("err = %v asked = %q", err, asked)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	if sess.EnvID != "winpe-win11" {
		t.Fatalf("env = %s", sess.EnvID)
	}
	// 提取失败且没有 ADK 包：错误里带原因，并指向镜像详情。
	sess.State = domain.PEStateDone
	_ = st.PESessions().Update(ctx, sess)
	st.WinPEEnvs().Delete(ctx, "winpe-win11")
	svc.EnsureEnv = func(context.Context, string) (domain.WinPEEnv, error) {
		return domain.WinPEEnv{}, errors.New("镜像里没有恢复环境（Winre.wim）")
	}
	_, err = svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err == nil || !strings.Contains(err.Error(), "Win11") || !strings.Contains(err.Error(), "镜像详情") || !strings.Contains(err.Error(), "Winre.wim") {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionINIAllowsOnlyKnownServicingBuildPairs(t *testing.T) {
	for _, tc := range []struct{ build, wantMax int }{{18362, 18363}, {19041, 19045}, {22621, 22631}, {26100, 26200}, {26200, 26200}, {26000, 26000}, {28000, 28000}} {
		t.Run(fmt.Sprint(tc.build), func(t *testing.T) {
			svc, st, _ := newSessionFixture(t)
			ctx := context.Background()
			env, err := st.WinPEEnvs().Get(ctx, "winpe-win11")
			if err != nil {
				t.Fatal(err)
			}
			env.Build = tc.build
			if err := st.WinPEEnvs().Update(ctx, env); err != nil {
				t.Fatal(err)
			}
			res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
			if err != nil {
				t.Fatal(err)
			}
			sess, err := st.PESessions().Get(ctx, res.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			file, err := svc.File(ctx, sess.Token, "ndpe.ini", "efi")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{fmt.Sprintf("pe_build=%d\r\n", tc.build), fmt.Sprintf("pe_max_build=%d\r\n", tc.wantMax)} {
				if !strings.Contains(string(file.Content), want) {
					t.Fatalf("ndpe.ini lacks %q: %s", want, file.Content)
				}
			}
		})
	}
}

func TestSessionFailureHintsPreserveTheActualEnvironmentOrDISMCause(t *testing.T) {
	for _, tc := range []struct{ stage, message string }{
		{"environment", "加载镜像SOFTWARE注册表失败"},
		{"environment", "无法读取镜像CurrentBuildNumber"},
		{"environment", "Windows版本超出当前恢复环境的兼容上限"},
		{"adapter", "DISM未生成网卡接口，请检查任务DISM日志"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			svc, st, _ := newSessionFixture(t)
			ctx := context.Background()
			res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
			if err != nil {
				t.Fatal(err)
			}
			sess, err := st.PESessions().Get(ctx, res.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Complete(ctx, sess.Token, PEResult{OK: false, Stage: tc.stage, Message: tc.message}); err != nil {
				t.Fatal(err)
			}
			task, err := st.Tasks().Get(ctx, res.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != domain.TaskStatusFailed || !strings.Contains(task.Error, tc.message) || !strings.Contains(task.Error, "任务详情") {
				t.Fatalf("task=%+v", task)
			}
			if strings.Contains(task.Error, "上传") || strings.Contains(task.Error, "镜像详情") {
				t.Fatalf("misleading task error: %s", task.Error)
			}
		})
	}
}

func TestAdaptPayloadKeepsVersionGuardAndDISMFailureEvidence(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.PESessions().Get(ctx, res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	file, err := svc.File(ctx, sess.Token, "ndadapt-pe.cmd", "efi")
	if err != nil {
		t.Fatal(err)
	}
	script := string(file.Content)
	for _, want := range []string{
		"echo Windows build=!OSBUILD! WinPE build=%ND_PE_BUILD% compatible max=%ND_PE_MAX_BUILD%",
		"if defined ND_PE_MAX_BUILD if !OSBUILD! GTR !ND_PE_MAX_BUILD! exit /b 20",
		"echo DISM /Add-Driver exit=!errorlevel!",
		"echo DISM /Add-NetAdapter exit=!errorlevel!",
		"exit /b 12", "exit /b 14",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("adapt payload missing %q", want)
		}
	}
}
