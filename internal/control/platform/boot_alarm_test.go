package platform

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

var bootAlarmNow = time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)

func seedBootAlarm(t *testing.T, terminal domain.Terminal, failure domain.BootFailure) store.Store {
	t.Helper()
	ctx := context.Background()
	st := newAlarmStore(t)
	img := domain.Image{ID: "ubuntu", Name: "ubuntu", OSType: domain.OSTypeLinux, State: domain.ImageStateNormal, CreatedAt: bootAlarmNow}
	cfg := domain.Config{ID: "ubuntu_default", ImageID: img.ID, Name: "default", CreatedAt: bootAlarmNow}
	red := domain.Reduction{ID: "ubuntu_0", ConfigID: cfg.ID, Name: "@0", Status: domain.ReductionStatusReady, CreatedAt: bootAlarmNow}
	group := domain.Group{ID: "g-ubuntu", Name: "ubuntu", StartIP: "192.168.10.40", ClientMax: 30, Netmask: "255.255.255.0",
		SystemImageID: img.ID, SystemConfigID: cfg.ID, SystemReductionID: red.ID}
	for _, err := range []error{
		st.Images().Create(ctx, img),
		st.Configs().Create(ctx, cfg),
		st.Reductions().Create(ctx, red),
		st.Groups().Create(ctx, group),
		st.Terminals().Create(ctx, terminal),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if failure.MAC != "" {
		if err := st.BootFailures().Upsert(ctx, failure); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func frontDesk() domain.Terminal {
	return domain.Terminal{ID: "terminal-0050562EF64E", Name: "前台01", MAC: "0050562EF64E", IP: "192.168.10.40", GroupID: "g-ubuntu", State: domain.TerminalStateOffline}
}

func TestComputedIdentityShortDisconnectSuggestsCaptureUntilWindowsBoots(t *testing.T) {
	ctx := context.Background()
	terminal := frontDesk()
	boot := bootAlarmNow.Add(-80 * time.Second)
	offline := bootAlarmNow.Add(-30 * time.Second)
	terminal.LastBootAt, terminal.OfflineAt = &boot, &offline
	terminal.LastBootProfileID, terminal.LastBootReductionID = "vmware-model", "ubuntu_0"
	terminal.HardwareProfileID = "vmware-model"
	st := seedBootAlarm(t, terminal, domain.BootFailure{})
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "vmware-model", Source: "computed", InstanceID: "PCI\\VEN_15AD&DEV_07B0\\005056FFFF2EF64E00", LayerReductionRef: "ubuntu_0"}); err != nil {
		t.Fatal(err)
	}
	conds := evalBootAlarms(t, st)
	if len(conds) != 1 || conds[0].Severity != "warn" || !strings.Contains(conds[0].Message, "重新取证") || !strings.Contains(conds[0].Message, "前台01") {
		t.Fatalf("short disconnect=%+v", conds)
	}
	identity, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	identity.Source = "captured"
	if err := st.TerminalIdentities().Update(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if conds := evalBootAlarms(t, st); len(conds) != 0 {
		t.Fatalf("captured identity must not suggest recapture: %+v", conds)
	}
	identity.Source = "computed"
	if err := st.TerminalIdentities().Update(ctx, identity); err != nil {
		t.Fatal(err)
	}
	late := boot.Add(61 * time.Second)
	terminal.OfflineAt = &late
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	if conds := evalBootAlarms(t, st); len(conds) != 0 {
		t.Fatalf("later disconnect must not suggest recapture: %+v", conds)
	}
	terminal.OfflineAt = &offline
	terminal.LastBootOKAt = &offline
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	if conds := evalBootAlarms(t, st); len(conds) != 0 {
		t.Fatalf("successful Windows startup must clear suggestion: %+v", conds)
	}
}

func evalBootAlarms(t *testing.T, st store.Store) []AlarmCondition {
	t.Helper()
	conds, err := BootFailureAlarmRule(st, func() time.Time { return bootAlarmNow }).Eval(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return conds
}

func TestBootModeMismatchAlarmSaysWhatToChange(t *testing.T) {
	st := seedBootAlarm(t, frontDesk(), domain.BootFailure{
		MAC: "0050562EF64E", Stage: domain.BootStageBootMode, Platform: "efi", ImageID: "ubuntu", At: bootAlarmNow.Add(-time.Minute),
	})
	conds := evalBootAlarms(t, st)
	if len(conds) != 1 {
		t.Fatalf("conds = %#v", conds)
	}
	c := conds[0]
	if c.Severity != "error" || c.Type != "开机失败" || c.Resource != "前台01" {
		t.Fatalf("cond = %#v", c)
	}
	for _, want := range []string{"前台01", "UEFI", "ubuntu", "把客户机固件改成 BIOS", "支持 UEFI 引导的镜像"} {
		if !strings.Contains(c.Message, want) {
			t.Fatalf("message %q lacks %q", c.Message, want)
		}
	}
}

func TestSanhookAndSanbootAlarmsPointAtDifferentThings(t *testing.T) {
	hook := evalBootAlarms(t, seedBootAlarm(t, frontDesk(), domain.BootFailure{
		MAC: "0050562EF64E", Stage: domain.BootStageSanhook, Code: "0x3f102003", Platform: "efi", ImageID: "ubuntu", At: bootAlarmNow.Add(-time.Minute),
	}))
	boot := evalBootAlarms(t, seedBootAlarm(t, frontDesk(), domain.BootFailure{
		MAC: "0050562EF64E", Stage: domain.BootStageSanboot, Code: "0x7f048283", Platform: "efi", ImageID: "ubuntu", At: bootAlarmNow.Add(-time.Minute),
	}))
	if len(hook) != 1 || len(boot) != 1 {
		t.Fatalf("hook=%#v boot=%#v", hook, boot)
	}
	if !strings.Contains(hook[0].Message, "连不上系统盘") || !strings.Contains(hook[0].Message, "0x3f102003") {
		t.Fatalf("sanhook message = %q", hook[0].Message)
	}
	if !strings.Contains(boot[0].Message, "没能从盘上引导") || !strings.Contains(boot[0].Message, "体检") {
		t.Fatalf("sanboot message = %q", boot[0].Message)
	}
}

func TestBootFailureAlarmClears(t *testing.T) {
	failedAt := bootAlarmNow.Add(-5 * time.Minute)
	failure := domain.BootFailure{MAC: "0050562EF64E", Stage: domain.BootStageSanboot, Platform: "efi", ImageID: "ubuntu", At: failedAt}

	booted := frontDesk()
	booted.State = domain.TerminalStateOnline
	since := failedAt.Add(time.Minute) // 失败后又开机，会话保持了 4 分钟
	booted.OnlineSince = &since
	booted.LastBootOKAt = &since // Explicit iPXE failures require a real Windows success report to recover.
	if conds := evalBootAlarms(t, seedBootAlarm(t, booted, failure)); len(conds) != 0 {
		t.Fatalf("stayed online after the failure, still alarmed: %#v", conds)
	}

	flicker := frontDesk()
	flicker.State = domain.TerminalStateOnline
	recent := bootAlarmNow.Add(-time.Minute) // 刚挂上盘，还说明不了什么
	flicker.OnlineSince = &recent
	if conds := evalBootAlarms(t, seedBootAlarm(t, flicker, failure)); len(conds) != 1 {
		t.Fatalf("a fresh session cleared the alarm: %#v", conds)
	}

	old := failure
	old.At = bootAlarmNow.Add(-25 * time.Hour)
	if conds := evalBootAlarms(t, seedBootAlarm(t, frontDesk(), old)); len(conds) != 0 {
		t.Fatalf("a day-old failure still alarmed: %#v", conds)
	}

	st := seedBootAlarm(t, frontDesk(), failure)
	if err := st.Terminals().Delete(context.Background(), "terminal-0050562EF64E"); err != nil {
		t.Fatal(err)
	}
	if conds := evalBootAlarms(t, st); len(conds) != 0 {
		t.Fatalf("a deleted terminal still alarmed: %#v", conds)
	}
}

// 成功回报留在目录中：关机和巡检重建不能让旧失败复活，后来的失败仍需告警。
func TestBootFailureAlarmUsesPersistedWindowsSuccess(t *testing.T) {
	ctx := context.Background()
	failedAt := bootAlarmNow.Add(-5 * time.Minute)
	terminal := frontDesk()
	successAt := failedAt.Add(time.Minute)
	terminal.LastBootOKAt = &successAt
	failure := domain.BootFailure{MAC: terminal.MAC, Stage: domain.BootStageWindows, ImageID: "ubuntu", At: failedAt}
	st := seedBootAlarm(t, terminal, failure)
	for round := 0; round < 2; round++ {
		if conds := evalBootAlarms(t, st); len(conds) != 0 {
			t.Fatalf("offline after Windows success, evaluation %d: %+v", round, conds)
		}
	}
	failure.At = successAt.Add(time.Minute)
	if err := st.BootFailures().Upsert(ctx, failure); err != nil {
		t.Fatal(err)
	}
	if conds := evalBootAlarms(t, st); len(conds) != 1 {
		t.Fatalf("new failure after old success must remain: %+v", conds)
	}
}

// Windows 没起来的告警：这台机器在当前点上开机失败时，直说去「机型」页做完整适配；
// 失败依据对不上当前机型（换过网卡）时给通用提示，也不挂操作。
func TestMissingReportAlarmDoesNotClaimWindowsFailureOrOfferAdaptation(t *testing.T) {
	ctx := context.Background()
	term := frontDesk()
	term.HardwareProfileID = "hwp-1"
	failure := domain.BootFailure{MAC: "0050562EF64E", Stage: domain.BootStageWindows, ImageID: "ubuntu", ReductionID: "ubuntu_0", ProfileID: "hwp-1", At: bootAlarmNow.Add(-time.Minute)}
	st := seedBootAlarm(t, term, failure)
	img, _ := st.Images().Get(ctx, "ubuntu")
	img.OSType = domain.OSTypeWindows
	if err := st.Images().Update(ctx, img); err != nil {
		t.Fatal(err)
	}
	if err := st.HardwareProfiles().Create(ctx, domain.HardwareProfile{ID: "hwp-1", Name: "VMware · 网卡 15AD:07B0", Fingerprint: "15ad:07b0/1024/efi", NICVendor: "15ad", NICDevice: "07b0", CreatedAt: bootAlarmNow, UpdatedAt: bootAlarmNow}); err != nil {
		t.Fatal(err)
	}
	conds := evalBootAlarms(t, st)
	// 先说要做什么，网卡用型号名，页面名、按钮名与界面一致；列表里被截断也能看到关键句。
	if len(conds) != 1 {
		t.Fatalf("conds = %#v", conds)
	}
	msg := conds[0].Message
	for _, want := range []string{"请先查看客户机 前台01", "未收到 Windows 启动成功回报", "结果待确认"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if conds[0].Severity != "warn" || conds[0].Type != "启动待确认" || strings.Contains(msg, "没有进入") || strings.Contains(msg, "换一种网卡") {
		t.Fatalf("inference presented as failure: %+v", conds[0])
	}
	svc := AlarmService{Store: st, Now: func() time.Time { return bootAlarmNow }, Rules: []AlarmRule{BootFailureAlarmRule(st, func() time.Time { return bootAlarmNow })}}
	svc.evaluate(ctx)
	for _, status := range []string{"active", "acknowledged", "recovered"} {
		alarms, err := svc.List(ctx, AlarmQuery{})
		if err != nil || len(alarms.Items) != 1 {
			t.Fatalf("alarms = %+v, %v", alarms, err)
		}
		a := alarms.Items[0]
		if status == "recovered" {
			if a.ModelAdapt != nil {
				t.Fatalf("recovered action = %+v", a.ModelAdapt)
			}
			break
		}
		if a.ModelAdapt != nil {
			t.Fatalf("unverified callback must not offer adaptation: %+v", a.ModelAdapt)
		}
		if status == "active" {
			if err := svc.Acknowledge(ctx, a.ID); err != nil {
				t.Fatal(err)
			}
		} else {
			a.Alarm.Status = "recovered"
			if err := st.Alarms().Update(ctx, a.Alarm); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 失败记在旧网卡的机型上：不是当前机型的问题。
	failure.ProfileID = "old-nic"
	if err := st.BootFailures().Upsert(ctx, failure); err != nil {
		t.Fatal(err)
	}
	svc.evaluate(ctx)
	alarms, err := svc.List(ctx, AlarmQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range alarms.Items {
		if a.Status != "recovered" && a.ModelAdapt != nil {
			t.Fatalf("a failure of another model still has action: %+v", a)
		}
	}
	conds = evalBootAlarms(t, st)
	if len(conds) != 1 || strings.Contains(conds[0].Message, "完整适配") || !strings.Contains(conds[0].Message, "未收到 Windows 启动成功回报") || !strings.Contains(conds[0].Message, "「机型」页") {
		t.Fatalf("conds = %#v", conds)
	}
}

func TestBootAlarmDoesNotOfferAdaptationWithoutWindowsModel(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		os          domain.OSType
		profile     bool
	}{
		{"iSCSI 失败", domain.BootStageSanhook, domain.OSTypeWindows, true},
		{"Linux", domain.BootStageWindows, domain.OSTypeLinux, true},
		{"机型已删除", domain.BootStageWindows, domain.OSTypeWindows, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			term := frontDesk()
			term.HardwareProfileID = "hwp-1"
			st := seedBootAlarm(t, term, domain.BootFailure{MAC: term.MAC, Stage: tc.stage, ImageID: "ubuntu", At: bootAlarmNow.Add(-time.Minute)})
			img, err := st.Images().Get(ctx, "ubuntu")
			if err != nil {
				t.Fatal(err)
			}
			img.OSType = tc.os
			if err := st.Images().Update(ctx, img); err != nil {
				t.Fatal(err)
			}
			if tc.profile {
				if err := st.HardwareProfiles().Create(ctx, domain.HardwareProfile{ID: "hwp-1", Fingerprint: "15ad:07b0/1024/efi", NICVendor: "15ad", NICDevice: "07b0"}); err != nil {
					t.Fatal(err)
				}
			}
			svc := AlarmService{Store: st, Now: func() time.Time { return bootAlarmNow }, Rules: []AlarmRule{BootFailureAlarmRule(st, func() time.Time { return bootAlarmNow })}}
			svc.evaluate(ctx)
			alarms, err := svc.List(ctx, AlarmQuery{})
			if err != nil || len(alarms.Items) != 1 {
				t.Fatalf("alarms=%+v err=%v", alarms, err)
			}
			if alarms.Items[0].ModelAdapt != nil {
				t.Fatalf("unexpected action: %+v", alarms.Items[0].ModelAdapt)
			}
		})
	}
}

func TestWindowsMissingReportRecoveryPersistsAfterNormalOffline(t *testing.T) {
	ctx := context.Background()
	t1 := frontDesk()
	failedAt, bootAt, since := bootAlarmNow.Add(-20*time.Minute), bootAlarmNow.Add(-5*time.Minute), bootAlarmNow.Add(-4*time.Minute)
	t1.LastBootAt, t1.OnlineSince, t1.State = &bootAt, &since, domain.TerminalStateOnline
	f := domain.BootFailure{MAC: t1.MAC, Stage: domain.BootStageWindows, ImageID: "old-deleted-image", At: failedAt}
	st := seedBootAlarm(t, t1, f)
	if conds := evalBootAlarms(t, st); len(conds) != 0 {
		t.Fatalf("old missing-report alarm not suppressed by later stable boot: %+v", conds)
	}
	failures, err := st.BootFailures().List(ctx)
	if err != nil || len(failures) != 0 {
		t.Fatalf("recovery not persisted, old failure can resurrect: %+v %v", failures, err)
	}
	fresh, _ := st.Terminals().Get(ctx, t1.ID)
	if fresh.LastBootOKAt != nil {
		t.Fatal("iSCSI stability must not fabricate Windows success")
	}
	if rows, _ := st.ModelAdaptations().List(ctx); len(rows) != 0 {
		t.Fatal("iSCSI stability must not create an observed adaptation")
	}
	fresh.State, fresh.OfflineAt = domain.TerminalStateOffline, &bootAlarmNow
	if err := st.Terminals().Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if conds := evalBootAlarms(t, st); len(conds) != 0 {
		t.Fatalf("normal shutdown resurrected a resolved old failure: %+v", conds)
	}
	newer := f
	newer.Stage = domain.BootStageSanhook
	newer.At = bootAlarmNow
	if err := st.BootFailures().Upsert(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if conds := evalBootAlarms(t, st); len(conds) != 1 {
		t.Fatalf("new explicit boot failure lost after earlier recovery: %+v", conds)
	}
}

func TestStableSessionRecoveryIsBoundToTheNewBootAndOnlyMissingReports(t *testing.T) {
	for _, kind := range []string{"prior-session", "same-boot", "explicit-ipxe"} {
		t.Run(kind, func(t *testing.T) {
			t1 := frontDesk()
			t1.State = domain.TerminalStateOnline
			boot, since, failed := bootAlarmNow.Add(-time.Minute), bootAlarmNow.Add(-time.Hour), bootAlarmNow.Add(-10*time.Minute)
			f := domain.BootFailure{MAC: t1.MAC, Stage: domain.BootStageWindows, At: failed}
			if kind != "prior-session" {
				boot = bootAlarmNow.Add(-5 * time.Minute)
			}
			if kind == "same-boot" {
				f.At = boot
			}
			if kind == "explicit-ipxe" {
				f.Stage = domain.BootStageSanboot
			}
			t1.LastBootAt, t1.OnlineSince = &boot, &since
			st := seedBootAlarm(t, t1, f)
			if conds := evalBootAlarms(t, st); len(conds) != 1 {
				t.Fatalf("insufficient/explicit evidence incorrectly erased: %+v", conds)
			}
		})
	}
}

type replacedFailureStore struct {
	store.Store
	replaced bool
}

func (s *replacedFailureStore) BootFailures() store.BootFailureRepo {
	return replacedFailureRepo{BootFailureRepo: s.Store.BootFailures(), owner: s}
}

type replacedFailureRepo struct {
	store.BootFailureRepo
	owner *replacedFailureStore
}

func (r replacedFailureRepo) DeleteIfUnchanged(ctx context.Context, expected domain.BootFailure) (bool, error) {
	if !r.owner.replaced {
		r.owner.replaced = true
		replacement := expected
		replacement.Stage = domain.BootStageSanhook
		replacement.Code = "0x5678"
		if err := r.BootFailureRepo.Upsert(ctx, replacement); err != nil {
			return false, err
		}
	}
	return r.BootFailureRepo.DeleteIfUnchanged(ctx, expected)
}
func TestAReplacedFailureCannotBeRecoveredByStaleAlarmEvaluation(t *testing.T) {
	term := frontDesk()
	term.State = domain.TerminalStateOnline
	boot, since := bootAlarmNow.Add(-5*time.Minute), bootAlarmNow.Add(-4*time.Minute)
	term.LastBootAt, term.OnlineSince = &boot, &since
	old := domain.BootFailure{MAC: term.MAC, Stage: domain.BootStageWindows, At: bootAlarmNow.Add(-10 * time.Minute)}
	st := &replacedFailureStore{Store: seedBootAlarm(t, term, old)}
	if conds := evalBootAlarms(t, st); len(conds) != 1 {
		t.Fatalf("conditional delete lost its match but evaluation recovered the alarm: %+v", conds)
	}
	conds := evalBootAlarms(t, st)
	if len(conds) != 1 || conds[0].Value != domain.BootStageSanhook {
		t.Fatalf("new explicit failure was hidden: %+v", conds)
	}
}

func TestOfflineStableNewBootPermanentlyRetiresOlderMissingReport(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end time.Duration
		wantClear  bool
	}{
		{"completed stable boot", -6 * time.Minute, -2 * time.Minute, true},
		{"short new boot", -3 * time.Minute, -2 * time.Minute, false},
		{"future disconnect", -6 * time.Minute, time.Minute, false},
		{"old disconnect", -2 * time.Minute, -6 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tm := frontDesk()
			since := bootAlarmNow.Add(-time.Hour)
			start, end := bootAlarmNow.Add(tc.start), bootAlarmNow.Add(tc.end)
			tm.State = domain.TerminalStateOffline
			tm.OnlineSince = &since
			tm.LastBootAt = &start
			tm.OfflineAt = &end
			st := seedBootAlarm(t, tm, domain.BootFailure{MAC: tm.MAC, Stage: domain.BootStageWindows, At: bootAlarmNow.Add(-2 * time.Hour)})
			conds := evalBootAlarms(t, st)
			remaining, _ := st.BootFailures().List(ctx)
			if (len(conds) == 0) != tc.wantClear || (len(remaining) == 0) != tc.wantClear {
				t.Fatalf("clear=%v conditions=%v failures=%v", tc.wantClear, conds, remaining)
			}
			got, _ := st.Terminals().Get(ctx, tm.ID)
			if got.LastBootOKAt != nil {
				t.Fatal("session proof fabricated Windows success")
			}
		})
	}
}
